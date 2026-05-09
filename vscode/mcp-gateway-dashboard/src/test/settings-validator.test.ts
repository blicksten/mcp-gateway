import { strict as assert } from 'node:assert';
import { describe, it } from 'mocha';
import {
	debounce,
	LRUCacheWithTTL,
	memoize,
	validatePath,
	makeProbeCache,
	type ProbeResult,
} from '../settings-validator';

function sleep(ms: number): Promise<void> {
	return new Promise((r) => setTimeout(r, ms));
}

describe('settings-validator — debounce', () => {
	it('fires once after delay with the latest input', async () => {
		const calls: string[] = [];
		const fn = async (s: string) => { calls.push(s); return s.toUpperCase(); };
		const d = debounce(fn, 30);
		const p1 = d('a');
		const p2 = d('b');
		const p3 = d('c');
		const r1 = await p1;
		const r2 = await p2;
		const r3 = await p3;
		assert.deepStrictEqual(calls, ['c']);
		assert.strictEqual(r1, 'C');
		assert.strictEqual(r2, 'C');
		assert.strictEqual(r3, 'C');
	});

	it('cancel() prevents pending fire and clears state', async () => {
		const calls: string[] = [];
		const fn = async (s: string) => { calls.push(s); return s; };
		const d = debounce(fn, 30);
		const p = d('x');
		d.cancel();
		// give the timer time to NOT fire
		await sleep(50);
		assert.deepStrictEqual(calls, []);
		// the pending promise hangs until next call — fire one and assert it lands
		const p2 = d('y');
		await p2;
		assert.deepStrictEqual(calls, ['y']);
		// p never resolves after cancel; we don't await it. (Production callers
		// that cancel must not retain stale promises.)
		void p;
	});

	it('a fresh call after the previous fires completes a new debounce window', async () => {
		const calls: string[] = [];
		const fn = async (s: string) => { calls.push(s); return s; };
		const d = debounce(fn, 30);
		await d('a');
		assert.deepStrictEqual(calls, ['a']);
		await d('b');
		assert.deepStrictEqual(calls, ['a', 'b']);
	});
});

describe('settings-validator — LRUCacheWithTTL', () => {
	it('returns undefined for missing key', () => {
		const cache = new LRUCacheWithTTL<string, number>(2, 1000);
		assert.strictEqual(cache.get('missing'), undefined);
	});

	it('returns set value within TTL', () => {
		const cache = new LRUCacheWithTTL<string, number>(2, 1000);
		cache.set('a', 42);
		assert.strictEqual(cache.get('a'), 42);
	});

	it('expires entry after TTL', () => {
		let now = 0;
		const cache = new LRUCacheWithTTL<string, number>(2, 100, () => now);
		cache.set('a', 1);
		now = 50;
		assert.strictEqual(cache.get('a'), 1);
		now = 100;
		assert.strictEqual(cache.get('a'), undefined);
	});

	it('evicts oldest when size exceeds maxEntries', () => {
		const cache = new LRUCacheWithTTL<string, number>(2, 1000);
		cache.set('a', 1);
		cache.set('b', 2);
		cache.set('c', 3); // evicts 'a'
		assert.strictEqual(cache.get('a'), undefined);
		assert.strictEqual(cache.get('b'), 2);
		assert.strictEqual(cache.get('c'), 3);
	});

	it('get bumps recency so the entry survives eviction', () => {
		const cache = new LRUCacheWithTTL<string, number>(2, 1000);
		cache.set('a', 1);
		cache.set('b', 2);
		assert.strictEqual(cache.get('a'), 1); // bump 'a' to most-recent
		cache.set('c', 3); // should now evict 'b' (oldest), not 'a'
		assert.strictEqual(cache.get('a'), 1);
		assert.strictEqual(cache.get('b'), undefined);
		assert.strictEqual(cache.get('c'), 3);
	});

	it('clear() empties the cache', () => {
		const cache = new LRUCacheWithTTL<string, number>(2, 1000);
		cache.set('a', 1);
		cache.set('b', 2);
		cache.clear();
		assert.strictEqual(cache.size(), 0);
	});

	it('rejects invalid maxEntries / ttlMs', () => {
		assert.throws(() => new LRUCacheWithTTL<string, number>(0, 1000));
		assert.throws(() => new LRUCacheWithTTL<string, number>(1, 0));
	});
});

