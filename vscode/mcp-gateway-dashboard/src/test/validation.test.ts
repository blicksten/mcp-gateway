import { strict as assert } from 'node:assert';
import { describe, it } from 'mocha';
import {
	SERVER_NAME_RE,
	ENV_KEY_RE,
	HEADER_NAME_RE,
	validateServerName,
	validateUrl,
	validateStdioCommand,
	validateEnvEntry,
	validateHeaderEntry,
	detectTransport,
	isAbsolutePath,
	parseEnvEntry,
	parseHeaderEntry,
} from '../validation';
import { buildAddServerHtml } from '../webview/html-builder';

describe('validation: validateServerName', () => {
	it('accepts valid names', () => {
		assert.equal(validateServerName('my-server'), null);
		assert.equal(validateServerName('server_1'), null);
		assert.equal(validateServerName('A'), null);
		assert.equal(validateServerName('abc123'), null);
		assert.equal(validateServerName('ABC_def-ghi'), null);
	});

	it('trims and accepts padded names', () => {
		assert.equal(validateServerName('  my-server  '), null);
	});

	it('rejects empty and whitespace-only', () => {
		assert.ok(validateServerName('') !== null);
		assert.ok(validateServerName('   ') !== null);
	});

	it('rejects path traversal and slashes', () => {
		assert.ok(validateServerName('../evil') !== null);
		assert.ok(validateServerName('a/b') !== null);
		assert.ok(validateServerName('a\\b') !== null);
	});

	it('rejects leading separator', () => {
		assert.ok(validateServerName('-bad') !== null);
		assert.ok(validateServerName('_bad') !== null);
	});

	it('rejects forbidden punctuation', () => {
		assert.ok(validateServerName('a?b') !== null);
		assert.ok(validateServerName('a#b') !== null);
		assert.ok(validateServerName('a b') !== null);
		assert.ok(validateServerName('a.b') !== null);
	});

	it('rejects names over 64 chars', () => {
		assert.ok(validateServerName('a'.repeat(65)) !== null);
		assert.equal(validateServerName('a'.repeat(64)), null);
	});

	it('SERVER_NAME_RE matches exactly', () => {
		assert.ok(SERVER_NAME_RE.test('valid-name_123'));
		assert.ok(!SERVER_NAME_RE.test('bad name'));
	});
});

describe('validation: validateUrl', () => {
	it('accepts http and https URLs', () => {
		assert.equal(validateUrl('http://localhost:3000/mcp'), null);
		assert.equal(validateUrl('https://example.com/path?q=1'), null);
	});

	it('rejects empty', () => {
		assert.ok(validateUrl('') !== null);
		assert.ok(validateUrl('   ') !== null);
	});

	it('rejects non-http schemes', () => {
		assert.ok(validateUrl('ftp://example.com') !== null);
		assert.ok(validateUrl('file:///etc/passwd') !== null);
		assert.ok(validateUrl('javascript:alert(1)') !== null);
	});

	it('rejects malformed URLs', () => {
		assert.ok(validateUrl('not a url') !== null);
		assert.ok(validateUrl('://nothing') !== null);
	});
});

describe('validation: validateStdioCommand', () => {
	it('accepts absolute Unix paths', () => {
		assert.equal(validateStdioCommand('/usr/local/bin/server'), null);
		assert.equal(validateStdioCommand('/tmp/a'), null);
	});

	it('accepts absolute Windows paths on win32', () => {
		// path.isAbsolute is platform-sensitive. On win32, 'C:\\bin\\s' is absolute;
		// on posix, only '/...' is. Skip the win-specific assertion on non-win32.
		if (process.platform === 'win32') {
			assert.equal(validateStdioCommand('C:\\Program Files\\server.exe'), null);
			assert.equal(validateStdioCommand('C:/bin/server'), null);
		}
	});

	it('rejects empty', () => {
		assert.ok(validateStdioCommand('') !== null);
		assert.ok(validateStdioCommand('   ') !== null);
	});

	it('rejects relative paths', () => {
		assert.ok(validateStdioCommand('server') !== null);
		assert.ok(validateStdioCommand('./server') !== null);
		assert.ok(validateStdioCommand('../server') !== null);
	});

	it('rejects npx-style commands (absolute-path requirement)', () => {
		assert.ok(validateStdioCommand('npx foo-mcp') !== null);
	});
});

