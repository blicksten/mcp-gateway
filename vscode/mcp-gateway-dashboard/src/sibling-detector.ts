/**
 * Sibling claude.exe detector — spike 2026-05-11 FM 3 proper-fix path D.
 *
 * Enumerates `claude.exe` processes parented under the same VS Code utility
 * renderer as our extension. Used by the staleness heuristic: when the
 * gateway daemon respawns, every sibling `claude.exe` that started BEFORE
 * the new gateway is at risk of MCPR.1 (Claude Code's closed-source MCP
 * HTTP client may not auto-reconnect after the underlying daemon's TCP
 * socket dies).
 *
 * Cross-platform shape:
 *   - Windows: `Get-CimInstance Win32_Process` via PowerShell, filters on
 *     Name='claude.exe', parses CreationDate.
 *   - Linux / macOS: returns empty list (no-op). User's primary box is
 *     Windows per the spike's evidence; non-Windows support is a future
 *     enhancement.
 *
 * No external dependencies — uses node:child_process.execFile.
 */

import { execFile } from 'child_process';
import { promisify } from 'util';

const execFileP = promisify(execFile);

/** One sibling claude.exe process observed at enumeration time. */
export interface SiblingClaudeProcess {
    /** OS process id of the claude.exe leaf process. */
    pid: number;
    /** Parent process id; lets us scope to "same VS Code window". */
    parentPid: number;
    /** UTC timestamp the process started. */
    createdAt: Date;
    /** Full command line for diagnostics — never compared, only displayed. */
    commandLine: string;
}

/**
 * Detector contract — wraps the OS-specific enumeration so unit tests can
 * inject a fake source without touching child_process.
 */
export interface SiblingDetector {
    /**
     * Return all live `claude.exe` processes the current platform can see.
     * Errors are swallowed and surfaced as an empty list — staleness detection
     * is a best-effort UX assist, never a correctness gate.
     */
    enumerate(): Promise<SiblingClaudeProcess[]>;
}

/**
 * Default implementation — Windows uses PowerShell, other platforms return [].
 */
export function createDefaultSiblingDetector(): SiblingDetector {
    if (process.platform === 'win32') {
        return new WindowsSiblingDetector();
    }
    return new EmptySiblingDetector();
}

/** No-op detector for non-Windows platforms. */
export class EmptySiblingDetector implements SiblingDetector {
    async enumerate(): Promise<SiblingClaudeProcess[]> {
        return [];
    }
}

/**
 * Windows detector using `Get-CimInstance Win32_Process`. The same call we
 * used in the live diagnostic snapshot tonight (PID 7156 + 4 claude.exe
 * children under VS Code utility renderer 6688).
 *
 * Output format request (JSON, single line per process):
 *   {"ProcessId":N,"ParentProcessId":N,"CreationDate":"/Date(epoch)/","CommandLine":"..."}
 *
 * PowerShell ConvertTo-Json emits the .NET DateTime as "/Date(<ms>)/" by
 * default — we parse the epoch out so we keep the returned Date object on
 * the call site instead of an opaque string.
 */
export class WindowsSiblingDetector implements SiblingDetector {
    private readonly powershellPath: string;
    private readonly timeoutMs: number;

    constructor(opts?: { powershellPath?: string; timeoutMs?: number }) {
        this.powershellPath = opts?.powershellPath ?? 'powershell.exe';
        this.timeoutMs = opts?.timeoutMs ?? 5_000;
    }

    async enumerate(): Promise<SiblingClaudeProcess[]> {
        // The PS one-liner is deliberately minimal: filter on Name only,
        // then pipe to ConvertTo-Json with -Depth 1 (we only project
        // primitive fields). -NoProfile + -NonInteractive shave start-up
        // cost to ~100 ms.
        const args = [
            '-NoProfile',
            '-NonInteractive',
            '-Command',
            "Get-CimInstance Win32_Process -Filter \"Name='claude.exe'\" " +
                '| Select-Object ProcessId,ParentProcessId,CreationDate,CommandLine ' +
                '| ConvertTo-Json -Compress -Depth 1',
        ];
        let stdout: string;
        try {
            const result = await execFileP(this.powershellPath, args, {
                timeout: this.timeoutMs,
                maxBuffer: 1 << 20, // 1 MB — well above any realistic process-list size
                windowsHide: true,
            });
            stdout = result.stdout.trim();
        } catch {
            // PowerShell missing, permission denied, timeout — best-effort.
            return [];
        }
        if (stdout.length === 0) {
            return [];
        }
        return parsePowershellProcessJson(stdout);
    }
}

