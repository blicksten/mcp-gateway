/**
 * Phase 11 — Architectural pivot: extension-as-bridge for Claude Code 2.1.123.
 *
 * Subscribes to ServerDataCache.onDidRefresh and reflects gateway backend
 * state into ~/.claude.json::mcpServers under namespace prefix
 * (default `mcp-gateway:`). Each managed entry is HTTP type pointing at
 * the gateway proxy `<apiUrl>/mcp/<backend>` with Bearer auth header.
 *
 * Concurrency model:
 *   - CAS-style optimistic write: sha256 raw text before+after parse;
 *     retry the merge if the file mutated between read and write.
 *   - Atomic rename via temp+rename; retry on Windows EBUSY/EPERM.
 *   - JSON.parse retry: tolerates a transient mid-write file from
 *     another writer (Claude Code itself, `claude mcp add` CLI).
 *   - Diff-only writes: nothing happens when desired managed-set
 *     equals existing managed-set (canonicalized deep-equal).
 *
 * Co-tenancy guarantees:
 *   - Foreign keys (anything NOT prefix-matched) are preserved
 *     verbatim and in original insertion order.
 *   - Cleanup is gateway-as-truth: managed entries not in current
 *     backend list are removed on each refresh. Manual purge command
 *     (mcpGateway.cleanupClaudeConfig) drops ALL managed entries.
 *
 * See docs/PLAN-mcp-lifecycle.md §Phase 11 for the architecture
 * decision table and PAL-validated rationale.
 */

import * as crypto from 'node:crypto';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import * as vscode from 'vscode';
import type { ServerView } from './types';
import type { ServerDataCache, CacheRefreshPayload } from './server-data-cache';
import type { AuthHeaderProvider } from './gateway-client';
import { logger } from './logger';

const SOURCE = 'claude-config-sync';

const CAS_RETRY_BUDGET = 5;
const PARSE_RETRY_DELAY_MS = 50;
const PARSE_RETRY_BUDGET = 5;
const RENAME_RETRY_DELAY_MS = 100;
const RENAME_RETRY_BUDGET = 5;

// Telemetry counter exposed via getCasRetryCount() for test assertions.
// Increments every time the CAS-retry branch fires (hash mismatch detected).
let casRetryCounter = 0;

/**
 * Live options — getters allow `mcpGateway.claudeConfigSync.*` settings
 * to take effect on the next refresh tick without an extension reload.
 */
export interface ClaudeConfigSyncOptions {
	enabled: () => boolean;
	namespacePrefix: () => string;
	configPath: () => string;
	gatewayUrl: () => string;
	authHeader: AuthHeaderProvider;
	/**
	 * Optional aggregate-endpoint entry name. When non-empty, an extra
	 * managed entry under this name points at `<gatewayUrl>/mcp` (no
	 * backend suffix) so Claude Code's /mcp panel exposes the aggregate
	 * gateway alongside per-backend entries. Default `mcp-gateway`
	 * matches the legacy plugin name.
	 */
	aggregateEntryName?: () => string;
}

export interface ManagedHttpEntry {
	type: 'http';
	url: string;
	headers?: Record<string, string>;
}

export interface PreviewDiff {
	added: string[];
	removed: string[];
	updated: string[];
	unchanged: string[];
}

interface ClaudeJson {
	mcpServers?: Record<string, unknown>;
	[key: string]: unknown;
}

export class ClaudeConfigSync implements vscode.Disposable {
	private readonly cache: ServerDataCache;
	private readonly opts: ClaudeConfigSyncOptions;
	private readonly subscription: vscode.Disposable;
	private disposed = false;
	// Re-entrancy guard: a refresh that fires while a previous write is
	// still in flight is dropped (the next tick will reconcile fresh state).
	// Prevents the 5s timer from racing the previous safeWrite.
	private writing = false;

	constructor(cache: ServerDataCache, opts: ClaudeConfigSyncOptions) {
		this.cache = cache;
		this.opts = opts;
		this.subscription = cache.onDidRefresh((payload) => {
			void this.onRefresh(payload).catch((err: unknown) => {
				logger.error(SOURCE, 'reconcile failed', err);
			});
		});
	}

	// --- Public API (also exercised by tests) ---

