-- name: DeleteCatalog :exec
-- Every catalog's entries; sources' releases stay.
DELETE FROM catalog_entries WHERE source = '';

-- name: EntryNames :many
SELECT name FROM catalog_entries;

-- name: InsertCatalogEntry :exec
-- OR IGNORE: a release a source has under another name stays the source's.
INSERT OR IGNORE INTO catalog_entries(uuid, name, flavour, title, summary, language, category, tags,
	article_count, media_count, creator, publisher, date, meta4_url, approx_size, source, catalog)
VALUES (@uuid, @name, @flavour, @title, @summary, @language, @category, @tags,
	@article_count, @media_count, @creator, @publisher, @date, @meta4_url, @approx_size, @source, @catalog);

-- name: RebuildIndex :exec
INSERT INTO catalog_fts(catalog_fts) VALUES('rebuild');

-- name: CountCatalog :one
SELECT count(*) FROM catalog_entries WHERE source = '';

-- name: SearchText :many
-- Entries matching an FTS5 query, best first (weights: title, summary,
-- name, tags). langs is a JSON array, matching any; '' or 0 filters nothing.
SELECT e.* FROM catalog_fts(CAST(@match AS TEXT)) JOIN catalog_entries e ON e.id = catalog_fts.rowid
WHERE (json_array_length(@langs) = 0 OR EXISTS (
		SELECT 1 FROM json_each(@langs) l WHERE (',' || e.language || ',') LIKE '%,' || l.value || ',%'))
	AND (CAST(@category AS TEXT) = '' OR e.category = @category)
	AND (CAST(@flavour AS TEXT) = '' OR e.flavour = @flavour)
	AND (CAST(@max_size AS INTEGER) = 0 OR e.approx_size <= @max_size)
ORDER BY bm25(catalog_fts, 10.0, 1.0, 5.0, 2.0), e.name, e.flavour
LIMIT @lim;

-- name: SearchAll :many
-- SearchText without words: by name.
SELECT e.* FROM catalog_entries e
WHERE (json_array_length(@langs) = 0 OR EXISTS (
		SELECT 1 FROM json_each(@langs) l WHERE (',' || e.language || ',') LIKE '%,' || l.value || ',%'))
	AND (CAST(@category AS TEXT) = '' OR e.category = @category)
	AND (CAST(@flavour AS TEXT) = '' OR e.flavour = @flavour)
	AND (CAST(@max_size AS INTEGER) = 0 OR e.approx_size <= @max_size)
ORDER BY e.name, e.flavour
LIMIT @lim;

-- name: EntriesWithPrefix :many
-- Entries whose "name[:flavour]" starts with prefix, in any case.
SELECT * FROM catalog_entries e
WHERE lower(substr(e.name || CASE WHEN e.flavour = '' THEN '' ELSE ':' || e.flavour END, 1, length(CAST(@prefix AS TEXT)))) = lower(@prefix)
	AND (json_array_length(@langs) = 0 OR EXISTS (
		SELECT 1 FROM json_each(@langs) l WHERE (',' || e.language || ',') LIKE '%,' || l.value || ',%'))
ORDER BY e.name, e.flavour
LIMIT @lim;

-- name: EntriesOfCatalog :many
SELECT * FROM catalog_entries WHERE catalog = @catalog AND source = '';

-- name: EntryByUUID :many
SELECT * FROM catalog_entries WHERE uuid = @uuid;

-- name: EntriesByName :many
SELECT * FROM catalog_entries WHERE name = @name ORDER BY flavour;

-- name: Families :many
-- names is a JSON array.
SELECT * FROM catalog_entries WHERE name IN (SELECT value FROM json_each(@names)) ORDER BY name, flavour;

-- name: EntriesByKeys :many
-- keys is a JSON array of [name, flavour] pairs.
SELECT e.* FROM catalog_entries e JOIN json_each(@keys) k
	ON e.name = json_extract(k.value, '$[0]') AND e.flavour = json_extract(k.value, '$[1]');

-- name: Categories :many
SELECT category, count(*) AS n FROM catalog_entries WHERE category != '' GROUP BY category ORDER BY n DESC, category;

-- name: EntryLanguages :many
SELECT language FROM catalog_entries;
