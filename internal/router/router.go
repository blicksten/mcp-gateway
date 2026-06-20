// Package router routes MCP tool calls to the appropriate backend session.
package router

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"mcp-gateway/internal/lifecycle"
	"mcp-gateway/internal/models"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// SessionProvider returns the active MCP client session for a backend.
// EnsureStarted and IsLazyPending support the TASK C2.2 lazy-spawn path;
// when the flag is OFF, Call/CallDirect never invoke them.
type SessionProvider interface {
	Session(name string) (*mcp.ClientSession, bool)
	Entry(name string) (models.ServerEntry, bool)
	// EnsureStarted triggers an on-demand spawn for an Idle backend and blocks
	// until the backend is Running, the caller's budget expires, or the spawn
	// fails. Returns (StatusRunning, nil) on success, (StatusIdle, ErrLazyWarming)
	// on budget expiry, or (StatusError, err) on spawn failure.
	EnsureStarted(ctx context.Context, name string) (models.ServerStatus, error)
	// IsLazyPending reports whether a lazy spawn is currently in flight for name.
	IsLazyPending(name string) bool
}

// Router routes namespaced tool calls to the appropriate backend.
type Router struct {
	sp        SessionProvider
	separator string

	// inflightCalls is the count of currently executing Call/CallDirect
	// invocations. Incremented on entry, decremented on return (via defer).
	// Used by the idle exit monitor (TASK A) to ensure no tool call is
	// killed mid-flight. int64 for atomic.AddInt64 alignment on 32-bit.
	inflightCalls atomic.Int64

	// lastCallNano is the wall-clock time (UnixNano) of the most recent
	// Call/CallDirect entry. Stored as int64 so atomic.Int64 can be used.
	// Zero means no call has occurred since startup.
	lastCallNano atomic.Int64
}

// New creates a router with the given session provider.
func New(sp SessionProvider) *Router {
	return &Router{
		sp:        sp,
		separator: models.ToolNameSeparator,
	}
}

// InflightCalls returns the number of tool calls currently executing.
// Safe for concurrent use; used by the idle exit monitor.
func (r *Router) InflightCalls() int64 {
	return r.inflightCalls.Load()
}

// LastCallTime returns the wall-clock time of the most recent Call or
// CallDirect invocation, or the zero time if no call has occurred.
// Safe for concurrent use; used by the idle exit monitor.
func (r *Router) LastCallTime() time.Time {
	ns := r.lastCallNano.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
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
	r.inflightCalls.Add(1)
	r.lastCallNano.Store(time.Now().UnixNano())
	defer r.inflightCalls.Add(-1)

	server, tool, ok := r.SplitToolName(namespacedTool)
	if !ok {
		return nil, fmt.Errorf("invalid tool name %q: must be server%stool", namespacedTool, r.separator)
	}

	entry, exists := r.sp.Entry(server)
	if !exists {
		return nil, fmt.Errorf("server %q not found", server)
	}
	if entry.Status != models.StatusRunning && entry.Status != models.StatusDegraded {
		// TASK C2.2: when the flag is ON and the backend is Idle (or has a
		// spawn already in flight), trigger or join a lazy spawn before
		// dispatching. On success, re-fetch entry and fall through to dispatch.
		// Flag OFF leaves this block unreachable — the else branch returns the
		// original rejection error, preserving flag-OFF byte-identity.
		if lifecycle.LazySpawnEnabled() &&
			(entry.Status == models.StatusIdle || r.sp.IsLazyPending(server)) {
			status, err := r.sp.EnsureStarted(ctx, server)
			if err != nil {
				return nil, fmt.Errorf("server %q: %w", server, err)
			}
			if status != models.StatusRunning && status != models.StatusDegraded {
				return nil, fmt.Errorf("server %q is not running after spawn (status: %s)", server, status)
			}
			// Re-fetch entry so the session look-up below sees the live state.
			entry, exists = r.sp.Entry(server)
			if !exists {
				return nil, fmt.Errorf("server %q disappeared after spawn", server)
			}
		} else {
			return nil, fmt.Errorf("server %q is not running (status: %s)", server, entry.Status)
		}
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
	r.inflightCalls.Add(1)
	r.lastCallNano.Store(time.Now().UnixNano())
	defer r.inflightCalls.Add(-1)

	// CR-6 fix: check server status before accessing session.
	entry, exists := r.sp.Entry(server)
	if !exists {
		return nil, fmt.Errorf("server %q not found", server)
	}
	if entry.Status != models.StatusRunning && entry.Status != models.StatusDegraded {
		// TASK C2.2: same lazy-spawn path as Call (see Call for rationale).
		if lifecycle.LazySpawnEnabled() &&
			(entry.Status == models.StatusIdle || r.sp.IsLazyPending(server)) {
			status, err := r.sp.EnsureStarted(ctx, server)
			if err != nil {
				return nil, fmt.Errorf("server %q: %w", server, err)
			}
			if status != models.StatusRunning && status != models.StatusDegraded {
				return nil, fmt.Errorf("server %q is not available after spawn (status: %s)", server, status)
			}
			entry, exists = r.sp.Entry(server)
			if !exists {
				return nil, fmt.Errorf("server %q disappeared after spawn", server)
			}
		} else {
			return nil, fmt.Errorf("server %q is not available (status: %s)", server, entry.Status)
		}
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
