package sapcreds_test

import (
	"testing"

	"mcp-gateway/internal/sapcreds"
	"mcp-gateway/internal/saplandscape"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHybrid_IntersectedSID verifies that a SID present in both landscape and
// KP is returned with KPMissing=false and the user from the KP entry.
func TestHybrid_IntersectedSID(t *testing.T) {
	landscape := &saplandscape.Landscape{
		Services: []saplandscape.Service{
			{SID: "PRD", Client: "100"},
		},
	}
	kpEntries := []sapcreds.Entry{
		{SID: "PRD", Client: "100", User: "alice"},
	}

	rows, warnings := sapcreds.Hybrid(landscape, kpEntries)
	require.Len(t, rows, 1)
	assert.Equal(t, "PRD", rows[0].SID)
	assert.Equal(t, "100", rows[0].Client)
	assert.Equal(t, "alice", rows[0].User)
	assert.False(t, rows[0].KPMissing)
	assert.Empty(t, warnings)
}

// TestHybrid_LandscapeOnlySID verifies that a landscape SID without a matching
// KP entry is returned with KPMissing=true.
func TestHybrid_LandscapeOnlySID(t *testing.T) {
	landscape := &saplandscape.Landscape{
		Services: []saplandscape.Service{
			{SID: "QAS", Client: "200"},
		},
	}
	kpEntries := []sapcreds.Entry{} // no match

	rows, warnings := sapcreds.Hybrid(landscape, kpEntries)
	require.Len(t, rows, 1)
	assert.Equal(t, "QAS", rows[0].SID)
	assert.True(t, rows[0].KPMissing)
	assert.Equal(t, "", rows[0].User)
	assert.Empty(t, warnings)
}

// TestHybrid_KPOnlyExcluded verifies that KP-only entries (not in landscape)
// do not appear in the result — landscape is the master list (R-14).
func TestHybrid_KPOnlyExcluded(t *testing.T) {
	landscape := &saplandscape.Landscape{
		Services: []saplandscape.Service{
			{SID: "PRD", Client: "100"},
		},
	}
	kpEntries := []sapcreds.Entry{
		{SID: "PRD", Client: "100", User: "alice"},
		{SID: "DEV", Client: "300", User: "bob"}, // KP-only — must NOT appear
	}

	rows, warnings := sapcreds.Hybrid(landscape, kpEntries)
	require.Len(t, rows, 1, "KP-only SID must not appear in result")
	assert.Equal(t, "PRD", rows[0].SID)
	assert.Empty(t, warnings)
}

// TestHybrid_MixedSIDs verifies a realistic mix: one intersected, one landscape-only.
func TestHybrid_MixedSIDs(t *testing.T) {
	landscape := &saplandscape.Landscape{
		Services: []saplandscape.Service{
			{SID: "PRD", Client: "100"},
			{SID: "QAS", Client: "200"},
		},
	}
	kpEntries := []sapcreds.Entry{
		{SID: "PRD", Client: "100", User: "alice"},
		{SID: "XYZ", Client: "999", User: "charlie"}, // KP-only
	}

	rows, warnings := sapcreds.Hybrid(landscape, kpEntries)
	require.Len(t, rows, 2)
	assert.Empty(t, warnings)

	byKey := make(map[string]sapcreds.Row)
	for _, r := range rows {
		byKey[r.SID+"-"+r.Client] = r
	}

	prd := byKey["PRD-100"]
	assert.False(t, prd.KPMissing)
	assert.Equal(t, "alice", prd.User)

	qas := byKey["QAS-200"]
	assert.True(t, qas.KPMissing)
	assert.Equal(t, "", qas.User)
}

// TestHybrid_NilLandscape verifies that a nil landscape returns nil without panic.
func TestHybrid_NilLandscape(t *testing.T) {
	assert.NotPanics(t, func() {
		rows, warnings := sapcreds.Hybrid(nil, nil)
		assert.Nil(t, rows)
		assert.Nil(t, warnings)
	})
}

// TestHybrid_DuplicateKPEntry verifies that duplicate (SID, Client) entries in
// KeePass produce a warning and apply first-wins semantics — F-04 fix.
func TestHybrid_DuplicateKPEntry(t *testing.T) {
	landscape := &saplandscape.Landscape{
		Services: []saplandscape.Service{
			{SID: "PRD", Client: "100"},
		},
	}
	kpEntries := []sapcreds.Entry{
		{SID: "PRD", Client: "100", User: "alice", Title: "vsp-PRD-100"},
		{SID: "PRD", Client: "100", User: "bob", Title: "vsp-PRD-100-dup"}, // duplicate
	}

	rows, warnings := sapcreds.Hybrid(landscape, kpEntries)
	require.Len(t, rows, 1)
	assert.Equal(t, "alice", rows[0].User, "first-wins: alice retained, bob dropped")
	require.Len(t, warnings, 1, "duplicate must produce exactly one warning")
	assert.Contains(t, warnings[0], "PRD")
	assert.Contains(t, warnings[0], "100")
	assert.Contains(t, warnings[0], "vsp-PRD-100-dup")
}
