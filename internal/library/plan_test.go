package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/collections"
	"github.com/kazie/zimon/internal/setup"
	"github.com/kazie/zimon/internal/store"
)

// deferred lists a plan's deferred steps as "reason kind key".
func deferred(p *Plan) []string {
	var out []string
	for _, d := range p.Deferred {
		out = append(out, [...]string{"apply", "budget", "ask", "problem", "held"}[d.Reason]+" "+[...]string{"remove", "update", "switch", "add"}[d.Kind]+" "+d.Key.String())
	}
	return out
}

func checkSteps(t *testing.T, p *Plan, want ...string) {
	t.Helper()
	if got := steps(p); !slices.Equal(got, want) {
		t.Errorf("steps = %q, want %q", got, want)
	}
}

func checkDisk(t *testing.T, e *env, want ...string) {
	t.Helper()
	if got := e.onDisk(); !slices.Equal(got, want) {
		t.Errorf("on disk = %q, want %q", got, want)
	}
}

func TestApplyLevelsUpAndDown(t *testing.T) {
	e := newEnv(t)
	e.edit(func(d *setup.Doc) { d.SetCollection("basics", collections.Light) })
	if p := e.plan(false); p.Download() != 100<<10 || p.Freed() != 0 {
		t.Errorf("light: download %d, frees %d", p.Download(), p.Freed())
	}
	checkSteps(t, e.apply(), "add wiki_en_all:mini")
	checkDisk(t, e, "wiki_en_all:mini")

	e.edit(func(d *setup.Doc) { d.SetCollection("basics", collections.Standard) })
	p := e.apply()
	checkSteps(t, p, "switch wiki_en_all:nopic", "add devdocs_en_go")
	if p.Steps[0].Freed != 100<<10 || p.Current != 100<<10 || p.After != (200+200)<<10 {
		t.Errorf("sizes: freed %d, current %d, after %d", p.Steps[0].Freed, p.Current, p.After)
	}
	checkDisk(t, e, "devdocs_en_go", "wiki_en_all:nopic")

	// Deep: maxi and the deep item, but never the giant.
	e.edit(func(d *setup.Doc) { d.SetCollection("basics", collections.Deep) })
	checkSteps(t, e.apply(), "switch wiki_en_all:maxi", "add wikipedia_en_100")
	checkDisk(t, e, "devdocs_en_go", "wiki_en_all:maxi", "wikipedia_en_100")

	// Back down to light. devdocs_en_go stays: docs wants it too, and so
	// does the hand-picked giant.
	e.edit(func(d *setup.Doc) {
		d.SetCollection("basics", collections.Light)
		d.SetCollection("docs", collections.Light)
		d.SetItem("huge_en_all", setup.On)
	})
	checkSteps(t, e.apply(), "remove wikipedia_en_100", "switch wiki_en_all:mini", "add huge_en_all")
	checkDisk(t, e, "devdocs_en_go", "huge_en_all", "wiki_en_all:mini")
	if p := e.plan(false); len(p.Steps) != 0 {
		t.Errorf("apply should leave nothing to do: %q", steps(p))
	}

	// Unsubscribing removes what nothing else wants.
	e.edit(func(d *setup.Doc) { d.DeleteCollection("basics") })
	checkSteps(t, e.apply(), "remove wiki_en_all:mini")
	checkDisk(t, e, "devdocs_en_go", "huge_en_all")
}

