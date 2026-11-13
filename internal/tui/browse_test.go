package tui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/backend"
	"github.com/kazie/zimon/internal/client"
	"github.com/kazie/zimon/internal/collections"
	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/fakekiwix"
	"github.com/kazie/zimon/internal/library"
	"github.com/kazie/zimon/internal/remote"
	"github.com/kazie/zimon/internal/server"
	"github.com/kazie/zimon/internal/store"
)

// env is a library in a temp dir, backed by a fake Kiwix with the demo books.
type env struct {
	t   *testing.T
	fk  *fakekiwix.Server
	man *library.Manager

	// The state lock: busy makes the next taker wait for who first; err makes it fail.
	busy  string
	err   error
	locks int // held right now
}

func newEnv(t *testing.T) *env {
	t.Helper()
	fk := fakekiwix.New(fakekiwix.DemoBooks())
	srv := httptest.NewServer(fk)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	cfg := config.Default()
	cfg.LibraryDir = filepath.Join(dir, "library")
	cfg.StateDir = filepath.Join(dir, "state")
	cfg.CatalogURL = srv.URL + "/catalog/v2"
	cfg.Languages = []string{"eng"}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cols, err := collections.Load(filepath.Join(dir, "collections.toml"))
	if err != nil {
		t.Fatal(err)
	}
	man := &library.Manager{
		Cfg: cfg, Store: st, HTTP: srv.Client(),
		Backoff:     func(int) time.Duration { return 0 },
		SetupPath:   filepath.Join(dir, "library.toml"),
		Collections: cols,
	}
	e := &env{t: t, fk: fk, man: man}
	e.sync()
	return e
}

// sync pulls the fake's catalog into the store, like `zimon refresh`.
func (e *env) sync() {
	e.t.Helper()
	if _, _, _, err := library.RefreshCatalog(context.Background(), e.man.Store, e.man.HTTP, []string{e.man.Cfg.CatalogURL}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) lock(_ context.Context, onWait func(string)) (func(), error) {
	if e.err != nil {
		return nil, e.err
	}
	if e.busy != "" {
		onWait(e.busy)
	}
	e.locks++
	return func() { e.locks-- }, nil
}

// browse opens the browser at 120x30.
func (e *env) browse() *driver {
	e.t.Helper()
	be := &backend.Local{
		Manager: func() (*library.Manager, error) { return e.man, nil },
		Lock:    e.lock,
		TryLock: func() (func(), bool) { return func() {}, true },
	}
	return e.browseWith(Deps{Backend: be})
}

// browseRemote opens the browser on e's library through zimon serve's API,
// as zimon --remote srv browse does.
func (e *env) browseRemote() *driver {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := server.New(e.man, "")
	token, hash := server.NewToken()
	if err := e.man.Store.AddToken(ctx, "test", hash); err != nil {
		e.t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		e.t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, ln, nil) }()
	e.t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			e.t.Error(err)
		}
	})
	c, err := client.New(remote.Remote{Name: "srv", URL: "http://" + ln.Addr().String(), Token: token}, http.DefaultClient)
	if err != nil {
		e.t.Fatal(err)
	}
	return e.browseWith(Deps{Backend: backend.NewRemote(c), Remote: "srv"})
}

func (e *env) browseWith(deps Deps) *driver {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	e.t.Cleanup(cancel)
	// Room for every event of a test: the driver reads them only between commands.
	sh := &shared{events: make(chan tea.Msg, 1<<12)}
	m, err := newModel(ctx, deps, sh)
	if err != nil {
		e.t.Fatal(err)
	}
	st := m.input.Styles()
	st.Cursor.Blink = false // a blinking cursor is a timer that never ends
	m.input.SetStyles(st)
	d := &driver{t: e.t, m: m, sh: sh}
	d.update(tea.WindowSizeMsg{Width: 120, Height: 30})
	return d
}

