package catalog_test

import (
	"bytes"
	"slices"
	"testing"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/catalog/catalogtest"
)

// The generated feed parses back to the entries it was made from (their
// summaries escaped in XML), but for a name no file could have.
func TestCatalogtestFeed(t *testing.T) {
	all := catalogtest.Entries(50, []string{"wikipedia_en_all", "a&b <odd> name"})
	want := slices.DeleteFunc(slices.Clone(all), func(e catalog.Entry) bool { return !e.Safe() })
	got, err := catalog.Parse(bytes.NewReader(catalogtest.Feed(all)), nil)
	if err != nil || !slices.Equal(got, want) || len(want) != len(all)-3 {
		t.Fatalf("got %d entries (%v) of %d; first %+v, want %+v", len(got), err, len(all), got[:min(1, len(got))], want[0])
	}
}

// BenchmarkParse is the parsing part of `zimon refresh`: the whole catalog.
func BenchmarkParse(b *testing.B) {
	feed := catalogtest.Feed(catalogtest.Entries(catalogtest.Size, nil))
	b.SetBytes(int64(len(feed)))
	b.ReportAllocs()
	for b.Loop() {
		if es, err := catalog.Parse(bytes.NewReader(feed), nil); err != nil || len(es) != catalogtest.Size {
			b.Fatal(len(es), err)
		}
	}
}
