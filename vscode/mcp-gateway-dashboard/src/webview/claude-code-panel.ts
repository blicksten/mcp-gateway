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
import { escapeHtml } from './html-builder';

export interface ClaudeCodePanelDeps {
	extensionUri: vscode.Uri;
	extensionPath: string;
	/** Resolves the gateway URL (honours the live `mcpGateway.apiUrl` setting). */
	getGatewayUrl(): string;
	/** Returns the Bearer token or undefined. */
	getAuthToken(): string | undefined;
	/** Factory returning an HTTP client. Injected for tests. */
	fetch: typeof fetch;
}

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

			const facts = await this.gatherFacts();
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
		}
	}

	/** Gathers non-heartbeat facts. Keeps inline implementations short — real
	 * CC plugin detection lives in T16.5.2 and is stubbed here until that
	 * task ships. */
	private async gatherFacts(): Promise<ExternalFacts> {
		// For now, assume patch installed when apply-mcp-gateway.sh is resolvable;
		// a proper FS check lives in the activation hook (T16.4.5, out-of-scope here).
		const ccVersion =
			Array.from(this.state.tracks.values())[0]?.lastHeartbeat?.cc_version ?? '';
		const altE =
			this.state.compatMatrix?.alt_e_verified_versions ?? [];
		return {
			patchInstalled: true, // TODO: FS check in T16.5.2
			patchStale: false,
			pluginInstalled: true, // TODO: `claude plugin list --json` in T16.5.2
			gatewayReachable: true,
			tokenRotationDriftMs: null,
			ccVersion,
			altEVerifiedVersions: altE,
			maxAltEVersion: altE[altE.length - 1] ?? '',
			corsReachable: null,
			anyRecentHeartbeat: this.state.tracks.size > 0,
		};
	}

	private async handleMessage(msg: unknown): Promise<void> {
		if (typeof msg !== 'object' || msg === null) {
			return;
		}
		const m = msg as { command?: string };
		switch (m.command) {
			case 'activate':
				await this.handleActivate();
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

	/** T16.5.2 — [Activate for Claude Code] flow. Posts to /plugin-sync; the
	 * Claude CLI marketplace install flow is surfaced via a terminal spawn
	 * rather than wrapped here — the command text is the authoritative
	 * install step, documented in Phase 16.9. */
	private async handleActivate(): Promise<void> {
		const url = this.deps.getGatewayUrl();
		const token = this.deps.getAuthToken();
		const headers: Record<string, string> = { 'Content-Type': 'application/json' };
		if (token) {
			headers['Authorization'] = `Bearer ${token}`;
		}
		try {
			const resp = await this.deps.fetch(`${url}/api/v1/claude-code/plugin-sync`, {
				method: 'POST',
				headers,
			});
			if (resp.ok) {
				const body = (await resp.json()) as { entries_count?: number; status?: string };
				void vscode.window.showInformationMessage(
					`Claude Code plugin synced (${body.entries_count ?? 0} entries). ` +
						`Install via: claude plugin marketplace add <repo>/installer/marketplace.json && claude plugin install mcp-gateway@mcp-gateway-local`,
				);
			} else if (resp.status === 409) {
				void vscode.window.showErrorMessage(
					'Plugin directory not configured on gateway. Set GATEWAY_PLUGIN_DIR or install the plugin via `claude plugin install`.',
				);
			} else {
				void vscode.window.showErrorMessage(`plugin-sync failed: HTTP ${resp.status}`);
			}
		} catch (err: unknown) {
			const e = err instanceof Error ? err : new Error(String(err));
			void vscode.window.showErrorMessage(`Activate failed: ${e.message}`);
		}
	}

	/** T16.5.3 — Auto-reload checkbox handler. Spawns apply / uninstall script. */
	private async handleToggleAutoReload(enabled: boolean): Promise<void> {
		const url = this.deps.getGatewayUrl();
		const token = this.deps.getAuthToken();
		if (!token) {
			void vscode.window.showErrorMessage(
				'Cannot install patch: no gateway auth token available. Start the daemon and retry.',
			);
			return;
		}
		const result = await runPatchInstaller({
			extensionPath: this.deps.extensionPath,
			gatewayUrl: url,
			gatewayAuthToken: token,
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
			// Poll for the result up to 15 s.
			const deadline = Date.now() + 15_000;
			while (Date.now() < deadline) {
				await new Promise((r) => setTimeout(r, 500));
				// The patch reports via POST /probe-result; the gateway stores by nonce.
				// No per-nonce GET on the FROZEN contract, so we rely on next heartbeat
				// carrying the probe ack — UI already displays the result path via
				// heartbeat stream. This stub simply toasts "Probe sent".
			}
			void vscode.window.showInformationMessage(
				`Probe sent (nonce ${nonce.slice(0, 8)}…). Check patch status — a probe-reconnect that rejects with "Server not found" is the GREEN success signal.`,
			);
		} catch (err: unknown) {
			const e = err instanceof Error ? err : new Error(String(err));
			void vscode.window.showErrorMessage(`Probe failed: ${e.message}`);
		}
	}

	/** T16.5.7 — [Copy diagnostics] generates structured report → clipboard. */
	private async handleCopyDiagnostics(): Promise<void> {
		const input: DiagnosticsInput = {
			platform: process.platform,
			vscodeVersion: vscode.version,
			gatewayVersion: 'unknown', // TODO: wire /api/v1/version
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
</style>
</head>
<body>
<h1>Claude Code Integration</h1>

<div class="section">
  <button id="activate">Activate for Claude Code</button>
  <span id="pluginStatus" class="mono">● Checking…</span>
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

<div id="banner" class="banner yellow">Polling gateway…</div>

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
  $('autoReload').addEventListener('change', (e) =>
    vscode.postMessage({ command: 'toggleAutoReload', enabled: e.target.checked }));
  $('probe').addEventListener('click', () => vscode.postMessage({ command: 'probeReconnect' }));
  $('copyDiag').addEventListener('click', () => vscode.postMessage({ command: 'copyDiagnostics' }));

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

  window.addEventListener('message', (ev) => {
    const msg = ev.data;
    if (!msg || typeof msg !== 'object') return;
    if (msg.type === 'status') { renderStatus(msg); }
  });
})();
</script>
</body>
</html>`;
}
