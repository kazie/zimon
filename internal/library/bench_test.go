package library

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/kazie/zimon/internal/catalog/catalogtest"
	"github.com/kazie/zimon/internal/collections"
	"github.com/kazie/zimon/internal/store"
)

// benchManager is a big library: a catalog the size of the live one, every
// built-in collection at deep, and 100 ZIMs on disk (as database rows; the
// planner never reads the files).
func benchManager(b *testing.B) *Manager {
	b.Helper()
	ctx := context.Background()
	m := newManager(b)
	m.Cfg.Languages = catalogtest.Languages
	m.Collections = collections.Builtin()
	entries := catalogtest.BuiltinEntries()
	if err := m.Store.ReplaceCatalog(ctx, entries, ""); err != nil {
		b.Fatal(err)
	}
	files := 0
	for _, e := range entries {
		if e.Flavour == "maxi" && files < 100 {
			f := store.FileFromEntry(e)
			f.Path, f.Size = e.Name+".zim", e.ApproxSize
			if err := m.Store.InstallFile(ctx, f); err != nil {
				b.Fatal(err)
			}
			files++
		}
	}
	toml := []string{`region = "` + catalogtest.Region + `"`, "[collections]"}
	for _, c := range m.Collections {
		toml = append(toml, c.ID+` = "deep"`)
	}
	if err := os.WriteFile(m.SetupPath, []byte(strings.Join(toml, "\n")+"\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	return m
}

// BenchmarkPlan is planning `zimon apply` (and update) for a big library:
// read library.toml, resolve it against the catalog, compare with the disk.
func BenchmarkPlan(b *testing.B) {
	m := benchManager(b)
	b.ReportAllocs()
	for b.Loop() {
		if p, err := m.Plan(context.Background(), PlanOptions{}); err != nil || len(p.Steps) == 0 {
			b.Fatal(err)
		}
	}
}

// BenchmarkItems is what `zimon list` and the browse screen load.
func BenchmarkItems(b *testing.B) {
	m := benchManager(b)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := m.Items(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCollectionStatus is `zimon collections`: every collection's size
// at every level.
func BenchmarkCollectionStatus(b *testing.B) {
	m := benchManager(b)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := m.CollectionStatus(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}