func (e *env) exists(name string) bool {
	_, err := os.Stat(filepath.Join(e.man.Cfg.LibraryDir, name))
	return err == nil
}

func (e *env) libraryTOML() string {
	data, _ := os.ReadFile(e.man.SetupPath)
	return string(data)
}

// driver runs the model the way bubbletea does, but one command at a time:
// a command's work (a download, say) finishes before the next key, and the
// events it sent are delivered, in order, before its result.
type driver struct {
	t        *testing.T
	m        model
	sh       *shared
	cmds     []tea.Cmd // started, not run yet
	held     []tea.Cmd // started with start: they run at finish
	statuses []string  // every status line shown, in order
	quit     bool
}

func (d *driver) update(msg tea.Msg) {
	next, cmd := d.m.Update(msg)
	d.m = next.(model)
	if s := ansi.Strip(d.m.status); len(d.statuses) == 0 || d.statuses[len(d.statuses)-1] != s {
		d.statuses = append(d.statuses, s)
	}
	if _, ok := msg.(eventMsg); ok {
		return // the command is listen, and the driver reads the events itself
	}
	if cmd != nil {
		d.cmds = append(d.cmds, cmd)
	}
}

// settle runs the started commands (not the held ones), and what they
// start, to the end.
func (d *driver) settle() {
	for len(d.cmds) > 0 {
		cmd := d.cmds[0]
		d.cmds = d.cmds[1:]
		msg := cmd()
		d.drain()
		switch msg := msg.(type) {
		case nil:
		case tea.BatchMsg:
			d.cmds = append(d.cmds, msg...)
		case tea.QuitMsg:
			d.quit = true
		default:
			d.update(msg)
		}
	}
	d.drain()
}

func (d *driver) drain() {
	for {
		select {
		case msg := <-d.sh.events:
			d.update(msg)
		default:
			return
		}
	}
}

// press sends keys, letting what each one starts finish before the next.
func (d *driver) press(keys ...string) {
	d.t.Helper()
	for _, k := range keys {
		d.update(keyMsg(k))
		d.settle()
	}
}

// start sends a key and holds what it starts, so that a download, say, stays
// running while the test presses other keys.
func (d *driver) start(k string) {
	d.update(keyMsg(k))
	d.held, d.cmds = append(d.held, d.cmds...), nil
}

// finish runs the held commands, and everything else, to the end.
func (d *driver) finish() {
	d.cmds, d.held = append(d.held, d.cmds...), nil
	d.settle()
}

// typeText types s into the filter.
func (d *driver) typeText(s string) {
	for _, r := range s {
		d.press(string(r))
	}
}

func keyMsg(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	r := []rune(k)
	if len(r) != 1 {
		panic("keyMsg: unknown key " + k)
	}
	return tea.KeyPressMsg{Code: r[0], Text: k}
}

// screen is the view without styling.
func (d *driver) screen() string { return ansi.Strip(d.m.View().Content) }

func (d *driver) status() string { return ansi.Strip(d.m.status) }

// question is the confirmation asked in the footer, or "".
func (d *driver) question() string {
	if d.m.confirm == nil {
		return ""
	}
	return d.m.confirm.question
}

// showed checks that status was shown at some point.
func (d *driver) showed(status string) {
	d.t.Helper()
	if !slices.Contains(d.statuses, status) {
		d.t.Fatalf("statuses %q lack %q", d.statuses, status)
	}
}

func (d *driver) wantQuestion(want string) {
	d.t.Helper()
	if got := d.question(); got != want {
		d.t.Fatalf("question = %q, want %q", got, want)
	}
}

func (d *driver) sees(texts ...string) {
	d.t.Helper()
	s := d.screen()
	for _, text := range texts {
		if !strings.Contains(s, text) {
			d.t.Fatalf("screen lacks %q:\n%s", text, s)
		}
	}
}

