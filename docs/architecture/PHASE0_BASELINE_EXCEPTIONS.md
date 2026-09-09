# Phase 0 Baseline Exceptions

This file is an allowlist, not a place to hide an unexplained failure. A
failure can be added here only after the exact failure is reproduced on the
unmodified `a6364e7182ec4eee93dd78e09fe7a7efd92bffab` checkout in the same
environment and the Linux baseline result is recorded.

## Current allowlist

There are currently **no accepted baseline exceptions**.

## Evidence from the local Windows run

| Tree | Command | Result |
| --- | --- | --- |
| upstream baseline `a6364e7` | `CGO_ENABLED=0 go test ./...` | PASS |
| upstream baseline `a6364e7` | `CGO_ENABLED=0 go test ./...` from `adapter/` | PASS |
| DDBOT-AI current tree | `CGO_ENABLED=0 go test ./...` | PASS |
| DDBOT-AI current tree | `CGO_ENABLED=0 go test ./...` from `adapter/` | PASS |

The commands were run on the development Windows host with the local Go
1.26.8 toolchain on 2026-09-09. An earlier current-tree invocation emitted
Windows `fork/exec ... Access is denied` messages and a
`utils.TestExecWithRunas` process-handle panic, but neither failure reproduced
on the unmodified baseline and both disappeared on the rerun. They are
therefore deliberately **not** treated as exceptions.

## Required fields for a future exception

Every proposed entry must include:

- package and exact test name;
- complete command and environment;
- exact error text;
- result on the unmodified baseline at `a6364e7`;
- result on the Linux baseline;
- whether DDBOT-AI touched the relevant files;
- CI gate treatment and an owner/date for revalidation.

Until all of those fields are present, the failure must fail the Phase 0 gate.
