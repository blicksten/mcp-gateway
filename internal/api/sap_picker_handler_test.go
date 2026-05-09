package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSAPPicker_Snapshot pins the GET /api/v1/sap/picker-snapshot contract
// shape (T-A.1). Until T-A.3 / T-A.4 wire the data sources, the rows
// slice is empty and a single warning explains the gap. The contract
// fields exist so webview code can be written against this baseline.
func TestSAPPicker_Snapshot(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	rr := doRequest(t, handler, http.MethodGet, "/api/v1/sap/picker-snapshot", nil)
	require.Equal(t, http.StatusOK, rr.Code)

	var snap SAPPickerSnapshot
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &snap))
	assert.NotNil(t, snap.Rows, "rows must be a non-nil slice (even when empty) so JSON consumers don't trip on null")
	assert.Empty(t, snap.Rows, "T-A.1 baseline: rows are empty until T-A.3 + T-A.4")
	require.Len(t, snap.Warnings, 1)
	assert.Contains(t, snap.Warnings[0], "T-A.3")
	assert.Contains(t, snap.Warnings[0], "T-A.4")
}

// TestSAPPicker_BatchBeginEnd_Roundtrip verifies the happy-path begin →
// end transition: begin returns a 16-hex-char batch_id, end accepts the
// same id, and the active flag returns to false.
func TestSAPPicker_BatchBeginEnd_Roundtrip(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	beginRR := doRequest(t, handler, http.MethodPost, "/api/v1/sap/batch-begin", nil)
	require.Equal(t, http.StatusOK, beginRR.Code)

	var beginResp SAPBatchBeginResponse
	require.NoError(t, json.Unmarshal(beginRR.Body.Bytes(), &beginResp))
	assert.Len(t, beginResp.BatchID, 16, "batch_id is 16 hex chars (8 random bytes)")
	assert.True(t, srv.sapBatchActive(), "batch must be active immediately after begin")

	endRR := doRequest(t, handler, http.MethodPost, "/api/v1/sap/batch-end", SAPBatchEndRequest{BatchID: beginResp.BatchID})
	require.Equal(t, http.StatusOK, endRR.Code)

	var endResp SAPBatchEndResponse
	require.NoError(t, json.Unmarshal(endRR.Body.Bytes(), &endResp))
	assert.True(t, endResp.OK)
	assert.False(t, srv.sapBatchActive(), "batch must be cleared after end")
}

// TestSAPPicker_BatchBegin_Conflict409 covers the no-nested-batches v1
// rule: a second begin while a non-expired batch is open must 409.
func TestSAPPicker_BatchBegin_Conflict409(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	first := doRequest(t, handler, http.MethodPost, "/api/v1/sap/batch-begin", nil)
	require.Equal(t, http.StatusOK, first.Code)

	second := doRequest(t, handler, http.MethodPost, "/api/v1/sap/batch-begin", nil)
	assert.Equal(t, http.StatusConflict, second.Code, "second begin must 409 — no nested batches in v1")
}

// TestSAPPicker_BatchEnd_NoOpenBatch409 — end without an active batch
// must 409.
func TestSAPPicker_BatchEnd_NoOpenBatch409(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	rr := doRequest(t, handler, http.MethodPost, "/api/v1/sap/batch-end", SAPBatchEndRequest{BatchID: "deadbeefdeadbeef"})
	assert.Equal(t, http.StatusConflict, rr.Code)
}

// TestSAPPicker_BatchEnd_MismatchedID409 — the batch_id submitted to end
// must match the open batch. Stale IDs from a crashed earlier client
// must not close someone else's batch.
func TestSAPPicker_BatchEnd_MismatchedID409(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	begin := doRequest(t, handler, http.MethodPost, "/api/v1/sap/batch-begin", nil)
	require.Equal(t, http.StatusOK, begin.Code)

	end := doRequest(t, handler, http.MethodPost, "/api/v1/sap/batch-end", SAPBatchEndRequest{BatchID: "00000000ffffffff"})
	assert.Equal(t, http.StatusConflict, end.Code)
	assert.True(t, srv.sapBatchActive(), "mismatched-id end must not close the live batch")
}

// TestSAPPicker_BatchEnd_BadJSON400 — malformed body returns 400, not 409,
// so the client can distinguish a wire-format bug from a stale batch_id.
func TestSAPPicker_BatchEnd_BadJSON400(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	begin := doRequest(t, handler, http.MethodPost, "/api/v1/sap/batch-begin", nil)
	require.Equal(t, http.StatusOK, begin.Code)

	// Send raw text instead of JSON. Inlined to avoid a one-off helper.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sap/batch-end", bytes.NewReader([]byte("this is not json")))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// TestSAPPicker_BatchEnd_MissingID400 — an empty batch_id is a client
// programming error, not a transient conflict.
func TestSAPPicker_BatchEnd_MissingID400(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	begin := doRequest(t, handler, http.MethodPost, "/api/v1/sap/batch-begin", nil)
	require.Equal(t, http.StatusOK, begin.Code)

	end := doRequest(t, handler, http.MethodPost, "/api/v1/sap/batch-end", SAPBatchEndRequest{BatchID: ""})
	assert.Equal(t, http.StatusBadRequest, end.Code)
}

// TestSAPPicker_Batch_TTLAutoRelease — once sapBatchExpiry passes, a
// fresh begin succeeds even if end was never called. Confirms the
// abandoned-client safety net described in sap_picker_handler.go's
// docstring.
func TestSAPPicker_Batch_TTLAutoRelease(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	begin := doRequest(t, handler, http.MethodPost, "/api/v1/sap/batch-begin", nil)
	require.Equal(t, http.StatusOK, begin.Code)
	assert.True(t, srv.sapBatchActive())

	// Force expiry without waiting 5 minutes.
	srv.sapBatchMu.Lock()
	srv.sapBatchExpiry = time.Now().Add(-time.Second)
	srv.sapBatchMu.Unlock()
	assert.False(t, srv.sapBatchActive(), "expired batch must report inactive")

	second := doRequest(t, handler, http.MethodPost, "/api/v1/sap/batch-begin", nil)
	assert.Equal(t, http.StatusOK, second.Code, "second begin must succeed once the prior batch expired")
}
