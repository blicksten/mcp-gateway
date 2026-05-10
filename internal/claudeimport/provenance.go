package claudeimport

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ProvenanceRecord is one entry in ~/.mcp-gateway/claude-imported.json.
//
// Records preserve enough context for the snapshot endpoint to surface
// a "previously imported" badge:
//
//   - Source — which Claude Code config the entry came from (cc_global
//     / cc_project / desktop).
//   - SourcePath — the absolute path of the source file at import time.
//     Stored for forensic visibility; not used by the badge logic.
//   - Name — the entry name in the source file at import time. The
//     gateway's own internal name may differ if the operator renamed.
//   - ImportedAt — RFC3339Nano UTC timestamp.
//   - Action — "copy" or "move" (for completeness; "move" entries that
//     deleted the source still get a record so badge survives a
//     second-pass re-import attempt).
type ProvenanceRecord struct {
	Source     string `json:"source"`
	SourcePath string `json:"source_path"`
	Name       string `json:"name"`
	ImportedAt string `json:"imported_at"`
	Action     string `json:"action"`
}

// ProvenanceLog is the sidecar JSON file's top-level shape.
type ProvenanceLog struct {
	Version int                `json:"version"`
	Records []ProvenanceRecord `json:"records"`
}

// provenanceLogVersion is the schema version. Bump if the record
// shape changes incompatibly; readers that see a higher version log
// should refuse to mutate it.
const provenanceLogVersion = 1

// provenanceMu serialises the read-modify-write sidecar update against
// concurrent in-process callers. The atomic-rename write itself is safe
// against concurrent EXTERNAL processes; this mutex prevents in-process
// callers from racing each other and clobbering newly-appended records.
var provenanceMu sync.Mutex

// DefaultSidecarPath returns the canonical sidecar location, namely
// $HOME/.mcp-gateway/claude-imported.json. The directory is created on
// demand by writeProvenance; missing directory is not an error on read.
func DefaultSidecarPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".mcp-gateway", "claude-imported.json"), nil
}

// LoadProvenance reads the sidecar at path. A missing file returns
// (zero ProvenanceLog, nil) so callers can treat first-import as a
// normal case. Other I/O errors (permission, malformed) bubble up.
func LoadProvenance(path string) (ProvenanceLog, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err) {
			return ProvenanceLog{Version: provenanceLogVersion}, nil
		}
		return ProvenanceLog{}, fmt.Errorf("claudeimport: read sidecar %q: %w", path, err)
	}
	var log ProvenanceLog
	if err := json.Unmarshal(body, &log); err != nil {
		return ProvenanceLog{}, fmt.Errorf("claudeimport: decode sidecar %q: %w", path, err)
	}
	if log.Records == nil {
		log.Records = []ProvenanceRecord{}
	}
	return log, nil
}

// AppendProvenance reads the sidecar at path, appends a new record,
// and writes the result back atomically (CreateTemp in the same
// directory + Rename). The same record may be appended more than once
// over the lifetime of an installation; that is intentional — it lets
// the snapshot endpoint show e.g. "imported on date1 and date2".
//
// Concurrent callers from the same process are serialised by
// provenanceMu. Concurrent EXTERNAL writers (other gateway daemons
// sharing the same home dir) would still race and a later writer's
// version of the file would win — that is an out-of-scope multi-tenant
// scenario.
func AppendProvenance(path string, rec ProvenanceRecord) error {
	provenanceMu.Lock()
	defer provenanceMu.Unlock()

	if rec.ImportedAt == "" {
		rec.ImportedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}

	log, err := LoadProvenance(path)
	if err != nil {
		return err
	}
	if log.Version > provenanceLogVersion {
		return fmt.Errorf("claudeimport: sidecar %q version %d > supported %d", path, log.Version, provenanceLogVersion)
	}
	log.Version = provenanceLogVersion
	log.Records = append(log.Records, rec)
	return writeAtomic(path, &log)
}

// writeAtomic encodes log to a temp file in path's directory, then
// renames it over the destination. Atomic on POSIX; "best-effort
// atomic" on Windows (rename succeeds if the destination is closed,
// which it always is here because we never hold an open handle to it).
func writeAtomic(path string, log *ProvenanceLog) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("claudeimport: mkdir %q: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".claude-imported.*.tmp")
	if err != nil {
		return fmt.Errorf("claudeimport: CreateTemp: %w", err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup if any subsequent step fails.
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(log); err != nil {
		cleanup()
		return fmt.Errorf("claudeimport: encode sidecar: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("claudeimport: fsync sidecar: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("claudeimport: close sidecar tmp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("claudeimport: rename sidecar: %w", err)
	}
	return nil
}

// PreviouslyImported reports whether a row with the given (source, name)
// pair was ever imported before — used by the snapshot endpoint to
// drive the "previously imported" badge.
//
// The latest matching record's ImportedAt is returned alongside; empty
// when no match.
func PreviouslyImported(log ProvenanceLog, source, name string) (bool, string) {
	matches := []ProvenanceRecord{}
	for _, rec := range log.Records {
		if rec.Source == source && rec.Name == name {
			matches = append(matches, rec)
		}
	}
	if len(matches) == 0 {
		return false, ""
	}
	// Sort by ImportedAt descending (RFC3339 strings sort
	// lexicographically the same as chronologically when the format
	// is consistent — which RFC3339Nano is).
	sort.Slice(matches, func(i, j int) bool { return matches[i].ImportedAt > matches[j].ImportedAt })
	return true, matches[0].ImportedAt
}
