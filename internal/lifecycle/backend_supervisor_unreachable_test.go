// Tests for the StatusUnreachable gate in BackendSupervisor.Serve. Closes
// review-feature-64baca01 MEDIUM gap: integration test
// "TestBackendSupervisor_UnreachableEarlyReturn" listed in cd931db commit
// message under NOT-YET-DONE.
package lifecycle

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"mcp-gateway/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thejerf/suture/v4"
)

// TestBackendSupervisor_UnreachableEarlyReturn verifies that Serve() returns
// suture.ErrDoNotRestart immediately when the health monitor has classified
// the backend as StatusUnreachable. This prevents suture from spinning the
// restart loop on top of the monitor's slow-poll cycle (defeating the
// stop-spinning UX) and from counting the gate as a failure for the
// FailureBackoff policy. Mirrors TestBackendSupervisor_ServeReturnsErrDoNotRestartWhenDisabled
// for the parallel StatusDisabled gate at L72.
func TestBackendSupervisor_UnreachableEarlyReturn(t *testing.T) {
	checker := newFakeStatusChecker()
	checker.set("b1", models.StatusUnreachable)
	mgr := newFakeBackendManager()

	svc := NewBackendSupervisor("b1", mgr, checker, slog.Default())

	err := svc.Serve(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, suture.ErrDoNotRestart),
		"expected suture.ErrDoNotRestart for StatusUnreachable, got %v", err)
	assert.Equal(t, 0, mgr.startCalls["b1"],
		"Start must not be called when backend is StatusUnreachable — health monitor owns recovery")
	assert.Equal(t, 0, mgr.stopCalls["b1"],
		"Stop must not be called on the Unreachable gate path")
}
