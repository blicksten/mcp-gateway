import * as http from 'node:http';
import { AuthTokenError } from './auth-header';
import type {
	ApiError,
	CallToolResult,
	HealthResponse,
	ServerConfig,
	ServerView,
	StatusResponse,
	ToolInfo,
} from './types';
import type { PickerSnapshot } from './sap-picker-state';

// SAP Picker REST contract — mirrors Go types in
// internal/api/sap_picker_handler.go (T-A.1). Kept in this file so the
// gateway-client stays the single seam for HTTP shapes.
export type SapPickerSnapshotResponse = PickerSnapshot;
export interface SapBatchBeginResponse { batch_id: string; }
export interface SapBatchEndResponse { ok: boolean; }

export type GatewayErrorKind = 'connection' | 'http' | 'parse' | 'timeout' | 'auth';

/**
 * Function that returns the "Authorization" header value on demand, or
 * undefined to skip auth (legacy path). Errors from the function are
 * surfaced to the caller verbatim.
 *
 * AUDIT B-NEW-29 (Phase 11): updated to async to support the mtime-cached
 * resolveTokenAsync path. Callers that previously returned a string
 * synchronously can still do so via `async () => 'Bearer ...'` or wrap in
 * `Promise.resolve`.
 */
export type AuthHeaderProvider = () => Promise<string | undefined>;

export class GatewayError extends Error {
	constructor(
		public readonly kind: GatewayErrorKind,
		message: string,
		public readonly statusCode?: number,
		public readonly body?: string,
	) {
		super(message);
		this.name = 'GatewayError';
	}
}

export class GatewayClient {
	private readonly baseUrl: URL;
	private readonly timeoutMs: number;
	private readonly authHeader?: AuthHeaderProvider;
	// MCPR.3: separate provider for admin-scope endpoints (currently
	// /api/v1/shutdown). When undefined, admin-scope calls fall back to
	// the regular authHeader — that path will 401 against an MCPR.3-aware
	// daemon and surface as GatewayError('auth', ...). Recommended:
	// always wire both providers in extension.ts so daemon-control works.
	private readonly adminAuthHeader?: AuthHeaderProvider;
	// FM 7 (spike 2026-05-11): keep-alive agent eliminates per-request TCP
	// socket churn that caused Windows ephemeral port exhaustion + TIME_WAIT
	// pile-up + spurious connect failures under N-window load.
	private readonly agent = new http.Agent({
		keepAlive: true,
		maxSockets: 8,
		maxFreeSockets: 4,
		keepAliveMsecs: 1000,
	});

	constructor(
		baseUrl = 'http://localhost:8765',
		timeoutMs = 5000,
		authHeader?: AuthHeaderProvider,
		adminAuthHeader?: AuthHeaderProvider,
	) {
		this.baseUrl = new URL(baseUrl);
		this.timeoutMs = timeoutMs;
		this.authHeader = authHeader;
		this.adminAuthHeader = adminAuthHeader;
	}

	// --- Health ---

	async getHealth(): Promise<HealthResponse> {
		return this.request<HealthResponse>('GET', '/api/v1/health');
	}

	// Phase D.3 + MCPR.3: graceful daemon control via REST (admin scope).
	// Uses the admin-scope auth provider — falls back to the regular
	// provider only when none is wired (legacy single-tier deployments).
	// Returns on 202 Accepted; translates connection-refused into a
	// GatewayError('connection', ...) the caller can inspect.
	async shutdown(): Promise<StatusResponse> {
		return this.request<StatusResponse>(
			'POST',
			'/api/v1/shutdown',
			undefined,
			{ useAdminAuth: true },
		);
	}

	// --- Servers ---

	async listServers(): Promise<ServerView[]> {
		return this.request<ServerView[]>('GET', '/api/v1/servers');
	}

	async getServer(name: string): Promise<ServerView> {
		return this.request<ServerView>('GET', `/api/v1/servers/${enc(name)}`);
	}

	async addServer(name: string, config: ServerConfig): Promise<StatusResponse> {
		return this.request<StatusResponse>('POST', '/api/v1/servers', { name, config });
	}

	async removeServer(name: string): Promise<StatusResponse> {
		return this.request<StatusResponse>('DELETE', `/api/v1/servers/${enc(name)}`);
	}

	async patchServer(name: string, patch: {
		new_name?: string;
		disabled?: boolean;
		add_env?: string[];
		remove_env?: string[];
		add_headers?: Record<string, string>;
		remove_headers?: string[];
	}): Promise<StatusResponse> {
		return this.request<StatusResponse>('PATCH', `/api/v1/servers/${enc(name)}`, patch);
	}

