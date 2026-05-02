// Import mock BEFORE production modules (CommonJS require order).
import { dialogResponses, dispatchedCommands, fireConfigChange, mockCalls, mockConfigValues, mockOutputChannels, mockWebviewPanels, resetMockState, getRegisteredCommands, MockSecretStorage, MockMemento } from './mock-vscode';

import * as assert from 'node:assert';
import { describe, it, beforeEach } from 'mocha';
import { activate, validateServerName, validateUrl, _pendingOps } from '../extension';
import { BackendItem } from '../backend-item';
import { DaemonManager } from '../daemon';
import type { SpawnFn } from '../daemon';
import type { ServerView } from '../types';

/** No-op spawn for tests that don't exercise daemon behavior. */
const noopSpawn: SpawnFn = (() => {
	const { EventEmitter } = require('node:events');
	const child = new EventEmitter();
	child.stdout = new EventEmitter();
	child.stderr = new EventEmitter();
	child.pid = 0;
	child.killed = false;
	child.kill = () => { child.killed = true; child.emit('exit', null, 'SIGTERM'); return true; };
	return child;
}) as unknown as SpawnFn;

/** Create a mock DaemonManager that never spawns real processes. */
function createMockDaemon(client: any): DaemonManager {
	const output = {
		name: 'test', lines: [] as string[], disposed: false,
		appendLine() {}, append() {}, clear() {}, show() {}, hide() {}, dispose() {},
	};
	return new DaemonManager(client, '', output as any, noopSpawn);
}

// Minimal mock of ExtensionContext — tracks subscriptions + secrets + globalState.
function createMockContext() {
	return {
		subscriptions: [] as Array<{ dispose(): void }>,
		secrets: new MockSecretStorage(),
		globalState: new MockMemento(),
	};
}

// Mock GatewayClient with configurable behavior.
interface MockClientCall {
	method: string;
	args: unknown[];
}

function createTrackingClient() {
	const calls: MockClientCall[] = [];
	let shouldFail = false;
	let failMessage = 'mock error';
	// After shutdown() is called, getHealth() rejects to simulate daemon exiting.
	// This makes the stopDaemon poll-until-unreachable loop exit immediately.
	let daemonShutDown = false;

	const client = {
		calls,
		set shouldFail(v: boolean) { shouldFail = v; },
		set failMessage(v: string) { failMessage = v; },
		listServers: async () => [],
		getHealth: async () => {
			if (daemonShutDown) { throw new Error('ECONNREFUSED'); }
			return { status: 'ok', servers: 0, running: 0 };
		},
		addServer: async (name: string, config: unknown) => {
			calls.push({ method: 'addServer', args: [name, config] });
			if (shouldFail) { throw new Error(failMessage); }
			return { status: 'ok' };
		},
		removeServer: async (name: string) => {
			calls.push({ method: 'removeServer', args: [name] });
			if (shouldFail) { throw new Error(failMessage); }
			return { status: 'ok' };
		},
		patchServer: async (name: string, patch: unknown) => {
			calls.push({ method: 'patchServer', args: [name, patch] });
			if (shouldFail) { throw new Error(failMessage); }
			return { status: 'ok' };
		},
		restartServer: async (name: string) => {
			calls.push({ method: 'restartServer', args: [name] });
			if (shouldFail) { throw new Error(failMessage); }
			return { status: 'ok' };
		},
		resetCircuit: async (name: string) => {
			calls.push({ method: 'resetCircuit', args: [name] });
			if (shouldFail) { throw new Error(failMessage); }
			return { status: 'ok' };
		},
		callTool: async () => ({ content: null }),
		listTools: async () => [],
		// B-06 fix: stopDaemon now calls client.shutdown() for REST-first stop.
		// After shutdown resolves, mark daemonShutDown=true so subsequent getHealth
		// calls reject — this lets the poll-until-unreachable loop exit immediately.
		shutdown: async () => {
			calls.push({ method: 'shutdown', args: [] });
			if (shouldFail) { throw new Error(failMessage); }
			daemonShutDown = true;
			return { status: 'ok' };
		},
	};

	return client;
}

// Helper to create a BackendItem for command testing.
function makeBackendItem(name: string, status = 'running' as ServerView['status']): BackendItem {
	const server: ServerView = {
		name,
		status,
		transport: 'stdio',
		restart_count: 0,
	};
	return new BackendItem(server);
}

// activate() accepts an optional injectedClient parameter for DI.
// Tests use createTrackingClient() for fine-grained control over client behavior.

