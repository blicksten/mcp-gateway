package sapcreds

import (
	"fmt"

	"mcp-gateway/internal/saplandscape"
)

// Row is one result row in the hybrid landscape × KeePass intersection.
// The landscape is the master list — every landscape SID/Client appears in
// the result. KPMissing is set when no matching KeePass entry was found for
// the SID+Client pair.
type Row struct {
	SID       string
	Client    string
	User      string
	KPMissing bool
}

// Hybrid joins the landscape service list against the KeePass entry list.
// All landscape SIDs are returned. KP-only entries (not in landscape) are
// excluded per spike requirement R-14.
//
// Returns (rows, warnings). warnings is non-empty when KP contains duplicate
// (SID, Client) entries (first-wins is applied; later duplicates are skipped
// and surfaced as warnings so operators know the picker may be hiding a
// conflicting credential — F-04 fix).
func Hybrid(landscape *saplandscape.Landscape, kpEntries []Entry) ([]Row, []string) {
	if landscape == nil {
		return nil, nil
	}

	// Build a lookup map from (SID, Client) → KeePass entry for O(1) joins.
	// Duplicate (SID, Client) entries are first-wins; later ones are surfaced
	// as warnings so operators can resolve the conflict in their vault.
	type key struct{ sid, client string }
	kpMap := make(map[key]Entry, len(kpEntries))
	var warnings []string
	for _, e := range kpEntries {
		k := key{e.SID, e.Client}
		if _, exists := kpMap[k]; exists {
			warnings = append(warnings, fmt.Sprintf(
				"duplicate KeePass entry for (SID=%q, Client=%q) — first-wins, ignoring %q",
				e.SID, e.Client, e.Title))
			continue
		}
		kpMap[k] = e
	}

	rows := make([]Row, 0, len(landscape.Services))
	for _, svc := range landscape.Services {
		k := key{svc.SID, svc.Client}
		if kp, found := kpMap[k]; found {
			rows = append(rows, Row{
				SID:       svc.SID,
				Client:    svc.Client,
				User:      kp.User,
				KPMissing: false,
			})
		} else {
			rows = append(rows, Row{
				SID:       svc.SID,
				Client:    svc.Client,
				KPMissing: true,
			})
		}
	}
	return rows, warnings
}
