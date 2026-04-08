import '../mock-vscode';
import { strict as assert } from 'node:assert';
import { escapeHtml, buildMcpDetailHtml, buildSapDetailHtml } from '../../webview/html-builder';
import type { ServerView } from '../../types';
import type { SapSystem } from '../../sap-detector';

describe('escapeHtml', () => {
	it('escapes < and >', () => {
		assert.equal(escapeHtml('<script>'), '&lt;script&gt;');
	});

	it('escapes &', () => {
		assert.equal(escapeHtml('a&b'), 'a&amp;b');
	});

	it('escapes double quotes', () => {
		assert.equal(escapeHtml('"onclick"'), '&quot;onclick&quot;');
	});

	it('escapes single quotes', () => {
		assert.equal(escapeHtml("it's"), 'it&#39;s');
	});

	it('escapes combined XSS payload', () => {
		assert.equal(
			escapeHtml('<img onerror="alert(1)">'),
			'&lt;img onerror=&quot;alert(1)&quot;&gt;',
		);
	});

	it('preserves safe text', () => {
		assert.equal(escapeHtml('hello world 123'), 'hello world 123');
	});

	it('handles empty string', () => {
		assert.equal(escapeHtml(''), '');
	});
});

describe('buildMcpDetailHtml', () => {
	const server: ServerView = {
		name: 'my-server',
		status: 'running',
		transport: 'stdio',
		pid: 1234,
		restart_count: 2,
		last_error: 'connection reset',
		tools: [
			{ name: 'tool-one', description: 'First tool', server: 'my-server' },
			{ name: 'tool-two', description: 'Second tool', server: 'my-server' },
		],
	};

	const nonce = 'dGVzdG5vbmNlMTIzNDU2'; // base64-like test value
	const cspSource = 'https://webview.test.source';

	function buildDefault(overrides: Partial<typeof server> = {}): string {
		return buildMcpDetailHtml({
			server: { ...server, ...overrides },
			credentialKeys: { env: ['API_KEY', 'SECRET'], headers: ['Authorization'] },
			nonce,
			cspSource,
		});
	}

	it('includes CSP meta tag without unsafe-inline', () => {
		const html = buildDefault();
		assert.ok(html.includes('Content-Security-Policy'));
		assert.ok(!html.includes("'unsafe-inline'"));
	});

	it('includes nonce in style tag', () => {
		const html = buildDefault();
		assert.ok(html.includes(`<style nonce="${nonce}">`));
	});

	it('includes nonce in script tag', () => {
		const html = buildDefault();
		assert.ok(html.includes(`<script nonce="${nonce}">`));
	});

	it('nonce in CSP matches nonce in tags character-for-character', () => {
		const html = buildDefault();
		const cspMatch = html.match(/nonce-([^']+)'/);
		const styleMatch = html.match(/<style nonce="([^"]+)">/);
		const scriptMatch = html.match(/<script nonce="([^"]+)">/);
		assert.ok(cspMatch);
		assert.ok(styleMatch);
		assert.ok(scriptMatch);
		assert.equal(cspMatch![1], styleMatch![1]);
		assert.equal(cspMatch![1], scriptMatch![1]);
	});

	it('contains expected sections: tools, config, status', () => {
		const html = buildDefault();
		assert.ok(html.includes('Tools (2)'));
		assert.ok(html.includes('Configuration'));
		assert.ok(html.includes('running'));
	});

	it('escapes dynamic data (XSS in server name)', () => {
		const html = buildDefault({ name: '<img onerror="alert(1)">' });
		assert.ok(html.includes('&lt;img onerror='));
		assert.ok(!html.includes('<img onerror'));
	});

	it('escapes dynamic data in tool descriptions', () => {
		const html = buildMcpDetailHtml({
			server: {
				...server,
				tools: [{ name: 'xss', description: '<script>alert(1)</script>', server: 'x' }],
			},
			credentialKeys: { env: [], headers: [] },
			nonce,
			cspSource,
		});
		assert.ok(html.includes('&lt;script&gt;alert(1)&lt;/script&gt;'));
	});

	it('credential key names present, values always masked', () => {
		const html = buildDefault();
		assert.ok(html.includes('API_KEY'));
		assert.ok(html.includes('SECRET'));
		assert.ok(html.includes('Authorization'));
		// Verify values are masked, not actual secret values.
		const maskCount = html.split('********').length - 1;
		assert.equal(maskCount, 3); // 2 env + 1 header
	});

	it('hides credentials section when none exist', () => {
		const html = buildMcpDetailHtml({
			server,
			credentialKeys: { env: [], headers: [] },
			nonce,
			cspSource,
		});
		assert.ok(!html.includes('Credentials'));
	});

	it('includes PID when present', () => {
		const html = buildDefault();
		assert.ok(html.includes('1234'));
	});

	it('includes last_error when present', () => {
		const html = buildDefault();
		assert.ok(html.includes('connection reset'));
	});

	it('shows "No tools exposed" when no tools', () => {
		const html = buildDefault({ tools: [] });
		assert.ok(html.includes('No tools exposed'));
	});

	it('includes action buttons', () => {
		const html = buildDefault();
		assert.ok(html.includes('Restart'));
		assert.ok(html.includes('Reset Circuit'));
		assert.ok(html.includes('Show Logs'));
	});

	it('server name with </script> does not break out of script block', () => {
		const html = buildDefault({ name: '</script><script>alert(1)</script>' });
		// The literal </script> must not appear unescaped inside the script block
		assert.ok(html.includes('\\u003c/script\\u003e'));
		assert.ok(!html.includes('</script><script>alert'));
	});

	it('server name with & is escaped in script context', () => {
		const html = buildDefault({ name: 'foo&bar' });
		assert.ok(html.includes('\\u0026'));
	});

	it('credential key names with XSS payload are escaped', () => {
		const html = buildMcpDetailHtml({
			server,
			credentialKeys: { env: ['<script>alert(1)</script>'], headers: [] },
			nonce,
			cspSource,
		});
		assert.ok(!html.includes('<script>alert(1)'));
		assert.ok(html.includes('&lt;script&gt;'));
	});
});