func (d *driver) doesNotSee(text string) {
	d.t.Helper()
	if s := d.screen(); strings.Contains(s, text) {
		d.t.Fatalf("screen shows %q:\n%s", text, s)
	}
}

func (d *driver) wantStatus(want string) {
	d.t.Helper()
	if got := d.status(); got != want {
		d.t.Fatalf("status = %q, want %q", got, want)
	}
}

// selectKey moves the catalog cursor to key.
func (d *driver) selectKey(key string) {
	d.t.Helper()
	d.press("g")
	for range d.m.entries {
		if e := d.m.selected(); e != nil && e.Key == key {
			return
		}
		d.press("j")
	}
	d.t.Fatalf("%s is not in the list", key)
}

// selectCol moves the collections cursor to id.
func (d *driver) selectCol(id string) {
	d.t.Helper()
	d.press("g")
	for range d.m.cols {
		if d.m.cols[d.m.colCursor].ID == id {
			return
		}
		d.press("j")
	}
	d.t.Fatalf("@%s is not in the list", id)
}

// add downloads key from the catalog view.
func (d *driver) add(key string) {
	d.t.Helper()
	d.selectKey(key)
	d.press("a")
	d.wantStatus("✓ " + key + " downloaded and verified")
}

// On a remote, the same keys do the same things, as jobs there.
func TestBrowseRemote(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	d := e.browseRemote()
	d.sees("zimon browse", "allthetropes_en_all:maxi")
	d.add("devdocs_en_bash")
	if !e.exists("devdocs_en_bash_2026-10.zim") {
		t.Fatal("no file after add")
	}
	d.sees("up to date · devdocs_en_bash_2026-10.zim")
	d.press("d", "y")
	d.wantStatus("✓ Removed devdocs_en_bash (freed 2.00 MB)")
	if e.exists("devdocs_en_bash_2026-10.zim") {
		t.Fatal("file still there")
	}
	d.press("c")
	d.selectCol("linux")
	d.sees("Linux & sysadmin · Wikis, man pages", "zimon collections show linux")

	d.press("c") // back to the catalog
	d.selectKey("devdocs_en_git")
	d.start("a")
	d.press("q")
	d.wantQuestion("Downloads are running on srv; they carry on there (see zimon jobs). Quit?")
	d.finish()
}

func TestBrowseFilter(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	d := e.browse()
	d.sees("zimon browse", fmt.Sprintf("%d of %d ZIMs · eng", len(d.m.entries), d.m.total),
		"/ to filter", "Category ‹ all ›   Languages eng", "NAME", "allthetropes_en_all:maxi", "1.48 GB  2026-10-20",
		"All The Tropes · Everything about fiction", "press a to download allthetropes_en_all:maxi")
	d.doesNotSee("wikipedia_sv_all")

	// Typing filters as you go; every word must match.
	d.press("/")
	d.typeText("stack")
	d.sees("/ stack", "superuser.com_en_all", "askubuntu.com_en_all")
	d.doesNotSee("allthetropes")
	d.typeText(" ask")
	d.sees("askubuntu.com_en_all", "Ask Ubuntu · Q&A for Ubuntu users and developers", "press a to download askubuntu.com_en_all")
	d.doesNotSee("superuser.com_en_all")
	d.press("backspace", "backspace", "backspace", "backspace")
	d.sees("superuser.com_en_all")

	// enter ends typing but keeps the filter; esc then clears it.
	d.press("enter")
	if d.m.filtering {
		t.Fatal("still filtering after enter")
	}
	d.sees("/ stack", "superuser.com_en_all")
	d.press("esc")
	d.sees("/ to filter", "allthetropes")

	d.press("/")
	d.typeText("zzzz")
	d.sees("No matches. Try another filter, tab for categories, or l for all languages.")
	d.press("esc", "esc")
	d.sees("allthetropes")

	// tab steps through the categories, shift+tab back.
	d.press("tab")
	cat := d.m.categories[d.m.catIdx].Value
	if cat == "" {
		t.Fatal("tab stayed on all categories")
	}
	d.sees("‹ " + cat + " ›")
	for _, en := range d.m.entries {
		if en.Category != cat {
			t.Fatalf("%s (%s) shown in category %s", en.Key, en.Category, cat)
		}
	}
	d.press("shift+tab")
	d.sees("‹ all ›")

	// l switches between your languages and all of them.
	d.press("l")
	d.sees("Languages all languages")
	if !slices.ContainsFunc(d.m.entries, func(en api.Entry) bool { return en.Name == "wikipedia_sv_all" }) {
		t.Fatal("all languages does not list wikipedia_sv_all")
	}
	d.press("l")
	d.sees("Languages eng")
}

