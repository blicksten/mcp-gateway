package claudeconfig

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolvePath_CCGlobal(t *testing.T) {
	got, err := ResolvePath(SourceCCGlobal, "")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.HasSuffix(got, ".claude.json") {
		t.Errorf("path %q does not end with .claude.json", got)
	}
}

func TestResolvePath_CCProject_RequiresRoot(t *testing.T) {
	_, err := ResolvePath(SourceCCProject, "")
	if !errors.Is(err, ErrEmptyProjectRoot) {
		t.Errorf("got %v, want ErrEmptyProjectRoot", err)
	}
}

func TestResolvePath_CCProject_JoinsWorkspace(t *testing.T) {
	got, err := ResolvePath(SourceCCProject, "/tmp/proj")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	want := filepath.Join("/tmp/proj", ".mcp.json")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolvePath_Desktop_OSAware(t *testing.T) {
	got, err := ResolvePath(SourceDesktop, "")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.HasSuffix(got, "claude_desktop_config.json") {
		t.Errorf("desktop path %q missing claude_desktop_config.json", got)
	}
	switch runtime.GOOS {
	case "windows":
		// Path should contain "Claude" subdir.
		if !strings.Contains(got, "Claude") {
			t.Errorf("windows desktop path %q missing Claude subdir", got)
		}
	case "darwin":
		if !strings.Contains(got, "Library/Application Support/Claude") {
			t.Errorf("darwin desktop path %q wrong shape", got)
		}
	default:
		if !strings.Contains(got, "Claude") {
			t.Errorf("unix desktop path %q missing Claude subdir", got)
		}
	}
}

func TestResolvePath_UnknownSource(t *testing.T) {
	_, err := ResolvePath(Source("unknown-thing"), "")
	if !errors.Is(err, ErrUnknownSource) {
		t.Errorf("got %v, want ErrUnknownSource", err)
	}
}

func TestRead_NonExistent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	snap, err := Read(SourceCCGlobal, "")
	if err != nil {
		t.Fatalf("Read returned err: %v", err)
	}
	if snap.Exists {
		t.Errorf("snap.Exists = true; want false")
	}
	if len(snap.Entries) != 0 {
		t.Errorf("expected no entries on missing file")
	}
}

func TestRead_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(""), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	snap, err := Read(SourceCCGlobal, "")
	if err != nil {
		t.Fatalf("Read returned err: %v", err)
	}
	if !snap.Exists {
		t.Errorf("snap.Exists = false")
	}
	if len(snap.Entries) != 0 {
		t.Errorf("expected no entries on empty file")
	}
}

func TestRead_NoMcpServersKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"),
		[]byte(`{"verbose":false,"projects":{}}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	snap, err := Read(SourceCCGlobal, "")
	if err != nil {
		t.Fatalf("Read returned err: %v", err)
	}
	if !snap.Exists {
		t.Errorf("snap.Exists = false")
	}
	if len(snap.Entries) != 0 {
		t.Errorf("expected no entries when mcpServers absent")
	}
}

func TestRead_ParsesMcpServersEntries(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	body := `{
  "verbose": false,
  "mcpServers": {
    "stdio-server": {
      "type": "stdio",
      "command": "uvx",
      "args": ["pal-mcp"],
      "env": {"PAL_HOME": "/opt/pal"}
    },
    "http-server": {
      "type": "http",
      "url": "http://localhost:7080/mcp/proxy",
      "headers": {"Authorization": "Bearer xyz"}
    }
  },
  "projects": {}
}`
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	snap, err := Read(SourceCCGlobal, "")
	if err != nil {
		t.Fatalf("Read returned err: %v", err)
	}
	if !snap.Exists {
		t.Errorf("snap.Exists = false")
	}
	if got := len(snap.Entries); got != 2 {
		t.Errorf("entries count = %d, want 2", got)
	}
	stdio := snap.Entries["stdio-server"]
	if stdio.Type != "stdio" || stdio.Command != "uvx" || len(stdio.Args) != 1 || stdio.Args[0] != "pal-mcp" {
		t.Errorf("stdio-server convenience fields wrong: %+v", stdio)
	}
	if stdio.Env["PAL_HOME"] != "/opt/pal" {
		t.Errorf("stdio-server env wrong: %+v", stdio.Env)
	}
	http := snap.Entries["http-server"]
	if http.Type != "http" || http.URL != "http://localhost:7080/mcp/proxy" {
		t.Errorf("http-server convenience fields wrong: %+v", http)
	}
}

func TestRead_MalformedJSON_TypedError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"),
		[]byte(`{ this is not valid json `), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Read(SourceCCGlobal, "")
	if err == nil {
		t.Fatalf("expected error on malformed JSON")
	}
	if !strings.Contains(err.Error(), "claudeconfig:") {
		t.Errorf("error message %q lacks 'claudeconfig:' prefix", err)
	}
}

func TestRead_CCProject_RoutesViaWorkspace(t *testing.T) {
	proj := t.TempDir()
	body := `{"mcpServers":{"x":{"type":"stdio","command":"x"}}}`
	if err := os.WriteFile(filepath.Join(proj, ".mcp.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	snap, err := Read(SourceCCProject, proj)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !snap.Exists {
		t.Errorf("snap.Exists = false")
	}
	if _, ok := snap.Entries["x"]; !ok {
		t.Errorf("expected entry 'x'")
	}
}

func TestRead_PreservesRawBytes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	body := `{"mcpServers":{"a":{"type":"stdio","custom_unknown_field":42}}}`
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	snap, err := Read(SourceCCGlobal, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(snap.Raw) != body {
		t.Errorf("snap.Raw mismatch")
	}
	// Even unrecognised fields are preserved in EntryRaw.Raw.
	entry := snap.Entries["a"]
	if !strings.Contains(string(entry.Raw), "custom_unknown_field") {
		t.Errorf("entry.Raw lost custom_unknown_field: %s", entry.Raw)
	}
}

func TestRead_MTimeCAS_TolerancesStableFile(t *testing.T) {
	// File never changes during read — should never trigger CAS retry.
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"),
		[]byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	for i := range 5 {
		_, err := Read(SourceCCGlobal, "")
		if err != nil {
			t.Fatalf("iter %d: Read err: %v", i, err)
		}
	}
}
