package auth

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureLogger returns a logger whose output is captured in the returned
// buffer so tests can assert that the received token never appears in logs.
func captureLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return logger, buf
}

func runRequest(t *testing.T, mw func(http.Handler) http.Handler, header string) *http.Response {
	t.Helper()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	srv := httptest.NewServer(mw(ok))
	t.Cleanup(srv.Close)

	req, err := http.NewRequest("GET", srv.URL+"/probe", nil)
	require.NoError(t, err)
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func TestMiddleware_200WithCorrectBearer(t *testing.T) {
	logger, _ := captureLogger()
	token, err := GenerateToken()
	require.NoError(t, err)

	resp := runRequest(t, Middleware(token, logger), "Bearer "+token)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestMiddleware_401WhenMissing(t *testing.T) {
	logger, _ := captureLogger()
	token, _ := GenerateToken()

	resp := runRequest(t, Middleware(token, logger), "")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, "Bearer", resp.Header.Get("WWW-Authenticate"))
}

func TestMiddleware_401WhenLowercaseScheme(t *testing.T) {
	logger, _ := captureLogger()
	token, _ := GenerateToken()

	resp := runRequest(t, Middleware(token, logger), "bearer "+token)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestMiddleware_401WhenWrongScheme(t *testing.T) {
	logger, _ := captureLogger()
	token, _ := GenerateToken()

	resp := runRequest(t, Middleware(token, logger), "Basic "+token)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestMiddleware_401WhenBearerWithEmptyToken(t *testing.T) {
	logger, _ := captureLogger()
	token, _ := GenerateToken()

	resp := runRequest(t, Middleware(token, logger), "Bearer ")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestMiddleware_401WhenWrongToken(t *testing.T) {
	logger, _ := captureLogger()
	token, _ := GenerateToken()
	other, _ := GenerateToken()

	resp := runRequest(t, Middleware(token, logger), "Bearer "+other)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestMiddleware_401BodyShapeHasErrorAndHint(t *testing.T) {
	logger, _ := captureLogger()
	token, _ := GenerateToken()

	resp := runRequest(t, Middleware(token, logger), "")
	defer resp.Body.Close()

	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var body ErrorBody
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &body))

	assert.Equal(t, "authentication required", body.Error)
	// Fixed wording — future rewording must still mention both fallbacks.
	assert.Contains(t, body.Hint, EnvVarName)
	assert.Contains(t, body.Hint, "auth.token")
}

func TestMiddleware_NeverLogsReceivedToken(t *testing.T) {
	logger, logs := captureLogger()
	token, _ := GenerateToken()
	other, _ := GenerateToken()

	// Wrong-token path — middleware logs a debug line but must not
	// include the received token value.
	resp := runRequest(t, Middleware(token, logger), "Bearer "+other)
	defer resp.Body.Close()

	logOutput := logs.String()
	assert.NotContains(t, logOutput, other, "received token must not appear in logs")
	assert.NotContains(t, logOutput, token, "expected token must not appear in logs")
	// But the debug line itself must have fired.
	assert.Contains(t, logOutput, "auth: rejected request")
}

func TestMiddleware_ConstantTimeOnDifferentLengths(t *testing.T) {
	// Smoke test — ConstantTimeCompare short-circuits on unequal lengths.
	// We cannot meaningfully measure timing in a unit test, so this only
	// exercises the path: a much-shorter wrong token must still return 401.
	logger, _ := captureLogger()
	token, _ := GenerateToken()

	resp := runRequest(t, Middleware(token, logger), "Bearer abc")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestMiddleware_NilLoggerFallback(t *testing.T) {
	// Middleware must not panic when logger is nil.
	token, _ := GenerateToken()
	mw := Middleware(token, nil)
	assert.NotNil(t, mw)

	// Run a rejected request — no panic.
	req := httptest.NewRequest("GET", "/x", nil)
	rec := httptest.NewRecorder()
	mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})).ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestMiddleware_HintWordingFixed(t *testing.T) {
	// Testing the constant directly guards against well-meaning reword
	// that silently drops one of the two fallbacks.
	assert.Contains(t, hintMessage, "MCP_GATEWAY_AUTH_TOKEN")
	assert.Contains(t, hintMessage, "~/.mcp-gateway/auth.token")
	assert.True(t, strings.Contains(hintMessage, "env var") || strings.Contains(hintMessage, "environment variable"))
}
