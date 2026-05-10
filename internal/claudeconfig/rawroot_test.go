package claudeconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestRawRoot_RoundTripPreservesNonMcpServersBytes asserts the central
// invariant: replacing mcpServers with a new value leaves every byte
// outside that range unchanged. This is the property iancoleman/orderedmap
// would NOT preserve (it re-encodes the document).
func TestRawRoot_RoundTripPreservesNonMcpServersBytes(t *testing.T) {
	src := []byte(`{
  "numStartups": 23,
  "verbose": false,
  "mcpServers": {
    "old-server": {
      "type": "stdio",
      "command": "old-cmd"
    }
  },
  "projects": {
    "/some/path": {"history": []}
  },
  "oauthAccount": {"emailAddress": "user@example.com"},
  "customApiKeyResponses": {"approved": []}
}`)

	rr, err := ParseRawRoot(src)
	if err != nil {
		t.Fatalf("ParseRawRoot: %v", err)
	}
	if !rr.HasMcpServers() {
		t.Fatal("HasMcpServers = false; want true")
	}

	newServers := []byte(`{"new-server": {"type": "http", "url": "http://localhost:7080/mcp/foo"}}`)
	out, err := rr.ReplaceMcpServers(newServers)
	if err != nil {
		t.Fatalf("ReplaceMcpServers: %v", err)
	}

	// Verify result is still valid JSON.
	if !json.Valid(out) {
		t.Fatalf("output not valid JSON:\n%s", out)
	}

	// Verify non-mcpServers byte ranges are byte-identical.
	// Find "mcpServers" in src and compute the prefix that should match.
	srcStr := string(src)
	idx := strings.Index(srcStr, `"mcpServers"`)
	if idx < 0 {
		t.Fatal("test fixture missing mcpServers")
	}
	if string(out[:idx]) != srcStr[:idx] {
		t.Errorf("prefix bytes diverged:\n want: %q\n got:  %q", srcStr[:idx], string(out[:idx]))
	}

	// Verify the projects/oauthAccount/customApiKeyResponses keys are
	// preserved verbatim by checking exact substrings.
	for _, want := range []string{
		`"projects"`,
		`"oauthAccount"`,
		`"customApiKeyResponses"`,
		`"emailAddress": "user@example.com"`,
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("output missing preserved substring %q", want)
		}
	}

	// Verify the new server is present.
	if !bytes.Contains(out, []byte(`"new-server"`)) {
		t.Errorf("output missing new server")
	}
	// Verify the old server is gone.
	if bytes.Contains(out, []byte(`"old-server"`)) {
		t.Errorf("output still contains old server")
	}
}

// TestRawRoot_PreservesIndentationVerbatim asserts whitespace inside the
// non-mcpServers byte ranges is preserved exactly. This is the property
// that distinguishes raw-bytes splice from any decode-then-encode strategy.
func TestRawRoot_PreservesIndentationVerbatim(t *testing.T) {
	src := []byte("{\r\n\t\"verbose\": true,\r\n\t\"mcpServers\": {},\r\n\t\"projects\": {}\r\n}\r\n")

	rr, err := ParseRawRoot(src)
	if err != nil {
		t.Fatalf("ParseRawRoot: %v", err)
	}
	out, err := rr.ReplaceMcpServers([]byte(`{"a": {"type":"stdio","command":"x"}}`))
	if err != nil {
		t.Fatalf("ReplaceMcpServers: %v", err)
	}

	// Whitespace before/after mcpServers and around the other keys
	// must be byte-identical.
	for _, want := range []string{
		"{\r\n\t\"verbose\": true,\r\n\t\"mcpServers\":",
		",\r\n\t\"projects\": {}\r\n}\r\n",
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("output missing preserved whitespace %q", want)
		}
	}
}

func TestRawRoot_DuplicateMcpServersRejected(t *testing.T) {
	src := []byte(`{"mcpServers": {"a": {}}, "x": 1, "mcpServers": {"b": {}}}`)
	_, err := ParseRawRoot(src)
	if !errors.Is(err, ErrDuplicateKey) {
		t.Errorf("got %v, want ErrDuplicateKey", err)
	}
}

func TestRawRoot_NonObjectRoot(t *testing.T) {
	for _, src := range [][]byte{
		[]byte(`[]`),
		[]byte(`"string"`),
		[]byte(`42`),
		[]byte(`null`),
	} {
		_, err := ParseRawRoot(src)
		if !errors.Is(err, ErrNotObject) {
			t.Errorf("ParseRawRoot(%q): got %v, want ErrNotObject", src, err)
		}
	}
}

func TestRawRoot_AbsentKey_InsertWithComma(t *testing.T) {
	src := []byte(`{"verbose":true,"projects":{}}`)
	rr, err := ParseRawRoot(src)
	if err != nil {
		t.Fatalf("ParseRawRoot: %v", err)
	}
	if rr.HasMcpServers() {
		t.Fatal("HasMcpServers = true; want false")
	}
	out, err := rr.ReplaceMcpServers([]byte(`{}`))
	if err != nil {
		t.Fatalf("ReplaceMcpServers: %v", err)
	}
	if !json.Valid(out) {
		t.Fatalf("invalid JSON: %s", out)
	}
	if !bytes.Contains(out, []byte(`"mcpServers": {}`)) {
		t.Errorf("missing inserted mcpServers; got %s", out)
	}
}

