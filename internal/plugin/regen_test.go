package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"mcp-gateway/internal/models"
)

// httpBackend is a helper for building a registered HTTP backend
// ServerConfig that passes Validate. Disabled defaults to false.
func httpBackend(url string) *models.ServerConfig {
	return &models.ServerConfig{URL: url}
}

// disabledHTTPBackend builds a Disabled=true HTTP backend.
func disabledHTTPBackend(url string) *models.ServerConfig {
	return &models.ServerConfig{URL: url, Disabled: true}
}

// readFileT is a Fatal-on-error Read helper for tests.
func readFileT(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

// parseMCPJSON Fatal-parses the generated file into a generic map for
// content assertions. Keeps tests resilient to formatting changes.
func parseMCPJSON(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("parse .mcp.json: %v\nbody=%s", err, string(body))
	}
	return out
}

// mcpServersFromDoc extracts the "mcpServers" child map from a parsed
// .mcp.json document, fataling with a clear message on shape mismatch.
func mcpServersFromDoc(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	raw, ok := doc["mcpServers"]
	if !ok {
		t.Fatalf("expected mcpServers key in .mcp.json, got keys %v", keysOf(doc))
	}
	srv, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("mcpServers is not an object: %T", raw)
	}
	return srv
}

func keysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// TestRegen_AtomicWrite asserts that the target .mcp.json is installed
// via rename (no partially-written state is ever observable under the
// target name). We approximate this by confirming no `.tmp.*` file is
// left behind on success and that the final file parses fully.
func TestRegen_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	regen := NewRegenerator()

	servers := map[string]*models.ServerConfig{
		"alpha": httpBackend("http://127.0.0.1:1/"),
	}
	if err := regen.Regenerate(dir, servers, "http://gw"); err != nil {
		t.Fatalf("Regenerate: %v", err)
	}

	// Target exists and is full JSON.
	target := filepath.Join(dir, MCPJSONFileName)
	body := readFileT(t, target)
	doc := parseMCPJSON(t, body)
	if _, ok := doc[GeneratedBannerKey]; !ok {
		t.Fatalf("expected generated banner key %q in output, got keys=%v",
			GeneratedBannerKey, keysOf(doc))
	}

	// No stray tmp file left in the dir.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), MCPJSONFileName+".tmp.") {
			t.Fatalf("stray tmp file left behind: %s", e.Name())
		}
	}
}

// TestRegen_Idempotent asserts that a second Regenerate call with the
// same inputs is a no-op — same mtime, no backup produced on the second
// call.
func TestRegen_Idempotent(t *testing.T) {
	dir := t.TempDir()
	regen := NewRegenerator()

	servers := map[string]*models.ServerConfig{
		"alpha": httpBackend("http://127.0.0.1:1/"),
		"beta":  httpBackend("http://127.0.0.1:2/"),
	}

	if err := regen.Regenerate(dir, servers, "http://gw"); err != nil {
		t.Fatalf("first Regenerate: %v", err)
	}

	target := filepath.Join(dir, MCPJSONFileName)
	info1, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}

	// A second call with identical inputs must not touch the file.
	if err := regen.Regenerate(dir, servers, "http://gw"); err != nil {
		t.Fatalf("second Regenerate: %v", err)
	}
	info2, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target (2nd): %v", err)
	}
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Fatalf("idempotent regen changed mtime: %v -> %v",
			info1.ModTime(), info2.ModTime())
	}

	// Also: no .bak file should appear after a no-op idempotent call.
	if _, err := os.Stat(filepath.Join(dir, MCPJSONBackupFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("idempotent regen created a backup file unexpectedly (err=%v)", err)
	}
}

// TestRegen_BackupOnOverwrite asserts that when a prior `.mcp.json`
// exists and the new content differs, the prior bytes are copied to
// `.mcp.json.bak` before the rename.
func TestRegen_BackupOnOverwrite(t *testing.T) {
	dir := t.TempDir()
	regen := NewRegenerator()

	// First regen lays down the initial file.
	first := map[string]*models.ServerConfig{
		"alpha": httpBackend("http://127.0.0.1:1/"),
	}
	if err := regen.Regenerate(dir, first, "http://gw"); err != nil {
		t.Fatalf("first Regenerate: %v", err)
	}
	target := filepath.Join(dir, MCPJSONFileName)
	priorBody := readFileT(t, target)

	// Second regen with a different backend set triggers overwrite.
	second := map[string]*models.ServerConfig{
		"alpha": httpBackend("http://127.0.0.1:1/"),
		"beta":  httpBackend("http://127.0.0.1:2/"),
	}
	if err := regen.Regenerate(dir, second, "http://gw"); err != nil {
		t.Fatalf("second Regenerate: %v", err)
	}

	backup := filepath.Join(dir, MCPJSONBackupFileName)
	backupBody := readFileT(t, backup)
	if string(backupBody) != string(priorBody) {
		t.Fatalf("backup does not match prior content.\nprior=%q\nbackup=%q",
			string(priorBody), string(backupBody))
	}

	// And the target has the new content.
	newBody := readFileT(t, target)
	newDoc := parseMCPJSON(t, newBody)
	srv := mcpServersFromDoc(t, newDoc)
	if _, ok := srv["beta"]; !ok {
		t.Fatalf("expected beta in new output, got keys=%v", keysOf(srv))
	}
}

