package lifecycle

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckTCPReachable_ReachableHost verifies that checkTCPReachable returns
// nil when the target host is actually listening.
func TestCheckTCPReachable_ReachableHost(t *testing.T) {
	// Spin up a real listener so the dial succeeds.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	addr := ln.Addr().String()
	rawURL := "http://" + addr + "/mcp"

	ctx := context.Background()
	err = checkTCPReachable(ctx, rawURL, 3*time.Second)
	assert.NoError(t, err, "reachable host must return nil")
}

// TestCheckTCPReachable_UnreachableHost verifies that checkTCPReachable returns
// an error containing "host unreachable" when the port is closed.
func TestCheckTCPReachable_UnreachableHost(t *testing.T) {
	// Bind then immediately close to get a reliably refused port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	rawURL := "http://" + addr + "/mcp"

	ctx := context.Background()
	err = checkTCPReachable(ctx, rawURL, 3*time.Second)
	require.Error(t, err, "closed port must return an error")
	assert.Contains(t, err.Error(), "host unreachable",
		"error message must contain 'host unreachable'")
}

// TestCheckTCPReachable_FastFail verifies that checkTCPReachable respects the
// timeout parameter and returns well within 2× the given timeout rather than
// blocking indefinitely.
func TestCheckTCPReachable_FastFail(t *testing.T) {
	// 203.0.113.0/24 is TEST-NET-3 (RFC 5737) — routable but unallocated,
	// causing a timeout rather than an immediate refusal.  This simulates the
	// "host unreachable on the network" case (as opposed to a refused localhost port).
	// Use a short 500ms timeout so the test stays fast.
	rawURL := "http://203.0.113.1:9999/mcp"
	timeout := 500 * time.Millisecond

	start := time.Now()
	ctx := context.Background()
	err := checkTCPReachable(ctx, rawURL, timeout)
	elapsed := time.Since(start)

	require.Error(t, err, "non-routable address must return an error")
	assert.Less(t, elapsed, 2*timeout,
		"checkTCPReachable must return within 2× timeout (%s); elapsed=%s", timeout, elapsed)
}

// TestCheckTCPReachable_InvalidURL verifies that a malformed URL returns an
// error instead of panicking.
func TestCheckTCPReachable_InvalidURL(t *testing.T) {
	err := checkTCPReachable(context.Background(), "://not-a-url", 3*time.Second)
	require.Error(t, err, "invalid URL must return an error")
}

// TestCheckTCPReachable_DefaultPortHTTP verifies that http:// URLs with no
// explicit port default to port 80 for the TCP dial.
func TestCheckTCPReachable_DefaultPortHTTP(t *testing.T) {
	// We don't need port 80 to actually be open — we just want to confirm
	// checkTCPReachable attempts to dial port 80 (evidenced by an error that
	// does NOT say "invalid URL" but rather "host unreachable").
	err := checkTCPReachable(context.Background(), "http://127.0.0.1/no-port", 500*time.Millisecond)
	// Port 80 is almost certainly not listening in CI; expect "host unreachable".
	require.Error(t, err)
	assert.Contains(t, err.Error(), "host unreachable",
		"missing port should fall back to :80 and fail as unreachable, not as invalid URL")
}

// TestCheckTCPReachable_DefaultPortHTTPS verifies that https:// URLs with no
// explicit port default to port 443.
func TestCheckTCPReachable_DefaultPortHTTPS(t *testing.T) {
	err := checkTCPReachable(context.Background(), "https://127.0.0.1/no-port", 500*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "host unreachable",
		"missing port should fall back to :443 and fail as unreachable, not as invalid URL")
}