func TestBrowseMove(t *testing.T) {
	t.Parallel()
	probe := newEnv(t).browse()
	n, h := len(probe.m.entries), probe.m.listHeight()
	for _, c := range []struct {
		name           string
		keys           []string
		cursor, offset int // the selected row, and the first row shown
	}{
		{"down", []string{"j", "down"}, 2, 0},
		{"not above the top", []string{"j", "k", "up", "up"}, 0, 0},
		{"page down", []string{"pgdown"}, h, 1},
		{"page up", []string{"pgdown", "pgup"}, 0, 0},
		{"end", []string{"G"}, n - 1, n - h},
		{"not below the end", []string{"G", "j", "down"}, n - 1, n - h},
		{"home", []string{"G", "g"}, 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			d := newEnv(t).browse()
			d.press(c.keys...)
			if d.m.cursor != c.cursor || d.m.offset != c.offset {
				t.Errorf("cursor, offset = %d, %d; want %d, %d", d.m.cursor, d.m.offset, c.cursor, c.offset)
			}
			e := d.m.entries[d.m.cursor] // the selected row is shown, with its details below
			d.sees(e.Key, e.Title)
		})
	}
}

func TestBrowseAdd(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	d := e.browse()
	d.add("devdocs_en_bash")
	d.showed("↓ Downloading devdocs_en_bash")
	if !e.exists("devdocs_en_bash_2026-10.zim") {
		t.Fatal("no file after add")
	}
	if !strings.Contains(e.libraryTOML(), `"devdocs_en_bash" = "on"`) {
		t.Fatalf("library.toml:\n%s", e.libraryTOML())
	}
	d.sees("✓ devdocs_en_bash", "up to date · devdocs_en_bash_2026-10.zim")
	if e.locks != 0 {
		t.Fatalf("%d locks still held", e.locks)
	}

	d.press("a")
	d.wantStatus("devdocs_en_bash is already up to date")

	// u updates to a newer release the same way.
	e.fk.Bump("devdocs_en_bash")
	e.sync()
	d = e.browse()
	d.selectKey("devdocs_en_bash")
	d.sees("update available → 2026-11-20 · devdocs_en_bash_2026-10.zim · press u to update")
	d.press("u")
	d.wantStatus("✓ devdocs_en_bash downloaded and verified")
	d.showed("✓ Removed old version devdocs_en_bash_2026-10.zim")
	if !e.exists("devdocs_en_bash_2026-11.zim") || e.exists("devdocs_en_bash_2026-10.zim") {
		t.Fatal("the update did not replace the file")
	}
}

func TestBrowseAddAsksBeforeDeleting(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	d := e.browse()
	d.press("l")
	d.add("wikipedia_ja_top:nopic")

	// Another flavour replaces this one, so zimon asks first.
	d.selectKey("wikipedia_ja_top:maxi")
	d.press("a")
	want := "Add wikipedia_ja_top:maxi? wikipedia_ja_top:nopic is deleted once it is verified (2.00 MB)."
	d.wantQuestion(want)
	d.sees(want + " [y/N]")
	d.press("n")
	d.wantStatus("Cancelled.")
	if e.exists("wikipedia_ja_top_maxi_2026-10.zim") {
		t.Fatal("downloaded after no")
	}

	d.press("a", "y")
	d.wantStatus("✓ wikipedia_ja_top:maxi downloaded and verified")
	if !e.exists("wikipedia_ja_top_maxi_2026-10.zim") || e.exists("wikipedia_ja_top_nopic_2026-10.zim") {
		t.Fatal("the flavour did not switch")
	}
}

