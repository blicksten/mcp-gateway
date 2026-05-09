// Package api — SAP Picker REST handlers (Phase A T-A.1).
//
// Endpoints (all under /api/v1/sap/*, authMW + same CORS as /claude-code/*,
// no csrfProtect — same precedent as ADR-0003 §csrf-scope, see
// claude-code group at server.go around l.433):
//
//   GET  /api/v1/sap/picker-snapshot — landscape ∪ KeePass picker view
//   POST /api/v1/sap/batch-begin     — open a batch (returns batch_id)
//   POST /api/v1/sap/batch-end       — close a batch, fire single regen
//
// The picker snapshot's data sources (encoding/xml landscape parser,
// structured KeePass extraction, hybrid intersection) land in T-A.3 and
// T-A.4. This file ships the contract + state machine only; the snapshot
// handler returns an empty rows slice with a warning until the data
// sources arrive. Tests in sap_picker_handler_test.go pin the contract
// shape so T-A.3/T-A.4 cannot accidentally drift from it.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"
)

// sapBatchTTL is the auto-release timeout for an idle SAP batch. A client
// that crashes between batch-begin and batch-end no longer freezes later
// batches once this window elapses. T-A.5 may shorten the value once
// per-call regen suppression is wired and the batch hold becomes a hot
// path; for the contract scaffolding here, 5 min is generous.
const sapBatchTTL = 5 * time.Minute

// SAPPickerRow is a single row returned by GET /api/v1/sap/picker-snapshot.
// Field shape matches spike §3.3 / §4.6. Fields whose values come from
// data sources not yet wired (T-A.3 landscape parser, T-A.4 KeePass) are
// zero-valued in the T-A.1 baseline; T-A.4 will populate them.
type SAPPickerRow struct {
	SID        string                 `json:"sid"`
	Client     string                 `json:"client"`
	User       string                 `json:"user,omitempty"`
	KPMissing  bool                   `json:"kpMissing"`
	Registered SAPPickerComponentBool `json:"registered"`
	Status     SAPPickerComponentStr  `json:"status"`
}

// SAPPickerComponentBool reports per-component (vsp / sap-gui) registration
// state — `registered.vsp = true` ⇔ a server named `vsp-<SID>[-<Client>]`
// is in the gateway config. Computed by T-A.4's intersection layer.
type SAPPickerComponentBool struct {
	VSP bool `json:"vsp"`
	GUI bool `json:"gui"`
}

// SAPPickerComponentStr reports per-component runtime status (e.g. "running",
// "stopped", "starting", "" when the component is not registered). Wired
// against lifecycle.Manager.Status by T-A.4.
type SAPPickerComponentStr struct {
	VSP string `json:"vsp"`
	GUI string `json:"gui"`
}

// SAPPickerSnapshot is the GET /api/v1/sap/picker-snapshot response body.
type SAPPickerSnapshot struct {
	Rows     []SAPPickerRow `json:"rows"`
	Warnings []string       `json:"warnings"`
}

// SAPBatchBeginResponse is returned by POST /api/v1/sap/batch-begin. The
// batch_id is opaque to the client — it must be echoed in the matching
// batch-end call.
type SAPBatchBeginResponse struct {
	BatchID string `json:"batch_id"`
}

// SAPBatchEndRequest is the POST /api/v1/sap/batch-end body.
type SAPBatchEndRequest struct {
	BatchID string `json:"batch_id"`
}

// SAPBatchEndResponse is the POST /api/v1/sap/batch-end response.
type SAPBatchEndResponse struct {
	OK bool `json:"ok"`
}

// handleSAPPickerSnapshot returns the picker view (T-A.1 contract; T-A.3
// + T-A.4 will populate Rows from the landscape parser × KeePass
// extraction × hybrid intersection layers).
func (s *Server) handleSAPPickerSnapshot(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, SAPPickerSnapshot{
		Rows: []SAPPickerRow{},
		Warnings: []string{
			"picker data sources not yet wired (T-A.3 landscape + T-A.4 KeePass pending)",
		},
	})
}

// handleSAPBatchBegin opens a new batch. Returns 409 if another batch is
// already open AND has not yet expired. Once T-A.5 lands, in-process
// add/remove handlers will check sapBatchActive and skip per-call
// TriggerPluginRegen + RebuildTools so the end-of-batch fire-once path
// kicks in (R-26 / X2 fix).
func (s *Server) handleSAPBatchBegin(w http.ResponseWriter, _ *http.Request) {
	now := time.Now()

	s.sapBatchMu.Lock()
	defer s.sapBatchMu.Unlock()

	if s.sapBatchID != "" && now.Before(s.sapBatchExpiry) {
		writeError(w, http.StatusConflict, "another batch is already open")
		return
	}

	id, err := newSAPBatchID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate batch_id")
		return
	}
	s.sapBatchID = id
	s.sapBatchExpiry = now.Add(sapBatchTTL)

	writeJSON(w, http.StatusOK, SAPBatchBeginResponse{BatchID: id})
}

// handleSAPBatchEnd closes the batch identified by batch_id. The
// end-of-batch single-shot TriggerPluginRegen + RebuildTools fire here
// once T-A.5 has wired the suppression (kept here so the contract is
// stable from T-A.1; T-A.5 will only have to flip the suppression flag).
func (s *Server) handleSAPBatchEnd(w http.ResponseWriter, r *http.Request) {
	var req SAPBatchEndRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.BatchID == "" {
		writeError(w, http.StatusBadRequest, "batch_id is required")
		return
	}

	s.sapBatchMu.Lock()
	if s.sapBatchID == "" {
		s.sapBatchMu.Unlock()
		writeError(w, http.StatusConflict, "no batch is open")
		return
	}
	if s.sapBatchID != req.BatchID {
		s.sapBatchMu.Unlock()
		writeError(w, http.StatusConflict, "batch_id mismatch")
		return
	}
	s.sapBatchID = ""
	s.sapBatchExpiry = time.Time{}
	s.sapBatchMu.Unlock()

	// End-of-batch single-shot regen + RebuildTools. T-A.5 will gate the
	// in-process add/remove handlers on sapBatchActive so this is the only
	// regen the client sees per batch — closing R-26 / X2 (N×plugin-regen
	// storm). At T-A.1 we still fire it so the contract behaves correctly
	// when batches wrap a single mutation; suppression is the optimisation,
	// not a correctness requirement.
	if s.gw != nil {
		s.gw.RebuildTools()
	}
	s.TriggerPluginRegen()

	writeJSON(w, http.StatusOK, SAPBatchEndResponse{OK: true})
}

// sapBatchActive reports whether a non-expired SAP batch is currently
// open. Read by T-A.5's addServerInProcess / removeServerInProcess to
// decide whether to skip per-call TriggerPluginRegen.
func (s *Server) sapBatchActive() bool {
	s.sapBatchMu.Lock()
	defer s.sapBatchMu.Unlock()
	return s.sapBatchID != "" && time.Now().Before(s.sapBatchExpiry)
}

// newSAPBatchID returns a 16-hex-char random batch identifier. Same shape
// as patchstate.newID — see internal/patchstate/state.go:678. Lifted into
// this file to keep sap_picker_handler self-contained; the cost is one
// extra symbol vs. cross-package import.
func newSAPBatchID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
