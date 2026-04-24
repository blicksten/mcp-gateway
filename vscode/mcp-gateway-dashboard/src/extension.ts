import * as vscode from 'vscode';
import { GatewayClient } from './gateway-client';
import { buildAuthHeader, resolveTokenPath, AuthTokenError } from './auth-header';
import { runKeepassImport, applyImportedCredentials, KeepassImportError } from './keepass-importer';
import { BackendTreeProvider } from './backend-tree-provider';
import { BackendItem } from './backend-item';
import { McpStatusBar } from './status-bar';
import { DaemonManager } from './daemon';
import { LogViewer } from './log-viewer';
import { CredentialStore } from './credential-store';
import { ServerDataCache } from './server-data-cache';
import { SapTreeProvider } from './sap-tree-provider';
import { SapSystemItem, SapComponentItem } from './sap-item';
import { SapStatusBar } from './sap-status-bar';
import { ServerDetailPanel } from './webview/server-detail-panel';
import { SapDetailPanel } from './webview/sap-detail-panel';
import { AddServerPanel } from './webview/add-server-panel';
import { AddSapPanel } from './webview/add-sap-panel';
import { ClaudeCodePanel } from './webview/claude-code-panel';
import { SlashCommandGenerator } from './slash-command-generator';
import {
	SERVER_NAME_RE,
	validateServerName,
	validateUrl,
} from './validation';

// Accepted client interface — allows dependency injection for tests.
export interface IGatewayClient {
	getHealth(): Promise<unknown>;
	shutdown(): Promise<unknown>;
	listServers(): Promise<unknown[]>;
	getServer(name: string): Promise<unknown>;
	addServer(name: string, config: Record<string, unknown>): Promise<unknown>;
	removeServer(name: string): Promise<unknown>;
	patchServer(name: string, patch: Record<string, unknown>): Promise<unknown>;
	restartServer(name: string): Promise<unknown>;
	resetCircuit(name: string): Promise<unknown>;
	callTool(server: string, tool: string, args?: Record<string, unknown>): Promise<unknown>;
	listTools(): Promise<unknown[]>;
}

