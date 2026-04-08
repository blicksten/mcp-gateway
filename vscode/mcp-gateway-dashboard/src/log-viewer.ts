import * as http from 'node:http';
import * as vscode from 'vscode';

/** Maximum number of reconnect attempts before giving up. */
const MAX_RETRIES = 10;

/** Maximum backoff delay in milliseconds (5 minutes). */
const MAX_BACKOFF_MS = 5 * 60 * 1000;

/** Initial backoff delay in milliseconds. */
const INITIAL_BACKOFF_MS = 1000;

/** Default connection timeout in milliseconds (30 seconds). */
const CONNECT_TIMEOUT_MS = 30_000;

/** Configuration options for LogViewer (exposed for testability). */
export interface LogViewerOptions {
	createChannel?: (name: string) => vscode.OutputChannel;
	httpRequest?: typeof http.request;
	maxRetries?: number;
	initialBackoffMs?: number;
	maxBackoffMs?: number;
	connectTimeoutMs?: number;
}

/** Manages a single SSE log connection for one backend server. */
interface LogConnection {
	/** The OutputChannel where log lines are written. */
	channel: vscode.OutputChannel;
	/** The active HTTP request, if connected. */
	request: http.ClientRequest | undefined;
	/** Number of consecutive reconnect attempts. */
	retries: number;
	/** Pending reconnect timer handle. */
	reconnectTimer: ReturnType<typeof setTimeout> | undefined;
	/** Whether the connection has been permanently closed. */
	closed: boolean;
}

export class LogViewer implements vscode.Disposable {
	private readonly baseUrl: URL;
	private readonly connections = new Map<string, LogConnection>();
	private disposed = false;

	private readonly createChannel: (name: string) => vscode.OutputChannel;
	private readonly httpRequest: typeof http.request;
	private readonly maxRetries: number;
	private readonly initialBackoffMs: number;
	private readonly maxBackoffMs: number;
	private readonly connectTimeoutMs: number;

	constructor(baseUrl: string, opts?: LogViewerOptions) {
		this.baseUrl = new URL(baseUrl);
		this.createChannel = opts?.createChannel ?? ((name) => vscode.window.createOutputChannel(name));
		this.httpRequest = opts?.httpRequest ?? http.request;
		this.maxRetries = opts?.maxRetries ?? MAX_RETRIES;
		this.initialBackoffMs = opts?.initialBackoffMs ?? INITIAL_BACKOFF_MS;
		this.maxBackoffMs = opts?.maxBackoffMs ?? MAX_BACKOFF_MS;
		this.connectTimeoutMs = opts?.connectTimeoutMs ?? CONNECT_TIMEOUT_MS;
	}

	/** Show logs for a backend server. Creates or re-opens the output channel. */
	show(serverName: string): void {
		if (this.disposed) { return; }

		const existing = this.connections.get(serverName);
		if (existing && !existing.closed) {
			// Already connected — just reveal the channel.
			existing.channel.show(true);
			return;
		}

		// Create a new connection.
		const channel = this.createChannel(`MCP: ${serverName}`);
		const conn: LogConnection = {
			channel,
			request: undefined,
			retries: 0,
			reconnectTimer: undefined,
			closed: false,
		};
		this.connections.set(serverName, conn);
		channel.show(true);
		this.connect(serverName, conn);
	}

	/** Close the log stream for a specific server. */
	close(serverName: string): void {
		const conn = this.connections.get(serverName);
		if (!conn) { return; }
		this.teardown(conn);
		this.connections.delete(serverName);
	}

	/** Returns whether a log stream is active for the given server. */
	isConnected(serverName: string): boolean {
		const conn = this.connections.get(serverName);
		return conn !== undefined && !conn.closed;
	}

	dispose(): void {
		if (this.disposed) { return; }
		this.disposed = true;
		for (const [, conn] of this.connections) {
			this.teardown(conn);
		}
		this.connections.clear();
	}

	private teardown(conn: LogConnection): void {
		conn.closed = true;
		if (conn.reconnectTimer !== undefined) {
			clearTimeout(conn.reconnectTimer);
			conn.reconnectTimer = undefined;
		}
		if (conn.request) {
			conn.request.destroy();
			conn.request = undefined;
		}
		conn.channel.dispose();
	}

