import * as crypto from 'node:crypto';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import * as vscode from 'vscode';

/** Cyrillic-character regex — same as
 *  claude-team-control/vscode-dashboard/src/services/credential-manager.ts
 *  _hasCyrillic. Catches Russian А-Я + lowercase plus the Ё outlier. */
const CYRILLIC_RE = /[а-яёА-ЯЁ]/;

/** Max retry attempts when KeePass rejects the password — mirrors the
 *  team-local dashboard's MAX_ATTEMPTS=3 loop in
 *  CredentialManager.getCredentials. After 3 wrong attempts the operator
 *  is bounced to a single error banner; they can re-trigger the picker
 *  to start over. */
const MAX_PASSWORD_ATTEMPTS = 3;
import type { IGatewayClient } from '../extension';
import type { ServerDataCache } from '../server-data-cache';
import {
	type PickerSnapshot,
	type PickerSnapshotRow,
	type RowState,
	type RowOverride,
	type BatchOp,
	type LifecycleEvent,
	type PickerDefaults,
	serverName,
	initRowsFromSnapshot,
	buildOpsList,
	buildOpsListWithDefaults,
	transitionRow,
	resetFailedRowsForRetry,
	runWithConcurrency,
} from '../sap-picker-state';
import {
	listPickerRows,
	fetchSapCredentials,
	resolveSapCredentialsPy,
	SapPickerImportError,
	type PickerListRow,
	type SapCredentials,
} from '../sap-picker-importer';
import { buildSapPickerHtml } from './sap-picker-html';
import { logger } from '../logger';

/** Where the picker reads KP from + which python interpreter + script to
 *  run. Built once per refresh() call from workspace configuration.
 *  Landscape XML is intentionally absent — the python-pykeepass path
 *  does not consult the landscape for SID listing (matches the team-
 *  local CredentialManager.listAllEntries behaviour). Reintroduce a
 *  landscape spawn if/when the hybrid "available but no creds" rows
 *  are needed again. */
interface PickerInputs {
	kdbxPath: string;
	pythonPath: string;
	scriptPath: string;
}

/** Wire shape for what the webview sends back on Apply / Retry. The host
 *  re-derives the BatchOp[] from this so a tampered DOM cannot bypass
 *  R-30 (kpMissing) or skip command validation. */
interface RowDiffFromWebview {
	rowKey: string;
	desired: { vsp: boolean; gui: boolean };
	override: RowOverride;
}

/** SAP Picker — hybrid landscape ∪ KeePass picker webview (Phase B).
 *
 * Holds host-side authoritative state:
 *   - latestSnapshot: last picker-snapshot REST response
 *   - rows: derived RowState[] (with per-component status from the apply
 *     state machine in sap-picker-state.ts)
 *   - applying: in-flight guard so back-to-back Apply clicks cannot stack
 *
 * Communicates with the webview script via postMessage; the script renders
 * the UI and posts back user intent (Apply, Retry, ForceKill, Refresh).
 */
export class SapPickerPanel {
	private static current: SapPickerPanel | undefined;

	private readonly panel: vscode.WebviewPanel;
	private readonly client: IGatewayClient;
	private readonly cache: ServerDataCache;
	private readonly secrets: vscode.SecretStorage;
	private readonly disposables: vscode.Disposable[] = [];
	private disposed = false;
	private applying = false;

	private latestSnapshot: PickerSnapshot = { rows: [], warnings: [] };
	private rows: RowState[] = [];

	/** Master password as Buffer (matches
	 *  claude-team-control/vscode-dashboard CredentialManager pattern).
	 *  Buffer instead of JS string so:
	 *    1. clearCache() can zero the bytes via fill(0) when the panel
	 *       disposes (JS strings are interned + immutable, cannot be
	 *       cleared).
	 *    2. spawn().stdin.write(buf) sends the raw bytes byte-for-byte
	 *       to mcp-ctl — no default-encoding pass-through that can
	 *       byte-differ for non-ASCII (Cyrillic) inputs.
	 *  Hydrated on first access from SecretStorage; on cache-miss
	 *  prompted via showInputBox, persisted via offer-to-remember dialog
	 *  AFTER a successful list-structured run (explicit operator consent,
	 *  matching the team-local "Remember KeePass password?" flow). */
	private kpMasterPasswordBuf?: Buffer;

	/** Set when the operator declines the offer-to-remember dialog in
	 *  the current panel session — suppresses the dialog until panel
	 *  reopen so we don't ask after every refresh. */
	private rememberDeclinedThisSession = false;

	/** Inputs (kdbxPath / pythonPath / scriptPath) cached after a
	 *  successful loadSnapshot — runOneOp reads them to spawn
	 *  sap-credentials.py per row for credential injection on Apply. */
	private lastInputs?: PickerInputs;

	private constructor(
		panel: vscode.WebviewPanel,
		client: IGatewayClient,
		cache: ServerDataCache,
		secrets: vscode.SecretStorage,
	) {
		this.panel = panel;
		this.client = client;
		this.cache = cache;
		this.secrets = secrets;

		this.disposables.push(this.panel.onDidDispose(() => this.dispose()));
		this.disposables.push(this.panel.webview.onDidReceiveMessage((msg: unknown) => {
			void this.handleMessage(msg);
		}));
	}

	static async createOrShow(
		extensionUri: vscode.Uri,
		client: IGatewayClient,
		cache: ServerDataCache,
		secrets: vscode.SecretStorage,
	): Promise<SapPickerPanel> {
		if (SapPickerPanel.current && !SapPickerPanel.current.disposed) {
			SapPickerPanel.current.panel.reveal();
			void SapPickerPanel.current.refresh();
			return SapPickerPanel.current;
		}

		const panel = vscode.window.createWebviewPanel(
			'mcpSapPicker',
			'SAP Picker',
			vscode.ViewColumn.One,
			{ enableScripts: true, localResourceRoots: [extensionUri], retainContextWhenHidden: true },
		);

		const instance = new SapPickerPanel(panel, client, cache, secrets);
		SapPickerPanel.current = instance;
		instance.render();
		void instance.refresh();
		return instance;
	}

	private render(): void {
		const nonce = crypto.randomBytes(16).toString('base64');
		this.panel.webview.html = buildSapPickerHtml(nonce, this.panel.webview.cspSource);
	}

