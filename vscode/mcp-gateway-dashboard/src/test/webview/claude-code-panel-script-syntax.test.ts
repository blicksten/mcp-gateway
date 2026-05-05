// Regression test for the 2026-05-06 webview-script bug:
// the inline <script> in ClaudeCodePanel lives inside an outer template
// literal in TS source. Single-escaped sequences like '\n' inside the
// script body get interpreted by the OUTER template literal and produce
// a literal newline in the rendered HTML — which makes the JS string
// literal unterminated, throws a SyntaxError on script load, and silently
// disables every event handler in the panel (buttons stop responding,
// status text never updates).
//
// This test renders the panel HTML, extracts the <script> body, and
// parses it with the standard JS parser. A failure here means the panel
// is broken in production even if all other unit tests pass.

import './../mock-vscode';
import { strict as assert } from 'node:assert';
import { ClaudeCodePanel } from '../../webview/claude-code-panel';

interface PanelInternals {
	render: () => void;
	panel: { webview: { html: string } };
	dispose: () => void;
}

describe('ClaudeCodePanel — webview script syntax (regression)', () => {
	it('renders a syntactically valid <script> body', () => {
		const ClaudeCodePanelAny = ClaudeCodePanel as unknown as {
			createOrShow: (deps: Record<string, unknown>) => PanelInternals;
			current?: PanelInternals;
		};
		// Reset singleton so this test is independent of order.
		ClaudeCodePanelAny.current = undefined;

		const fakeUri = { fsPath: 'C:/test', toString: () => 'file://test' };
		const instance = ClaudeCodePanelAny.createOrShow({
			extensionUri: fakeUri,
			extensionPath: 'C:/test',
			getGatewayUrl: () => 'http://localhost:8765',
			getAuthToken: () => undefined,
			getTokenPath: () => 'C:/test/token',
			fetch: (() => Promise.reject(new Error('test'))) as unknown,
			getMcpCtlPath: () => '',
			getGatewayVersion: () => undefined,
			getWorkspaceFolder: () => undefined,
			getMarketplaceJsonPath: () => undefined,
		});

		const html = instance.panel.webview.html;
		assert.ok(html.length > 0, 'panel must render non-empty HTML');

		const scriptMatch = html.match(/<script[^>]*>([\s\S]*?)<\/script>/);
		assert.ok(scriptMatch, 'panel HTML must contain a <script> tag');
		const body = scriptMatch[1];
		assert.ok(body.length > 0, 'script body must be non-empty');

		// new Function(body) compiles the script in the same way V8 would
		// when the webview loads. A SyntaxError here is the production bug.
		try {
			new Function(body);
		} catch (err) {
			const msg = err instanceof Error ? err.message : String(err);
			// Show ~80 chars around the first invalid byte so the failure
			// pinpoints the offending TS source line on next regression.
			const around = body.slice(0, 200).replace(/\n/g, '\\n');
			assert.fail(
				`webview <script> failed to parse: ${msg}. ` +
					`First 200 chars of body (newlines escaped): ${around}`,
			);
		}

		instance.dispose();
	});
});
