// Package lifecycle — TASK C2.1: tool-manifest cache.
//
// The manifest persists per-backend tool lists to a durable user-private file
// (~/.mcp-gateway/tool-manifest.json) so the gateway can advertise an idle
// SAP backend's tools without spawning it. The file is owned-only (0600 on
// POSIX; protected DACL on Windows) and never contains secret values.
//
// Design: docs/DESIGN-mcp-gateway-lazy-spawn.md §4.1
// Feature flag: MCP_GATEWAY_LAZY_SPAWN (default OFF).
// When the flag is OFF this file's public API is available for import but
// LoadManifest/Persist become no-ops so the caller can still call them safely.
package lifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mcp-gateway/internal/models"
)

// lazySpawnEnv is the feature-flag environment variable for TASK C2.
// Default OFF. When set to "1", the manifest cache is active.
const lazySpawnEnv = "MCP_GATEWAY_LAZY_SPAWN"

// LazySpawnEnabled reports whether the C2 lazy-spawn feature is active.
// It reads MCP_GATEWAY_LAZY_SPAWN from the environment at call time so
// tests can toggle it with t.Setenv without restarting the binary.
// Default (env absent or empty or any value other than "1") is DISABLED.
func LazySpawnEnabled() bool {
	return os.Getenv(lazySpawnEnv) == "1"
}

// manifestSchemaVersion is bumped on incompatible schema changes.
// A loaded file whose schema_version > this constant is rejected.
const manifestSchemaVersion = 1

// manifestTTL is the maximum age of a manifest entry before it is
// treated as stale and the backend is eagerly re-spawned once to refresh.
// Bounds drift for backends that change tool sets without a config change.
const manifestTTL = 7 * 24 * time.Hour

// CachedTool is the serialised form of one tool stored in the manifest.
// It mirrors models.ToolInfo but omits the Server field (redundant —
// the owning backend name is the ManifestRecord.Name).
type CachedTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema,omitempty"`
}

// ManifestRecord is one entry in the tool-manifest cache.
// Secret hygiene: Sig is derived from env KEYS only (never values).
// Raw Env/Headers are NEVER persisted here.
type ManifestRecord struct {
	// Name is the backend server name (e.g. "vsp-P01").
	Name string `json:"name"`
	// Sig is a stable hash of the backend's launch config, redacted:
	// SHA-256 of (Command + sorted env KEYS + sorted Args).
	// Value changes are NOT included — only structural/identity changes.
	Sig string `json:"sig"`
	// Tools is the discovered tool list, without secrets.
	Tools []CachedTool `json:"tools"`
	// DiscoveredAt is the UTC timestamp of the last successful tool discovery.
	DiscoveredAt time.Time `json:"discovered_at"`
	// SchemaVersion allows forward-compatibility checks.
	SchemaVersion int `json:"schema_version"`
}

// manifestFile is the on-disk shape of the full manifest.
type manifestFile struct {
	SchemaVersion int              `json:"schema_version"`
	Records       []ManifestRecord `json:"records"`
}

// Manifest is an in-memory tool-manifest cache backed by a durable JSON file.
// All exported methods are safe for concurrent use.
type Manifest struct {
	mu      sync.Mutex
	path    string
	records map[string]ManifestRecord // keyed by backend name
}

// DefaultManifestDir returns the durable per-user directory for the manifest.
// This is ~/.mcp-gateway — the same directory that holds auth.token and
// admin.token. It is NOT DefaultRegistryDir() which maps to %TEMP%.
func DefaultManifestDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("manifest dir: home dir unavailable: %w", err)
	}
	return filepath.Join(home, ".mcp-gateway"), nil
}

// DefaultManifestPath returns the canonical path to the manifest file.
// Callers pass the result to LoadManifest.
func DefaultManifestPath() (string, error) {
	dir, err := DefaultManifestDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tool-manifest.json"), nil
}

// LoadManifest reads the manifest from path (creating an empty one when the
// file is absent). Returns a ready-to-use *Manifest.
//
// When the feature flag is OFF, this function still returns a valid (empty)
// *Manifest so callers do not need to nil-check; Put/Persist become no-ops.
//
// Migration: if path does not exist but the legacy TEMP-based path does,
// we copy the legacy file as a one-shot best-effort migration.
func LoadManifest(path string) (*Manifest, error) {
	m := &Manifest{
		path:    path,
		records: make(map[string]ManifestRecord),
	}

	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Attempt legacy migration before returning empty manifest.
			_ = m.migrateFromLegacy()
			return m, nil
		}
		return m, fmt.Errorf("manifest: read %q: %w", path, err)
	}

	var f manifestFile
	if err := json.Unmarshal(body, &f); err != nil {
		// Corrupt file — start fresh.
		return m, nil
	}
	if f.SchemaVersion > manifestSchemaVersion {
		// Newer schema written by a future binary — start fresh conservatively.
		return m, nil
	}
	for _, r := range f.Records {
		m.records[r.Name] = r
	}
	return m, nil
}

