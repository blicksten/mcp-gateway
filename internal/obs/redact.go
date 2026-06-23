package obs

import (
	"encoding/json"
	"reflect"
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

// longBase64Re matches a long base64 / base64url blob (≥40 chars), EMBEDDED
// anywhere in the value (finding #2: anchored ^...$ let embedded blobs leak).
// The character class includes base64url's -_ alongside standard +/ so both
// alphabets are caught. Regex needed: a length-classed character class with
// optional padding — a length constraint over a character set, not a fixed
// substring.
//
// THRESHOLD = 40 chars (kept deliberately). Lowering it would start redacting
// legitimate identifiers (UUIDs, git SHAs are 40 hex, long opaque ids). Over-
// redaction at 40+ is the accepted SAFE direction for SECRET-handling code: we
// would rather blank a long id than leak a 40-char key.
var longBase64Re = regexp.MustCompile(`[A-Za-z0-9+/_-]{40,}={0,2}`)

// longHexRe matches a long hex blob (≥40 chars) EMBEDDED anywhere, using \b
// word boundaries so a hex run inside a longer string is caught (finding #2).
// Same rationale and 40-char threshold as longBase64Re.
var longHexRe = regexp.MustCompile(`\b[0-9a-fA-F]{40,}\b`)

// wholeBase64Re / wholeHexRe are ANCHORED variants used only for the
// whole-value fast path: when the ENTIRE trimmed value is a single blob we can
// label it precisely (and consume trailing '=' padding) without the embedded
// ReplaceAll leaving a dangling tail. Hex is checked first (subset of base64).
var wholeHexRe = regexp.MustCompile(`^[0-9a-fA-F]{40,}$`)
var wholeBase64Re = regexp.MustCompile(`^[A-Za-z0-9+/_-]{40,}={0,2}$`)

// Redact returns a NEW map with every value sanitized per the two layers
// above. It never mutates the caller's map. A nil input returns nil so the
// zero-cost-when-off path (which never calls Redact) and the empty-attrs path
// both stay allocation-free where possible.
func Redact(attrs map[string]any) map[string]any {
	if len(attrs) == 0 {
		return attrs
	}
	// Finding #4: a secret can appear AS A KEY (e.g. a header map keyed by the
	// raw authorization line, or a kv blob shoved into a key). redactMapStringAny
	// scrubs each key's own string before using it as the output key, while the
	// VALUE sensitivity decision still uses the ORIGINAL key (so e.g. an
	// "api_token" key still blanks its value wholesale).
	return redactMapStringAny(attrs, 0)
}

// maxRedactDepth bounds recursion through nested/reflected values so a
// pathological (or cyclic-after-Marshal) payload can never spin the redactor.
// 8 is generous for realistic attrs while still finite.
const maxRedactDepth = 8

// redactValue scrubs a single value. Strings are pattern-scrubbed; nested
// maps and slices are recursed; TYPED containers (http.Header, url.Values,
// map[string]string, []byte, structs, pointers) are handled reflectively so no
// secret can slip through the default pass-through (finding #1).
func redactValue(v any) any { return redactValueDepth(v, 0) }

func redactValueDepth(v any, depth int) any {
	if depth > maxRedactDepth {
		// Too deep to keep walking safely: return the marker (safe direction —
		// never leak the unredacted remainder).
		return redactedMarker
	}
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		return scrubString(t)
	case map[string]any:
		// Scrub keys too (finding #4 applies at every level, not just top).
		return redactMapStringAny(t, depth)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = redactValueDepth(e, depth+1)
		}
		return out
	case []string:
		out := make([]string, len(t))
		for i, e := range t {
			out[i] = scrubString(e)
		}
		return out
	case []byte:
		// A secret can arrive as a raw byte slice (e.g. a marshalled token).
		// Scrub its string form (finding #1).
		return scrubString(string(t))
	default:
		return redactReflect(v, depth)
	}
}