	async buildDesired(servers: ServerView[]): Promise<Record<string, ManagedHttpEntry>> {
		const prefix = this.opts.namespacePrefix();
		const baseUrl = this.opts.gatewayUrl().replace(/\/+$/, '');
		const auth = await safeCallAuth(this.opts.authHeader);
		const out: Record<string, ManagedHttpEntry> = {};

		// Aggregate-gateway entry (e.g. "mcp-gateway") — exposes the
		// universal /mcp endpoint with meta-tools (gateway.list_servers,
		// gateway.list_tools, gateway.invoke). Skipped when
		// aggregateEntryName getter returns empty.
		const aggName = this.opts.aggregateEntryName?.() ?? '';
		if (aggName.length > 0) {
			const aggEntry: ManagedHttpEntry = {
				type: 'http',
				url: `${baseUrl}/mcp`,
			};
			if (auth) {
				aggEntry.headers = { Authorization: auth };
			}
			out[aggName] = aggEntry;
		}

		for (const s of servers) {
			const key = `${prefix}${s.name}`;
			const entry: ManagedHttpEntry = {
				type: 'http',
				url: `${baseUrl}/mcp/${encodeURIComponent(s.name)}`,
			};
			if (auth) {
				entry.headers = { Authorization: auth };
			}
			out[key] = entry;
		}
		return out;
	}

	/**
	 * All Claude config paths to keep in sync: the primary configPath()
	 * plus any profile-specific ~/.claude_XXX/.claude.json files discovered
	 * on disk. Lets per-profile instances (e.g. claude-personal) see
	 * gateway servers without manual configuration.
	 * NOTE: avoid "star-slash" in this comment — it closes the JSDoc block.
	 */
	getAllConfigPaths(): string[] {
		const primary = this.opts.configPath();
		const paths = new Set<string>();
		paths.add(primary);
		try {
			const home = os.homedir();
			const entries = fs.readdirSync(home, { withFileTypes: true });
			for (const e of entries) {
				if (e.isDirectory() && e.name.startsWith('.claude')) {
					const candidate = path.join(home, e.name, '.claude.json');
					if (fs.existsSync(candidate)) {
						paths.add(candidate);
					}
				}
			}
		} catch { /* best-effort — missing home or permission error */ }
		return [...paths];
	}

	async reconcile(servers: ServerView[]): Promise<void> {
		const desired = await this.buildDesired(servers);
		// Best-effort multi-path: primary path failures propagate (callers depend
		// on the primary succeeding); secondary profile paths are best-effort.
		const paths = this.getAllConfigPaths();
		const [primary, ...secondaries] = paths;
		if (primary) {
			await this.safeUpdateMcpServers(desired, primary);
		}
		await Promise.allSettled(
			secondaries.map((p) => this.safeUpdateMcpServers(desired, p).catch((err: unknown) => {
				logger.warn(SOURCE, `secondary config path update failed: ${p}`, err);
			})),
		);
	}

	async cleanup(): Promise<void> {
		const paths = this.getAllConfigPaths();
		const [primary, ...secondaries] = paths;
		if (primary) {
			await this.safeUpdateMcpServers({}, primary);
		}
		await Promise.allSettled(
			secondaries.map((p) => this.safeUpdateMcpServers({}, p).catch((err: unknown) => {
				logger.warn(SOURCE, `secondary config path cleanup failed: ${p}`, err);
			})),
		);
	}

	async preview(servers: ServerView[]): Promise<PreviewDiff> {
		const desired = await this.buildDesired(servers);
		const { parsed } = await this.readWithRetry(this.opts.configPath());
		const existing = (parsed.mcpServers ?? {}) as Record<string, unknown>;
		const currentManaged = this.pickManaged(existing);
		const added: string[] = [];
		const removed: string[] = [];
		const updated: string[] = [];
		const unchanged: string[] = [];
		for (const k of Object.keys(desired)) {
			if (!(k in currentManaged)) { added.push(k); continue; }
			if (deepEqualJson(currentManaged[k], desired[k])) { unchanged.push(k); }
			else { updated.push(k); }
		}
		for (const k of Object.keys(currentManaged)) {
			if (!(k in desired)) { removed.push(k); }
		}
		added.sort(); removed.sort(); updated.sort(); unchanged.sort();
		return { added, removed, updated, unchanged };
	}

	dispose(): void {
		this.disposed = true;
		this.subscription.dispose();
	}

	// --- Internals ---

