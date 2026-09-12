-- Release provenance is metadata only.  It never stores a signing secret or
-- a private build artifact.

CREATE TABLE release_metadata (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
