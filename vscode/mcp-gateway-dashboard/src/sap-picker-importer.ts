/**
 * SAP Picker importer — spawns `python <sap-credentials.py> --list-all
 * --stdin --db <kdbx>` and parses the JSON output.
 *
 * VERBATIM PORT of claude-team-control/vscode-dashboard's
 * CredentialManager.listAllEntries (services/credential-manager.ts L131-217).
 * The earlier mcp-ctl + gokeepasslib path rejected the operator's KDBX
 * with HMAC-SHA256 mismatch despite a 100%-correct master password
 * (reported 2026-05-27). pykeepass (Python) opens the SAME KDBX with
 * the SAME password without issue — verified live by the operator on
 * 2026-05-27. Switching to the proven pipeline.
 *
 * Security invariants (mirror team-local + keepass-importer):
 *   - spawn with explicit argv (shell:false) — no shell expansion.
 *   - Master password Buffer piped DIRECTLY to stdin (no string concat,
 *     no execFile default-encoding pass-through). Trailing newline
 *     written separately so the Python `sys.stdin.readline().rstrip('\n')`
 *     bounds correctly.
 *   - child stdout/stderr captured as Buffer; stdout parsed as JSON
 *     once and discarded. stderr surfaces first line only on non-zero
 *     exit; never logged to user-visible toasts (may contain paths).
 *
 * Script discovery:
 *   1. mcpGateway.sapCredentialsPyPath setting if set explicitly.
 *   2. Derived from mcpDashboard.orchestratorPath (the team-local
 *      dashboard's setting — most operators already have it set):
 *        ${orchestratorPath}/../scripts/sap-credentials.py
 *   3. Hard-coded conventional path
 *      ~/claude-workspace/claude-team-control/scripts/sap-credentials.py
 *      as last resort (matches the standard operator-machine layout).
 */

import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { spawn } from 'node:child_process';
import * as vscode from 'vscode';

/** Output buffer cap. 1 MiB matches keepass-importer + team-local — a
 *  KDBX with so many entries that JSON exceeds 1 MiB is pathological. */
const MAX_BUFFER = 1 << 20;

/** Spawn timeout. team-local listAllEntries uses 15 s; we match. */
const DEFAULT_TIMEOUT_MS = 15_000;

/** Row shape returned to the caller. Same fields the previous mcp-ctl-
 *  backed implementation exposed, so SapPickerPanel.augmentWithCache
 *  is unchanged. */
export interface PickerListRow {
	sid: string;
	client: string;
	user: string;
	/** Always false in the python-pykeepass path — every returned row
	 *  IS a KP entry. The hybrid-with-landscape intersection that set
	 *  this to true for landscape-only SIDs lived in the old mcp-ctl
	 *  path; reintroduce it via a separate landscape spawn if/when the
	 *  operator surface needs "available but no creds" rows again. */
	kpMissing: boolean;
}

export interface PickerListOptions {
	/** Absolute path to the KDBX. */
	kdbxPath: string;
	/** Master password as Buffer — written byte-for-byte to stdin. */
	masterPassword: Buffer;
	/** Absolute path to sap-credentials.py — resolved by
	 *  resolveSapCredentialsPy() in SapPickerPanel. */
	scriptPath: string;
	/** Python interpreter to spawn. Defaults to "python" (resolved via
	 *  PATH) — matches the team-local invocation. Override via
	 *  mcpGateway.pythonPath setting when "python" isn't on PATH or
	 *  refers to the wrong interpreter. */
	pythonPath?: string;
	timeoutMs?: number;
}

export class SapPickerImportError extends Error {
	constructor(
		message: string,
		public readonly exitCode?: number,
		/** Set when the stderr first line contains "Wrong master password" —
		 *  signals SapPickerPanel to evict the SecretStorage entry and
		 *  re-prompt. */
		public readonly wrongPassword?: boolean,
		/** Set when stderr mentions pykeepass — actionable hint that the
		 *  Python module is missing. */
		public readonly pykeepassMissing?: boolean,
	) {
		super(message);
		this.name = 'SapPickerImportError';
	}
}

/**
 * Resolve sap-credentials.py path from settings + filesystem probes.
 * Returns null if no candidate exists.
 *
 * Priority:
 *   1. mcpGateway.sapCredentialsPyPath (explicit override)
 *   2. ${mcpDashboard.orchestratorPath}/../scripts/sap-credentials.py
 *      (team-local convention; operator usually has orchestratorPath set)
 *   3. ~/claude-workspace/claude-team-control/scripts/sap-credentials.py
 */
