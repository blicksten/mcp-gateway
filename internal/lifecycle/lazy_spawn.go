// Package lifecycle — TASK C2.2: lazy-spawn coordinator.
//
// EnsureStarted is the single entry point for on-demand backend spawning.
// It is called by the router when a tool call arrives for a StatusIdle backend.
//
// Design: docs/DESIGN-mcp-gateway-lazy-spawn.md §4.4 (Option C+).
// Feature flag: MCP_GATEWAY_LAZY_SPAWN (default OFF).
// When the flag is OFF, EnsureStarted is never called (router keeps the
// existing rejection path), so this file has zero runtime impact.
//
// Concurrency model:
//
//	singleflight.Group deduplicates concurrent first-invoke races — N callers
//	all seeing StatusIdle trigger exactly ONE Start call; every caller
//	receives the same (status, err) via DoChan. The spawn goroutine runs on
//	a DETACHED context (context.Background bounded by lazySpawnMaxStartup)
//	so it survives the caller's deadline; callers that time out while waiting
//	receive ErrLazyWarming and may retry on the next call.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"time"

	"mcp-gateway/internal/models"
)

// ErrLazyWarming is returned to a caller whose budget expires while the
// background spawn is still in progress. The spawn continues; a subsequent
// call will find the backend Running (or Error on failure).
var ErrLazyWarming = errors.New("backend warming — tools spawning, retry shortly")

// lazySpawnMaxStartup is the hard cap for a background spawn.
// The spawn context uses context.Background so it is not bound by the
// caller's deadline; this constant prevents an indefinite wait.
const lazySpawnMaxStartup = 60 * time.Second

// lazySpawnServiceCap is the default caller budget when the request context
// has no deadline. Kept short so callers do not stall indefinitely in
// environments where no per-request timeout is set.
const lazySpawnServiceCap = 7 * time.Second

// lazySpawnBudgetFloor is the minimum budget allocated to a caller even when
// the request deadline is very near. Prevents immediate-return on tiny budgets.
const lazySpawnBudgetFloor = 500 * time.Millisecond

// IsLazyPending reports whether a lazy spawn is currently in flight for name.
// Returns true from the moment the singleflight goroutine marks the backend
// pending until the spawn completes (success or failure). Used by filteredTools
// in the gateway to keep advertised tools visible during the brief
// StatusStarting window that would otherwise hide them.
func (m *Manager) IsLazyPending(name string) bool {
	m.lazyPendingMu.Lock()
	defer m.lazyPendingMu.Unlock()
	_, ok := m.lazyPending[name]
	return ok
}

