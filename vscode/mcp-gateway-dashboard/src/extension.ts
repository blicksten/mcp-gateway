import * as vscode from 'vscode';
import { GatewayClient, GatewayError } from './gateway-client';
import { buildAuthHeader, resolveTokenPath, AuthTokenError } from './auth-header';
import { runKeepassImport, applyImportedCredentials, KeepassImportError } from './keepass-importer';
import { BackendTreeProvider } from './backend-tree-provider';
import { BackendItem } from './backend-item';
import { GatewayTreeProvider } from './gateway-tree-provider';
import { McpStatusBar } from './status-bar';
import { DaemonManager } from './daemon';
import { LogViewer } from './log-viewer';
import { logger } from './logger';
import { CredentialStore } from './credential-store';
import { ServerDataCache } from './server-data-cache';
import { ClaudeConfigSync, defaultClaudeJsonPath } from './claude-config-sync';
import { SapTreeProvider } from './sap-tree-provider';
import { SapSystemItem, SapComponentItem } from './sap-item';
import { SapStatusBar } from './sap-status-bar';
import { ServerDetailPanel } from './webview/server-detail-panel';
import { SapDetailPanel } from './webview/sap-detail-panel';
import { AddServerPanel } from './webview/add-server-panel';
import { AddSapPanel } from './webview/add-sap-panel';
import { ClaudeCodePanel } from './webview/claude-code-panel';
import { SlashCommandGenerator } from './slash-command-generator';
import { assertCompatible } from './version-compat';
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
	const sapSystemsEnabled = config.get<boolean>('sapSystemsEnabled', false);

	// Seed the context key BEFORE any view registration so the `when` clause
	// on `mcpSapSystems` in package.json is correct on first paint.
	void vscode.commands.executeCommand(
		'setContext',
		'mcpGateway.sapSystemsEnabled',
		sapSystemsEnabled,
	);

	// T12A.8/T12A.10: Bearer auth provider (env > file) shared by
	// GatewayClient REST requests and LogViewer SSE connections.
	// Resolved per request so rotating the token file takes effect
	// without a VS Code reload.
	const tokenPath = resolveTokenPath(config);
	let authErrorNotified = false;
	// Phase 4: version-skew guardrail — only check once per session so the toast
	// is not repeated on every poll cycle. Reset if the daemon version changes
	// (e.g. operator hot-swaps the binary) so users are re-notified.
	let versionCompatChecked = false;
	let lastCheckedDaemonVersion: string | undefined;
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

	// Phase D.4 — Gateway daemon tree view. Shown at the top of the
	// mcp-gateway activity container (package.json view order). Provides
	// PID/Version/Started/Uptime rows and inline start/stop/restart icons.
	const gatewayTreeProvider = new GatewayTreeProvider(cache);
	const gatewayTreeView = vscode.window.createTreeView('mcpGatewayDaemon', {
		treeDataProvider: gatewayTreeProvider,
	});

	// Phase 8.3: SAP tree view — auto-detected SAP systems.
	// Gated by `mcpGateway.sapSystemsEnabled` (default false) because SAP is a
	// team-specific feature. Also gates the `SapStatusBar` below to avoid the
	// status-bar item appearing when KeePass-imported systems populate the cache.
	let sapTreeProvider: SapTreeProvider | undefined;
	let sapTreeView: vscode.TreeView<unknown> | undefined;
	if (sapSystemsEnabled) {
		sapTreeProvider = new SapTreeProvider(cache);
		sapTreeView = vscode.window.createTreeView('mcpSapSystems', {
			treeDataProvider: sapTreeProvider,
		});
	}

	// Register disposables before starting side effects (A6 fix).
	context.subscriptions.push(treeView);
	context.subscriptions.push({ dispose: () => treeProvider.dispose() });
	context.subscriptions.push(gatewayTreeView);
	context.subscriptions.push({ dispose: () => gatewayTreeProvider.dispose() });
	if (sapTreeView) { context.subscriptions.push(sapTreeView); }
	if (sapTreeProvider) {
		const provider = sapTreeProvider;
		context.subscriptions.push({ dispose: () => provider.dispose() });
	}

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
			cache.refresh().catch((err: unknown) => {
				logger.error('extension', 'KeePass toggle: cache refresh failed', err);
			});
		}
	}));

	// Update the SAP view context key when the setting flips. Full provider/
	// status-bar lifecycle still requires a window reload — surface that via
	// an informational toast with a one-click reload action.
	context.subscriptions.push(vscode.workspace.onDidChangeConfiguration((e) => {
		if (!e.affectsConfiguration('mcpGateway.sapSystemsEnabled')) { return; }
		const next = vscode.workspace.getConfiguration('mcpGateway')
			.get<boolean>('sapSystemsEnabled', false);
		void vscode.commands.executeCommand(
			'setContext',
			'mcpGateway.sapSystemsEnabled',
			next,
		);
		void vscode.window.showInformationMessage(
			`MCP Gateway: SAP Systems view ${next ? 'enabled' : 'disabled'}. Reload the window to finish applying.`,
			'Reload Window',
		).then((pick) => {
			if (pick === 'Reload Window') {
				void vscode.commands.executeCommand('workbench.action.reloadWindow');
			}
		});
	}));

	registerCommands(context, client, cache, daemon, logViewer, credentialStore);

	// Phase 8.3: shared listServers() timer in cache (replaces per-provider timers).
	// Phase 11.B: McpStatusBar now consumes cache events; only cache has a timer.
	cache.startAutoRefresh(pollInterval);

	// Phase 8.4: auto-update open webview panels on cache refresh.
	// SAP detail panel updates are gated on `sapSystemsEnabled`: when the view
	// is hidden no SapDetailPanel can be opened (the `showSapDetail` command
	// requires an item arg from the hidden tree), so the call is a no-op in
	// practice — the explicit guard makes intent clear and avoids per-poll work.
	context.subscriptions.push(cache.onDidRefresh((payload) => {
		// post-dispose race absorber — see audit ADR for B-08 reassessment
		ServerDetailPanel.updateAll(cache.getAllServers()).catch(() => {});
		if (sapSystemsEnabled) {
			// post-dispose race absorber — see audit ADR for B-08 reassessment
			SapDetailPanel.updateAll(cache.getSapSystems()).catch(() => {});
		}
		// Phase 0d (B-NEW-28): surface a one-shot toast when the gateway rejects
		// requests with 401. Reset the latch when auth recovers so re-failure re-toasts.
		if (payload.lastAuthFailed === true && !authErrorNotified) {
			authErrorNotified = true;
			void vscode.window.showWarningMessage(
				'MCP Gateway: auth token rejected (401). Run `mcp-ctl install-claude-code --refresh-token` or reload the window to refresh credentials.',
				'Reload window',
			).then((selection) => {
				if (selection === 'Reload window') {
					void vscode.commands.executeCommand('workbench.action.reloadWindow');
				}
			});
		} else if (payload.lastAuthFailed !== true) {
			authErrorNotified = false;
		}

		// Phase 4: version-skew guardrail — check once after the first successful
		// health fetch and re-check if the daemon version changes (hot-swap).
		const currentDaemonVersion = payload.gatewayHealth?.version;
		if (payload.gatewayHealth && currentDaemonVersion !== lastCheckedDaemonVersion) {
			versionCompatChecked = false;
			lastCheckedDaemonVersion = currentDaemonVersion;
		}
		if (!versionCompatChecked && payload.gatewayHealth) {
			versionCompatChecked = true;
			const extVersion = (context.extension.packageJSON as { version?: string }).version ?? 'unknown';
			const compatErr = assertCompatible(extVersion, payload.gatewayHealth);
			if (compatErr) {
				logger.error('extension', 'gateway version skew detected', compatErr);
				void vscode.window.showErrorMessage(
					compatErr.remediation,
					'Show Output',
				).then((selection) => {
					if (selection === 'Show Output') {
						void vscode.commands.executeCommand('mcpGateway.showOutput');
					}
				});
			}
		}
	}));

	// Phase 2.6: daemon auto-start (after tree view, before status bar)
	if (autoStart) {
		daemon.start().catch(() => { /* logged by DaemonManager */ });
	}

	// Phase 2.5: status bar — aggregate MCP N/M indicator.
	// Phase 11.B: driven by ServerDataCache.onDidRefresh (no independent polling).
	const statusBar = new McpStatusBar(cache);
	context.subscriptions.push(statusBar);

	// Phase 8.3: SAP status bar. Gated on the same setting as the SAP view so
	// disabled users never see the status-bar indicator (even when KeePass has
	// populated the cache with imported systems).
	if (sapSystemsEnabled) {
		const sapStatusBar = new SapStatusBar(cache);
		context.subscriptions.push(sapStatusBar);
	}

	// Phase 11 (mcp-lifecycle) — extension-as-bridge for Claude Code v2.1.123.
	// Subscribes to cache.onDidRefresh and reflects backend list into
	// ~/.claude.json::mcpServers under prefix `mcp-gateway:` so /mcp panel
	// surfaces gateway-routed servers via the working direct-config path.
	// Plugin loader regression tracked as F-CC-V2-PLUGIN-MCPSERVERS-NOT-LOADED.
	const claudeConfigSync = new ClaudeConfigSync(cache, {
		enabled: () => vscode.workspace
			.getConfiguration('mcpGateway')
			.get<boolean>('claudeConfigSync.enabled', true),
		namespacePrefix: () => vscode.workspace
			.getConfiguration('mcpGateway')
			.get<string>('claudeConfigSync.namespacePrefix', 'mcp-gateway:'),
		configPath: () => {
			const configured = vscode.workspace
				.getConfiguration('mcpGateway')
				.get<string>('claudeConfigSync.path', '');
			return configured && configured.trim().length > 0
				? configured
				: defaultClaudeJsonPath();
		},
		gatewayUrl: () => apiUrl,
		authHeader,
		aggregateEntryName: () => vscode.workspace
			.getConfiguration('mcpGateway')
			.get<string>('claudeConfigSync.aggregateEntryName', 'mcp-gateway'),
	});
	context.subscriptions.push(claudeConfigSync);

	context.subscriptions.push(vscode.commands.registerCommand(
		'mcpGateway.cleanupClaudeConfig',
		async () => {
			const choice = await vscode.window.showWarningMessage(
				'Remove ALL mcp-gateway:* entries from ~/.claude.json? User entries are untouched.',
				{ modal: true },
				'Remove',
			);
			if (choice !== 'Remove') { return; }
			try {
				await claudeConfigSync.cleanup();
				vscode.window.showInformationMessage(
					'Claude Code config: managed mcp-gateway:* entries removed.',
				);
			} catch (err) {
				vscode.window.showErrorMessage(
					`Cleanup failed: ${err instanceof Error ? err.message : String(err)}`,
				);
			}
		},
	));

	context.subscriptions.push(vscode.commands.registerCommand(
		'mcpGateway.previewClaudeConfigSync',
		async () => {
			try {
				const diff = await claudeConfigSync.preview(cache.getAllServers());
				const lines: string[] = [];
				lines.push('Preview of ~/.claude.json::mcpServers reconciliation:');
				lines.push('');
				lines.push(`Added (${diff.added.length}):     ${diff.added.join(', ') || '—'}`);
				lines.push(`Updated (${diff.updated.length}):   ${diff.updated.join(', ') || '—'}`);
				lines.push(`Removed (${diff.removed.length}):   ${diff.removed.join(', ') || '—'}`);
				lines.push(`Unchanged (${diff.unchanged.length}): ${diff.unchanged.join(', ') || '—'}`);
				lines.push('');
				lines.push('No file was modified. Disable mcpGateway.claudeConfigSync.enabled to stop auto-sync.');
				const doc = await vscode.workspace.openTextDocument({
					content: lines.join('\n'),
					language: 'plaintext',
				});
				await vscode.window.showTextDocument(doc, { preview: true });
			} catch (err) {
				vscode.window.showErrorMessage(
					`Preview failed: ${err instanceof Error ? err.message : String(err)}`,
				);
			}
		},
	));

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

	// Phase 8 (B-NEW-22) — Reload-required settings: these are read once on
	// activation and not re-resolved on every poll, so flipping them in
	// settings.json has no effect until the window is reloaded. Surface a
	// one-shot informational toast with a Reload Window action so the user
	// is not left wondering why a setting change appeared to do nothing.
	// Settings already handled by dedicated watchers (keepassEnabled,
	// sapSystemsEnabled, slashCommandsEnabled, claudeConfigSync.*) are NOT
	// included here — they apply live and need no reload.
	const RELOAD_REQUIRED_KEYS: readonly string[] = [
		'mcpGateway.apiUrl',
		'mcpGateway.pollInterval',
		'mcpGateway.autoStart',
		'mcpGateway.daemonPath',
		'mcpGateway.authTokenPath',
		'mcpGateway.mcpCtlPath',
	];
	let reloadPromptShown = false;
	context.subscriptions.push(vscode.workspace.onDidChangeConfiguration((e) => {
		// Collect ALL affected reload-required keys so a bulk settings.json
		// edit names every changed key in the toast — not just the first one
		// in `RELOAD_REQUIRED_KEYS` order (PAL fallback finding F-2).
		const changedKeys = RELOAD_REQUIRED_KEYS.filter((k) => e.affectsConfiguration(k));
		if (changedKeys.length === 0 || reloadPromptShown) { return; }
		reloadPromptShown = true;
		const prefix = 'mcpGateway.';
		const shortNames = changedKeys.map((k) =>
			k.startsWith(prefix) ? k.slice(prefix.length) : k);
		const label = shortNames.length === 1
			? `setting "${shortNames[0]}"`
			: `settings ${shortNames.map((n) => `"${n}"`).join(', ')}`;
		void vscode.window.showInformationMessage(
			`MCP Gateway: ${label} changed — reload the window to apply.`,
			'Reload Window',
		).then((pick) => {
			if (pick === 'Reload Window') {
				void vscode.commands.executeCommand('workbench.action.reloadWindow');
			} else {
				// User dismissed without reloading — re-arm so a later edit
				// re-prompts (otherwise a single dismissed toast silences all
				// future setting changes for the lifetime of the window).
				reloadPromptShown = false;
			}
		});
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
		ClaudeCodePanel.createOrShow({
			extensionUri: context.extensionUri,
			extensionPath: context.extensionPath,
			getGatewayUrl: () => apiUrl,
			getAuthToken: () => {
				const tp = resolveTokenPath(vscode.workspace.getConfiguration('mcpGateway'));
				try {
					const header = buildAuthHeader(tp);
					if (header === undefined) { return undefined; }
					return header.startsWith('Bearer ') ? header.slice(7) : undefined;
				} catch {
					return undefined;
				}
			},
			getTokenPath: () => resolveTokenPath(vscode.workspace.getConfiguration('mcpGateway')),
			fetch: globalThis.fetch,
			// Phase 4B — read the setting live so operator edits are picked up
			// without reopening the panel. Default '' means "look up on PATH".
			getMcpCtlPath: () => vscode.workspace
				.getConfiguration('mcpGateway')
				.get<string>('mcpCtlPath', ''),
			// Phase 4 (B-10) — provide the real daemon version from the cached
			// health response so Copy Diagnostics includes an accurate version string.
			getGatewayVersion: () => cache.gatewayHealth?.version ?? undefined,
			// Phase 5 (B-12) — first workspace folder path so mcp-ctl walks
			// ancestors from the correct location when resolving marketplace.json.
			getWorkspaceFolder: () => vscode.workspace.workspaceFolders?.[0]?.uri.fsPath ?? undefined,
			// Phase 5 (B-12) — optional operator override for marketplace.json;
			// when empty the ancestor walk is used (same behaviour as before).
			getMarketplaceJsonPath: () => {
				const v = vscode.workspace.getConfiguration('mcpGateway').get<string>('marketplaceJsonPath', '');
				return v.trim() === '' ? undefined : v;
			},
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

	// B-07 fix: wrap daemon.start() in try/catch so spawn failures surface
	// as a user-facing error toast instead of the generic "command resulted
	// in an error" VSCode fallback. DaemonManager already logs details to
	// the 'MCP Gateway' channel; the toast adds the human-visible summary.
	push(vscode.commands.registerCommand('mcpGateway.startDaemon', async () => {
		try {
			const spawned = await daemon.start();
			if (spawned) {
				vscode.window.showInformationMessage('MCP Gateway daemon started.');
			} else {
				vscode.window.showInformationMessage('MCP Gateway daemon is already running.');
			}
		} catch (err) {
			logger.error('extension', 'startDaemon spawn failed', err);
			vscode.window.showErrorMessage(
				`Start daemon failed: ${err instanceof Error ? err.message : String(err)}`,
			);
		}
	}));

	// B-06 fix: try REST shutdown FIRST, regardless of whether the extension
	// owns the child process. This handles externally-started daemons
	// (e.g. via `mcp-ctl daemon start`) where daemon.running is false.
	push(vscode.commands.registerCommand('mcpGateway.stopDaemon', async () => {
		// Step 1: check reachability — null means daemon is not running.
		const health = await client.getHealth().catch(() => null);
		if (health === null) {
			vscode.window.showInformationMessage('No daemon process to stop.');
			return;
		}

		// Step 2: REST shutdown — works regardless of ownership.
		try {
			await client.shutdown();
		} catch (err) {
			if (err instanceof GatewayError && err.kind === 'auth') {
				logger.error('extension', 'stopDaemon: auth rejected during shutdown', err);
				vscode.window.showErrorMessage(
					'MCP Gateway: auth token rejected. Run `mcp-ctl install-claude-code --refresh-token` to refresh credentials.',
				);
				return;
			}
			// Connection-level failure — fall through to daemon.stop() if available.
			logger.warn('extension', 'stopDaemon: REST shutdown failed — falling back to local stop', err);
			if (daemon.running) {
				daemon.stop();
			} else {
				vscode.window.showInformationMessage('Could not reach daemon for shutdown.');
				return;
			}
		}

		// Step 3: poll until unreachable (up to 5 s, 250 ms intervals).
		const deadline = Date.now() + 5_000;
		while (Date.now() < deadline) {
			const reachable = await client.getHealth().then(() => true, () => false);
			if (!reachable) { break; }
			await new Promise((resolve) => setTimeout(resolve, 250));
		}

		// Step 4: also stop local child handle if we own one (clean up spawn handle).
		if (daemon.running) {
			daemon.stop();
		}

		void cache.refresh();
		vscode.window.showInformationMessage('MCP Gateway daemon stopped.');
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
