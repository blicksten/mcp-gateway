package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"mcp-gateway/internal/models"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thejerf/suture/v4"
)

// BackendKeepaliveInterval is the go-sdk KeepAlive ping period injected into each
// backend's ClientSession.  A missed ping causes session.Close() which unblocks
// session.Wait() and lets Serve() return so suture can restart the backend.
const BackendKeepaliveInterval = 30 * time.Second

// StatusChecker is the read-only view of the health monitor used by BackendSupervisor
// to decide whether the circuit is open (StatusDisabled) before attempting a restart.
type StatusChecker interface {
	BackendStatus(name string) models.ServerStatus
}

// BackendManager is the lifecycle facade a BackendSupervisor drives.
type BackendManager interface {
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
	Session(name string) (*mcp.ClientSession, bool)
	SetStatus(name string, status models.ServerStatus, lastErr string)
}

// BackendSupervisor implements suture.Service for a single MCP backend.
// suture calls Serve() in a goroutine and re-calls it when it returns an
// error, honouring FailureThreshold / FailureBackoff / FailureDecay from
// DefaultSupervisorSpec.
type BackendSupervisor struct {
	name    string
	manager BackendManager
	checker StatusChecker
	logger  *slog.Logger
}

// NewBackendSupervisor constructs a BackendSupervisor.
func NewBackendSupervisor(
	name string,
	manager BackendManager,
	checker StatusChecker,
	logger *slog.Logger,
) *BackendSupervisor {
	return &BackendSupervisor{
		name:    name,
		manager: manager,
		checker: checker,
		logger:  logger,
	}
}

// String satisfies fmt.Stringer — suture uses this for log messages.
func (b *BackendSupervisor) String() string {
	return "backend/" + b.name
}

// Serve is called by suture each time the service should run.
// It returns nil on clean shutdown (context cancelled) and a non-nil error on
// unexpected termination so suture applies the failure-backoff policy.
// Returning suture.ErrDoNotRestart removes the service when the circuit is open.
func (b *BackendSupervisor) Serve(ctx context.Context) error {
	// Gate: if the health monitor has opened the circuit, do not attempt restart.
	if b.checker.BackendStatus(b.name) == models.StatusDisabled {
		b.logger.Info("suture: circuit open, not restarting", "backend", b.name)
		return suture.ErrDoNotRestart
	}
	// Gate: StatusUnreachable backends are slow-polled by the health
	// monitor (60s probe cycle) instead of suture-restarted. Returning
	// ErrDoNotRestart here removes the service from the supervisor;
	// the monitor's maybeProbeUnreachable calls AddBackendToSupervisor
	// (via the LifecycleManager interface) to re-add it after a successful
	// Start() when the host becomes reachable again. Without this gate,
	// suture's FailureBackoff would still pile retries on top of the
	// monitor's slow-poll, defeating the "stop spinning" UX.
	// See docs/PLAN-unreachable-handling.md.
	if b.checker.BackendStatus(b.name) == models.StatusUnreachable {
		b.logger.Info("suture: backend unreachable, deferring to health monitor slow-poll",
			"backend", b.name)
		return suture.ErrDoNotRestart
	}

	b.logger.Info("suture: starting backend", "backend", b.name)
	if err := b.manager.Start(ctx, b.name); err != nil {
		b.logger.Warn("suture: start failed", "backend", b.name, "err", err)
		return fmt.Errorf("backend %s start: %w", b.name, err)
	}

	session, ok := b.manager.Session(b.name)
	if !ok {
		// Backend started but produced no session — treat as transient failure.
		b.logger.Warn("suture: no session after start", "backend", b.name)
		return fmt.Errorf("backend %s: no session after start", b.name)
	}

	// Wait for session termination or context cancellation.
	done := make(chan error, 1)
	go func() {
		done <- session.Wait()
	}()

	select {
	case <-ctx.Done():
		// Supervisor is shutting down cleanly — stop the backend and return nil
		// so suture does not count this as a failure.
		stopErr := b.manager.Stop(context.Background(), b.name)
		if stopErr != nil && !errors.Is(stopErr, context.Canceled) {
			b.logger.Warn("suture: stop error on context cancel", "backend", b.name, "err", stopErr)
		}
		// CV /check LOW: drain the session.Wait() goroutine bounded by a short
		// deadline so it does not outlive Serve() return. Stop() closes the
		// session which should unblock Wait() immediately; the timeout caps
		// the leak window if the SDK ever fails to close cleanly.
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			b.logger.Warn("suture: session.Wait() did not return after Stop", "backend", b.name)
		}
		return nil

	case sessionErr := <-done:
		// HIGH-2 (P1.5 step 2): distinguish intentional stop from unexpected
		// session loss. Manager.Stop writes StatusStopped before session.Close
		// (manager.go ~line 472), so by the time Wait() unblocks here the status
		// is Stopped for a plain Stop. For a Restart the sequence is:
		//   1. Restart sets StatusRestarting under m.mu.Lock (manager.go ~561).
		//   2. Restart calls Stop. Stop sees StatusRestarting and PRESERVES it
		//      (manager.go ~480 — conditional write).
		//   3. Stop closes the session; Wait() unblocks here.
		//   4. We read BackendStatus -> see Restarting -> fall through to the
		//      "session ended unexpectedly" error so suture's FailureBackoff
		//      retries. Restart's own m.Start runs concurrently; the Manager's
		//      `starting` guard serialises the two paths.
		// Returning ErrDoNotRestart on Stopped/Disabled prevents a phantom
		// restart after a deliberate Manager.Stop call.
		// Tested by: TestBackendSupervisor_CleanStopNoRestart_HIGH2,
		//   TestBackendSupervisor_DisabledBottomBranch_HIGH2,
		//   TestBackendSupervisor_RestartWithRealSession_NoErrDoNotRestart.
		status := b.checker.BackendStatus(b.name)
		if status == models.StatusStopped || status == models.StatusDisabled {
			b.logger.Info("suture: deliberate stop detected, not restarting",
				"backend", b.name, "status", status)
			return suture.ErrDoNotRestart
		}
		if sessionErr == nil || errors.Is(sessionErr, context.Canceled) {
			// Session ended cleanly but backend is not stopped — allow suture to restart.
			return fmt.Errorf("backend %s: session ended unexpectedly", b.name)
		}
		b.logger.Warn("suture: session terminated with error", "backend", b.name, "err", sessionErr)
		return fmt.Errorf("backend %s session: %w", b.name, sessionErr)
	}
}