// TestRegen_JSONValid asserts that the regenerated content is valid
// JSON and carries the expected structural invariants: banner key, one
// http-entry per enabled backend, Authorization header placeholder.
func TestRegen_JSONValid(t *testing.T) {
	dir := t.TempDir()
	regen := NewRegenerator()

	servers := map[string]*models.ServerConfig{
		"gamma": httpBackend("http://127.0.0.1:1/"),
	}
	if err := regen.Regenerate(dir, servers, "http://gw"); err != nil {
		t.Fatalf("Regenerate: %v", err)
	}

	body := readFileT(t, filepath.Join(dir, MCPJSONFileName))
	doc := parseMCPJSON(t, body)

	if got := doc[GeneratedBannerKey]; got != GeneratedBannerValue {
		t.Fatalf("banner mismatch: got %v, want %v", got, GeneratedBannerValue)
	}
	srv := mcpServersFromDoc(t, doc)
	entry, ok := srv["gamma"].(map[string]any)
	if !ok {
		t.Fatalf("gamma entry missing or wrong type: %#v", srv["gamma"])
	}
	if entry["type"] != "http" {
		t.Fatalf("expected type=http, got %v", entry["type"])
	}
	if entry["url"] != "http://gw/mcp/gamma" {
		t.Fatalf("expected url=http://gw/mcp/gamma, got %v", entry["url"])
	}
	headers, ok := entry["headers"].(map[string]any)
	if !ok {
		t.Fatalf("headers missing or wrong type: %#v", entry["headers"])
	}
	wantAuth := "Bearer " + AuthTokenPlaceholder
	if headers["Authorization"] != wantAuth {
		t.Fatalf("expected Authorization=%q, got %v", wantAuth, headers["Authorization"])
	}
}

// TestRegen_DisabledBackendExcluded asserts Disabled=true backends do
// not appear in the generated output, but enabled ones still do.
func TestRegen_DisabledBackendExcluded(t *testing.T) {
	dir := t.TempDir()
	regen := NewRegenerator()

	servers := map[string]*models.ServerConfig{
		"enabled1":  httpBackend("http://127.0.0.1:1/"),
		"disabled1": disabledHTTPBackend("http://127.0.0.1:2/"),
		"enabled2":  httpBackend("http://127.0.0.1:3/"),
	}
	if err := regen.Regenerate(dir, servers, "http://gw"); err != nil {
		t.Fatalf("Regenerate: %v", err)
	}

	body := readFileT(t, filepath.Join(dir, MCPJSONFileName))
	srv := mcpServersFromDoc(t, parseMCPJSON(t, body))

	if _, ok := srv["enabled1"]; !ok {
		t.Errorf("expected enabled1 in output")
	}
	if _, ok := srv["enabled2"]; !ok {
		t.Errorf("expected enabled2 in output")
	}
	if _, ok := srv["disabled1"]; ok {
		t.Errorf("disabled1 must not appear in output, got keys=%v", keysOf(srv))
	}
}

// TestRegen_EmptyDirRejected asserts an empty pluginDir is rejected
// rather than writing to the process CWD.
func TestRegen_EmptyDirRejected(t *testing.T) {
	regen := NewRegenerator()
	err := regen.Regenerate("", nil, "http://gw")
	if err == nil {
		t.Fatalf("expected error for empty plugin dir, got nil")
	}
	if !strings.Contains(err.Error(), "plugin directory is empty") {
		t.Fatalf("unexpected error text: %v", err)
	}
}

// TestRegen_DefaultPlaceholderWhenURLEmpty asserts that passing an empty
// gatewayURL yields the ${user_config.gateway_url} placeholder in the
// output — required so the checked-in plugin stub is portable.
func TestRegen_DefaultPlaceholderWhenURLEmpty(t *testing.T) {
	dir := t.TempDir()
	regen := NewRegenerator()
	servers := map[string]*models.ServerConfig{
		"alpha": httpBackend("http://127.0.0.1:1/"),
	}
	if err := regen.Regenerate(dir, servers, ""); err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	body := readFileT(t, filepath.Join(dir, MCPJSONFileName))
	want := DefaultGatewayURLPlaceholder + "/mcp/alpha"
	if !strings.Contains(string(body), want) {
		t.Fatalf("expected placeholder URL %q in output, got:\n%s", want, string(body))
	}
}