export function resolveSapCredentialsPy(): string | null {
	const gatewayCfg = vscode.workspace.getConfiguration('mcpGateway');
	const explicit = gatewayCfg.get<string>('sapCredentialsPyPath', '').trim();
	if (explicit && fs.existsSync(explicit)) {
		return explicit;
	}

	const dashCfg = vscode.workspace.getConfiguration('mcpDashboard');
	const orchPath = dashCfg.get<string>('orchestratorPath', '').trim();
	if (orchPath) {
		const derived = path.resolve(orchPath, '..', 'scripts', 'sap-credentials.py');
		if (fs.existsSync(derived)) {
			return derived;
		}
	}

	const conventional = path.join(
		os.homedir(),
		'claude-workspace',
		'claude-team-control',
		'scripts',
		'sap-credentials.py',
	);
	if (fs.existsSync(conventional)) {
		return conventional;
	}

	return null;
}

/**
 * Run python sap-credentials.py --list-all --stdin and return parsed
 * picker rows. Buffer pipe for the password — same shape as
 * claude-team-control/vscode-dashboard CredentialManager.listAllEntries.
 *
 * Failure modes:
 *   - "Wrong master password" in stderr → SapPickerImportError with
 *     wrongPassword=true; caller evicts SecretStorage + retries.
 *   - "pykeepass" in stderr → SapPickerImportError with
 *     pykeepassMissing=true; caller surfaces an install hint.
 *   - Other non-zero exit → generic SapPickerImportError with the
 *     stderr first line; never includes stdout (may contain user/SID).
 */
export async function listPickerRows(opts: PickerListOptions): Promise<PickerListRow[]> {
	const pythonPath = opts.pythonPath ?? 'python';
	const args = [opts.scriptPath, '--list-all', '--stdin', '--db', opts.kdbxPath];
	const timeoutMs = opts.timeoutMs ?? DEFAULT_TIMEOUT_MS;

	return new Promise<PickerListRow[]>((resolve, reject) => {
		let settled = false;
		const stdoutChunks: Buffer[] = [];
		const stderrChunks: Buffer[] = [];

		const child = spawn(pythonPath, args, {
			shell: false,
			stdio: ['pipe', 'pipe', 'pipe'],
			windowsHide: true,
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
				`python (${pythonPath}) timed out after ${timeoutMs}ms`,
			)));
		}, timeoutMs);

		child.stdout?.on('data', (c: Buffer) => stdoutChunks.push(c));
		child.stderr?.on('data', (c: Buffer) => stderrChunks.push(c));

		child.on('error', (err) => {
			settle(() => reject(new SapPickerImportError(
				`python (${pythonPath}) spawn error: ${err.message}`,
			)));
		});

		child.on('close', (code) => {
			if (Buffer.concat(stdoutChunks).length > MAX_BUFFER) {
				settle(() => reject(new SapPickerImportError(
					`python stdout exceeded ${MAX_BUFFER}-byte cap`,
				)));
				return;
			}
			if (code !== 0) {
				const stderr = Buffer.concat(stderrChunks).toString('utf8').trim();
				const stderrHead = stderr.split(/\r?\n/, 1)[0] ?? '';
				const wrongPassword = stderr.includes('Wrong master password');
				const pykeepassMissing = stderr.toLowerCase().includes('pykeepass');
				settle(() => reject(new SapPickerImportError(
					`sap-credentials.py failed${stderrHead ? ': ' + stderrHead : ''}`,
					code ?? undefined,
					wrongPassword,
					pykeepassMissing,
				)));
				return;
			}
			try {
				const raw = JSON.parse(Buffer.concat(stdoutChunks).toString('utf8').trim());
				if (!Array.isArray(raw)) {
					settle(() => reject(new SapPickerImportError(
						'sap-credentials.py output is not a JSON array',
					)));
					return;
				}
				const rows: PickerListRow[] = [];
				for (const r of raw) {
					if (!r || typeof r !== 'object') { continue; }
					const obj = r as Record<string, unknown>;
					if (typeof obj.sid !== 'string' || obj.sid.length === 0) { continue; }
					rows.push({
						sid: obj.sid,
						client: typeof obj.client === 'string' ? obj.client : '',
						user: typeof obj.user === 'string' ? obj.user : '',
						kpMissing: false,
					});
				}
				settle(() => resolve(rows));
			} catch (err) {
				settle(() => reject(new SapPickerImportError(
					`sap-credentials.py output JSON parse failed: ${(err as Error).message}`,
				)));
			}
		});

		if (!child.stdin) {
			settle(() => reject(new SapPickerImportError('python child has no stdin pipe')));
			return;
		}
		child.stdin.on('error', (err) => {
			settle(() => reject(new SapPickerImportError(`stdin write failed: ${err.message}`)));
		});
		// VERBATIM from team-local CredentialManager.listAllEntries L211-212:
		//   proc.stdin?.write(this._masterPasswordBuf!);
		//   proc.stdin?.write("\n", () => proc.stdin?.end());
		// Buffer goes directly, newline as separate string write that
		// closes stdin in the callback so Python's
		// sys.stdin.readline().rstrip('\n') terminates immediately.
		child.stdin.write(opts.masterPassword);
		child.stdin.write('\n', () => child.stdin?.end());
	});
}
