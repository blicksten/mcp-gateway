package keepass

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tobischo/gokeepasslib/v3"
	w "github.com/tobischo/gokeepasslib/v3/wrappers"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func mkValue(key, value string) gokeepasslib.ValueData {
	return gokeepasslib.ValueData{Key: key, Value: gokeepasslib.V{Content: value}}
}

func mkProtectedValue(key, value string) gokeepasslib.ValueData {
	return gokeepasslib.ValueData{
		Key:   key,
		Value: gokeepasslib.V{Content: value, Protected: w.NewBoolWrapper(true)},
	}
}

// createTestKDBX writes a KDBX file with a single root group that contains
// the given entries. Returns the file path.
func createTestKDBX(t *testing.T, password string, rootEntries []gokeepasslib.Entry, extraGroups []gokeepasslib.Group) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.kdbx")

	rootGroup := gokeepasslib.NewGroup()
	rootGroup.Name = "Root"
	rootGroup.Entries = rootEntries
	rootGroup.Groups = extraGroups

	db := &gokeepasslib.Database{
		Header:      gokeepasslib.NewHeader(),
		Credentials: gokeepasslib.NewPasswordCredentials(password),
		Content: &gokeepasslib.DBContent{
			Meta: gokeepasslib.NewMetaData(),
			Root: &gokeepasslib.RootData{
				Groups: []gokeepasslib.Group{rootGroup},
			},
		},
	}

	db.LockProtectedEntries()

	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	require.NoError(t, gokeepasslib.NewEncoder(f).Encode(db))
	return path
}

// simpleEntry builds an entry with Title, UserName, and Password fields.
func simpleEntry(title, user, password string) gokeepasslib.Entry {
	e := gokeepasslib.NewEntry()
	e.Values = []gokeepasslib.ValueData{
		mkValue("Title", title),
		mkValue("UserName", user),
		mkProtectedValue("Password", password),
	}
	return e
}

// ---------------------------------------------------------------------------
// OpenDatabase
// ---------------------------------------------------------------------------

func TestOpenDatabase_Success(t *testing.T) {
	path := createTestKDBX(t, "master", []gokeepasslib.Entry{simpleEntry("my-server", "admin", "secret123")}, nil)

	db, err := OpenDatabase(path, []byte("master"), "")

	require.NoError(t, err)
	require.NotNil(t, db)
	require.NotNil(t, db.Content)
	require.NotNil(t, db.Content.Root)
	require.NotEmpty(t, db.Content.Root.Groups)
}

func TestOpenDatabase_WrongPassword(t *testing.T) {
	path := createTestKDBX(t, "correct-password", nil, nil)

	db, err := OpenDatabase(path, []byte("wrong-password"), "")

	assert.Error(t, err)
	assert.Nil(t, db)
}

func TestOpenDatabase_FileNotFound(t *testing.T) {
	nonExistent := filepath.Join(t.TempDir(), "does-not-exist.kdbx")

	db, err := OpenDatabase(nonExistent, []byte("password"), "")

	assert.Error(t, err)
	assert.Nil(t, db)
}

func TestOpenDatabase_KeyFileIsDir(t *testing.T) {
	path := createTestKDBX(t, "master", nil, nil)
	dirPath := t.TempDir() // a directory, not a file

	db, err := OpenDatabase(path, []byte("master"), dirPath)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "is a directory")
	assert.Nil(t, db)
}

func TestOpenDatabase_MalformedKDBX(t *testing.T) {
	// Write garbage bytes — must return an error, never panic.
	path := filepath.Join(t.TempDir(), "garbage.kdbx")
	require.NoError(t, os.WriteFile(path, []byte("this is not a kdbx file!!!"), 0o600))

	db, err := OpenDatabase(path, []byte("anypassword"), "")

	assert.Error(t, err)
	assert.Nil(t, db)
}

// ---------------------------------------------------------------------------
// ExtractEntries
// ---------------------------------------------------------------------------

func TestExtractEntries_BasicFields(t *testing.T) {
	entries := []gokeepasslib.Entry{simpleEntry("my-server", "admin", "secret123")}
	path := createTestKDBX(t, "master", entries, nil)

	db, err := OpenDatabase(path, []byte("master"), "")
	require.NoError(t, err)

	got := ExtractEntries(db, "")

	require.Len(t, got, 1)
	assert.Equal(t, "my-server", got[0].Title)
	assert.Equal(t, "admin", got[0].User)
	assert.Equal(t, []byte("secret123"), got[0].Password)
	assert.Equal(t, "Root", got[0].Group)
}

func TestExtractEntries_CustomFields(t *testing.T) {
	e := gokeepasslib.NewEntry()
	e.Values = []gokeepasslib.ValueData{
		mkValue("Title", "my-server"),
		mkValue("UserName", "admin"),
		mkProtectedValue("Password", "secret123"),
		mkValue("URL", "https://example.com"),
		mkValue("Notes", "some notes"),
		mkValue("CustomField", "custom-value"),
		mkValue("AnotherField", "another-value"),
	}

	path := createTestKDBX(t, "master", []gokeepasslib.Entry{e}, nil)
	db, err := OpenDatabase(path, []byte("master"), "")
	require.NoError(t, err)

	got := ExtractEntries(db, "")

	require.Len(t, got, 1)
	entry := got[0]

	// Standard fields must NOT appear in CustomFields.
	assert.NotContains(t, entry.CustomFields, "Title")
	assert.NotContains(t, entry.CustomFields, "UserName")
	assert.NotContains(t, entry.CustomFields, "Password")
	assert.NotContains(t, entry.CustomFields, "URL")
	assert.NotContains(t, entry.CustomFields, "Notes")

	// Custom fields must be present.
	assert.Equal(t, "custom-value", entry.CustomFields["CustomField"])
	assert.Equal(t, "another-value", entry.CustomFields["AnotherField"])
}

