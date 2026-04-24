// Type definitions matching the MCP Gateway REST API responses.
// See internal/api/server.go and internal/models/types.go for Go source.

export type ServerStatus =
	| 'stopped'
	| 'starting'
	| 'running'
	| 'degraded'
	| 'error'
	| 'restarting'
	| 'disabled';

export interface ToolInfo {
	name: string;
	description: string;
	server: string;
	input_schema?: unknown;
}

export interface ServerView {
	name: string;
	status: ServerStatus;
	transport: string;
	pid?: number;
	tools?: ToolInfo[];
	restart_count: number;
	last_error?: string;
}

export interface HealthResponse {
	status: string;
	servers: number;
	running: number;
	// Phase D.1 additions — all optional so older daemons remain compatible.
	//
	// AUDIT A-M2 NOTE: these fields are marked optional because we cannot
	// enforce that every deployed daemon is >= D.1 (pre-D.1 daemons omit
	// them). Consumers rendering uptime/pid/version MUST use a fallback
	// (e.g. `h.uptime_seconds ?? 0` or `h.version ?? 'unknown'`) rather
	// than assume presence. D.4 tree view / status bar tooltip code must
	// treat undefined as "metadata unavailable — old daemon version".
	auth?: string;
	started_at?: string;      // RFC3339 UTC
	pid?: number;
	version?: string;
	uptime_seconds?: number;
}

export interface ServerConfig {
	command?: string;
	args?: string[];
	cwd?: string;
	url?: string;
	rest_url?: string;
	health_endpoint?: string;
	disabled?: boolean;
	expose_tools?: boolean;
	env?: string[];
	headers?: Record<string, string>;
}

export interface ServerCredentials {
	env: string[];
	headers: string[];
}

export interface CredentialIndex {
	_version: number;
	servers: Record<string, ServerCredentials>;
}

export interface StatusResponse {
	status: string;
}

export interface ApiError {
	error: string;
}

export interface CallToolRequest {
	tool: string;
	arguments?: Record<string, unknown>;
}

export interface CallToolResult {
	content: Array<{ type: string; text?: string }> | null;
	isError?: boolean;
}
