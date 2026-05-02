import * as vscode from 'vscode';
import type { CredentialIndex, ServerCredentials } from './types';
import { SERVER_NAME_RE, ENV_KEY_RE, HEADER_NAME_RE as HEADER_KEY_RE } from './validation';

const INDEX_KEY = 'mcpGateway.credentialIndex';
const CURRENT_VERSION = 1;

/**
 * Manages server credentials using VS Code SecretStorage (OS keychain).
 * Secrets are keyed as mcpGateway/{serverName}/env/{KEY} or .../header/{KEY}.
 * A non-secret index in globalState tracks which keys exist per server.
 *
 * AUDIT B-NEW-24 (Phase 10): all index read-modify-write segments serialize
 * through `_indexChain`. Without this, `reconcile()` (which awaits many
 * `secrets.get` calls between read and write) can race with concurrent
 * `storeEnvVar`/`storeHeader` and lose updates — last writer wins, the
 * other's mutation is dropped silently. The chain pattern mirrors
 * `SlashCommandGenerator.lastTask` (slash-command-generator.ts:165): each
 * mutation enqueues onto a tail Promise and the chain catches errors so a
 * single failed mutation does not break the rest of the queue.
 */
export class CredentialStore {
	private readonly secrets: vscode.SecretStorage;
	private readonly globalState: vscode.Memento;
	private _indexChain: Promise<void> = Promise.resolve();

	constructor(context: vscode.ExtensionContext) {
		this.secrets = context.secrets;
		this.globalState = context.globalState;
	}

	/**
	 * Serialize an index-mutating task through `_indexChain`. The returned
	 * Promise resolves with the task's value; the chain itself is updated
	 * to wait for this task (with errors swallowed so one failed mutation
	 * does not stall the rest).
	 */
	private _chainIndexMutation<T>(task: () => Promise<T>): Promise<T> {
		const result = this._indexChain.then(task);
		this._indexChain = result.then(() => undefined, () => undefined);
		return result;
	}

	async storeEnvVar(server: string, key: string, value: string): Promise<void> {
		this._validateServerName(server);
		this._validateKey(key, 'env');
		// Index first — an orphaned index entry is prunable by reconcile().
		// An orphaned secret (secret first, then crash) is unrecoverable.
		// B-NEW-24: index mutation runs through the serialization chain.
		await this._chainIndexMutation(() => this._addToIndex(server, 'env', key));
		await this.secrets.store(`mcpGateway/${server}/env/${key}`, value);
	}

	async storeHeader(server: string, key: string, value: string): Promise<void> {
		this._validateServerName(server);
		this._validateKey(key, 'header');
		await this._chainIndexMutation(() => this._addToIndex(server, 'headers', key));
		await this.secrets.store(`mcpGateway/${server}/header/${key}`, value);
	}

	async getEnvVar(server: string, key: string): Promise<string | undefined> {
		this._validateServerName(server);
		this._validateKey(key, 'env');
		return this.secrets.get(`mcpGateway/${server}/env/${key}`);
	}

	async getHeader(server: string, key: string): Promise<string | undefined> {
		this._validateServerName(server);
		this._validateKey(key, 'header');
		return this.secrets.get(`mcpGateway/${server}/header/${key}`);
	}

	async getServerCredentials(server: string): Promise<{ env: Record<string, string>; headers: Record<string, string> }> {
		this._validateServerName(server);
		const index = this._getIndex();
		const entry = index.servers[server];
		const env: Record<string, string> = {};
		const headers: Record<string, string> = {};

		if (!entry) {
			return { env, headers };
		}

		for (const key of entry.env) {
			const val = await this.secrets.get(`mcpGateway/${server}/env/${key}`);
			if (val !== undefined) {
				env[key] = val;
			}
		}
		for (const key of entry.headers) {
			const val = await this.secrets.get(`mcpGateway/${server}/header/${key}`);
			if (val !== undefined) {
				headers[key] = val;
			}
		}

		return { env, headers };
	}

	async deleteServerCredentials(server: string): Promise<void> {
		this._validateServerName(server);
		// B-NEW-24: serialize the read-delete-write index segment so it can
		// not interleave with a concurrent reconcile() that's mid-loop.
		await this._chainIndexMutation(async () => {
			const index = this._getIndex();
			const entry = index.servers[server];
			if (!entry) { return; }
			for (const key of entry.env) {
				await this.secrets.delete(`mcpGateway/${server}/env/${key}`);
			}
			for (const key of entry.headers) {
				await this.secrets.delete(`mcpGateway/${server}/header/${key}`);
			}
			delete index.servers[server];
			await this._setIndex(index);
		});
	}

