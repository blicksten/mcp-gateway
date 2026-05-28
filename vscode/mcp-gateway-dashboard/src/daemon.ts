import { spawn as nodeSpawn, type ChildProcess, type SpawnOptions } from 'node:child_process';
import * as vscode from 'vscode';
import type { IGatewayClient } from './extension';
import { GatewayError } from './gateway-client';
import { logger } from './logger';
import type { DaemonLogFile } from './daemon-log-file';

/** Spawn function signature — matches child_process.spawn subset used here. */
export type SpawnFn = (cmd: string, args: string[], opts: SpawnOptions) => ChildProcess;

/** Configuration for the crash-recovery supervisor built into DaemonManager. */
export interface CrashRestartConfig {
	/** Initial backoff delay before first restart attempt. Default 1000ms. */
	initialDelayMs?: number;
	/** Maximum backoff delay. Default 60_000ms. */
	maxDelayMs?: number;
	/** Jitter ratio applied as ±fraction of baseDelay. Default 0.2 (±20%). */
	jitterRatio?: number;
	/** Maximum crash attempts counted in the rolling window before aborting. Default 5. */
	maxAttemptsInWindow?: number;
	/** Rolling window size for counting quick crashes. Default 600_000ms (10 min). */
	windowMs?: number;
	/** Minimum uptime (ms) for a run to be considered stable; resets crash counter. Default 60_000ms. */
	stableThresholdMs?: number;
}

/** Options for DaemonManager constructor — all optional for backward compatibility. */
export interface DaemonManagerOptions {
	/** Auto-restart on unexpected child exit (default true). */
	autoRestartOnCrash?: boolean;
	/** Optional file-backed log sink for stdout/stderr/lifecycle events. */
	fileLogger?: DaemonLogFile;
	/** Override backoff schedule for tests. */
	crashRestartConfig?: CrashRestartConfig;
	/** Injectable timers for tests. */
	setTimeout?: (cb: () => void, ms: number) => ReturnType<typeof setTimeout>;
	clearTimeout?: (h: ReturnType<typeof setTimeout>) => void;
	/** Jitter source for backoff calculation; default Math.random. */
	random?: () => number;
	/** Delay (ms) between the first failed getHealth probe and the re-probe
	 *  inside start(). Production default 2500ms — gives concurrent windows
	 *  time to win the spawn race. Tests pass 0 to keep mocha within its
	 *  default 2000ms per-test timeout. */
	raceDetectDelayMs?: number;
}

/**
 * Manages the mcp-gateway daemon process lifecycle.
 * Spawns the daemon if not already running, writes to the shared logger channel.
 * Includes crash-recovery supervisor with exponential backoff and crash-loop detection.
 */
export class DaemonManager {
	private child: ChildProcess | undefined;
	private readonly spawnFn: SpawnFn;
	private disposed = false;
	private starting = false;
	/** Promise of the in-flight start() call so concurrent start() invocations
	 *  await the same outcome instead of bailing with `false`. Fixes the race
	 *  where activate() fires daemon.start() and a startDaemon command click
	 *  before activate's spawn completes silently returns "already starting". */
	private startingPromise: Promise<boolean> | undefined;
	private stopping = false;
	// AUDIT A-H1: mutex with start()/stop(). Without this, auto-start + user
	// restart can race — REST shutdown kills daemon, then start() sees
	// starting=true (auto-start in flight) and returns false → daemon dead,
	// UI reports "did not restart".
	private restarting = false;
	// AUDIT B-NEW-30 (Phase 10): one-shot flag set by restart() after a
	// successful REST shutdown so the next start() bypasses the
	// `client.getHealth()` fast-path. Without this, if a successor daemon
	// (started externally between our shutdown and our spawn) responds to
	// the health probe, start() returns false ("already running") even
	// though we never spawned anything. Result: `daemon.running` is false,
	// next stopDaemon hits the no-op path, and UI reports inconsistent state.
	private skipHealthFastPathOnce = false;

	// Supervisor dependencies (injectable for tests)
	private readonly fileLogger: DaemonLogFile | undefined;
	private readonly autoRestartOnCrash: boolean;
	private readonly cfg: Required<CrashRestartConfig>;
	private readonly _setTimeout: (cb: () => void, ms: number) => ReturnType<typeof setTimeout>;
	private readonly _clearTimeout: (h: ReturnType<typeof setTimeout>) => void;
	private readonly _random: () => number;
	private readonly raceDetectDelayMs: number;