export function activate(
	context: vscode.ExtensionContext,
	injectedClient?: IGatewayClient,
	injectedDaemon?: DaemonManager,
): void {
	const config = vscode.workspace.getConfiguration('mcpGateway');
	const apiUrl = config.get<string>('apiUrl', 'http://localhost:8765');
	const rawInterval = config.get<number>('pollInterval', 5000);
	const pollInterval = Math.max(rawInterval, 1000);
	const autoStart = config.get<boolean>('autoStart', true);
	const daemonPath = config.get<string>('daemonPath', '');

	// T12A.8/T12A.10: Bearer auth provider (env > file) shared by
	// GatewayClient REST requests and LogViewer SSE connections.
	// Resolved per request so rotating the token file takes effect
	// without a VS Code reload.
	const tokenPath = resolveTokenPath(config);
	let authErrorNotified = false;
	const authHeader = (): string | undefined => {
		try {
			return buildAuthHeader(tokenPath);
		} catch (err) {
			if (err instanceof AuthTokenError && !authErrorNotified) {
				authErrorNotified = true;
				void vscode.window.showWarningMessage(
					'MCP Gateway: auth token not found. Start the daemon once to generate ~/.mcp-gateway/auth.token, or set MCP_GATEWAY_AUTH_TOKEN.',
					'Reload token',
				).then((pick) => {
					if (pick === 'Reload token') {
						authErrorNotified = false; // allow next attempt to show again on failure
					}
				});
			}
			throw err;
		}
	};

	const client: IGatewayClient = injectedClient ?? new GatewayClient(apiUrl, 5000, authHeader);

	// Phase 8.2: credential store — OS keychain via SecretStorage.
	// Constructed before the cache so the Phase 17.5 keepass-imported provider
	// closure below can capture it by reference.
	const credentialStore = new CredentialStore(context);
	credentialStore.reconcile().catch(() => { /* stale entries pruned on best-effort */ });

	// Phase 17.5 — KeePass-imported SAP rows provider. Reads the toggle fresh on
	// every refresh so flipping the setting takes effect without a reload.
	// Returns only credential names (never secret values).
	const importedProvider = (): readonly string[] => {
		const cfg = vscode.workspace.getConfiguration('mcpGateway');
		if (!cfg.get<boolean>('keepassEnabled', false)) { return []; }
		return credentialStore.listServers();
	};

	// Phase 8.3: shared data cache — single listServers() call for all consumers.
	const cache = new ServerDataCache(client, importedProvider);
	context.subscriptions.push(cache);

	const treeProvider = new BackendTreeProvider(cache);

	// Phase 2.3: tree view registration
	const treeView = vscode.window.createTreeView('mcpBackends', {
		treeDataProvider: treeProvider,
	});

	// Phase 8.3: SAP tree view — auto-detected SAP systems.
	const sapTreeProvider = new SapTreeProvider(cache);
	const sapTreeView = vscode.window.createTreeView('mcpSapSystems', {
		treeDataProvider: sapTreeProvider,
	});

	// Register disposables before starting side effects (A6 fix).
	context.subscriptions.push(treeView);
	context.subscriptions.push({ dispose: () => treeProvider.dispose() });
	context.subscriptions.push(sapTreeView);
	context.subscriptions.push({ dispose: () => sapTreeProvider.dispose() });

	// Phase 2.4: command registration (daemon passed for start/stop wiring)
	const daemon = injectedDaemon ?? new DaemonManager(client, daemonPath);
	context.subscriptions.push(daemon);

	// Phase 2.7: log viewer — SSE-based live logs per backend.
	// T12A.9: share the same authHeader provider with GatewayClient so
	// rotating the token (via Reload token action) affects both.
	const logViewer = new LogViewer(apiUrl, { authHeader });
	context.subscriptions.push(logViewer);

	// Phase 17.1: sidebar ServerDetail view removed. Details are shown on demand
	// via the `mcpGateway.showServerDetail` / `mcpGateway.showSapDetail` context
	// commands, which open modal `ServerDetailPanel` / `SapDetailPanel` webviews.

	// Phase 17.5 — refresh the SAP tree when the KeePass toggle flips.
	context.subscriptions.push(vscode.workspace.onDidChangeConfiguration((e) => {
		if (e.affectsConfiguration('mcpGateway.keepassEnabled')) {
			cache.refresh().catch(() => {});
		}
	}));

	registerCommands(context, client, cache, daemon, logViewer, credentialStore);

	// Phase 8.3: shared listServers() timer in cache (replaces per-provider timers).
	// Phase 11.B: McpStatusBar now consumes cache events; only cache has a timer.
	cache.startAutoRefresh(pollInterval);

	// Phase 8.4: auto-update open webview panels on cache refresh.
	context.subscriptions.push(cache.onDidRefresh(() => {
		ServerDetailPanel.updateAll(cache.getAllServers()).catch(() => {});
		SapDetailPanel.updateAll(cache.getSapSystems()).catch(() => {});
	}));

	// Phase 2.6: daemon auto-start (after tree view, before status bar)
	if (autoStart) {
		daemon.start().catch(() => { /* logged by DaemonManager */ });
	}

	// Phase 2.5: status bar — aggregate MCP N/M indicator.
	// Phase 11.B: driven by ServerDataCache.onDidRefresh (no independent polling).
	const statusBar = new McpStatusBar(cache);
	context.subscriptions.push(statusBar);

	// Phase 8.3: SAP status bar.
	const sapStatusBar = new SapStatusBar(cache);
	context.subscriptions.push(sapStatusBar);

	// Phase 11.E: slash command auto-generation.
	// catalog.C: pass extensionUri so the generator can resolve the bundled
	// catalog dir (or operator override via mcpGateway.catalogPath) at write
	// time for ${server_name}/${server_url} substitution.
	const slashGen = new SlashCommandGenerator(cache, context.extensionUri);
	context.subscriptions.push(slashGen);
	if (config.get<boolean>('slashCommandsEnabled', false)) {
		slashGen.enable();
	}
	context.subscriptions.push(vscode.workspace.onDidChangeConfiguration((e) => {
		if (e.affectsConfiguration('mcpGateway.slashCommandsEnabled')) {
			const enabled = vscode.workspace.getConfiguration('mcpGateway')
				.get<boolean>('slashCommandsEnabled', false);
			if (enabled) { slashGen.enable(); } else { slashGen.disable(); }
		}
	}));
}

export function deactivate(): void {
	// Cleanup handled by disposables in context.subscriptions
}

