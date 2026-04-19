import './mock-vscode';
import { strict as assert } from 'node:assert';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { createTmpDir, cleanupTmpDir } from './helpers/tmpdir';
import { fireConfigChange, mockVscode, mockConfigValues, resetMockState } from './mock-vscode';
import { _resetSchemaCacheForTests } from '../catalog';
import { ServerDataCache, type CacheRefreshPayload } from '../server-data-cache';
import { SlashCommandGenerator, MARKER } from '../slash-command-generator';
import type { ServerView } from '../types';

function makeServer(name: string, status: string, tools?: Array<{ name: string; description?: string }>): ServerView {
	return { name, status, transport: 'stdio', restart_count: 0, tools } as ServerView;
}

function makePayload(servers: ServerView[], lastRefreshFailed = false): CacheRefreshPayload {
	return { servers, lastRefreshFailed };
}

describe('SlashCommandGenerator', () => {
	let tmpDir: string;
	let cache: ServerDataCache;
	let gen: SlashCommandGenerator;
	let client: { listServers: () => Promise<unknown[]> };

	beforeEach(() => {
		resetMockState();
		tmpDir = createTmpDir();

		// Point the config to the tmpDir.
		mockConfigValues['mcpGateway.slashCommandsPath'] = tmpDir;
		mockVscode.workspace.workspaceFolders = [{ uri: { fsPath: tmpDir }, name: 'test', index: 0 }];

		client = { listServers: () => Promise.resolve([]) };
		cache = new ServerDataCache(client as any);
		gen = new SlashCommandGenerator(cache);
	});

	afterEach(() => {
		gen.dispose();
		cache.dispose();
		cleanupTmpDir(tmpDir);
	});

	function fireRefresh(payload: CacheRefreshPayload): void {
		// Access the private event emitter via the cache's onDidRefresh.
		// The generator subscribes via enable(), so we trigger a cache refresh
		// by directly firing through the cache's emitter.
		(cache as any)._onDidRefresh.fire(payload);
	}

	async function drain(): Promise<void> {
		// Wait for the async queue to flush.
		await (gen as any).lastTask;
	}

	describe('resolveCommandsDir', () => {
		it('returns absolute path as-is', () => {
			mockConfigValues['mcpGateway.slashCommandsPath'] = '/absolute/path';
			assert.equal(gen.resolveCommandsDir(), '/absolute/path');
		});

		it('expands ${workspaceFolder}', () => {
			mockConfigValues['mcpGateway.slashCommandsPath'] = '${workspaceFolder}/.claude/commands';
			mockVscode.workspace.workspaceFolders = [{ uri: { fsPath: '/my/workspace' }, name: 'ws', index: 0 }];
			assert.equal(gen.resolveCommandsDir(), '/my/workspace/.claude/commands');
		});

		it('returns null when no workspace and path uses ${workspaceFolder}', () => {
			mockConfigValues['mcpGateway.slashCommandsPath'] = '${workspaceFolder}/.claude/commands';
			mockVscode.workspace.workspaceFolders = undefined;
			assert.equal(gen.resolveCommandsDir(), null);
		});
	});

	describe('generateCommand (T11E.12)', () => {
		it('writes new file with marker as line 1', async () => {
			gen.enable();
			// Seed
			fireRefresh(makePayload([makeServer('alpha', 'stopped')]));
			// Transition to running
			fireRefresh(makePayload([makeServer('alpha', 'running', [{ name: 'tool1' }])]));
			await drain();

			const filePath = path.join(tmpDir, 'alpha.md');
			assert.ok(fs.existsSync(filePath), 'file should be created');
			const content = fs.readFileSync(filePath, 'utf8');
			assert.equal(content.split('\n')[0], MARKER);
			assert.ok(content.includes('# alpha'));
			assert.ok(content.includes('- tool1'));
		});

		it('content matches template with status and transport', async () => {
			gen.enable();
			fireRefresh(makePayload([makeServer('beta', 'stopped')]));
			fireRefresh(makePayload([makeServer('beta', 'running')]));
			await drain();

			const content = fs.readFileSync(path.join(tmpDir, 'beta.md'), 'utf8');
			assert.ok(content.includes('**Status:** running'));
			assert.ok(content.includes('**Transport:** stdio'));
			assert.ok(content.includes('_(no tools exposed)_'));
		});
	});

	describe('overwrite and skip (T11E.13)', () => {
		it('overwrites existing file when marker is present', async () => {
			const filePath = path.join(tmpDir, 'gamma.md');
			fs.writeFileSync(filePath, MARKER + '\n# old content\n', 'utf8');

			gen.enable();
			fireRefresh(makePayload([makeServer('gamma', 'stopped')]));
			fireRefresh(makePayload([makeServer('gamma', 'running')]));
			await drain();

			const content = fs.readFileSync(filePath, 'utf8');
			assert.ok(content.includes('# gamma'), 'content should be refreshed');
			assert.ok(!content.includes('old content'));
		});

		it('skips file without marker (user-authored)', async () => {
			const filePath = path.join(tmpDir, 'gamma.md');
			fs.writeFileSync(filePath, '# User-authored file\nDo not overwrite.\n', 'utf8');

			gen.enable();
			fireRefresh(makePayload([makeServer('gamma', 'stopped')]));
			fireRefresh(makePayload([makeServer('gamma', 'running')]));
			await drain();

			const content = fs.readFileSync(filePath, 'utf8');
			assert.ok(content.includes('User-authored'), 'user file must be preserved');
		});

		it('deduplicates log-once on repeated skip', async () => {
			const filePath = path.join(tmpDir, 'delta.md');
			fs.writeFileSync(filePath, '# user file\n', 'utf8');

			gen.enable();
			fireRefresh(makePayload([makeServer('delta', 'stopped')]));
			fireRefresh(makePayload([makeServer('delta', 'running')]));
			await drain();
			// Transition again
			fireRefresh(makePayload([makeServer('delta', 'stopped')]));
			fireRefresh(makePayload([makeServer('delta', 'running')]));
			await drain();

			const logged = (gen as any).loggedUnmarkedFiles as Set<string>;
			assert.equal(logged.size, 1, 'should log skip only once per file');
		});
	});

	describe('deleteCommand (T11E.14)', () => {
		it('deletes file with marker', async () => {
			const filePath = path.join(tmpDir, 'epsilon.md');
			fs.writeFileSync(filePath, MARKER + '\n# auto\n', 'utf8');

			gen.enable();
			fireRefresh(makePayload([makeServer('epsilon', 'running')]));
			fireRefresh(makePayload([makeServer('epsilon', 'stopped')]));
			await drain();

			assert.ok(!fs.existsSync(filePath), 'file should be deleted');
		});

		it('skips file without marker on delete', async () => {
			const filePath = path.join(tmpDir, 'epsilon.md');
			fs.writeFileSync(filePath, '# user authored\n', 'utf8');

			gen.enable();
			fireRefresh(makePayload([makeServer('epsilon', 'running')]));
			fireRefresh(makePayload([makeServer('epsilon', 'stopped')]));
			await drain();

			assert.ok(fs.existsSync(filePath), 'user file must be preserved');
		});

		it('no throw on missing file', async () => {
			gen.enable();
			fireRefresh(makePayload([makeServer('ghost', 'running')]));
			fireRefresh(makePayload([makeServer('ghost', 'stopped')]));
			await drain();
			// No assertion needed — test passes if no throw.
		});
	});

	describe('orphan cleanup (T11E.15)', () => {
		it('deletes generator-authored files not in cache', async () => {
			const orphanPath = path.join(tmpDir, 'old-server.md');
			fs.writeFileSync(orphanPath, MARKER + '\n# old\n', 'utf8');

			gen.enable();
			fireRefresh(makePayload([makeServer('live', 'running')]));
			// Second refresh with live server only — old-server is an orphan.
			fireRefresh(makePayload([makeServer('live', 'running')]));
			await drain();

			assert.ok(!fs.existsSync(orphanPath), 'orphan should be cleaned');
		});

		it('skips orphan cleanup when lastRefreshFailed is true', async () => {
			const orphanPath = path.join(tmpDir, 'preserved.md');
			fs.writeFileSync(orphanPath, MARKER + '\n# keep\n', 'utf8');

			gen.enable();
			fireRefresh(makePayload([makeServer('live', 'running')]));
			// Failed refresh — daemon offline.
			fireRefresh(makePayload([], true));
			await drain();

			assert.ok(fs.existsSync(orphanPath), 'orphan must be preserved during daemon outage');
		});

		it('no-workspace scenario returns null and generator no-ops', async () => {
			mockConfigValues['mcpGateway.slashCommandsPath'] = '${workspaceFolder}/.claude/commands';
			mockVscode.workspace.workspaceFolders = undefined;

			gen.enable();
			fireRefresh(makePayload([makeServer('x', 'stopped')]));
			fireRefresh(makePayload([makeServer('x', 'running')]));
			await drain();

			// No files should be created anywhere — resolveCommandsDir returns null.
			assert.equal(gen.resolveCommandsDir(), null);
		});
	});

	describe('transition detection (T11E.16)', () => {
		it('first refresh seeds map without emitting writes', async () => {
			gen.enable();
			fireRefresh(makePayload([makeServer('srv', 'running')]));
			await drain();

			assert.ok(!fs.existsSync(path.join(tmpDir, 'srv.md')),
				'first refresh should seed only, not write');
		});

		it('stopped → running triggers generateCommand', async () => {
			gen.enable();
			fireRefresh(makePayload([makeServer('srv', 'stopped')]));
			fireRefresh(makePayload([makeServer('srv', 'running')]));
			await drain();

			assert.ok(fs.existsSync(path.join(tmpDir, 'srv.md')));
		});

		it('running → stopped triggers deleteCommand', async () => {
			gen.enable();
			// Seed with running
			fireRefresh(makePayload([makeServer('srv', 'running')]));
			// Manually create the file so delete has something to remove.
			fs.writeFileSync(path.join(tmpDir, 'srv.md'), MARKER + '\n# srv\n', 'utf8');
			// Transition to stopped
			fireRefresh(makePayload([makeServer('srv', 'stopped')]));
			await drain();

			assert.ok(!fs.existsSync(path.join(tmpDir, 'srv.md')));
		});

		it('running → degraded does not trigger delete (still active)', async () => {
			gen.enable();
			fireRefresh(makePayload([makeServer('srv', 'running')]));
			fs.writeFileSync(path.join(tmpDir, 'srv.md'), MARKER + '\n# srv\n', 'utf8');
			fireRefresh(makePayload([makeServer('srv', 'degraded')]));
			await drain();

			// degraded is an active state — file should still exist (regenerated).
			assert.ok(fs.existsSync(path.join(tmpDir, 'srv.md')));
		});

		it('server removed from list triggers delete', async () => {
			gen.enable();
			fireRefresh(makePayload([makeServer('srv', 'running')]));
			fs.writeFileSync(path.join(tmpDir, 'srv.md'), MARKER + '\n# srv\n', 'utf8');
			// srv disappears from the list
			fireRefresh(makePayload([]));
			await drain();

			assert.ok(!fs.existsSync(path.join(tmpDir, 'srv.md')));
		});
	});

	describe('daemon outage protection', () => {
		it('does not delete files when lastRefreshFailed is true (empty server list)', async () => {
			gen.enable();
			// Seed with a running server.
			fireRefresh(makePayload([makeServer('srv', 'running')]));
			// Create the file so delete has something to target.
			fs.writeFileSync(path.join(tmpDir, 'srv.md'), MARKER + '\n# srv\n', 'utf8');
			// Daemon goes offline — cache returns empty list with lastRefreshFailed=true.
			fireRefresh(makePayload([], true));
			await drain();

			assert.ok(fs.existsSync(path.join(tmpDir, 'srv.md')),
				'file must survive daemon outage even though server list is empty');
		});

		it('resumes normal operation after outage ends', async () => {
			gen.enable();
			fireRefresh(makePayload([makeServer('srv', 'running')]));
			fs.writeFileSync(path.join(tmpDir, 'srv.md'), MARKER + '\n# srv\n', 'utf8');
			// Outage
			fireRefresh(makePayload([], true));
			// Recovery — server is stopped now
			fireRefresh(makePayload([makeServer('srv', 'stopped')]));
			await drain();

			assert.ok(!fs.existsSync(path.join(tmpDir, 'srv.md')),
				'file should be deleted after outage recovery when server is stopped');
		});
	});

	describe('async queue serialization (T11E.17)', () => {
		it('serializes concurrent generate and delete calls', async () => {
			const order: string[] = [];
			const origGen = (gen as any).generateCommand.bind(gen);
			const origDel = (gen as any).deleteCommand.bind(gen);

			(gen as any).generateCommand = async (dir: string, server: ServerView) => {
				order.push(`gen:${server.name}`);
				return origGen(dir, server);
			};
			(gen as any).deleteCommand = async (dir: string, name: string) => {
				order.push(`del:${name}`);
				return origDel(dir, name);
			};

			gen.enable();
			fireRefresh(makePayload([
				makeServer('a', 'stopped'),
				makeServer('b', 'running'),
			]));
			// Trigger multiple transitions in one refresh.
			fireRefresh(makePayload([
				makeServer('a', 'running'),
				makeServer('b', 'stopped'),
			]));
			await drain();

			assert.equal(order[0], 'gen:a');
			assert.equal(order[1], 'del:b');
		});
	});

	describe('enable/disable lifecycle', () => {
		it('disable stops processing refreshes', async () => {
			gen.enable();
			fireRefresh(makePayload([makeServer('srv', 'stopped')]));
			gen.disable();
			fireRefresh(makePayload([makeServer('srv', 'running')]));
			await drain();

			assert.ok(!fs.existsSync(path.join(tmpDir, 'srv.md')),
				'disabled generator should not write files');
		});

		it('re-enable reseeds from fresh state', async () => {
			gen.enable();
			fireRefresh(makePayload([makeServer('srv', 'stopped')]));
			gen.disable();
			gen.enable();
			// This is a seed refresh — should not write.
			fireRefresh(makePayload([makeServer('srv', 'running')]));
			await drain();

			assert.ok(!fs.existsSync(path.join(tmpDir, 'srv.md')),
				're-enabled generator should seed first');
		});
	});

	// Catalog enrichment tests — CC.1 (catalog lookup), CC.2 (variable
	// substitution), CC.3 (marker semantics). These run alongside the 25
	// pre-CC tests above: the pre-CC tests do NOT set mcpGateway.catalogPath,
	// so the generator falls through to the skeleton path and their
	// assertions are unaffected by catalog-enrichment behaviour.
	describe('catalog enrichment (CC.1 / CC.2 / CC.3)', () => {
		let catalogDir: string;

		beforeEach(() => {
			catalogDir = createTmpDir();
			// Schema validator state is process-global (see catalog.ts
			// schemaCache). Reset so a prior test-file's schema-error state
			// cannot leak into CC tests.
			_resetSchemaCacheForTests();
		});

		afterEach(() => {
			cleanupTmpDir(catalogDir);
		});

		function writeCatalog(
			servers: Array<Record<string, unknown>>,
			commands: Array<Record<string, unknown>>,
		): void {
			fs.writeFileSync(
				path.join(catalogDir, 'servers.json'),
				JSON.stringify(servers),
				'utf8',
			);
			fs.writeFileSync(
				path.join(catalogDir, 'commands.json'),
				JSON.stringify(commands),
				'utf8',
			);
			mockConfigValues['mcpGateway.catalogPath'] = catalogDir;
		}

		it('CC.1: enriched template is written when the commands catalog has a matching entry', async () => {
			writeCatalog(
				[{
					name: 'alpha',
					display_name: 'Alpha',
					transport: 'http',
					description: 'alpha server',
					url: 'http://localhost:9901',
				}],
				[{
					server_name: 'alpha',
					command_name: 'cmd',
					description: 'alpha cmd',
					template_md: '# /alpha-cmd\n\nServer: ${server_name} at ${server_url}\n',
				}],
			);

			gen.enable();
			fireRefresh(makePayload([makeServer('alpha', 'stopped')]));
			fireRefresh(makePayload([makeServer('alpha', 'running')]));
			await drain();

			const content = fs.readFileSync(path.join(tmpDir, 'alpha.md'), 'utf8');
			assert.equal(content.split('\n')[0], MARKER, 'marker must remain on line 1');
			assert.ok(content.includes('# /alpha-cmd'), 'enriched heading expected');
			assert.ok(
				content.includes('Server: alpha at http://localhost:9901'),
				'both allow-list variables must be substituted',
			);
			assert.ok(!content.includes('**Status:**'), 'skeleton fields must not appear on enriched write');
		});

		it('CC.1: skeleton fallback when the commands catalog has no entry for the server', async () => {
			// Catalog loads successfully but contains no entry for "beta" —
			// loader warnings-free, schema-valid, just no match.
			writeCatalog(
				[{
					name: 'other',
					display_name: 'Other',
					transport: 'http',
					description: 'unrelated',
					url: 'http://localhost:9999',
				}],
				[{
					server_name: 'other',
					command_name: 'misc',
					description: 'unrelated',
					template_md: 'nothing\n',
				}],
			);

			gen.enable();
			fireRefresh(makePayload([makeServer('beta', 'stopped')]));
			fireRefresh(makePayload([makeServer('beta', 'running', [{ name: 'probe' }])]));
			await drain();

			const content = fs.readFileSync(path.join(tmpDir, 'beta.md'), 'utf8');
			assert.equal(content.split('\n')[0], MARKER);
			assert.ok(content.includes('# beta'), 'skeleton heading expected');
			assert.ok(content.includes('**Status:** running'));
			assert.ok(content.includes('**Transport:** stdio'));
			assert.ok(content.includes('- probe'), 'tools list from skeleton expected');
		});

		it('CC.2: allow-list substitution — only ${server_name} and ${server_url} replaced, unknown tokens literal', async () => {
			writeCatalog(
				[{
					name: 'gamma',
					display_name: 'Gamma',
					transport: 'http',
					description: 'gamma',
					url: 'http://localhost:9090',
				}],
				[{
					server_name: 'gamma',
					command_name: 'cmd',
					description: 'gamma cmd',
					template_md: 'Name: ${server_name}\nURL: ${server_url}\nOther: ${unknown_var}\nRepeat: ${server_name}/${server_name}\n',
				}],
			);

			gen.enable();
			fireRefresh(makePayload([makeServer('gamma', 'stopped')]));
			fireRefresh(makePayload([makeServer('gamma', 'running')]));
			await drain();

			const content = fs.readFileSync(path.join(tmpDir, 'gamma.md'), 'utf8');
			assert.ok(content.includes('Name: gamma'), 'known var server_name must be substituted');
			assert.ok(content.includes('URL: http://localhost:9090'), 'known var server_url must be substituted');
			assert.ok(content.includes('Other: ${unknown_var}'), 'unknown var must survive verbatim');
			assert.ok(content.includes('Repeat: gamma/gamma'), 'replaceAll must substitute every occurrence');
		});

		it('CC.3: user-authored file without marker is preserved even when the catalog has an entry for that server', async () => {
			writeCatalog(
				[{
					name: 'delta',
					display_name: 'Delta',
					transport: 'http',
					description: 'delta',
					url: 'http://localhost:9',
				}],
				[{
					server_name: 'delta',
					command_name: 'cmd',
					description: 'd',
					template_md: '# ENRICHED — should never appear\n',
				}],
			);

			const filePath = path.join(tmpDir, 'delta.md');
			fs.writeFileSync(filePath, '# User-authored\nDo not overwrite.\n', 'utf8');

			gen.enable();
			fireRefresh(makePayload([makeServer('delta', 'stopped')]));
			fireRefresh(makePayload([makeServer('delta', 'running')]));
			await drain();

			const content = fs.readFileSync(filePath, 'utf8');
			assert.ok(content.includes('User-authored'),
				'user file must be preserved — marker gate must run BEFORE catalog lookup');
			assert.ok(!content.includes('ENRICHED'),
				'enriched template must not leak past the isOwnedFile gate');
		});

		it('CC.1: entry removed from catalog + cache invalidated → next regeneration falls back to skeleton', async () => {
			writeCatalog(
				[{
					name: 'epsilon',
					display_name: 'Epsilon',
					transport: 'http',
					description: 'epsilon',
					url: 'http://localhost:9091',
				}],
				[{
					server_name: 'epsilon',
					command_name: 'cmd',
					description: 'e cmd',
					template_md: '# /epsilon-cmd\nFirst write — enriched.\n',
				}],
			);

			gen.enable();
			fireRefresh(makePayload([makeServer('epsilon', 'stopped')]));
			fireRefresh(makePayload([makeServer('epsilon', 'running')]));
			await drain();

			const filePath = path.join(tmpDir, 'epsilon.md');
			assert.ok(
				fs.readFileSync(filePath, 'utf8').includes('/epsilon-cmd'),
				'first write must be enriched from the catalog entry',
			);

			// Operator removes the entry from the catalog and (in real life)
			// the config-change watcher invalidates the cache. We simulate
			// both sides here: rewrite commands.json, then explicitly
			// invalidate so the next transition sees fresh state.
			fs.writeFileSync(path.join(catalogDir, 'commands.json'), JSON.stringify([]), 'utf8');
			gen.invalidateCatalogCache();

			fireRefresh(makePayload([makeServer('epsilon', 'stopped')]));
			fireRefresh(makePayload([makeServer('epsilon', 'running', [{ name: 'probe' }])]));
			await drain();

			const second = fs.readFileSync(filePath, 'utf8');
			assert.ok(
				second.includes('**Status:** running'),
				'skeleton must be emitted on regeneration once the catalog entry is gone',
			);
			assert.ok(!second.includes('/epsilon-cmd'),
				'enriched body must be replaced, not appended to');
		});

		it('CC.1: onDidChangeConfiguration(mcpGateway.catalogPath) invalidates the catalog cache', async () => {
			writeCatalog(
				[{
					name: 'zeta',
					display_name: 'Zeta',
					transport: 'http',
					description: 'zeta',
					url: 'http://localhost:9092',
				}],
				[{
					server_name: 'zeta',
					command_name: 'cmd',
					description: 'zeta cmd',
					template_md: '# /first-body\n',
				}],
			);

			gen.enable();
			fireRefresh(makePayload([makeServer('zeta', 'stopped')]));
			fireRefresh(makePayload([makeServer('zeta', 'running')]));
			await drain();

			const filePath = path.join(tmpDir, 'zeta.md');
			assert.ok(fs.readFileSync(filePath, 'utf8').includes('/first-body'));

			// Swap the catalog body and fire the config-change event. The
			// watcher registered in enable() must call
			// invalidateCatalogCache() so the next regeneration sees the new
			// template.
			fs.writeFileSync(
				path.join(catalogDir, 'commands.json'),
				JSON.stringify([{
					server_name: 'zeta',
					command_name: 'cmd',
					description: 'zeta cmd',
					template_md: '# /second-body\n',
				}]),
				'utf8',
			);
			fireConfigChange('mcpGateway.catalogPath');

			fireRefresh(makePayload([makeServer('zeta', 'stopped')]));
			fireRefresh(makePayload([makeServer('zeta', 'running')]));
			await drain();

			const after = fs.readFileSync(filePath, 'utf8');
			assert.ok(after.includes('/second-body'),
				'regeneration after config-change must pick up the new catalog body');
			assert.ok(!after.includes('/first-body'),
				'old catalog body must be replaced, not appended');
		});

		it('CC.2: empty ${server_url} for servers without a matching servers.json entry', async () => {
			// Commands catalog has an entry for "eta" but servers catalog
			// doesn't — loader drift that the cross-ref script would catch
			// in CI. The generator must not crash; ${server_url} substitutes
			// to the empty string so the rendered file is still valid
			// Markdown, while ${server_name} still resolves.
			writeCatalog(
				[],
				[{
					server_name: 'eta',
					command_name: 'cmd',
					description: 'eta cmd',
					template_md: 'Name: ${server_name}\nURL: [${server_url}]\n',
				}],
			);

			gen.enable();
			fireRefresh(makePayload([makeServer('eta', 'stopped')]));
			fireRefresh(makePayload([makeServer('eta', 'running')]));
			await drain();

			const content = fs.readFileSync(path.join(tmpDir, 'eta.md'), 'utf8');
			assert.ok(content.includes('Name: eta'));
			assert.ok(content.includes('URL: []'),
				'missing servers.json entry should substitute server_url to empty string, not leave literal token');
		});
	});
});
