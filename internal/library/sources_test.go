package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/download"
	"github.com/kazie/zimon/internal/fakekiwix"
	"github.com/kazie/zimon/internal/setup"
	"github.com/kazie/zimon/internal/store"
)

// addSource adds a ZIM from a URL or file the way zimon add does.
func (e *env) addSource(from, name string) *Plan {
	e.t.Helper()
	ctx := context.Background()
	f, err := e.m.AddSource(ctx, from, name, "")
	if err != nil {
		e.t.Fatal(err)
	}
	ch, err := e.m.AddChange(ctx, []catalog.Entry{f.Entry}, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	ch.Sources = []*Found{f}
	return e.change(ch)
}

// noted checks a source and records it, as a change does, and returns its
// release.
func (e *env) noted(from, sha string) (catalog.Entry, error) {
	ctx := context.Background()
	f, err := e.m.AddSource(ctx, from, "", sha)
	if err != nil {
		return catalog.Entry{}, err
	}
	return f.Entry, e.m.NoteSources(ctx, []*Found{f})
}

func (e *env) file(key string) *store.File {
	e.t.Helper()
	f, err := e.m.Store.ActiveFile(context.Background(), catalog.ParseKey(key))
	if err != nil || f == nil {
		e.t.Fatalf("no file of %s: %v", key, err)
	}
	return f
}

func (e *env) checkSources(all bool) {
	e.t.Helper()
	problems, err := e.m.CheckSources(context.Background(), all)
	if err != nil || len(problems) > 0 {
		e.t.Fatalf("check sources: %v %v", problems, err)
	}
}

func TestSourceFromURL(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	url := e.srv.URL + "/own/mysite_en_all_2026-11.zim"
	e.fk.PutOwn("mysite_en_all_2026-11.zim", fakekiwix.OwnZIM("mysite_en_all", 1), fakekiwix.OwnDate(1))

	if _, err := e.m.AddSource(ctx, url, "wiki_en_all", ""); err == nil || !strings.Contains(err.Error(), "is in the catalog") {
		t.Errorf("a catalog's name: %v", err)
	}
	if _, err := e.m.AddSource(ctx, e.srv.URL+"/own/nothere.zim", "", ""); err == nil {
		t.Error("added a 404")
	}

	checkSteps(t, e.addSource(url, ""), "add mysite_en_all") // named after the file
	checkDisk(t, e, "mysite_en_all")
	if text := e.setupText(); !strings.Contains(text, "[sources]\n\"mysite_en_all\" = \""+url+"\"\n") {
		t.Errorf("library.toml:\n%s", text)
	}
	f := e.file("mysite_en_all")
	if f.Title != "My mysite_en_all" || f.Path != "mysite_en_all_2026-11.zim" || f.Date != "2026-11-01" {
		t.Errorf("file: %+v", f)
	}
	if entry := e.entry("mysite_en_all"); entry.Title != "My mysite_en_all" || entry.Source != url {
		t.Errorf("entry: %+v", entry)
	}

	// Unchanged: nothing to do, and nothing downloaded.
	hits := e.fk.Hits()["own"]
	e.checkSources(true)
	checkSteps(t, e.plan(true))
	if got := e.fk.Hits()["own"]; got != hits {
		t.Errorf("checking downloaded: %d hits, then %d", hits, got)
	}

	// A new release is an update.
	e.fk.PutOwn("mysite_en_all_2026-11.zim", fakekiwix.OwnZIM("mysite_en_all", 2), fakekiwix.OwnDate(2))
	e.checkSources(false) // checked already: not looked at again until a refresh
	checkSteps(t, e.plan(true))
	e.checkSources(true)
	checkSteps(t, e.apply(), "update mysite_en_all")
	if f := e.file("mysite_en_all"); f.Date != "2026-12-01" || f.Path != "mysite_en_all_2026-11.zim" {
		t.Errorf("after the update: %+v", f) // it has the old file's name back: book URLs stay
	}

	// Removing it drops the source, and the next change forgets it.
	ch, err := e.m.RemoveChange(ctx, nil, []catalog.Key{{Name: "mysite_en_all"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	checkSteps(t, e.change(ch), "remove mysite_en_all")
	if strings.Contains(e.setupText(), "mysite") {
		t.Errorf("library.toml:\n%s", e.setupText())
	}
	if err := e.m.ForgetSources(ctx); err != nil {
		t.Fatal(err)
	}
	if es, _ := e.m.Store.EntriesByName(ctx, "mysite_en_all"); len(es) != 0 {
		t.Errorf("entries left: %+v", es)
	}
}

func TestSourceSHA256(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	body := fakekiwix.OwnZIM("site_en_all", 1)
	e.fk.PutOwn("site.zim", body, fakekiwix.OwnDate(1))
	url := e.srv.URL + "/own/site.zim"
	sum := sha256.Sum256(body)

	if _, err := e.m.AddSource(ctx, url, "", "abc"); err == nil {
		t.Error("took a short sha-256")
	}
	entry, err := e.noted(url, strings.Repeat("0", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Install(ctx, entry, nil); !errors.Is(err, download.ErrHashMismatch) {
		t.Errorf("a wrong sha-256: %v", err)
	}
	var verified string
	entry, err = e.noted(url, hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Install(ctx, entry, func(ev Event) {
		if ev.Kind == EvDone && ev.Msg != "" {
			verified = ev.Msg
		}
	}); err != nil {
		t.Fatal(err)
	}
	if verified != "matches the sha-256 given" {
		t.Errorf("verified = %q", verified)
	}
}

func TestSourceFromFile(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "build.zim")
	write := func(n int) {
		t.Helper()
		if err := os.WriteFile(src, fakekiwix.OwnZIM("notes_en_all", n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A symbolic link to the build, as from a build script's "latest".
	link := filepath.Join(dir, "latest.zim")
	if err := os.Symlink("build.zim", link); err != nil {
		t.Fatal(err)
	}
	write(1)
	if _, err := e.m.AddSource(ctx, "build.zim", "", ""); err == nil {
		t.Error("took a relative path")
	}
	checkSteps(t, e.addSource(link, ""), "add notes_en_all") // named after the ZIM's own name
	f := e.file("notes_en_all")
	if f.Path != "latest.zim" || f.Title != "My notes_en_all" || f.Date != "2026-11-01" {
		t.Errorf("file: %+v", f)
	}
	inLib := filepath.Join(e.m.Cfg.LibraryDir, f.Path)
	if st, err := os.Lstat(inLib); err != nil || !st.Mode().IsRegular() {
		t.Fatalf("in the library: %v, %v", st, err)
	}

	// The build changes in place: the library's copy (a clone, or a copy
	// here, where cloneFile fails) does not.
	cloneFile = func(string, string) error { return errors.New("can't clone") }
	t.Cleanup(func() { cloneFile = cloneOf })
	want, _ := os.ReadFile(inLib)
	write(2)
	if got, _ := os.ReadFile(inLib); !bytes.Equal(got, want) {
		t.Error("changing the build changed the library's file")
	}

	// It is an update; the copy replaces the old one.
	e.checkSources(true)
	checkSteps(t, e.apply(), "update notes_en_all")
	f = e.file("notes_en_all")
	if got, _ := os.ReadFile(filepath.Join(e.m.Cfg.LibraryDir, f.Path)); f.Date != "2026-12-01" || f.Path != "latest.zim" ||
		!bytes.Equal(got, fakekiwix.OwnZIM("notes_en_all", 2)) {
		t.Errorf("copy: %+v", f) // under the old one's name, which is gone
	}

	// A file in the library folder stays where it is.
	in := filepath.Join(e.m.Cfg.LibraryDir, "dropped.zim")
	if err := os.WriteFile(in, fakekiwix.OwnZIM("dropped_en_all", 1), 0o644); err != nil {
		t.Fatal(err)
	}
	checkSteps(t, e.addSource(in, "mine"), "add mine")
	if f := e.file("mine"); f.Path != "dropped.zim" {
		t.Errorf("in place: %+v", f)
	}

	// --sha256 checks a file too.
	other := filepath.Join(dir, "other.zim")
	if err := os.WriteFile(other, fakekiwix.OwnZIM("other_en_all", 1), 0o644); err != nil {
		t.Fatal(err)
	}
	entry, err := e.noted(other, strings.Repeat("0", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Install(ctx, entry, nil); !errors.Is(err, download.ErrHashMismatch) {
		t.Errorf("a wrong sha-256: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.m.Cfg.LibraryDir, "other.zim")); !os.IsNotExist(err) {
		t.Errorf("a file that failed its check is in the library: %v", err)
	}

	// Gone: a problem, and the copy here stays.
	if err := os.Remove(src); err != nil {
		t.Fatal(err)
	}
	if problems, err := e.m.CheckSources(ctx, true); err != nil || len(problems) != 1 {
		t.Errorf("problems %v, %v", problems, err)
	}
	checkSteps(t, e.plan(false))
}

func TestSourceOfAKnownRelease(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.fk.PutOwn("site.zim", fakekiwix.OwnZIM("site_en_all", 1), fakekiwix.OwnDate(1))
	url := e.srv.URL + "/own/site.zim"
	e.addSource(url, "")
	if _, err := e.m.AddSource(ctx, url, "mine", ""); err == nil || !strings.Contains(err.Error(), "already comes from") {
		t.Errorf("the same release under another name: %v", err)
	}
	// A catalog's release, under its file name.
	b, _ := e.fk.Book("wiki_en_all:maxi")
	if _, err := e.m.AddSource(ctx, e.srv.URL+"/zim/other/"+b.FileName(), "", ""); err == nil ||
		!strings.Contains(err.Error(), "a release of wiki_en_all:maxi from the catalog: zimon add wiki_en_all:maxi") {
		t.Errorf("a catalog's release: %v", err)
	}
}

func TestSourcePreviewAndPlaces(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.fk.PutOwn("site.zim", fakekiwix.OwnZIM("site_en_all", 1), fakekiwix.OwnDate(1))
	url := e.srv.URL + "/own/site.zim"

	// A preview plans with what it found, and records nothing.
	f, err := e.m.AddSource(ctx, url, "", "")
	if err != nil {
		t.Fatal(err)
	}
	ch, err := e.m.AddChange(ctx, []catalog.Entry{f.Entry}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ch.Sources = []*Found{f}
	p, err := e.m.Preview(ctx, ch)
	if err != nil {
		t.Fatal(err)
	}
	checkSteps(t, p.Plan, "add site")
	if es, _ := e.m.Store.EntriesByName(ctx, "site"); len(es) != 0 {
		t.Errorf("a preview recorded %+v", es)
	}

	// Once added, a [sources] line pointing elsewhere is not planned from
	// the old place, even when the new one can't be reached.
	e.change(ch)
	e.edit(func(d *setup.Doc) { d.SetSource("site", e.srv.URL+"/own/moved.zim") })
	if problems, err := e.m.CheckSources(ctx, false); err != nil || len(problems) != 1 {
		t.Errorf("problems %v, %v", problems, err)
	}
	p2 := e.plan(false)
	checkSteps(t, p2)
	if len(p2.Problems) != 1 || !strings.Contains(p2.Problems[0].String(), "/own/moved.zim, which zimon could not check") {
		t.Errorf("problems: %v", p2.Problems)
	}

	// A hand-written source of a catalog's release, under another name.
	b, _ := e.fk.Book("wiki_en_all:maxi")
	e.edit(func(d *setup.Doc) { d.SetSource("mywiki", e.srv.URL+"/zim/other/"+b.FileName()) })
	problems, err := e.m.CheckSources(ctx, false)
	if err != nil || len(problems) != 2 || !strings.Contains(problems[0].Error(), "known under another name: wiki_en_all:maxi") {
		t.Errorf("problems %v, %v", problems, err)
	}
	if got := e.entry("wiki_en_all:maxi"); got.Source != "" {
		t.Errorf("the catalog's entry was taken: %+v", got)
	}
}

func TestNameOfFile(t *testing.T) {
	for from, want := range map[string]string{
		"/x/mysite_en_all_2026-01.zim":                 "mysite_en_all",
		"/x/mysite_en_all_2026-01-15.zim":              "mysite_en_all",
		"https://h/zims/site.zim?x=1":                  "site",
		"https://h/download/wikipedia_en_all_maxi.zim": "wikipedia_en_all_maxi",
	} {
		if got := nameOfFile(from); got != want {
			t.Errorf("%s: %q, want %q", from, got, want)
		}
	}
}

func TestExtraCatalogWithDirectLinks(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	fk := fakekiwix.New([]fakekiwix.Book{
		{Name: "extra_en_all", Title: "Extra", Language: "eng", Date: "2026-10-05", Size: 300 << 10, ListedSize: 1 << 20},
		{Name: "devdocs_en_go", Title: "Newer Go Docs", Language: "eng", Date: "2026-11-01", Size: 100 << 10},
	})
	fk.DirectLinks = true
	srv := httptest.NewServer(fk)
	t.Cleanup(srv.Close)
	e.m.Cfg.Catalogs = []string{srv.URL + "/catalog/v2"}
	refresh := func() (int, bool) {
		t.Helper()
		n, changed, problems, err := RefreshCatalog(ctx, e.m.Store, e.srv.Client(), e.m.Cfg.CatalogURLs())
		if err != nil || len(problems) > 0 {
			t.Fatal(problems, err)
		}
		return n, changed
	}
	if n, changed := refresh(); n != len(books)+1 || !changed {
		t.Errorf("first refresh: %d entries, changed %v", n, changed)
	}
	if got := e.entry("devdocs_en_go"); got.Title != "Newer Go Docs" { // the newest wins
		t.Errorf("devdocs_en_go from %s", got.Meta4URL)
	}
	if _, changed := refresh(); changed {
		t.Error("nothing changed, but the catalog was written again")
	}
	e.fk.Bump("huge_en_all") // one changes: the other's entries stay, without fetching it again
	feeds := fk.Hits()["feed"]
	if n, changed := refresh(); n != len(books)+1 || !changed || fk.Hits()["feed"] != feeds {
		t.Errorf("after a bump: %d entries, changed %v, the extra catalog fetched %d times", n, changed, fk.Hits()["feed"]-feeds)
	}

	// An extra catalog that fails keeps its entries; Kiwix's still refreshes.
	e.m.Cfg.Catalogs = append(e.m.Cfg.Catalogs, e.srv.URL+"/nothere/catalog/v2")
	e.fk.Bump("huge_en_all")
	n, changed, problems, err := RefreshCatalog(ctx, e.m.Store, e.srv.Client(), e.m.Cfg.CatalogURLs())
	if err != nil || len(problems) != 1 || !changed || n != len(books)+1 {
		t.Errorf("a failing catalog: %d entries, changed %v, problems %v, %v", n, changed, problems, err)
	}
	// One taken out of catalogs takes its entries with it.
	e.m.Cfg.Catalogs = nil
	if n, _ := refresh(); n != len(books) {
		t.Errorf("without the extra catalog: %d entries", n)
	}
	e.m.Cfg.Catalogs = []string{srv.URL + "/catalog/v2"}
	refresh()

	var verified string
	f, err := e.m.Install(ctx, e.entry("extra_en_all"), func(ev Event) {
		if ev.Kind == EvDone && ev.Msg != "" {
			verified = ev.Msg
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.Size != 300<<10 || !strings.HasPrefix(verified, "not verified: 127.0.0.1:") {
		t.Errorf("file %+v, verified %q", f, verified)
	}
}
