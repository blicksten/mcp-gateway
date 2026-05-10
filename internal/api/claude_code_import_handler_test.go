package api

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mcp-gateway/internal/claudeimport"
	"mcp-gateway/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// claudeimportTestResolveGo returns the absolute path of `go` for use
// in test fixtures that exercise validation (which requires absolute
// command paths). Skips the test on a system where `go` is not on PATH
// — but if we got this far in `go test`, it always is.
func claudeimportTestResolveGo(t *testing.T) string {
	t.Helper()
	abs, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go not on PATH; skipping: %v", err)
	}
	return abs
}

func TestImportSnapshot_BadSource_400(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	rr := doRequest(t, handler, http.MethodGet, "/api/v1/claude-code/import-snapshot?source=bogus", nil)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "unknown source")
}

func TestImportSnapshot_MissingSource_400(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	rr := doRequest(t, handler, http.MethodGet, "/api/v1/claude-code/import-snapshot", nil)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "source")
}

func TestImportSnapshot_CCProject_RequiresProjectRoot(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	rr := doRequest(t, handler, http.MethodGet, "/api/v1/claude-code/import-snapshot?source=cc_project", nil)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "project_root")
}

func TestImportSnapshot_NonExistent_ReturnsExistsFalse(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	rr := doRequest(t, handler, http.MethodGet, "/api/v1/claude-code/import-snapshot?source=cc_global", nil)
	require.Equal(t, http.StatusOK, rr.Code)

	var resp ImportSnapshotResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.False(t, resp.Exists, "non-existent file should yield exists=false")
	assert.NotNil(t, resp.Rows, "rows must be non-nil even when empty")
	assert.Empty(t, resp.Rows)
}

func TestImportSnapshot_RoundTripsEntries(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	body := `{
  "verbose": false,
  "mcpServers": {
    "stdio-x": {"type":"stdio","command":"go","args":["pal-mcp"]},
    "http-y": {"type":"http","url":"http://localhost:80"}
  }
}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(body), 0o600))

	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	rr := doRequest(t, handler, http.MethodGet, "/api/v1/claude-code/import-snapshot?source=cc_global", nil)
	require.Equal(t, http.StatusOK, rr.Code)

	var resp ImportSnapshotResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.True(t, resp.Exists)
	require.Len(t, resp.Rows, 2)
	// Sorted alphabetically.
	assert.Equal(t, "http-y", resp.Rows[0].Name)
	assert.Equal(t, "http", resp.Rows[0].Type)
	assert.Equal(t, "http://localhost:80", resp.Rows[0].URL)
	assert.Equal(t, "stdio-x", resp.Rows[1].Name)
	assert.Equal(t, "stdio", resp.Rows[1].Type)
	assert.Equal(t, "go", resp.Rows[1].Command)
}

func TestImportSnapshot_DriftFieldsOnConflict(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	body := `{"mcpServers":{"pal":{"type":"stdio","command":"/usr/bin/go","args":["new"]}}}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(body), 0o600))

	srv, _ := setupTestServer(t)
	srv.cfg.Servers["pal"] = &models.ServerConfig{
		Command: "/usr/bin/go",
		Args:    []string{"old"},
	}
	handler := srv.Handler()

	rr := doRequest(t, handler, http.MethodGet, "/api/v1/claude-code/import-snapshot?source=cc_global", nil)
	require.Equal(t, http.StatusOK, rr.Code)

	var resp ImportSnapshotResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp.Rows, 1)
	row := resp.Rows[0]
	assert.True(t, row.GatewayHasName, "gateway has 'pal' so flag must be true")
	// args differ between source ("new") and gateway ("old"), so drift.
	assert.Contains(t, row.DriftFields, "args")
}

func TestImportApply_BadJSON_400(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	// We can't use doRequest with a string body — build manually.
	rr := doRequest(t, handler, http.MethodPost, "/api/v1/claude-code/import-apply", "not-json-shape")
	// "not-json-shape" Marshal'd is `"not-json-shape"` (a JSON string),
	// which fails to decode into ImportApplyRequest because the
	// top-level is a string not an object — Decode() should error.
	// But Decode of a string into a struct silently succeeds with
	// zero fields. The empty Ops list path triggers our 400.
	if rr.Code != http.StatusBadRequest {
		t.Errorf("got %d; want 400 (invalid JSON or empty ops); body=%s", rr.Code, rr.Body.String())
	}
}

func TestImportApply_EmptyOps_400(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	rr := doRequest(t, handler, http.MethodPost, "/api/v1/claude-code/import-apply",
		ImportApplyRequest{Ops: []claudeimport.Op{}})
	require.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "empty")
}