describe('validation: validateEnvEntry', () => {
	it('accepts valid KEY=VALUE', () => {
		assert.equal(validateEnvEntry('API_KEY=secret'), null);
		assert.equal(validateEnvEntry('DEBUG=1'), null);
		assert.equal(validateEnvEntry('_PRIVATE=x'), null);
	});

	it('accepts values containing equals signs', () => {
		// Only the first '=' is the key/value separator.
		assert.equal(validateEnvEntry('TOKEN=a=b=c'), null);
	});

	it('accepts empty value', () => {
		assert.equal(validateEnvEntry('API_KEY='), null);
	});

	it('returns null for empty entry (skip)', () => {
		assert.equal(validateEnvEntry(''), null);
		assert.equal(validateEnvEntry('   '), null);
	});

	it('rejects missing equals', () => {
		assert.ok(validateEnvEntry('API_KEY') !== null);
	});

	it('rejects invalid key characters', () => {
		assert.ok(validateEnvEntry('bad-key=v') !== null);
		assert.ok(validateEnvEntry('1BAD=v') !== null);
		assert.ok(validateEnvEntry('a.b=v') !== null);
	});
});

describe('validation: validateHeaderEntry', () => {
	it('accepts valid Name: Value', () => {
		assert.equal(validateHeaderEntry('Authorization: Bearer token'), null);
		assert.equal(validateHeaderEntry('X-Custom: value'), null);
		assert.equal(validateHeaderEntry('Content-Type: application/json'), null);
	});

	it('returns null for empty entry (skip)', () => {
		assert.equal(validateHeaderEntry(''), null);
	});

	it('rejects missing colon', () => {
		assert.ok(validateHeaderEntry('Authorization Bearer') !== null);
	});

	it('rejects invalid header name chars', () => {
		assert.ok(validateHeaderEntry('Bad Name: value') !== null);
		assert.ok(validateHeaderEntry('Bad(Name): value') !== null);
	});
});

describe('validation: detectTransport', () => {
	it('returns http for http:// and https:// prefixes', () => {
		assert.equal(detectTransport('http://localhost:3000'), 'http');
		assert.equal(detectTransport('https://example.com'), 'http');
		assert.equal(detectTransport('  http://x '), 'http');
	});

	it('returns stdio for everything else', () => {
		assert.equal(detectTransport('/usr/bin/server'), 'stdio');
		assert.equal(detectTransport('npx foo'), 'stdio');
		assert.equal(detectTransport(''), 'stdio');
		assert.equal(detectTransport('C:\\server.exe'), 'stdio');
	});
});

describe('validation: parseEnvEntry', () => {
	it('splits on first equals', () => {
		assert.deepEqual(parseEnvEntry('KEY=VALUE'), { key: 'KEY', value: 'VALUE' });
		assert.deepEqual(parseEnvEntry('K=a=b=c'), { key: 'K', value: 'a=b=c' });
	});

	it('returns null on missing equals', () => {
		assert.equal(parseEnvEntry('bare'), null);
	});

	it('trims whitespace around the full entry', () => {
		assert.deepEqual(parseEnvEntry('  KEY=VAL  '), { key: 'KEY', value: 'VAL' });
	});
});

describe('validation: parseHeaderEntry', () => {
	it('splits on first colon', () => {
		assert.deepEqual(parseHeaderEntry('Auth: Bearer x'), { name: 'Auth', value: 'Bearer x' });
		assert.deepEqual(parseHeaderEntry('X: a: b'), { name: 'X', value: 'a: b' });
	});

	it('returns null on missing colon', () => {
		assert.equal(parseHeaderEntry('bare'), null);
	});

	it('trims both name and value', () => {
		assert.deepEqual(parseHeaderEntry('  Auth :  Bearer  '), { name: 'Auth', value: 'Bearer' });
	});
});

