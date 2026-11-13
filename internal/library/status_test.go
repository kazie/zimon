package library

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/kazie/zimon/internal/collections"
	"github.com/kazie/zimon/internal/setup"
)

const kib = 1 << 10

func TestCollectionStatus(t *testing.T) {
	e := newEnv(t)
	e.edit(func(d *setup.Doc) { d.SetCollection("docs", collections.Deep) })
	got, err := e.m.CollectionStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Collection.ID != "basics" || got[1].Collection.ID != "docs" {
		t.Fatalf("collections = %+v", got)
	}
	// basics: light is the mini wiki; standard adds devdocs and takes nopic;
	// deep takes maxi and adds wikipedia_en_100. The giant never counts.
	if want := [3]int64{100 * kib, 400 * kib, 900 * kib}; got[0].Sizes != want || got[0].Mine {
		t.Errorf("basics = %+v, want sizes %v and not mine", got[0], want)
	}
	if want := [3]int64{200 * kib, 200 * kib, 200 * kib}; got[1].Sizes != want || !got[1].Mine || got[1].Level != collections.Deep {
		t.Errorf("docs = %+v, want sizes %v, mine at deep", got[1], want)
	}
}

func TestBudget(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.edit(func(d *setup.Doc) { d.SetCollection("docs", collections.Light) })
	rep, err := e.m.Budget(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// No hand-picked ZIMs: no row for them.
	if len(rep.Uses) != 1 || rep.Uses[0] != (Use{Via: "docs", Level: collections.Light, Wanted: 200 * kib}) {
		t.Errorf("uses = %+v", rep.Uses)
	}

	e.edit(func(d *setup.Doc) {
		d.SetCollection("basics", collections.Standard)
		d.SetItem("md_en_all", "maxi")
		d.SetBudget(2 << 20)
	})
	e.apply() // wiki_en_all:nopic, devdocs_en_go (wanted by both), md_en_all:maxi
	if _, err := e.m.Install(ctx, e.entry("huge_en_all"), nil); err != nil {
		t.Fatal(err) // on disk, but nothing wants it
	}
	e.edit(func(d *setup.Doc) { d.SetCollection("basics", collections.Deep) }) // not applied

	rep, err = e.m.Budget(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []Use{
		// devdocs is on disk; maxi and wikipedia_en_100 are still to come.
		{Via: "basics", Level: collections.Deep, Disk: 200 * kib, Wanted: (400 + 200 + 300) * kib},
		// A ZIM two collections want counts in each.
		{Via: "docs", Level: collections.Light, Disk: 200 * kib, Wanted: 200 * kib},
		{Via: setup.ViaItems, Disk: 100 * kib, Wanted: 100 * kib},
	}
	if !slices.Equal(rep.Uses, want) {
		t.Errorf("uses = %+v\nwant   %+v", rep.Uses, want)
	}
	// The nopic wiki (replaced by maxi) and the giant are on disk but unwanted.
	if rep.Extra != (200+500)*kib || rep.Disk != (200+200+100+500)*kib {
		t.Errorf("extra %d, disk %d", rep.Extra, rep.Disk)
	}
	if rep.Total != (400+200+300+100)*kib || rep.Budget != 2<<20 {
		t.Errorf("total %d, budget %d", rep.Total, rep.Budget)
	}
}

func TestSuggestLevels(t *testing.T) {
	e := newEnv(t)
	// a grows by flavour (100, 200, 400 KiB); b by tier (200, then 500 KiB).
	e.m.Collections = []collections.Collection{
		{ID: "a", Items: []collections.Item{{Name: "wiki_en_all", Tier: collections.Core}}},
		{ID: "b", Items: []collections.Item{
			{Name: "devdocs_en_go", Tier: collections.Core},
			{Name: "wikipedia_en_100", Tier: collections.More},
		}},
	}
	e.edit(func(d *setup.Doc) { d.SetItem("md_en_all", "maxi") }) // 100 KiB, counted as it is
	L, S, D := collections.Light, collections.Standard, collections.Deep
	cases := []struct {
		ids    []string
		limit  int64
		levels map[string]collections.Level
		total  int64
	}{
		{[]string{"a", "b"}, 10 << 20, map[string]collections.Level{"a": D, "b": D}, (400 + 500 + 100) * kib},
		// Priority order: the first collection is raised first, at every step.
		{[]string{"a", "b"}, 700 * kib, map[string]collections.Level{"a": D, "b": L}, (400 + 200 + 100) * kib},
		{[]string{"b", "a"}, 700 * kib, map[string]collections.Level{"a": L, "b": D}, (100 + 500 + 100) * kib},
		{[]string{"a", "b"}, 500 * kib, map[string]collections.Level{"a": S, "b": L}, (200 + 200 + 100) * kib},
	}
	for _, c := range cases {
		levels, total, ok, err := e.m.SuggestLevels(context.Background(), c.ids, c.limit)
		if err != nil || !ok || !maps.Equal(levels, c.levels) || total != c.total {
			t.Errorf("SuggestLevels(%v, %d) = %v, %d, %v, %v; want %v, %d", c.ids, c.limit, levels, total, ok, err, c.levels, c.total)
		}
	}

	// Even light does not fit: no levels, and the size at light.
	levels, total, ok, err := e.m.SuggestLevels(context.Background(), []string{"a", "b"}, 300*kib)
	if err != nil || ok || levels != nil || total != (100+200+100)*kib {
		t.Errorf("too small: %v, %d, %v, %v", levels, total, ok, err)
	}
}
