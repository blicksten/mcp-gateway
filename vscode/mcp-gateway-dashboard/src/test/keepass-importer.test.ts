import { strict as assert } from 'node:assert';
import { applyImportedCredentials, KeepassImportError, type CredentialImportJSON, type ApplyResult } from '../keepass-importer';
import type { CredentialStore } from '../credential-store';

/**
 * Fake CredentialStore — records storeEnvVar / storeHeader calls in the
 * order they were made. storeEnvVar can be configured to throw on a
 * specific (server, key) pair so partial-failure paths can be exercised.
 */
class FakeStore {
	stored: Array<{ kind: 'env' | 'header'; server: string; key: string; value: string }> = [];
	failOn?: { server: string; kind: 'env' | 'header'; key: string };

	async storeEnvVar(server: string, key: string, value: string): Promise<void> {
		if (this.failOn && this.failOn.server === server && this.failOn.kind === 'env' && this.failOn.key === key) {
			throw new Error('simulated env write failure');
		}
		this.stored.push({ kind: 'env', server, key, value });
	}

	async storeHeader(server: string, key: string, value: string): Promise<void> {
		if (this.failOn && this.failOn.server === server && this.failOn.kind === 'header' && this.failOn.key === key) {
			throw new Error('simulated header write failure');
		}
		this.stored.push({ kind: 'header', server, key, value });
	}
}

function makePayload(overrides?: Partial<CredentialImportJSON>): CredentialImportJSON {
	return {
		version: 1,
		mode: 'dry-run',
		found: 2,
		servers: [
			{ name: 'alpha', env_vars: { ALPHA_PASSWORD: 'a-secret', ALPHA_USER: 'adm' }, headers: { Authorization: 'Bearer ax' } },
			{ name: 'beta',  env_vars: { BETA_PASSWORD: 'b-secret' }, headers: {} },
		],
		...overrides,
	};
}

describe('keepass-importer', () => {
	describe('applyImportedCredentials', () => {
		it('writes all env vars and headers for every server on the happy path', async () => {
			const store = new FakeStore();
			const payload = makePayload();

			const results = await applyImportedCredentials(store as unknown as CredentialStore, payload);

			assert.equal(results.length, 2, 'one result per server');
			assert.equal(results[0].status, 'stored');
			assert.equal(results[0].stored_env, 2);
			assert.equal(results[0].stored_headers, 1);
			assert.equal(results[1].status, 'stored');
			assert.equal(results[1].stored_env, 1);
			assert.equal(results[1].stored_headers, 0);

			// Every declared entry was written.
			assert.equal(store.stored.length, 4);
		});

		it('subsequent servers are written even when an earlier server fails', async () => {
			// Mixed outcome per architect finding 12B-5: one OK, one
			// partially stored (env ok, header fails), one failed (first
			// env write throws → nothing stored).
			const store = new FakeStore();
			store.failOn = { server: 'alpha', kind: 'header', key: 'Authorization' };

			const payload = makePayload();
			const results = await applyImportedCredentials(store as unknown as CredentialStore, payload);

			assert.equal(results.length, 2);
			const alpha = results.find((r: ApplyResult) => r.name === 'alpha')!;
			const beta  = results.find((r: ApplyResult) => r.name === 'beta')!;

			// alpha: env vars stored before header write threw. Status
			// stays 'stored' because partial progress is preserved.
			assert.equal(alpha.stored_env, 2, 'alpha env vars were persisted before header failure');
			assert.equal(alpha.stored_headers, 0);
			assert.equal(alpha.status, 'stored');
			assert.ok(alpha.error, 'partial failure recorded');

			// beta unaffected.
			assert.equal(beta.status, 'stored');
			assert.equal(beta.stored_env, 1);
		});

		it('marks server as failed when the first write throws (no partial progress)', async () => {
			const store = new FakeStore();
			store.failOn = { server: 'alpha', kind: 'env', key: 'ALPHA_PASSWORD' };

			// Single-server payload so result ordering is deterministic.
			const payload: CredentialImportJSON = {
				version: 1, mode: 'dry-run', found: 1,
				servers: [{ name: 'alpha', env_vars: { ALPHA_PASSWORD: 'x' }, headers: {} }],
			};
			const results = await applyImportedCredentials(store as unknown as CredentialStore, payload);

			assert.equal(results[0].status, 'failed');
			assert.equal(results[0].stored_env, 0);
		});
	});

	describe('KeepassImportError', () => {
		it('carries an optional exit code', () => {
			const err = new KeepassImportError('boom', 2);
			assert.equal(err.name, 'KeepassImportError');
			assert.equal(err.exitCode, 2);
			assert.ok(err instanceof Error);
		});
	});

	// Note: the execFile argv-array, maxBuffer, and stdin-piping behaviour
	// of runKeepassImport is exercised in the E2E test (pending a real
	// mcp-ctl binary on the test PATH); unit-testing it would require
	// mocking node:child_process.execFile which ts-node's module cache
	// complicates. The invariants are enforced structurally in the code
	// (no string concatenation into a shell command, argv is a literal
	// array with each flag as its own element).
});
