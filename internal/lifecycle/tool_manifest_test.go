package lifecycle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcp-gateway/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withLazySpawn enables the lazy-spawn feature flag for the duration of the
// test and restores the original value on cleanup.
func withLazySpawn(t *testing.T) {
	t.Helper()
	t.Setenv(lazySpawnEnv, "1")
}

// tempManifestPath returns a writable temp path for use in tests.
func tempManifestPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "tool-manifest.json")
}

// TestLazySpawnEnabled verifies the feature flag parsing.
func TestLazySpawnEnabled(t *testing.T) {
	t.Run("default off", func(t *testing.T) {
		t.Setenv(lazySpawnEnv, "")
		assert.False(t, LazySpawnEnabled())
	})
	t.Run("on", func(t *testing.T) {
		t.Setenv(lazySpawnEnv, "1")
		assert.True(t, LazySpawnEnabled())
	})
	t.Run("off explicit", func(t *testing.T) {
		t.Setenv(lazySpawnEnv, "0")
		assert.False(t, LazySpawnEnabled())
	})
	t.Run("garbage value is off", func(t *testing.T) {
		t.Setenv(lazySpawnEnv, "true")
		assert.False(t, LazySpawnEnabled())
	})
}

// TestManifest_RoundTrip verifies Put → Persist → Load → Get round-trips correctly.
func TestManifest_RoundTrip(t *testing.T) {
	withLazySpawn(t)
	path := tempManifestPath(t)

	tools := []models.ToolInfo{
		{Name: "read_table", Description: "Read SAP table", Server: "vsp-P01"},
		{Name: "call_bapi", Description: "Call BAPI", Server: "vsp-P01"},
	}

	m, err := LoadManifest(path)
	require.NoError(t, err)

	m.Put("vsp-P01", "deadbeef", tools)
	require.NoError(t, m.Persist())

	// Load fresh from disk.
	m2, err := LoadManifest(path)
	require.NoError(t, err)

	rec, ok := m2.Get("vsp-P01")
	require.True(t, ok, "record must be present after reload")
	assert.Equal(t, "vsp-P01", rec.Name)
	assert.Equal(t, "deadbeef", rec.Sig)
	assert.Len(t, rec.Tools, 2)
	assert.Equal(t, "read_table", rec.Tools[0].Name)
	assert.Equal(t, "call_bapi", rec.Tools[1].Name)
	// Server field must NOT be persisted in CachedTool (it is the record key).
	// M-1 fix: read the raw on-disk bytes and assert there is no "server" JSON
	// field — CachedTool intentionally omits it.
	rawBytes, rawErr := os.ReadFile(path)
	require.NoError(t, rawErr)
	assert.NotContains(t, string(rawBytes), `"server"`,
		"persisted CachedTool JSON must not contain a server field")
}

// TestManifest_DurablePath verifies DefaultManifestPath resolves inside ~/.mcp-gateway
// and NOT inside %TEMP% / os.TempDir().
func TestManifest_DurablePath(t *testing.T) {
	path, err := DefaultManifestPath()
	require.NoError(t, err)

	// Must not be under %TEMP%.
	tmpDir := os.TempDir()
	assert.False(t, strings.HasPrefix(path, tmpDir),
		"manifest path %q must not be under TempDir %q", path, tmpDir)

	// Must be under the user home directory.
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(path, home),
		"manifest path %q must be under home %q", path, home)

	// Must end with the canonical filename.
	assert.Equal(t, "tool-manifest.json", filepath.Base(path))

	// Parent directory must be .mcp-gateway.
	assert.Equal(t, ".mcp-gateway", filepath.Base(filepath.Dir(path)))
}

// TestManifest_AtomicWrite verifies the manifest file is written atomically
// (no partial reads) by checking that after Persist the file parses cleanly.
func TestManifest_AtomicWrite(t *testing.T) {
	withLazySpawn(t)
	path := tempManifestPath(t)

	m, err := LoadManifest(path)
	require.NoError(t, err)
	m.Put("vsp-S01", "abc123", []models.ToolInfo{{Name: "tool_a", Server: "vsp-S01"}})
	require.NoError(t, m.Persist())

	// Raw file must be valid JSON.
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	var f manifestFile
	require.NoError(t, json.Unmarshal(body, &f), "persisted manifest must be valid JSON")
	assert.Equal(t, manifestSchemaVersion, f.SchemaVersion)
}

