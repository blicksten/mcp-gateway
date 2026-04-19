import { promises as fsp } from 'node:fs';
import * as fs from 'node:fs';
import * as path from 'node:path';
import Ajv, { ValidateFunction } from 'ajv';
import addFormats from 'ajv-formats';

export interface ServerEntry {
	name: string;
	display_name: string;
	transport: 'http' | 'stdio';
	description: string;
	url?: string;
	command?: string;
	args?: string[];
	env_keys?: string[];
	header_keys?: string[];
	homepage?: string;
	tags?: string[];
	default_config?: Record<string, unknown>;
}

export interface CommandEntry {
	server_name: string;
	command_name: string;
	description: string;
	template_md: string;
	required_vars?: string[];
	suggested_vars?: string[];
}

export interface CatalogResult<T> {
	entries: T[];
	warnings: string[];
}

export const MAX_BYTES = 1_048_576;
export const SUPPORTED_MAJOR = '1';

// Schemas are bundled at <extensionPath>/docs/catalog/. From compiled out/catalog.js
// or from src/catalog.ts (under ts-node), `__dirname/..` reaches the extension root.
const SCHEMA_DIR_RELATIVE = path.join('..', 'docs', 'catalog');

interface SchemaCache {
	ajv: Ajv;
	serverValidate: ValidateFunction;
	commandValidate: ValidateFunction;
}

let schemaCache: SchemaCache | null = null;
let schemaCacheError: string | null = null;

/**
 * Reset the schema cache. Test-only — do not call from production code.
 */
export function _resetSchemaCacheForTests(): void {
	schemaCache = null;
	schemaCacheError = null;
}

function loadSchemasOnce(): SchemaCache | null {
	if (schemaCache) {
		return schemaCache;
	}
	if (schemaCacheError !== null) {
		return null;
	}
	try {
		const baseDir = path.join(__dirname, SCHEMA_DIR_RELATIVE);
		const serverSchema = JSON.parse(
			fs.readFileSync(path.join(baseDir, 'schema.server.json'), 'utf8'),
		) as object;
		const commandSchema = JSON.parse(
			fs.readFileSync(path.join(baseDir, 'schema.command.json'), 'utf8'),
		) as object;
		const ajv = new Ajv({
			strict: true,
			allErrors: false,
			allowUnionTypes: false,
		});
		addFormats(ajv);
		const serverValidate = ajv.compile(serverSchema);
		const commandValidate = ajv.compile(commandSchema);
		schemaCache = { ajv, serverValidate, commandValidate };
		return schemaCache;
	} catch (err) {
		schemaCacheError = (err as Error).message;
		return null;
	}
}

/**
 * Returns true when the given schema/data $id encodes a v1 major version.
 * Regex-free parse per audit fix N-3 — split on the literal `.v` separator,
 * take the suffix, accept when it equals `"1"` or starts with `"1."`.
 */
export function isSupportedId(schemaId: unknown): boolean {
	if (typeof schemaId !== 'string') {
		return false;
	}
	const sepIdx = schemaId.lastIndexOf('.v');
	if (sepIdx === -1) {
		return false;
	}
	const tail = schemaId.slice(sepIdx + 2);
	return tail === SUPPORTED_MAJOR || tail.startsWith(`${SUPPORTED_MAJOR}.`);
}

type SchemaKind = 'server' | 'command';

async function loadCatalogFile<T>(
	filePath: string | undefined,
	kind: SchemaKind,
): Promise<CatalogResult<T>> {
	if (!filePath) {
		return { entries: [], warnings: ['catalog: no path provided'] };
	}

	const cache = loadSchemasOnce();
	if (!cache) {
		return {
			entries: [],
			warnings: [`catalog: validator init failed: ${schemaCacheError ?? 'unknown'}`],
		};
	}
	const validate = kind === 'server' ? cache.serverValidate : cache.commandValidate;

	// TOCTOU-safe read: single file handle owns stat + bounded read so an attacker
	// cannot swap the inode between the size precheck and the content read.
	let handle: fsp.FileHandle | null = null;
	try {
		handle = await fsp.open(filePath, 'r');
		const stat = await handle.stat();
		if (stat.size > MAX_BYTES) {
			return {
				entries: [],
				warnings: [
					`catalog: ${filePath} exceeds ${MAX_BYTES} bytes (got ${stat.size}); refusing to read`,
				],
			};
		}
		// Bounded read — even if stat reports a small size (sparse file or racing writer),
		// cap the actual read at MAX_BYTES + 1 so we can detect overrun.
		const buf = Buffer.alloc(MAX_BYTES + 1);
		const { bytesRead } = await handle.read(buf, 0, MAX_BYTES + 1, 0);
		if (bytesRead > MAX_BYTES) {
			return {
				entries: [],
				warnings: [
					`catalog: ${filePath} exceeded ${MAX_BYTES} bytes during bounded read; refusing to load`,
				],
			};
		}
		const content = buf.subarray(0, bytesRead).toString('utf8');

		let parsed: unknown;
		try {
			parsed = JSON.parse(content);
		} catch (err) {
			return {
				entries: [],
				warnings: [`catalog: ${filePath} JSON parse failed: ${(err as Error).message}`],
			};
		}

		// Two accepted layouts:
		//   1. Bare array (current v1 seeds).
		//   2. Wrapped object { "$id": "...v1.json", "entries": [...] } — forward-compat
		//      so v2 wrappers are explicitly rejected here, letting v1 + v2 schemas coexist
		//      per decision D4.
		if (Array.isArray(parsed)) {
			if (!validate(parsed)) {
				return {
					entries: [],
					warnings: [
						`catalog: ${filePath} schema validation failed: ${cache.ajv.errorsText(
							validate.errors,
						)}`,
					],
				};
			}
			return { entries: parsed as T[], warnings: [] };
		}

		if (typeof parsed === 'object' && parsed !== null && '$id' in parsed) {
			const wrapper = parsed as { $id?: unknown; entries?: unknown };
			if (!isSupportedId(wrapper.$id)) {
				return {
					entries: [],
					warnings: [
						`catalog: ${filePath} unsupported $id ${String(
							wrapper.$id,
						)}; expected major version v${SUPPORTED_MAJOR}`,
					],
				};
			}
			if (!Array.isArray(wrapper.entries)) {
				return {
					entries: [],
					warnings: [`catalog: ${filePath} v1 wrapper missing 'entries' array`],
				};
			}
			if (!validate(wrapper.entries)) {
				return {
					entries: [],
					warnings: [
						`catalog: ${filePath} schema validation failed: ${cache.ajv.errorsText(
							validate.errors,
						)}`,
					],
				};
			}
			return { entries: wrapper.entries as T[], warnings: [] };
		}

		return {
			entries: [],
			warnings: [
				`catalog: ${filePath} expected array or v1 wrapper object, got ${typeof parsed}`,
			],
		};
	} catch (err) {
		return {
			entries: [],
			warnings: [`catalog: failed to read ${filePath}: ${(err as Error).message}`],
		};
	} finally {
		if (handle) {
			try {
				await handle.close();
			} catch {
				// ignore — close failure on read-only handle is non-fatal
			}
		}
	}
}

export async function loadServersCatalog(
	filePath?: string,
): Promise<CatalogResult<ServerEntry>> {
	return loadCatalogFile<ServerEntry>(filePath, 'server');
}

export async function loadCommandsCatalog(
	filePath?: string,
): Promise<CatalogResult<CommandEntry>> {
	return loadCatalogFile<CommandEntry>(filePath, 'command');
}
