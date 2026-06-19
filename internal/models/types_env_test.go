package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestServerConfig_SAPEnvURL covers the SAP_URL accessor used by the TASK C1
// stdio reachability gate (internal/lifecycle/stdio_skip.go). The accessor is a
// dependency of the orphan-process-fix work; this test pins its contract.
func TestServerConfig_SAPEnvURL(t *testing.T) {
	tests := []struct {
		name    string
		env     []string
		wantURL string
		wantOK  bool
	}{
		{
			name:    "present",
			env:     []string{"SAP_URL=sapserver.example.com:3200"},
			wantURL: "sapserver.example.com:3200",
			wantOK:  true,
		},
		{
			name:    "present among other entries",
			env:     []string{"SAP_USER=DEVELOPER", "SAP_URL=host:3200", "SAP_CLIENT=100"},
			wantURL: "host:3200",
			wantOK:  true,
		},
		{
			name:    "absent",
			env:     []string{"SAP_USER=DEVELOPER", "SAP_CLIENT=100"},
			wantURL: "",
			wantOK:  false,
		},
		{
			name:    "empty value treated as absent",
			env:     []string{"SAP_URL="},
			wantURL: "",
			wantOK:  false,
		},
		{
			name:    "nil env",
			env:     nil,
			wantURL: "",
			wantOK:  false,
		},
		{
			name:    "malformed entry without equals is skipped",
			env:     []string{"SAP_URL", "SAP_URL=host:3200"},
			wantURL: "host:3200",
			wantOK:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sc := &ServerConfig{Env: tc.env}
			url, ok := sc.SAPEnvURL()
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantURL, url)
		})
	}
}
