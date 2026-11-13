package setup

import (
	"context"
	"testing"

	"github.com/kazie/zimon/internal/catalog/catalogtest"
	"github.com/kazie/zimon/internal/collections"
)

// benchResolver resolves against a catalog the size of the live one, held
// in memory: this measures the resolver, not the database.
func benchResolver() *Resolver {
	fams := families{}
	for _, e := range catalogtest.BuiltinEntries() {
		fams[e.Name] = append(fams[e.Name], e)
	}
	return &Resolver{Collections: collections.Builtin(), Languages: catalogtest.Languages, Catalog: fams}
}

// BenchmarkResolve works out what every built-in collection wants at deep,
// as planning `zimon apply` does.
func BenchmarkResolve(b *testing.B) {
	base := benchResolver()
	s := &Setup{Region: catalogtest.Region, Collections: map[string]collections.Level{}, Items: map[string]Choice{}}
	for _, c := range base.Collections {
		s.Collections[c.ID] = collections.Deep
	}
	b.ReportAllocs()
	for b.Loop() {
		r := *base // a fresh lookup cache, as each command has
		if w, err := r.Resolve(context.Background(), s); err != nil || len(w.Names) == 0 {
			b.Fatal(err)
		}
	}
}

// BenchmarkSizes is the size of every collection at every level, as
// `zimon collections` shows them.
func BenchmarkSizes(b *testing.B) {
	base := benchResolver()
	b.ReportAllocs()
	for b.Loop() {
		r := *base
		if _, err := r.Sizes(context.Background(), r.Collections, catalogtest.Region); err != nil {
			b.Fatal(err)
		}
	}
}
