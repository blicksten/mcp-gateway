#!/usr/bin/env node
/*
 * check-zombie-dom.js — scan webview source for `id="..."` HTML elements and
 * verify each has a corresponding reference in the same file (i.e.
 * `getElementById('foo')`, `$('foo')`, or `querySelector('#foo')`). IDs that
 * are intentionally unreferenced (decorative wrappers, CSS-only grouping
 * containers) must be listed in ZOMBIE_ALLOWLIST with a justification.
 *
 * Run as part of `npm test` (via `pretest`) to catch the next "вечный
 * checking" zombie-DOM regression — the failure mode where an HTML
 * placeholder ships without its JS updater (B-01..B-04 in the audit-dashboard
 * plan).
 *
 * Plain JavaScript per CV-gate D-2 (mirror of check-catalog-refs.js): keeps
 * this off the TypeScript build path so CI never has to run a TS executor.
 */

'use strict';

const fs = require('node:fs');
const path = require('node:path');

// Map file path (relative to extension root) -> { id: justification }.
// Add an entry only when an HTML id is intentionally unreferenced (e.g.
// CSS-only wrapper). Stale entries are detected and reported as errors.
const ZOMBIE_ALLOWLIST = {
	'src/webview/claude-code-panel.ts': {
		autoReloadRows:
			'Wrapper div for Patch+Channel rows; CSS-only grouping with no JS interaction.',
	},
};

// Files scanned by default. Listing a file with an empty allowlist still
// runs the check — a future webview that grows id-bearing elements without
// wiring is exactly what this scanner is meant to surface.
const TARGET_FILES = Object.keys(ZOMBIE_ALLOWLIST);

function isValidIdChar(code, isFirst) {
	const isLetter =
		(code >= 0x41 && code <= 0x5a) || (code >= 0x61 && code <= 0x7a);
	if (isFirst) return isLetter;
	const isDigit = code >= 0x30 && code <= 0x39;
	return isLetter || isDigit || code === 0x2d /* - */ || code === 0x5f /* _ */;
}

function isValidId(s) {
	if (s.length === 0) return false;
	if (!isValidIdChar(s.charCodeAt(0), true)) return false;
	for (let i = 1; i < s.length; i++) {
		if (!isValidIdChar(s.charCodeAt(i), false)) return false;
	}
	return true;
}

// stripJsComments — replace JS line-comments and block-comments with
// whitespace so commented-out wiring (// $('foo')) does not satisfy the
// zombie check and a stray `/<asterisk> id="legacy" <asterisk>/` does
// not register a fake id. Block-comment delimiters are described in
// words rather than literally to avoid closing this very comment when
// the file is parsed by tooling that respects /<asterisk>...<asterisk>/.
//
// Naive scanner — does NOT track string literals, so it can occasionally
// strip a `//` that lives inside a quoted string (e.g. 'http://…'). That
// trade-off is acceptable here: the worst case is a slightly larger gap
// in the rendered source, which only changes whether a token match
// lands. We still match the actual id="…" / getElementById('…') tokens
// AROUND such gaps, so URL strings inside scripts do not corrupt the
// scan.
function stripJsComments(content) {
	const out = [];
	const len = content.length;
	let i = 0;
	while (i < len) {
		const c = content.charCodeAt(i);
		const next = i + 1 < len ? content.charCodeAt(i + 1) : 0;
		if (c === 0x2f && next === 0x2f /* `//` */) {
			// Line comment — copy a single space placeholder, then skip to
			// the next newline (preserved so line numbers line up).
			out.push(' ');
			i += 2;
			while (i < len && content.charCodeAt(i) !== 0x0a) i++;
			continue;
		}
		if (c === 0x2f && next === 0x2a /* `/*` */) {
			// Block comment — replace the body with spaces so byte offsets
			// stay roughly stable.
			out.push(' ');
			i += 2;
			while (i + 1 < len && !(content.charCodeAt(i) === 0x2a && content.charCodeAt(i + 1) === 0x2f)) {
				const ch = content.charCodeAt(i);
				out.push(ch === 0x0a ? '\n' : ' ');
				i++;
			}
			i += 2; // skip the closing */
			continue;
		}
		out.push(content[i]);
		i++;
	}
	return out.join('');
}

/**
 * Extract the value of every `id="…"` or `id='…'` HTML attribute in the
 * source. Uses indexOf scanning (not a regex) so the boundary check is
 * explicit and the code stays under the project's regex-discipline rule.
 *
 * `id=` is case-sensitive — HTML allows `ID=…` but our webview templates
 * uniformly use lower-case attributes. Extending to case-insensitive
 * matching would require either a normalization pass or two scans; not
 * worth the complexity until a webview actually uses upper-case.
 */