describe('Commands', () => {
	let context: ReturnType<typeof createMockContext>;
	let commands: Map<string, (...args: unknown[]) => unknown>;

	beforeEach(() => {
		resetMockState();
		_pendingOps.clear();
		context = createMockContext();
		// activate registers commands via mock vscode.commands.registerCommand
		const daemon = createMockDaemon({});
		activate(context as any, undefined, daemon);
		commands = getRegisteredCommands();
	});

	describe('command registration', () => {
		const expectedCommands = [
			'mcpGateway.refresh',
			'mcpGateway.startServer',
			'mcpGateway.stopServer',
			'mcpGateway.restartServer',
			'mcpGateway.removeServer',
			'mcpGateway.addServer',
			'mcpGateway.importKeepassCredentials',
			'mcpGateway.resetCircuit',
			'mcpGateway.showLogs',
			'mcpGateway.startDaemon',
			'mcpGateway.stopDaemon',
			'mcpGateway.restartSapVsp',
			'mcpGateway.restartSapGui',
			'mcpGateway.showSapVspLogs',
			'mcpGateway.showSapGuiLogs',
			'mcpGateway.showSapDetail',
			'mcpGateway.addSapSystem',
			'mcpGateway.openSettings',
		];

		for (const cmd of expectedCommands) {
			it(`registers ${cmd}`, () => {
				assert.ok(commands.has(cmd), `Command ${cmd} not registered`);
			});
		}

		it('pushes disposables to context.subscriptions', () => {
			// treeView + treeProvider dispose + 10 commands + daemon + logViewer
			assert.ok(context.subscriptions.length >= 12,
				`Expected at least 12 subscriptions, got ${context.subscriptions.length}`);
		});
	});

	describe('mcpGateway.sapSystemsEnabled gating', () => {
		// Activation seeds the `setContext` command with the current value of
		// `mcpGateway.sapSystemsEnabled` so the `when` clause on the view entry
		// is correct on first paint. Default is `false` (team-specific feature).

		function seedSetContextDispatches(): Array<{ id: string; args: unknown[] }> {
			return dispatchedCommands.filter((c) => c.id === 'setContext');
		}

		it('seeds context key with false when setting is absent (default)', () => {
			// beforeEach already ran activate() once with mockConfigValues empty,
			// so the default `false` path is already under test here.
			const setContextCalls = seedSetContextDispatches();
			const sapCall = setContextCalls.find((c) => c.args[0] === 'mcpGateway.sapSystemsEnabled');
			assert.ok(sapCall, 'expected setContext dispatch for mcpGateway.sapSystemsEnabled');
			assert.strictEqual(sapCall!.args[1], false);
		});

		it('seeds context key with true when setting is enabled', () => {
			resetMockState();
			_pendingOps.clear();
			mockConfigValues['mcpGateway.sapSystemsEnabled'] = true;
			const ctx = createMockContext();
			const daemon = createMockDaemon({});
			activate(ctx as any, undefined, daemon);
			const sapCall = dispatchedCommands
				.filter((c) => c.id === 'setContext')
				.find((c) => c.args[0] === 'mcpGateway.sapSystemsEnabled');
			assert.ok(sapCall, 'expected setContext dispatch for mcpGateway.sapSystemsEnabled');
			assert.strictEqual(sapCall!.args[1], true);
		});

		it('still registers SAP commands even when view is disabled (palette fallback)', () => {
			// SAP commands must be reachable via the command palette regardless of
			// view visibility — palette is the operator escape hatch when the tab
			// is hidden.
			for (const cmd of [
				'mcpGateway.restartSapVsp',
				'mcpGateway.restartSapGui',
				'mcpGateway.showSapVspLogs',
				'mcpGateway.showSapGuiLogs',
				'mcpGateway.showSapDetail',
				'mcpGateway.addSapSystem',
			]) {
				assert.ok(commands.has(cmd), `Command ${cmd} not registered`);
			}
		});

		// Phase 8 (B-NEW-22) — unified reload-required settings watcher.
		// These keys are read once on activate() and have no live-watcher
		// upstream; flipping them in settings.json must surface a Reload
		// Window toast or the user is left wondering why nothing happened.
		describe('reload-required settings watcher (B-NEW-22)', () => {
			const reloadKeys = [
				'mcpGateway.apiUrl',
				'mcpGateway.pollInterval',
				'mcpGateway.autoStart',
				'mcpGateway.daemonPath',
				'mcpGateway.authTokenPath',
				'mcpGateway.mcpCtlPath',
			];

			for (const key of reloadKeys) {
				it(`shows a Reload Window toast when ${key} changes`, () => {
					const before = mockCalls.infoMessages.length;
					mockConfigValues[key] = 'changed-value';
					fireConfigChange(key);
					assert.ok(
						mockCalls.infoMessages.length > before,
						`expected an info toast after ${key} changed`,
					);
					const last = mockCalls.infoMessages[mockCalls.infoMessages.length - 1];
					const shortName = key.startsWith('mcpGateway.')
						? key.slice('mcpGateway.'.length) : key;
					assert.ok(last.includes(shortName),
						`toast text should reference the changed setting "${shortName}"`);
					assert.ok(/reload/i.test(last),
						'toast text should mention reload');
				});
			}

			it('does NOT show the toast for unrelated settings', () => {
				const before = mockCalls.infoMessages.length;
				// `slashCommandsEnabled` has its own watcher and applies live —
				// it must NOT trigger the reload-required prompt.
				mockConfigValues['mcpGateway.slashCommandsEnabled'] = true;
				fireConfigChange('mcpGateway.slashCommandsEnabled');
				const reloadToasts = mockCalls.infoMessages
					.slice(before)
					.filter((m) => /reload/i.test(m) && /apiUrl|pollInterval|autoStart|daemonPath|authTokenPath|mcpCtlPath/.test(m));
				assert.strictEqual(reloadToasts.length, 0,
					'live-applied settings must not trigger the reload-required toast');
			});

			it('debounces consecutive changes — second change does NOT re-prompt while toast is pending', () => {
				const before = mockCalls.infoMessages.length;
				mockConfigValues['mcpGateway.apiUrl'] = 'http://localhost:9000';
				fireConfigChange('mcpGateway.apiUrl');
				const afterFirst = mockCalls.infoMessages.length;
				mockConfigValues['mcpGateway.pollInterval'] = 7000;
				fireConfigChange('mcpGateway.pollInterval');
				const afterSecond = mockCalls.infoMessages.length;
				assert.strictEqual(afterFirst, before + 1, 'first change should toast once');
				assert.strictEqual(afterSecond, afterFirst,
					'second change while first toast is pending must not re-toast');
			});

			it('reloads window when user picks "Reload Window" action', async () => {
				dialogResponses.showInformationMessage = 'Reload Window';
				mockConfigValues['mcpGateway.apiUrl'] = 'http://localhost:9000';
				fireConfigChange('mcpGateway.apiUrl');
				// Allow the showInformationMessage promise to settle.
				await new Promise((resolve) => setImmediate(resolve));
				const reloaded = dispatchedCommands.find((c) => c.id === 'workbench.action.reloadWindow');
				assert.ok(reloaded, 'expected workbench.action.reloadWindow dispatch when user picks Reload Window');
			});

			it('re-arms after dismissal so a later change re-prompts', async () => {
				dialogResponses.showInformationMessage = undefined; // user dismisses
				mockConfigValues['mcpGateway.apiUrl'] = 'http://localhost:9000';
				fireConfigChange('mcpGateway.apiUrl');
				const beforeSecond = mockCalls.infoMessages.length;
				// Wait for the dismissal `then()` to re-arm the latch.
				await new Promise((resolve) => setImmediate(resolve));
				mockConfigValues['mcpGateway.daemonPath'] = '/usr/local/bin/mcp-gateway';
				fireConfigChange('mcpGateway.daemonPath');
				assert.ok(
					mockCalls.infoMessages.length > beforeSecond,
					'expected a re-prompt after the previous toast was dismissed',
				);
			});
		});

		it('onDidChangeConfiguration re-fires setContext and prompts window reload', () => {
			// Precondition: activate() ran with default false — dispatch history
			// already contains one setContext(..., false) entry from beforeEach.
			const beforeCount = dispatchedCommands
				.filter((c) => c.id === 'setContext')
				.filter((c) => c.args[0] === 'mcpGateway.sapSystemsEnabled').length;
			assert.ok(beforeCount >= 1);

			// Operator flips the setting from false → true in a live window.
			mockConfigValues['mcpGateway.sapSystemsEnabled'] = true;
			fireConfigChange('mcpGateway.sapSystemsEnabled');

			const afterCalls = dispatchedCommands
				.filter((c) => c.id === 'setContext')
				.filter((c) => c.args[0] === 'mcpGateway.sapSystemsEnabled');
			assert.strictEqual(afterCalls.length, beforeCount + 1,
				'expected one additional setContext dispatch after config change');
			assert.strictEqual(afterCalls[afterCalls.length - 1].args[1], true,
				'expected the new setContext call to carry the new value (true)');
			assert.ok(
				mockCalls.infoMessages.some((m) => /reload/i.test(m)),
				'expected an informational toast mentioning reload',
			);
		});
	});

	describe('mcpGateway.refresh', () => {
		it('does not throw when invoked', () => {
			// refresh command calls treeProvider.refresh() which fires an event
			assert.doesNotThrow(() => commands.get('mcpGateway.refresh')!());
		});
	});

	describe('mcpGateway.startServer', () => {
		it('does nothing without item argument', async () => {
			await commands.get('mcpGateway.startServer')!();
			// No error, no calls — silent no-op
			assert.deepStrictEqual(mockCalls.errorMessages, []);
		});
	});

	describe('mcpGateway.stopServer', () => {
		it('does nothing without item argument', async () => {
			await commands.get('mcpGateway.stopServer')!();
			assert.deepStrictEqual(mockCalls.errorMessages, []);
		});
	});

	describe('mcpGateway.restartServer', () => {
		it('does nothing without item argument', async () => {
			await commands.get('mcpGateway.restartServer')!();
			assert.deepStrictEqual(mockCalls.errorMessages, []);
		});
	});

	describe('mcpGateway.resetCircuit', () => {
		it('does nothing without item argument', async () => {
			await commands.get('mcpGateway.resetCircuit')!();
			assert.deepStrictEqual(mockCalls.errorMessages, []);
		});
	});

	describe('mcpGateway.removeServer', () => {
		it('does nothing without item argument', async () => {
			await commands.get('mcpGateway.removeServer')!();
			assert.deepStrictEqual(mockCalls.errorMessages, []);
			assert.deepStrictEqual(mockCalls.warningMessages, []);
		});

		it('shows warning dialog with server name', async () => {
			dialogResponses.showWarningMessage = 'Cancel';
			const item = makeBackendItem('test-server');
			await commands.get('mcpGateway.removeServer')!(item);
			assert.ok(mockCalls.warningMessages.some((m) => m.includes('test-server')));
		});

		it('does not remove on Cancel', async () => {
			dialogResponses.showWarningMessage = 'Cancel';
			const item = makeBackendItem('test-server');
			await commands.get('mcpGateway.removeServer')!(item);
			// Client removeServer would cause a connection error since no real server;
			// but since Cancel was chosen, no HTTP call should be attempted.
			assert.deepStrictEqual(mockCalls.errorMessages, []);
		});
	});

	describe('mcpGateway.openSettings', () => {
		it('dispatches workbench.action.openSettings with extension filter', async () => {
			await commands.get('mcpGateway.openSettings')!();
			const settingsCalls = dispatchedCommands.filter(
				(c) => c.id === 'workbench.action.openSettings',
			);
			assert.equal(settingsCalls.length, 1, 'expected exactly one openSettings dispatch');
			assert.deepStrictEqual(
				settingsCalls[0].args,
				['@ext:mcp-gateway.mcp-gateway-dashboard'],
			);
			assert.deepStrictEqual(mockCalls.errorMessages, []);
		});
	});

	describe('mcpGateway.addServer', () => {
		it('opens the Add Server webview panel (Phase 11.C)', async () => {
			// Phase 11.C: the addServer command no longer shows InputBox —
			// it opens an AddServerPanel webview. Verify the panel is created.
			const before = mockWebviewPanels.length;
			await commands.get('mcpGateway.addServer')!();
			const newPanels = mockWebviewPanels.slice(before);
			assert.equal(newPanels.length, 1, 'expected one new webview panel');
			assert.equal(newPanels[0].viewType, 'mcpAddServer');
			assert.equal(newPanels[0].title, 'Add MCP Server');
			assert.deepStrictEqual(mockCalls.errorMessages, []);
			// Cleanup singleton for subsequent tests.
			const { AddServerPanel } = require('../webview/add-server-panel');
			AddServerPanel._reset();
		});
	});
});