func TestRawRoot_AbsentKey_EmptyRoot(t *testing.T) {
	src := []byte(`{}`)
	rr, err := ParseRawRoot(src)
	if err != nil {
		t.Fatalf("ParseRawRoot: %v", err)
	}
	out, err := rr.ReplaceMcpServers([]byte(`{"a":{}}`))
	if err != nil {
		t.Fatalf("ReplaceMcpServers: %v", err)
	}
	if !json.Valid(out) {
		t.Fatalf("invalid JSON: %s", out)
	}
	if !bytes.Contains(out, []byte(`"mcpServers"`)) {
		t.Errorf("output missing mcpServers: %s", out)
	}
}

func TestRawRoot_McpServersBytes_Aliases(t *testing.T) {
	src := []byte(`{"mcpServers": {"a": {"type":"stdio"}}, "x": 1}`)
	rr, err := ParseRawRoot(src)
	if err != nil {
		t.Fatalf("ParseRawRoot: %v", err)
	}
	got := rr.McpServersBytes()
	want := `{"a": {"type":"stdio"}}`
	if string(got) != want {
		t.Errorf("McpServersBytes = %q, want %q", got, want)
	}
}

func TestRawRoot_NestedBracesInsideStrings(t *testing.T) {
	// Pathological string values containing unbalanced braces and
	// escaped quotes — must not confuse the scanner.
	src := []byte(`{
  "weird": "value with } brace } and \" escaped quote",
  "mcpServers": {"a": {}},
  "after": "value with { brace { open"
}`)
	rr, err := ParseRawRoot(src)
	if err != nil {
		t.Fatalf("ParseRawRoot: %v", err)
	}
	out, err := rr.ReplaceMcpServers([]byte(`{"new":{}}`))
	if err != nil {
		t.Fatalf("ReplaceMcpServers: %v", err)
	}
	if !bytes.Contains(out, []byte(`"weird": "value with } brace } and \" escaped quote"`)) {
		t.Errorf("output corrupted weird key: %s", out)
	}
	if !bytes.Contains(out, []byte(`"after": "value with { brace { open"`)) {
		t.Errorf("output corrupted after key: %s", out)
	}
}

func TestRawRoot_ScalarTypes(t *testing.T) {
	// Array, number, boolean, null at top level — scanner must skip
	// them correctly to find mcpServers in the middle.
	src := []byte(`{"a":[1,2,3],"b":true,"c":null,"mcpServers":{"x":{}},"d":-3.14e2,"e":false}`)
	rr, err := ParseRawRoot(src)
	if err != nil {
		t.Fatalf("ParseRawRoot: %v", err)
	}
	if string(rr.McpServersBytes()) != `{"x":{}}` {
		t.Errorf("McpServersBytes = %q", rr.McpServersBytes())
	}
}

func TestRawRoot_ReplaceWithInvalidJSON_Rejected(t *testing.T) {
	src := []byte(`{"mcpServers":{}}`)
	rr, err := ParseRawRoot(src)
	if err != nil {
		t.Fatalf("ParseRawRoot: %v", err)
	}
	_, err = rr.ReplaceMcpServers([]byte(`{ not valid }`))
	if err == nil {
		t.Errorf("expected error on invalid newValue, got nil")
	}
}

func TestRawRoot_LargeRealisticFixture(t *testing.T) {
	// Exercises the byte-identical preservation invariant on a
	// realistic ~/.claude.json shape: many top-level keys, deeply
	// nested projects map, a mid-position mcpServers entry. Every
	// byte outside the mcpServers range MUST be preserved.
	src := []byte(`{
  "numStartups": 47,
  "verbose": false,
  "autoUpdaterStatus": "enabled",
  "userID": "abc123",
  "mcpServers": {
    "old-pal": {
      "type": "stdio",
      "command": "old-pal-cmd",
      "args": ["--flag"]
    }
  },
  "projects": {
    "/path/one": {
      "lastUsedAt": "2026-01-01T00:00:00Z",
      "history": [{"role":"user","content":"hi"}],
      "allowedTools": []
    },
    "/path/two": {
      "lastUsedAt": "2026-02-01T00:00:00Z",
      "history": []
    }
  },
  "oauthAccount": {
    "emailAddress": "user@example.com",
    "organizationName": "Example Inc"
  },
  "cachedGrowthBookFeatures": {
    "feature_a": {"enabled": true},
    "feature_b": {"variant": "control"}
  }
}`)
	idx := bytes.Index(src, []byte(`"mcpServers"`))
	if idx < 0 {
		t.Fatal("fixture missing mcpServers")
	}
	prefix := append([]byte(nil), src[:idx]...)

	rr, err := ParseRawRoot(src)
	if err != nil {
		t.Fatalf("ParseRawRoot: %v", err)
	}

	newServers := []byte(`{"new-pal":{"type":"http","url":"http://localhost:7080/mcp/pal"}}`)
	out, err := rr.ReplaceMcpServers(newServers)
	if err != nil {
		t.Fatalf("ReplaceMcpServers: %v", err)
	}
	if !bytes.HasPrefix(out, prefix) {
		t.Errorf("prefix not preserved byte-identically")
	}
	// Locate the byte after the new mcpServers value and check the
	// suffix is preserved verbatim.
	suffixWant := src[rr.mcpServersEnd:]
	suffixGot := out[idx+len(`"mcpServers": `)+len(newServers):]
	if !bytes.Equal(suffixWant, suffixGot) {
		t.Errorf("suffix bytes diverged\n want: %q\n got:  %q", suffixWant, suffixGot)
	}
}
