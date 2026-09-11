# DDBOT-AI Phase 1 — P1E Final Acceptance Matrix

状态：**P1E ACCEPTANCE IN PROGRESS**（所有本地与 CI 门禁完成后更新为
**P1E DONE / CLOSED**）。

本文件是 Phase 1 的最终验收记录，不是新的运行时设计。验收只覆盖
Platform Foundation：P1A SQLite、P1B Admin Auth、P1C Secret Store 和 P1D
API/Vue shell。Source、Target、Subscription、Connector、AI、Shadow、ENFORCE
和 Legacy Subscription Primary 仍然明确不在范围内。

## Frozen anchors

| 项目 | 固定值 |
| --- | --- |
| 工作分支 | `codex/phase1-foundation` |
| Phase 0 baseline | `a6364e7182ec4eee93dd78e09fe7a7efd92bffab` |
| Phase 0 frozen tag | `phase0-baseline` → `2030834d423e8313df4ae7a937aec0f0badbd443` |
| migration count | 5（001–005，全部 immutable） |
| Compatibility requirement | baseline 21/21、current 21/21、semantic diff 0 |
| build requirement | `CGO_ENABLED=0` linux/amd64、linux/arm64、windows/amd64 |

P1E 不修改 `main`、`phase0-baseline`、`codex/phase0-foundation` 或已发布
001–005 migration，也不创建 `phase1-baseline`。

## Acceptance matrix

状态只允许使用 `PASS`、`FAIL`、`NOT_APPLICABLE` 或
`DEFERRED_BY_FROZEN_SCOPE`。每一项都必须有自动化测试、命令输出或可审计的
代码/文档证据；不得用“手工看起来正常”代替门禁。

| Gate | Status | Evidence |
| --- | --- | --- |
| Legacy AI-OFF compatibility | `PASS` after final run | `compat/cmd verify`、Phase 0 manifest、baseline/current 21 probes |
| Fresh platform database | `PASS` | platformdb fresh migration tests、`internal/p1e/TestPhase1FreshRestartAndRecoveryFlow` |
| Existing database restart | `PASS` | ordered migration idempotency、Secret Store sentinel/key restart、P1E integration test |
| v1–v4 upgrade and backup-before-migrate | `PASS` | platformdb v1→v5、v2→v5、v3→v5、v4→v5 fixtures |
| Backup failure safety | `PASS` | backup failure/collision tests; live schema/history remain unchanged |
| Setup Token / unique admin | `PASS` | adminauth/auth setup, replay, expiry, TOCTOU, concurrency tests |
| Local password recovery | `PASS` after final run | `ddbot-ai admin reset-password`; adminreset/auth/repository reset tests |
| Session/cookie/CSRF/Origin | `PASS` | adminauth/adminapi/session/csrf/origin tests; Login missing Origin remains allowed by default |
| Master Key ownership | `PASS` | Secret Store parent-permission, file collision and exclusive-create tests |
| Secret Store encryption/restart | `PASS` | AES-256-GCM/AAD/revision and restart tests |
| Secret Store Recovery | `PASS` | missing/wrong/malformed key, corrupted sentinel, mismatch, health/readiness and P1E tests |
| Non-sensitive health/readiness | `PASS` | health tests and secret material leak tests |
| Dashboard API shell | `PASS` | adminapi overview/about/auth flow and JSON 404 tests |
| Vue shell deterministic artifact | `PASS` after final run | `npm ci`, typecheck, Vitest, build, committed `internal/webui/dist` diff |
| Browser Setup→Login→Overview→Logout | `PASS` after final run | deterministic browser smoke evidence; server flow also covered by adminapi tests |
| Portability / deployment binding | `PASS` | runtime listen tests and `docs/deployment/DOCKER_LISTENING.md`; no personal coupling |
| AGPL/source metadata | `PASS` | root `LICENSE`, `/api/v2/about`, buildinfo tests |
| Dependency/license audit | `PASS` after final run | locked Go modules and `web/package-lock.json` inventory |
| Three pure-Go release targets | `PASS` after final run | main and platform package archives for all three targets |
| CI release gate | `PASS` after final run | GitHub Actions run URL recorded below |

