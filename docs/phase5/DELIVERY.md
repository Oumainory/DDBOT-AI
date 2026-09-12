# Delivery domain

Phase 5 records actual outbound work separately from routing/filtering. Valid
statuses are:

`planned`, `sending`, `migration_held`, `queued`, `sent`, `partial`,
`not_sent`, `unknown`, `rejected`, `expired`, `abandoned_restart` and
`skipped_empty`.

There is no `ai_dropped` status. A filtered route has a RouteDecision with
`effective_action=drop` and no normal outbound Delivery.

Normal sends commit `planned` before entering the Messenger boundary and then
transition to `sending`. Terminal results are monotonic. On process restart,
durable `sending` rows are marked `abandoned_restart` before new traffic is
observed; this is warning-only recovery and does not block Legacy startup.

`unknown` means the remote outcome cannot be proven and is terminal for
automation. No general SQLite retry daemon exists. The only manual retry
boundary is an authenticated, idempotent command for `not_sent` or `rejected`,
which creates a new attempt row.

Connector migration may own `migration_held` rows and release only its own
holds. The Phase 5 delivery state machine is not a general retry queue.
