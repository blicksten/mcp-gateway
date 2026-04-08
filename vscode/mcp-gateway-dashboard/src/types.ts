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
