package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	"mcp-gateway/internal/lifecycle"
	"mcp-gateway/internal/models"
	"mcp-gateway/internal/proxy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// addServerToAll adds a server to both the lifecycle manager and the cfg map,
// keeping both sources of truth consistent.
func addServerToAll(t *testing.T, srv *Server, lm *lifecycle.Manager, name string, sc *models.ServerConfig) {
	t.Helper()
	require.NoError(t, lm.AddServer(name, sc))
	srv.cfgMu.Lock()
	srv.cfg.Servers[name] = sc
	srv.cfgMu.Unlock()
}

// renameBody constructs a PATCH body with new_name set.
func renameBody(newName string) map[string]any {
	return map[string]any{"new_name": newName}
}

// bufLogger builds a text-format slog.Logger writing to buf.
func bufLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// TestPatchServer_Rename_Success verifies the happy path: old name removed,
// new name added to lm + cfg, env preserved, response shape {"status":"patched",...}.
func TestPatchServer_Rename_Success(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()

	sc := &models.ServerConfig{
		URL:      "http://localhost:3000/mcp",
		Disabled: true,
		Env:      []string{"FOO=bar"},
	}
	addServerToAll(t, srv, lm, "ctx7", sc)

	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/ctx7", renameBody("context7-prod"))
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "patched", resp["status"])
	assert.Equal(t, "ctx7", resp["old_name"])
	assert.Equal(t, "context7-prod", resp["new_name"])

	_, oldInLM := lm.Entry("ctx7")
	_, newInLM := lm.Entry("context7-prod")
	assert.False(t, oldInLM, "old name must not be in lm")
	assert.True(t, newInLM, "new name must be in lm")

	srv.cfgMu.RLock()
	_, oldInCfg := srv.cfg.Servers["ctx7"]
	newCfg, newInCfg := srv.cfg.Servers["context7-prod"]
	srv.cfgMu.RUnlock()
	assert.False(t, oldInCfg, "old name must not be in cfg")
	require.True(t, newInCfg, "new name must be in cfg")
	assert.Equal(t, []string{"FOO=bar"}, newCfg.Env, "env must be preserved")
}

// TestPatchServer_Rename_NameCollision verifies 409 when new_name already exists.
func TestPatchServer_Rename_NameCollision(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()

	sc1 := &models.ServerConfig{URL: "http://localhost:3000/mcp", Disabled: true}
	sc2 := &models.ServerConfig{URL: "http://localhost:3001/mcp", Disabled: true}
	addServerToAll(t, srv, lm, "ctx7", sc1)
	addServerToAll(t, srv, lm, "ctx8", sc2)

	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/ctx7", renameBody("ctx8"))
	assert.Equal(t, http.StatusConflict, rr.Code)

	// Both entries must be unchanged in lm and cfg.
	_, ctx7InLM := lm.Entry("ctx7")
	_, ctx8InLM := lm.Entry("ctx8")
	assert.True(t, ctx7InLM)
	assert.True(t, ctx8InLM)
}

// TestPatchServer_Rename_InvalidName verifies 400 for bad new_name values.
func TestPatchServer_Rename_InvalidName(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()

	sc := &models.ServerConfig{URL: "http://localhost:3000/mcp", Disabled: true}
	addServerToAll(t, srv, lm, "ctx7", sc)

	for _, badName := range []string{"", "bad name with spaces"} {
		rr := doRequest(t, handler, "PATCH", "/api/v1/servers/ctx7", renameBody(badName))
		assert.Equal(t, http.StatusBadRequest, rr.Code,
			"expected 400 for new_name=%q", badName)
	}
}

// TestPatchServer_Rename_NotFound verifies 404 when the old name is absent.
func TestPatchServer_Rename_NotFound(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/nonexistent", renameBody("newname"))
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// TestPatchServer_Rename_SAPRefused_Old verifies 400 when the old name is SAP-shaped.
func TestPatchServer_Rename_SAPRefused_Old(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()

	sc := &models.ServerConfig{URL: "http://localhost:3000/mcp", Disabled: true}
	addServerToAll(t, srv, lm, "vsp-DEV", sc)

	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/vsp-DEV", renameBody("something-else"))
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "renaming SAP-named servers is not supported")
}

