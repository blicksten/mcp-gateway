// Package proxy implements the MCP Gateway — the core engine that aggregates
// backend MCP servers and exposes them through a unified MCP interface.
package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"mcp-gateway/internal/lifecycle"
	"mcp-gateway/internal/models"
	"mcp-gateway/internal/router"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Gateway is the core MCP proxy engine. It aggregates tools from all
// running backends and exposes them through two MCP server surfaces:
//
//   - aggregateServer: existing behavior — all backend tools flattened into
//     one MCP server with namespaced names (<backend>__<tool>) and
//     descriptions prefixed by "[<backend>] ". Mounted at /mcp.
//   - perBackendServer[name]: one MCP server per running backend, exposing
//     ONLY that backend's tools, unnamespaced, descriptions without prefix.
//     Mounted at /mcp/<backend>. Lazy-created on first tool registration.
//
// Phase 16.1 introduces the per-backend surface for Claude Code plugin
// integration; the aggregate surface is unchanged for backward compatibility.
type Gateway struct {
	lm      *lifecycle.Manager
	router  *router.Router
	cfg     *models.Config
	version string
	logger  *slog.Logger

	cfgMu   sync.RWMutex // protects g.cfg reads/writes (separate from toolsMu)
	toolsMu sync.Mutex   // protects tool registration on MCP servers (aggregate + per-backend)

	// Phase 16.1 dual-mode: aggregateServer is the legacy single-server
	// surface; perBackendServer is the new per-backend surface map.
	aggregateServer  *mcp.Server
	perBackendServer map[string]*mcp.Server
	serverMu         sync.RWMutex // guards perBackendServer map mutations

	// Phase 16.6 cache-busting: aggregateImpl is the Implementation we handed
	// to mcp.NewServer; the SDK stores the pointer (server.go:1463 reads
	// s.impl on initialize). Mutating aggregateImpl.Version is observable by
	// fresh client sessions, allowing topology-driven refetch. versionMu
	// guards our own writes; see RebuildTools for the rationale on why SDK
	// read-side locking is not attempted.
	aggregateImpl *mcp.Implementation
	versionMu     sync.RWMutex

	// registeredTools tracks currently-registered tool names on aggregateServer
	// (keyed by namespaced name). backendRegistered tracks per-backend
	// unnamespaced tool names, outer map keyed by backend name.
	registeredTools   map[string]struct{}
	backendRegistered map[string]map[string]struct{}
}

// New creates a new Gateway with the given config and lifecycle manager.
// The version parameter is injected via ldflags at build time ("dev" for local builds).
func New(cfg *models.Config, lm *lifecycle.Manager, version string, logger *slog.Logger) *Gateway {
	if logger == nil {
		logger = slog.Default()
	}
	r := router.New(lm)
	g := &Gateway{
		lm:                lm,
		router:            r,
		cfg:               cfg,
		version:           version,
		logger:            logger,
		registeredTools:   make(map[string]struct{}),
		perBackendServer:  make(map[string]*mcp.Server),
		backendRegistered: make(map[string]map[string]struct{}),
	}
	g.aggregateServer = g.buildMCPServer()
	g.registerGatewayBuiltins()
	return g
}

// gatewayInstructions is surfaced to MCP clients via the initialize response
// (Phase 16.6 T16.6.3). It tells the client how tool names are namespaced and
// which built-in fallback tools are available when a backend's concrete tools
// are not yet visible (e.g. after a just-added backend whose tools list is
// still being fetched). Kept terse because MCP clients feed this directly to
// an LLM — the 1–2 sentence shape matches the rest of the tool descriptions.
const gatewayInstructions = "This gateway aggregates multiple MCP backends. " +
	"Tool names are namespaced as <backend>__<tool>. " +
	"Call `gateway.list_servers` to see backend topology. " +
	"Use `gateway.invoke` to call any backend tool when the list is stale."