	private async refresh(): Promise<void> {
		if (this.disposed) { return; }
		const snap = await this.loadSnapshot();
		if (!snap) { return; }    // loadSnapshot already posted an error / cancel banner
		this.latestSnapshot = snap;
		this.rows = initRowsFromSnapshot(snap);
		await this.postInit();
	}

	/**
	 * Build a fresh PickerSnapshot by:
	 *   1. Resolving picker inputs (kdbx / landscape / mcp-ctl paths) from
	 *      workspace settings. Missing kdbx → banner + return null.
	 *   2. Prompting for the KeePass master password (cached for the panel
	 *      lifetime). Cancelled prompt → null.
	 *   3. Spawning `mcp-ctl credential list-structured --password-stdin`
	 *      and parsing the JSON array.
	 *   4. Augmenting each row with `registered.vsp/gui` + `status.vsp/gui`
	 *      from the live gateway server cache (no extra REST round-trip).
	 *
	 * Returns null on any failure or operator cancellation — callers
	 * should treat null as "abort the current refresh path".
	 */
	private async loadSnapshot(): Promise<PickerSnapshot | null> {
		const inputs = this.resolveInputs();
		if (!inputs) { return null; }
		this.lastInputs = inputs;

		// Mirror the team-local CredentialManager retry loop: up to 3
		// password attempts (SecretStorage on attempt 1, prompt on
		// attempts 2-3 after wrong-password eviction). Any non-wrong-
		// password error breaks out immediately — only HMAC mismatches
		// trigger the retry-with-fresh-prompt path.
		for (let attempt = 1; attempt <= MAX_PASSWORD_ATTEMPTS; attempt++) {
			const have = await this.ensureMasterPassword(inputs.kdbxPath, attempt);
			if (!have) { return null; } // operator cancelled

			const proceed = await this.confirmCyrillic();
			if (!proceed) {
				// Operator wants to re-enter (likely wrong layout).
				// Clear in-memory buffer; loop falls through to the
				// next ensureMasterPassword which will prompt fresh.
				this.clearMasterPasswordCache();
				continue;
			}

			let rows: PickerListRow[];
			try {
				rows = await listPickerRows({
					kdbxPath: inputs.kdbxPath,
					masterPassword: this.kpMasterPasswordBuf!,
					scriptPath: inputs.scriptPath,
					pythonPath: inputs.pythonPath,
				});
			} catch (err) {
				const msg = err instanceof SapPickerImportError ? err.message : errorMsg(err);
				logger.error('sap-picker', `sap-credentials.py failed (attempt ${attempt})`, err);

				// sap-credentials.py + pykeepass returns "Wrong master
				// password" on stderr on auth fail; SapPickerImportError
				// surfaces that as the wrongPassword flag. Evict both
				// caches + retry the loop.
				const wrongPassword =
					(err instanceof SapPickerImportError && err.wrongPassword === true) ||
					msg.toLowerCase().includes('wrong master password');
				if (wrongPassword) {
					await this.forgetSavedPassword(inputs.kdbxPath);
					if (attempt === MAX_PASSWORD_ATTEMPTS) {
						await this.postError(
							`KeePass master password rejected after ${MAX_PASSWORD_ATTEMPTS} attempts. Click Refresh to try again.`,
						);
						return null;
					}
					continue;
				}

				// pykeepass-not-installed: surface the install hint.
				if (err instanceof SapPickerImportError && err.pykeepassMissing) {
					await this.postError(
						'pykeepass is not installed. Run: pip install pykeepass. ' +
						'Then click Refresh.',
					);
					return null;
				}

				await this.postError(`Failed to load SAP systems: ${msg}`);
				return null;
			}

			// Success path. Offer to persist the password if not yet
			// saved (separate explicit-consent dialog, matches team-
			// local pattern). Fire-and-forget so the snapshot returns
			// even if the operator dismisses the offer dialog.
			void this.offerToRememberIfNeeded(inputs.kdbxPath).catch((err) => {
				logger.warn('sap-picker', `offer-to-remember failed: ${errorMsg(err)}`);
			});

			const snapshotRows = this.augmentWithCache(rows);
			return { rows: snapshotRows, warnings: [] };
		}

		// Unreachable in practice — the loop returns null or a snapshot
		// from every branch. Defensive fallback.
		return null;
	}

	private resolveInputs(): PickerInputs | null {
		const cfg = vscode.workspace.getConfiguration('mcpGateway');
		const kdbxPath = cfg.get<string>('keepassPath', '').trim();
		if (!kdbxPath) {
			void this.postError(
				'SAP Picker needs mcpGateway.keepassPath set to your KDBX file. ' +
				'Open Settings → mcp-gateway → KeePass.',
			);
			return null;
		}

		const scriptPath = resolveSapCredentialsPy();
		if (!scriptPath) {
			void this.postError(
				'SAP Picker needs sap-credentials.py. Set mcpGateway.sapCredentialsPyPath, ' +
				'or set mcpDashboard.orchestratorPath (team-local convention — script is ' +
				'looked up at ${orchestratorPath}/../scripts/sap-credentials.py).',
			);
			return null;
		}

		// mcpGateway.pythonPath wins; falls back to bare 'python' on PATH
		// (matches the team-local dashboard's CredentialManager.spawn call).
		const pythonPath = cfg.get<string>('pythonPath', '').trim() || 'python';
		logger.info('sap-picker', `using python "${pythonPath}" + script "${scriptPath}" + kdbx "${kdbxPath}"`);

		return { kdbxPath, pythonPath, scriptPath };
	}

