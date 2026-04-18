package auth

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// bearerPrefix is the exact scheme name the middleware accepts. The check
// is case-sensitive per RFC 6750 §2.1 (scheme must be "Bearer").
const bearerPrefix = "Bearer "

// hintMessage is the fixed operator guidance returned in the 401 body's
// `hint` field. Wording is deliberate — tests grep for the two fallbacks
// (env var name, token file path) so future rewording still surfaces both.
const hintMessage = "add Bearer token via MCP_GATEWAY_AUTH_TOKEN env var or ~/.mcp-gateway/auth.token file"

// ErrorBody is the JSON shape returned on 401. The `hint` field is
// additive — existing clients that only parse `error` are unaffected.
// Defined in ADR-0003 §401-hint.
type ErrorBody struct {
	Error string `json:"error"`
	Hint  string `json:"hint,omitempty"`
}

// Middleware returns an HTTP middleware that requires an
// `Authorization: Bearer <token>` header matching expected. Missing /
// malformed / mismatched tokens return 401 with a JSON body
// `{"error":"authentication required","hint":"..."}` and a
// `WWW-Authenticate: Bearer` header.
//
// The compare uses crypto/subtle.ConstantTimeCompare to resist timing
// side-channels. The received token is never logged; a single redacted
// debug line identifying the request path is emitted on 401.
//
// See ADR-0003 §policy-matrix, §401-hint.
func Middleware(expected string, logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	expectedBytes := []byte(expected)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			// Case-sensitive scheme match. RFC 6750 §2.1 mandates "Bearer".
			// Lowercase "bearer" is not accepted. An empty header is not
			// accepted.
			if !strings.HasPrefix(header, bearerPrefix) {
				writeUnauthorized(w, r, logger, "missing_or_malformed_scheme")
				return
			}
			received := header[len(bearerPrefix):]
			if received == "" {
				writeUnauthorized(w, r, logger, "empty_token")
				return
			}
			if subtle.ConstantTimeCompare([]byte(received), expectedBytes) != 1 {
				writeUnauthorized(w, r, logger, "mismatch")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// writeUnauthorized writes a 401 response with the `hint` guidance body
// and logs a single redacted line. The received token is NEVER included
// in the log — only the path and the reason class.
func writeUnauthorized(w http.ResponseWriter, r *http.Request, logger *slog.Logger, reason string) {
	logger.Debug("auth: rejected request", "path", r.URL.Path, "reason", reason)
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	body := ErrorBody{
		Error: "authentication required",
		Hint:  hintMessage,
	}
	_ = json.NewEncoder(w).Encode(body)
}
