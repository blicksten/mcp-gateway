package keepass

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMapToCredentials_Basic verifies that a minimal entry is mapped correctly:
// Title → ServerName, Password → PREFIX_PASSWORD, User → PREFIX_USER.
func TestMapToCredentials_Basic(t *testing.T) {
	entries := []KeePassEntry{
		{
			Title:        "my-server",
			User:         "admin",
			Password:     []byte("pw"),
			CustomFields: map[string]string{},
		},
	}

	creds, err := MapToCredentials(entries)
	require.NoError(t, err)
	require.Len(t, creds, 1)

	sc := creds[0]
	assert.Equal(t, "my-server", sc.ServerName)
	assert.Equal(t, "pw", sc.EnvVars["MY_SERVER_PASSWORD"])
	assert.Equal(t, "admin", sc.EnvVars["MY_SERVER_USER"])
}

// TestMapToCredentials_HDR_Prefix verifies that a custom field with "HDR_" prefix
// is separated into Headers (with the prefix stripped from the key).
func TestMapToCredentials_HDR_Prefix(t *testing.T) {
	entries := []KeePassEntry{
		{
			Title:        "api",
			User:         "",
			Password:     []byte{},
			CustomFields: map[string]string{"HDR_Authorization": "Bearer token123"},
		},
	}

	creds, err := MapToCredentials(entries)
	require.NoError(t, err)
	require.Len(t, creds, 1)

	sc := creds[0]
	assert.Equal(t, "Bearer token123", sc.Headers["Authorization"])
	// Must not appear in EnvVars.
	assert.NotContains(t, sc.EnvVars, "API_HDR_AUTHORIZATION")
	assert.NotContains(t, sc.EnvVars, "HDR_AUTHORIZATION")
}

// TestMapToCredentials_HyphenToUnderscore verifies that hyphens in the title are
// converted to underscores when forming the env var prefix.
func TestMapToCredentials_HyphenToUnderscore(t *testing.T) {
	entries := []KeePassEntry{
		{
			Title:        "my-server",
			User:         "u",
			Password:     []byte("p"),
			CustomFields: map[string]string{},
		},
	}

	creds, err := MapToCredentials(entries)
	require.NoError(t, err)
	require.Len(t, creds, 1)

	sc := creds[0]
	// Prefix must be MY_SERVER (not MY-SERVER).
	assert.Contains(t, sc.EnvVars, "MY_SERVER_PASSWORD")
	assert.Contains(t, sc.EnvVars, "MY_SERVER_USER")
	// Must not contain a hyphen in any key.
	for k := range sc.EnvVars {
		assert.NotContains(t, k, "-", "env var key must not contain hyphens: %q", k)
	}
}

// TestMapToCredentials_PrefixCollision verifies that two entries whose titles
// produce the same env var prefix ("my-server" and "my_server" both → MY_SERVER)
// result in an error.
func TestMapToCredentials_PrefixCollision(t *testing.T) {
	entries := []KeePassEntry{
		{Title: "my-server", User: "a", Password: []byte("p1"), CustomFields: map[string]string{}},
		{Title: "my_server", User: "b", Password: []byte("p2"), CustomFields: map[string]string{}},
	}

	_, err := MapToCredentials(entries)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MY_SERVER")
}

// TestMapToCredentials_CustomFields verifies that a non-HDR custom field is mapped
// to a {PREFIX}_{SANITIZED_FIELD} env var.
func TestMapToCredentials_CustomFields(t *testing.T) {
	entries := []KeePassEntry{
		{
			Title:    "my-server",
			User:     "",
			Password: []byte{},
			CustomFields: map[string]string{
				"api_key": "secretvalue",
			},
		},
	}

	creds, err := MapToCredentials(entries)
	require.NoError(t, err)
	require.Len(t, creds, 1)

	sc := creds[0]
	assert.Equal(t, "secretvalue", sc.EnvVars["MY_SERVER_API_KEY"])
}

// TestMapToCredentials_EmptyPassword verifies that when Password is nil or empty,
// no _PASSWORD key is written to EnvVars.
func TestMapToCredentials_EmptyPassword(t *testing.T) {
	tests := []struct {
		name     string
		password []byte
	}{
		{"nil password", nil},
		{"empty slice", []byte{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entries := []KeePassEntry{
				{
					Title:        "srv",
					User:         "u",
					Password:     tc.password,
					CustomFields: map[string]string{},
				},
			}

			creds, err := MapToCredentials(entries)
			require.NoError(t, err)
			require.Len(t, creds, 1)

			sc := creds[0]
			assert.NotContains(t, sc.EnvVars, "SRV_PASSWORD",
				"_PASSWORD key must be absent when password is empty")
			// User should still be present.
			assert.Equal(t, "u", sc.EnvVars["SRV_USER"])
		})
	}
}

