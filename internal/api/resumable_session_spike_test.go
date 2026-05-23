package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- registry mechanics -----------------------------------------------------

func TestSessionStateRegistry_PutGetDelete(t *testing.T) {
	r := NewSessionStateRegistry()
	assert.Equal(t, 0, r.Size())

	assert.Nil(t, r.Get("missing"))

	state := &CachedSessionState{
		InitializeParams: &mcp.InitializeParams{
			ProtocolVersion: "2025-06-18",
			ClientInfo:      &mcp.Implementation{Name: "test-client", Version: "0.0.1"},
		},
		LogLevel: "info",
		UserID:   "u-1",
	}
	r.Put("sid-1", state)
	assert.Equal(t, 1, r.Size())
	assert.False(t, r.Get("sid-1").LastSeen.IsZero(), "Put must stamp LastSeen")

	cp := r.Get("sid-1")
	require.NotNil(t, cp)
	assert.Equal(t, "test-client", cp.InitializeParams.ClientInfo.Name)
	// Mutating the returned copy must NOT affect the registry.
	cp.UserID = "tampered"
	assert.Equal(t, "u-1", r.Get("sid-1").UserID)

	r.Delete("sid-1")
	assert.Equal(t, 0, r.Size())
	assert.Nil(t, r.Get("sid-1"))
}

// --- disk persistence (T0.7.1 closure 2026-05-23) ---------------------------

// makeTestState returns a deterministic state for persistence-roundtrip tests.
func makeTestState(client, version string) *CachedSessionState {
	return &CachedSessionState{
		InitializeParams: &mcp.InitializeParams{
			ProtocolVersion: "2025-06-18",
			ClientInfo:      &mcp.Implementation{Name: client, Version: version},
		},
		LogLevel: "info",
		UserID:   "u-" + client,
	}
}

// Put → New(load) → Get yields the same state with LastSeen preserved. The
// roundtrip is the core acceptance for T0.7.1's stated goal "registry
// survives daemon restart".
func TestSessionStateRegistry_PersistenceRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")

	r1 := NewSessionStateRegistryWithPath(path)
	r1.Put("sid-A", makeTestState("alpha", "1.0"))
	r1.Put("sid-B", makeTestState("beta", "2.0"))
	require.Equal(t, 2, r1.Size())

	// File must exist after Put (atomic flush is synchronous).
	_, statErr := os.Stat(path)
	require.NoError(t, statErr, "Put must flush to disk synchronously")

	// Simulate daemon restart: new registry, same path, expect the same state.
	r2 := NewSessionStateRegistryWithPath(path)
	require.Equal(t, 2, r2.Size(), "post-restart registry must rehydrate Size")

	a := r2.Get("sid-A")
	require.NotNil(t, a)
	assert.Equal(t, "alpha", a.InitializeParams.ClientInfo.Name)
	assert.Equal(t, "u-alpha", a.UserID)
	assert.False(t, a.LastSeen.IsZero(), "LastSeen must round-trip through disk")

	b := r2.Get("sid-B")
	require.NotNil(t, b)
	assert.Equal(t, "beta", b.InitializeParams.ClientInfo.Name)
}

// Delete must remove from disk too — otherwise a deleted session would be
// resurrected on next startup, contradicting MCP DELETE /mcp semantics.
func TestSessionStateRegistry_PersistenceDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")

	r1 := NewSessionStateRegistryWithPath(path)
	r1.Put("sid-A", makeTestState("alpha", "1.0"))
	r1.Put("sid-B", makeTestState("beta", "2.0"))
	r1.Delete("sid-A")

	r2 := NewSessionStateRegistryWithPath(path)
	assert.Equal(t, 1, r2.Size())
	assert.Nil(t, r2.Get("sid-A"), "deleted session must not resurrect on restart")
	require.NotNil(t, r2.Get("sid-B"))
}

// Concurrent Put from N goroutines: every state must end up in the on-disk
// file with no corruption (atomic rename guarantees indivisible writes).
// Validates the "best-effort, last writer wins" contract per Put doc.
func TestSessionStateRegistry_PersistenceConcurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")

	r := NewSessionStateRegistryWithPath(path)
	const N = 20
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := "concurrent-" + string(rune('A'+n))
			r.Put(id, makeTestState(id, "v"))
		}(i)
	}
	wg.Wait()

	// File must be valid JSON after the contention storm.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var got map[string]*CachedSessionState
	require.NoError(t, json.Unmarshal(data, &got), "disk file must remain valid JSON under concurrent Put")
	// Final state may have any subset depending on flush ordering, but in-memory
	// AND on-disk must agree on the post-Wait snapshot.
	r2 := NewSessionStateRegistryWithPath(path)
	assert.Equal(t, r.Size(), r2.Size(), "post-restart Size must match in-memory Size")
}

