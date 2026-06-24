# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.6.0](https://github.com/blicksten/mcp-gateway/compare/v1.5.0...v1.6.0) (2026-06-24)


### Features

* **api,vscode:** close 3-layer register-pid pipeline RCA + Path 1 sentinel ([5793377](https://github.com/blicksten/mcp-gateway/commit/57933772698f4abda2f8fde54c0e2dee06aea9f7))
* **api:** add redacted GET /api/v1/debug/dump state endpoint (Phase 3) ([dc838cc](https://github.com/blicksten/mcp-gateway/commit/dc838ccd64d2cd75575a68e6b1a3b3ca19eaf937))
* **api:** P0.7 D-5 viability spike — session resurrection primitives ([fcc47cd](https://github.com/blicksten/mcp-gateway/commit/fcc47cd2e292be1f0105d8e8815b7b79b7d1b97d))
* **api:** P1.1 — REST PAL queue endpoints (FM-34, admin scope) ([06a431f](https://github.com/blicksten/mcp-gateway/commit/06a431f94a0d3c7a59b44b4e7d9922ce3ef53809))
* **api:** P1.2 — FM-9 multi-instance hard-limit (window_id at register-pid) ([84ee581](https://github.com/blicksten/mcp-gateway/commit/84ee581927e81713a22cf3c7a19f250f94ba2dff))
* **api:** server rename via PATCH new_name — Phase 1 of server-rename plan ([5d21509](https://github.com/blicksten/mcp-gateway/commit/5d21509224a9ff7a615c9c72f8f7734d5ddf4d35))
* **api:** T0.7.1 closure — SessionStateRegistry disk persistence ([cee568f](https://github.com/blicksten/mcp-gateway/commit/cee568f353caf30bf112ae1e72f27c0c6c580388))
* **api:** T0.7.1 wire ResumableStreamableHTTPHandler — session resurrection on daemon restart ([dfc4d60](https://github.com/blicksten/mcp-gateway/commit/dfc4d601cced81f5a4f067eccb20041fd9ebf4f9))
* **api:** TASK C2.1 MED-3 — health summary idle:N counter ([75009b9](https://github.com/blicksten/mcp-gateway/commit/75009b9d86718c6d65572bc24a5fe9d85d38cc49))
* **catalog-A:** server + command catalogs v1.5.0 — schemas, seeds, TS loader, CI ([54f8c16](https://github.com/blicksten/mcp-gateway/commit/54f8c16beac7ba708f9762aeb8fd641363e72045))
* **catalog-B:** Add Server browse webview — catalog dropdown + host re-validation ([c49e6ef](https://github.com/blicksten/mcp-gateway/commit/c49e6ef422bba92b6a95189267002645ef68f58a))
* **catalog-C:** slash-command template enrichment — catalog-aware SlashCommandGenerator ([6e70dbd](https://github.com/blicksten/mcp-gateway/commit/6e70dbd4b15e0dbbd5d36f2b7d598203fc3c1c1c))
* **catalog-D:** v1.5.0 release gate — README + CHANGELOG + VSIX + final security codereview ([864c5d5](https://github.com/blicksten/mcp-gateway/commit/864c5d51cae7d0d00e29841244741188183dd1db))
* **claude-code:** unfreeze-button daemon endpoints (T1-T4 of PLAN-unfreeze-button v3) ([ea6bd42](https://github.com/blicksten/mcp-gateway/commit/ea6bd427e15471291371517e94007884750e4267))
* **claudeimport:** T-D.0..T-D.5 — Import-from-Claude backend (claudeconfig + claudeimport + REST handlers) ([f0d1570](https://github.com/blicksten/mcp-gateway/commit/f0d1570fabaadf3f66eb1bde80131fd191e68efc))
* **daemon:** Phase D.1 — daemon lifecycle control foundation ([7e560de](https://github.com/blicksten/mcp-gateway/commit/7e560de99503479c471c667cb2728a8e2e287465))
* **extension:** audit-dashboard Phase 0 — contract-drift fixes (v1.10.0) ([e6b621d](https://github.com/blicksten/mcp-gateway/commit/e6b621d0f0d0ee45c06dc6154c5c66a6fbc59e25))
* **extension:** audit-dashboard Phase 1 — ClaudeCodePanel detection (v1.11.0) ([6d646f4](https://github.com/blicksten/mcp-gateway/commit/6d646f49651e6a50b9b34db5d22f5ac5410ad187))
* **extension:** audit-dashboard Phase 2 — shared Logger + silent-catch cleanup (v1.12.0) ([fd03241](https://github.com/blicksten/mcp-gateway/commit/fd03241785556b9e2f396c2ba27559de60714c06))
* **extension:** audit-dashboard Phase 3 — backend-aware daemon commands (v1.13.0) ([5430cfb](https://github.com/blicksten/mcp-gateway/commit/5430cfb597fd4a47809c53b7b8e3f5ea685bb306))
* **extension:** audit-dashboard Phase 4 — gatewayVersion + probe cleanup + version-skew (v1.15.0) ([7255fb7](https://github.com/blicksten/mcp-gateway/commit/7255fb76787c07aa2d4f29a5952bcc4faa5ec6bc))
* **extension:** audit-dashboard Phase 5 — Activate-button CWD independence (v1.16.0) ([f43ca22](https://github.com/blicksten/mcp-gateway/commit/f43ca2268764f86513ee2b50de98248dc1830c67))
* **extension:** audit-dashboard Phase 6 — zombie-DOM CI + smoke checklist (v1.18.0) ([24d8c4c](https://github.com/blicksten/mcp-gateway/commit/24d8c4c62550f4bce98c6c7b588941cbb408b791))
* **extension:** audit-dashboard Phase 8 + CI fix — detail-panel reconcile + settings watcher (v1.19.0) ([e223dd4](https://github.com/blicksten/mcp-gateway/commit/e223dd4dd2e60aa694bde2a0a807b8fa4926e3af))
* **extension:** audit-dashboard Phase 9 — Windows daemon-kill safety + REST-first stop (v1.20.0) ([3b7c0c2](https://github.com/blicksten/mcp-gateway/commit/3b7c0c2b8b4cf19e684c36df90fc9ff69680b735))
* **extension:** mcpGateway.sapSystemsEnabled — hide SAP Systems view by default (v1.9.0) ([0c4e6f6](https://github.com/blicksten/mcp-gateway/commit/0c4e6f639c69c3b859b1acab36ad9a433ff72e4d))
* **extension:** phase 11.E slash command auto-generation ([991121f](https://github.com/blicksten/mcp-gateway/commit/991121f82f946e103e007630395d5b5997bc8581))
* **extension:** Phase 17 dashboard UX polish — v1.7.0 ([dffb4b3](https://github.com/blicksten/mcp-gateway/commit/dffb4b325ea40986160167c42cbbdd78f29257a2))
* **extension:** server rename TS client — Phase 2 of server-rename plan ([a634b5c](https://github.com/blicksten/mcp-gateway/commit/a634b5cac796e46c6aabad37bfb5a10119ccc6a3))
* **extension:** server rename UI + VSIX — Phase 3 of server-rename plan ([d5d6157](https://github.com/blicksten/mcp-gateway/commit/d5d61573b3ab5b4659b0e17f35d75210a0de6d6b))
* **health:** connection-gate sap-gui-* on COM engine unavailability (spike Part B) ([2f11f28](https://github.com/blicksten/mcp-gateway/commit/2f11f2871af16ed9f3592f92081630187fe47761))
* **import-claude:** T-E.1..T-E.3 — Import-from-Claude webview (cc_global / cc_project / desktop sources, copy/move with conflict policy, R-23 move+overwrite warning, preview-confirm modal, retry-failed-rows) ([680e1ba](https://github.com/blicksten/mcp-gateway/commit/680e1bacfded6aa6ac1176de4e0e712bc2aeb41e))
* **lifecycle,health,vscode:** StatusUnreachable for TCP-level backend failures ([cd931db](https://github.com/blicksten/mcp-gateway/commit/cd931dbfc5bbb80af9abab96afb7bf1af3a43734))
* **lifecycle,health:** P1.5 step 1 — suture/v4 + failsafe-go adoption ([7854c12](https://github.com/blicksten/mcp-gateway/commit/7854c1298de7bb4cfb82b0e3f3cce61eac4f2193))
* **lifecycle:** FM 1 — subprocess registry for crash-recovery orphan reaping ([7228aef](https://github.com/blicksten/mcp-gateway/commit/7228aef2edb9c211b0a910fc34ccc49258e69e15))
* **lifecycle:** P1.3 Task C — runtime supervisor Add/RemoveAndWait ([5ad4464](https://github.com/blicksten/mcp-gateway/commit/5ad44644e1a6defa9a163685063c152fbdadcedf))
* **lifecycle:** P1.5 step 2 — activate suture supervisor tree in production ([22a81c0](https://github.com/blicksten/mcp-gateway/commit/22a81c090cf618ef5686120cd37b5cc066d76f90))
* **lifecycle:** TASK C1 — skip eager-spawn of unreachable stdio SAP backends + retry ([4f5053b](https://github.com/blicksten/mcp-gateway/commit/4f5053b4cc47deffd402869b06d750a0384d320b))
* **lifecycle:** TASK C2.1 — durable tool-manifest cache + StatusIdle (dormant, flag-gated) ([7f90583](https://github.com/blicksten/mcp-gateway/commit/7f90583c41ea09c82519f6bd8dfab51a6a443e23))
* **lifecycle:** TASK C2.2 — lazy spawn-on-first-invoke (dormant, flag-gated) ([e7b26cf](https://github.com/blicksten/mcp-gateway/commit/e7b26cf6b6b7d5008679efc7dad35431ca8f3eb8))
* **lifecycle:** TASK C2.3 — lazy-spawn boot wiring + supervisor Idle-gate (dormant, flag-gated) ([010156b](https://github.com/blicksten/mcp-gateway/commit/010156b2b43532bb768b318bae35bb9e2559c424))
* **lifecycle:** TASK C2.5 — Guard 1 stale-manifest re-discovery + close functional-coverage gaps ([0678a9d](https://github.com/blicksten/mcp-gateway/commit/0678a9d9a53265eb0d19b8d70e449a3e62d63ce8))
* **mcp-ctl:** add --password-stdin + --password-file to `credential list-structured` ([7dd97d9](https://github.com/blicksten/mcp-gateway/commit/7dd97d9c42f8f50d4721b3105a558b69de2af1d4))
* **mcp-ctl:** doctor subcommand + dashboard ARCHITECTURE.md (audit-dashboard Phase 7) ([127cd51](https://github.com/blicksten/mcp-gateway/commit/127cd512d207fa6d4078c25bc8121d1cb1b12756))
* **mcp-ctl:** Phase D.2 — daemon lifecycle subcommands ([4aa3e0e](https://github.com/blicksten/mcp-gateway/commit/4aa3e0e77091f21a6f365c4b3f6fa1cb14a6a64f))
* mcp-gateway v1.0.0 ([5df38c3](https://github.com/blicksten/mcp-gateway/commit/5df38c348de01f29196afc868b53fa54d3e3bf43))
* **MCPR.3:** admin Bearer middleware + bearerMiddleware refactor ([3b7cab5](https://github.com/blicksten/mcp-gateway/commit/3b7cab5d066715a549efa76b0e223f4374b58acb))
* **MCPR.3:** admin route split + audit logging for daemon-control endpoint ([c090a30](https://github.com/blicksten/mcp-gateway/commit/c090a30b150df92f1ef38edd2a91d240bea13864))
* **MCPR.3:** mcp-ctl admin migration + ADR-0007 (final commit) ([303ea9a](https://github.com/blicksten/mcp-gateway/commit/303ea9a4a170218203edd3ae99b841b4b6a26974))
* **MCPR.4:** TouchMtime helper for plugin .mcp.json fs-watcher signal ([d3ff8d3](https://github.com/blicksten/mcp-gateway/commit/d3ff8d3326ec4368176ff111dd9ac1a70fc70c8a))
* **MCPR.4:** TriggerPluginReannounce wires two-layer respawn recovery ([6f7d096](https://github.com/blicksten/mcp-gateway/commit/6f7d096d99c05ab2b5985f1434647e06723acc9d))
* **metrics:** TASK T1 — lazy-spawn observability counters on /api/v1/metrics ([0f50fea](https://github.com/blicksten/mcp-gateway/commit/0f50fea9a839d47303fa3a326ab578d95d7f1974))
* **models:** add ServerConfig.SAPEnvURL accessor (prereq for orphan-fix C1) ([db5ac87](https://github.com/blicksten/mcp-gateway/commit/db5ac8728bd319d923f0f94307e0632570e080d8))
* **models:** re-add StatusIdle + StatusIdleReason (prereq for orphan-fix C2.1) ([bdb97bd](https://github.com/blicksten/mcp-gateway/commit/bdb97bdcd63e49af652e3809e232df3b78c6e2d6))
* **obs:** add schema_version as the first field of every emitted event ([43cd0b8](https://github.com/blicksten/mcp-gateway/commit/43cd0b81c643e49f44fe3e28746fcae2e64e6184))
* **obs:** add toggleable structured-event emitter (Phase 1 core) ([66e1b16](https://github.com/blicksten/mcp-gateway/commit/66e1b16fbbe9d3b7a7957764bce7ec76d3860739))
* **obs:** wire structured-event emitter into gateway (main/api/lifecycle) ([8a8017f](https://github.com/blicksten/mcp-gateway/commit/8a8017f8aa05c5ecfc24078dec961ede6848f977))
* **phase-12.A:** Bearer auth - VS Code extension (T12A.8-T12A.12) ([4e075bf](https://github.com/blicksten/mcp-gateway/commit/4e075bfb63d7c71e867b8927e5d0182a4b0bbf57))
* **phase-12.A:** Bearer token auth - daemon + mcp-ctl (Go side) ([6686cd2](https://github.com/blicksten/mcp-gateway/commit/6686cd2ba4d0c28fba606a66d8def6ea3af8c55b))
* **phase-12.B:** KeePass credential push (T12B.1-T12B.6) ([7b1e52f](https://github.com/blicksten/mcp-gateway/commit/7b1e52f51669b674b398dfea79cd8cda03bf5ea7))
* **phase-13:** security hardening - process groups + watcher race + TLS + log redaction ([a845a78](https://github.com/blicksten/mcp-gateway/commit/a845a7830ab0208ed60fab509203750ea493a7ec))
* **phase-14:** community/CI — SECURITY.md + gitleaks + README auth/TLS/redaction ([29e6fc2](https://github.com/blicksten/mcp-gateway/commit/29e6fc22eb40d225752d69352f9e1a910cc7daa8))
* **phase-15.A:** LOW findings closure — ConstantTimeCompare hygiene + scanner 1MB cap ([5c949ca](https://github.com/blicksten/mcp-gateway/commit/5c949caa5a2e26a0c5d24b7928ac59aecf75465c))
* **phase-15.B:** TLS integration tier — half-configured refusal + ServeTLS coverage ([be9bbe9](https://github.com/blicksten/mcp-gateway/commit/be9bbe98d12b96d2c585c0f03658f6bbb90fc34f))
* **phase-15.C:** Windows DACL enforcement tier — integration test + manual-protocol branch ([22f94c3](https://github.com/blicksten/mcp-gateway/commit/22f94c36682c6066e7c4b244ec6191991df060a2))
* **phase-16.1:** Gateway dual-mode — aggregate + per-backend MCP proxy ([41ddb7f](https://github.com/blicksten/mcp-gateway/commit/41ddb7f80996eb2b101c325f4c69f52f2e32b75b))
* **phase-16.2:** Claude Code plugin packaging + .mcp.json regen pipeline ([a397696](https://github.com/blicksten/mcp-gateway/commit/a3976961b0d7419ca070da4d9665ee6b15d0a8fd))
* **phase-16.3:** Claude Code REST endpoints + patchstate package ([a7521fa](https://github.com/blicksten/mcp-gateway/commit/a7521fa4413294695db49dca34b602e0a24e5363))
* **phase-16.4:** Claude Code webview patch — Alt-E native reconnect ([e8de700](https://github.com/blicksten/mcp-gateway/commit/e8de7006aa9daa91b63204a9a3fb4326bf91c5cf))
* **phase-16.5:** Dashboard Claude Code Integration panel ([4c7b54e](https://github.com/blicksten/mcp-gateway/commit/4c7b54e96aabe565eca78ba9b62126dc18941076))
* **phase-16.6:** gateway.invoke fallback + meta-tools + cache-bust version ([aa49242](https://github.com/blicksten/mcp-gateway/commit/aa49242b325b9edb7d7091a702cf0ae266d7034e))
* **phase-16.7:** integration tests — E2E patch chain + CORS ([11e1a6c](https://github.com/blicksten/mcp-gateway/commit/11e1a6c1b1d7e4e025d98bb00a787983264b3d49))
* **phase-16.8:** mcp-ctl install-claude-code bootstrap CLI ([46d72ac](https://github.com/blicksten/mcp-gateway/commit/46d72ac7af24b609522a4c0db4ffc31eadb1f2cc))
* **phase-16.9:** docs + ADR-0005 + CHANGELOG v1.6.0 + dogfood-smoke CI ([4e39e84](https://github.com/blicksten/mcp-gateway/commit/4e39e84368bafa9c9da65b67edc836f1b8a5fcf8))
* **proxy:** TASK A — gateway in-process idle self-exit (active) ([355dea4](https://github.com/blicksten/mcp-gateway/commit/355dea4dc6ace22ed040b706e10ab6c889ed581b))
* **sap-picker-and-import-mcp:** T-F.1..T-F.6 — Phase F closure (Wave 2 cross-cutting tests + docs + final security pass) ([e94aac6](https://github.com/blicksten/mcp-gateway/commit/e94aac6b55e1d9ed5c76f91df309410aa1ff97db))
* **sap-picker:** T-A.1 REST contracts /api/v1/sap/{picker-snapshot,batch-begin,batch-end} ([fd3f892](https://github.com/blicksten/mcp-gateway/commit/fd3f8925cc824c70e99ed47b962812f96a8b661c))
* **sap-picker:** T-A.2 grammar codegen — Go+TS parsers from YAML SoT ([85cbebc](https://github.com/blicksten/mcp-gateway/commit/85cbebc974b96b10b58101d92e96a6388660447d))
* **sap-picker:** T-A.3..T-A.5 — landscape parser + KP intersection + lifecycle Stop-error fix ([8c262c9](https://github.com/blicksten/mcp-gateway/commit/8c262c9f0cdf5b0ece561eb194704c4be0170b90))
* **sap-picker:** T-B.1..T-B.5 — SAP Picker webview + apply state machine ([0a785c4](https://github.com/blicksten/mcp-gateway/commit/0a785c4a63e6ab799a9da4a392f88bdd5adc9e37))
* **sapcreds:** T-A.0 gokeepasslib/v3 kill-switch PoC ([86734a5](https://github.com/blicksten/mcp-gateway/commit/86734a5b186093d0d6c411c5949b17e9953221da))
* **settings:** T-C.1..T-C.4 — Settings webview with sticky layout, debounce/LRU validator, restart toast, Import-from-mcpDashboard ([49ee409](https://github.com/blicksten/mcp-gateway/commit/49ee409093f28341ea00dd486fd64e636e768ba2))
* **vscode-dashboard+daemon:** SAP picker A+B+C+E UX fixes + close 21 preexisting test fails ([d69ce81](https://github.com/blicksten/mcp-gateway/commit/d69ce81f98491c4de89121480be8c48fc96b0d92))
* **vscode:** P2.3 FM-16 — daemon path resolution prefers ~/go/bin/ ([041f1cc](https://github.com/blicksten/mcp-gateway/commit/041f1cc068175fd517ee82b697c3c3dfa5414236))


### Bug Fixes

* **api,lifecycle:** /check fix-in-cycle — T0.7.1 + P1.5 step 1 finalisation ([5b2d385](https://github.com/blicksten/mcp-gateway/commit/5b2d3855a05c8544e3015880b1889a00cbc79a96))
* **api,patchstate,vscode:** close 6 review findings on commit 0393974 (Path 1 Option B respawn-claim) ([526f515](https://github.com/blicksten/mcp-gateway/commit/526f515efab28a80a9f313644a9e4b9607beee3d))
* **api:** F-8 generalisation — clear WriteDeadline on GET /mcp + /sse ([e43e979](https://github.com/blicksten/mcp-gateway/commit/e43e979a01fb423045a37063ed364025e6898827))
* **api:** F3 — decouple backend lifecycle ops from HTTP request context ([e6924e7](https://github.com/blicksten/mcp-gateway/commit/e6924e780a1cce25a1392f0619853b4ed0eaee3c))
* **api:** F4 — async POST /api/v1/servers returns 202 immediately ([42b370e](https://github.com/blicksten/mcp-gateway/commit/42b370e48fe3083a6589b7e9a406cb6745b663cc))
* **api:** F4-P4 — skip RebuildTools when async Start failed (Sonnet F4 retroactive review) ([7bc38b4](https://github.com/blicksten/mcp-gateway/commit/7bc38b4e6aed38986d9c20d7caaf68aaeadf2347))
* **api:** FM 3 B+C — graceful shutdown + HTTP timeouts that survive long MCP calls ([1138fd2](https://github.com/blicksten/mcp-gateway/commit/1138fd2db7ffa44cbadd8c02d6887b30046ee45c))
* audit-e7618c9c closeout — F-A4 CI assertion + SB-5 test rewrite + SC-C-M1 helper extract (v1.33.0) ([7b1cdaf](https://github.com/blicksten/mcp-gateway/commit/7b1cdafd563447135f7b5b8d171a911724bd498b))
* audit-e7618c9c PAL closeout review — type predicate + double-v regression + magic-number cleanup (v1.33.1) ([a7ca728](https://github.com/blicksten/mcp-gateway/commit/a7ca72866912f85d1ca3531038e613c729ffaf34))
* **ci:** dogfood-smoke — use --env-file to provide absolute npx path ([2b6ef12](https://github.com/blicksten/mcp-gateway/commit/2b6ef12d8d03afc6d5200278c7c611145fcc6676))
* **ci:** normalise CRLF in grammar codegen output for Windows runners ([98c21e3](https://github.com/blicksten/mcp-gateway/commit/98c21e38d68b33e018e3e161c5b31707ee762864))
* **ci:** repair dogfood-smoke — pass --config + use portable reference MCP servers ([180ce0c](https://github.com/blicksten/mcp-gateway/commit/180ce0c6b737d3f2bdac2d813a1d6d17e862042c))
* **ci:** skip Windows-path-only test cases on non-Windows runners ([1ad8cbf](https://github.com/blicksten/mcp-gateway/commit/1ad8cbf1678aaa2792da236f94e352115046c5ee))
* **ci:** TestBaseName — expect node.exe on non-Windows (no .exe strip) ([e0ac90d](https://github.com/blicksten/mcp-gateway/commit/e0ac90d3748efea5beb20cee918b9cbca2be83c8))
* **daemon:** audit Scope A + B — runtime/debug build info + //go:embed compat-matrix ([1b98785](https://github.com/blicksten/mcp-gateway/commit/1b987851905b758949d4b61a2aed7d14b2447465))
* **dashboard:** honest daemon liveness supervision (kill silent-death 60000ms) ([85898df](https://github.com/blicksten/mcp-gateway/commit/85898dfb696fe6dfdafb1f9c83381db2b24dffc9))
* **deploy:** prune orphan extension folders so disk matches registry (only ONE version) ([6da635c](https://github.com/blicksten/mcp-gateway/commit/6da635caa4b9cd03a310aecec8c32a96477e07dd))
* **extension:** audit-e7618c9c — CRITICAL detection.ts schema bug + HIGH cache wipe (v1.29.0) ([3c0f59c](https://github.com/blicksten/mcp-gateway/commit/3c0f59c714a77a0de42ab5fb6866c40a420c202b))
* **extension:** debug-flicker phase 1 — preserve ServerDataCache on refresh error ([8f405f4](https://github.com/blicksten/mcp-gateway/commit/8f405f40e439b163689ac6dd60f5d892328b178d))
* **extension:** debug-flicker phase 2 — cold-start placeholder ([f6fc6d1](https://github.com/blicksten/mcp-gateway/commit/f6fc6d1f334cbfcdaba36e2a956dfa25c37ae779))
* FM 10 + FM 6 from spike 2026-05-11 — comment drift + marketplace cleanup (v1.33.5) ([5f52d56](https://github.com/blicksten/mcp-gateway/commit/5f52d56d91345f651bc467b9037d8cfc4dbb388f))
* **gateway:** apply sessionless-GET storm guard under all transport policies (CV HIGH) ([38d52ad](https://github.com/blicksten/mcp-gateway/commit/38d52ad12c3099c22f53893a3f66e877c72f52d9))
* **gateway:** prevent Claude Code reconnect storm on unreachable HTTP backend ([64aa10b](https://github.com/blicksten/mcp-gateway/commit/64aa10b14941f66f4da77e3a3b4b0d65edf1ac90))
* **gateway:** prevent Claude Code reconnect storm on unreachable HTTP backend ([e70e170](https://github.com/blicksten/mcp-gateway/commit/e70e170c7d1211ec5b743128fd93c1f040479560))
* **gateway:** stop MCP GET-stream 400 hot-loop storm (77 req/s, 832MB log) ([bec0557](https://github.com/blicksten/mcp-gateway/commit/bec0557f911e9582a9c694b5f9e1156c2c978d0a))
* **gateway:** Transports — mount /mcp+/sse as one HTTP family (revert silent /sse loss) ([bdc4788](https://github.com/blicksten/mcp-gateway/commit/bdc47881408221f6fdfa8540a90fbdf001d282df))
* **gateway:** wire GatewaySettings.Transports + remove dead config stub (audit F1/F3/F4) ([d8b02fd](https://github.com/blicksten/mcp-gateway/commit/d8b02fd790ba0944fee13271c7b384f3dfffc151))
* **health,lifecycle,daemon:** P2 dial-timeout + GOMEMLIMIT + P3 Running-&gt;Unreachable transition ([da4c551](https://github.com/blicksten/mcp-gateway/commit/da4c5519540a72269f9e6037c2250c1ee0087967))
* **health:** auto-inject orchestrator REST health defaults so :8100 is cross-checked (STAB-SYN T5.3 / W-5b) ([7863b92](https://github.com/blicksten/mcp-gateway/commit/7863b9298093663f7c45479db7af1fbe6bd2c5ae))
* **health:** F2 — Monitor defers backend restart to suture supervisor ([48b2b78](https://github.com/blicksten/mcp-gateway/commit/48b2b78222a43fbe085ed24ff48f333a61d0cc56))
* **health:** harden sap-gui verdict — fail-closed on unknown SID + trim env ([77ae3ad](https://github.com/blicksten/mcp-gateway/commit/77ae3adb9a1e817986811f20d44b8214fe219a33))
* **health:** P0 gateway crash-stop — sticky circuit + restart backoff + panic isolation ([8dea302](https://github.com/blicksten/mcp-gateway/commit/8dea302de9286dfa0473358dd64132454326cd85))
* **health:** P1 — single shared SAP snapshot per cycle + dual-counter blip mask ([6af1192](https://github.com/blicksten/mcp-gateway/commit/6af1192a12c984667f884b87c6dd8fdb3a1b84cf))
* **health:** parse ALL sap_list_sessions blocks — kills the always-green badge ([789298f](https://github.com/blicksten/mcp-gateway/commit/789298fd4cfc0018a8c5d5fe2b476d9d16378925))
* **health:** per-system SAP GUI verdict — stop reporting all sap-gui-* Running when one system is logged in ([e4f64b4](https://github.com/blicksten/mcp-gateway/commit/e4f64b46760e34e63c7b032114a228b45b3ab8a4))
* **health:** SAP GUI verdict keyed on system id (SID), not shared user+client ([9cb2ca8](https://github.com/blicksten/mcp-gateway/commit/9cb2ca8cbf5e59629a28cf7624899fdb519b6f92))
* **health:** SAP stdio backends never stranded in StatusUnreachable ([0dab765](https://github.com/blicksten/mcp-gateway/commit/0dab7650de189848ebfd1a1b1e6c90275140633f))
* **health:** stale-session recovery — monitor force-restart at 2x threshold when supervisor active ([3c3bd42](https://github.com/blicksten/mcp-gateway/commit/3c3bd42e2d2f3d3eda24dda2315edac1dc34bb39))
* **keepass+sapcreds:** typed ErrNoCredentials sentinel + recycle-bin guard ([37cb635](https://github.com/blicksten/mcp-gateway/commit/37cb635f7c9ef4e216750382d3b46ff52870eaab))
* **lifecycle,api:** P1.3 Task C — Sonnet fix-up cycle (HIGH+MEDIUM+2LOW) ([3042500](https://github.com/blicksten/mcp-gateway/commit/30425000e0aab59e4edaa6f527f37c7598550ea0))
* **lifecycle:** add ResponseHeaderTimeout to HTTP/SSE backend transport ([7ae3484](https://github.com/blicksten/mcp-gateway/commit/7ae34848c0fa847a71e05554f48b5fc29ff12878))
* **lifecycle:** F1 — register ToolListChangedHandler so backend tool updates propagate ([8162eca](https://github.com/blicksten/mcp-gateway/commit/8162eca38a4fa91a68a5bbdaee4d3f425a571b39))
* **lifecycle:** F1-R2 — handleToolsChanged uses Background ctx, not SDK handler ctx ([095d96d](https://github.com/blicksten/mcp-gateway/commit/095d96d94d957aeecded77a3a36dbe4ffd710014))
* **lifecycle:** suppress child console windows on Windows ([e156d42](https://github.com/blicksten/mcp-gateway/commit/e156d42dc54b573024a7585a479d9a4d735febf5))
* **lifecycle:** TASK D' — remediate job-assign failure (close L2 orphan leak) ([cafa2f9](https://github.com/blicksten/mcp-gateway/commit/cafa2f9ad408ec6caa33ffca828eb6393bf99909))
* **mcp-ctl validate:** hide spawned server console on Windows ([81334ba](https://github.com/blicksten/mcp-gateway/commit/81334bac4a612430f522094fa0cc7366b45129b4))
* **mcp-ctl,plugin,catalog,extension:** MCP-lifecycle test campaign — Phase 10 + earlier fixes ([47fc179](https://github.com/blicksten/mcp-gateway/commit/47fc1791ac2f423eee284de265dfd04f33f59f13))
* **mcp-ctl:** activate-for-claude-code now actually works end-to-end ([846f34d](https://github.com/blicksten/mcp-gateway/commit/846f34d98c8f75928bfdf74dba60f6f699ac7cbc))
* **obs:** harden redaction against typed containers, embedded blobs, and unredacted columns ([5537034](https://github.com/blicksten/mcp-gateway/commit/5537034eccc4ce6e2edf62a99a8bbe7a9e855b26))
* **phase-12.A gate:** PAL codereview findings — CRITICAL X-Forwarded-For bypass + 6 others ([a168647](https://github.com/blicksten/mcp-gateway/commit/a168647f09e8501a58b0b51571f1d2b519770b9f))
* **phase-12.B gate:** PAL codereview findings — 1 CRITICAL + 3 HIGH + 5 MEDIUM ([b05f30e](https://github.com/blicksten/mcp-gateway/commit/b05f30e720bf9f17b14512d84708dc4375931592))
* **phase-13 gate:** PAL codereview findings — 6 HIGH + 2 MEDIUM ([c242b35](https://github.com/blicksten/mcp-gateway/commit/c242b351c561afd1dfbd6f20778754fc85e103e8))
* **phase-16:** architect-review findings A-FIN-01..05 — all in-cycle ([983a0da](https://github.com/blicksten/mcp-gateway/commit/983a0dae4e370aa0235112de6c1a454a30e9cd06))
* **saplandscape,vscode:** real SAP Logon XML root is &lt;Landscape&gt;, not &lt;Workspace&gt; (v1.33.8) ([cad5169](https://github.com/blicksten/mcp-gateway/commit/cad5169285f2fc275162be6259d9b1bb1be5dea4))
* security hardening — env blocklist bypass, CRLF injection, URL validation ([b38447d](https://github.com/blicksten/mcp-gateway/commit/b38447debfdfacbad803936a1583be2849dc786c))
* **startup:** decouple TriggerPluginReannounce from StartAll + daemon.log + sweep stale tmp ([02bf947](https://github.com/blicksten/mcp-gateway/commit/02bf9479cbf51ecf215cdbca5e2160a862e09cca))
* **startup:** prevent exit-code-1 crash when multiple windows spawn gateway simultaneously ([58acfb8](https://github.com/blicksten/mcp-gateway/commit/58acfb8d63c0ac2575582c7c756b07189a49773d))
* **startup:** prevent reconnect storm when TriggerPluginReannounce fires before backends ready ([ce26720](https://github.com/blicksten/mcp-gateway/commit/ce26720216511911e16777e2edf48e3856d4d049))
* **startup:** reannounce plugin after RebuildTools to unblock per-backend init ([4d044fc](https://github.com/blicksten/mcp-gateway/commit/4d044fc983acf00f5999ce30eb2faddea379d2fd))
* **test:** cmd/mcp-ctl daemon stop/restart — wire MCP_GATEWAY_ADMIN_TOKEN env ([c08c557](https://github.com/blicksten/mcp-gateway/commit/c08c55741cb73f8f1ddd2c4e9215a2513c665f65))
* thinkdeep gate findings A-1 + E-1 (PLAN-unfreeze-button) ([516f6ce](https://github.com/blicksten/mcp-gateway/commit/516f6ce0486beed3f2ccc8ceac5fd539f49eae5e))
* **vscode-dashboard:** reduce mocha --timeout 30000 -&gt; 10000 + Anthropic issue draft ([d2cd0d8](https://github.com/blicksten/mcp-gateway/commit/d2cd0d817b38a29775772e3cdeb97d4c3435cb36))
* **vscode-dashboard:** SAP Picker — auto-fill mcpGateway.* from mcpDashboard.* + clearer settings docs (v1.33.14) ([d4eee28](https://github.com/blicksten/mcp-gateway/commit/d4eee28afd2f46740fb782a55d5e3d8896f23d2c))
* **vscode-dashboard:** switch SAP Picker to python sap-credentials.py (v1.33.11) ([ab4710a](https://github.com/blicksten/mcp-gateway/commit/ab4710ac8c7824f11d38555405621db8ee29331e))
* **windows:** suppress console windows on every child spawn path ([63c3f20](https://github.com/blicksten/mcp-gateway/commit/63c3f2072a706812584467521e25e516c93b6cb0))

## [Unreleased] — Stability: SAP stdio backends stuck Unreachable on transient SAP/VPN blip

**Incident 2026-06-18.** `vsp-*` and `sap-gui-*` (stdio) backends were flipped to terminal `StatusUnreachable` on a single ~3s SAP-reachability probe failure even though the MCP child was alive and answering MCP ping. `StatusUnreachable` recovery (`maybeProbeUnreachable`) early-returns for stdio (`Config.URL == ""`) and suture returns `ErrDoNotRestart` for Unreachable, so a live, ping-OK backend was stranded until a manual `mcp-ctl servers restart`. The router refuses Unreachable (`Running`/`Degraded` route fine), so all SAP tools silently vanished. Regular backends were unaffected (they ride the MCP-ping → threshold → Degraded → restart loop).

**Root cause:** SAP reachability was a bolted-on third probe level in `checkOne` that (1) had no consecutive-failure threshold — a single blip flipped the whole backend — and (2) reused `StatusUnreachable`, whose recovery/suture handling is HTTP-URL-only, a dead-end for stdio.

### Fixed

- **SAP reachability is now a thresholded `Degraded` signal, never `Unreachable` for stdio** (`internal/health/monitor.go`, `checkOne`): a bad SAP probe (`StatusUnreachable`/`StatusDegraded` from the vsp host-dial or sap-gui session check) feeds a new `serverState.sapProbeFailures` counter against `SAPProbeFailureThreshold` (= `DefaultSAPProbeFailures` = 3). Below threshold → `Running` (absorbs transient VPN/host blips); at/above → `Degraded`. `Degraded` stays routable and is re-probed every health tick, so the backend self-recovers to `Running` when SAP returns. Counter resets on a good probe and in `ResetCircuit`. Restores the invariant that stdio backends never enter `StatusUnreachable`.
- **Removed the start-time vsp SAP TCP pre-check** (`internal/lifecycle/manager.go`, `Start`): it set terminal `StatusUnreachable` and aborted the spawn. The vsp child serves MCP independently of SAP reachability, so SAP is now a runtime `Degraded` signal owned by the monitor. The HTTP (`cfg.URL`) pre-check is untouched.

**Verified:** `go vet ./...` clean; `go test ./...` green (20 packages, incl. new threshold + self-recovery tests in `monitor_sap_test.go`); PAL (gpt-5.1-codex-mini) verdict SHIP; live induced-flap on `vsp-TST`: `running` (blip tolerated) → `degraded` (not `unreachable`) → restore → `running`; post-deploy `mcp-ctl servers list` shows all `vsp-*` running and `sap-gui-*` `degraded` (no open GUI session) instead of stuck `unreachable`, RESTARTS 0.

---

## [Unreleased] — Stability: GET notification-stream 400 hot-loop storm

**Incident 2026-06-14.** With 13 parallel Claude Code sessions the gateway took a flat **~77 transport requests/second** (zero jitter, evenly spread across all 23 MCP surfaces), bloating `daemon.log` to **832 MB** and saturating the MCP router so new `initialize` handshakes timed out (PAL/orchestrator namespaces failed to register in-session). The daemon process itself was healthy — this was a request storm, not a respawn cascade.

**Root cause** (`internal/api/resumable_streamable.go:251-254`): a Claude Code MCP client whose notification GET stream loses its session reopens `GET /mcp/<backend>` with **no `Mcp-Session-Id`**. In stateful mode that is always a protocol error → HTTP 400. The client retries with **no backoff** (upstream anthropics/claude-code#57642) on a ~298ms timer; 13 sessions × 23 backends = the steady 77/s. Same 400→reconnect-storm *class* as "Bug A" below, different trigger (GET notification stream vs. POST-init on an error-state backend).

### Fixed

- **Early-reject GET with empty session id** (`internal/api/server.go`, `mcpTransportPolicy`): the pathological shape (`GET` + empty `Mcp-Session-Id`) is now rejected at the policy layer with a cheap 400, before the session-map lookup / protocol negotiation in the resumable handler. Keyed **strictly** on the empty-header shape — a GET carrying an unknown/stale session id is untouched so it still reaches `tryResurrect` for restart recovery (FM-3). Healthy GET streams always carry a session id post-`initialize`, so they are unaffected.
- **Happy-path transport logs dropped to DEBUG** (`internal/api/server.go`, `logMCPDecision`): `allow-loopback` / `allow-if-bearer` now log at `slog.LevelDebug` (daemon runs at `LevelInfo`, so they drop at the handler). `deny-*` decisions stay at INFO — rare and security-relevant. This removes the 832 MB/day log amplifier at the source.

**Verified live:** post-deploy the storm dropped from 77 req/s to **0 lines in 12s**; `POST initialize /mcp/pal` recovered to **HTTP 200 in 961ms** (was timing out); `GET` with no session id returns 400 in **41ms** (was ~298ms).

**Deferred to a follow-up pipeline:** per-path token-bucket throttle (429 + Retry-After) and a daemon-side size-capped log rotator (today the only rotation is the date-based VS Code `DaemonLogFile`, bypassed on CLI/Task-Scheduler launches).

---

## [Unreleased] — Stability: Claude Code reconnect storm + TCP fast-fail

Two bugs caused Claude Code to disconnect from mcp-gateway every 44 seconds whenever any configured HTTP backend was unreachable (e.g., VPN-dependent `pdap-docs` while VPN is off):

### Bug A — Empty backend stub keeps long-lived streams alive

`RebuildTools()` deleted `perBackendServer["pdap-docs"]` when the backend had 0 tools (StatusError). This caused `GET /mcp/pdap-docs` to return HTTP 400 "no server available". Claude Code treats any HTTP 400 during MCP initialize as a trigger to reinitialize ALL transports (exponential backoff starting at 8s), creating cascading reconnect cycles every ~44s in all active Claude sessions simultaneously.

**Fix** (`internal/proxy/gateway.go`): `RebuildTools()` now keeps an empty stub server for configured backends in error state. The empty stub returns HTTP 200 with 0 tools — Claude Code does not retry on 200. The stub is deleted only when the backend is removed from config entirely. A stale-tool cleanup pass clears any previously registered tools from the stub on error transition.

### Bug B — TCP connect hangs block health monitor for 42 seconds

`lifecycle/manager.go` `Start()` had no TCP connectivity check before calling `connectSafe()`. On Windows, an unreachable host blocked on `connectex` for ~42 seconds. With the health monitor retrying continuously (due to the CR-15 circuit-breaker reset-window bug), this accumulated hundreds of blocked goroutines per day.

**Fix** (`internal/lifecycle/transport.go` + `manager.go`): New `checkTCPReachable(ctx, rawURL, 3s)` helper does a quick TCP dial before the full MCP initialize handshake. On failure: returns `"host unreachable <addr>: ..."` in under 4 seconds instead of 42 seconds.

### Tests

- `TestRebuildTools_ErrorStateBackendKeepsStub`
- `TestStart_HTTPBackend_UnreachableHost_FastFail`
- 6 `TestCheckTCPReachable_*` unit tests
- `TestStart_StdioBackend_NoTCPCheck`
- **Total: 10 new tests, 130/130 passing**

---

## [Unreleased] — Fix: SSE 11-minute disconnect (WriteTimeout generalisation)

**Root cause:** `http.Server.WriteTimeout = 10 * time.Minute` fired on long-lived GET notification streams from Claude Code to the gateway (`/mcp`, `/mcp/*`, `/sse`, `/sse/*`). `ServerOptions.KeepAlive = 60s` pings do NOT reset Go's single-shot per-connection write deadline. Connections dropped after ~660s with "SSE stream disconnected: TimeoutError" → 2 retries → "HTTP connection closed after 692s with errors".

The prior F-8 fix (`handleServerLogs`, lines 1417-1422) already applied `SetWriteDeadline(time.Time{})` for the `/api/v1/servers/{name}/logs` SSE endpoint but the same pattern was missing from the streamable/SSE handlers on the MCP transport routes.

### Fixed

- **Per-connection deadline cleared for GET** (`internal/api/server.go`): Added `clearWriteDeadlineForGET` middleware wrapping the four MCP routes: `/mcp`, `/mcp/*`, `/sse`, `/sse/*`. The middleware calls `http.NewResponseController(w).SetWriteDeadline(time.Time{})` on GET requests only — POST requests retain `WriteTimeout` for slow-write DoS protection (H-001 invariant).

### Tests

- `TestClearWriteDeadlineForGET` (4 unit subtests)
- `TestMCPStreamWriteDeadlineCleared` (integration, WriteTimeout=2s + 4.5s hold)
- `TestMCPPostWriteTimeoutRetained` (H-001 regression guard)
- `TestMCPStreamWriteDeadline_LongRun` (`//go:build long` 720s real-server test)
- **Total: 10+ new tests across both suites**

---

## [Daemon 1.33.6] - 2026-05-13 — Unfreeze-Button Endpoints (Windows-only v1)

**Plan:** [docs/PLAN-unfreeze-button.md](../claude-team-control/docs/PLAN-unfreeze-button.md) (claude-team-control repo) — single-phase, operator-locked v3.

### Added — Gateway daemon

- **`POST /api/v1/claude-code/register-pid`** — accepts `{session_id, pid}` from `hooks/statusline.mjs`, stores in `patchstate.State.sessionPids` (in-memory, no disk persistence). Per-session rate limit 5/min. Rejects PID < 5 (Windows kernel reserves 0-4: System Idle, System, secure System).
- **`POST /api/v1/claude-code/unfreeze`** — accepts `{session_id}` from `patches/porfiry-taskbar.js` when the operator clicks the 🔄 button. Looks up the registered PID, runs `powershell.exe -NoProfile -NonInteractive -Command "Stop-Process -Id <pid> -Force"` with a 5 s timeout, drops the registration on both success and failure (stale PID after natural exit). Per-session rate limit 10/min. 404 when session is not registered.
- **`patchstate.SessionPid` + `RecordSessionPid` / `GetSessionPid` / `RemoveSessionPid`** — three concurrent-safe methods on `patchstate.State` for in-memory PID storage, modeled after the existing heartbeat APIs but without disk persistence (PIDs are transient).
- **`unfreezeExecFunc` injection point** — package-level function variable so tests override the real `Stop-Process` shell-out without spawning processes. Production default uses `exec.CommandContext` with PowerShell.

### Tests

- **8 new Go tests** in `internal/api/claude_code_handlers_test.go`: register happy path / PID=0 → 400 / PID=2 (kernel reserved) → 400 / unfreeze happy path with mocked exec / unfreeze unknown session → 404 / unfreeze exec failure → 500 with stale-registration drop / unfreeze rate limit at compressed-budget bucket / empty session_id → 400 on both endpoints.

### Security

- Webview cannot specify the target PID — daemon resolves session_id → pid via patchState lookup. Compromised webview can only kill its own claude.exe PID (the one registered for its session_id), not arbitrary system processes.
- PID < 5 rejected at registration time to fail fast against kernel-reserved PIDs that Stop-Process cannot kill anyway.
- Reuses existing `claudeCodeCORS` (vscode-webview:// origin echo) + Bearer auth chain; per-session rate limiter prevents budget exhaustion from one session affecting others.

## [Extension 1.33.5] - 2026-05-12 — Server Rename Feature

**Plan:** [docs/PLAN-server-rename.md](docs/PLAN-server-rename.md) — 4-phase plan (Go API → TS Extension Client → TS Extension UI → Documentation + manual E2E).

### Added — Gateway daemon

- **`PATCH /api/v1/servers/{name}` accepts `new_name`** — full rename support on the existing PATCH endpoint, transactional with env / header / disabled updates. `internal/models/types.go::ServerPatch.NewName *string` (pointer so empty string is distinguishable from "field absent"). Response on rename: `200 {"status":"patched","old_name":"{old}","new_name":"{new}"}`. No-op rename (`new_name == name`) preserves the existing `{"status":"updated"}` shape.
- **Plan A ordering** in `handlePatchServer`: `lm.AddServer({new})` → `lm.RemoveServer(r.Context(), {old})` with `context.Background()` rollback on failure → `cfgMu`-protected map swap → auto-start under new name (warn-only) → `RebuildTools` + `TriggerPluginRegen` (R-26 + spike 2026-05-08 routing-bypasses F1: `RebuildTools` is the single propagation channel for clients).
- **SAP refusal via `mcp-gateway/internal/sapname`** — the regex-free codegen package from `docs/grammar/sap-server-name.yaml` (R-21, sap-picker T-A.2) is imported by `internal/api/server.go`. `sapname.IsSAP(name) || sapname.IsSAP(*patch.NewName)` → 400 `"renaming SAP-named servers is not supported"`. No new file, no new regex (CLAUDE.md "Regex Discipline"). Existing env-only / disabled-only PATCHes against SAP-named servers continue to work (SAP non-goal is renaming, not all-mutation).
- **`lifecycle.Manager` test-only hooks**: `SetTestStopHook` + `SetTestRemoveHook` for error injection from the `api` package's rename tests (write-once-before-traffic invariant — production never calls these).
- **Bonus operator-approved fix**: `internal/proxy/gateway.go` adds `KeepAlive: 60 * time.Second` to both the aggregate `/mcp` server and per-backend `mcp.Server` instances. Mitigates Claude Code's 5-min idle MCP disconnect ("SSE stream disconnected: TimeoutError" → 3 strikes → "Closing transport"), empirically verified 2026-05-12 by curl probe (GET /mcp produced zero bytes over 5 min before this fix).

### Added — VSCode extension

- **`mcpGateway.renameServer` command** + `view/item/context` menu entry on the MCP Backends tree (`viewItem` regex whitelist of 7 lifecycle states `running|stopped|degraded|error|disabled|starting|restarting` — deliberately excludes SAP `contextValue`s).
- **`extension.ts` handler** — input box with `validateInput` rejecting empty / unchanged / format-invalid (`SERVER_NAME_RE`) / SAP-shaped names via the exported `parseSapServerName` helper (NOT regex literals — drift Go↔TS structurally impossible because both sides come from the same YAML grammar). Confirm modal showing **preserves summary** {env count, header count, secret count} computed via `credentialStore.listServerCredentials`. On confirm: gateway `patchServer` → on success, `credentialStore.renameServerCredentials` wrapped in try/catch → on throw, **warning toast**: *"Server renamed to '{new}' but {N} credential(s) could not be migrated. They remain under '{old}' in the keychain. Re-import KeePass or re-enter them manually."* `cache.refresh()` always fires on gateway success.
- **`credential-store.ts::renameServerCredentials(oldName, newName)`** — index-first ordering inside `_chainIndexMutation`: STEP 1 commit `newName` index entry FIRST → STEP 2 copy each secret from `mcpGateway/{old}/*` → `mcpGateway/{new}/*` → STEP 3 delete old secrets + remove `oldName` index entry. Crash-mid-rename leaves `{newName: entry-shape}` in the index — recoverable by `reconcile()`.
- **`credential-store.ts::listServerCredentials(server)`** — read-only `{env, headers}` shallow-copy helper (returns `{env:[], headers:[]}` for unknown server).
- **`gateway-client.ts::patchServer`** signature extended with `new_name?` + `add_env?` + `remove_env?` + `add_headers?` + `remove_headers?` (purely additive — existing callers compile + work unchanged).
- **`MockSecretStorage::failAfterNStores(n, error)` + `failAfterNGets(n, error)`** failure-injection knobs (default no-ops; existing call sites byte-identical). Required by Test 16b crash-mid-rename + reconcile recovery.

### Tests

- **25 new Go tests** in `internal/api/server_rename_test.go` covering happy path / collision (409) / invalid name (400) / not-found (404) / SAP refusal both directions (400) / SAP-beats-bad-env validation order / rollback / rollback-of-rollback ERROR log / start-fail warn-only / bad-env short-circuit / plugin-regen failure swallowed / stop-timed-out silent zombie regression guard (F-ARCH-4) / preserves env / combined rename+env atomic / disabled flag / no-op rename returns `{"status":"updated"}` / RebuildTools called and env-only PATCH does NOT call RebuildTools / case-strict invariants (`vsp-DEV` SAP, `random-server` proceed, `Vsp-DEV` proceed, `vsp-dev` proceed) / response shape / rollback ERROR-level log assertion / ValidateServerName on `*new_name`.
- **13 new TS tests** across `credential-store.test.ts`, `gateway-client.test.ts`, `commands.test.ts` covering migrate env+header / missing entry / patchServer with `new_name` / race + stranded-index (F-ARCH-2 option a) / crash-mid-rename + reconcile recoverable (uses `failAfterNStores(1)` knob) / `listServerCredentials` / UI happy path / SAP rejection / cancel input / cancel confirm / API failure / gateway success + creds failure / validateInput rejections.

### Security

- SAP-name detector source-of-truth lives in `docs/grammar/sap-server-name.yaml` (R-21). Both Go and TS sides are emitted from the same YAML — drift impossible. No new regex literals introduced.
- `ValidateServerName` guards `new_name` against injection: 1-64 chars, `[A-Za-z0-9_-]+`, no `__` separator (would collide with tool-namespace token).
- Index-first ordering in `renameServerCredentials` ensures secrets never live under an unindexed key — `reconcile()` can detect and prune partial-rename state on next extension activation.

### Known limitations

- **Orphan secrets after partial-migration failure** (LOW): if the gateway PATCH succeeds but the extension's credential migration throws mid-copy, secrets under `mcpGateway/{old}/*` remain in the keychain (the warning toast names them). Operator must re-import via KeePass or re-enter manually. Tracker: `v17-rename-orphan-audit` for a future `auditOrphanSecrets` command.
- **Stranded index entry after concurrent storeEnvVar**: if `storeEnvVar({old}, K3, v)` lands after `renameServerCredentials({old}, {new})` completes, the old-name index entry is resurrected with the new K3 secret. `reconcile()` cannot prune (K3 secret is genuinely present). Documented in `docs/REVIEW-server-rename.md` Phase 2 §P2-DOC-01.

### Operator action required

After installing this extension version, run **VSCode → Developer: Reload Window** so the new `mcpGateway.renameServer` command + context menu are activated.

---

## [Daemon 1.9.0 + Extension 1.32.0] - 2026-05-10 — Wave 2 (Import-from-Claude)

**Plan:** [docs/PLAN-sap-picker-and-import-mcp.md](docs/PLAN-sap-picker-and-import-mcp.md) Wave 2 (Phases D + E + F).

### Added — Daemon

- **Import-from-Claude REST endpoints** under FROZEN `/api/v1/claude-code/*`
  namespace (R-15 + ADR-0005 §Appendix A):
  - `GET /api/v1/claude-code/import-snapshot?source={cc_global|cc_project|desktop}&project_root=…` — returns rows + per-row gateway-state diff with `drift_fields` + provenance badge.
  - `POST /api/v1/claude-code/import-apply` — per-row copy/move with conflict policy (skip/overwrite), single end-of-batch `TriggerPluginRegen` (R-26 / X2 — closes the N×regen-storm under bulk import).
- **`internal/claudeconfig/`** — `cc_global` / `cc_project` / `desktop` readers with mtime-CAS retry (R-08), unrecognized-field preservation, and lockfile acquisition.
- **`internal/claudeconfig/rawroot.go`** — byte-level scanner that splices new `mcpServers` value into `~/.claude.json` while keeping every other top-level key (`oauthAccount`, `cachedGrowthBookFeatures`, `projects`, …) byte-identical (R-02). Zero regex; rejects duplicate `mcpServers`, non-object roots, and pathological string-escape inputs explicitly.
- **`internal/claudeimport/`** — apply / diff / commandresolve / provenance:
  - Refcounted per-file source-write mutex (`sourceLocks`) — entries deleted at zero waiters so the map is bounded by active concurrent paths, not total paths ever seen (F-02 audit fix).
  - `mutateSourceRemove` mtime-CAS catches concurrent external writers (TS-side reflector) and surfaces `Status=Applied, SourceUpdated=false, Reason="mtime"` (R-31 / X7 — see ANALYSIS §R-31 for the two-layer coordination contract).
  - `commandresolve.go` resolves `npx` / `uvx` / `node` to absolute paths via `os/exec.LookPath`; on Windows strips `.exe`/`.cmd`/`.bat` suffix for canonical name comparison.
  - `provenance.go` — atomic `CreateTemp` + `Rename` write of `~/.mcp-gateway/claude-imported.json`; in-process `sync.Mutex` serialises concurrent appenders; `OpResult.ProvenanceWarning` surfaces non-fatal write failures.
- **`internal/lifecycle/manager.go::RemoveServer`** signature change: now returns `(RemoveResult{Orphan bool, StopErr error}, error)`. `Orphan=true` surfaces when the OS Stop call fails — entry deletion remains unconditional (operator intent honoured even if OS process leaks). Closes R-28 / X4 (Stop-error swallow).

### Added — VSCode extension

- **Import-from-Claude webview** (`mcpGateway.openImportClaude` command):
  - Sources radio (`cc_global` / `cc_project` / `desktop`); refetches on switch.
  - Per-row checkbox + name + transport + command preview; provenance badge `◊ previously imported` + drift badge `⚠ drift: <fields>` + collision badge `◇ name in use`.
  - Action select (copy / move) × Conflict select (skip / overwrite). `move + overwrite` surfaces a red toolbar banner whenever any CHECKED row matches — visual cue is duplicated in the Preview / Apply modal (R-23).
  - Preview button: local-projection of final state per row — no destructive backend round-trip (spec evolution from TASKS T-E.3 — `dry_run` removed in favour of stateless host-side projection; closure record in PLAN-sap-picker-and-import-mcp.md Phase E section).
  - Apply button: 7-state row machine (`idle` / `pending` / `in_progress` / `applied` / `skipped` / `conflict` / `error`); retry-failed-rows captures pre-reset failed-key set so a fresh `idle+checked` row cannot slip through.
  - Host-side `coerceEdits` tamper guard: rejects payloads with `action='duplicate'` / `conflict='merge'` / unknown source / oversized rowKey before the daemon ever sees them.
- **`mcp-ctl install-claude-code`** — unchanged contract; the new endpoints are additive under the existing FROZEN namespace and require no installer flag.

### Documentation

- README — new "SAP Picker" + "Import-from-Claude" sections (Wave 1 + Wave 2 features) and a "Known limitations — webview file dialogs" subsection covering Q3.4 multi-monitor `showOpenDialog` quirk.
- `docs/ANALYSIS.md` — new section "Patterns introduced in Wave 1 + Wave 2" covering R-21 codegen, R-31 reflector hash-CAS coordination, R-03 provenance sidecar, R-02 raw-bytes-splice.
- `docs/ADR-0005-claude-code-integration.md` — Appendix A: additivity proof for the Import endpoints under the existing FROZEN `/api/v1/claude-code/*` namespace.
- `docs/SMOKE-2026-05-07.md` — 13-item manual smoke checklist for Windows + Linux.

### Breaking

None on the wire. `RemoveServer` Go signature change is structurally backward-compatible — new `Orphan` field; callers ignoring it preserve prior behaviour.

### Tag-history note

The daemon's git tag `v1.0.0` was a legacy stale tag from the initial public release; the next git tag jumps to **`v1.9.0`** to align with the ldflags-embedded version users see (`mcp-ctl version`). See [docs/PLAN-sap-picker-and-import-mcp.md](docs/PLAN-sap-picker-and-import-mcp.md) §C OpenQuestion 5 for the full rationale.

## [Daemon 1.8.0 + Extension 1.31.0] - 2026-05-09 — Wave 1 (SAP Picker + Settings)

**Plan:** [docs/PLAN-sap-picker-and-import-mcp.md](docs/PLAN-sap-picker-and-import-mcp.md) Wave 1 (Phases A + B + C).

### Added — Daemon

- **SAP Picker REST endpoints** under new `/api/v1/sap/*` namespace
  (additive under existing claudeCodeCORS + authMW middleware,
  ADR-0003 §csrf-scope precedent — comment in `internal/api/server.go`
  references it explicitly):
  - `GET /api/v1/sap/picker-snapshot` — joined landscape ∪ KeePass
    rows.
  - `POST /api/v1/sap/batch-begin` — opens a 5-minute batch window;
    returns `{batch_id}`.
  - `POST /api/v1/sap/batch-end` — closes the batch + fires single
    `TriggerPluginRegen` + `RebuildTools` (R-26 / X2 fix). 409 on
    nested batches.
- **`internal/saplandscape/parser.go`** — regex-free `encoding/xml`
  parser for `SAPUILandscape.xml` with `<Include>` cycle detection
  (visited map + max depth 8), URL normalisation
  (`%APPDATA%`/`%USERPROFILE%` expansion, `file:///C:/path` → backslash,
  `file://server/share/...` → UNC, `\\?\` long-path passthrough).
  Malformed XML / cycles / missing-include surface as
  `Landscape.Warnings`, never crashes the parser (R-05 / R-06).
- **`internal/sapcreds/keepass.go`** — production `ListEntries(kdbxPath, password, keyfile)` via
  `gokeepasslib/v3` (MIT-licensed, validated in T-A.0 PoC). Recycle-bin
  entries filtered. Locked-vault path returns typed
  `keepass.ErrNoCredentials`.
- **`internal/sapcreds/intersection.go`** — hybrid join: every landscape
  SID returned with `kpMissing: bool` flag; KP-only entries excluded
  (R-14 / R-30 backend).
- **`tools/grammar-gen/`** — codegen pipeline: single YAML SoT at
  `docs/grammar/sap-server-name.yaml` produces both Go
  (`internal/sapname/grammar_gen.go`) and TS
  (`vscode/mcp-gateway-dashboard/src/sap-name-grammar.gen.ts`) parsers.
  Both are regex-free; charcode comparisons only. Staleness check at
  `tools/grammar-gen/check`. CI job `grammar-staleness` in
  `.github/workflows/ci.yml` (NEW — repo's first GitHub Actions
  workflow file). 50 cross-language fixture cases at
  `testdata/sap-name-fixtures.json` shared by Go + TS test suites
  (R-21 / X1 fix).
- **`mcp-ctl credential list-structured`** — new cobra subcommand
  emitting `[{sid,client,user,kpMissing}]` JSON to stdout.
- **`internal/api/server.go`** — `addServerInProcess` /
  `removeServerInProcess` extracted from HTTP handlers; auto-suppress
  `TriggerPluginRegen` + `RebuildTools` when `s.sapBatchActive()` is
  true. Single end-of-batch regen verified by
  `TestSapBatch_SingleRegen` (5 servers added in one batch → exactly
  1 regen).

### Added — VSCode extension

- **SAP Picker webview** (`mcpGateway.openSapPicker` command): hybrid
  picker with virtualized rows (`content-visibility: auto`),
  3-toggle filter (registered/available/no-credentials) with
  degenerate-state guard, per-row VSP+GUI checkboxes (disabled +
  tooltip on `kpMissing` rows — R-30 UI), `[⋮]` expand whose state
  survives filter (R-18) + per-row override fields (vspCommand /
  guiCommand / guiUvProject), batch Apply (concurrency=4) with 9-state
  row lifecycle, retry-failed-rows preserves succeeded rows,
  force-kill button on `removed_with_orphan` rows surfaces
  `removeServerInProcess` Orphan via confirm-dialog (R-28 UI).
- **Settings webview** (`mcpGateway.openSettings`): sticky
  header + footer + scroll body fits 800 px viewport (R-10), Browse
  buttons with `defaultUri` fallback chain
  (`currentValue → parentDir → os.homedir()` — R-17), debounced (300 ms
  trailing) + LRU (TTL=10 s, max 64 entries) live validation (R-11),
  Save batches all changes atomically (any one error rejects entire
  batch, no partial writes), restart-required toast on
  `apiUrl`/`daemonPath`/`authTokenPath`/`claudeConfigSync.{enabled,namespacePrefix,path,aggregateEntryName}`
  with `[Restart Daemon]` action (R-29 / X5),
  `[Import paths from mcpDashboard]` button maps the four legacy
  `mcpDashboard.*` paths to `mcpGateway.*` equivalents (only fills
  empty targets — does not overwrite).
- **4 new `mcpGateway.*` settings** declared in `package.json`:
  `defaultVspCommand`, `defaultGuiUvProject`, `defaultGuiMode`
  (`exec` | `uv`), `uvPath`.
- **Regex-free server-name parsing** — `vscode/mcp-gateway-dashboard/src/sap-detector.ts`
  regex constants `VSP_RE` / `GUI_RE` DELETED; replaced by import
  from generated `sap-name-grammar.gen.ts`.

### Security

- SAP routes mount with `claudeCodeCORS + authMW` only — explicit code
  comment in `internal/api/server.go` references ADR-0003 §csrf-scope
  precedent. Picker is a VSCode-webview origin-restricted call; csrf
  stays off for the same reason as the existing `claude-code/*` group.
- `coerceEdits` tamper guard (Settings + SAP Picker + Import) — host
  validates every webview message envelope BEFORE invoking
  `vscode.workspace.getConfiguration().update()` or the daemon REST
  client. Tampered diffs (`disabled=false` on a `kpMissing` row,
  unknown action enum, oversized rowKey) are dropped at the host
  boundary.

### Breaking

None. All additions are backward-compatible — new REST endpoints under
new path, new settings have sensible defaults, generated parsers
mirror the regex behaviour they replaced.

## [1.9.1] - 2026-04-24

### Added — VSCode extension

- **Pin Claude Code Integration to view title bars** — the `mcpGateway.showClaudeCodeIntegration` command (`$(plug)` icon) is now in the `view/title` menu of all three sidebar views (Gateway daemon, Backends, SAP Systems) at `navigation@50`. Pure discoverability fix — the command itself was already there but only reachable from the command palette.

### Fixed — `mcp-ctl install-claude-code`

- **Marketplace JSON schema** updated for Claude Code CLI 2.1.x: `owner: {name, email?}` as a top-level field, `metadata.{version,description}` nested (not flat), and the file relocated to `installer/.claude-plugin/marketplace.json` so relative plugin `source` paths resolve against the marketplace root.
- **Plugin userConfig fields** in `installer/plugin/.claude-plugin/plugin.json` now carry `type` + `title` so the Claude Code installer renders the configuration prompt.
- **`mcp-ctl` resolves marketplace paths to absolute** (`resolveMarketplacePath()`) before passing to `claude plugin marketplace add`. Previously a relative arg was treated as a `github.com/<owner>/<repo>` shorthand and Claude attempted (and failed) to clone it over SSH.
- **409 ALREADY_INSTALLED** is now a non-fatal branch — the install flow no longer rolls the marketplace back when the plugin is already present.

## [1.9.0] - 2026-04-24

### Added — VSCode extension

- **`mcpGateway.sapSystemsEnabled`** setting (bool, default `false`, scope `window`). Hides the SAP Systems view by default — SAP integration is team-specific and most users of the published extension do not need it. The setting gates four runtime constructions in `activate()`: `SapTreeProvider`, `sapTreeView`, `SapStatusBar`, and the `SapDetailPanel.updateAll` cache-refresh listener. View visibility is driven by a `when: "mcpGateway.sapSystemsEnabled"` clause on both the view entry and its `viewsWelcome` entry, seeded via `executeCommand('setContext', ...)` before view registration so first paint is correct. SAP commands stay registered unconditionally so palette access remains an operator escape hatch.
- **Live-toggle handler** — `onDidChangeConfiguration` updates the context key immediately (view appears/disappears) and surfaces an informational toast with a one-click `Reload Window` action; full provider/status-bar lifecycle requires the reload to take effect.

### Documentation

- README — new `mcpGateway.sapSystemsEnabled` row in the Settings table.
- ROADMAP — new "UX toggles (post-v1.7.x)" section recording this entry.

### Build hygiene

- `.vscodeignore` — added `*.log` exclusion so stray build/test logs in the extension root never end up bundled in the VSIX.

### Breaking

- Users who had the SAP Systems view visible on v1.8.x will see it disappear on upgrade. Re-enable via Settings → `mcpGateway.sapSystemsEnabled: true` and reload the window. SAP commands remain available from the command palette regardless of the setting.

## [1.7.0] - 2026-04-24

### Added — Daemon lifecycle control

- **`POST /api/v1/shutdown`** — auth-gated graceful shutdown endpoint. Returns 202 + `{"status":"shutting_down"}`, flushes response via `http.Flusher` before triggering the root `context.CancelFunc`, idempotent under concurrent requests (returns `already_shutting_down` for re-entry). Wired to the same signal-handler path as `SIGTERM`, so the in-flight errgroup drain and the new 8-second bounded `context.WithTimeout` apply to both exit paths.
- **Extended `/api/v1/health`** — response now includes `started_at` (RFC3339 UTC), `pid`, `version`, and `uptime_seconds` alongside the existing `status`/`servers`/`running`/`auth` fields. All new fields `omitempty` — older clients decode unchanged.
- **`internal/pidfile` package** — atomic PID file acquisition (`O_CREAT|O_EXCL|O_WRONLY` + post-write `Lstat` non-symlink verification, `ErrAlreadyRunning` sentinel). Liveness probe is HTTP-based (`GET /api/v1/health` with 500ms timeout, TLS-aware with `InsecureSkipVerify` for self-signed loopback certs), so stale-reap works identically on Linux and Windows. `DefaultPath` prefers `$XDG_RUNTIME_DIR/mcp-gateway.pid` on Linux, falls back to `os.TempDir()` on other platforms.
- **`mcp-ctl daemon` CLI subcommands** — `start` (spawns detached via `DETACHED_PROCESS|CREATE_NEW_PROCESS_GROUP` on Windows, `Setpgid` on POSIX; polls `/health` for reachability), `stop` (REST `/shutdown` → PID-file-based OS kill fallback with SIGTERM → 2 s wait → SIGKILL escalation on POSIX), `restart` (composed stop + start with connection-error tolerance), `status` (tabwriter table: STATUS / PID / VERSION / STARTED / UPTIME / SERVERS / RUNNING). Uptime formatter handles `Ns` / `Nm Ss` / `Nh Mm Ss` / `Nd Hh Mm` ranges.

### Added — VSCode extension

- **`mcpGateway.restartDaemon` command** — REST-based (works for daemons started externally via `mcp-ctl daemon start`, not just extension-owned children). `DaemonManager.restart()` flow: `shutdown()` → poll `/health` unreachable → cleanup own child handle if any → spawn fresh. Serialised by a new `restarting` mutex with `start()`/`stop()` to prevent auto-start + user-restart races.
- **Gateway tree view** — new `mcpGatewayDaemon` view at the top of the MCP Gateway activity container. Root "Gateway" row with status icon + uptime description, expandable into `PID` / `Version` / `Started` / `Uptime` detail rows. Inline action buttons: start (when offline), stop + restart (when running). Fingerprint collapses uptime into 5-second buckets so the tree doesn't re-render every poll tick.
- **Status bar tooltip** now leads with `**Gateway**: 2h 3m · v1.7.3 · pid 12345` line when `/health` metadata is available. Missing fields are skipped rather than printed as `unknown`.
- **`ServerDataCache.gatewayHealth`** — cache fetches `/servers` and `/health` in parallel via `Promise.allSettled` on the same refresh cycle. `/health` failures don't mark the cache as offline (only `/servers` does); consumers get `gatewayHealth: null` and render "offline".

### Security

- `POST /api/v1/shutdown` is mounted inside the Bearer-auth-required router group alongside all other mutating endpoints. Rejected with 401 without a valid token.
- PID file mode `0600` with post-write `Lstat` check — world-writable `/tmp` symlink attacks rejected.
- `--no-auth` mode caveat documented: with auth disabled, any local process can POST `/shutdown`. Acceptable per existing `MCP_GATEWAY_I_UNDERSTAND_NO_AUTH=1` operator attestation (ADR-0003 §no-auth-escape-hatch).

### Documentation

- `README.md` — new "Managing the daemon" section covering CLI, extension UI, status bar tooltip, and graceful shutdown semantics.

### Breaking

None. All additions are backward-compatible — `HealthResponse` fields use JSON `omitempty` and TypeScript `?`; `DaemonManager.start()`/`stop()` signatures unchanged.

## [1.6.0] - 2026-04-22

### Added

- **Dual-mode gateway** — `/mcp` aggregate + `/mcp/{backend}` per-backend MCP surfaces from a single daemon. Unblocks Claude Code plugin packaging where each backend registers as its own `.mcp.json` entry without breaking clients that depend on the aggregate endpoint.
- **Claude Code Plugin packaging** — `installer/plugin/` ships an installable plugin with `.claude-plugin/plugin.json` (userConfig: `gateway_url` + `auth_token`) and `installer/marketplace.json` for one-command install. The plugin's `.mcp.json` is regenerated from the gateway's live backend list on every REST mutation (atomic tmp+rename, 0600 POSIX / DACL Windows).
- **`mcp-ctl install-claude-code`** — headless bootstrap CLI. Flags: `--mode|--scope|--no-patch|--dry-run|--refresh-token|--check-only`. LIFO rollback on partial failure. Exit codes 0/1/2/3/4 distinguishing usage / gateway-down / token-drift / rollback-executed.
- **Webview patch with native MCP reconnect (Alt-E pattern)** — opt-in. Walks Claude Code's React fiber tree to capture a reference to `session.reconnectMcpServer` (the same native method the `/mcp` panel's Reconnect button calls) and invokes it when the gateway enqueues a reconnect action. Closes the "tools/list caching" bug class (#13646) without patching `extension.js`.
- **`gateway.invoke` universal fallback tool** + `gateway.list_servers` / `gateway.list_tools` meta-tools on the aggregate endpoint. Callable even when the specific tool isn't in the client's current `tools/list` cache.
- **Supported-versions map** — `configs/supported_claude_code_versions.json` tracks `alt_e_verified_versions`. Served via `GET /api/v1/claude-code/compat-matrix`. Dashboard surfaces Mode C (yellow advisory) when the running CC version is unverified.
- **`/api/v1/claude-code/*` REST endpoints** — `patch-heartbeat`, `patch-status`, `pending-actions`, `pending-actions/{id}/ack`, `probe-trigger`, `probe-result`, `plugin-sync`, `compat-matrix`. FROZEN v1.6.0 contract in `docs/api/claude-code-endpoints.md`.
- **VSCode dashboard "Claude Code Integration" panel** — new command `mcpGateway.showClaudeCodeIntegration`. Displays plugin + patch + channel status with a 12-mode failure matrix (A-M, E obsoleted under Alt-E). Buttons: `[Activate for Claude Code]`, `[Probe reconnect]`, `[Copy diagnostics]`. Diagnostics report includes Alt-E metrics (p50/p95 reconnect latency, fiber depth history, dedup recent errors).
- **Slash-command disclaimer** — every auto-generated `.claude/commands/*.md` carries two disclaimer lines below the AUTO-GENERATED marker stating "this is a slash-command prompt template, NOT an MCP server registration" + pointer to the mcp-gateway plugin install path. Closes operator-confusion bug class (#16143). Regression-pinned by test.

### Security

- **CORS policy for `vscode-webview://`** narrowly scoped to `/api/v1/claude-code/*`; rest of `/api/v1` retains existing csrf-protected origin policy. OPTIONS preflight runs BEFORE bearer auth so browsers can preflight without `Authorization` (REVIEW-16 L-02). Unknown origins get 204 WITHOUT `Access-Control-Allow-Origin` — deny by omission.
- **Rate limits** — separate token-bucket limiters on `/patch-heartbeat` (5/min per session_id), `/pending-actions` (60/min per IP), `/patch-status` (60/min per IP). Amortized idle-bucket eviction.
- **Patch state durability (REVIEW-16 M-01)** — pending reconnect actions + recent heartbeats persist to `~/.mcp-gateway/patch-state.json` (0600, atomic tmp+rename) on every mutation. TTL-filtered on daemon startup. Graceful-shutdown path flushes in-flight persists before `lm.StopAll`.
- **Inlined auth token in patched index.js locked to 0600 on POSIX / DACL on Windows** (REVIEW-16 L-03). `mcp-ctl install-claude-code --refresh-token` re-registers plugin + re-applies patch after gateway token rotation (REVIEW-16 M-03).

### Documentation

- `docs/ADR-0005-claude-code-integration.md` — architectural decision record for the hybrid dual-mode + plugin + Alt-E webview-patch approach.
- `docs/api/claude-code-endpoints.md` — FROZEN v1.6.0 REST contract.
- `docs/TESTING-PHASE-16.md` — four-tier test documentation.
- README §"Connecting Claude Code to the Gateway" + §"Commands vs MCP servers".

### Breaking

None. All additions are backward-compatible.

### Known limitations

- **Webview patch is opt-in** and modifies Claude Code's own `webview/index.js`. Operators who decline still get full functionality via manual `/mcp` panel Reconnect.
- **CC version drift** mitigated via `configs/supported_claude_code_versions.json` + dashboard Mode C advisory — unverified versions are warnings, not errors.

## [1.5.0] - 2026-04-20

### Added
- **Server & command catalogs** — first-party JSON catalogs of popular MCP servers (context7, pdap-docs, orchestrator, pal-mcp, sap-gui-control) and matching slash-command templates. Versioned draft-07 JSON Schemas pinned by `$id` (`v1`). Catalogs ship bundled with the extension VSIX; never fetched from the network.
- **Add Server "Choose from catalog" dropdown** — `AddServerPanel` webview now exposes a catalog dropdown above the Name field. Selecting an entry pre-fills transport / url / command / args and renders one empty row per declared `env_keys` / `header_keys` so the operator fills only secret values. `(Custom server)` preserves the pre-catalog free-form flow.
- **Slash-command template enrichment** — `SlashCommandGenerator` injects the catalog's `template_md` body into `.claude/commands/<server>.md` on server transition to `running`. Allow-list substitution of `${server_name}` / `${server_url}`; unknown `${var}` tokens are left literal. Servers without a catalog entry keep the pre-v1.5 bare skeleton unchanged.
- **`mcpGateway.catalogPath` setting** (`type: string`, `default: ""`, `scope: machine`) — optional override path to a directory containing `servers.json` + `commands.json`. Operator path wins when non-empty and the directory exists; otherwise falls back to the bundled catalog under the extension's installation directory.
- **`npm run lint:catalog`** — ajv-cli validation of both seed files against their schemas plus a cross-reference check that every `command.server_name` resolves to a `server.name`. Added as a CI step alongside a VSIX-contents assertion ensuring the four catalog files plus ajv runtime dependencies are packaged.

### Security
- **Host-side re-validation of catalog selection** — `AddServerPanel.handleSubmit` re-loads the catalog and re-runs every field through `validation.ts` helpers before calling `client.addServer()`; forged `catalogId` payloads are rejected before they reach the daemon.
- **No catalog HTML interpolation** — every catalog string reaches the webview via `jsonForScript` and is rendered via `textContent` / `.value` (never `innerHTML`). `escapeHtml` neutralises `<script>`-laden catalog entries; verified by targeted test.
- **1 MiB catalog cap with TOCTOU-safe bounded read** — loader uses `fs.promises.open` + `fileHandle.stat` + bounded `fileHandle.read` on a single file handle, eliminating the swap window between stat and read. Oversized files produce a warning and an empty entry list; `readFile` is never invoked.
- **`scope: machine`** on `mcpGateway.catalogPath` prevents per-workspace catalog override (exfiltration-vector mitigation).
- **`$id` network refusal by design** — ajv is configured with bundled schema files via `addSchema`; catalog `$id`s are documentation-only and never trigger HTTP fetch.

### Breaking-config

- **Half-configured TLS now refuses to start** (T15B.3). Previously, setting
  exactly one of `gateway.tls_cert_path` / `gateway.tls_key_path` silently
  dropped back to plain HTTP — an operator who edited the config and forgot
  the second setting would see no error, assume TLS, and actually run
  cleartext. The daemon now refuses to start with an error message naming
  **both** paths. The wording is deliberately stable (grep target; future
  refactors must keep the string intact):

  > `TLS is half-configured: gateway.tls_cert_path is set but gateway.tls_key_path is empty — both must be set to enable TLS, or both must be empty for plain HTTP`

  Symmetric variant when only `tls_key_path` is set:

  > `TLS is half-configured: gateway.tls_key_path is set but gateway.tls_cert_path is empty — both must be set to enable TLS, or both must be empty for plain HTTP`

  Both variants are stable grep targets — future refactors must keep the
  strings intact. **No grace period** —
  silent plain-HTTP when the operator intended TLS is a security defect, not
  a feature. Installations running with half-finished TLS config from v1.4.0
  must either complete the pair or remove both settings before upgrading.

### Fixes

- **Scanner line-length cap raised from 64KB to 1MB** on both log paths
  (T15A.2a + T15A.2b — atomic pair, F-11 closed). `bufio.Scanner` defaults to
  a 64KB line limit, which silently truncated long lines both in
  `internal/ctlclient/client.go` (SSE client-side, `streamLogsOnce`) and in
  `internal/lifecycle/manager.go` (producer-side, `scanStderr`). The effective
  end-to-end cap is the minimum of the two sites, so fixing only one would
  still leave the user-visible ceiling at 64KB. Both sites now call
  `scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)` with a comment explaining
  the 64KB→1MB trade-off. Closes ROADMAP F-11.

### Hygiene

- **Bearer auth constant-time compare — pad-to-expected-length refactor**
  (T15A.1). `internal/auth/middleware.go` previously called
  `subtle.ConstantTimeCompare([]byte(received), expectedBytes)`, which the Go
  stdlib documents as returning 0 immediately on length mismatch. For the
  fixed 43-char token the practical leakage is 1 bit out of 256 — this is
  **not a security fix**. Landed anyway to remove the recurring PAL-review
  pattern and provide a clean reference for anyone copying the code to a
  variable-length secret: compare a pad-to-expected-length buffer, then do a
  separate `ConstantTimeEq` length check, combine both results
  unconditionally. Existing `TestMiddleware_ConstantTimeOnDifferentLengths`
  pins the coverage shape.

### Tests

- **TLS integration tier** (T15B.1 / T15B.2 / T15B.3). New
  `internal/api/tls_integration_test.go`: generates a CA → leaf cert chain in
  `t.TempDir()`, drives `ListenAndServeTLS`, probes with a custom `RootCAs`
  client pool — asserts 200 on `/api/v1/health` and 401 on an authed route
  without Bearer. Pins the previously-unexercised `ServeTLS` branch. Negative
  tests cover non-loopback + `authEnabled` + no TLS → startup refusal with
  pinned wording, and half-configured TLS refusal in both orderings
  (cert-only, key-only). Runs under the default `go test ./...` path — no
  external prereqs.
- **Windows DACL enforcement tier** (T15C.1). New
  `internal/auth/token_perms_integration_windows_test.go` under the
  `integration` build tag. Uses `LogonUserW` + `ImpersonateLoggedOnUser` via
  `advapi32.dll` to attempt `os.Open` on the token file as a second local
  account; expects `ACCESS_DENIED`. Confirms the token-file DACL is
  **OS-enforced**, not just structurally correct. Gated behind
  `make test-integration-windows` so the default `go test ./...` path is
  unaffected. `runtime.LockOSThread` pin + deferred `RevertToSelf` prevent
  impersonation from bleeding into other goroutines. Skips gracefully when
  `MCPGW_TEST_USER` / `MCPGW_TEST_PASSWORD` env vars are absent.
- **Manual-protocol branch for Windows enforcement** (T15C.2). The
  `windows-latest` GitHub-hosted runner spike
  (`docs/spikes/2026-04-19-windows-latest-impersonate.md`) was deferred — the
  branch cross-compiles clean but the repo's pre-push hook blocks leaking the
  spike branch to the remote. Scoped back to documented manual protocol:
  new `Makefile` target `test-integration-windows` (fail-fast env-var guard)
  plus a three-tier Testing section in the README with the elevated-PowerShell
  operator protocol. No `.github/workflows/ci.yml` change in v1.5.0.

### Documentation

- **README Testing tiers section** (T15D.2). Three-tier table separates what
  each test command proves and what it needs to run: default `go test ./...`
  covers unit + structural + TLS integration; `make test-integration-windows`
  covers the Windows DACL enforcement tier on a pre-provisioned local test
  account. Includes the elevated-PowerShell sequence (`net user /add` → env
  vars → make → `net user /delete`) and the behavior of the integration test
  when credentials are absent (`go test ./...` unaffected;
  `go test -tags integration ./...` skips with a pointer back to the README;
  `make test-integration-windows` fails fast).
- **README Catalogs section** (CD.1). New end-user-facing section documenting
  catalog layout (`servers.json` + `commands.json`), the `$id` version-pinning
  convention, the `mcpGateway.catalogPath` machine-scope override, hard limits
  (1 MiB cap, `v1.*` schema pin, fail-soft on malformed files), and the
  known-limitation note on slash-command edits below line 1 (regeneration
  overwrites edits unless the line-1 marker is removed). Paired with the
  feature entries in `### Added` / `### Security` above.

### ROADMAP

- **F-11 (bufio.Scanner 64KB stderr limit) — CLOSED** in Phase 15.A. Both
  scanner sites (SSE client + stderr producer) raised to 1MB atomically;
  regression tests pin the cap. End-to-end log-line ceiling is now 1MB.

## [1.0.0] - 2026-04-09

### Added
- **Go daemon** (`mcp-gateway`): MCP server lifecycle management for stdio and HTTP/SSE backends
- **CLI** (`mcp-ctl`): full server management, tool calls, log streaming, stdio compliance validation
- **VS Code extension** (`mcp-gateway-dashboard`): tree view, status bar, daemon lifecycle, webview detail panels
- **REST API** (v1): CRUD for servers, tool listing and calls, metrics, SSE log streaming
- Health monitoring with circuit breakers and configurable auto-restart
- Per-server tool budget with `ConsolidateExcess` meta-tool for budget overflow
- `compress_schemas` option: truncate tool descriptions, strip schema examples for token savings
- Environment variable expansion (`${VAR}`) in config with security-restricted fallback allowlist
- KeePass KDBX credential import via CLI (`mcp-ctl credential import-kdbx`)
- Windows Job Objects for automatic child process cleanup on daemon exit
- Installer scripts for Linux, macOS, and Windows with system service registration
- Binary signing with Sigstore cosign and SHA-256 checksum verification
- `GET /api/v1/metrics`: per-server crash counts, MTBF, uptime, token cost estimates
- `mcp-ctl validate`: black-box stdio compliance harness for MCP server onboarding
- API versioning with backward-compatible redirect (`/api/*` -> `/api/v1/*`)
- SAP system auto-detection and grouping by SID (opt-in via settings)

### Security
- CSRF protection via `Sec-Fetch-Site` header validation on mutating requests
- SSE connection limit (max 20 concurrent) to prevent resource exhaustion
- Non-loopback binding blocked without explicit `allow_remote` configuration
- Rate limiting (100 concurrent / 200 backlog) and 1 MB body size limit
- Dangerous environment key blocklist (25+ hijack vectors: `LD_PRELOAD`, `DYLD_INSERT_LIBRARIES`, etc.)
- Header injection prevention (CRLF/NUL validation)
- Atomic config writes (temp file + rename)
