package proxy

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"mcp-gateway/internal/lifecycle"
	"mcp-gateway/internal/models"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Phase 16.6 — gateway.invoke universal fallback tool + gateway.list_servers /
// gateway.list_tools meta-tools + cache-busting serverInfo.version.
//
// Test coverage:
//   TestGatewayInvoke_HappyPath             — invoke a backend tool via
//                                             gateway.invoke; result matches
//                                             a direct namespaced call.
//   TestGatewayInvoke_UnknownBackend        — clear error on missing backend.
//   TestGatewayInvoke_StaleToolsCache_Fallback
//                                           — CR-16.6-01 regression: a live
//                                             backend whose Tools list has
//                                             NOT yet synchronized must still
//                                             be reachable via gateway.invoke
//                                             (the whole point of the tool).
//   TestGatewayInvoke_MissingRequiredArgs   — validation of required args.
//   TestListServers_IncludesStatus          — summary shape: running /
//                                             degraded / stopped reported.
//   TestListTools_FiltersByServer           — optional server filter.
//   TestServerInfoVersionChangesOnTopology  — adding/removing a backend
//                                             changes the cache-bust hash.
//   TestGatewayBuiltins_NotOnPerBackend     — built-ins do NOT leak into
//                                             per-backend surfaces.
//   TestGatewayInstructions_Surfaced        — initialize response carries
//                                             the gateway instructions.

// startInProcessBackend spins up an in-process mcp.Server with a single
// `echo` tool and returns a *ClientSession connected to it via InMemoryTransports.
// The returned cleanup shuts both sides down. This lets happy-path tests
// exercise the router.Call path without spawning real child processes.
func startInProcessBackend(t *testing.T) (*mcp.ClientSession, func()) {
	t.Helper()

	srv := mcp.NewServer(&mcp.Implementation{Name: "phase16-test-backend", Version: "1.0"}, nil)
	type echoArgs struct {
		Message string `json:"message"`
	}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "echo",
		Description: "Echo a message back.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, args echoArgs) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + args.Message}},
		}, nil, nil
	})

	serverSide, clientSide := mcp.NewInMemoryTransports()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Run the server side on the in-memory transport.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Run(context.Background(), serverSide)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "phase16-test-client", Version: "1.0"}, nil)
	sess, err := client.Connect(ctx, clientSide, nil)
	require.NoError(t, err, "in-process client must connect to backend via InMemoryTransport")

	cleanup := func() {
		_ = sess.Close()
		<-done
	}
	return sess, cleanup
}

// setupGatewayWithLiveBackend builds a gateway whose "alpha" backend has a
// real *mcp.ClientSession attached via SetSession, so router.Call succeeds
// end-to-end. The tool set on alpha is a single `echo` tool that matches the
// in-process backend from startInProcessBackend.
func setupGatewayWithLiveBackend(t *testing.T) (*Gateway, *lifecycle.Manager, func()) {
	t.Helper()

	sess, cleanup := startInProcessBackend(t)

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"alpha": {Command: "in-process"}, // stdio shape; command is never invoked
		},
	}
	cfg.ApplyDefaults()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	lm := lifecycle.NewManager(cfg, "test", logger)
	gw := New(cfg, lm, "test", logger)

	lm.SetStatus("alpha", models.StatusRunning, "")
	lm.SetTools("alpha", []models.ToolInfo{
		{Name: "echo", Description: "Echo a message back."},
	})
	lm.SetSession("alpha", sess)

	gw.RebuildTools()
	return gw, lm, cleanup
}

// extractText pulls the first TextContent from a CallToolResult, failing the
// test if the shape is not what the gateway built-ins are contracted to
// return. Keeps every assertion site compact.
func extractText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	require.NotNil(t, res, "result must be non-nil")
	require.Len(t, res.Content, 1, "built-in tools always return a single content element")
	tc, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok, "expected *mcp.TextContent, got %T", res.Content[0])
	return tc.Text
}

// callToolDirect calls a registered tool on the aggregate server directly by
// routing a JSON-RPC-style CallToolRequest. Used by the built-in tests to
// avoid needing a full SDK round-trip (they exercise the handler closures,
// which is where all the new logic lives).
func callToolDirect(t *testing.T, gw *Gateway, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	handler := lookupHandler(t, gw, name)
	body, err := json.Marshal(args)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := handler(ctx, &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: name, Arguments: body},
	})
	require.NoError(t, err, "handler must not return transport errors (domain errors go in IsError)")
	return res
}

