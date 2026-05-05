// Claude Code Integration webview panel (Phase 16.5 T16.5.1).
//
// Displays plugin + patch + channel status, drives the Activate/Probe/
// Copy-diagnostics flows. The failure-mode state machine lives in
// `../claude-code/status.ts` so the HTML layer is thin.
//
// Security: CSP nonce on every inline script, no remote resources, no
// eval, HTML-escape every user-facing field.

import * as vscode from 'vscode';
import * as crypto from 'node:crypto';
import * as readline from 'node:readline';
import { execFile, type ChildProcess } from 'node:child_process';

import { buildDiagnosticsReport, type DiagnosticsInput } from '../claude-code/diagnostics';
import {
	applyConfigOverride,
	computeSessionStatus,
	ingestHeartbeat,
	type ExternalFacts,
} from '../claude-code/status';
import type {
	CompatMatrix,
	FailureMode,
	Heartbeat,
	SessionStatus,
	SessionTrack,
	StatusColor,
} from '../claude-code/types';
import { runPatchInstaller } from '../claude-code/patch-installer';
import {
	detectPluginInstalled,
	detectPatchInstalled,
	detectChannelStatus,
} from '../claude-code/detection';
import { escapeHtml } from './html-builder';

/**
 * Minimal child-process handle consumed by the Activate flow. Exposes only
 * the fields the panel needs (streaming stdout/stderr, close/error events,
 * kill()). Tests provide a fake implementation — the production path uses
 * node:child_process.ChildProcess, which satisfies this interface.
 */
export interface InstallChildHandle {
	stdout: NodeJS.ReadableStream | null;
	stderr: NodeJS.ReadableStream | null;
	kill(signal?: NodeJS.Signals | number): boolean;
	/**
	 * `'close'` listener receives (code, signal). When the child is killed
	 * by a signal (SIGKILL, OS-level termination, etc.) `code` is `null`
	 * and `signal` carries the signal name — we use that to distinguish
	 * "external kill we didn't ask for" from "user-initiated cancel" and
	 * show an accurate error toast instead of silently swallowing the
	 * outcome.
	 */
	on(event: 'close', listener: (code: number | null, signal: NodeJS.Signals | null) => void): void;
	on(event: 'error', listener: (err: Error) => void): void;
}

export interface ClaudeCodePanelDeps {
	extensionUri: vscode.Uri;
	extensionPath: string;
	/** Resolves the gateway URL (honours the live `mcpGateway.apiUrl` setting). */
	getGatewayUrl(): string;
	/** Returns the Bearer token or undefined. */
	getAuthToken(): string | undefined;
	/**
	 * Returns the filesystem path to the auth-token file (B-NEW-31).
	 * Used by runPatchInstaller for the auto-reload toggle flow.
	 */
	getTokenPath(): string;
	/** Factory returning an HTTP client. Injected for tests. */
	fetch: typeof fetch;
	/** Phase 4B — resolves the mcp-ctl executable path (empty = look up on PATH). */
	getMcpCtlPath(): string;
	/**
	 * Phase 4B — spawn factory for `mcp-ctl install-claude-code`. Omit in
	 * production to use the default execFile-based implementation; tests
	 * inject a fake that returns a controllable child process handle.
	 *
	 * Phase 5 (B-12) — third argument carries CWD + env overrides so that
	 * Activate works from any VSCode workspace (not just the mcp-gateway repo).
	 */
	spawnInstall?: (
		mcpCtlPath: string,
		argv: string[],
		opts?: { cwd?: string; envOverrides?: Record<string, string> },
	) => InstallChildHandle;
	/**
	 * Phase 1 — override for detectPluginInstalled. Tests inject a fake that
	 * returns a controlled PluginDetection without spawning a real child.
	 * Omit in production to use the default detection helper.
	 */
	detectPlugin?: () => Promise<import('../claude-code/detection').PluginDetection>;
	/**
	 * Phase 1 — override for detectPatchInstalled. Tests inject a fake that
	 * returns a controlled PatchDetection without touching the filesystem.
	 * Omit in production to use the default detection helper.
	 */
	detectPatch?: () => Promise<import('../claude-code/detection').PatchDetection>;
	/**
	 * Phase 4 — returns the running daemon's version string from the cached
	 * /api/v1/health response (closes B-10). Tests inject a fake returning a
	 * controlled version string. When undefined or not injected, Copy Diagnostics
	 * falls back to the literal 'unknown'.
	 */
	getGatewayVersion?: () => string | undefined;
	/**
	 * Phase 5 (B-12) — returns the first workspace folder path so that
	 * `mcp-ctl install-claude-code` can walk ancestors from the correct
	 * location to find marketplace.json. Omit in tests that do not exercise
	 * CWD behaviour; production wires vscode.workspace.workspaceFolders.
	 */
	getWorkspaceFolder?: () => string | undefined;
	/**
	 * Phase 5 (B-12) — optional override for the marketplace.json path.
	 * When non-empty, passed as GATEWAY_MARKETPLACE_JSON env var to mcp-ctl
	 * so that the ancestor walk is bypassed entirely. Omit in tests that do
	 * not exercise env-override behaviour; production reads
	 * mcpGateway.marketplaceJsonPath from VSCode configuration.
	 */
	getMarketplaceJsonPath?: () => string | undefined;
}