// Empty diskPath disables persistence — same observable behaviour as
// NewSessionStateRegistry(). Guards backward compat for tests that don't
// want disk I/O.
func TestSessionStateRegistry_EmptyPathInMemoryOnly(t *testing.T) {
	r := NewSessionStateRegistryWithPath("")
	r.Put("sid-X", makeTestState("x", "1.0"))
	assert.Equal(t, 1, r.Size())
	// No file should be written when path is empty — nothing to assert
	// directly, but a follow-up constructor must not see the state.
	r2 := NewSessionStateRegistryWithPath("")
	assert.Equal(t, 0, r2.Size(), "second empty-path registry must be a fresh empty one")
}

// Corrupted JSON on disk: registry must boot empty (no panic), so the
// daemon never refuses to start over a malformed cache.
func TestSessionStateRegistry_CorruptedFileBootsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	require.NoError(t, os.WriteFile(path, []byte("not valid json {{{"), 0o600))

	r := NewSessionStateRegistryWithPath(path)
	assert.Equal(t, 0, r.Size(), "corrupted file must yield empty registry, not panic")

	// Subsequent Put must work and overwrite the corrupted file.
	r.Put("sid-recover", makeTestState("recover", "1.0"))
	r2 := NewSessionStateRegistryWithPath(path)
	assert.Equal(t, 1, r2.Size(), "post-corruption Put must be persisted on top")
}

// Missing file on disk is the cold-start case: registry boots empty with no
// error, ready to accept the first Put.
func TestSessionStateRegistry_MissingFileColdStart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent-subdir", "sessions.json")

	r := NewSessionStateRegistryWithPath(path)
	assert.Equal(t, 0, r.Size())

	// Parent dir was auto-created by MkdirAll in constructor; first Put
	// must succeed end-to-end.
	r.Put("sid-cold", makeTestState("cold", "1.0"))
	_, statErr := os.Stat(path)
	require.NoError(t, statErr, "MkdirAll + first Put must produce the file")
}

// DefaultSessionRegistryDiskPath gracefully returns "" when home is
// unresolvable (degrades to in-memory) — verified by best-effort cross-
// platform probe: returns non-empty under normal test environments and
// behaves correctly when used as a path.
func TestDefaultSessionRegistryDiskPath_NonEmpty(t *testing.T) {
	p := DefaultSessionRegistryDiskPath()
	if p == "" {
		t.Skip("home directory unresolvable in this environment — skip")
	}
	assert.True(t, filepath.IsAbs(p), "default path must be absolute")
	assert.Contains(t, p, ".mcp-gateway", "default path should be under .mcp-gateway/")
	assert.True(t, strings.HasSuffix(p, "sessions.json"), "default path filename should be sessions.json")
}

// --- body capture -----------------------------------------------------------

