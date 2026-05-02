import { strict as assert } from 'node:assert';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import {
	ENV_VAR_NAME,
	MIN_TOKEN_LEN,
	AuthTokenError,
	looksLikeToken,
	resolveToken,
	buildAuthHeader,
	resolveTokenAsync,
	buildAuthHeaderAsync,
	resolveTokenPath,
	defaultTokenPath,
	_clearTokenCacheForTests,
} from '../auth-header';

const BASE64URL = 'A'.repeat(MIN_TOKEN_LEN); // minimal valid shape

function freshTokenPath(): string {
	const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'auth-header-test-'));
	return path.join(dir, 'auth.token');
}

function cleanupTokenPath(p: string) {
	try {
		fs.rmSync(path.dirname(p), { recursive: true, force: true });
	} catch { /* best-effort */ }
}

describe('auth-header', () => {
	let prevEnv: string | undefined;
	beforeEach(() => {
		prevEnv = process.env[ENV_VAR_NAME];
		delete process.env[ENV_VAR_NAME];
	});
	afterEach(() => {
		if (prevEnv === undefined) {
			delete process.env[ENV_VAR_NAME];
		} else {
			process.env[ENV_VAR_NAME] = prevEnv;
		}
	});

	describe('looksLikeToken', () => {
		it('accepts minimum-length base64url', () => {
			assert.equal(looksLikeToken(BASE64URL), true);
		});
		it('rejects empty string', () => {
			assert.equal(looksLikeToken(''), false);
		});
		it('rejects too-short string', () => {
			assert.equal(looksLikeToken('abc'), false);
		});
		it('rejects right-length but bad alphabet', () => {
			assert.equal(looksLikeToken('@'.repeat(MIN_TOKEN_LEN)), false);
		});
		it('accepts URL-safe alphabet with dash and underscore', () => {
			assert.equal(looksLikeToken('A'.repeat(MIN_TOKEN_LEN - 2) + '-_'), true);
		});
		it('rejects base64 padding', () => {
			assert.equal(looksLikeToken('A'.repeat(MIN_TOKEN_LEN - 1) + '='), false);
		});
	});

	describe('resolveToken', () => {
		it('env var wins over file', () => {
			const p = freshTokenPath();
			try {
				fs.writeFileSync(p, 'F'.repeat(MIN_TOKEN_LEN));
				process.env[ENV_VAR_NAME] = 'E'.repeat(MIN_TOKEN_LEN);
				const tok = resolveToken(p);
				assert.equal(tok, 'E'.repeat(MIN_TOKEN_LEN));
			} finally { cleanupTokenPath(p); }
		});

		it('falls back to file when env absent', () => {
			const p = freshTokenPath();
			try {
				fs.writeFileSync(p, BASE64URL);
				const tok = resolveToken(p);
				assert.equal(tok, BASE64URL);
			} finally { cleanupTokenPath(p); }
		});

		it('tolerates trailing whitespace in file', () => {
			const p = freshTokenPath();
			try {
				fs.writeFileSync(p, BASE64URL + '\n');
				const tok = resolveToken(p);
				assert.equal(tok, BASE64URL);
			} finally { cleanupTokenPath(p); }
		});

		it('throws AuthTokenError when both env and file absent', () => {
			const p = freshTokenPath();
			try {
				assert.throws(() => resolveToken(p), (err: Error) => {
					return err instanceof AuthTokenError &&
						err.message.includes(ENV_VAR_NAME) &&
						err.message.includes(p);
				});
			} finally { cleanupTokenPath(p); }
		});

		it('throws when env is malformed (too short)', () => {
			const p = freshTokenPath();
			try {
				process.env[ENV_VAR_NAME] = 'too-short';
				assert.throws(() => resolveToken(p), (err: Error) => {
					return err instanceof AuthTokenError && err.message.includes('malformed');
				});
			} finally { cleanupTokenPath(p); }
		});

		it('malformed file falls through to no-token error (not silent use)', () => {
			const p = freshTokenPath();
			try {
				fs.writeFileSync(p, '@'.repeat(MIN_TOKEN_LEN));
				assert.throws(() => resolveToken(p), (err: Error) => err instanceof AuthTokenError);
			} finally { cleanupTokenPath(p); }
		});
	});

	describe('buildAuthHeader', () => {
		it('returns "Bearer <token>"', () => {
			const p = freshTokenPath();
			try {
				fs.writeFileSync(p, BASE64URL);
				assert.equal(buildAuthHeader(p), 'Bearer ' + BASE64URL);
			} finally { cleanupTokenPath(p); }
		});
	});

	describe('resolveTokenAsync (B-NEW-29 async cache)', () => {
		beforeEach(() => {
			// Reset the module-level cache between tests for isolation.
			_clearTokenCacheForTests();
		});

		it('returns token from file on first call', async () => {
			const p = freshTokenPath();
			try {
				fs.writeFileSync(p, BASE64URL);
				const tok = await resolveTokenAsync(p);
				assert.equal(tok, BASE64URL);
			} finally { cleanupTokenPath(p); }
		});

		it('cache hit — re-read not triggered when mtime is stable', async () => {
			const p = freshTokenPath();
			let readCount = 0;
			const realReadFile = fs.promises.readFile.bind(fs.promises);
			(fs.promises as any).readFile = async (...args: unknown[]) => {
				if (typeof args[0] === 'string' && (args[0] as string).includes('auth')) {
					readCount++;
				}
				return realReadFile(...(args as Parameters<typeof fs.promises.readFile>));
			};
			try {
				fs.writeFileSync(p, BASE64URL);
				// First call loads from disk.
				await resolveTokenAsync(p);
				const countAfterFirst = readCount;
				// Second call with same file (mtime unchanged) → cache hit, no re-read.
				await resolveTokenAsync(p);
				assert.equal(readCount, countAfterFirst, 'readFile must NOT be called again when mtime is stable');
			} finally {
				(fs.promises as any).readFile = realReadFile;
				cleanupTokenPath(p);
			}
		});

		it('token rotation invalidates cache — re-read on mtime change', async () => {
			const p = freshTokenPath();
			const tokenV1 = BASE64URL;
			const tokenV2 = 'B'.repeat(MIN_TOKEN_LEN);
			try {
				fs.writeFileSync(p, tokenV1);
				const v1 = await resolveTokenAsync(p);
				assert.equal(v1, tokenV1);

				// Simulate token rotation: overwrite file and advance mtime by
				// touching it 10ms later. fs.utimesSync lets us set a future mtime
				// without relying on wall-clock drift across the test.
				const futureMs = Date.now() + 1000;
				const futureSec = futureMs / 1000;
				fs.writeFileSync(p, tokenV2);
				fs.utimesSync(p, futureSec, futureSec);

				// Next call should see different mtime → re-read → new token.
				const v2 = await resolveTokenAsync(p);
				assert.equal(v2, tokenV2, 'token rotation must invalidate cache and return new token (B-NEW-29)');
			} finally { cleanupTokenPath(p); }
		});

		it('env var wins over file and does not pollute cache', async () => {
			const p = freshTokenPath();
			try {
				fs.writeFileSync(p, BASE64URL);
				process.env[ENV_VAR_NAME] = 'E'.repeat(MIN_TOKEN_LEN);
				const tok = await resolveTokenAsync(p);
				assert.equal(tok, 'E'.repeat(MIN_TOKEN_LEN), 'env var must win over file');
				delete process.env[ENV_VAR_NAME];
				// Cache should hold file content; verify by reading again without env.
				const tok2 = await resolveTokenAsync(p);
				assert.equal(tok2, BASE64URL, 'after env removed, file value returned');
			} finally {
				delete process.env[ENV_VAR_NAME];
				cleanupTokenPath(p);
			}
		});

		it('buildAuthHeaderAsync returns "Bearer <token>"', async () => {
			const p = freshTokenPath();
			try {
				fs.writeFileSync(p, BASE64URL);
				const hdr = await buildAuthHeaderAsync(p);
				assert.equal(hdr, 'Bearer ' + BASE64URL);
			} finally { cleanupTokenPath(p); }
		});
	});

	describe('resolveTokenPath', () => {
		it('returns default when no config provided', () => {
			assert.equal(resolveTokenPath(undefined), defaultTokenPath());
		});

		it('returns default when setting is empty string', () => {
			const cfg = { get: (_s: string) => '' };
			assert.equal(resolveTokenPath(cfg), defaultTokenPath());
		});

		it('uses configured path verbatim when set', () => {
			const cfg = { get: (_s: string) => '/custom/path/token' };
			assert.equal(resolveTokenPath(cfg), '/custom/path/token');
		});

		it('expands leading ~/ to home directory', () => {
			const cfg = { get: (_s: string) => '~/custom/token' };
			const got = resolveTokenPath(cfg);
			assert.ok(got.startsWith(os.homedir()), `expected ${got} to start with home`);
			assert.ok(got.endsWith(path.join('custom', 'token')));
		});
	});
});
