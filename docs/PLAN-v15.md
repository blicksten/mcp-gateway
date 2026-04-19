# Plan: v1.5.0 Tail Items (LOW findings + deferred integration tests)

## Session: v15 → `docs/PLAN-v15.md`

## Context

`PLAN-main.md` carried Phase 12.A, 12.B, and 13 through PAL gates with
zero MEDIUM+ findings (commits `a168647`, `b05f30e`, `c242b35`). Two
LOW items and two deferred integration-test tiers were consciously
accepted at the time because the fixes are non-blocking and the test
infra cost was high relative to v1.4.0 scope.

This plan picks them up for v1.5.0 alongside `PLAN-catalogs.md`.

**Scope assessment input:** `docs/REVIEW-v15.md` (architect + PAL gpt-5.2
cross-validation). Key refinements applied here:

- **T15A.1** — code hygiene, not security. CHANGELOG must frame as hygiene.
- **T15A.2** — extended to cover BOTH scanner sites (client + producer) per
  architect recommendation; F-11 closes out if both land.
- **T15C** — spike-first. Throwaway branch confirms whether
  `windows-latest` + `LogonUser` + `ImpersonateLoggedOnUser` is feasible
  before committing CI workflow change. If spike fails, T15C.2 scopes back
  to documented manual protocol.
- **T15D.1 CHANGELOG** — explicit sections for hygiene (T15A.1),
  breaking-config (T15B.3), F-11 re-triage.

## Phase 15.A — LOW findings from prior PAL reviews

- [x] T15A.1 — **Code hygiene (not a security fix).** `internal/auth/middleware.go`
  currently calls `subtle.ConstantTimeCompare([]byte(received), expectedBytes)`,
  which the Go stdlib documents as returning 0 immediately on length mismatch.
  Practical leakage for the fixed 43-char token is 1 bit out of 256 — PAL
  gpt-5.2 and the existing `TestMiddleware_ConstantTimeOnDifferentLengths`
  comment both agree this is not a meaningful attack surface. Land the change
  anyway to remove the recurring PAL-finding pattern and provide a clean
  reference for anyone copying this code to a variable-length secret in
  future: compare a pad-to-expected-length buffer, then do a separate length
  check. Add a timing-shape smoke unit test. **CHANGELOG wording: "hygiene",
  not "security fix".**

  **Pattern to land (both compares run unconditionally, result combined at end):**

  ```go
  // Pad-to-expected buffer so ConstantTimeCompare always runs on
  // equal-length inputs. copy() truncates if received > expected;
  // length equality is verified in a separate ConstantTimeEq call.
  padded := make([]byte, len(expectedBytes))
  copy(padded, receivedBytes)
  compareEq := subtle.ConstantTimeCompare(padded, expectedBytes)
  lengthEq := subtle.ConstantTimeEq(int32(len(receivedBytes)), int32(len(expectedBytes)))
  if compareEq&lengthEq != 1 { /* reject: 401 */ }
  ```

  Existing `TestMiddleware_ConstantTimeOnDifferentLengths` (middleware_test.go:130+)
  must still pass unchanged after the refactor — it pins the coverage shape.
- [x] T15A.2a — `internal/ctlclient/client.go:301` (`streamLogsOnce`) — SSE
  client-side scanner. Replace `bufio.NewScanner(resp.Body)` with an explicit
  `scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)` (1MB cap) + comment
  explaining the 64KB→1MB trade-off.
