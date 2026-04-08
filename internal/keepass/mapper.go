package keepass

import (
	"fmt"
	"strings"

	"mcp-gateway/internal/models"
)

// ServerCredentials holds mapped credentials for one server.
type ServerCredentials struct {
	ServerName string
	EnvVars    map[string]string
	Headers    map[string]string
}

// MapToCredentials maps KeePass entries to server credentials.
// Each entry's Title becomes the server name. Standard fields (Password, UserName)
// are mapped to env vars with a prefix derived from the server name.
// Custom fields with "HDR_" prefix are separated into Headers.
// Returns an error if two entries produce the same env key prefix (collision).
func MapToCredentials(entries []KeePassEntry) ([]ServerCredentials, error) {
	prefixOwner := make(map[string]string) // prefix → original title
	var result []ServerCredentials

	for _, e := range entries {
		prefix := envPrefix(e.Title)

		if owner, exists := prefixOwner[prefix]; exists {
			return nil, fmt.Errorf("env key prefix collision: %q and %q both produce prefix %q",
				owner, e.Title, prefix)
		}
		prefixOwner[prefix] = e.Title

		sc := ServerCredentials{
			ServerName: e.Title,
			EnvVars:    make(map[string]string),
			Headers:    make(map[string]string),
		}

		if len(e.Password) > 0 {
			key := prefix + "_PASSWORD"
			if models.IsDangerousEnvKey(key) {
				return nil, fmt.Errorf("KDBX entry %q produces dangerous env key %q", e.Title, key)
			}
			sc.EnvVars[key] = string(e.Password)
		}
		if e.User != "" {
			key := prefix + "_USER"
			if models.IsDangerousEnvKey(key) {
				return nil, fmt.Errorf("KDBX entry %q produces dangerous env key %q", e.Title, key)
			}
			sc.EnvVars[key] = e.User
		}

		for k, v := range e.CustomFields {
			if headerName, ok := strings.CutPrefix(k, "HDR_"); ok {
				if headerName != "" {
					sc.Headers[headerName] = v
				}
			} else {
				envKey := prefix + "_" + envSanitize(k)
				if models.IsDangerousEnvKey(envKey) {
					return nil, fmt.Errorf("KDBX entry %q field %q produces dangerous env key %q", e.Title, k, envKey)
				}
				sc.EnvVars[envKey] = v
			}
		}

		result = append(result, sc)
	}

	return result, nil
}

// envPrefix converts a server name to a POSIX-safe env var prefix.
// Applies envSanitize to handle non-ASCII characters, hyphens, etc.
func envPrefix(name string) string {
	return envSanitize(name)
}

// envSanitize converts a custom field name to a valid env var key segment.
// Uppercase, non-alphanumeric chars → underscores.
func envSanitize(name string) string {
	s := strings.ToUpper(name)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