// DefaultSupervisorSpec returns the suture.Spec used for every backend supervisor.
//
//   - FailureThreshold=5 / FailureDecay=30s: opens circuit after 5 failures in 30 s
//   - FailureBackoff=15s: minimum pause between restart attempts
//   - DontPropagateTermination=true: one backend's death does not kill siblings
func DefaultSupervisorSpec(name string, logger *slog.Logger) suture.Spec {
	return suture.Spec{
		EventHook: func(e suture.Event) {
			switch ev := e.(type) {
			case suture.EventServicePanic:
				logger.Error("suture panic", "backend", name, "msg", ev.PanicMsg)
			case suture.EventServiceTerminate:
				logger.Warn("suture terminate", "backend", name, "err", ev.Err)
			case suture.EventBackoff:
				logger.Info("suture backoff", "backend", name)
			case suture.EventStopTimeout:
				logger.Warn("suture stop timeout", "backend", name)
			}
		},
		FailureThreshold:         5,
		FailureDecay:             30,
		FailureBackoff:           15 * time.Second,
		DontPropagateTermination: true,
	}
}

// NewBackendSupervisorTree creates a root suture.Supervisor containing one
// child supervisor per backend, each backed by a BackendSupervisor service.
// The tree is returned stopped — call supervisor.ServeBackground(ctx) to start.
//
// Note: the root spec intentionally omits FailureThreshold/FailureBackoff/
// FailureDecay because it contains only child *suture.Supervisor instances —
// never BackendSupervisor services directly — so the root's restart policy
// is never exercised. The child specs (DefaultSupervisorSpec) carry the real
// policy. DontPropagateTermination=true on both root AND child guarantees
// one backend's panic does not cascade across siblings (R1 fault isolation).
func NewBackendSupervisorTree(
	manager BackendManager,
	checker StatusChecker,
	names []string,
	logger *slog.Logger,
) *suture.Supervisor {
	tree, _ := newBackendSupervisorTreeWithTokens(manager, checker, names, logger)
	return tree
}

// newBackendSupervisorTreeWithTokens is the internal variant that also
// returns the per-backend ServiceToken map so Manager.SetupSupervisor can
// later RemoveAndWait a startup-time backend via the same code path as
// runtime-added ones (Task C 2026-05-22).
func newBackendSupervisorTreeWithTokens(
	manager BackendManager,
	checker StatusChecker,
	names []string,
	logger *slog.Logger,
) (*suture.Supervisor, map[string]suture.ServiceToken) {
	rootSpec := suture.Spec{
		EventHook: func(e suture.Event) {
			if ev, ok := e.(suture.EventServicePanic); ok {
				logger.Error("suture root panic", "msg", ev.PanicMsg)
			}
		},
		DontPropagateTermination: true,
	}
	root := suture.New("mcp-gateway", rootSpec)
	tokens := make(map[string]suture.ServiceToken, len(names))

	for _, name := range names {
		svc := NewBackendSupervisor(name, manager, checker, logger)
		childSpec := DefaultSupervisorSpec(name, logger)
		child := suture.New("backends/"+name, childSpec)
		child.Add(svc)
		tokens[name] = root.Add(child)
	}

	return root, tokens
}