function findHtmlIds(content) {
	// Drop comments first so `/* id="x" */` does not register a fake id.
	const stripped = stripJsComments(content);
	const ids = new Set();
	const len = stripped.length;
	let pos = 0;
	while (pos < len) {
		const hit = stripped.indexOf('id=', pos);
		if (hit === -1) break;
		const prev = hit > 0 ? stripped.charCodeAt(hit - 1) : 0;
		const isBoundary =
			prev === 0x20 /* space */ ||
			prev === 0x09 /* tab */ ||
			prev === 0x0a /* \n */ ||
			prev === 0x0d /* \r */ ||
			prev === 0x3c; /* < */
		const quote = hit + 3 < len ? stripped.charCodeAt(hit + 3) : 0;
		const isQuote = quote === 0x22 /* " */ || quote === 0x27 /* ' */;
		if (!isBoundary || !isQuote) {
			pos = hit + 3;
			continue;
		}
		const quoteChar = String.fromCharCode(quote);
		const start = hit + 4;
		const end = stripped.indexOf(quoteChar, start);
		if (end === -1) {
			pos = hit + 3;
			continue;
		}
		const id = stripped.slice(start, end);
		if (isValidId(id)) ids.add(id);
		pos = end + 1;
	}
	return ids;
}

/**
 * For each candidate id, decide whether the source contains a JS reference
 * to it. Recognised patterns:
 *
 *   getElementById('foo') / "foo" / `foo`
 *   $('foo') / "foo" / `foo`           (the file-local helper from claude-code-panel.ts)
 *   querySelector('#foo') / "#foo" / `#foo`
 *
 * String-token matching only — no regex. Each id is short, the pattern
 * count per id is fixed (9), and `String.prototype.includes` is faster than
 * a per-id RegExp.
 */
function findScriptRefs(content, ids) {
	// Strip comments before scanning so a commented-out `// $('foo')` is
	// not treated as live wiring.
	const stripped = stripJsComments(content);
	const refs = new Set();
	for (const id of ids) {
		const tokens = [
			"getElementById('" + id + "')",
			'getElementById("' + id + '")',
			'getElementById(`' + id + '`)',
			"$('" + id + "')",
			'$("' + id + '")',
			'$(`' + id + '`)',
			"querySelector('#" + id + "')",
			'querySelector("#' + id + '")',
			'querySelector(`#' + id + '`)',
		];
		for (const t of tokens) {
			if (stripped.includes(t)) {
				refs.add(id);
				break;
			}
		}
	}
	return refs;
}

function checkFile(absPath, relPath) {
	const content = fs.readFileSync(absPath, 'utf8');
	const allowlist = ZOMBIE_ALLOWLIST[relPath] || {};

	const ids = findHtmlIds(content);
	const refs = findScriptRefs(content, ids);

	const errors = [];
	const allowedUnused = [];
	for (const id of ids) {
		if (refs.has(id)) continue;
		if (Object.prototype.hasOwnProperty.call(allowlist, id)) {
			allowedUnused.push({ id, reason: allowlist[id] });
			continue;
		}
		errors.push(
			relPath +
				': id="' +
				id +
				'" has no JS reference (getElementById / $() / querySelector) ' +
				'and is not in ZOMBIE_ALLOWLIST. Either wire the element up to a ' +
				'JS updater or add it to ZOMBIE_ALLOWLIST with a justification.',
		);
	}

	// Detect stale allowlist entries (drift in either direction).
	for (const allowedId of Object.keys(allowlist)) {
		if (!ids.has(allowedId)) {
			errors.push(
				relPath +
					': ZOMBIE_ALLOWLIST contains \'' +
					allowedId +
					'\' but no such id="..." exists in the source. Remove the stale entry.',
			);
		} else if (refs.has(allowedId)) {
			errors.push(
				relPath +
					': ZOMBIE_ALLOWLIST contains \'' +
					allowedId +
					'\' but it IS referenced from JS. Remove the stale entry.',
			);
		}
	}

	return { file: relPath, ids, refs, errors, allowedUnused };
}

function checkAll(files, baseDir) {
	const targets = files || TARGET_FILES;
	const root = baseDir || process.cwd();
	const results = [];
	let hasErrors = false;
	for (const rel of targets) {
		const abs = path.isAbsolute(rel) ? rel : path.join(root, rel);
		const r = checkFile(abs, rel);
		if (r.errors.length > 0) hasErrors = true;
		results.push(r);
	}
	return { hasErrors, results };
}

function main(argv) {
	const baseDir = argv[2] || process.cwd();
	const { hasErrors, results } = checkAll(undefined, baseDir);

	for (const r of results) {
		if (r.errors.length > 0) {
			for (const e of r.errors) process.stderr.write('check-zombie-dom: ' + e + '\n');
		} else {
			const allowedNote =
				r.allowedUnused.length > 0
					? ', ' + r.allowedUnused.length + ' allowlisted'
					: '';
			process.stdout.write(
				'check-zombie-dom: OK — ' +
					r.file +
					' (' +
					r.ids.size +
					' ids, ' +
					r.refs.size +
					' wired' +
					allowedNote +
					')\n',
			);
		}
	}

	if (hasErrors) process.exit(1);
}

if (require.main === module) {
	main(process.argv);
}

module.exports = {
	checkFile,
	checkAll,
	findHtmlIds,
	findScriptRefs,
	stripJsComments,
	isValidId,
	ZOMBIE_ALLOWLIST,
	TARGET_FILES,
};
