#!/usr/bin/env bash
# Verify that a release job checked out exactly the requested tag/ref.  This
# is intentionally a small fail-closed script so every packaging job shares
# the same ref-binding invariant instead of relying on checkout defaults.
set -euo pipefail

release_ref="${1:?release ref is required}"
git fetch --tags --prune origin
expected="$(git rev-list -n 1 "$release_ref")"
actual="$(git rev-parse HEAD)"
test -n "$expected"
test "$actual" = "$expected"
