package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"mcp-gateway/internal/models"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Phase 16.1 — dual-mode (aggregate + per-backend) server tests.
//
// Covers T16.1.2 (RebuildTools updates both registries) and T16.1.3
// (registerToolForBackend helper output) via four scenarios:
//
//	TestRebuildTools_DualMode              — aggregate + per-backend produced
//	TestPerBackendServer_ListChangedScoping — per-backend instances are
//	                                          independent, teardown on
//	                                          backend removal
//	TestPerBackendServer_ToolDescriptionNoBrackets — SDK round-trip asserts
//	                                                 no "[<backend>] " prefix
//	                                                 on per-backend tools
//	TestConcurrentRebuildAndBackendAdd      — race-safe under concurrent
//	                                          RebuildTools + ServerFor +
//	                                          UpdateConfig (mirrors
//	                                          TestConcurrentRebuildAndFilteredTools
//	                                          at gateway_test.go:312)

// TestRebuildTools_DualMode verifies a RebuildTools pass populates both
// surfaces: aggregate has namespaced tool names with "[backend] " description
// prefix, and each backend gets its own *mcp.Server populated with
// unnamespaced tool names.
func TestRebuildTools_DualMode(t *testing.T) {
	gw, _ := setupGatewayWithTools(t)
	gw.RebuildTools()

	// Aggregate surface: namespaced registered names.
	gw.toolsMu.Lock()
	_, hasAlphaRead := gw.registeredTools["alpha__read"]
	_, hasAlphaWrite := gw.registeredTools["alpha__write"]
	_, hasBetaSearch := gw.registeredTools["beta__search"]
	gw.toolsMu.Unlock()
	assert.True(t, hasAlphaRead, "aggregate missing alpha__read")
	assert.True(t, hasAlphaWrite, "aggregate missing alpha__write")
	assert.True(t, hasBetaSearch, "aggregate missing beta__search")

	// Per-backend surface: each backend has its own *mcp.Server.
	alphaSrv := gw.ServerFor("alpha")
	betaSrv := gw.ServerFor("beta")
	require.NotNil(t, alphaSrv, "ServerFor(alpha) must be non-nil after RebuildTools")
	require.NotNil(t, betaSrv, "ServerFor(beta) must be non-nil after RebuildTools")
	require.Nil(t, gw.ServerFor("nonexistent"), "ServerFor(nonexistent) must be nil")
	assert.NotSame(t, alphaSrv, betaSrv, "per-backend servers must be distinct instances")

	// Per-backend registered sets: unnamespaced names only.
	gw.toolsMu.Lock()
	alphaReg := gw.backendRegistered["alpha"]
	betaReg := gw.backendRegistered["beta"]
	gw.toolsMu.Unlock()
	assert.Contains(t, alphaReg, "read")
	assert.Contains(t, alphaReg, "write")
	assert.NotContains(t, alphaReg, "alpha__read", "per-backend name must not be namespaced")
	assert.Contains(t, betaReg, "search")
	assert.NotContains(t, betaReg, "read", "beta must not leak alpha's tools")
}

// TestPerBackendServer_ListChangedScoping verifies that (a) per-backend
// servers are independent *mcp.Server instances with independent tool
// registries, and (b) teardown happens when a backend stops producing tools.
// The SDK's list_changed notifications are scoped to the *mcp.Server that
// received AddTool / RemoveTools by construction (one subscriber channel per
// server instance), so asserting instance independence + registry
// independence captures the scoping contract end-to-end without needing a
// full SDK notification fixture.
func TestPerBackendServer_ListChangedScoping(t *testing.T) {
	gw, lm := setupGatewayWithTools(t)
	gw.RebuildTools()

	alphaSrv := gw.ServerFor("alpha")
	betaSrv := gw.ServerFor("beta")
	require.NotNil(t, alphaSrv)
	require.NotNil(t, betaSrv)
	require.NotSame(t, alphaSrv, betaSrv)

	// Mutate alpha only: add a new tool to alpha's backend.
	lm.SetTools("alpha", []models.ToolInfo{
		{Name: "read", Description: "Read a file"},
		{Name: "write", Description: "Write a file"},
		{Name: "exec", Description: "Execute"}, // new
	})
	gw.RebuildTools()

	// alpha gained "exec"; beta unchanged.
	gw.toolsMu.Lock()
	alphaReg := gw.backendRegistered["alpha"]
	betaReg := gw.backendRegistered["beta"]
	gw.toolsMu.Unlock()
	assert.Contains(t, alphaReg, "exec")
	assert.NotContains(t, betaReg, "exec", "beta registry must not receive alpha's new tool")

	// Pointer identity preserved on update (we mutate existing server).
	assert.Same(t, alphaSrv, gw.ServerFor("alpha"), "alpha per-backend server pointer must be stable across RebuildTools")
	assert.Same(t, betaSrv, gw.ServerFor("beta"), "beta per-backend server pointer must be stable across RebuildTools")

	// Tear down beta: stop its backend in lifecycle. RebuildTools should
	// drop beta's per-backend server entry.
	lm.SetStatus("beta", models.StatusStopped, "")
	gw.RebuildTools()
	assert.Nil(t, gw.ServerFor("beta"), "beta per-backend server must be torn down after its backend stopped")
	assert.NotNil(t, gw.ServerFor("alpha"), "alpha per-backend server must persist when beta is removed")
}

