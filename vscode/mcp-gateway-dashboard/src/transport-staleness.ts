/**
 * Transport-staleness detector — spike 2026-05-11 FM 3 proper-fix path D.
 *
 * Watches `cachedGatewayHealth.started_at` across refresh cycles. When the
 * timestamp jumps forward (gateway respawned), enumerate sibling claude.exe
 * processes and surface any that pre-date the new gateway start — those are
 * the MCPR.1 candidates (their MCP HTTP transport may be stuck pointing at
 * the dead daemon).
 *
 * Recovery offered to the user:
 *   - Default behavior (opt-in setting `mcpGateway.autoKillStaleClaudeSessions`
 *     unset / false): show a notification with an action button "Kill stale
 *     claude.exe sessions" — user confirms; reopening Claude starts a fresh
 *     MCP handshake against the live gateway.
 *   - Opt-in (setting true): silently kill stale siblings as soon as
 *     detected. No notification noise; user opens a new conversation.
 *
 * Rationale: killing a user's running claude.exe loses session state. That
 * is acceptable when the alternative is a silently broken MCP transport
 * the user does not realise is broken (today's confusion). It must NEVER
 * happen without consent for the default install.
 */

import * as vscode from 'vscode';
import type { HealthResponse } from './types';
import { logger } from './logger';
import {
    type SiblingClaudeProcess,
    type SiblingDetector,
    createDefaultSiblingDetector,
    filterStaleSiblings,
} from './sibling-detector';
import { type RespawnCoordinator } from './respawn-coordinator';

/** Kill function abstraction — production uses process.kill, tests inject a spy. */
export type KillFn = (pid: number, signal?: NodeJS.Signals | number) => void;

/** Setting key for the opt-in auto-kill behavior. */
const SETTING_AUTO_KILL = 'autoKillStaleClaudeSessions';

/** Notification action buttons. */
const ACTION_KILL = 'Kill stale claude.exe sessions';
const ACTION_DISMISS = 'Dismiss';

/**
 * Public detector contract. Caller wires `noteHealth` into ServerDataCache's
 * onDidRefresh event. On detected respawn-with-stale-siblings, the detector
 * either auto-kills (if opted-in) or prompts the user (default).
 */
export interface StalenessDetector extends vscode.Disposable {
    /**
     * Record the latest health snapshot. Returns the number of stale siblings
     * detected this cycle (0 = no respawn or no pre-existing siblings).
     * Public return value is for tests; the caller can ignore it.
     */
    noteHealth(health: HealthResponse | null): Promise<number>;
}

export interface StalenessDetectorOptions {
    /** Source of sibling claude.exe processes. Default uses platform detector. */
    siblingDetector?: SiblingDetector;
    /** Override `process.kill` for tests. Default uses the real one. */
    killFn?: KillFn;
    /**
     * Source of the auto-kill setting. Default reads `mcpGateway.<SETTING_AUTO_KILL>`
     * from VS Code workspace config. Tests inject a constant.
     */
    autoKillEnabled?: () => boolean;
    /**
     * Source for the user prompt. Default uses vscode.window.showWarningMessage.
     * Tests inject a stub that returns a canned action label.
     */
    promptUser?: (
        message: string,
        ...actions: string[]
    ) => Promise<string | undefined>;
    /**
     * Cross-window respawn coordinator (Path 1 of FM-33 spike, Option B).
     * When omitted the detector behaves as if it always wins the claim —
     * useful for tests that don't exercise multi-window coordination and
     * as a safety net when the dashboard extension activates against a
     * gateway that predates the /respawn-claim endpoint. Production code
     * in extension.ts always injects the gateway-backed coordinator.
     */
    respawnCoordinator?: RespawnCoordinator;
}

/**
 * Local fallback when no coordinator is supplied. Behaves as "always won"
 * — preserves the pre-Option-B per-window prompt behavior so the detector
 * never silently suppresses a prompt due to a missing dependency.
 */
const ALWAYS_WON_COORDINATOR: RespawnCoordinator = {
    claim: async () => ({ kind: 'won' as const }),
};

export function createDefaultStalenessDetector(
    opts?: StalenessDetectorOptions,
): StalenessDetector {
    return new StalenessDetectorImpl({
        siblingDetector: opts?.siblingDetector ?? createDefaultSiblingDetector(),
        killFn: opts?.killFn ?? ((pid, signal) => process.kill(pid, signal)),
        autoKillEnabled: opts?.autoKillEnabled ?? defaultAutoKillEnabled,
        promptUser:
            opts?.promptUser ??
            ((message, ...actions) =>
                Promise.resolve(vscode.window.showWarningMessage(message, ...actions))),
        respawnCoordinator: opts?.respawnCoordinator ?? ALWAYS_WON_COORDINATOR,
    });
}

function defaultAutoKillEnabled(): boolean {
    const cfg = vscode.workspace.getConfiguration('mcpGateway');
    return cfg.get<boolean>(SETTING_AUTO_KILL, false);
}

class StalenessDetectorImpl implements StalenessDetector {
    private readonly siblingDetector: SiblingDetector;
    private readonly killFn: KillFn;
    private readonly autoKillEnabled: () => boolean;
    private readonly promptUser: (
        message: string,
        ...actions: string[]
    ) => Promise<string | undefined>;
    private readonly respawnCoordinator: RespawnCoordinator;

    /**
     * Tracks the started_at of the most recently observed live gateway. The
     * first non-null observation seeds this; subsequent refreshes compare.
     * Reset on Dispose so a re-activation starts fresh.
     */
    private lastSeenStartedAt: number | undefined;

