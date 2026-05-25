# Scenario catalog — mcp-gateway audit 2026-05-24

This catalog lists every named operator scenario used to prove runtime exercise + correctness assertion (axes 2 + 4 of the audit). Each `scenario_id` is referenced from `claims.yaml` and from `outputs/coverage.json`.

Scenarios are either **auto** (runnable by `scripts/04-scenarios.ps1`) or **manual** (operator runs by hand and records evidence in `operator-manual-checklist.md`).

## Scope (project isolation)

This file contains scenarios for **mcp-gateway code only**. Cross-project scenarios (claude-team-control hook tests like `anti-passive-stop.py`, `mcp-rehydrate.sh`) belong in those projects' audit packets.

## Required fields per scenario

- `id` — stable identifier (referenced from `claims.yaml`)
- `type` — `auto` or `manual`
- `name` — human-readable
- `steps` — reproducible operator instructions
- `assertions` — explicit checks the scenario MUST verify (closes axis 4 — correctness). Without these, the scenario can mark a function as "executed" but cannot mark it PROVEN.
- `evidence_path` — relative path under `outputs/` where the scenario writes its evidence file (auto scenarios) or where the operator attaches captured artifact (manual scenarios). MANDATORY.
- `expected_coverage` — files/functions this scenario is expected to touch
- `last_verified` — date

A scenario without `assertions` is flagged ASSERTION_DEFICIENT in `gap-report.md`. A scenario without `evidence_path` (or with missing file at that path) fails 07-verdict-check.

---

## scn-daemon-lifecycle-cold-start

- **type:** auto
- **steps:**
  1. Stop any running daemon: `mcp-ctl daemon-stop`
  2. Start fresh: `mcp-ctl daemon-start --log-level=debug`
  3. Wait for `GET /api/v1/health` 200
  4. Register a test MCP backend
  5. Invoke a test tool
  6. Stop daemon
- **assertions:**
  - `/api/v1/health` returns 200 within 10s of start
  - `gateway_list_servers` returns ≥1 backend after register
  - Tool invoke returns documented response shape
  - Stop completes with exit 0
- **evidence_path:** `outputs/scenarios/scn-daemon-lifecycle-cold-start.jsonl`
- **expected_coverage:** daemon main, supervisor.Add, registry.Register, auth bearer middleware, stdio proxy
- **last_verified:** null

## scn-daemon-restart-state-replay

- **type:** auto
- **steps:**
  1. Start daemon, register backend, create stateful session
  2. Capture session state snapshot via REST → save to `outputs/scenarios/scn-daemon-restart-state-replay.snap-before.json`
  3. Kill daemon via `mcp-ctl daemon-restart` (admin-token path)
  4. After restart, re-fetch state via REST → save to `outputs/scenarios/scn-daemon-restart-state-replay.snap-after.json`
- **assertions:**
  - **equal(snap-before, snap-after) field-by-field — this is the load-bearing T0.7.1 invariant**
  - Daemon survives restart (same persistence file, different PID)
  - Resumable streaming session ID is identical before and after
- **evidence_path:** `outputs/scenarios/scn-daemon-restart-state-replay.snap-before.json` + `.snap-after.json`
- **proves:** T0.7.1-session-state-disk-persistence
- **expected_coverage:** SessionStateRegistry struct + Save/Load methods (internal/api/resumable_session_spike.go)
- **last_verified:** null

## scn-vscode-close-cascade-no-daemon-death

- **type:** manual
- **steps:**
  1. Record daemon PID + StartTime: `Get-Process mcp-gateway | Select Id, StartTime` → save snapshot file
  2. Open VS Code window A with mcp-gateway extension active
  3. Open VS Code window B with mcp-gateway extension active
  4. Open VS Code window C with mcp-gateway extension active
  5. Close window A; record PID + StartTime
  6. Close window B; record PID + StartTime
  7. Close window C; record PID + StartTime
  8. Active probe: `Invoke-WebRequest -Uri http://127.0.0.1:8765/api/v1/shutdown -Method POST -Headers @{Authorization="Bearer $(Get-Content ~/.mcp-gateway/auth.token)"} -TimeoutSec 5`
