-- Soft deletion keeps metadata and block manifests recoverable until the
-- owner explicitly empties the trash. Trashed roots are detached from the
-- active tree; descendants retain their original parent relationships.

ALTER TABLE files ADD COLUMN deleted_at TEXT;
ALTER TABLE files ADD COLUMN restore_parent_id TEXT;
ALTER TABLE files ADD COLUMN trash_root_id TEXT;

DROP INDEX files_unique_name;
CREATE UNIQUE INDEX files_unique_name ON files(parent_id, name) WHERE deleted_at IS NULL;
CREATE INDEX files_deleted_idx ON files(deleted_at);
CREATE INDEX files_trash_root_idx ON files(trash_root_id);
