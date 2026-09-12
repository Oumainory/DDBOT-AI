# Media cache

The media cache is best-effort support for replay and never changes the
authoritative route decision. SQLite stores metadata and links; bytes live below
the configured cache root using content-addressed SHA-256 storage.

Limits are 10 MiB per file, 30 MiB per event and 2 GiB globally. Entries expire
after seven days and duplicate bytes are linked instead of downloaded again.
Expired/LRU eviction is bounded and safe if a file has already disappeared.

Only `http`/`https` URLs from the persisted public snapshot are accepted. URLs
with embedded credentials, redirects beyond three hops, loopback/private/
link-local/multicast/metadata addresses or an unsafe DNS answer are rejected.
The socket dial path repeats the IP check to close the DNS-rebinding window.
SVG and other active formats are excluded; the allowlist is JPEG, PNG, GIF,
WebP, AVIF and PDF. Cache errors degrade to text/link replay or normal PASS;
they never expose a secret or make Legacy delivery fail.