- **assertions:**
  - PID and StartTime IDENTICAL across all 4 snapshots
  - Active probe returns HTTP 401 (regular bearer rejected at admin gate)
- **evidence_path:** `outputs/scenarios/scn-vscode-close-cascade.pid-snapshots.txt` + screenshot of 4 process snapshots
- **proves:** bug-a-daemon-survives-vscode-close-cascade
- **expected_coverage:** internal/auth/admin.go AdminMiddleware
- **last_verified:** null

## scn-shutdown-auth-rejection

- **type:** auto
- **steps:**
  1. Start daemon
  2. Issue POST /api/v1/shutdown with regular user-config bearer
  3. Read response and audit log
  4. Issue POST /api/v1/shutdown with admin token from ~/.mcp-gateway/admin.token
  5. Read response and audit log
- **assertions:**
  - Step 2 response: HTTP 401, body contains `scope=admin` hint
  - Step 2 audit log: `auth: rejected request path=/api/v1/shutdown scope=admin reason=mismatch`
  - Step 4 response: HTTP 200 + daemon process exits within 5s
- **evidence_path:** `outputs/scenarios/scn-shutdown-auth-rejection.{request,response,audit-log}.txt`
- **proves:** MCPR.3-admin-token-gates-shutdown
- **expected_coverage:** internal/auth/admin.go:39 AdminMiddleware
- **last_verified:** null

## scn-claude-code-endpoints-roundtrip

- **type:** auto
- **steps:**
  1. Start daemon
  2. POST each of the 10 Claude Code REST endpoints with documented payload (heartbeat, patch-status, pending-actions, pending-action-ack, probe-trigger, plugin-sync, compat-matrix, probe-result, register-pid, unfreeze)
  3. Verify response shape matches frozen contract field-by-field against `docs/api/claude-code-endpoints.md`
- **assertions:**
  - For each endpoint: response HTTP status matches docs
  - For each endpoint: response JSON field set matches docs (no missing fields, no extra fields)
  - For each endpoint: field types match docs
- **evidence_path:** `outputs/scenarios/scn-claude-code-endpoints/<endpoint-name>.{req,res}.json` (10 pairs)
- **proves:** phase-16.3-claude-code-handlers-contract
- **expected_coverage:** internal/api/claude_code_handlers.go lines 252, 321, 337, 353, 379, 415, 489, 511, 561, 707
- **last_verified:** null

## scn-scalability-3x16

- **type:** manual
- **steps:**
  1. Open 3 VS Code windows
  2. In each, open 16 different MCP tabs (16 backends)
  3. Record connection-establishment timestamps per session
  4. Within 30s window: issue invoke through 10 random sessions concurrently
  5. Record response routing per request
  6. Close all windows; record final daemon PID
