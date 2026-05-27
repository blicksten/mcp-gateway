// Package sapcreds extracts SAP credentials from KeePass vaults for the
// SAP Picker feature.
package sapcreds

import (
	"errors"

	"mcp-gateway/internal/keepass"
	"mcp-gateway/internal/sapname"

	"github.com/tobischo/gokeepasslib/v3"
)

// Entry holds one SAP credential extracted from a KeePass vault entry.
// SID and Client are derived by running the entry's Title through the SAP
// server-name grammar (internal/sapname.ParseServerName). If the title does
// not match the grammar, SID and Client are empty.
type Entry struct {
	Title  string
	SID    string
	Client string
	User   string
	Group  string
}

// ListEntries opens the KDBX vault at kdbxPath, decodes it with the supplied
// password and optional keyfile path, filters out recycle-bin entries, and
// returns a structured list of SAP credential entries.
//
// Returns keepass.ErrNoCredentials (wrapped) when neither a password nor a
// keyfile is supplied. Returns a decode error when the password is wrong.
func ListEntries(kdbxPath, password, keyfilePath string) ([]Entry, error) {
	return ListEntriesBytes(kdbxPath, []byte(password), keyfilePath)
}

// ListEntriesBytes is the []byte-keyed variant. Use this when the caller
// already holds the password as []byte to avoid a string round-trip. The
// credential_import.go path (known-good on the operator's KDBX) passes
// []byte directly to keepass.OpenDatabase — this entrypoint mirrors that
// shape exactly so credential list-structured can do the same.
func ListEntriesBytes(kdbxPath string, password []byte, keyfilePath string) ([]Entry, error) {
	db, err := keepass.OpenDatabase(kdbxPath, password, keyfilePath)
	if err != nil {
		return nil, err
	}

	raw := extractEntriesExcludingRecycleBin(db)
	out := make([]Entry, 0, len(raw))
	for _, ke := range raw {
		parsed, ok := sapname.ParseServerName(ke.Title)
		sid := ""
		client := ""
		if ok {
			sid = parsed.SID
			client = parsed.Client
		}
		out = append(out, Entry{
			Title:  ke.Title,
			SID:    sid,
			Client: client,
			User:   ke.User,
			Group:  ke.Group,
		})
	}
	return out, nil
}

// extractEntriesExcludingRecycleBin walks the database groups, skipping the
// group whose UUID matches Meta.RecycleBinUUID when RecycleBinEnabled is
// true (F4 guard from T-A.0 PoC — both conditions must hold).
func extractEntriesExcludingRecycleBin(db *gokeepasslib.Database) []keepass.KeePassEntry {
	if db == nil || db.Content == nil || db.Content.Meta == nil || db.Content.Root == nil {
		return nil
	}
	meta := db.Content.Meta
	recycleEnabled := meta.RecycleBinEnabled.Bool
	recycleUUID := meta.RecycleBinUUID
	filterApplies := recycleEnabled && !isZeroUUID(recycleUUID)

	var out []keepass.KeePassEntry
	for _, g := range db.Content.Root.Groups {
		out = append(out, walkGroupExcludingRecycle(g, recycleUUID, filterApplies, g.Name)...)
	}
	return out
}

func walkGroupExcludingRecycle(
	g gokeepasslib.Group,
	recycleUUID gokeepasslib.UUID,
	filterApplies bool,
	path string,
) []keepass.KeePassEntry {
	if filterApplies && g.UUID.Compare(recycleUUID) {
		return nil
	}
	var out []keepass.KeePassEntry
	for _, e := range g.Entries {
		title := e.GetTitle()
		if title == "" {
			continue
		}
		out = append(out, keepass.KeePassEntry{
			Title: title,
			User:  e.GetContent("UserName"),
			Group: path,
		})
	}
	for _, sg := range g.Groups {
		out = append(out, walkGroupExcludingRecycle(sg, recycleUUID, filterApplies, path+"/"+sg.Name)...)
	}
	return out
}

func isZeroUUID(u gokeepasslib.UUID) bool {
	var zero gokeepasslib.UUID
	return u == zero
}

// errNoCredentials is re-exported for callers that need errors.Is checking.
// The canonical sentinel lives in internal/keepass.
var errNoCredentials = keepass.ErrNoCredentials

// IsErrNoCredentials reports whether err wraps keepass.ErrNoCredentials.
// Convenience wrapper for callers that import internal/sapcreds but not
// internal/keepass.
func IsErrNoCredentials(err error) bool {
	return errors.Is(err, keepass.ErrNoCredentials)
}
