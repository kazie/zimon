package setup

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/collections"
)

func TestSizes(t *testing.T) {
	r := testResolver()
	got, err := r.Sizes(context.Background(), r.Collections, "")
	if err != nil {
		t.Fatal(err)
	}
	// fam sizes are 100 per position: mini 100, nopic 200, maxi 300.
	want := map[string][3]int64{
		"essentials": {100 + 100, 200 + 200 + 100, 300 + 300 + 100}, // en and sv wikis, ifixit from standard on
		"linux":      {100, 200, 300},                               // the giant never counts, nor the gone item
		"big-wiki":   {300, 300, 300},                               // a fixed flavour
		"maps":       {0, 0, 0},                                     // needs a region
	}
	if !maps.Equal(got, want) {
		t.Errorf("sizes = %v\nwant    %v", got, want)
	}
	got, err = r.Sizes(context.Background(), r.Collections, "sweden")
	if err != nil || got["maps"] != [3]int64{100, 100, 100} {
		t.Errorf("maps with a region: %v, %v", got["maps"], err)
	}
}

func TestExpand(t *testing.T) {
	r := testResolver()
	c := collections.Find(r.Collections, "essentials")
	got, err := r.Expand(context.Background(), c, collections.Standard, "", false)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, x := range got {
		line := x.Name
		switch {
		case x.Gone && x.Variant:
			line += " gone variant"
		case x.Entry != nil:
			line += ":" + x.Entry.Flavour
		}
		lines = append(lines, line)
	}
	want := []string{"wikipedia_en_all:nopic", "wikipedia_sv_all:nopic", "ifixit_en_all:", "ifixit_sv_all gone variant"}
	if !slices.Equal(lines, want) {
		t.Errorf("expand = %q, want %q", lines, want)
	}

	// all: every tier, giants too; a gone item is not a variant.
	got, err = r.Expand(context.Background(), collections.Find(r.Collections, "linux"), collections.Light, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || got[4].Name != "so_en_all" || got[4].Entry == nil || !got[3].Gone || got[3].Variant {
		t.Errorf("expand all = %+v", got)
	}

	got, err = r.Expand(context.Background(), collections.Find(r.Collections, "maps"), collections.Light, "", false)
	if err != nil || len(got) != 1 || !got[0].NeedsRegion || got[0].Entry != nil {
		t.Errorf("no region: %+v, %v", got, err)
	}
}

// countingFamilies records how often each name is looked up.
type countingFamilies struct {
	families
	asked map[string]int
}

func (c *countingFamilies) Families(ctx context.Context, names []string) (map[string][]catalog.Entry, error) {
	for _, n := range names {
		c.asked[n]++
	}
	return c.families.Families(ctx, names)
}

func TestResolverCachesLookups(t *testing.T) {
	r := testResolver()
	cf := &countingFamilies{families: r.Catalog.(families), asked: map[string]int{}}
	r.Catalog = cf
	resolve(t, r, "[collections]\nlinux = \"deep\"\n[items]\n\"md_en_all\" = \"maxi\"")
	resolve(t, r, "[collections]\nlinux = \"light\"\n[items]\n\"md_en_all\" = \"on\"")
	if _, err := r.Sizes(context.Background(), r.Collections, ""); err != nil {
		t.Fatal(err)
	}
	for n, times := range cf.asked {
		if times != 1 {
			t.Errorf("%s looked up %d times", n, times) // names not in the catalog are remembered too
		}
	}
	if cf.asked["gone_en_all"] != 1 {
		t.Errorf("lookups = %v", cf.asked)
	}
}

func TestWantedTotal(t *testing.T) {
	w := resolve(t, testResolver(), "[collections]\nlinux = \"light\"\n[items]\n\"md_en_all\" = \"maxi\"")
	// archlinux (100) and md maxi (100); gone_en_all is wanted but has no size.
	if w.Names["gone_en_all"].Size() != 0 || w.Total() != 200 {
		t.Errorf("total = %d", w.Total())
	}
}

func TestChoicePick(t *testing.T) {
	md := fam("md_en_all", "maxi", "") // maxi 100, flavourless 200
	wiki := fam("wikipedia_en_all", "mini", "nopic", "maxi")
	pick := func(c string, f []catalog.Entry, current *catalog.Entry) string {
		ch, err := ParseChoice(c)
		if err != nil {
			t.Fatal(err)
		}
		e := ch.Pick(f, current)
		if e == nil {
			return "nil"
		}
		return e.Key().String()
	}
	cases := []struct{ choice, got, want string }{
		{"off", pick("off", wiki, nil), "nil"},
		{"default", pick("default", md, nil), "md_en_all"},
		{"default without one", pick("default", wiki, nil), "nil"},
		{"on keeps the collection's pick", pick("on", wiki, &wiki[2]), "wikipedia_en_all:maxi"},
		{"on alone is standard", pick("on", wiki, nil), "wikipedia_en_all:nopic"},
		{"level", pick("light", wiki, nil), "wikipedia_en_all:mini"},
		{"flavour", pick("maxi", md, nil), "md_en_all:maxi"},
		{"missing flavour", pick("nopic", md, nil), "nil"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %s, want %s", c.choice, c.got, c.want)
		}
	}
}

func TestProblemString(t *testing.T) {
	for p, want := range map[Problem]string{
		{Kind: UnknownCollection, Collection: "nope"}:                                 `unknown collection "nope" (see: zimon collections)`,
		{Kind: NotInCatalog, Name: "x_en_all"}:                                        "x_en_all is not in the Kiwix catalog right now",
		{Kind: NotInCatalog, Collection: "maps", Name: "maps_swedn", Region: "swedn"}: `maps: maps_swedn is not in the Kiwix catalog: is region "swedn" right?`,
		{Kind: NoSuchFlavour, Collection: "linux", Name: "a", Flavour: "maxi"}:        `linux: a has no "maxi" flavour`,
		{Kind: NoSuchFlavour, Name: "a"}:                                              "a has no variant without a flavour",
		{Kind: NeedsRegion, Collection: "maps", Name: "maps_en_{region}"}:             "maps: set region in library.toml to include maps_en_{region}",
		{Kind: SourceUnchecked, Name: "mine", Source: "https://h/m.zim"}:              "mine comes from https://h/m.zim, which zimon could not check (see: zimon refresh)",
	} {
		if got := p.String(); got != want {
			t.Errorf("%+v: %q, want %q", p, got, want)
		}
	}
	// Summarize shortens long lists.
	var ps []Problem
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		ps = append(ps, Problem{Kind: NotInCatalog, Collection: "games", Name: n})
	}
	if got := Summarize(ps); !slices.Equal(got, []string{"games: 5 items are not in the Kiwix catalog right now (a, b, c, d, …)"}) {
		t.Errorf("summary = %q", got)
	}
}