describe('Commands (with injected client)', () => {
	let trackingClient: ReturnType<typeof createTrackingClient>;
	let commands: Map<string, (...args: unknown[]) => unknown>;
	let context: ReturnType<typeof createMockContext>;

	beforeEach(() => {
		resetMockState();
		_pendingOps.clear();
		trackingClient = createTrackingClient();
		context = createMockContext();
		const daemon = createMockDaemon(trackingClient);
		activate(context as any, trackingClient as any, daemon);
		commands = getRegisteredCommands();
	});

	// Success paths — verify correct client calls

	it('startServer calls patchServer with disabled=false', async () => {
		const item = makeBackendItem('my-server', 'disabled');
		await commands.get('mcpGateway.startServer')!(item);
		assert.strictEqual(trackingClient.calls.length, 1);
		assert.strictEqual(trackingClient.calls[0].method, 'patchServer');
		assert.deepStrictEqual(trackingClient.calls[0].args, ['my-server', { disabled: false }]);
		assert.deepStrictEqual(mockCalls.errorMessages, []);
	});

	it('stopServer calls patchServer with disabled=true', async () => {
		const item = makeBackendItem('my-server', 'running');
		await commands.get('mcpGateway.stopServer')!(item);
		assert.strictEqual(trackingClient.calls.length, 1);
		assert.strictEqual(trackingClient.calls[0].method, 'patchServer');
		assert.deepStrictEqual(trackingClient.calls[0].args, ['my-server', { disabled: true }]);
	});

	it('restartServer calls client.restartServer with correct name', async () => {
		const item = makeBackendItem('my-server', 'running');
		await commands.get('mcpGateway.restartServer')!(item);
		assert.strictEqual(trackingClient.calls.length, 1);
		assert.strictEqual(trackingClient.calls[0].method, 'restartServer');
		assert.deepStrictEqual(trackingClient.calls[0].args, ['my-server']);
	});

	it('resetCircuit calls client.resetCircuit with correct name', async () => {
		const item = makeBackendItem('my-server', 'error');
		await commands.get('mcpGateway.resetCircuit')!(item);
		assert.strictEqual(trackingClient.calls.length, 1);
		assert.strictEqual(trackingClient.calls[0].method, 'resetCircuit');
		assert.deepStrictEqual(trackingClient.calls[0].args, ['my-server']);
	});

	it('removeServer calls client.removeServer after confirm', async () => {
		dialogResponses.showWarningMessage = 'Remove';
		const item = makeBackendItem('my-server', 'stopped');
		await commands.get('mcpGateway.removeServer')!(item);
		assert.strictEqual(trackingClient.calls.length, 1);
		assert.strictEqual(trackingClient.calls[0].method, 'removeServer');
		assert.deepStrictEqual(trackingClient.calls[0].args, ['my-server']);
	});

	it('removeServer does not call client on Cancel', async () => {
		dialogResponses.showWarningMessage = 'Cancel';
		const item = makeBackendItem('my-server', 'stopped');
		await commands.get('mcpGateway.removeServer')!(item);
		assert.strictEqual(trackingClient.calls.length, 0);
	});

	// Phase 11.C: the sequential InputBox addServer flow was replaced by
	// AddServerPanel. End-to-end coverage for stdio / http / env / headers lives
	// in src/test/webview/add-server-panel.test.ts. Here we only verify that the
	// command dispatches to the panel — no client.addServer call at command time.
	it('addServer command opens panel without calling client.addServer', async () => {
		const before = trackingClient.calls.length;
		const panelsBefore = mockWebviewPanels.length;
		await commands.get('mcpGateway.addServer')!();
		assert.strictEqual(trackingClient.calls.length, before, 'command must not call client directly');
		assert.strictEqual(mockWebviewPanels.length, panelsBefore + 1, 'command must open exactly one panel');
		const { AddServerPanel } = require('../webview/add-server-panel');
		AddServerPanel._reset();
	});

	// Phase 11.D: SAP command dispatch with SapSystemItem vs SapComponentItem.
	describe('SAP commands (Phase 11.D dispatch)', () => {
		function makeSapSystem(name = 'DEV', hasVsp = true, hasGui = true) {
			const { SapSystemItem } = require('../sap-item');
			return new SapSystemItem({
				key: name,
				sid: name,
				status: 'running',
				vsp: hasVsp ? { name: `vsp-${name}`, status: 'running', transport: 'stdio', restart_count: 0 } : undefined,
				gui: hasGui ? { name: `sap-gui-${name}`, status: 'running', transport: 'http', restart_count: 0 } : undefined,
			});
		}

		function makeSapComponent(systemName: string, kind: 'vsp' | 'gui') {
			const { SapComponentItem } = require('../sap-item');
			const system = {
				key: systemName,
				sid: systemName,
				status: 'running',
				vsp: { name: `vsp-${systemName}`, status: 'running', transport: 'stdio', restart_count: 0 },
				gui: { name: `sap-gui-${systemName}`, status: 'running', transport: 'http', restart_count: 0 },
			};
			const server = kind === 'vsp' ? system.vsp : system.gui;
			return new SapComponentItem(system, kind, server);
		}

		it('restartSapVsp with SapSystemItem calls client.restartServer for VSP', async () => {
			const item = makeSapSystem('DEV');
			await commands.get('mcpGateway.restartSapVsp')!(item);
			assert.strictEqual(trackingClient.calls.length, 1);
			assert.strictEqual(trackingClient.calls[0].method, 'restartServer');
			assert.deepStrictEqual(trackingClient.calls[0].args, ['vsp-DEV']);
		});

		it('restartSapVsp with SapComponentItem (vsp) calls client.restartServer', async () => {
			const item = makeSapComponent('DEV', 'vsp');
			await commands.get('mcpGateway.restartSapVsp')!(item);
			assert.strictEqual(trackingClient.calls.length, 1);
			assert.deepStrictEqual(trackingClient.calls[0].args, ['vsp-DEV']);
		});

		it('restartSapVsp with SapComponentItem (gui) is a no-op (wrong kind)', async () => {
			const item = makeSapComponent('DEV', 'gui');
			await commands.get('mcpGateway.restartSapVsp')!(item);
			assert.strictEqual(trackingClient.calls.length, 0);
		});

		it('restartSapGui with SapComponentItem (gui) calls client.restartServer', async () => {
			const item = makeSapComponent('DEV', 'gui');
			await commands.get('mcpGateway.restartSapGui')!(item);
			assert.strictEqual(trackingClient.calls.length, 1);
			assert.deepStrictEqual(trackingClient.calls[0].args, ['sap-gui-DEV']);
		});

		it('restartSapGui with SapSystemItem calls client.restartServer for GUI', async () => {
			const item = makeSapSystem('DEV');
			await commands.get('mcpGateway.restartSapGui')!(item);
			assert.strictEqual(trackingClient.calls.length, 1);
			assert.deepStrictEqual(trackingClient.calls[0].args, ['sap-gui-DEV']);
		});

		it('restartSapVsp on SapSystemItem without vsp is a no-op', async () => {
			const item = makeSapSystem('DEV', false, true);
			await commands.get('mcpGateway.restartSapVsp')!(item);
			assert.strictEqual(trackingClient.calls.length, 0);
		});

		it('addSapSystem command opens panel without calling client.addServer', async () => {
			const before = trackingClient.calls.length;
			const panelsBefore = mockWebviewPanels.length;
			await commands.get('mcpGateway.addSapSystem')!();
			assert.strictEqual(trackingClient.calls.length, before, 'command must not call client directly');
			assert.strictEqual(mockWebviewPanels.length, panelsBefore + 1, 'command must open exactly one panel');
			const { AddSapPanel } = require('../webview/add-sap-panel');
			AddSapPanel._reset();
		});
	});

	it('removeServer cleans credentials even when daemon fails', async () => {
		// Store a credential first.
		const { CredentialStore } = require('../credential-store');
		const credStore = new CredentialStore(context);
		await credStore.storeEnvVar('fail-server', 'KEY', 'val');

		trackingClient.shouldFail = true;
		dialogResponses.showWarningMessage = 'Remove';
		const item = makeBackendItem('fail-server', 'stopped');
		await commands.get('mcpGateway.removeServer')!(item);

		// Daemon call failed, but credentials should still be cleaned.
		assert.ok(mockCalls.errorMessages.length > 0);
		const creds = await credStore.getServerCredentials('fail-server');
		assert.deepStrictEqual(creds, { env: {}, headers: {} });
	});

	it('removeServer cleans credentials when daemon succeeds', async () => {
		const { CredentialStore } = require('../credential-store');
		const credStore = new CredentialStore(context);
		await credStore.storeEnvVar('my-server', 'KEY', 'val');

		dialogResponses.showWarningMessage = 'Remove';
		const item = makeBackendItem('my-server', 'stopped');
		await commands.get('mcpGateway.removeServer')!(item);

		assert.strictEqual(trackingClient.calls.length, 1);
		assert.strictEqual(trackingClient.calls[0].method, 'removeServer');
		const creds = await credStore.getServerCredentials('my-server');
		assert.deepStrictEqual(creds, { env: {}, headers: {} });
	});

	// Error paths — verify error messages shown

	it('startServer shows error on client failure', async () => {
		trackingClient.shouldFail = true;
		trackingClient.failMessage = 'connection refused';
		const item = makeBackendItem('fail-server', 'disabled');
		await commands.get('mcpGateway.startServer')!(item);
		assert.ok(mockCalls.errorMessages.length > 0, 'Expected error message');
		assert.ok(mockCalls.errorMessages[0].includes('Failed to enable server'));
	});

	it('stopServer shows error on client failure', async () => {
		trackingClient.shouldFail = true;
		const item = makeBackendItem('fail-server', 'running');
		await commands.get('mcpGateway.stopServer')!(item);
		assert.ok(mockCalls.errorMessages.length > 0);
		assert.ok(mockCalls.errorMessages[0].includes('Failed to disable server'));
	});

	it('restartServer shows error on client failure', async () => {
		trackingClient.shouldFail = true;
		const item = makeBackendItem('fail-server', 'running');
		await commands.get('mcpGateway.restartServer')!(item);
		assert.ok(mockCalls.errorMessages.length > 0);
		assert.ok(mockCalls.errorMessages[0].includes('Failed to restart server'));
	});

	it('resetCircuit shows error on client failure', async () => {
		trackingClient.shouldFail = true;
		const item = makeBackendItem('fail-server', 'error');
		await commands.get('mcpGateway.resetCircuit')!(item);
		assert.ok(mockCalls.errorMessages.length > 0);
		assert.ok(mockCalls.errorMessages[0].includes('Failed to reset circuit'));
	});

	it('removeServer shows error on client failure after confirm', async () => {
		trackingClient.shouldFail = true;
		dialogResponses.showWarningMessage = 'Remove';
		const item = makeBackendItem('fail-server', 'stopped');
		await commands.get('mcpGateway.removeServer')!(item);
		assert.ok(mockCalls.errorMessages.length > 0);
		assert.ok(mockCalls.errorMessages[0].includes('Failed to remove server'));
	});

	// Concurrency guard (D1 fix)

	it('blocks concurrent operations on the same server', async () => {
		// Make the client slow
		let resolveCall: () => void;
		const slowPromise = new Promise<void>((r) => { resolveCall = r; });
		const originalPatch = trackingClient.patchServer;
		trackingClient.patchServer = async (name: string, patch: unknown) => {
			trackingClient.calls.push({ method: 'patchServer', args: [name, patch] });
			await slowPromise;
			return { status: 'ok' };
		};

		const item = makeBackendItem('my-server', 'running');
		const p1 = commands.get('mcpGateway.stopServer')!(item);
		const p2 = commands.get('mcpGateway.stopServer')!(item);
		resolveCall!();
		await Promise.all([p1, p2]);
		// Only one call should have gone through
		assert.strictEqual(trackingClient.calls.length, 1);
	});

	// Stub command tests

	it('showLogs does not throw when invoked', async () => {
		const item = makeBackendItem('my-server');
		// showLogs now opens a LogViewer SSE connection; no real server, but should not crash.
		await commands.get('mcpGateway.showLogs')!(item);
		// Verify an output channel was created for the server.
		assert.ok(mockOutputChannels.some((ch) => ch.name === 'MCP: my-server'));
	});

	it('startDaemon shows info message', async () => {
		await commands.get('mcpGateway.startDaemon')!();
		assert.ok(mockCalls.infoMessages.length > 0);
	});

	it('stopDaemon shows info message', async () => {
		// B-06 fix: stopDaemon now probes getHealth first. The shared trackingClient
		// resolves getHealth (daemon is "reachable"), calls shutdown (which flips
		// daemonShutDown=true), then the poll-until-unreachable loop exits immediately
		// because subsequent getHealth calls reject. Result: "stopped" toast.
		await commands.get('mcpGateway.stopDaemon')!();
		assert.ok(
			mockCalls.infoMessages.some((m) => m.includes('stopped')),
			`expected "stopped" info toast, got: ${JSON.stringify(mockCalls.infoMessages)}`,
		);
	});
});

