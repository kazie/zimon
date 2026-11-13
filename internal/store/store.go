// Package store is zimon's SQLite state: the catalog cache,
// which files are on disk and which downloads are in flight.
package store

import (
	"context"
	"database/sql"
	_ "embed" // the schema
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"modernc.org/sqlite" // the "sqlite" database/sql driver, in pure Go
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/kazie/zimon/internal/store/gen"
)

// Store wraps the database. Its queries are in queries/, made into Go by
// sqlc (gen/).
type Store struct {
	db *sql.DB
	q  *gen.Queries
}

// Open opens the database at path, creating it if needed.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	// Turning a new database into WAL mode can fail fast with SQLITE_BUSY
	// (no busy wait) when another zimon opens it at the same moment; retry.
	for attempt := 1; ; attempt++ {
		s, err := openOnce(path)
		if err == nil || attempt == 50 || !isCode(err, sqlite3.SQLITE_BUSY) {
			return s, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func openOnce(path string) (*Store, error) {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Set("_txlock", "immediate")
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.setUp(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if s.q, err = gen.Prepare(context.Background(), db); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return s, nil
}

// Check runs SQLite's integrity check: nil when the database is sound.
func (s *Store) Check(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA integrity_check(5)")
	if err != nil {
		return err
	}
	defer rows.Close()
	var found []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return err
		}
		found = append(found, line)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(found) == 1 && found[0] == "ok" {
		return nil
	}
	return errors.New(strings.Join(found, "; "))
}

// Close closes the database.
func (s *Store) Close() error { return errors.Join(s.q.Close(), s.db.Close()) }

//go:embed schema.sql
var schema string

// schemaVersion is stored as PRAGMA user_version: a checksum of the schema's
// statements (not its comments or spacing), so a real edit refuses a
// database made before (nothing is released, so there are no migrations;
// see setUp). Never 0, which is a new database.
var schemaVersion = func() int32 {
	var b strings.Builder
	for line := range strings.Lines(schema) {
		code, _, _ := strings.Cut(line, "--")
		b.WriteString(strings.Join(strings.Fields(code), " ") + " ")
	}
	return int32(crc32.ChecksumIEEE([]byte(b.String()))>>1) | 1
}()

// setUp creates the schema in a new database and checks the version of an
// existing one. A database at this version only needs a read; else the
// version is read again inside the (immediate, so exclusive) transaction,
// so two zimons opening a new database at once don't both create it.
func (s *Store) setUp(ctx context.Context) error {
	var version int32
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version == schemaVersion {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // a no-op after Commit
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	switch version {
	case schemaVersion:
		return nil
	case 0:
		if _, err := tx.ExecContext(ctx, schema); err != nil {
			return fmt.Errorf("create the schema: %w", err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
			return err
		}
		return tx.Commit()
	default:
		return fmt.Errorf("the database is from a zimon build with another schema; " +
			"delete it and run zimon again (it then forgets which ZIMs it has)")
	}
}

// tx runs fn in a transaction, with queries that are part of it.
func (s *Store) tx(ctx context.Context, fn func(*gen.Queries) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(s.q.WithTx(tx)); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Get reads a kv value; missing keys return "".
func (s *Store) Get(ctx context.Context, key string) (string, error) {
	v, err := one(s.q.GetKV(ctx, key))
	if v == nil {
		return "", err
	}
	return *v, err
}

// Set writes a kv value.
func (s *Store) Set(ctx context.Context, key, value string) error {
	return s.q.SetKV(ctx, gen.SetKVParams{Key: key, Value: value})
}

// isCode reports whether err is SQLite's error code (its primary code:
// SQLITE_CONSTRAINT also for a UNIQUE constraint).
func isCode(err error, code int) bool {
	e, ok := errors.AsType[*sqlite.Error](err)
	return ok && e.Code()&0xff == code
}

// one returns the first result, or nil when there is none.
func one[T any](xs []T, err error) (*T, error) {
	if err != nil || len(xs) == 0 {
		return nil, err
	}
	return &xs[0], nil
}

// each converts every result with f: each(f)(s.q.Query(ctx, ...)).
func each[T, U any](f func(T) U) func([]T, error) ([]U, error) {
	return func(xs []T, err error) ([]U, error) {
		if err != nil {
			return nil, err
		}
		out := make([]U, len(xs))
		for i, x := range xs {
			out[i] = f(x)
		}
		return out, nil
	}
}

// jsonList is a list as a query takes it: a JSON array (see json_each).
func jsonList[T any](xs []T) string {
	if xs == nil {
		return "[]"
	}
	b, _ := json.Marshal(xs) // of strings, or pairs of them: it can't fail
	return string(b)
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}