	/**
	 * Resolve the SAP-launcher defaults from settings, with fallback to
	 * the legacy mcpDashboard.* keys (most operators have those set from
	 * team-local dashboard). Schema-default 'uv' is the typical choice.
	 *
	 * Why each setting matters (for the package.json description copy):
	 *   - defaultVspCommand: where vsp.exe is. Spawned for VSP rows on Apply.
	 *   - defaultGuiUvProject: sap-gui-control source dir. Used by uv run.
	 *   - uvPath: uv binary that runs sap-gui-server from the project dir.
	 *   - defaultGuiMode: 'uv' is the verified-working path; 'exec' is for
	 *     setups where a single binary handles both VSP + GUI.
	 */
	/**
	 * Resolve SAP launcher defaults with a 4-layer fallback chain so
	 * the Apply path NEVER skips ops because of an opaque
	 * getConfiguration cache miss:
	 *
	 *   1. vscode.workspace.getConfiguration('mcpGateway').get(key)
	 *   2. vscode.workspace.getConfiguration('mcpDashboard').get(legacy)
	 *   3. settings.json read DIRECTLY off disk (bypasses VSCode cache)
	 *      key: mcpGateway.<X>
	 *   4. settings.json read DIRECTLY off disk
	 *      key: mcpDashboard.<legacy>
	 *
	 * Layer 3+4 exists because operators reported 2026-05-27 that
	 * getConfiguration returned empty values for keys that were
	 * present in settings.json. Reading the file directly is brutal
	 * but reliable.
	 */
	private static resolveDefaults(): PickerDefaults {
		const g = vscode.workspace.getConfiguration('mcpGateway');
		const d = vscode.workspace.getConfiguration('mcpDashboard');

		const trim = (s: string | undefined): string | undefined => {
			const t = (s ?? '').trim();
			return t.length === 0 ? undefined : t;
		};

		// Layer 3+4 — read settings.json from disk. Lazy + cached for
		// this single resolveDefaults() call (re-runs per Apply pick
		// up fresh edits).
		const jsonSettings = SapPickerPanel.readSettingsJsonDirect();
		const fromJson = (fullKey: string): string | undefined => {
			const v = jsonSettings[fullKey];
			if (typeof v !== 'string') { return undefined; }
			const t = v.trim();
			return t.length === 0 ? undefined : t;
		};

		// Settings.json FIRST for path-shaped values. Operator reported
		// 2026-05-27 that the picker passed command="uv" (relative) to
		// daemon, which rejected it as "must be an absolute path". Root
		// cause: mcpDashboard.uvPath has schema-default "uv" (relative),
		// so getConfiguration returned "uv" instead of the operator's
		// absolute path from settings.json. nullish coalescing (??)
		// accepts "uv" as a valid value and never falls through to file
		// read. Fix: file-read is layer 1 for path-shaped keys; only
		// fall back to getConfiguration when file says nothing.
		const resolved: PickerDefaults = {
			vspCommand: fromJson('mcpGateway.defaultVspCommand')
				?? fromJson('mcpDashboard.vibingPath')
				?? trim(g.get<string>('defaultVspCommand', ''))
				?? trim(d.get<string>('vibingPath', '')),
			guiUvProject: fromJson('mcpGateway.defaultGuiUvProject')
				?? fromJson('mcpDashboard.sapGuiPath')
				?? trim(g.get<string>('defaultGuiUvProject', ''))
				?? trim(d.get<string>('sapGuiPath', '')),
			uvPath: fromJson('mcpGateway.uvPath')
				?? fromJson('mcpDashboard.uvPath')
				?? trim(g.get<string>('uvPath', ''))
				?? trim(d.get<string>('uvPath', '')),
			defaultGuiMode: fromJson('mcpGateway.defaultGuiMode')
				?? trim(g.get<string>('defaultGuiMode', ''))
				?? 'uv',
		};

		logger.info('sap-picker',
			`resolveDefaults: vsp=${JSON.stringify(resolved.vspCommand)} ` +
			`guiUv=${JSON.stringify(resolved.guiUvProject)} ` +
			`uv=${JSON.stringify(resolved.uvPath)} ` +
			`mode=${JSON.stringify(resolved.defaultGuiMode)}`,
		);
		return resolved;
	}

