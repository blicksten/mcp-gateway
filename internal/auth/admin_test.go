package auth

import (
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

// runAdminRequest is the AdminMiddleware-shaped sibling of runRequest
// (defined in middleware_test.go). It exists to keep test wiring
// explicit — readers should see "admin path" in the function name.
func runAdminRequest(t *testing.T, expected, header string, logger *slog.Logger) *http.Response {
	t.Helper()
	if logger == nil {
		logger, _ = captureLogger()
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	srv := httptest.NewServer(AdminMiddleware(expected, logger)(ok))
	t.Cleanup(srv.Close)

	req, err := http.NewRequest("GET", srv.URL+"/admin-probe", nil)
	require.NoError(t, err)
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func TestAdminMiddleware_200WithCorrectAdminBearer(t *testing.T) {
	adminToken, err := GenerateToken()
	require.NoError(t, err)

	resp := runAdminRequest(t, adminToken, "Bearer "+adminToken, nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestAdminMiddleware_401WhenMissingHeader(t *testing.T) {
	adminToken, _ := GenerateToken()

	resp := runAdminRequest(t, adminToken, "", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, "Bearer", resp.Header.Get("WWW-Authenticate"))
}

func TestAdminMiddleware_401WhenLowercaseScheme(t *testing.T) {
	adminToken, _ := GenerateToken()

	resp := runAdminRequest(t, adminToken, "bearer "+adminToken, nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestAdminMiddleware_401WhenWrongScheme(t *testing.T) {
	adminToken, _ := GenerateToken()

	resp := runAdminRequest(t, adminToken, "Basic "+adminToken, nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestAdminMiddleware_401WhenBearerWithEmptyToken(t *testing.T) {
	adminToken, _ := GenerateToken()

	resp := runAdminRequest(t, adminToken, "Bearer ", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// TestAdminMiddleware_401WhenRegularBearer is the central scope-isolation
// test for MCPR.3: presenting the regular Bearer to the admin scope must
// yield 401, NOT 200. Without this guard, the layered-vs-exclusive design
// decision regresses silently.
func TestAdminMiddleware_401WhenRegularBearer(t *testing.T) {
	adminToken, _ := GenerateToken()
	regularToken, _ := GenerateToken() // simulates the regular Bearer
	require.NotEqual(t, adminToken, regularToken)

	resp := runAdminRequest(t, adminToken, "Bearer "+regularToken, nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"regular Bearer must NOT satisfy admin scope (exclusive scope, MCPR.3)")
}

func TestAdminMiddleware_401BodyShapeHasErrorAndAdminHint(t *testing.T) {
	adminToken, _ := GenerateToken()

	resp := runAdminRequest(t, adminToken, "", nil)
	defer resp.Body.Close()

	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var body ErrorBody
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &body))

	assert.Equal(t, "authentication required", body.Error)
	// Admin hint must mention BOTH fallbacks (env var + file path) so
	// future rewording does not silently drop one.
	assert.Contains(t, body.Hint, EnvVarNameAdmin)
	assert.Contains(t, body.Hint, "admin.token")
	// And must NOT direct callers to the regular auth.token fallbacks
	// — that would mislead operators on which token shape to provide.
	assert.NotContains(t, body.Hint, EnvVarName,
		"admin hint must not mention regular env var")
}

func TestAdminMiddleware_NeverLogsReceivedToken(t *testing.T) {
	logger, logs := captureLogger()
	adminToken, _ := GenerateToken()
	regularToken, _ := GenerateToken()

	resp := runAdminRequest(t, adminToken, "Bearer "+regularToken, logger)
	defer resp.Body.Close()

	logOutput := logs.String()
	assert.NotContains(t, logOutput, regularToken, "received token must not appear in logs")
	assert.NotContains(t, logOutput, adminToken, "expected token must not appear in logs")
	assert.Contains(t, logOutput, "auth: rejected request")
	// Scope label must appear so operators can grep admin rejections.
	assert.Contains(t, logOutput, "admin",
		"401 log line must include scope=admin so operators can distinguish admin rejections from regular ones")
}

func TestAdminMiddleware_NilLoggerFallback(t *testing.T) {
	adminToken, _ := GenerateToken()
	mw := AdminMiddleware(adminToken, nil)
	assert.NotNil(t, mw)

	req := httptest.NewRequest("GET", "/x", nil)
	rec := httptest.NewRecorder()
	mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})).ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAdminMiddleware_AdminHintWordingFixed(t *testing.T) {
	// Guard against well-meaning rewording that drops env var or file path.
	assert.Contains(t, adminHintMessage, "MCP_GATEWAY_ADMIN_TOKEN")
	assert.Contains(t, adminHintMessage, "~/.mcp-gateway/admin.token")
	assert.True(t,
		strings.Contains(adminHintMessage, "env var") ||
			strings.Contains(adminHintMessage, "environment variable"),
	)
}

func TestAdminMiddleware_ConstantTimeOnLongerToken(t *testing.T) {
	// Mirror TestMiddleware_ConstantTimeOnLongerToken — ensures the
	// pad-to-expected-length guard is preserved through the
	// bearerMiddleware refactor.
	adminToken, _ := GenerateToken()

	resp := runAdminRequest(t, adminToken, "Bearer "+adminToken+"extra-padding-bytes", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestDefaultAdminTokenPath(t *testing.T) {
	// Path must be distinct from DefaultTokenPath so the two tokens
	// never collide in a single file.
	configDir := "/tmp/mcp-gateway-test"
	regular := DefaultTokenPath(configDir)
	admin := DefaultAdminTokenPath(configDir)

	assert.NotEqual(t, regular, admin, "regular and admin tokens must live in distinct files")
	assert.Contains(t, admin, "admin.token")
	assert.NotContains(t, admin, "auth.token")
}