// ── Phase 3 tests: B-06 (stopDaemon REST-first) + B-07 (startDaemon try/catch) ──

describe('mcpGateway.stopDaemon (Phase 3 — REST-first)', () => {
	// Each test builds its own client + daemon so it can control exactly how
	// getHealth / shutdown behave without interfering with the shared beforeEach.

	function buildSetup(opts: {
		healthRejects?: boolean;
		shutdownRejects?: boolean;
		shutdownRejectWith?: Error;
		daemonRunning?: boolean;
	}) {
		resetMockState();
		_pendingOps.clear();
		// Disable autoStart so activate() does NOT consume getHealth calls
		// before the test invokes stopDaemon.
		mockConfigValues['mcpGateway.autoStart'] = false;

		const daemonStopCalls: number[] = [];

		// Minimal spawn that records stop() calls via kill().
		const trackingSpawn: SpawnFn = (() => {
			const { EventEmitter } = require('node:events');
			const child = new EventEmitter();
			child.stdout = new EventEmitter();
			child.stderr = new EventEmitter();
			child.pid = 42;
			child.killed = false;
			child.kill = () => {
				child.killed = true;
				daemonStopCalls.push(1);
				child.emit('exit', null, 'SIGTERM');
				return true;
			};
			return child;
		}) as unknown as SpawnFn;

		const healthResponse = { status: 'ok', servers: 0, running: 0 };
		const shutdownResponse = { status: 'ok' };

		const client = {
			getHealth: async () => {
				if (opts.healthRejects) { throw new Error('ECONNREFUSED'); }
				return healthResponse;
			},
			shutdown: async () => {
				if (opts.shutdownRejects) {
					throw opts.shutdownRejectWith ?? new Error('shutdown failed');
				}
				return shutdownResponse;
			},
			listServers: async () => [],
			getServer: async () => ({}),
			addServer: async () => ({ status: 'ok' }),
			removeServer: async () => ({ status: 'ok' }),
			patchServer: async () => ({ status: 'ok' }),
			restartServer: async () => ({ status: 'ok' }),
			resetCircuit: async () => ({ status: 'ok' }),
			callTool: async () => ({ content: null }),
			listTools: async () => [],
		};

		// Use an output channel stub (DaemonManager requires one for legacy compat).
		const outputStub = {
			name: 'test', lines: [] as string[], disposed: false,
			appendLine() {}, append() {}, clear() {}, show() {}, hide() {}, dispose() {},
		};
		const daemon = new DaemonManager(client as any, '', outputStub as any, trackingSpawn);

		// Simulate an extension-owned child by calling start() when daemonRunning=true.
		// We do NOT actually await start() here — we'll let tests control the flow.
		// Instead, directly patch the running property via a guard flag:
		// DaemonManager.running = child !== undefined && !stopping.
		// The simplest way is to trigger the spawn path: set a fake health rejection
		// then start(), which will spawn because getHealth throws initially.
		// But we need start() to NOT make a real network call, so re-use the client.
		// For daemonRunning=false: just don't call start(). daemon.running will be false.

		const ctx = createMockContext();
		activate(ctx as any, client as any, daemon);
		const commands = getRegisteredCommands();

		return { commands, daemonStopCalls, daemon, client };
	}

	it('(a) extension owns child + REST shutdown succeeds → toast "stopped", refresh triggered (Phase 9: SIGTERM is fallback-only)', async () => {
		resetMockState();
		_pendingOps.clear();
		mockConfigValues['mcpGateway.autoStart'] = false;

		const shutdownCalls: string[] = [];
		const daemonStopCalls: number[] = [];

		const trackingSpawn2: SpawnFn = (() => {
			const { EventEmitter } = require('node:events');
			const child = new EventEmitter();
			child.stdout = new EventEmitter();
			child.stderr = new EventEmitter();
			child.pid = 42;
			child.killed = false;
			child.kill = () => {
				child.killed = true;
				daemonStopCalls.push(1);
				child.emit('exit', null, 'SIGTERM');
				return true;
			};
			return child;
		}) as unknown as SpawnFn;

		// healthMode controls what getHealth returns:
		// 'reject' = throws (used during daemon.start() fast-path and post-shutdown polls)
		// 'resolve' = returns health (used for the stopDaemon reachability probe)
		let healthMode: 'reject' | 'resolve' = 'reject';
		const client = {
			getHealth: async () => {
				if (healthMode === 'reject') { throw new Error('ECONNREFUSED'); }
				return { status: 'ok', servers: 0, running: 0 };
			},
			shutdown: async () => { shutdownCalls.push('shutdown'); return { status: 'ok' }; },
			listServers: async () => [], getServer: async () => ({}),
			addServer: async () => ({ status: 'ok' }), removeServer: async () => ({ status: 'ok' }),
			patchServer: async () => ({ status: 'ok' }), restartServer: async () => ({ status: 'ok' }),
			resetCircuit: async () => ({ status: 'ok' }), callTool: async () => ({ content: null }),
			listTools: async () => [],
		};

		const outputStub = {
			name: 'test', lines: [] as string[], disposed: false,
			appendLine() {}, append() {}, clear() {}, show() {}, hide() {}, dispose() {},
		};
		const daemon = new DaemonManager(client as any, '', outputStub as any, trackingSpawn2);

		// Spawn so daemon.running = true. getHealth rejects during start() fast-path.
		await daemon.start();
		assert.ok(daemon.running, 'daemon should be running after start()');

		// Activate with autoStart=false. cache.startAutoRefresh() fires immediately but
		// getHealth still rejects — cache logs error and continues. That's fine.
		const ctx = createMockContext();
		activate(ctx as any, client as any, daemon);
		const commands = getRegisteredCommands();

		// Switch to resolve mode BEFORE invoking stopDaemon so the reachability probe sees
		// a live daemon. Polls after shutdown() will reject (healthMode switches back below).
		healthMode = 'resolve';

		// Intercept shutdown to flip healthMode back to reject, simulating daemon exit.
		const origShutdown = client.shutdown;
		client.shutdown = async () => {
			const result = await origShutdown();
			healthMode = 'reject'; // daemon is gone after shutdown
			return result;
		};

		await commands.get('mcpGateway.stopDaemon')!();

		assert.ok(shutdownCalls.length > 0, 'shutdown must be called');
		// Phase 9 (B-NEW-25): with REST-first stop, SIGTERM is a fallback-only path.
		// When REST shutdown succeeds and the daemon is unreachable, no SIGTERM is
		// sent — the daemon's own signal handler runs to completion. The local
		// child handle is cleaned up by the 'exit' handler when the real daemon
		// process exits (mock here doesn't emit exit on shutdown, but production
		// would). What matters for this test: shutdown was called, no error toast.
		assert.strictEqual(daemonStopCalls.length, 0, 'SIGTERM fallback should NOT fire when REST shutdown succeeded (B-NEW-25 fix)');
		assert.ok(
			mockCalls.infoMessages.some((m) => m.includes('stopped')),
			`expected "stopped" info toast, got: ${JSON.stringify(mockCalls.infoMessages)}`,
		);
		assert.deepStrictEqual(mockCalls.errorMessages, []);
	});

	it('(b) extension does NOT own child + REST shutdown succeeds → toast "stopped", no daemon.stop() call', async () => {
		resetMockState();
		_pendingOps.clear();
		mockConfigValues['mcpGateway.autoStart'] = false;

		const daemonStopCalls: number[] = [];
		const trackingSpawn3: SpawnFn = (() => {
			const { EventEmitter } = require('node:events');
			const child = new EventEmitter();
			child.stdout = new EventEmitter();
			child.stderr = new EventEmitter();
			child.pid = 42;
			child.killed = false;
			child.kill = () => {
				child.killed = true;
				daemonStopCalls.push(1);
				child.emit('exit', null, 'SIGTERM');
				return true;
			};
			return child;
		}) as unknown as SpawnFn;

		// healthMode: resolves for the reachability probe, rejects for polls.
		let healthMode: 'resolve' | 'reject' = 'resolve';
		const client = {
			getHealth: async () => {
				if (healthMode === 'reject') { throw new Error('ECONNREFUSED'); }
				return { status: 'ok', servers: 0, running: 0 };
			},
			shutdown: async () => {
				healthMode = 'reject'; // daemon exits after shutdown
				return { status: 'ok' };
			},
			listServers: async () => [], getServer: async () => ({}),
			addServer: async () => ({ status: 'ok' }), removeServer: async () => ({ status: 'ok' }),
			patchServer: async () => ({ status: 'ok' }), restartServer: async () => ({ status: 'ok' }),
			resetCircuit: async () => ({ status: 'ok' }), callTool: async () => ({ content: null }),
			listTools: async () => [],
		};

		const outputStub = {
			name: 'test', lines: [] as string[], disposed: false,
			appendLine() {}, append() {}, clear() {}, show() {}, hide() {}, dispose() {},
		};
		const daemon = new DaemonManager(client as any, '', outputStub as any, trackingSpawn3);
		// daemon.running is false — no start() called, no child handle.
		assert.ok(!daemon.running, 'daemon must NOT be running for this test');

		const ctx = createMockContext();
		activate(ctx as any, client as any, daemon);
		const commands = getRegisteredCommands();

		// Note: activate() calls cache.startAutoRefresh() which immediately calls
		// cache.refresh() → client.getHealth(). With healthMode='resolve', this will
		// resolve but then we need the stopDaemon reachability probe to also resolve.
		// The cache.refresh() is async (fire-and-forget), so by the time we call
		// stopDaemon below it may or may not have consumed the first resolve.
		// We keep healthMode='resolve' throughout until shutdown() flips it to 'reject',
		// so both cache.refresh() AND the stopDaemon probe correctly see a live daemon.

		await commands.get('mcpGateway.stopDaemon')!();

		assert.strictEqual(daemonStopCalls.length, 0, 'daemon.stop() must NOT be called (no child ownership)');
		assert.ok(
			mockCalls.infoMessages.some((m) => m.includes('stopped')),
			`expected "stopped" info toast, got: ${JSON.stringify(mockCalls.infoMessages)}`,
		);
		assert.deepStrictEqual(mockCalls.errorMessages, []);
	});

	it('(c) daemon completely unreachable → "No daemon process to stop" info toast, no error', async () => {
		const { commands } = buildSetup({ healthRejects: true });

		await commands.get('mcpGateway.stopDaemon')!();

		assert.ok(
			mockCalls.infoMessages.some((m) => m.includes('No daemon')),
			`expected "No daemon" toast, got: ${JSON.stringify(mockCalls.infoMessages)}`,
		);
		assert.deepStrictEqual(mockCalls.errorMessages, []);
	});

	it('(d) REST shutdown rejects with kind:"auth" (401) → error toast mentioning --refresh-token', async () => {
		const { GatewayError } = require('../gateway-client');
		const authErr = new GatewayError('auth', 'HTTP 401', 401, '{"error":"Unauthorized"}');

		const { commands } = buildSetup({
			healthRejects: false,
			shutdownRejects: true,
			shutdownRejectWith: authErr,
		});

		await commands.get('mcpGateway.stopDaemon')!();

		assert.ok(
			mockCalls.errorMessages.some((m) => m.includes('--refresh-token')),
			`expected error toast with --refresh-token hint, got: ${JSON.stringify(mockCalls.errorMessages)}`,
		);
		assert.ok(
			mockCalls.infoMessages.every((m) => !m.includes('stopped')),
			'must NOT show "stopped" toast on auth failure',
		);
	});
});

