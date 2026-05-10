package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"mcp-gateway/internal/claudeconfig"
	"mcp-gateway/internal/claudeimport"
	"mcp-gateway/internal/models"
)

// ImportSnapshotRow is one row in the GET /import-snapshot response.
//
// Per spike §4.2: each row carries enough state to render a picker entry
// + drift warning + provenance badge.
type ImportSnapshotRow struct {
	Source              string   `json:"source"`
	Name                string   `json:"name"`
	Type                string   `json:"type"`
	Command             string   `json:"command,omitempty"`
	Args                []string `json:"args,omitempty"`
	URL                 string   `json:"url,omitempty"`
	GatewayHasName      bool     `json:"gateway_has_name"`
	DriftFields         []string `json:"drift_fields,omitempty"`
	PreviouslyImported  bool     `json:"previously_imported"`
	PreviouslyImportedAt string   `json:"previously_imported_at,omitempty"`
}

// ImportSnapshotResponse is the GET /import-snapshot response shape.
type ImportSnapshotResponse struct {
	Source   string              `json:"source"`
	Path     string              `json:"path"`
	Exists   bool                `json:"exists"`
	Rows     []ImportSnapshotRow `json:"rows"`
	Warnings []string            `json:"warnings,omitempty"`
}

// ImportApplyRequest is the POST /import-apply request shape.
//
// BatchID is currently optional — Phase D ships without an explicit
// import-batch begin/end pair (unlike SAP picker which has them).
// Instead, /import-apply does a single batch internally: sets a flag,
// runs ops, fires one regen at the end. If a future cross-source bulk
// import is added, switching to explicit batch endpoints is additive.
type ImportApplyRequest struct {
	Ops []claudeimport.Op `json:"ops"`
}

// ImportApplyResponse is the POST /import-apply response shape.
type ImportApplyResponse struct {
	Results []claudeimport.OpResult `json:"results"`
}