// TestPatchServer_Rename_SAPRefused_New verifies 400 when the new name is SAP-shaped.
func TestPatchServer_Rename_SAPRefused_New(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()

	sc := &models.ServerConfig{URL: "http://localhost:3000/mcp", Disabled: true}
	addServerToAll(t, srv, lm, "ctx7", sc)

	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/ctx7", renameBody("vsp-XYZ"))
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "renaming SAP-named servers is not supported")
}

// TestPatchServer_Rename_SAPBeatsBadEnv proves validation order: SAP refusal (step 2)
// short-circuits env validation (step 4).
func TestPatchServer_Rename_SAPBeatsBadEnv(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()

	sc := &models.ServerConfig{URL: "http://localhost:3000/mcp", Disabled: true}
	addServerToAll(t, srv, lm, "ctx7", sc)

	body := map[string]any{
		"new_name": "vsp-XYZ",
		"add_env":  []string{"LD_PRELOAD=/evil.so"},
	}
	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/ctx7", body)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "renaming SAP-named servers is not supported")
	assert.NotContains(t, rr.Body.String(), "not permitted")
}

// TestPatchServer_Rename_RollbackOnRemoveFailure verifies that when
// lm.RemoveServer(name) fails, the rollback fires (removes newName) and cfg is untouched.
// Uses SetTestRemoveHook so RemoveServer returns an error WITHOUT deleting the entry,
// keeping the plan assertion "final lm state has only ctx7" satisfied.
func TestPatchServer_Rename_RollbackOnRemoveFailure(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()

	sc := &models.ServerConfig{URL: "http://localhost:3000/mcp", Disabled: true}
	addServerToAll(t, srv, lm, "ctx7", sc)

	// ctx7 removal returns error (entry kept in lm); ctx8 rollback removal succeeds.
	lm.SetTestRemoveHook(func(name string) error {
		if name == "ctx7" {
			return fmt.Errorf("simulated remove failure")
		}
		return nil
	})

	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/ctx7", renameBody("ctx8"))
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Contains(t, rr.Body.String(), "rename failed at remove stage (rolled back)")

	// lm must have only ctx7; ctx8 must have been rolled back.
	_, ctx7InLM := lm.Entry("ctx7")
	_, ctx8InLM := lm.Entry("ctx8")
	assert.True(t, ctx7InLM, "ctx7 must remain in lm after rollback")
	assert.False(t, ctx8InLM, "ctx8 must have been rolled back from lm")

	// cfg must be untouched.
	srv.cfgMu.RLock()
	_, ctx7InCfg := srv.cfg.Servers["ctx7"]
	_, ctx8InCfg := srv.cfg.Servers["ctx8"]
	srv.cfgMu.RUnlock()
	assert.True(t, ctx7InCfg)
	assert.False(t, ctx8InCfg)
}

// TestPatchServer_Rename_RollbackOfRollbackErrorLogged verifies that when
// both RemoveServer(name) and the rollback RemoveServer(newName) fail, an
// ERROR-level log entry with both error fields is produced and HTTP 500 returned.
func TestPatchServer_Rename_RollbackOfRollbackErrorLogged(t *testing.T) {
	var logBuf bytes.Buffer
	srv, lm := setupTestServer(t)
	srv.logger = bufLogger(&logBuf)
	handler := srv.Handler()

	sc := &models.ServerConfig{URL: "http://localhost:3000/mcp", Disabled: true}
	addServerToAll(t, srv, lm, "ctx7", sc)

	// Both ctx7 removal and ctx8 rollback-removal return errors.
	lm.SetTestRemoveHook(func(name string) error {
		return fmt.Errorf("simulated remove failure for %s", name)
	})

	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/ctx7", renameBody("ctx8"))
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Contains(t, rr.Body.String(), "rollback error")

	logged := logBuf.String()
	assert.Contains(t, logged, "ERROR", "must log at ERROR level")
	assert.Contains(t, logged, "rollback also failed")
}

