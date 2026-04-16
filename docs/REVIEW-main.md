# Full Codebase Audit — mcp-gateway v1.0.0

**Date:** 2026-04-14
**Auditor:** Porfiry [Opus 4.6]
**Cross-validated by:** GPT-5.2 Pro via PAL MCP
**Scope:** All implemented phases (1-10.5 + 11.0 status bar fix)
**Verdict:** CONDITIONAL APPROVE

---

## Audit Summary

Four parallel auditors reviewed the entire codebase (15,137 lines Go, 6,936 lines TypeScript):
- **Lead auditor** (Go daemon core): 6 findings
- **CLI specialist** (mcp-ctl): 7 findings
- **Extension specialist** (VS Code): 8 findings
- **Security auditor** (cross-cutting): 14 findings

Two rounds of fixes applied. 13 code edits across 5 Go files. All Go tests pass (11 packages, 0 failures). `go vet` clean. `go build` clean.

---

## Findings — Fixed in This Audit

| ID | Severity | File | Description | Fix |
|----|----------|------|-------------|-----|
| CLI-F1 | HIGH | `servers_setenv.go` | `set-env` bypassed dangerous-key blocklist (LD_PRELOAD, PATH, etc.) | Replaced inline check with `models.ValidateEnvEntries()` |
| SEC-F5 | HIGH | `config.go:231,237` | Config files written 0750/0640 — group-readable secrets | Tightened to 0700/0600 (owner-only) |
| GPT-1 | HIGH | `types.go:209` | Env key blocklist case-sensitive — `path`/`ld_preload` bypassed on Windows | Added `strings.ToUpper(key)` normalization before lookup |
| CORE-F1 | MEDIUM | `manager.go:525-532` | `RemoveServer` returned nil for non-existent servers (HTTP 200 on 404) | Added existence check with proper error return |
| CORE-F2 | MEDIUM | `manager.go:617-653` | `configChanged` missed `ExposeTools` field — hot-reload ignored changes | Added nil-safe pointer comparison |
| CORE-F3 | MEDIUM | `config.go:194-214` | `mergeLocal` omitted `CompressSchemas` from local overlay | Added field merge |
| CLI-F2 | MEDIUM | `servers_setenv.go:24` | `=VALUE` (empty key) passed validation | Added empty-key rejection in `validateEnv` |
| CLI-F3 | MEDIUM | `servers_setheader.go:21-28` | `set-header` missing CRLF/NUL/dangerous-header validation | Added `models.ValidateHeaderEntries()` call |
| SEC-F9 | MEDIUM | `manager.go:507-521` | `AddServer` didn't enforce `MaxServers` limit via REST API | Added `len >= MaxServers` check under write lock |
| SEC-F10 | MEDIUM | `types.go:334-336` | `Config.Validate` nil-panicked on `"server": null` in JSON | Added nil-check before `sc.Validate()` |
| GPT-2 | MEDIUM | `types.go:170-180` | `validateHTTPURL` accepted empty host and userinfo in URLs | Added `Host==""` and `User!=nil` checks |
| GPT-3 | MEDIUM | `config.go:244-262` | `SaveBytes` leaked tmp file on non-Windows rename error | Added `os.Remove(tmpPath)` on all error paths |
| GPT-4 | MEDIUM | `server.go:421` | PATCH `disabled=true` set status but didn't stop process (zombie) | Added `lm.Stop()` before `SetStatus` |

---

## Findings — Deferred (with justification)

