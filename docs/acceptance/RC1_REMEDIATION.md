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
