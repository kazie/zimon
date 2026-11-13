-- name: OtherKeyOfRelease :many
-- The key another name has release uuid under, if any.
SELECT name, flavour FROM catalog_entries WHERE uuid = @uuid AND name != @name LIMIT 1;

-- name: UnindexName :exec
INSERT INTO catalog_fts(catalog_fts, rowid, title, summary, name, tags)
SELECT 'delete', c.id, c.title, c.summary, c.name, c.tags FROM catalog_entries c WHERE c.name = @name;

-- name: DeleteEntriesOf :exec
DELETE FROM catalog_entries WHERE name = @name;

-- name: InsertSourceEntry :execlastid
INSERT INTO catalog_entries(uuid, name, flavour, title, summary, language, category, tags,
	article_count, media_count, creator, publisher, date, meta4_url, approx_size, source, catalog)
VALUES (@uuid, @name, @flavour, @title, @summary, @language, @category, @tags,
	@article_count, @media_count, @creator, @publisher, @date, @meta4_url, @approx_size, @source, @catalog);

-- name: IndexRow :exec
INSERT INTO catalog_fts(rowid, title, summary, name, tags)
SELECT c.id, c.title, c.summary, c.name, c.tags FROM catalog_entries c WHERE c.id = @id;

-- name: PutSeen :exec
INSERT OR REPLACE INTO sources(name, source, etag, last_modified, sha256, sha256_uuid)
VALUES (@name, @source, @etag, @last_modified, @sha256, @sha256_uuid);

-- name: Seen :many
SELECT * FROM sources WHERE name = @name;

-- name: UnindexRelease :exec
INSERT INTO catalog_fts(catalog_fts, rowid, title, summary, name, tags)
SELECT 'delete', c.id, c.title, c.summary, c.name, c.tags FROM catalog_entries c
WHERE c.name = @name AND c.uuid = @uuid AND c.source != '';

-- name: Describe :exec
-- What a source's release says about itself.
UPDATE catalog_entries SET title = @title, summary = @summary, language = @language, creator = @creator,
	publisher = @publisher, tags = @tags
WHERE name = @name AND uuid = @uuid AND source != '';

-- name: IndexRelease :exec
INSERT INTO catalog_fts(rowid, title, summary, name, tags)
SELECT c.id, c.title, c.summary, c.name, c.tags FROM catalog_entries c
WHERE c.name = @name AND c.uuid = @uuid AND c.source != '';

-- name: UnindexSourcesBut :exec
-- keep is a JSON array of names.
INSERT INTO catalog_fts(catalog_fts, rowid, title, summary, name, tags)
SELECT 'delete', c.id, c.title, c.summary, c.name, c.tags FROM catalog_entries c
WHERE c.source != '' AND c.name NOT IN (SELECT value FROM json_each(@keep));

-- name: DeleteSourceEntriesBut :exec
DELETE FROM catalog_entries WHERE source != '' AND name NOT IN (SELECT value FROM json_each(@keep));

-- name: DeleteSourcesBut :execrows
DELETE FROM sources WHERE name NOT IN (SELECT value FROM json_each(@keep));