// TestMapToCredentials_KDBXValueContainingDollar verifies that a value containing
// "${VAR}" is stored verbatim in EnvVars — escaping is envwriter's responsibility.
func TestMapToCredentials_KDBXValueContainingDollar(t *testing.T) {
	entries := []KeePassEntry{
		{
			Title:    "srv",
			User:     "",
			Password: []byte{},
			CustomFields: map[string]string{
				"token": "${SOME_VAR}",
			},
		},
	}

	creds, err := MapToCredentials(entries)
	require.NoError(t, err)
	require.Len(t, creds, 1)

	sc := creds[0]
	// Value must be stored as-is; no escaping at the mapper layer.
	assert.Equal(t, "${SOME_VAR}", sc.EnvVars["SRV_TOKEN"])
}

// TestMapToCredentials_EmptyEntries verifies that an empty input slice returns an
// empty result without error.
func TestMapToCredentials_EmptyEntries(t *testing.T) {
	creds, err := MapToCredentials([]KeePassEntry{})
	require.NoError(t, err)
	assert.Empty(t, creds)
}

// TestMapToCredentials_NilEntries verifies that a nil input is safe.
func TestMapToCredentials_NilEntries(t *testing.T) {
	creds, err := MapToCredentials(nil)
	require.NoError(t, err)
	assert.Empty(t, creds)
}

// TestMapToCredentials_MultipleEntries verifies that multiple non-colliding entries
// are all mapped and returned in order.
func TestMapToCredentials_MultipleEntries(t *testing.T) {
	entries := []KeePassEntry{
		{Title: "alpha", User: "ua", Password: []byte("pa"), CustomFields: map[string]string{}},
		{Title: "beta", User: "ub", Password: []byte("pb"), CustomFields: map[string]string{}},
	}

	creds, err := MapToCredentials(entries)
	require.NoError(t, err)
	require.Len(t, creds, 2)

	assert.Equal(t, "alpha", creds[0].ServerName)
	assert.Equal(t, "pa", creds[0].EnvVars["ALPHA_PASSWORD"])

	assert.Equal(t, "beta", creds[1].ServerName)
	assert.Equal(t, "pb", creds[1].EnvVars["BETA_PASSWORD"])
}

// TestMapToCredentials_HDR_EmptyHeaderName verifies that "HDR_" with an empty name
// after stripping the prefix does NOT produce an empty-key header.
func TestMapToCredentials_HDR_EmptyHeaderName(t *testing.T) {
	entries := []KeePassEntry{
		{
			Title:        "srv",
			User:         "",
			Password:     []byte{},
			CustomFields: map[string]string{"HDR_": "ignored"},
		},
	}

	creds, err := MapToCredentials(entries)
	require.NoError(t, err)
	require.Len(t, creds, 1)

	sc := creds[0]
	// Empty header name must be discarded.
	assert.NotContains(t, sc.Headers, "", "empty header key must not be inserted")
}

// TestMapToCredentials_CustomField_NonAlnumChars verifies that non-alphanumeric
// characters in a custom field name are converted to underscores.
func TestMapToCredentials_CustomField_NonAlnumChars(t *testing.T) {
	entries := []KeePassEntry{
		{
			Title:    "srv",
			User:     "",
			Password: []byte{},
			CustomFields: map[string]string{
				"my-field.name": "val",
			},
		},
	}

	creds, err := MapToCredentials(entries)
	require.NoError(t, err)
	require.Len(t, creds, 1)

	sc := creds[0]
	// "my-field.name" → "MY_FIELD_NAME"
	assert.Equal(t, "val", sc.EnvVars["SRV_MY_FIELD_NAME"])
}

// TestMapToCredentials_EmptyUser verifies that when User is empty, no _USER key
// is written to EnvVars.
func TestMapToCredentials_EmptyUser(t *testing.T) {
	entries := []KeePassEntry{
		{
			Title:        "srv",
			User:         "",
			Password:     []byte("pw"),
			CustomFields: map[string]string{},
		},
	}

	creds, err := MapToCredentials(entries)
	require.NoError(t, err)
	require.Len(t, creds, 1)

	sc := creds[0]
	assert.NotContains(t, sc.EnvVars, "SRV_USER",
		"_USER key must be absent when user is empty")
	assert.Equal(t, "pw", sc.EnvVars["SRV_PASSWORD"])
}
