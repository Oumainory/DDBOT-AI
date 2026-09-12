# Feedback and replay

Feedback is an authenticated, audited review of a durable RouteDecision. The
supported labels are `correct_pass`, `correct_drop`, `false_drop`,
`false_pass` and `uncertain_review`. A false DROP for important content blocks
readiness until it is resolved; new feedback is considered at the DROP
boundary, so an existing approval cannot silently continue suppressing content.

## Replay contract

Only an effective DROP with an unexpired `replayable_events` row can be replayed.
The snapshot contains versioned public normalized fields, route/event/source/
subscription identity, target identity, template input, media references and
classifier reference. It contains no provider response, credential, cookie,
token, renderer pointer or source-adapter object.

Replay resolves the current target, renders with the current renderer/template,
creates a new Delivery and sends once. It bypasses AI, Profile and Policy and
never refetches the source. Media fallback is cache → bounded remote fetch from
the allowlisted snapshot URL → text/link. A missing target, expired snapshot or
empty rendered message fails without sending.

The replay command requires an idempotency key. The cached outcome includes
both successful and terminal/unknown results; repeating the same key returns
the original result and does not send again. An `unknown` result is never
automatically retried.
