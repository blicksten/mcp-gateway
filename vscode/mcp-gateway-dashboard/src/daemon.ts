import { spawn as nodeSpawn, type ChildProcess, type SpawnOptions } from 'node:child_process';
import type * as vscode from 'vscode';
import type { IGatewayClient } from './extension';
import { logger } from './logger';

/** Spawn function signature — matches child_process.spawn subset used here. */
export type SpawnFn = (cmd: string, args: string[], opts: SpawnOptions) => ChildProcess;

/**
 * Manages the mcp-gateway daemon process lifecycle.
 * Spawns the daemon if not already running, writes to the shared logger channel.
 */
export class DaemonManager {
	private child: ChildProcess | undefined;
	private readonly spawnFn: SpawnFn;
	private disposed = false;
	private starting = false;
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

	constructor(
		private readonly client: IGatewayClient,
		private readonly daemonPath: string,
		// outputChannel is no longer used — all output goes through the shared
		// logger module. Kept in the signature only for backward-compat with
		// existing tests; ignored at runtime.
		_outputChannel?: vscode.OutputChannel,
		spawnFn?: SpawnFn,
	) {
		this.spawnFn = spawnFn ?? nodeSpawn;
	}

	/** Start the daemon if it is not already running. Returns true if spawned. */
	async start(): Promise<boolean> {
		if (this.disposed || this.child || this.starting || this.restarting) { return false; }
		this.starting = true;

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
				} catch {
					// Gateway offline — proceed to spawn.
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

			this.child.stdout?.on('data', (chunk: Buffer) => {
				if (!this.disposed) { logger.info('daemon', chunk.toString().trimEnd()); }
			});

			this.child.stderr?.on('data', (chunk: Buffer) => {
				if (!this.disposed) { logger.warn('daemon', `[stderr] ${chunk.toString().trimEnd()}`); }
			});

			this.child.on('error', (err) => {
				if (!this.disposed) { logger.error('daemon', 'Process error', err); }
				this.child = undefined;
				this.stopping = false;
			});

			this.child.on('exit', (code, signal) => {
				const reason = signal ? `signal ${signal}` : `code ${code ?? 'unknown'}`;
				if (!this.disposed) { logger.info('daemon', `Exited (${reason})`); }
				this.child = undefined;
				this.stopping = false;
			});

			return true;
		} finally {
			this.starting = false;
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
		// AUDIT A-M1: reject during restart to avoid racing with the
		// restart() kill+spawn sequence. Also re-entry guard for stop().
		if (this.stopping || this.restarting) { return; }
		// Nothing to stop and no daemon reachable to ask politely — bail out.
		if (!this.child) {
			const reachable = await this.client.getHealth().then(() => true, () => false);
			if (!reachable) { return; }
		}
		this.stopping = true;
		if (!this.disposed) { logger.info('daemon', 'Stopping...'); }

		// 1. Graceful REST shutdown — works for external daemons too.
		// On success: poll until /health unreachable so the daemon's signal
		// handler runs to completion (defer pidfile.Remove, etc.). On
		// failure: fall through to SIGTERM if we own a child.
		let restShutdownAccepted = false;
		try {
			await this.client.shutdown();
			restShutdownAccepted = true;
		} catch (err) {
			// REST failed (network down, 401, 5xx). Log; SIGTERM fallback
			// may still close out our owned child below.
			logger.warn('daemon', 'REST shutdown failed — will fall back to SIGTERM if local child is owned.', err);
		}

		// 2. If REST accepted, poll /health until unreachable bounded by timeoutMs.
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
				await new Promise((resolve) => setTimeout(resolve, 200));
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
	 *   2. Poll /health until unreachable (daemon actually exited)
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
	 * spawn() step is fire-and-forget-detect — start() polls /health via
	 * its own fast-path check.
	 */
	async restart(timeoutMs = 10_000): Promise<boolean> {
		// AUDIT A-H1/A-M1: hard mutex with start()/stop(). If a restart is
		// already in flight OR the manager is disposed, refuse re-entry.
		if (this.disposed || this.restarting) { return false; }
		this.restarting = true;
		try {
			logger.info('daemon', 'Restarting...');

			// 1. Graceful REST shutdown — works for external daemons.
			try {
				await this.client.shutdown();
			} catch (err) {
				// Daemon may be unreachable, auth may have failed, or endpoint
				// may not exist on older daemons. Log the reason so operators
				// have a diagnostic trail (CV-LOW fix), then proceed to poll —
				// if /health is reachable we'll still time out and bail out.
				logger.warn('daemon', 'REST shutdown failed — proceeding to poll /health anyway.', err);
			}

			// 2. Poll /health until unreachable.
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
				await new Promise((resolve) => setTimeout(resolve, 200));
			}
			// If the poll loop exited because we hit the deadline (not because
			// /health became unreachable), abort without a final extra probe —
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
			return await this.start();
		} finally {
			this.restarting = false;
		}
	}

	/** Whether a child process is actively running (false while stopping). */
	get running(): boolean {
		return this.child !== undefined && !this.stopping;
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
		this.disposed = true;
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
		}
	}
}
