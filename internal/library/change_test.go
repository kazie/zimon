package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/collections"
	"github.com/kazie/zimon/internal/setup"
)

func (e *env) setupText() string {
	e.t.Helper()
	doc, err := setup.Open(e.m.SetupPath)
	if err != nil {
		e.t.Fatal(err)
	}
	return string(doc.Bytes())
}

func (e *env) setup() *setup.Setup {
	e.t.Helper()
	s, err := e.m.LoadSetup(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func TestChangeBuilders(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// add @basics --level light, plus devdocs_en_go and md_en_all (its
	// flavourless variant) by name.
	ch, err := e.m.AddChange(ctx, []catalog.Entry{e.entry("devdocs_en_go"), e.entry("md_en_all")},
		map[string]collections.Level{"basics": collections.Light})
	if err != nil {
		t.Fatal(err)
	}
	checkSteps(t, e.change(ch), "add devdocs_en_go", "add md_en_all", "add wiki_en_all:mini")
	s := e.setup()
	if s.Collections["basics"] != collections.Light || s.Items["devdocs_en_go"].String() != setup.On ||
		s.Items["md_en_all"].String() != setup.Default {
		t.Errorf("library.toml:\n%s", e.setupText())
	}

	checkSteps(t, e.change(LevelChange("basics", collections.Standard)), "switch wiki_en_all:nopic")
	checkSteps(t, e.change(ItemChange("wiki_en_all", "maxi")), "switch wiki_en_all:maxi")
	checkDisk(t, e, "devdocs_en_go", "md_en_all", "wiki_en_all:maxi")

	// Removing devdocs_en_go while basics still wants it sets it "off";
	// removing md_en_all drops its line.
	ch, err = e.m.RemoveChange(ctx, nil, []catalog.Key{{Name: "devdocs_en_go"}, {Name: "md_en_all"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	checkSteps(t, e.change(ch), "remove devdocs_en_go", "remove md_en_all")
	s = e.setup()
	if _, ok := s.Items["md_en_all"]; ok || s.Items["devdocs_en_go"].Kind != setup.ChoiceOff {
		t.Errorf("library.toml:\n%s", e.setupText())
	}
	checkDisk(t, e, "wiki_en_all:maxi")

	// Dropping the collection keeps the hand-picked wiki; --keep-file
	// forgets it but leaves the file.
	ch, err = e.m.RemoveChange(ctx, []string{"basics"}, []catalog.Key{{Name: "wiki_en_all", Flavour: "maxi"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	checkSteps(t, e.change(ch), "remove wiki_en_all:maxi")
	if s := e.setup(); len(s.Collections) != 0 || len(s.Items) != 1 {
		t.Errorf("library.toml:\n%s", e.setupText()) // only devdocs_en_go = "off" is left
	}
	checkDisk(t, e)
	book, _ := e.fk.Book("wiki_en_all:maxi")
	if _, err := os.Stat(filepath.Join(e.m.Cfg.LibraryDir, book.FileName())); err != nil {
		t.Errorf("--keep-file deleted the file: %v", err)
	}
}

func TestRemoveChangeOfUnwantedFlavourKeepsTheWant(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.add("wiki_en_all:mini")
	e.want("wiki_en_all:nopic") // mini is now the unwanted old flavour
	ch, err := e.m.RemoveChange(ctx, nil, []catalog.Key{{Name: "wiki_en_all", Flavour: "mini"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	// Removing never downloads: the wanted nopic waits for apply.
	p := e.change(ch)
	checkSteps(t, p, "remove wiki_en_all:mini")
	if !slices.Equal(deferred(p), []string{"apply add wiki_en_all:nopic"}) {
		t.Errorf("deferred = %q", deferred(p))
	}
	if s := e.setup(); s.Items["wiki_en_all"].String() != "nopic" {
		t.Errorf("library.toml:\n%s", e.setupText())
	}
}

func TestRemoveOneCopyOfAZIMGoneUpstream(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.add("wiki_en_all:mini")
	e.add("wiki_en_all:nopic") // mini stays on disk, unwanted
	for _, fl := range []string{"mini", "nopic", "maxi"} {
		e.fk.Remove("wiki_en_all:" + fl)
	}
	e.sync() // gone upstream: both copies are kept, and wanted
	ch, err := e.m.RemoveChange(ctx, nil, []catalog.Key{{Name: "wiki_en_all", Flavour: "mini"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	p := e.change(ch)
	checkSteps(t, p, "remove wiki_en_all:mini")
	if !slices.Equal(p.Gone, []string{"wiki_en_all"}) {
		t.Errorf("gone = %v: nopic is kept", p.Gone)
	}
	checkDisk(t, e, "wiki_en_all:nopic") // it can't be downloaded again: it must stay
	if s := e.setup(); s.Items["wiki_en_all"].String() != "nopic" {
		t.Errorf("after removing one copy, library.toml:\n%s", e.setupText())
	}

	// Removing the last copy forgets the name too.
	ch, err = e.m.RemoveChange(ctx, nil, []catalog.Key{{Name: "wiki_en_all", Flavour: "nopic"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	p = e.change(ch)
	checkSteps(t, p, "remove wiki_en_all:nopic")
	if len(p.Gone) != 0 {
		t.Errorf("gone = %v: no copy is kept", p.Gone)
	}
	if s := e.setup(); len(s.Items) != 0 {
		t.Errorf("after removing the last copy, library.toml:\n%s", e.setupText())
	}
}

// Preview says whether a change would rewrite library.toml, so a command
// can say "nothing to do" instead of saving the same file.
func TestPreviewEdits(t *testing.T) {
	e := newEnv(t)
	e.edit(func(d *setup.Doc) { d.SetCollection("docs", collections.Light) })
	for _, c := range []struct {
		ch    Change
		edits bool
	}{
		{LevelChange("docs", collections.Light), false},
		{LevelChange("docs", collections.Deep), true},
		{ItemChange("devdocs_en_go", setup.Off), true},
		{Change{}, false}, // apply
	} {
		p, err := e.m.Preview(context.Background(), c.ch)
		if err != nil || p.Edits != c.edits {
			t.Errorf("%+v: edits %v, %v; want %v", c.ch, p.Edits, err, c.edits)
		}
	}

	// A file the editor can't change is refused up front, but still applies.
	if err := os.WriteFile(e.m.SetupPath, []byte("collections.docs = \"light\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Preview(context.Background(), LevelChange("docs", collections.Deep)); !errors.Is(err, setup.ErrNotFlat) {
		t.Errorf("edit of a dotted key: %v", err)
	}
	if _, err := e.m.Preview(context.Background(), Change{}); err != nil {
		t.Errorf("apply of a dotted key: %v", err)
	}
}

// A change that leaves library.toml as it is writes nothing, so it works on
// a read-only file (one a NixOS module generates, say).
func TestNoOpEditWritesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anyway")
	}
	e := newEnv(t)
	ctx := context.Background()
	e.add("wiki_en_all:mini")
	e.want("wiki_en_all:nopic") // mini is the old flavour
	if err := os.Chmod(e.m.SetupPath, 0o444); err != nil {
		t.Fatal(err)
	}
	ch, err := e.m.RemoveChange(ctx, nil, []catalog.Key{{Name: "wiki_en_all", Flavour: "mini"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.m.Preview(ctx, ch)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.m.Apply(ctx, ch, p, nil, nil)
	if err != nil || res.Saved || res.Failed > 0 {
		t.Fatalf("apply: %+v, %v", res, err)
	}
	checkDisk(t, e)

	// An edit that changes something is refused.
	_, err = e.m.Edit(ctx, func(d *setup.Doc) error { d.DeleteItem("wiki_en_all"); return nil })
	if !errors.Is(err, setup.ErrReadOnly) {
		t.Errorf("edit of a read-only file: %v", err)
	}
}
