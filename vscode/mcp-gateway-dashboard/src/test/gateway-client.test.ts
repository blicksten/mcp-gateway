import * as http from 'node:http';
import * as assert from 'node:assert';
import { describe, it, before, after } from 'mocha';
import { GatewayClient, GatewayError } from '../gateway-client';
import type { HealthResponse, ServerView, ToolInfo } from '../types';

// Stub HTTP server that returns canned responses per route.
type RouteHandler = (req: http.IncomingMessage, body: string) => { status: number; body: unknown };
const routes = new Map<string, RouteHandler>();

let server: http.Server;
let port: number;
let client: GatewayClient;

function addRoute(method: string, path: string, handler: RouteHandler): void {
	routes.set(`${method} ${path}`, handler);
}

before((done) => {
	server = http.createServer((req, res) => {
		let body = '';
		req.on('data', (chunk) => { body += chunk; });
		req.on('end', () => {
			const key = `${req.method} ${req.url}`;
			const handler = routes.get(key);
			if (!handler) {
				res.writeHead(404);
				res.end(JSON.stringify({ error: 'not found' }));
				return;
			}
			const result = handler(req, body);
			res.writeHead(result.status, { 'Content-Type': 'application/json' });
			res.end(JSON.stringify(result.body));
		});
	});
	server.listen(0, '127.0.0.1', () => {
		const addr = server.address();
		if (addr && typeof addr === 'object') {
			port = addr.port;
			client = new GatewayClient(`http://127.0.0.1:${port}`, 2000);
		}
		done();
	});
});

beforeEach(() => {
	routes.clear();
});

after((done) => {
	routes.clear();
	server.close(done);
});