Known public upstream community-group IDs printed by the Legacy banner are
product compatibility text, not developer environment data; they are retained
to avoid changing Legacy observable behavior. No author-specific filesystem
path, private address, credential, or deployment endpoint is part of the release.

## Local administrator password recovery

The exact local-only command is:

```text
ddbot-ai admin reset-password
```

The command opens only the platform SQLite database, prompts twice on stdin
without accepting a password argument, derives the normal Argon2id hash, and
atomically updates the singleton administrator while revoking existing
sessions. It does not start BuntDB, Legacy acquisition, Dashboard HTTP,
Connector, or AI workers. The database path is `DDBOT_AI_PLATFORM_DB`, falling
back to `ddbot-ai.sqlite`. Failures are reported as one generic CLI message;
passwords, hashes, tokens, session material and database paths are not printed.

## Fresh, restart, upgrade and recovery evidence

Fresh databases apply migrations in order `001 → 002 → 003 → 004 → 005` and
create the latest schema without a meaningless backup. Existing databases with
pending migrations take a deterministic pre-migration backup before any schema
mutation; the backup retains the old schema and data. Latest databases do not
create another backup. Backup destination failure or collision blocks migration
and leaves the live schema/history untouched.

The P1E integration test creates a fresh database, bootstraps the one-time
Setup Token, completes Setup/Login, creates and resolves an encrypted
credential, closes and reopens the same database/key, reuses the persisted
Session, then deliberately creates a metadata/envelope mismatch. The restart
remains Ready; the mismatch enters Recovery. Metadata and authenticated
Dashboard overview remain readable, Resolve/Set are rejected, `/healthz` is
HTTP 200 degraded, and `/readyz` is HTTP 503 with the stable
`secret_store_recovery` code. No recovery path repairs the database or reveals
plaintext/ciphertext/nonce/key-path data.

## Security and frozen boundaries

- Setup uses cheap validation → token hash/precheck → Argon2id → final
  transactional recheck. Invalid, expired, consumed or completed Setup requests
  do not invoke the password hasher.
- Login preserves the P1B Origin contract: a present Origin must match; a
  missing Origin is accepted by default unless deployment configuration
  explicitly requires it. Setup and Logout require Origin.
- Secret Store keeps the AES-256-GCM envelope version, canonical AAD and
  Master Key text format unchanged. Existing ciphertext remains compatible.
- Recovery is fail-open for Legacy Core and fail-closed only for secret-dependent
  operations. P1B Auth remains independently usable.
- Foundation packages have no `init()` registration, global SQLite handle,
  startup goroutine or Legacy hook side effect.

The following remain `DEFERRED_BY_FROZEN_SCOPE`: Credential HTTP CRUD, Legacy
YAML credential migration, Vue domain management pages, Source/Target/
Subscription runtime, Connector Migration Coordinator, AI Provider/Worker,
Shadow, ENFORCE, model cost accounting and durable general delivery retry.

## Final command record

The following block is filled only with commands that actually ran on the
release candidate:

```text
CGO_ENABLED=0 go test -mod=readonly ./...                         [pending]
CGO_ENABLED=0 go vet -mod=readonly ./...                          [pending]
cd adapter && CGO_ENABLED=0 go test -mod=readonly ./...           [pending]
cd adapter && CGO_ENABLED=0 go vet -mod=readonly ./...            [pending]
go run ./compat/cmd verify --baseline a6364e7...                  [pending]
linux/amd64 pure-Go build                                        [pending]
linux/arm64 pure-Go build                                        [pending]
windows/amd64 pure-Go build                                      [pending]
frontend typecheck/test/build and embedded diff                   [pending]
GitHub Actions run                                                [pending]
```

## Completion declaration

This declaration is intentionally not advanced until every row above is
`PASS`/`NOT_APPLICABLE`/`DEFERRED_BY_FROZEN_SCOPE` and the Compatibility,
frontend, adapter, three-target and CI gates have concrete evidence:

```text
P1E DONE / CLOSED
PHASE 1 READY FOR FREEZE
PHASE 2 NOT STARTED
```
