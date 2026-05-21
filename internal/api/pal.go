// Package api: REST PAL queue endpoints (FM-34).
//
// `POST /api/v1/pal/queue_review` and `GET /api/v1/pal/get_review/{task_id}` are
// REST proxies onto the orchestrator backend's `queue_review` and `get_review`
// MCP tools. They give operators a non-MCP recovery path when the MCP transport
// between Claude Code and the gateway is broken (FM-2 + FM-24 + FM-32) but the
// daemon and its orchestrator stdio backend are still alive.
//
// Auth: admin scope (ADR-0007). PAL queue access is operator-initiated and
// must remain unreachable by VSCode's built-in McpGatewayService, which only
// holds the regular Bearer via plugin .mcp.json.
//
// Routing: see Handler() in server.go — the route group is mounted under the
// adminMW + csrfProtect chain alongside /shutdown.
package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// palOrchestratorBackend is the canonical backend name registered for the
// orchestrator stdio MCP server. Centralised so a rename in user config does
// not silently break recovery — a missing backend returns 503.
const palOrchestratorBackend = "orchestrator"

// handlePalQueueReview proxies POST /api/v1/pal/queue_review to the orchestrator
// backend's `queue_review` MCP tool. Request body is forwarded verbatim as the
// tool arguments; the backend validates the schema.
func (s *Server) handlePalQueueReview(w http.ResponseWriter, r *http.Request) {
	var args map[string]any
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	s.invokePalTool(w, r, "queue_review", args)
}

// handlePalGetReview proxies GET /api/v1/pal/get_review/{task_id} to the
// orchestrator backend's `get_review` MCP tool. The task_id path parameter
// becomes the single tool argument.
func (s *Server) handlePalGetReview(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "task_id")
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "task_id path parameter is required")
		return
	}
	s.invokePalTool(w, r, "get_review", map[string]any{"task_id": taskID})
}

// invokePalTool is the shared dispatch path for both PAL proxy handlers.
// It returns 503 if the orchestrator backend is not registered (recovery
// surface unavailable), 502 on backend-level errors, and unwraps the
// CallToolResult's text content as JSON for a clean REST response.
func (s *Server) invokePalTool(w http.ResponseWriter, r *http.Request, tool string, args map[string]any) {
	if _, ok := s.lm.Entry(palOrchestratorBackend); !ok {
		writeError(w, http.StatusServiceUnavailable,
			"orchestrator backend is not registered — REST PAL recovery path requires the orchestrator MCP server in gateway config")
		return
	}

	result, err := s.gw.Router().CallDirect(r.Context(), palOrchestratorBackend, tool, args)
	if err != nil {
		writeError(w, http.StatusBadGateway, "pal tool dispatch failed: "+err.Error())
		return
	}

	// MCP CallToolResult.IsError signals a tool-level error (e.g. invalid
	// args, backend stdio dead). Map to 502 so clients can distinguish
	// from gateway-level failures (already 502) and the not-registered
	// recovery surface (503).
	if result.IsError {
		writeError(w, http.StatusBadGateway, "pal tool returned error: "+collectText(result.Content))
		return
	}

	body := collectText(result.Content)
	if body == "" {
		writeError(w, http.StatusBadGateway, "pal tool returned empty response")
		return
	}

	// Tool output is a JSON string per the MCP text-content convention;
	// surface it directly as the response body instead of wrapping it in
	// another JSON envelope (avoids double-encoding for callers).
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

// collectText concatenates the text segments of an MCP CallToolResult's
// content array. Non-text segments are skipped — the orchestrator's PAL
// tools always emit a single text segment.
func collectText(content []mcp.Content) string {
	var b strings.Builder
	for _, c := range content {
		if t, ok := c.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}
