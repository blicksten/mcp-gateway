package config

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWatch_FileModify(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"gateway":{}}`), 0o640))

	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- Watch(ctx, path, "", "", func() { calls.Add(1) }, nil)
	}()

	// Wait for watcher to start.
	time.Sleep(200 * time.Millisecond)

	// Modify file.
	require.NoError(t, os.WriteFile(path, []byte(`{"gateway":{"http_port":9999}}`), 0o640))

	// Wait for debounce + processing.
	time.Sleep(1 * time.Second)

	assert.GreaterOrEqual(t, calls.Load(), int32(1), "onChange should have been called")

	cancel()
	err := <-errCh
	assert.ErrorIs(t, err, context.Canceled)
}

func TestWatch_Debounce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{}`), 0o640))

	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- Watch(ctx, path, "", "", func() { calls.Add(1) }, nil)
	}()

	time.Sleep(200 * time.Millisecond)

	// Rapid writes — should be debounced into one or few calls.
	for i := range 5 {
		_ = i
		require.NoError(t, os.WriteFile(path, []byte(`{"n":1}`), 0o640))
		time.Sleep(50 * time.Millisecond)
	}

	time.Sleep(1 * time.Second)

	// Should be debounced — far fewer calls than 5 writes.
	c := calls.Load()
	assert.LessOrEqual(t, c, int32(3), "debounce should reduce call count")
	assert.GreaterOrEqual(t, c, int32(1), "at least one call expected")

	cancel()
	<-errCh
}

func TestWatch_EnvFileModify(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	envPath := filepath.Join(dir, ".env")
	require.NoError(t, os.WriteFile(cfgPath, []byte(`{}`), 0o640))
	require.NoError(t, os.WriteFile(envPath, []byte("FOO=bar\n"), 0o640))

	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- Watch(ctx, cfgPath, "", envPath, func() { calls.Add(1) }, nil)
	}()

	time.Sleep(200 * time.Millisecond)

	// Modify env file — should trigger onChange.
	require.NoError(t, os.WriteFile(envPath, []byte("FOO=baz\n"), 0o640))

	time.Sleep(1 * time.Second)

	assert.GreaterOrEqual(t, calls.Load(), int32(1), "onChange should fire on env file change")

	cancel()
	err := <-errCh
	assert.ErrorIs(t, err, context.Canceled)
}

func TestWatch_NonexistentEnvFile_NotFatal(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(cfgPath, []byte(`{}`), 0o640))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		// Non-existent env file path — should not cause Watch to fail.
		errCh <- Watch(ctx, cfgPath, "", filepath.Join(dir, "missing.env"), func() {}, nil)
	}()

	time.Sleep(200 * time.Millisecond)

	// Watch should still be running (no error from non-existent env file).
	select {
	case err := <-errCh:
		t.Fatalf("Watch should not have returned, got: %v", err)
	default:
		// expected — still running
	}

	cancel()
	err := <-errCh
	assert.ErrorIs(t, err, context.Canceled)
}

// TestWatch_OnChangeSerialization (T13A.3-4 / F-6) verifies that
// concurrent debounce firings never invoke onChange concurrently.
// Without the mutex, two rapid bursts could fire AfterFunc callbacks
// that overlap and produce duplicate reconcile work.
func TestWatch_OnChangeSerialization(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{}`), 0o640))

	var inFlight atomic.Int32
	var peak atomic.Int32
	var totalCalls atomic.Int32

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- Watch(ctx, path, "", "", func() {
			cur := inFlight.Add(1)
			// Record the peak concurrency observed.
			for {
				pv := peak.Load()
				if cur <= pv {
					break
				}
				if peak.CompareAndSwap(pv, cur) {
					break
				}
			}
			totalCalls.Add(1)
			// Sleep slightly longer than the debounce so a second
			// burst would overlap absent the mutex.
			time.Sleep(800 * time.Millisecond)
			inFlight.Add(-1)
		}, nil)
	}()

	time.Sleep(200 * time.Millisecond) // watcher boot

	// Fire a burst, wait for debounce to schedule, then fire another
	// burst while the first onChange is still sleeping.
	require.NoError(t, os.WriteFile(path, []byte(`{"a":1}`), 0o640))
	time.Sleep(600 * time.Millisecond) // > debounce(500ms), onChange running
	require.NoError(t, os.WriteFile(path, []byte(`{"a":2}`), 0o640))
	// Wait long enough for the second debounce + second onChange to run
	// to completion if serialized.
	time.Sleep(3 * time.Second)

	assert.Equal(t, int32(1), peak.Load(),
		"onChange must never be entered concurrently (peak in-flight must be 1)")
	assert.GreaterOrEqual(t, totalCalls.Load(), int32(1),
		"at least the first burst must have invoked onChange")

	cancel()
	<-errCh
}

func TestWatch_InvalidPath(t *testing.T) {
	ctx := context.Background()
	err := Watch(ctx, "/nonexistent/path/config.json", "", "", func() {}, nil)
	assert.Error(t, err)
}