// Server returns the aggregate MCP server (unchanged API for HTTP/SSE mounts
// at /mcp and /sse). This is the flattened view with namespaced tool names.
func (g *Gateway) Server() *mcp.Server {
	return g.aggregateServer
}

// ServerFor returns the per-backend MCP server for the given backend name,
// or nil if no such backend is currently registered. Intended for the
// Phase 16.1 per-backend HTTP route at /mcp/{backend}. The returned server
// exposes the backend's tools unnamespaced (no "<backend>__" prefix) and
// without the "[<backend>] " description prefix.
func (g *Gateway) ServerFor(backend string) *mcp.Server {
	g.serverMu.RLock()
	defer g.serverMu.RUnlock()
	return g.perBackendServer[backend]
}

// Router returns the tool router.
func (g *Gateway) Router() *router.Router {
	return g.router
}

// UpdateConfig replaces the gateway's config pointer (CR-4/AR-2 fix).
// Called after config reload to keep tool filtering in sync.
func (g *Gateway) UpdateConfig(cfg *models.Config) {
	g.cfgMu.Lock()
	g.cfg = cfg
	g.cfgMu.Unlock()
}

// buildMCPServer creates the MCP server with tool aggregation. The
// Implementation pointer is retained on Gateway so RebuildTools can mutate
// Version for cache-busting on topology change (Phase 16.6 T16.6.4).
// Instructions is wired via ServerOptions so the aggregate initialize
// response carries guidance on the namespacing scheme and built-in fallbacks
// (Phase 16.6 T16.6.3).
func (g *Gateway) buildMCPServer() *mcp.Server {
	impl := &mcp.Implementation{
		Name:    "mcp-gateway",
		Version: g.version,
	}
	g.aggregateImpl = impl
	server := mcp.NewServer(impl, &mcp.ServerOptions{
		Instructions: gatewayInstructions,
	})
	return server
}

// registerGatewayBuiltins wires the Phase 16.6 built-in tools onto the
// aggregate server only. These tools are NOT registered on per-backend
// servers because per-backend endpoints (mounted at /mcp/{backend}) already
// expose exactly that backend's tools unnamespaced — aggregate-wide fallbacks
// have no meaning there.
//
//   - gateway.invoke        — universal fallback invoker for any backend tool
//   - gateway.list_servers  — runtime topology snapshot
//   - gateway.list_tools    — tools grouped by backend (optional server filter)
//
// Called once from New(). RebuildTools does NOT re-register these (they are
// stable across topology changes); only backend-supplied tools are rebuilt.
func (g *Gateway) registerGatewayBuiltins() {
	g.aggregateServer.AddTool(&mcp.Tool{
		Name:        "gateway.invoke",
		Description: "[gateway] Universal fallback invoker. Call any backend tool by name. Use when specific tools aren't yet visible (e.g. recently added).",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"backend", "tool"},
			"properties": map[string]any{
				"backend": map[string]any{"type": "string", "description": "Backend server name"},
				"tool":    map[string]any{"type": "string", "description": "Tool name on that backend"},
				"args":    map[string]any{"type": "object", "description": "Arguments for the tool"},
			},
		},
	}, g.handleGatewayInvoke)

	g.aggregateServer.AddTool(&mcp.Tool{
		Name:        "gateway.list_servers",
		Description: "[gateway] List all configured backend servers with runtime status, transport, tool count, and uptime.",
		InputSchema: map[string]any{"type": "object"},
	}, g.handleGatewayListServers)

	g.aggregateServer.AddTool(&mcp.Tool{
		Name:        "gateway.list_tools",
		Description: "[gateway] List tools grouped by backend. Optionally filter by server name.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"server": map[string]any{"type": "string", "description": "Optional backend name filter"},
			},
		},
	}, g.handleGatewayListTools)
}

// errToolResult returns an MCP tool result carrying the given error message,
// with IsError set. The MCP contract lets a tool report a domain-level failure
// without tripping a transport-level error.
func errToolResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
		IsError: true,
	}
}

