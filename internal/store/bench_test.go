package store

import (
	"context"
	"testing"

	"github.com/kazie/zimon/internal/catalog/catalogtest"
)

// benchStore is a store holding a catalog the size of the live one.
func benchStore(b *testing.B) *Store {
	b.Helper()
	s := open(b)
	if err := s.ReplaceCatalog(context.Background(), catalogtest.BuiltinEntries(), ""); err != nil {
		b.Fatal(err)
	}
	return s
}

// BenchmarkReplaceCatalog is the database part of `zimon refresh`.
func BenchmarkReplaceCatalog(b *testing.B) {
	s := open(b)
	entries := catalogtest.Entries(catalogtest.Size, nil)
	b.ReportAllocs()
	for b.Loop() {
		if err := s.ReplaceCatalog(context.Background(), entries, ""); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSearch is `zimon search` and the browse screen's filter.
func BenchmarkSearch(b *testing.B) {
	s := benchStore(b)
	for name, q := range map[string]Query{
		"word":           {Text: "wikipedia"},
		"two words lang": {Text: "linux docs", Languages: []string{"eng"}},
		"prefix 3 langs": {Text: "cook", Languages: catalogtest.Languages},
		"category":       {Category: "stack_exchange", Languages: []string{"eng"}},
		"everything":     {},
	} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := s.Search(context.Background(), q); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkFamilies looks up every name the built-in collections use, as
// resolving library.toml does.
func BenchmarkFamilies(b *testing.B) {
	s, names := benchStore(b), catalogtest.BuiltinNames()
	b.ReportAllocs()
	for b.Loop() {
		if fams, err := s.Families(context.Background(), names); err != nil || len(fams) == 0 {
			b.Fatal(len(fams), err)
		}
	}
}

// BenchmarkEntriesWithPrefix is shell completion of a ZIM name.
func BenchmarkEntriesWithPrefix(b *testing.B) {
	s := benchStore(b)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := s.EntriesWithPrefix(context.Background(), "wikipedia_", catalogtest.Languages, 50); err != nil {
			b.Fatal(err)
		}
	}
}