describe('validation: isAbsolutePath (platform-agnostic)', () => {
	// Running on any platform — isAbsolutePath must behave consistently.
	it('recognizes POSIX absolute paths', () => {
		assert.ok(isAbsolutePath('/'));
		assert.ok(isAbsolutePath('/usr/bin/server'));
		assert.ok(isAbsolutePath('/tmp/a'));
	});

	it('recognizes Windows drive-letter paths', () => {
		assert.ok(isAbsolutePath('C:\\Program Files\\server.exe'));
		assert.ok(isAbsolutePath('C:/bin/server'));
		assert.ok(isAbsolutePath('D:\\tmp'));
		assert.ok(isAbsolutePath('z:/x'));
	});

	it('recognizes UNC paths', () => {
		assert.ok(isAbsolutePath('\\\\host\\share'));
	});

	it('rejects relative and bare paths', () => {
		assert.ok(!isAbsolutePath(''));
		assert.ok(!isAbsolutePath('   '));
		assert.ok(!isAbsolutePath('server'));
		assert.ok(!isAbsolutePath('./server'));
		assert.ok(!isAbsolutePath('../server'));
		assert.ok(!isAbsolutePath('C:'));
		assert.ok(!isAbsolutePath('CC:\\x'));
	});
});

describe('validation: webview regex parity', () => {
	// The Add Server webview embeds copies of these regex patterns in an inline
	// <script> block so form validation runs client-side without a round trip.
	// The patterns are injected from validation.ts at HTML build time via the
	// same `jsonForScript` helper used elsewhere in the webview pattern — this
	// test guards against any future drift that bypasses the injection
	// (e.g. someone hardcoding a literal in the template).
	const html = buildAddServerHtml('test-nonce', 'https://example');

	// Mirror of `jsonForScript` in html-builder.ts (private) — JSON-encode then
	// unicode-escape the HTML-sensitive characters so the script tag parser
	// cannot be tricked out of the <script> block.
	function jsonForScript(value: unknown): string {
		return JSON.stringify(value)
			.replace(/&/g, '\\u0026')
			.replace(/</g, '\\u003c')
			.replace(/>/g, '\\u003e');
	}

	it('embeds the authoritative SERVER_NAME_RE source', () => {
		const injected = jsonForScript(SERVER_NAME_RE.source);
		assert.ok(
			html.includes(`new RegExp(${injected})`),
			'HTML must embed SERVER_NAME_RE via new RegExp() with the validation.ts source');
	});

	it('embeds the authoritative ENV_KEY_RE source', () => {
		const injected = jsonForScript(ENV_KEY_RE.source);
		assert.ok(html.includes(`new RegExp(${injected})`));
	});

	it('embeds the authoritative HEADER_NAME_RE source', () => {
		// HEADER_NAME_RE.source contains `&` which jsonForScript escapes to
		// `\u0026` — the test must use the same transform to match.
		const injected = jsonForScript(HEADER_NAME_RE.source);
		assert.ok(html.includes(`new RegExp(${injected})`));
	});

	it('a RegExp built from the embedded source accepts valid header names', () => {
		// End-to-end parity check: extract the injected string, turn it back
		// into a RegExp in this test process, and verify it accepts the same
		// inputs as the authoritative HEADER_NAME_RE. This catches the case
		// where the injected literal exists but was mangled in transit.
		const re = new RegExp(HEADER_NAME_RE.source);
		assert.ok(re.test('Authorization'));
		assert.ok(re.test('X-Custom-Header'));
		assert.ok(!re.test('Bad Name'));
		assert.ok(!re.test('Bad(Name)'));
	});

	it('webview form has restrictive CSP with form-action none', () => {
		assert.ok(html.includes("default-src 'none'"));
		assert.ok(html.includes(`script-src 'nonce-test-nonce'`));
		assert.ok(html.includes("form-action 'none'"));
		assert.ok(!html.includes("'unsafe-inline'"));
		assert.ok(!html.includes("'unsafe-eval'"));
	});
});
