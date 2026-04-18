package logbuf

import (
	"regexp"
	"strings"
)

// Redacted is the placeholder substituted for secret-shaped tokens
// anywhere in a child-process log line. The value is fixed so operators
// can grep for it in captured logs.
const Redacted = "***REDACTED***"

// redactionPatterns are applied in order. The first match consumes its
// input; downstream patterns do not see the same characters. Each
// pattern intentionally over-matches rather than under-matches: false
// positives produce redacted log lines (harmless), false negatives leak
// secrets (unacceptable). Regex is used here because the patterns need
// alternation, anchoring, and byte-class character groups — string
// methods cannot express them succinctly (CLAUDE.md §regex-discipline).
var redactionPatterns = []*regexp.Regexp{
	// "Authorization: Bearer <token>" header — case-insensitive header name,
	// anything non-whitespace after "Bearer ".
	regexp.MustCompile(`(?i)authorization:\s*bearer\s+\S+`),
	// Bare "Bearer <token>" substring.
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._\-+/=]{16,}`),
	// "api[_-]key=<value>" and "apikey=<value>" tokens in query strings
	// or env-like syntax. Accept either `=` or `: ` as the separator.
	regexp.MustCompile(`(?i)\b(?:api[_-]?key|x-api-key|access[_-]?token|secret[_-]?key|auth[_-]?token)\s*[=:]\s*[A-Za-z0-9._\-+/=]{8,}`),
	// "password=<value>" in env or CLI argument shape.
	regexp.MustCompile(`(?i)\bpassword\s*[=:]\s*\S{4,}`),
	// AWS access key IDs (AKIA…) — fixed 20-char prefix pattern.
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	// GitHub personal-access tokens (ghp_, gho_, ghu_, ghs_, ghr_) —
	// 36 base62+underscore chars after the type prefix.
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`),
	// Long base64url blobs (32+ chars of base64 alphabet), likely to be
	// tokens. Placed last because it's the broadest pattern.
	regexp.MustCompile(`\b[A-Za-z0-9_\-]{32,}={0,2}\b`),
}

// Redact returns a copy of s with every secret-shaped substring
// replaced by Redacted. Matches are applied in the order defined in
// redactionPatterns; a header-line match (Authorization: …) consumes
// the whole header so the broader base64 pattern does not re-match
// and produce "Authorization: ***REDACTED***" followed by a second
// "***REDACTED***".
func Redact(s string) string {
	out := s
	for _, re := range redactionPatterns {
		out = re.ReplaceAllString(out, Redacted)
	}
	return out
}

// RedactBytes redacts in place on a []byte slice by rewriting via
// Redact. Callers with a large byte stream should prefer streaming
// through a scanner rather than calling this on a multi-MB buffer.
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
