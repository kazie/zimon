package setup

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/collections"
)

func TestParse(t *testing.T) {
	s, err := Parse([]byte(`
budget = "250GB"
region = "sweden"

[collections]
linux = "deep"
games = "light"

[items]
"wikipedia_en_all" = "nopic"
"minecraft.wiki_en_all" = "off"

[hold]
"wikipedia_en_all" = "2025-11-03"

[sources]
"mysite_en_all" = "https://example.org/mysite.zim"
"notes" = "/home/me/notes.zim"
`))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Held("wikipedia_en_all") || s.Held("minecraft.wiki_en_all") || s.Hold["wikipedia_en_all"] != "2025-11-03" {
		t.Errorf("hold: %v", s.Hold)
	}
	if s.Sources["mysite_en_all"] != "https://example.org/mysite.zim" || s.Sources["notes"] != "/home/me/notes.zim" {
		t.Errorf("sources: %v", s.Sources)
	}
	if s.Budget != 250e9 || s.Region != "sweden" || s.NewItems != NewWithinBudget {
		t.Errorf("top level: %+v", s)
	}
	if s.Collections["linux"] != collections.Deep || s.Collections["games"] != collections.Light {
		t.Errorf("collections: %v", s.Collections)
	}
	if s.Items["wikipedia_en_all"] != (Choice{Kind: ChoiceFlavour, Flavour: "nopic"}) || s.Items["minecraft.wiki_en_all"].Kind != ChoiceOff {
		t.Errorf("items: %v", s.Items)
	}
	if !slices.Equal(s.CollectionIDs(), []string{"games", "linux"}) {
		t.Errorf("ids: %v", s.CollectionIDs())
	}
	for _, bad := range []string{
		"[collections]\nlinux = \"huge\"",
		"new_items = \"sometimes\"",
		"[items]\nx = \"\"",
		"[hold]\n\" \" = \"2025-11\"",
		"[hold]\nx = 1",
		"[sources]\nx = \"notes.zim\"",
		"[sources]\nx = \"ftp://h/x.zim\"",
		"[sources]\n\"a b\" = \"/x.zim\"",
		"[sources]\n\"a:b\" = \"/x.zim\"",
		"unknown = 1",
		"budget = \"lots\"",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if s, err := Parse(nil); err != nil || len(s.Collections) != 0 || s.NewItems != NewWithinBudget {
		t.Errorf("empty: %+v %v", s, err)
	}
}

func TestDocEditsKeepComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.toml")
	orig := `# my library, in git
budget = "100GB"   # for now

[collections]
# the things I care about
linux = "light"   # bump later

[items]
"wikipedia_en_all" = 'mini'
`
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d.SetCollection("linux", collections.Deep)
	d.SetCollection("anime-manga", collections.Standard)
	d.SetItem("wikipedia_en_all", "nopic")
	d.SetItem("stackoverflow.com_en_all", On)
	d.SetBudget(250e9)
	if err := d.Save(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := `# my library, in git
budget = "250GB"   # for now

[collections]
# the things I care about
linux = "deep"   # bump later
anime-manga = "standard"

[items]
"wikipedia_en_all" = "nopic"
"stackoverflow.com_en_all" = "on"
`
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode changed to %v", st.Mode().Perm())
	}

	d, _ = Open(path)
	if !d.DeleteCollection("linux") || d.DeleteCollection("linux") || !d.DeleteItem("wikipedia_en_all") {
		t.Error("delete reported wrongly")
	}
	d.SetBudget(0)
	s, err := d.Setup()
	if err != nil {
		t.Fatal(err)
	}
	if s.Budget != 0 || len(s.Collections) != 1 || len(s.Items) != 1 {
		t.Errorf("after deletes: %+v", s)
	}
	if !strings.Contains(string(d.Bytes()), "# the things I care about") {
		t.Error("comment lost")
	}
}

func TestDocFromTemplate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "library.toml")
	d, err := Open(path)
	if err != nil || d.Exists() {
		t.Fatalf("open: %v exists=%v", err, d.Exists())
	}
	d.SetBudget(250e9)
	d.SetCollection("linux", collections.Standard)
	d.SetItem("wikipedia_en_all", "maxi")
	if err := d.Save(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	text := string(got)
	// The budget goes after the comments, before [collections], with one blank line.
	if !strings.Contains(text, "# region = \"sweden\"             # for map items (maps_en_<region>)\nbudget = \"250GB\"\n\n[collections]") {
		t.Errorf("budget placement:\n%s", text)
	}
	if !strings.Contains(text, "\nlinux = \"standard\"\n") || !strings.Contains(text, "\n\"wikipedia_en_all\" = \"maxi\"\n") {
		t.Errorf("keys:\n%s", text)
	}
	data, _ := os.ReadFile(path)
	s, err := Parse(data)
	if err != nil || s.Collections["linux"] != collections.Standard || s.Items["wikipedia_en_all"].String() != "maxi" || s.Budget != 250e9 {
		t.Errorf("reload: %+v %v", s, err)
	}
}

func TestDocAddsMissingTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.toml")
	if err := os.WriteFile(path, []byte("budget = \"1TB\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, _ := Open(path)
	d.SetItem("xkcd.com_en_all", On)
	d.SetCollection("fun", collections.Light)
	want := "budget = \"1TB\"\n\n[items]\n\"xkcd.com_en_all\" = \"on\"\n\n[collections]\nfun = \"light\"\n"
	if got := string(d.Bytes()); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestDocHold(t *testing.T) {
	d := newDoc("library.toml", "[items]\n\"a_en_all\" = \"on\"\n")
	d.SetHold("a_en_all", "2025-11-03")
	d.SetHold("b_en_all", "2025-10-01")
	d.SetHold("a_en_all", "2025-12-01")
	want := "[items]\n\"a_en_all\" = \"on\"\n\n[hold]\n\"a_en_all\" = \"2025-12-01\"\n\"b_en_all\" = \"2025-10-01\"\n"
	if got := string(d.Bytes()); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	d.SetSource("a_en_all", "https://h/a.zim")
	if !strings.HasSuffix(string(d.Bytes()), "\n[sources]\n\"a_en_all\" = \"https://h/a.zim\"\n") {
		t.Errorf("source:\n%s", d.Bytes())
	}
	// Any path survives a round trip through library.toml.
	for _, p := range []string{"/zims/caf\u00e9 \"x\" \\ y.zim", "/a\tb\x07\x7f.zim"} {
		d.SetSource("odd", p)
		s, err := d.Setup()
		if err != nil || s.Sources["odd"] != p {
			t.Errorf("%q came back as %q, %v", p, s.Sources["odd"], err)
		}
	}
	d.DeleteSource("odd")
	if !d.DeleteSource("a_en_all") || d.DeleteSource("a_en_all") {
		t.Error("delete source reported wrongly")
	}
	if !d.DeleteHold("b_en_all") || d.DeleteHold("b_en_all") {
		t.Error("delete reported wrongly")
	}
	s, err := d.Setup()
	if err != nil || !maps.Equal(s.Hold, map[string]string{"a_en_all": "2025-12-01"}) {
		t.Errorf("setup: %v %v", s, err)
	}
}

func TestDocRefusesInvalidAndReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "library.toml")
	if err := os.WriteFile(path, []byte("[collections]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, _ := Open(path)
	d.lines = append(d.lines, "linux = \"huge\"")
	if err := d.Save(); err == nil {
		t.Error("saved an invalid file")
	}
	if got, _ := os.ReadFile(path); string(got) != "[collections]\n" {
		t.Errorf("file changed: %q", got)
	}

	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permissions do not apply")
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	d, _ = Open(path)
	d.SetCollection("linux", collections.Light)
	if err := d.Save(); !errors.Is(err, ErrReadOnly) {
		t.Errorf("read-only file: %v", err)
	}
}

// Valid TOML that line edits could get wrong is read, but not edited.
func TestDocRefusesLayoutItCannotEdit(t *testing.T) {
	for _, text := range []string{
		"items.wikipedia_en_all = \"maxi\"\n",                       // a dotted key
		"collections = { linux = \"deep\" }\n",                      // an inline table
		"region = \"\"\"\n[items]\n\"x_en_all\" = \"on\"\n\"\"\"\n", // a multi-line string
	} {
		d := newDoc("library.toml", text)
		if _, err := d.Setup(); err != nil {
			t.Errorf("%q should still read: %v", text, err)
		}
		d.SetItem("wikipedia_en_all", "nopic")
		if string(d.Bytes()) != text {
			t.Errorf("%q was edited:\n%s", text, d.Bytes())
		}
		if _, err := d.Setup(); !errors.Is(err, ErrNotFlat) || !strings.Contains(err.Error(), "line 1") {
			t.Errorf("%q: %v", text, err)
		}
	}
}

// families is a fake catalog.
type families map[string][]catalog.Entry

func (f families) Families(_ context.Context, names []string) (map[string][]catalog.Entry, error) {
	out := map[string][]catalog.Entry{}
	for _, n := range names {
		if fam, ok := f[n]; ok {
			out[n] = fam
		}
	}
	return out, nil
}

func fam(name string, flavours ...string) []catalog.Entry {
	var out []catalog.Entry
	for i, f := range flavours {
		out = append(out, catalog.Entry{Name: name, Flavour: f, UUID: name + ":" + f, ApproxSize: int64(i+1) * 100})
	}
	return out
}

func testResolver() *Resolver {
	cat := families{
		"wikipedia_en_all": fam("wikipedia_en_all", "mini", "nopic", "maxi"),
		"wikipedia_sv_all": fam("wikipedia_sv_all", "mini", "nopic", "maxi"),
		"ifixit_en_all":    fam("ifixit_en_all", ""),
		"md_en_all":        fam("md_en_all", "maxi", ""),
		"archlinux_en_all": fam("archlinux_en_all", "maxi"),
		"unix_en_all":      fam("unix_en_all", ""),
		"askubuntu_en_all": fam("askubuntu_en_all", ""),
		"so_en_all":        fam("so_en_all", ""),
		"maps_en_sweden":   fam("maps_en_sweden", ""),
	}
	cols := []collections.Collection{
		{ID: "essentials", Items: []collections.Item{
			{Name: "wikipedia_{lang}_all", Tier: collections.Core, Note: "encyclopedia"},
			{Name: "ifixit_{lang}_all", Tier: collections.More},
		}},
		{ID: "linux", Items: []collections.Item{
			{Name: "archlinux_en_all", Tier: collections.Core},
			{Name: "unix_en_all", Tier: collections.More},
			{Name: "askubuntu_en_all", Tier: collections.DeepTier},
			{Name: "gone_en_all", Tier: collections.Core},
			{Name: "so_en_all", Tier: collections.Giant},
		}},
		{ID: "big-wiki", Items: []collections.Item{
			{Name: "wikipedia_en_all", Tier: collections.Core, Flavour: "maxi"},
		}},
		{ID: "maps", Items: []collections.Item{
			{Name: "maps_en_{region}", Tier: collections.Core},
		}},
	}
	return &Resolver{Collections: cols, Languages: []string{"eng", "swe"}, Catalog: cat}
}

func flavours(w *Wanted) map[string]string {
	out := map[string]string{}
	for n, want := range w.Names {
		if want.Entry != nil {
			out[n] = want.Entry.Flavour
		}
	}
	return out
}

// gone lists the wanted names that are not in the catalog.
func gone(w *Wanted) []string {
	var out []string
	for n, want := range w.Names {
		if want.Entry == nil {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

func resolve(t *testing.T, r *Resolver, text string) *Wanted {
	t.Helper()
	s, err := Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	w, err := r.Resolve(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestResolveLevels(t *testing.T) {
	r := testResolver()
	w := resolve(t, r, "[collections]\nlinux = \"light\"\nessentials = \"light\"")
	got := flavours(w)
	want := map[string]string{"archlinux_en_all": "maxi", "wikipedia_en_all": "mini", "wikipedia_sv_all": "mini"}
	if !maps.Equal(got, want) {
		t.Errorf("light: %v, want %v", got, want)
	}
	// Missing items are wanted all the same (a copy on disk stays) and reported.
	if g := gone(w); !slices.Equal(g, []string{"gone_en_all"}) {
		t.Errorf("gone = %v", g)
	}
	if !slices.Contains(w.Problems, Problem{Kind: NotInCatalog, Collection: "linux", Name: "gone_en_all"}) {
		t.Errorf("missing item not reported: %v", w.Problems)
	}

	w = resolve(t, r, "[collections]\nlinux = \"deep\"\nessentials = \"standard\"")
	want = map[string]string{"archlinux_en_all": "maxi", "unix_en_all": "", "askubuntu_en_all": "",
		"wikipedia_en_all": "nopic", "wikipedia_sv_all": "nopic", "ifixit_en_all": ""}
	if got := flavours(w); !maps.Equal(got, want) {
		t.Errorf("deep: %v, want %v", got, want)
	}
	if _, ok := w.Names["so_en_all"]; ok {
		t.Error("a giant was added by a level")
	}
	if wiki := w.Names["wikipedia_en_all"]; !slices.Equal(wiki.Via, []string{"essentials"}) {
		t.Errorf("wiki want: %+v", wiki)
	}
	// ifixit_sv_all does not exist: a missing language variant is not a problem.
	if slices.ContainsFunc(w.Problems, func(p Problem) bool { return p.Name == "ifixit_sv_all" }) {
		t.Errorf("variant reported: %v", w.Problems)
	}
}

func TestResolveSharedKeyTakesDeeper(t *testing.T) {
	w := resolve(t, testResolver(), "[collections]\nessentials = \"light\"\nbig-wiki = \"light\"")
	wiki := w.Names["wikipedia_en_all"]
	if wiki.Entry.Flavour != "maxi" || !slices.Equal(wiki.Via, []string{"big-wiki", "essentials"}) {
		t.Errorf("shared: %+v", wiki)
	}
}

func TestResolveItems(t *testing.T) {
	r := testResolver()
	w := resolve(t, r, `
[collections]
big-wiki = "standard"
linux = "light"

[items]
"wikipedia_en_all" = "mini"
"archlinux_en_all" = "off"
"so_en_all" = "on"
"wikipedia_sv_all" = "deep"
"ifixit_en_all" = "fancy"
"nope_en_all" = "on"
"md_en_all" = "default"
"unix_en_all" = "default"
`)
	want := map[string]string{"wikipedia_en_all": "mini", "so_en_all": "", "wikipedia_sv_all": "maxi", "md_en_all": "", "unix_en_all": ""}
	if got := flavours(w); !maps.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if _, ok := w.Names["archlinux_en_all"]; ok {
		t.Error("off did not switch it off")
	}
	// nope_en_all is asked for by name: wanted as gone, so a copy on disk would stay.
	if g := gone(w); !slices.Equal(g, []string{"gone_en_all", "nope_en_all"}) {
		t.Errorf("gone = %v", g)
	}
	if !slices.Equal(w.Names["wikipedia_en_all"].Via, []string{"big-wiki", ViaItems}) {
		t.Errorf("via: %v", w.Names["wikipedia_en_all"].Via)
	}
	for _, p := range []Problem{
		{Kind: NoSuchFlavour, Name: "ifixit_en_all", Flavour: "fancy"},
		{Kind: NotInCatalog, Name: "nope_en_all"},
	} {
		if !slices.Contains(w.Problems, p) {
			t.Errorf("problem %v missing from %v", p, w.Problems)
		}
	}
}

func TestResolveHold(t *testing.T) {
	w := resolve(t, testResolver(), `
[collections]
big-wiki = "standard"

[items]
"so_en_all" = "on"

[hold]
"wikipedia_en_all" = "2025-01-01"
"so_en_all" = "2025-01-01"
"archlinux_en_all" = "2025-01-01"
`)
	if !w.Names["wikipedia_en_all"].Held || !w.Names["so_en_all"].Held {
		t.Errorf("not held: %+v %+v", w.Names["wikipedia_en_all"], w.Names["so_en_all"])
	}
	if _, ok := w.Names["archlinux_en_all"]; ok {
		t.Error("a hold made a ZIM wanted")
	}
}

func TestResolveLangFallbackAndRegion(t *testing.T) {
	r := testResolver()
	r.Languages = []string{"jpn"} // no Japanese ZIMs in the fake catalog: English instead
	w := resolve(t, r, "[collections]\nessentials = \"standard\"\nmaps = \"light\"")
	if got := flavours(w); !maps.Equal(got, map[string]string{"wikipedia_en_all": "nopic", "ifixit_en_all": ""}) {
		t.Errorf("fallback: %v", got)
	}
	if !slices.Contains(w.Problems, Problem{Kind: NeedsRegion, Collection: "maps", Name: "maps_en_{region}"}) {
		t.Errorf("region problem missing: %v", w.Problems)
	}
	w = resolve(t, r, "region = \"sweden\"\n[collections]\nmaps = \"light\"")
	if _, ok := w.Names["maps_en_sweden"]; !ok {
		t.Errorf("region map missing: %v", flavours(w))
	}
}

func TestResolveUnknownCollection(t *testing.T) {
	w := resolve(t, testResolver(), "[collections]\nnope = \"light\"")
	if len(w.Names) != 0 || !slices.Equal(w.Problems, []Problem{{Kind: UnknownCollection, Collection: "nope"}}) {
		t.Errorf("%+v", w)
	}
}

func TestSummarize(t *testing.T) {
	got := Summarize([]Problem{
		{Kind: NotInCatalog, Collection: "linux", Name: "a"},
		{Kind: NotInCatalog, Name: "b"},
		{Kind: NotInCatalog, Collection: "linux", Name: "c"},
		{Kind: NotInCatalog, Collection: "games", Name: "d"},
	})
	want := []string{
		"b is not in the Kiwix catalog right now",
		"linux: 2 items are not in the Kiwix catalog right now (a, c)",
		"games: d is not in the Kiwix catalog right now",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %q", got)
	}
}

func TestParseChoice(t *testing.T) {
	for in, want := range map[string]Choice{
		"on": {Kind: ChoiceOn}, "off": {Kind: ChoiceOff}, "default": {Kind: ChoiceDefault},
		"deep": {Kind: ChoiceLevel, Level: collections.Deep}, "maxi": {Kind: ChoiceFlavour, Flavour: "maxi"},
	} {
		if got, err := ParseChoice(in); err != nil || got != want || got.String() != in {
			t.Errorf("ParseChoice(%q) = %+v, %v", in, got, err)
		}
	}
	if _, err := ParseChoice(" "); err == nil {
		t.Error("accepted an empty choice")
	}
}
