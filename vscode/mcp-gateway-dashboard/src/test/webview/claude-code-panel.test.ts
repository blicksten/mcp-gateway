// Phase 4B — ClaudeCodePanel Activate-via-child-process tests.
//
// Exercises the runInstallClaudeCode flow: line streaming via postMessage,
// SIGTERM on cancel + dispose, Bearer redaction, and the concurrent-click
// guard. The test injects a fake spawn factory so no real mcp-ctl binary
// is invoked.

import '../mock-vscode';
import { strict as assert } from 'node:assert';
import { EventEmitter } from 'node:events';
import { PassThrough } from 'node:stream';
import { describe, it, beforeEach } from 'mocha';
import {
	resetMockState,
	mockWebviewPanels,
	mockCalls,
	type MockWebviewPanel,
} from '../mock-vscode';
import {
	ClaudeCodePanel,
	type ClaudeCodePanelDeps,
	type InstallChildHandle,
} from '../../webview/claude-code-panel';

/**
 * Minimal fake ChildProcess used by the tests. Implements only the subset
 * of ChildProcess that runInstallClaudeCode consumes: stdout/stderr streams,
 * close+error events, kill(). Extends EventEmitter so child.on('close', …)
 * and child.on('error', …) work as in production.
 */
class FakeChild extends EventEmitter implements InstallChildHandle {
	stdout = new PassThrough();
	stderr = new PassThrough();
	killed = false;
	lastSignal: NodeJS.Signals | number | undefined;

	kill(signal: NodeJS.Signals | number = 'SIGTERM'): boolean {
		if (this.killed) { return false; }
		this.killed = true;
		this.lastSignal = signal;
		// Simulate the OS tearing down the child — stdout/stderr end,
		// then close fires with SIGTERM's standard exit code. Second
		// arg is the signal name, matching Node's real ChildProcess.
		setImmediate(() => {
			this.stdout.end();
			this.stderr.end();
			this.emit('close', 143, typeof signal === 'string' ? signal : null);
		});
		return true;
	}

	/** Test helper — drive a normal close with an exit code. */
	finish(code: number): void {
		setImmediate(() => {
			this.stdout.end();
			this.stderr.end();
			this.emit('close', code, null);
		});
	}

	/**
	 * Test helper — simulate an external signal kill (OS OOM killer,
	 * Task Manager, `kill -9`). Node fires `'close'` with `(null, signal)`
	 * in this case.
	 */
	externalKill(signal: NodeJS.Signals = 'SIGKILL'): void {
		this.killed = true;
		this.lastSignal = signal;
		setImmediate(() => {
			this.stdout.end();
			this.stderr.end();
			this.emit('close', null, signal);
		});
	}

	/** Test helper — drive an error event (e.g. ENOENT). */
	fail(err: Error): void {
		setImmediate(() => this.emit('error', err));
	}
}

function makeDeps(overrides: Partial<ClaudeCodePanelDeps> = {}): ClaudeCodePanelDeps {
	return {
		extensionUri: {
			scheme: 'file',
			path: '/t',
			fsPath: '/t',
			with: () => ({}),
			toString: () => 'file:///t',
		} as unknown as ClaudeCodePanelDeps['extensionUri'],
		extensionPath: '/t',
		getGatewayUrl: () => 'http://localhost:8765',
		getAuthToken: () => undefined,
		getTokenPath: () => '/t/auth.token',
		fetch: (async () => ({
			ok: true,
			status: 200,
			json: async () => ({}),
		})) as unknown as typeof fetch,
		getMcpCtlPath: () => 'mcp-ctl',
		...overrides,
	};
}

function latestPanel(): MockWebviewPanel {
	assert.ok(mockWebviewPanels.length > 0, 'expected a webview panel');
	return mockWebviewPanels[mockWebviewPanels.length - 1];
}

function postedOfKind(panel: MockWebviewPanel, kind: string): Array<Record<string, unknown>> {
	return (panel._postedMessages as Array<Record<string, unknown>>).filter(
		(m) => m.kind === kind,
	);
}

async function flush(ticks: number = 4): Promise<void> {
	for (let i = 0; i < ticks; i++) {
		await new Promise((r) => setImmediate(r));
	}
}