	async restartServer(name: string): Promise<StatusResponse> {
		try {
			return await this.request<StatusResponse>('POST', `/api/v1/servers/${enc(name)}/restart`);
		} catch (err) {
			// Go API returns 500 for both "not found" and "restart failed".
			// Parse error body to provide better UX (SP-2 fix).
			if (err instanceof GatewayError && err.kind === 'http' && err.body?.includes('not found')) {
				throw new GatewayError('http', `Server "${name}" no longer exists — refresh the tree`, err.statusCode, err.body);
			}
			throw err;
		}
	}

	async resetCircuit(name: string): Promise<StatusResponse> {
		try {
			return await this.request<StatusResponse>('POST', `/api/v1/servers/${enc(name)}/reset-circuit`);
		} catch (err) {
			// Go API returns 503 when health monitor is nil (SP-5 fix).
			if (err instanceof GatewayError && err.statusCode === 503) {
				throw new GatewayError('http', 'Circuit reset unavailable: health monitor is not running', 503, err.body);
			}
			throw err;
		}
	}

	// --- Claude Code bridge (register-pid pipeline fix, Gap 3 2026-05-24) ---

	/**
	 * Post a (session_id, pid, window_id) tuple to the gateway so /unfreeze
	 * can target this claude.exe and FM-9 multi-instance enforcement sees
	 * it. Used by ClaudeSessionBridge; replaces the broken statusline.mjs
	 * pipeline that never fires under VSCode-embedded Claude Code.
	 *
	 * Returns `{stored: true}` on success. Throws GatewayError on transport
	 * or http failure — caller swallows + retries on next refresh.
	 */
	async registerPid(req: { session_id: string; pid: number; window_id?: string }): Promise<StatusResponse> {
		return this.request<StatusResponse>('POST', '/api/v1/claude-code/register-pid', req);
	}

	/**
	 * Atomically claim a daemon-respawn event identified by started_at_ms
	 * (the new daemon's started_at converted to Unix milliseconds). Returns
	 * `{kind: "won"}` for the first dashboard-extension instance to POST;
	 * subsequent POSTs receive `{kind: "lost", claimed_by: {...}}` so they
	 * can suppress their user prompt. Replaces the v1 filesystem-sentinel
	 * approach (FM-33 Path 1 Option B refactor, 2026-05-25).
	 */
	async claimRespawn(req: { started_at_ms: number; pid?: number; window_id?: string }): Promise<{
		kind: 'won' | 'lost';
		claimed_by?: { pid: number; window_id?: string; claimed_at_ms: number };
	}> {
		return this.request('POST', '/api/v1/claude-code/respawn-claim', req);
	}

	// --- Tools ---

	async listTools(): Promise<ToolInfo[]> {
		return this.request<ToolInfo[]>('GET', '/api/v1/tools');
	}

	async callTool(server: string, tool: string, args?: Record<string, unknown>): Promise<CallToolResult> {
		const body = args !== undefined ? { tool, arguments: args } : { tool };
		return this.request<CallToolResult>('POST', `/api/v1/servers/${enc(server)}/call`, body);
	}

	// --- SAP Picker (Phase A T-A.1 contract; Phase B webview consumer) ---

	async getSapPickerSnapshot(): Promise<SapPickerSnapshotResponse> {
		return this.request<SapPickerSnapshotResponse>('GET', '/api/v1/sap/picker-snapshot');
	}

	async beginSapBatch(): Promise<SapBatchBeginResponse> {
		return this.request<SapBatchBeginResponse>('POST', '/api/v1/sap/batch-begin');
	}

	async endSapBatch(batchId: string): Promise<SapBatchEndResponse> {
		return this.request<SapBatchEndResponse>('POST', '/api/v1/sap/batch-end', { batch_id: batchId });
	}

	// --- Import-from-Claude (Phase D backend; Phase E webview consumer) ---

	async importSnapshot(source: string, projectRoot?: string): Promise<unknown> {
		// Per claude_code_import_handler.go: project_root is required for
		// cc_project source, optional otherwise. encodeURIComponent handles
		// any spaces or special chars in the workspace path.
		const params = new URLSearchParams();
		params.set('source', source);
		if (projectRoot && projectRoot.length > 0) {
			params.set('project_root', projectRoot);
		}
		return this.request<unknown>('GET', `/api/v1/claude-code/import-snapshot?${params.toString()}`);
	}

	async importApply(ops: unknown[]): Promise<unknown> {
		return this.request<unknown>('POST', '/api/v1/claude-code/import-apply', { ops });
	}

	// --- Core HTTP ---

