package sapcreds_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"mcp-gateway/internal/keepass"
	"mcp-gateway/internal/sapcreds"

	gokeepasslib "github.com/tobischo/gokeepasslib/v3"
	w "github.com/tobischo/gokeepasslib/v3/wrappers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test helpers — KDBX fixture builders (mirrors the PoC helpers in keepass_poc_test.go)
// ---------------------------------------------------------------------------

func buildKDBX(t *testing.T, password string, regularTitles []string, withRecycleBin bool, recycleBinTitle string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "vault.kdbx")

	regular := gokeepasslib.NewGroup()
	regular.Name = "SAP"
	for _, title := range regularTitles {
		regular.Entries = append(regular.Entries, buildEntry(title, "alice", "secret-pw"))
	}

	root := gokeepasslib.NewGroup()
	root.Name = "Root"
	root.Groups = []gokeepasslib.Group{regular}

	meta := gokeepasslib.NewMetaData()

	if withRecycleBin {
		bin := gokeepasslib.NewGroup()
		bin.Name = "Recycle Bin"
		if recycleBinTitle != "" {
			bin.Entries = []gokeepasslib.Entry{buildEntry(recycleBinTitle, "deleted", "gone")}
		}
		meta.RecycleBinEnabled = w.NewBoolWrapper(true)
		meta.RecycleBinUUID = bin.UUID
		root.Groups = append(root.Groups, bin)
	}

	db := gokeepasslib.NewDatabase()
	db.Credentials = gokeepasslib.NewPasswordCredentials(password)
	db.Content.Meta = meta
	db.Content.Root = &gokeepasslib.RootData{Groups: []gokeepasslib.Group{root}}
	db.LockProtectedEntries()

	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	require.NoError(t, gokeepasslib.NewEncoder(f).Encode(db))

	return path
}

func buildEntry(title, user, password string) gokeepasslib.Entry {
	e := gokeepasslib.NewEntry()
	e.Values = []gokeepasslib.ValueData{
		{Key: "Title", Value: gokeepasslib.V{Content: title}},
		{Key: "UserName", Value: gokeepasslib.V{Content: user}},
		{Key: "Password", Value: gokeepasslib.V{Content: password, Protected: w.NewBoolWrapper(true)}},
	}
	return e
}

// ---------------------------------------------------------------------------
// ListEntries tests
// ---------------------------------------------------------------------------

// TestListEntries_Empty verifies that an empty vault returns an empty slice.
func TestListEntries_Empty(t *testing.T) {
	path := buildKDBX(t, "pw", nil, false, "")
	entries, err := sapcreds.ListEntries(path, "pw", "")
	require.NoError(t, err)
	assert.Empty(t, entries, "empty vault must return empty entry list")
}

// TestListEntries_RecycleBinFiltered verifies that entries in the recycle-bin
// group are excluded from the returned list.
func TestListEntries_RecycleBinFiltered(t *testing.T) {
	path := buildKDBX(t, "pw", []string{"vsp-PRD-100"}, true, "vsp-DEL-999")
	entries, err := sapcreds.ListEntries(path, "pw", "")
	require.NoError(t, err)

	titles := make(map[string]bool)
	for _, e := range entries {
		titles[e.Title] = true
	}
	assert.True(t, titles["vsp-PRD-100"], "regular entry must be present")
	assert.False(t, titles["vsp-DEL-999"], "recycle-bin entry must be filtered out")
}

// TestListEntries_SAPNameParsed verifies that SAP server-name grammar entries
// get their SID and Client parsed correctly.
func TestListEntries_SAPNameParsed(t *testing.T) {
	path := buildKDBX(t, "pw", []string{"vsp-PRD-100", "sap-gui-QAS", "not-a-sap-entry"}, false, "")
	entries, err := sapcreds.ListEntries(path, "pw", "")
	require.NoError(t, err)

	byTitle := make(map[string]sapcreds.Entry)
	for _, e := range entries {
		byTitle[e.Title] = e
	}

	prd := byTitle["vsp-PRD-100"]
	assert.Equal(t, "PRD", prd.SID)
	assert.Equal(t, "100", prd.Client)

	qas := byTitle["sap-gui-QAS"]
	assert.Equal(t, "QAS", qas.SID)
	assert.Equal(t, "", qas.Client)

	plain := byTitle["not-a-sap-entry"]
	assert.Equal(t, "", plain.SID, "non-SAP title must have empty SID")
}

// TestListEntries_LockedVault verifies that a missing-credentials error wraps
// keepass.ErrNoCredentials.
func TestListEntries_LockedVault(t *testing.T) {
	path := buildKDBX(t, "pw", []string{"vsp-PRD-100"}, false, "")
	_, err := sapcreds.ListEntries(path, "", "")
	require.Error(t, err)
	assert.True(t, sapcreds.IsErrNoCredentials(err),
		"locked vault must return an error wrapping keepass.ErrNoCredentials, got: %v", err)
}

// TestListEntries_WrongPassword verifies that a wrong password returns a decode
// error that does NOT wrap keepass.ErrNoCredentials.
func TestListEntries_WrongPassword(t *testing.T) {
	path := buildKDBX(t, "correct", []string{"vsp-PRD-100"}, false, "")
	_, err := sapcreds.ListEntries(path, "wrong", "")
	require.Error(t, err)
	assert.False(t, sapcreds.IsErrNoCredentials(err),
		"wrong password must NOT be ErrNoCredentials, got: %v", err)
	got := err.Error()
	assert.True(t,
		strings.Contains(got, "decode KDBX") || strings.Contains(got, "malformed KDBX") || strings.Contains(got, "HMAC"),
		"wrong-password error must be a decode failure, got: %v", err)
}

// Suppress the GC workaround warning from linters by using runtime explicitly.
var _ = runtime.GC

// TestListEntries_MissingKeyFile verifies that a missing key file produces an
// error mentioning "key file".
func TestListEntries_MissingKeyFile(t *testing.T) {
	path := buildKDBX(t, "pw", nil, false, "")
	_, err := sapcreds.ListEntries(path, "pw", filepath.Join(t.TempDir(), "missing.keyx"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key file")
}

// Compile-time check: keepass.ErrNoCredentials sentinel is visible via sapcreds.
var _ = keepass.ErrNoCredentials