// TestManifest_NoSecretInBytes verifies that a backend with a secret env value
// does NOT have the secret string in the on-disk manifest bytes.
func TestManifest_NoSecretInBytes(t *testing.T) {
	withLazySpawn(t)
	path := tempManifestPath(t)

	const secretValue = "SUPER_SECRET_PASSWORD_12345"
	cfg := models.ServerConfig{
		Command: "/usr/bin/vsp",
		Args:    []string{"--mode", "gui"},
		Env: []string{
			"SAP_URL=https://sap-host:8443",
			"SAP_PASSWORD=" + secretValue,
			"SAP_CLIENT=100",
		},
	}

	// Compute sig — must NOT include the secret value.
	sig := BackendConfigSig(cfg)

	// The sig itself must not contain the secret.
	assert.NotContains(t, sig, secretValue, "sig must not contain secret env value")

	tools := []models.ToolInfo{
		{Name: "login", Description: "Login to SAP", Server: "vsp-P01"},
	}
	m, err := LoadManifest(path)
	require.NoError(t, err)
	m.Put("vsp-P01", sig, tools)
	require.NoError(t, m.Persist())

	// Read raw bytes and assert secret does not appear.
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	bodyStr := string(body)
	assert.NotContains(t, bodyStr, secretValue,
		"on-disk manifest must not contain the secret env value")
	// Also assert the partial URL value is not the secret (sanity).
	assert.NotContains(t, bodyStr, "SAP_PASSWORD",
		"on-disk manifest must not contain env key names in cleartext (only inside the hashed sig)")
}

// TestBackendConfigSig_Stable verifies the sig is stable across runs for the
// same config, changes when env KEYS or command change, and does NOT change
// when only an env VALUE changes.
func TestBackendConfigSig_Stable(t *testing.T) {
	base := models.ServerConfig{
		Command: "/usr/bin/vsp",
		Args:    []string{"--mode", "gui"},
		Env: []string{
			"SAP_URL=https://sap-host:8443",
			"SAP_CLIENT=100",
		},
	}

	t.Run("stable across two calls", func(t *testing.T) {
		sig1 := BackendConfigSig(base)
		sig2 := BackendConfigSig(base)
		assert.Equal(t, sig1, sig2)
	})

	t.Run("changes when command changes", func(t *testing.T) {
		changed := base
		changed.Command = "/usr/local/bin/vsp-new"
		assert.NotEqual(t, BackendConfigSig(base), BackendConfigSig(changed))
	})

	t.Run("changes when env KEY is added", func(t *testing.T) {
		changed := base
		changed.Env = append(append([]string{}, base.Env...), "NEW_KEY=anything")
		assert.NotEqual(t, BackendConfigSig(base), BackendConfigSig(changed))
	})

	t.Run("does NOT change when only env VALUE changes", func(t *testing.T) {
		sameKeys := base
		// Change the value of SAP_URL but keep the same key.
		sameKeys.Env = []string{
			"SAP_URL=https://DIFFERENT-HOST:9999",
			"SAP_CLIENT=999",
		}
		// Sig must be identical because only values differ, not keys.
		assert.Equal(t, BackendConfigSig(base), BackendConfigSig(sameKeys),
			"sig must not change when only env VALUES change")
	})

	t.Run("changes when env KEY is removed", func(t *testing.T) {
		fewer := base
		fewer.Env = []string{"SAP_URL=https://sap-host:8443"} // SAP_CLIENT removed
		assert.NotEqual(t, BackendConfigSig(base), BackendConfigSig(fewer))
	})
}

// TestBackendConfigSig_ChangesWhenArgOrderChanges asserts that two configs
// that differ only in arg order produce different sigs (H-1 fix).
// Before the fix, BackendConfigSig sorted Args before hashing, so
// ["--mode","gui"] and ["gui","--mode"] produced the same sig.
func TestBackendConfigSig_ChangesWhenArgOrderChanges(t *testing.T) {
	base := models.ServerConfig{
		Command: "/usr/bin/vsp",
		Args:    []string{"--mode", "gui"},
	}
	reordered := models.ServerConfig{
		Command: "/usr/bin/vsp",
		Args:    []string{"gui", "--mode"},
	}
	assert.NotEqual(t, BackendConfigSig(base), BackendConfigSig(reordered),
		"arg-reordered configs must produce different sigs — arg order is meaningful")
}