	listServers(): string[] {
		const index = this._getIndex();
		return Object.keys(index.servers);
	}

	async reconcile(): Promise<void> {
		// B-NEW-24: the entire reconcile pass — read index, await many
		// secrets.get calls, write back the pruned index — runs as one
		// serialized chain entry. This prevents an interleaving
		// `storeEnvVar` from writing the index while we're still reading
		// secrets, which would cause our final `_setIndex` to undo their
		// addition.
		await this._chainIndexMutation(() => this._reconcileLocked());
	}

	private async _reconcileLocked(): Promise<void> {
		const index = this._getIndex();
		let modified = false;

		for (const [server, entry] of Object.entries(index.servers)) {
			// Validate names from untrusted globalState before constructing
			// SecretStorage keys. Prune entries that fail validation.
			if (!SERVER_NAME_RE.test(server)) {
				delete index.servers[server];
				modified = true;
				continue;
			}

			const validEnv: string[] = [];
			for (const key of entry.env) {
				if (!ENV_KEY_RE.test(key)) { modified = true; continue; }
				const val = await this.secrets.get(`mcpGateway/${server}/env/${key}`);
				if (val !== undefined) {
					validEnv.push(key);
				}
			}

			const validHeaders: string[] = [];
			for (const key of entry.headers) {
				if (!HEADER_KEY_RE.test(key)) { modified = true; continue; }
				const val = await this.secrets.get(`mcpGateway/${server}/header/${key}`);
				if (val !== undefined) {
					validHeaders.push(key);
				}
			}

			if (validEnv.length !== entry.env.length || validHeaders.length !== entry.headers.length) {
				modified = true;
				const pruned = (entry.env.length - validEnv.length) + (entry.headers.length - validHeaders.length);
				console.warn(`[CredentialStore] reconcile: pruned ${pruned} stale entries for server "${server}"`);
				if (validEnv.length === 0 && validHeaders.length === 0) {
					delete index.servers[server];
				} else {
					index.servers[server] = { env: validEnv, headers: validHeaders };
				}
			}
		}

		if (modified) {
			await this._setIndex(index);
		}
	}

	private _validateServerName(name: string): void {
		if (!SERVER_NAME_RE.test(name)) {
			throw new Error(`Invalid server name: ${JSON.stringify(name)}`);
		}
	}

	private _validateKey(key: string, kind: 'env' | 'header'): void {
		if (!key) {
			throw new Error(`${kind} key must not be empty`);
		}
		if (kind === 'env' && !ENV_KEY_RE.test(key)) {
			throw new Error(`Invalid env key: ${JSON.stringify(key)}`);
		}
		if (kind === 'header' && !HEADER_KEY_RE.test(key)) {
			throw new Error(`Invalid header key: ${JSON.stringify(key)}`);
		}
	}

	private _getIndex(): CredentialIndex {
		const raw = this.globalState.get<unknown>(INDEX_KEY);
		if (
			!raw ||
			typeof raw !== 'object' ||
			(raw as CredentialIndex)._version !== CURRENT_VERSION ||
			typeof (raw as CredentialIndex).servers !== 'object' ||
			(raw as CredentialIndex).servers === null ||
			Array.isArray((raw as CredentialIndex).servers)
		) {
			return { _version: CURRENT_VERSION, servers: {} };
		}
		// Sanitize: keep only well-formed entries (arrays of strings).
		const servers: Record<string, ServerCredentials> = {};
		for (const [k, v] of Object.entries((raw as CredentialIndex).servers)) {
			if (
				v && typeof v === 'object' && !Array.isArray(v) &&
				Array.isArray((v as ServerCredentials).env) &&
				Array.isArray((v as ServerCredentials).headers) &&
				(v as ServerCredentials).env.every((e: unknown) => typeof e === 'string') &&
				(v as ServerCredentials).headers.every((h: unknown) => typeof h === 'string')
			) {
				servers[k] = v as ServerCredentials;
			}
		}
		return { _version: CURRENT_VERSION, servers };
	}

	private async _setIndex(index: CredentialIndex): Promise<void> {
		await this.globalState.update(INDEX_KEY, index);
	}

	private async _addToIndex(server: string, field: 'env' | 'headers', key: string): Promise<void> {
		const index = this._getIndex();
		if (!index.servers[server]) {
			index.servers[server] = { env: [], headers: [] };
		}
		const list = index.servers[server][field];
		if (!list.includes(key)) {
			list.push(key);
		}
		await this._setIndex(index);
	}
}