// A typo in library.toml must not cost files: apply holds every deletion
// back until it is fixed, but still downloads what is wanted.
func TestTypoHoldsDeletions(t *testing.T) {
	e := newEnv(t)
	reset := func(d *setup.Doc) {
		d.SetCollection("basics", collections.Standard)
		d.DeleteCollection("basic")
		d.SetItem("huge_en_all", setup.On)
		d.DeleteItem("md_en_all")
	}
	e.edit(reset)
	e.apply() // devdocs_en_go, wiki_en_all:nopic, huge_en_all
	for _, c := range []struct {
		name     string
		typo     func(d *setup.Doc)
		steps    []string
		deferred []string
	}{
		{"unknown collection", func(d *setup.Doc) { d.DeleteCollection("basics"); d.SetCollection("basic", collections.Standard) },
			nil, []string{"problem remove devdocs_en_go", "problem remove wiki_en_all:nopic"}},
		{"no such flavour", func(d *setup.Doc) { d.SetItem("huge_en_all", "maxi") },
			nil, []string{"problem remove huge_en_all"}},
		{"downloads go ahead", func(d *setup.Doc) { d.SetItem("huge_en_all", "maxi"); d.SetItem("md_en_all", "maxi") },
			[]string{"add md_en_all:maxi"}, []string{"problem remove huge_en_all"}},
	} {
		e.edit(func(d *setup.Doc) { reset(d); c.typo(d) })
		p := e.plan(false)
		if got := steps(p); !slices.Equal(got, c.steps) || !slices.Equal(deferred(p), c.deferred) {
			t.Errorf("%s: steps %q, deferred %q", c.name, got, deferred(p))
		}
	}

	// A command that names what it removes is not held back.
	e.edit(func(d *setup.Doc) { d.SetItem("huge_en_all", "maxi") })
	ch, err := e.m.RemoveChange(context.Background(), nil, []catalog.Key{{Name: "devdocs_en_go"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	checkSteps(t, e.change(ch), "remove devdocs_en_go")
}

// A region that is missing or names no map, and a misspelled [items] name,
// hold deletions too: maps or a ZIM the user still wants could go.
func TestRegionAndNameTyposHoldDeletions(t *testing.T) {
	e := newEnv(t)
	e.m.Collections = append(slices.Clone(testCollections),
		collections.Collection{ID: "maps", Items: []collections.Item{{Name: "maps_{region}", Tier: collections.Core}}})
	write := func(lines ...string) {
		t.Helper()
		if err := os.WriteFile(e.m.SetupPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("[collections]", `basics = "standard"`, "[items]", `huge_en_all = "on"`)
	e.apply()
	for _, c := range []struct {
		name  string
		lines []string
	}{
		{"no region", []string{"[collections]", `basics = "standard"`, `maps = "standard"`}},
		{"a region with no map", []string{`region = "swedn"`, "[collections]", `basics = "standard"`, `maps = "standard"`}},
		{"a misspelled name", []string{"[collections]", `basics = "standard"`, "[items]", `huge_en_al = "on"`}},
	} {
		write(c.lines...)
		p := e.plan(false)
		if len(p.Steps) != 0 || !slices.Equal(deferred(p), []string{"problem remove huge_en_all"}) {
			t.Errorf("%s: steps %q, deferred %q", c.name, steps(p), deferred(p))
		}
	}
}

// Problems the user can't fix in library.toml don't hold deletions
// library-wide: a collection's flavour gone upstream keeps only that ZIM's
// copies.
func TestCatalogProblemsDontHoldDeletions(t *testing.T) {
	e := newEnv(t)
	e.m.Collections = append(slices.Clone(testCollections),
		collections.Collection{ID: "pinned", Items: []collections.Item{{Name: "md_en_all", Flavour: "maxi", Tier: collections.Core}}})
	e.edit(func(d *setup.Doc) {
		d.SetCollection("pinned", collections.Standard)
		d.SetItem("devdocs_en_go", setup.On)
	})
	e.apply()
	checkDisk(t, e, "devdocs_en_go", "md_en_all:maxi")

	// Kiwix drops md_en_all:maxi, and the user drops devdocs_en_go.
	e.fk.Remove("md_en_all:maxi")
	e.sync()
	e.edit(func(d *setup.Doc) { d.DeleteItem("devdocs_en_go") })
	p := e.plan(false)
	checkSteps(t, p, "remove devdocs_en_go")
	if len(p.Deferred) != 0 || !slices.Equal(p.Gone, []string{"md_en_all"}) {
		t.Errorf("deferred %q, gone %q; want md_en_all kept as gone", deferred(p), p.Gone)
	}
}

// While deletions are held, prune keeps the partial downloads the typo
// made unwanted.
func TestTypoKeepsPartials(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.edit(func(d *setup.Doc) { d.SetCollection("docs", collections.Standard) })
	dl, _, err := e.m.prepareDownload(ctx, e.entry("devdocs_en_go"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.m.partPath(dl.UUID), []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.edit(func(d *setup.Doc) { d.DeleteCollection("docs"); d.SetCollection("doc", collections.Standard) })
	if plan, _ := e.m.Prune(ctx, true); len(plan) != 0 {
		t.Errorf("prune would delete a partial while library.toml has a typo: %+v", plan)
	}
	e.edit(func(d *setup.Doc) { d.DeleteCollection("doc") })
	if plan, _ := e.m.Prune(ctx, true); len(plan) != 1 {
		t.Errorf("prune keeps an unwanted partial with no typo: %+v", plan)
	}
}

func TestItemsOverrideAndOff(t *testing.T) {
	e := newEnv(t)
	e.edit(func(d *setup.Doc) {
		d.SetCollection("basics", collections.Standard)
		d.SetItem("wiki_en_all", "maxi")
		d.SetItem("devdocs_en_go", setup.Off)
	})
	checkSteps(t, e.apply(), "add wiki_en_all:maxi")
	items, _ := e.m.Items(context.Background())
	it := items[catalog.ParseKey("wiki_en_all:maxi")]
	if it == nil || !it.Wanted || !slices.Equal(it.Want.Via, []string{"basics", setup.ViaItems}) {
		t.Errorf("item = %+v", it)
	}
}

func TestUnwantedFileIsRemovedByApplyOnly(t *testing.T) {
	e := newEnv(t)
	f := e.add("devdocs_en_go")
	e.edit(func(d *setup.Doc) { d.DeleteItem("devdocs_en_go") })
	if s := e.state("devdocs_en_go"); s != Unwanted {
		t.Fatalf("state = %v", s)
	}
	if p := e.plan(true); len(p.Steps) != 0 || !slices.Equal(deferred(p), []string{"apply remove devdocs_en_go"}) {
		t.Errorf("update must not delete: %q, deferred %q", steps(p), deferred(p))
	}
	p := e.apply()
	checkSteps(t, p, "remove devdocs_en_go")
	if p.Freed() != f.Size || p.After != 0 {
		t.Errorf("freed %d, after %d", p.Freed(), p.After)
	}
	checkDisk(t, e)
}

func TestFailedSwitchKeepsOldFlavour(t *testing.T) {
	e := newEnv(t)
	e.edit(func(d *setup.Doc) { d.SetCollection("basics", collections.Light) })
	e.apply()
	e.fk.BreakAll()
	e.edit(func(d *setup.Doc) { d.SetItem("wiki_en_all", "maxi") })
	p := e.plan(false)
	checkSteps(t, p, "switch wiki_en_all:maxi")
	if _, removed, err := e.m.install(context.Background(), p.Steps[0], nil); err == nil || len(removed) != 0 {
		t.Fatalf("switch against broken mirrors: removed %v, err %v", removed, err)
	}
	checkDisk(t, e, "wiki_en_all:mini")
	checkSteps(t, e.plan(false), "switch wiki_en_all:maxi") // still to do
}

func TestUnattendedPolicy(t *testing.T) {
	e := newEnv(t)
	set := func(text string) {
		t.Helper()
		if err := os.WriteFile(e.m.SetupPath, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	base := "[collections]\nbasics = \"standard\"\n" // wiki nopic 200K + devdocs 200K

	set(base) // no budget: within-budget adds everything
	checkSteps(t, e.plan(true), "add devdocs_en_go", "add wiki_en_all:nopic")

	set(`budget = "300KiB"` + "\n" + base)
	p := e.plan(true)
	checkSteps(t, p, "add devdocs_en_go")
	if d := deferred(p); !slices.Equal(d, []string{"budget add wiki_en_all:nopic"}) {
		t.Errorf("deferred = %q", d)
	}

	set(`new_items = "ask"` + "\n" + base)
	if p := e.plan(true); len(p.Steps) != 0 || !slices.Equal(deferred(p), []string{"ask add devdocs_en_go", "ask add wiki_en_all:nopic"}) {
		t.Errorf("ask: steps %q, deferred %q", steps(p), deferred(p))
	}
	// Named on the command line counts as approved.
	p, err := e.m.Plan(context.Background(), PlanOptions{Unattended: true, Only: map[string]bool{"wiki_en_all": true}})
	if err != nil {
		t.Fatal(err)
	}
	checkSteps(t, p, "add wiki_en_all:nopic")

	set(`budget = "1KiB"` + "\n" + `new_items = "always"` + "\n" + base)
	checkSteps(t, e.plan(true), "add devdocs_en_go", "add wiki_en_all:nopic")

	// apply ignores the policy and the budget (it shows them instead).
	set(`budget = "1KiB"` + "\n" + `new_items = "ask"` + "\n" + base)
	if p := e.apply(); p.After <= p.Budget {
		t.Errorf("after %d, budget %d", p.After, p.Budget)
	}
	// A flavour change waits for apply; a new release does not.
	set(base + "[items]\n\"wiki_en_all\" = \"maxi\"\n")
	e.fk.Bump("devdocs_en_go")
	e.sync()
	p = e.plan(true)
	checkSteps(t, p, "update devdocs_en_go")
	if d := deferred(p); !slices.Equal(d, []string{"apply switch wiki_en_all:maxi"}) {
		t.Errorf("deferred = %q", d)
	}
}

func TestHold(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.edit(func(d *setup.Doc) { d.SetCollection("basics", collections.Light) })
	e.apply()
	mini := e.entry("wiki_en_all:mini")

	hold := func(hold bool, args ...string) error { // only edits library.toml, as task does
		ch, err := e.m.HoldChange(ctx, args, hold)
		if err == nil {
			_, err = e.m.Edit(ctx, ch.Edit)
		}
		return err
	}
	if err := hold(true, "devdocs_en_go"); err == nil {
		t.Error("held a ZIM with no file")
	}
	if err := hold(false, "wiki_en_all"); err == nil {
		t.Error("unheld a ZIM that is not held")
	}
	if err := hold(true, "wiki_en_all:mini"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.setupText(), "[hold]\n\"wiki_en_all\" = \""+mini.Date+"\"\n") {
		t.Errorf("library.toml:\n%s", e.setupText())
	}

	// A newer release waits, for update and apply alike.
	e.fk.Bump("wiki_en_all:mini")
	e.sync()
	if s := e.state("wiki_en_all:mini"); s != Held {
		t.Errorf("state = %v", s)
	}
	for _, unattended := range []bool{true, false} {
		p := e.plan(unattended)
		checkSteps(t, p)
		if d := deferred(p); !slices.Equal(d, []string{"held update wiki_en_all:mini"}) {
			t.Errorf("unattended %v: deferred = %q", unattended, d)
		}
	}

	// So does a flavour switch, and the held flavour is not removed meanwhile.
	e.edit(func(d *setup.Doc) { d.SetItem("wiki_en_all", "maxi") })
	p := e.apply()
	checkSteps(t, p)
	if d := deferred(p); !slices.Equal(d, []string{"held switch wiki_en_all:maxi"}) {
		t.Errorf("deferred = %q", d)
	}
	checkDisk(t, e, "wiki_en_all:mini")
	if s := e.state("wiki_en_all:mini"); s != Held {
		t.Errorf("state of the old flavour = %v", s)
	}
	if s := e.state("wiki_en_all:maxi"); s != Held { // not missing: apply won't fetch it
		t.Errorf("state of the wanted flavour = %v", s)
	}

	// Unheld, it switches.
	if err := hold(false, "wiki_en_all"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(e.setupText(), "wiki_en_all\" = \""+mini.Date) {
		t.Errorf("unhold left the line:\n%s", e.setupText())
	}
	checkSteps(t, e.apply(), "switch wiki_en_all:maxi")

	// Removing a held ZIM drops its hold.
	if err := hold(true, "wiki_en_all"); err != nil {
		t.Fatal(err)
	}
	ch, err := e.m.RemoveChange(ctx, nil, []catalog.Key{{Name: "wiki_en_all", Flavour: "maxi"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	checkSteps(t, e.change(ch), "remove wiki_en_all:maxi")
	if s := e.setup(); len(s.Hold) != 0 {
		t.Errorf("library.toml:\n%s", e.setupText())
	}
}

func TestConfirmedDropsChangedSteps(t *testing.T) {
	e := newEnv(t)
	e.add("wikipedia_en_100")
	e.add("devdocs_en_go")
	e.fk.Bump("wikipedia_en_100")
	e.fk.Bump("devdocs_en_go")
	e.sync()
	confirmed := e.plan(true)
	checkSteps(t, confirmed, "update devdocs_en_go", "update wikipedia_en_100")
	// While the user was answering: one unwanted, the other re-released.
	e.edit(func(d *setup.Doc) { d.DeleteItem("wikipedia_en_100") })
	e.fk.Bump("devdocs_en_go")
	e.sync()
	if still := e.plan(true).Confirmed(confirmed); len(still) != 0 {
		t.Fatalf("confirmed = %+v; want nothing", still)
	}
}

func TestRecoveryAdoptsFilesOnDisk(t *testing.T) {
	e := newEnv(t)
	e.edit(func(d *setup.Doc) { d.SetCollection("basics", collections.Standard) })
	e.apply()
	before := e.onDisk()

	// The database is lost; library.toml and the ZIMs survive.
	e.m.Store.Close()
	if err := os.RemoveAll(e.m.Cfg.StateDir); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(e.m.Cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e.m.Store = st
	e.sync()
	e.fk.BreakAll() // nothing may be downloaded: a broken mirror would fail the test

	p := e.plan(false)
	var notes, done []string
	for _, step := range p.Steps {
		_, _, err := e.m.install(context.Background(), step, func(ev Event) {
			switch ev.Kind {
			case EvNote:
				notes = append(notes, ev.Msg)
			case EvDone:
				done = append(done, ev.Msg)
			}
		})
		if err != nil {
			t.Fatalf("%s: %v", step.Key, err)
		}
	}
	if got := e.onDisk(); !slices.Equal(got, before) {
		t.Errorf("after recovery %q, want %q", got, before)
	}
	if len(notes) != 2 || !strings.Contains(notes[0], "already in the library folder") {
		t.Errorf("notes = %q", notes)
	}
	if !slices.Contains(done, "found on disk, sha-256 verified") {
		t.Errorf("done = %q", done)
	}
	entries, _ := os.ReadDir(e.m.Cfg.LibraryDir)
	var zims int
	for _, de := range entries {
		if strings.HasSuffix(de.Name(), ".zim") {
			zims++
		}
	}
	if zims != 2 {
		t.Errorf("%d ZIMs in the library dir; adopting must not duplicate them", zims)
	}
}

func TestAdoptSkipsMismatch(t *testing.T) {
	e := newEnv(t)
	b, _ := e.fk.Book("devdocs_en_go")
	if err := os.MkdirAll(e.m.Cfg.LibraryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Same name and size, different bytes: download as usual, under another name.
	if err := os.WriteFile(filepath.Join(e.m.Cfg.LibraryDir, b.FileName()), make([]byte, len(e.fk.Content(b))), 0o644); err != nil {
		t.Fatal(err)
	}
	f := e.add("devdocs_en_go")
	if f.Path == b.FileName() {
		t.Errorf("a mismatching file was adopted")
	}
}

func TestPruneListsEachFileOnce(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.edit(func(*setup.Doc) {}) // an (empty) library.toml: nothing is wanted
	// A partial download for a ZIM that is no longer wanted.
	dl, _, err := e.m.prepareDownload(ctx, e.entry("devdocs_en_go"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.m.partPath(dl.UUID), []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := e.m.Prune(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 1 || plan[0].Size != 7 {
		t.Fatalf("plan = %+v; want the partial once, with its real size", plan)
	}
}

func TestNewReleaseReplacesOldPartial(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.want("devdocs_en_go")
	// An interrupted download of the current release...
	old, _, err := e.m.prepareDownload(ctx, e.entry("devdocs_en_go"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.m.partPath(old.UUID), []byte("old partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	// ...then Kiwix republishes under the same file name, and we install that.
	e.fk.Republish("devdocs_en_go")
	e.sync()
	if _, err := e.m.Install(ctx, e.entry("devdocs_en_go"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.m.partPath(old.UUID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the superseded partial was left behind: %v", err)
	}
	if ds, _ := e.m.Store.Downloads(ctx); len(ds) != 0 {
		t.Errorf("download rows left: %+v", ds)
	}
	if plan, _ := e.m.Prune(ctx, true); len(plan) != 0 {
		t.Errorf("nothing should be left to prune: %+v", plan)
	}
}

func TestResumeWithoutPartialDir(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.want("devdocs_en_go")
	if _, _, err := e.m.prepareDownload(ctx, e.entry("devdocs_en_go"), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(e.m.Cfg.PartialDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Install(ctx, e.entry("devdocs_en_go"), nil); err != nil {
		t.Fatalf("resuming without the partial dir: %v", err)
	}
}

func TestPartialOfDroppedZIMIsPruned(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.want("devdocs_en_go")
	if _, _, err := e.m.prepareDownload(ctx, e.entry("devdocs_en_go"), nil); err != nil {
		t.Fatal(err)
	}
	e.fk.Remove("devdocs_en_go")
	e.sync()
	if s := e.state("devdocs_en_go"); s != Orphaned {
		t.Errorf("state = %v, want Orphaned", s)
	}
	if plan, _ := e.m.Prune(ctx, true); len(plan) != 1 || plan[0].Reason != "stale partial download" {
		t.Errorf("prune plan = %+v", plan)
	}
}

func TestPartialOfWantedZIMIsKeptAndResumed(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.want("devdocs_en_go")
	// An interrupted download, a quarter done.
	dl, _, err := e.m.prepareDownload(ctx, e.entry("devdocs_en_go"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.m.Store.Checkpoint(ctx, dl.UUID, 50<<10, nil); err != nil {
		t.Fatal(err)
	}
	if s := e.state("devdocs_en_go"); s != Downloading {
		t.Fatalf("state = %v", s)
	}
	if plan, _ := e.m.Prune(ctx, true); len(plan) != 0 {
		t.Errorf("prune would delete a wanted partial: %+v", plan)
	}
	p := e.plan(true)
	checkSteps(t, p, "add devdocs_en_go")
	if p.Steps[0].Bytes >= 200<<10 {
		t.Errorf("bytes left = %d; resuming should count only what is missing", p.Steps[0].Bytes)
	}
}

func TestPreviewAndCommit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.add("wikipedia_en_100") // hand-picked, outside the change below

	ch := Change{Edit: func(d *setup.Doc) error { d.SetCollection("basics", collections.Light); return nil }, Collections: []string{"basics"}}
	p, err := e.m.Preview(ctx, ch)
	if err != nil {
		t.Fatal(err)
	}
	checkSteps(t, p.Plan, "add wiki_en_all:mini")
	if s, _ := e.m.LoadSetup(ctx); len(s.Collections) != 0 {
		t.Fatal("Preview saved library.toml")
	}
	if len(p.Problems) != 0 {
		t.Errorf("problems = %#v; want none new", p.Problems)
	}
	steps, saved, err := e.m.commit(ctx, ch, p)
	if err != nil || !saved {
		t.Fatal(saved, err)
	}
	if s, _ := e.m.LoadSetup(ctx); s.Collections["basics"] != collections.Light {
		t.Fatal("Commit did not save library.toml")
	}
	if len(steps) != 1 || steps[0].Key.String() != "wiki_en_all:mini" {
		t.Errorf("committed steps = %+v", steps)
	}

	// Removals only: basics at deep still wants wikipedia_en_100, so
	// un-hand-picking it deletes nothing, and the downloads deep would bring
	// are not part of a removals-only plan.
	ch = Change{Edit: func(d *setup.Doc) error {
		d.DeleteItem("wikipedia_en_100")
		d.SetCollection("basics", collections.Deep)
		return nil
	}, RemovalsOnly: true}
	p, err = e.m.Preview(ctx, ch)
	if err != nil {
		t.Fatal(err)
	}
	checkSteps(t, p.Plan)

	// A step that changed after the preview is dropped by Commit.
	e.edit(func(d *setup.Doc) { d.SetCollection("basics", collections.Light) })
	ch = Change{Edit: func(d *setup.Doc) error { d.SetItem("devdocs_en_go", setup.On); return nil }}
	p, _ = e.m.Preview(ctx, ch)
	checkSteps(t, p.Plan, "add devdocs_en_go")
	e.fk.Bump("devdocs_en_go")
	e.sync()
	if steps, _, err := e.m.commit(ctx, ch, p); err != nil || len(steps) != 0 {
		t.Errorf("steps after a new release = %+v, %v", steps, err)
	}
}

func TestNoLibraryTomlKeepsWhatIsOnDisk(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.add("devdocs_en_go")
	e.add("wiki_en_all:nopic")
	// The database knows the files, but library.toml is gone (lost, or never
	// written): that must read as "keep these", not "want nothing".
	if err := os.Remove(e.m.SetupPath); err != nil {
		t.Fatal(err)
	}
	if p := e.plan(false); len(p.Steps) != 0 {
		t.Fatalf("apply without library.toml would do %q", steps(p))
	}
	if s := e.state("devdocs_en_go"); s != UpToDate {
		t.Errorf("state = %v", s)
	}
	// The first edit writes them into library.toml.
	e.edit(func(d *setup.Doc) { d.SetItem("wikipedia_en_100", setup.On) })
	s, err := e.m.LoadSetup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"devdocs_en_go": setup.On, "wiki_en_all": "nopic", "wikipedia_en_100": setup.On}
	if len(s.Items) != len(want) {
		t.Fatalf("items = %v", s.Items)
	}
	for k, v := range want {
		if s.Items[k].String() != v {
			t.Errorf("items[%s] = %q, want %q", k, s.Items[k], v)
		}
	}
}

func TestChoice(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, tc := range []struct {
		entry catalog.Entry
		want  string
	}{
		{catalog.Entry{Name: "wiki_en_all", Flavour: "maxi"}, "maxi"},
		{catalog.Entry{Name: "devdocs_en_go"}, setup.On},
		{catalog.Entry{Name: "not_in_catalog"}, setup.On},
		{catalog.Entry{Name: "md_en_all"}, setup.Default},
	} {
		if got, err := e.m.Choice(ctx, tc.entry.Key()); err != nil || got != tc.want {
			t.Errorf("Choice(%v) = %q, %v; want %q", tc.entry.Key(), got, err, tc.want)
		}
	}
}

func TestDefaultChoicePicksTheFlavourlessVariant(t *testing.T) {
	e := newEnv(t)
	e.edit(func(d *setup.Doc) { d.SetItem("md_en_all", setup.Default) })
	checkSteps(t, e.plan(false), "add md_en_all")
	e.edit(func(d *setup.Doc) { d.SetItem("md_en_all", setup.On) }) // on: the standard pick, the smaller maxi
	checkSteps(t, e.plan(false), "add md_en_all:maxi")
}

func TestNoLibraryTomlKeepsPartials(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// A partial download and no library.toml: it is kept for resuming, not
	// pruned or planned away.
	if _, _, err := e.m.prepareDownload(ctx, e.entry("devdocs_en_go"), nil); err != nil {
		t.Fatal(err)
	}
	if plan, _ := e.m.Prune(ctx, true); len(plan) != 0 {
		t.Errorf("prune would delete %+v", plan)
	}
	checkSteps(t, e.plan(false), "add devdocs_en_go")
}
