//go:build long

// Package api — long-run (720 s) stream survival test for the
// clearWriteDeadlineForGET middleware (F-8 / BL-3).
//
// Run with:
//
//	go test -tags long -timeout 15m ./internal/api/... -run TestMCPStreamWriteDeadline_LongRun
package api

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mcp-gateway/internal/models"
)

// TestMCPStreamWriteDeadline_LongRun is the BL-3 full acceptance test.
// It opens a GET /mcp stream and holds it for ≥720 s with the production
// WriteTimeout (10 min). The clearWriteDeadlineForGET middleware must keep
// the connection alive for the full duration; without it the server's
// 10-min WriteTimeout fires at ~660 s and sends a TCP RST.
func TestMCPStreamWriteDeadline_LongRun(t *testing.T) {
	const streamDuration = 720 * time.Second

	srv := newTLSTestServer(t, models.GatewaySettings{
		HTTPPort:    0,
		BindAddress: "127.0.0.1",
		Transports:  []string{"http"},
	}, AuthConfig{})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(10 * time.Second):
		}
	})

	addr := waitForListener(t, srv)
	// Leave srv.httpServer.WriteTimeout at its production default (10 min).

	reqCtx, reqCancel := context.WithTimeout(context.Background(), streamDuration+30*time.Second)
	defer reqCancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet,
		"http://"+addr.String()+"/mcp", nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/event-stream")

	client := &http.Client{
		Transport: &http.Transport{DisableKeepAlives: false},
	}

	start := time.Now()
	resp, doErr := client.Do(req)
	elapsed := time.Since(start)

	if doErr != nil {
		if elapsed < streamDuration {
			t.Errorf("BL-3 FAIL: GET /mcp stream died at %v (before %v); "+
				"clearWriteDeadlineForGET may not be clearing the write deadline. error: %v",
				elapsed, streamDuration, doErr)
		} else {
			t.Logf("BL-3 PASS: GET /mcp survived %v before connection closed (%v)", elapsed, doErr)
		}
		return
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	elapsed = time.Since(start)
	t.Logf("BL-3 stream completed in %v with status %d", elapsed, resp.StatusCode)
	assert.GreaterOrEqual(t, elapsed, streamDuration,
		"BL-3: GET /mcp must survive ≥ %v before completing", streamDuration)
}