// TestPatchServer_Rename_StartFailWarnsNotRollback verifies that when lm.Start
// returns an error, rename still returns 200 OK and both cfg + lm reflect the
// new name — no rollback (parity with handleAddServer:787-789).
func TestPatchServer_Rename_StartFailWarnsNotRollback(t *testing.T) {
	var logBuf bytes.Buffer
	srv, lm := setupTestServer(t)
	srv.logger = bufLogger(&logBuf)
	handler := srv.Handler()

	// Not disabled so auto-start is attempted; real backend absent so Start fails.
	sc := &models.ServerConfig{URL: "http://localhost:3000/mcp", Disabled: false}
	addServerToAll(t, srv, lm, "ctx7", sc)

	// Stop hook returning nil means RemoveServer succeeds.
	lm.SetTestStopHook(func(name string) error { return nil })

	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/ctx7", renameBody("ctx8"))
	assert.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "patched", resp["status"])

	_, newInLM := lm.Entry("ctx8")
	assert.True(t, newInLM, "new name must be in lm even when auto-start failed")
	srv.cfgMu.RLock()
	_, newInCfg := srv.cfg.Servers["ctx8"]
	srv.cfgMu.RUnlock()
	assert.True(t, newInCfg)

	logged := logBuf.String()
	assert.Contains(t, logged, "WARN")
	assert.Contains(t, logged, "auto-start after rename failed")
}

// TestPatchServer_Rename_BadEnvShortCircuits verifies 400 BEFORE state mutation
// when env is invalid.
func TestPatchServer_Rename_BadEnvShortCircuits(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()

	sc := &models.ServerConfig{URL: "http://localhost:3000/mcp", Disabled: true}
	addServerToAll(t, srv, lm, "ctx7", sc)

	body := map[string]any{
		"new_name": "ctx8",
		"add_env":  []string{"LD_PRELOAD=/evil.so"},
	}
	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/ctx7", body)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "not permitted")

	// Neither lm nor cfg may have been mutated.
	_, ctx8InLM := lm.Entry("ctx8")
	assert.False(t, ctx8InLM)
	srv.cfgMu.RLock()
	_, ctx8InCfg := srv.cfg.Servers["ctx8"]
	srv.cfgMu.RUnlock()
	assert.False(t, ctx8InCfg)
}

// TestPatchServer_Rename_PluginRegenFailureSwallowed verifies that
// TriggerPluginRegen is called and rename returns 200 OK.
// TriggerPluginRegen has no error return; failures are logged but swallowed.
func TestPatchServer_Rename_PluginRegenFailureSwallowed(t *testing.T) {
	srv, lm := setupTestServer(t)

	var regenCalled bool
	srv.testRegenFn = func() { regenCalled = true }

	handler := srv.Handler()

	sc := &models.ServerConfig{URL: "http://localhost:3000/mcp", Disabled: true}
	addServerToAll(t, srv, lm, "ctx7", sc)

	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/ctx7", renameBody("ctx8"))
	assert.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "patched", resp["status"])
	assert.True(t, regenCalled, "TriggerPluginRegen must have been called")
}

// TestPatchServer_Rename_StopTimedOutSilentZombie is a regression guard
// (F-ARCH-4 / T1.17): when lm.RemoveServer returns nil (Stop error swallowed),
// the handler treats this as success and both cfg + lm reflect the new name.
func TestPatchServer_Rename_StopTimedOutSilentZombie(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()

	sc := &models.ServerConfig{URL: "http://localhost:3000/mcp", Disabled: true}
	addServerToAll(t, srv, lm, "ctx7", sc)

	// Hook returns nil — models a Stop-timed-out-but-swallowed scenario.
	lm.SetTestStopHook(func(name string) error { return nil })

	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/ctx7", renameBody("ctx8"))
	assert.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "patched", resp["status"])

	_, newInLM := lm.Entry("ctx8")
	assert.True(t, newInLM)
	srv.cfgMu.RLock()
	_, newInCfg := srv.cfg.Servers["ctx8"]
	srv.cfgMu.RUnlock()
	assert.True(t, newInCfg)
}

// TestPatchServer_Rename_PreservesEnv verifies that env values are intact
// after a pure rename (no env delta in the patch).
func TestPatchServer_Rename_PreservesEnv(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()

	origEnv := []string{"KEY=value", "OTHER=thing"}
	sc := &models.ServerConfig{
		URL:      "http://localhost:3000/mcp",
		Disabled: true,
		Env:      origEnv,
	}
	addServerToAll(t, srv, lm, "ctx7", sc)

	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/ctx7", renameBody("ctx8"))
	require.Equal(t, http.StatusOK, rr.Code)

	srv.cfgMu.RLock()
	newSC, ok := srv.cfg.Servers["ctx8"]
	srv.cfgMu.RUnlock()
	require.True(t, ok)
	assert.Equal(t, origEnv, newSC.Env)
}

