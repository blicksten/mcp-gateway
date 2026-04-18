package logbuf

import (
	"regexp"
	"strings"
)

// Redacted is the placeholder substituted for secret-shaped tokens
// anywhere in a child-process log line. The value is fixed so operators
// can grep for it in captured logs.
const Redacted = "***REDACTED***"

// redactionPattern pairs a regex with its replacement template so
// context-bearing patterns can preserve the lefthand ("api_key=") part
// while scrubbing only the secret value. PAL MEDIUM fix — log
// diagnostic value is greatly improved by keeping the field name.
type redactionPattern struct {
	re   *regexp.Regexp
	repl string
}

// redactionPatterns are applied in order. The first match consumes its
// input; downstream patterns do not see the same characters. Each
// pattern intentionally over-matches rather than under-matches: false
// positives produce redacted log lines (harmless), false negatives leak
// secrets (unacceptable). Regex is used here because the patterns need
// alternation, anchoring, and byte-class character groups — string
// methods cannot express them succinctly (CLAUDE.md §regex-discipline).
var redactionPatterns = []redactionPattern{
	// "Authorization: Bearer <token>" header — case-insensitive header
	// name. Keep the "Authorization: Bearer " prefix so operators know
	// an auth header WAS present; scrub only the credential.
	{regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)\S+`), "${1}" + Redacted},
	// Bare "Bearer <token>" substring (not part of an Authorization
	// header line — those were handled above).
	{regexp.MustCompile(`(?i)(\bbearer\s+)[A-Za-z0-9._\-+/=]{16,}`), "${1}" + Redacted},
	// "api[_-]key=<value>" and friends — preserve the field name,
	// scrub the value.
	{regexp.MustCompile(`(?i)(\b(?:api[_-]?key|x-api-key|access[_-]?token|secret[_-]?key|auth[_-]?token)\s*[=:]\s*)[A-Za-z0-9._\-+/=]{8,}`), "${1}" + Redacted},
	// "password=<value>" in env or CLI argument shape.
	{regexp.MustCompile(`(?i)(\bpassword\s*[=:]\s*)\S{4,}`), "${1}" + Redacted},
	// AWS access key IDs (AKIA…) — fixed 20-char prefix pattern; fully
	// scrub (there is no useful "field name" around it).
	{regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), Redacted},
	// GitHub personal-access tokens — preserve the type prefix so
	// operators know which kind of token leaked (ghp/gho/ghu/ghs/ghr).
	{regexp.MustCompile(`\b(gh[pousr]_)[A-Za-z0-9]{36,}\b`), "${1}" + Redacted},
	// JWTs — three base64url segments separated by dots. The generic
	// base64url pattern below does NOT include `.`, so JWTs would slip
	// through without this specific pattern.
	{regexp.MustCompile(`\b[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\b`), Redacted},
	// Long base64url blobs (32+ chars of base64 alphabet), likely to
	// be tokens. Placed last because it's the broadest pattern.
	{regexp.MustCompile(`\b[A-Za-z0-9_\-]{32,}={0,2}\b`), Redacted},
}

// Redact returns a copy of s with every secret-shaped substring
// replaced by Redacted. Context-bearing patterns preserve the field
// name (e.g. "Authorization: Bearer ***REDACTED***") so operators
// retain diagnostic value.
func Redact(s string) string {
	out := s
	for _, p := range redactionPatterns {
		out = p.re.ReplaceAllString(out, p.repl)
	}
	return out
}

// RedactBytes returns a NEW []byte with secret-shaped substrings
// replaced. It does not modify the input slice. Callers with a large
// byte stream should prefer streaming through a scanner rather than
// calling this on a multi-MB buffer.
func RedactBytes(b []byte) []byte {
	return []byte(Redact(string(b)))
}

// containsSecretShape is a cheap pre-filter: return true if the input
// MIGHT contain a redactable pattern so callers can skip the regex
// machinery on boring log lines. Not used in Ring.Write by default
// (the regex engine is fast enough for log volumes) — exposed for
// downstream consumers that need a different performance trade-off.
func containsSecretShape(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "bearer") ||
		strings.Contains(l, "password") ||
		strings.Contains(l, "api") ||
		strings.Contains(l, "token") ||
		strings.Contains(l, "secret") ||
		strings.Contains(l, "akia") ||
		strings.Contains(l, "gh")
}
