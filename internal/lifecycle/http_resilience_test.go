package lifecycle

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"mcp-gateway/internal/health"
	"mcp-gateway/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPBackend_SyntheticLogs(t *testing.T) {
	url := startHTTPMockServer(t, "http")

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"log-test": {URL: url},
		},
	}
	cfg.ApplyDefaults()

	m := NewManager(cfg, "test", testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	require.NoError(t, m.Start(ctx, "log-test"))

	// Check ring buffer for synthetic log entries.
	ring, ok := m.LogBuffer("log-test")
	require.True(t, ok)

	lines := ring.Lines()
	require.NotEmpty(t, lines, "expected synthetic log entries for HTTP backend")

	var found []string
	for _, l := range lines {
		found = append(found, l.Text)
	}
	t.Logf("Synthetic logs: %v", found)

	// Should contain connection-related entries (search by content, not position).
	var hasConnecting, hasEstablished bool
	for _, line := range found {
		if !hasConnecting {
			hasConnecting = line == "[gateway] connecting to HTTP endpoint: "+url
		}
		if !hasEstablished {
			hasEstablished = line == "[gateway] HTTP connection established"
		}
	}
	assert.True(t, hasConnecting, "expected 'connecting' log entry")
	assert.True(t, hasEstablished, "expected 'connection established' log entry")

	// Stop and check for session closing log.
	require.NoError(t, m.Stop(ctx, "log-test"))

	lines = ring.Lines()
	var lastLog string
	for _, l := range lines {
		lastLog = l.Text
	}
	assert.Contains(t, lastLog, "[gateway] session closing")
}

func TestHTTPBackend_InitialConnectionFailure(t *testing.T) {
	// Point to a port that nothing is listening on.
	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"bad-url": {URL: "http://127.0.0.1:1/mcp"},
		},
	}
	cfg.ApplyDefaults()

	m := NewManager(cfg, "test", testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := m.Start(ctx, "bad-url")
	require.Error(t, err, "connecting to closed port should fail")
	t.Logf("Expected error: %v", err)

	e, ok := m.Entry("bad-url")
	require.True(t, ok)
	assert.Equal(t, models.StatusError, e.Status)
	assert.NotEmpty(t, e.LastError)

	// Ring buffer should have a failure log entry.
	ring, ok := m.LogBuffer("bad-url")
	require.True(t, ok)
	lines := ring.Lines()
	require.NotEmpty(t, lines)
	var hasConnecting, hasFailedLog bool
	for _, l := range lines {
		if l.Text != "" {
			t.Logf("Log: %s", l.Text)
		}
		if l.Text == "[gateway] connecting to HTTP endpoint: http://127.0.0.1:1/mcp" {
			hasConnecting = true
		}
		if strings.HasPrefix(l.Text, "[gateway] HTTP connect failed:") {
			hasFailedLog = true
		}
	}
	assert.True(t, hasConnecting, "should log connection attempt")
	assert.True(t, hasFailedLog, "should log connection failure")
}

func TestHealthMonitor_HTTPBackend(t *testing.T) {
	url := startHTTPMockServer(t, "http")

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"health-http": {URL: url},
		},
	}
	cfg.ApplyDefaults()

	m := NewManager(cfg, "test", testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	require.NoError(t, m.Start(ctx, "health-http"))

	// Create health monitor with fast interval.
	monitor := health.NewMonitor(m, 1*time.Second, testLogger())
	monitor.ConsecutiveFailureThreshold = 3

	// Run one health check — should succeed.
	monitor.CheckOnce(ctx)
	e, _ := m.Entry("health-http")
	assert.Equal(t, models.StatusRunning, e.Status, "healthy HTTP backend should stay running")

	// Stop the backend (simulates session close without killing external process).
	require.NoError(t, m.Stop(ctx, "health-http"))

	// Health check on a stopped server should not crash.
	// The monitor skips servers that are not in Running/Degraded/Error state.
	monitor.CheckOnce(ctx)
	e, _ = m.Entry("health-http")
	assert.Equal(t, models.StatusStopped, e.Status)
}

func TestHTTPBackend_GoroutineLeak(t *testing.T) {
	url := startHTTPMockServer(t, "http")

	cfg := &models.Config{
		Servers: map[string]*models.ServerConfig{
			"leak-test": {URL: url},
		},
	}
	cfg.ApplyDefaults()

	m := NewManager(cfg, "test", testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Warm up — first connect may create background goroutines in the SDK.
	require.NoError(t, m.Start(ctx, "leak-test"))
	require.NoError(t, m.Stop(ctx, "leak-test"))
	time.Sleep(100 * time.Millisecond) // let goroutines settle

	baseline := runtime.NumGoroutine()
	t.Logf("Baseline goroutines: %d", baseline)

	// Start/stop 5 times.
	for i := range 5 {
		require.NoError(t, m.Start(ctx, "leak-test"), "start iteration %d", i)
		require.NoError(t, m.Stop(ctx, "leak-test"), "stop iteration %d", i)
	}

	time.Sleep(500 * time.Millisecond) // let goroutines settle

	after := runtime.NumGoroutine()
	t.Logf("After 5 cycles: %d goroutines (baseline: %d)", after, baseline)

	// Allow some slack (SDK may keep a few persistent goroutines).
	// But growth should not be proportional to cycle count.
	maxAllowed := baseline + 10
	assert.LessOrEqual(t, after, maxAllowed,
		"goroutine count grew from %d to %d after 5 start/stop cycles — possible leak", baseline, after)
}
