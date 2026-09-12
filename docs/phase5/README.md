# Phase 5 — ENFORCE / Feedback / Replay / Release

Phase 5 adds the last optional decision layer to DDBOT-AI. Legacy collection,
BuntDB subscriptions, rendering and OneBot delivery remain authoritative. The
new layer can record observations and durable decisions, but it may suppress a
normal send only after an approved ENFORCE decision and its replay snapshot are
committed in SQLite.

## Safety invariants

- AI, storage, timeout, queue, parsing and identity failures are fail-open:
  the original Legacy message is sent.
- OFF never calls a model. SHADOW may classify asynchronously but never changes
  delivery. ENFORCE is locked until the current release, readiness evidence,
  policy/profile digests and a human approval all match.
- A DROP is a RouteDecision, not a Delivery status. The transaction writes the
  RouteDecision and a public replayable snapshot together before suppression.
- `unknown` deliveries are terminal for automation and are never retried by a
  background worker. Manual retry is limited to `not_sent` and `rejected`.
- Replay uses only the durable snapshot, current renderer and an explicitly
  resolved target. It does not refetch a source event or run AI/policy again.
- SQLite remains a Phase 2–5 diagnostic/decision store. BuntDB remains the
  sole authority for Legacy Subscription state.

The authoritative Phase 5 schema is the ordered migration history in
`internal/platformdb/migrations/011_enforce_delivery_feedback.sql`,
`012_media_cache.sql` and `013_release_metadata.sql`. These migrations are
additive; migrations 001–010 are immutable.

## Operating documents

- [ENFORCE](./ENFORCE.md) — readiness, approval, kill switch and decision flow.
- [Feedback and Replay](./FEEDBACK_REPLAY.md) — review, correction and manual
  replay boundaries.
- [Media Cache](./MEDIA_CACHE.md) — bounded public-media cache and fallback.
- [Delivery](./DELIVERY.md) — durable state machine and restart semantics.
- [Release](./RELEASE.md) — Full/Lite packaging and provenance requirements.
- [Final acceptance](./PHASE5_FINAL_ACCEPTANCE.md) — the complete gate list.