// textToolResult wraps a JSON-serializable payload as a text content result.
func textToolResult(v any) (*mcp.CallToolResult, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return errToolResult("marshal: " + err.Error()), nil
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
	}, nil
}

// gatewayInvokeArgs mirrors the gateway.invoke inputSchema.
type gatewayInvokeArgs struct {
	Backend string         `json:"backend"`
	Tool    string         `json:"tool"`
	Args    map[string]any `json:"args"`
}

// handleGatewayInvoke implements gateway.invoke: validates that the backend
// exists, then routes through the shared router.Call so the same namespace
// splitting + session resolution path is used as for namespaced calls.
//
// Deliberately does NOT validate `tool` against entry.Tools: the whole point
// of gateway.invoke is a fallback when the aggregate tools/list is stale
// (backend just added, refresh not yet propagated to Claude Code). Rejecting
// the call in that window would defeat the fallback. The backend itself
// returns a clear error if the tool doesn't exist (method-not-found), which
// router.Call surfaces as an IsError result. Backend existence is still
// checked here so the caller gets a gateway-level "unknown backend" message
// rather than an opaque "no active session" from the router.
func (g *Gateway) handleGatewayInvoke(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args gatewayInvokeArgs
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return errToolResult("invalid arguments: " + err.Error()), nil
		}
	}
	if args.Backend == "" {
		return errToolResult("backend argument is required"), nil
	}
	if args.Tool == "" {
		return errToolResult("tool argument is required"), nil
	}
	if _, ok := g.lm.Entry(args.Backend); !ok {
		return errToolResult(fmt.Sprintf("unknown backend %q", args.Backend)), nil
	}

	namespaced := g.router.NamespacedTool(args.Backend, args.Tool)
	result, err := g.router.Call(ctx, namespaced, args.Args)
	if err != nil {
		return errToolResult(err.Error()), nil
	}
	return result, nil
}

// serverSummary mirrors the gateway.list_servers output shape.
//
// SCHEMA-FREEZE v1.6.0 — the JSON field names below are part of the Phase 16
// wire contract consumed by LLM clients (via the built-in gateway.list_servers
// tool) and by the dashboard. Renames/removals are breaking changes and
// require a coordinated matrix update. New OPTIONAL fields may be added; see
// the LLM-client + dashboard rollout path before doing so.
//
// The Health field currently reflects Status for parity with the dashboard's
// rendering; a richer health signal (from health.Monitor) can replace it
// without breaking the schema.
//
// UptimeSeconds is 0 for any status other than "running" (stopped/degraded/
// starting report 0). Consumers that want historical uptime should derive it
// from restart_count + started_at in /api/v1/metrics.
type serverSummary struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	Transport     string `json:"transport"`
	ToolCount     int    `json:"tool_count"`
	Health        string `json:"health"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}

// handleGatewayListServers implements gateway.list_servers: returns the full
// runtime topology in a single call. Sorted by name for stable output.
func (g *Gateway) handleGatewayListServers(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	entries := g.lm.Entries()
	summary := make([]serverSummary, 0, len(entries))
	now := time.Now()
	for _, e := range entries {
		uptime := int64(0)
		if !e.StartedAt.IsZero() && e.Status == models.StatusRunning {
			uptime = int64(now.Sub(e.StartedAt).Seconds())
		}
		summary = append(summary, serverSummary{
			Name:          e.Name,
			Status:        string(e.Status),
			Transport:     e.Config.TransportType(),
			ToolCount:     len(e.Tools),
			Health:        string(e.Status),
			UptimeSeconds: uptime,
		})
	}
	sort.Slice(summary, func(i, j int) bool { return summary[i].Name < summary[j].Name })
	return textToolResult(summary)
}

// toolSummary mirrors the gateway.list_tools per-tool output shape.
//
// SCHEMA-FREEZE v1.6.0 — the JSON field names below are part of the Phase 16
// wire contract. Renames/removals are breaking changes; new OPTIONAL fields
// may be added with coordinated client rollout.
type toolSummary struct {
	Name        string `json:"name"`
	Namespaced  string `json:"namespaced"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema,omitempty"`
}

