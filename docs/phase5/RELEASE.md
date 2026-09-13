# Release packaging

DDBOT-AI publishes a release candidate before a stable product version. The
native matrix is Linux amd64, Linux arm64 and Windows amd64, each with Full and
Lite flavors.

- Lite contains the pure-Go `ddbot-ai` binary and does not bundle FFmpeg. It
  resolves an explicit configured path, then PATH; missing FFmpeg only degrades
  media features.
- Full bundles a pinned FFmpeg executable as a separate runtime process. The
  Go binary remains `CGO_ENABLED=0` and never links `libav*`.
- Every archive contains the binary, quick-start documentation, AGPL license,
  NOTICE/upstream attribution and third-party notices. No database, master key,
  setup token, `.env`, credential, fixture secret or `node_modules` is included.
- `SHA256SUMS` covers every asset.

The release workflow must fail closed unless the FFmpeg version, source URL,
SHA-256 and license/provenance URL are explicitly pinned to an audited source.
No unknown or silently downloaded binary is acceptable. The Docker image is
Full-only, listens internally on `0.0.0.0:15631`, and contains the same notices;
Compose should publish only `127.0.0.1:15631:15631` or use a private proxy
network. Native defaults remain `127.0.0.1:15631`.

The authoritative pin is committed in [`release/ffmpeg.json`](../../release/ffmpeg.json):
BtbN/FFmpeg-Builds `autobuild-2026-09-12-13-12`, FFmpeg
`n9.0.1-29-gad500d59cb`, source commit
`ad500d59cb6e0126add4fcb95afb4e2557c4292c`, `lgpl-static`, and the three
target-specific asset URLs and SHA-256 digests. The workflow also downloads the
provider's `checksums.sha256` and requires each committed digest to appear in
that manifest. Repository Variables, when configured, are assertions and must
match the committed pin; they cannot select a different artifact.

Full artifacts and the Full Docker image include `FFMPEG-PROVENANCE.txt` and
the exact LGPL text from the pinned FFmpeg source commit. The BtbN build
repository/tooling is MIT-licensed; that does not change the LGPL terms of the
bundled FFmpeg executable or the licenses of its other third-party libraries.
The release secret scan covers every extracted file. Some upstream FFmpeg
builds retain test certificate/key marker strings; those matches are accepted
only for the provenance-bound `bin/ffmpeg` (or `bin/ffmpeg.exe`) in a Full
archive. Any matching path elsewhere remains a release-blocking finding.

The release workflow supports a `workflow_dispatch` validation run. A manual
run builds and smoke-tests the six archives and Docker image without pushing
an image or creating a GitHub Release. A real `release` event performs the
same validation before publishing the pre-release assets and GHCR image.

The only product tag in the V1 plan is the pre-release `v1.0.0-rc1`; a stable
`v1.0.0` is intentionally out of scope until real deployment evidence exists.