// handleImportSnapshot serves GET /api/v1/claude-code/import-snapshot.
//
// Query params:
//   - source: cc_global | cc_project | desktop  (required)
//   - project_root: workspace path  (required when source=cc_project)
//
// Returns 400 on bad/missing params, 200 with a possibly-empty rows
// list on success. A non-existent source file is NOT an error — the
// response will have exists=false and rows=[].
func (s *Server) handleImportSnapshot(w http.ResponseWriter, r *http.Request) {
	srcStr := r.URL.Query().Get("source")
	if srcStr == "" {
		writeError(w, http.StatusBadRequest, "source query param required")
		return
	}
	src := claudeconfig.Source(srcStr)
	switch src {
	case claudeconfig.SourceCCGlobal, claudeconfig.SourceCCProject, claudeconfig.SourceDesktop:
	default:
		writeError(w, http.StatusBadRequest, "unknown source")
		return
	}

	projectRoot := r.URL.Query().Get("project_root")
	if src == claudeconfig.SourceCCProject && projectRoot == "" {
		writeError(w, http.StatusBadRequest, "project_root query param required for cc_project source")
		return
	}

	snap, err := claudeconfig.Read(src, projectRoot)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Build a sorted list of names from the snapshot for stable ordering.
	names := make([]string, 0, len(snap.Entries))
	for k := range snap.Entries {
		names = append(names, k)
	}
	sort.Strings(names)

	// Provenance log — load once for this snapshot to drive the
	// "previously imported" badge.
	sidecar := s.importSidecarPath()
	prov, _ := claudeimport.LoadProvenance(sidecar)

	// Gateway state — used for drift detection.
	gwState := s.gatewayStateForImportDiff()

	resp := ImportSnapshotResponse{
		Source:   string(src),
		Path:     snap.Path,
		Exists:   snap.Exists,
		Warnings: snap.Warnings,
		Rows:     make([]ImportSnapshotRow, 0, len(names)),
	}
	for _, name := range names {
		entry := snap.Entries[name]
		row := ImportSnapshotRow{
			Source:  string(src),
			Name:    name,
			Type:    entry.Type,
			Command: entry.Command,
			Args:    entry.Args,
			URL:     entry.URL,
		}
		_, gwHas := gwState.Entries[name]
		row.GatewayHasName = gwHas
		if gwHas {
			row.DriftFields = claudeimport.DriftFields(json.RawMessage(entry.Raw), gwState, name)
		}
		row.PreviouslyImported, row.PreviouslyImportedAt = claudeimport.PreviouslyImported(prov, string(src), name)
		resp.Rows = append(resp.Rows, row)
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleImportApply serves POST /api/v1/claude-code/import-apply.
//
// Decodes ops, builds the gateway state + Adder/Remover bridge, calls
// claudeimport.Apply, then fires one TriggerPluginRegen + RebuildTools
// at end of batch (R-26 / X2 fix).
func (s *Server) handleImportApply(w http.ResponseWriter, r *http.Request) {
	var req ImportApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if len(req.Ops) == 0 {
		writeError(w, http.StatusBadRequest, "ops list is empty")
		return
	}

	deps := claudeimport.Dependencies{
		Adder:           s.importAdder,
		Remover:         s.importRemover,
		GatewaySnapshot: s.gatewayStateForImportApply(),
		SidecarPath:     s.importSidecarPath(),
		// T-D.4 hooks: BeforeSourceWrite/AfterSourceWrite are optional.
		// Phase D ships without an in-process Pause/Resume primitive
		// (the TS-side reflector's existing CAS handles concurrent
		// writers). Hooks are wired so a future implementation can
		// bolt them on without changing the call site.
		BeforeSourceWrite: nil,
		AfterSourceWrite:  nil,
	}
	results := claudeimport.Apply(r.Context(), req.Ops, deps)

	// Single end-of-batch regen + RebuildTools — mirrors the SAP batch
	// pattern (R-26 / X2 fix). Skip when nothing changed (no Applied
	// rows) to avoid wasted work on all-skip / all-error batches
	// (F-06 fix).
	anyApplied := false
	for i := range results {
		if results[i].Status == claudeimport.StatusApplied {
			anyApplied = true
			break
		}
	}
	if anyApplied {
		if s.gw != nil {
			s.gw.RebuildTools()
		}
		s.TriggerPluginRegen()
	}

	writeJSON(w, http.StatusOK, ImportApplyResponse{Results: results})
}

// importAdder bridges claudeimport's typed Adder to the server's
// addServerInProcess. The conversion is type-only; the Suppress…
// flag passes through.
func (s *Server) importAdder(ctx context.Context, name string, sc *models.ServerConfig, opts claudeimport.AddOpts) error {
	return s.addServerInProcess(ctx, name, sc, AddOpts{
		SuppressPluginRegen: opts.SuppressPluginRegen,
		SkipAutoStart:       opts.SkipAutoStart,
	})
}

// importRemover bridges claudeimport's typed Remover.
func (s *Server) importRemover(ctx context.Context, name string, opts claudeimport.RemoveOpts) (claudeimport.RemoveResult, error) {
	res, err := s.removeServerInProcess(ctx, name, RemoveOpts{
		SuppressPluginRegen: opts.SuppressPluginRegen,
	})
	if err != nil {
		return claudeimport.RemoveResult{}, err
	}
	return claudeimport.RemoveResult{
		Orphan:  res.Orphan,
		StopErr: res.StopErr,
	}, nil
}

// importSidecarPath returns the path to the provenance sidecar.
// Wraps DefaultSidecarPath so tests can override via build tag if needed.
func (s *Server) importSidecarPath() string {
	p, err := claudeimport.DefaultSidecarPath()
	if err != nil {
		return ""
	}
	return p
}

// gatewayStateForImportDiff builds a snapshot of the gateway's current
// Servers map for use by DriftFields. Each entry is marshalled to a
// compact JSON RawMessage so the diff helper can compare key-by-key.
func (s *Server) gatewayStateForImportDiff() claudeimport.GatewayState {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	out := claudeimport.GatewayState{
		Entries: make(map[string]json.RawMessage, len(s.cfg.Servers)),
	}
	for name, sc := range s.cfg.Servers {
		raw, err := json.Marshal(sc)
		if err != nil {
			continue
		}
		out.Entries[name] = raw
	}
	return out
}

// gatewayStateForImportApply mirrors gatewayStateForImportDiff but in
// claudeimport's GatewaySnapshot type. A separate function rather than
// a typed alias because the two packages are separate concerns and
// future divergence (e.g. the Apply-side wanting timestamps) would
// otherwise be silently shared.
func (s *Server) gatewayStateForImportApply() claudeimport.GatewaySnapshot {
	state := s.gatewayStateForImportDiff()
	return claudeimport.GatewaySnapshot{Entries: state.Entries}
}

// importApplyTimestamp returns the current UTC time formatted to RFC3339Nano.
// Indirection point for tests that want deterministic timestamps.
func importApplyTimestamp() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