// lookupHandler resolves a built-in tool name to the matching Gateway method
// value. We take the handler-dispatch shortcut (vs. a live SDK client) so the
// tests stay package-local and do not pay the HTTP setup cost.
func lookupHandler(t *testing.T, gw *Gateway, name string) mcp.ToolHandler {
	t.Helper()
	switch name {
	case "gateway.invoke":
		return gw.handleGatewayInvoke
	case "gateway.list_servers":
		return gw.handleGatewayListServers
	case "gateway.list_tools":
		return gw.handleGatewayListTools
	default:
		t.Fatalf("unknown built-in tool %q", name)
		return nil
	}
}

// --- gateway.invoke -------------------------------------------------------

func TestGatewayInvoke_HappyPath(t *testing.T) {
	gw, _, cleanup := setupGatewayWithLiveBackend(t)
	defer cleanup()

	// Direct namespaced call — the baseline.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	direct, err := gw.router.Call(ctx, "alpha__echo", map[string]any{"message": "hi"})
	require.NoError(t, err)
	require.NotNil(t, direct)
	directText := extractText(t, direct)

	// Same call via gateway.invoke — the fallback.
	res := callToolDirect(t, gw, "gateway.invoke", map[string]any{
		"backend": "alpha",
		"tool":    "echo",
		"args":    map[string]any{"message": "hi"},
	})
	require.False(t, res.IsError, "happy path must not set IsError; got text=%q", extractText(t, res))
	assert.Equal(t, directText, extractText(t, res),
		"gateway.invoke result must match direct namespaced call result")
}

func TestGatewayInvoke_UnknownBackend(t *testing.T) {
	gw, _ := setupGatewayWithTools(t)
	gw.RebuildTools()

	res := callToolDirect(t, gw, "gateway.invoke", map[string]any{
		"backend": "no-such-backend",
		"tool":    "echo",
	})
	require.True(t, res.IsError, "unknown backend must set IsError")
	text := extractText(t, res)
	assert.Contains(t, text, "unknown backend")
	assert.Contains(t, text, "no-such-backend")
}

// TestGatewayInvoke_StaleToolsCache_Fallback is the CR-16.6-01 regression:
// gateway.invoke is the fallback when the aggregate tools/list is stale, so
// it MUST NOT pre-reject calls based on entry.Tools. The happy-path test
// already covers the cached case; this test clears the Tools cache after
// the backend is live to force the stale-cache path, and asserts the call
// still reaches the backend.
func TestGatewayInvoke_StaleToolsCache_Fallback(t *testing.T) {
	gw, lm, cleanup := setupGatewayWithLiveBackend(t)
	defer cleanup()

	// Simulate "backend live, tools list not yet resynchronized" — the exact
	// window gateway.invoke is designed for.
	lm.SetTools("alpha", nil)
	gw.RebuildTools()

	res := callToolDirect(t, gw, "gateway.invoke", map[string]any{
		"backend": "alpha",
		"tool":    "echo",
		"args":    map[string]any{"message": "stale-cache-path"},
	})
	require.False(t, res.IsError,
		"gateway.invoke MUST route even when lifecycle Tools cache is empty; got error: %q",
		extractText(t, res))
	assert.Contains(t, extractText(t, res), "echo: stale-cache-path")
}

func TestGatewayInvoke_MissingRequiredArgs(t *testing.T) {
	gw, _ := setupGatewayWithTools(t)
	gw.RebuildTools()

	t.Run("no backend", func(t *testing.T) {
		res := callToolDirect(t, gw, "gateway.invoke", map[string]any{"tool": "echo"})
		require.True(t, res.IsError)
		assert.Contains(t, extractText(t, res), "backend argument is required")
	})
	t.Run("no tool", func(t *testing.T) {
		res := callToolDirect(t, gw, "gateway.invoke", map[string]any{"backend": "alpha"})
		require.True(t, res.IsError)
		assert.Contains(t, extractText(t, res), "tool argument is required")
	})
}

// --- gateway.list_servers -------------------------------------------------

