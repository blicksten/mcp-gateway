import './mock-vscode';
import { strict as assert } from 'node:assert';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { spawn } from 'node:child_process';
import {
	ClaudeConfigSync,
	deepEqualJson,
	pickPrefix,
	defaultClaudeJsonPath,
	getCasRetryCount,
	_resetCasRetryCount,
	type ClaudeConfigSyncOptions,
	type ManagedHttpEntry,
} from '../claude-config-sync';
import type { ServerView } from '../types';
import type { CacheRefreshPayload, ServerDataCache } from '../server-data-cache';

// Minimal stub of ServerDataCache.onDidRefresh — only the surface we use.
function fakeCache(): {
	cache: Pick<ServerDataCache, 'onDidRefresh'>;
	fire: (payload: CacheRefreshPayload) => void;
} {
	const handlers: Array<(payload: CacheRefreshPayload) => void> = [];
	return {
		cache: {
			onDidRefresh: (handler) => {
				handlers.push(handler);
				return { dispose: () => { const i = handlers.indexOf(handler); if (i >= 0) { handlers.splice(i, 1); } } };
			},
		} as Pick<ServerDataCache, 'onDidRefresh'>,
		fire: (payload) => { for (const h of [...handlers]) { h(payload); } },
	};
}

function srv(name: string): ServerView {
	return { name, status: 'running', transport: 'http', restart_count: 0 };
}

function freshConfigPath(label = 'cfg'): string {
	const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'claude-cfg-sync-test-'));
	return path.join(dir, `.${label}.json`);
}

function cleanupConfigPath(p: string): void {
	try { fs.rmSync(path.dirname(p), { recursive: true, force: true }); } catch { /* best-effort */ }
}

function readJson(p: string): Record<string, unknown> {
	return JSON.parse(fs.readFileSync(p, 'utf8')) as Record<string, unknown>;
}

function makeOpts(overrides: Partial<{
	enabled: boolean;
	prefix: string;
	gatewayUrl: string;
	authValue: string | undefined;
	authThrows: boolean;
	aggregateEntryName: string;
}> = {}): { opts: ClaudeConfigSyncOptions; setAuth: (v: string | undefined) => void } {
	let auth = overrides.authValue;
	let throws = overrides.authThrows ?? false;
	const enabled = overrides.enabled ?? true;
	const prefix = overrides.prefix ?? 'mcp-gateway:';
	const gatewayUrl = overrides.gatewayUrl ?? 'http://localhost:8765';
	// Tests default to NO aggregate entry to keep counts predictable; opt-in
	// via overrides.aggregateEntryName for the dedicated aggregate test cases.
	const aggregateEntryName = overrides.aggregateEntryName ?? '';
	let configPath = '';
	const opts: ClaudeConfigSyncOptions = {
		enabled: () => enabled,
		namespacePrefix: () => prefix,
		configPath: () => configPath,
		gatewayUrl: () => gatewayUrl,
		authHeader: () => {
			if (throws) { throw new Error('auth missing'); }
			return auth;
		},
		aggregateEntryName: () => aggregateEntryName,
	};
	// Allow tests to set the path after constructing opts via closure.
	(opts as ClaudeConfigSyncOptions & { _setPath?: (p: string) => void })._setPath = (p: string) => { configPath = p; };
	return {
		opts,
		setAuth: (v) => { auth = v; throws = false; },
	};
}

function setOptsPath(opts: ClaudeConfigSyncOptions, p: string): void {
	const setter = (opts as ClaudeConfigSyncOptions & { _setPath?: (p: string) => void })._setPath;
	if (setter) { setter(p); }
}