	private async onRefresh(payload: CacheRefreshPayload): Promise<void> {
		if (this.disposed) { return; }
		if (!this.opts.enabled()) { return; }
		// Gateway down: do not blow away managed entries — leave the
		// last-good state until the daemon recovers. Avoids /mcp panel
		// flicker when the daemon momentarily drops.
		if (payload.lastRefreshFailed) { return; }
		if (this.writing) { return; }
		this.writing = true;
		try {
			await this.reconcile(payload.servers);
		} finally {
			this.writing = false;
		}
	}

	/**
	 * Returns the subset of `obj` that is owned by this extension —
	 * keys matching `namespacePrefix` plus the optional aggregate-entry
	 * name. Used for diff comparison and gateway-as-truth cleanup.
	 */
	private pickManaged(obj: Record<string, unknown>): Record<string, unknown> {
		const prefix = this.opts.namespacePrefix();
		const aggName = this.opts.aggregateEntryName?.() ?? '';
		const out: Record<string, unknown> = {};
		for (const [k, v] of Object.entries(obj)) {
			if (k.startsWith(prefix) || (aggName.length > 0 && k === aggName)) {
				out[k] = v;
			}
		}
		return out;
	}

	private isManagedKey(k: string): boolean {
		const prefix = this.opts.namespacePrefix();
		const aggName = this.opts.aggregateEntryName?.() ?? '';
		return k.startsWith(prefix) || (aggName.length > 0 && k === aggName);
	}

	private async safeUpdateMcpServers(
		desiredManaged: Record<string, ManagedHttpEntry>,
		target: string,
	): Promise<void> {

		for (let attempt = 1; attempt <= CAS_RETRY_BUDGET; attempt++) {
			const { hash, parsed } = await this.readWithRetry(target);
			const existingServers = (parsed.mcpServers ?? {}) as Record<string, unknown>;
			const currentManaged = this.pickManaged(existingServers);

			if (deepEqualJson(currentManaged, desiredManaged)) {
				return;
			}

			// Foreign keys preserved in original insertion order; managed
			// entries appended after — gives stable layout when only
			// gateway state changes.
			const merged: Record<string, unknown> = {};
			for (const [k, v] of Object.entries(existingServers)) {
				if (!this.isManagedKey(k)) { merged[k] = v; }
			}
			for (const [k, v] of Object.entries(desiredManaged)) {
				merged[k] = v;
			}

			const nextDoc: ClaudeJson = { ...parsed, mcpServers: merged };
			const out = JSON.stringify(nextDoc, null, 2) + '\n';

			// CAS commit: write tmp first, then re-stat target IMMEDIATELY
			// before rename. If hash drifted while we built+wrote the tmp,
			// discard tmp and restart from scratch with fresh state. This
			// narrows the race window to a single rename syscall (microseconds)
			// rather than spanning the build+writeFile span (multi-ms).
			const committed = await this.writeAtomicWithCas(target, out, hash);
			if (!committed) {
				casRetryCounter++;
				logger.debug(
					SOURCE,
					`cas_retry attempt=${attempt} reason=hash_changed_pre_rename`,
				);
				continue;
			}
			logger.info(
				SOURCE,
				`updated mcpServers managed=${Object.keys(desiredManaged).length} ` +
					`foreign=${Object.keys(merged).length - Object.keys(desiredManaged).length}`,
			);
			return;
		}
		throw new Error(
			`CAS retry budget exhausted (${CAS_RETRY_BUDGET}) for ${target}`,
		);
	}

	private async readRaw(target: string): Promise<{ raw: string; hash: string }> {
		try {
			const raw = await fs.promises.readFile(target, 'utf8');
			const hash = crypto.createHash('sha256').update(raw).digest('hex');
			return { raw, hash };
		} catch (err) {
			if ((err as NodeJS.ErrnoException).code === 'ENOENT') {
				return { raw: '', hash: hashOfEmpty() };
			}
			throw err;
		}
	}

	private async readWithRetry(target: string): Promise<{
		raw: string;
		hash: string;
		parsed: ClaudeJson;
	}> {
		let lastErr: unknown = null;
		for (let attempt = 1; attempt <= PARSE_RETRY_BUDGET; attempt++) {
			const { raw, hash } = await this.readRaw(target);
			if (raw.length === 0 || raw.trim() === '') {
				return { raw, hash, parsed: {} };
			}
			try {
				const parsed = JSON.parse(raw) as ClaudeJson;
				return { raw, hash, parsed };
			} catch (err) {
				if (err instanceof SyntaxError) {
					lastErr = err;
					logger.debug(
						SOURCE,
						`parse_retry attempt=${attempt} reason=syntax`,
					);
					await sleep(PARSE_RETRY_DELAY_MS);
					continue;
				}
				throw err;
			}
		}
		throw lastErr ?? new Error('readWithRetry exhausted');
	}