// In-flight guard: prevents concurrent operations on the same server (D1 fix).
const pendingOps = new Set<string>();

// Re-export shared validators so existing tests importing from '../extension'
// continue to work without a test-file rewrite.
export { validateServerName, validateUrl };

function registerCommands(
	context: vscode.ExtensionContext,
	client: IGatewayClient,
	cache: ServerDataCache,
	daemon: DaemonManager,
	logViewer: LogViewer,
	credentialStore: CredentialStore,
): void {
	const push = (d: vscode.Disposable) => context.subscriptions.push(d);

	/** Run a server operation with in-flight guard (keyed by server name only
	 *  so that SAP and MCP commands on the same physical server cannot overlap). */
	async function guarded(serverName: string, label: string, fn: () => Promise<void>): Promise<void> {
		const key = serverName;
		if (pendingOps.has(key)) { return; }
		pendingOps.add(key);
		try {
			await fn();
			await cache.refresh(); // Re-fetch from API; providers update via onDidRefresh.
		} catch (err) {
			vscode.window.showErrorMessage(`Failed to ${label}: ${errorMsg(err)}`);
		} finally {
			pendingOps.delete(key);
		}
	}

	push(vscode.commands.registerCommand('mcpGateway.refresh', () => {
		cache.refresh(); // Re-fetch from API; all providers update via onDidRefresh.
	}));

	push(vscode.commands.registerCommand('mcpGateway.openSettings', () => {
		void vscode.commands.executeCommand(
			'workbench.action.openSettings',
			'@ext:mcp-gateway.mcp-gateway-dashboard',
		);
	}));

	push(vscode.commands.registerCommand('mcpGateway.showClaudeCodeIntegration', () => {
		const cfg = vscode.workspace.getConfiguration('mcpGateway');
		const apiUrl = cfg.get<string>('apiUrl', 'http://localhost:8765');
		const tokenPath = resolveTokenPath(cfg);
		ClaudeCodePanel.createOrShow({
			extensionUri: context.extensionUri,
			extensionPath: context.extensionPath,
			getGatewayUrl: () => apiUrl,
			getAuthToken: () => {
				try {
					const header = buildAuthHeader(tokenPath);
					if (header === undefined) { return undefined; }
					return header.startsWith('Bearer ') ? header.slice(7) : undefined;
				} catch {
					return undefined;
				}
			},
			fetch: globalThis.fetch,
			// Phase 4B — read the setting live so operator edits are picked up
			// without reopening the panel. Default '' means "look up on PATH".
			getMcpCtlPath: () => vscode.workspace
				.getConfiguration('mcpGateway')
				.get<string>('mcpCtlPath', ''),
		});
	}));

	push(vscode.commands.registerCommand('mcpGateway.startServer', async (item?: BackendItem) => {
		if (!item) { return; }
		await guarded(item.server.name, 'enable server', () =>
			client.patchServer(item.server.name, { disabled: false }) as Promise<void>);
	}));

	push(vscode.commands.registerCommand('mcpGateway.stopServer', async (item?: BackendItem) => {
		if (!item) { return; }
		await guarded(item.server.name, 'disable server', () =>
			client.patchServer(item.server.name, { disabled: true }) as Promise<void>);
	}));

	push(vscode.commands.registerCommand('mcpGateway.restartServer', async (item?: BackendItem) => {
		if (!item) { return; }
		await guarded(item.server.name, 'restart server', () =>
			client.restartServer(item.server.name) as Promise<void>);
	}));

	push(vscode.commands.registerCommand('mcpGateway.removeServer', async (item?: BackendItem) => {
		if (!item) { return; }
		const answer = await vscode.window.showWarningMessage(
			`Remove server "${item.server.name}"? This cannot be undone.`,
			'Remove',
			'Cancel',
		);
		if (answer !== 'Remove') { return; }
		await guarded(item.server.name, 'remove server', async () => {
			try {
				await client.removeServer(item.server.name);
			} finally {
				// Phase 8.2: always clean credentials, even if daemon API fails.
				await credentialStore.deleteServerCredentials(item.server.name);
			}
		});
	}));

	push(vscode.commands.registerCommand('mcpGateway.addServer', async () => {
		await AddServerPanel.createOrShow(
			context.extensionUri,
			client,
			credentialStore,
			() => { void cache.refresh(); },
		);
	}));

	// T12B.5 — KeePass credential import via mcp-ctl child process.
	push(vscode.commands.registerCommand('mcpGateway.importKeepassCredentials', async () => {
		const cfg = vscode.workspace.getConfiguration('mcpGateway');
		const kdbxPath = cfg.get<string>('keepassPath', '').trim();
		if (!kdbxPath) {
			vscode.window.showErrorMessage(
				'MCP Gateway: set mcpGateway.keepassPath to your KDBX file path before importing.',
			);
			return;
		}
		// PAL HIGH fix: dedicated setting for the mcp-ctl binary. Reusing
		// `daemonPath` (which points at mcp-gateway) would spawn the
		// wrong binary and surface a confusing error.
		const mcpCtlPath = cfg.get<string>('mcpCtlPath', '').trim() || 'mcp-ctl';
		const group = cfg.get<string>('keepassGroup', '').trim() || undefined;

		const password = await vscode.window.showInputBox({
			prompt: `KeePass master password for ${kdbxPath}`,
			password: true,
			ignoreFocusOut: true,
		});
		if (!password) { return; } // cancelled

		try {
			const payload = await runKeepassImport({
				mcpCtlPath,
				kdbxPath,
				masterPassword: password,
				group,
			});
			const results = await applyImportedCredentials(credentialStore, payload);

			const stored = results.filter((r) => r.status === 'stored').length;
			const failed = results.filter((r) => r.status === 'failed').length;
			const skipped = results.filter((r) => r.status === 'skipped').length;
			const warned = results.filter((r) => !!r.error && r.status !== 'failed').length;

			if (failed === 0 && warned === 0 && skipped === 0) {
				vscode.window.showInformationMessage(
					`Imported ${stored} server(s) from KeePass. Credentials are in SecretStorage.`,
				);
			} else {
				// PAL MEDIUM fix: partial-failure and skipped entries
				// must surface as a warning, not buried in an info toast.
				vscode.window.showWarningMessage(
					`KeePass import: ${stored} stored, ${failed} failed, ${skipped} skipped${warned > 0 ? `, ${warned} with warnings` : ''}.`,
				);
			}
			void cache.refresh();
		} catch (err) {
			if (err instanceof KeepassImportError) {
				vscode.window.showErrorMessage(`KeePass import failed: ${err.message}`);
			} else {
				vscode.window.showErrorMessage(`KeePass import failed: ${(err as Error).message}`);
			}
		}
	}));

	push(vscode.commands.registerCommand('mcpGateway.resetCircuit', async (item?: BackendItem) => {
		if (!item) { return; }
		await guarded(item.server.name, 'reset circuit', () =>
			client.resetCircuit(item.server.name) as Promise<void>);
	}));

	// Phase 2.7: show SSE log stream for a backend.
	push(vscode.commands.registerCommand('mcpGateway.showLogs', async (item?: BackendItem) => {
		if (!item) { return; }
		logViewer.show(item.server.name);
	}));

	push(vscode.commands.registerCommand('mcpGateway.startDaemon', async () => {
		const spawned = await daemon.start();
		if (spawned) {
			vscode.window.showInformationMessage('MCP Gateway daemon started.');
		} else {
			vscode.window.showInformationMessage('MCP Gateway daemon is already running.');
		}
	}));

	push(vscode.commands.registerCommand('mcpGateway.stopDaemon', () => {
		if (daemon.running) {
			daemon.stop();
			vscode.window.showInformationMessage('MCP Gateway daemon stopped.');
		} else {
			vscode.window.showInformationMessage('No daemon process to stop.');
		}
	}));

	// Phase D.3: restart the daemon via REST shutdown + poll + respawn.
	// Works for both extension-owned children and externally-started daemons
	// (mcp-ctl daemon start) — uses POST /api/v1/shutdown rather than
	// child.kill, which would no-op when this extension doesn't own the process.
	push(vscode.commands.registerCommand('mcpGateway.restartDaemon', async () => {
		try {
			const spawned = await daemon.restart();
			if (spawned) {
				vscode.window.showInformationMessage('MCP Gateway daemon restarted.');
			} else {
				vscode.window.showWarningMessage('MCP Gateway daemon did not restart — it may still be running.');
			}
		} catch (err) {
			vscode.window.showErrorMessage(`Restart failed: ${(err as Error).message}`);
		} finally {
			// AUDIT A-L1: refresh unconditionally — both success and
			// failure branches need the cache re-pulled so the status bar
			// and gateway tree drop stale gatewayHealth (pid/uptime) data.
			void cache.refresh();
		}
	}));

	// Phase 8.3: SAP-specific commands.
	// Resolve the VSP/GUI server name from either a SapSystemItem (flat tree
	// or group parent in hierarchical mode) or a SapComponentItem (child row
	// in hierarchical mode). Returns null when the component is not present.
	function resolveSapServer(item: SapSystemItem | SapComponentItem | undefined, kind: 'vsp' | 'gui'): string | null {
		if (!item) { return null; }
		if (item instanceof SapComponentItem) {
			return item.kind === kind ? item.server.name : null;
		}
		return item.system[kind]?.name ?? null;
	}

	push(vscode.commands.registerCommand('mcpGateway.restartSapVsp', async (item?: SapSystemItem | SapComponentItem) => {
		const name = resolveSapServer(item, 'vsp');
		if (!name) { return; }
		await guarded(name, 'restart SAP VSP', () =>
			client.restartServer(name) as Promise<void>);
	}));

	push(vscode.commands.registerCommand('mcpGateway.restartSapGui', async (item?: SapSystemItem | SapComponentItem) => {
		const name = resolveSapServer(item, 'gui');
		if (!name) { return; }
		await guarded(name, 'restart SAP GUI', () =>
			client.restartServer(name) as Promise<void>);
	}));

	push(vscode.commands.registerCommand('mcpGateway.showSapVspLogs', (item?: SapSystemItem | SapComponentItem) => {
		const name = resolveSapServer(item, 'vsp');
		if (!name) { return; }
		logViewer.show(name);
	}));

	push(vscode.commands.registerCommand('mcpGateway.showSapGuiLogs', (item?: SapSystemItem | SapComponentItem) => {
		const name = resolveSapServer(item, 'gui');
		if (!name) { return; }
		logViewer.show(name);
	}));

	push(vscode.commands.registerCommand('mcpGateway.addSapSystem', async () => {
		await AddSapPanel.createOrShow(
			context.extensionUri,
			client,
			cache,
			() => { void cache.refresh(); },
		);
	}));

	// Phase 8.4: webview detail panels.
	push(vscode.commands.registerCommand('mcpGateway.showServerDetail', async (item?: BackendItem) => {
		if (!item) { return; }
		await ServerDetailPanel.createOrShow(
			context.extensionUri, item.server, credentialStore, client);
	}));

	push(vscode.commands.registerCommand('mcpGateway.showSapDetail', async (item?: SapSystemItem | SapComponentItem) => {
		if (!item) { return; }
		// In hierarchical mode, showSapDetail may be invoked from a child row —
		// the detail panel always targets the parent SapSystem, so SapComponentItem
		// falls back to item.system.
		const system = item.system;
		await SapDetailPanel.createOrShow(
			context.extensionUri, system, credentialStore, client);
	}));

	// Phase 8.4: internal command for webview action messages.
	push(vscode.commands.registerCommand('mcpGateway._webviewAction', async (msg: { action: string; serverName?: string; component?: string }) => {
		if (!msg?.action) { return; }
		const name = msg.serverName;
		if (!name || !SERVER_NAME_RE.test(name)) { return; }
		switch (msg.action) {
			case 'restart':
				await guarded(name, 'restart server', () =>
					client.restartServer(name) as Promise<void>);
				break;
			case 'enable':
				await guarded(name, 'enable server', () =>
					client.patchServer(name, { disabled: false }) as Promise<void>);
				break;
			case 'disable':
				await guarded(name, 'disable server', () =>
					client.patchServer(name, { disabled: true }) as Promise<void>);
				break;
			case 'resetCircuit':
				await guarded(name, 'reset circuit', () =>
					client.resetCircuit(name) as Promise<void>);
				break;
			case 'showLogs':
				logViewer.show(name);
				break;
			default:
				break;
		}
	}));
}

function errorMsg(err: unknown): string {
	if (err instanceof Error) { return err.message; }
	if (typeof err === 'object' && err !== null) { return JSON.stringify(err); }
	return String(err);
}

// Export for testing — allows access to in-flight guard state.
export { pendingOps as _pendingOps };