// redactMapStringAny redacts a map[string]any honoring BOTH the key heuristic
// and key-string scrub at the current depth. Shared by the typed fast path and
// the top-level Redact body's per-level recursion.
func redactMapStringAny(m map[string]any, depth int) map[string]any {
	out := make(map[string]any, len(m))
	for k, val := range m {
		outKey := scrubString(k)
		if keyIsSensitive(k) {
			out[outKey] = redactedMarker
			continue
		}
		out[outKey] = redactValueDepth(val, depth+1)
	}
	return out
}

// redactReflect handles TYPED values the type switch did not catch:
// reflect.Map (http.Header, url.Values, map[string]string, …), reflect.Slice/
// Array (including []byte aliases), reflect.Ptr/Interface, and reflect.Struct
// (flattened via JSON). This closes finding #1: previously the default case
// returned these UNREDACTED, leaking e.g. an http.Header carrying an
// Authorization value or a struct field holding a secret.
func redactReflect(v any, depth int) any {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Map:
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			// Scrub the key's string form, and key-sensitivity blanks the value
			// wholesale (e.g. http.Header keyed "Authorization").
			rawKey := stringify(iter.Key())
			outKey := scrubString(rawKey)
			if keyIsSensitive(rawKey) {
				out[outKey] = redactedMarker
				continue
			}
			out[outKey] = redactValueDepth(iter.Value().Interface(), depth+1)
		}
		return out
	case reflect.Slice, reflect.Array:
		// GUARD: rv.Bytes() is only legal for a Slice whose Elem is Uint8.
		// (An Array of bytes is not addressable for .Bytes(), so fall through
		// to element recursion for arrays.)
		if rv.Kind() == reflect.Slice && rv.Type().Elem().Kind() == reflect.Uint8 {
			return scrubString(string(rv.Bytes()))
		}
		out := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out[i] = redactValueDepth(rv.Index(i).Interface(), depth+1)
		}
		return out
	case reflect.Ptr, reflect.Interface:
		if rv.IsNil() {
			return nil
		}
		return redactValueDepth(rv.Elem().Interface(), depth+1)
	case reflect.Struct:
		// Flatten the struct to map[string]any / []any / primitives via JSON,
		// then redact that. This avoids hand-walking unexported fields and gives
		// us a value the existing string/map/slice paths already cover. If
		// Marshal fails, return the marker (safe direction — never leak).
		b, err := json.Marshal(v)
		if err != nil {
			return redactedMarker
		}
		var decoded any
		if err := json.Unmarshal(b, &decoded); err != nil {
			return redactedMarker
		}
		return redactValueDepth(decoded, depth+1)
	default:
		// Genuine primitives the type switch already excludes (int/float/bool)
		// plus anything exotic with no string content to leak.
		return v
	}
}

// stringify renders a reflected map key as a string for the key heuristic /
// key scrub. Attrs maps and the typed containers we care about (http.Header,
// url.Values, map[string]string) are all string-keyed, so string keys are the
// only meaningful case; any non-string key (exotic, not seen in attrs) yields
// "" which is treated as a non-sensitive, empty key.
func stringify(k reflect.Value) string {
	if k.Kind() == reflect.String {
		return k.String()
	}
	return ""
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

	// sk- prefixed API keys — string prefix check, no regex.
	if strings.HasPrefix(s, "sk-") {
		return redactedKind("api-key")
	}
	// GitHub tokens / personal access tokens (finding #7). github_pat_ is
	// checked before pat_ since the latter is a substring-prefix of neither but
	// both are distinct prefixes; order is irrelevant for correctness here.
	if strings.HasPrefix(s, "ghp_") || strings.HasPrefix(s, "gho_") ||
		strings.HasPrefix(s, "ghs_") || strings.HasPrefix(s, "ghr_") ||
		strings.HasPrefix(s, "github_pat_") || strings.HasPrefix(s, "pat_") {
		return redactedKind("api-key")
	}
	// Slack tokens — string prefix checks, no regex.
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

	// URL scrub (finding #5): for ANY scheme://… (http, https, ws, wss,
	// postgres, redis, mongodb, …) strip BOTH the userinfo (user:pass@) and the
	// query string, since either can carry credentials. String operations only.
	if hasSchemeSeparator(s) {
		s = scrubURL(s)
	}

	// Long base64 / hex blob. Two passes:
	//
	//  (a) WHOLE-VALUE fast path: if the entire trimmed value is one blob, label
	//      it precisely and consume any '=' padding (so a lone base64 value is
	//      «redacted:base64», not «redacted:hex»== with a dangling tail). Hex is
	//      checked first since its alphabet is a strict subset of base64's.
	//  (b) EMBEDDED pass (finding #2: anchored ^...$ used to let a blob inside a
	//      larger string leak): ReplaceAllString the matched spans, preserving
	//      surrounding context. Hex spans first so they get the "hex" label; the
	//      «redacted:hex» marker has no 40+ hex run, so the base64 pass below
	//      cannot re-match it.
	trimmed := strings.TrimSpace(s)
	if len(trimmed) >= 40 {
		if wholeHexRe.MatchString(trimmed) {
			return redactedKind("hex")
		}
		if wholeBase64Re.MatchString(trimmed) {
			return redactedKind("base64")
		}
	}
	if containsLongRun(s) {
		s = longHexRe.ReplaceAllString(s, redactedKind("hex"))
		s = longBase64Re.ReplaceAllString(s, redactedKind("base64"))
	}

	return s
}