/**
 * Redaction regex for Bearer tokens surfaced on the install log stream.
 * Defence-in-depth — mcp-ctl should never print its Bearer token, but if
 * a future subcommand does (e.g. verbose auth trace) this strips it
 * before the line reaches the webview.
 */
const BEARER_REDACT = /bearer\s+[A-Za-z0-9+/=_-]{16,}/gi;

interface ClaudeCodeState {
	tracks: Map<string, SessionTrack>;
	lastFacts?: ExternalFacts;
	recentHeartbeats: Map<string, Heartbeat[]>; // per session_id, newest-first, capped at 5
	compatMatrix: CompatMatrix | null;
	/** Mode banner shown at the top of the panel — derived from the worst mode across sessions. */
	lastStatuses: SessionStatus[];
	lastFailureTrace?: string;
}

/**
 * Top-level panel orchestrator. Handles polling, user actions, and the
 * postMessage bridge to the webview HTML.
 */
export class ClaudeCodePanel {
	private static current: ClaudeCodePanel | undefined;

	private readonly panel: vscode.WebviewPanel;
	private readonly deps: ClaudeCodePanelDeps;
	private readonly disposables: vscode.Disposable[] = [];
	private pollTimer: NodeJS.Timeout | undefined;
	private disposed = false;
	private state: ClaudeCodeState = {
		tracks: new Map(),
		recentHeartbeats: new Map(),
		compatMatrix: null,
		lastStatuses: [],
	};
	// Phase 4B — Activate install state.
	private installInProgress = false;
	private activeInstallChild: InstallChildHandle | null = null;
	/**
	 * Phase 4B — set to true when we intentionally kill the child
	 * (cancelInstall or dispose). The subsequent SIGTERM-induced close
	 * event reads this flag so finish() does not surface a misleading
	 * "install failed" error toast for user-initiated termination.
	 */
	private installCanceled = false;

	private constructor(panel: vscode.WebviewPanel, deps: ClaudeCodePanelDeps) {
		this.panel = panel;
		this.deps = deps;
		this.disposables.push(this.panel.onDidDispose(() => this.dispose()));
		this.disposables.push(
			this.panel.webview.onDidReceiveMessage((msg: unknown) => {
				void this.handleMessage(msg);
			}),
		);
	}

	static createOrShow(deps: ClaudeCodePanelDeps): ClaudeCodePanel {
		if (ClaudeCodePanel.current && !ClaudeCodePanel.current.disposed) {
			ClaudeCodePanel.current.panel.reveal();
			return ClaudeCodePanel.current;
		}
		const panel = vscode.window.createWebviewPanel(
			'mcpClaudeCode',
			'Claude Code Integration',
			vscode.ViewColumn.One,
			{
				enableScripts: true,
				localResourceRoots: [deps.extensionUri],
				retainContextWhenHidden: true,
			},
		);
		const instance = new ClaudeCodePanel(panel, deps);
		ClaudeCodePanel.current = instance;
		instance.render();
		instance.startPolling();
		return instance;
	}

	private render(): void {
		this.panel.webview.html = buildPanelHtml(crypto.randomBytes(16).toString('hex'), this.panel.webview.cspSource);
	}

	/** Starts the 10s status poller. T16.5.4. */
	private startPolling(): void {
		void this.poll(); // immediate first fetch
		this.pollTimer = setInterval(() => {
			void this.poll();
		}, 10_000);
	}