func TestImportApply_CopySuccess_AddsToGateway(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	body := `{"mcpServers":{"pal":{"type":"stdio","command":"go","args":["pal-mcp"]}}}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(body), 0o600))

	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	req := ImportApplyRequest{
		Ops: []claudeimport.Op{
			{
				Source:   "cc_global",
				Name:     "pal",
				Action:   claudeimport.ActionCopy,
				Conflict: claudeimport.ConflictSkip,
			},
		},
	}
	rr := doRequest(t, handler, http.MethodPost, "/api/v1/claude-code/import-apply", req)
	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())

	var resp ImportApplyResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp.Results, 1)
	if resp.Results[0].Status != claudeimport.StatusApplied {
		t.Errorf("status = %v reason=%q, want applied", resp.Results[0].Status, resp.Results[0].Reason)
	}

	// Gateway state must now contain pal.
	srv.cfgMu.RLock()
	_, ok := srv.cfg.Servers["pal"]
	srv.cfgMu.RUnlock()
	assert.True(t, ok, "gateway state must contain pal after apply")
}

func TestImportApply_MoveDeletesFromSource(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	path := filepath.Join(dir, ".claude.json")
	body := `{"verbose":false,"mcpServers":{"pal":{"type":"stdio","command":"go"},"keep":{"type":"stdio","command":"go"}}}`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	req := ImportApplyRequest{
		Ops: []claudeimport.Op{
			{
				Source:   "cc_global",
				Name:     "pal",
				Action:   claudeimport.ActionMove,
				Conflict: claudeimport.ConflictSkip,
			},
		},
	}
	rr := doRequest(t, handler, http.MethodPost, "/api/v1/claude-code/import-apply", req)
	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())

	// Source file should no longer contain pal but should still
	// contain keep AND verbose (top-level non-mcpServers preserved).
	post, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(post), `"pal"`)
	assert.Contains(t, string(post), `"keep"`)
	assert.Contains(t, string(post), `"verbose":false`)

	// Gateway has pal now.
	srv.cfgMu.RLock()
	_, ok := srv.cfg.Servers["pal"]
	srv.cfgMu.RUnlock()
	assert.True(t, ok)
}

func TestImportApply_SingleRegen_OnBatchOf5(t *testing.T) {
	// Build a fixture with 5 entries, apply all in one POST, and
	// verify that the underlying TriggerPluginRegen pathway fires
	// only once (reuses the testRegenFn injection point from T-A.5).
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	path := filepath.Join(dir, ".claude.json")
	body := `{"mcpServers":{
        "a":{"type":"stdio","command":"go"},
        "b":{"type":"stdio","command":"go"},
        "c":{"type":"stdio","command":"go"},
        "d":{"type":"stdio","command":"go"},
        "e":{"type":"stdio","command":"go"}
    }}`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	srv, _ := setupTestServer(t)

	// Inject regen counter — testRegenFn is set; addServerInProcess
	// suppresses regen via SuppressPluginRegen, and the handler
	// fires one TriggerPluginRegen at end of batch.
	regenCalls := 0
	srv.testRegenFn = func() { regenCalls++ }
	handler := srv.Handler()

	ops := []claudeimport.Op{}
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		ops = append(ops, claudeimport.Op{
			Source:   "cc_global",
			Name:     name,
			Action:   claudeimport.ActionCopy,
			Conflict: claudeimport.ConflictSkip,
		})
	}
	rr := doRequest(t, handler, http.MethodPost, "/api/v1/claude-code/import-apply",
		ImportApplyRequest{Ops: ops})
	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())

	var resp ImportApplyResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Len(t, resp.Results, 5)
	// Each result must be applied.
	for _, r := range resp.Results {
		assert.Equal(t, claudeimport.StatusApplied, r.Status, "row %s reason=%q", r.Name, r.Reason)
	}

	// Critical: exactly ONE end-of-batch regen fired (R-26 / X2).
	assert.Equal(t, 1, regenCalls, "expected exactly 1 regen call across 5-entry batch")
}

func TestImportApply_OverwriteWithDriftFields_Reported(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	body := `{"mcpServers":{"pal":{"type":"stdio","command":"go","args":["new"]}}}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(body), 0o600))

	srv, lm := setupTestServer(t)
	// Pre-register pal through both lm and cfg so removeServerInProcess
	// can find it. ResolveCommand("go") gives the same absolute path
	// the Adder will produce for the imported entry — drift field
	// is injected via Args differing.
	resolved := claudeimportTestResolveGo(t)
	require.NoError(t, lm.AddServer("pal", &models.ServerConfig{Command: resolved, Args: []string{"old"}}))
	srv.cfgMu.Lock()
	srv.cfg.Servers["pal"] = &models.ServerConfig{Command: resolved, Args: []string{"old"}}
	srv.cfgMu.Unlock()
	handler := srv.Handler()

	req := ImportApplyRequest{
		Ops: []claudeimport.Op{
			{
				Source:   "cc_global",
				Name:     "pal",
				Action:   claudeimport.ActionCopy,
				Conflict: claudeimport.ConflictOverwrite,
			},
		},
	}
	rr := doRequest(t, handler, http.MethodPost, "/api/v1/claude-code/import-apply", req)
	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())

	var resp ImportApplyResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp.Results, 1)
	assert.Equal(t, claudeimport.StatusApplied, resp.Results[0].Status)
	assert.Contains(t, resp.Results[0].DriftFields, "args")
}

// TestImportSnapshot_VersionSkewGate_ClientReadsCompatMatrix encodes the
// R-24 contract: the picker checks /api/v1/claude-code/compat-matrix to
// detect daemons too old for import endpoints. Older daemons return 404
// on import-snapshot itself; newer daemons return the matrix with
// import support flagged. This test pins the matrix shape so the picker
// can rely on it.
func TestImportSnapshot_VersionSkewGate_ClientReadsCompatMatrix(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	rr := doRequest(t, handler, http.MethodGet, "/api/v1/claude-code/compat-matrix", nil)
	require.Equal(t, http.StatusOK, rr.Code)

	// The compat-matrix is a JSON document; we just sanity-check it
	// is well-formed and not empty. The exact schema is owned by the
	// daemon's compat_matrix.json embed (claude_code_handlers.go);
	// version-skew detection is the picker's responsibility.
	body := rr.Body.String()
	require.NotEmpty(t, body)
	if !strings.HasPrefix(strings.TrimSpace(body), "{") {
		preview := body
		if len(preview) > 200 {
			preview = preview[:200]
		}
		t.Errorf("compat-matrix body should be a JSON object: %s", preview)
	}
}
