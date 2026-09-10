-- DDBOT-AI P1C Secret Store Foundation.
-- Plaintext secrets and Master Keys are intentionally absent from this schema.

CREATE TABLE secret_store_state (
    singleton INTEGER PRIMARY KEY
        CHECK (singleton = 1),
    envelope_version INTEGER NOT NULL
        CHECK (envelope_version > 0),
    sentinel_nonce BLOB NOT NULL,
    sentinel_ciphertext BLOB NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE credentials (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    label TEXT NOT NULL,
    source TEXT NOT NULL,
    configured INTEGER NOT NULL DEFAULT 0
        CHECK (configured IN (0, 1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE credential_secrets (
    credential_id TEXT PRIMARY KEY,
    envelope_version INTEGER NOT NULL
        CHECK (envelope_version > 0),
    secret_revision INTEGER NOT NULL
        CHECK (secret_revision > 0),
    nonce BLOB NOT NULL,
    ciphertext BLOB NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    FOREIGN KEY (credential_id) REFERENCES credentials (id) ON DELETE CASCADE
);

CREATE INDEX idx_credentials_type
    ON credentials (type);

CREATE INDEX idx_credentials_source
    ON credentials (source);

CREATE INDEX idx_credentials_updated
    ON credentials (updated_at);
