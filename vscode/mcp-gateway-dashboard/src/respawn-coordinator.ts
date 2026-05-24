/**
 * Respawn coordinator -- Path 1 of FM-33 spike (closes Gap B: N windows
 * x 1 daemon respawn = 1 prompt instead of N).
 *
 * Background: each VSCode window instantiates its own StalenessDetector
 * (transport-staleness.ts). When the gateway daemon respawns, every
 * detector independently fires showWarningMessage(), flooding the
 * operator with N near-simultaneous popups. This coordinator lets the
 * first detector claim the respawn event via an atomic filesystem
 * sentinel; the others observe the claim and skip the user prompt.
 *
 * Mechanism: a per-started-at sentinel file at
 *   %TEMP%/mcp-gateway/respawn-{started_at_ms}.claimed
 * containing JSON {pid, windowId, claimedAtMs}. The first detector
 * opens the file with O_CREAT|O_EXCL ("wx" flag in Node); others get
 * EEXIST and read the claimant JSON to log the loser path.
 *
 * Cleanup: two layers.
 *   - claim-time sweep: on every claim attempt, list respawn-*.claimed
 *     in the temp dir and unlink any older than mtime+1h (handles the
 *     normal case of one respawn per day).
 *   - activate-time sweep (F-05 v2 fix): callable separately at
 *     extension activate() so an operator who never sees a respawn for
 *     days does not accumulate stale files.
 *
 * Why filesystem over REST:
 *   - no new daemon endpoint required
 *   - O_CREAT|O_EXCL is atomic on Windows + Unix (Node fs docs)
 *   - daemon respawn means /health is flapping; the FS is independent
 *     of daemon liveness
 *   - matches existing repo precedent (per-PPID .session files,
 *     .stop-ack, .spawn-token lease)
 */

import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import { logger } from './logger';

/** Default mtime threshold for stale-sweep deletion (1 hour). */
const STALE_SWEEP_AGE_MS = 60 * 60 * 1000;

/** Subdirectory under os.tmpdir() that holds the sentinel files. */
const COORDINATOR_SUBDIR = 'mcp-gateway';

/** Filename pattern: respawn-{startedAtMs}.claimed */
const FILE_PREFIX = 'respawn-';
const FILE_SUFFIX = '.claimed';

export type ClaimResult =
    | { kind: 'won' }
    | { kind: 'lost'; claimedBy: { pid: number; windowId?: string; claimedAtMs: number } };

export interface RespawnCoordinator {
    /**
     * Attempt to claim the respawn event identified by startedAtMs (the
     * gateway daemon started_at converted to ms). Exactly one caller
     * across all dashboard-extension instances on this host wins; the
     * others get 'lost' with the winner's claim metadata.
     */
    claim(startedAtMs: number): ClaimResult;

    /**
     * Sweep stale sentinel files (claim-time + activate-time hook).
     * Returns the number unlinked. Safe to call frequently.
     */
    sweepStale(): number;
}

export interface CoordinatorOptions {
    /** Override base directory. Default os.tmpdir(). */
    baseDir?: string;
    /** Override caller PID (test injection). */
    pid?: number;
    /** Tag this dashboard instance for the loser-side log line. */
    windowId?: string;
    /** Stale threshold override (ms). Default 1h. */
    staleAgeMs?: number;
    /** Clock injection for tests. Default Date.now. */
    now?: () => number;
}

/**
 * Construct a coordinator with the default temp-dir layout. The
 * returned object is stateless aside from logger sourcing.
 */
export function createDefaultRespawnCoordinator(opts: CoordinatorOptions = {}): RespawnCoordinator {
    const baseDir = opts.baseDir ?? path.join(os.tmpdir(), COORDINATOR_SUBDIR);
    const pid = opts.pid ?? process.pid;
    const windowId = opts.windowId ?? '';
    const staleAgeMs = opts.staleAgeMs ?? STALE_SWEEP_AGE_MS;
    const now = opts.now ?? (() => Date.now());

    function ensureBaseDir(): void {
        try {
            fs.mkdirSync(baseDir, { recursive: true });
        } catch (err) {
            // Directory may already exist (race). Any other error gets
            // surfaced via the subsequent open call.
            logger.debug('respawn-coordinator', 'mkdir baseDir: ' + String(err));
        }
    }

    function sentinelPath(startedAtMs: number): string {
        return path.join(baseDir, FILE_PREFIX + startedAtMs + FILE_SUFFIX);
    }

    function sweepStale(): number {
        let removed = 0;
        let entries: string[];
        try {
            entries = fs.readdirSync(baseDir);
        } catch {
            return 0;
        }
        const cutoff = now() - staleAgeMs;
        for (const name of entries) {
            if (!name.startsWith(FILE_PREFIX) || !name.endsWith(FILE_SUFFIX)) {
                continue;
            }
            const file = path.join(baseDir, name);
            try {
                const stat = fs.statSync(file);
                if (stat.mtimeMs < cutoff) {
                    fs.unlinkSync(file);
                    removed++;
                }
            } catch {
                // file gone between readdir + stat/unlink; skip
            }
        }
        if (removed > 0) {
            logger.info('respawn-coordinator', 'swept ' + removed + ' stale sentinel(s)');
        }
        return removed;
    }

    function claim(startedAtMs: number): ClaimResult {
        ensureBaseDir();
        sweepStale();

        const file = sentinelPath(startedAtMs);
        const payload = JSON.stringify({
            pid,
            windowId,
            claimedAtMs: now(),
        });

        try {
            fs.writeFileSync(file, payload, { flag: 'wx' });
            logger.info('respawn-coordinator', 'claimed respawn started_at=' + startedAtMs);
            return { kind: 'won' };
        } catch (err) {
            const code = (err as NodeJS.ErrnoException).code;
            if (code !== 'EEXIST') {
                // Disk full, permission, etc. -- fall back to "won" so the
                // detector still prompts (better to over-prompt than to
                // silently lose the signal).
                logger.warn(
                    'respawn-coordinator',
                    'sentinel write failed with non-EEXIST; treating as won',
                    err,
                );
                return { kind: 'won' };
            }
            // Race lost -- read the claimant. Read failure also falls back to
            // "won" so we never deadlock.
            try {
                const raw = fs.readFileSync(file, 'utf8');
                const claim = JSON.parse(raw) as { pid: number; windowId?: string; claimedAtMs: number };
                logger.info(
                    'respawn-coordinator',
                    'lost claim started_at=' + startedAtMs + ' to pid=' + claim.pid
                        + ' (windowId=' + (claim.windowId || '<unset>') + ')',
                );
                return { kind: 'lost', claimedBy: claim };
            } catch (readErr) {
                logger.warn(
                    'respawn-coordinator',
                    'sentinel exists but unreadable; treating as won',
                    readErr,
                );
                return { kind: 'won' };
            }
        }
    }

    return { claim, sweepStale };
}