| ID | Severity | Category | Description | Deferred to | Justification |
|----|----------|----------|-------------|-------------|---------------|
| SEC-F1 | CRITICAL | AuthN | No authentication on REST API | Phase 12 | Localhost-only by default; `allow_remote` requires explicit opt-in with warnings. Phase 12 specifically designed for this. |
| SEC-F2 | CRITICAL | RCE | Arbitrary command execution via `handleAddServer` | Phase 12 | Same as F1 — auth is the mitigation. Absolute path requirement limits scope. |
| SEC-F3 | HIGH | SSRF | No private IP filtering in `validateHTTPURL` | Phase 13 | SSRF requires API access (localhost-only). Host/userinfo checks added now; IP range filtering in Phase 13. |
| SEC-F4 | HIGH | SSRF | HTTP client follows redirects without re-validation | Phase 13 | Requires transport-level changes planned for Phase 13.B. |
| SEC-F7 | MEDIUM | Supply Chain | GitHub Actions pinned to version tags, not SHA hashes | Phase 14 | Cannot verify correct SHAs without web access; CI security planned for Phase 14.2. |
| SEC-F6 | MEDIUM | Injection | `ExpandVar` applied to `Command` field | Needs design review | Breaking change risk; absolute path validation mitigates. Requires ADR. |
| SEC-F8 | MEDIUM | Memory | KeePass password → Go string (never zeroed) | Documented (F-10) | Upstream `gokeepasslib` API constraint. Already in ROADMAP Known Limitations. |
| SEC-F11 | MEDIUM | Env | Child processes inherit dangerous env from parent | Needs design review | Breaking change risk; requires careful analysis of legitimate use cases. |
| EXT-F01 | MEDIUM | Memory | SSE buffer unbounded in `log-viewer.ts` | Phase 11.A | TypeScript fix; npm unavailable in current shell. |
| EXT-F02 | MEDIUM | Transport | `gateway-client.ts` HTTP-only (no HTTPS) | Phase 13.B | TLS support planned for Phase 13. |
| EXT-F03 | MEDIUM | Concurrency | Credential store concurrent index writes | Phase 11 | TypeScript fix; npm unavailable in current shell. |

---

## Verification Evidence

- **Go tests:** `go test ./...` — 11 packages pass, 0 failures (both before and after fixes)
- **Go vet:** clean (no findings)
- **Go build:** `go build ./...` — clean
- **PAL codereview:** GPT-5.2 Pro via PAL MCP, gate_mode=true, 27 files examined
- **4 auditor agents:** lead-auditor (Go core), 2x specialist-auditor (CLI, Extension), security-auditor
- **Audit agents used PAL:** thinkdeep (gpt-5.2-pro), consensus confirmation on all CRITICAL/HIGH findings

---

## Manual Review Required

| Item | Why manual verification needed | Risk if skipped |
|------|-------------------------------|-----------------|
| TypeScript extension fixes (EXT-F01, F02, F03) | npm not available in current shell; cannot run `npm test` | Medium — SSE buffer leak under adversarial server |
| GitHub Actions SHA pinning (SEC-F7) | Need web access to look up correct commit SHAs | Medium — supply chain risk on release workflow |
| ExpandVar on Command field (SEC-F6) | Breaking change risk requires design discussion | Low — mitigated by absolute path requirement |
| Child env inheritance (SEC-F11) | Needs analysis of legitimate use cases before filtering | Low — only affects multi-user deployments |

---

## Phase 11.E Review — Slash Command Auto-generation

**Date:** 2026-04-16
**Reviewer:** Porfiry [Opus 4.6] + GPT-5.1-Codex (PAL)
**Commit:** 991121f

### Files Reviewed

| File | Lines | Type |
|------|-------|------|
| `src/slash-command-generator.ts` | 192 | NEW |
| `src/extension.ts` | +15 | MODIFIED |
| `src/test/helpers/tmpdir.ts` | 12 | NEW |
| `src/test/slash-command-generator.test.ts` | ~270 | NEW |
| `package.json` | +10 | MODIFIED |

### Findings

| ID | Severity | Source | Description | Status |
|----|----------|--------|-------------|--------|
| 11E-H1 | HIGH | [O] PAL precommit | Daemon outage (lastRefreshFailed=true, empty server list) would trigger transition-detection deletes for all servers | Fixed — added early return when `lastRefreshFailed=true` + 2 regression tests |
| 11E-L1 | LOW | [C+O] | Silent error swallowing in fs.writeFile/unlink catch blocks | By-design — queued writes must not break the promise chain |

### Expert-Raised Items (by-design, not bugs)

| Item | Expert Severity | Resolution |
|------|----------------|------------|
| First-refresh seeding doesn't sync filesystem | HIGH | By-design per REFINEMENT E-3: prevents spurious writes on extension startup |
| Multi-root workspace resolves to first folder | MEDIUM | By-design per REFINEMENT E-1: "use first workspace folder or skip gracefully" |

### Verdict

**APPROVE** — zero MEDIUM+ after 11E-H1 fix. 441 passing tests, 24 dedicated to slash-command-generator.