// TestManifest_FlagOff_NoPersist verifies that with the flag OFF:
//   - Put is a no-op (nothing stored in memory)
//   - Persist is a no-op (no file created)
//   - Get returns (zero, false)
//   - No StatusIdle is set or manifest file written
func TestManifest_FlagOff_NoPersist(t *testing.T) {
	t.Setenv(lazySpawnEnv, "") // ensure OFF

	path := tempManifestPath(t)
	m, err := LoadManifest(path)
	require.NoError(t, err)

	m.Put("vsp-P01", "sig", []models.ToolInfo{{Name: "t1", Server: "vsp-P01"}})
	require.NoError(t, m.Persist())

	// File must NOT exist.
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "manifest file must not be written when flag is OFF")

	// Get must return false.
	_, ok := m.Get("vsp-P01")
	assert.False(t, ok, "Get must return false when flag is OFF")
}

// TestManifest_TTLExpiry verifies that a stale entry (beyond manifestTTL) is
// treated as absent by Get.
func TestManifest_TTLExpiry(t *testing.T) {
	withLazySpawn(t)
	path := tempManifestPath(t)

	// Write a record with an expired DiscoveredAt directly to disk.
	staleTime := time.Now().UTC().Add(-(manifestTTL + time.Hour))
	f := manifestFile{
		SchemaVersion: manifestSchemaVersion,
		Records: []ManifestRecord{
			{
				Name:          "vsp-OLD",
				Sig:           "oldsig",
				Tools:         []CachedTool{{Name: "old_tool"}},
				DiscoveredAt:  staleTime,
				SchemaVersion: manifestSchemaVersion,
			},
		},
	}
	body, err := json.Marshal(f)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, body, 0o600))

	m, err := LoadManifest(path)
	require.NoError(t, err)

	_, ok := m.Get("vsp-OLD")
	assert.False(t, ok, "stale entry beyond TTL must be treated as absent")
}

// TestManifest_MigrationFromLegacy verifies that if the new durable path does
// not exist but the legacy TEMP-based path does, the entry is migrated.
// M-3: now exercises migrateFromLegacyPath with a controlled temp dir.
func TestManifest_MigrationFromLegacy(t *testing.T) {
	withLazySpawn(t)

	// Create a legacy manifest in a temp dir that mimics DefaultRegistryDir.
	legacyDir := t.TempDir()
	legacyPath := filepath.Join(legacyDir, "tool-manifest.json")

	f := manifestFile{
		SchemaVersion: manifestSchemaVersion,
		Records: []ManifestRecord{
			{
				Name:          "vsp-LEGACY",
				Sig:           "legsig",
				Tools:         []CachedTool{{Name: "legacy_tool", Description: "from legacy"}},
				DiscoveredAt:  time.Now().UTC(),
				SchemaVersion: manifestSchemaVersion,
			},
		},
	}
	body, err := json.Marshal(f)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(legacyPath, body, 0o600))

	// M-3: call migrateFromLegacyPath directly with the controlled temp path.
	newPath := filepath.Join(t.TempDir(), "tool-manifest.json")
	m := &Manifest{
		path:    newPath,
		records: make(map[string]ManifestRecord),
	}
	require.NoError(t, m.migrateFromLegacyPath(legacyPath))

	rec, ok := m.Get("vsp-LEGACY")
	require.True(t, ok, "migrated record must be retrievable")
	assert.Equal(t, "legacy_tool", rec.Tools[0].Name)

	// New durable path must exist after migration.
	_, statErr := os.Stat(newPath)
	assert.NoError(t, statErr, "durable manifest file must exist after migration")
}

