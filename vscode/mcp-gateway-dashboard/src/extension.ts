import * as vscode from 'vscode';
import { GatewayClient } from './gateway-client';
import { BackendTreeProvider } from './backend-tree-provider';
import { BackendItem } from './backend-item';
import { McpStatusBar } from './status-bar';
import { DaemonManager } from './daemon';
import { LogViewer } from './log-viewer';
import { CredentialStore } from './credential-store';
import { ServerDataCache } from './server-data-cache';
import { SapTreeProvider } from './sap-tree-provider';
import { SapSystemItem } from './sap-item';
import { SapStatusBar } from './sap-status-bar';
import { ServerDetailPanel } from './webview/server-detail-panel';
import { SapDetailPanel } from './webview/sap-detail-panel';
import { ServerDetailViewProvider } from './webview/server-detail-view-provider';
import { AddServerPanel } from './webview/add-server-panel';
import {
	SERVER_NAME_RE,
	validateServerName,
	validateUrl,
} from './validation';

// Accepted client interface — allows dependency injection for tests.
export interface IGatewayClient {
	getHealth(): Promise<unknown>;
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

	const client: IGatewayClient = injectedClient ?? new GatewayClient(apiUrl);

	// Phase 8.3: shared data cache — single listServers() call for all consumers.
	const cache = new ServerDataCache(client);
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
	const logViewer = new LogViewer(apiUrl);
	context.subscriptions.push(logViewer);

	// Phase 8.2: credential store — OS keychain via SecretStorage.
	const credentialStore = new CredentialStore(context);
	credentialStore.reconcile().catch(() => { /* stale entries pruned on best-effort */ });

	// Phase 11.B: sidebar detail view provider — always-on webview replacing
	// the click-toggle WebviewPanel UX pitfall (VS Code issues #34130, #51536,
	// #77418, #85636, #105256). Legacy ServerDetailPanel / SapDetailPanel are
	// retained for the explicit context-menu "Show Details" action.
	const detailViewProvider = new ServerDetailViewProvider(context.extensionUri, cache, credentialStore);
	context.subscriptions.push(detailViewProvider);
	context.subscriptions.push(vscode.window.registerWebviewViewProvider(
		ServerDetailViewProvider.viewType,
		detailViewProvider,
	));
	context.subscriptions.push(treeView.onDidChangeSelection((e) => {
		const first = e.selection[0];
		if (first instanceof BackendItem) {
			detailViewProvider.setMcpSelection(first.server);
		} else {
			detailViewProvider.setMcpSelection(null);
		}
	}));
	context.subscriptions.push(sapTreeView.onDidChangeSelection((e) => {
		const first = e.selection[0];
		if (first instanceof SapSystemItem) {
			detailViewProvider.setSapSelection(first.system);
		} else {
			detailViewProvider.setSapSelection(null);
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

	// Phase 8.3: SAP-specific commands.
	push(vscode.commands.registerCommand('mcpGateway.restartSapVsp', async (item?: SapSystemItem) => {
		if (!item?.system.vsp) { return; }
		await guarded(item.system.vsp.name, 'restart SAP VSP', () =>
			client.restartServer(item.system.vsp!.name) as Promise<void>);
	}));

	push(vscode.commands.registerCommand('mcpGateway.restartSapGui', async (item?: SapSystemItem) => {
		if (!item?.system.gui) { return; }
		await guarded(item.system.gui.name, 'restart SAP GUI', () =>
			client.restartServer(item.system.gui!.name) as Promise<void>);
	}));

	push(vscode.commands.registerCommand('mcpGateway.showSapVspLogs', (item?: SapSystemItem) => {
		if (!item?.system.vsp) { return; }
		logViewer.show(item.system.vsp.name);
	}));

	push(vscode.commands.registerCommand('mcpGateway.showSapGuiLogs', (item?: SapSystemItem) => {
		if (!item?.system.gui) { return; }
		logViewer.show(item.system.gui.name);
	}));

	// Phase 8.4: webview detail panels.
	push(vscode.commands.registerCommand('mcpGateway.showServerDetail', async (item?: BackendItem) => {
		if (!item) { return; }
		await ServerDetailPanel.createOrShow(
			context.extensionUri, item.server, credentialStore, client);
	}));

	push(vscode.commands.registerCommand('mcpGateway.showSapDetail', async (item?: SapSystemItem) => {
		if (!item) { return; }
		await SapDetailPanel.createOrShow(
			context.extensionUri, item.system, credentialStore, client);
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