// TestRegen_Concurrent asserts the internal mutex serializes concurrent
// Regenerate calls: ten goroutines writing the same target directory
// with the same inputs must never leave stray tmp files and must
// produce a readable, valid final file.
func TestRegen_Concurrent(t *testing.T) {
	dir := t.TempDir()
	regen := NewRegenerator()

	const n = 10
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			servers := map[string]*models.ServerConfig{
				fmt.Sprintf("s%d", i): httpBackend("http://127.0.0.1:1/"),
			}
			if err := regen.Regenerate(dir, servers, "http://gw"); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Regenerate error: %v", err)
	}

	body := readFileT(t, filepath.Join(dir, MCPJSONFileName))
	// Final content must at minimum parse as JSON.
	parseMCPJSON(t, body)

	// No stray tmp files.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), MCPJSONFileName+".tmp.") {
			t.Fatalf("stray tmp file after concurrent regen: %s", e.Name())
		}
	}
}

// --- Discover tests ---

// TestDiscover_EnvVarPriority asserts that $GATEWAY_PLUGIN_DIR takes
// priority over any home-dir fallback.
func TestDiscover_EnvVarPriority(t *testing.T) {
	envDir := t.TempDir()
	t.Setenv(PluginDirEnvVar, envDir)

	got, err := Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got != envDir {
		t.Fatalf("expected %q, got %q", envDir, got)
	}
}

// TestDiscover_EnvVarMissingDir asserts that an env var pointing at a
// non-existent path returns a descriptive error (never silently falls
// through to the glob fallback — the operator asked for this path).
func TestDiscover_EnvVarMissingDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	t.Setenv(PluginDirEnvVar, missing)

	_, err := Discover()
	if err == nil {
		t.Fatalf("expected error for missing env dir, got nil")
	}
	if !strings.Contains(err.Error(), PluginDirEnvVar) {
		t.Fatalf("expected error to mention %s, got: %v", PluginDirEnvVar, err)
	}
}

// TestDiscover_EnvVarIsFile asserts that if $GATEWAY_PLUGIN_DIR points
// at a file (not a directory), Discover rejects it.
func TestDiscover_EnvVarIsFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	t.Setenv(PluginDirEnvVar, f)

	_, err := Discover()
	if err == nil {
		t.Fatalf("expected error for env dir pointing to file, got nil")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestDiscover_GlobFallback asserts that when the env var is unset and
// the ~/.claude/plugins/cache/mcp-gateway@* glob matches, the lex-max
// result wins. We fake HOME via t.Setenv; works on Unix via $HOME and
// on Windows via $USERPROFILE (os.UserHomeDir consults both).
func TestDiscover_GlobFallback(t *testing.T) {
	home := t.TempDir()
	setHomeDir(t, home)
	t.Setenv(PluginDirEnvVar, "") // ensure env override is off

	cacheDir := filepath.Join(home, ".claude", "plugins", "cache")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	// Two candidate directories + one stray file that must be ignored.
	for _, name := range []string{"mcp-gateway@alpha", "mcp-gateway@zeta"} {
		if err := os.MkdirAll(filepath.Join(cacheDir, name), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "mcp-gateway@stray"), []byte("x"), 0o600); err != nil {
		t.Fatalf("stray file: %v", err)
	}

	got, err := Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	want := filepath.Join(cacheDir, "mcp-gateway@zeta")
	if got != want {
		t.Fatalf("expected %q (lex-max), got %q", want, got)
	}
}

// TestDiscover_GlobNoMatch asserts that when neither env nor glob
// matches, ErrPluginDirNotFound is returned (and errors.Is detects it).
func TestDiscover_GlobNoMatch(t *testing.T) {
	home := t.TempDir() // empty tmp — no .claude/plugins subtree at all
	setHomeDir(t, home)
	t.Setenv(PluginDirEnvVar, "")

	_, err := Discover()
	if !errors.Is(err, ErrPluginDirNotFound) {
		t.Fatalf("expected ErrPluginDirNotFound, got %v", err)
	}
}

// TestDiscover_GlobOnlyStrayFile asserts that if the cache dir exists
// but contains only non-directory matches (files, symlinks to files,
// etc.), Discover returns ErrPluginDirNotFound rather than a file path.
func TestDiscover_GlobOnlyStrayFile(t *testing.T) {
	home := t.TempDir()
	setHomeDir(t, home)
	t.Setenv(PluginDirEnvVar, "")

	cacheDir := filepath.Join(home, ".claude", "plugins", "cache")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "mcp-gateway@file"), []byte("x"), 0o600); err != nil {
		t.Fatalf("stray file: %v", err)
	}

	_, err := Discover()
	if !errors.Is(err, ErrPluginDirNotFound) {
		t.Fatalf("expected ErrPluginDirNotFound, got %v", err)
	}
}

// setHomeDir overrides the OS's idea of the user home dir for the
// current test. On POSIX, HOME is authoritative; on Windows,
// os.UserHomeDir reads USERPROFILE.
func setHomeDir(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", dir)
	} else {
		t.Setenv("HOME", dir)
	}
}
