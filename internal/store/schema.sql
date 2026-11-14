-- zimon's database; sqlc reads it too. Another schema's database is refused (no migrations yet, see
-- store.go), so since v0.1.0 an edit here breaks every library: new state goes into kv. Mind the line
-- count too: schemaVersion counts lines, comments included (TestSchemaIsReleased).

CREATE TABLE kv (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE catalog_entries (
	id            INTEGER PRIMARY KEY,        -- the rowid, which the search index (catalog_fts) is keyed by
	uuid          TEXT NOT NULL UNIQUE,
	name          TEXT NOT NULL,
	flavour       TEXT NOT NULL DEFAULT '',
	title         TEXT NOT NULL DEFAULT '',
	summary       TEXT NOT NULL DEFAULT '',
	language      TEXT NOT NULL DEFAULT '',
	category      TEXT NOT NULL DEFAULT '',
	tags          TEXT NOT NULL DEFAULT '',
	article_count INTEGER NOT NULL DEFAULT 0,
	media_count   INTEGER NOT NULL DEFAULT 0,
	creator       TEXT NOT NULL DEFAULT '',
	publisher     TEXT NOT NULL DEFAULT '',
	date          TEXT NOT NULL DEFAULT '',
	meta4_url     TEXT NOT NULL DEFAULT '',
	approx_size   INTEGER NOT NULL DEFAULT 0,
	source        TEXT NOT NULL DEFAULT '',   -- '' for a catalog's; else the URL or file of library.toml's [sources]
	catalog       TEXT NOT NULL DEFAULT ''    -- the catalog it came from; '' for a source's
);
-- The catalog lists only the latest release of each name+flavour.
CREATE UNIQUE INDEX catalog_entries_key ON catalog_entries(name, flavour);

CREATE VIRTUAL TABLE catalog_fts USING fts5(
	title, summary, name, tags,
	content='catalog_entries', content_rowid='id',
	tokenize='unicode61 remove_diacritics 2'
);

-- files: ZIMs on disk. Metadata is copied from the catalog at download
-- time so library.xml still works if the catalog later drops the entry.
CREATE TABLE files (
	uuid          TEXT PRIMARY KEY,           -- catalog UUID
	zim_uuid      TEXT NOT NULL DEFAULT '',   -- UUID in the file's header (normally the same)
	name          TEXT NOT NULL,
	flavour       TEXT NOT NULL DEFAULT '',
	path          TEXT NOT NULL,              -- relative to library_dir
	size          INTEGER NOT NULL,
	sha256        TEXT NOT NULL DEFAULT '',
	date          TEXT NOT NULL DEFAULT '',
	title         TEXT NOT NULL DEFAULT '',
	summary       TEXT NOT NULL DEFAULT '',
	language      TEXT NOT NULL DEFAULT '',
	category      TEXT NOT NULL DEFAULT '',
	tags          TEXT NOT NULL DEFAULT '',
	article_count INTEGER NOT NULL DEFAULT 0,
	media_count   INTEGER NOT NULL DEFAULT 0,
	creator       TEXT NOT NULL DEFAULT '',
	publisher     TEXT NOT NULL DEFAULT '',
	state         TEXT NOT NULL,           -- active | superseded
	added_at      TEXT NOT NULL,
	verified_at   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX files_key ON files(name, flavour);

-- The blobs come last: SQLite then reads the other columns without
-- walking a large .meta4's overflow pages.
CREATE TABLE downloads (
	uuid           TEXT PRIMARY KEY,
	name           TEXT NOT NULL,
	flavour        TEXT NOT NULL DEFAULT '',
	filename       TEXT NOT NULL,           -- the ZIM's file name; the partial is <partial dir>/<uuid>.part
	size           INTEGER NOT NULL,
	verified_bytes INTEGER NOT NULL DEFAULT 0,
	error          TEXT NOT NULL DEFAULT '',
	started_at     TEXT NOT NULL,
	updated_at     TEXT NOT NULL,
	hash_state     BLOB,                   -- sha-256 state at verified_bytes
	meta4          BLOB NOT NULL,          -- empty for a pull or a source
	pulled         INTEGER NOT NULL DEFAULT 0 -- 1: not from a catalog's .meta4 (zimon pull, a source)
);

-- What was last seen of each ZIM in library.toml's [sources]; its release
-- is the catalog_entries row with the same name and source.
CREATE TABLE sources (
	name          TEXT PRIMARY KEY,
	source        TEXT NOT NULL,
	etag          TEXT NOT NULL DEFAULT '',  -- a URL's, or a file's size and mtime
	last_modified TEXT NOT NULL DEFAULT '',
	sha256        TEXT NOT NULL DEFAULT '',  -- given with zimon add --sha256, for the release it was given for
	sha256_uuid   TEXT NOT NULL DEFAULT ''
);

-- API tokens for zimon serve's admin page and remotes.
CREATE TABLE tokens (
	name         TEXT PRIMARY KEY,
	sha256       TEXT NOT NULL UNIQUE,   -- of the token: the token itself is never stored
	created_at   TEXT NOT NULL,
	last_used_at TEXT NOT NULL DEFAULT ''
);

-- Jobs the server runs for the API and the admin page.
CREATE TABLE jobs (
	id          INTEGER PRIMARY KEY,
	kind        TEXT NOT NULL,             -- add, level, remove, apply, update, prune, verify, refresh
	summary     TEXT NOT NULL DEFAULT '',  -- what was asked, in words
	by          TEXT NOT NULL DEFAULT '',  -- the token's name
	state       TEXT NOT NULL,             -- queued, running, done, failed, canceled, interrupted
	error       TEXT NOT NULL DEFAULT '',
	log         TEXT NOT NULL DEFAULT '',  -- what it did, one line each
	created_at  TEXT NOT NULL,
	started_at  TEXT NOT NULL DEFAULT '',
	finished_at TEXT NOT NULL DEFAULT ''
);
