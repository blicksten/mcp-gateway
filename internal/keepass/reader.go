// Package keepass provides KeePass KDBX file reading and credential extraction.
package keepass

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/tobischo/gokeepasslib/v3"
)

// ErrNoCredentials is returned by OpenDatabase / buildCredentials when the
// caller supplied neither a master password nor a key file. Exported so that
// downstream callers (mcp-ctl credential import; SAP Picker T-A.4) can use
// `errors.Is(err, keepass.ErrNoCredentials)` instead of brittle substring
// matches on err.Error().
var ErrNoCredentials = errors.New("no credentials provided: use --password-file or --key-file")

// KeePassEntry holds extracted credential data from a KDBX entry.
type KeePassEntry struct {
	Title        string
	User         string
	Password     []byte
	CustomFields map[string]string
	Group        string
}

// ZeroPassword overwrites the password bytes with zeros.
func (e *KeePassEntry) ZeroPassword() {
	for i := range e.Password {
		e.Password[i] = 0
	}
}

// OpenDatabase opens and decodes a KDBX database file.
// password is the master password (already read from file or interactive prompt).
// keyFile is the path to a key file (optional, pass "" to skip).
// Wraps Decode in a narrow recover() to convert panics from malformed KDBX to errors.
//
// Symlink-rejection invariant (T-F.5 finding MEDIUM-2, 2026-05-10): both
// `path` and `keyFile` are Lstat'd before any read so symlinks placed at
// the operator-configured path cannot redirect the open into an
// unrelated sensitive file (e.g. /etc/shadow, Windows SAM). KeePass
// credential paths must be regular files — a symlink shape is rejected
// with a typed error rather than silently followed.
func OpenDatabase(path string, password []byte, keyFile string) (*gokeepasslib.Database, error) {
	if info, err := os.Lstat(path); err != nil {
		return nil, fmt.Errorf("kdbx file: %w", err)
	} else if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("kdbx file %q is a symlink — rejected (must be a regular file)", path)
	} else if info.IsDir() {
		return nil, fmt.Errorf("kdbx file %q is a directory", path)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open KDBX: %w", err)
	}
	defer f.Close()

	if keyFile != "" {
		info, err := os.Lstat(keyFile)
		if err != nil {
			return nil, fmt.Errorf("key file: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("key file %q is a symlink — rejected (must be a regular file)", keyFile)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("key file %q is a directory", keyFile)
		}
	}

	creds, err := buildCredentials(password, keyFile)
	if err != nil {
		return nil, err
	}

	db := gokeepasslib.NewDatabase()
	db.Credentials = creds

	if err := decodeWithRecover(db, f); err != nil {
		return nil, err
	}

	if err := db.UnlockProtectedEntries(); err != nil {
		return nil, fmt.Errorf("unlock entries: %w", err)
	}

	return db, nil
}

func buildCredentials(password []byte, keyFile string) (*gokeepasslib.DBCredentials, error) {
	pw := string(password)
	hasPassword := len(password) > 0
	hasKey := keyFile != ""

	switch {
	case hasPassword && hasKey:
		return gokeepasslib.NewPasswordAndKeyCredentials(pw, keyFile)
	case hasKey:
		return gokeepasslib.NewKeyCredentials(keyFile)
	case hasPassword:
		return gokeepasslib.NewPasswordCredentials(pw), nil
	default:
		return nil, ErrNoCredentials
	}
}

func decodeWithRecover(db *gokeepasslib.Database, f *os.File) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("malformed KDBX: %v", r)
		}
	}()
	if decErr := gokeepasslib.NewDecoder(f).Decode(db); decErr != nil {
		return fmt.Errorf("decode KDBX: %w", decErr)
	}
	return nil
}

// standardFields are KeePass fields that are mapped to dedicated struct fields,
// not included in CustomFields.
var standardFields = map[string]bool{
	"Title":    true,
	"UserName": true,
	"Password": true,
	"URL":      true,
	"Notes":    true,
}

// ExtractEntries recursively walks groups and extracts entries.
// If groupFilter is non-empty, only entries from matching groups are included.
func ExtractEntries(db *gokeepasslib.Database, groupFilter string) []KeePassEntry {
	var entries []KeePassEntry
	for _, g := range db.Content.Root.Groups {
		entries = append(entries, extractGroup(g, groupFilter, g.Name)...)
	}
	return entries
}

func extractGroup(g gokeepasslib.Group, filter, path string) []KeePassEntry {
	var entries []KeePassEntry

	matchesFilter := filter == "" || g.Name == filter

	if matchesFilter {
		for _, e := range g.Entries {
			title := e.GetTitle()
			if title == "" {
				continue // skip entries without a title
			}
			ke := KeePassEntry{
				Title:        title,
				User:         e.GetContent("UserName"),
				Password:     []byte(e.GetPassword()),
				CustomFields: make(map[string]string),
				Group:        path,
			}
			for _, v := range e.Values {
				if standardFields[v.Key] || v.Key == "" {
					continue
				}
				ke.CustomFields[v.Key] = v.Value.Content
			}
			entries = append(entries, ke)
		}
	}

	for _, sg := range g.Groups {
		entries = append(entries, extractGroup(sg, filter, path+"/"+sg.Name)...)
	}

	return entries
}

// ReadPasswordFile reads the master password from a file, trimming trailing newlines.
func ReadPasswordFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read password file: %w", err)
	}
	s := strings.TrimRight(string(data), "\r\n")
	return []byte(s), nil
}