func TestListServers_IncludesStatus(t *testing.T) {
	gw, lm := setupGatewayWithTools(t)
	// Beta is stopped; alpha stays running; add a degraded gamma for shape
	// coverage.
	lm.SetStatus("beta", models.StatusStopped, "")
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"alpha": {Command: "echo"},
			"beta":  {Command: "echo"},
			"gamma": {URL: "http://example.invalid/mcp"},
		},
	}
	cfg.ApplyDefaults()
	gw.UpdateConfig(cfg)
	// Re-register gamma so Entries() reflects three backends.
	require.NoError(t, lm.AddServer("gamma", cfg.Servers["gamma"]))
	lm.SetStatus("gamma", models.StatusDegraded, "probe failed")
	gw.RebuildTools()

	res := callToolDirect(t, gw, "gateway.list_servers", nil)
	require.False(t, res.IsError, "list_servers must not be an error result")

	var summary []serverSummary
	require.NoError(t, json.Unmarshal([]byte(extractText(t, res)), &summary))
	byName := make(map[string]serverSummary, len(summary))
	for _, s := range summary {
		byName[s.Name] = s
	}

	require.Contains(t, byName, "alpha")
	require.Contains(t, byName, "beta")
	require.Contains(t, byName, "gamma")

	assert.Equal(t, string(models.StatusRunning), byName["alpha"].Status)
	assert.Equal(t, string(models.StatusStopped), byName["beta"].Status)
	assert.Equal(t, string(models.StatusDegraded), byName["gamma"].Status)

	assert.Equal(t, "stdio", byName["alpha"].Transport)
	assert.Equal(t, "http", byName["gamma"].Transport)

	// alpha reports two tools (read, write); beta had one (search) before stop.
	assert.Equal(t, 2, byName["alpha"].ToolCount)

	// Output must be sorted for stable consumption.
	for i := 1; i < len(summary); i++ {
		assert.LessOrEqual(t, summary[i-1].Name, summary[i].Name,
			"list_servers output must be sorted by name")
	}
}

// --- gateway.list_tools ---------------------------------------------------

func TestListTools_FiltersByServer(t *testing.T) {
	gw, _ := setupGatewayWithTools(t)
	gw.RebuildTools()

	// No filter: both backends present.
	all := callToolDirect(t, gw, "gateway.list_tools", nil)
	require.False(t, all.IsError)
	var allMap map[string][]toolSummary
	require.NoError(t, json.Unmarshal([]byte(extractText(t, all)), &allMap))
	assert.Contains(t, allMap, "alpha")
	assert.Contains(t, allMap, "beta")
	assert.Len(t, allMap["alpha"], 2)
	assert.Len(t, allMap["beta"], 1)
	// Each tool entry carries name + namespaced form + description.
	for _, tool := range allMap["alpha"] {
		assert.Equal(t, "alpha__"+tool.Name, tool.Namespaced,
			"namespaced form must match NamespacedTool(server, name)")
		assert.NotEmpty(t, tool.Description)
	}

	// Filter by alpha: beta must be absent.
	filtered := callToolDirect(t, gw, "gateway.list_tools", map[string]any{"server": "alpha"})
	require.False(t, filtered.IsError)
	var filteredMap map[string][]toolSummary
	require.NoError(t, json.Unmarshal([]byte(extractText(t, filtered)), &filteredMap))
	assert.Contains(t, filteredMap, "alpha")
	assert.NotContains(t, filteredMap, "beta", "server filter must exclude non-matching backends")
}

// --- serverInfo.version cache-busting -------------------------------------

func TestServerInfoVersionChangesOnTopology(t *testing.T) {
	gw, lm := setupGatewayWithTools(t)
	gw.RebuildTools()
	v1 := gw.ServerInfoVersion()
	require.True(t, strings.HasPrefix(v1, "test+"),
		"version must be baseVersion + \"+\" + shortHash; got %q", v1)

	// Same topology → identical version.
	gw.RebuildTools()
	assert.Equal(t, v1, gw.ServerInfoVersion(),
		"stable topology must yield a stable version hash")

	// Add a new backend → version must change.
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"alpha":   {Command: "echo"},
			"beta":    {Command: "echo"},
			"charlie": {Command: "echo"},
		},
	}
	cfg.ApplyDefaults()
	gw.UpdateConfig(cfg)
	require.NoError(t, lm.AddServer("charlie", cfg.Servers["charlie"]))
	lm.SetStatus("charlie", models.StatusRunning, "")
	lm.SetTools("charlie", []models.ToolInfo{{Name: "ping", Description: "ping"}})
	gw.RebuildTools()

	v2 := gw.ServerInfoVersion()
	assert.NotEqual(t, v1, v2, "new backend must invalidate the version hash")

	// Remove a backend → version must change again.
	lm.SetStatus("charlie", models.StatusStopped, "")
	gw.RebuildTools()
	v3 := gw.ServerInfoVersion()
	assert.NotEqual(t, v2, v3, "removing a backend must invalidate the version hash")

	// Tool count change on an existing backend → version must change.
	lm.SetTools("alpha", []models.ToolInfo{
		{Name: "read", Description: "Read a file"},
		{Name: "write", Description: "Write a file"},
		{Name: "exec", Description: "Execute"},
	})
	gw.RebuildTools()
	v4 := gw.ServerInfoVersion()
	assert.NotEqual(t, v3, v4, "tool count change must invalidate the version hash")
}