describe('ClaudeCodePanel — Activate install flow (Phase 4B)', () => {
	let child: FakeChild;

	beforeEach(() => {
		resetMockState();
		mockWebviewPanels.length = 0;
		ClaudeCodePanel._resetForTests();
		child = new FakeChild();
	});

	it('streams stdout lines as activate-log postMessages and finishes with activate-done', async () => {
		const deps = makeDeps({ spawnInstall: () => child });
		ClaudeCodePanel.createOrShow(deps);
		const panel = latestPanel();

		panel.webview._simulateMessage({ command: 'activate' });
		await flush();
		child.stdout.write('starting install\n');
		child.stdout.write('patch applied\n');
		child.finish(0);
		await flush(8);

		const logs = postedOfKind(panel, 'activate-log').map((m) => m.line);
		assert.deepStrictEqual(logs, ['starting install', 'patch applied']);
		const done = postedOfKind(panel, 'activate-done');
		assert.strictEqual(done.length, 1);
		assert.strictEqual(done[0].exitCode, 0);
		assert.ok(
			mockCalls.infoMessages.some((m) => m.includes('installed')),
			'expected success info toast',
		);
	});

	it('Activate argv passes --api-url with the configured gateway URL (B-NEW-23)', async () => {
		let capturedArgv: string[] | undefined;
		const deps = makeDeps({
			getGatewayUrl: () => 'http://custom-host:9001',
			spawnInstall: (_mcpCtlPath, argv) => {
				capturedArgv = argv;
				return child;
			},
		});
		ClaudeCodePanel.createOrShow(deps);
		const panel = latestPanel();
		panel.webview._simulateMessage({ command: 'activate' });
		await flush();
		child.finish(0);
		await flush(8);
		assert.ok(capturedArgv, 'spawnInstall was not called');
		assert.ok(
			capturedArgv.includes('install-claude-code'),
			`install-claude-code subcommand missing from argv: ${capturedArgv.join(' ')}`,
		);
		const apiUrlIdx = capturedArgv.indexOf('--api-url');
		assert.ok(apiUrlIdx !== -1, `--api-url flag missing from argv: ${capturedArgv.join(' ')}`);
		assert.strictEqual(
			capturedArgv[apiUrlIdx + 1],
			'http://custom-host:9001',
			`--api-url value mismatch: ${capturedArgv.join(' ')}`,
		);
	});

	it('redacts Bearer tokens before posting install log lines to the webview', async () => {
		const deps = makeDeps({ spawnInstall: () => child });
		ClaudeCodePanel.createOrShow(deps);
		const panel = latestPanel();

		panel.webview._simulateMessage({ command: 'activate' });
		await flush();
		const secret = 'AbCdEfGhIj1234567890ZZZZ';
		child.stdout.write(`sending Authorization: Bearer ${secret} to gateway\n`);
		child.finish(0);
		await flush(8);

		const logs = postedOfKind(panel, 'activate-log').map((m) => String(m.line));
		assert.strictEqual(logs.length, 1);
		assert.ok(!logs[0].includes(secret), `secret leaked: ${logs[0]}`);
		assert.ok(logs[0].includes('[REDACTED]'), `no redaction marker: ${logs[0]}`);
	});

	it('cancel message kills child with SIGTERM, posts canceled=true, shows info toast (no error toast)', async () => {
		const deps = makeDeps({ spawnInstall: () => child });
		ClaudeCodePanel.createOrShow(deps);
		const panel = latestPanel();

		panel.webview._simulateMessage({ command: 'activate' });
		await flush();
		assert.strictEqual(child.killed, false);

		panel.webview._simulateMessage({ command: 'activateCancel' });
		await flush(8);

		assert.strictEqual(child.killed, true);
		assert.strictEqual(child.lastSignal, 'SIGTERM');
		// After kill → simulated close(143) fires → activate-done is posted
		// with canceled=true so the webview can render "cancelled" instead
		// of "failed".
		const done = postedOfKind(panel, 'activate-done');
		assert.strictEqual(done.length, 1);
		assert.strictEqual(done[0].exitCode, 143);
		assert.strictEqual(done[0].canceled, true);
		// User-initiated cancel must NOT show a red "install failed" toast.
		assert.strictEqual(
			mockCalls.errorMessages.length,
			0,
			`no error toast expected on cancel, got: ${mockCalls.errorMessages.join(' | ')}`,
		);
		// …it should instead show a neutral "Install cancelled" info toast.
		assert.ok(
			mockCalls.infoMessages.some((m) => m.toLowerCase().includes('cancel')),
			`expected an "Install cancelled" info toast, got: ${mockCalls.infoMessages.join(' | ')}`,
		);
	});

	it('panel dispose during an install kills the child and emits no toasts', async () => {
		const deps = makeDeps({ spawnInstall: () => child });
		ClaudeCodePanel.createOrShow(deps);
		const panel = latestPanel();

		panel.webview._simulateMessage({ command: 'activate' });
		await flush();
		assert.strictEqual(child.killed, false);

		// Simulate VSCode closing the panel — the onDidDispose handler
		// registered by ClaudeCodePanel fires, which triggers the Phase 4B
		// kill path. The close handler then runs finish() *after* the panel
		// is disposed; finish() must short-circuit toasts in that case so
		// the user isn't greeted with "install cancelled" / "install failed"
		// popups for a panel they just closed.
		panel.dispose();
		await flush(8);

		assert.strictEqual(child.killed, true);
		assert.strictEqual(child.lastSignal, 'SIGTERM');
		assert.strictEqual(mockCalls.errorMessages.length, 0, 'no error toast after dispose');
		// The success toast from any earlier run should also not appear.
		assert.ok(
			!mockCalls.infoMessages.some((m) => m.toLowerCase().includes('cancel')),
			'no cancelled toast after dispose',
		);
	});

	it('external SIGKILL (exitCode=null, not user-cancelled) surfaces an error toast with the signal name', async () => {
		const deps = makeDeps({ spawnInstall: () => child });
		ClaudeCodePanel.createOrShow(deps);
		const panel = latestPanel();

		panel.webview._simulateMessage({ command: 'activate' });
		await flush();
		// Simulate OS OOM killer / Task Manager / kill -9 — Node fires
		// close with (null, 'SIGKILL'). No user Cancel was pressed, so
		// installCanceled stays false and the user MUST see an error.
		child.externalKill('SIGKILL');
		await flush(8);

		const done = postedOfKind(panel, 'activate-done');
		assert.strictEqual(done.length, 1);
		assert.strictEqual(done[0].exitCode, null);
		assert.strictEqual(done[0].signal, 'SIGKILL');
		assert.strictEqual(done[0].canceled, false);
		assert.strictEqual(
			mockCalls.errorMessages.length,
			1,
			`expected 1 error toast, got: ${mockCalls.errorMessages.join(' | ')}`,
		);
		assert.ok(
			mockCalls.errorMessages[0].includes('SIGKILL'),
			`error toast should reference the signal: ${mockCalls.errorMessages[0]}`,
		);
		assert.strictEqual(
			mockCalls.infoMessages.length,
			0,
			'external kill must not show an info toast',
		);
	});

	it('rejects a second Activate click while an install is in progress', async () => {
		let spawnCount = 0;
		const deps = makeDeps({
			spawnInstall: () => {
				spawnCount++;
				return child;
			},
		});
		ClaudeCodePanel.createOrShow(deps);
		const panel = latestPanel();

		panel.webview._simulateMessage({ command: 'activate' });
		await flush();
		assert.strictEqual(spawnCount, 1);

		panel.webview._simulateMessage({ command: 'activate' });
		await flush();
		assert.strictEqual(spawnCount, 1, 'second click must not spawn another child');
		assert.ok(
			mockCalls.warningMessages.some((m) => m.toLowerCase().includes('already in progress')),
			'expected an "already in progress" warning toast',
		);

		// Let the first install complete cleanly so no child is left running.
		child.finish(0);
		await flush(8);
	});
});