// gatewayListToolsArgs mirrors the gateway.list_tools inputSchema.
type gatewayListToolsArgs struct {
	Server string `json:"server"`
}

// handleGatewayListTools implements gateway.list_tools: returns a map keyed
// by backend name with each backend's tools. Backend names that match the
// optional server filter are included; missing filter means all backends.
// Tool order within a backend is the order reported by the backend.
func (g *Gateway) handleGatewayListTools(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args gatewayListToolsArgs
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return errToolResult("invalid arguments: " + err.Error()), nil
		}
	}
	entries := g.lm.Entries()
	result := make(map[string][]toolSummary)
	for _, e := range entries {
		if args.Server != "" && e.Name != args.Server {
			continue
		}
		tools := make([]toolSummary, 0, len(e.Tools))
		for _, t := range e.Tools {
			tools = append(tools, toolSummary{
				Name:        t.Name,
				Namespaced:  g.router.NamespacedTool(e.Name, t.Name),
				Description: t.Description,
				InputSchema: t.InputSchema,
			})
		}
		result[e.Name] = tools
	}
	return textToolResult(result)
}

// computeTopologyVersion returns the serverInfo.version string for the given
// sorted backend names + total tool count. The short hash is SHA-256 over
// "name1,name2,...|<tool_count>"; 8 hex chars is enough to invalidate client
// caches on topology change without bloating serverInfo payloads.
func computeTopologyVersion(baseVersion string, sortedBackends []string, toolCount int) string {
	h := sha256.New()
	_, _ = h.Write([]byte(strings.Join(sortedBackends, ",")))
	_, _ = h.Write([]byte{'|'})
	_, _ = fmt.Fprintf(h, "%d", toolCount)
	digest := h.Sum(nil)
	return baseVersion + "+" + hex.EncodeToString(digest[:4])
}

// ServerInfoVersion returns the current cache-busting version string reported
// to clients via the aggregate MCP server's initialize response. Exported for
// tests and diagnostics. The value is refreshed inside RebuildTools on every
// topology change.
func (g *Gateway) ServerInfoVersion() string {
	g.versionMu.RLock()
	defer g.versionMu.RUnlock()
	if g.aggregateImpl == nil {
		return g.version
	}
	return g.aggregateImpl.Version
}