describe('mcpGateway.startDaemon (Phase 3 — B-07 try/catch)', () => {
	it('(e) daemon.start() throws → showErrorMessage called with "Start daemon failed:" prefix', async () => {
		// Use a mock daemon object (not DaemonManager) so start() actually throws.
		// DaemonManager.start() absorbs spawn errors internally; the B-07 fix wraps
		// the call site for cases where start() itself propagates an error.
		resetMockState();
		_pendingOps.clear();

		const throwingDaemon = {
			start: async (): Promise<boolean> => {
				throw new Error('ENOENT: mcp-gateway not found');
			},
			stop: () => {},
			restart: async () => false,
			dispose: () => {},
			get running() { return false; },
		};

		const client = {
			getHealth: async () => ({ status: 'ok', servers: 0, running: 0 }),
			shutdown: async () => ({ status: 'ok' }),
			listServers: async () => [], getServer: async () => ({}),
			addServer: async () => ({ status: 'ok' }), removeServer: async () => ({ status: 'ok' }),
			patchServer: async () => ({ status: 'ok' }), restartServer: async () => ({ status: 'ok' }),
			resetCircuit: async () => ({ status: 'ok' }), callTool: async () => ({ content: null }),
			listTools: async () => [],
		};

		const ctx = createMockContext();
		activate(ctx as any, client as any, throwingDaemon as any);
		const commands = getRegisteredCommands();

		await commands.get('mcpGateway.startDaemon')!();

		assert.ok(
			mockCalls.errorMessages.some((m) => m.startsWith('Start daemon failed:')),
			`expected error toast with "Start daemon failed:" prefix, got: ${JSON.stringify(mockCalls.errorMessages)}`,
		);
		assert.ok(
			mockCalls.errorMessages.some((m) => m.includes('ENOENT')),
			`expected error message to include ENOENT, got: ${JSON.stringify(mockCalls.errorMessages)}`,
		);
	});
});