func TestExtractEntries_GroupFilter_MatchingGroup(t *testing.T) {
	// Root group has entry A; sub-group "Servers" has entry B.
	serverGroup := gokeepasslib.NewGroup()
	serverGroup.Name = "Servers"
	serverGroup.Entries = []gokeepasslib.Entry{simpleEntry("server-entry", "root", "pass2")}

	rootEntries := []gokeepasslib.Entry{simpleEntry("root-entry", "user1", "pass1")}

	path := createTestKDBX(t, "master", rootEntries, []gokeepasslib.Group{serverGroup})
	db, err := OpenDatabase(path, []byte("master"), "")
	require.NoError(t, err)

	got := ExtractEntries(db, "Servers")

	require.Len(t, got, 1)
	assert.Equal(t, "server-entry", got[0].Title)
	assert.Equal(t, "Root/Servers", got[0].Group)
}

func TestExtractEntries_GroupFilter_NoMatch(t *testing.T) {
	entries := []gokeepasslib.Entry{simpleEntry("my-server", "admin", "secret")}
	path := createTestKDBX(t, "master", entries, nil)
	db, err := OpenDatabase(path, []byte("master"), "")
	require.NoError(t, err)

	got := ExtractEntries(db, "NonExistentGroup")

	assert.Empty(t, got)
}

func TestExtractEntries_GroupFilter_EmptyReturnsAll(t *testing.T) {
	serverGroup := gokeepasslib.NewGroup()
	serverGroup.Name = "Servers"
	serverGroup.Entries = []gokeepasslib.Entry{simpleEntry("server-entry", "root", "pass2")}

	rootEntries := []gokeepasslib.Entry{simpleEntry("root-entry", "user1", "pass1")}

	path := createTestKDBX(t, "master", rootEntries, []gokeepasslib.Group{serverGroup})
	db, err := OpenDatabase(path, []byte("master"), "")
	require.NoError(t, err)

	got := ExtractEntries(db, "")

	assert.Len(t, got, 2)
}

func TestExtractEntries_SkipsUntitledEntries(t *testing.T) {
	titled := simpleEntry("has-title", "user", "pass")

	untitled := gokeepasslib.NewEntry()
	untitled.Values = []gokeepasslib.ValueData{
		mkValue("UserName", "ghost"),
		mkProtectedValue("Password", "ghost-pass"),
		// intentionally no Title field
	}

	path := createTestKDBX(t, "master", []gokeepasslib.Entry{titled, untitled}, nil)
	db, err := OpenDatabase(path, []byte("master"), "")
	require.NoError(t, err)

	got := ExtractEntries(db, "")

	require.Len(t, got, 1)
	assert.Equal(t, "has-title", got[0].Title)
}

func TestExtractEntries_EmptyDatabase(t *testing.T) {
	path := createTestKDBX(t, "master", nil, nil)
	db, err := OpenDatabase(path, []byte("master"), "")
	require.NoError(t, err)

	got := ExtractEntries(db, "")

	assert.Empty(t, got)
}

// ---------------------------------------------------------------------------
// ReadPasswordFile
// ---------------------------------------------------------------------------

func TestReadPasswordFile_PlainContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password.txt")
	require.NoError(t, os.WriteFile(path, []byte("mysecret"), 0o600))

	got, err := ReadPasswordFile(path)

	require.NoError(t, err)
	assert.Equal(t, []byte("mysecret"), got)
}

func TestReadPasswordFile_TrimsTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password.txt")
	require.NoError(t, os.WriteFile(path, []byte("mysecret\n"), 0o600))

	got, err := ReadPasswordFile(path)

	require.NoError(t, err)
	assert.Equal(t, []byte("mysecret"), got)
}

func TestReadPasswordFile_TrimsTrailingCRLF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password.txt")
	require.NoError(t, os.WriteFile(path, []byte("mysecret\r\n"), 0o600))

	got, err := ReadPasswordFile(path)

	require.NoError(t, err)
	assert.Equal(t, []byte("mysecret"), got)
}

func TestReadPasswordFile_FileNotFound(t *testing.T) {
	nonExistent := filepath.Join(t.TempDir(), "no-such-file.txt")

	got, err := ReadPasswordFile(nonExistent)

	assert.Error(t, err)
	assert.Nil(t, got)
}

func TestReadPasswordFile_EmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.txt")
	require.NoError(t, os.WriteFile(path, []byte(""), 0o600))

	got, err := ReadPasswordFile(path)

	require.NoError(t, err)
	assert.Equal(t, []byte(""), got)
}

// ---------------------------------------------------------------------------
// KeePassEntry.ZeroPassword
// ---------------------------------------------------------------------------

func TestZeroPassword_ZeroesAllBytes(t *testing.T) {
	e := &KeePassEntry{
		Title:    "test",
		Password: []byte("secret123"),
	}

	e.ZeroPassword()

	for i, b := range e.Password {
		assert.Equal(t, byte(0), b, "byte at index %d is not zeroed", i)
	}
}

func TestZeroPassword_EmptyPassword(t *testing.T) {
	e := &KeePassEntry{
		Title:    "test",
		Password: []byte{},
	}

	// Must not panic on empty slice.
	assert.NotPanics(t, func() { e.ZeroPassword() })
}

func TestZeroPassword_NilPassword(t *testing.T) {
	e := &KeePassEntry{
		Title:    "test",
		Password: nil,
	}

	// Must not panic on nil slice.
	assert.NotPanics(t, func() { e.ZeroPassword() })
}