// RebuildTools re-registers all tools from running backends on BOTH the
// aggregate MCP server AND the per-backend MCP servers (Phase 16.1). Called
// after backend start/stop to update the exposed tool sets.
//
// Thread safety: the MCP SDK's AddTool and RemoveTools methods are internally
// synchronized via sync.Mutex (confirmed in go-sdk v1.4.1, server.go
// changeAndNotify). Our toolsMu serializes the diff logic (add/remove) so
// the registeredTools / backendRegistered maps stay consistent with the
// registered state on each server. serverMu guards perBackendServer map
// mutations (create/delete entries); reads via ServerFor use RLock.
//
// Per-backend scoping: each backend gets its own *mcp.Server lazy-created on
// first tool registration. Tools are exposed WITHOUT namespace prefix
// ("echo" rather than "context7__echo") and descriptions WITHOUT
// "[<backend>] " prefix. list_changed notifications fire only on the
// affected per-backend server because each backend has its own instance.
func (g *Gateway) RebuildTools() {
	allTools := g.filteredTools()

	g.toolsMu.Lock()
	defer g.toolsMu.Unlock()

	// ---------- Aggregate surface (existing behavior) ----------

	// CR-3 fix: determine which aggregate tools to add and which to remove.
	newNames := make(map[string]struct{}, len(allTools))
	for _, nt := range allTools {
		newNames[nt.namespaced] = struct{}{}
	}

	var toRemove []string
	for name := range g.registeredTools {
		if _, exists := newNames[name]; !exists {
			toRemove = append(toRemove, name)
		}
	}
	if len(toRemove) > 0 {
		g.aggregateServer.RemoveTools(toRemove...)
		for _, name := range toRemove {
			delete(g.registeredTools, name)
		}
	}

	for _, nt := range allTools {
		g.registerTool(nt)
		g.registeredTools[nt.namespaced] = struct{}{}
	}

	// ---------- Per-backend surface (Phase 16.1) ----------

	// Group current tools by backend. Synthetic meta-tools are included so
	// the per-backend view mirrors the aggregate view for that backend.
	byBackend := make(map[string][]namespacedTool)
	for _, nt := range allTools {
		byBackend[nt.server] = append(byBackend[nt.server], nt)
	}

	// Tear down per-backend servers for backends no longer present.
	g.serverMu.Lock()
	for backend := range g.perBackendServer {
		if _, stillPresent := byBackend[backend]; !stillPresent {
			delete(g.perBackendServer, backend)
			delete(g.backendRegistered, backend)
		}
	}
	// Lazy-create per-backend servers for new backends (keyed by name).
	for backend := range byBackend {
		if _, exists := g.perBackendServer[backend]; !exists {
			g.perBackendServer[backend] = mcp.NewServer(&mcp.Implementation{
				Name:    backend,
				Version: g.version,
			}, nil)
			g.backendRegistered[backend] = make(map[string]struct{})
		}
	}
	g.serverMu.Unlock()

	// Diff + register each backend's tools on its per-backend server. Any
	// map reads for perBackendServer here are safe without RLock because we
	// hold toolsMu (the only other mutator of the same maps is this same
	// critical section) and the earlier Lock/Unlock above synchronized the
	// membership changes.
	for backend, tools := range byBackend {
		srv := g.perBackendServer[backend]
		reg := g.backendRegistered[backend]

		// Build the set of unnamespaced names this backend currently exposes.
		wantNames := make(map[string]struct{}, len(tools))
		for _, nt := range tools {
			wantNames[nt.name] = struct{}{}
		}

		// Remove tools no longer exposed by this backend.
		var bRemove []string
		for name := range reg {
			if _, keep := wantNames[name]; !keep {
				bRemove = append(bRemove, name)
			}
		}
		if len(bRemove) > 0 {
			srv.RemoveTools(bRemove...)
			for _, name := range bRemove {
				delete(reg, name)
			}
		}

		// Register current tools on the per-backend server.
		for _, nt := range tools {
			g.registerToolForBackend(srv, nt)
			reg[nt.name] = struct{}{}
		}
	}

	g.logger.Info("tools rebuilt",
		"count", len(allTools),
		"removed", len(toRemove),
		"backends", len(byBackend),
	)

	// Phase 16.6 T16.6.4 — cache-busting serverInfo.version. Hash over the
	// sorted backend names and the current total tool count. The SDK reads
	// aggregateImpl.Version on every initialize (server.go:1463). Some
	// clients key their tool-list cache by (name, version); changing version
	// on topology change invites a fresh fetch.
	//
	// Concurrency: we hold toolsMu here (the single writer path for tool
	// topology), so computing off of allTools + byBackend is consistent.
	// versionMu guards our own reads (ServerInfoVersion) against the
	// assignment below. The SDK's initialize read of aggregateImpl.Version
	// races with the assignment in principle, but:
	//   (a) initialize is per-session, infrequent, and completes in microseconds;
	//   (b) RebuildTools runs in response to backend start/stop, also infrequent;
	//   (c) the write is a single string-header store (two words); on all
	//       supported platforms it is observably atomic in practice.
	// We therefore accept the theoretical race rather than recreating the
	// server (which would sever every existing session).
	names := make([]string, 0, len(byBackend))
	for name := range byBackend {
		names = append(names, name)
	}
	sort.Strings(names)
	newVer := computeTopologyVersion(g.version, names, len(allTools))
	g.versionMu.Lock()
	if g.aggregateImpl != nil {
		g.aggregateImpl.Version = newVer
	}
	g.versionMu.Unlock()
}

