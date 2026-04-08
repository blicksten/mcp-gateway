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
});
