/**
 * Mocha setup — seeds a valid MCP_GATEWAY_AUTH_TOKEN so tests that
 * construct the real GatewayClient + LogViewer (via `activate()`
 * without injectedClient) do not fire the "no token" warning toast.
 *
 * Per-test overrides use `delete process.env[...]` inside a before/after
 * pair when a test needs to exercise the no-token path.
 *
 * Loaded via package.json `test` script's `--file src/test/setup.ts`.
 */

import { ENV_VAR_NAME, MIN_TOKEN_LEN } from '../auth-header';

if (!process.env[ENV_VAR_NAME]) {
	// Synthetic token with the right shape — tests don't need a real one.
	process.env[ENV_VAR_NAME] = 'A'.repeat(MIN_TOKEN_LEN);
}