func TestBrowseRemove(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	d := e.browse()
	d.add("devdocs_en_bash")
	d.press("d")
	d.wantQuestion("Delete devdocs_en_bash (2.00 MB)?")
	d.start("y")
	d.press("d") // while it is being removed
	d.wantStatus("Removing devdocs_en_bash…")
	d.finish()
	d.wantStatus("✓ Removed devdocs_en_bash (freed 2.00 MB)")
	if e.exists("devdocs_en_bash_2026-10.zim") {
		t.Fatal("file still there")
	}
	if strings.Contains(e.libraryTOML(), "devdocs_en_bash") {
		t.Fatalf("library.toml still wants it:\n%s", e.libraryTOML())
	}
	d.sees("press a to download devdocs_en_bash")

	d.press("d")
	d.wantStatus("devdocs_en_bash is not in your library")
}

func TestBrowseCollections(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	d := e.browse()
	d.press("c")
	d.sees(fmt.Sprintf("collections · 0 of %d in your library", len(d.m.cols)),
		"1 light: the core, smallest", "YOURS", "LIGHT", "STANDARD", "DEEP",
		"@anime-manga", "Not in your library")
	d.press("tab") // no categories here
	d.sees("collections · 0 of")
	d.selectCol("linux")
	d.sees("Linux & sysadmin · Wikis, man pages", "Not in your library · ", "zimon collections show linux")

	// 1, 2 and 3 set the level after showing what it takes.
	d.selectCol("anime-manga")
	d.sees("Anime & manga · Anime and manga Q&A", "zimon collections show anime-manga")
	d.press("1")
	d.wantQuestion("@anime-manga → light: download about 1.50 GB?")
	d.press("y")
	d.wantStatus("✓ @anime-manga → light: 3 changes done")
	d.showed("⇄ @anime-manga → light")
	d.sees(fmt.Sprintf("collections · 1 of %d", len(d.m.cols)), "In your library at light")
	if !strings.Contains(e.libraryTOML(), `anime-manga = "light"`) || !e.exists("evageeks_en_all_maxi_2026-10.zim") {
		t.Fatalf("light not applied; library.toml:\n%s", e.libraryTOML())
	}
	d.press("1")
	d.wantStatus("@anime-manga is already light")

	d.press("2", "y")
	d.wantStatus("✓ @anime-manga → standard: 4 changes done")
	if !e.exists("pokemonwiki_en_all_maxi_2026-10.zim") {
		t.Fatal("standard not applied")
	}
	d.press("3", "y")
	d.wantStatus("✓ @anime-manga → deep: 1 change done")
	if !e.exists("wikipedia_ja_all_maxi_2026-10.zim") {
		t.Fatal("deep not applied")
	}
	d.press("1")
	d.wantQuestion("@anime-manga → light: delete 10.0 MB?")
	d.press("n")
	d.wantStatus("Cancelled.")
	d.press("1", "y")
	d.wantStatus("✓ @anime-manga → light: 5 changes done")
	if e.exists("pokemonwiki_en_all_maxi_2026-10.zim") || e.exists("wikipedia_ja_all_maxi_2026-10.zim") {
		t.Fatal("light did not delete the deeper items")
	}

	// x drops the collection.
	d.press("x")
	d.wantQuestion("remove @anime-manga: delete 7.00 MB?")
	d.press("y")
	d.wantStatus("✓ remove @anime-manga: 3 changes done")
	if strings.Contains(e.libraryTOML(), "anime-manga =") || e.exists("evageeks_en_all_maxi_2026-10.zim") {
		t.Fatalf("collection not removed; library.toml:\n%s", e.libraryTOML())
	}
	d.press("x")
	d.wantStatus("@anime-manga is not in your library")

	// Moving around, and back to the catalog.
	d.press("G")
	if d.m.colCursor != len(d.m.cols)-1 {
		t.Fatalf("G: colCursor = %d", d.m.colCursor)
	}
	d.press("up", "k", "down")
	if d.m.colCursor != len(d.m.cols)-2 {
		t.Fatalf("colCursor = %d", d.m.colCursor)
	}
	d.press("esc")
	d.sees("Category ‹ all ›")
	d.press("c", "c")
	d.sees("Category ‹ all ›")
}

