package claudeimport

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadProvenance_Missing(t *testing.T) {
	dir := t.TempDir()
	log, err := LoadProvenance(filepath.Join(dir, "no-such.json"))
	if err != nil {
		t.Fatalf("LoadProvenance on missing should succeed, got %v", err)
	}
	if log.Version != 1 {
		t.Errorf("default Version = %d, want 1", log.Version)
	}
	if len(log.Records) != 0 {
		t.Errorf("expected empty Records on missing")
	}
}

func TestAppendProvenance_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-imported.json")

	rec := ProvenanceRecord{
		Source:     "cc_global",
		SourcePath: "/home/user/.claude.json",
		Name:       "pal-mcp",
		Action:     "copy",
	}
	if err := AppendProvenance(path, rec); err != nil {
		t.Fatalf("Append: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var log ProvenanceLog
	if err := json.Unmarshal(body, &log); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if log.Version != 1 || len(log.Records) != 1 {
		t.Errorf("unexpected log: %+v", log)
	}
	got := log.Records[0]
	if got.Source != "cc_global" || got.Name != "pal-mcp" || got.Action != "copy" {
		t.Errorf("record fields wrong: %+v", got)
	}
	if got.ImportedAt == "" {
		t.Errorf("ImportedAt was not auto-populated")
	}
	if _, err := time.Parse(time.RFC3339Nano, got.ImportedAt); err != nil {
		t.Errorf("ImportedAt %q is not RFC3339Nano: %v", got.ImportedAt, err)
	}
}

func TestAppendProvenance_AppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-imported.json")

	for _, name := range []string{"a", "b", "c"} {
		if err := AppendProvenance(path, ProvenanceRecord{
			Source: "cc_global", Name: name, Action: "copy",
		}); err != nil {
			t.Fatalf("Append %q: %v", name, err)
		}
	}

	log, err := LoadProvenance(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(log.Records) != 3 {
		t.Errorf("expected 3 records, got %d", len(log.Records))
	}
}

func TestPreviouslyImported(t *testing.T) {
	now := time.Now().UTC()
	log := ProvenanceLog{
		Version: 1,
		Records: []ProvenanceRecord{
			{Source: "cc_global", Name: "x", Action: "copy", ImportedAt: now.Add(-2 * time.Hour).Format(time.RFC3339Nano)},
			{Source: "cc_global", Name: "x", Action: "copy", ImportedAt: now.Add(-1 * time.Hour).Format(time.RFC3339Nano)},
			{Source: "desktop", Name: "y", Action: "move", ImportedAt: now.Add(-3 * time.Hour).Format(time.RFC3339Nano)},
		},
	}
	ok, latest := PreviouslyImported(log, "cc_global", "x")
	if !ok {
		t.Errorf("expected previously imported")
	}
	wantLatest := log.Records[1].ImportedAt
	if latest != wantLatest {
		t.Errorf("latest = %q, want %q", latest, wantLatest)
	}
	ok, _ = PreviouslyImported(log, "cc_global", "z")
	if ok {
		t.Errorf("z was never imported")
	}
}

func TestAppendProvenance_AtomicityOnTempCleanup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-imported.json")
	if err := AppendProvenance(path, ProvenanceRecord{
		Source: "cc_global", Name: "x", Action: "copy",
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// After a successful append, no .tmp residue should remain in dir.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("tmp residue found: %s", e.Name())
		}
	}
}

func TestLoadProvenance_RejectsHigherVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-imported.json")
	if err := os.WriteFile(path,
		[]byte(`{"version": 999, "records": []}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Load tolerates higher versions; only Append rejects them.
	if _, err := LoadProvenance(path); err != nil {
		t.Fatalf("LoadProvenance should not reject higher version on read: %v", err)
	}
	// Append must reject.
	err := AppendProvenance(path, ProvenanceRecord{Source: "cc_global", Name: "x", Action: "copy"})
	if err == nil {
		t.Errorf("expected error appending to higher-version sidecar")
	}
}

func TestLoadProvenance_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-imported.json")
	if err := os.WriteFile(path, []byte(`{ not valid `), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadProvenance(path)
	if err == nil {
		t.Fatalf("expected error on malformed sidecar")
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("malformed should not look like NotExist: %v", err)
	}
}