- [x] T15A.2b — `internal/lifecycle/manager.go:302` (`scanStderr`) — producer-side
  stderr scanner feeding the ring buffer. Same 1MB cap + comment. This is the
  upstream twin of T15A.2a; fixing only T15A.2a leaves the effective
  end-to-end cap at 64KB because the producer truncates first. Architect
  recommends covering both in the same phase so the CHANGELOG entry ("raised
  log line cap to 1MB") is accurate rather than true-but-misleading. Closes
  ROADMAP F-11.
- [x] GATE: tests + codereview + thinkdeep — zero errors (any finding at or above CLAUDE_GATE_MIN_BLOCKING_SEVERITY; default: any finding) — PASSED 2026-04-19, see docs/REVIEW-v15.md §"Phase 15.A GATE"

**Files:** `internal/auth/middleware.go`, `internal/auth/middleware_test.go`,
`internal/ctlclient/client.go`, `internal/lifecycle/manager.go`, existing
lifecycle tests.

**Acceptance criterion (T15A.2a + T15A.2b atomicity):** T15A.2a and T15A.2b
MUST land in the same commit, OR T15A.2a MUST NOT land without T15A.2b.
T15D.1 Fixes section must not claim the 1MB cap unless both are merged —
the end-to-end cap is the minimum of the two scanner limits, so landing
only one leaves the user-visible cap at 64KB while the CHANGELOG would
read as 1MB. If either task fails its GATE and must be reverted, revert
the other to keep doc and behavior aligned (either 64KB everywhere or
1MB everywhere).

## Rollback

All three tasks are additive and independent. `git revert` on the
middleware.go change restores the current ConstantTimeCompare path; revert
on either scanner change restores the default 64KB limit at that site.
Reverting one scanner does not affect the other — the two `Buffer()` calls
are independent. No data migration, no config change.

## Phase 15.B — TLS self-signed integration test + half-configured TLS refusal

- [ ] T15B.1 — `internal/api/tls_integration_test.go` — generate a
  self-signed cert in `t.TempDir()`, configure `GatewaySettings.TLSCertPath`
  + `TLSKeyPath`, start the server via `ListenAndServeTLS`, probe with
  `http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: ...}}}`,
  assert 200 on `/api/v1/health` and 401 on an authed route without Bearer.
  The `ServeTLS` branch at `server.go:369` is currently unexercised by
  `go test ./...`; this pins it.

  **Stdlib cert-generation pattern (pinned to avoid reviewer churn):**
  `ecdsa.GenerateKey(elliptic.P256(), rand.Reader)` →
  `x509.CreateCertificate` with template
  `{IsCA: true, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}` →
  PEM-encode cert to `certPath`, PEM-encode key to `keyPath`, both in
  `t.TempDir()`. Client config: `&tls.Config{RootCAs: certPool}` where
  `certPool.AppendCertsFromPEM(certPEM)`. Reference shape:
  `crypto/tls/generate_cert.go` in the Go source tree.
- [ ] T15B.2 — Negative: non-loopback bind + `authEnabled` + no TLS files →
  startup refusal. Current code at `server.go:321` already does this; test
  pins the deliberate error wording (same pattern as
  `middleware.go:16` wording comment) so future refactors don't regress it.
- [ ] T15B.3 — **Defect fix + negative test.** Currently, only
  `TLSCertPath` set (no key) or only `TLSKeyPath` set (no cert) silently
  drops back to plain HTTP via the `tlsEnabled := certPath != "" && keyPath != ""`
  gate at `server.go:310`. No warning, no refusal — operator edits config,
  sees no error, assumes TLS, actually runs plain HTTP. Fix: refuse to start
  if exactly one of the two paths is set, and name both paths in the error
  message. Test the refusal wording. **This is a breaking-config change** —
  installations with half-finished TLS config will refuse to start after
  v1.5.0 (see T15D.1 CHANGELOG note).

  **Deliberate error wording (grep target — future refactors must keep the
  string intact; same pattern as `middleware.go:16`):**

  > `TLS is half-configured: gateway.tls_cert_path is set but gateway.tls_key_path is empty — both must be set to enable TLS, or both must be empty for plain HTTP`

  Symmetric version when `tls_key_path` is set without `tls_cert_path`.
  T15D.1 CHANGELOG must quote this exact string (not a paraphrase).
  T15B.3 test `TestServer_HalfConfiguredTLS_RefusesToStart` pins both
  orderings (cert-only, key-only).
- [ ] GATE: tests + codereview + thinkdeep — zero errors (any finding at or above CLAUDE_GATE_MIN_BLOCKING_SEVERITY; default: any finding)

**Files:** new `internal/api/tls_integration_test.go`, `internal/api/server.go`
(half-configured refusal).

**Ordering note:** Land T15B.3 (refusal) first and T15B.1 (self-signed
success path) second, OR land atomic. T15B.2 is independent.

## Rollback

T15B.1 is a pure test addition — delete the file to revert. T15B.2 is also
a pure test addition. T15B.3 is the only behavior change: revert restores
the silent-downgrade behavior (dangerous but restores prior behavior for
operators surprised by the refusal). If reverting T15B.3, also revert any
CHANGELOG note added in T15D.1.

## Phase 15.C — Windows DACL enforcement-tier integration test

- [ ] T15C.0 — **Spike (throwaway branch, no merge): windows-latest
  `LogonUser` + `ImpersonateLoggedOnUser` feasibility.** Create a throwaway
  branch with a minimal `.github/workflows/spike-windows-impersonate.yml`
  that: (a) provisions a non-elevated test user via `net user testuser
  Pass123! /add`, (b) calls `LogonUser(testuser, ..., LOGON32_LOGON_INTERACTIVE,
  LOGON32_PROVIDER_DEFAULT, &hToken)`, (c) calls
  `ImpersonateLoggedOnUser(hToken)`, (d) attempts `os.Open` on a
  deny-everyone file. Confirm it does NOT fail with `SeTcbPrivilege` or
  similar elevation errors, AND that the CI agent context (often SYSTEM)
  does not trivially bypass the ACL (i.e., the test fails when impersonation
  is skipped — checking the test fails for the right reason). Decide:
  (1) windows-latest is viable → T15C.2 uses GitHub-hosted runner,
  (2) windows-latest fails / passes for wrong reasons → T15C.2 scopes back
  to documented manual protocol + `make test-integration-windows` target
  with README pointer, no CI workflow change. Discard the spike branch
  after decision.

  **Spike outcome artifact (required input for T15C.2 branching):**
  record the decision at `docs/spikes/2026-04-xx-windows-latest-impersonate.md`
  (replace `xx` with spike date) with these fields:
  - **Status:** `PASS` | `FAIL`
  - **Decision:** `CI workflow on windows-latest` | `manual protocol only`
  - **Evidence:** spike commit SHA / CI run link
  - **Reference path:** Makefile target name used by T15C.2

  T15C.2 reads this file to decide which branch to execute. The spike
  branch itself is discarded; only the spike report is kept on main.
- [ ] T15C.1 — `internal/auth/token_perms_integration_windows_test.go`
  (build tag `integration`). Uses `LogonUser` + `ImpersonateLoggedOnUser`
  to attempt `os.Open` on the token file as a second local account. Expect
  `ACCESS_DENIED`. Requires a dedicated Windows test account
  (`net user testuser Pass123! /add`); gated behind
  `make test-integration-windows` so the normal `go test ./...` path is
  unaffected. This picks up what the existing structural test file already
  promised in its header comment
  (`token_perms_windows_test.go:19-28`: "Real enforcement... is tested in
  token_perms_integration_windows_test.go under the integration build tag").
- [ ] T15C.2 — **Gated by T15C.0 spike outcome.**
  - If spike succeeds: new `test-integration-windows` job on `windows-latest`
    GitHub-hosted runner with the provisioning step inline in
    `.github/workflows/ci.yml`. Runs only on `push: [main]` (not every PR)
    to keep CI cost bounded.
  - If spike fails: no CI workflow change. Instead, add
    `make test-integration-windows` target to `Makefile` with the locked
    command, and a `README.md` pointer documenting the manual protocol
    (provisioning the test account + running the integration-tagged tests
    on a local Windows machine).
  The plan intentionally splits the decision so v1.5.0 does not block on
  runner infra.
- [ ] GATE: tests + codereview + thinkdeep — zero errors (any finding at or above CLAUDE_GATE_MIN_BLOCKING_SEVERITY; default: any finding)

**Files:** new
`internal/auth/token_perms_integration_windows_test.go`,
`Makefile` (new `test-integration-windows` target — both spike outcomes),
`.github/workflows/ci.yml` (only if T15C.0 succeeds),
`README.md` (only if T15C.0 fails — manual protocol pointer).

## Rollback

T15C.0 is a throwaway branch — nothing to revert on main. T15C.1 is a
pure test addition under a build tag that the default test path ignores;
deleting the file is a clean revert. T15C.2 revert depends on branch taken:
CI workflow branch → revert the YAML block; documented manual protocol
branch → revert the Makefile target + README pointer.

## Phase 15.D — Release + docs

- [ ] T15D.1 — `CHANGELOG.md` v1.5.0 entry. **Must include:**
  - **Hygiene** section: T15A.1 framed as code hygiene (not security fix)
    — ConstantTimeCompare pad-to-expected-buffer pattern, defensible
    reference for future variable-length secrets.
  - **Breaking-config** section: T15B.3 — half-configured TLS (exactly one
    of `TLSCertPath` / `TLSKeyPath` set) now refuses to start with an error
    message naming **both missing paths**. Operators running with
    half-finished TLS config from v1.4.0 must either complete the pair or
    remove both settings. No grace period — silent plain-HTTP when operator
    intended TLS is a security defect, not a feature.
  - **Fixes** section: T15A.2a + T15A.2b — scanner line-length cap raised
    from 64KB to 1MB on both the SSE client side and the producer stderr
    side.
  - **ROADMAP update**: F-11 (bufio.Scanner 64KB stderr limit) status.
    If both T15A.2a and T15A.2b landed clean, mark F-11 closed. If either
    reverted or scope reduced, leave F-11 LOW with updated comment pointing
    at the remaining scanner site.
  - **Tests** section: T15B.1/T15B.2/T15B.3 integration tier (TLS), T15C.1
    enforcement tier (Windows DACL), T15C.2 either CI-hosted
    (`windows-latest`) or documented-manual protocol depending on T15C.0
    outcome.
- [ ] T15D.2 — `README.md` pointer at the integration-test tier story:
  `go test ./...` covers unit + structural tiers; `make
  test-integration-windows` covers the Windows DACL enforcement tier; new
  TLS integration tests run under the default path (they're self-contained,
  no runner prerequisites). Operator-facing framing so users know the
  Windows token-file DACL is OS-enforced (not just structurally correct)
  when the integration tier has been executed.
- [ ] GATE: tests + codereview + thinkdeep — zero errors (any finding at or above CLAUDE_GATE_MIN_BLOCKING_SEVERITY; default: any finding)

**Files:** `CHANGELOG.md`, `README.md`, `docs/ROADMAP.md` (F-11 status).

## Rollback

T15D.1 and T15D.2 are pure documentation edits. Revert via `git revert` on
the doc commit. No runtime impact.

## Next Plans

Tail work continues in `docs/PLAN-catalogs.md` (v1.5.0 catalog UX —
server catalog, command catalog, catalog browse in Add Server webview,
slash-command template enrichment). This plan (`docs/PLAN-v15.md`) and
`docs/PLAN-catalogs.md` are independent and can land in either order or
in parallel streams.
