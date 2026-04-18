/**
 * KeePass credential importer — spawns `mcp-ctl credential import --json`
 * with argv-array exec (no shell) and writes results into SecretStorage
 * via the CredentialStore.
 *
 * Security invariants (T12B.3 / 12B-4):
 *   - execFile with an argv array; the shell never sees user-controlled
 *     strings like the KDBX path or KeePass group name.
 *   - Master password piped via stdin (--password-stdin), never on argv
 *     or in env (where it would be visible to `ps` / task manager).
 *   - child stdout / stderr are NEVER logged. stdout carries plaintext
 *     credentials; stderr may echo paths or diagnostic lines. We read
 *     stdout into a buffer, parse as JSON, then discard. stderr is only
 *     preserved for exit-code > 0 error messages, and even then it is
 *     surfaced to the user via OutputChannel only on explicit request.
 *   - maxBuffer set explicitly so an unexpectedly large KDBX cannot
 *     exhaust extension-host memory.
 *
 * See docs/ADR-0003 §keepass-password-flow and docs/PLAN-main.md:297.
 */

import { execFile } from 'node:child_process';
import type { CredentialStore } from './credential-store';

/** Maximum stdout+stderr buffer size (1 MB). */
const MAX_BUFFER = 1 << 20;

/** Default spawn timeout for the mcp-ctl child process (30 s). */
const DEFAULT_TIMEOUT_MS = 30_000;

/** The JSON contract emitted by `mcp-ctl credential import --json`. */
export interface CredentialImportJSON {
	version: number;
	mode: 'dry-run' | 'to-env-file' | 'to-server';
	found: number;
	servers: CredentialImportServer[];
	results?: CredentialImportResult[];
}

export interface CredentialImportServer {
	name: string;
	env_vars: Record<string, string>;
	headers: Record<string, string>;
}

export interface CredentialImportResult {
	name: string;
	status: 'ok' | 'skipped' | 'failed' | 'partial';
	detail?: string;
}

export interface ImportOptions {
	mcpCtlPath: string;      // absolute path to mcp-ctl executable
	kdbxPath: string;         // absolute path to KDBX file
	masterPassword: string;   // cleared from memory by caller via zeroing
	group?: string;           // optional KeePass group filter
	timeoutMs?: number;
}

/** Raised by runKeepassImport on any failure. Keeps child stderr out of message. */
export class KeepassImportError extends Error {
	constructor(message: string, public readonly exitCode?: number) {
		super(message);
		this.name = 'KeepassImportError';
	}
}

/**
 * Run mcp-ctl and return the parsed JSON contract. Does not touch
 * SecretStorage — caller decides what to persist (applyImportedCredentials).
 *
 * execFile with an argv ARRAY guarantees no shell expansion occurs on
 * kdbxPath or group (both of which originate from user input).
 */
export async function runKeepassImport(opts: ImportOptions): Promise<CredentialImportJSON> {
	const args = [
		'credential', 'import',
		'--keepass', opts.kdbxPath,
		'--password-stdin',
		'--json',
	];
	if (opts.group) {
		args.push('--group', opts.group);
	}

	const timeoutMs = opts.timeoutMs ?? DEFAULT_TIMEOUT_MS;

	const stdout: string = await new Promise((resolve, reject) => {
		const child = execFile(
			opts.mcpCtlPath,
			args,
			{
				maxBuffer: MAX_BUFFER,
				timeout: timeoutMs,
				// No shell. No env inheritance of MCP_GATEWAY_AUTH_TOKEN
				// would not hurt here but we also don't need it —
				// credential import does not require an authed daemon
				// unless --to-server is set, which the extension never
				// uses (it writes via SecretStorage then PATCHes after).
				windowsHide: true,
			},
			(err, stdoutBuf, stderrBuf) => {
				if (err) {
					// Only the exit-code and a brief stderr first-line are
					// surfaced. Full stderr may contain paths; stdout is
					// NEVER logged.
					const stderrHead = (stderrBuf ?? '').split(/\r?\n/, 1)[0] ?? '';
					reject(new KeepassImportError(
						`mcp-ctl failed${stderrHead ? ': ' + stderrHead : ''}`,
						(err as NodeJS.ErrnoException & { code?: number }).code as number | undefined,
					));
					return;
				}
				resolve(stdoutBuf ?? '');
			},
		);

		// Pipe master password in, close stdin immediately. The trailing
		// newline is required for the Go readPasswordStdin bufio.Reader.
		if (child.stdin) {
			child.stdin.on('error', (err) => {
				reject(new KeepassImportError(`stdin write failed: ${err.message}`));
			});
			child.stdin.end(opts.masterPassword + '\n');
		}
	});

	let payload: CredentialImportJSON;
	try {
		payload = JSON.parse(stdout);
	} catch {
		throw new KeepassImportError('mcp-ctl output was not valid JSON (contract mismatch?)');
	}

	if (payload.version !== 1) {
		throw new KeepassImportError(
			`unsupported JSON contract version ${payload.version} (extension expects 1)`,
		);
	}

	return payload;
}

/** Per-server outcome produced by applyImportedCredentials. */
export interface ApplyResult {
	name: string;
	status: 'stored' | 'skipped' | 'failed';
	stored_env: number;
	stored_headers: number;
	error?: string;
}

/**
 * Apply imported credentials to SecretStorage with partial-failure
 * tolerance (T12B.4 / 12B-5). Each server is written independently —
 * a failure on one server does not prevent subsequent servers from
 * being written, and partially-written state is retained (not rolled
 * back) because:
 *   - SecretStorage has no transactional API;
 *   - the index is write-before-secret so orphans are prunable via
 *     CredentialStore.reconcile();
 *   - rolling back would require a second round of deletes that can
 *     themselves fail, compounding the problem.
 *
 * Returns a result slice the caller renders to the user. Never logs
 * credential values.
 */
export async function applyImportedCredentials(
	store: CredentialStore,
	payload: CredentialImportJSON,
): Promise<ApplyResult[]> {
	const results: ApplyResult[] = [];

	for (const srv of payload.servers) {
		const res: ApplyResult = {
			name: srv.name,
			status: 'stored',
			stored_env: 0,
			stored_headers: 0,
		};

		try {
			for (const [key, value] of Object.entries(srv.env_vars)) {
				await store.storeEnvVar(srv.name, key, value);
				res.stored_env++;
			}
			for (const [key, value] of Object.entries(srv.headers)) {
				await store.storeHeader(srv.name, key, value);
				res.stored_headers++;
			}
		} catch (err) {
			res.status = res.stored_env > 0 || res.stored_headers > 0 ? 'stored' : 'failed';
			// Only the message — never value content.
			res.error = (err as Error).message;
			if (res.status === 'failed') {
				results.push(res);
				continue;
			}
		}

		results.push(res);
	}

	return results;
}