// EnsureStarted guarantees that a StatusIdle backend is running before a
// tool call is dispatched. It is a no-op for already-Running/Degraded backends.
//
// Control flow:
//
//  1. Fast path: if the backend is already Running or Degraded, return (status, nil).
//  2. Compute caller budget from ctx.Deadline (70%) or the default cap, floored at 500ms.
//  3. Launch (or join) a singleflight goroutine via DoChan. The goroutine:
//     a. Marks the backend lazyPending (cleared in defer).
//     b. Spawns on a DETACHED context (Background + lazySpawnMaxStartup).
//     c. On success: registers with the supervisor, refreshes the manifest, returns StatusRunning.
//     d. On failure: sets StatusError, removes the manifest entry, fires toolsChangedCb.
//  4. select on {DoChan result, time.After(budget), ctx.Done}.
//     - DoChan result → propagate (status, err).
//     - Timeout/ctx.Done → return (StatusIdle, ErrLazyWarming); spawn continues.
func (m *Manager) EnsureStarted(ctx context.Context, name string) (models.ServerStatus, error) {
	// Fast path: already serving.
	entry, ok := m.Entry(name)
	if !ok {
		return models.StatusError, m.errNotFound(name)
	}
	if entry.Status == models.StatusRunning || entry.Status == models.StatusDegraded {
		return entry.Status, nil
	}
	// Fix 6: do not re-enter spawn for a backend that already failed.
	if entry.Status == models.StatusError {
		return models.StatusError, errors.New("server " + name + " is in error state")
	}

	// Compute caller budget.
	budget := lazySpawnServiceCap
	if dl, hasDL := ctx.Deadline(); hasDL {
		computed := time.Duration(float64(time.Until(dl)) * 0.7)
		if computed < lazySpawnBudgetFloor {
			computed = lazySpawnBudgetFloor
		}
		budget = computed
	}

	// Retrieve the backend config for manifest refresh after spawn.
	m.mu.RLock()
	e, eOK := m.entries[name]
	var cfg models.ServerConfig
	if eOK {
		cfg = e.Config
	}
	m.mu.RUnlock()
	if !eOK {
		return models.StatusError, m.errNotFound(name)
	}

	// DoChan returns immediately with a channel; the goroutine starts once
	// per unique key regardless of how many concurrent callers arrive.
	ch := m.sfGroup.DoChan(name, func() (interface{}, error) {
		// Mark pending so filteredTools keeps tools visible during StatusStarting.
		m.lazyPendingMu.Lock()
		m.lazyPending[name] = struct{}{}
		m.lazyPendingMu.Unlock()
		defer func() {
			m.lazyPendingMu.Lock()
			delete(m.lazyPending, name)
			m.lazyPendingMu.Unlock()
		}()

		// Detached context: survives the caller's deadline.
		bgCtx, cancel := context.WithTimeout(context.Background(), lazySpawnMaxStartup)
		defer cancel()

		// testSpawnHook allows tests to count distinct Start invocations
		// without touching the production Start body.
		if m.testSpawnHook != nil {
			m.testSpawnHook(name)
		}
		if err := m.Start(bgCtx, name); err != nil {
			// Spawn failed: mark error and evict from manifest so we stop
			// advertising a tool that will not start (Guard 2).
			m.SetStatus(name, models.StatusError, err.Error())
			if m.manifest != nil {
				m.manifest.Remove(name)
				_ = m.manifest.Persist()
			}
			// Notify gateway to drop the tool from the tool list.
			if cb := m.toolsChangedCb; cb != nil {
				cb(name)
			}
			// TASK T1: Guard-2 degrade — spawn failed, status Error, manifest evicted.
			m.lazyMetrics.degradeEvicted.Add(1)
			return models.StatusError, err
		}

		// Spawn succeeded: add to supervisor tree (idempotent).
		m.AddBackendToSupervisor(name, m.logger)

		// Refresh manifest from the now-live tool list.
		if m.manifest != nil {
			session, sessionOK := m.Session(name)
			if sessionOK && session != nil {
				fetchCtx, fCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer fCancel()
				if tools, err := m.fetchTools(fetchCtx, name, session); err == nil && tools != nil {
					m.manifest.Put(name, BackendConfigSig(cfg), tools)
					_ = m.manifest.Persist()
				}
			}
		}

		// Fix 1: notify the gateway so it rebuilds its advertised tool list
		// from the now-live backend. Must fire even when manifest is nil —
		// the backend is Running and its tools must be reflected immediately.
		if cb := m.toolsChangedCb; cb != nil {
			cb(name)
		}

		// TASK T1: successful on-demand spawn (counted once per singleflight,
		// not per coalesced caller).
		m.lazyMetrics.spawnOnInvoke.Add(1)
		return models.StatusRunning, nil
	})

	// Fix 4: use an explicit timer so it is stopped on early success,
	// preventing a goroutine leak on the time.After heap when the spawn
	// completes within the caller's budget.
	t := time.NewTimer(budget)
	defer t.Stop()

	select {
	case res := <-ch:
		// Fix 5: reject unexpected result types instead of silently zero-ing.
		status, ok := res.Val.(models.ServerStatus)
		if !ok {
			return models.StatusError, fmt.Errorf("internal: unexpected lazy-spawn result type %T", res.Val)
		}
		return status, res.Err
	case <-t.C:
		// TASK T1: caller budget expired; warming returned. Spawn continues.
		m.lazyMetrics.warmingReturned.Add(1)
		return models.StatusIdle, ErrLazyWarming
	case <-ctx.Done():
		// TASK T1: caller context cancelled before spawn finished; warming returned.
		m.lazyMetrics.warmingReturned.Add(1)
		return models.StatusIdle, ErrLazyWarming
	}
}

// errNotFound returns a consistent "server not found" error without fmt import
// dependency (fmt is already in manager.go which this file's methods extend).
func (m *Manager) errNotFound(name string) error {
	return errors.New("server " + name + " not found")
}
