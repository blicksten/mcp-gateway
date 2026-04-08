package config

import (
	"bytes"
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

// LoadEnvFile reads a .env file and returns a map of key-value pairs.
// It does NOT load variables into the process environment, and does NOT
// interpolate ${VAR} from os.Environ() (preventing process secret leakage).
// This is achieved by using godotenv.Parse instead of godotenv.Read.
//
// Note: godotenv.Parse DOES perform intra-file interpolation — a variable
// defined earlier in the .env file can be referenced by a later variable
// (e.g., BASE=http://host then URL=${BASE}/path resolves URL to http://host/path).
// This is intentional godotenv behavior and is useful for DRY .env files.
// The critical security property is: process env vars do NOT leak in.
//
// Returns an empty (non-nil) map when path is empty (no env file specified).
func LoadEnvFile(path string) (map[string]string, error) {
	if path == "" {
		return map[string]string{}, nil
	}

	path = expandHome(path)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("env file %s: %w", path, err)
	}

	envMap, err := godotenv.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("parse env file %s: %w", path, err)
	}

	return envMap, nil
}
