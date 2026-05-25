# Operator manual checklist — 2026-05-24

`scripts/04-scenarios.ps1` runs auto scenarios automatically, then pauses here. Operator works through each manual scenario below, ticks the checkbox, **and attaches captured artifacts at the listed evidence_path before signoff**. `scripts/07-verdict-check.ps1` will refuse to mark the audit complete if any required artifact is missing.

Operator-observed results go into `outputs/manual-results.md` (template at bottom of this file).

---

## scn-vscode-close-cascade-no-daemon-death

**Evidence requirement (MANDATORY):** capture `outputs/scenarios/scn-vscode-close-cascade.pid-snapshots.txt` containing all 4 PID+StartTime snapshots, plus a screenshot of Process Explorer / Task Manager at the final state.

- [ ] Initial PID + StartTime recorded BEFORE opening windows → captured in evidence file at line 1
- [ ] 3 VS Code windows opened with mcp-gateway extension active
- [ ] Closed window A; PID + StartTime captured → evidence file line 2
- [ ] Closed window B; PID + StartTime captured → evidence file line 3
- [ ] Closed window C; PID + StartTime captured → evidence file line 4
- [ ] Active probe: POST /api/v1/shutdown with user bearer → HTTP 401 received; full request+response captured
- [ ] Screenshot of final daemon process state attached at `outputs/scenarios/scn-vscode-close-cascade.screenshot.png`
- **Assertions verified:**
  - [ ] PID and StartTime IDENTICAL across all 4 snapshots
  - [ ] HTTP 401 received from active probe (regular bearer rejected at admin gate)
- **Result:** PASS / FAIL
- **Operator notes:**

---

## scn-scalability-3x16

**Evidence requirement (MANDATORY):**
- `outputs/scenarios/scn-scalability-3x16.timings.csv` with columns: session_id, connect_start, connect_end, invoke_count, success_count, error_count, P50_ms, P99_ms
- Screenshot of all 48 sessions connected (`outputs/scenarios/scn-scalability-3x16.screenshot.png`)
- Final daemon PID record (`outputs/scenarios/scn-scalability-3x16.daemon-pid.txt`)

- [ ] 3 VS Code windows opened
- [ ] 16 distinct MCP backends configured in each (48 total)
- [ ] All 48 sessions show `health: running` in `gateway_list_servers` within 30s — CSV captured
- [ ] 10 concurrent invokes completed; per-request routing logged
- [ ] All 3 windows closed; final daemon PID captured
- **Assertions verified:**
  - [ ] All 48 sessions connected within 30s
  - [ ] No cross-session response delivery observed (each response routed to invoker's session_id)
  - [ ] Latency P50 ≤ 2000ms, P99 ≤ 10000ms (record actual values)
  - [ ] Final daemon PID == initial daemon PID (cascade survivability)
- **Result:** PASS / FAIL
- **Operator notes:**
- **Performance numbers (actual):**
  - P50: ___ ms
  - P99: ___ ms

---

## Sign-off

- Operator: ___________
- Date completed: ___________
- All manual scenarios attempted: [ ]
- All evidence_path artifacts present and reviewable: [ ]
- Total PASS: ___ / 2
- Escalation needed: [ ]

---

## Template: `outputs/manual-results.md` (operator fills out)

```markdown
# Manual scenario results — 2026-05-24

## scn-vscode-close-cascade-no-daemon-death
- Result: PASS / FAIL
- Evidence: outputs/scenarios/scn-vscode-close-cascade.pid-snapshots.txt
- Screenshot: outputs/scenarios/scn-vscode-close-cascade.screenshot.png
- Notes: ...

## scn-scalability-3x16
- Result: PASS / FAIL
- Evidence: outputs/scenarios/scn-scalability-3x16.timings.csv
- Screenshot: outputs/scenarios/scn-scalability-3x16.screenshot.png
- Performance: P50=___ms, P99=___ms
- Notes: ...
```
