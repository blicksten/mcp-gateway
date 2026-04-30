package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestWritePluginUserConfig_Create asserts that when settings.json does
// not yet exist, the writer creates it with a well-formed pluginConfigs
// entry holding gateway_url + auth_token. First-time-install scenario.
func TestWritePluginUserConfig_Create(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	ins := &installer{}

	if err := ins.writePluginUserConfig(settingsPath, "http://127.0.0.1:8765", "tok-abc"); err != nil {
		t.Fatalf("writePluginUserConfig: %v", err)
	}

	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var top map[string]interface{}
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("parse: %v\nbody=%s", err, string(raw))
	}
	pc, ok := top["pluginConfigs"].(map[string]interface{})
	if !ok {
		t.Fatalf("pluginConfigs missing or wrong type: %#v", top["pluginConfigs"])
	}
	entry, ok := pc[pluginID].(map[string]interface{})
	if !ok {
		t.Fatalf("pluginConfigs[%q] missing or wrong type: %#v", pluginID, pc[pluginID])
	}
	if got := entry["gateway_url"]; got != "http://127.0.0.1:8765" {
		t.Fatalf("gateway_url: got %v, want http://127.0.0.1:8765", got)
	}
	if got := entry["auth_token"]; got != "tok-abc" {
		t.Fatalf("auth_token: got %v, want tok-abc", got)
	}
}

// TestWritePluginUserConfig_Merge asserts that other top-level keys and
// other plugins' pluginConfigs entries round-trip untouched when we
// merge our two keys in. Critical safety check — settings.json carries
// hundreds of unrelated keys and we MUST NOT clobber them.
func TestWritePluginUserConfig_Merge(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	// Pre-populate with realistic foreign keys + an unrelated plugin's config.
	prior := map[string]interface{}{
		"allowedTools":       []interface{}{"Bash(git status)", "Read"},
		"prefersReducedMotion": true,
		"enabledPlugins": map[string]interface{}{
			"some-other-plugin@market-x": true,
			pluginID:                     true,
		},
		"pluginConfigs": map[string]interface{}{
			"some-other-plugin@market-x": map[string]interface{}{
				"foreign_key": "untouched",
			},
		},
	}
	priorBody, _ := json.MarshalIndent(prior, "", "  ")
	if err := os.WriteFile(settingsPath, priorBody, 0o600); err != nil {
		t.Fatalf("write prior: %v", err)
	}

	ins := &installer{}
	if err := ins.writePluginUserConfig(settingsPath, "http://10.0.0.1:9999", "tok-xyz"); err != nil {
		t.Fatalf("writePluginUserConfig: %v", err)
	}

	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var top map[string]interface{}
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Foreign top-level keys preserved.
	if got, _ := top["prefersReducedMotion"].(bool); !got {
		t.Errorf("prefersReducedMotion clobbered: %v", top["prefersReducedMotion"])
	}
	allowed, _ := top["allowedTools"].([]interface{})
	if len(allowed) != 2 {
		t.Errorf("allowedTools clobbered: %v", top["allowedTools"])
	}

	// enabledPlugins preserved verbatim.
	ep, _ := top["enabledPlugins"].(map[string]interface{})
	if _, ok := ep["some-other-plugin@market-x"]; !ok {
		t.Errorf("enabledPlugins[some-other-plugin] clobbered: %#v", ep)
	}

	// Other plugin's pluginConfigs entry preserved.
	pc, _ := top["pluginConfigs"].(map[string]interface{})
	other, _ := pc["some-other-plugin@market-x"].(map[string]interface{})
	if got := other["foreign_key"]; got != "untouched" {
		t.Errorf("foreign plugin's foreign_key clobbered: %v", got)
	}

	// Our entry was added.
	ours, ok := pc[pluginID].(map[string]interface{})
	if !ok {
		t.Fatalf("our entry missing: %#v", pc[pluginID])
	}
	if got := ours["gateway_url"]; got != "http://10.0.0.1:9999" {
		t.Errorf("gateway_url: got %v", got)
	}
	if got := ours["auth_token"]; got != "tok-xyz" {
		t.Errorf("auth_token: got %v", got)
	}
}