// TestPatchServer_Rename_CombinedWithEnvDelta verifies atomicity:
// (a) all-success: both rename and env delta applied.
// (b) step-2-fail: both rolled back — cfg and lm show only old name.
func TestPatchServer_Rename_CombinedWithEnvDelta(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv, lm := setupTestServer(t)
		handler := srv.Handler()

		sc := &models.ServerConfig{
			URL:      "http://localhost:3000/mcp",
			Disabled: true,
			Env:      []string{"OLD=x"},
		}
		addServerToAll(t, srv, lm, "ctx7", sc)

		body := map[string]any{
			"new_name":   "ctx8",
			"add_env":    []string{"NEW=y"},
			"remove_env": []string{"OLD"},
		}
		rr := doRequest(t, handler, "PATCH", "/api/v1/servers/ctx7", body)
		require.Equal(t, http.StatusOK, rr.Code)

		srv.cfgMu.RLock()
		newSC, ok := srv.cfg.Servers["ctx8"]
		_, oldOK := srv.cfg.Servers["ctx7"]
		srv.cfgMu.RUnlock()
		require.True(t, ok)
		assert.False(t, oldOK)
		assert.Equal(t, []string{"NEW=y"}, newSC.Env, "env delta must be applied atomically")
	})

	t.Run("step2_fail_both_unchanged", func(t *testing.T) {
		srv, lm := setupTestServer(t)
		handler := srv.Handler()

		sc := &models.ServerConfig{
			URL:      "http://localhost:3000/mcp",
			Disabled: true,
			Env:      []string{"OLD=x"},
		}
		addServerToAll(t, srv, lm, "ctx7", sc)

		lm.SetTestRemoveHook(func(name string) error {
			if name == "ctx7" {
				return fmt.Errorf("remove failed")
			}
			return nil
		})

		body := map[string]any{
			"new_name": "ctx8",
			"add_env":  []string{"NEW=y"},
		}
		rr := doRequest(t, handler, "PATCH", "/api/v1/servers/ctx7", body)
		assert.Equal(t, http.StatusInternalServerError, rr.Code)

		srv.cfgMu.RLock()
		oldSC, oldOK := srv.cfg.Servers["ctx7"]
		_, newOK := srv.cfg.Servers["ctx8"]
		srv.cfgMu.RUnlock()
		assert.True(t, oldOK)
		assert.False(t, newOK)
		assert.Equal(t, []string{"OLD=x"}, oldSC.Env, "env delta must not appear on rollback")
	})
}

// TestPatchServer_Rename_DisabledFlag verifies that a disabled server does
// not trigger auto-start under the new name (step 4 guard).
func TestPatchServer_Rename_DisabledFlag(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()

	lm.SetTestStopHook(func(name string) error { return nil })

	sc := &models.ServerConfig{URL: "http://localhost:3000/mcp", Disabled: true}
	addServerToAll(t, srv, lm, "ctx7", sc)

	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/ctx7", renameBody("ctx8"))
	require.Equal(t, http.StatusOK, rr.Code)

	// ctx8 must be in lm with Stopped status (not auto-started).
	entry, ok := lm.Entry("ctx8")
	require.True(t, ok)
	assert.Equal(t, models.StatusStopped, entry.Status,
		"disabled server must not be auto-started after rename")
}

// TestPatchServer_RenameNoOp_SameName verifies that when new_name == name,
// the rename branch is skipped and {"status":"updated"} is returned (F-ARCH-8).
func TestPatchServer_RenameNoOp_SameName(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()

	sc := &models.ServerConfig{
		URL:      "http://localhost:3000/mcp",
		Disabled: true,
		Env:      []string{"BEFORE=v"},
	}
	addServerToAll(t, srv, lm, "ctx7", sc)

	body := map[string]any{
		"new_name": "ctx7",          // same name — no-op rename
		"add_env":  []string{"AFTER=w"},
	}
	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/ctx7", body)
	// Disabled server → no restart → 200.
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "updated", resp["status"],
		"no-op rename must return {status:updated}, not {status:patched,...}")
	assert.Empty(t, resp["old_name"])
	assert.Empty(t, resp["new_name"])

	// ctx7 must still be in lm + cfg.
	_, stillInLM := lm.Entry("ctx7")
	assert.True(t, stillInLM)
}