	/**
	 * Read VSCode user settings.json off disk and JSON.parse it
	 * (stripping line + block comments first — VSCode's editor accepts
	 * JSON5 comments). Returns an empty object on any failure.
	 *
	 * Used as a defence-in-depth fallback when getConfiguration() does
	 * not surface a setting that is in fact present in the file.
	 */
	private static readSettingsJsonDirect(): Record<string, unknown> {
		const candidates: string[] = [];
		// Windows: %APPDATA%\Code\User\settings.json
		const appData = process.env.APPDATA;
		if (appData) {
			candidates.push(path.join(appData, 'Code', 'User', 'settings.json'));
		}
		// macOS / Linux fallback locations.
		const home = os.homedir();
		candidates.push(path.join(home, '.config', 'Code', 'User', 'settings.json'));
		candidates.push(path.join(home, 'Library', 'Application Support', 'Code', 'User', 'settings.json'));

		for (const p of candidates) {
			try {
				if (!fs.existsSync(p)) { continue; }
				const text = fs.readFileSync(p, 'utf8');
				// Strip JSON5 comments before parsing — VSCode's editor
				// allows // and /* */ in settings.json.
				const stripped = text
					.replace(/\/\*[\s\S]*?\*\//g, '')
					.split('\n')
					.map(line => {
						// Strip // comments but be careful about //
						// inside string literals. Simple heuristic: count
						// unescaped quotes before //; odd count = inside
						// string, leave alone.
						const dblSlash = line.indexOf('//');
						if (dblSlash < 0) { return line; }
						let inString = false;
						let escape = false;
						for (let i = 0; i < dblSlash; i++) {
							const ch = line[i];
							if (escape) { escape = false; continue; }
							if (ch === '\\') { escape = true; continue; }
							if (ch === '"') { inString = !inString; }
						}
						return inString ? line : line.substring(0, dblSlash);
					})
					.join('\n');
				const parsed = JSON.parse(stripped);
				if (parsed && typeof parsed === 'object') {
					return parsed as Record<string, unknown>;
				}
			} catch (err) {
				logger.warn('sap-picker', `readSettingsJsonDirect(${p}) failed: ${errorMsg(err)}`);
			}
		}
		return {};
	}

	/**
	 * SecretStorage key for the KeePass master password — sha256-hashed
	 * kdbxPath (lowercased on Windows) so the key is case-insensitive +
	 * fixed-width. Matches claude-team-control/vscode-dashboard's
	 * CredentialManager._secretKey:
	 *   mcpDashboard.keepassMasterPassword::${sha256(dbPath.toLowerCase())}
	 * We use our own namespace prefix so the two extensions can coexist
	 * on the same machine without aliasing each other's stored secrets.
	 */
	private static kpPasswordKey(kdbxPath: string): string {
		const hash = crypto.createHash('sha256').update(kdbxPath.toLowerCase()).digest('hex');
		return `mcpGateway.sapPicker.kpMasterPassword::${hash}`;
	}

	/**
	 * Ensure a master password is cached. Lookup order:
	 *   1. In-memory Buffer (this.kpMasterPasswordBuf).
	 *   2. SecretStorage at the hashed-kdbxPath key — silent fast path.
	 *   3. showInputBox prompt; cached in-memory ONLY (persistence is
	 *      offered separately via _offerToRememberIfNeeded AFTER a
	 *      successful list-structured run — matches team-local pattern).
	 *
	 * Returns false on operator cancel.
	 *
	 * On wrong-password from mcp-ctl, loadSnapshot calls clearCache()
	 * which zeroes the Buffer; the caller's retry loop then re-enters
	 * this method, which falls back to showInputBox (SecretStorage was
	 * also evicted in the wrong-password branch).
	 */
	private async ensureMasterPassword(kdbxPath: string, attempt: number): Promise<boolean> {
		if (this.kpMasterPasswordBuf) { return true; }

		// SecretStorage is consulted only on the FIRST attempt — a wrong-
		// password retry must always re-prompt. Matches the team-local
		// dashboard's first-attempt-only SecretStorage read.
		if (attempt === 1) {
			const key = SapPickerPanel.kpPasswordKey(kdbxPath);
			const stored = await this.secrets.get(key);
			if (stored !== undefined && stored.length > 0) {
				this.kpMasterPasswordBuf = Buffer.from(stored, 'utf8');
				logger.info('sap-picker', `KeePass master password loaded from SecretStorage (${key})`);
				return true;
			}
			if (stored !== undefined && stored.length === 0) {
				// Clean up invalid empty entry (defensive — mirrors
				// CredentialManager._doEnsurePassword L106).
				try { await this.secrets.delete(key); } catch { /* best-effort */ }
			}
		}

		const suffix = attempt > 1 ? ` (attempt ${attempt}/${MAX_PASSWORD_ATTEMPTS})` : '';
		const placeHolder = attempt > 1
			? 'Wrong password — try again'
			: 'Enter KeePass master password to retrieve SAP credentials';
		const input = await vscode.window.showInputBox({
			password: true,
			prompt: `KeePass master password${suffix}`,
			placeHolder,
			ignoreFocusOut: true,
		});
		if (input === undefined || input.length === 0) { return false; }
		this.kpMasterPasswordBuf = Buffer.from(input, 'utf8');
		return true;
	}

	/** Check for Cyrillic letters — common sign of wrong keyboard layout.
	 *  Matches CredentialManager._hasCyrillic. Surfaces a "Try anyway /
	 *  Re-enter" warning so the operator can fix layout before sinking
	 *  a wrong-password attempt. Returns true if the operator wants to
	 *  proceed, false if they want to re-enter. */
	private async confirmCyrillic(): Promise<boolean> {
		if (!this.kpMasterPasswordBuf) { return false; }
		const s = this.kpMasterPasswordBuf.toString('utf8');
		if (!CYRILLIC_RE.test(s)) { return true; }
		const choice = await vscode.window.showWarningMessage(
			'Password contains Cyrillic characters — check keyboard layout (RU → EN)?',
			'Try anyway',
			'Re-enter',
		);
		return choice === 'Try anyway';
	}

	/**
	 * Offer the operator to persist the current cached password to
	 * SecretStorage. Called ONLY after a successful list-structured run
	 * so we never persist a wrong password. If already saved, no prompt.
	 * Operator opt-out is sticky for the panel session.
	 *
	 * Mirrors claude-team-control/vscode-dashboard
	 * CredentialManager._doOfferToRemember.
	 */
	private async offerToRememberIfNeeded(kdbxPath: string): Promise<void> {
		if (this.rememberDeclinedThisSession) {
			logger.info('sap-picker', 'offer-to-remember: skipped (declined this session)');
			return;
		}
		if (!this.kpMasterPasswordBuf) {
			logger.info('sap-picker', 'offer-to-remember: skipped (no in-memory password)');
			return;
		}
		const key = SapPickerPanel.kpPasswordKey(kdbxPath);
		let existingNonEmpty = false;
		try {
			const existing = await this.secrets.get(key);
			// Treat empty stored entries as "not saved" so a leftover
			// blank from v1.33.9-era auto-save doesn't silently suppress
			// the offer dialog. Operator-reported 2026-05-27: they
			// never saw the dialog despite typing the password fresh.
			existingNonEmpty = typeof existing === 'string' && existing.length > 0;
		} catch (err) {
			logger.warn('sap-picker', `offer-to-remember: secrets.get failed: ${errorMsg(err)}`);
			return;
		}
		if (existingNonEmpty) {
			logger.info('sap-picker', `offer-to-remember: skipped (already in SecretStorage at ${key})`);
			return;
		}
		// MODAL dialog so the operator cannot miss it (toasts go to the
		// notification tray and can be dismissed without reading).
		logger.info('sap-picker', `offer-to-remember: firing modal dialog for ${key}`);
		const choice = await vscode.window.showInformationMessage(
			'Remember KeePass master password for next time? It will be stored encrypted in your OS keychain (Windows Credential Manager / macOS Keychain / Linux libsecret).',
			{ modal: true, detail: `Vault: ${kdbxPath}\n\nClick Yes to skip the prompt on future SAP Picker opens. Click No to keep entering it manually each time.` },
			'Yes', 'No',
		);
		if (choice === 'Yes' && this.kpMasterPasswordBuf) {
			try {
				await this.secrets.store(key, this.kpMasterPasswordBuf.toString('utf8'));
				logger.info('sap-picker', `KeePass master password persisted to SecretStorage (${key})`);
				void vscode.window.showInformationMessage(
					'KeePass password saved — next SAP Picker open will skip the prompt.',
				);
			} catch (err) {
				logger.warn('sap-picker', `failed to persist KP master password: ${errorMsg(err)}`);
				void vscode.window.showWarningMessage(
					`Failed to save KeePass password to keychain: ${errorMsg(err)}`,
				);
			}
		} else {
			logger.info('sap-picker', `offer-to-remember: operator chose "${choice ?? 'dismissed'}"`);
			this.rememberDeclinedThisSession = true;
		}
	}

	/**
	 * Zero + drop the cached master-password Buffer. Defends against
	 * heap inspection (Buffer.fill(0) overwrites the underlying bytes;
	 * JS strings are unzeroable so this is the only meaningful cleanup
	 * we can do). Called on dispose + on wrong-password retry path.
	 */
	private clearMasterPasswordCache(): void {
		if (this.kpMasterPasswordBuf) {
			this.kpMasterPasswordBuf.fill(0);
			this.kpMasterPasswordBuf = undefined;
		}
	}

	/**
	 * Evict the SecretStorage entry for kdbxPath. Called when mcp-ctl
	 * rejects the password (HMAC mismatch). Matches the team-local
	 * "Clean up stale SecretStorage entry on wrong password" branch.
	 */
	private async forgetSavedPassword(kdbxPath: string): Promise<void> {
		this.clearMasterPasswordCache();
		const key = SapPickerPanel.kpPasswordKey(kdbxPath);
		try {
			await this.secrets.delete(key);
			logger.info('sap-picker', `evicted KP master password from SecretStorage (${key})`);
		} catch (err) {
			logger.warn('sap-picker', `failed to evict KP master password: ${errorMsg(err)}`);
		}
	}

	/**
	 * Translate the wire shape from mcp-ctl into the snapshot row shape
	 * the webview consumes. The wire shape carries only sid/client/user/
	 * kpMissing; we synthesise the `registered` and `status` fields by
	 * looking up the expected backend names in the live gateway cache so
	 * the operator sees the same green/yellow/red indicators they see
	 * in the backends tree.
	 */
	private augmentWithCache(wireRows: PickerListRow[]): PickerSnapshotRow[] {
		const servers = this.cache.getMcpServers();
		const byName = new Map(servers.map((s) => [s.name, s]));

		return wireRows.map((r) => {
			const vspName = serverName('vsp', r.sid, r.client);
			const guiName = serverName('gui', r.sid, r.client);
			const vsp = byName.get(vspName);
			const gui = byName.get(guiName);
			return {
				sid: r.sid,
				client: r.client,
				user: r.user || undefined,
				kpMissing: r.kpMissing,
				registered: {
					vsp: Boolean(vsp),
					gui: Boolean(gui),
				},
				status: {
					vsp: vsp ? vsp.status : '',
					gui: gui ? gui.status : '',
				},
			};
		});
	}

	private async handleMessage(msg: unknown): Promise<void> {
		if (this.disposed) { return; }
		if (!msg || typeof msg !== 'object') { return; }
		const m = msg as Record<string, unknown>;
		switch (m.type) {
			case 'apply':
				await this.handleApply(m.diffs, /*onlyFailed*/ false);
				break;
			case 'retryFailed':
				await this.handleApply(m.diffs, /*onlyFailed*/ true);
				break;
			case 'forceKill':
				await this.handleForceKill(m.rowKey, m.component);
				break;
			case 'refresh':
				await this.refresh();
				break;
			case 'cancel':
				this.dispose();
				break;
			default: break;
		}
	}

	private async handleApply(rawDiffs: unknown, onlyFailed: boolean): Promise<void> {
		if (this.applying) { return; }
		const diffs = SapPickerPanel.coerceDiffs(rawDiffs);
		if (diffs === null) {
			await this.postError('Malformed Apply payload.');
			return;
		}

		// Reconcile webview-supplied diffs into the host-authoritative rows[].
		// Only desired + override are accepted from the webview; sid / client /
		// kpMissing come from latestSnapshot — the only tamper-resistant source.
		const byKey = new Map<string, RowDiffFromWebview>();
		for (const d of diffs) { byKey.set(d.rowKey, d); }

		// On retry, reset only failed rows to idle so the apply driver re-runs
		// them; succeeded rows keep their status untouched (R-04 retry).
		if (onlyFailed) {
			this.rows = resetFailedRowsForRetry(this.rows);
		}

		// Merge desired + override into rows; never touch snapshot fields.
		this.rows = this.rows.map((r) => {
			const d = byKey.get(r.key);
			if (!d) { return r; }
			return {
				...r,
				desired: { vsp: Boolean(d.desired.vsp), gui: Boolean(d.desired.gui) },
				override: SapPickerPanel.sanitizeOverride(d.override),
			};
		});

		const defaults = SapPickerPanel.resolveDefaults();
		logger.info('sap-picker', `resolved defaults: vsp=${JSON.stringify(defaults.vspCommand)} guiUv=${JSON.stringify(defaults.guiUvProject)} uv=${JSON.stringify(defaults.uvPath)} mode=${JSON.stringify(defaults.defaultGuiMode)}`);
		const { ops, skipped } = buildOpsListWithDefaults(this.rows, defaults);
		// Filter to retry-only failed rows when onlyFailed is set: keep an op
		// only if its rowKey/component is currently in a failed status.
		const filteredOps = onlyFailed ? ops.filter((op) => this.opTargetsFailedRow(op)) : ops;

		// 2026-05-27 fix: previously buildOpsList silently dropped any
		// add-op whose override.vspCommand / override.guiCommand was
		// empty. Operators reported "I clicked Apply and nothing
		// happened" because the override is empty unless they expand
		// the row and type a command. Now we (a) fill from settings
		// defaults in buildOpsListWithDefaults, and (b) surface any
		// remaining skipped ops as a clear banner so the operator
		// knows what setting is missing.
		if (filteredOps.length === 0) {
			if (skipped.length > 0 && !onlyFailed) {
				const lines = skipped.slice(0, 4).map(s => `  - ${s.rowKey} ${s.component}: ${s.reason}`);
				const more = skipped.length > 4 ? `\n  …and ${skipped.length - 4} more` : '';
				const resolvedDump =
					`\n\nResolved defaults at apply time:\n` +
					`  mcpGateway.defaultVspCommand or mcpDashboard.vibingPath -> ${defaults.vspCommand ?? '(empty)'}\n` +
					`  mcpGateway.defaultGuiUvProject or mcpDashboard.sapGuiPath -> ${defaults.guiUvProject ?? '(empty)'}\n` +
					`  mcpGateway.uvPath or mcpDashboard.uvPath -> ${defaults.uvPath ?? '(empty)'}\n` +
					`  mcpGateway.defaultGuiMode -> ${defaults.defaultGuiMode ?? '(empty)'}`;
				await this.postError(
					`Apply skipped ${skipped.length} change(s) because configuration is missing:\n${lines.join('\n')}${more}${resolvedDump}`,
				);
				return;
			}
			await this.postApplied(0, 0, onlyFailed
				? 'No failed rows to retry.'
				: 'No changes to apply.');
			return;
		}

		if (skipped.length > 0) {
			logger.warn('sap-picker', `Apply skipped ${skipped.length} op(s) due to missing config`);
		}

		await this.runBatch(filteredOps);
	}

	private opTargetsFailedRow(op: BatchOp): boolean {
		const r = this.rows.find((x) => x.key === op.rowKey);
		if (!r) { return false; }
		// After resetFailedRowsForRetry, formerly-failed rows are 'idle' again.
		// We re-derive failed-eligibility from the snapshot vs desired delta:
		// any row whose row had a failed status before reset would still appear
		// in buildOpsList output, so this branch is a defence-in-depth filter.
		const status = op.component === 'vsp' ? r.vspStatus : r.guiStatus;
		return status === 'idle' || status === 'pending';
	}

	private async runBatch(ops: BatchOp[]): Promise<void> {
		this.applying = true;
		await this.postApplying(true);

		let batchId: string | undefined;
		try {
			if (!this.client.beginSapBatch || !this.client.endSapBatch) {
				throw new Error('Gateway daemon does not support SAP batch endpoints (need v1.8+).');
			}
			const beg = (await this.client.beginSapBatch()) as { batch_id: string };
			batchId = beg.batch_id;

			// Mark all targeted rows as 'pending' before kicking off concurrent
			// work — the webview reflects this immediately.
			for (const op of ops) {
				this.rows = this.rows.map((r) =>
					transitionRow(r, { kind: 'queue', rowKey: op.rowKey, component: op.component }),
				);
			}
			await this.postRows();

			// Bounded concurrency = 4 per spike §3.5 (R-09). Driver lives in
			// the state module so the concurrency cap is unit-tested in one
			// place; this panel calls into it with a thin per-op closure.
			await runWithConcurrency(ops, (op) => this.runOneOp(op).then(() => undefined), 4);

			let failed = 0;
			let ok = 0;
			for (const op of ops) {
				const r = this.rows.find((x) => x.key === op.rowKey);
				if (!r) { continue; }
				const status = op.component === 'vsp' ? r.vspStatus : r.guiStatus;
				// `config_added` is a transient state that runOneOp always
				// transitions out of (to config_added_running or
				// config_added_start_failed). It only persists if a
				// post-add operation panics before the polling branch — in
				// that case treat it as ok (config DID land in the daemon),
				// which matches what the row badge shows the operator.
				if (status === 'config_added_running' || status === 'config_added' || status === 'removed') { ok++; }
				else { failed++; }
			}
			const summary = failed === 0
				? `Applied ${ok} change(s) successfully.`
				: `Applied ${ok}, failed ${failed}. Click "Retry failed rows" to re-run.`;
			await this.postApplied(ok, failed, summary);

			// Re-fetch snapshot so registered state + statuses reflect the new
			// gateway view; preserve the host-side error/orphan annotations by
			// merging on top of the fresh snapshot.
			void this.refreshAfterApply();
		} catch (err) {
			logger.error('sap-picker', 'apply failed', err);
			await this.postError(`Apply failed: ${errorMsg(err)}`);
		} finally {
			if (batchId) {
				try {
					if (this.client.endSapBatch) {
						await this.client.endSapBatch(batchId);
					}
				} catch (endErr) {
					logger.error('sap-picker', 'batch-end failed', endErr);
				}
			}
			this.applying = false;
			await this.postApplying(false);
		}
	}

	private async refreshAfterApply(): Promise<void> {
		if (this.disposed) { return; }
		// Use the cached cache view + cached KeePass password — no need
		// to re-prompt the operator after a successful Apply. If the
		// password somehow got cleared (panel quirk) loadSnapshot will
		// re-prompt; that's acceptable.
		const snap = await this.loadSnapshot();
		if (!snap) { return; }
		try {
			this.latestSnapshot = snap;
			// Carry forward orphan / start_failed annotations from old rows by
			// matching on rowKey — fresh snapshot resets desired = registered.
			const oldByKey = new Map(this.rows.map((r) => [r.key, r]));
			this.rows = initRowsFromSnapshot(snap).map((nr) => {
				const old = oldByKey.get(nr.key);
				if (!old) { return nr; }
				return {
					...nr,
					vspStatus: SapPickerPanel.carryForwardStatus(old.vspStatus, nr.vspStatus),
					guiStatus: SapPickerPanel.carryForwardStatus(old.guiStatus, nr.guiStatus),
					vspError: old.vspError,
					guiError: old.guiError,
					override: old.override, // override survives refresh until row collapse
				};
			});
			await this.postInit();
		} catch (err) {
			logger.warn('sap-picker', 'refresh after apply failed', err);
		}
	}

	private static carryForwardStatus(oldS: RowState['vspStatus'], freshS: RowState['vspStatus']): RowState['vspStatus'] {
		// Keep terminal-failure annotations (orphan, start_failed, removal_failed)
		// visible until the user retries / dismisses; otherwise use fresh snapshot
		// idle which reflects the now-canonical gateway state.
		if (
			oldS === 'removed_with_orphan' ||
			oldS === 'config_added_start_failed' ||
			oldS === 'removal_failed'
		) {
			return oldS;
		}
		return freshS;
	}

	private async runOneOp(op: BatchOp): Promise<void> {
		this.rows = this.rows.map((r) =>
			transitionRow(r, { kind: 'start_op', rowKey: op.rowKey, component: op.component }),
		);
		await this.postRows();

		try {
			if (op.kind === 'add') {
				// Enrich config with per-SID SAP credentials BEFORE add.
				// Without env vars vsp.exe / sap-gui-server EOF on
				// initialize (SAP login fails). Matches team-local
				// dashboard's CredentialManager.getCredentials -> spawn
				// flow: fetch creds from KP per SID, then start server.
				const enrichedConfig = await this.enrichConfigWithCreds(op);
				await this.client.addServer(op.serverName, enrichedConfig);
				this.applyEvent({ kind: 'add_ok', rowKey: op.rowKey, component: op.component });
				// Poll /health to see whether the new entry transitions to running
				// or fails to start. 5 s deadline matches spike §3.4 acceptance.
				const started = await this.pollServerRunning(op.serverName, 5_000);
				if (started === 'running') {
					this.applyEvent({ kind: 'add_started', rowKey: op.rowKey, component: op.component });
				} else {
					const err = started === 'error'
						? 'Server reported error after start'
						: 'Server did not transition to running within 5s';
					this.applyEvent({
						kind: 'add_start_failed', rowKey: op.rowKey, component: op.component, error: err,
					});
				}
			} else {
				try {
					await this.client.removeServer(op.serverName);
					this.applyEvent({ kind: 'remove_ok', rowKey: op.rowKey, component: op.component });
				} catch (err) {
					// R-28 / X4: distinguish orphan (entry removed but Stop failed)
					// from outright removal failure. The daemon surfaces orphan via
					// a `Orphan: true` JSON field once T-A.5 is wired; for now we
					// inspect the error message — the body will contain "orphan"
					// for the orphan path.
					const msg = errorMsg(err);
					if (msg.toLowerCase().includes('orphan')) {
						this.applyEvent({
							kind: 'remove_orphan', rowKey: op.rowKey, component: op.component, error: msg,
						});
					} else {
						this.applyEvent({
							kind: 'remove_failed', rowKey: op.rowKey, component: op.component, error: msg,
						});
					}
				}
			}
		} catch (err) {
			const msg = errorMsg(err);
			if (op.kind === 'add') {
				this.applyEvent({ kind: 'add_failed', rowKey: op.rowKey, component: op.component, error: msg });
			} else {
				this.applyEvent({ kind: 'remove_failed', rowKey: op.rowKey, component: op.component, error: msg });
			}
		}
		await this.postRows();
	}

	private applyEvent(ev: LifecycleEvent): void {
		this.rows = this.rows.map((r) => transitionRow(r, ev));
	}

	/** Poll /health for `name` until status reflects running / error or
	 *  deadline elapses. Returns 'running' / 'error' / 'timeout'. The
	 *  HealthResponse shape exposes total counts not per-server status, so
	 *  we cross-reference with /servers via the cache when available. */
	/**
	 * Fetch SAP credentials per SID via sap-credentials.py and merge
	 * them into the addServer config as env vars. Without this, daemon
	 * spawns vsp.exe / sap-gui-server with no SAP_PASSWORD etc. and
	 * the process EOFs on initialize (operator-reported 2026-05-27:
	 * "Applied 0, failed 6").
	 *
	 * Returns the original config if credential fetch fails — the
	 * server still gets added but in error state, matching the pre-
	 * fix behaviour so the operator sees the row in the daemon list
	 * with a clear last_error.
	 */
	private async enrichConfigWithCreds(op: BatchOp): Promise<Record<string, unknown>> {
		const baseConfig = (op.config ?? {}) as Record<string, unknown>;
		const row = this.rows.find((x) => x.key === op.rowKey);
		if (!row) { return baseConfig; }
		if (!this.lastInputs) {
			logger.warn('sap-picker', `enrichConfig(${op.serverName}): no lastInputs — cred fetch skipped`);
			return baseConfig;
		}
		if (!this.kpMasterPasswordBuf) {
			logger.warn('sap-picker', `enrichConfig(${op.serverName}): no master password buf — cred fetch skipped`);
			return baseConfig;
		}

		let creds: SapCredentials;
		try {
			creds = await fetchSapCredentials({
				sid: row.snapshot.sid,
				client: row.snapshot.client,
				scriptPath: this.lastInputs.scriptPath,
				kdbxPath: this.lastInputs.kdbxPath,
				masterPassword: this.kpMasterPasswordBuf,
				pythonPath: this.lastInputs.pythonPath,
			});
		} catch (err) {
			logger.warn('sap-picker',
				`enrichConfig(${op.serverName}): sap-credentials.py failed: ${errorMsg(err)}`);
			// Sonnet LOW (PAL fallback review 2026-05-27): re-throw on
			// wrong-password so the batch surfaces a clear "wrong KP
			// password" error instead of silently adding the server in
			// error state. Operator gets actionable diagnostic.
			if (err instanceof SapPickerImportError && err.wrongPassword) {
				await this.forgetSavedPassword(this.lastInputs.kdbxPath);
				throw new Error(
					'KeePass master password rejected by sap-credentials.py for SID ' +
					row.snapshot.sid + '. Re-open SAP Picker to re-enter (current session pwd cache cleared).',
				);
			}
			return baseConfig;
		}

		// Build env array: "KEY=VALUE" strings, the daemon's
		// ServerConfig.Env shape. Skip empty / undefined values so the
		// daemon doesn't surface bogus empty env vars.
		const envFromBase = Array.isArray(baseConfig.env) ? (baseConfig.env as unknown[]) : [];
		const envOut: string[] = [];
		for (const e of envFromBase) {
			if (typeof e === 'string') { envOut.push(e); }
		}
		for (const [k, v] of Object.entries(creds)) {
			if (typeof v !== 'string') { continue; }
			if (v.length === 0) { continue; }
			envOut.push(`${k}=${v}`);
		}

		logger.info('sap-picker',
			`enrichConfig(${op.serverName}): added ${envOut.length} env vars (${Object.keys(creds).filter(k => typeof creds[k] === 'string').join(',')})`);

		return { ...baseConfig, env: envOut };
	}

	private async pollServerRunning(name: string, timeoutMs: number): Promise<'running' | 'error' | 'timeout'> {
		const deadline = Date.now() + timeoutMs;
		while (Date.now() < deadline) {
			try {
				// Refresh cache so server view reflects fresh state.
				await this.cache.refresh();
				const servers = this.cache.getAllServers();
				const sv = servers.find((s) => s.name === name);
				if (sv) {
					if (sv.status === 'running') { return 'running'; }
					if (sv.status === 'error') { return 'error'; }
				}
			} catch (err) {
				logger.warn('sap-picker', `pollServerRunning(${name}) cache refresh failed`, err);
			}
			await sleep(500);
		}
		return 'timeout';
	}

	private async handleForceKill(rowKey: unknown, component: unknown): Promise<void> {
		if (typeof rowKey !== 'string' || (component !== 'vsp' && component !== 'gui')) {
			await this.postError('Force-kill payload malformed.');
			return;
		}
		const r = this.rows.find((x) => x.key === rowKey);
		if (!r) { return; }
		const name = serverName(component, r.snapshot.sid, r.snapshot.client);
		// VSCode confirmation — same shape as removeServer in extension.ts. The
		// actual kill is a removeServer retry which on the daemon side maps to
		// SIGKILL when the entry is in orphan state.
		const answer = await vscode.window.showWarningMessage(
			`Force-kill orphan process for "${name}"? This sends SIGKILL via the daemon.`,
			'Force kill', 'Cancel',
		);
		if (answer !== 'Force kill') { return; }
		try {
			await this.client.removeServer(name);
			this.applyEvent({ kind: 'remove_ok', rowKey, component });
			await this.postRows();
		} catch (err) {
			const msg = errorMsg(err);
			this.applyEvent({ kind: 'remove_failed', rowKey, component, error: msg });
			await this.postRows();
			await this.postError(`Force-kill failed: ${msg}`);
		}
	}

	private async postInit(): Promise<void> {
		if (this.disposed) { return; }
		await this.panel.webview.postMessage({
			type: 'init',
			rows: this.rows.map(serializeRowState),
			warnings: this.latestSnapshot.warnings,
		});
	}

	private async postRows(): Promise<void> {
		if (this.disposed) { return; }
		await this.panel.webview.postMessage({
			type: 'rows',
			rows: this.rows.map(serializeRowState),
		});
	}

	private async postApplying(active: boolean): Promise<void> {
		if (this.disposed) { return; }
		await this.panel.webview.postMessage({ type: 'applying', active });
	}

	private async postApplied(ok: number, failed: number, summary: string): Promise<void> {
		if (this.disposed) { return; }
		await this.panel.webview.postMessage({ type: 'applied', ok, failed, summary });
	}

	private async postError(message: string): Promise<void> {
		if (this.disposed) { return; }
		await this.panel.webview.postMessage({ type: 'error', message });
	}

	private static coerceDiffs(raw: unknown): RowDiffFromWebview[] | null {
		if (!Array.isArray(raw)) { return null; }
		const out: RowDiffFromWebview[] = [];
		for (const item of raw) {
			if (!item || typeof item !== 'object') { return null; }
			const r = item as Record<string, unknown>;
			if (typeof r.rowKey !== 'string' || r.rowKey.length === 0 || r.rowKey.length > 64) { return null; }
			const desired = r.desired;
			if (!desired || typeof desired !== 'object') { return null; }
			const dr = desired as Record<string, unknown>;
			out.push({
				rowKey: r.rowKey,
				desired: { vsp: dr.vsp === true, gui: dr.gui === true },
				override: SapPickerPanel.sanitizeOverride(r.override),
			});
		}
		return out;
	}

	/** Trim and length-cap override fields. Anything not a string is dropped. */
	private static sanitizeOverride(raw: unknown): RowOverride {
		const out: RowOverride = {};
		if (!raw || typeof raw !== 'object') { return out; }
		const r = raw as Record<string, unknown>;
		const cap = (v: unknown): string | undefined => {
			if (typeof v !== 'string') { return undefined; }
			const t = v.trim();
			if (t.length === 0) { return undefined; }
			if (t.length > 4096) { return undefined; }
			return t;
		};
		const vspCmd = cap(r.vspCommand);
		const guiCmd = cap(r.guiCommand);
		const guiUv = cap(r.guiUvProject);
		if (vspCmd) { out.vspCommand = vspCmd; }
		if (guiCmd) { out.guiCommand = guiCmd; }
		if (guiUv) { out.guiUvProject = guiUv; }
		return out;
	}

	dispose(): void {
		if (this.disposed) { return; }
		this.disposed = true;
		// Zero the cached master password Buffer (Buffer.fill(0)
		// overwrites the underlying bytes in-place; the JS reference
		// drops too). Matches CredentialManager.clearCache / dispose.
		this.clearMasterPasswordCache();
		if (SapPickerPanel.current === this) {
			SapPickerPanel.current = undefined;
		}
		while (this.disposables.length > 0) {
			const d = this.disposables.pop();
			try { d?.dispose(); } catch { /* best-effort cleanup */ }
		}
		try { this.panel.dispose(); } catch { /* panel may already be disposed */ }
	}

	/** Reset the singleton (for testing). */
	static _reset(): void {
		if (SapPickerPanel.current && !SapPickerPanel.current.disposed) {
			SapPickerPanel.current.dispose();
		}
		SapPickerPanel.current = undefined;
	}

	/** Expose the current rows[] for testing assertions. */
	_rows(): readonly RowState[] { return this.rows; }
}

interface SerializedRowState {
	key: string;
	sid: string;
	client: string;
	user: string;
	kpMissing: boolean;
	registered: { vsp: boolean; gui: boolean };
	status: { vsp: string; gui: string };
	desired: { vsp: boolean; gui: boolean };
	vspStatus: string;
	guiStatus: string;
	vspError?: string;
	guiError?: string;
	override: RowOverride;
}

function serializeRowState(r: RowState): SerializedRowState {
	return {
		key: r.key,
		sid: r.snapshot.sid,
		client: r.snapshot.client,
		user: r.snapshot.user ?? '',
		kpMissing: r.snapshot.kpMissing,
		registered: r.snapshot.registered,
		status: r.snapshot.status,
		desired: r.desired,
		vspStatus: r.vspStatus,
		guiStatus: r.guiStatus,
		vspError: r.vspError,
		guiError: r.guiError,
		override: r.override,
	};
}

function errorMsg(err: unknown): string {
	if (err instanceof Error) { return err.message; }
	if (typeof err === 'object' && err !== null) { return JSON.stringify(err); }
	return String(err);
}

function sleep(ms: number): Promise<void> {
	return new Promise((resolve) => setTimeout(resolve, ms));
}

// Expose for tests — types only used inside this file otherwise.
export type { SerializedRowState };
// PickerSnapshotRow re-export to keep the panel + webview wire format aligned.
export type { PickerSnapshotRow };