// --------------------------------------------------------------------------
// Phase 1 — facts-updated message tests
// --------------------------------------------------------------------------

/** A fetch that returns empty arrays for both gateway endpoints (no heartbeats, no matrix). */
function makeEmptyGatewayFetch(): ClaudeCodePanelDeps['fetch'] {
	return (async (url: string) => {
		const u = String(url);
		if (u.includes('compat-matrix')) {
			return { ok: false, status: 503, json: async () => null };
		}
		// patch-status returns empty heartbeat array
		return { ok: true, status: 200, json: async () => [] };
	}) as unknown as typeof fetch;
}

describe('ClaudeCodePanel — facts-updated postMessage (Phase 1)', () => {
	beforeEach(() => {
		resetMockState();
		mockWebviewPanels.length = 0;
		ClaudeCodePanel._resetForTests();
	});

	it('posts facts-updated with pluginInstalled=true and version when detectPlugin returns installed', async () => {
		const deps = makeDeps({
			fetch: makeEmptyGatewayFetch(),
			detectPlugin: async () => ({ installed: true, version: '3.1.4', marketplace: 'mcp-gateway-local' }),
			detectPatch: async () => ({ installed: false }),
		});
		ClaudeCodePanel.createOrShow(deps);
		const panel = latestPanel();

		// Wait for the initial poll to complete (fetch + gatherFacts + postMessage).
		await flush(16);

		const updates = postedOfKind(panel, 'facts-updated');
		assert.ok(updates.length > 0, 'expected at least one facts-updated message');
		const last = updates[updates.length - 1];
		assert.strictEqual(last['pluginInstalled'], true);
		assert.strictEqual(last['pluginVersion'], '3.1.4');
		assert.strictEqual(last['patchInstalled'], false);
	});

	it('posts facts-updated with patchStale=true when detectPatch reports a stale patch', async () => {
		const deps = makeDeps({
			fetch: makeEmptyGatewayFetch(),
			detectPlugin: async () => ({ installed: false }),
			detectPatch: async () => ({
				installed: true,
				stale: true,
				currentVersion: '1.0.0',
				latestVersion: '1.1.0',
			}),
		});
		ClaudeCodePanel.createOrShow(deps);
		await flush(16);

		const panel = latestPanel();
		const updates = postedOfKind(panel, 'facts-updated');
		assert.ok(updates.length > 0, 'expected at least one facts-updated message');
		const last = updates[updates.length - 1];
		assert.strictEqual(last['patchInstalled'], true);
		assert.strictEqual(last['patchStale'], true);
		assert.strictEqual(last['patchCurrentVersion'], '1.0.0');
		assert.strictEqual(last['patchLatestVersion'], '1.1.0');
	});
});
