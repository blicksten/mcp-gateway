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
	});
});
