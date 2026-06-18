package models

// Regression tests for ServerConfig.SAPEnvURL (BUG-STDIO-1/3 fix).
//
// These are pure-function table tests — no I/O, no goroutines, no timeouts.
// Each sub-test pins a distinct semantic of the strings.Cut-based parser so a
// future refactor cannot silently drop a case.
//
// Fail-without-fix property: before the fix SAPEnvURL did not exist; every
// test here would fail at compile time ("sc.SAPEnvURL undefined").

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSAPEnvURL_PresentSingleEntry(t *testing.T) {
	sc := &ServerConfig{
		Env: []string{"SAP_URL=http://saphost:50000"},
	}
	got, ok := sc.SAPEnvURL()
	assert.True(t, ok, "SAP_URL present: ok must be true")
	assert.Equal(t, "http://saphost:50000", got)
}

func TestSAPEnvURL_AbsentReturnsEmpty(t *testing.T) {
	sc := &ServerConfig{
		Env: []string{"OTHER_VAR=value", "ANOTHER=123"},
	}
	got, ok := sc.SAPEnvURL()
	assert.False(t, ok, "no SAP_URL: ok must be false")
	assert.Equal(t, "", got)
}

func TestSAPEnvURL_EmptyEnv(t *testing.T) {
	sc := &ServerConfig{}
	got, ok := sc.SAPEnvURL()
	assert.False(t, ok)
	assert.Equal(t, "", got)
}

func TestSAPEnvURL_MultipleEntriesSAPURLFirst(t *testing.T) {
	sc := &ServerConfig{
		Env: []string{
			"SAP_URL=http://first:50000",
			"OTHER=foo",
		},
	}
	got, ok := sc.SAPEnvURL()
	assert.True(t, ok)
	assert.Equal(t, "http://first:50000", got)
}

func TestSAPEnvURL_MultipleEntriesSAPURLLast(t *testing.T) {
	sc := &ServerConfig{
		Env: []string{
			"OTHER=foo",
			"THIRD=bar",
			"SAP_URL=http://last:50000",
		},
	}
	got, ok := sc.SAPEnvURL()
	assert.True(t, ok)
	assert.Equal(t, "http://last:50000", got)
}

// TestSAPEnvURL_ValueContainsEquals ensures strings.Cut splits on the FIRST '='
// only: a SAP URL with query params (e.g. ?key=value) must be preserved verbatim.
func TestSAPEnvURL_ValueContainsEquals(t *testing.T) {
	sc := &ServerConfig{
		Env: []string{"SAP_URL=http://saphost:50000/path?a=1&b=2"},
	}
	got, ok := sc.SAPEnvURL()
	assert.True(t, ok, "SAP_URL with '=' in value must still be found")
	assert.Equal(t, "http://saphost:50000/path?a=1&b=2", got,
		"value after first '=' separator must be returned intact")
}

// TestSAPEnvURL_EmptyValueSkipped: an entry "SAP_URL=" (empty value) must NOT
// match; callers rely on the returned URL being non-empty (ok=false contract).
func TestSAPEnvURL_EmptyValueSkipped(t *testing.T) {
	sc := &ServerConfig{
		Env: []string{"SAP_URL="},
	}
	got, ok := sc.SAPEnvURL()
	assert.False(t, ok, "SAP_URL with empty value must not match (ok=false)")
	assert.Equal(t, "", got)
}

// TestSAPEnvURL_TableDriven collects the full semantic matrix in one place for
// quick scanning during code review.
func TestSAPEnvURL_TableDriven(t *testing.T) {
	tests := []struct {
		name    string
		env     []string
		wantVal string
		wantOK  bool
	}{
		{
			name:    "single SAP_URL",
			env:     []string{"SAP_URL=http://sap.corp:50000"},
			wantVal: "http://sap.corp:50000",
			wantOK:  true,
		},
		{
			name:    "SAP_URL with equals in value",
			env:     []string{"SAP_URL=http://sap.corp:50000/?client=100&lang=EN"},
			wantVal: "http://sap.corp:50000/?client=100&lang=EN",
			wantOK:  true,
		},
		{
			name:    "absent",
			env:     []string{"FOO=bar"},
			wantVal: "",
			wantOK:  false,
		},
		{
			name:    "empty env slice",
			env:     nil,
			wantVal: "",
			wantOK:  false,
		},
		{
			name:    "empty value",
			env:     []string{"SAP_URL="},
			wantVal: "",
			wantOK:  false,
		},
		{
			name:    "similar prefix does not match",
			env:     []string{"SAP_URL_BACKUP=http://sap.corp:50001"},
			wantVal: "",
			wantOK:  false,
		},
		{
			name:    "lowercase key does not match",
			env:     []string{"sap_url=http://sap.corp:50000"},
			wantVal: "",
			wantOK:  false,
		},
		{
			name:    "mixed env: SAP_URL among others",
			env:     []string{"A=1", "SAP_URL=http://s:8000", "B=2"},
			wantVal: "http://s:8000",
			wantOK:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := &ServerConfig{Env: tt.env}
			got, ok := sc.SAPEnvURL()
			assert.Equal(t, tt.wantOK, ok, "ok mismatch")
			assert.Equal(t, tt.wantVal, got, "value mismatch")
		})
	}
}