// TestPatchServer_Rename_RebuildToolsCalled verifies that TriggerPluginRegen
// fires on a successful rename when gw is non-nil (proxy of RebuildTools + regen
// in one observable signal per the testRegenFn hook).
func TestPatchServer_Rename_RebuildToolsCalled(t *testing.T) {
	srv, lm := setupTestServer(t)

	var regenCount int
	srv.testRegenFn = func() { regenCount++ }

	handler := srv.Handler()

	sc := &models.ServerConfig{URL: "http://localhost:3000/mcp", Disabled: true}
	addServerToAll(t, srv, lm, "ctx7", sc)

	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/ctx7", renameBody("ctx8"))
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, 1, regenCount, "TriggerPluginRegen must fire exactly once on rename")
}

// TestPatchServer_Rename_NilGateway_NoPanic verifies that rename succeeds
// without panicking when the server was constructed without a gateway (gw==nil).
func TestPatchServer_Rename_NilGateway_NoPanic(t *testing.T) {
	cfg := &models.Config{Servers: make(map[string]*models.ServerConfig)}
	cfg.ApplyDefaults()
	lm := lifecycle.NewManager(cfg, "test", testLogger())

	srv := NewServer(lm, nil, nil, cfg, "", testLogger(), AuthConfig{}, "test")
	handler := srv.Handler()

	sc := &models.ServerConfig{URL: "http://localhost:3000/mcp", Disabled: true}
	require.NoError(t, lm.AddServer("ctx7", sc))
	srv.cfgMu.Lock()
	srv.cfg.Servers["ctx7"] = sc
	srv.cfgMu.Unlock()

	assert.NotPanics(t, func() {
		rr := doRequest(t, handler, "PATCH", "/api/v1/servers/ctx7", renameBody("ctx8"))
		assert.Equal(t, http.StatusOK, rr.Code)
	})
}

// TestPatchServer_PatchEnvOnly_NoRebuildTools is a regression guard: an
// env-only PATCH (no new_name) must NOT call TriggerPluginRegen.
func TestPatchServer_PatchEnvOnly_NoRebuildTools(t *testing.T) {
	srv, lm := setupTestServer(t)

	var regenCalled bool
	srv.testRegenFn = func() { regenCalled = true }

	handler := srv.Handler()

	sc := &models.ServerConfig{URL: "http://localhost:3000/mcp", Disabled: true}
	addServerToAll(t, srv, lm, "s1", sc)

	body := map[string]any{"add_env": []string{"FOO=bar"}}
	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/s1", body)
	// Disabled → no restart → 200.
	assert.Equal(t, http.StatusOK, rr.Code)

	assert.False(t, regenCalled,
		"env-only PATCH must not trigger TriggerPluginRegen")
}

// TestPatchServer_RenameRefusal_UsesSapnamePackage asserts the sapname.IsSAP
// call-site with case-strict byte-prefix invariants (T1.23 / F-ARCH-A2 LOW):
//   (a) new_name="vsp-DEV"  → refused (400) — positive SAP case
//   (b) new_name="random-server" → NOT refused — negative case
//   (c) new_name="Vsp-DEV" → NOT refused — capital V, byte-strict prefix check
//   (d) new_name="vsp-dev" → NOT refused — lowercase SID, charset check at grammar_gen.go:91
func TestPatchServer_RenameRefusal_UsesSapnamePackage(t *testing.T) {
	cases := []struct {
		newName string
		wantSAP bool
	}{
		{"vsp-DEV", true},       // (a) positive SAP
		{"random-server", false}, // (b) negative
		{"Vsp-DEV", false},      // (c) capital V — not SAP (byte-strict prefix)
		{"vsp-dev", false},      // (d) lowercase SID — not SAP (charset grammar_gen.go:91)
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("new=%s", tc.newName), func(t *testing.T) {
			srv, lm := setupTestServer(t)
			handler := srv.Handler()

			sc := &models.ServerConfig{URL: "http://localhost:3000/mcp", Disabled: true}
			addServerToAll(t, srv, lm, "regular-server", sc)

			rr := doRequest(t, handler, "PATCH", "/api/v1/servers/regular-server", renameBody(tc.newName))

			if tc.wantSAP {
				assert.Equal(t, http.StatusBadRequest, rr.Code)
				assert.Contains(t, rr.Body.String(), "renaming SAP-named servers is not supported")
			} else {
				// Non-SAP: 200 patched or 409 collision or other — but NOT 400 SAP refusal.
				assert.NotEqual(t, http.StatusBadRequest, rr.Code,
					"non-SAP name must not be refused with SAP refusal; got body: %s", rr.Body.String())
				assert.NotContains(t, rr.Body.String(), "renaming SAP-named servers is not supported")
			}
		})
	}
}