func TestCaptureInitializeFromRequest_SingleAndBatch(t *testing.T) {
	// Single initialize.
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"c","version":"1"},"capabilities":{}}}`)
	req := mustPOST(t, body)
	got, err := CaptureInitializeFromRequest(req)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "2025-06-18", got.ProtocolVersion)
	// Body must be re-readable for the downstream dispatcher.
	rest, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	assert.Equal(t, body, rest, "body must be rewound after capture")

	// Batch containing an initialize.
	body = []byte(`[{"jsonrpc":"2.0","id":1,"method":"tools/list"},{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"c","version":"1"},"capabilities":{}}}]`)
	req = mustPOST(t, body)
	got, err = CaptureInitializeFromRequest(req)
	require.NoError(t, err)
	require.NotNil(t, got)

	// Non-initialize body returns nil.
	body = []byte(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	req = mustPOST(t, body)
	got, err = CaptureInitializeFromRequest(req)
	require.NoError(t, err)
	assert.Nil(t, got)

	// GET requests are skipped.
	r2, _ := http.NewRequest(http.MethodGet, "/mcp", nil)
	got, err = CaptureInitializeFromRequest(r2)
	require.NoError(t, err)
	assert.Nil(t, got)
}

// --- the load-bearing viability test: resurrection actually works -----------

// TestResurrectSession_AcceptsToolCallWithoutReInitialize is the central
// proof-of-viability for P0.7 D-5. Given a server, a previously-known
// session ID, and a cached InitializeParams + InitializedParams, can we
// reconstruct a session whose transport.ServeHTTP processes a tools/list
// POST WITHOUT the client first re-issuing an `initialize` handshake?
//
// If this returns a tools list (not a "session not initialized" error), the
// resurrection design is viable on public SDK primitives alone, and the
// remaining D-5 work is purely a ServeHTTP dispatch fork around this core.
func TestResurrectSession_AcceptsToolCallWithoutReInitialize(t *testing.T) {
	server := mcp.NewServer(
		&mcp.Implementation{Name: "spike-server", Version: "0.0.1"},
		nil,
	)
	mcp.AddTool(server,
		&mcp.Tool{Name: "echo", Description: "echo a value"},
		func(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, echoOut, error) {
			return nil, echoOut{Echoed: in.Value}, nil
		},
	)

	const sessionID = "spike-session-id-abcdef"
	cached := &CachedSessionState{
		InitializeParams: &mcp.InitializeParams{
			ProtocolVersion: "2025-06-18",
			ClientInfo:      &mcp.Implementation{Name: "spike-client", Version: "0.0.1"},
			Capabilities:    &mcp.ClientCapabilities{},
		},
		InitializedParams: &mcp.InitializedParams{},
		LogLevel:          "info",
	}

	session, transport, err := ResurrectSession(context.Background(), server, sessionID, cached)
	require.NoError(t, err, "resurrection must succeed with cached InitializeParams")
	require.NotNil(t, session)
	require.NotNil(t, transport)
	defer session.Close()

	assert.Equal(t, sessionID, transport.SessionID, "transport must carry the cached sessionID")

	// Dispatch a tools/list POST to the resurrected transport. A working
	// resurrection answers with a JSON-RPC result listing the registered tool.
	postBody := []byte(`{"jsonrpc":"2.0","id":7,"method":"tools/list","params":{}}`)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(postBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Session-Id", sessionID)
	req.Header.Set("MCP-Protocol-Version", "2025-06-18")

	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		transport.ServeHTTP(rec, req)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("transport.ServeHTTP timed out on tools/list against resurrected session")
	}

	assert.Equal(t, http.StatusOK, rec.Code,
		"resurrected session must accept tools/list without re-initialize; body=%s", rec.Body.String())

	// Drill into the response payload — must contain the echo tool.
	body := rec.Body.Bytes()
	assert.Truef(t,
		bytes.Contains(body, []byte(`"name":"echo"`)) ||
			strings.Contains(rec.Body.String(), "echo"),
		"response must mention the registered tool; got: %s", rec.Body.String())
}

// TestResurrectSession_RejectsBadInputs documents the contract.
func TestResurrectSession_RejectsBadInputs(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "s", Version: "0"}, nil)
	cached := &CachedSessionState{
		InitializeParams:  &mcp.InitializeParams{ProtocolVersion: "2025-06-18"},
		InitializedParams: &mcp.InitializedParams{},
	}

	_, _, err := ResurrectSession(context.Background(), nil, "id", cached)
	assert.Error(t, err, "nil server must error")

	_, _, err = ResurrectSession(context.Background(), server, "", cached)
	assert.Error(t, err, "empty sessionID must error")

	_, _, err = ResurrectSession(context.Background(), server, "id", nil)
	assert.Error(t, err, "nil cached state must error")

	bad := &CachedSessionState{InitializedParams: &mcp.InitializedParams{}}
	_, _, err = ResurrectSession(context.Background(), server, "id", bad)
	assert.Error(t, err, "missing InitializeParams must error")
}

// --- helpers ----------------------------------------------------------------

func mustPOST(t *testing.T, body []byte) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	require.NoError(t, err)
	return r
}

type echoIn struct {
	Value string `json:"value"`
}
type echoOut struct {
	Echoed string `json:"echoed"`
}

// SDK type usage assertions (purely to document the dependency surface).
var (
	_ *mcp.InitializeParams   = nil
	_ *mcp.InitializedParams  = nil
	_ *mcp.ServerSessionState = nil
	_                         = json.Marshaler(nil) // satisfies the json import
)