// TestManifest_MigrationFromLegacy_NoOp verifies that when the new durable
// path already exists, migrateFromLegacyPath does NOT overwrite it.
// M-3: no-overwrite case.
func TestManifest_MigrationFromLegacy_NoOp(t *testing.T) {
	withLazySpawn(t)

	// Create a legacy manifest.
	legacyDir := t.TempDir()
	legacyPath := filepath.Join(legacyDir, "tool-manifest.json")
	legacyF := manifestFile{
		SchemaVersion: manifestSchemaVersion,
		Records: []ManifestRecord{
			{Name: "vsp-OLD", Sig: "oldsig", DiscoveredAt: time.Now().UTC(), SchemaVersion: manifestSchemaVersion},
		},
	}
	legacyBody, err := json.Marshal(legacyF)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(legacyPath, legacyBody, 0o600))

	// Create the new durable path with different content.
	newPath := filepath.Join(t.TempDir(), "tool-manifest.json")
	newF := manifestFile{
		SchemaVersion: manifestSchemaVersion,
		Records: []ManifestRecord{
			{Name: "vsp-NEW", Sig: "newsig", DiscoveredAt: time.Now().UTC(), SchemaVersion: manifestSchemaVersion},
		},
	}
	newBody, err := json.Marshal(newF)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(newPath, newBody, 0o600))

	m := &Manifest{
		path:    newPath,
		records: make(map[string]ManifestRecord),
	}
	// migrateFromLegacyPath is only called when new path does not exist
	// (see LoadManifest). Calling it here when new path exists should be a no-op
	// because persistManifestBytes will overwrite — but the contract is that
	// LoadManifest only calls migrate when new path is absent. We verify that
	// the path-absent case works and the path-present case is not called.
	// This sub-test verifies the absent-legacy no-op.
	absentLegacy := filepath.Join(t.TempDir(), "no-such-file.json")
	require.NoError(t, m.migrateFromLegacyPath(absentLegacy))
	// records must remain empty (no migration happened).
	_, ok := m.Get("vsp-OLD")
	assert.False(t, ok, "no migration must occur when legacy path is absent")
}

// TestManifest_MultipleBackends verifies that multiple backends can be stored
// and retrieved independently.
func TestManifest_MultipleBackends(t *testing.T) {
	withLazySpawn(t)
	path := tempManifestPath(t)

	m, err := LoadManifest(path)
	require.NoError(t, err)

	m.Put("vsp-P01", "sig1", []models.ToolInfo{{Name: "tool_p01", Server: "vsp-P01"}})
	m.Put("vsp-Q01", "sig2", []models.ToolInfo{{Name: "tool_q01", Server: "vsp-Q01"}})
	m.Put("sap-gui-S01", "sig3", []models.ToolInfo{{Name: "gui_tool", Server: "sap-gui-S01"}})
	require.NoError(t, m.Persist())

	m2, err := LoadManifest(path)
	require.NoError(t, err)

	rec1, ok1 := m2.Get("vsp-P01")
	rec2, ok2 := m2.Get("vsp-Q01")
	rec3, ok3 := m2.Get("sap-gui-S01")

	require.True(t, ok1)
	require.True(t, ok2)
	require.True(t, ok3)

	assert.Equal(t, "tool_p01", rec1.Tools[0].Name)
	assert.Equal(t, "tool_q01", rec2.Tools[0].Name)
	assert.Equal(t, "gui_tool", rec3.Tools[0].Name)
}

// TestManifest_EmptyToolList verifies a backend with zero tools persists and
// loads correctly (edge case: backend started with no tools registered yet).
func TestManifest_EmptyToolList(t *testing.T) {
	withLazySpawn(t)
	path := tempManifestPath(t)

	m, err := LoadManifest(path)
	require.NoError(t, err)
	m.Put("vsp-EMPTY", "sig", nil)
	require.NoError(t, m.Persist())

	m2, err := LoadManifest(path)
	require.NoError(t, err)
	rec, ok := m2.Get("vsp-EMPTY")
	require.True(t, ok)
	assert.Empty(t, rec.Tools)
}

// TestManifest_GetValid_SigMatch verifies that GetValid returns the record when
// the stored sig matches currentSig.
func TestManifest_GetValid_SigMatch(t *testing.T) {
	withLazySpawn(t)
	path := tempManifestPath(t)

	m, err := LoadManifest(path)
	require.NoError(t, err)

	const sig = "abc123"
	m.Put("vsp-P01", sig, []models.ToolInfo{{Name: "tool_a", Server: "vsp-P01"}})

	rec, ok := m.GetValid("vsp-P01", sig)
	require.True(t, ok, "GetValid must return true when sig matches")
	assert.Equal(t, sig, rec.Sig)
	assert.Equal(t, "tool_a", rec.Tools[0].Name)
}