// Downloads queue up while one runs; collection changes and removals of the
// running download wait.
func TestBrowseQueue(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	d := e.browse()
	d.selectKey("devdocs_en_bash")
	d.start("a") // running, not finished
	d.wantStatus("↓ Downloading devdocs_en_bash")
	d.sees("↓ devdocs_en_bash")

	// The download's events show in the detail line.
	k := "devdocs_en_bash"
	d.update(eventMsg{Type: api.EvProgress, Phase: api.PhaseStart, Key: k, Total: 2000000})
	d.update(eventMsg{Type: api.EvProgress, Key: k, Done: 1000000, Total: 2000000})
	d.sees(" 50%  1.00 MB / 2.00 MB")
	d.update(eventMsg{Type: api.EvProgress, Phase: api.PhaseVerify, Key: k})
	d.sees("verifying…")
	d.update(eventMsg{Type: api.EvNote, Key: k, Line: "mirror1 failed, trying the next mirror"})
	d.wantStatus("! mirror1 failed, trying the next mirror")

	d.press("a")
	d.selectKey("devdocs_en_git")
	d.start("a")
	d.wantStatus("Queued devdocs_en_git")
	d.sees("↓ queued")

	// d drops a queued download, and a queues it again.
	d.press("d")
	d.wantStatus("Dropped devdocs_en_git from the queue")
	d.doesNotSee("↓ queued")
	d.start("a")
	d.wantStatus("Queued devdocs_en_git")

	d.selectKey("devdocs_en_bash")
	d.press("d")
	d.wantStatus("! " + waitForDownload)
	d.press("c")
	d.selectCol("anime-manga")
	d.press("1")
	d.wantStatus("! " + waitForDownload)
	d.sees(" 50%  1.00 MB / 2.00 MB")
	d.press("c")

	d.finish()
	d.wantStatus("✓ devdocs_en_git downloaded and verified")
	if !e.exists("devdocs_en_bash_2026-10.zim") || !e.exists("devdocs_en_git_2026-10.zim") {
		t.Fatal("not both downloaded")
	}
}

func TestBrowseDropQueuedUpdate(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	d := e.browse()
	d.add("devdocs_en_bash")
	e.fk.Bump("devdocs_en_bash")
	e.sync()
	d = e.browse()
	d.selectKey("devdocs_en_git")
	d.start("a")
	d.selectKey("devdocs_en_bash")
	d.start("u")
	d.wantStatus("Queued devdocs_en_bash")
	d.press("d")
	d.wantStatus("Dropped devdocs_en_bash from the queue; d again deletes it")
	d.wantQuestion("")
	d.finish()
	if !e.exists("devdocs_en_bash_2026-10.zim") || e.exists("devdocs_en_bash_2026-11.zim") {
		t.Fatal("the update ran, or the old file is gone")
	}
}

