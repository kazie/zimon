package store

import (
	"context"
	"testing"

	"github.com/kazie/zimon/internal/catalog"
)

func TestSources(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	mine := catalog.Entry{UUID: "m1", Name: "mysite_en_all", Title: "My site", Date: "2026-10-01", Source: "https://example.org/mysite.zim"}
	if err := s.PutSource(ctx, Seen{Name: mine.Name, Source: mine.Source, ETag: `"a"`}, mine); err != nil {
		t.Fatal(err)
	}
	// A catalog refresh keeps it, and leaves out the catalog's flavours of its name.
	clash := catalog.Entry{UUID: "k1", Name: "mysite_en_all", Title: "Kiwix's", Date: "2026-11-01", Meta4URL: "https://x/y.meta4"}
	other := catalog.Entry{UUID: "k2", Name: "mysite_en_all", Flavour: "maxi", Date: "2026-11-01", Meta4URL: "https://x/z.meta4"}
	if err := s.ReplaceCatalog(ctx, append([]catalog.Entry{clash, other}, sample...), ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.EntriesByName(ctx, "mysite_en_all")
	if err != nil || len(got) != 1 || got[0] != mine {
		t.Fatalf("entries = %+v, %v", got, err)
	}
	if _, _, count, _ := s.CatalogState(ctx); count != len(sample) {
		t.Errorf("catalog count %d counts sources", count)
	}
	if es, _ := s.Search(ctx, Query{Text: "my site"}); len(es) != 1 || es[0].Name != mine.Name {
		t.Errorf("search: %+v", es)
	}
	seen, err := s.Seen(ctx, mine.Name)
	if err != nil || seen == nil || seen.ETag != `"a"` || seen.Source != mine.Source {
		t.Fatalf("seen = %+v, %v", seen, err)
	}

	// A new release replaces the old one, in the search index too.
	next := mine
	next.UUID, next.Title = "m2", "My new site"
	if err := s.PutSource(ctx, Seen{Name: next.Name, Source: next.Source}, next); err != nil {
		t.Fatal(err)
	}
	if es, _ := s.Search(ctx, Query{Text: "new site"}); len(es) != 1 || es[0].UUID != "m2" {
		t.Errorf("search after a new release: %+v", es)
	}

	if err := s.KeepSources(ctx, []string{"other"}); err != nil {
		t.Fatal(err)
	}
	if es, _ := s.EntriesByName(ctx, mine.Name); len(es) != 0 {
		t.Errorf("forgotten source still has entries: %+v", es)
	}
	if seen, _ := s.Seen(ctx, mine.Name); seen != nil {
		t.Errorf("forgotten source still seen: %+v", seen)
	}
	if es, _ := s.Search(ctx, Query{Text: "site"}); len(es) != 0 {
		t.Errorf("search index kept it: %+v", es)
	}
	if es, _ := s.Search(ctx, Query{Text: "wikipedia"}); len(es) != 3 {
		t.Errorf("the catalog suffered: %d entries", len(es))
	}
}
