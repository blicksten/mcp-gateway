.PHONY: test test-integration-windows help

# Default Go test path — unit + structural tiers. Covers the DACL
# SHAPE on Windows (TestApplyTokenFilePerms_Windows_Structural) but
# not DACL ENFORCEMENT. See test-integration-windows for the latter.
test:
	go test ./...

# Windows DACL enforcement-tier integration test (PLAN-v15.md T15C.1).
#
# Requires MCPGW_TEST_USER and MCPGW_TEST_PASSWORD to be set in the
# environment — the target FAILS FAST (exit 1) if either is missing,
# so a silent skip cannot be mistaken for a successful enforcement
# run. This is intentional: when an operator explicitly invokes this
# target, absence of credentials is an operator error, not a
# "run the test path without verifying anything" condition.
#
# The target does NOT provision the test account itself: `net user
# /add` needs administrator rights and an interactive Windows shell,
# so silent-fail would give a misleading PASS. Operator protocol is
# documented in README.md §Testing tiers (the authoritative source —
# do not duplicate here to avoid drift). The test itself uses
# LogonUser + ImpersonateLoggedOnUser to become the test user, then
# attempts to read a token file protected by the production DACL.
# Expected outcome: ERROR_ACCESS_DENIED.
#
# Why not a GitHub Actions job: feasibility was deferred in v1.5.0
# — see docs/spikes/2026-04-19-windows-latest-impersonate.md. A future
# plan may promote this target to a CI job after the spike runs.
test-integration-windows:
	@[ -n "$$MCPGW_TEST_USER" ] && [ -n "$$MCPGW_TEST_PASSWORD" ] || { \
		echo "ERROR: MCPGW_TEST_USER and MCPGW_TEST_PASSWORD must be set."; \
		echo "See README.md §Testing tiers for the provisioning protocol."; \
		exit 1; \
	}
	go test -tags integration -run TestTokenPerms_Integration_Windows ./internal/auth/ -v

help:
	@echo "Targets:"
	@echo "  test                       - run unit + structural tests (all platforms)"
	@echo "  test-integration-windows   - run Windows DACL enforcement test (requires MCPGW_TEST_USER/PASSWORD; see Makefile header)"