	// Supervisor runtime state
	private spawnedAt: number | undefined;
	private restartTimer: ReturnType<typeof setTimeout> | undefined;
	private crashTimestamps: number[] = [];
	private supervisorAborted = false;
	private expectedExit = false;

	constructor(
		private readonly client: IGatewayClient,
		private readonly daemonPath: string,
		/**
		 * @deprecated Pre-Phase-N legacy parameter, ignored at runtime.
		 * Removed in next major when test call sites migrate.
		 */
		_outputChannel?: vscode.OutputChannel,
		spawnFn?: SpawnFn,
		options?: DaemonManagerOptions,
	) {
		this.spawnFn = spawnFn ?? nodeSpawn;
		this.fileLogger = options?.fileLogger;
		this.autoRestartOnCrash = options?.autoRestartOnCrash ?? true;
		this.cfg = {
			initialDelayMs: options?.crashRestartConfig?.initialDelayMs ?? 1000,
			maxDelayMs: options?.crashRestartConfig?.maxDelayMs ?? 60_000,
			jitterRatio: options?.crashRestartConfig?.jitterRatio ?? 0.2,
			maxAttemptsInWindow: options?.crashRestartConfig?.maxAttemptsInWindow ?? 5,
			windowMs: options?.crashRestartConfig?.windowMs ?? 600_000,
			stableThresholdMs: options?.crashRestartConfig?.stableThresholdMs ?? 60_000,
		};
		this._setTimeout = options?.setTimeout ?? ((cb, ms) => setTimeout(cb, ms));
		this._clearTimeout = options?.clearTimeout ?? ((h) => clearTimeout(h));
		this._random = options?.random ?? Math.random;
		this.raceDetectDelayMs = options?.raceDetectDelayMs ?? 2500;
	}

	/** Number of quick crashes tracked in the current rolling window. */
	get crashCount(): number { return this.crashTimestamps.length; }

	/** True when the supervisor is armed and not aborted due to crash-loop. */
	get supervisorActive(): boolean {
		return this.autoRestartOnCrash && !this.supervisorAborted && !this.disposed;
	}

	/** True when a scheduled restart timer is pending. */
	get pendingRestartScheduled(): boolean { return this.restartTimer !== undefined; }

	/** Start the daemon if it is not already running. Returns true if THIS
	 *  call did the spawn; returns false if another call (in flight) won the
	 *  race or if the daemon is already running. */
	async start(): Promise<boolean> {
		if (this.disposed || this.child || this.restarting) { return false; }
		// Concurrent start() while one is in flight (e.g. activate's auto-start
		// racing with a startDaemon command click) — await the in-flight result
		// so daemon.running reflects the post-spawn state on return, but
		// return false because THIS call did not own the spawn. Preserves
		// D6-01's "exactly one call returns true" contract.
		if (this.starting && this.startingPromise) {
			await this.startingPromise.catch(() => false);
			return false;
		}
		this.starting = true;
		this.startingPromise = this._doStart().finally(() => {
			this.starting = false;
			this.startingPromise = undefined;
		});
		return this.startingPromise;
	}

