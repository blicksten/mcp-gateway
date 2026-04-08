// Package router routes MCP tool calls to the appropriate backend session.
package router

import (
	"context"
	"fmt"
	"strings"

	"mcp-gateway/internal/models"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// SessionProvider returns the active MCP client session for a backend.
type SessionProvider interface {
	Session(name string) (*mcp.ClientSession, bool)
	Entry(name string) (models.ServerEntry, bool)
}

// Router routes namespaced tool calls to the appropriate backend.
type Router struct {
	sp        SessionProvider
	separator string
}

// New creates a router with the given session provider.
func New(sp SessionProvider) *Router {
	return &Router{
		sp:        sp,
		separator: models.ToolNameSeparator,
	}
}

// SplitToolName splits "server__tool" into ("server", "tool").
func (r *Router) SplitToolName(namespacedTool string) (server, tool string, ok bool) {
	parts := strings.SplitN(namespacedTool, r.separator, 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// NamespacedTool joins server + tool with the separator.
func (r *Router) NamespacedTool(server, tool string) string {
	return server + r.separator + tool
}

// Call routes a tool call to the appropriate backend session.
func (r *Router) Call(ctx context.Context, namespacedTool string, args map[string]any) (*mcp.CallToolResult, error) {
	server, tool, ok := r.SplitToolName(namespacedTool)
	if !ok {
		return nil, fmt.Errorf("invalid tool name %q: must be server%stool", namespacedTool, r.separator)
	}

	entry, exists := r.sp.Entry(server)
	if !exists {
		return nil, fmt.Errorf("server %q not found", server)
	}
	if entry.Status != models.StatusRunning && entry.Status != models.StatusDegraded {
		return nil, fmt.Errorf("server %q is not running (status: %s)", server, entry.Status)
	}

	session, ok := r.sp.Session(server)
	if !ok {
		return nil, fmt.Errorf("server %q has no active session", server)
	}

	return session.CallTool(ctx, &mcp.CallToolParams{
		Name:      tool,
		Arguments: args,
	})
}

// CallDirect calls a tool on a specific backend (bypasses namespace splitting).
// Used by REST API for direct backend access (e.g., hidden servers).
func (r *Router) CallDirect(ctx context.Context, server, tool string, args map[string]any) (*mcp.CallToolResult, error) {
	// CR-6 fix: check server status before accessing session.
	entry, exists := r.sp.Entry(server)
	if !exists {
		return nil, fmt.Errorf("server %q not found", server)
	}
	if entry.Status != models.StatusRunning && entry.Status != models.StatusDegraded {
		return nil, fmt.Errorf("server %q is not available (status: %s)", server, entry.Status)
	}

	session, ok := r.sp.Session(server)
	if !ok {
		return nil, fmt.Errorf("server %q has no active session", server)
	}

	return session.CallTool(ctx, &mcp.CallToolParams{
		Name:      tool,
		Arguments: args,
	})
}
