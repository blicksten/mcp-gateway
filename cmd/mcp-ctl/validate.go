package main

import (
	"mcp-gateway/internal/models"
	"strings"
)

// validateServerName trims whitespace and validates the server name format.
// Returns the trimmed name or an error.
func validateServerName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if err := models.ValidateServerName(name); err != nil {
		return "", err
	}
	return name, nil
}
