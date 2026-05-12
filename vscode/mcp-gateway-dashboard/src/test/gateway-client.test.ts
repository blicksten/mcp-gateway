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

		// Test 14 — T2.6: patchServer with new_name
		it('Test 14: patchServer sends new_name and parses {status,old_name,new_name} response', async () => {
			let capturedAuth: string | undefined;
			let capturedBody: unknown;
			addRoute('PATCH', '/api/v1/servers/ctx7', (req, body) => {
				capturedAuth = req.headers.authorization;
				capturedBody = JSON.parse(body);
				return {
					status: 200,
					body: { status: 'patched', old_name: 'ctx7', new_name: 'ctx8' },
				};
			});

			// Build a client with an auth provider so we can verify the header.
			const authProvider = async (): Promise<string> => 'Bearer test-token';
			const authClient = new GatewayClient(`http://127.0.0.1:${port}`, 2000, authProvider);

			const result = await authClient.patchServer('ctx7', { new_name: 'ctx8' });

			assert.strictEqual(result.status, 'patched');
			assert.strictEqual((result as unknown as { old_name: string }).old_name, 'ctx7');
			assert.strictEqual((result as unknown as { new_name: string }).new_name, 'ctx8');
			assert.strictEqual(capturedAuth, 'Bearer test-token',
				'Authorization header must be forwarded from buildAuthHeader provider');
			assert.deepStrictEqual(capturedBody, { new_name: 'ctx8' },
				'body must contain only new_name when no other fields are set');

			authClient.dispose();
		});

		// Backward-compat regression guard for T2.1: existing callers must still compile.
		it('backward-compat: existing add_env caller compiles and works unchanged', async () => {
			addRoute('PATCH', '/api/v1/servers/ctx7', (_req, body) => {
				const parsed = JSON.parse(body);
				assert.deepStrictEqual(parsed.add_env, ['API_KEY=secret']);
				return { status: 200, body: { status: 'updated' } };
			});
			const result = await client.patchServer('ctx7', { add_env: ['API_KEY=secret'] });
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

		it('Bug B Layer 1: hung authHeader provider rejects with kind:timeout (authHeader)', async () => {
			// Defense-in-depth: fs.promises.stat does NOT accept {signal} option,
			// so AbortController on the HTTP phase cannot cancel a hung token
			// resolution. authHeader gets its own Promise.race + timeout, capped
			// at min(timeoutMs, 5000).
			//
			// We register a healthy /servers route to prove the request never
			// reaches the HTTP phase — the timeout fires entirely in Layer 1.
			let routeCalled = false;
			addRoute('GET', '/api/v1/servers', () => {
				routeCalled = true;
				return { status: 200, body: [] };
			});

			// Provider that never resolves — simulates fs.promises.stat hanging.
			const hangingProvider = async (): Promise<string> => {
				await new Promise<never>(() => { /* never */ });
				return 'unreachable';
			};
			// timeoutMs=100 → authHeaderTimeoutMs = min(100, 5000) = 100.
			const hangClient = new GatewayClient(
				`http://127.0.0.1:${port}`,
				100,
				hangingProvider,
			);
			await assert.rejects(
				() => hangClient.listServers(),
				(err: GatewayError) => {
					assert.strictEqual(err.kind, 'timeout');
					assert.ok(
						err.message.includes('authHeader'),
						`expected authHeader-specific timeout message, got: ${err.message}`,
					);
					return true;
				},
			);
			assert.strictEqual(
				routeCalled, false,
				'HTTP phase must not run when authHeader is still resolving',
			);
		});
	});

	// FM 7 (spike 2026-05-11): keep-alive agent reuses TCP sockets across requests.
	// Without the agent, every getHealth() opens a new TCP connection, causing
	// Windows ephemeral port exhaustion under N-window load. The regression tests
	// below verify socket reuse (connection count == 1 for N calls) and that
	// dispose() destroys the agent cleanly.
	describe('keep-alive socket reuse (FM 7)', () => {
		it('FM 7: keep-alive agent reuses socket across consecutive requests', async () => {
			// Use a dedicated server so connection counting is isolated from the
			// shared server used by other tests (which is kept-alive too, making
			// the count cumulative and test-order-sensitive).
			const connectionCount = { n: 0 };
			const kaServer = http.createServer((req, res) => {
				res.writeHead(200, { 'Content-Type': 'application/json' });
				res.end(JSON.stringify({ status: 'ok', servers: 0, running: 0 }));
			});
			kaServer.on('connection', () => { connectionCount.n++; });
			await new Promise<void>((r) => kaServer.listen(0, '127.0.0.1', r));
			const kaAddr = kaServer.address();
			const kaPort = kaAddr && typeof kaAddr === 'object' ? (kaAddr as { port: number }).port : 0;

			const kaClient = new GatewayClient(`http://127.0.0.1:${kaPort}`, 2000);
			try {
				await kaClient.getHealth();
				await kaClient.getHealth();
				await kaClient.getHealth();
				// FM 7: with keep-alive, all 3 requests share 1 socket.
				// Without keepAlive:true agent, each request opens a new connection
				// and connectionCount.n would equal 3.
				assert.strictEqual(
					connectionCount.n, 1,
					`FM 7 regression: expected 1 socket for 3 sequential requests, got ${connectionCount.n}. ` +
					`This indicates the keep-alive agent (FM 7 fix) is not being used.`,
				);
			} finally {
				kaClient.dispose();
				await new Promise<void>((r) => kaServer.close(() => r()));
			}
		});

		it('FM 7: dispose() destroys keep-alive agent and is idempotent', () => {
			const kaClient = new GatewayClient('http://127.0.0.1:9999');
			// Access the private agent field — TS-private but runtime-public.
			const agent = (kaClient as unknown as { agent: http.Agent }).agent;
			assert.ok(agent, 'FM 7 regression: GatewayClient must expose a private http.Agent field');
			assert.strictEqual(typeof agent.destroy, 'function', 'http.Agent must have destroy()');
			// First dispose — destroys the agent.
			kaClient.dispose();
			// Second dispose must not throw (idempotency — agent.destroy() is safe to call twice).
			assert.doesNotThrow(
				() => kaClient.dispose(),
				'FM 7 regression: dispose() must be idempotent — second call must not throw',
			);
		});
	});

	// MCPR.3: admin-scope auth provider must be used for /shutdown.
	// The regular Bearer must NOT leak into daemon-control endpoints —
	// otherwise VSCode 1.119's McpGatewayService (which only knows the
	// regular Bearer via plugin .mcp.json) could still trigger /shutdown.
	describe('admin auth (MCPR.3)', () => {
		it('shutdown uses admin Bearer when adminAuthHeader provider is wired', async () => {
			let capturedAuth: string | undefined;
			addRoute('POST', '/api/v1/shutdown', (req) => {
				capturedAuth = req.headers.authorization;
				return { status: 202, body: { status: 'shutting_down' } };
			});

			const regularProvider = async (): Promise<string> => 'Bearer regular-token-XYZ';
			const adminProvider = async (): Promise<string> => 'Bearer admin-token-ABC';
			const dualClient = new GatewayClient(
				`http://127.0.0.1:${port}`,
				2000,
				regularProvider,
				adminProvider,
			);

			const resp = await dualClient.shutdown();
			assert.strictEqual(resp.status, 'shutting_down');
			assert.strictEqual(capturedAuth, 'Bearer admin-token-ABC',
				'shutdown must carry the admin Bearer, not the regular one');
		});

		it('regular endpoint still uses regular Bearer when both providers wired', async () => {
			let capturedAuth: string | undefined;
			addRoute('GET', '/api/v1/servers', (req) => {
				capturedAuth = req.headers.authorization;
				return { status: 200, body: [] };
			});

			const regularProvider = async (): Promise<string> => 'Bearer regular-token-XYZ';
			const adminProvider = async (): Promise<string> => 'Bearer admin-token-ABC';
			const dualClient = new GatewayClient(
				`http://127.0.0.1:${port}`,
				2000,
				regularProvider,
				adminProvider,
			);

			await dualClient.listServers();
			assert.strictEqual(capturedAuth, 'Bearer regular-token-XYZ',
				'regular endpoints must continue to carry the regular Bearer');
		});

		it('shutdown falls back to regular provider when admin provider is unwired', async () => {
			// Legacy single-tier deployments (or pre-MCPR.3 daemons) — admin
			// provider absent means client falls back to regular for shutdown.
			// Such a call WILL fail with 401 against an MCPR.3-aware daemon,
			// but the client itself does not crash and surfaces the daemon's
			// 401 as kind:auth.
			let capturedAuth: string | undefined;
			addRoute('POST', '/api/v1/shutdown', (req) => {
				capturedAuth = req.headers.authorization;
				return { status: 202, body: { status: 'shutting_down' } };
			});

			const regularProvider = async (): Promise<string> => 'Bearer regular-only';
			const legacyClient = new GatewayClient(
				`http://127.0.0.1:${port}`,
				2000,
				regularProvider,
				// adminProvider intentionally omitted
			);

			await legacyClient.shutdown();
			assert.strictEqual(capturedAuth, 'Bearer regular-only',
				'fallback must use regular provider when admin is unwired');
		});

		it('admin provider error surfaces as kind:auth', async () => {
			// Mirrors the regular-path behavior — admin provider failures
			// must classify as 'auth' so UI can distinguish from network
			// failures.
			const regularProvider = async (): Promise<string> => 'Bearer regular';
			const adminProvider = async (): Promise<string> => {
				const err = new Error('admin token missing') as Error & { name: string };
				err.name = 'AuthTokenError';
				// Use a real AuthTokenError instance to match the runtime check.
				const { AuthTokenError } = await import('../auth-header');
				throw new AuthTokenError('admin token missing', '/fake/admin.token');
			};
			const failClient = new GatewayClient(
				`http://127.0.0.1:${port}`,
				2000,
				regularProvider,
				adminProvider,
			);
			await assert.rejects(
				() => failClient.shutdown(),
				(err: GatewayError) => {
					assert.strictEqual(err.kind, 'auth');
					assert.ok(err.message.includes('admin token missing'));
					return true;
				},
			);
		});
	});
});
