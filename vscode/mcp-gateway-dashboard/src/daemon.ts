import { spawn as nodeSpawn, type ChildProcess, type SpawnOptions } from 'node:child_process';
import * as vscode from 'vscode';
import type { IGatewayClient } from './extension';

/** Spawn function signature — matches child_process.spawn subset used here. */
export type SpawnFn = (cmd: string, args: string[], opts: SpawnOptions) => ChildProcess;

/**
 * Manages the mcp-gateway daemon process lifecycle.
 * Spawns the daemon if not already running, captures output to an OutputChannel.
 */
export class DaemonManager {
	private child: ChildProcess | undefined;
	private readonly output: vscode.OutputChannel;
	private readonly spawnFn: SpawnFn;
	private readonly ownsOutput: boolean;
	private disposed = false;
	private starting = false;
	private stopping = false;

	constructor(
		private readonly client: IGatewayClient,
		private readonly daemonPath: string,
		outputChannel?: vscode.OutputChannel,
		spawnFn?: SpawnFn,
	) {
		this.ownsOutput = !outputChannel;
		this.output = outputChannel ?? vscode.window.createOutputChannel('MCP Gateway');
		this.spawnFn = spawnFn ?? nodeSpawn;
	}

	/** Start the daemon if it is not already running. Returns true if spawned. */
	async start(): Promise<boolean> {
		if (this.disposed || this.child || this.starting) { return false; }
		this.starting = true;

		try {
			// Check if gateway is already reachable — no need to spawn.
			try {
				await this.client.getHealth();
				this.output.appendLine('[daemon] Gateway already running — skipping spawn.');
				return false;
			} catch {
				// Gateway offline — proceed to spawn.
			}

			if (this.disposed) { return false; }

			const cmd = this.daemonPath || 'mcp-gateway';
			this.output.appendLine(`[daemon] Starting: ${cmd}`);

			try {
				this.child = this.spawnFn(cmd, [], {
					stdio: ['ignore', 'pipe', 'pipe'],
					detached: false,
					windowsHide: true,
				});
			} catch (err) {
				this.output.appendLine(`[daemon] Failed to spawn: ${err instanceof Error ? err.message : String(err)}`);
				return false;
			}

			this.child.stdout?.on('data', (chunk: Buffer) => {
				if (!this.disposed) { this.output.appendLine(chunk.toString().trimEnd()); }
			});

			this.child.stderr?.on('data', (chunk: Buffer) => {
				if (!this.disposed) { this.output.appendLine(`[stderr] ${chunk.toString().trimEnd()}`); }
			});

			this.child.on('error', (err) => {
				if (!this.disposed) { this.output.appendLine(`[daemon] Process error: ${err.message}`); }
				this.child = undefined;
				this.stopping = false;
			});

			this.child.on('exit', (code, signal) => {
				const reason = signal ? `signal ${signal}` : `code ${code ?? 'unknown'}`;
				if (!this.disposed) { this.output.appendLine(`[daemon] Exited (${reason})`); }
				this.child = undefined;
				this.stopping = false;
			});

			return true;
		} finally {
			this.starting = false;
		}
	}

	/** Stop the daemon by sending SIGTERM. */
	stop(): void {
		if (!this.child || this.stopping) { return; }
		this.stopping = true;
		if (!this.disposed) { this.output.appendLine('[daemon] Stopping...'); }
		this.child.kill('SIGTERM');
		// child = undefined and stopping = false will be set by the 'exit' handler.
	}

	/** Whether a child process is actively running (false while stopping). */
	get running(): boolean {
		return this.child !== undefined && !this.stopping;
	}

	dispose(): void {
		if (this.disposed) { return; }
		this.disposed = true;
		if (this.child) {
			this.child.stdout?.removeAllListeners();
			this.child.stderr?.removeAllListeners();
			this.child.removeAllListeners();
		}
		this.stop();
		if (this.ownsOutput) { this.output.dispose(); }
	}
}