	/** One poll cycle: GET /patch-status + GET /compat-matrix, ingest, recompute status. */
	private async poll(): Promise<void> {
		if (this.disposed) {
			return;
		}
		const url = this.deps.getGatewayUrl();
		const token = this.deps.getAuthToken();
		const headers: Record<string, string> = {};
		if (token) {
			headers['Authorization'] = `Bearer ${token}`;
		}
		try {
			const [statusResp, matrixResp] = await Promise.all([
				this.deps.fetch(`${url}/api/v1/claude-code/patch-status`, { headers }),
				this.deps.fetch(`${url}/api/v1/claude-code/compat-matrix`, { headers }),
			]);

			if (statusResp.ok) {
				const heartbeats: Heartbeat[] = (await statusResp.json()) as Heartbeat[];
				for (const hb of heartbeats) {
					const track = this.state.tracks.get(hb.session_id) ?? {
						session_id: hb.session_id,
						consecutiveReconnectErrors: 0,
						consecutiveNonReadyHighRetry: 0,
					};
					this.state.tracks.set(hb.session_id, ingestHeartbeat(track, hb));
					const recent = this.state.recentHeartbeats.get(hb.session_id) ?? [];
					recent.unshift(hb);
					this.state.recentHeartbeats.set(hb.session_id, recent.slice(0, 5));
				}
			}
			if (matrixResp.ok) {
				this.state.compatMatrix = (await matrixResp.json()) as CompatMatrix;
			} else if (matrixResp.status === 503) {
				this.state.compatMatrix = null;
			}

			const gathered = await this.gatherFacts();
			const { facts, plugin, patch } = gathered;
			this.state.lastFacts = facts;

			const statuses: SessionStatus[] = [];
			for (const [sid, track] of this.state.tracks) {
				statuses.push(
					computeSessionStatus(track, facts, this.state.recentHeartbeats.get(sid) ?? []),
				);
			}
			this.state.lastStatuses = statuses;

			void this.panel.webview.postMessage({
				type: 'status',
				statuses,
				compatMatrix: this.state.compatMatrix,
			});

			this.postFactsUpdate(facts, plugin, patch);
			this.postBannerUpdate(true, facts.pluginInstalled, this.state.tracks.size);
		} catch (err: unknown) {
			// Gateway unreachable: post a synthetic H status so the UI reflects it.
			const e = err instanceof Error ? err : new Error(String(err));
			void this.panel.webview.postMessage({
				type: 'status',
				statuses: [
					{
						session_id: '(no session)',
						color: 'red' as StatusColor,
						mode: 'H' as FailureMode,
						banner: 'mcp-gateway daemon not running',
						action: `Error: ${e.message}`,
						recentHeartbeats: [],
					},
				],
				compatMatrix: null,
			});
			this.postBannerUpdate(false, false, 0);
		}
	}

	/**
	 * Gathers non-heartbeat facts using real detection helpers.
	 * Phase 1: detectPluginInstalled (spawn `claude plugin list --json`) and
	 * detectPatchInstalled (FS glob + marker check) replace the hardcoded stubs.
	 *
	 * Returns both the ExternalFacts and the raw detection results so that
	 * poll() can post a `facts-updated` message with version details.
	 */
	private async gatherFacts(): Promise<{
		facts: ExternalFacts;
		plugin: import('../claude-code/detection').PluginDetection;
		patch: import('../claude-code/detection').PatchDetection;
	}> {
		const ccVersion =
			Array.from(this.state.tracks.values())[0]?.lastHeartbeat?.cc_version ?? '';
		const altE =
			this.state.compatMatrix?.alt_e_verified_versions ?? [];

		const ctlPath = this.deps.getMcpCtlPath().trim() || undefined;
		const detectPluginFn = this.deps.detectPlugin ?? (() => detectPluginInstalled({ ctlPath }));
		const detectPatchFn = this.deps.detectPatch ?? (() => detectPatchInstalled());
		const [plugin, patch] = await Promise.all([
			detectPluginFn(),
			detectPatchFn(),
		]);

		const facts: ExternalFacts = {
			patchInstalled: patch.installed,
			patchStale: patch.stale === true,
			pluginInstalled: plugin.installed,
			gatewayReachable: true,
			tokenRotationDriftMs: null,
			ccVersion,
			altEVerifiedVersions: altE,
			maxAltEVersion: altE[altE.length - 1] ?? '',
			corsReachable: null,
			anyRecentHeartbeat: this.state.tracks.size > 0,
		};

		return { facts, plugin, patch };
	}

	/**
	 * Posts a `facts-updated` message to the webview with current plugin,
	 * patch, and channel status. Called from the poll() success path.
	 */
	private postFactsUpdate(
		facts: ExternalFacts,
		plugin: import('../claude-code/detection').PluginDetection,
		patch: import('../claude-code/detection').PatchDetection,
	): void {
		const channel = detectChannelStatus();
		void this.panel.webview.postMessage({
			kind: 'facts-updated',
			pluginInstalled: facts.pluginInstalled,
			pluginVersion: plugin.version,
			patchInstalled: facts.patchInstalled,
			patchStale: facts.patchStale,
			patchCurrentVersion: patch.currentVersion,
			patchLatestVersion: patch.latestVersion,
			channelState: channel.state,
			channelDetail: channel.detail,
		});
	}

