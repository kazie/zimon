package library

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/collections"
	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/fakekiwix"
	"github.com/kazie/zimon/internal/setup"
	"github.com/kazie/zimon/internal/store"
)

var books = []fakekiwix.Book{
	{Name: "wikipedia_en_100", Title: "Wikipedia 100", Summary: "Top 100 articles", Language: "eng", Category: "wikipedia",
		Creator: "Wikipedia", Publisher: "openZIM", Tags: "wikipedia;_pictures:yes", Date: "2026-08-20", Size: 300 << 10, ArticleCount: 100},
	{Name: "devdocs_en_go", Title: "Go Docs", Language: "eng", Category: "devdocs", Date: "2026-10-12", Size: 200 << 10},
	{Name: "wiki_en_all", Flavour: "mini", Title: "Wiki", Language: "eng", Date: "2026-10-01", Size: 100 << 10},
	{Name: "wiki_en_all", Flavour: "nopic", Title: "Wiki", Language: "eng", Date: "2026-10-01", Size: 200 << 10},
	{Name: "wiki_en_all", Flavour: "maxi", Title: "Wiki", Language: "eng", Date: "2026-10-01", Size: 400 << 10},
	{Name: "huge_en_all", Title: "Huge", Language: "eng", Date: "2026-10-01", Size: 500 << 10},
	{Name: "md_en_all", Title: "MD", Language: "eng", Date: "2026-10-01", Size: 300 << 10},
	{Name: "md_en_all", Flavour: "maxi", Title: "MD", Language: "eng", Date: "2026-10-01", Size: 100 << 10},
}

// testCollections: basics has an item in every tier; docs shares devdocs_en_go.
var testCollections = []collections.Collection{
	{ID: "basics", Items: []collections.Item{
		{Name: "wiki_en_all", Tier: collections.Core},
		{Name: "devdocs_en_go", Tier: collections.More},
		{Name: "wikipedia_en_100", Tier: collections.DeepTier},
		{Name: "huge_en_all", Tier: collections.Giant},
	}},
	{ID: "docs", Items: []collections.Item{{Name: "devdocs_en_go", Tier: collections.Core}}},
}

type env struct {
	t   *testing.T
	fk  *fakekiwix.Server
	srv *httptest.Server
	m   *Manager
}

func newEnv(t *testing.T) *env {
	t.Helper()
	fk := fakekiwix.New(books)
	fk.PieceLength = 64 << 10
	srv := httptest.NewServer(fk)
	t.Cleanup(srv.Close)
	m := newManager(t)
	m.Cfg.CatalogURL = srv.URL + "/catalog/v2"
	m.HTTP = srv.Client()
	m.Backoff = func(int) time.Duration { return 0 }
	m.Collections = testCollections
	e := &env{t: t, fk: fk, srv: srv, m: m}
	e.sync()
	return e
}

// newManager is a Manager with its own state and library dirs, an empty
// store, and no library.toml yet.
func newManager(tb testing.TB) *Manager {
	tb.Helper()
	dir := tb.TempDir()
	cfg := config.Default()
	cfg.LibraryDir = filepath.Join(dir, "library")
	cfg.StateDir = filepath.Join(dir, "state")
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { st.Close() })
	return &Manager{Cfg: cfg, Store: st, SetupPath: filepath.Join(dir, "library.toml")}
}

