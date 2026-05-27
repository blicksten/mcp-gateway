/**
 * SAP Picker importer — spawns `mcp-ctl credential list-structured --json`
 * with argv-array exec (no shell) and parses the result into picker rows.
 *
 * Why this exists separately from the daemon's /api/v1/sap/picker-snapshot
 * endpoint: the daemon has no slot for the KeePass master password and
 * intentionally never sees it. Password lives in the operator's head and
 * is supplied at SapPickerPanel.refresh() time via vscode.window.showInputBox,
 * then piped to mcp-ctl's stdin (--password-stdin). The daemon endpoint
 * is a T-A.1 contract stub that returns empty rows — we bypass it entirely.
 *
 * Security invariants (mirror keepass-importer.ts T12B.3):
 *   - execFile with an argv array; the shell never sees user-controlled
 *     paths (kdbx, landscape).
 *   - Master password piped via stdin (--password-stdin), never on argv
 *     or in env.
 *   - child stdout / stderr are NEVER logged. stdout carries the SID/user
 *     intersection; stderr may echo paths or KeePass warnings.
 *   - maxBuffer set explicitly so an oversized landscape ∪ KP intersection
 *     cannot exhaust extension-host memory.
 *
 * Output JSON contract (matches Go `structuredRow` in
 * cmd/mcp-ctl/credential_list_structured.go):
 *
 *   [{ "sid": "ABA", "client": "800", "user": "NAUMOV", "kpMissing": false }, ...]
 */

import { spawn } from 'node:child_process';

/** Maximum stdout+stderr buffer size (1 MB — same envelope as keepass-importer). */
const MAX_BUFFER = 1 << 20;

/** Default spawn timeout for mcp-ctl. Landscape XML parsing + KDBX decode
 *  on a typical operator vault is well under 10 s; 30 s leaves headroom
 *  for slow corporate fileshares (Include URLs may point at UNC paths). */
const DEFAULT_TIMEOUT_MS = 30_000;

/** Mirrors Go structuredRow (cmd/mcp-ctl/credential_list_structured.go:13).
 *  Field names match the JSON tag casing, NOT Go field names — this is the
 *  wire shape. */
export interface PickerListRow {
	sid: string;
	client: string;
	user: string;
	kpMissing: boolean;
}

export interface PickerListOptions {
	mcpCtlPath: string;        // absolute path or "mcp-ctl" (resolved via PATH)
	kdbxPath: string;          // absolute path to the KeePass vault
	landscapePath: string;     // absolute path to SAPUILandscape.xml
	/** Buffer (not string!) so non-ASCII passwords are written to stdin
	 *  byte-for-byte without any default-encoding pass through Node's
	 *  Writable stream. Matches claude-team-control/vscode-dashboard
	 *  CredentialManager._runScript pattern. */
	masterPassword: Buffer;
	keyfile?: string;
	timeoutMs?: number;
}

/** Raised by listPickerRows on any failure. Keeps child stderr out of message. */
export class SapPickerImportError extends Error {
	constructor(message: string, public readonly exitCode?: number) {
		super(message);
		this.name = 'SapPickerImportError';
	}
}

/**
 * Spawn mcp-ctl and return the parsed picker rows.
 *
 * Failure modes:
 *   - ENOENT on mcp-ctl: SapPickerImportError("mcp-ctl: not found ...")
 *   - landscape parse error: non-zero exit, stderr first line surfaced
 *   - wrong KeePass password: non-zero exit, stderr first line surfaced
 *   - JSON parse failure: SapPickerImportError("output was not valid JSON ...")
 *
 * Caller (SapPickerPanel) maps these to webview banner messages.
 */
