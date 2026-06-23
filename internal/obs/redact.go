package obs

import (
	"regexp"
	"strings"
)

// Redaction (PLAN §C.3) — defense-in-depth so no secret VALUE ever reaches an
// event line. Two layers applied to every attrs payload before marshalling:
//
//  1. Field key heuristic: any value whose KEY ends in a sensitive suffix
//     (_password / _token / _key / _secret / cookie) is replaced wholesale,
//     regardless of the value's shape.
//  2. Value-pattern scrub: every string value (including those that passed
//     layer 1) is scanned for known secret SHAPES and the matching span (or
//     the whole value) is replaced with «redacted:<kind>».
//
// Replacement marker. Using a visible sentinel makes redaction auditable in
// the JSONL without leaking the original.
const redactedMarker = "«redacted»"

func redactedKind(kind string) string { return "«redacted:" + kind + "»" }

// sensitiveKeySuffixes — a value whose source key ends with one of these is
// redacted regardless of shape (PLAN §C.3 env-key heuristic). Lower-cased
// comparison; string suffix match, no regex.
var sensitiveKeySuffixes = []string{
	"_password", "_token", "_key", "_secret",
	"password", "token", "secret", "apikey", "api_key",
}

// sensitiveKeyExact — keys redacted by exact (lower-cased) match.
var sensitiveKeyExact = map[string]struct{}{
	"cookie":        {},
	"authorization": {},
	"auth":          {},
	"set-cookie":    {},
}

// keyIsSensitive reports whether a value should be redacted purely from its
// key name. String methods only.
func keyIsSensitive(key string) bool {
	k := strings.ToLower(key)
	if _, ok := sensitiveKeyExact[k]; ok {
		return true
	}
	for _, suf := range sensitiveKeySuffixes {
		if strings.HasSuffix(k, suf) {
			return true
		}
	}
	return false
}

// Regex is used ONLY for the secret SHAPES that string methods genuinely
// cannot express (length-classed character sets, base64url JWT triples). Each
// pattern carries a one-line justification per the regex-discipline rule.

// jwtRe matches a JWT: three base64url segments separated by dots, eyJ-prefixed
// header. Regex needed: dot-segmented alternation over a character class with
// length — not expressible with startsWith/contains alone.
var jwtRe = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}`)

// longBase64Re matches a standalone long base64 blob (≥40 chars). Regex
// needed: a length-classed character class with optional padding — a length
// constraint over a character set, not a fixed substring.
var longBase64Re = regexp.MustCompile(`^[A-Za-z0-9+/]{40,}={0,2}$`)

// longHexRe matches a standalone long hex blob (≥40 chars). Same rationale as
// longBase64Re: a length-classed character set.
var longHexRe = regexp.MustCompile(`^[0-9a-fA-F]{40,}$`)

// Redact returns a NEW map with every value sanitized per the two layers
// above. It never mutates the caller's map. A nil input returns nil so the
// zero-cost-when-off path (which never calls Redact) and the empty-attrs path
// both stay allocation-free where possible.
func Redact(attrs map[string]any) map[string]any {
	if len(attrs) == 0 {
		return attrs
	}
	out := make(map[string]any, len(attrs))
	for k, v := range attrs {
		if keyIsSensitive(k) {
			out[k] = redactedMarker
			continue
		}
		out[k] = redactValue(v)
	}
	return out
}

// redactValue scrubs a single value. Strings are pattern-scrubbed; nested
// maps and slices are recursed; everything else is passed through unchanged.
func redactValue(v any) any {
	switch t := v.(type) {
	case string:
		return scrubString(t)
	case map[string]any:
		return Redact(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = redactValue(e)
		}
		return out
	case []string:
		out := make([]string, len(t))
		for i, e := range t {
			out[i] = scrubString(e)
		}
		return out
	default:
		return v
	}
}

// scrubString applies the value-pattern scrub (layer 2). It prefers cheap
// string methods and only falls back to regex for genuinely shape-classed
// secrets.
func scrubString(s string) string {
	if s == "" {
		return s
	}

	// Bearer <token> — string prefix, case-insensitive on the scheme.
	if len(s) >= 7 {
		head := s[:7]
		if head == "Bearer " || strings.EqualFold(head, "Bearer ") {
			return "Bearer " + redactedMarker
		}
	}

	// sk- / xoxb- prefixed API keys — string prefix checks, no regex.
	if strings.HasPrefix(s, "sk-") {
		return redactedKind("api-key")
	}
	if strings.HasPrefix(s, "xoxb-") || strings.HasPrefix(s, "xoxp-") ||
		strings.HasPrefix(s, "xapp-") {
		return redactedKind("slack-token")
	}

	// JWT — regex (dot-segmented base64url triple). Replace the matched span
	// only, preserving any surrounding non-secret context.
	if strings.Contains(s, "eyJ") {
		s = jwtRe.ReplaceAllString(s, redactedKind("jwt"))
	}

	// kv params: token= / api_key= / apikey= / password= / secret= / key=.
	// Handled by string scan (split on & / ; / whitespace), no regex.
	if hasKVSecret(s) {
		s = scrubKVParams(s)
	}

	// URL query-strip: keep scheme+host+path, drop the query string (creds in
	// query). String split on '?', no regex.
	if (strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")) &&
		strings.Contains(s, "?") {
		if i := strings.IndexByte(s, '?'); i >= 0 {
			s = s[:i] + "?" + redactedMarker
		}
	}

	// Standalone long base64 / hex blob — regex on the whole (trimmed) value.
	// Hex is the MORE SPECIFIC shape (its character set is a strict subset of
	// base64's), so a pure-hex string also matches longBase64Re; check hex
	// first to label it correctly. Either way the secret is fully redacted.
	trimmed := strings.TrimSpace(s)
	if len(trimmed) >= 40 {
		if longHexRe.MatchString(trimmed) {
			return redactedKind("hex")
		}
		if longBase64Re.MatchString(trimmed) {
			return redactedKind("base64")
		}
	}

	return s
}

// kvSecretKeys are the kv-param key names whose value is scrubbed.
var kvSecretKeys = []string{"token", "api_key", "apikey", "password", "secret", "key", "access_token"}

// hasKVSecret cheaply reports whether s contains a "<secretkey>=" param so the
// more expensive scrubKVParams only runs when needed.
func hasKVSecret(s string) bool {
	lower := strings.ToLower(s)
	for _, k := range kvSecretKeys {
		if strings.Contains(lower, k+"=") {
			return true
		}
	}
	return false
}

// scrubKVParams redacts the VALUE of any sensitive kv param, splitting on the
// common separators (& ; space). String operations only — no regex.
func scrubKVParams(s string) string {
	// Split into fields on the common kv separators, scrub, rejoin preserving
	// the original separator at each boundary.
	var b strings.Builder
	b.Grow(len(s))
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '&' || s[i] == ';' || s[i] == ' ' {
			field := s[start:i]
			b.WriteString(scrubKVField(field))
			if i < len(s) {
				b.WriteByte(s[i]) // preserve the separator
			}
			start = i + 1
		}
	}
	return b.String()
}

// scrubKVField redacts the value of a single "key=value" field when the key is
// sensitive; otherwise returns the field unchanged.
func scrubKVField(field string) string {
	eq := strings.IndexByte(field, '=')
	if eq < 0 {
		return field
	}
	key := strings.ToLower(strings.TrimSpace(field[:eq]))
	for _, k := range kvSecretKeys {
		if key == k {
			return field[:eq+1] + redactedMarker
		}
	}
	return field
}