describe('claude-config-sync', () => {

	describe('helpers', () => {
		it('pickPrefix selects only prefix-matching keys', () => {
			const got = pickPrefix({
				'foo': 1,
				'mcp-gateway:a': 2,
				'mcp-gateway:b': 3,
				'bar': 4,
			}, 'mcp-gateway:');
			assert.deepEqual(got, { 'mcp-gateway:a': 2, 'mcp-gateway:b': 3 });
		});

		it('deepEqualJson is order-insensitive on object keys', () => {
			assert.equal(deepEqualJson({ a: 1, b: 2 }, { b: 2, a: 1 }), true);
			assert.equal(deepEqualJson({ a: { x: 1, y: 2 } }, { a: { y: 2, x: 1 } }), true);
			assert.equal(deepEqualJson({ a: 1 }, { a: 2 }), false);
		});

		it('deepEqualJson handles arrays positionally', () => {
			assert.equal(deepEqualJson([1, 2, 3], [1, 2, 3]), true);
			assert.equal(deepEqualJson([1, 2, 3], [3, 2, 1]), false);
		});

		it('defaultClaudeJsonPath resolves under home', () => {
			const p = defaultClaudeJsonPath();
			assert.ok(p.startsWith(os.homedir()), `expected ${p} to start with ${os.homedir()}`);
			assert.ok(p.endsWith('.claude.json'));
		});
	});

	describe('reconcile — first refresh creates managed entries', () => {
		it('creates entries on a brand-new file (ENOENT path)', async () => {
			const cfg = freshConfigPath();
			const { opts } = makeOpts({ authValue: 'Bearer XYZ' });
			setOptsPath(opts, cfg);
			const { cache } = fakeCache();
			const sync = new ClaudeConfigSync(cache as ServerDataCache, opts);
			try {
				await sync.reconcile([srv('alpha'), srv('beta')]);
				const json = readJson(cfg);
				const servers = json.mcpServers as Record<string, ManagedHttpEntry>;
				assert.ok(servers['mcp-gateway:alpha']);
				assert.ok(servers['mcp-gateway:beta']);
				assert.equal(servers['mcp-gateway:alpha'].type, 'http');
				assert.equal(servers['mcp-gateway:alpha'].url, 'http://localhost:8765/mcp/alpha');
				assert.deepEqual(servers['mcp-gateway:alpha'].headers, { Authorization: 'Bearer XYZ' });
			} finally {
				sync.dispose();
				cleanupConfigPath(cfg);
			}
		});

		it('omits headers when auth provider throws', async () => {
			const cfg = freshConfigPath();
			const { opts } = makeOpts({ authThrows: true });
			setOptsPath(opts, cfg);
			const { cache } = fakeCache();
			const sync = new ClaudeConfigSync(cache as ServerDataCache, opts);
			try {
				await sync.reconcile([srv('alpha')]);
				const servers = readJson(cfg).mcpServers as Record<string, ManagedHttpEntry>;
				assert.equal(servers['mcp-gateway:alpha'].headers, undefined);
			} finally {
				sync.dispose();
				cleanupConfigPath(cfg);
			}
		});
	});

	describe('co-tenancy — preserves user entries', () => {
		it('keeps unrelated mcpServers keys + top-level fields untouched', async () => {
			const cfg = freshConfigPath();
			const initial = {
				mcpServers: {
					'pal': { type: 'stdio', command: 'pal-mcp', args: ['serve'] },
					'context7': { type: 'http', url: 'https://mcp.context7.com/mcp' },
				},
				somethingElse: 'preserve me',
				autoUpdates: true,
			};
			fs.writeFileSync(cfg, JSON.stringify(initial, null, 2) + '\n', { mode: 0o600 });

			const { opts } = makeOpts({ authValue: 'Bearer T' });
			setOptsPath(opts, cfg);
			const { cache } = fakeCache();
			const sync = new ClaudeConfigSync(cache as ServerDataCache, opts);
			try {
				await sync.reconcile([srv('alpha')]);
				const after = readJson(cfg);
				const servers = after.mcpServers as Record<string, unknown>;
				assert.deepEqual(servers['pal'], initial.mcpServers.pal);
				assert.deepEqual(servers['context7'], initial.mcpServers.context7);
				assert.ok((servers['mcp-gateway:alpha'] as { url: string }).url);
				assert.equal(after.somethingElse, 'preserve me');
				assert.equal(after.autoUpdates, true);
			} finally {
				sync.dispose();
				cleanupConfigPath(cfg);
			}
		});
	});

	describe('namespace round-trip — add/remove backends', () => {
		it('drops managed entries no longer in backend list (gateway-as-truth)', async () => {
			const cfg = freshConfigPath();
			const { opts } = makeOpts({ authValue: 'Bearer T' });
			setOptsPath(opts, cfg);
			const { cache } = fakeCache();
			const sync = new ClaudeConfigSync(cache as ServerDataCache, opts);
			try {
				await sync.reconcile([srv('alpha'), srv('beta'), srv('gamma')]);
				let servers = readJson(cfg).mcpServers as Record<string, unknown>;
				assert.equal(Object.keys(servers).filter((k) => k.startsWith('mcp-gateway:')).length, 3);

				await sync.reconcile([srv('alpha')]);
				servers = readJson(cfg).mcpServers as Record<string, unknown>;
				assert.ok(servers['mcp-gateway:alpha']);
				assert.ok(!('mcp-gateway:beta' in servers));
				assert.ok(!('mcp-gateway:gamma' in servers));

				await sync.reconcile([]);
				servers = readJson(cfg).mcpServers as Record<string, unknown>;
				assert.equal(Object.keys(servers).filter((k) => k.startsWith('mcp-gateway:')).length, 0);
			} finally {
				sync.dispose();
				cleanupConfigPath(cfg);
			}
		});

		it('cleanup() removes all managed entries but preserves foreign keys', async () => {
			const cfg = freshConfigPath();
			fs.writeFileSync(cfg, JSON.stringify({
				mcpServers: { 'pal': { type: 'stdio' } },
			}, null, 2) + '\n');
			const { opts } = makeOpts({ authValue: 'Bearer T' });
			setOptsPath(opts, cfg);
			const { cache } = fakeCache();
			const sync = new ClaudeConfigSync(cache as ServerDataCache, opts);
			try {
				await sync.reconcile([srv('alpha'), srv('beta')]);
				await sync.cleanup();
				const after = readJson(cfg);
				const servers = after.mcpServers as Record<string, unknown>;
				assert.deepEqual(servers, { 'pal': { type: 'stdio' } });
			} finally {
				sync.dispose();
				cleanupConfigPath(cfg);
			}
		});
	});

	describe('diff-only writes', () => {
		it('does not modify file when desired managed-set unchanged', async () => {
			const cfg = freshConfigPath();
			const { opts } = makeOpts({ authValue: 'Bearer T' });
			setOptsPath(opts, cfg);
			const { cache } = fakeCache();
			const sync = new ClaudeConfigSync(cache as ServerDataCache, opts);
			try {
				await sync.reconcile([srv('alpha')]);
				const mtime1 = fs.statSync(cfg).mtimeMs;
				// Sleep a tick so mtime resolution catches a real write.
				await new Promise((r) => setTimeout(r, 10));
				await sync.reconcile([srv('alpha')]);
				const mtime2 = fs.statSync(cfg).mtimeMs;
				assert.equal(mtime1, mtime2, 'second reconcile must not touch file when set unchanged');
			} finally {
				sync.dispose();
				cleanupConfigPath(cfg);
			}
		});

		it('writes when token rotates (Authorization header changes)', async () => {
			const cfg = freshConfigPath();
			const { opts, setAuth } = makeOpts({ authValue: 'Bearer OLD' });
			setOptsPath(opts, cfg);
			const { cache } = fakeCache();
			const sync = new ClaudeConfigSync(cache as ServerDataCache, opts);
			try {
				await sync.reconcile([srv('alpha')]);
				let entry = (readJson(cfg).mcpServers as Record<string, ManagedHttpEntry>)['mcp-gateway:alpha'];
				assert.deepEqual(entry.headers, { Authorization: 'Bearer OLD' });

				setAuth('Bearer NEW');
				await sync.reconcile([srv('alpha')]);
				entry = (readJson(cfg).mcpServers as Record<string, ManagedHttpEntry>)['mcp-gateway:alpha'];
				assert.deepEqual(entry.headers, { Authorization: 'Bearer NEW' });
			} finally {
				sync.dispose();
				cleanupConfigPath(cfg);
			}
		});
	});

	describe('JSON parse retry', () => {
		it('recovers from a transient mid-write truncation', async () => {
			const cfg = freshConfigPath();
			fs.writeFileSync(cfg, JSON.stringify({ mcpServers: { user: { type: 'stdio' } } }, null, 2));
			// Simulate a writer that is currently mid-flight: leave the file
			// truncated for one read cycle, then restore valid JSON before
			// the parse-retry budget exhausts.
			const valid = fs.readFileSync(cfg, 'utf8');
			fs.writeFileSync(cfg, valid.slice(0, valid.length / 2));
			setTimeout(() => { fs.writeFileSync(cfg, valid); }, 75);

			const { opts } = makeOpts({ authValue: 'Bearer T' });
			setOptsPath(opts, cfg);
			const { cache } = fakeCache();
			const sync = new ClaudeConfigSync(cache as ServerDataCache, opts);
			try {
				await sync.reconcile([srv('alpha')]);
				const servers = readJson(cfg).mcpServers as Record<string, unknown>;
				// User entry survived, managed entry added.
				assert.deepEqual(servers['user'], { type: 'stdio' });
				assert.ok(servers['mcp-gateway:alpha']);
			} finally {
				sync.dispose();
				cleanupConfigPath(cfg);
			}
		});
	});

	describe('kill-switch — enabled=false', () => {
		it('skips reconcile when opts.enabled() returns false', async () => {
			const cfg = freshConfigPath();
			const initial = { mcpServers: { user: { type: 'stdio' } } };
			fs.writeFileSync(cfg, JSON.stringify(initial, null, 2) + '\n');

			let enabled = false;
			const opts: ClaudeConfigSyncOptions = {
				enabled: () => enabled,
				namespacePrefix: () => 'mcp-gateway:',
				configPath: () => cfg,
				gatewayUrl: () => 'http://localhost:8765',
				authHeader: () => 'Bearer T',
			};

			const { cache, fire } = fakeCache();
			const sync = new ClaudeConfigSync(cache as ServerDataCache, opts);
			try {
				fire({ servers: [srv('alpha')], lastRefreshFailed: false, gatewayHealth: null });
				// Wait one tick for the async handler.
				await new Promise((r) => setImmediate(r));
				let servers = readJson(cfg).mcpServers as Record<string, unknown>;
				assert.ok(!('mcp-gateway:alpha' in servers), 'must not write while disabled');

				enabled = true;
				fire({ servers: [srv('alpha')], lastRefreshFailed: false, gatewayHealth: null });
				await new Promise((r) => setTimeout(r, 30));
				servers = readJson(cfg).mcpServers as Record<string, unknown>;
				assert.ok(servers['mcp-gateway:alpha'], 'must write once enabled flips on');
			} finally {
				sync.dispose();
				cleanupConfigPath(cfg);
			}
		});

		it('skips reconcile when payload.lastRefreshFailed is true', async () => {
			const cfg = freshConfigPath();
			const initial = { mcpServers: { 'mcp-gateway:alpha': { type: 'http', url: 'old' } } };
			fs.writeFileSync(cfg, JSON.stringify(initial, null, 2) + '\n');

			const { opts } = makeOpts({ authValue: 'Bearer T' });
			setOptsPath(opts, cfg);
			const { cache, fire } = fakeCache();
			const sync = new ClaudeConfigSync(cache as ServerDataCache, opts);
			try {
				fire({ servers: [], lastRefreshFailed: true, gatewayHealth: null });
				await new Promise((r) => setImmediate(r));
				const servers = readJson(cfg).mcpServers as Record<string, unknown>;
				// Last-good preserved when daemon momentarily drops.
				assert.ok(servers['mcp-gateway:alpha']);
			} finally {
				sync.dispose();
				cleanupConfigPath(cfg);
			}
		});
	});

	describe('preview() — non-mutating dry-run', () => {
		it('returns added/removed/updated/unchanged without touching the file', async () => {
			const cfg = freshConfigPath();
			const initial = {
				mcpServers: {
					'pal': { type: 'stdio' },
					'mcp-gateway:alpha': { type: 'http', url: 'http://localhost:8765/mcp/alpha' },
					'mcp-gateway:legacy': { type: 'http', url: 'http://localhost:8765/mcp/legacy' },
				},
			};
			fs.writeFileSync(cfg, JSON.stringify(initial, null, 2) + '\n');
			const mtimeBefore = fs.statSync(cfg).mtimeMs;
			await new Promise((r) => setTimeout(r, 10));

			const { opts } = makeOpts({ authValue: undefined });  // no headers — alpha url-only
			setOptsPath(opts, cfg);
			const { cache } = fakeCache();
			const sync = new ClaudeConfigSync(cache as ServerDataCache, opts);
			try {
				const diff = await sync.preview([srv('alpha'), srv('beta')]);
				assert.deepEqual(diff.added, ['mcp-gateway:beta']);
				assert.deepEqual(diff.removed, ['mcp-gateway:legacy']);
				// alpha existing has no headers; desired has no headers either → unchanged.
				assert.deepEqual(diff.unchanged, ['mcp-gateway:alpha']);
				assert.deepEqual(diff.updated, []);

				const mtimeAfter = fs.statSync(cfg).mtimeMs;
				assert.equal(mtimeBefore, mtimeAfter, 'preview() must not modify file');
			} finally {
				sync.dispose();
				cleanupConfigPath(cfg);
			}
		});
	});

	describe('concurrent-writer simulation (PLAN T11.5 inv-4A)', () => {
		// Real cross-process FS contention. Spawns a child Node process that
		// mutates the same file while the extension reconciles. CAS retry
		// must ensure user entries are never lost — the load-bearing
		// invariant for the architectural pivot.
		//
		// PAL gpt-5.1-codex/high HIGH finding fix: previous version used
		// spawnSync which blocks the event loop, serializing the writers
		// instead of overlapping them. Now uses async spawn + a busy-loop
		// in the child to maximize the contention window. Asserts
		// getCasRetryCount() > 0 across rounds, formally proving the CAS
		// retry path was exercised (not just final-state correctness).
		it('preserves user entries across N rounds with parallel child mutator', async function () {
			this.timeout(60000);
			const cfg = freshConfigPath('concurrent');
			fs.writeFileSync(cfg, JSON.stringify({ mcpServers: {} }, null, 2) + '\n');

			const helperPath = path.join(path.dirname(cfg), 'mutator.js');
			// Tight loop: each iteration is a complete read-modify-rename
			// cycle. Inserting a small setTimeout yields the libuv pool to
			// the parent so writes overlap rather than batching.
			fs.writeFileSync(helperPath, `
				const fs = require('fs');
				const cfg = ${JSON.stringify(cfg)};
				const round = process.argv[2];
				const ITER = 6;
				(async () => {
					for (let i = 0; i < ITER; i++) {
						let succeeded = false;
						for (let attempt = 0; attempt < 20 && !succeeded; attempt++) {
							try {
								const raw = fs.readFileSync(cfg, 'utf8');
								const obj = JSON.parse(raw);
								obj.mcpServers = obj.mcpServers || {};
								obj.mcpServers['user-' + round + '-' + i] = { type: 'stdio', command: 'echo', args: [String(i)] };
								const tmp = cfg + '.child.tmp.' + process.pid + '.' + Date.now();
								fs.writeFileSync(tmp, JSON.stringify(obj, null, 2) + '\\n');
								fs.renameSync(tmp, cfg);
								succeeded = true;
							} catch (e) {
								// transient JSON parse mid-write or rename EBUSY — retry
								await new Promise(r => setTimeout(r, 5 + Math.random() * 10));
							}
						}
						await new Promise(r => setTimeout(r, 1 + Math.random() * 4));
					}
				})();
			`);

			_resetCasRetryCount();
			const { opts } = makeOpts({ authValue: 'Bearer T' });
			setOptsPath(opts, cfg);
			const { cache } = fakeCache();
			const sync = new ClaudeConfigSync(cache as ServerDataCache, opts);
			const ROUNDS = 4;
			const ITER_PER_ROUND = 6;

			try {
				for (let r = 0; r < ROUNDS; r++) {
					const child = spawn(
						process.execPath,
						[helperPath, String(r)],
						{ stdio: 'ignore' },
					);
					const childExited = new Promise<void>((resolve) => {
						child.on('exit', () => resolve());
					});

					const desired: ServerView[] = [srv('alpha'), srv(`round-${r}`)];
					// Hammer the file from our side while the child does its
					// 6 iterations. Each parent reconcile is independent.
					const reconcilePromises: Promise<void>[] = [];
					for (let p = 0; p < 4; p++) {
						reconcilePromises.push(sync.reconcile(desired));
						await new Promise((r) => setTimeout(r, 8));
					}
					await Promise.all(reconcilePromises);
					await childExited;

					// Final reconcile — picks up whatever child wrote last.
					await sync.reconcile(desired);
				}

				const finalServers = readJson(cfg).mcpServers as Record<string, unknown>;
				const userKeys = Object.keys(finalServers).filter((k) => k.startsWith('user-'));
				const managedKeys = Object.keys(finalServers).filter((k) => k.startsWith('mcp-gateway:'));

				assert.ok(finalServers['mcp-gateway:alpha'], 'final alpha entry missing');
				assert.ok(
					finalServers[`mcp-gateway:round-${ROUNDS - 1}`],
					'final round entry missing',
				);

				// CAS guarantee: every child write must survive — none silently lost
				// to extension's overwrite. Each round writes ITER_PER_ROUND entries
				// (some may collide with extension reconciles; CAS retry rebuilds).
				assert.equal(
					userKeys.length,
					ROUNDS * ITER_PER_ROUND,
					`expected ${ROUNDS * ITER_PER_ROUND} user-* keys after ${ROUNDS} rounds, ` +
						`got ${userKeys.length}: ${userKeys.sort().join(',')}`,
				);

				assert.equal(
					managedKeys.length,
					2,
					`expected exactly 2 managed keys (alpha + final round), got ${managedKeys.length}`,
				);

				// Formally assert the CAS retry path was exercised at least once
				// across all rounds — proves the test would catch a CAS regression
				// (e.g., a future refactor that removes the pre-rename hash check).
				const casRetries = getCasRetryCount();
				assert.ok(
					casRetries > 0,
					`expected at least 1 CAS retry across ${ROUNDS} rounds with cross-process contention, got ${casRetries}`,
				);
			} finally {
				sync.dispose();
				cleanupConfigPath(cfg);
			}
		});
	});

	describe('aggregate-gateway entry', () => {
		it('writes aggregate entry pointing at /mcp (no backend suffix)', async () => {
			const cfg = freshConfigPath();
			const { opts } = makeOpts({
				authValue: 'Bearer T',
				aggregateEntryName: 'mcp-gateway',
			});
			setOptsPath(opts, cfg);
			const { cache } = fakeCache();
			const sync = new ClaudeConfigSync(cache as ServerDataCache, opts);
			try {
				await sync.reconcile([srv('alpha')]);
				const servers = readJson(cfg).mcpServers as Record<string, ManagedHttpEntry>;
				assert.equal(servers['mcp-gateway'].url, 'http://localhost:8765/mcp');
				assert.deepEqual(servers['mcp-gateway'].headers, { Authorization: 'Bearer T' });
				assert.equal(servers['mcp-gateway:alpha'].url, 'http://localhost:8765/mcp/alpha');
			} finally {
				sync.dispose();
				cleanupConfigPath(cfg);
			}
		});

		it('cleanup() removes the aggregate entry too (not just prefixed)', async () => {
			const cfg = freshConfigPath();
			fs.writeFileSync(cfg, JSON.stringify({
				mcpServers: { 'pal': { type: 'stdio' } },
			}, null, 2) + '\n');
			const { opts } = makeOpts({
				authValue: 'Bearer T',
				aggregateEntryName: 'mcp-gateway',
			});
			setOptsPath(opts, cfg);
			const { cache } = fakeCache();
			const sync = new ClaudeConfigSync(cache as ServerDataCache, opts);
			try {
				await sync.reconcile([srv('alpha')]);
				let servers = readJson(cfg).mcpServers as Record<string, unknown>;
				assert.ok(servers['mcp-gateway']);
				assert.ok(servers['mcp-gateway:alpha']);

				await sync.cleanup();
				servers = readJson(cfg).mcpServers as Record<string, unknown>;
				assert.equal(Object.keys(servers).length, 1, 'only foreign user entry should remain');
				assert.deepEqual(servers, { 'pal': { type: 'stdio' } });
			} finally {
				sync.dispose();
				cleanupConfigPath(cfg);
			}
		});

		it('aggregateEntryName="" disables aggregate entry (per-backend only)', async () => {
			const cfg = freshConfigPath();
			const { opts } = makeOpts({
				authValue: 'Bearer T',
				aggregateEntryName: '',
			});
			setOptsPath(opts, cfg);
			const { cache } = fakeCache();
			const sync = new ClaudeConfigSync(cache as ServerDataCache, opts);
			try {
				await sync.reconcile([srv('alpha')]);
				const servers = readJson(cfg).mcpServers as Record<string, unknown>;
				assert.ok(!('mcp-gateway' in servers));
				assert.ok(servers['mcp-gateway:alpha']);
			} finally {
				sync.dispose();
				cleanupConfigPath(cfg);
			}
		});
	});

	describe('atomic write hygiene', () => {
		it('does not leave .tmp.* files behind on success', async () => {
			const cfg = freshConfigPath();
			const { opts } = makeOpts({ authValue: 'Bearer T' });
			setOptsPath(opts, cfg);
			const { cache } = fakeCache();
			const sync = new ClaudeConfigSync(cache as ServerDataCache, opts);
			try {
				await sync.reconcile([srv('alpha')]);
				const dir = path.dirname(cfg);
				const stragglers = fs.readdirSync(dir).filter((f) => f.includes('.tmp.'));
				assert.deepEqual(stragglers, [], `unexpected tmp files: ${stragglers.join(',')}`);
			} finally {
				sync.dispose();
				cleanupConfigPath(cfg);
			}
		});
	});
});