describe('validateServerName', () => {
	it('accepts valid names', () => {
		assert.strictEqual(validateServerName('my-server'), null);
		assert.strictEqual(validateServerName('server_1'), null);
		assert.strictEqual(validateServerName('A'), null);
		assert.strictEqual(validateServerName('abc123'), null);
	});

	it('rejects empty input', () => {
		assert.ok(validateServerName('') !== null);
		assert.ok(validateServerName('   ') !== null);
	});

	it('rejects names with path separators', () => {
		assert.ok(validateServerName('../evil') !== null);
		assert.ok(validateServerName('a/b') !== null);
	});

	it('rejects names starting with hyphen', () => {
		assert.ok(validateServerName('-bad') !== null);
	});

	it('rejects names with special characters', () => {
		assert.ok(validateServerName('a?b') !== null);
		assert.ok(validateServerName('a#b') !== null);
		assert.ok(validateServerName('a b') !== null);
	});

	it('rejects names longer than 64 chars', () => {
		assert.ok(validateServerName('a'.repeat(65)) !== null);
	});

	it('accepts exactly 64-char name', () => {
		assert.strictEqual(validateServerName('a'.repeat(64)), null);
	});
});

describe('validateUrl', () => {
	it('accepts valid http URLs', () => {
		assert.strictEqual(validateUrl('http://localhost:3000'), null);
		assert.strictEqual(validateUrl('https://example.com/mcp'), null);
	});

	it('rejects empty input', () => {
		assert.ok(validateUrl('') !== null);
		assert.ok(validateUrl('   ') !== null);
	});

	it('rejects non-http schemes', () => {
		assert.ok(validateUrl('ftp://example.com') !== null);
		assert.ok(validateUrl('javascript:alert(1)') !== null);
		assert.ok(validateUrl('file:///etc/passwd') !== null);
	});

	it('rejects invalid URL format', () => {
		assert.ok(validateUrl('not-a-url') !== null);
		assert.ok(validateUrl('://missing-scheme') !== null);
	});
});
