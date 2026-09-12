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

The only product tag in the V1 plan is the pre-release `v1.0.0-rc1`; a stable
`v1.0.0` is intentionally out of scope until real deployment evidence exists.
