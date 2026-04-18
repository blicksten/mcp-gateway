// Package auth implements Bearer token authentication for the mcp-gateway
// daemon and its clients (mcp-ctl, VS Code extension).
//
// Token lifecycle and policy are documented in docs/ADR-0003-bearer-token-auth.md.
package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TokenByteLen is the number of random bytes used for each generated token.
// 32 bytes encode to 43 base64url characters (no padding).
const TokenByteLen = 32

// MinTokenLen is the minimum acceptable length for a persisted token.
// Tokens shorter than this are treated as malformed and regenerated.
const MinTokenLen = 43

// base64urlAlphabet is the set of characters that may appear in a base64url
// encoded token (RFC 4648 §5, unpadded).
const base64urlAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

// GenerateToken returns a cryptographically random token encoded as a
// URL-safe base64 string (no padding). The token is TokenByteLen bytes of
// entropy, which encodes to 43 ASCII characters.
func GenerateToken() (string, error) {
	buf := make([]byte, TokenByteLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// LoadOrCreate resolves the active Bearer token.
//
// Discovery order (per ADR-0003 §auth-header-fallback and §token-lifecycle):
//   1. If envToken is non-empty, return it verbatim (ephemeral override,
//      never persisted).
//   2. Else read path: if the file exists and its trimmed content is a
//      well-formed base64url token of length >= MinTokenLen, return it.
//   3. Else generate a new token, atomically persist it (temp + rename)
//      with platform-correct permissions, and return it.
//
// The path's parent directory is created if missing (0700 on POSIX).
// Atomic write must complete before the daemon's http.Server.Serve is
// called (see T12A.5).
func LoadOrCreate(path, envToken string) (string, error) {
	// (1) env override wins, file is not touched.
	if envToken != "" {
		if !looksLikeToken(envToken) {
			return "", errors.New("MCP_GATEWAY_AUTH_TOKEN env var is set but malformed (expected >=43 base64url chars)")
		}
		return envToken, nil
	}

	// (2) try to read an existing persisted token.
	if tok, ok := tryReadToken(path); ok {
		return tok, nil
	}

	// (3) generate + persist atomically.
	tok, err := GenerateToken()
	if err != nil {
		return "", err
	}
	if err := writeTokenAtomic(path, tok); err != nil {
		return "", err
	}
	return tok, nil
}

// tryReadToken reads path and returns (token, true) if it holds a
// well-formed token, or ("", false) otherwise. Short/malformed files
// are treated as "absent" and lead to regeneration.
func tryReadToken(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	tok := strings.TrimSpace(string(data))
	if !looksLikeToken(tok) {
		return "", false
	}
	return tok, true
}

// writeTokenAtomic writes the token to path via a temp-file + rename
// sequence so readers never observe a partial file. Permissions are set
// per-platform (see applyTokenFilePerms in token_perms_*.go).
//
// Windows hardening (PAL-2026-04-18 HIGH):
//   - applyTokenFilePerms runs BEFORE WriteString so the tiny window
//     between CreateTemp and perms-applied contains an EMPTY file; any
//     race-reader sees no token content.
//   - On Windows, if os.Rename fails because the destination exists
//     (MoveFileEx quirk on some filesystems), we explicitly remove the
//     destination and retry. This matters when LoadOrCreate detects a
//     corrupt token and needs to regenerate — naive rename would leave
//     the daemon unable to replace the old file and crash on startup.
func writeTokenAtomic(path, token string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create token dir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".auth-token-*")
	if err != nil {
		return fmt.Errorf("create temp token file: %w", err)
	}
	tmpPath := tmp.Name()
	// Belt-and-braces: remove the temp file if anything after this point fails.
	cleanup := func() { _ = os.Remove(tmpPath) }

	// Apply platform-correct permissions BEFORE writing the token content.
	// On Windows this means the DACL is installed while the file is empty —
	// no race-reader window sees the token under default (inherited) perms.
	if err := applyTokenFilePerms(tmpPath); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("apply token file perms: %w", err)
	}

	if _, err := tmp.WriteString(token); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp token file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sync temp token file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp token file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		// Windows can refuse to replace an existing file in some
		// edge cases (non-NTFS paths, readonly bit set, etc). Try
		// an explicit remove+rename before giving up so regenerating
		// a malformed token does not permanently block startup.
		if _, statErr := os.Stat(path); statErr == nil {
			if rmErr := os.Remove(path); rmErr == nil {
				if err2 := os.Rename(tmpPath, path); err2 == nil {
					return nil
				}
			}
		}
		cleanup()
		return fmt.Errorf("rename token file into place: %w", err)
	}
	return nil
}

// looksLikeToken reports whether s has the shape of a base64url token of
// at least MinTokenLen characters. This is a structural check only —
// there is no version field (see ADR-0003 §token-lifecycle, L-2).
func looksLikeToken(s string) bool {
	if len(s) < MinTokenLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(base64urlAlphabet, rune(s[i])) {
			return false
		}
	}
	return true
}

// DefaultTokenPath returns the canonical token location relative to the
// config directory (~/.mcp-gateway/auth.token).
func DefaultTokenPath(configDir string) string {
	return filepath.Join(configDir, "auth.token")
}
