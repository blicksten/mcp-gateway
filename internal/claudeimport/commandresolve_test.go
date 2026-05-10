package claudeimport

import (
	"runtime"
	"testing"
)

func TestResolveCommand_Empty(t *testing.T) {
	got := ResolveCommand("")
	if got.Resolved {
		t.Errorf("empty command should not resolve")
	}
}

func TestResolveCommand_GoBinary(t *testing.T) {
	// `go` is in PATH for any environment that builds this package.
	got := ResolveCommand("go")
	if !got.Resolved {
		t.Skipf("go not on PATH; skipping")
	}
	if got.Name != "go" {
		t.Errorf("Name = %q, want %q", got.Name, "go")
	}
	if got.AbsPath == "" {
		t.Errorf("AbsPath empty for resolved command")
	}
}

func TestResolveCommand_Unresolvable(t *testing.T) {
	got := ResolveCommand("definitely-not-a-command-xyz-12345")
	if got.Resolved {
		t.Errorf("resolved unknown command: %+v", got)
	}
	if got.Name != "definitely-not-a-command-xyz-12345" {
		t.Errorf("Name should fall back to base, got %q", got.Name)
	}
}

func TestBaseName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"npx", "npx"},
		{"/usr/local/bin/uvx", "uvx"},
		{"/usr/bin/node", "node"},
		{"C:\\Program Files\\nodejs\\node.exe", "node"},
		{"NODE.EXE", maybeStripExe("node")},
	}
	for _, c := range cases {
		got := baseName(c.in)
		want := c.want
		if runtime.GOOS != "windows" && c.in == "NODE.EXE" {
			// On non-Windows we don't strip ".exe" — name stays "node.exe" lowercased.
			want = "node.exe"
		}
		if got != want {
			t.Errorf("baseName(%q) = %q, want %q", c.in, got, want)
		}
	}
}

func maybeStripExe(s string) string { return s }
