# Experiment: Validate Claude Code stdio MCP behavior

Validates three product-gap hypotheses for `mcp-gateway`:
- **A. Session resumption** — Claude Code's response when an stdio MCP server crashes
- **B. Live tool updates** — does it refresh tools on `notifications/tools/list_changed`
- **C. Cold-start tool latency** — does a slow `tools/list` block the UI

## Hypotheses (predictions BEFORE running)

| # | Hypothesis | Confidence |
|---|---|---|
| H1 | Claude Code respawns stdio MCP server after `exit(1)` and re-initializes cleanly (canonical case) | **~90%** — basic MCP client behavior |
| H2 | Claude Code processes `notifications/tools/list_changed` over stdio and refreshes the tool list **without window reload** | **~60%** — schema is declared in bundle; HTTP-side bugs may or may not apply to stdio |
| H3 | Claude Code shows the user some indicator when a backend takes 30s on `tools/list` (loading spinner / "loading tools" message) | **~70%** — UX necessity |
| H4 | Claude Code has a hard timeout on initial `tools/list` (likely 60s based on existing main.go comments). After timeout, that backend's tools are absent until next reconnect | **~50%** — uncertain |

Outcome of any of these tells us where to invest in shim design:
- If H1 holds (very likely): shim built on stdio respawn pattern is solid
- If H2 holds: shim can drive live tool updates via stdio notifications — best case
- If H2 fails: shim needs fallback (force-respawn pattern to refresh tools)
- If H3/H4 show specific timings: gives us SLA targets for shim cold-start

## Setup

The mock binary is already built: `mock-mcp-stdio.exe`. Three scenarios are wired
via env vars. The `claude-mcp-experiment.json` config (below) instructs Claude
Code to spawn three instances simultaneously, each with different behaviors.

### Add to `.mcp.json`

Add this block under `mcpServers`:

```json
{
  "mcpServers": {
    "expA_crash": {
      "command": "c:\\Users\\stanislav.naumov\\claude-workspace\\mcp-gateway\\experiments\\mock-mcp-stdio\\mock-mcp-stdio.exe",
      "env": {
        "MOCK_SERVER_NAME": "expA",
        "MOCK_SCENARIO_NAME": "crash",
        "MOCK_INITIAL_TOOL_COUNT": "2",
        "MOCK_CRASH_AFTER_S": "20",
        "MOCK_LOG_FILE": "c:\\Users\\stanislav.naumov\\claude-workspace\\mcp-gateway\\experiments\\mock-mcp-stdio\\expA.log"
      }
    },
    "expB_notify": {
      "command": "c:\\Users\\stanislav.naumov\\claude-workspace\\mcp-gateway\\experiments\\mock-mcp-stdio\\mock-mcp-stdio.exe",
      "env": {
        "MOCK_SERVER_NAME": "expB",
        "MOCK_SCENARIO_NAME": "notify",
        "MOCK_INITIAL_TOOL_COUNT": "1",
        "MOCK_NOTIFY_AFTER_S": "15",
        "MOCK_EXTRA_TOOL_COUNT": "3",
        "MOCK_LOG_FILE": "c:\\Users\\stanislav.naumov\\claude-workspace\\mcp-gateway\\experiments\\mock-mcp-stdio\\expB.log"
      }
    },
    "expC_slow": {
      "command": "c:\\Users\\stanislav.naumov\\claude-workspace\\mcp-gateway\\experiments\\mock-mcp-stdio\\mock-mcp-stdio.exe",
      "env": {
        "MOCK_SERVER_NAME": "expC",
        "MOCK_SCENARIO_NAME": "slow",
        "MOCK_INITIAL_TOOL_COUNT": "2",
        "MOCK_TOOLS_LIST_DELAY_S": "30",
        "MOCK_LOG_FILE": "c:\\Users\\stanislav.naumov\\claude-workspace\\mcp-gateway\\experiments\\mock-mcp-stdio\\expC.log"
      }
    }
  }
}
```

Then reload the Claude Code MCP layer in VS Code (`Developer: Reload Window` if needed).

## Observations to record

For each scenario, start a stopwatch when Claude Code window opens. Note:

### Scenario A — `expA_crash` (server crashes at t=20s)
1. When does `expA_initial_1` / `expA_initial_2` appear in tool palette?
2. At ~t=20s, does Claude Code log a server crash?
3. Does Claude Code respawn the server? Within how many seconds?
4. After respawn, are tools available again? Without `/clear`?
5. **Output:** read `expA.log` — look for multiple `=== mock-mcp-stdio start ===` lines (one per respawn) and time between them.

### Scenario B — `expB_notify` (tools list_changed at t=15s)
1. At t=0, are `expB_initial_1` tool visible? (only ONE initial tool)
2. At ~t=15s, the mock sends `notifications/tools/list_changed`. Does Claude Code:
   - (a) react and call `tools/list` again? → check `expB.log` for `RECV tools/list` near t=15s
   - (b) ignore the notification? → log will show only ONE `RECV tools/list` (the initial)
3. If (a), do the three extra tools (`expB_extra_1`/`2`/`3`) appear in tool palette without window reload?

### Scenario C — `expC_slow` (tools/list delayed by 30s)
1. Does Claude Code show any "loading tools" indicator?
2. After 30s, are `expC_initial_1` / `expC_initial_2` tools visible?
3. If MORE than 30s: did Claude Code time out and abandon the backend? Check log for `RECV tools/list` (timing) vs no follow-up.
4. Does the slow scenario block OTHER tools (from `expA` / `expB`) from appearing in the meanwhile?

## What to send back

Just the three log files (`expA.log`, `expB.log`, `expC.log`) plus a one-paragraph description of what you saw in the UI (tool palette appearance, indicators, errors). The logs are tiny.

## Cleanup

After the experiment, remove the three `expA_*` / `expB_*` / `expC_*` entries from `.mcp.json` and reload.