	/**
	 * Write `content` to `target` atomically AND commit only if the file's
	 * hash still equals `expectedHash` immediately before rename. Returns
	 * `true` on commit, `false` when CAS detected drift (caller restarts).
	 *
	 * The CAS check happens after writing the tmp but before rename, so the
	 * race window between check and rename is just the rename syscall
	 * itself (microseconds). Earlier checks would leave a multi-ms window
	 * spanning the build + writeFile.
	 */
	private async writeAtomicWithCas(
		target: string,
		content: string,
		expectedHash: string,
	): Promise<boolean> {
		const dir = path.dirname(target);
		const rand = crypto.randomBytes(6).toString('hex');
		const tmp = path.join(dir, `${path.basename(target)}.tmp.${rand}`);

		await fs.promises.writeFile(tmp, content, { encoding: 'utf8', mode: 0o600 });

		// Final CAS check immediately before rename. Reading the file is
		// the cheapest way to detect a competing writer in the gap between
		// our parse and our commit.
		const { hash: hashJustBeforeRename } = await this.readRaw(target);
		if (hashJustBeforeRename !== expectedHash) {
			await safeUnlink(tmp);
			return false;
		}

		let lastErr: unknown = null;
		for (let attempt = 1; attempt <= RENAME_RETRY_BUDGET; attempt++) {
			try {
				await fs.promises.rename(tmp, target);
				return true;
			} catch (err) {
				lastErr = err;
				const code = (err as NodeJS.ErrnoException).code;
				// Windows: EBUSY/EPERM/EACCES on rename when CC has a
				// transient handle on the target. Retry with backoff.
				if (code === 'EBUSY' || code === 'EPERM' || code === 'EACCES') {
					logger.debug(
						SOURCE,
						`rename_retry attempt=${attempt} code=${code}`,
					);
					await sleep(RENAME_RETRY_DELAY_MS);
					continue;
				}
				await safeUnlink(tmp);
				throw err;
			}
		}
		await safeUnlink(tmp);
		throw lastErr ?? new Error('writeAtomic rename exhausted');
	}
}

/**
 * Test-facing CAS retry counter accessor. Exposed so tests asserting that
 * the CAS-retry path actually fires can do so without relying on log scraping.
 */
export function getCasRetryCount(): number {
	return casRetryCounter;
}

export function _resetCasRetryCount(): void {
	casRetryCounter = 0;
}

// --- module-private helpers (exported for tests) ---

export function pickPrefix(
	obj: Record<string, unknown>,
	prefix: string,
): Record<string, unknown> {
	const out: Record<string, unknown> = {};
	for (const [k, v] of Object.entries(obj)) {
		if (k.startsWith(prefix)) { out[k] = v; }
	}
	return out;
}

export function deepEqualJson(a: unknown, b: unknown): boolean {
	return canonicalize(a) === canonicalize(b);
}

function canonicalize(v: unknown): string {
	if (v === null || v === undefined) { return JSON.stringify(v); }
	if (Array.isArray(v)) {
		return '[' + v.map(canonicalize).join(',') + ']';
	}
	if (typeof v === 'object') {
		const obj = v as Record<string, unknown>;
		const keys = Object.keys(obj).sort();
		const parts = keys.map((k) => JSON.stringify(k) + ':' + canonicalize(obj[k]));
		return '{' + parts.join(',') + '}';
	}
	return JSON.stringify(v);
}

async function safeCallAuth(provider: AuthHeaderProvider): Promise<string | undefined> {
	try {
		return await provider();
	} catch {
		return undefined;
	}
}

async function safeUnlink(p: string): Promise<void> {
	try { await fs.promises.unlink(p); } catch { /* ignore */ }
}

function sleep(ms: number): Promise<void> {
	return new Promise((resolve) => setTimeout(resolve, ms));
}

function hashOfEmpty(): string {
	return crypto.createHash('sha256').update('').digest('hex');
}

export function defaultClaudeJsonPath(): string {
	return path.join(os.homedir(), '.claude.json');
}