/**
 * Parse PowerShell's ConvertTo-Json output for the Get-CimInstance projection.
 *
 * ConvertTo-Json returns:
 *   - a JSON object when exactly one process matched
 *   - a JSON array when 2+ matched
 *   - empty string when zero matched (already handled by caller)
 *
 * CreationDate is emitted as `"/Date(<unix-ms>)/"`. We extract the integer.
 *
 * Exported for tests.
 */
export function parsePowershellProcessJson(stdout: string): SiblingClaudeProcess[] {
    let parsed: unknown;
    try {
        parsed = JSON.parse(stdout);
    } catch {
        return []; // malformed PS output — give up silently
    }
    const rows: unknown[] = Array.isArray(parsed) ? parsed : [parsed];
    const out: SiblingClaudeProcess[] = [];
    for (const r of rows) {
        const sibling = rowToSibling(r);
        if (sibling !== null) {
            out.push(sibling);
        }
    }
    return out;
}

interface PsProcessRow {
    ProcessId?: number;
    ParentProcessId?: number;
    CreationDate?: string;
    CommandLine?: string | null;
}

function rowToSibling(raw: unknown): SiblingClaudeProcess | null {
    if (raw === null || typeof raw !== 'object') {
        return null;
    }
    const row = raw as PsProcessRow;
    const pid = typeof row.ProcessId === 'number' ? row.ProcessId : NaN;
    const parentPid = typeof row.ParentProcessId === 'number' ? row.ParentProcessId : NaN;
    if (!Number.isFinite(pid) || pid <= 0) {
        return null;
    }
    if (!Number.isFinite(parentPid) || parentPid <= 0) {
        return null;
    }
    const createdAt = parseDotNetDate(row.CreationDate);
    if (createdAt === null) {
        return null;
    }
    return {
        pid,
        parentPid,
        createdAt,
        commandLine: typeof row.CommandLine === 'string' ? row.CommandLine : '',
    };
}

/**
 * Parse PowerShell's `/Date(<unix-ms>)/` DateTime serialization.
 * Exported for tests.
 */
export function parseDotNetDate(value: string | null | undefined): Date | null {
    if (typeof value !== 'string') {
        return null;
    }
    // Format is exactly "/Date(<digits>)/" or "/Date(<digits>+timezone)/".
    // We only need the integer prefix; offset (if present) does not change
    // the absolute instant in JavaScript Date.
    const inner = stripDateWrapper(value);
    if (inner === null) {
        return null;
    }
    const msPart = stripOffsetSuffix(inner);
    const ms = Number.parseInt(msPart, 10);
    if (!Number.isFinite(ms)) {
        return null;
    }
    return new Date(ms);
}

function stripDateWrapper(value: string): string | null {
    const prefix = '/Date(';
    const suffix = ')/';
    if (!value.startsWith(prefix) || !value.endsWith(suffix)) {
        return null;
    }
    return value.slice(prefix.length, value.length - suffix.length);
}

function stripOffsetSuffix(value: string): string {
    // Offset can be "+0200" or "-0500"; strip from first sign character on.
    for (let i = 1; i < value.length; i++) {
        const c = value.charCodeAt(i);
        if (c === 43 || c === 45) { // '+' or '-'
            return value.slice(0, i);
        }
    }
    return value;
}

/**
 * Filter to only siblings that share the given parent (same VS Code
 * utility renderer). When `vsCodeUtilityPid` is undefined, returns the input
 * unchanged — useful for environments where we cannot identify our own
 * parent (e.g. non-Windows).
 */
export function filterByParent(
    siblings: SiblingClaudeProcess[],
    vsCodeUtilityPid: number | undefined,
): SiblingClaudeProcess[] {
    if (vsCodeUtilityPid === undefined) {
        return siblings;
    }
    return siblings.filter((s) => s.parentPid === vsCodeUtilityPid);
}

/**
 * Return only siblings whose createdAt is BEFORE the gateway's startedAt.
 * Those are the candidates for "MCP transport may be stuck disconnected
 * because the daemon they handshook with no longer exists".
 *
 * The comparison is strict (`<`, not `<=`): a sibling that started at
 * exactly the same instant as the gateway is treated as fresh.
 */
export function filterStaleSiblings(
    siblings: SiblingClaudeProcess[],
    gatewayStartedAt: Date,
): SiblingClaudeProcess[] {
    const cutoffMs = gatewayStartedAt.getTime();
    return siblings.filter((s) => s.createdAt.getTime() < cutoffMs);
}
