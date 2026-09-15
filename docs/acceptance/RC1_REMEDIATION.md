# v1.0.0-rc1 acceptance remediation

This document records the independent RC1 findings and the bounded fixes on
`fix/rc1-acceptance`. It does not rewrite the historical Phase 5 acceptance
record and does not move `main`, `phase5-baseline`, or `v1.0.0-rc1`.

## Findings

| Finding | Disposition | Remediation/evidence |
| --- | --- | --- |
| F-001 migration hold serializes before eligibility | Fixed | The Legacy hold hook checks for the same active **committing** migration and affected target before converting a message or forward node to a snapshot. No migration, draft/non-committing migration, and unaffected targets remain pure no-ops. `TryHoldForTarget` repeats the migration/state/target check immediately before persistence. |
| F-002 process-local idempotency | Fixed | Admin domain commands and Replay share `platformdb.DurableIdempotencyStore`, backed by the existing SQLite `idempotency_records` contract. Claims are durable `in_progress`; completion stores an allowlisted response and preserves the first-claim seven-day expiry. A restart never re-executes an in-progress command. |
| F-003 mixed-release readiness evidence | Fixed | Readiness evaluation joins results through completed runs for the active `classifier_release_id`, selects one deterministic latest result per case, and counts distinct current-release cases. Shadow decisions, reviewed drops, feedback, and false-drop evidence are release-scoped. |
| F-004 emergency/approval race | Fixed | `PersistDropIfAllowed` is the DROP linearization boundary. The same SQLite transaction rechecks emergency disable, active release, approval identity/revocation, policy/profile digests, and unresolved critical false drops before inserting the decision and replay snapshot. A later disable, approval revoke, or release activation therefore fails open to PASS. |
| F-005 decision recovery | Fixed | Platform startup recovers AI decisions before constructing/accepting the ENFORCE route. A durable `call_started` marker becomes `uncertain_call`/PASS and is never called again; a queued claim remains resumable. Recovery errors are degraded/fail-open and do not stop Legacy. |
| F-006 media quota race | Fixed | A process-local quota critical section covers deduplication, event/global usage reads, bounded eviction, filesystem rename, and metadata/link commits. Durable usage errors fail closed for the cache operation instead of allowing an unbounded write. Concurrent event/global and same-SHA tests cover the boundary. |
| F-007 callback FIFO correlation | Not reproduced under current send contract | The production path creates one `RouteTrace` per concrete Legacy `Notify`; `ConcernNotify` executes each route's `SendMsgObserved` synchronously in its worker, and segmented/@ retry sends are sequential within that invocation. No production-reachable path currently submits concurrent sends carrying the same `RouteTrace`. The existing ordered per-route ledger remains as defensive handling for future overlap. |
| F-008 resolved false-drop gate | Intentionally unchanged | A known important false drop remains evidence against that classifier release even after an operator marks feedback resolved. A new release starts with its own evidence, so old-release evidence cannot block it. |
| F-009 release ref binding | Fixed | Release packaging uses the requested tag/ref for release events and manual validation, runs `release/verify-ref.sh`, and asserts the checked-out commit equals the peeled tag commit. Manual dispatch remains non-publishing. |
| F-010 policy identity | Fixed | The production bridge resolves Source by `(platform, external_id)`, Target by durable domain identity, and the active Source/Target subscription projection by durable IDs. System, Global, Source, Target, and Subscription overlays are applied in order. Missing/ambiguous identity or an unresolved profile fails open to Shadow/PASS and never falls back to an external ID. |
| F-011 third-party inventory | Deferred minor | No large dependency-management system was introduced in this remediation. Existing release notices and the pinned FFmpeg provenance remain authoritative. |
| F-012 JSON body limit | Fixed | All bounded JSON and body-only domain command reads consume at most 1 MiB + 1 byte and reject any byte beyond 1 MiB, including chunked bodies and valid JSON followed by a whitespace tail. |