- **assertions:**
  - All 48 sessions show `health: running` in `gateway_list_servers` within 30s
  - 10 concurrent invokes complete without cross-session response delivery (response routed to invoker's session_id)
  - Latency P50 ≤ 2000ms, P99 ≤ 10000ms (numeric threshold; record actual)
  - Final daemon PID == initial daemon PID (cascade survivability)
- **evidence_path:** `outputs/scenarios/scn-scalability-3x16.timings.csv` + screenshot of all 48 sessions connected
- **proves:** scalability-3x16-sessions
- **expected_coverage:** session manager, supervisor, multiplexer (internal/proxy/*)
- **last_verified:** null

## scn-server-rename-credential-move

- **type:** auto (via VS Code extension Jest harness)
- **steps:**
  1. Add a test server with credentials
  2. Trigger rename via command palette
  3. Inspect SecretStorage state
  4. Simulate partial-failure mid-rename (MockSecretStorage failAfterN=2)
  5. Inspect SecretStorage state after recovery
- **assertions:**
  - Step 3: SecretStorage has credentials under new name only; old name keys absent
  - Step 3: insertion order matches new-name index-first ordering
  - Step 5: post-recovery state is consistent — either all-old OR all-new, never partial mixed
- **evidence_path:** `outputs/scenarios/scn-server-rename-credential-move.jest-report.json`
- **proves:** server-rename-credential-store-move
- **expected_coverage:** vscode/mcp-gateway-dashboard/src/credential-store.ts:148 renameServerCredentials
- **last_verified:** null

## scn-sap-keepass-readonly

- **type:** auto
- **steps:**
  1. Set up KDBX test fixture with both AES and ChaCha20 entries
  2. Record file mtime + sha256 hash BEFORE read
  3. Read entries via sapcreds.keepass reader
  4. Record file mtime + sha256 hash AFTER read
- **assertions:**
  - mtime AFTER == mtime BEFORE (no file write)
  - sha256 AFTER == sha256 BEFORE (no content change)
  - Read returned expected entries for both ciphers
- **evidence_path:** `outputs/scenarios/scn-sap-keepass-readonly.hashes.txt`
- **proves:** sap-keepass-readonly
- **expected_coverage:** internal/sapcreds/keepass.go read path
- **last_verified:** null

## scn-gateway-meta-tools-roundtrip

- **type:** auto
- **steps:**
  1. Start daemon with 3 backends registered
  2. Call `gateway.list_servers` via MCP
  3. Call `gateway.list_tools` via MCP
  4. Call `gateway.invoke(backend=X, tool=Y, args=...)` for a tool from each of the 3 backends
- **assertions:**
  - list_servers returns exactly 3 entries with name, status, transport, tool_count fields
  - list_tools returns union of all backend tools, no duplicates
  - invoke result for each backend matches direct backend call (compare against control invocation through native MCP)
- **evidence_path:** `outputs/scenarios/scn-gateway-meta-tools.{servers,tools,invokes}.json`
- **proves:** gateway-invoke-meta-tools
- **expected_coverage:** internal/proxy/gateway.go:172 registerGatewayBuiltins + 245/296/339 handlers
- **last_verified:** null

## scn-mcp-ctl-doctor

- **type:** auto
- **steps:**
  1. Run `mcp-ctl doctor` against healthy daemon
  2. Force one check to fail (e.g. corrupt config path); run doctor again
- **assertions:**
  - Healthy run: exit 0, all 5 checks reported PASS
  - Failed run: exit non-zero, the corrupted check reports FAIL with diagnostic message
- **evidence_path:** `outputs/scenarios/scn-mcp-ctl-doctor.{healthy,failed}.txt`
- **expected_coverage:** mcp-ctl doctor subcommand
- **last_verified:** null

## scn-tls-half-configured-refusal

- **type:** auto
- **steps:**
  1. Start daemon with TLS cert but no key (half-configured)
  2. Capture exit code + stderr
  3. Start daemon with both cert and key
  4. Probe TLS handshake with `openssl s_client`
- **assertions:**
  - Step 2: daemon refuses to start (non-zero exit), error mentions both cert and key
  - Step 4: TLS handshake succeeds with expected cert
- **evidence_path:** `outputs/scenarios/scn-tls-half-configured.{stderr,handshake}.txt`
- **expected_coverage:** TLS init path, half-configured guard
- **last_verified:** null

---

## NEW SCENARIOS (added per PAL review 2026-05-24 — closing missing crash/restart/orphan coverage)

## scn-backend-crash-mid-session

- **type:** auto
- **steps:**
  1. Start daemon with 2 backends (target + control)
  2. Open a stateful session against the target backend
  3. Send an invoke; capture response
  4. Force-kill the target backend process (`Stop-Process -Force` or equivalent)
  5. Send another invoke from the same session
  6. Wait for supervisor respawn (configured policy)
  7. Send a third invoke with a NEW session_id
- **assertions:**
  - Step 3: invoke succeeds normally
  - Step 5: invoke returns structured error containing `session not found` or `backend unavailable` (NOT hang, NOT silent corruption)
  - Step 6: supervisor restarts the backend (visible in `gateway_list_servers` health=running)
  - Step 7: new-session invoke succeeds against the respawned backend
  - Control backend (untouched) remains healthy throughout
- **evidence_path:** `outputs/scenarios/scn-backend-crash-mid-session.timeline.jsonl`
- **proves:** gateway-orphan-mcp-session-recovery
- **expected_coverage:** internal/proxy/gateway.go session lookup error path + internal/lifecycle supervisor respawn
- **last_verified:** null

## scn-daemon-restart-during-invoke

- **type:** auto
- **steps:**
  1. Start daemon, register backend, open stateful session
  2. Start a long-running invoke (e.g. a sleep-tool that returns after 8s)
  3. At t+2s, send SIGTERM to daemon (graceful restart via mcp-ctl)
  4. After daemon comes back up, attempt to resume the invoke
- **assertions:**
  - Invoke is cancelled cleanly OR is resumable (depending on scenario flavor — assert one specific behavior per claim)
  - Post-restart, SessionStateRegistry shows the session as either `resumable` or `terminated`, never `unknown`
  - No orphan child processes remain (`Get-Process | Where-Object Path -like "*mcp-gateway*"` matches expected set)
- **evidence_path:** `outputs/scenarios/scn-daemon-restart-during-invoke.ps-tree.txt`
- **expected_coverage:** lifecycle restart path + SessionStateRegistry state machine
- **last_verified:** null

## scn-orphan-cleanup-during-active-session

- **type:** auto
- **steps:**
  1. Start daemon, register backend, open session
  2. Artificially create a stale session in SessionStateRegistry (write directly to sessions.json with old timestamp)
  3. Restart daemon (triggers cleanup pass)
  4. After restart, list sessions
  5. Verify the active session still works
- **assertions:**
  - Stale session is purged (not present after restart)
  - Active session survives the cleanup pass (verifiable via state-fingerprint match before/after)
  - No errors logged for active session during cleanup
- **evidence_path:** `outputs/scenarios/scn-orphan-cleanup.{before,after}-sessions.json`
- **expected_coverage:** SessionStateRegistry cleanup pass
- **last_verified:** null

## scn-patchstate-concurrent-enforce

- **type:** auto
- **steps:**
  1. Spawn N=10 concurrent goroutines, each calling EnforceWindowAndRecordPid with distinct (windowID, pid) pairs
  2. After all complete, read patchstate
- **assertions:**
  - Final state contains exactly N (windowID, pid) pairs (no lost writes)
  - For every (windowID, pid) pair: either BOTH window record AND pid record exist, or NEITHER (no partial state observed)
  - No goroutine returns a "PID already enforced" error for a unique PID (no false positives from race)
- **evidence_path:** `outputs/scenarios/scn-patchstate-concurrent.final-state.json`
- **proves:** patchstate-toctou-atomic-compound
- **expected_coverage:** internal/patchstate/state.go EnforceWindowAndRecordPid
- **last_verified:** null

---

## Catalog metrics (2026-05-24)

Total scenarios: 15
- Auto: 13
- Manual: 2 (scn-vscode-close-cascade, scn-scalability-3x16)
- With assertions: 15 / 15 (100%)
- With evidence_path: 15 / 15 (100%)
- Linked to claims.yaml: 9 / 15 (60%) — remaining 6 are infrastructure scenarios (cold-start, doctor, TLS, restart-during-invoke, orphan-cleanup, mcp-ctl-doctor) that prove implicit invariants without an explicit RTM claim

Goal next audit (2026-08-24):
- Automate `scn-vscode-close-cascade-no-daemon-death` (Playwright-controllable VSCode harness candidate)
- Achieve 100% claim → scenario coverage (currently 10 claims, 10 should have scenario_id link)
- Add at least 1 more crash/restart scenario for VSCode extension paths
