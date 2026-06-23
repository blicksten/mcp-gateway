package obs

import (
	"strings"
	"testing"
)

// containsAny reports whether s contains any of the needles. Used to assert a
// scrubbed value no longer holds a secret SUBSTRING (never the secret itself
// appears in a failure message — we assert structure, not the secret value).
func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

func TestRedact_KeyHeuristic(t *testing.T) {
	// Synthetic non-secrets shaped like values; the KEY alone triggers redaction.
	in := map[string]any{
		"sap_password": "PLAINTEXT_VALUE_A",
		"api_token":    "PLAINTEXT_VALUE_B",
		"auth_key":     "PLAINTEXT_VALUE_C",
		"my_secret":    "PLAINTEXT_VALUE_D",
		"cookie":       "PLAINTEXT_VALUE_E",
		"Authorization": "PLAINTEXT_VALUE_F",
		"step":         "compile", // safe key, must survive
	}
	out := Redact(in)

	for _, k := range []string{"sap_password", "api_token", "auth_key", "my_secret", "cookie", "Authorization"} {
		got, _ := out[k].(string)
		if got != redactedMarker {
			t.Errorf("key %q: got %q, want %q", k, got, redactedMarker)
		}
	}
	if out["step"] != "compile" {
		t.Errorf("safe key 'step' was altered: %v", out["step"])
	}
	// The original sentinel plaintext must not survive anywhere.
	for k, v := range out {
		if s, ok := v.(string); ok && strings.HasPrefix(s, "PLAINTEXT_VALUE_") && k != "step" {
			t.Errorf("plaintext leaked for key %q: %q", k, s)
		}
	}
}

func TestRedact_BearerToken(t *testing.T) {
	// Synthetic bearer token (not a real credential).
	out := Redact(map[string]any{"hdr": "Bearer abc123def456ghijklmnop"})
	got := out["hdr"].(string)
	if !strings.HasPrefix(got, "Bearer ") || strings.Contains(got, "abc123def456") {
		t.Fatalf("bearer not scrubbed: %q", got)
	}
}

func TestRedact_JWT(t *testing.T) {
	// Structurally-valid but meaningless JWT (synthetic segments).
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.AAAABBBBCCCCDDDD"
	out := Redact(map[string]any{"note": "token is " + jwt + " end"})
	got := out["note"].(string)
	if strings.Contains(got, "eyJzdWIi") || !strings.Contains(got, "«redacted:jwt»") {
		t.Fatalf("jwt not scrubbed: %q", got)
	}
	// Surrounding context preserved.
	if !strings.HasPrefix(got, "token is ") || !strings.HasSuffix(got, " end") {
		t.Fatalf("context not preserved: %q", got)
	}
}

func TestRedact_APIKeyPrefixes(t *testing.T) {
	cases := map[string]string{
		"sk-AAAAAAAAAAAAAAAAAAAA":   "api-key",
		"xoxb-1111-2222-abcdefghij": "slack-token",
	}
	for val, kind := range cases {
		out := Redact(map[string]any{"v": val})
		got := out["v"].(string)
		if got != redactedKind(kind) {
			t.Errorf("value %q: got %q, want %q", val, got, redactedKind(kind))
		}
	}
}

func TestRedact_KVParams(t *testing.T) {
	// Synthetic query/kv string with a secret param interleaved with safe ones.
	in := "user=alice&token=SECRETTOKENVALUE&page=2"
	out := Redact(map[string]any{"q": in})
	got := out["q"].(string)
	if strings.Contains(got, "SECRETTOKENVALUE") {
		t.Fatalf("kv token value leaked: %q", got)
	}
	if !containsAny(got, "user=alice") || !containsAny(got, "page=2") {
		t.Fatalf("safe kv params dropped: %q", got)
	}
	if !strings.Contains(got, "token="+redactedMarker) {
		t.Fatalf("token param not redacted: %q", got)
	}
}

func TestRedact_URLQueryStripped(t *testing.T) {
	in := "https://host.example/path?token=SECRETXYZ&id=7"
	out := Redact(map[string]any{"url": in})
	got := out["url"].(string)
	if strings.Contains(got, "SECRETXYZ") {
		t.Fatalf("url query secret leaked: %q", got)
	}
	if !strings.HasPrefix(got, "https://host.example/path?") {
		t.Fatalf("url scheme/host/path not preserved: %q", got)
	}
}

func TestRedact_LongBase64AndHex(t *testing.T) {
	b64 := strings.Repeat("A", 44) + "==" // ≥40, base64 shape
	hexv := strings.Repeat("a", 40)       // ≥40, hex shape
	out := Redact(map[string]any{"b": b64, "h": hexv})
	if out["b"] != redactedKind("base64") {
		t.Errorf("long base64 not scrubbed: %v", out["b"])
	}
	if out["h"] != redactedKind("hex") {
		t.Errorf("long hex not scrubbed: %v", out["h"])
	}
}

func TestRedact_ShortSafeValuesUntouched(t *testing.T) {
	in := map[string]any{
		"backend":  "vsp-PC1",
		"duration": 1234,
		"ok":       true,
		"tool":     "search_docs",
	}
	out := Redact(in)
	for k, v := range in {
		if out[k] != v {
			t.Errorf("safe value for %q altered: %v -> %v", k, v, out[k])
		}
	}
}

func TestRedact_NestedMapsAndSlices(t *testing.T) {
	in := map[string]any{
		"evidence": map[string]any{
			"bearer": "Bearer NESTEDSECRET12345678",
			"safe":   "fine",
		},
		"list": []any{"sk-NESTEDAPIKEYVALUEXXXX", "ok"},
	}
	out := Redact(in)
	nested := out["evidence"].(map[string]any)
	if strings.Contains(nested["bearer"].(string), "NESTEDSECRET") {
		t.Errorf("nested bearer leaked: %v", nested["bearer"])
	}
	if nested["safe"] != "fine" {
		t.Errorf("nested safe value altered: %v", nested["safe"])
	}
	list := out["list"].([]any)
	if list[0] != redactedKind("api-key") {
		t.Errorf("slice api-key not scrubbed: %v", list[0])
	}
	if list[1] != "ok" {
		t.Errorf("slice safe value altered: %v", list[1])
	}
}

func TestRedact_NilAndEmpty(t *testing.T) {
	if got := Redact(nil); got != nil {
		t.Errorf("Redact(nil) = %v, want nil", got)
	}
	empty := map[string]any{}
	if got := Redact(empty); len(got) != 0 {
		t.Errorf("Redact(empty) = %v, want empty", got)
	}
}