func TestBrowseQuit(t *testing.T) {
	t.Parallel()
	download := func(d *driver) { d.selectKey("devdocs_en_bash"); d.start("a") }
	removal := func(d *driver) { d.add("devdocs_en_bash"); d.press("d"); d.start("y") }
	for _, c := range []struct {
		name     string
		before   func(*driver) // what is going on when the user quits
		keys     []string
		quit     bool
		question string // asked instead of quitting
		status   string
	}{
		{name: "q", keys: []string{"q"}, quit: true},
		{name: "ctrl+c", keys: []string{"ctrl+c"}, quit: true},
		{name: "download running", before: download, keys: []string{"q"},
			question: "Downloads are running; they will resume next time. Quit?"},
		{name: "download running, yes", before: download, keys: []string{"q", "y"}, quit: true},
		{name: "download running, no", before: download, keys: []string{"q", "n"}, status: "Cancelled."},
		{name: "collection change running", keys: []string{"q"},
			before:   func(d *driver) { d.press("c"); d.selectCol("anime-manga"); d.press("1"); d.start("y") },
			question: "@anime-manga → light is running; zimon apply finishes it later. Quit?"},
		{name: "removal running", before: removal, keys: []string{"q"}, status: "Finishing a removal first…"},
		{name: "removal finished", keys: []string{"q"}, quit: true,
			before: func(d *driver) { removal(d); d.press("q"); d.finish() }},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			d := newEnv(t).browse()
			if c.before != nil {
				c.before(d)
			}
			d.press(c.keys...)
			if d.quit != c.quit {
				t.Errorf("quit = %v, want %v", d.quit, c.quit)
			}
			d.wantQuestion(c.question)
			if c.status != "" {
				d.wantStatus(c.status)
			}
		})
	}
}

func TestBrowseFailures(t *testing.T) {
	t.Parallel()
	breakMirrors := func(e *env) { e.fk.BreakAll() }
	addBash := func(_ *env, d *driver) { d.selectKey("devdocs_en_bash"); d.press("a") }
	for _, c := range []struct {
		name   string
		setup  func(*env)
		do     func(*env, *driver)
		status string // a regexp
		shown  string // a status shown on the way
		screen string // on the screen afterwards
	}{
		{name: "download fails", setup: breakMirrors, do: addBash,
			status: `^✗ devdocs_en_bash: download failed after trying every mirror: `,
			screen: "downloading 0% (stopped: download failed after trying every mirror"},
		{name: "collection change fails", setup: breakMirrors,
			do:     func(_ *env, d *driver) { d.press("c"); d.selectCol("anime-manga"); d.press("1", "y") },
			status: `^✗ @anime-manga → light: 3 of 3 changes failed \(.*\); zimon apply retries$`},
		{name: "another zimon holds the lock", setup: func(e *env) { e.busy = "another zimon (pid 42)" }, do: addBash,
			status: `^✓ devdocs_en_bash downloaded and verified$`, shown: "Waiting for another zimon (pid 42) to finish…"},
		{name: "no lock for a download", setup: func(e *env) { e.err = errors.New("no lock for you") }, do: addBash,
			status: `^✗ devdocs_en_bash: no lock for you$`},
		{name: "no lock for a removal",
			do: func(e *env, d *driver) {
				d.add("devdocs_en_bash")
				e.err = errors.New("no lock for you")
				d.press("d", "y")
			},
			status: `^✗ devdocs_en_bash: no lock for you$`, screen: "up to date · devdocs_en_bash_2026-10.zim"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			if c.setup != nil {
				c.setup(e)
			}
			d := e.browse()
			c.do(e, d)
			if !regexp.MustCompile(c.status).MatchString(d.status()) {
				t.Errorf("status = %q, want %s", d.status(), c.status)
			}
			if c.shown != "" {
				d.showed(c.shown)
			}
			if c.screen != "" {
				d.sees(c.screen)
			}
		})
	}
}

