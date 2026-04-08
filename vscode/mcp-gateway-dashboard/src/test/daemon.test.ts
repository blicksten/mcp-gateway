import { resetMockState, mockCalls, type MockOutputChannel } from './mock-vscode';

import * as assert from 'node:assert';
import { describe, it, beforeEach, afterEach } from 'mocha';
import { EventEmitter } from 'node:events';
import { DaemonManager, type SpawnFn } from '../daemon';
import type { IGatewayClient } from '../extension';
import type { ChildProcess } from 'node:child_process';

// Minimal mock client — only getHealth() is used by DaemonManager.
function createMockClient(online = false): IGatewayClient & { online: boolean } {
	const mock = {
		online,
		getHealth: async () => {
			if (!mock.online) { throw new Error('connection refused'); }
			return { status: 'ok', servers: 0, running: 0 };
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
	return mock;
}

/** Create a mock OutputChannel for injection. */
function createMockOutputChannel(): MockOutputChannel {
	return {
		name: 'MCP Gateway',
		lines: [],
		disposed: false,
		appendLine(line: string) { this.lines.push(line); },
		append(text: string) { this.lines.push(text); },
		clear() { this.lines.length = 0; },
		show() {},
		hide() {},
		dispose() { this.disposed = true; },
	};
}

/** Create a mock ChildProcess backed by EventEmitter. */
function createMockChild(): ChildProcess {
	const child = new EventEmitter() as unknown as ChildProcess & EventEmitter;
	(child as any).stdout = new EventEmitter();
	(child as any).stderr = new EventEmitter();
	(child as any).pid = 12345;
	(child as any).killed = false;
	(child as any).kill = (signal?: string) => {
		(child as any).killed = true;
		(child as any).emit('exit', null, signal ?? 'SIGTERM');
		return true;
	};
	return child;
}

describe('DaemonManager', () => {
	let daemon: DaemonManager;
	let client: ReturnType<typeof createMockClient>;
	let output: MockOutputChannel;
	let mockChild: ChildProcess & { killed: boolean };
	let mockSpawn: SpawnFn;
	let lastSpawnCmd: string;
	let spawnCount: number;

	beforeEach(() => {
		resetMockState();
		client = createMockClient(false);
		output = createMockOutputChannel();
		mockChild = createMockChild() as ChildProcess & { killed: boolean };
		lastSpawnCmd = '';
		spawnCount = 0;
		mockSpawn = ((cmd: string) => {
			lastSpawnCmd = cmd;
			spawnCount++;
			return mockChild;
		}) as unknown as SpawnFn;
	});

	afterEach(() => {
		if (daemon) { daemon.dispose(); }
	});

	describe('start', () => {
		it('spawns when gateway is offline', async () => {
			daemon = new DaemonManager(client as any, 'mcp-gateway', output as any, mockSpawn);
			const result = await daemon.start();
			assert.strictEqual(result, true);
			assert.ok(output.lines.some((l) => l.includes('Starting: mcp-gateway')));
		});

		it('skips spawn when gateway is already running', async () => {
			client.online = true;
			daemon = new DaemonManager(client as any, 'mcp-gateway', output as any, mockSpawn);
			const result = await daemon.start();
			assert.strictEqual(result, false);
			assert.ok(output.lines.some((l) => l.includes('already running')));
		});

		it('returns false if already spawned', async () => {
			daemon = new DaemonManager(client as any, 'mcp-gateway', output as any, mockSpawn);
			await daemon.start();
			const result = await daemon.start();
			assert.strictEqual(result, false);
		});

		it('returns false after dispose', async () => {
			daemon = new DaemonManager(client as any, 'mcp-gateway', output as any, mockSpawn);
			daemon.dispose();
			const result = await daemon.start();
			assert.strictEqual(result, false);
		});

		it('uses custom daemon path when provided', async () => {
			daemon = new DaemonManager(client as any, '/usr/local/bin/mcp-gateway', output as any, mockSpawn);
			await daemon.start();
			assert.strictEqual(lastSpawnCmd, '/usr/local/bin/mcp-gateway');
		});

		it('falls back to mcp-gateway when path is empty', async () => {
			daemon = new DaemonManager(client as any, '', output as any, mockSpawn);
			await daemon.start();
			assert.strictEqual(lastSpawnCmd, 'mcp-gateway');
		});

		it('captures stdout to output channel', async () => {
			daemon = new DaemonManager(client as any, 'mcp-gateway', output as any, mockSpawn);
			await daemon.start();
			(mockChild as any).stdout.emit('data', Buffer.from('server started on :8765\n'));
			assert.ok(output.lines.some((l) => l.includes('server started on :8765')));
		});

		it('captures stderr with prefix', async () => {
			daemon = new DaemonManager(client as any, 'mcp-gateway', output as any, mockSpawn);
			await daemon.start();
			(mockChild as any).stderr.emit('data', Buffer.from('warning: slow backend\n'));
			assert.ok(output.lines.some((l) => l.includes('[stderr]') && l.includes('warning: slow backend')));
		});

		it('second concurrent start() is a no-op (D6-01 fix)', async () => {
			daemon = new DaemonManager(client as any, 'mcp-gateway', output as any, mockSpawn);
			const [r1, r2] = await Promise.all([daemon.start(), daemon.start()]);
			// Only one should have spawned
			assert.strictEqual(spawnCount, 1);
			assert.ok((r1 && !r2) || (!r1 && r2), 'exactly one call should return true');
		});

		it('does not spawn if disposed during getHealth (race guard)', async () => {
			let resolveHealth!: () => void;
			const slowClient = {
				...client,
				getHealth: () => new Promise<never>((_, rej) => {
					resolveHealth = () => rej(new Error('offline'));
				}),
			};
			daemon = new DaemonManager(slowClient as any, 'mcp-gateway', output as any, mockSpawn);
			const startPromise = daemon.start();
			daemon.dispose();
			resolveHealth();
			const result = await startPromise;
			assert.strictEqual(result, false, 'start() must return false after dispose');
			assert.strictEqual(spawnCount, 0, 'must not spawn into disposed manager');
		});

		it('handles spawn error gracefully', async () => {
			const failSpawn = (() => { throw new Error('ENOENT'); }) as unknown as SpawnFn;
			daemon = new DaemonManager(client as any, '/bad/path', output as any, failSpawn);
			const result = await daemon.start();
			assert.strictEqual(result, false);
			assert.ok(output.lines.some((l) => l.includes('Failed to spawn')));
		});
	});

	describe('stop', () => {
		it('sends SIGTERM to child process', async () => {
			daemon = new DaemonManager(client as any, 'mcp-gateway', output as any, mockSpawn);
			await daemon.start();
			assert.strictEqual(daemon.running, true);
			daemon.stop();
			assert.ok(mockChild.killed);
			assert.ok(output.lines.some((l) => l.includes('Stopping')));
		});

		it('is a no-op when not running', () => {
			daemon = new DaemonManager(client as any, 'mcp-gateway', output as any, mockSpawn);
			daemon.stop(); // Should not throw
			assert.strictEqual(daemon.running, false);
		});

		it('clears child reference on exit', async () => {
			daemon = new DaemonManager(client as any, 'mcp-gateway', output as any, mockSpawn);
			await daemon.start();
			assert.strictEqual(daemon.running, true);
			(mockChild as any).emit('exit', 0, null);
			assert.strictEqual(daemon.running, false);
		});

		it('running is false during stopping (D6-02 fix)', async () => {
			// Use a non-auto-exit kill to observe stopping state
			const child = new EventEmitter() as any;
			child.stdout = new EventEmitter();
			child.stderr = new EventEmitter();
			child.pid = 99;
			child.killed = false;
			child.kill = () => { child.killed = true; return true; }; // no auto exit
			const spawn = (() => child) as unknown as SpawnFn;
			daemon = new DaemonManager(client as any, 'mcp-gateway', output as any, spawn);
			await daemon.start();
			assert.strictEqual(daemon.running, true);
			daemon.stop();
			assert.strictEqual(daemon.running, false, 'running should be false while stopping');
			// Simulate eventual exit
			child.emit('exit', 0, null);
		});

		it('error event clears stopping when exit does not fire (D7-02 fix)', async () => {
			const child = new EventEmitter() as any;
			child.stdout = new EventEmitter();
			child.stderr = new EventEmitter();
			child.pid = 99;
			child.killed = false;
			child.kill = () => { child.killed = true; return true; }; // no auto exit
			const spawn = (() => child) as unknown as SpawnFn;
			daemon = new DaemonManager(client as any, 'mcp-gateway', output as any, spawn);
			await daemon.start();
			daemon.stop();
			assert.strictEqual(daemon.running, false, 'stopping — running is false');
			// Simulate error without exit (e.g., orphaned error event)
			child.emit('error', new Error('EPIPE'));
			// stopping should be cleared by the error handler
			// child is now undefined, so running is false regardless, but stopping must be false
			// to allow a fresh start() cycle
			const result = await daemon.start();
			assert.strictEqual(result, true, 'start() should succeed after error clears stopping');
		});

		it('second stop() is a no-op while stopping (D6-02 fix)', async () => {
			const child = new EventEmitter() as any;
			child.stdout = new EventEmitter();
			child.stderr = new EventEmitter();
			child.pid = 99;
			child.killed = false;
			let killCount = 0;
			child.kill = () => { killCount++; child.killed = true; return true; };
			const spawn = (() => child) as unknown as SpawnFn;
			daemon = new DaemonManager(client as any, 'mcp-gateway', output as any, spawn);
			await daemon.start();
			daemon.stop();
			daemon.stop();
			assert.strictEqual(killCount, 1, 'kill should only be called once');
			child.emit('exit', 0, null);
		});
	});

	describe('running', () => {
		it('is false before start', () => {
			daemon = new DaemonManager(client as any, 'mcp-gateway', output as any, mockSpawn);
			assert.strictEqual(daemon.running, false);
		});

		it('is true after successful start', async () => {
			daemon = new DaemonManager(client as any, 'mcp-gateway', output as any, mockSpawn);
			await daemon.start();
			assert.strictEqual(daemon.running, true);
		});

		it('becomes false after process error', async () => {
			daemon = new DaemonManager(client as any, 'mcp-gateway', output as any, mockSpawn);
			await daemon.start();
			(mockChild as any).emit('error', new Error('EPIPE'));
			assert.strictEqual(daemon.running, false);
			assert.ok(output.lines.some((l) => l.includes('Process error: EPIPE')));
		});
	});

	describe('dispose', () => {
		it('stops child and preserves injected output channel (D6-03 fix)', async () => {
			daemon = new DaemonManager(client as any, 'mcp-gateway', output as any, mockSpawn);
			await daemon.start();
			daemon.dispose();
			assert.strictEqual(mockChild.killed, true);
			assert.strictEqual(output.disposed, false, 'injected channel should not be disposed');
		});

		it('preserves injected output channel even without child (D6-03 fix)', () => {
			daemon = new DaemonManager(client as any, 'mcp-gateway', output as any, mockSpawn);
			daemon.dispose();
			assert.strictEqual(output.disposed, false, 'injected channel should not be disposed');
		});

		it('double dispose is safe', async () => {
			daemon = new DaemonManager(client as any, 'mcp-gateway', output as any, mockSpawn);
			await daemon.start();
			daemon.dispose();
			daemon.dispose(); // Should not throw
		});

		it('removes stdout/stderr listeners on dispose', async () => {
			daemon = new DaemonManager(client as any, 'mcp-gateway', output as any, mockSpawn);
			await daemon.start();
			daemon.dispose();
			const linesBefore = output.lines.length;
			// Data after dispose should not reach the output channel
			(mockChild as any).stdout.emit('data', Buffer.from('ghost\n'));
			assert.strictEqual(output.lines.length, linesBefore, 'no stdout output after dispose');
			(mockChild as any).stderr.emit('data', Buffer.from('ghost-err\n'));
			assert.strictEqual(output.lines.length, linesBefore, 'no stderr output after dispose');
		});
	});

	describe('exit logging', () => {
		it('logs exit code', async () => {
			daemon = new DaemonManager(client as any, 'mcp-gateway', output as any, mockSpawn);
			await daemon.start();
			(mockChild as any).emit('exit', 1, null);
			assert.ok(output.lines.some((l) => l.includes('code 1')));
		});

		it('logs exit signal', async () => {
			daemon = new DaemonManager(client as any, 'mcp-gateway', output as any, mockSpawn);
			await daemon.start();
			(mockChild as any).emit('exit', null, 'SIGKILL');
			assert.ok(output.lines.some((l) => l.includes('signal SIGKILL')));
		});
	});
});

describe('Daemon commands (integration)', () => {
	let client: ReturnType<typeof createMockClient>;
	let output: MockOutputChannel;
	let mockSpawn: SpawnFn;

	beforeEach(() => {
		resetMockState();
		client = createMockClient(false);
		output = createMockOutputChannel();
		mockSpawn = (() => createMockChild()) as unknown as SpawnFn;
	});

	it('startDaemon command spawns and shows message', async () => {
		const { activate } = await import('../extension');
		const { getRegisteredCommands } = await import('./mock-vscode');
		const subs: { dispose(): void }[] = [];
		const daemon = new DaemonManager(client as any, '', output as any, mockSpawn);
		activate({ subscriptions: subs } as any, client as any, daemon);

		// Wait for auto-start to complete (fires on activate), then stop so command can re-start.
		await new Promise((r) => setTimeout(r, 10));
		daemon.stop();

		const cmd = getRegisteredCommands().get('mcpGateway.startDaemon');
		assert.ok(cmd, 'startDaemon command should be registered');
		await cmd();

		assert.ok(mockCalls.infoMessages.some((m) => m.includes('started')));

		for (const s of subs) { s.dispose(); }
	});

	it('startDaemon shows already-running when gateway online', async () => {
		client.online = true;
		const { activate } = await import('../extension');
		const { getRegisteredCommands } = await import('./mock-vscode');
		const subs: { dispose(): void }[] = [];
		const daemon = new DaemonManager(client as any, '', output as any, mockSpawn);
		activate({ subscriptions: subs } as any, client as any, daemon);

		const cmd = getRegisteredCommands().get('mcpGateway.startDaemon');
		await cmd!();

		assert.ok(mockCalls.infoMessages.some((m) => m.includes('already running')));

		for (const s of subs) { s.dispose(); }
	});

	it('stopDaemon command stops and shows message', async () => {
		const { activate } = await import('../extension');
		const { getRegisteredCommands } = await import('./mock-vscode');
		const subs: { dispose(): void }[] = [];
		const daemon = new DaemonManager(client as any, '', output as any, mockSpawn);
		activate({ subscriptions: subs } as any, client as any, daemon);

		await daemon.start();
		assert.strictEqual(daemon.running, true);

		const cmd = getRegisteredCommands().get('mcpGateway.stopDaemon');
		assert.ok(cmd);
		await cmd();

		assert.ok(mockCalls.infoMessages.some((m) => m.includes('stopped')));

		for (const s of subs) { s.dispose(); }
	});

	it('stopDaemon shows no-process message when not running', async () => {
		const { activate } = await import('../extension');
		const { getRegisteredCommands } = await import('./mock-vscode');
		const subs: { dispose(): void }[] = [];
		const daemon = new DaemonManager(client as any, '', output as any, mockSpawn);
		activate({ subscriptions: subs } as any, client as any, daemon);

		const cmd = getRegisteredCommands().get('mcpGateway.stopDaemon');
		await cmd!();

		assert.ok(mockCalls.infoMessages.some((m) => m.includes('No daemon')));

		for (const s of subs) { s.dispose(); }
	});
});
