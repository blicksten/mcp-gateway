# Plan: v1.5.0 Tail Items (LOW findings + deferred integration tests)

## Session: v15 → `docs/PLAN-v15.md`

## Context

`PLAN-main.md` carried Phase 12.A, 12.B, and 13 through PAL gates with
zero MEDIUM+ findings (commits `a168647`, `b05f30e`, `c242b35`). Two
LOW items and two deferred integration-test tiers were consciously
accepted at the time because the fixes are non-blocking and the test
infra cost was high relative to v1.4.0 scope.

This plan picks them up for v1.5.0 alongside `PLAN-catalogs.md`.

## Phase 15.A — LOW findings from prior PAL reviews

- [ ] T15A.1 — `auth.Middleware` constant-time compare: current code uses `subtle.ConstantTimeCompare([]byte(received), expectedBytes)` which early-returns on length mismatch. Token length is fixed at 43 chars so the practical timing signal is tiny, but a "clean" implementation compares a pad-to-expected-length buffer and checks length separately. Fix in `internal/auth/middleware.go` + unit test timing smoke.
- [ ] T15A.2 — `ctlclient.streamLogsOnce` uses `bufio.NewScanner(resp.Body)` with the default 64KB per-line limit. A child-process log line > 64KB (e.g. a stack trace with a huge payload) aborts the SSE stream. Fix: explicit `scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)` (1MB cap) + comment pointing at the trade-off.
- [ ] T15A.GATE — tests + codereview — zero MEDIUM+.

**Files:** `internal/auth/middleware.go`, `internal/ctlclient/client.go`, tests.

## Phase 15.B — TLS self-signed integration test

- [ ] T15B.1 — `internal/api/tls_integration_test.go` — generate a self-signed cert in `t.TempDir()`, configure `GatewaySettings.TLSCertPath` + `TLSKeyPath`, start the server via `ListenAndServe`, probe with `http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: ...}}}`, assert 200 on `/api/v1/health` and 401 on an authed route without Bearer.
- [ ] T15B.2 — Negative: non-loopback bind + authEnabled + no TLS files → startup refusal (current code does this; test pins the wording so future refactors don't regress it).
- [ ] T15B.3 — Negative: only `TLSCertPath` set (no key) or only `TLSKeyPath` set — currently silently drops back to plain HTTP. Fix: refuse to start if exactly one of the two paths is set, and test the refusal.
- [ ] T15B.GATE — tests + codereview — zero MEDIUM+.

**Files:** new `internal/api/tls_integration_test.go`, `internal/api/server.go` (one-path-set refusal).

## Phase 15.C — Windows DACL enforcement-tier integration test

- [ ] T15C.1 — `internal/auth/token_perms_integration_windows_test.go` (build tag `integration`). Uses `LogonUser` + `ImpersonateLoggedOnUser` to attempt `os.Open` on the token file as a second local account. Expect `ACCESS_DENIED`. Requires a dedicated Windows runner with a pre-provisioned test account (`net user testuser Pass123! /add`); gated behind `make test-integration-windows` so the normal `go test ./...` path is unaffected.
- [ ] T15C.2 — CI change: new `test-integration-windows` job on a self-hosted Windows runner (or a GitHub-hosted windows-latest runner with the provisioning step inline). Runs only on `push: [main]` — not on every PR, to keep CI cost bounded.
- [ ] T15C.GATE — manual run + codereview — zero MEDIUM+.

**Files:** new `internal/auth/token_perms_integration_windows_test.go`, `.github/workflows/ci.yml`.

## Phase 15.D — Release + docs

- [ ] T15D.1 — CHANGELOG.md v1.5.0 entry listing LOW fixes + new integration test tiers.
- [ ] T15D.2 — README pointer at the integration-test story (so operators know the Windows DACL is OS-enforced, not just structurally correct).
- [ ] T15D.GATE — PAL precommit.

## Rollback

Every phase is additive. The LOW fixes can be reverted individually; the integration tests can simply be skipped via build-tags or CI removal.
