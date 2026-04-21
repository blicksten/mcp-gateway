// Package proxy implements the MCP Gateway — the core engine that aggregates
// backend MCP servers and exposes them through a unified MCP interface.
package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
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
	return g
}

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

// buildMCPServer creates the MCP server with tool aggregation.
func (g *Gateway) buildMCPServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "mcp-gateway",
		Version: g.version,
	}, nil)
	return server
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
