/**
 * Shared logger module — thin wrapper around the VSCode OutputChannel 'MCP Gateway'.
 *
 * All extension components (daemon, cache, panels, generators) write to a single
 * channel through this module, creating one unified diagnostics surface for operators.
 *
 * Usage:
 *   import { logger } from './logger';
 *   logger.info('server-data-cache', 'Auto-refresh started');
 *   logger.error('server-data-cache', 'Refresh failed', err);
 *
 * Debug lines are suppressed unless mcpGateway.verboseLogging is true.
 * The logger never throws — any internal failure is silently swallowed so
 * a log call can never break a caller.
 */

import * as vscode from 'vscode';

/** Minimal OutputChannel surface used by the logger (allows test injection). */
export interface LogChannel {
	appendLine(line: string): void;
}

let channel: LogChannel | undefined;

/** Replace the internal channel — ONLY for unit tests. */
export function _setLoggerForTests(ch: LogChannel): void {
	channel = ch;
}

/** Expose the internal channel — ONLY for unit tests. */
export function _getInternalChannel(): LogChannel | undefined {
	return channel;
}

/** Lazily create (or reuse) the shared VSCode OutputChannel. */
function getChannel(): LogChannel {
	if (!channel) {
		channel = vscode.window.createOutputChannel('MCP Gateway');
	}
	return channel;
}

/** Format an ISO timestamp prefix. */
function timestamp(): string {
	return new Date().toISOString();
}

/** Append error detail lines if an error value is provided. */
function formatError(err: unknown): string {
	if (err === undefined || err === null) { return ''; }
	const msg = err instanceof Error ? err.message : String(err);
	return `\n  Error: ${msg}`;
}

/** Build a formatted log line. */
function formatLine(level: string, source: string, msg: string, err?: unknown): string {
	return `[${timestamp()}] [${level}] [${source}] ${msg}${formatError(err)}`;
}

/** Return true when verbose/debug logging is enabled in settings. */
function isVerboseEnabled(): boolean {
	try {
		return vscode.workspace
			.getConfiguration('mcpGateway')
			.get<boolean>('verboseLogging', false);
	} catch {
		return false;
	}
}

/** Write a line to the channel, swallowing any internal error. */
function write(line: string): void {
	try {
		getChannel().appendLine(line);
	} catch {
		// Logger must never break callers — silently discard.
	}
}

export const logger = {
	/**
	 * Log an informational message.
	 * @param source  Short identifier for the calling component (e.g. 'daemon').
	 * @param msg     Human-readable message.
	 */
	info(source: string, msg: string): void {
		write(formatLine('INFO', source, msg));
	},

	/**
	 * Log a warning, optionally with an error cause.
	 * @param source  Short identifier for the calling component.
	 * @param msg     Human-readable message.
	 * @param err     Optional error value; `.message` is appended for Error instances.
	 */
	warn(source: string, msg: string, err?: unknown): void {
		write(formatLine('WARN', source, msg, err));
	},

	/**
	 * Log an error, optionally with an error cause.
	 * @param source  Short identifier for the calling component.
	 * @param msg     Human-readable message.
	 * @param err     Optional error value; `.message` is appended for Error instances.
	 */
	error(source: string, msg: string, err?: unknown): void {
		write(formatLine('ERROR', source, msg, err));
	},

	/**
	 * Log a debug/verbose message. Suppressed unless mcpGateway.verboseLogging is true.
	 * @param source  Short identifier for the calling component.
	 * @param msg     Human-readable message.
	 */
	debug(source: string, msg: string): void {
		if (!isVerboseEnabled()) { return; }
		write(formatLine('DEBUG', source, msg));
	},
};
