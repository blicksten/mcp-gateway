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
	resolveTokenPath,
	defaultTokenPath,
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