	private async _doStart(): Promise<boolean> {
		try {
			// Check if gateway is already reachable — no need to spawn.
			// B-NEW-30: restart() sets `skipHealthFastPathOnce` after a
			// successful REST shutdown so we don't mistake a successor
			// daemon (started externally between our shutdown and our
			// spawn) for "already running". Consume the flag exactly once.
			if (this.skipHealthFastPathOnce) {
				this.skipHealthFastPathOnce = false;
			} else {
				try {
					await this.client.getHealth();
					logger.info('daemon', 'Gateway already running — skipping spawn.');
					return false;
				} catch (e) {
					// FM 8 (spike 2026-05-11): only non-connection GatewayError kinds
					// (timeout/auth/parse/http) indicate the daemon is alive-but-slow
					// (under FM 7 load). A blanket catch was causing PID-collision crashes
					// when two windows raced to spawn after parallel slow probes.
					// Non-GatewayError throws (e.g. plain Error from tests, unexpected
					// throws) fall through to the re-probe path below — only after the
					// re-probe also fails with non-skip semantics do we proceed to spawn.
					if (e instanceof GatewayError && e.kind !== 'connection') {
						logger.warn('daemon', `getHealth pre-spawn failed (kind=${e.kind}) — assuming gateway is alive, skipping spawn`, e);
						return false;
					}
					// Belt-and-suspenders re-probe with a delay so that a concurrent
					// window that won the spawn race has time to bind the port and
					// start serving before we check again. Without the delay all
					// windows do the re-probe in the same millisecond window,
					// see "still down", and all spawn — causing port-bind crashes
					// (exit code 1) and a visible offline blip on every window reload.
					if (this.disposed) { return false; }
					if (this.raceDetectDelayMs > 0) {
						await new Promise(resolve => setTimeout(resolve, this.raceDetectDelayMs));
					}
					if (this.disposed) { return false; }
					try {
						await this.client.getHealth();
						logger.info('daemon', 'Gateway reachable on re-probe — skipping spawn.');
						return false;
					} catch (e2) {
						if (e2 instanceof GatewayError && e2.kind !== 'connection') {
							logger.warn('daemon', `getHealth re-probe failed (kind=${e2.kind}) — assuming gateway is alive, skipping spawn`, e2);
							return false;
						}
						// Both probes failed with connection error — daemon is genuinely down. Proceed to spawn.
					}
				}
			}

			if (this.disposed) { return false; }

			const cmd = this.daemonPath || 'mcp-gateway';
			logger.info('daemon', `Starting: ${cmd}`);

			try {
				this.child = this.spawnFn(cmd, [], {
					stdio: ['ignore', 'pipe', 'pipe'],
					detached: false,
					windowsHide: true,
				});
			} catch (err) {
				logger.error('daemon', 'Failed to spawn', err);
				return false;
			}

			this.spawnedAt = Date.now();
			this.fileLogger?.writeEvent(`spawn: ${cmd}`);

			this.child.stdout?.on('data', (chunk: Buffer) => {
				const text = chunk.toString();
				if (!this.disposed) { logger.info('daemon', text.trimEnd()); }
				this.fileLogger?.writeStdout(text);
			});

			this.child.stderr?.on('data', (chunk: Buffer) => {
				const text = chunk.toString();
				if (!this.disposed) { logger.warn('daemon', `[stderr] ${text.trimEnd()}`); }
				this.fileLogger?.writeStderr(text);
			});

			this.child.on('error', (err) => {
				if (!this.disposed) { logger.error('daemon', 'Process error', err); }
				this.child = undefined;
				this.stopping = false;
			});

			this.child.on('exit', (code, signal) => {
				const reason = signal ? `signal ${signal}` : `code ${code ?? 'unknown'}`;
				if (!this.disposed) { logger.info('daemon', `Exited (${reason})`); }
				this.fileLogger?.writeEvent(`exit: ${reason}`);
				const aliveMs = this.spawnedAt ? Date.now() - this.spawnedAt : 0;
				this.child = undefined;
				this.stopping = false;
				this.spawnedAt = undefined;

				this.handleExit(code, signal, aliveMs);
			});

			return true;
		} catch (err) {
			// Spawn failures (e.g. ENOENT) bubble through the wrapped finally
			// above so `this.starting` resets correctly. Re-raise so callers see
			// the actual error rather than a misleading false.
			throw err;
		}
	}

