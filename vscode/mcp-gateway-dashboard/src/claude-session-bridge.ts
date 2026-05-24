/**
 * Claude session bridge -- closes the register-pid pipeline Gap 3.
 *
 * Background (per spike 2026-05-23 V2 + memory
 * project_register_pid_pipeline_broken_2026_05_24): the gateway exposes
 * POST /api/v1/claude-code/register-pid and stores (sessionId, pid,
 * windowId) in patchState.sessionPids so /unfreeze can target a specific
 * claude.exe and FM-9 multi-instance limit can deduplicate by window. The
 * only production caller of register-pid is
 * claude-team-control/hooks/statusline.mjs. In VSCode-embedded Claude
 * Code 2.1.145 the CLI statusline command is never invoked, so
 * register-pid never fires, sessionPids is always empty, and /unfreeze +
 * FM-9 are silently non-functional.
 *
 * This bridge replaces the broken statusline pipeline by reading the
 * per-PID session files Claude Code itself writes at
 *   ~/.claude/sessions/PID.json
 *   ~/.claude-personal/sessions/PID.json
 * and POSTing register-pid for each. The file shape (confirmed
 * 2026-05-24 against cc 2.1.145) is:
 *   { pid, sessionId, cwd, startedAt, version, kind, entrypoint }
 *
 * Idempotency: an in-memory Set of "sessionId:pid" tuples avoids
 * spamming the gateway on every cache refresh. The set is cleared
 * automatically when sync() is called with a different gatewayKey
 * (typically the daemon started_at ISO timestamp) so a respawn triggers
 * re-registration against the fresh sessionPids map.
 */

import * as fs from 'fs';
import * as path from 'path';
import * as os from 'os';
import * as vscode from 'vscode';
import { logger } from './logger';

/**
 * Default Claude Code data dirs scanned for session files. Two profiles
 * (.claude + .claude-personal) cover the layout observed on this
 * machine; other layouts (custom CLAUDE_HOME, additional profile dirs)
 * can override via the homes argument to readClaudeSessions and
 * ClaudeSessionBridge.
 */
export const DEFAULT_CLAUDE_HOMES: readonly string[] = [
    path.join(os.homedir(), '.claude'),
    path.join(os.homedir(), '.claude-personal'),
];

interface ClaudeSessionFile {
    pid: number;
    sessionId: string;
    cwd?: string;
    startedAt?: number;
    version?: string;
    kind?: string;
    entrypoint?: string;
}

export interface ClaudeSession {
    pid: number;
    sessionId: string;
    sourceFile: string;
}

/**
 * Read Claude Code session files across known profile homes. Returns
 * one entry per readable file; silently skips parse failures and
 * missing dirs. Validates that pid is a positive integer and sessionId
 * is a non-empty string -- anything else is dropped so a malformed
 * file cannot poison the registration cycle.
 */
export function readClaudeSessions(homes: readonly string[] = DEFAULT_CLAUDE_HOMES): ClaudeSession[] {
    const sessions: ClaudeSession[] = [];
    for (const home of homes) {
        const dir = path.join(home, 'sessions');
        let entries: string[];
        try {
            entries = fs.readdirSync(dir).filter(n => n.endsWith('.json'));
        } catch {
            continue;
        }
        for (const name of entries) {
            const file = path.join(dir, name);
            try {
                const raw = fs.readFileSync(file, 'utf8');
                const json = JSON.parse(raw) as ClaudeSessionFile;
                if (
                    typeof json.pid === 'number' &&
                    Number.isInteger(json.pid) &&
                    json.pid > 0 &&
                    typeof json.sessionId === 'string' &&
                    json.sessionId.length > 0
                ) {
                    sessions.push({ pid: json.pid, sessionId: json.sessionId, sourceFile: file });
                }
            } catch {
                // skip malformed file
            }
        }
    }
    return sessions;
}

/**
 * Minimal contract the bridge needs from the gateway client. Matches
 * the shape used by GatewayClient.registerPid added alongside this
 * module.
 */
export interface RegisterPidApi {
    registerPid(req: { session_id: string; pid: number; window_id?: string }): Promise<unknown>;
}

/**
 * Maintains the gateway sessionPids map for VSCode-embedded Claude
 * Code users by reading Claude Code own per-PID session files and
 * POSTing register-pid. Drop-in replacement for the broken statusline
 * pipeline.
 */
export class ClaudeSessionBridge implements vscode.Disposable {
    private readonly registered = new Set<string>();
    private lastGatewayKey: string | undefined;
    private disposed = false;

    constructor(
        private readonly client: RegisterPidApi,
        private readonly readSessions: () => ClaudeSession[] = () => readClaudeSessions(),
        private readonly windowId: string = '',
    ) {}

    /**
     * Push all currently-known sessions to the gateway. Returns the
     * number of new register-pid posts attempted (already-registered
     * tuples are skipped). Errors are logged + swallowed -- the next
     * refresh retries.
     *
     * @param gatewayKey  Opaque identifier of the current gateway
     *   lifetime (typically the daemon started_at ISO timestamp). When
     *   it changes, the internal idempotency set is cleared so a
     *   respawn triggers re-registration against the new (empty)
     *   sessionPids map. Pass undefined when the gateway is unhealthy;
     *   the bridge returns 0 without touching the registration state
     *   -- next healthy refresh re-detects the respawn legitimately.
     */
    async sync(gatewayKey?: string): Promise<number> {
        if (this.disposed) {
            return 0;
        }
        if (gatewayKey === undefined) {
            return 0;
        }
        if (this.lastGatewayKey !== undefined && this.lastGatewayKey !== gatewayKey) {
            logger.info(
                'claude-session-bridge',
                'gateway respawn detected (' + this.lastGatewayKey + ' -> ' + gatewayKey
                    + '); clearing ' + this.registered.size + ' cached registration(s)',
            );
            this.registered.clear();
        }
        this.lastGatewayKey = gatewayKey;
        const sessions = this.readSessions();
        let posted = 0;
        for (const s of sessions) {
            const key = s.sessionId + ':' + s.pid;
            if (this.registered.has(key)) {
                continue;
            }
            try {
                await this.client.registerPid({
                    session_id: s.sessionId,
                    pid: s.pid,
                    window_id: this.windowId,
                });
                this.registered.add(key);
                posted++;
                logger.info(
                    'claude-session-bridge',
                    'registered sid=' + s.sessionId.slice(0, 8) + '... pid=' + s.pid
                        + ' (' + path.basename(s.sourceFile) + ')',
                );
            } catch (err) {
                logger.warn(
                    'claude-session-bridge',
                    'register-pid failed sid=' + s.sessionId.slice(0, 8) + '... pid=' + s.pid,
                    err,
                );
            }
        }
        return posted;
    }

    /**
     * Forget previously-registered tuples -- wired to the
     * gateway-respawn signal so the next sync repopulates the (newly
     * empty) sessionPids map.
     */
    resetRegistrations(): void {
        this.registered.clear();
    }

    /** Test helper. */
    isRegistered(sessionId: string, pid: number): boolean {
        return this.registered.has(sessionId + ':' + pid);
    }

    dispose(): void {
        this.disposed = true;
        this.registered.clear();
    }
}
