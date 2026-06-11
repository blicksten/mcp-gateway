# ADR-0008 — Daemon liveness supervision & honest health indication

**Status:** Accepted (2026-06-12)
**Supersedes:** none
**Related:** ADR-0007 (two-tier auth), `claude-team-control` MCP Emergency Runbook §Symptom E

## Context

The gateway daemon is a **singleton** bound to `127.0.0.1:8765`, shared by every VS Code
window and every child `claude.exe`. The dashboard extension's `DaemonManager` was supposed
to keep it alive (`mcpGateway.autoStart`, `mcpGateway.autoRestartOnCrash`, both default `true`)
and the status bar was supposed to show its health.

A production incident (verified from logs 2026-06-06 → 2026-06-11) exposed three defects that
share one false assumption — *that some live window always owns and observes the singleton*:

1. **autoStart assumed-alive on probe timeout.** `_doStart`'s pre-spawn `getHealth()` treated any
   non-`connection` `GatewayError` (notably `kind:'timeout'`) as "alive, skip spawn". On Windows a
   connect to a dead/filtered port can *time out* rather than fast-RST, so a genuinely dead daemon
   was classified alive and never started. Logged proof: `getHealth re-probe failed (kind=timeout)
   — assuming gateway is alive, skipping spawn` while the daemon was a corpse.
2. **autoRestart watched only the own-spawned child.** The respawn path fired from
   `this.child.on('exit')` — only set in the window that won the spawn. When that window closed, or
   the daemon was started externally (`mcp-ctl`, scheduled task), no surviving instance received an
   exit event → never respawned. (Worse: the `exit(0)` graceful-exit guard meant even the owner
   would not respawn a clean shutdown.)
3. **Status bar had no heartbeat.** It repainted only on `ServerDataCache.onDidRefresh`; if polling
   stalled it froze on the last green paint — "no signal" rendered as "healthy".

Net effect: the daemon died (clean exit), stayed dead 5 days across several reboots, the indicator
showed alive, and every new `claude.exe` hung on MCP init → `Subprocess initialization did not
complete within 60000ms`.

## Decision

### 1. `/health` is the sole authority for "alive"; TCP is only a negative pre-filter

A successful HTTP `/health` response is the *only* proof of liveness. A raw TCP connect is used
solely as:
- a **fast negative pre-filter** — TCP refused ⇒ port closed ⇒ definitely dead ⇒ spawn now; and
- a **race-loss suppressor** in the respawn path — TCP open ⇒ another window already bound the
  port ⇒ do not spawn.

TCP-accept is **never** treated as alive: a hung daemon ("dead-but-bound") accepts TCP but fails
`/health`. The skip-spawn decision requires a *successful* `/health`, never a bare TCP accept.
(`daemon.ts::_resolveTimeoutKind`.)

### 2. Respawn is driven by the health-poll loop, not the child exit event

`DaemonManager.considerRespawnFromPoll(reachable)` is called from the extension's
`cache.onDidRefresh` handler every poll. After `respawnAfterFailedPolls` (default 3) consecutive
failures it respawns — independent of which window (if any) spawned the daemon. This is the only
mechanism that recovers a clean-exit death of an unowned singleton.

### 3. Multi-window respawn-storm coordination WITHOUT a lockfile

The OS port-bind is the host-wide atomic mutex (exactly one daemon survives; losers exit
`EADDRINUSE`). On top of it:
- **Jittered re-probe** — the pre-spawn re-probe delay is `raceDetectDelayMs * (0.5 + random)` so
  late detectors re-probe *after* the winner bound the port, see it healthy, and skip spawning.
- **EADDRINUSE reclassification** — a child that exits `code=1, signal=null, aliveMs<addrinuseGraceMs`
  (prod 1500ms) and whose port now answers `/health` is a benign *lost race*, not a crash, so reload
  storms do not poison the 5-crashes-in-10-min crash-loop guard. (`handleExit` → confirm via
  `/health` → `recordCrashAndSchedule` only if genuinely down.)
- **Cold-start grace** — `considerRespawnFromPoll` does not respawn within
  `respawnColdStartGraceMs` (default 10 000) of our own `spawnedAt`, preventing an infinite restart
  loop while a freshly-spawned daemon's `/health` is not yet up.

A **`%TEMP%` lockfile is explicitly rejected** (TTL/staleness-sweep/crash-release complexity,
cross-process file-lock races) — the OS port bind is a strictly better host-wide mutex, and the
team already removed filesystem sentinels once.

### 4. Status bar heartbeat

`McpStatusBar` runs an independent timer; if no *successful* refresh arrives within
`pollInterval * statusHeartbeatMultiplier` (default 3) it renders a distinct **"unknown / no
signal"** state, never stale green. An explicit `lastRefreshFailed` event (offline) takes
precedence over heartbeat-unknown.

### 5. Out-of-band backstop (authority chain)

Because all of the above live inside VS Code, a **Windows scheduled task `mcp-gateway-watchdog`**
hard-checks HTTP 200 on `:8765` every 2 min and runs `mcp-ctl daemon start` if down — independent
of whether any VS Code window is open. This is the only layer that covers the **dead-but-bound**
case (the extension cannot bind an occupied port nor SIGKILL a daemon it does not own).

**Liveness authority chain:** `scheduled task → /health → TCP pre-filter`.

## Consequences

- The extension recovers **port-closed** deaths; the scheduled task recovers **dead-but-bound**
  hangs. This boundary is intentional and documented — do not have the extension force-kill a
  foreign hung daemon (violates the ownership boundary in `daemon.ts::stop`/`dispose`).
- New settings: `mcpGateway.tcpProbeTimeoutMs` (400), `mcpGateway.respawnAfterFailedPolls` (3),
  `mcpGateway.respawnColdStartGraceMs` (10000), `mcpGateway.statusHeartbeatMultiplier` (3).
- Validated by architect review + PAL (thinkdeep ×2, codereview PASS) + 1203 unit tests.
