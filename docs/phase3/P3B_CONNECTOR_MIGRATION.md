# P3B — Connector Migration, Holds, Pairing and Audit

P3B adds a durable migration coordinator without changing BuntDB authority or
the non-migration Legacy delivery path. `008_connector_migration.sql` and
`009_observation_status.sql` are the authoritative additive migrations;
001–007 remain immutable.

## Migration contract

Connector kind changes are never ordinary connector PATCHes. A migration is a
durable journal with states `draft`, `preparing`, `preflight_ready`,
`committing`, `completed`, `failed`, `rolled_back`, and `recovery_required`.
The journal stores topology and explicit old-target → new-target mappings,
affected target IDs, progress markers and a seven-day rollback deadline. It
stores only credential references, never credential material.

Preflight verifies both connectors, the current projection, every affected
subscription and explicit resolved target mapping. Missing, ambiguous or
unavailable mappings block commit. During commit only affected subscription
mutations are frozen (`migration_in_progress`); source polling and observation
continue.

Affected sends are serialized as `DeliveryPayloadSnapshot` v1 and persisted in
`delivery_migration_holds` with `release_state` claim markers. The payload
contains event/route/target identity, ordered connector-neutral segments,
template identity/digest and timestamps, but no Messenger, renderer, client,
credential or token. A release uses the snapshot rather than re-rendering.
`unknown` is terminal and is never automatically retried. A restart examines
the progress marker: an uncommitted route is rolled back to the old connector,
an already committed route releases only unreleased holds on the new route,
and an indeterminate state is exposed as `recovery_required`. A release claim
is persisted before calling the external sender; a restart marks an abandoned
`releasing` row `unknown` instead of sending it again.

After a route switch, the coordinator reads the Legacy subscription snapshot
again and rebuilds only the SQLite projection. Confirmed mappings are applied
to that projection so a Satori/other connector target is not guessed from a
naked Legacy group number. BuntDB is never overwritten. A projection rebuild
failure leaves the journal recoverable and the platform health surface
degraded.

The passive delivery observation vocabulary records a held send as
`migration_held`; v9 adds this value without turning observation storage into a
general retry queue.

## Telegram pairing

Pairing codes are 32 random bytes, URL-safe, valid for ten minutes and locked
after five failed attempts. SQLite stores only `sha256:<hex>` and consumes a
challenge exactly once. The plaintext is returned once by the create command;
it is not logged, audited, persisted in browser storage or included in error
responses. A Telegram adapter must verify membership, sendability and an
unambiguous group/channel identity before a Target becomes active. Manual
fallback uses the same verifier boundary and cannot create an unverified
Target.

## Audit

High-risk migration and pairing commands append to the `audit_entries` hash
chain. The ledger is append-only through the application API and exposes
`VerifyAuditChain`; metadata is allowlisted/sanitized and never contains
passwords, tokens, secrets or key material.

## API and Dashboard

The API exposes migration draft, preflight, mapping, commit, rollback and held
delivery reads under `/api/v2/connector-migrations`. Every write uses the
existing Auth + Origin + CSRF + Idempotency boundary. Connector kind changes
open the Dashboard Migration Wizard; a direct PATCH returns `migration_required`.
Telegram pairing is initiated from the Connectors page and keeps the one-time
code only in current browser state. Verification is addressed by
`/api/v2/connectors/{connector_id}/pairing/{challenge_id}/verify` (a code-only
`.../pairing/verify` form is also accepted with an optional challenge id); the
handler binds the resource connector to the durable challenge before invoking
the Telegram verifier. Audit retention uses one repository prune operation,
which writes an `audit.prune` anchor and re-chains retained entries atomically.
