import * as http from 'node:http';
import type {
	ApiError,
	CallToolRequest,
	CallToolResult,
	HealthResponse,
	ServerConfig,
	ServerView,
	StatusResponse,
	ToolInfo,
} from './types';

export type GatewayErrorKind = 'connection' | 'http' | 'parse' | 'timeout';

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

	constructor(baseUrl = 'http://localhost:8765', timeoutMs = 5000) {
		this.baseUrl = new URL(baseUrl);
		this.timeoutMs = timeoutMs;
	}

	// --- Health ---

	async getHealth(): Promise<HealthResponse> {
		return this.request<HealthResponse>('GET', '/api/v1/health');
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

	async patchServer(name: string, patch: { disabled?: boolean }): Promise<StatusResponse> {
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

	// --- Tools ---

	async listTools(): Promise<ToolInfo[]> {
		return this.request<ToolInfo[]>('GET', '/api/v1/tools');
	}

	async callTool(server: string, tool: string, args?: Record<string, unknown>): Promise<CallToolResult> {
		const body = args !== undefined ? { tool, arguments: args } : { tool };
		return this.request<CallToolResult>('POST', `/api/v1/servers/${enc(server)}/call`, body);
	}

	// --- Core HTTP ---

	private request<T>(method: string, path: string, body?: unknown): Promise<T> {
		return new Promise((resolve, reject) => {
			const url = new URL(path, this.baseUrl);

			const headers: Record<string, string> = {};
			if (body !== undefined) {
				headers['Content-Type'] = 'application/json';
			}

			const options: http.RequestOptions = {
				method,
				hostname: url.hostname,
				port: url.port,
				path: url.pathname + url.search,
				headers,
				timeout: this.timeoutMs,
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
						reject(new GatewayError('http', message, code, data));
					}
				});
			});

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
		});
	}
}

function enc(s: string): string {
	return encodeURIComponent(s);
}