describe('buildSapDetailHtml', () => {
	const system: SapSystem = {
		key: 'DEV-100',
		sid: 'DEV',
		client: '100',
		vsp: { name: 'vsp-DEV-100', status: 'running', transport: 'stdio', restart_count: 0, pid: 5678 },
		gui: { name: 'sap-gui-DEV-100', status: 'running', transport: 'http', restart_count: 1 },
		status: 'running',
	};

	const nonce = 'c2FwdGVzdG5vbmNl';
	const cspSource = 'https://webview.sap.source';

	function buildDefault(): string {
		return buildSapDetailHtml({
			system,
			vspCredentialKeys: { env: ['SAP_USER'], headers: [] },
			guiCredentialKeys: { env: [], headers: ['X-Auth'] },
			nonce,
			cspSource,
		});
	}

	it('includes CSP meta tag without unsafe-inline', () => {
		const html = buildDefault();
		assert.ok(html.includes('Content-Security-Policy'));
		assert.ok(!html.includes("'unsafe-inline'"));
	});

	it('contains both VSP and GUI component sections', () => {
		const html = buildDefault();
		assert.ok(html.includes('VSP'));
		assert.ok(html.includes('GUI'));
		assert.ok(html.includes('vsp-DEV-100'));
		assert.ok(html.includes('sap-gui-DEV-100'));
	});

	it('includes SAP system title with SID-Client', () => {
		const html = buildDefault();
		assert.ok(html.includes('SAP DEV-100'));
	});

	it('credential key names present, values always masked', () => {
		const html = buildDefault();
		assert.ok(html.includes('SAP_USER'));
		assert.ok(html.includes('X-Auth'));
		const maskCount = html.split('********').length - 1;
		assert.equal(maskCount, 2);
	});

	it('nonce in CSP matches nonce in tags', () => {
		const html = buildDefault();
		const cspMatch = html.match(/nonce-([^']+)'/);
		const scriptMatch = html.match(/<script nonce="([^"]+)">/);
		assert.ok(cspMatch);
		assert.ok(scriptMatch);
		assert.equal(cspMatch![1], scriptMatch![1]);
	});

	it('shows "Not configured" for missing GUI component', () => {
		const html = buildSapDetailHtml({
			system: { ...system, gui: undefined },
			vspCredentialKeys: { env: [], headers: [] },
			guiCredentialKeys: { env: [], headers: [] },
			nonce,
			cspSource,
		});
		assert.ok(html.includes('Not configured'));
	});

	it('escapes dynamic data in component names', () => {
		const html = buildSapDetailHtml({
			system: { ...system, vsp: { ...system.vsp!, name: '<script>x</script>' } },
			vspCredentialKeys: { env: [], headers: [] },
			guiCredentialKeys: { env: [], headers: [] },
			nonce,
			cspSource,
		});
		assert.ok(html.includes('&lt;script&gt;x&lt;/script&gt;'));
		assert.ok(!html.includes('<script>x</script>'));
	});

	it('includes action buttons for both components', () => {
		const html = buildDefault();
		assert.ok(html.includes('Restart VSP'));
		assert.ok(html.includes('Restart GUI'));
		assert.ok(html.includes('VSP Logs'));
		assert.ok(html.includes('GUI Logs'));
	});
});