	// AUDIT B-NEW-29 (Phase 11): request is now async so it can await the
	// async authHeader provider. Previously authHeader was called synchronously
	// inside a new Promise() constructor, which meant fs.readFileSync blocked
	// the event loop on every REST call. The async provider uses the mtime-
	// cached resolveTokenAsync path instead.
	//
	// MCPR.3: opts.useAdminAuth selects the admin-scope provider for
	// daemon-control endpoints (currently /api/v1/shutdown). Falls back to
	// the regular provider when the admin one is unwired so legacy
	// single-tier deployments keep working.
	private async request<T>(
		method: string,
		path: string,
		body?: unknown,
		opts?: { useAdminAuth?: boolean },
	): Promise<T> {
		const url = new URL(path, this.baseUrl);

		const headers: Record<string, string> = {};
		if (body !== undefined) {
			headers['Content-Type'] = 'application/json';
		}

		// Attach Authorization header if the caller supplied a provider.
		// Provider errors surface as GatewayError('auth', ...) so UI code
		// can distinguish "no token" from network failures.
		//
		// LAYER 1 (Bug B defensive): authHeader race. fs.promises.stat does NOT
		// accept {signal} — AbortController cannot cancel a hung resolveTokenAsync
		// (e.g. encrypted FS / OneDrive sync / antivirus probe). A separate
		// timeout race is the only way to bound this phase. Cap at 5s so a
		// pathological stat hang cannot defer the HTTP-phase deadline.
		const provider = opts?.useAdminAuth
			? (this.adminAuthHeader ?? this.authHeader)
			: this.authHeader;
		if (provider) {
			const authHeaderTimeoutMs = Math.min(this.timeoutMs, 5000);
			let authTimer: ReturnType<typeof setTimeout> | undefined;
			const authTimeout = new Promise<never>((_, reject) => {
				authTimer = setTimeout(
					() => reject(new GatewayError(
						'timeout',
						`authHeader resolution timeout (${authHeaderTimeoutMs}ms) — likely fs.promises.stat hang on token file`,
					)),
					authHeaderTimeoutMs,
				);
			});
			try {
				const hdr = await Promise.race([provider(), authTimeout]);
				if (hdr) { headers['Authorization'] = hdr; }
			} catch (err) {
				if (err instanceof AuthTokenError) {
					throw new GatewayError('auth', err.message);
				}
				throw err;
			} finally {
				if (authTimer) { clearTimeout(authTimer); }
			}
		}

		// LAYER 2 (Bug B defensive): AbortController for the HTTP phase.
		// http.RequestOptions.timeout is socket-inactivity (Node docs §http.request),
		// NOT an absolute deadline — a server that drips one byte per second
		// keeps the socket "active" indefinitely. AbortController fires after
		// timeoutMs regardless of socket activity. req.on('timeout') is kept as
		// belt-and-suspenders for socket-inactivity.
		const ac = new AbortController();
		const deadlineTimer = setTimeout(() => ac.abort(), this.timeoutMs);

		try {
			return await new Promise<T>((resolve, reject) => {
				const options: http.RequestOptions = {
					method,
					hostname: url.hostname,
					port: url.port,
					path: url.pathname + url.search,
					headers,
					timeout: this.timeoutMs,
					agent: this.agent,
				};

				const req = http.request(options, (res) => {
				let data = '';
				res.on('data', (chunk: Buffer) => { data += chunk.toString(); });
				res.on('end', () => {
					const code = res.statusCode ?? 0;
					if (code >= 200 && code < 300) {
						try {
							resolve(JSON.parse(data) as T);
						} catch {
							reject(new GatewayError('parse', `Invalid JSON response from ${method} ${path}`, code, data));
						}
					} else {
						let message = `HTTP ${code} from ${method} ${path}`;
						try {
							const parsed = JSON.parse(data) as ApiError;
							if (parsed.error) {
								message = parsed.error;
							}
						} catch {
							// Non-JSON error body — use raw text.
						}
						if (code === 401) {
							reject(new GatewayError('auth', message, code, data));
							return;
						}
						reject(new GatewayError('http', message, code, data));
					}
				});
			});

			const onAbort = (): void => {
				req.destroy();
				reject(new GatewayError('timeout', `Request timeout: ${method} ${path} (${this.timeoutMs}ms)`));
			};
			if (ac.signal.aborted) {
				onAbort();
				return;
			}
			ac.signal.addEventListener('abort', onAbort, { once: true });

			req.on('timeout', () => {
				req.destroy();
				reject(new GatewayError('timeout', `Request timeout: ${method} ${path} (${this.timeoutMs}ms)`));
			});

			req.on('error', (err) => {
				if (req.destroyed) return; // Suppress spurious error after timeout destroy.
				if ((err as NodeJS.ErrnoException).code === 'ECONNREFUSED') {
					reject(new GatewayError('connection', 'MCP Gateway is not running (connection refused)'));
				} else {
					reject(new GatewayError('connection', `Connection error: ${err.message}`));
				}
			});

			if (body !== undefined) {
				req.write(JSON.stringify(body));
			}
			req.end();
		}); // closes new Promise<T>
		} finally {
			clearTimeout(deadlineTimer);
		}
	}

	/** Release the keep-alive agent's pooled sockets. Idempotent. */
	dispose(): void {
		this.agent.destroy();
	}
}

function enc(s: string): string {
	return encodeURIComponent(s);
}
