// Package config — variable expansion for gateway configuration.
package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"mcp-gateway/internal/models"
)

// expandVarAllowlist lists env vars safe for os.Getenv fallback.
// Vars not in this list AND not in the explicit envMap resolve to empty string,
// preventing silent leakage of secrets like AWS_SECRET_ACCESS_KEY.
var expandVarAllowlist = map[string]bool{
	// PATH intentionally excluded — it's in dangerousEnvKeys (command hijack vector).
	"HOME":    true,
	"USER":    true,
	"LOGNAME": true,
	"TMPDIR":  true,
	"TEMP":    true,
	"TMP":     true,
	"GOPATH":      true,
	"GOROOT":      true,
	"USERPROFILE": true, // Windows HOME equivalent
}

// varRe matches braced variable references: ${VAR_NAME}.
// Bare $VAR is intentionally NOT matched to avoid false positives in shell args.
var varRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ExpandVar replaces all ${VAR} references in s using envMap, with restricted
// os.Getenv fallback for allowlisted vars only. Missing vars resolve to "".
func ExpandVar(s string, envMap map[string]string) string {
	return varRe.ReplaceAllStringFunc(s, func(match string) string {
		// Extract variable name from ${NAME}.
		sub := varRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		name := sub[1]

		// 1. Check explicit envMap first.
		if v, ok := envMap[name]; ok {
			return v
		}

		// 2. Restricted os.Getenv fallback — allowlisted vars only.
		if expandVarAllowlist[name] {
			return os.Getenv(name)
		}

		// 3. Not found anywhere — resolve to empty string (triggers validation
		// errors for required fields, providing fail-fast on missing vars).
		return ""
	})
}

// ExpandServerConfig applies ${VAR} expansion to all string fields of a
// ServerConfig. Returns an error if any expanded env value contains newlines
// (prevents env injection via multi-line values).
func ExpandServerConfig(sc *models.ServerConfig, envMap map[string]string) error {
	sc.Command = ExpandVar(sc.Command, envMap)
	sc.Cwd = ExpandVar(sc.Cwd, envMap)
	sc.URL = ExpandVar(sc.URL, envMap)
	sc.RestURL = ExpandVar(sc.RestURL, envMap)
	sc.HealthEndpoint = ExpandVar(sc.HealthEndpoint, envMap)

	// Expand Args.
	for i, a := range sc.Args {
		sc.Args[i] = ExpandVar(a, envMap)
	}

	// Expand Env values (VALUE part of KEY=VALUE; keys are NOT expanded).
	for i, e := range sc.Env {
		key, val, ok := strings.Cut(e, "=")
		if !ok {
			continue // malformed — will be caught by validation
		}
		expanded := ExpandVar(val, envMap)
		// Reject newlines in expanded env values (prevents env injection).
		if strings.ContainsAny(expanded, "\n\r\x00") {
			return fmt.Errorf("expanded env value for key %q contains illegal characters (newline or NUL)", key)
		}
		sc.Env[i] = key + "=" + expanded
	}

	// Expand Header values (NOT keys).
	// Reject newlines in expanded header values (prevents HTTP header injection — CWE-113).
	for k, v := range sc.Headers {
		expanded := ExpandVar(v, envMap)
		if strings.ContainsAny(expanded, "\n\r\x00") {
			return fmt.Errorf("expanded header value for key %q contains illegal characters (newline or NUL)", k)
		}
		sc.Headers[k] = expanded
	}

	return nil
}

// ExpandConfig applies ${VAR} expansion to all servers in cfg.
// Returns the first error encountered (e.g. newline in env value).
func ExpandConfig(cfg *models.Config, envMap map[string]string) error {
	for name, sc := range cfg.Servers {
		if sc == nil {
			return fmt.Errorf("server %q: nil config entry", name)
		}
		if err := ExpandServerConfig(sc, envMap); err != nil {
			return fmt.Errorf("server %q: %w", name, err)
		}
	}
	return nil
}
