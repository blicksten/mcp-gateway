import './mock-vscode';
import { MockSecretStorage, MockMemento } from './mock-vscode';
import { CredentialStore } from '../credential-store';
import { strict as assert } from 'node:assert';

function makeContext(): { secrets: MockSecretStorage; globalState: MockMemento } {
	return {
		secrets: new MockSecretStorage(),
		globalState: new MockMemento(),
	};
}

describe('CredentialStore', () => {
	let ctx: ReturnType<typeof makeContext>;
	let store: CredentialStore;

	beforeEach(() => {
		ctx = makeContext();
		// CredentialStore expects vscode.ExtensionContext — cast the mock.
		store = new CredentialStore(ctx as never);
	});

	describe('storeEnvVar / getEnvVar', () => {
		it('stores and retrieves an env var', async () => {
			await store.storeEnvVar('server1', 'API_KEY', 'secret123');
			const val = await store.getEnvVar('server1', 'API_KEY');
			assert.equal(val, 'secret123');
		});

		it('returns undefined for missing key', async () => {
			const val = await store.getEnvVar('server1', 'MISSING');
			assert.equal(val, undefined);
		});
	});

	describe('storeHeader / getHeader', () => {
		it('stores and retrieves a header', async () => {
			await store.storeHeader('server1', 'Authorization', 'Bearer token');
			const val = await store.getHeader('server1', 'Authorization');
			assert.equal(val, 'Bearer token');
		});
	});

	describe('getServerCredentials', () => {
		it('returns all credentials for a server', async () => {
			await store.storeEnvVar('s1', 'KEY1', 'val1');
			await store.storeEnvVar('s1', 'KEY2', 'val2');
			await store.storeHeader('s1', 'Auth', 'Bearer x');

			const creds = await store.getServerCredentials('s1');
			assert.deepEqual(creds.env, { KEY1: 'val1', KEY2: 'val2' });
			assert.deepEqual(creds.headers, { Auth: 'Bearer x' });
		});

		it('returns empty for unknown server', async () => {
			const creds = await store.getServerCredentials('unknown');
			assert.deepEqual(creds, { env: {}, headers: {} });
		});
	});

	describe('deleteServerCredentials', () => {
		it('removes all secrets and index entry', async () => {
			await store.storeEnvVar('s1', 'KEY', 'val');
			await store.storeHeader('s1', 'Auth', 'tok');

			await store.deleteServerCredentials('s1');

			assert.equal(await store.getEnvVar('s1', 'KEY'), undefined);
			assert.equal(await store.getHeader('s1', 'Auth'), undefined);
			assert.deepEqual(store.listServers(), []);
		});

		it('is safe to call on nonexistent server', async () => {
			await store.deleteServerCredentials('nope'); // should not throw
		});
	});

	describe('listServers', () => {
		it('returns servers with stored credentials', async () => {
			await store.storeEnvVar('s1', 'K', 'v');
			await store.storeEnvVar('s2', 'K', 'v');
			const servers = store.listServers();
			assert.deepEqual(servers.sort(), ['s1', 's2']);
		});
	});

	describe('reconcile', () => {
		it('prunes stale index entries', async () => {
			await store.storeEnvVar('s1', 'KEY', 'val');
			// Manually delete the secret but leave the index intact.
			await ctx.secrets.delete('mcpGateway/s1/env/KEY');

			await store.reconcile();

			// Server should be removed from index (no remaining secrets).
			assert.deepEqual(store.listServers(), []);
		});

		it('keeps valid entries', async () => {
			await store.storeEnvVar('s1', 'KEY', 'val');
			await store.reconcile();
			assert.deepEqual(store.listServers(), ['s1']);
		});

		// Phase 10 — B-NEW-24 chainIndexMutation race regression
		it('serializes concurrent reconcile + storeEnvVar through the index chain (no lost updates)', async () => {
			// Seed: server 'A' with one secret. Reconcile() will iterate it
			// and call secrets.get; while it's awaiting the get, we slip a
			// concurrent storeEnvVar('B', ...) onto the chain. Pre-Phase-10
			// behaviour: both read the index at the same revision, then both
			// writeBack — last writer wins, the other entry is lost.
			// Post-Phase-10: chainIndexMutation serializes the two segments
			// so storeEnvVar's _addToIndex sees A's reconciled index, then
			// B is appended cleanly. Both servers must be present at end.
			await store.storeEnvVar('A', 'KEY_A', 'val_a');

			// Slow secrets.get so reconcile is mid-loop while storeEnvVar fires.
			const realGet = ctx.secrets.get.bind(ctx.secrets);
			let getCalls = 0;
			(ctx.secrets as any).get = async (key: string) => {
				getCalls++;
				// First reconcile-driven get: pause so the concurrent
				// storeEnvVar can attempt to enqueue an index mutation.
				if (getCalls === 1) {
					await new Promise((r) => setTimeout(r, 30));
				}
				return realGet(key);
			};

			try {
				const reconcileP = store.reconcile();
				// Yield so reconcile gets onto the chain first, then start storeEnvVar.
				await new Promise((r) => setImmediate(r));
				const storeP = store.storeEnvVar('B', 'KEY_B', 'val_b');
				await Promise.all([reconcileP, storeP]);
			} finally {
				(ctx.secrets as any).get = realGet;
			}

			const servers = store.listServers().sort();
			assert.deepEqual(servers, ['A', 'B'],
				'both A and B must survive — chain must serialize reconcile + storeEnvVar (B-NEW-24)');
			// Verify both secrets are still resolvable (index entries match real secrets).
			assert.equal(await store.getEnvVar('A', 'KEY_A'), 'val_a');
			assert.equal(await store.getEnvVar('B', 'KEY_B'), 'val_b');
		});
	});

	describe('_validateServerName', () => {
		it('rejects path-traversal names', async () => {
			await assert.rejects(
				() => store.storeEnvVar('../foo', 'K', 'v'),
				/Invalid server name/,
			);
		});

		it('rejects slash in name', async () => {
			await assert.rejects(
				() => store.storeEnvVar('a/b', 'K', 'v'),
				/Invalid server name/,
			);
		});

		it('rejects empty name', async () => {
			await assert.rejects(
				() => store.storeEnvVar('', 'K', 'v'),
				/Invalid server name/,
			);
		});

		it('rejects names longer than 64 chars', async () => {
			const longName = 'a'.repeat(65);
			await assert.rejects(
				() => store.storeEnvVar(longName, 'K', 'v'),
				/Invalid server name/,
			);
		});

		it('accepts valid 64-char name', async () => {
			const name = 'a'.repeat(64);
			await store.storeEnvVar(name, 'K', 'v');
			const val = await store.getEnvVar(name, 'K');
			assert.equal(val, 'v');
		});
	});

	describe('_validateKey', () => {
		it('rejects empty env key', async () => {
			await assert.rejects(
				() => store.storeEnvVar('s1', '', 'v'),
				/must not be empty/,
			);
		});

		it('rejects env key with slash', async () => {
			await assert.rejects(
				() => store.storeEnvVar('s1', 'a/b', 'v'),
				/Invalid env key/,
			);
		});

		it('rejects header key with control chars', async () => {
			await assert.rejects(
				() => store.storeHeader('s1', 'Bad\x00Key', 'v'),
				/Invalid header key/,
			);
		});
	});

	describe('index _version preservation', () => {
		it('preserves _version across operations', async () => {
			await store.storeEnvVar('s1', 'K', 'v');
			await store.storeEnvVar('s2', 'K', 'v');
			await store.deleteServerCredentials('s1');

			const raw = ctx.globalState.get<{ _version: number }>('mcpGateway.credentialIndex');
			assert.equal(raw?._version, 1);
		});
	});

	// Test 15 — T2.7: renameServerCredentials migrates env+header; index-first ordering
	describe('renameServerCredentials', () => {
		it('Test 15: migrates env+header secrets and updates index; index-first ordering verified', async () => {
			// Pre-populate with two env keys and one header key.
			await store.storeEnvVar('ctx7', 'K1', 'v1');
			await store.storeEnvVar('ctx7', 'K2', 'v2');
			await store.storeHeader('ctx7', 'Auth', 'Bearer tok');

			// Track call order to verify STEP 1 (index) precedes STEP 2 (secrets.store).
			const callOrder: string[] = [];

			// Intercept _setIndex via globalState.update to record index writes.
			const realUpdate = ctx.globalState.update.bind(ctx.globalState);
			(ctx.globalState as any).update = async (key: string, value: unknown) => {
				callOrder.push(`index:${key}`);
				return realUpdate(key, value);
			};

			// Intercept secrets.store to record secret writes.
			const realStore = ctx.secrets.store.bind(ctx.secrets);
			(ctx.secrets as any).store = async (key: string, value: string) => {
				callOrder.push(`secret:${key}`);
				return realStore(key, value);
			};

			await store.renameServerCredentials('ctx7', 'ctx8');

			// Restore interceptors.
			(ctx.globalState as any).update = realUpdate;
			(ctx.secrets as any).store = realStore;

			// Verify STEP 1: first call-order entry is an index write (before any secret store).
			const firstEntry = callOrder[0];
			assert.ok(
				firstEntry.startsWith('index:'),
				`Index must be written BEFORE first secrets.store call. First call was: "${firstEntry}"`,
			);

			// Verify all secrets moved from old to new key.
			assert.equal(await store.getEnvVar('ctx8', 'K1'), 'v1');
			assert.equal(await store.getEnvVar('ctx8', 'K2'), 'v2');
			assert.equal(await store.getHeader('ctx8', 'Auth'), 'Bearer tok');

			// Verify old keys deleted.
			assert.equal(await store.getEnvVar('ctx7', 'K1'), undefined);
			assert.equal(await store.getEnvVar('ctx7', 'K2'), undefined);
			assert.equal(await store.getHeader('ctx7', 'Auth'), undefined);

			// Verify index updated correctly.
			const servers = store.listServers();
			assert.ok(servers.includes('ctx8'), 'ctx8 must be in index');
			assert.ok(!servers.includes('ctx7'), 'ctx7 must be removed from index');

			// Verify secret storage keys directly.
			const keys = ctx.secrets.keys();
			assert.ok(keys.includes('mcpGateway/ctx8/env/K1'));
			assert.ok(keys.includes('mcpGateway/ctx8/env/K2'));
			assert.ok(keys.includes('mcpGateway/ctx8/header/Auth'));
			assert.ok(!keys.includes('mcpGateway/ctx7/env/K1'));
			assert.ok(!keys.includes('mcpGateway/ctx7/env/K2'));
			assert.ok(!keys.includes('mcpGateway/ctx7/header/Auth'));
		});

		// Test 16 — T2.8: renameServerCredentials handles missing entry
		it('Test 16: handles missing entry — early return, no error, no secret operations', async () => {
			const keysBefore = ctx.secrets.keys();

			// rename a server not in index → should be a no-op
			await store.renameServerCredentials('nonexistent', 'ctx8');

			const keysAfter = ctx.secrets.keys();
			assert.deepEqual(keysBefore, keysAfter, 'No secret operations must occur for unknown server');
			assert.deepEqual(store.listServers(), [], 'Index must remain empty');
		});

		// T2.8 edge case: same name → no-op
		it('same-name rename is a no-op', async () => {
			await store.storeEnvVar('ctx7', 'K1', 'v1');
			const keysBefore = ctx.secrets.keys().sort();

			await store.renameServerCredentials('ctx7', 'ctx7');

			const keysAfter = ctx.secrets.keys().sort();
			assert.deepEqual(keysBefore, keysAfter, 'Same-name rename must not mutate secrets');
			assert.ok(store.listServers().includes('ctx7'), 'ctx7 must still be in index');
		});

		// Test 16b — T2.5: crash mid-rename → reconcile recoverable
		it('Test 16b: crash mid-rename (failAfterNStores(1)) leaves index with newName; reconcile leaves consistent state', async () => {
			// Pre-populate with two env keys.
			await store.storeEnvVar('ctx7', 'K1', 'v1');
			await store.storeEnvVar('ctx7', 'K2', 'v2');

			// Arm fail-after-1-stores: first secrets.store(newName/K1) succeeds,
			// second secrets.store(newName/K2) throws.
			ctx.secrets.failAfterNStores(1, new Error('SecretStorage unavailable'));

			// rename should throw (propagated from _chainIndexMutation callback).
			await assert.rejects(
				() => store.renameServerCredentials('ctx7', 'ctx8'),
				/SecretStorage unavailable/,
			);

			// STEP 1 must have committed before the crash: index has ctx8.
			const servers = store.listServers();
			assert.ok(servers.includes('ctx8'),
				'index must contain ctx8 (STEP 1 committed before crash)');

			// Partial migration: first key copied, second not.
			assert.equal(await store.getEnvVar('ctx8', 'K1'), 'v1',
				'first migrated secret must be present');

			// Index is consistent (no double-entry for K2 under ctx8 that does not exist in secrets).
			// Now call reconcile() — it must NOT throw and must leave a consistent state.
			// ctx8 has K1 (secret present) and K2 (secret absent → pruned).
			// ctx7 still has its old secrets (Step 3 not reached).
			await store.reconcile();

			// After reconcile: ctx8 index entry reflects only keys whose secrets exist.
			const creds = store.listServerCredentials('ctx8');
			assert.ok(!creds.env.includes('K2'),
				'K2 must be pruned from ctx8 index by reconcile (secret missing)');
			// No double-entry: ctx7 and ctx8 do not both claim the same key shape.
			const allServers = store.listServers();
			// Both ctx7 and ctx8 may exist — that is acceptable; the invariant is
			// that ctx8 index entries are consistent (only keys with present secrets).
			for (const name of allServers) {
				const c = store.listServerCredentials(name);
				// All listed keys must have a corresponding secret.
				for (const key of c.env) {
					assert.notEqual(await store.getEnvVar(name, key), undefined,
						`index entry ${name}/env/${key} must have a corresponding secret after reconcile`);
				}
				for (const key of c.headers) {
					assert.notEqual(await store.getHeader(name, key), undefined,
						`index entry ${name}/header/${key} must have a corresponding secret after reconcile`);
				}
			}
		});

		// Test 17 — T2.4: renameServerCredentials race + stranded-index-detection
		it('Test 17: race — post-rename storeEnvVar resurrects old index entry; reconcile does NOT prune it', async () => {
			// Pre-populate index with ctx7 having two env keys.
			await store.storeEnvVar('ctx7', 'K1', 'v1');
			await store.storeEnvVar('ctx7', 'K2', 'v2');

			// STEP A: rename ctx7 → ctx8 (completes fully first).
			await store.renameServerCredentials('ctx7', 'ctx8');

			// Verify rename completed.
			assert.ok(!store.listServers().includes('ctx7'), 'ctx7 must be gone after rename');
			assert.ok(store.listServers().includes('ctx8'), 'ctx8 must be present after rename');

			// STEP B: storeEnvVar('ctx7', 'K3', 'v3') runs AFTER rename completes.
			// Per credential-store.ts:232-234, _addToIndex creates a new ctx7 entry.
			await store.storeEnvVar('ctx7', 'K3', 'v3');

			// Assert final state: index has both ctx8 (migrated) and ctx7 (resurrected).
			const finalServers = store.listServers();
			assert.ok(finalServers.includes('ctx8'), 'ctx8 must be in index');
			assert.ok(finalServers.includes('ctx7'), 'ctx7 must be resurrected in index by post-rename storeEnvVar');

			// ctx8 has the migrated keys.
			assert.deepEqual(store.listServerCredentials('ctx8').env.sort(), ['K1', 'K2']);
			// ctx7 has only the new key.
			assert.deepEqual(store.listServerCredentials('ctx7').env, ['K3']);

			// Verify secrets exist at the right keys.
			assert.equal(await store.getEnvVar('ctx8', 'K1'), 'v1');
			assert.equal(await store.getEnvVar('ctx8', 'K2'), 'v2');
			assert.equal(await store.getEnvVar('ctx7', 'K3'), 'v3');

			// STEP C: call reconcile() and assert ctx7 is NOT pruned.
			// Secret K3 is present under ctx7, so reconcile cannot identify ctx7 as orphaned.
			await store.reconcile();

			assert.ok(store.listServers().includes('ctx7'),
				'ctx7 must NOT be pruned by reconcile — secret K3 is still present under ctx7 ' +
				'(stranded index entry persists; manual cleanup via future auditOrphanSecrets is the documented mitigation)');
			assert.equal(await store.getEnvVar('ctx7', 'K3'), 'v3',
				'K3 secret must still be accessible under ctx7 after reconcile');
		});
	});

	// Test 18 — T2.9: listServerCredentials
	describe('listServerCredentials', () => {
		it('Test 18: returns env+header arrays for known server and empty arrays for unknown', async () => {
			await store.storeEnvVar('ctx7', 'API_KEY', 'secret');
			await store.storeEnvVar('ctx7', 'TOKEN', 'tok');
			await store.storeHeader('ctx7', 'Authorization', 'Bearer x');

			const creds = store.listServerCredentials('ctx7');
			assert.deepEqual(creds.env.sort(), ['API_KEY', 'TOKEN']);
			assert.deepEqual(creds.headers, ['Authorization']);

			// Verify shallow copy: mutating the returned array does not affect the index.
			creds.env.push('EXTRA');
			const creds2 = store.listServerCredentials('ctx7');
			assert.deepEqual(creds2.env.sort(), ['API_KEY', 'TOKEN'],
				'mutating returned env array must not affect stored index');

			// Unknown server returns empty arrays.
			const unknown = store.listServerCredentials('nonexistent');
			assert.deepEqual(unknown, { env: [], headers: [] });
		});

		it('throws on invalid server name', () => {
			assert.throws(
				() => store.listServerCredentials('../evil'),
				/Invalid server name/,
			);
		});
	});
});