// namespacedTool holds info for a tool to be registered.
type namespacedTool struct {
	server      string
	name        string
	description string
	namespaced  string
	inputSchema any
	synthetic    bool     // true for meta-tools created by consolidateExcess
	allowedTools []string // tool names dispatchable via this meta-tool
}

// registerTool registers a single namespaced tool on the MCP server.
// Uses server.AddTool (non-generic) to preserve the backend's InputSchema.
// The generic mcp.AddTool would override the schema based on the Go type.
func (g *Gateway) registerTool(nt namespacedTool) {
	desc := fmt.Sprintf("[%s] %s", nt.server, nt.description)

	// Preserve the backend's original schema so MCP clients can pass arguments.
	// If no schema from backend, use a permissive object schema.
	schema := nt.inputSchema
	if schema == nil {
		schema = map[string]any{"type": "object"}
	}

	tool := &mcp.Tool{
		Name:        nt.namespaced,
		Description: desc,
		InputSchema: schema,
	}

	if nt.synthetic {
		g.registerMetaTool(tool, nt)
		return
	}

	// Use non-generic server.AddTool to avoid SDK schema generation.
	g.aggregateServer.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args map[string]any
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: "invalid arguments: " + err.Error()}},
					IsError: true,
				}, nil
			}
		}
		result, err := g.router.Call(ctx, nt.namespaced, args)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
				IsError: true,
			}, nil
		}
		return result, nil
	})
}

// registerToolForBackend registers a single tool on a per-backend MCP server
// (Phase 16.1). This mirrors registerTool but:
//   - the tool name is the BARE backend-local name (e.g. "echo"),
//     not the aggregate-namespaced form ("context7__echo");
//   - the description carries NO "[<backend>] " prefix, because the
//     per-backend server already identifies the backend in its own
//     Implementation.Name (visible to clients via serverInfo);
//   - routing still uses nt.namespaced because g.router keys dispatch by
//     the aggregate namespaced form regardless of which surface received
//     the call.
//
// Synthetic meta-tools (consolidateExcess) are registered identically —
// the meta-tool dispatcher resolves tool_name against the allowed set, and
// the target backend is captured at registration time.
func (g *Gateway) registerToolForBackend(target *mcp.Server, nt namespacedTool) {
	schema := nt.inputSchema
	if schema == nil {
		schema = map[string]any{"type": "object"}
	}

	tool := &mcp.Tool{
		Name:        nt.name, // unnamespaced on per-backend surface
		Description: nt.description,
		InputSchema: schema,
	}

	if nt.synthetic {
		allowedSet := make(map[string]struct{}, len(nt.allowedTools))
		for _, t := range nt.allowedTools {
			allowedSet[t] = struct{}{}
		}
		target.AddTool(tool, g.makeMetaToolHandler(allowedSet, nt.server))
		return
	}

	target.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args map[string]any
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: "invalid arguments: " + err.Error()}},
					IsError: true,
				}, nil
			}
		}
		result, err := g.router.Call(ctx, nt.namespaced, args)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
				IsError: true,
			}, nil
		}
		return result, nil
	})
}

const (
	// metaToolName is the bare name for synthetic meta-tools.
	// If a backend exposes a tool with this name, a collision warning is logged.
	metaToolName = "more_tools"

	// metaToolDescMaxNames caps the number of tool names listed in the
	// meta-tool description to avoid unbounded token cost for LLM clients.
	metaToolDescMaxNames = 10
)

// registerMetaTool registers a synthetic __more_tools dispatcher.
// The allowedSet is built once at registration time for O(1) lookup.
func (g *Gateway) registerMetaTool(tool *mcp.Tool, nt namespacedTool) {
	allowedSet := make(map[string]struct{}, len(nt.allowedTools))
	for _, t := range nt.allowedTools {
		allowedSet[t] = struct{}{}
	}
	g.aggregateServer.AddTool(tool, g.makeMetaToolHandler(allowedSet, nt.server))
}

