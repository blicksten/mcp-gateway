// Settings webview validator (Phase C T-C.2).
//
// Pure module — no vscode imports — so unit tests run on plain node.
// Provides:
//   * debounce — wraps an async fn with a trailing-edge debounce
//   * LRU cache with TTL — bounds repeated probes within a TTL window
//     (R-11: 300ms debounce + LRU TTL=10s on existsSync / `--version` calls)
//   * validatePath — typed result for required / optional path fields
//   * makeProbeCache — composes debounce + cache around a probe function
//
// The actual fs/exec probes live behind injected functions so the tests can
// assert exec spy counts (acceptance: ≤1 exec per unique value within TTL).

export interface ProbeResult {
	ok: boolean;
	message?: string;
}

/** A probe fn — implementation may call fs.existsSync, exec --version, etc.
 *  Tests inject a spy. */
export type ProbeFn = (value: string) => Promise<ProbeResult>;

/** A trailing-edge debouncer keyed per call. The most recent call wins; any
 *  pending earlier call's promise resolves with the latest result. */
export interface Debounced<TIn, TOut> {
	(input: TIn): Promise<TOut>;
	cancel(): void;
}

/** Trailing-edge debounce. The returned promise resolves with the last
 *  call's result after the delay has elapsed without new calls. Earlier
 *  promises in the same debounce window resolve to the same final result.
 *
 *  Implementation note: we keep a single shared promise per debounce window
 *  so callers waiting on stale calls do not see stale results. */
export function debounce<TIn, TOut>(
	fn: (input: TIn) => Promise<TOut>,
	delayMs: number,
): Debounced<TIn, TOut> {
	let timer: ReturnType<typeof setTimeout> | undefined;
	let pendingInput: TIn | undefined;
	let pendingResolves: Array<(v: TOut) => void> = [];
	let pendingRejects: Array<(e: unknown) => void> = [];

	const fire = (): void => {
		const input = pendingInput as TIn;
		const resolves = pendingResolves;
		const rejects = pendingRejects;
		pendingResolves = [];
		pendingRejects = [];
		pendingInput = undefined;
		timer = undefined;
		void fn(input).then(
			(v) => { for (const r of resolves) { r(v); } },
			(e) => { for (const r of rejects) { r(e); } },
		);
	};

	const debounced = ((input: TIn): Promise<TOut> => {
		pendingInput = input;
		return new Promise<TOut>((resolve, reject) => {
			pendingResolves.push(resolve);
			pendingRejects.push(reject);
			if (timer !== undefined) { clearTimeout(timer); }
			timer = setTimeout(fire, delayMs);
		});
	}) as Debounced<TIn, TOut>;

	debounced.cancel = (): void => {
		if (timer !== undefined) {
			clearTimeout(timer);
			timer = undefined;
			pendingInput = undefined;
			pendingResolves = [];
			pendingRejects = [];
		}
	};

	return debounced;
}

interface CacheEntry<T> {
	value: T;
	expiresAt: number;
}

/** LRU cache with per-entry TTL. `maxEntries` bounds memory; `ttlMs` bounds
 *  staleness. Both `set` and `get` count as access for LRU recency. */
export class LRUCacheWithTTL<K, V> {
	private readonly map = new Map<K, CacheEntry<V>>();

	constructor(
		private readonly maxEntries: number,
		private readonly ttlMs: number,
		private readonly now: () => number = Date.now,
	) {
		if (maxEntries < 1) { throw new Error('maxEntries must be >= 1'); }
		if (ttlMs < 1) { throw new Error('ttlMs must be >= 1'); }
	}

	get(key: K): V | undefined {
		const entry = this.map.get(key);
		if (!entry) { return undefined; }
		if (this.now() >= entry.expiresAt) {
			this.map.delete(key);
			return undefined;
		}
		// Recency bump: re-insert at the end of the iteration order.
		this.map.delete(key);
		this.map.set(key, entry);
		return entry.value;
	}

	set(key: K, value: V): void {
		if (this.map.has(key)) { this.map.delete(key); }
		this.map.set(key, { value, expiresAt: this.now() + this.ttlMs });
		while (this.map.size > this.maxEntries) {
			const oldest = this.map.keys().next();
			if (oldest.done) { break; }
			this.map.delete(oldest.value);
		}
	}

	has(key: K): boolean {
		return this.get(key) !== undefined;
	}

	clear(): void {
		this.map.clear();
	}

	size(): number {
		return this.map.size;
	}
}

/** Compose a TTL-cached probe. Repeat calls with the same value within the
 *  TTL window return the cached result without invoking the underlying
 *  probe (R-11 acceptance: ≤1 exec call per unique value within 10s).
 *
 *  Sentinel contract (C-04): `LRUCacheWithTTL.get()` returns `undefined`
 *  both when the key is missing AND when the entry has TTL-expired. This
 *  composer therefore relies on `TRes` NEVER being `undefined`. The
 *  current consumer (`ProbeResult` — always a plain object) satisfies the
 *  contract. If a future caller needs `undefined` as a stored value, swap
 *  to a `has()`-then-`get()` pair to avoid the sentinel collision. */
export function memoize<TArg extends string, TRes>(
	fn: (arg: TArg) => Promise<TRes>,
	cache: LRUCacheWithTTL<TArg, TRes>,
): (arg: TArg) => Promise<TRes> {
	return async (arg: TArg): Promise<TRes> => {
		const cached = cache.get(arg);
		if (cached !== undefined) { return cached; }
		const value = await fn(arg);
		cache.set(arg, value);
		return value;
	};
}

/** validatePath — typed result for path-shaped form fields.
 *
 *  - empty + required        → invalid with "required" message
 *  - empty + optional        → valid (no probe)
 *  - non-empty + probe.ok    → valid
 *  - non-empty + probe.fail  → invalid with the probe's message
 *
 *  Caller injects `probe` so tests can spy on call count and synchronously
 *  control the result. Production wires probe to a `fs.existsSync` /
 *  `child_process.exec --version` chain at the panel layer. */
export async function validatePath(
	value: string,
	required: boolean,
	probe: ProbeFn,
): Promise<ProbeResult> {
	const trimmed = value.trim();
	if (trimmed.length === 0) {
		if (required) { return { ok: false, message: 'Required' }; }
		return { ok: true };
	}
	return probe(trimmed);
}

/** Convenience: build a debounced + cached probe wrapper. Default 300ms
 *  debounce + 10s TTL + 64-entry cap match T-C.2 acceptance. */
export function makeProbeCache(
	probe: ProbeFn,
	opts: { debounceMs?: number; ttlMs?: number; maxEntries?: number; now?: () => number } = {},
): { run: (value: string) => Promise<ProbeResult>; cache: LRUCacheWithTTL<string, ProbeResult> } {
	const debounceMs = opts.debounceMs ?? 300;
	const ttlMs = opts.ttlMs ?? 10_000;
	const maxEntries = opts.maxEntries ?? 64;
	const now = opts.now;
	const cache = new LRUCacheWithTTL<string, ProbeResult>(maxEntries, ttlMs, now);
	const memoizedProbe = memoize(probe, cache);
	const debounced = debounce(memoizedProbe, debounceMs);
	return {
		run: (value: string) => debounced(value),
		cache,
	};
}
