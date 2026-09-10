# P1C — Secret Store Foundation

状态：**P1C DONE / CLOSED；P1D READY**。

P1C adds the platform Secret Store foundation beside the existing Legacy WSa
runtime. It provides a durable encrypted envelope for future connector
credentials and a health/readiness boundary; it does not yet expose credential
CRUD over HTTP or connect any connector, AI provider, normalizer, or delivery
runtime.

## Security boundary and scope

The Secret Store is an explicit, process-owned service. `internal/platformdb`
remains the only SQLite owner; `internal/secretstore` receives repository
records and never opens a second database handle. The Master Key is a separate
file and is never stored in SQLite, ordinary logs, API responses, health
responses, WebSocket messages, audit records, or error envelopes.

P1C does not implement Legacy YAML credential migration, Connector use,
Dashboard credential pages, HTTP secret CRUD, key rotation, KMS/HSM wrapping,
or a general delivery retry queue. A compromised host/root account remains
outside this phase's threat model.

## Migration 005

Migrations `001_core.sql` through `004_admin_auth.sql` are immutable. P1C adds
only `internal/platformdb/migrations/005_secret_store.sql`; the migration
framework discovers an ordered, contiguous list and validates each applied
filename/name/checksum before any schema mutation.

Fresh databases apply `1 → 2 → 3 → 4 → 5`. Existing v4 databases take the
existing pre-migration backup gate, leaving the backup at v4, and then apply v5
in the same migration transaction. A failed backup therefore leaves the live
database at v4 with no v5 tables.

Migration 005 creates:

- `secret_store_state`, a singleton containing only the sentinel envelope
  version, nonce, ciphertext, and timestamps;
- `credentials`, non-secret metadata (`id`, `type`, `label`, `source`,
  `configured`, timestamps);
- `credential_secrets`, one encrypted envelope per credential, with envelope
  version, strictly positive `secret_revision`, nonce, ciphertext, and
  timestamps; the row is foreign-keyed to `credentials` and contains no
  plaintext or key material.

The authoritative schema is always the embedded migration source. The SQL in
`docs/storage/SQLITE_FOUNDATION.sql` is a non-authoritative reference only.

## Master Key lifecycle

The default key path is `ddbot-ai.master.key`; deployments can set
`DDBOT_AI_MASTER_KEY_FILE` (the Docker image may use a mounted path such as
`/run/secrets/ddbot-ai-master-key`). The file format is strict and versioned:

```text
ddbot-ai-master-key-v1:<base64url-without-padding-of-exactly-32-bytes>
```

On a fresh store, the service:

1. creates the parent directory with restrictive permissions;
2. generates exactly 32 bytes with `crypto/rand`;
3. writes with exclusive create, syncs, closes, and applies `0600` to the file
   (`0700` to a newly-created parent where supported);
4. re-reads and strictly parses the persisted representation;
5. only then creates and persists the encrypted sentinel.

An existing key file is never overwritten. If the database already contains a
sentinel or credential ciphertext and the key is missing, malformed, or cannot
be loaded, initialization enters `recovery`; it never generates a replacement
key. A wrong key or a tampered sentinel also enters `recovery`. A database with
metadata but no encrypted data may initialize a new store normally.

## AES-256-GCM envelope

All secret envelopes use the standard-library `crypto/aes`, `crypto/cipher`,
and `crypto/rand` packages. AES-256-GCM uses a fresh random 12-byte nonce for
each encryption. Empty plaintext and plaintext larger than 64 KiB are rejected.
The stored envelope is version 1 and records a strictly increasing revision;
the repository rejects a non-increasing revision so an older ciphertext cannot
silently replace a newer one.

Credential AAD is canonical and domain-separated:

```text
"DDBOT-AI\\x00credential-secret\\x00" || 0x01 ||
u32be(len(credential_id)) || credential_id ||
u32be(len(credential_type)) || credential_type ||
u32be(envelope_version) || u64be(secret_revision)
```

The sentinel uses a separate `secret-store-sentinel` domain and its own fixed
sentinel plaintext. Consequently, swapping a ciphertext between credentials,
changing its type, revision, envelope version, or sentinel domain fails GCM
authentication. Authentication failures exposed by the service are collapsed
to a stable integrity/recovery error rather than leaking crypto internals.

Secret updates encrypt before entering the repository transaction. The
repository atomically replaces the envelope and marks metadata `configured`;
if encryption, the transaction, or the commit fails, the previous envelope
remains intact.

## API and memory boundary

P1C exposes an internal service boundary only:

- metadata reads return `CredentialView` with `configured` and `masked` flags,
  never nonce, ciphertext, plaintext, or key bytes;
- `SetSecret` is the only P1C write operation and requires a ready store;
- `ResolveSecret` is reserved for future trusted connector integration and is
  not wired to an HTTP endpoint;
- metadata remains observable during `recovery`, while secret writes and
  resolution are rejected.

The service does not claim secure memory erasure after a caller receives a
plaintext `[]byte`; callers must keep that value within the trusted connector
boundary and avoid logging it.

## Health, readiness, and Legacy fail-open

The Secret Store implements the small platform dependency check used by
`/healthz` and `/readyz`:

| State | Health | Readiness | Stable code |
| --- | --- | --- | --- |
| ready | healthy / 200 | ready / 200 | `secret_store_ready` |
| recovery | degraded / 200 | not ready / 503 | `secret_store_recovery` |
| unavailable | degraded / 200 | not ready / 503 | `secret_store_unavailable` |

These reports contain only stable status codes. They do not include the Master
Key path, key bytes, sentinel, ciphertext, or low-level filesystem/SQLite
errors. A Secret Store or platform database failure degrades the new platform
surface but does not stop Legacy collection, BuntDB subscriptions, templates,
or normal OneBot delivery.

## P1C boundary

This slice intentionally stops at the encrypted storage and recovery
foundation. Admin/Auth/Session/CSRF remain P1B; the Vue/API shell is P1D;
credential migration, connector/target/subscription management, and any AI
provider or Shadow/ENFORCE runtime belong to later phases.