	/**
	 * Stop the daemon gracefully.
	 *
	 * AUDIT B-NEW-25 (Phase 9): on Windows, Node maps `child.kill('SIGTERM')`
	 * to `TerminateProcess`, which gives the daemon NO chance to run signal
	 * handlers (no `defer pidfile.Remove()`, no graceful session cleanup).
	 * The fix is to attempt REST `/shutdown` first — works for any daemon
	 * regardless of which process spawned it, and the daemon's own signal
	 * handler runs to completion. SIGTERM remains as a last-resort fallback
	 * when the REST flow fails AND we still own a child handle.
	 *
	 * Mirrors the REST-first flow already in restart(); this is the second
	 * caller of that pattern (B-NEW-25 said "stop() and dispose() did not
	 * get the upgrade" — Phase 9 closes that gap).
	 *
	 * Returns a Promise so callers can `await` the graceful flow. Both
	 * production callsites (extension.ts:683 stopDaemon command, dispose()
	 * below) are already in async contexts and just need an `await`.
	 */
	async stop(timeoutMs = 5_000): Promise<void> {
		this.cancelPendingRestart();
		// AUDIT A-M1: reject during restart to avoid racing with the
		// restart() kill+spawn sequence. Also re-entry guard for stop().
		// REVIEW HIGH-2: expectedExit only set after re-entry guard passes —
		// prevents orphan flag suppressing next legitimate crash restart.
		if (this.stopping || this.restarting) { return; }
		this.expectedExit = true;
		// Nothing to stop and no daemon reachable to ask politely — bail out.
		if (!this.child) {
			const reachable = await this.client.getHealth().then(() => true, () => false);
			if (!reachable) { return; }
		}
		this.stopping = true;
		if (!this.disposed) { logger.info('daemon', 'Stopping...'); }

		// 1. Graceful REST shutdown — works for external daemons too.
		// On success: poll until /api/v1/health unreachable so the daemon's signal
		// handler runs to completion (defer pidfile.Remove, etc.). On
		// failure: fall through to SIGTERM if we own a child.
		let restShutdownAccepted = false;
		try {
			// [DEBUG-INSTR] trace caller of client.shutdown for "dashboard kills daemon" investigation
			logger.warn('daemon', 'shutdown.invocation@stop', new Error('shutdown caller trace'));
			await this.client.shutdown();
			restShutdownAccepted = true;
		} catch (err) {
			// REST failed (network down, 401, 5xx). Log; SIGTERM fallback
			// may still close out our owned child below.
			logger.warn('daemon', 'REST shutdown failed — will fall back to SIGTERM if local child is owned.', err);
		}

		// 2. If REST accepted, poll /api/v1/health until unreachable bounded by timeoutMs.
		// (When REST rejected we don't poll — it would just hit the same error
		// every iteration and waste the deadline before reaching SIGTERM fallback.)
		// NOTE: we deliberately do NOT short-circuit on this.disposed here. The
		// poll's own deadline bounds the wait, and dispose() calls stop() with
		// disposed=true — exiting early on disposed would skip past the
		// reachability check and fall through to the SIGTERM fallback even
		// when REST shutdown already gracefully terminated the daemon.
		if (restShutdownAccepted) {
			const deadline = Date.now() + timeoutMs;
			while (Date.now() < deadline) {
				const reachable = await this.client.getHealth().then(() => true, () => false);
				if (!reachable) {
					// Daemon shut down gracefully. If we owned a child, its
					// 'exit' handler will clear state; otherwise we clear
					// the stopping flag explicitly.
					if (!this.child) { this.stopping = false; }
					return;
				}
				await new Promise<void>((resolve) => this._setTimeout(() => resolve(), 200));
			}
			// Deadline passed but daemon still reachable — fall through to SIGTERM
			// for our child if we own it (last resort).
			if (!this.disposed) { logger.warn('daemon', 'Graceful shutdown timed out — falling back to SIGTERM.'); }
		}

		// 3. SIGTERM fallback: only if we own a child. If not, there's nothing
		// local to kill — the daemon was started externally and didn't honour
		// our REST shutdown. Operator must intervene.
		if (this.child) {
			this.child.kill('SIGTERM');
			// child = undefined and stopping = false set by the 'exit' handler.
		} else {
			this.stopping = false;
		}
	}

