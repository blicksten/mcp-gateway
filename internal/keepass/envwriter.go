package keepass

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WriteEnvFile writes server credentials to a .env file.
// Preserves comments and blank lines from the original file.
// Creates the file with 0600 permissions if it doesn't exist.
// Uses atomic write via temp file in the same directory.
// Values are double-quoted with $ escaped as \$ and " escaped as \".
// HDR_ entries are written as env vars with a warning comment.
func WriteEnvFile(path string, creds []ServerCredentials) error {
	existing, comments, err := readExistingEnv(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read existing env file: %w", err)
	}

	// Merge new credentials into existing map.
	hasHDR := false
	for _, sc := range creds {
		for k, v := range sc.EnvVars {
			existing[k] = v
			if strings.Contains(k, "_HDR_") {
				hasHDR = true
			}
		}
		// In env-file mode, headers are written as env vars with HDR_ prefix.
		for hdrName, hdrVal := range sc.Headers {
			prefix := envPrefix(sc.ServerName)
			key := prefix + "_HDR_" + envSanitize(hdrName)
			existing[key] = hdrVal
			hasHDR = true
		}
	}

	// Build output. Use os.CreateTemp in the same directory for an unpredictable
	// temp file name and atomic rename (same filesystem).
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".env-import-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := f.Name()
	if chErr := f.Chmod(0600); chErr != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("set temp file permissions: %w", chErr)
	}

	w := bufio.NewWriter(f)

	// Write preserved comments and blank lines first.
	for _, line := range comments {
		fmt.Fprintln(w, line)
	}

	if hasHDR {
		fmt.Fprintln(w, "# WARNING: HDR_ entries are env vars, not HTTP headers.")
		fmt.Fprintln(w, "# To use as headers, reference them in server config: \"Authorization\": \"${SERVER_HDR_AUTHORIZATION}\"")
	}

	// Write env vars sorted by key for deterministic output.
	keys := make([]string, 0, len(existing))
	for k := range existing {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		v, vErr := escapeEnvValue(existing[k])
		if vErr != nil {
			f.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("env key %q: %w", k, vErr)
		}
		fmt.Fprintf(w, "%s=\"%s\"\n", k, v)
	}

	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp file: %w", err)
	}

	return nil
}

// readExistingEnv reads an existing .env file, returning key-value pairs and
// comment/blank lines to preserve.
func readExistingEnv(path string) (kvs map[string]string, comments []string, err error) {
	kvs = make(map[string]string)

	f, err := os.Open(path)
	if err != nil {
		return kvs, nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			comments = append(comments, line)
			continue
		}

		key, val, ok := strings.Cut(trimmed, "=")
		if !ok {
			comments = append(comments, line) // preserve malformed lines
			continue
		}
		// Unquote the value if quoted.
		val = strings.TrimSpace(val)
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
			val = strings.ReplaceAll(val, `\"`, `"`)
			val = strings.ReplaceAll(val, `\$`, "$")
			val = strings.ReplaceAll(val, `\\`, `\`)
		}
		kvs[strings.TrimSpace(key)] = val
	}

	return kvs, comments, scanner.Err()
}

// escapeEnvValue escapes a value for double-quoted env file format.
// Rejects values containing CR, LF, or NUL (would corrupt the env file).
// $ is escaped as \$ to prevent godotenv intra-file interpolation.
// " is escaped as \".
func escapeEnvValue(v string) (string, error) {
	if strings.ContainsAny(v, "\r\n\x00") {
		return "", fmt.Errorf("value contains invalid characters (CR/LF/NUL)")
	}
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	v = strings.ReplaceAll(v, "$", `\$`)
	return v, nil
}