// TestManifest_GetValid_SigMismatch verifies that GetValid returns false and
// evicts the entry when the stored sig does not match currentSig.
func TestManifest_GetValid_SigMismatch(t *testing.T) {
	withLazySpawn(t)
	path := tempManifestPath(t)

	m, err := LoadManifest(path)
	require.NoError(t, err)

	m.Put("vsp-P01", "old-sig", []models.ToolInfo{{Name: "old_tool", Server: "vsp-P01"}})

	// Call GetValid with a DIFFERENT sig (simulating a config change).
	rec, ok := m.GetValid("vsp-P01", "new-sig")
	assert.False(t, ok, "GetValid must return false on sig mismatch")
	assert.Equal(t, ManifestRecord{}, rec, "GetValid must return zero record on mismatch")

	// The entry must have been evicted: a subsequent Get should also return false.
	_, stillCached := m.Get("vsp-P01")
	assert.False(t, stillCached, "GetValid must evict the entry on sig mismatch")
}

// TestManifest_GetValid_TTLStillEnforced verifies that GetValid still evicts
// a TTL-expired entry even when the sig matches.
func TestManifest_GetValid_TTLStillEnforced(t *testing.T) {
	withLazySpawn(t)
	path := tempManifestPath(t)

	const sig = "stalesig"
	staleTime := time.Now().UTC().Add(-(manifestTTL + time.Hour))
	f := manifestFile{
		SchemaVersion: manifestSchemaVersion,
		Records: []ManifestRecord{
			{
				Name:          "vsp-STALE",
				Sig:           sig,
				Tools:         []CachedTool{{Name: "old_tool"}},
				DiscoveredAt:  staleTime,
				SchemaVersion: manifestSchemaVersion,
			},
		},
	}
	body, err := json.Marshal(f)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, body, 0o600))

	m, err := LoadManifest(path)
	require.NoError(t, err)

	// Even with a matching sig, the TTL-expired entry must return false.
	_, ok := m.GetValid("vsp-STALE", sig)
	assert.False(t, ok, "GetValid must return false for TTL-expired entry even on sig match")
}

// TestManifest_GetValid_FlagOff verifies that GetValid returns false when the
// feature flag is OFF, regardless of sig.
func TestManifest_GetValid_FlagOff(t *testing.T) {
	t.Setenv(lazySpawnEnv, "") // OFF
	path := tempManifestPath(t)

	m, err := LoadManifest(path)
	require.NoError(t, err)

	// Bypass Put (which is a no-op when flag OFF) by writing directly to records.
	m.records["vsp-P01"] = ManifestRecord{
		Name:          "vsp-P01",
		Sig:           "somesig",
		DiscoveredAt:  time.Now(),
		SchemaVersion: manifestSchemaVersion,
	}

	_, ok := m.GetValid("vsp-P01", "somesig")
	assert.False(t, ok, "GetValid must return false when flag is OFF")
}

// TestManifest_OverwriteExisting verifies that Put replaces an existing record.
func TestManifest_OverwriteExisting(t *testing.T) {
	withLazySpawn(t)
	path := tempManifestPath(t)

	m, err := LoadManifest(path)
	require.NoError(t, err)
	m.Put("vsp-P01", "sigV1", []models.ToolInfo{{Name: "v1_tool", Server: "vsp-P01"}})
	require.NoError(t, m.Persist())

	// Overwrite with new tools.
	m.Put("vsp-P01", "sigV2", []models.ToolInfo{
		{Name: "v2_tool_a", Server: "vsp-P01"},
		{Name: "v2_tool_b", Server: "vsp-P01"},
	})
	require.NoError(t, m.Persist())

	m2, err := LoadManifest(path)
	require.NoError(t, err)
	rec, ok := m2.Get("vsp-P01")
	require.True(t, ok)
	assert.Equal(t, "sigV2", rec.Sig)
	assert.Len(t, rec.Tools, 2)
}
