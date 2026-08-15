CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);

CREATE TABLE files (
    id TEXT PRIMARY KEY,
    parent_id TEXT,
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK(kind IN ('file', 'directory')),
    object_key TEXT,
    size INTEGER NOT NULL DEFAULT 0 CHECK(size >= 0),
    mime_type TEXT,
    etag TEXT,
    status TEXT NOT NULL DEFAULT 'ready' CHECK(status IN ('pending', 'ready', 'deleting', 'failed')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY(parent_id) REFERENCES files(id),
    CHECK((kind = 'directory' AND object_key IS NULL) OR (kind = 'file' AND object_key IS NOT NULL))
);

CREATE UNIQUE INDEX files_unique_name ON files(parent_id, name);
CREATE INDEX files_parent_idx ON files(parent_id);

CREATE TABLE uploads (
    id TEXT PRIMARY KEY,
    file_id TEXT NOT NULL,
    mode TEXT NOT NULL CHECK(mode IN ('single', 'multipart')),
    s3_upload_id TEXT,
    part_size INTEGER,
    expected_size INTEGER NOT NULL CHECK(expected_size >= 0),
    status TEXT NOT NULL CHECK(status IN ('pending', 'completed', 'aborted', 'failed')),
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    FOREIGN KEY(file_id) REFERENCES files(id) ON DELETE CASCADE
);

CREATE INDEX uploads_pending_idx ON uploads(status, expires_at);

CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);

CREATE INDEX sessions_token_idx ON sessions(token_hash);
CREATE INDEX sessions_expiry_idx ON sessions(expires_at);

CREATE TABLE settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

INSERT INTO files (id, parent_id, name, kind, status, created_at, updated_at)
VALUES ('00000000-0000-0000-0000-000000000000', NULL, '', 'directory', 'ready', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