	/**
	 * Restart the daemon via REST (AUDIT H-1 fix).
	 *
	 * Works both for extension-owned children and for daemons started
	 * externally (mcp-ctl daemon start, manual spawn). The flow is:
	 *   1. POST /api/v1/shutdown — graceful exit regardless of ownership
	 *   2. Poll /api/v1/health until unreachable (daemon actually exited)
	 *   3. Clean up own child handle if any
	 *   4. start() — spawns a fresh daemon
	 *
	 * Returns true when a new daemon was spawned, false if the existing
	 * one could not be shut down within timeoutMs.
	 *
	 * AUDIT A-M3: `timeoutMs` bounds the poll-until-unreachable loop only.
	 * Total wall-clock also includes the shutdown REST call (up to the
	 * GatewayClient timeout, default 5s) and the post-start health probe,
	 * so worst-case is roughly `timeoutMs + client.timeoutMs + 2s`. The
	 * spawn() step is fire-and-forget-detect — start() polls /api/v1/health via
	 * its own fast-path check.
	 */
	async restart(timeoutMs = 10_000): Promise<boolean> {
		this.cancelPendingRestart();
		// AUDIT A-H1/A-M1: hard mutex with start()/stop(). If a restart is
		// already in flight OR the manager is disposed, refuse re-entry.
		// REVIEW HIGH-2: expectedExit only set after re-entry guard passes —
		// prevents orphan flag suppressing next legitimate crash restart.
		if (this.disposed || this.restarting) { return false; }
		this.expectedExit = true;
		this.restarting = true;
		try {
			logger.info('daemon', 'Restarting...');

			// 1. Graceful REST shutdown — works for external daemons.
			try {
				// [DEBUG-INSTR] trace caller of client.shutdown for "dashboard kills daemon" investigation
				logger.warn('daemon', 'shutdown.invocation@restart', new Error('shutdown caller trace'));
				await this.client.shutdown();
			} catch (err) {
				// Daemon may be unreachable, auth may have failed, or endpoint
				// may not exist on older daemons. Log the reason so operators
				// have a diagnostic trail (CV-LOW fix), then proceed to poll —
				// if /api/v1/health is reachable we'll still time out and bail out.
				logger.warn('daemon', 'REST shutdown failed — proceeding to poll /api/v1/health anyway.', err);
			}

			// 2. Poll /api/v1/health until unreachable.
			const deadline = Date.now() + timeoutMs;
			let daemonStillReachable = true;
			while (Date.now() < deadline) {
				if (this.disposed) { return false; }
				try {
					await this.client.getHealth();
				} catch {
					daemonStillReachable = false;
					break; // unreachable — daemon is down
				}
				await new Promise<void>((resolve) => this._setTimeout(() => resolve(), 200));
			}
			// If the poll loop exited because we hit the deadline (not because
			// /api/v1/health became unreachable), abort without a final extra probe —
			// otherwise GatewayClient's own HTTP timeout (5s default) could
			// extend total wait past timeoutMs (CV-LOW fix).
			if (daemonStillReachable) {
				logger.warn('daemon', 'Restart aborted — daemon did not stop within timeout.');
				return false;
			}

			// 3. Clean up our child handle if we owned it (daemon already exited).
			// Safe against concurrent stop() because restarting=true blocks both
			// start() and (via guard below) stop() re-entry during this window.
			if (this.child) {
				this.child.kill('SIGTERM'); // no-op if already dead; 'exit' handler resets state
			}

			// 4. Spawn a fresh daemon. Temporarily release the restarting mutex
			// so start() can acquire its own starting flag — otherwise start()
			// guards would reject the call as "restart in progress".
			// B-NEW-30: arm the skip-flag BEFORE start() so its health
			// fast-path is bypassed exactly once on this respawn.
			this.skipHealthFastPathOnce = true;
			this.restarting = false;
			const result = await this.start();
			if (result) {
				// Manual restart resets crash counter and re-arms supervisor.
				this.crashTimestamps = [];
				this.supervisorAborted = false;
			}
			return result;
		} finally {
			this.restarting = false;
		}
	}

	/** Whether a child process is actively running (false while stopping). */
	get running(): boolean {
		return this.child !== undefined && !this.stopping;
	}

	/**
	 * Called from the child 'exit' handler to decide whether the supervisor
	 * should respawn. Implements crash-loop detection with exponential backoff.
	 */
	private handleExit(code: number | null, signal: string | null, aliveMs: number): void {
		if (this.disposed || !this.autoRestartOnCrash || this.supervisorAborted) { return; }
		if (this.expectedExit) { this.expectedExit = false; return; }
		if (this.restarting) { return; } // restart() will spawn its own

		// exit(0) with no signal — daemon decided to quit cleanly (e.g. external mcp-ctl shutdown)
		const wasGracefulZero = code === 0 && signal === null;
		if (wasGracefulZero) { return; }

		// Reset crash counter if the previous run was stable long enough.
		if (aliveMs >= this.cfg.stableThresholdMs) {
			this.crashTimestamps = [];
		}

		const now = Date.now();
		this.crashTimestamps.push(now);
		this.crashTimestamps = this.crashTimestamps.filter(t => now - t < this.cfg.windowMs);

		if (this.crashTimestamps.length > this.cfg.maxAttemptsInWindow) {
			this.supervisorAborted = true;
			logger.error('daemon', `Crash loop detected (${this.crashTimestamps.length} crashes in ${Math.round(this.cfg.windowMs / 60_000)}min). Auto-restart disabled.`);
			this.fileLogger?.writeEvent('supervisor: aborted (crash loop)');
			void vscode.window.showErrorMessage(
				'MCP Gateway: daemon is crash-looping — auto-restart disabled. Inspect daemon logs and run `MCP Gateway: Restart Daemon` manually.',
				'Show Output',
			).then(pick => {
				if (this.disposed) { return; }
				if (pick === 'Show Output') { void vscode.commands.executeCommand('mcpGateway.showOutput'); }
			});
			return;
		}

		this.scheduleRestart();
	}

