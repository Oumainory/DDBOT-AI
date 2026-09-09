# Phase 0 compatibility baseline

This directory records the behavior contract captured from DDBOT-WSa
`next-dev` at commit `a6364e7182ec4eee93dd78e09fe7a7efd92bffab`.

`manifest.json` and `fixtures/*.json` are the checked-in baseline artifacts.
The manifest maps each required observable area to existing upstream tests;
each JSON fixture fixes a replay scenario, expected observable behavior, and
the normalization rules allowed during baseline/current diffing. The fixture
vectors are deliberately separate from the implementation so a future runner
can execute the same scenarios against the baseline and current binaries.

The compatibility gate compares only externally meaningful behavior:

- command replies and canonicalized BuntDB state;
- whether a message is sent, filtered, queued, or dropped;
- message element content and ordering;
- template fallback behavior;
- offline queue transitions, expiry, and unknown-result handling;
- chunk ordering and partial-send behavior;
- representative Bilibili and Twitter event output.

Wall-clock timestamps, random request IDs, temporary paths, and internal
Observation/Audit records are not behavioral outputs and must be normalized by
the future capture runner. Message content, target identity, send count,
chunk ordering, filter result, and BuntDB state must never be ignored. The
Phase 0 Go test validates that every manifest entry has a reviewable fixture;
it does not pretend to be a second implementation of the upstream bot.

Any intentional Legacy behavior change requires an explicit specification
change and a reviewed fixture update. Observation, AI, Dashboard, and
Connector work may not silently update the baseline.
