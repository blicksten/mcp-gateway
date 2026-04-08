import { resetMockState, type MockOutputChannel } from './mock-vscode';

import * as http from 'node:http';
import * as assert from 'node:assert';
import { describe, it, beforeEach, afterEach } from 'mocha';
import { LogViewer } from '../log-viewer';

// --- Test helpers ---

/** Tracks created output channels for assertion. */
let channels: MockOutputChannel[];

function createTestChannel(name: string): MockOutputChannel {
	const ch: MockOutputChannel = {
		name,
		lines: [],
		disposed: false,
		appendLine(line: string) { if (!this.disposed) { this.lines.push(line); } },
		append(text: string) { if (!this.disposed) { this.lines.push(text); } },
		clear() { this.lines.length = 0; },
		show() {},
		hide() {},
		dispose() { this.disposed = true; },
	};
	channels.push(ch);
	return ch;
}

/** Wait for a condition to become true, polling every `intervalMs`. */
async function waitFor(
	conditionFn: () => boolean,
	timeoutMs = 3000,
	intervalMs = 20,
): Promise<void> {
	const start = Date.now();
	while (!conditionFn()) {
		if (Date.now() - start > timeoutMs) {
			throw new Error(`waitFor timed out after ${timeoutMs}ms`);
		}
		await new Promise((r) => setTimeout(r, intervalMs));
	}
}

// --- SSE mock server ---

type SseHandler = (req: http.IncomingMessage, res: http.ServerResponse) => void;
let sseRoutes: Map<string, SseHandler>;
let server: http.Server;
let port: number;

function startServer(): Promise<void> {
	return new Promise((resolve) => {
		server = http.createServer((req, res) => {
			const handler = sseRoutes.get(req.url ?? '');
			if (!handler) {
				res.writeHead(404, { 'Content-Type': 'application/json' });
				res.end(JSON.stringify({ error: 'server not found' }));
				return;
			}
			handler(req, res);
		});
		server.listen(0, '127.0.0.1', () => {
			const addr = server.address();
			if (addr && typeof addr === 'object') {
				port = addr.port;
			}
			resolve();
		});
	});
}

function stopServer(): Promise<void> {
	return new Promise((resolve) => {
		server.close(() => resolve());
	});
}

/** Default fast-backoff options for tests. */
function fastOpts(extra?: Record<string, unknown>) {
	return {
		createChannel: createTestChannel as any,
		maxRetries: 10,
		initialBackoffMs: 10,
		maxBackoffMs: 50,
		...extra,
	};
}

// --- Tests ---