    /**
     * Tracks PIDs we already prompted/killed for the current respawn cycle so
     * we do not nag the user repeatedly while they are still deciding (cache
     * refreshes every 5s).
     */
    private handledPids = new Set<number>();

    private disposed = false;

    constructor(opts: Required<StalenessDetectorOptions>) {
        this.siblingDetector = opts.siblingDetector;
        this.killFn = opts.killFn;
        this.autoKillEnabled = opts.autoKillEnabled;
        this.promptUser = opts.promptUser;
        this.respawnCoordinator = opts.respawnCoordinator;
    }

    async noteHealth(health: HealthResponse | null): Promise<number> {
        if (this.disposed) {
            return 0;
        }
        if (health === null || typeof health.started_at !== 'string') {
            // No health = no signal; preserve last-seen to detect the next
            // legitimate respawn after a transient outage.
            return 0;
        }
        const startedAtMs = Date.parse(health.started_at);
        if (!Number.isFinite(startedAtMs)) {
            return 0;
        }
        if (this.lastSeenStartedAt === undefined) {
            this.lastSeenStartedAt = startedAtMs;
            return 0; // first observation, no respawn detected yet
        }
        if (startedAtMs <= this.lastSeenStartedAt) {
            return 0; // same gateway, no respawn
        }
        // Respawn detected — gateway started_at advanced. Reset the handled
        // set so siblings from the previous cycle get re-evaluated against
        // the new cutoff.
        const previousStartedAt = this.lastSeenStartedAt;
        this.lastSeenStartedAt = startedAtMs;
        this.handledPids.clear();
        logger.info(
            'staleness',
            `gateway respawn detected: started_at ${new Date(previousStartedAt).toISOString()} -> ${health.started_at}`,
        );
        return await this.handleRespawn(new Date(startedAtMs));
    }

    dispose(): void {
        this.disposed = true;
        this.lastSeenStartedAt = undefined;
        this.handledPids.clear();
    }

    private async handleRespawn(gatewayStartedAt: Date): Promise<number> {
        const allSiblings = await this.siblingDetector.enumerate();
        if (allSiblings.length === 0) {
            return 0; // non-Windows or no claude.exe present
        }
        const stale = filterStaleSiblings(allSiblings, gatewayStartedAt).filter(
            (s) => !this.handledPids.has(s.pid),
        );
        if (stale.length === 0) {
            return 0;
        }
        // Mark every candidate as handled before we await user input — if the
        // next cache refresh fires while the dialog is open, we must not
        // re-prompt for the same PIDs.
        for (const s of stale) {
            this.handledPids.add(s.pid);
        }
        // Path 1 sentinel (FM-33 spike, Gap B): coordinate with sibling
        // dashboard-extension instances across other VSCode windows so a
        // single daemon respawn produces ONE user prompt, not N. The first
        // detector to atomically create the per-startedAt sentinel file wins
        // and keeps the existing prompt/kill path; losers observe the
        // sentinel and skip the UI prompt (autoKill still runs locally — the
        // kill action is idempotent and the gateway is the single source of
        // truth for the surviving claude.exe).
        const claim = await this.respawnCoordinator.claim(gatewayStartedAt.getTime());
        if (claim.kind === 'lost') {
            logger.info(
                'staleness',
                `respawn coordination: claim lost (pid=${claim.claimedBy.pid}) — skipping user prompt`,
            );
            return stale.length;
        }
        if (this.autoKillEnabled()) {
            await this.killAll(stale);
            this.showAutoKillSummary(stale);
            return stale.length;
        }
        const message = buildPromptMessage(stale);
        const choice = await this.promptUser(message, ACTION_KILL, ACTION_DISMISS);
        if (choice === ACTION_KILL) {
            await this.killAll(stale);
            this.showAutoKillSummary(stale);
        } else {
            logger.info(
                'staleness',
                `user dismissed stale-sibling prompt; ${stale.length} candidate(s) left running`,
            );
        }
        return stale.length;
    }

    private async killAll(stale: SiblingClaudeProcess[]): Promise<void> {
        for (const s of stale) {
            try {
                // SIGTERM first. Node maps to TerminateProcess on Windows;
                // claude.exe gets ~immediate exit. The fallback SIGKILL path
                // in proc_live_other.go is for gateway-spawned children, not
                // for sibling claude.exe — we deliberately do not escalate
                // here, leaving the choice to the OS.
                this.killFn(s.pid, 'SIGTERM');
                logger.info('staleness', `killed stale claude.exe pid=${s.pid}`);
            } catch (err) {
                // Likely ESRCH (already gone) or EPERM. Log + move on.
                logger.warn('staleness', `kill pid=${s.pid} failed`, err);
            }
        }
    }

    private showAutoKillSummary(stale: SiblingClaudeProcess[]): void {
        const summary =
            stale.length === 1
                ? `MCP Gateway: terminated 1 stale claude.exe session after gateway restart. Open a new Claude conversation for a fresh MCP transport.`
                : `MCP Gateway: terminated ${stale.length} stale claude.exe sessions after gateway restart. Open a new Claude conversation for a fresh MCP transport.`;
        void vscode.window.showInformationMessage(summary);
    }
}

/**
 * Compose the warning prompt shown when stale siblings are detected.
 * Exported for tests.
 */
export function buildPromptMessage(stale: SiblingClaudeProcess[]): string {
    const count = stale.length;
    const noun = count === 1 ? 'session' : 'sessions';
    return (
        `MCP Gateway respawned. ${count} claude.exe ${noun} started before the restart ` +
        `may have a stuck-disconnected MCP transport (MCPR.1). ` +
        `Kill them so reopening Claude starts a fresh MCP handshake?`
    );
}
