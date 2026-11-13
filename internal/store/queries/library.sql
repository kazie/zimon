-- name: DropFilesAt :exec
-- Rows of other files at path: the bytes there are now uuid's.
DELETE FROM files WHERE path = @path AND uuid != @uuid;

-- name: SupersedeKey :exec
UPDATE files SET state = @superseded WHERE name = @name AND flavour = @flavour AND state = @active AND uuid != @uuid;

-- name: PutFile :exec
INSERT OR REPLACE INTO files(uuid, zim_uuid, name, flavour, path, size, sha256, date, title, summary, language,
	category, tags, article_count, media_count, creator, publisher, state, added_at, verified_at)
VALUES (@uuid, @zim_uuid, @name, @flavour, @path, @size, @sha256, @date, @title, @summary, @language,
	@category, @tags, @article_count, @media_count, @creator, @publisher, @state, @added_at, @verified_at);

-- name: Files :many
-- Newest first within each key.
SELECT * FROM files ORDER BY name, flavour, date DESC, added_at DESC;

-- name: FileByUUID :many
SELECT * FROM files WHERE uuid = @uuid;

-- name: FileOfRelease :many
-- A file of release uuid: its catalog UUID or the one in its header.
SELECT * FROM files WHERE uuid = @uuid OR zim_uuid = @uuid LIMIT 1;

-- name: FilesByKey :many
SELECT * FROM files WHERE name = @name AND flavour = @flavour ORDER BY date DESC, added_at DESC;

-- name: ActiveFiles :many
SELECT * FROM files WHERE state = @active ORDER BY name, flavour;

-- name: ActiveFile :many
SELECT * FROM files WHERE name = @name AND flavour = @flavour AND state = @active LIMIT 1;

-- name: SetFilePath :exec
UPDATE files SET path = @path WHERE uuid = @uuid;

-- name: DeleteFile :exec
DELETE FROM files WHERE uuid = @uuid;

-- name: MarkVerified :exec
UPDATE files SET verified_at = @verified_at WHERE uuid = @uuid;

-- name: StartDownload :exec
-- A new download, unless one of the release is there already.
INSERT INTO downloads(uuid, name, flavour, filename, size, pulled, meta4, started_at, updated_at)
VALUES (@uuid, @name, @flavour, @filename, @size, @pulled, @meta4, @now, @now)
ON CONFLICT(uuid) DO NOTHING;

-- name: Checkpoint :exec
UPDATE downloads SET verified_bytes = @verified_bytes, hash_state = @hash_state, error = '', updated_at = @now WHERE uuid = @uuid;

-- name: SetDownloadSource :exec
UPDATE downloads SET pulled = @pulled, meta4 = @meta4, updated_at = @now WHERE uuid = @uuid;

-- name: SetDownloadError :exec
UPDATE downloads SET error = @error, updated_at = @now WHERE uuid = @uuid;

-- name: DeleteDownload :exec
DELETE FROM downloads WHERE uuid = @uuid;

-- name: DownloadByUUID :many
SELECT * FROM downloads WHERE uuid = @uuid;

-- name: Downloads :many
-- Without the blobs (hash_state, meta4): DownloadByUUID has them.
SELECT uuid, name, flavour, filename, size, verified_bytes, error, pulled FROM downloads ORDER BY started_at;