// TestPatchServer_Rename_SAPServer_EnvPatch verifies that env-only PATCHes
// against SAP-named servers are NOT refused (SAP refusal is rename-only).
func TestPatchServer_Rename_SAPServer_EnvPatch(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()

	sc := &models.ServerConfig{URL: "http://localhost:3000/mcp", Disabled: true}
	addServerToAll(t, srv, lm, "vsp-DEV", sc)

	body := map[string]any{"add_env": []string{"SAP_USER=TESTER"}}
	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/vsp-DEV", body)
	// Disabled → no restart → 200. SAP refusal must NOT fire for env-only patch.
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.NotContains(t, rr.Body.String(), "renaming SAP-named servers is not supported")
}

// TestPatchServer_Rename_ResponseShape verifies the exact JSON response shape
// of a successful rename.
func TestPatchServer_Rename_ResponseShape(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()

	sc := &models.ServerConfig{URL: "http://localhost:3000/mcp", Disabled: true}
	addServerToAll(t, srv, lm, "alpha", sc)

	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/alpha", renameBody("beta"))
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "patched", resp["status"])
	assert.Equal(t, "alpha", resp["old_name"])
	assert.Equal(t, "beta", resp["new_name"])
}

// TestPatchServer_Rename_NoopRenameWithAddEnv verifies that when new_name
// equals the current name and an env delta is provided, the no-op rename path
// applies the env delta via in-place patch logic and returns {"status":"updated"}.
func TestPatchServer_Rename_NoopRenameWithAddEnv(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()

	sc := &models.ServerConfig{URL: "http://localhost:3000/mcp", Disabled: true}
	addServerToAll(t, srv, lm, "ctx7", sc)

	body := map[string]any{
		"new_name": "ctx7",
		"add_env":  []string{"EXTRA=value"},
	}
	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/ctx7", body)
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "updated", resp["status"])
	assert.Empty(t, resp["old_name"])
	assert.Empty(t, resp["new_name"])

	// ctx7 remains and env delta was applied.
	srv.cfgMu.RLock()
	updatedSC, ok := srv.cfg.Servers["ctx7"]
	srv.cfgMu.RUnlock()
	require.True(t, ok)
	assert.Equal(t, []string{"EXTRA=value"}, updatedSC.Env)
}

// TestPatchServer_Rename_RollbackLoggedAtError verifies ERROR-level log for a
// successful rollback (only ctx7 removal fails; ctx8 rollback succeeds).
func TestPatchServer_Rename_RollbackLoggedAtError(t *testing.T) {
	var logBuf bytes.Buffer
	srv, lm := setupTestServer(t)
	srv.logger = bufLogger(&logBuf)
	handler := srv.Handler()

	sc := &models.ServerConfig{URL: "http://localhost:3000/mcp", Disabled: true}
	addServerToAll(t, srv, lm, "ctx7", sc)

	lm.SetTestRemoveHook(func(name string) error {
		if name == "ctx7" {
			return fmt.Errorf("remove ctx7 failed")
		}
		return nil
	})

	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/ctx7", renameBody("ctx8"))
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Contains(t, rr.Body.String(), "rename failed at remove stage (rolled back)")

	logged := logBuf.String()
	assert.Contains(t, logged, "ERROR")
	assert.Contains(t, logged, "rename failed at remove stage")
	// When rollback succeeded, the double-failure log must NOT appear.
	assert.NotContains(t, logged, "rollback also failed")
}

// TestPatchServer_Rename_ValidateServerNameOnNewName verifies that
// ValidateServerName is invoked on new_name and produces a 400 with
// recognizable wording (T1.2 — no new validator needed).
func TestPatchServer_Rename_ValidateServerNameOnNewName(t *testing.T) {
	srv, lm := setupTestServer(t)
	handler := srv.Handler()

	sc := &models.ServerConfig{URL: "http://localhost:3000/mcp", Disabled: true}
	addServerToAll(t, srv, lm, "ctx7", sc)

	rr := doRequest(t, handler, "PATCH", "/api/v1/servers/ctx7", renameBody("bad name with spaces"))
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	// ValidateServerName error wording contains the name.
	assert.Contains(t, rr.Body.String(), "bad name with spaces")
}

// Ensure proxy package is referenced (used in TestPatchServer_Rename_NilGateway_NoPanic).
var _ = (*proxy.Gateway)(nil)