	/**
	 * Posts a `banner-updated` message. Produces actionable copy based on
	 * gateway reachability and plugin installation state.
	 */
	private postBannerUpdate(gatewayReachable: boolean, pluginInstalled: boolean, sessionCount: number): void {
		if (!gatewayReachable) {
			void this.panel.webview.postMessage({
				kind: 'banner-updated',
				tone: 'red',
				text: 'mcp-gateway daemon not running',
			});
			return;
		}
		if (sessionCount === 0) {
			if (pluginInstalled) {
				void this.panel.webview.postMessage({
					kind: 'banner-updated',
					tone: 'yellow',
					text: '⏸ No Claude Code sessions reporting yet — restart Claude Code to pick up the plugin',
				});
			} else {
				void this.panel.webview.postMessage({
					kind: 'banner-updated',
					tone: 'yellow',
					text: '⏸ Plugin not installed — click Activate for Claude Code below',
				});
			}
		}
		// When sessionCount >= 1, the existing renderStatus logic in the webview handles the banner.
	}

	private async handleMessage(msg: unknown): Promise<void> {
		if (typeof msg !== 'object' || msg === null) {
			return;
		}
		const m = msg as { command?: string };
		switch (m.command) {
			case 'activate':
				await this.runInstallClaudeCode();
				break;
			case 'activateCancel':
				this.cancelInstall();
				break;
			case 'toggleAutoReload':
				await this.handleToggleAutoReload((msg as { enabled: boolean }).enabled);
				break;
			case 'probeReconnect':
				await this.handleProbeReconnect();
				break;
			case 'copyDiagnostics':
				await this.handleCopyDiagnostics();
				break;
		}
	}

	/**
	 * Phase 4B — [Activate for Claude Code] flow. Spawns `mcp-ctl
	 * install-claude-code` as a child process, streams stdout + stderr
	 * line-by-line to the webview via postMessage, and posts a final
	 * `activate-done` when the child exits.
	 *
	 * Security:
	 *   - execFile + argv array — no shell expansion, no command injection.
	 *   - windowsHide: true — no console popup on Windows.
	 *   - Bearer redaction on every line before postMessage (defence in
	 *     depth; see BEARER_REDACT).
	 *   - Webview appends via textContent (XSS-safe) + existing CSP nonce.
	 *   - Concurrent-click guard: second Activate click while a spawn is
	 *     running is refused, never spawns a second child.
	 *   - Dispose during run kills the child — see dispose().
	 */
	/**
	 * Phase 5 (B-12) — builds the optional CWD + env-override bag for the
	 * Activate spawn. Called by runInstallClaudeCode.
	 *
	 * - `cwd`: first workspace folder path (undefined when no workspace is open
	 *   or when getWorkspaceFolder is not wired — mcp-ctl falls back to
	 *   process.cwd(), which was the previous behaviour).
	 * - `envOverrides.GATEWAY_MARKETPLACE_JSON`: path from the operator setting
	 *   (undefined / empty → key omitted so mcp-ctl's ancestor walk is used).
	 */
	private getActivateSpawnOptions(): { cwd?: string; envOverrides?: Record<string, string> } {
		const cwd = this.deps.getWorkspaceFolder?.() || undefined;
		const marketplacePath = this.deps.getMarketplaceJsonPath?.() || undefined;
		const envOverrides: Record<string, string> | undefined = marketplacePath
			? { GATEWAY_MARKETPLACE_JSON: marketplacePath }
			: undefined;
		return { cwd, envOverrides };
	}