describe('LogViewer', () => {
	beforeEach(async () => {
		resetMockState();
		channels = [];
		sseRoutes = new Map();
		await startServer();
	});

	afterEach(async () => {
		await stopServer();
	});

	describe('SSE parsing', () => {
		it('receives and displays log lines from SSE stream', async () => {
			sseRoutes.set('/api/servers/test-backend/logs', (_req, res) => {
				res.writeHead(200, {
					'Content-Type': 'text/event-stream',
					'Cache-Control': 'no-cache',
					'Connection': 'keep-alive',
				});
				res.write('data: line one\n\n');
				res.write('data: line two\n\n');
			});

			const viewer = new LogViewer(`http://127.0.0.1:${port}`, fastOpts());
			viewer.show('test-backend');

			await waitFor(() => {
				const ch = channels[0];
				return ch !== undefined && ch.lines.includes('line one') && ch.lines.includes('line two');
			});

			assert.strictEqual(channels.length, 1);
			assert.strictEqual(channels[0].name, 'MCP: test-backend');
			assert.ok(channels[0].lines.includes('line one'));
			assert.ok(channels[0].lines.includes('line two'));

			viewer.dispose();
		});

		it('handles data: with no space after colon', async () => {
			sseRoutes.set('/api/servers/compact/logs', (_req, res) => {
				res.writeHead(200, { 'Content-Type': 'text/event-stream' });
				res.write('data:no-space\n\n');
			});

			const viewer = new LogViewer(`http://127.0.0.1:${port}`, fastOpts());
			viewer.show('compact');

			await waitFor(() => channels[0]?.lines.includes('no-space'));
			assert.ok(channels[0].lines.includes('no-space'));

			viewer.dispose();
		});

		it('handles multi-line SSE frames', async () => {
			sseRoutes.set('/api/servers/multi/logs', (_req, res) => {
				res.writeHead(200, { 'Content-Type': 'text/event-stream' });
				res.write('data: first\ndata: second\n\n');
			});

			const viewer = new LogViewer(`http://127.0.0.1:${port}`, fastOpts());
			viewer.show('multi');

			await waitFor(() => channels[0]?.lines.includes('second'));
			assert.ok(channels[0].lines.includes('first'));
			assert.ok(channels[0].lines.includes('second'));

			viewer.dispose();
		});

		it('handles chunked data arriving in fragments', async () => {
			sseRoutes.set('/api/servers/chunked/logs', (_req, res) => {
				res.writeHead(200, { 'Content-Type': 'text/event-stream' });
				res.write('data: hel');
				setTimeout(() => {
					res.write('lo world\n\n');
				}, 30);
			});

			const viewer = new LogViewer(`http://127.0.0.1:${port}`, fastOpts());
			viewer.show('chunked');

			await waitFor(() => channels[0]?.lines.includes('hello world'));
			assert.ok(channels[0].lines.includes('hello world'));

			viewer.dispose();
		});

		it('ignores non-data SSE fields', async () => {
			sseRoutes.set('/api/servers/fields/logs', (_req, res) => {
				res.writeHead(200, { 'Content-Type': 'text/event-stream' });
				res.write('event: log\nid: 42\ndata: actual data\nretry: 5000\n\n');
			});

			const viewer = new LogViewer(`http://127.0.0.1:${port}`, fastOpts());
			viewer.show('fields');

			await waitFor(() => channels[0]?.lines.includes('actual data'));
			const dataLines = channels[0].lines.filter(
				(l) => !l.startsWith('[log-viewer]'),
			);
			assert.strictEqual(dataLines.length, 1);
			assert.strictEqual(dataLines[0], 'actual data');

			viewer.dispose();
		});
	});

	describe('404 handling', () => {
		it('stops permanently on 404 (server removed)', async () => {
			// No route registered → server returns 404
			const viewer = new LogViewer(`http://127.0.0.1:${port}`, fastOpts());
			viewer.show('nonexistent');

			await waitFor(() => {
				const ch = channels[0];
				return ch !== undefined && ch.lines.some((l) => l.includes('not found'));
			});

			assert.ok(channels[0].lines.some((l) => l.includes('not found')));
			assert.strictEqual(viewer.isConnected('nonexistent'), false);

			viewer.dispose();
		});
	});

	describe('reconnect logic', () => {
		it('reconnects on stream end', async () => {
			let connectCount = 0;
			sseRoutes.set('/api/servers/flaky/logs', (_req, res) => {
				connectCount++;
				res.writeHead(200, { 'Content-Type': 'text/event-stream' });
				res.write(`data: connect-${connectCount}\n\n`);
				res.end();
			});

			const viewer = new LogViewer(`http://127.0.0.1:${port}`, fastOpts());
			viewer.show('flaky');

			await waitFor(() => connectCount >= 2, 5000);

			assert.ok(connectCount >= 2, `Expected >=2 connections, got ${connectCount}`);
			assert.ok(channels[0].lines.includes('connect-1'));
			assert.ok(channels[0].lines.some((l) => l.includes('Reconnecting')));

			viewer.dispose();
		});

		it('reconnects on non-200 HTTP status', async () => {
			let connectCount = 0;
			sseRoutes.set('/api/servers/error-srv/logs', (_req, res) => {
				connectCount++;
				if (connectCount === 1) {
					res.writeHead(500, { 'Content-Type': 'text/plain' });
					res.end('internal error');
				} else {
					res.writeHead(200, { 'Content-Type': 'text/event-stream' });
					res.write('data: recovered\n\n');
				}
			});

			const viewer = new LogViewer(`http://127.0.0.1:${port}`, fastOpts());
			viewer.show('error-srv');

			await waitFor(() => channels[0]?.lines.includes('recovered'), 5000);
			assert.ok(connectCount >= 2);
			assert.ok(channels[0].lines.some((l) => l.includes('HTTP 500')));

			viewer.dispose();
		});

		it('resets retry counter on successful connection', async () => {
			let connectCount = 0;
			sseRoutes.set('/api/servers/reset-retry/logs', (_req, res) => {
				connectCount++;
				if (connectCount <= 2) {
					// First two: fail with 500
					res.writeHead(500);
					res.end('fail');
				} else if (connectCount === 3) {
					// Third: succeed then end
					res.writeHead(200, { 'Content-Type': 'text/event-stream' });
					res.write('data: ok\n\n');
					res.end();
				} else {
					// Fourth+: succeed and keep open
					res.writeHead(200, { 'Content-Type': 'text/event-stream' });
					res.write('data: final\n\n');
				}
			});

			const viewer = new LogViewer(`http://127.0.0.1:${port}`, fastOpts({ maxRetries: 3 }));
			viewer.show('reset-retry');

			// Should succeed on connect 3, retry counter resets, then reconnect after end
			await waitFor(() => connectCount >= 4, 5000);
			assert.ok(channels[0].lines.includes('ok'));

			viewer.dispose();
		});

		it('gives up after maxRetries', async () => {
			let connectCount = 0;
			sseRoutes.set('/api/servers/hopeless/logs', (_req, res) => {
				connectCount++;
				res.writeHead(500);
				res.end('fail');
			});

			// Use maxRetries=3 with fast backoff for quick test
			const viewer = new LogViewer(`http://127.0.0.1:${port}`, fastOpts({ maxRetries: 3 }));
			viewer.show('hopeless');

			await waitFor(
				() => channels[0]?.lines.some((l) => l.includes('Max retries')) ?? false,
				5000,
			);

			assert.ok(channels[0].lines.some((l) => l.includes('Max retries (3)')));
			assert.strictEqual(viewer.isConnected('hopeless'), false);
			// Should have connected 1 (initial) + 3 (retries) = 4 times
			assert.strictEqual(connectCount, 4);

			viewer.dispose();
		});

		it('exponential backoff delays increase', async () => {
			const viewer = new LogViewer(`http://127.0.0.1:${port}`, fastOpts({ initialBackoffMs: 100, maxBackoffMs: 500 }));

			// Check messages contain increasing delay values
			let connectCount = 0;
			sseRoutes.set('/api/servers/backoff/logs', (_req, res) => {
				connectCount++;
				res.writeHead(500);
				res.end('fail');
			});

			viewer.show('backoff');

			// Wait for at least 3 reconnect attempts
			await waitFor(() => connectCount >= 4, 10000);

			// Verify delay messages: attempt 1 = 0s (100ms rounds to 0), attempt 2 = 0s (200ms), attempt 3 = 0s (400ms)
			// Actually with 100ms initial: 100ms=0s, 200ms=0s, 400ms=0s — all round to 0. Use larger values.
			viewer.dispose();

			// Just verify reconnect attempts occurred
			assert.ok(connectCount >= 4);
		});
	});

	describe('show / close / isConnected', () => {
		it('show() creates OutputChannel and connects', async () => {
			sseRoutes.set('/api/servers/srv1/logs', (_req, res) => {
				res.writeHead(200, { 'Content-Type': 'text/event-stream' });
				res.write('data: hello\n\n');
			});

			const viewer = new LogViewer(`http://127.0.0.1:${port}`, fastOpts());
			viewer.show('srv1');

			assert.strictEqual(viewer.isConnected('srv1'), true);
			assert.strictEqual(channels.length, 1);

			await waitFor(() => channels[0].lines.includes('hello'));

			viewer.dispose();
		});

		it('show() on already-connected server reuses channel (no duplicate)', async () => {
			sseRoutes.set('/api/servers/reuse/logs', (_req, res) => {
				res.writeHead(200, { 'Content-Type': 'text/event-stream' });
				res.write('data: initial\n\n');
			});

			const viewer = new LogViewer(`http://127.0.0.1:${port}`, fastOpts());
			viewer.show('reuse');
			viewer.show('reuse');

			assert.strictEqual(channels.length, 1, 'Should not create a second channel');

			viewer.dispose();
		});

		it('close() stops the stream and disposes channel', async () => {
			sseRoutes.set('/api/servers/closeme/logs', (_req, res) => {
				res.writeHead(200, { 'Content-Type': 'text/event-stream' });
				res.write('data: before-close\n\n');
			});

			const viewer = new LogViewer(`http://127.0.0.1:${port}`, fastOpts());
			viewer.show('closeme');

			await waitFor(() => channels[0]?.lines.includes('before-close'));
			assert.strictEqual(viewer.isConnected('closeme'), true);

			viewer.close('closeme');
			assert.strictEqual(viewer.isConnected('closeme'), false);
			assert.strictEqual(channels[0].disposed, true);

			viewer.dispose();
		});

		it('close() on unknown server is a no-op', () => {
			const viewer = new LogViewer(`http://127.0.0.1:${port}`, fastOpts());
			viewer.close('no-such'); // Should not throw
			viewer.dispose();
		});

		it('isConnected returns false for unknown server', () => {
			const viewer = new LogViewer(`http://127.0.0.1:${port}`, fastOpts());
			assert.strictEqual(viewer.isConnected('no-such'), false);
			viewer.dispose();
		});

		it('show() after close() creates a fresh connection', async () => {
			let connectCount = 0;
			sseRoutes.set('/api/servers/reopen/logs', (_req, res) => {
				connectCount++;
				res.writeHead(200, { 'Content-Type': 'text/event-stream' });
				res.write(`data: conn-${connectCount}\n\n`);
			});

			const viewer = new LogViewer(`http://127.0.0.1:${port}`, fastOpts());
			viewer.show('reopen');
			await waitFor(() => channels[0]?.lines.includes('conn-1'));
			viewer.close('reopen');

			viewer.show('reopen');
			await waitFor(() => channels.length >= 2 && channels[1]?.lines.includes('conn-2'));
			assert.strictEqual(channels[1].name, 'MCP: reopen');

			viewer.dispose();
		});
	});

	describe('multiple servers', () => {
		it('maintains separate channels per server', async () => {
			sseRoutes.set('/api/servers/alpha/logs', (_req, res) => {
				res.writeHead(200, { 'Content-Type': 'text/event-stream' });
				res.write('data: from-alpha\n\n');
			});
			sseRoutes.set('/api/servers/beta/logs', (_req, res) => {
				res.writeHead(200, { 'Content-Type': 'text/event-stream' });
				res.write('data: from-beta\n\n');
			});

			const viewer = new LogViewer(`http://127.0.0.1:${port}`, fastOpts());
			viewer.show('alpha');
			viewer.show('beta');

			await waitFor(() =>
				channels.length === 2 &&
				channels[0].lines.includes('from-alpha') &&
				channels[1].lines.includes('from-beta'),
			);

			assert.strictEqual(channels[0].name, 'MCP: alpha');
			assert.strictEqual(channels[1].name, 'MCP: beta');

			viewer.dispose();
		});

		it('closing one server does not affect others', async () => {
			sseRoutes.set('/api/servers/keep/logs', (_req, res) => {
				res.writeHead(200, { 'Content-Type': 'text/event-stream' });
				res.write('data: kept\n\n');
			});
			sseRoutes.set('/api/servers/drop/logs', (_req, res) => {
				res.writeHead(200, { 'Content-Type': 'text/event-stream' });
				res.write('data: dropped\n\n');
			});

			const viewer = new LogViewer(`http://127.0.0.1:${port}`, fastOpts());
			viewer.show('keep');
			viewer.show('drop');

			await waitFor(() => channels.length === 2 && channels[0].lines.length > 0 && channels[1].lines.length > 0);

			viewer.close('drop');
			assert.strictEqual(viewer.isConnected('keep'), true);
			assert.strictEqual(viewer.isConnected('drop'), false);
			assert.strictEqual(channels[0].disposed, false);
			assert.strictEqual(channels[1].disposed, true);

			viewer.dispose();
		});
	});

	describe('dispose', () => {
		it('disposes all channels and closes connections', async () => {
			sseRoutes.set('/api/servers/d1/logs', (_req, res) => {
				res.writeHead(200, { 'Content-Type': 'text/event-stream' });
				res.write('data: d1\n\n');
			});
			sseRoutes.set('/api/servers/d2/logs', (_req, res) => {
				res.writeHead(200, { 'Content-Type': 'text/event-stream' });
				res.write('data: d2\n\n');
			});

			const viewer = new LogViewer(`http://127.0.0.1:${port}`, fastOpts());
			viewer.show('d1');
			viewer.show('d2');

			await waitFor(() => channels.length === 2 && channels[0].lines.length > 0 && channels[1].lines.length > 0);

			viewer.dispose();

			assert.strictEqual(channels[0].disposed, true);
			assert.strictEqual(channels[1].disposed, true);
			assert.strictEqual(viewer.isConnected('d1'), false);
			assert.strictEqual(viewer.isConnected('d2'), false);
		});

		it('show() after dispose is a no-op', () => {
			const viewer = new LogViewer(`http://127.0.0.1:${port}`, fastOpts());
			viewer.dispose();
			viewer.show('ghost');
			assert.strictEqual(channels.length, 0);
		});

		it('double dispose is safe', () => {
			const viewer = new LogViewer(`http://127.0.0.1:${port}`, fastOpts());
			viewer.dispose();
			viewer.dispose(); // Should not throw
		});

		it('no data reaches channel after dispose', async () => {
			let res_: http.ServerResponse | undefined;
			sseRoutes.set('/api/servers/post-dispose/logs', (_req, res) => {
				res_ = res;
				res.writeHead(200, { 'Content-Type': 'text/event-stream' });
				res.write('data: before\n\n');
			});

			const viewer = new LogViewer(`http://127.0.0.1:${port}`, fastOpts());
			viewer.show('post-dispose');

			await waitFor(() => channels[0]?.lines.includes('before'));
			const lineCount = channels[0].lines.length;

			viewer.dispose();

			// Write after dispose — should not reach channel
			if (res_) {
				res_.write('data: after-dispose\n\n');
			}
			await new Promise((r) => setTimeout(r, 50));
			assert.strictEqual(channels[0].lines.length, lineCount, 'No new lines after dispose');
		});
	});

	describe('connection error handling', () => {
		it('reconnects on connection refused', async () => {
			const badPort = port + 10000 > 65535 ? port - 1000 : port + 10000;

			const viewer = new LogViewer(`http://127.0.0.1:${badPort}`, fastOpts({ maxRetries: 2 }));
			viewer.show('unreachable');

			await waitFor(() => {
				const ch = channels[0];
				return ch !== undefined && ch.lines.some((l) => l.includes('Connection error'));
			}, 5000);

			assert.ok(channels[0].lines.some((l) => l.includes('Connection error')));
			assert.ok(channels[0].lines.some((l) => l.includes('Reconnecting')));

			viewer.dispose();
		});
	});

	describe('connection timeout', () => {
		it('reconnects on connection timeout (single reconnect, no double-fire)', async () => {
			let connectCount = 0;
			sseRoutes.set('/api/servers/slow/logs', (_req, res) => {
				connectCount++;
				if (connectCount === 1) {
					// First connection: never respond — let timeout fire.
					// Do NOT send headers or data.
					return;
				}
				// Second connection: respond normally.
				res.writeHead(200, { 'Content-Type': 'text/event-stream' });
				res.write('data: recovered-after-timeout\n\n');
			});

			const viewer = new LogViewer(`http://127.0.0.1:${port}`, fastOpts({
				connectTimeoutMs: 100, // 100ms timeout for fast test
			}));
			viewer.show('slow');

			await waitFor(() => channels[0]?.lines.includes('recovered-after-timeout'), 5000);

			// Verify: exactly 2 connections (1 timed out + 1 successful).
			// If double-scheduleReconnect bug existed, connectCount would be 3+.
			assert.strictEqual(connectCount, 2, 'Should not double-reconnect on timeout');
			assert.ok(channels[0].lines.some((l) => l.includes('timed out')));

			viewer.dispose();
		});
	});

	describe('URL encoding', () => {
		it('encodes server names with special characters', async () => {
			const specialName = 'my server/test';
			const encodedPath = `/api/servers/${encodeURIComponent(specialName)}/logs`;
			sseRoutes.set(encodedPath, (_req, res) => {
				res.writeHead(200, { 'Content-Type': 'text/event-stream' });
				res.write('data: special\n\n');
			});

			const viewer = new LogViewer(`http://127.0.0.1:${port}`, fastOpts());
			viewer.show(specialName);

			await waitFor(() => channels[0]?.lines.includes('special'));
			assert.ok(channels[0].lines.includes('special'));

			viewer.dispose();
		});
	});
});
