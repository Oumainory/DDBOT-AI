# Phase 5 final acceptance

This checklist is the release gate, not a promise that a fresh installation is
ready to suppress messages. Fresh installations must show ENFORCE `LOCKED`
until real Shadow/review evidence and a human approval exist.

- [ ] ENFORCE runtime, readiness metrics, approval/invalidation and emergency
      disable are durable and fail-open.
- [ ] DROP commits RouteDecision and a complete replay snapshot before send
      suppression; ambiguous identity and any storage/AI error PASS.
- [ ] Delivery state machine, restart abandonment, migration-held ownership,
      unknown terminal semantics and manual retry boundary are covered.
- [ ] Feedback, false-DROP blocking, idempotent replay, current rendering and
      no-source-refetch behavior are covered.
- [ ] Media cache limits (10 MiB/file, 30 MiB/event, 2 GiB/global, 7-day
      retention), deduplication, SSRF protections and fallback are covered.
- [ ] BuntDB remains Legacy Subscription authority; migrations 001–010 remain
      immutable and no general retry queue exists.
- [ ] Native/Docker listen defaults, module path, AGPL license and upstream
      attribution are documented.
- [ ] Frontend `npm ci`, typecheck, test, build and embedded-dist diff pass.
- [ ] `CGO_ENABLED=0 go test -mod=readonly ./...`, `go vet`, adapter tests/vet,
      compatibility baseline/current 21/21 with semantic diff 0, and all three
      pure-Go targets pass.
- [ ] Full/Lite artifacts, Docker Full, audited FFmpeg provenance/license and
      SHA256SUMS are present. If provenance cannot be confirmed, stop before
      tagging or publishing.
