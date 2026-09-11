# DDBOT-AI Phase 3 Final Acceptance

Status: **PHASE 3 IMPLEMENTATION DONE / READY FOR FREEZE** (AI not started).

The Phase 3 baseline includes the P3A Source/Target/Connector domain and
rebuildable BuntDB projection plus the P3B durable migration coordinator.
Migration state, explicit Target mapping, preflight, affected-subscription
freeze, migration-held snapshot persistence, release claim markers, startup
recovery, failure release, and the seven-day rollback contract are documented
in [P3B Connector Migration](./P3B_CONNECTOR_MIGRATION.md).

The same baseline includes hash-only, one-time Telegram pairing with bounded
failures and verified manual fallback, and an append-only audit hash chain.
No general delivery retry queue was added. BuntDB remains the only Legacy
subscription authority; SQLite projections remain rebuildable. Observation and
non-migration Legacy compatibility are unchanged.

Acceptance gates:

- frontend `npm ci`, typecheck, tests, build and embedded dist check;
- `CGO_ENABLED=0 go test -mod=readonly ./...` and `go vet`;
- adapter test/vet;
- Compatibility baseline/current 21/21 with semantic diff 0;
- Linux amd64, Linux arm64 and Windows amd64 pure-Go builds;
- GitHub Actions final run green.

Phase 4 AI Shadow, providers, classifiers, profiles and ENFORCE are explicitly
deferred and have no runtime or dependency in this baseline.
