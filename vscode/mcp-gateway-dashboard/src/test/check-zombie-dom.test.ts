import { strict as assert } from 'node:assert';
import { describe, it } from 'mocha';
import * as fs from 'node:fs';
import * as path from 'node:path';

// Runtime import of a sibling TypeScript module — keeps ts-node's
// CommonJS extension hook engaged for this file. Without at least one
// runtime relative TS import, Node 22+ falls back to its native ESM
// type-stripping loader where `require` is undefined.
//
// Imports MIN_TOKEN_LEN (a number) and references it via `void` so the
// runtime side-effect is explicit and a future maintainer cannot remove
// the import as "unused" without re-introducing the ESM-fallback bug.
import { MIN_TOKEN_LEN } from '../auth-header';
void MIN_TOKEN_LEN;

const checker = require('../../scripts/check-zombie-dom.js') as {
	findHtmlIds: (s: string) => Set<string>;
	findScriptRefs: (s: string, ids: Set<string>) => Set<string>;
	stripJsComments: (s: string) => string;
	checkAll: (
		files?: string[],
		baseDir?: string,
	) => {
		hasErrors: boolean;
		results: {
			file: string;
			ids: Set<string>;
			refs: Set<string>;
			errors: string[];
			allowedUnused: { id: string; reason: string }[];
		}[];
	};
	checkFile: (
		absPath: string,
		relPath: string,
	) => {
		file: string;
		ids: Set<string>;
		refs: Set<string>;
		errors: string[];
		allowedUnused: { id: string; reason: string }[];
	};
	isValidId: (s: string) => boolean;
	ZOMBIE_ALLOWLIST: Record<string, Record<string, string>>;
	TARGET_FILES: string[];
};

describe('check-zombie-dom: id extraction', () => {
	it('extracts ids from HTML attributes', () => {
		const ids = checker.findHtmlIds('<div id="foo"><span id="bar"></span></div>');
		assert.deepEqual([...ids].sort(), ['bar', 'foo']);
	});

	it('handles multiple attributes per element', () => {
		const ids = checker.findHtmlIds('<input type="checkbox" id="abc" name="x">');
		assert.deepEqual([...ids], ['abc']);
	});

	it('handles tabs and newlines as attribute separators', () => {
		const ids = checker.findHtmlIds('<div\n\tclass="row"\n\tid="x">');
		assert.deepEqual([...ids], ['x']);
	});

	it('rejects ids with disallowed characters', () => {
		const ids = checker.findHtmlIds('<div id="a b">');
		assert.equal(ids.size, 0);
	});

	it('rejects ids that start with a digit', () => {
		const ids = checker.findHtmlIds('<div id="1abc">');
		assert.equal(ids.size, 0);
	});

	it('does not match attribute substrings without a boundary', () => {
		const ids = checker.findHtmlIds('<div validid="x">');
		assert.equal(ids.size, 0);
	});

	it('extracts ids quoted with single quotes', () => {
		const ids = checker.findHtmlIds("<div id='foo'><span id='bar'></span></div>");
		assert.deepEqual([...ids].sort(), ['bar', 'foo']);
	});

	it('does not extract ids declared inside a /* … */ block comment', () => {
		const ids = checker.findHtmlIds('/* <div id="legacy"></div> */');
		assert.equal(ids.size, 0);
	});

	it('does not extract ids declared after a // line comment', () => {
		const ids = checker.findHtmlIds("// <div id='inline'></div>\n");
		assert.equal(ids.size, 0);
	});
});

describe('check-zombie-dom: script reference detection', () => {
	it("matches getElementById('foo')", () => {
		const refs = checker.findScriptRefs(
			"document.getElementById('foo')",
			new Set(['foo']),
		);
		assert.ok(refs.has('foo'));
	});

	it('matches getElementById("foo")', () => {
		const refs = checker.findScriptRefs(
			'document.getElementById("foo")',
			new Set(['foo']),
		);
		assert.ok(refs.has('foo'));
	});

	it("matches helper $('foo')", () => {
		const refs = checker.findScriptRefs("$('foo')", new Set(['foo']));
		assert.ok(refs.has('foo'));
	});

	it('matches helper $("foo")', () => {
		const refs = checker.findScriptRefs('$("foo")', new Set(['foo']));
		assert.ok(refs.has('foo'));
	});

	it("matches querySelector('#foo')", () => {
		const refs = checker.findScriptRefs(
			"document.querySelector('#foo')",
			new Set(['foo']),
		);
		assert.ok(refs.has('foo'));
	});

	it('does not match unrelated ids', () => {
		const refs = checker.findScriptRefs("$('bar')", new Set(['foo']));
		assert.equal(refs.size, 0);
	});

	it('does not match a similar substring (no quote boundary)', () => {
		const refs = checker.findScriptRefs("$('prefix-foo')", new Set(['foo']));
		assert.equal(refs.size, 0);
	});

	it('ignores tokens inside a // line comment', () => {
		const refs = checker.findScriptRefs(
			"// $('foo') — TODO wire this up",
			new Set(['foo']),
		);
		assert.equal(
			refs.size,
			0,
			'commented-out wiring must not satisfy the zombie check',
		);
	});

	it('ignores tokens inside a /* … */ block comment', () => {
		const refs = checker.findScriptRefs(
			"/* once upon a time: $('foo') */",
			new Set(['foo']),
		);
		assert.equal(refs.size, 0);
	});

	it('still matches the LIVE token when a similar one is also commented out', () => {
		const refs = checker.findScriptRefs(
			"// historic: $('foo')\n$('foo');",
			new Set(['foo']),
		);
		assert.ok(refs.has('foo'));
	});
});