	/** Schedule the next restart attempt using exponential backoff with jitter. */
	private scheduleRestart(): void {
		// windowCrashIdx is the count of crashes within `windowMs` (not lifetime).
		// Backoff "steps down" when older crashes age out — intentional.
		// REVIEW MEDIUM-1: use window-relative count in log message (not lifetime) so
		// "crashes in last Xmin" accurately reflects the rolling window, not total crashes.
		const windowCrashIdx = this.crashTimestamps.length - 1; // 0-based
		const baseDelay = Math.min(this.cfg.initialDelayMs * (2 ** windowCrashIdx), this.cfg.maxDelayMs);
		const jitter = baseDelay * this.cfg.jitterRatio * (this._random() * 2 - 1);
		const delay = Math.max(0, Math.round(baseDelay + jitter));
		logger.info('daemon', `Auto-restart in ${delay}ms (crashes in last ${Math.round(this.cfg.windowMs / 60_000)}min: ${windowCrashIdx + 1}/${this.cfg.maxAttemptsInWindow})`);
		this.fileLogger?.writeEvent(`supervisor: scheduling restart in ${delay}ms (window-crash ${windowCrashIdx + 1}/${this.cfg.maxAttemptsInWindow})`);
		this.restartTimer = this._setTimeout(() => {
			this.restartTimer = undefined;
			if (this.disposed) { return; }
			// After a known crash the child is dead — the getHealth fast-path
			// and the 2.5s "concurrent-window race" wait inside start() are
			// both meaningless and just delay the respawn (or in tests with a
			// permissive mock client, skip the spawn entirely). Mirror the
			// manual restart() path at line 395 by arming the flag here.
			// Closes the "second spawn after backoff" gap in A1/A10..A14/A16.
			this.skipHealthFastPathOnce = true;
			void this.start().catch(err => logger.error('daemon', 'Auto-restart spawn failed', err));
		}, delay);
	}

	/** Cancel any pending supervisor restart timer. */
	private cancelPendingRestart(): void {
		if (this.restartTimer) {
			this._clearTimeout(this.restartTimer);
			this.restartTimer = undefined;
		}
	}

	/**
	 * Dispose: best-effort graceful shutdown of an OWNED child via the
	 * REST-first flow. VSCode treats sync and async dispose()
	 * interchangeably so returning a Promise is safe. Errors from the
	 * shutdown chain are swallowed — dispose must not throw.
	 *
	 * AUDIT B-NEW-25: previously called `this.stop()` synchronously, which
	 * went straight to SIGTERM = TerminateProcess on Windows and stranded
	 * the daemon's pidfile. Now we await the same graceful path.
	 *
	 * Scope: dispose() ONLY shuts down a child this manager spawned. If we
	 * don't own the child, dispose is a no-op — the extension being
	 * deactivated is not authority to terminate an externally-started
	 * daemon (that decision belongs to the operator via mcpGateway.stopDaemon).
	 */
	async dispose(): Promise<void> {
		if (this.disposed) { return; }
		this.cancelPendingRestart();
		this.disposed = true;
		this.expectedExit = true;
		const ownedChild = this.child;
		if (ownedChild) {
			ownedChild.stdout?.removeAllListeners();
			ownedChild.stderr?.removeAllListeners();
			ownedChild.removeAllListeners();
			try {
				// Shorter timeout than stop() — extension-deactivate path
				// must return briskly to VS Code.
				await this.stop(2_000);
			} catch (err) {
				// dispose() must not throw. Log and move on.
				logger.warn('daemon', 'dispose: graceful stop failed', err);
			}
			// REVIEW HIGH-1: stop() may fall back to SIGTERM with listeners stripped,
			// leaving child set; clear unconditionally so this.running stays false post-dispose.
			// dispose() is one-shot; disposed=true gates all further use.
			this.child = undefined;
		}
	}
}