// migrateFromLegacy copies the manifest from the legacy TEMP-based registry dir
// to the durable path on a one-shot best-effort basis.
// M-3: delegates to migrateFromLegacyPath so tests can inject a controlled path.
func (m *Manifest) migrateFromLegacy() error {
	legacyPath := filepath.Join(DefaultRegistryDir(), "tool-manifest.json")
	return m.migrateFromLegacyPath(legacyPath)
}

// migrateFromLegacyPath copies the manifest from legacyPath to the durable
// manifest path m.path. Best-effort: all errors are silently ignored.
// Extracted from migrateFromLegacy so tests can exercise with a temp dir (M-3).
func (m *Manifest) migrateFromLegacyPath(legacyPath string) error {
	body, err := os.ReadFile(legacyPath)
	if err != nil {
		return nil // no legacy file — nothing to migrate
	}
	// Write to the new durable location atomically.
	if err := persistManifestBytes(m.path, body); err != nil {
		return nil // migration is best-effort; ignore failures
	}
	// Reload from the new file so our in-memory state is consistent.
	var f manifestFile
	if err := json.Unmarshal(body, &f); err != nil {
		return nil
	}
	for _, r := range f.Records {
		m.records[r.Name] = r
	}
	return nil
}

// Len returns the number of records currently held in memory (including
// potentially stale entries — Get removes stale ones lazily on access).
// Used for logging at startup; does not call LazySpawnEnabled.
func (m *Manifest) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.records)
}

