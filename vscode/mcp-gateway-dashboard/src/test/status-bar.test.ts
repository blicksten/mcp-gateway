import { mockCalls, resetMockState, mockStatusBarItems, type MockStatusBarItem } from './mock-vscode';

import * as assert from 'node:assert';
import { describe, it, beforeEach, afterEach } from 'mocha';
import { McpStatusBar } from '../status-bar';
import type { IGatewayClient } from '../extension';

function createMockClient(health: { status: string; servers: number; running: number } = { status: 'ok', servers: 3, running: 3 }): IGatewayClient & { health: typeof health; shouldFail: boolean } {
	const mock = {
		health,
		shouldFail: false,
		getHealth: async () => {
			if (mock.shouldFail) { throw new Error('connection refused'); }
			return mock.health;
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

/** Read backgroundColor.id without triggering TypeScript narrowing. */
function bgId(item: MockStatusBarItem): string | undefined {
	const bg = item.backgroundColor;
	if (bg === undefined || bg === null) { return undefined; }
	return bg.id;
}

describe('McpStatusBar', () => {
	let statusBar: McpStatusBar;
	let client: ReturnType<typeof createMockClient>;

	beforeEach(() => {
		resetMockState();
		client = createMockClient();
	});

	afterEach(() => {
		if (statusBar) { statusBar.dispose(); }
	});

	describe('initial state', () => {
		it('creates a status bar item on construction', () => {
			statusBar = new McpStatusBar(client as any);
			assert.strictEqual(mockStatusBarItems.length, 1);
		});

		it('shows the item immediately', () => {
			statusBar = new McpStatusBar(client as any);
			assert.strictEqual(mockStatusBarItems[0].visible, true);
		});

		it('sets command to focus tree view', () => {
			statusBar = new McpStatusBar(client as any);
			assert.strictEqual(mockStatusBarItems[0].command, 'mcpBackends.focus');
		});
	});

	describe('poll — all running', () => {
		it('shows N/M with all running', async () => {
			client.health = { status: 'ok', servers: 4, running: 4 };
			statusBar = new McpStatusBar(client as any);
			await statusBar.poll();
			assert.strictEqual(mockStatusBarItems[0].text, '$(server) MCP: 4/4');
		});

		it('clears background color when all running', async () => {
			client.health = { status: 'ok', servers: 3, running: 3 };
			statusBar = new McpStatusBar(client as any);
			await statusBar.poll();
			assert.strictEqual(mockStatusBarItems[0].backgroundColor, undefined);
		});

		it('sets tooltip for all running', async () => {
			client.health = { status: 'ok', servers: 3, running: 3 };
			statusBar = new McpStatusBar(client as any);
			await statusBar.poll();
			assert.ok(mockStatusBarItems[0].tooltip?.includes('all 3 servers running'));
		});
	});

	describe('poll — partial', () => {
		it('shows warning background when partial', async () => {
			client.health = { status: 'ok', servers: 4, running: 2 };
			statusBar = new McpStatusBar(client as any);
			await statusBar.poll();
			assert.strictEqual(mockStatusBarItems[0].text, '$(server) MCP: 2/4');
			assert.ok(mockStatusBarItems[0].backgroundColor !== undefined);
			assert.strictEqual(bgId(mockStatusBarItems[0]), 'statusBarItem.warningBackground');
		});

		it('sets tooltip for partial', async () => {
			client.health = { status: 'ok', servers: 4, running: 2 };
			statusBar = new McpStatusBar(client as any);
			await statusBar.poll();
			assert.ok(mockStatusBarItems[0].tooltip?.includes('2 of 4'));
		});
	});

	describe('poll — all offline', () => {
		it('shows error background when all servers offline', async () => {
			client.health = { status: 'ok', servers: 3, running: 0 };
			statusBar = new McpStatusBar(client as any);
			await statusBar.poll();
			assert.strictEqual(mockStatusBarItems[0].text, '$(server) MCP: 0/3');
			assert.strictEqual(bgId(mockStatusBarItems[0]), 'statusBarItem.errorBackground');
		});
	});

	describe('poll — no servers', () => {
		it('shows 0/0 with no background', async () => {
			client.health = { status: 'ok', servers: 0, running: 0 };
			statusBar = new McpStatusBar(client as any);
			await statusBar.poll();
			assert.strictEqual(mockStatusBarItems[0].text, '$(server) MCP: 0/0');
			assert.strictEqual(mockStatusBarItems[0].backgroundColor, undefined);
			assert.ok(mockStatusBarItems[0].tooltip?.includes('no servers configured'));
		});
	});

	describe('poll — gateway unreachable', () => {
		it('shows offline with error background', async () => {
			client.shouldFail = true;
			statusBar = new McpStatusBar(client as any);
			await statusBar.poll();
			assert.strictEqual(mockStatusBarItems[0].text, '$(server) MCP: offline');
			assert.strictEqual(bgId(mockStatusBarItems[0]), 'statusBarItem.errorBackground');
		});

		it('sets offline tooltip', async () => {
			client.shouldFail = true;
			statusBar = new McpStatusBar(client as any);
			await statusBar.poll();
			assert.ok(mockStatusBarItems[0].tooltip?.includes('cannot reach API'));
		});
	});

	describe('polling lifecycle', () => {
		it('startPolling triggers immediate poll', async () => {
			client.health = { status: 'ok', servers: 2, running: 1 };
			statusBar = new McpStatusBar(client as any);
			statusBar.startPolling(50000);
			// Wait for the immediate async poll to complete
			await new Promise((r) => setTimeout(r, 50));
			assert.strictEqual(mockStatusBarItems[0].text, '$(server) MCP: 1/2');
		});

		it('polls repeatedly at interval', async () => {
			let pollCount = 0;
			const originalGetHealth = client.getHealth;
			client.getHealth = async () => {
				pollCount++;
				return originalGetHealth.call(client);
			};
			statusBar = new McpStatusBar(client as any);
			statusBar.startPolling(50);
			await new Promise((r) => setTimeout(r, 180));
			statusBar.stopPolling();
			// 1 immediate + ~3 interval polls
			assert.ok(pollCount >= 3, `Expected at least 3 polls, got ${pollCount}`);
		});

		it('stopPolling stops further polls', async () => {
			let pollCount = 0;
			client.getHealth = async () => {
				pollCount++;
				return { status: 'ok', servers: 0, running: 0 };
			};
			statusBar = new McpStatusBar(client as any);
			statusBar.startPolling(50);
			await new Promise((r) => setTimeout(r, 80));
			statusBar.stopPolling();
			const countAfterStop = pollCount;
			await new Promise((r) => setTimeout(r, 150));
			assert.strictEqual(pollCount, countAfterStop);
		});
	});

	describe('dispose', () => {
		it('stops polling and disposes item', () => {
			statusBar = new McpStatusBar(client as any);
			statusBar.startPolling(50);
			statusBar.dispose();
			assert.strictEqual(mockStatusBarItems[0].disposed, true);
		});

		it('poll after dispose is a no-op', async () => {
			statusBar = new McpStatusBar(client as any);
			statusBar.dispose();
			await statusBar.poll();
			// Text should not be updated after dispose — remains empty/undefined
			assert.ok(!mockStatusBarItems[0].text?.includes('MCP'));
		});
	});

	describe('state transitions', () => {
		it('transitions from running to offline', async () => {
			statusBar = new McpStatusBar(client as any);
			const item = mockStatusBarItems[0];
			client.health = { status: 'ok', servers: 3, running: 3 };
			await statusBar.poll();
			assert.ok(item.backgroundColor === undefined, 'Expected no background when all running');

			client.shouldFail = true;
			await statusBar.poll();
			assert.strictEqual(item.text, '$(server) MCP: offline');
			assert.strictEqual(bgId(item), 'statusBarItem.errorBackground');
		});

		it('transitions from offline to running', async () => {
			client.shouldFail = true;
			statusBar = new McpStatusBar(client as any);
			const item = mockStatusBarItems[0];
			await statusBar.poll();
			assert.strictEqual(bgId(item), 'statusBarItem.errorBackground');

			client.shouldFail = false;
			client.health = { status: 'ok', servers: 2, running: 2 };
			await statusBar.poll();
			assert.strictEqual(item.text, '$(server) MCP: 2/2');
			assert.ok(item.backgroundColor === undefined, 'Expected no background when all running');
		});
	});
});