	private runInstallClaudeCode(): Promise<void> {
		if (this.installInProgress) {
			void vscode.window.showWarningMessage(
				'Install already in progress — wait for completion or press Cancel.',
			);
			return Promise.resolve();
		}
		const mcpCtlPath = this.deps.getMcpCtlPath().trim() || 'mcp-ctl';
		this.installInProgress = true;
		this.installCanceled = false; // reset from prior run, if any
		const spawnFn = this.deps.spawnInstall ?? defaultSpawnInstall;
		const spawnOpts = this.getActivateSpawnOptions();

		return new Promise<void>((resolve) => {
			let child: InstallChildHandle;
			try {
				child = spawnFn(mcpCtlPath, ['install-claude-code', '--api-url', this.deps.getGatewayUrl()], spawnOpts);
			} catch (err: unknown) {
				const msg = err instanceof Error ? err.message : String(err);
				this.installInProgress = false;
				void this.panel.webview.postMessage({ kind: 'activate-error', message: msg });
				void vscode.window.showErrorMessage(`Activate failed: ${msg}`);
				resolve();
				return;
			}
			this.activeInstallChild = child;
			void this.panel.webview.postMessage({ kind: 'activate-start' });

			const rlOut = child.stdout
				? readline.createInterface({ input: child.stdout, crlfDelay: Infinity })
				: null;
			const rlErr = child.stderr
				? readline.createInterface({ input: child.stderr, crlfDelay: Infinity })
				: null;
			const postLine = (line: string): void => {
				const safe = line.replace(BEARER_REDACT, 'Bearer [REDACTED]');
				void this.panel.webview.postMessage({ kind: 'activate-log', line: safe });
			};
			rlOut?.on('line', postLine);
			rlErr?.on('line', postLine);

			let finished = false;
			const finish = (
				exitCode: number | null,
				err?: Error,
				signal: NodeJS.Signals | null = null,
			): void => {
				if (finished) { return; }
				finished = true;
				rlOut?.close();
				rlErr?.close();
				const wasCanceled = this.installCanceled;
				this.activeInstallChild = null;
				this.installInProgress = false;

				// Always notify the webview — postMessage on a disposed
				// panel silently resolves to false, which is harmless.
				if (err) {
					void this.panel.webview.postMessage({
						kind: 'activate-error',
						message: err.message,
					});
				} else {
					void this.panel.webview.postMessage({
						kind: 'activate-done',
						exitCode,
						signal,
						canceled: wasCanceled,
					});
				}

				// Skip user-facing toasts when the panel is being disposed
				// (user already closed it — any toast would be noise).
				if (this.disposed) {
					resolve();
					return;
				}

				if (err) {
					void vscode.window.showErrorMessage(`Activate failed: ${err.message}`);
				} else if (wasCanceled) {
					// User pressed Cancel (or panel dispose handler killed
					// the child). SIGTERM produces exit 143 on *nix and
					// various values on Windows — do not surface that as
					// an error.
					void vscode.window.showInformationMessage('Install cancelled.');
				} else if (exitCode === 0) {
					void vscode.window
						.showInformationMessage(
							'Claude Code plugin installed. Reload VSCode window to activate.',
							'Reload Window',
						)
						.then((selection) => {
							if (selection === 'Reload Window') {
								void vscode.commands.executeCommand('workbench.action.reloadWindow');
							}
						});
				} else if (exitCode === null) {
					// External kill we didn't ask for — OS OOM killer,
					// `kill -9 <pid>`, Windows Task Manager, etc.
					// exitCode=null means "process died via signal, not
					// via normal exit". Surface it so the install doesn't
					// silently vanish.
					const reason = signal ? `signal ${signal}` : 'an external termination';
					void vscode.window.showErrorMessage(
						`mcp-ctl install-claude-code was killed by ${reason}. See install log for details.`,
					);
				} else {
					void vscode.window.showErrorMessage(
						`mcp-ctl install-claude-code exited ${exitCode}. See install log for details.`,
					);
				}
				resolve();
			};
			child.on('error', (err) => finish(null, err));
			child.on('close', (code, signal) => finish(code, undefined, signal));
		});
	}

	/**
	 * Phase 4B — cancel an in-progress install. Sends SIGTERM; the spawned
	 * process handles cleanup itself. If no install is running, no-op.
	 */
	private cancelInstall(): void {
		const child = this.activeInstallChild;
		if (!child) { return; }
		// Mark BEFORE kill so the async 'close' handler sees the flag
		// and routes through the cancelled-not-failed path in finish().
		this.installCanceled = true;
		try {
			child.kill('SIGTERM');
		} catch {
			// best-effort — the close handler still runs
		}
	}

	/** T16.5.3 — Auto-reload checkbox handler. Spawns apply / uninstall script. */
	private async handleToggleAutoReload(enabled: boolean): Promise<void> {
		const url = this.deps.getGatewayUrl();
		const tokenPath = this.deps.getTokenPath();
		const result = await runPatchInstaller({
			extensionPath: this.deps.extensionPath,
			gatewayUrl: url,
			tokenPath,
			uninstall: !enabled,
		});
		if (result.ok) {
			void vscode.window
				.showInformationMessage(
					enabled
						? 'Patch installed. Reload VSCode window to activate.'
						: 'Patch uninstalled. Reload VSCode window to clear.',
					'Reload Window',
				)
				.then((selection) => {
					if (selection === 'Reload Window') {
						void vscode.commands.executeCommand('workbench.action.reloadWindow');
					}
				});
		} else {
			this.state.lastFailureTrace = `apply-mcp-gateway exited ${result.exitCode}\nstdout:\n${result.stdout}\nstderr:\n${result.stderr}`;
			void vscode.window.showErrorMessage(
				`Patch install failed (exit ${result.exitCode}). See [Copy diagnostics] for full output.`,
			);
		}
	}