describe('settings-validator — memoize (R-11 acceptance: ≤1 probe per unique value within TTL)', () => {
	it('reuses cached result for repeat calls within TTL', async () => {
		let calls = 0;
		const probe = async (v: string) => { calls++; return { ok: true, message: v } satisfies ProbeResult; };
		const cache = new LRUCacheWithTTL<string, ProbeResult>(8, 1000);
		const m = memoize(probe, cache);
		await m('one');
		await m('one');
		await m('one');
		assert.strictEqual(calls, 1);
	});

	it('refetches after TTL expiry', async () => {
		let calls = 0;
		let now = 0;
		const probe = async (v: string) => { calls++; return { ok: true, message: v } satisfies ProbeResult; };
		const cache = new LRUCacheWithTTL<string, ProbeResult>(8, 100, () => now);
		const m = memoize(probe, cache);
		await m('one');
		now = 50; await m('one');
		now = 200; await m('one'); // expired
		assert.strictEqual(calls, 2);
	});

	it('separate keys probe independently', async () => {
		let calls: string[] = [];
		const probe = async (v: string): Promise<ProbeResult> => { calls.push(v); return { ok: true }; };
		const cache = new LRUCacheWithTTL<string, ProbeResult>(8, 1000);
		const m = memoize(probe, cache);
		await m('a'); await m('b'); await m('a');
		assert.deepStrictEqual(calls, ['a', 'b']);
	});
});

describe('settings-validator — validatePath', () => {
	it('empty + required → invalid', async () => {
		const probe = async () => { throw new Error('should not probe'); };
		const r = await validatePath('   ', true, probe);
		assert.strictEqual(r.ok, false);
		assert.strictEqual(r.message, 'Required');
	});

	it('empty + optional → valid', async () => {
		const probe = async () => { throw new Error('should not probe'); };
		const r = await validatePath('', false, probe);
		assert.strictEqual(r.ok, true);
	});

	it('non-empty trims before probe', async () => {
		let probed = '';
		const probe = async (v: string) => { probed = v; return { ok: true } satisfies ProbeResult; };
		await validatePath('  /opt/x  ', true, probe);
		assert.strictEqual(probed, '/opt/x');
	});

	it('non-empty + probe failure surfaces probe message', async () => {
		const probe = async () => ({ ok: false, message: 'Not found' } satisfies ProbeResult);
		const r = await validatePath('/missing', true, probe);
		assert.strictEqual(r.ok, false);
		assert.strictEqual(r.message, 'Not found');
	});
});

describe('settings-validator — makeProbeCache (composed)', () => {
	it('debounces + caches: a burst of repeat calls fires the probe exactly once', async () => {
		let calls = 0;
		const probe = async (v: string) => { calls++; return { ok: true, message: v } satisfies ProbeResult; };
		const { run } = makeProbeCache(probe, { debounceMs: 30, ttlMs: 1000 });
		const p1 = run('foo');
		const p2 = run('foo');
		const p3 = run('foo');
		await Promise.all([p1, p2, p3]);
		// All three calls collapse into one debounce window. The first run
		// (after debounce) populates the cache; subsequent rapid bursts within
		// the same debounce window are swallowed.
		assert.strictEqual(calls, 1);
	});

	it('cache hit on second window: repeated unique-value call within TTL bypasses probe', async () => {
		let calls = 0;
		const probe = async (v: string) => { calls++; return { ok: true, message: v } satisfies ProbeResult; };
		const { run } = makeProbeCache(probe, { debounceMs: 10, ttlMs: 1000 });
		await run('foo');
		await run('foo');
		assert.strictEqual(calls, 1);
	});
});