	private connect(serverName: string, conn: LogConnection): void {
		if (conn.closed || this.disposed) { return; }

		const url = new URL(`/api/v1/servers/${encodeURIComponent(serverName)}/logs`, this.baseUrl);

		const options: http.RequestOptions = {
			method: 'GET',
			hostname: url.hostname,
			port: url.port,
			path: url.pathname + url.search,
			headers: { 'Accept': 'text/event-stream' },
		};

		const req = this.httpRequest(options, (res) => {
			const statusCode = res.statusCode ?? 0;

			// 404 — server was removed, stop permanently.
			if (statusCode === 404) {
				res.resume(); // A-06 fix: drain response body to release socket.
				conn.channel.appendLine('[log-viewer] Server not found — stream closed.');
				this.teardown(conn);
				// H-01 fix: only delete if this conn is still the current entry.
				if (this.connections.get(serverName) === conn) {
					this.connections.delete(serverName);
				}
				return;
			}

			// Non-200 — reconnect.
			if (statusCode < 200 || statusCode >= 300) {
				res.resume(); // A-06 fix: drain response body to release socket.
				conn.channel.appendLine(`[log-viewer] HTTP ${statusCode} — will reconnect.`);
				conn.request = undefined;
				this.scheduleReconnect(serverName, conn);
				return;
			}

			// Connected successfully — reset retry counter.
			conn.retries = 0;

			// Parse SSE: accumulate chunks, split on \n\n boundaries.
			let buffer = '';
			res.on('data', (chunk: Buffer) => {
				if (conn.closed) { return; }
				// M-01/A-01 fix: normalize \r\n and bare \r to \n for SSE spec compliance.
				buffer += chunk.toString().replace(/\r\n|\r/g, '\n');

				// Process complete SSE frames (delimited by double newline).
				let idx: number;
				while ((idx = buffer.indexOf('\n\n')) !== -1) {
					const frame = buffer.slice(0, idx);
					buffer = buffer.slice(idx + 2);

					for (const line of frame.split('\n')) {
						if (line.startsWith('data: ')) {
							conn.channel.appendLine(line.slice(6));
						} else if (line.startsWith('data:')) {
							conn.channel.appendLine(line.slice(5));
						}
						// Ignore non-data SSE fields (event:, id:, retry:, comments).
					}
				}
			});

			res.on('end', () => {
				if (conn.closed) { return; }
				conn.request = undefined;
				conn.channel.appendLine('[log-viewer] Stream ended — reconnecting...');
				this.scheduleReconnect(serverName, conn);
			});

			res.on('error', (err) => {
				if (conn.closed) { return; }
				conn.request = undefined;
				conn.channel.appendLine(`[log-viewer] Stream error: ${err.message}`);
				this.scheduleReconnect(serverName, conn);
			});
		});

		// A-09 fix: connection timeout — prevents permanent hang on unresponsive daemon.
		req.on('timeout', () => {
			if (conn.closed) { return; }
			conn.channel.appendLine('[log-viewer] Connection timed out — reconnecting...');
			conn.request = undefined; // Clear before destroy so error handler knows it was intentional.
			req.destroy();
			this.scheduleReconnect(serverName, conn);
		});
		req.setTimeout(this.connectTimeoutMs);

		req.on('error', (err) => {
			if (conn.closed) { return; }
			// D7-03 fix: ignore error after intentional destroy (timeout handler already scheduled reconnect).
			if (conn.request !== req) { return; }
			conn.request = undefined;
			conn.channel.appendLine(`[log-viewer] Connection error: ${err.message}`);
			this.scheduleReconnect(serverName, conn);
		});

		// H-02/A-02 fix: assign conn.request before req.end() to prevent
		// stale reference if error fires synchronously.
		conn.request = req;
		req.end();
	}

	private scheduleReconnect(serverName: string, conn: LogConnection): void {
		if (conn.closed || this.disposed) { return; }
		// D7-04 fix: prevent double-scheduling (e.g. timeout + error both fire).
		if (conn.reconnectTimer !== undefined) { return; }

		conn.retries++;
		if (conn.retries > this.maxRetries) {
			conn.channel.appendLine(`[log-viewer] Max retries (${this.maxRetries}) reached — giving up.`);
			this.teardown(conn);
			// H-01 fix: only delete if this conn is still the current entry.
			if (this.connections.get(serverName) === conn) {
				this.connections.delete(serverName);
			}
			return;
		}

		const delay = Math.min(this.initialBackoffMs * Math.pow(2, conn.retries - 1), this.maxBackoffMs);
		conn.channel.appendLine(`[log-viewer] Reconnecting in ${Math.round(delay / 1000)}s (attempt ${conn.retries}/${this.maxRetries})...`);

		conn.reconnectTimer = setTimeout(() => {
			conn.reconnectTimer = undefined;
			this.connect(serverName, conn);
		}, delay);
	}
}