export async function listPickerRows(opts: PickerListOptions): Promise<PickerListRow[]> {
	const args = [
		'credential', 'list-structured',
		'--kdbx', opts.kdbxPath,
		'--landscape', opts.landscapePath,
		'--password-stdin',
	];
	if (opts.keyfile) {
		args.push('--keyfile', opts.keyfile);
	}

	const timeoutMs = opts.timeoutMs ?? DEFAULT_TIMEOUT_MS;

	const stdout: string = await new Promise((resolve, reject) => {
		// MCP_GATEWAY_AUTH_TOKEN blanked — same precaution as keepass-importer.
		// `credential list-structured` does not hit the authed daemon either.
		const childEnv = { ...process.env, MCP_GATEWAY_AUTH_TOKEN: '' };

		// spawn (not execFile) so we can write the password as a Buffer
		// directly to stdin — mirrors claude-team-control/vscode-dashboard
		// CredentialManager._runScript. execFile + string-concat
		// `pw + '\n'` routes through Node's Writable default encoding,
		// which can byte-differ from the buffer's raw bytes for non-ASCII
		// passwords (the symptom: HMAC-SHA256 mismatch on a correct
		// Cyrillic master password — reported 2026-05-27).
		let settled = false;
		const stdoutChunks: Buffer[] = [];
		const stderrChunks: Buffer[] = [];

		const child = spawn(opts.mcpCtlPath, args, {
			shell: false,
			stdio: ['pipe', 'pipe', 'pipe'],
			windowsHide: true,
			env: childEnv,
		});

		const settle = (fn: () => void) => {
			if (settled) { return; }
			settled = true;
			clearTimeout(timer);
			fn();
		};

		const timer = setTimeout(() => {
			if (child.exitCode === null) { child.kill(); }
			settle(() => reject(new SapPickerImportError(
				`mcp-ctl (${opts.mcpCtlPath}) timed out after ${timeoutMs}ms`,
			)));
		}, timeoutMs);

		child.stdout?.on('data', (c: Buffer) => stdoutChunks.push(c));
		child.stderr?.on('data', (c: Buffer) => stderrChunks.push(c));

		child.on('error', (err) => {
			settle(() => reject(new SapPickerImportError(
				`mcp-ctl (${opts.mcpCtlPath}) spawn error: ${err.message}`,
			)));
		});

		child.on('close', (code) => {
			if (Buffer.concat(stdoutChunks).length > MAX_BUFFER) {
				settle(() => reject(new SapPickerImportError(
					`mcp-ctl (${opts.mcpCtlPath}) stdout exceeded ${MAX_BUFFER}-byte cap`,
				)));
				return;
			}
			if (code !== 0) {
				const stderrHead = Buffer.concat(stderrChunks).toString('utf8').split(/\r?\n/, 1)[0] ?? '';
				settle(() => reject(new SapPickerImportError(
					`mcp-ctl (${opts.mcpCtlPath}) failed${stderrHead ? ': ' + stderrHead : ''}`,
					code ?? undefined,
				)));
				return;
			}
			settle(() => resolve(Buffer.concat(stdoutChunks).toString('utf8')));
		});

		if (!child.stdin) {
			settle(() => reject(new SapPickerImportError('mcp-ctl child has no stdin pipe')));
			return;
		}
		child.stdin.on('error', (err) => {
			settle(() => reject(new SapPickerImportError(`stdin write failed: ${err.message}`)));
		});
		// Write password Buffer DIRECTLY (byte-for-byte, no string
		// re-encoding), then a separate newline write — same shape as
		// claude-team-control/vscode-dashboard:
		//   proc.stdin.write(masterPasswordBuf);
		//   proc.stdin.write("\n", () => proc.stdin.end());
		// Go readPasswordStdin needs the trailing newline to bound
		// bufio.Reader.ReadBytes('\n').
		child.stdin.write(opts.masterPassword);
		child.stdin.write('\n', () => child.stdin?.end());
	});

	let payload: unknown;
	try {
		payload = JSON.parse(stdout);
	} catch {
		throw new SapPickerImportError('mcp-ctl output was not valid JSON (contract mismatch?)');
	}

	if (!Array.isArray(payload)) {
		throw new SapPickerImportError('mcp-ctl JSON top-level is not an array');
	}

	const rows: PickerListRow[] = [];
	for (const r of payload) {
		if (!r || typeof r !== 'object') { continue; }
		const obj = r as Record<string, unknown>;
		// Defensive: missing fields → row excluded. The Go side always
		// emits all four fields, but a future contract bump should not
		// crash the picker.
		if (typeof obj.sid !== 'string' || obj.sid.length === 0) { continue; }
		rows.push({
			sid: obj.sid,
			client: typeof obj.client === 'string' ? obj.client : '',
			user: typeof obj.user === 'string' ? obj.user : '',
			kpMissing: obj.kpMissing === true,
		});
	}

	return rows;
}