// TestWritePluginUserConfig_Idempotent asserts that calling the writer
// twice with identical inputs is a no-op — the file's mtime stays the
// same after the second call. Avoids mtime churn that would invalidate
// downstream watchers.
func TestWritePluginUserConfig_Idempotent(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	ins := &installer{}

	if err := ins.writePluginUserConfig(settingsPath, "http://127.0.0.1:8765", "tok-abc"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	info1, err := os.Stat(settingsPath)
	if err != nil {
		t.Fatalf("stat 1: %v", err)
	}

	if err := ins.writePluginUserConfig(settingsPath, "http://127.0.0.1:8765", "tok-abc"); err != nil {
		t.Fatalf("second write: %v", err)
	}
	info2, err := os.Stat(settingsPath)
	if err != nil {
		t.Fatalf("stat 2: %v", err)
	}

	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Fatalf("idempotent re-write changed mtime: %v -> %v",
			info1.ModTime(), info2.ModTime())
	}

	// Also confirm no .tmp.* leftover.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if name := e.Name(); name != "settings.json" {
			t.Errorf("unexpected leftover: %s", name)
		}
	}
}

// TestWritePluginUserConfig_TokenRotation asserts that calling the
// writer with a NEW auth_token (token rotation scenario) overwrites
// the prior value and bumps mtime. Counterpart to the idempotent test.
func TestWritePluginUserConfig_TokenRotation(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	ins := &installer{}

	if err := ins.writePluginUserConfig(settingsPath, "http://127.0.0.1:8765", "tok-old"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := ins.writePluginUserConfig(settingsPath, "http://127.0.0.1:8765", "tok-NEW"); err != nil {
		t.Fatalf("rotation write: %v", err)
	}

	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var top map[string]interface{}
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("parse: %v", err)
	}
	pc, _ := top["pluginConfigs"].(map[string]interface{})
	entry, _ := pc[pluginID].(map[string]interface{})
	if got := entry["auth_token"]; got != "tok-NEW" {
		t.Fatalf("rotation did not overwrite: got %v, want tok-NEW", got)
	}
}

// TestWritePluginUserConfig_Concurrent asserts that two parallel writer
// invocations both succeed and the file ends up valid JSON with our
// plugin's entry present. Last-writer-wins semantics are acceptable for
// this layer (single OS user, mcp-ctl invocations are rare); a proper
// file lock is a candidate v2 enhancement (see writePluginUserConfig
// docstring's "Concurrency" note).
func TestWritePluginUserConfig_Concurrent(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	ins := &installer{}

	const N = 8
	var wg sync.WaitGroup
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok := "tok-worker-" + string(rune('a'+i))
			errs[i] = ins.writePluginUserConfig(settingsPath, "http://127.0.0.1:8765", tok)
		}()
	}
	wg.Wait()

	// With the advisory file lock in writePluginUserConfig, all
	// writers must succeed (lock serializes them). Any failure
	// indicates the lock implementation is broken.
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d failed under lock: %v", i, err)
		}
	}

	// Final file must be valid JSON with our entry intact.
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var top map[string]interface{}
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("final state is not valid JSON: %v\nbody=%s", err, string(raw))
	}
	pc, ok := top["pluginConfigs"].(map[string]interface{})
	if !ok {
		t.Fatalf("pluginConfigs missing in final state")
	}
	entry, ok := pc[pluginID].(map[string]interface{})
	if !ok {
		t.Fatalf("our pluginConfigs entry missing: %#v", pc[pluginID])
	}
	if _, ok := entry["gateway_url"].(string); !ok {
		t.Errorf("gateway_url missing or non-string: %#v", entry["gateway_url"])
	}
	if _, ok := entry["auth_token"].(string); !ok {
		t.Errorf("auth_token missing or non-string: %#v", entry["auth_token"])
	}
}

// TestWritePluginUserConfig_RejectNonObjectPluginConfigs asserts that
// the writer fails loud if pluginConfigs already exists but is the
// wrong type (e.g., operator hand-edit mistake) rather than silently
// destroying the malformed value.
func TestWritePluginUserConfig_RejectNonObjectPluginConfigs(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	prior := []byte(`{"pluginConfigs": "this-should-be-an-object-not-a-string"}` + "\n")
	if err := os.WriteFile(settingsPath, prior, 0o600); err != nil {
		t.Fatalf("write prior: %v", err)
	}

	ins := &installer{}
	err := ins.writePluginUserConfig(settingsPath, "http://127.0.0.1:8765", "tok-abc")
	if err == nil {
		t.Fatalf("expected error for malformed pluginConfigs, got nil")
	}
}
