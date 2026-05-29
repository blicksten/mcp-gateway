.PHONY: deploy test test-integration-windows test-integration-phase16 check-grammar grammar help

# Unified build + deploy + verify — THE canonical "ship my changes" command.
# Rebuilds & reinstalls the Go daemon AND the VS Code extension, restarts the
# daemon so the running process is the fresh binary, removes duplicate extension
# installs, and verifies the live state matches what was just built. Eliminates
# the "we tested a stale build" class of bug. See scripts/deploy-all.js header.
#   make deploy                       full deploy (restarts daemon)
#   node scripts/deploy-all.js --skip-daemon-restart   stage daemon, don't restart
deploy:
	node scripts/deploy-all.js

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

# Phase 16.7 — full-chain Claude Code integration tests. Builds the stub
# MCP server on the fly (buildMockServer helper), starts the gateway with
# plugin regen + patch state wired, and exercises the patch lifecycle
# end-to-end. No external accounts or fixtures required.
test-integration-phase16:
	go test -tags integration -run 'TestIntegration_Phase16|TestIntegration_CORS' ./internal/api/... -v

# Grammar codegen — single source of truth at docs/grammar/sap-server-name.yaml.
# `grammar` regenerates both the Go (internal/sapname/grammar_gen.go) and TS
# (vscode/mcp-gateway-dashboard/src/sap-name-grammar.gen.ts) parsers.
# `check-grammar` is the staleness gate: non-zero exit when on-disk parsers
# drift from the YAML. CI's grammar-staleness job uses this target.
#
# `check-grammar` runs the Go check first (fast, no node_modules required);
# the TS-side delegate (`npm run check-grammar`) then re-runs the same Go
# binary so a developer working purely inside vscode/mcp-gateway-dashboard/
# gets the same staleness signal. First non-zero exit propagates via &&.
# Plan reference: docs/PLAN-sap-picker-and-import-mcp.md task T-A.2.
grammar:
	go run ./tools/grammar-gen

check-grammar:
	go run ./tools/grammar-gen/check && cd vscode/mcp-gateway-dashboard && npm run check-grammar

help:
	@echo "Targets:"
	@echo "  test                         - run unit + structural tests (all platforms)"
	@echo "  test-integration-windows     - run Windows DACL enforcement test (requires MCPGW_TEST_USER/PASSWORD; see Makefile header)"
	@echo "  test-integration-phase16     - run Phase 16 Claude Code E2E integration tests (Linux/macOS/Windows)"
	@echo "  grammar                      - regenerate SAP server-name parsers (Go + TS) from docs/grammar/sap-server-name.yaml"
	@echo "  check-grammar                - verify generated parsers are in sync with YAML (CI gate)"