// containsLongRun cheaply gates the base64/hex regexes: only run them when s
// holds a run of at least 40 consecutive base64/base64url-alphabet characters
// (a superset of hex). Avoids the regex on every short value.
func containsLongRun(s string) bool {
	run := 0
	for i := 0; i < len(s); i++ {
		if isB64Char(s[i]) {
			run++
			if run >= 40 {
				return true
			}
		} else {
			run = 0
		}
	}
	return false
}

// isB64Char reports whether c is in the base64 / base64url alphabet (the
// superset that also covers hex). Byte comparisons only — no regex.
func isB64Char(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z':
		return true
	case c >= 'a' && c <= 'z':
		return true
	case c >= '0' && c <= '9':
		return true
	case c == '+' || c == '/' || c == '-' || c == '_':
		return true
	default:
		return false
	}
}

// hasSchemeSeparator reports whether s looks like a URL with a scheme: it
// contains "://" and the part before it is a non-empty run of scheme-legal
// characters (letters, digits, +, -, .) starting with a letter. String ops
// only — no regex.
func hasSchemeSeparator(s string) bool {
	i := strings.Index(s, "://")
	if i <= 0 {
		return false
	}
	scheme := s[:i]
	first := scheme[0]
	if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z')) {
		return false
	}
	for j := 0; j < len(scheme); j++ {
		c := scheme[j]
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '+' || c == '-' || c == '.'
		if !ok {
			return false
		}
	}
	return true
}

// scrubURL strips credentials from a "scheme://…" string for ANY scheme
// (finding #5): the userinfo segment (everything between "://" and the first
// "@", when that "@" precedes the first "/", "?" or "#") and the query string
// (everything after the first "?"). String operations only — no regex.
func scrubURL(s string) string {
	sep := strings.Index(s, "://")
	if sep < 0 {
		return s
	}
	schemeAndSep := s[:sep+3] // includes "://"
	rest := s[sep+3:]

	// Determine the authority/path boundary: first of '/', '?', '#'.
	auth := rest
	tail := ""
	if i := indexAnyByte(rest, "/?#"); i >= 0 {
		auth = rest[:i]
		tail = rest[i:]
	}

	// Strip userinfo: if the authority contains '@', drop everything up to and
	// including the LAST '@' (userinfo may itself contain ':' but not '@' in a
	// well-formed URL; using the last '@' is the conservative split).
	if at := strings.LastIndexByte(auth, '@'); at >= 0 {
		auth = redactedMarker + "@" + auth[at+1:]
	}

	out := schemeAndSep + auth + tail

	// Strip query: drop everything after the first '?'.
	if q := strings.IndexByte(out, '?'); q >= 0 {
		out = out[:q] + "?" + redactedMarker
	}
	return out
}

// indexAnyByte returns the index of the first byte of s that appears in chars,
// or -1. A small helper so scrubURL stays regex-free.
func indexAnyByte(s, chars string) int {
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(chars, s[i]) >= 0 {
			return i
		}
	}
	return -1
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