// makeMetaToolHandler creates the dispatch handler closure for a meta-tool.
// Separated from registerMetaTool for testability.
func (g *Gateway) makeMetaToolHandler(allowedSet map[string]struct{}, serverName string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args map[string]any
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: "invalid arguments: " + err.Error()}},
					IsError: true,
				}, nil
			}
		}

		toolName, _ := args["tool_name"].(string)
		if toolName == "" {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "tool_name argument is required"}},
				IsError: true,
			}, nil
		}
		if len(toolName) > 128 {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "tool_name exceeds maximum length of 128 characters"}},
				IsError: true,
			}, nil
		}
		if strings.Contains(toolName, models.ToolNameSeparator) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "tool_name must not contain namespace separator"}},
				IsError: true,
			}, nil
		}
		if _, ok := allowedSet[toolName]; !ok {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("tool %q is not in the allowed set", toolName)}},
				IsError: true,
			}, nil
		}

		innerArgs, _ := args["arguments"].(map[string]any)
		result, err := g.router.CallDirect(ctx, serverName, toolName, innerArgs)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
				IsError: true,
			}, nil
		}
		return result, nil
	}
}

// serverAllowed checks if a server passes the include/exclude filter.
func (g *Gateway) serverAllowed(name string, filter *models.ToolFilter) bool {
	if filter == nil {
		return true
	}

	switch filter.Mode {
	case "allowlist":
		if len(filter.IncludeServers) == 0 {
			return true
		}
		return slices.Contains(filter.IncludeServers, name)

	case "blocklist":
		return !slices.Contains(filter.ExcludeServers, name)

	default:
		// No filter mode or empty — apply exclude list only.
		return !slices.Contains(filter.ExcludeServers, name)
	}
}

// filteredTools returns the filtered list of namespaced tools from all running
// backends. It acquires cfgMu.RLock internally to read ToolFilter — callers
// do not need to hold any lock. This is the single source of truth for tool
// filtering (server allowed, per-server budget, global budget).
func (g *Gateway) filteredTools() []namespacedTool {
	g.cfgMu.RLock()
	filter := g.cfg.Gateway.ToolFilter
	compress := g.cfg.Gateway.CompressSchemas
	g.cfgMu.RUnlock()

	entries := g.lm.Entries()

	var allTools []namespacedTool
	for _, entry := range entries {
		if entry.Status != models.StatusRunning && entry.Status != models.StatusDegraded {
			continue
		}
		if !entry.Config.ExposeToolsEnabled() {
			continue
		}
		if !g.serverAllowed(entry.Name, filter) {
			continue
		}

		budget := 0
		if filter != nil && filter.PerServerBudget > 0 {
			budget = filter.PerServerBudget
		}
		consolidate := budget > 0 && filter != nil && filter.ConsolidateExcess

		count := 0
		var excessTools []string
		for _, tool := range entry.Tools {
			if budget > 0 && count >= budget {
				if consolidate {
					excessTools = append(excessTools, tool.Name)
					continue
				}
				break
			}
			allTools = append(allTools, namespacedTool{
				server:      entry.Name,
				name:        tool.Name,
				description: tool.Description,
				namespaced:  g.router.NamespacedTool(entry.Name, tool.Name),
				inputSchema: tool.InputSchema,
			})
			count++
		}

		// Create meta-tool for excess tools beyond per-server budget.
		if len(excessTools) > 0 {
			// Warn if a backend tool collides with the reserved meta-tool name.
			for _, t := range excessTools {
				if t == metaToolName {
					g.logger.Warn("backend tool name collides with meta-tool reserved name",
						"server", entry.Name, "tool", metaToolName)
				}
			}

			// Cap description to avoid unbounded token cost for LLM clients.
			var desc string
			if len(excessTools) <= metaToolDescMaxNames {
				desc = "Access additional tools: " + strings.Join(excessTools, ", ")
			} else {
				desc = "Access additional tools: " + strings.Join(excessTools[:metaToolDescMaxNames], ", ") +
					fmt.Sprintf(" ... and %d more", len(excessTools)-metaToolDescMaxNames)
			}

			allTools = append(allTools, namespacedTool{
				server:      entry.Name,
				name:        metaToolName,
				description: desc,
				namespaced:  g.router.NamespacedTool(entry.Name, metaToolName),
				inputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"tool_name": map[string]any{
							"type": "string",
							"enum": excessTools,
						},
						"arguments": map[string]any{"type": "object"},
					},
					"required": []string{"tool_name"},
				},
				synthetic:    true,
				allowedTools: excessTools,
			})
		}
	}

	// Apply schema compression before global budgeting. Compression reduces
	// token cost per tool (shorter descriptions, no examples) but the global
	// ToolBudget counts tools, not tokens — so ordering does not affect which
	// tools survive the budget cut. Compression is applied first so that
	// meta-tool descriptions are also compressed.
	if compress {
		for i := range allTools {
			allTools[i].description = compressDescription(allTools[i].description)
			allTools[i].inputSchema = stripExamples(allTools[i].inputSchema)
		}
	}

	// Apply global tool budget.
	if filter != nil && filter.ToolBudget > 0 && len(allTools) > filter.ToolBudget {
		dropped := allTools[filter.ToolBudget:]
		for _, t := range dropped {
			if t.synthetic {
				g.logger.Warn("global ToolBudget truncated synthetic meta-tool — excess tools unreachable",
					"tool", t.namespaced, "server", t.server)
			}
		}
		allTools = allTools[:filter.ToolBudget]
	}

	return allTools
}