func TestFirstErr(t *testing.T) {
	k, other := "wikivoyage_en_all:nopic", "wikivoyage_en_all:maxi"
	for _, c := range []struct {
		name  string
		steps []api.StepResult
		want  string
	}{
		{"nothing ran", nil, ""},
		{"all fine", []api.StepResult{{Key: k}}, ""},
		{"the key asked for", []api.StepResult{{Key: k, Error: "boom"}}, "boom"},
		{"another key", []api.StepResult{{Key: k}, {Key: other, Error: "boom"}}, "wikivoyage_en_all:maxi: boom"},
	} {
		got := ""
		if err := firstErr(c.steps, k); err != nil {
			got = err.Error()
		}
		if got != c.want {
			t.Errorf("%s: firstErr = %q, want %q", c.name, got, c.want)
		}
	}
}

// Every line fits the window, with the short help or all keys, and no column
// is cut off.
func TestBrowseSizes(t *testing.T) {
	t.Parallel()
	d := newEnv(t).browse()
	date := regexp.MustCompile(`\d{4}-\d{2}-\d{2}$`)
	for _, size := range [][3]int{{80, 24, 0}, {120, 30, 0}, {200, 50, 0}, {80, 24, 1}, {120, 30, 1}, {200, 50, 1}} {
		w, h := size[0], size[1]
		d.m.help.ShowAll = size[2] == 1
		d.update(tea.WindowSizeMsg{Width: w, Height: h})
		for _, view := range []struct {
			name, last string // last is the last column's header
		}{
			{"catalog", "DATE"},
			{"collections", "DEEP"},
		} {
			d.m.showCols = view.name == "collections"
			if d.m.showCols {
				d.update(d.m.loadCollections()())
			}
			lines := strings.Split(d.screen(), "\n")
			if len(lines) != h {
				t.Errorf("%dx%d %s: %d lines", w, h, view.name, len(lines))
			}
			header := -1
			for i, l := range lines {
				if lw := ansi.StringWidth(l); lw > w {
					t.Errorf("%dx%d %s: line %d is %d wide: %q", w, h, view.name, i, lw, l)
				}
				if header < 0 && strings.HasPrefix(strings.TrimSpace(l), "NAME") {
					header = i
				}
			}
			if header < 0 {
				t.Fatalf("%dx%d %s: no header:\n%s", w, h, view.name, strings.Join(lines, "\n"))
			}
			if got := strings.TrimRight(lines[header], " "); !strings.HasSuffix(got, view.last) {
				t.Errorf("%dx%d %s: header cut off: %q", w, h, view.name, got)
			}
			if !d.m.showCols && !date.MatchString(strings.TrimRight(lines[header+1], " ")) {
				t.Errorf("%dx%d: first row cut off: %q", w, h, lines[header+1])
			}
		}
	}
}

// ? shows every key of the view on screen, and hides them again; while
// typing a filter it is just a character.
func TestBrowseHelp(t *testing.T) {
	t.Parallel()
	d := newEnv(t).browse()
	d.update(tea.WindowSizeMsg{Width: 80, Height: 24}) // everything fits the smallest size
	short := "↑↓ move  / filter  l languages  a add  d delete  c collections  ? more  q quit"
	d.sees(short)
	d.doesNotSee("page down")

	d.press("?")
	d.sees("page down", "previous category", "clear the filter", "add or update", "delete or cancel", "fewer keys", "q       quit")
	d.doesNotSee(short)
	d.press("?")
	d.sees(short)

	d.press("/")
	d.press("?")
	if got := d.m.input.Value(); got != "?" {
		t.Fatalf("filter = %q, want %q", got, "?")
	}
	d.doesNotSee("page down")
	d.press("esc", "esc")

	d.press("c")
	d.sees("↑↓ move  1 light  2 standard  3 deep  x remove  c catalog  ? more  q quit")
	d.press("?")
	d.sees("3   deep", "x/d remove", "back to the catalog", "q     quit")
	d.press("c")
	d.sees("page down", "add or update") // all keys stay on in the catalog
}
