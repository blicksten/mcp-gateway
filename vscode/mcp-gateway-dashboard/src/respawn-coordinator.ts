/**
 * Respawn coordinator -- Path 1 of FM-33 spike (closes Gap B: N windows
 * x 1 daemon respawn = 1 prompt instead of N).
 *
 * Background: each VSCode window instantiates its own StalenessDetector
 * (transport-staleness.ts). When the gateway daemon respawns, every
 * detector independently fires showWarningMessage(), flooding the
 * operator with N near-simultaneous popups. This coordinator lets the
 * first detector claim the respawn event; the others observe the claim
 * and skip the user prompt.
 *
 * Option B refactor (2026-05-25): coordination point is the gateway's
 * own `POST /api/v1/claude-code/respawn-claim` endpoint, NOT a
 * filesystem sentinel. Reasons:
 *   - co-located with sessionPids in the same patchState owner (one
 *     mutex serves both)
 *   - no %TEMP% sentinel files to sweep
 *   - no cross-process file-lock races
 *   - dashboard extension already has REST client; orchestrator
 *     dependency NOT added (which Option C would have required)
 *
 * The v1 filesystem implementation lived 1 day before this refactor.
 * See git history for the prior approach and spike-2026-05-23 V3 for
 * why filesystem was ultimately the wrong call.
 */

import { logger } from './logger';

/** Minimal contract the coordinator needs from the gateway client. */
export interface ClaimRespawnApi {
    claimRespawn(req: { started_at_ms: number; pid?: number; window_id?: string }): Promise<{
        kind: 'won' | 'lost';
        claimed_by?: { pid: number; window_id?: string; claimed_at_ms: number };
    }>;
}

export type ClaimResult =
    | { kind: 'won' }
    | { kind: 'lost'; claimedBy: { pid: number; windowId?: string; claimedAtMs: number } };

export interface RespawnCoordinator {
    /**
     * Attempt to claim the respawn event identified by startedAtMs (the
     * gateway daemon started_at converted to ms). Exactly one caller
     * across all dashboard-extension instances on this host wins; the
     * others get 'lost' with the winner's claim metadata.
     *
     * Network/transport failures fall back to 'won' so the operator still
     * sees the prompt (better to over-prompt than silently lose the
     * signal). Same fallback semantic as the v1 filesystem implementation.
     */
    claim(startedAtMs: number): Promise<ClaimResult>;
}

export interface CoordinatorOptions {
    /** Override caller PID (test injection). */
    pid?: number;
    /** Tag this dashboard instance for the loser-side log line. */
    windowId?: string;
}

/**
 * Construct a coordinator that consults the gateway's atomic claim endpoint.
 */
export function createDefaultRespawnCoordinator(
    client: ClaimRespawnApi,
    opts: CoordinatorOptions = {},
): RespawnCoordinator {
    const pid = opts.pid ?? process.pid;
    const windowId = opts.windowId ?? '';

    return {
        async claim(startedAtMs: number): Promise<ClaimResult> {
            if (!Number.isFinite(startedAtMs) || startedAtMs <= 0) {
                // Caller-side guard mirrors the gateway-side validation. A bad
                // startedAtMs here means the cache delivered a malformed
                // health.started_at -- treat as "won" so the prompt still fires.
                logger.warn(
                    'respawn-coordinator',
                    'invalid startedAtMs=' + startedAtMs + ' -- treating as won',
                );
                return { kind: 'won' };
            }
            try {
                const res = await client.claimRespawn({ started_at_ms: startedAtMs, pid, window_id: windowId });
                if (res.kind === 'won') {
                    logger.info('respawn-coordinator', 'claimed respawn started_at=' + startedAtMs);
                    return { kind: 'won' };
                }
                const cb = res.claimed_by;
                if (cb) {
                    logger.info(
                        'respawn-coordinator',
                        'lost claim started_at=' + startedAtMs + ' to pid=' + cb.pid
                            + ' (windowId=' + (cb.window_id || '<unset>') + ')',
                    );
                    return {
                        kind: 'lost',
                        claimedBy: { pid: cb.pid, windowId: cb.window_id, claimedAtMs: cb.claimed_at_ms },
                    };
                }
                // Gateway returned kind='lost' without metadata -- unexpected
                // but treat as a normal 'lost' so we still suppress the prompt.
                logger.warn('respawn-coordinator', 'lost claim without claimant metadata');
                return { kind: 'lost', claimedBy: { pid: 0, claimedAtMs: 0 } };
            } catch (err) {
                // Transport / HTTP error -- can't coordinate, so fall back to
                // local prompt (over-prompt > silent loss). Same fallback
                // policy as v1 filesystem implementation on non-EEXIST errors.
                logger.warn(
                    'respawn-coordinator',
                    'gateway claim failed -- treating as won',
                    err,
                );
                return { kind: 'won' };
            }
        },
    };
}
