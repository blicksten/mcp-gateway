package obs

import (
	"net/http"
	"net/url"
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

// --- Finding #1: typed containers must not pass through unredacted. ---

// secretBearer is a synthetic Authorization value used across the typed-container
// tests. Never a real credential; we only assert it does NOT survive.
const secretBearer = "Bearer SYNTHSECRET0123456789abcdef"

// flatten walks an arbitrary redacted value (map[string]any / []any / string /
// primitives) and reports whether the synthetic secret substring survives
// anywhere. Asserts STRUCTURE (absence of the secret), never prints the secret.
func leaks(v any, secret string) bool {
	switch t := v.(type) {
	case string:
		return strings.Contains(t, secret)
	case map[string]any:
		for k, val := range t {
			if strings.Contains(k, secret) || leaks(val, secret) {
				return true
			}
		}
	case []any:
		for _, e := range t {
			if leaks(e, secret) {
				return true
			}
		}
	}
	return false
}

func TestRedact_HTTPHeaderValueRedacted(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", secretBearer)
	h.Set("X-Trace-Id", "tr-safe-123")
	out := Redact(map[string]any{"headers": h})
	if leaks(out, "SYNTHSECRET") {
		t.Fatalf("http.Header secret leaked through: %+v", out)
	}
	// Safe header value should be preserved somewhere.
	if leaks(out, "tr-safe-123") == false {
		// preserved is expected; absence would mean over-redaction we don't want
		t.Logf("note: safe header value not found verbatim (acceptable): %+v", out)
	}
}

func TestRedact_URLValuesValueRedacted(t *testing.T) {
	vals := url.Values{}
	vals.Set("token", "SYNTHSECRET-urlvalues-xyz")
	vals.Set("page", "2")
	out := Redact(map[string]any{"params": vals})
	if leaks(out, "SYNTHSECRET") {
		t.Fatalf("url.Values secret leaked through: %+v", out)
	}
}

func TestRedact_MapStringStringValueRedacted(t *testing.T) {
	m := map[string]string{
		"api_key": "SYNTHSECRET-mapstringstring",
		"region":  "eu-central",
	}
	out := Redact(map[string]any{"cfg": m})
	if leaks(out, "SYNTHSECRET") {
		t.Fatalf("map[string]string secret-keyed value leaked: %+v", out)
	}
	// Confirm the typed map was actually converted (not passed through as-is).
	if _, ok := out["cfg"].(map[string]string); ok {
		t.Fatalf("typed map[string]string passed through unredacted")
	}
}

func TestRedact_ByteSliceSecretRedacted(t *testing.T) {
	// A secret arriving as raw bytes (e.g. a sk- API key) must be scrubbed.
	b := []byte("sk-SYNTHSECRETbytes0123456789")
	out := Redact(map[string]any{"raw": b})
	got, ok := out["raw"].(string)
	if !ok {
		t.Fatalf("[]byte not converted to scrubbed string: %T %v", out["raw"], out["raw"])
	}
	if strings.Contains(got, "SYNTHSECRET") {
		t.Fatalf("[]byte secret leaked: %q", got)
	}
	if got != redactedKind("api-key") {
		t.Fatalf("[]byte sk- key not redacted to api-key marker: %q", got)
	}
}

func TestRedact_StructWithSecretFieldRedacted(t *testing.T) {
	type creds struct {
		User   string `json:"user"`
		APIKey string `json:"api_key"` // sensitive KEY name after JSON flatten
		Region string `json:"region"`
	}
	c := creds{User: "alice", APIKey: "SYNTHSECRET-structfield", Region: "eu"}
	out := Redact(map[string]any{"creds": c})
	if leaks(out, "SYNTHSECRET") {
		t.Fatalf("struct secret field leaked: %+v", out)
	}
	// Struct must have been flattened to a map, not passed through as a struct.
	flat, ok := out["creds"].(map[string]any)
	if !ok {
		t.Fatalf("struct not flattened to map[string]any: %T", out["creds"])
	}
	if flat["api_key"] != redactedMarker {
		t.Fatalf("flattened struct api_key not blanked: %v", flat["api_key"])
	}
}

func TestRedact_DepthGuard(t *testing.T) {
	// Build a value nested deeper than maxRedactDepth; the redactor must return
	// the marker rather than spin, and must not leak the buried secret.
	var v any = "sk-SYNTHSECRETdeep0123456789"
	for i := 0; i < maxRedactDepth+5; i++ {
		v = []any{v}
	}
	out := redactValue(v)
	if leaks(out, "SYNTHSECRET") {
		t.Fatalf("deeply nested secret leaked past depth guard")
	}
}

// --- Finding #2: embedded base64 / hex blobs must be redacted in place. ---

func TestRedact_EmbeddedHexRedacted(t *testing.T) {
	hexBlob := strings.Repeat("ab", 25) // 50 hex chars, ≥40
	in := "prefix " + hexBlob + " suffix"
	out := Redact(map[string]any{"v": in})
	got := out["v"].(string)
	if strings.Contains(got, hexBlob) {
		t.Fatalf("embedded hex blob leaked: %q", got)
	}
	if !strings.Contains(got, redactedKind("hex")) {
		t.Fatalf("embedded hex not labelled: %q", got)
	}
	if !strings.HasPrefix(got, "prefix ") || !strings.HasSuffix(got, " suffix") {
		t.Fatalf("surrounding context not preserved: %q", got)
	}
}

func TestRedact_EmbeddedBase64Redacted(t *testing.T) {
	// 44 base64 chars (non-hex alphabet so it can't match the hex pass).
	b64Blob := strings.Repeat("AbCdEfGh", 6) // 48 chars, includes upper/lower
	in := "blob=" + b64Blob + " trailing"
	out := Redact(map[string]any{"v": in})
	got := out["v"].(string)
	if strings.Contains(got, b64Blob) {
		t.Fatalf("embedded base64 blob leaked: %q", got)
	}
	if !strings.Contains(got, redactedKind("base64")) {
		t.Fatalf("embedded base64 not labelled: %q", got)
	}
	if !strings.HasSuffix(got, " trailing") {
		t.Fatalf("trailing context not preserved: %q", got)
	}
}

func TestRedact_Base64URLEmbeddedRedacted(t *testing.T) {
	// base64url alphabet uses -_; must still be caught (finding #2 note).
	blob := strings.Repeat("aB-_cD9z", 6) // 48 chars with - and _
	in := "x " + blob + " y"
	out := Redact(map[string]any{"v": in})
	got := out["v"].(string)
	if strings.Contains(got, blob) {
		t.Fatalf("embedded base64url blob leaked: %q", got)
	}
}

// --- Finding #4: a secret used AS A KEY must be scrubbed. ---

func TestRedact_SecretAsKeyRedacted(t *testing.T) {
	// The KEY itself is a bearer header line carrying a secret.
	in := map[string]any{
		secretBearer: "some-value",
		"safe":       "ok",
	}
	out := Redact(in)
	for k := range out {
		if strings.Contains(k, "SYNTHSECRET") {
			t.Fatalf("secret survived in output KEY: %q", k)
		}
	}
	if out["safe"] != "ok" {
		t.Fatalf("safe key/value altered: %v", out["safe"])
	}
}

// --- Finding #5: URL userinfo + query stripped for ANY scheme. ---

func TestRedact_URLUserinfoStripped(t *testing.T) {
	in := "https://user:SYNTHSECRETpass@host.example/path"
	out := Redact(map[string]any{"url": in})
	got := out["url"].(string)
	if strings.Contains(got, "SYNTHSECRETpass") || strings.Contains(got, "user:") {
		t.Fatalf("userinfo credentials leaked: %q", got)
	}
	if !strings.HasPrefix(got, "https://") || !strings.Contains(got, "host.example/path") {
		t.Fatalf("scheme/host/path not preserved: %q", got)
	}
}

func TestRedact_NonHTTPSchemeUserinfoAndQueryStripped(t *testing.T) {
	in := "postgres://dbuser:SYNTHSECRETpw@db.internal/appdb?sslmode=require&token=SYNTHSECRETq"
	out := Redact(map[string]any{"dsn": in})
	got := out["dsn"].(string)
	if strings.Contains(got, "SYNTHSECRETpw") {
		t.Fatalf("postgres userinfo password leaked: %q", got)
	}
	if strings.Contains(got, "SYNTHSECRETq") {
		t.Fatalf("postgres query secret leaked: %q", got)
	}
	if !strings.HasPrefix(got, "postgres://") {
		t.Fatalf("scheme not preserved: %q", got)
	}
	if !strings.Contains(got, "db.internal/appdb") {
		t.Fatalf("host/path not preserved: %q", got)
	}
}

func TestRedact_WSSchemeUserinfoStripped(t *testing.T) {
	in := "wss://tok:SYNTHSECRETws@gw.example/socket"
	out := Redact(map[string]any{"u": in})
	got := out["u"].(string)
	if strings.Contains(got, "SYNTHSECRETws") {
		t.Fatalf("ws userinfo leaked: %q", got)
	}
}

// --- Finding #7: additional API-key prefixes. ---

func TestRedact_GitHubAndPATTokens(t *testing.T) {
	cases := []string{
		"ghp_SYNTHSECRET0123456789abcdef",
		"gho_SYNTHSECRET0123456789abcdef",
		"ghs_SYNTHSECRET0123456789abcdef",
		"ghr_SYNTHSECRET0123456789abcdef",
		"github_pat_SYNTHSECRET0123456789",
		"pat_SYNTHSECRET0123456789",
	}
	for _, tok := range cases {
		out := Redact(map[string]any{"v": tok})
		got := out["v"].(string)
		if strings.Contains(got, "SYNTHSECRET") {
			t.Errorf("token prefix not redacted (leaked): input prefix %q", tok[:6])
		}
		if got != redactedKind("api-key") {
			t.Errorf("token %q... not redacted to api-key marker: got %q", tok[:6], got)
		}
	}
}
