-- DDBOT-AI Phase 5 bounded media cache metadata.  Bytes live under the
-- configured data/cache directory; SQLite stores only portable metadata.

CREATE TABLE media_cache_entries (
    id TEXT PRIMARY KEY,
    sha256 TEXT NOT NULL UNIQUE,
    size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
    mime_type TEXT NOT NULL,
    storage_key TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    last_accessed_at INTEGER NOT NULL
);

CREATE INDEX idx_media_cache_expiry
    ON media_cache_entries (expires_at, last_accessed_at, id);

CREATE TABLE media_cache_links (
    entry_id TEXT NOT NULL,
    route_decision_id TEXT NOT NULL,
    event_id TEXT NOT NULL,
    source_url TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    PRIMARY KEY (entry_id, route_decision_id),
    FOREIGN KEY (entry_id) REFERENCES media_cache_entries (id) ON DELETE CASCADE
);

CREATE INDEX idx_media_cache_links_event
    ON media_cache_links (event_id, created_at, entry_id);

INSERT INTO retention_metadata (domain, retention_days, updated_at)
VALUES ('media_cache', 7, strftime('%s', 'now'))
ON CONFLICT(domain) DO UPDATE SET retention_days=excluded.retention_days, updated_at=excluded.updated_at;
