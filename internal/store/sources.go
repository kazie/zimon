package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/store/gen"
)

// Seen is what was last seen of a source: the validators that tell
// whether it changed since, and a sha-256 given for one of its releases.
type Seen struct {
	Name         string
	Source       string
	ETag         string // a URL's ETag, or a file's size and mtime
	LastModified string
	SHA256       string // given with zimon add --sha256, for release SHA256UUID
	SHA256UUID   string
}

// ErrSameRelease means a source's release is known under another name.
var ErrSameRelease = errors.New("the same release is known under another name")

// PutSource records what was seen of a source and its release e (e.Source
// set), replacing every entry of e's name. It fails with ErrSameRelease
// when a catalog or another source has that release under another name.
func (s *Store) PutSource(ctx context.Context, seen Seen, e catalog.Entry) error {
	return s.tx(ctx, func(q *gen.Queries) error {
		other, err := one(q.OtherKeyOfRelease(ctx, gen.OtherKeyOfReleaseParams{UUID: e.UUID, Name: e.Name}))
		switch {
		case err != nil:
			return err
		case other != nil:
			return fmt.Errorf("%w: %s", ErrSameRelease, catalog.Key{Name: other.Name, Flavour: other.Flavour})
		}
		if err := q.UnindexName(ctx, e.Name); err != nil {
			return err
		}
		if err := q.DeleteEntriesOf(ctx, e.Name); err != nil {
			return err
		}
		id, err := q.InsertSourceEntry(ctx, gen.InsertSourceEntryParams(e))
		if err != nil {
			return err
		}
		if err := q.IndexRow(ctx, id); err != nil {
			return err
		}
		return q.PutSeen(ctx, gen.PutSeenParams(seen))
	})
}

// NoteMetadata records what a source's release says about itself (title,
// summary and so on) in its entry, if that is still the entry of name.
func (s *Store) NoteMetadata(ctx context.Context, e catalog.Entry) error {
	return s.tx(ctx, func(q *gen.Queries) error {
		if err := q.UnindexRelease(ctx, gen.UnindexReleaseParams{Name: e.Name, UUID: e.UUID}); err != nil {
			return err
		}
		if err := q.Describe(ctx, gen.DescribeParams{Title: e.Title, Summary: e.Summary, Language: e.Language,
			Creator: e.Creator, Publisher: e.Publisher, Tags: e.Tags, Name: e.Name, UUID: e.UUID}); err != nil {
			return err
		}
		return q.IndexRelease(ctx, gen.IndexReleaseParams{Name: e.Name, UUID: e.UUID})
	})
}

// Seen returns what was last seen of the source of name, or nil.
func (s *Store) Seen(ctx context.Context, name string) (*Seen, error) {
	return one(each(func(r gen.Source) Seen { return Seen(r) })(s.q.Seen(ctx, name)))
}

// KeepSources forgets the sources, and their releases, of every name not in
// keep. When it forgets one, the next catalog refresh fetches every
// catalog in full: a source kept their entries of its name out.
func (s *Store) KeepSources(ctx context.Context, keep []string) error {
	list := jsonList(keep)
	return s.tx(ctx, func(q *gen.Queries) error {
		if err := q.UnindexSourcesBut(ctx, list); err != nil {
			return err
		}
		if err := q.DeleteSourceEntriesBut(ctx, list); err != nil {
			return err
		}
		n, err := q.DeleteSourcesBut(ctx, list)
		if err != nil || n == 0 {
			return err
		}
		if err := q.SetKV(ctx, gen.SetKVParams{Key: kvCatalogETag, Value: ""}); err != nil {
			return err
		}
		return q.SetKV(ctx, gen.SetKVParams{Key: kvCatalogSynced, Value: ""})
	})
}
