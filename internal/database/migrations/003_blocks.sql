-- Block storage schema: files reference a content-addressed manifest
-- instead of a raw object; uploads track block size instead of S3
-- multipart state. Pending upload rows have no manifest yet, so
-- files.object_key becomes nullable for kind='file'.

PRAGMA defer_foreign_keys=ON;

-- DROP TABLE files performs an implicit DELETE that cascades into shares;
-- snapshot the rows and restore them after the table swap.
CREATE TABLE shares_backup AS SELECT * FROM shares;

CREATE TABLE files_new (
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
    FOREIGN KEY(parent_id) REFERENCES files_new(id),
    CHECK((kind = 'directory' AND object_key IS NULL) OR kind = 'file')
);

INSERT INTO files_new
SELECT id, parent_id, name, kind, object_key, size, mime_type, etag, status, created_at, updated_at
FROM files;

CREATE TABLE uploads_new (
    id TEXT PRIMARY KEY,
    file_id TEXT NOT NULL,
    block_size INTEGER NOT NULL CHECK(block_size > 0),
    expected_size INTEGER NOT NULL CHECK(expected_size >= 0),
    status TEXT NOT NULL CHECK(status IN ('pending', 'completed', 'aborted', 'failed')),
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    FOREIGN KEY(file_id) REFERENCES files_new(id) ON DELETE CASCADE
);

-- Legacy rows predate block storage; block_size is only a placeholder for
-- them because CleanupExpiredUploads will abort those uploads anyway.
INSERT INTO uploads_new
SELECT id, file_id, COALESCE(part_size, 5242880), expected_size, status, created_at, expires_at
FROM uploads;

DROP TABLE uploads;
DROP TABLE files;

ALTER TABLE files_new RENAME TO files;
ALTER TABLE uploads_new RENAME TO uploads;

INSERT INTO shares SELECT * FROM shares_backup;
DROP TABLE shares_backup;

CREATE UNIQUE INDEX files_unique_name ON files(parent_id, name);
CREATE INDEX files_parent_idx ON files(parent_id);
CREATE INDEX uploads_pending_idx ON uploads(status, expires_at);
