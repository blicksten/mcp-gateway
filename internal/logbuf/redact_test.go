package logbuf

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRedact_BearerHeader asserts the secret in the canonical HTTP
// Authorization header is scrubbed while the "Authorization: Bearer "
// prefix is retained for operator diagnosability (context-preserving
// redaction).
func TestRedact_BearerHeader(t *testing.T) {
	cases := []struct{ in, secret string }{
		{"Authorization: Bearer abcdefABCDEF1234567890-_", "abcdefABCDEF1234567890-_"},
		{"authorization: bearer xyz12345-7890abcdefghij", "xyz12345-7890abcdefghij"},
		{"Authorization:   Bearer  DEADBEEFDEADBEEFDEADBEEFDEADBEEF", "DEADBEEFDEADBEEFDEADBEEFDEADBEEF"},
	}
	for _, tc := range cases {
		out := Redact(tc.in)
		assert.NotContains(t, out, tc.secret, "secret value leaked after redaction of %q", tc.in)
		assert.Contains(t, out, Redacted)
		// Context-preserving: the word "Bearer" stays so operators
		// know an auth header WAS present (PAL MEDIUM fix).
		assert.Regexp(t, `(?i)bearer`, out, "Bearer prefix should be retained for diagnosability")
	}
}

func TestRedact_ApiKey(t *testing.T) {
	cases := []string{
		"api_key=1234567890abcdef",
		"API-KEY: abcdefghij0123456789",
		"x-api-key: KQWE9837HGF0000000",
		"access_token=abcdefghij1234567",
	}
	for _, in := range cases {
		out := Redact(in)
		assert.Contains(t, out, Redacted, "api-key shape not redacted in %q", in)
	}
}

func TestRedact_AWSAccessKeyID(t *testing.T) {
	in := "AKIAIOSFODNN7EXAMPLE env var value"
	out := Redact(in)
	assert.NotContains(t, out, "AKIAIOSFODNN7EXAMPLE")
	assert.Contains(t, out, Redacted)
}

func TestRedact_GithubPAT(t *testing.T) {
	for _, prefix := range []string{"ghp", "gho", "ghu", "ghs", "ghr"} {
		secret := "ABCDEFghijklmnopqrstuvwxyzABCDEF1234567"
		in := prefix + "_" + secret
		out := Redact(in)
		assert.Contains(t, out, Redacted, "GitHub %s token not redacted", prefix)
		assert.NotContains(t, out, secret, "%s secret leaked", prefix)
		// The type prefix "ghp_" / "gho_" etc. is retained so
		// operators know which kind of token leaked (context-preserving).
		assert.Contains(t, out, prefix+"_", "%s prefix should be retained for diagnosability", prefix)
	}
}

// TestRedact_JWT verifies three-dot JSON Web Tokens are matched by the
// dedicated JWT pattern. The generic base64url pattern does NOT include
// `.` so would miss these without the JWT rule.
func TestRedact_JWT(t *testing.T) {
	// Realistic JWT shape: header.payload.signature, each base64url.
	in := "auth event: jwt=eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4ifQ.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U more context"
	out := Redact(in)
	assert.Contains(t, out, Redacted)
	// Assert the JWT payload "eyJzdWIiOi..." is gone. The general
	// pattern might substitute each segment individually, but it would
	// not span the dots; the JWT pattern ensures the whole token goes
	// in one shot.
	assert.NotContains(t, out, "eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4ifQ")
}

func TestRedact_PasswordAssignment(t *testing.T) {
	in := "password=hunter2 foo"
	out := Redact(in)
	assert.Contains(t, out, Redacted)
	assert.NotContains(t, out, "hunter2")
}

func TestRedact_GenericBase64urlBlob(t *testing.T) {
	// 43-char base64url string (like a Bearer token length).
	blob := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQR"
	in := "prefix " + blob + " suffix"
	out := Redact(in)
	assert.Contains(t, out, Redacted)
	assert.NotContains(t, out, blob)
}

func TestRedact_PreservesUnrelatedText(t *testing.T) {
	in := "starting server foo on port 8765 with 3 tools"
	out := Redact(in)
	assert.Equal(t, in, out, "benign log line must round-trip unchanged")
}

func TestRedact_IdempotentOnAlreadyRedacted(t *testing.T) {
	in := "prefix " + Redacted + " suffix"
	out := Redact(in)
	assert.Equal(t, in, out, "already-redacted line must not be rewritten further")
}

func TestContainsSecretShape(t *testing.T) {
	assert.True(t, containsSecretShape("Authorization: Bearer x"))
	assert.True(t, containsSecretShape("api_key=1"))
	assert.True(t, containsSecretShape("password=1"))
	assert.False(t, containsSecretShape("hello world"))
}

// TestRing_WriteAppliesRedaction verifies the Ring pipeline scrubs
// before storage — clients reading .Lines() or subscribing never see
// the raw token.
func TestRing_WriteAppliesRedaction(t *testing.T) {
	r := New(10)
	r.Write("Authorization: Bearer extremely-secret-token-value-0000000000-aaa")

	lines := r.Lines()
	if assert.Len(t, lines, 1) {
		assert.Contains(t, lines[0].Text, Redacted)
		assert.NotContains(t, lines[0].Text, "extremely-secret-token-value")
	}
}

// TestRing_SubscribersReceiveRedacted asserts SSE subscribers see the
// sanitized text, not the raw line.
func TestRing_SubscribersReceiveRedacted(t *testing.T) {
	r := New(10)
	ch := r.Subscribe()
	defer r.Unsubscribe(ch)

	go r.Write("password=super-secret-passphrase-1234")
	line := <-ch
	assert.Contains(t, line.Text, Redacted)
	assert.NotContains(t, line.Text, "super-secret-passphrase")

	// The raw input substring "super-secret" must not leak via the
	// subscriber channel's text payload.
	assert.False(t, strings.Contains(line.Text, "super-secret"))
}