describe('GatewayClient', () => {
	describe('getHealth', () => {
		it('returns health response', async () => {
			addRoute('GET', '/api/v1/health', () => ({
				status: 200,
				body: { status: 'ok', servers: 3, running: 2 } satisfies HealthResponse,
			}));
			const health = await client.getHealth();
			assert.strictEqual(health.status, 'ok');
			assert.strictEqual(health.servers, 3);
			assert.strictEqual(health.running, 2);
		});
	});

	describe('listServers', () => {
		it('returns server list', async () => {
			const servers: ServerView[] = [
				{ name: 'ctx7', status: 'running', transport: 'http', restart_count: 0, tools: [] },
				{ name: 'orch', status: 'stopped', transport: 'stdio', restart_count: 1 },
			];
			addRoute('GET', '/api/v1/servers', () => ({ status: 200, body: servers }));
			const result = await client.listServers();
			assert.strictEqual(result.length, 2);
			assert.strictEqual(result[0].name, 'ctx7');
			assert.strictEqual(result[1].status, 'stopped');
		});
	});

	describe('getServer', () => {
		it('returns single server', async () => {
			const sv: ServerView = { name: 'ctx7', status: 'running', transport: 'http', restart_count: 0 };
			addRoute('GET', '/api/v1/servers/ctx7', () => ({ status: 200, body: sv }));
			const result = await client.getServer('ctx7');
			assert.strictEqual(result.name, 'ctx7');
		});

		it('throws on 404', async () => {
			addRoute('GET', '/api/v1/servers/missing', () => ({
				status: 404, body: { error: 'server "missing" not found' },
			}));
			await assert.rejects(
				() => client.getServer('missing'),
				(err: GatewayError) => {
					assert.strictEqual(err.kind, 'http');
					assert.strictEqual(err.statusCode, 404);
					return true;
				},
			);
		});
	});

	describe('addServer', () => {
		it('returns status response (not ServerView)', async () => {
			addRoute('POST', '/api/v1/servers', (_req, body) => {
				const parsed = JSON.parse(body);
				assert.strictEqual(parsed.name, 'new-srv');
				assert.strictEqual(parsed.config.url, 'http://localhost:3000/mcp');
				return { status: 201, body: { status: 'added' } };
			});
			const result = await client.addServer('new-srv', { url: 'http://localhost:3000/mcp', disabled: true });
			assert.strictEqual(result.status, 'added');
		});
	});

	describe('removeServer', () => {
		it('returns status response', async () => {
			addRoute('DELETE', '/api/v1/servers/old-srv', () => ({ status: 200, body: { status: 'removed' } }));
			const result = await client.removeServer('old-srv');
			assert.strictEqual(result.status, 'removed');
		});
	});

	describe('patchServer', () => {
		it('sends disabled flag', async () => {
			addRoute('PATCH', '/api/v1/servers/ctx7', (_req, body) => {
				const parsed = JSON.parse(body);
				assert.strictEqual(parsed.disabled, true);
				return { status: 200, body: { status: 'updated' } };
			});
			const result = await client.patchServer('ctx7', { disabled: true });
			assert.strictEqual(result.status, 'updated');
		});
	});

	describe('restartServer', () => {
		it('returns status on success', async () => {
			addRoute('POST', '/api/v1/servers/ctx7/restart', () => ({ status: 200, body: { status: 'restarted' } }));
			const result = await client.restartServer('ctx7');
			assert.strictEqual(result.status, 'restarted');
		});

		it('provides friendly message on not-found (SP-2)', async () => {
			addRoute('POST', '/api/v1/servers/gone/restart', () => ({
				status: 500, body: { error: 'server "gone" not found' },
			}));
			await assert.rejects(
				() => client.restartServer('gone'),
				(err: GatewayError) => {
					assert.ok(err.message.includes('no longer exists'));
					return true;
				},
			);
		});
	});

	describe('resetCircuit', () => {
		it('returns status on success', async () => {
			addRoute('POST', '/api/v1/servers/ctx7/reset-circuit', () => ({
				status: 200, body: { status: 'circuit reset' },
			}));
			const result = await client.resetCircuit('ctx7');
			assert.strictEqual(result.status, 'circuit reset');
		});

		it('handles 503 when monitor unavailable (SP-5)', async () => {
			addRoute('POST', '/api/v1/servers/ctx7/reset-circuit', () => ({
				status: 503, body: { error: 'health monitor not available' },
			}));
			await assert.rejects(
				() => client.resetCircuit('ctx7'),
				(err: GatewayError) => {
					assert.ok(err.message.includes('health monitor'));
					assert.strictEqual(err.statusCode, 503);
					return true;
				},
			);
		});
	});

	describe('listTools', () => {
		it('returns tool list', async () => {
			const tools: ToolInfo[] = [
				{ name: 'ctx7__query-docs', description: 'Query docs', server: 'ctx7' },
			];
			addRoute('GET', '/api/v1/tools', () => ({ status: 200, body: tools }));
			const result = await client.listTools();
			assert.strictEqual(result.length, 1);
			assert.strictEqual(result[0].server, 'ctx7');
		});
	});

	describe('callTool', () => {
		it('sends tool call and returns result', async () => {
			addRoute('POST', '/api/v1/servers/ctx7/call', (_req, body) => {
				const parsed = JSON.parse(body);
				assert.strictEqual(parsed.tool, 'query-docs');
				return {
					status: 200,
					body: { content: [{ type: 'text', text: 'result' }], isError: false },
				};
			});
			const result = await client.callTool('ctx7', 'query-docs', { query: 'test' });
			assert.ok(result.content);
			assert.strictEqual(result.content[0].text, 'result');
		});

		it('sends tool call without arguments (F-03)', async () => {
			addRoute('POST', '/api/v1/servers/ctx7/call', (_req, body) => {
				const parsed = JSON.parse(body);
				assert.strictEqual(parsed.tool, 'ping');
				assert.strictEqual(parsed.arguments, undefined);
				return {
					status: 200,
					body: { content: [{ type: 'text', text: 'pong' }] },
				};
			});
			const result = await client.callTool('ctx7', 'ping');
			assert.ok(result.content);
			assert.strictEqual(result.content[0].text, 'pong');
		});
	});

	describe('URL encoding (F-04)', () => {
		it('encodes server names with special characters', async () => {
			addRoute('GET', '/api/v1/servers/my%20server', () => ({
				status: 200,
				body: { name: 'my server', status: 'stopped', transport: 'stdio', restart_count: 0 },
			}));
			const result = await client.getServer('my server');
			assert.strictEqual(result.name, 'my server');
		});
	});

	describe('error handling', () => {
		it('HTTP 401 response classifies as kind:auth — not kind:http (B-NEW-18)', async () => {
			// Critical security path: a 401 from the gateway means the auth token
			// is missing/expired. ServerDataCache checks err.kind === 'auth' to
			// flip lastAuthFailed and trigger the re-auth toast in extension.ts.
			// If this is misclassified as 'http', the toast never fires.
			addRoute('GET', '/api/v1/health', () => ({
				status: 401,
				body: { error: 'Unauthorized — missing or invalid Bearer token' },
			}));
			const authClient = new GatewayClient(`http://127.0.0.1:${port}`, 2000);
			await assert.rejects(
				() => authClient.getHealth(),
				(err: GatewayError) => {
					assert.strictEqual(err.kind, 'auth', `expected kind:auth, got: ${err.kind}`);
					assert.strictEqual(err.statusCode, 401);
					return true;
				},
			);
		});

		it('detects invalid JSON response (F-02)', async () => {
			// Create a server that returns 200 with non-JSON body.
			const badServer = http.createServer((_req, res) => {
				res.writeHead(200, { 'Content-Type': 'text/plain' });
				res.end('not-json');
			});
			await new Promise<void>((resolve) => badServer.listen(0, '127.0.0.1', resolve));
			const badAddr = badServer.address();
			const badPort = badAddr && typeof badAddr === 'object' ? badAddr.port : 0;
			const badClient = new GatewayClient(`http://127.0.0.1:${badPort}`, 2000);
			try {
				await assert.rejects(
					() => badClient.getHealth(),
					(err: GatewayError) => {
						assert.strictEqual(err.kind, 'parse');
						return true;
					},
				);
			} finally {
				badServer.close();
			}
		});

		it('detects connection refused', async () => {
			const deadClient = new GatewayClient('http://127.0.0.1:1', 1000);
			await assert.rejects(
				() => deadClient.getHealth(),
				(err: GatewayError) => {
					assert.strictEqual(err.kind, 'connection');
					assert.ok(err.message.includes('not running'));
					return true;
				},
			);
		});

		it('detects timeout', async () => {
			// Create a separate slow server that never responds.
			const slowServer = http.createServer(() => {
				// Intentionally never send a response.
			});
			await new Promise<void>((resolve) => slowServer.listen(0, '127.0.0.1', resolve));
			const slowAddr = slowServer.address();
			const slowPort = slowAddr && typeof slowAddr === 'object' ? slowAddr.port : 0;
			const slowClient = new GatewayClient(`http://127.0.0.1:${slowPort}`, 100);
			try {
				await assert.rejects(
					() => slowClient.getHealth(),
					(err: GatewayError) => {
						assert.strictEqual(err.kind, 'timeout');
						return true;
					},
				);
			} finally {
				slowServer.close();
			}
		});
	});
});
