// Import mock BEFORE production modules (CommonJS require order).
import { dialogResponses, mockCalls, mockOutputChannels, mockWebviewPanels, resetMockState, getRegisteredCommands, MockSecretStorage, MockMemento } from './mock-vscode';

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

	const client = {
		calls,
		set shouldFail(v: boolean) { shouldFail = v; },
		set failMessage(v: string) { failMessage = v; },
		listServers: async () => [],
		getHealth: async () => ({ status: 'ok', servers: 0, running: 0 }),
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
			'mcpGateway.resetCircuit',
			'mcpGateway.showLogs',
			'mcpGateway.startDaemon',
			'mcpGateway.stopDaemon',
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
		await commands.get('mcpGateway.stopDaemon')!();
		assert.ok(mockCalls.infoMessages.some((m) => m.includes('No daemon')));
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
