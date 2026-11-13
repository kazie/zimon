package collections

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kazie/zimon/internal/catalog"
)

func TestBuiltinParses(t *testing.T) {
	cols := Builtin()
	if len(cols) < 10 {
		t.Fatalf("only %d built-in collections", len(cols))
	}
	for _, c := range cols {
		if c.Title == "" || c.Blurb == "" || len(c.Items) == 0 {
			t.Errorf("%s: missing title, blurb or items", c.ID)
		}
		for _, it := range c.Items {
			if it.Note == "" {
				t.Errorf("%s: %s has no note", c.ID, it.Name)
			}
		}
	}
	if Find(cols, "linux") == nil || Find(cols, "nope") != nil {
		t.Error("Find")
	}
}

func TestNames(t *testing.T) {
	langs := []string{"en", "sv", "ja"}
	for _, tc := range []struct {
		item     Item
		region   string
		want     []string
		fallback string
	}{
		{Item{Name: "archlinux_en_all"}, "", []string{"archlinux_en_all"}, ""},
		{Item{Name: "wikipedia_{lang}_all"}, "", []string{"wikipedia_en_all", "wikipedia_sv_all", "wikipedia_ja_all"}, "wikipedia_en_all"},
		{Item{Name: "gutenberg_{lang}_all", Except: []string{"en"}}, "", []string{"gutenberg_sv_all", "gutenberg_ja_all"}, ""},
		{Item{Name: "maps_en_{region}"}, "sweden", []string{"maps_en_sweden"}, ""},
		{Item{Name: "maps_en_{region}"}, "", nil, ""},
	} {
		if got := tc.item.Names(langs, tc.region); !slices.Equal(got, tc.want) {
			t.Errorf("%s Names = %v, want %v", tc.item.Name, got, tc.want)
		}
		if got := tc.item.Fallback(tc.region); got != tc.fallback {
			t.Errorf("%s Fallback = %q, want %q", tc.item.Name, got, tc.fallback)
		}
	}
}

func TestLangCodes(t *testing.T) {
	if got := LangCodes([]string{"eng", "swe", "jpn", "swe"}); !slices.Equal(got, []string{"en", "sv", "ja"}) {
		t.Errorf("got %v", got)
	}
	if got := LangCodes(nil); !slices.Equal(got, []string{"en"}) {
		t.Errorf("no languages: got %v", got)
	}
}

func TestPick(t *testing.T) {
	fam := func(flavours ...string) []catalog.Entry {
		var out []catalog.Entry
		for i, f := range flavours {
			out = append(out, catalog.Entry{Name: "x", Flavour: f, ApproxSize: int64(i+1) * 100})
		}
		return out
	}
	flavour := func(e *catalog.Entry) string {
		if e == nil {
			return "<nil>"
		}
		return e.Flavour
	}
	full := fam("mini", "nopic", "maxi")
	for _, tc := range []struct {
		family []catalog.Entry
		level  Level
		fixed  string
		want   string
	}{
		{full, Light, "", "mini"},
		{full, Standard, "", "nopic"},
		{full, Deep, "", "maxi"},
		{fam("nopic", "maxi"), Light, "", "nopic"},
		{fam("maxi"), Light, "", "maxi"},
		{fam(""), Deep, "", ""},
		{fam("maxi", ""), Standard, "", "maxi"}, // no nopic: the smallest
		{fam("maxi", ""), Deep, "", "maxi"},
		{fam("full"), Standard, "", "full"},
		{append(fam("maxi"), catalog.Entry{Name: "x", Flavour: "odd"}), Standard, "", "maxi"},   // a size not known is not the smallest
		{[]catalog.Entry{{Name: "x", Flavour: "a"}, {Name: "x", Flavour: "b"}}, Light, "", "a"}, // none known: the first
		{full, Light, "maxi", "maxi"},
		{full, Light, "other", "<nil>"},
		{nil, Standard, "", "<nil>"},
	} {
		if got := flavour(Pick(tc.family, tc.level, tc.fixed)); got != tc.want {
			t.Errorf("Pick(%v, %v, %q) = %s, want %s", tc.family, tc.level, tc.fixed, got, tc.want)
		}
	}
}

func TestLevels(t *testing.T) {
	if !Light.Includes(Core) || Light.Includes(More) || !Deep.Includes(DeepTier) || Deep.Includes(Giant) {
		t.Error("Includes")
	}
	for _, l := range Levels {
		if got, ok := ParseLevel(l.String()); !ok || got != l {
			t.Errorf("ParseLevel(%s)", l)
		}
	}
	if _, ok := ParseLevel("huge"); ok {
		t.Error("ParseLevel accepted huge")
	}
}

func TestLoadUserCollections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collections.toml")
	data := `
[[collection]]
id = "linux"
title = "My Linux"
core = [{ name = "archlinux_en_all" }]

[[collection]]
id = "kemono"
title = "Kemono Friends"
core = [{ name = "allthetropes_en_all", note = "tropes" }]
giant = [{ name = "wikipedia_ja_all", flavour = "maxi" }]
`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	cols, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c := Find(cols, "linux"); c == nil || c.Title != "My Linux" || len(c.Items) != 1 {
		t.Errorf("linux not replaced: %+v", c)
	}
	k := Find(cols, "kemono")
	if k == nil || len(k.Items) != 2 || k.Items[1].Tier != Giant || k.Items[1].Flavour != "maxi" {
		t.Errorf("kemono: %+v", k)
	}
	if len(cols) != len(Builtin())+1 {
		t.Errorf("got %d collections", len(cols))
	}

	for _, bad := range []string{
		`[[collection]]` + "\nid = \"Bad ID\"",
		"[[collection]]\nid = \"a\"\n[[collection]]\nid = \"a\"",
		"[[collection]]\nid = \"a\"\ncore = [{ name = \"x_{oops}_all\" }]",
		"[[collection]]\nid = \"a\"\nunknown = 1",
	} {
		if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Errorf("accepted:\n%s", bad)
		}
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.toml")); err != nil {
		t.Errorf("missing file: %v", err)
	}
}
