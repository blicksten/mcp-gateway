# [BUG] v2.1.145 VSCode extension still exhibits memory accumulation pattern from #22968 / #22716

## Summary

The memory-accumulation pattern documented in [#22716 (v2.1.27)](https://github.com/anthropics/claude-code/issues/22716) and [#22968 (v2.1.29/30)](https://github.com/anthropics/claude-code/issues/22968) is still present in **v2.1.145 on Windows 11 Enterprise (x64)**. Both prior issues were closed as "not planned" but the underlying defect persists across all subsequent releases I have tested.

This issue contains empirical forensic data from a 2026-05-24 reproduction (process snapshots before/after, latency probes, CPU samples, per-process diffs) that may help if Anthropic chooses to revisit.

## Environment

- **Claude Code extension version:** `anthropic.claude-code-2.1.145-win32-x64` (installed 2026-05-20)
- **OS:** Windows 11 Enterprise 10.0.26200
- **VSCode:** built-in (path: `C:\Users\<user>\AppData\Local\Programs\Microsoft VS Code\Code.exe`)
- **Topology:** user runs **5 chat tabs per window × multiple windows** (concurrent conversations). Each chat tab spawns its own `claude.exe` child process from the extension host. Observed 6 simultaneous `claude.exe` processes.
- **Hardware:** 64 GB RAM, 12-core CPU (paging not a constraint).

## Reproducer

1. Open a VSCode window with the Claude Code extension active.
2. Open ≥3 chat tabs (Command Palette → `Claude Code: Open in New Tab`).
3. Use the chats actively (file reads, edits, bash commands, MCP tool calls) for **2-4 hours**.
4. Observe gradual degradation across **all** chat tabs simultaneously:
   - Long pause before first response token (>5 s for typical messages).
   - Slow tool call execution.
   - Chunky/jerky streaming output.
5. **Try `/clear` in the slow window — does NOT restore performance** (rules out conversation context size).
6. **Try `Developer: Reload Window` — does NOT restore performance** (rules out renderer / extension host webview state).
7. **Close ALL VSCode windows → reopen → fast again.** Closing all windows is the only reliable recovery (kills all `claude.exe` children).

## Critical observation: degradation is in `claude.exe`, NOT in renderer or extension host

| Action | Survives? | Restores performance? |
|---|---|---|
| `/clear` (resets conversation context) | claude.exe persists | ❌ |
| `Developer: Reload Window` (kills renderer) | claude.exe persists (child of extension host) | ❌ |
| Close one VSCode window (kills extension host) | other windows' claude.exe persist | only this one's chat |
| Close ALL VSCode windows | nothing persists | ✓ everything fast again |

This rules out:
- Conversation context bloat (would be fixed by `/clear`)
- Renderer DOM growth (would be fixed by `Reload Window`)
- Extension host event-listener accumulation (would be fixed by `Restart Extension Host`)

This rules in: **state accumulation inside the `claude.exe` (Node.js) process itself.**

## Process baseline (2026-05-24, captured during reproduced slowdown)

### `claude.exe` instances (6 simultaneous, one per chat tab/window)

| PID | Mem (WS) | Priv Mem | Handles | Threads | Age at slow-state | Notes |
|---|---|---|---|---|---|---|
| 19800 | 406.7 MB | 827.3 MB | 350 | 53 | ~3h | actively used tab |
| 24492 | 412.5 MB | 752.5 MB | 334 | 38 | ~3h | actively used tab |
| 13316 | 399.3 MB | 753.3 MB | 342 | 37 | ~3h | actively used tab |
| 32460 | 386.9 MB | 762.6 MB | 306 | 35 | ~3h | actively used tab |
| 30120 | 373.3 MB | 677.2 MB | 306 | 35 | ~3h | actively used tab |
| 26184 | 354.5 MB | 602.4 MB | 295 | 36 | ~3h | actively used tab |

**Note:** Memory growth per `claude.exe` is moderate (+15-30 MB / 45 min observed), NOT exponential as in #22716. The aggregate slowdown comes from compounding effect across all 6 processes plus V8 heap fragmentation accumulated from the high subprocess-spawn rate (each tool call fires 24 hooks in this user's configuration).

### Concurrent VSCode renderer process

PID 23628 (`Code.exe --type=renderer`) grew **660 MB → 1521 MB in 25 minutes during active session** (~30 MB/min). This is the renderer process for the active VSCode window. Renderer growth is real but does NOT explain "all windows slow" — other renderers (PIDs 21292, 30536) were stable.

The renderer growth is a **separate but related** symptom: it indicates DOM/JS heap accumulation in the conversation panel webview that does NOT release on `/clear` (since `/clear` doesn't touch rendered transcript history).

## What documented workaround `claude --resume` does NOT solve for VSCode extension users

[#22968](https://github.com/anthropics/claude-code/issues/22968) documents `claude --resume` (CLI) as the workaround: kill+respawn the Node process while preserving conversation context.

**This workaround is unavailable in the VSCode extension:**

I inspected the extension's `package.json` (v2.1.145) command palette and found NO equivalent command:
- `claude-vscode.newConversation` — creates NEW conversation (loses context) ❌
- `claude-vscode.reopenClosedSession` — restores closed session (untested whether memory is freed)
- `claude-vscode.editor.open` / `window.open` / `sidebar.open` — open new instance
- **No "Restart Conversation" / "Reset Session" / equivalent of `claude --resume`**

[#34320](https://github.com/anthropics/claude-code/issues/34320) ("Add /restart command to restart session from within chat") tracks this gap — feature request still open.

## Why I am refiling despite "not planned"

I am not trying to dispute Anthropic's "not planned" decision. I am filing because:

1. The bug is **still present in v2.1.145** (the issues were closed against 2.1.20 / 2.1.27 / 2.1.29-30; assumption of "fixed in later version" is incorrect).
2. **For VSCode extension users, no workaround exists** (the `claude --resume` CLI workaround is unavailable).
3. Forensic data here is more detailed than in prior reports (process baselines, ruling out renderer + extension host + conversation context via empirical tests, not just memory totals).
4. The recommendation in prior issues was effectively "use the CLI". Many enterprise users (myself included) cannot use the CLI for compliance reasons and depend on the VSCode extension.

## What I would find useful

In rough priority order:

1. **Implement the `claude --restart-current-conversation` command palette entry** matching [#34320](https://github.com/anthropics/claude-code/issues/34320). Even if the underlying leak isn't fixed, a one-click restart-while-preserving-context would make this entirely tractable.
2. **Investigate `claude.exe` heap behaviour under high subprocess-spawn rate.** My configuration fires 24 hooks per tool call (PreToolUse + PostToolUse). At 2.5 tool calls/minute, that's ~60 child processes/minute. Over 4 hours = 14,400 spawns per `claude.exe`. Plausible accumulation sources: pipe-handle leaks on Windows, V8 heap fragmentation from short-lived hook output buffers.
3. **Document the workaround that DOES work in VSCode** (closing all windows). Current docs don't mention this as a known issue.

## I am happy to provide more data

If a maintainer wants additional forensic input (full PowerShell snapshot CSVs, hook timing data, latency probe sequences), I have them and can share. Please comment here and I will attach.

---

**Related issues:**
- #22716 — v2.1.27 critical memory leak (closed "not planned")
- #22968 — v2.1.29/30 long-session memory + CPU accumulation (closed "not planned")
- #22225 — VS Code extension excessive memory immediately after launch
- #21182 — v2.1.20 11.6 GB per conversation window
- #12814 — heap out of memory after extended use
- #34320 — feature request: `/restart` command (NOT IMPLEMENTED)
- #25101 — extension restarts VSCode when loading past conversations
