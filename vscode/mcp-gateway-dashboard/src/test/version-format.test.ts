import { strict as assert } from 'node:assert';
import { hasRealVersion, formatGatewayVersion } from '../version-format';

describe('version-format helpers (audit-e7618c9c SC-C-M1)', () => {
	describe('hasRealVersion', () => {
		it('returns false for undefined / empty / "dev"', () => {
			assert.equal(hasRealVersion(undefined), false);
			assert.equal(hasRealVersion(''), false);
			assert.equal(hasRealVersion('dev'), false);
		});

		it('returns true for ldflags-style versions', () => {
			assert.equal(hasRealVersion('1.29.0'), true);
			assert.equal(hasRealVersion('v1.29.0'), true);
		});

		it('returns true for runtime/debug.ReadBuildInfo synthesised versions', () => {
			assert.equal(hasRealVersion('v0.0.0-20260506054626-3c0f59c714a7'), true);
			assert.equal(hasRealVersion('v0.0.0-20260506054626-3c0f59c714a7+dirty'), true);
			assert.equal(hasRealVersion('0.0.0-3c0f59c714a7'), true);
		});

		it('returns true for any non-dev string (forward-compat)', () => {
			// Other dev-build sentinels (Go's `(devel)`, hypothetical `devel`)
			// are intentionally NOT special-cased — only the literal "dev"
			// emitted by cmd/mcp-gateway/main.go is filtered. If Go's
			// behaviour changes upstream, this asserts the contract.
			assert.equal(hasRealVersion('(devel)'), true);
			assert.equal(hasRealVersion('devel'), true);
		});
	});

	describe('formatGatewayVersion', () => {
		it('returns placeholder for undefined / empty', () => {
			assert.equal(formatGatewayVersion(undefined), 'unknown');
			assert.equal(formatGatewayVersion(''), 'unknown');
		});

		it('honours the placeholder override argument', () => {
			assert.equal(formatGatewayVersion(undefined, '—'), '—');
			assert.equal(formatGatewayVersion('', '?'), '?');
		});

		it('renders "dev" as "dev build" (operator-friendly, not hidden)', () => {
			// Regression guard for audit D-LOW finding: pre-audit, the UI
			// hid the version entirely when the daemon reported "dev",
			// which was hostile to local-dev users. Now we surface it.
			assert.equal(formatGatewayVersion('dev'), 'dev build');
		});

		it('preserves explicit v-prefix without doubling', () => {
			assert.equal(formatGatewayVersion('v1.29.0'), 'v1.29.0');
		});

		it('adds v-prefix when missing', () => {
			assert.equal(formatGatewayVersion('1.29.0'), 'v1.29.0');
		});

		it('handles VCS-synthesised versions verbatim with v-prefix', () => {
			assert.equal(
				formatGatewayVersion('v0.0.0-20260506054626-3c0f59c714a7+dirty'),
				'v0.0.0-20260506054626-3c0f59c714a7+dirty',
			);
			assert.equal(
				formatGatewayVersion('0.0.0-3c0f59c714a7'),
				'v0.0.0-3c0f59c714a7',
			);
		});

		it('never produces a double-v prefix (regression guard for audit-e7618c9c)', () => {
			// Pre-migration, status-bar.ts emitted `v${health.version}` unconditionally,
			// which produced 'vv0.0.0-...' when the daemon's runtime/debug.ReadBuildInfo()
			// fallback synthesised a version starting with 'v'. The helper detects
			// the existing prefix via startsWith('v') — this test pins that contract
			// so a future "simpler" refactor can't silently regress to 'vv...'.
			// PAL feedback on closeout review, 2026-05-10.
			const cases = [
				'v1.29.0',
				'v0.0.0-20260506054626-3c0f59c714a7',
				'v0.0.0-20260506054626-3c0f59c714a7+dirty',
				'v2.0.0-rc1',
			];
			for (const v of cases) {
				const out = formatGatewayVersion(v);
				assert.ok(
					!out.startsWith('vv'),
					`formatGatewayVersion(${JSON.stringify(v)}) must not produce double-v, got: ${out}`,
				);
				assert.equal(out, v, `v-prefixed input must round-trip unchanged: ${v}`);
			}
		});
	});

	describe('hasRealVersion as TS type predicate', () => {
		it('narrows the type so callers do not need non-null assertion', () => {
			const v: string | undefined = 'v1.29.0' as string | undefined;
			if (hasRealVersion(v)) {
				// If the predicate signature is `v is string`, TS narrows v here
				// to plain `string` (not `string | undefined`). This test exists
				// for compile-time verification — at runtime it just confirms
				// the value is usable as a string without assertions.
				const noAssertNeeded: string = v;
				assert.equal(noAssertNeeded.length > 0, true);
			} else {
				assert.fail('predicate should accept v1.29.0');
			}
		});
	});
});