func TestComputeTopologyVersion_Invariants(t *testing.T) {
	// Deterministic for identical inputs.
	a := computeTopologyVersion("1.0", []string{"alpha", "beta"}, 5)
	b := computeTopologyVersion("1.0", []string{"alpha", "beta"}, 5)
	assert.Equal(t, a, b)

	// Order sensitivity: callers MUST pre-sort (RebuildTools does). A
	// different order yields a different hash, which documents the
	// callers' contract.
	c := computeTopologyVersion("1.0", []string{"beta", "alpha"}, 5)
	assert.NotEqual(t, a, c,
		"computeTopologyVersion treats input order as significant; "+
			"callers must pre-sort backend names")

	// Base version carries through.
	d := computeTopologyVersion("2.0", []string{"alpha", "beta"}, 5)
	assert.True(t, strings.HasPrefix(d, "2.0+"))

	// Tool count change invalidates.
	e := computeTopologyVersion("1.0", []string{"alpha", "beta"}, 6)
	assert.NotEqual(t, a, e)

	// Short hash is exactly 8 hex chars after the '+'.
	parts := strings.SplitN(a, "+", 2)
	require.Len(t, parts, 2)
	assert.Len(t, parts[1], 8, "short hash must be 8 hex characters")
}

// --- scope invariants -----------------------------------------------------

// TestGatewayBuiltins_NotOnPerBackend verifies that the gateway.* built-ins
// are registered on the aggregate server ONLY and never appear on any
// per-backend surface. Regressions here would surface as confusing "this
// tool is also available here" duplication in a Claude Code `/mcp` panel.
func TestGatewayBuiltins_NotOnPerBackend(t *testing.T) {
	gw, _ := setupGatewayWithTools(t)
	gw.RebuildTools()

	// Aggregate has all three built-ins.
	gw.toolsMu.Lock()
	_, hasInvoke := gw.registeredTools["gateway.invoke"]
	_, hasListServers := gw.registeredTools["gateway.list_servers"]
	_, hasListTools := gw.registeredTools["gateway.list_tools"]
	gw.toolsMu.Unlock()
	// NOTE: registeredTools tracks only tools wired via RebuildTools (backend
	// tools). Built-ins are registered directly in New() and are expected to
	// be NOT present in that map — they are reachable only via the aggregate
	// server's own tool registry. So the assertion below reverses intent:
	// built-ins must NOT leak into the backend-tools registry.
	assert.False(t, hasInvoke, "built-ins must not be tracked in backend registeredTools")
	assert.False(t, hasListServers, "built-ins must not be tracked in backend registeredTools")
	assert.False(t, hasListTools, "built-ins must not be tracked in backend registeredTools")

	// Per-backend registries must not carry the built-in names either.
	gw.toolsMu.Lock()
	alphaReg := gw.backendRegistered["alpha"]
	betaReg := gw.backendRegistered["beta"]
	gw.toolsMu.Unlock()
	for _, name := range []string{"gateway.invoke", "gateway.list_servers", "gateway.list_tools"} {
		assert.NotContains(t, alphaReg, name, "%s must not appear on per-backend alpha", name)
		assert.NotContains(t, betaReg, name, "%s must not appear on per-backend beta", name)
	}
}

// TestGatewayInstructions_Surfaced rebuilds against a fresh gateway and
// checks the aggregate Implementation carries the expected shape so the
// initialize response wired by the SDK (server.go:1463) returns the
// gatewayInstructions text.
func TestGatewayInstructions_Surfaced(t *testing.T) {
	gw, _ := setupGatewayWithTools(t)
	require.NotNil(t, gw.aggregateImpl, "buildMCPServer must retain the Implementation pointer")
	assert.Equal(t, "mcp-gateway", gw.aggregateImpl.Name)

	// The instructions constant is a 1–2 sentence block referencing the
	// namespace scheme + built-in fallbacks. Validate shape so a future
	// edit cannot silently drop either hint.
	assert.Contains(t, gatewayInstructions, "<backend>__<tool>")
	assert.Contains(t, gatewayInstructions, "gateway.list_servers")
	assert.Contains(t, gatewayInstructions, "gateway.invoke")
}