	/** T16.5.6 — [Probe reconnect] handler (Alt-E). */
	private async handleProbeReconnect(): Promise<void> {
		const url = this.deps.getGatewayUrl();
		const token = this.deps.getAuthToken();
		const headers: Record<string, string> = { 'Content-Type': 'application/json' };
		if (token) {
			headers['Authorization'] = `Bearer ${token}`;
		}
		const nonce = crypto.randomBytes(16).toString('hex');
		try {
			const trigger = await this.deps.fetch(`${url}/api/v1/claude-code/probe-trigger`, {
				method: 'POST',
				headers,
				body: JSON.stringify({ nonce }),
			});
			if (!trigger.ok) {
				void vscode.window.showErrorMessage(`Probe trigger failed: HTTP ${trigger.status}`);
				return;
			}
			// B-11 fix: the FROZEN contract has no per-nonce GET endpoint; the probe
			// outcome arrives via the next heartbeat and is already reflected in the
			// session panel. Removed the legacy 15 s poll loop that blocked the UI
			// for nothing — the user is directed to the session panel instead.
			void vscode.window.showInformationMessage(
				`Probe sent (nonce ${nonce.slice(0, 8)}…). Watch the session panel for the result.`,
			);
		} catch (err: unknown) {
			const e = err instanceof Error ? err : new Error(String(err));
			void vscode.window.showErrorMessage(`Probe failed: ${e.message}`);
		}
	}

	/** T16.5.7 — [Copy diagnostics] generates structured report → clipboard. */
	private async handleCopyDiagnostics(): Promise<void> {
		// B-10 fix: use the real daemon version from cached /api/v1/health when
		// available; fall back to 'unknown' only when the hook is absent or returns
		// undefined (e.g. daemon offline or pre-D.1 daemon without version field).
		const gatewayVersion = this.deps.getGatewayVersion?.() ?? 'unknown';
		const input: DiagnosticsInput = {
			platform: process.platform,
			vscodeVersion: vscode.version,
			gatewayVersion,
			ccVersion: this.state.lastFacts?.ccVersion ?? '',
			pluginStatus: { installed: this.state.lastFacts?.pluginInstalled ?? false },
			patchStatus: { installed: this.state.lastFacts?.patchInstalled ?? false },
			compatMatrix: this.state.compatMatrix,
			sessions: this.state.lastStatuses,
			failureTrace: this.state.lastFailureTrace,
			reportUrl: 'https://github.com/tungstenautomation/mcp-gateway/issues/new',
		};
		const report = buildDiagnosticsReport(input);
		await vscode.env.clipboard.writeText(report);
		void vscode.window.showInformationMessage(
			`Diagnostics copied to clipboard (${report.length} chars).`,
		);
	}

	private dispose(): void {
		if (this.disposed) {
			return;
		}
		this.disposed = true;
		if (this.pollTimer) {
			clearInterval(this.pollTimer);
		}
		// Phase 4B — kill any in-flight install to avoid a zombie child
		// writing into a disposed webview. Mark as cancelled so the
		// async close handler does not surface an error toast after the
		// panel is gone (finish() also short-circuits on this.disposed,
		// but setting the flag keeps the state consistent for anyone
		// inspecting the final activate-done message).
		if (this.activeInstallChild) {
			this.installCanceled = true;
			try {
				this.activeInstallChild.kill('SIGTERM');
			} catch {
				// best-effort — panel is being torn down
			}
			this.activeInstallChild = null;
			this.installInProgress = false;
		}
		while (this.disposables.length > 0) {
			const d = this.disposables.pop();
			try {
				d?.dispose();
			} catch {
				// ignore errors during shutdown
			}
		}
		if (ClaudeCodePanel.current === this) {
			ClaudeCodePanel.current = undefined;
		}
	}

	/** Test-only hook to clear the singleton between test cases. */
	static _resetForTests(): void {
		ClaudeCodePanel.current = undefined;
	}
}

/**
 * Default production factory for spawning `mcp-ctl install-claude-code`.
 * Uses execFile (argv array, no shell) and windowsHide so no console
 * popup flashes on Windows. The returned ChildProcess satisfies
 * InstallChildHandle: stdout/stderr are piped Readable streams, and
 * close/error events + kill() match the interface.
 *
 * Phase 5 (B-12): accepts optional `opts.cwd` and `opts.envOverrides` so
 * that the spawn runs from the VSCode workspace folder (not the inherited
 * VSCode process CWD) and can receive a pre-resolved marketplace.json path
 * via GATEWAY_MARKETPLACE_JSON — making the Activate button work from any
 * workspace, not just a descendant of the mcp-gateway repo.
 */
function defaultSpawnInstall(
	mcpCtlPath: string,
	argv: string[],
	opts?: { cwd?: string; envOverrides?: Record<string, string> },
): InstallChildHandle {
	const child: ChildProcess = execFile(mcpCtlPath, argv, {
		windowsHide: true,
		maxBuffer: 10 * 1024 * 1024, // 10 MB — covers verbose install output
		cwd: opts?.cwd,
		env: opts?.envOverrides ? { ...process.env, ...opts.envOverrides } : process.env,
	});
	return child as InstallChildHandle;
}