// Get returns the cached record for a backend, if it exists and is not stale.
// Returns (zero, false) when absent, stale (TTL expired), or flag is OFF.
// Does NOT validate the stored Sig against the current config — use GetValid
// when the caller has the current BackendConfigSig available.
func (m *Manifest) Get(name string) (ManifestRecord, bool) {
	if !LazySpawnEnabled() {
		return ManifestRecord{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.getUnderLock(name)
}

// ManifestLookup classifies the outcome of a signature-validated lookup
// (GetValidWithOutcome). It lets a caller distinguish a plain cache miss
// (cold-start) from a stale-signature eviction so that, e.g., the TASK T1
// sig_mismatch_rediscover counter only fires on a real config change.
type ManifestLookup int

const (
	// ManifestHit — a fresh entry whose stored Sig matches the current config.
	ManifestHit ManifestLookup = iota
	// ManifestAbsent — no entry for this backend (cold-start) or the flag is OFF.
	ManifestAbsent
	// ManifestStale — an entry existed but exceeded the TTL; it was evicted.
	ManifestStale
	// ManifestSigMismatch — an entry existed within TTL but its stored Sig did
	// not match the current config (config changed); it was evicted.
	ManifestSigMismatch
)

// GetValid returns the cached record for a backend only when the stored Sig
// matches currentSig (Guard 1 — design §4.1). Returns (zero, false) when the
// record is absent, TTL-stale, flag-OFF, or the stored Sig does not match
// currentSig. On a sig mismatch the stale entry is evicted so the backend is
// treated as uncached and re-discovered on the next eager spawn.
func (m *Manifest) GetValid(name, currentSig string) (ManifestRecord, bool) {
	r, outcome := m.GetValidWithOutcome(name, currentSig)
	return r, outcome == ManifestHit
}

// GetValidWithOutcome is GetValid with the classified lookup outcome exposed.
// Eviction semantics are identical to GetValid (stale and sig-mismatch entries
// are evicted under the lock). When the flag is OFF it returns ManifestAbsent
// without touching the map.
func (m *Manifest) GetValidWithOutcome(name, currentSig string) (ManifestRecord, ManifestLookup) {
	if !LazySpawnEnabled() {
		return ManifestRecord{}, ManifestAbsent
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.classifyUnderLock(name, currentSig)
}

// isStale reports whether a record has exceeded the manifest TTL. Single source
// of truth for the staleness check shared by getUnderLock and classifyUnderLock
// so the two eviction paths cannot drift.
func (r ManifestRecord) isStale() bool {
	return time.Since(r.DiscoveredAt) > manifestTTL
}

// classifyUnderLock resolves a backend lookup to a record + outcome, evicting
// the entry on TTL-staleness or sig mismatch. Caller must hold m.mu.
func (m *Manifest) classifyUnderLock(name, currentSig string) (ManifestRecord, ManifestLookup) {
	r, ok := m.records[name]
	if !ok {
		return ManifestRecord{}, ManifestAbsent
	}
	if r.isStale() {
		// Stale — treat as absent so caller re-discovers.
		delete(m.records, name)
		return ManifestRecord{}, ManifestStale
	}
	if r.Sig != currentSig {
		// Config changed since the manifest was written — evict the stale entry
		// so the backend is treated as uncached and re-discovers on next spawn.
		delete(m.records, name)
		return ManifestRecord{}, ManifestSigMismatch
	}
	return r, ManifestHit
}

// getUnderLock is the shared inner body of Get (no signature check).
// Caller must hold m.mu.
func (m *Manifest) getUnderLock(name string) (ManifestRecord, bool) {
	r, ok := m.records[name]
	if !ok {
		return ManifestRecord{}, false
	}
	if r.isStale() {
		// Stale — treat as absent so caller re-discovers.
		delete(m.records, name)
		return ManifestRecord{}, false
	}
	return r, true
}

// Put stores or updates the record for a backend.
// When the feature flag is OFF, Put is a no-op so no manifest file is written.
// Tools are converted from models.ToolInfo to CachedTool (dropping Server field).
func (m *Manifest) Put(name, sig string, tools []models.ToolInfo) {
	if !LazySpawnEnabled() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cached := make([]CachedTool, 0, len(tools))
	for _, t := range tools {
		cached = append(cached, CachedTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	m.records[name] = ManifestRecord{
		Name:          name,
		Sig:           sig,
		Tools:         cached,
		DiscoveredAt:  time.Now().UTC(),
		SchemaVersion: manifestSchemaVersion,
	}
}

// Remove deletes the manifest entry for a backend.
// When the feature flag is OFF, Remove is a no-op.
// Called by the lazy-spawn coordinator on spawn failure so a broken backend
// is no longer advertised (Guard 2 — C2.2).
func (m *Manifest) Remove(name string) {
	if !LazySpawnEnabled() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.records, name)
}

// Persist atomically flushes the in-memory manifest to disk.
// When the feature flag is OFF, Persist is a no-op.
// Callers must hold no lock before calling Persist.
func (m *Manifest) Persist() error {
	if !LazySpawnEnabled() {
		return nil
	}
	m.mu.Lock()
	records := make([]ManifestRecord, 0, len(m.records))
	for _, r := range m.records {
		records = append(records, r)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
	m.mu.Unlock()

	f := manifestFile{
		SchemaVersion: manifestSchemaVersion,
		Records:       records,
	}
	body, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("manifest: marshal: %w", err)
	}
	return persistManifestBytes(m.path, body)
}

// persistManifestBytes writes body to path atomically (tempfile → fsync → rename).
// Creates the parent directory with 0700 if missing.
// After a successful rename it applies platform-correct permissions (0600 POSIX /
// owner-only DACL Windows).
func persistManifestBytes(path string, body []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("manifest: mkdir %q: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tool-manifest-*.json")
	if err != nil {
		return fmt.Errorf("manifest: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	// Apply restrictive permissions before writing content so there is no
	// race window where the file has content but world-readable ACLs.
	if err := applyManifestFilePerms(tmpPath); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("manifest: apply perms: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("manifest: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("manifest: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("manifest: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		// Windows retry: the destination may be open (NTFS sharing violation).
		// Remove destination first, then retry the rename. L-1 fix: we must NOT
		// call cleanup() before confirming the second rename succeeds — if the
		// second rename also fails after dest removal, both the old dest and the
		// temp are gone (data loss window). Keep tmpPath alive until success.
		if _, statErr := os.Stat(path); statErr == nil {
			if rmErr := os.Remove(path); rmErr == nil {
				slog.Warn("manifest: Windows rename retry — destination removed, attempting second rename",
					"path", path, "tmp", tmpPath, "first_err", err)
				if err2 := os.Rename(tmpPath, path); err2 == nil {
					// Second rename consumed tmpPath — no cleanup needed.
					return nil
				}
				// Destination is gone and second rename also failed — both are lost.
				// Best-effort: clean up the temp and report the original error.
			}
		}
		cleanup()
		return fmt.Errorf("manifest: rename into place: %w", err)
	}
	return nil
}

// BackendConfigSig computes the redacted signature for a backend's launch config.
//
// The signature is a SHA-256 hex digest of:
//   - the Command field
//   - sorted Args fields (joined)
//   - sorted env KEY names only (values are NEVER included)
//
// Rationale: env VALUES may be expanded plaintext secrets (see types.go:118/122).
// Value changes are caught by TTL and re-discovery on spawn; we track only
// structural/identity changes (new key added, key removed, command changed).
//
// The signature does NOT include URL, Headers, or any field that may contain
// secret material.
func BackendConfigSig(cfg models.ServerConfig) string {
	h := sha256.New()

	// Command identifies the binary; args are hashed in DECLARATION ORDER.
	// Argument order is meaningful (["--mode","gui"] != ["gui","--mode"]),
	// so sorting would cause two arg-reordered configs to collide on the same
	// sig and serve stale tools. H-1 fix.
	fmt.Fprintf(h, "cmd=%s\n", cfg.Command)
	for _, a := range cfg.Args {
		fmt.Fprintf(h, "arg=%s\n", a)
	}

	// Env KEY names only. Extract keys, sort, write. Never write values.
	keys := make([]string, 0, len(cfg.Env))
	for _, entry := range cfg.Env {
		key, _, ok := strings.Cut(entry, "=")
		if ok && key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "envkey=%s\n", k)
	}

	return hex.EncodeToString(h.Sum(nil))
}
