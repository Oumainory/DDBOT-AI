# ENFORCE

ENFORCE is an opt-in, durable route decision mode. It is never enabled merely
because a policy row says `mode=enforce`.

## Readiness gate

The current `ClassifierRelease` must have all of the following evidence:

| Gate | Minimum |
| --- | ---: |
| real reviewed regression cases | 100 |
| important PASS cases | 40 |
| known important false DROP | 0 |
| DROP precision | 90% |
| parse success | 99% |
| current-release Shadow decisions | 200 |
| reviewed suggested DROP | 50 |
| unresolved critical false DROP | 0 |

Synthetic evaluation cases never satisfy a production gate. A fresh install is
therefore expected to show `LOCKED`.

## Approval and invalidation

An approval is bound to the active release ID, policy digest, profile digest,
readiness evidence snapshot, approver and timestamps. Changing classifier
semantics, profile actions, threshold or hard-safety semantics changes a digest
and invalidates the old approval. Credentials, pricing, timeout and concurrency
do not change semantic approval. Activating another release also invalidates the
old approval. An emergency disable is durable and immediately forces PASS.

## Runtime order

Legacy filters first. For each resolved route, DDBOT-AI resolves the effective
mode and target identity. OFF returns PASS without a model call; SHADOW records
diagnostics and returns PASS; ENFORCE performs one bounded/shared event +
release classification, applies the deterministic policy and hard-safety gate,
checks the current approval, and then either:

1. commits RouteDecision + replay snapshot and suppresses the original send; or
2. records a safe PASS and lets the original Legacy send proceed.

Provider panic, timeout, malformed output, queue-full, SQLite failure,
ambiguous target and stale release all follow the second path. AI never rewrites
the normal message body.