// sentenceBoundary matches a period followed by whitespace or end of string.
// Avoids splitting version numbers like "v2.0" (period not followed by space).
// \s covers all whitespace including \n and \r, so multi-line descriptions
// are truncated correctly at the first sentence boundary.
var sentenceBoundary = regexp.MustCompile(`\.(?:\s|$)`)

// compressDescription truncates a tool description to its first sentence.
// If no sentence boundary is found, falls back to 80 runes (or keeps as-is
// if shorter than 80 runes). Uses rune count to avoid splitting multibyte
// UTF-8 characters.
func compressDescription(desc string) string {
	loc := sentenceBoundary.FindStringIndex(desc)
	if loc != nil {
		return desc[:loc[0]+1]
	}
	if utf8.RuneCountInString(desc) <= 80 {
		return desc
	}
	// Truncate to 80 runes without splitting multibyte characters.
	n := 0
	for i := range desc {
		if n == 80 {
			return desc[:i]
		}
		n++
	}
	return desc
}

// stripExamples removes the top-level "examples" key from a JSON Schema map.
// Nested "examples" inside property sub-schemas are intentionally preserved
// (stripping is shallow — only the root schema object is affected).
// Returns the schema unmodified if it is not a map or has no examples key.
// Creates a shallow copy to avoid mutating the original backend schema;
// nested map values are shared references (read-only contract).
func stripExamples(schema any) any {
	m, ok := schema.(map[string]any)
	if !ok {
		return schema
	}
	if _, has := m["examples"]; !has {
		return schema
	}
	copied := make(map[string]any, len(m))
	for k, v := range m {
		if k != "examples" {
			copied[k] = v
		}
	}
	return copied
}

// ListTools returns the currently exposed namespaced tools (for REST API).
func (g *Gateway) ListTools() []models.ToolInfo {
	allTools := g.filteredTools()

	result := make([]models.ToolInfo, len(allTools))
	for i, nt := range allTools {
		result[i] = models.ToolInfo{
			Name:        nt.namespaced,
			Description: nt.description,
			Server:      nt.server,
			InputSchema: nt.inputSchema,
		}
	}
	return result
}