// sync pulls the fake catalog into the store, like `zimon refresh`.
func (e *env) sync() {
	e.t.Helper()
	if _, _, _, err := RefreshCatalog(context.Background(), e.m.Store, e.srv.Client(), []string{e.m.Cfg.CatalogURL}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) entry(key string) catalog.Entry {
	e.t.Helper()
	k := catalog.ParseKey(key)
	got, err := e.m.Store.EntriesByKeys(context.Background(), []catalog.Key{k})
	if err != nil || got[k].UUID == "" {
		e.t.Fatalf("entry %s: %v", key, err)
	}
	return got[k]
}

// edit changes library.toml.
func (e *env) edit(mutate func(*setup.Doc)) {
	e.t.Helper()
	if _, err := e.m.Edit(context.Background(), func(d *setup.Doc) error { mutate(d); return nil }); err != nil {
		e.t.Fatal(err)
	}
}

// want hand-picks key in library.toml, like `zimon add --no-download`.
func (e *env) want(key string) {
	k := catalog.ParseKey(key)
	e.edit(func(d *setup.Doc) { d.SetItem(k.Name, cmp.Or(k.Flavour, setup.On)) })
}

// add wants key and installs it, like `zimon add`.
func (e *env) add(key string) *store.File {
	e.t.Helper()
	e.want(key)
	f, err := e.m.Install(context.Background(), e.entry(key), nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return f
}

// plan plans like `zimon apply` (or update, when unattended).
func (e *env) plan(unattended bool) *Plan {
	e.t.Helper()
	p, err := e.m.Plan(context.Background(), PlanOptions{Unattended: unattended})
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

// apply runs `zimon apply`, failing on any error.
func (e *env) apply() *Plan {
	e.t.Helper()
	return e.change(Change{})
}

// change previews and runs ch the way the CLI does, failing on any error.
func (e *env) change(ch Change) *Plan {
	e.t.Helper()
	ctx := context.Background()
	p, err := e.m.Preview(ctx, ch)
	if err != nil {
		e.t.Fatal(err)
	}
	res, err := e.m.Apply(ctx, ch, p, nil, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, r := range res.Results {
		if r.Err != nil {
			e.t.Fatalf("%v %s: %v", r.Step.Kind, r.Step.Key, r.Err)
		}
	}
	return p.Plan
}

// onDisk lists the active keys and checks each file exists.
func (e *env) onDisk() []string {
	e.t.Helper()
	files, err := e.m.Store.ActiveFiles(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(e.m.Cfg.LibraryDir, f.Path)); err != nil {
			e.t.Errorf("%s: %v", f.Key(), err)
		}
		out = append(out, f.Key().String())
	}
	return out
}

func steps(p *Plan) []string {
	var out []string
	for _, st := range p.Steps {
		out = append(out, [...]string{"remove", "update", "switch", "add"}[st.Kind]+" "+st.Key.String())
	}
	return out
}

func (e *env) state(key string) State {
	e.t.Helper()
	items, err := e.m.Items(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	if it, ok := items[catalog.ParseKey(key)]; ok {
		return it.State
	}
	return NotInLibrary
}

func TestInstallWritesFileAndLibraryXML(t *testing.T) {
	e := newEnv(t)
	f := e.add("wikipedia_en_100")
	book, _ := e.fk.Book("wikipedia_en_100")
	if f.Path != book.FileName() || f.UUID != book.UUID() || f.ZimUUID != book.UUID() {
		t.Errorf("file = %+v", f)
	}
	data, err := os.ReadFile(filepath.Join(e.m.Cfg.LibraryDir, f.Path))
	if err != nil || len(data) != len(e.fk.Content(book)) {
		t.Fatalf("file on disk: %d bytes, %v", len(data), err)
	}
	if e.state("wikipedia_en_100") != UpToDate {
		t.Errorf("state = %v", e.state("wikipedia_en_100"))
	}
	if entries, _ := os.ReadDir(e.m.Cfg.PartialDir()); len(entries) != 0 {
		t.Errorf("partial dir not empty: %v", entries)
	}
	xml, err := os.ReadFile(e.m.Cfg.LibraryXMLPath())
	if err != nil {
		t.Fatal(err)
	}
	want := `<book id="` + book.UUID() + `" path="wikipedia_en_100_2026-08.zim" title="Wikipedia 100" description="Top 100 articles" language="eng" creator="Wikipedia" publisher="openZIM" name="wikipedia_en_100" tags="wikipedia;_pictures:yes" date="2026-08-20" articleCount="100" size="300"></book>`
	if !strings.Contains(string(xml), want) {
		t.Errorf("library.xml:\n%s\nwant a line like:\n%s", xml, want)
	}
	// Installing the same release again is a no-op.
	if again, err := e.m.Install(context.Background(), e.entry("wikipedia_en_100"), nil); err != nil || again.UUID != f.UUID {
		t.Errorf("reinstall: %+v %v", again, err)
	}
}

func TestUpdateSwapsAndPrunes(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	old := e.add("wikipedia_en_100")

	e.fk.Bump("wikipedia_en_100")
	e.sync()
	if s := e.state("wikipedia_en_100"); s != UpdateAvailable {
		t.Fatalf("state after bump = %v", s)
	}
	p := e.plan(true)
	if len(p.Steps) != 1 || p.Steps[0].Kind != StepUpdate || len(p.Gone) != 0 || p.Steps[0].From[0].UUID != old.UUID {
		t.Fatalf("plan = %+v", p)
	}
	var removed []string
	_, _, err := e.m.install(ctx, p.Steps[0], func(ev Event) {
		if ev.Kind == EvRemoved {
			removed = append(removed, ev.File)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != old.Path {
		t.Errorf("removed = %v", removed)
	}
	if _, err := os.Stat(filepath.Join(e.m.Cfg.LibraryDir, old.Path)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old file still on disk: %v", err)
	}
	if s := e.state("wikipedia_en_100"); s != UpToDate {
		t.Errorf("state after update = %v", s)
	}
	xml, _ := os.ReadFile(e.m.Cfg.LibraryXMLPath())
	if strings.Contains(string(xml), old.Path) || strings.Count(string(xml), "<book ") != 1 {
		t.Errorf("library.xml still lists the old file:\n%s", xml)
	}
}

func TestKeepVersions(t *testing.T) {
	e := newEnv(t)
	e.m.Cfg.KeepVersions = 2
	ctx := context.Background()
	first := e.add("devdocs_en_go")
	for range 2 {
		e.fk.Bump("devdocs_en_go")
		e.sync()
		if _, err := e.m.Install(ctx, e.entry("devdocs_en_go"), nil); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := e.m.Store.FilesByKey(ctx, catalog.ParseKey("devdocs_en_go"))
	if len(files) != 2 {
		t.Fatalf("kept %d versions, want 2", len(files))
	}
	for _, f := range files {
		if f.UUID == first.UUID {
			t.Error("the oldest version should have been pruned")
		}
	}
}

func TestFailedUpdateKeepsOldFile(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	old := e.add("wikipedia_en_100")
	e.fk.Bump("wikipedia_en_100")
	e.sync()
	e.fk.BreakAll()
	if _, err := e.m.Install(ctx, e.entry("wikipedia_en_100"), nil); err == nil {
		t.Fatal("expected the update to fail when every source sends bad data")
	}
	if active, _ := e.m.Store.ActiveFile(ctx, catalog.ParseKey("wikipedia_en_100")); active == nil || active.UUID != old.UUID {
		t.Fatalf("active file changed after a failed update: %+v", active)
	}
	if _, err := os.Stat(filepath.Join(e.m.Cfg.LibraryDir, old.Path)); err != nil {
		t.Fatalf("old file gone after a failed update: %v", err)
	}
	if s := e.state("wikipedia_en_100"); s != Downloading {
		t.Errorf("state = %v; the partial download should be kept for a retry", s)
	}
}

func TestGoneUpstream(t *testing.T) {
	e := newEnv(t)
	e.add("devdocs_en_go")
	e.fk.Remove("devdocs_en_go")
	e.sync()
	if s := e.state("devdocs_en_go"); s != Orphaned {
		t.Fatalf("state = %v, want Orphaned", s)
	}
	if p := e.plan(false); len(p.Steps) != 0 || !slices.Equal(p.Gone, []string{"devdocs_en_go"}) {
		t.Fatalf("plan = %+v; the copy must be kept", p)
	}
	xml, _ := os.ReadFile(e.m.Cfg.LibraryXMLPath())
	if !strings.Contains(string(xml), "devdocs_en_go") {
		t.Error("a ZIM gone upstream must stay in library.xml")
	}
}

func TestRemoveAndPrune(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := e.add("devdocs_en_go")
	stray := filepath.Join(e.m.Cfg.PartialDir(), "stray.part")
	if err := os.MkdirAll(e.m.Cfg.PartialDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stray, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := e.m.Prune(ctx, true)
	if err != nil || len(plan) != 1 || plan[0].Path != stray {
		t.Fatalf("prune plan = %+v, %v", plan, err)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Fatal("dry run deleted the file")
	}
	if _, err := e.m.Prune(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stray); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("prune left the stray file")
	}

	removed, err := e.m.Remove(ctx, []catalog.Key{f.Key()}, false)
	if err != nil || len(removed) != 1 || removed[0].Size != f.Size {
		t.Fatalf("removed = %+v, %v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(e.m.Cfg.LibraryDir, f.Path)); !errors.Is(err, os.ErrNotExist) {
		t.Error("file still on disk after remove")
	}
	// library.toml still wants it: missing, and apply would download it again.
	if s := e.state("devdocs_en_go"); s != Missing {
		t.Errorf("state after remove = %v", s)
	}
	e.edit(func(d *setup.Doc) { d.DeleteItem("devdocs_en_go") })
	if s := e.state("devdocs_en_go"); s != NotInLibrary {
		t.Errorf("state after unwanting = %v", s)
	}
}

func TestVerifyDetectsCorruption(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := e.add("devdocs_en_go")
	res, err := e.m.Verify(ctx, nil, nil)
	if err != nil || len(res) != 1 || res[0].Err != nil {
		t.Fatalf("verify clean file: %+v %v", res, err)
	}
	path := filepath.Join(e.m.Cfg.LibraryDir, f.Path)
	fh, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.WriteAt([]byte{0xAA}, 1000); err != nil {
		t.Fatal(err)
	}
	fh.Close()
	res, err = e.m.Verify(ctx, nil, nil)
	if err != nil || len(res) != 1 || res[0].Err == nil {
		t.Fatalf("verify corrupt file: %+v %v", res, err)
	}
}

func TestNotEnoughSpace(t *testing.T) {
	e := newEnv(t)
	free, ok := freeBytes(e.m.Cfg.StateDir)
	if !ok {
		t.Skip("no statfs here")
	}
	// A .meta4 that claims more than the free space; nothing is downloaded.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `<metalink xmlns="urn:ietf:params:xml:ns:metalink"><file name="huge.zim"><size>%d</size></file></metalink>`, free*2)
	}))
	defer srv.Close()
	entry := catalog.Entry{UUID: "big", Name: "huge_en_all", Date: "2026-10-01", Meta4URL: srv.URL + "/huge.zim.meta4"}
	_, err := e.m.Install(context.Background(), entry, nil)
	if !errors.Is(err, ErrNoSpace) {
		t.Fatalf("err = %v, want ErrNoSpace", err)
	}
}

func TestRepublishSameFileName(t *testing.T) {
	for _, keep := range []int{1, 2} {
		t.Run(fmt.Sprintf("keep_versions=%d", keep), func(t *testing.T) {
			e := newEnv(t)
			e.m.Cfg.KeepVersions = keep
			ctx := context.Background()
			old := e.add("devdocs_en_go")
			e.fk.Republish("devdocs_en_go")
			e.sync()
			f, err := e.m.Install(ctx, e.entry("devdocs_en_go"), nil)
			if err != nil {
				t.Fatal(err)
			}
			// With one version kept, the new file takes the name back once
			// the old one is gone: book URLs don't change with an update.
			if f.UUID == old.UUID || (f.Path == old.Path) != (keep == 1) {
				t.Fatalf("keep_versions %d: new file %+v, old path %s", keep, f, old.Path)
			}
			if got, _ := e.m.Store.ActiveFile(ctx, f.Key()); got == nil || got.Path != f.Path {
				t.Fatalf("recorded at %+v, installed at %s", got, f.Path)
			}
			if xml, _ := os.ReadFile(e.m.Cfg.LibraryXMLPath()); !strings.Contains(string(xml), f.Path) {
				t.Fatalf("library.xml does not list %s:\n%s", f.Path, xml)
			}
			book, _ := e.fk.Book("devdocs_en_go")
			data, err := os.ReadFile(filepath.Join(e.m.Cfg.LibraryDir, f.Path))
			if err != nil || len(data) != len(e.fk.Content(book)) {
				t.Fatalf("new file missing after install: %v", err)
			}
			if _, err := e.m.Prune(ctx, false); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(e.m.Cfg.LibraryDir, f.Path)); err != nil {
				t.Fatalf("prune deleted the active file: %v", err)
			}
		})
	}
}

func TestRepublishAfterOldFileVanished(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	old := e.add("devdocs_en_go")
	if err := os.Remove(filepath.Join(e.m.Cfg.LibraryDir, old.Path)); err != nil {
		t.Fatal(err)
	}
	e.fk.Republish("devdocs_en_go")
	e.sync()
	f, err := e.m.Install(ctx, e.entry("devdocs_en_go"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.m.Cfg.LibraryDir, f.Path)); err != nil {
		t.Fatalf("the new file was deleted: %v", err)
	}
	if files, _ := e.m.Store.FilesByKey(ctx, f.Key()); len(files) != 1 {
		t.Errorf("old row should be gone, have %d rows", len(files))
	}
}

// The catalog only lists the latest release, so one with no readable date
// still replaces the file: "never back in time" can't hold it back.
func TestUpdateWithoutReleaseDate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.add("devdocs_en_go")
	ent := e.entry("devdocs_en_go")
	ent.UUID, ent.Date = "a-new-release", ""
	if err := e.m.Store.ReplaceCatalog(ctx, []catalog.Entry{ent}, ""); err != nil {
		t.Fatal(err)
	}
	if s := e.state("devdocs_en_go"); s != UpdateAvailable {
		t.Errorf("state = %v", s)
	}
	checkSteps(t, e.plan(true), "update devdocs_en_go")

	// A dated release older than the file is not an update.
	ent.Date = "2020-01-01"
	if err := e.m.Store.ReplaceCatalog(ctx, []catalog.Entry{ent}, ""); err != nil {
		t.Fatal(err)
	}
	checkSteps(t, e.plan(true))
}

func TestStateNames(t *testing.T) {
	ids, labels := map[string]bool{}, map[string]bool{}
	for s := NotInLibrary; s <= Held; s++ {
		id, err := s.MarshalText()
		if err != nil || len(id) == 0 || ids[string(id)] || s.String() == "" || labels[s.String()] {
			t.Errorf("state %d: id %q, label %q, %v", s, id, s.String(), err)
		}
		ids[string(id)], labels[s.String()] = true, true
		var back State
		if err := back.UnmarshalText(id); err != nil || back != s {
			t.Errorf("state %d: %q reads back as %d, %v", s, id, back, err)
		}
	}
	back := Missing
	if err := back.UnmarshalText([]byte("from_the_future")); err != nil || back != NotInLibrary {
		t.Errorf("an unknown id reads as %d, %v; want NotInLibrary", back, err)
	}
}

func TestSortedItems(t *testing.T) {
	e := newEnv(t)
	e.add("wiki_en_all:mini")
	e.add("devdocs_en_go")
	e.want("wiki_en_all:nopic") // mini stays on disk, unwanted
	items, err := e.m.Items(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range SortedItems(items) {
		got = append(got, it.Key.String()+" "+it.State.String())
	}
	want := []string{"devdocs_en_go up to date", "wiki_en_all:mini not wanted", "wiki_en_all:nopic missing"}
	if !slices.Equal(got, want) {
		t.Errorf("items = %q, want %q", got, want)
	}
}

func TestFreeSpace(t *testing.T) {
	e := newEnv(t)
	want, ok := freeBytes(t.TempDir())
	if !ok {
		t.Skip("no statfs here")
	}
	// The library dir does not exist yet: its nearest parent counts.
	e.m.Cfg.LibraryDir = filepath.Join(t.TempDir(), "not", "yet")
	got, ok := e.m.FreeSpace()
	// Free space moves while tests run; same filesystem, same ballpark.
	if !ok || got <= 0 || got > want*2 || got < want/2 {
		t.Errorf("FreeSpace = %d, %v; statfs says %d", got, ok, want)
	}
}

func TestPruneConfirmedOnlyDeletesWhatWasShown(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := os.MkdirAll(e.m.Cfg.PartialDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(e.m.Cfg.PartialDir(), "a.part")
	b := filepath.Join(e.m.Cfg.PartialDir(), "b.part")
	if err := os.WriteFile(a, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	shown, _ := e.m.Prune(ctx, true)
	if err := os.WriteFile(b, []byte("b"), 0o644); err != nil { // appeared after the prompt
		t.Fatal(err)
	}
	if _, err := e.m.PruneConfirmed(ctx, shown); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a); !errors.Is(err, os.ErrNotExist) {
		t.Error("the confirmed file is still there")
	}
	if _, err := os.Stat(b); err != nil {
		t.Error("a file nobody confirmed was deleted")
	}
}