## Release safety boundaries

- No migration was added. Migrations 001–013 remain immutable.
- The RC1 tag and release remain historical artifacts; this branch does not
  create or publish an RC2 tag/release.
- `main` and `phase5-baseline` remain at the RC1 commit until an independent
  re-audit accepts this candidate.
- Legacy collection, BuntDB subscriptions, rendering, and normal Messenger
  delivery remain authoritative. New failure paths are fail-open for Legacy.

## Verification exception

The targeted Linux race job deliberately omits the root `admin` package. Its
unchanged import graph starts the legacy `lsp/weibo` initializer, which calls
`modern-go/gls` GoID pointer arithmetic and aborts under Go 1.26.2's
checkptr instrumentation before tests execute. The root `admin` package still
runs in the full non-race and locked-baseline gates; the targeted race job
covers the independently runnable Phase 5 packages and does not weaken the
product runtime or compatibility checks.

## Independent re-audit follow-up

The first RC2 candidate was rejected by an independent read-only audit. The
follow-up fixes below are intentionally limited to the three confirmed
blockers and the two verification conditions; they do not move `main`, alter
RC1, or introduce a new migration.

| Finding | Disposition | Evidence in this candidate |
| --- | --- | --- |
| F-002 legacy subscription commands | Fixed | `/api/v1/subs/add` and `/api/v1/subs/remove` now require a durable SQLite `Idempotency-Key` claim before invoking the Legacy subscription service. The claim uses the canonical method/path/query/body fingerprint and `create_subscription`/`delete_subscription` command type. Completion stores only the allowlisted JSON content type and response body. `admin/legacy_idempotency_test.go` closes and reopens the database, proving a replay after restart does not invoke the side effect twice and that a conflicting body returns 409. |
| F-003 mixed-release approval | Fixed | Readiness metrics are evaluated from one SQLite transaction and propagate every evaluation/feedback query error. The approval handler uses the `CurrentReleaseID` returned with the evidence; `SaveEnforceApproval` rechecks the active release inside its write transaction before inserting the approval. `internal/platformdb/ai_repository_test.go` and `phase5_repository_test.go` cover storage-error propagation and stale-release rejection. |
| N-001 policy mutation race | Fixed | `SaveProfile` and `SavePolicy` read the existing semantic row only after beginning their write transaction, then revoke approvals in that same transaction as the mutation. A stale pre-transaction read can no longer leave an approval valid across a policy/profile change. |
| F-010 durable identity integration | Fixed and proven | `admin/phase5_integration_test.go` drives the production `evaluateEnforceRoute` pre-send bridge with a durable Source UUID different from its upstream ID, a durable Target UUID different from the group number, and a durable Subscription projection ID different from the Legacy key. It populates System → Global → Source → Target → Subscription overlays, executes an approved DROP, and asserts the persisted RouteDecision contains all three durable IDs. The replay snapshot now carries the resolved durable Source ID so the final atomic DROP identity check cannot fail-open a valid route. Missing/ambiguous identity remains PASS. |
| N-002 race coverage | Fixed | The targeted Linux race gate continues to run the safe production boundaries (`internal/enforce`, `internal/platformdb`, `internal/migration`, media cache, replay, idempotency and API packages). `internal/enforce/runtime_test.go` now includes a deterministic concurrent emergency-disable plus approval-revoke test while a provider call is in flight; the final durable gate must return PASS. The root `admin` race exclusion remains the documented Go 1.26.2 `modern-go/gls` checkptr baseline exception, not a relaxed product gate. |
| F-011 third-party inventory | Deferred minor | No large dependency-management system was introduced. Existing release notices and pinned FFmpeg provenance remain authoritative. |

F-008 remains intentionally conservative: resolving an operator feedback item
does not erase a known important false-drop from the quality evidence for that
classifier release; a new release starts its own evidence set.