// TestPerBackendServer_ToolDescriptionNoBrackets wires the per-backend MCP
// server through a real streamable HTTP handler and queries tools/list via
// an SDK client. Asserts descriptions carry no "[<backend>] " prefix on the
// per-backend surface, and DO carry it on the aggregate surface.
func TestPerBackendServer_ToolDescriptionNoBrackets(t *testing.T) {
	gw, _ := setupGatewayWithTools(t)
	gw.RebuildTools()

	perBackendSrv := gw.ServerFor("alpha")
	require.NotNil(t, perBackendSrv)
	aggregateSrv := gw.Server()
	require.NotNil(t, aggregateSrv)

	perHandler := mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server { return perBackendSrv }, nil,
	)
	aggHandler := mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server { return aggregateSrv }, nil,
	)
	tsPer := httptest.NewServer(perHandler)
	defer tsPer.Close()
	tsAgg := httptest.NewServer(aggHandler)
	defer tsAgg.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	listOn := func(endpoint string) []*mcp.Tool {
		t.Helper()
		c := mcp.NewClient(&mcp.Implementation{Name: "phase16-test", Version: "1.0"}, nil)
		sess, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint}, nil)
		require.NoError(t, err)
		defer sess.Close()
		res, err := sess.ListTools(ctx, nil)
		require.NoError(t, err)
		return res.Tools
	}

	perTools := listOn(tsPer.URL)
	names := make(map[string]string, len(perTools))
	for _, tool := range perTools {
		names[tool.Name] = tool.Description
	}
	// Per-backend names must be unnamespaced.
	assert.Contains(t, names, "read", "per-backend alpha must expose unnamespaced 'read'")
	assert.Contains(t, names, "write", "per-backend alpha must expose unnamespaced 'write'")
	assert.NotContains(t, names, "alpha__read", "per-backend surface must NOT include namespaced names")
	// Descriptions must NOT start with "[alpha]".
	for name, desc := range names {
		assert.False(t, strings.HasPrefix(desc, "["),
			"per-backend tool %q description must not start with bracket prefix; got %q",
			name, desc)
	}

	aggTools := listOn(tsAgg.URL)
	aggDescByName := make(map[string]string, len(aggTools))
	for _, tool := range aggTools {
		aggDescByName[tool.Name] = tool.Description
	}
	// Aggregate names must be namespaced.
	assert.Contains(t, aggDescByName, "alpha__read")
	assert.Contains(t, aggDescByName, "beta__search")
	// Aggregate descriptions MUST start with the "[backend] " prefix.
	assert.True(t, strings.HasPrefix(aggDescByName["alpha__read"], "[alpha] "),
		"aggregate tool description must carry [backend] prefix; got %q", aggDescByName["alpha__read"])
	assert.True(t, strings.HasPrefix(aggDescByName["beta__search"], "[beta] "),
		"aggregate tool description must carry [backend] prefix; got %q", aggDescByName["beta__search"])
}

// TestConcurrentRebuildAndBackendAdd mirrors TestConcurrentRebuildAndFilteredTools
// (gateway_test.go:312) but also exercises ServerFor() reads and dual-surface
// RebuildTools. Run under -race to validate serverMu guards perBackendServer
// mutations without data races.
func TestConcurrentRebuildAndBackendAdd(t *testing.T) {
	gw, _ := setupGatewayWithTools(t)

	const goroutines = 10
	const iterations = 100

	var wg sync.WaitGroup

	// Concurrent RebuildTools (writes both aggregate + per-backend registries).
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				gw.RebuildTools()
			}
		}()
	}

	// Concurrent ServerFor reads (acquires serverMu.RLock).
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = gw.ServerFor("alpha")
				_ = gw.ServerFor("beta")
				_ = gw.ServerFor("nonexistent")
			}
		}()
	}

	// Concurrent UpdateConfig (changes ToolFilter → changes tool membership).
	for i := 0; i < goroutines/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				cfg := &models.Config{
					Servers: map[string]*models.ServerConfig{
						"alpha": {Command: "echo"},
						"beta":  {Command: "echo"},
					},
					Gateway: models.GatewaySettings{
						ToolFilter: &models.ToolFilter{
							PerServerBudget: 1,
						},
					},
				}
				cfg.ApplyDefaults()
				gw.UpdateConfig(cfg)
			}
		}()
	}

	wg.Wait()
}