/**
 * Renders the panel HTML with CSP nonce. The page binds to postMessage
 * events from the extension host: `{type:'status', statuses: SessionStatus[], compatMatrix}`.
 */
function buildPanelHtml(nonce: string, cspSource: string): string {
	// Escape nothing here — all dynamic content arrives via postMessage and
	// is rendered via textContent or escaped by the binding.
	const escapedNonce = escapeHtml(nonce);
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src ${cspSource} 'unsafe-inline'; script-src 'nonce-${escapedNonce}'; img-src ${cspSource} data:;">
<title>Claude Code Integration</title>
<style>
body { font-family: var(--vscode-font-family); padding: 16px; color: var(--vscode-foreground); }
h1 { font-size: 1.25em; margin: 0 0 12px 0; }
.banner { padding: 10px 14px; border-radius: 4px; margin: 12px 0; font-weight: 500; }
.banner.green  { background: #1f4d2b; color: #d4f7dc; }
.banner.yellow { background: #5a4a1b; color: #f7e9c3; }
.banner.red    { background: #5a1b1b; color: #f7c3c3; }
.section { margin: 16px 0; }
.row { display: flex; align-items: center; gap: 8px; margin: 6px 0; }
.row label { min-width: 90px; color: var(--vscode-descriptionForeground); }
button { padding: 6px 12px; cursor: pointer; background: var(--vscode-button-background); color: var(--vscode-button-foreground); border: 1px solid var(--vscode-button-border, transparent); border-radius: 2px; }
button:hover { background: var(--vscode-button-hoverBackground); }
.session { border-left: 3px solid var(--vscode-focusBorder); padding-left: 12px; margin-top: 12px; }
.mono { font-family: var(--vscode-editor-font-family); font-size: 12px; color: var(--vscode-descriptionForeground); }
#activate-progress { margin-top: 10px; }
#activate-progress .row { margin-bottom: 6px; }
#activate-log {
	font-family: var(--vscode-editor-font-family);
	font-size: 12px;
	background: var(--vscode-editor-background);
	color: var(--vscode-editor-foreground);
	border: 1px solid var(--vscode-panel-border, #333);
	border-radius: 2px;
	padding: 8px;
	max-height: 260px;
	overflow-y: auto;
	white-space: pre-wrap;
	margin: 0;
}
</style>
</head>
<body>
<h1>Claude Code Integration</h1>

<div class="section">
  <button id="activate">Activate for Claude Code</button>
  <span id="pluginStatus" class="mono">● Checking…</span>
</div>

<div id="activate-progress" class="section" hidden>
  <div class="row">
    <label>Install log:</label>
    <button id="activate-cancel">Cancel</button>
  </div>
  <pre id="activate-log"></pre>
</div>

<hr>

<div class="section">
  <div class="row">
    <input type="checkbox" id="autoReload">
    <label for="autoReload" style="min-width:auto; color:inherit;">Auto-reload plugins</label>
  </div>
  <div id="autoReloadRows">
    <div class="row"><label>Patch:</label><span id="patchStatus" class="mono">—</span></div>
    <div class="row"><label>Channel:</label><span id="channelStatus" class="mono">—</span></div>
  </div>
</div>

<div id="banner" class="banner yellow">Checking gateway…</div>

<div class="section">
  <button id="probe">Probe reconnect</button>
  <button id="copyDiag">Copy diagnostics</button>
</div>

<div id="sessions"></div>

<script nonce="${escapedNonce}">
(function() {
  const vscode = acquireVsCodeApi();
  const $ = (id) => document.getElementById(id);

  $('activate').addEventListener('click', () => vscode.postMessage({ command: 'activate' }));
  $('activate-cancel').addEventListener('click', () => vscode.postMessage({ command: 'activateCancel' }));
  $('autoReload').addEventListener('change', (e) =>
    vscode.postMessage({ command: 'toggleAutoReload', enabled: e.target.checked }));
  $('probe').addEventListener('click', () => vscode.postMessage({ command: 'probeReconnect' }));
  $('copyDiag').addEventListener('click', () => vscode.postMessage({ command: 'copyDiagnostics' }));

  function renderActivate(msg) {
    const progress = $('activate-progress');
    const logEl = $('activate-log');
    const activateBtn = $('activate');
    if (msg.kind === 'activate-start') {
      progress.hidden = false;
      logEl.textContent = '';
      activateBtn.disabled = true;
      return;
    }
    if (msg.kind === 'activate-log') {
      // textContent — XSS-safe append. Truncate at 1 MB to cap memory.
      // (Newlines must be double-escaped because this script lives inside
      // an outer template literal in TS source.)
      const next = logEl.textContent + String(msg.line ?? '') + '\\n';
      logEl.textContent = next.length > 1048576 ? next.slice(next.length - 1048576) : next;
      logEl.scrollTop = logEl.scrollHeight;
      return;
    }
    if (msg.kind === 'activate-done') {
      activateBtn.disabled = false;
      logEl.textContent += '\\n[exit ' + (msg.exitCode ?? 'null') + ']\\n';
      logEl.scrollTop = logEl.scrollHeight;
      return;
    }
    if (msg.kind === 'activate-error') {
      activateBtn.disabled = false;
      logEl.textContent += '\\n[error: ' + String(msg.message ?? '') + ']\\n';
      logEl.scrollTop = logEl.scrollHeight;
      return;
    }
  }

  function escapeText(s) { return String(s ?? ''); }

  function renderStatus(msg) {
    const statuses = Array.isArray(msg.statuses) ? msg.statuses : [];
    const banner = $('banner');
    const sessionsEl = $('sessions');

    if (statuses.length === 0) {
      banner.className = 'banner yellow';
      banner.textContent = '⏸ No Claude Code sessions reporting yet';
      sessionsEl.textContent = '';
      return;
    }
    // Worst severity wins for the top banner.
    const worst = statuses.reduce((acc, s) =>
      (s.color === 'red' || (s.color === 'yellow' && acc.color !== 'red')) ? s : acc,
      statuses[0]);
    banner.className = 'banner ' + worst.color;
    banner.textContent = worst.banner + (worst.action ? '  —  ' + worst.action : '');

    sessionsEl.textContent = '';
    for (const s of statuses) {
      const div = document.createElement('div');
      div.className = 'session';
      const title = document.createElement('div');
      title.textContent = 'Session ' + s.session_id + '  (' + s.color + ' / ' + s.mode + ')';
      div.appendChild(title);
      const bannerEl = document.createElement('div');
      bannerEl.className = 'mono';
      bannerEl.textContent = escapeText(s.banner);
      div.appendChild(bannerEl);
      if (s.action) {
        const action = document.createElement('div');
        action.className = 'mono';
        action.textContent = '→ ' + escapeText(s.action);
        div.appendChild(action);
      }
      const hb = Array.isArray(s.recentHeartbeats) && s.recentHeartbeats.length > 0 ? s.recentHeartbeats[0] : null;
      if (hb) {
        const metric = document.createElement('div');
        metric.className = 'mono';
        metric.textContent = 'fiber_depth ' + hb.mcp_method_fiber_depth +
          ', last_reconnect_latency_ms ' + hb.last_reconnect_latency_ms +
          ', state ' + hb.mcp_session_state;
        div.appendChild(metric);
      }
      sessionsEl.appendChild(div);
    }
  }

  function renderFacts(msg) {
    const pluginEl = $('pluginStatus');
    if (pluginEl) {
      if (msg.pluginInstalled) {
        pluginEl.textContent = msg.pluginVersion
          ? '✔ Installed (v' + escapeText(msg.pluginVersion) + ')'
          : '✔ Installed';
      } else {
        pluginEl.textContent = '✘ Not installed';
      }
    }
    const patchEl = $('patchStatus');
    if (patchEl) {
      if (!msg.patchInstalled) {
        patchEl.textContent = '✘ Not applied';
      } else if (msg.patchStale) {
        const cur = escapeText(msg.patchCurrentVersion ?? '?');
        const nxt = escapeText(msg.patchLatestVersion ?? '?');
        patchEl.textContent = '⚠ Stale (v' + cur + ' → v' + nxt + ')';
      } else {
        const ver = msg.patchCurrentVersion ? ' (v' + escapeText(msg.patchCurrentVersion) + ')' : '';
        patchEl.textContent = '✔ Applied' + ver;
      }
    }
    const channelEl = $('channelStatus');
    if (channelEl) {
      channelEl.textContent = escapeText(msg.channelState) + ' — ' + escapeText(msg.channelDetail);
    }
  }

  function renderBanner(msg) {
    const banner = $('banner');
    if (!banner) return;
    banner.className = 'banner ' + escapeText(msg.tone);
    banner.textContent = escapeText(msg.text);
  }

  window.addEventListener('message', (ev) => {
    const msg = ev.data;
    if (!msg || typeof msg !== 'object') return;
    if (msg.type === 'status') { renderStatus(msg); return; }
    if (msg.kind === 'facts-updated') { renderFacts(msg); return; }
    if (msg.kind === 'banner-updated') { renderBanner(msg); return; }
    if (typeof msg.kind === 'string' && msg.kind.startsWith('activate-')) {
      renderActivate(msg);
    }
  });
})();
</script>
</body>
</html>`;
}