describe('check-zombie-dom: end-to-end on real claude-code-panel.ts', () => {
	const repoRoot = path.join(__dirname, '..', '..');

	it('claude-code-panel.ts has zero unjustified zombie ids', () => {
		const result = checker.checkAll(undefined, repoRoot);
		const human = result.results.flatMap((r) => r.errors).join('\n  ');
		assert.equal(result.hasErrors, false, 'unexpected zombie ids:\n  ' + human);
	});

	it('the bundled allowlist refers only to ids that actually exist in the source', () => {
		const result = checker.checkAll(undefined, repoRoot);
		for (const r of result.results) {
			const allowlist = checker.ZOMBIE_ALLOWLIST[r.file] || {};
			for (const id of Object.keys(allowlist)) {
				assert.ok(
					r.ids.has(id),
					'allowlist entry ' + r.file + ':' + id + ' has no matching id in HTML',
				);
			}
		}
	});

	it('TARGET_FILES and ZOMBIE_ALLOWLIST keys agree', () => {
		const targets = new Set(checker.TARGET_FILES);
		const allowed = new Set(Object.keys(checker.ZOMBIE_ALLOWLIST));
		assert.deepEqual([...targets].sort(), [...allowed].sort());
	});
});

describe('check-zombie-dom: synthetic detection cases', () => {
	const tmpDir = path.join(__dirname, '..', '..', '_tmp-zombie-dom-fixture');

	function writeFixture(name: string, content: string): string {
		fs.mkdirSync(tmpDir, { recursive: true });
		const p = path.join(tmpDir, name);
		fs.writeFileSync(p, content);
		return p;
	}

	function cleanup() {
		if (fs.existsSync(tmpDir)) fs.rmSync(tmpDir, { recursive: true, force: true });
	}

	it('reports an error for an unwired id absent from the allowlist', () => {
		const fixture = writeFixture(
			'fx1.ts',
			[
				'<div id="foo"></div>',
				'<div id="bar"></div>',
				'<script>',
				"$('foo');",
				'</script>',
			].join('\n'),
		);
		try {
			const r = checker.checkFile(fixture, 'fx1.ts');
			assert.equal(r.errors.length, 1);
			assert.match(r.errors[0], /id="bar"/);
		} finally {
			cleanup();
		}
	});

	it('reports a stale allowlist entry when the listed id IS wired up', () => {
		const fixture = writeFixture(
			'fx2.ts',
			[
				'<div id="autoReloadRows"></div>',
				'<script>',
				"$('autoReloadRows');",
				'</script>',
			].join('\n'),
		);
		try {
			const r = checker.checkFile(fixture, 'src/webview/claude-code-panel.ts');
			const stale = r.errors.find((e: string) => /stale entry/i.test(e));
			assert.ok(
				stale,
				'expected a stale-allowlist error, got: ' + r.errors.join('; '),
			);
		} finally {
			cleanup();
		}
	});

	it('reports a stale allowlist entry when the listed id is missing from HTML', () => {
		const fixture = writeFixture(
			'fx3.ts',
			['<div id="other"></div>', '<script>', "$('other');", '</script>'].join('\n'),
		);
		try {
			const r = checker.checkFile(fixture, 'src/webview/claude-code-panel.ts');
			const stale = r.errors.find((e: string) =>
				/no such id="\.\.\." exists/i.test(e),
			);
			assert.ok(stale, 'expected a missing-id error, got: ' + r.errors.join('; '));
		} finally {
			cleanup();
		}
	});
});

describe('check-zombie-dom: comment stripping', () => {
	it('replaces a line comment with a single space + trailing newline', () => {
		const out = checker.stripJsComments('a // comment\nb');
		// Line numbers are preserved by keeping the newline.
		assert.ok(out.includes('\nb'));
		assert.ok(!out.includes('comment'));
	});

	it('replaces a block comment body with whitespace, keeping line breaks', () => {
		const out = checker.stripJsComments('a /* line1\nline2 */b');
		// `line1` / `line2` words must be gone.
		assert.ok(!out.includes('line1'));
		assert.ok(!out.includes('line2'));
		// Newline preserved.
		assert.ok(out.includes('\n'));
		// Tail content survives.
		assert.ok(out.endsWith('b'));
	});

	it('leaves source without comments unchanged', () => {
		const src = '<div id="foo"></div>\n$(\'foo\');';
		assert.equal(checker.stripJsComments(src), src);
	});
});

describe('check-zombie-dom: id validator', () => {
	it('accepts valid ids', () => {
		assert.equal(checker.isValidId('foo'), true);
		assert.equal(checker.isValidId('foo-bar'), true);
		assert.equal(checker.isValidId('foo_bar'), true);
		assert.equal(checker.isValidId('a1'), true);
	});

	it('rejects empty / digit-leading / non-ascii-leading / whitespace ids', () => {
		assert.equal(checker.isValidId(''), false);
		assert.equal(checker.isValidId('1abc'), false);
		assert.equal(checker.isValidId('-foo'), false);
		assert.equal(checker.isValidId('foo bar'), false);
	});
});
