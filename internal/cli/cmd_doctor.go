package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/collections"
	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/kiwix"
	"github.com/kazie/zimon/internal/library"
	"github.com/kazie/zimon/internal/lock"
	"github.com/kazie/zimon/internal/remote"
	"github.com/kazie/zimon/internal/setup"
	"github.com/kazie/zimon/internal/store"
	"github.com/kazie/zimon/internal/task"
	"github.com/kazie/zimon/internal/ui"
)

func newDoctorCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check that zimon and its library are in good shape",
		Long: `Check zimon's files and folders, the database, the library folder, the
catalog, kiwix-serve, the state lock and the remotes, and say in plain words
what is wrong and how to fix it.

A problem (✗) makes doctor exit with status 1; something worth a look (!)
does not. doctor changes nothing, and works even when config.toml or
library.toml has a mistake.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := &doctor{a: a, ctx: cmd.Context()}
			d.run()
			switch {
			case d.problems > 0:
				a.out.Fail("%d problem%s found", d.problems, ui.Plural(d.problems))
				return errSilent
			case d.notes > 0:
				a.out.Status("Nothing is broken; %d thing%s worth a look.", d.notes, ui.Plural(d.notes))
			default:
				a.out.Success("All is well.")
			}
			return nil
		},
	}
}

// doctor checks one thing after another, saying how each went.
type doctor struct {
	a               *app
	ctx             context.Context
	problems, notes int
}

func (d *doctor) ok(what, format string, args ...any) {
	d.a.out.Success("%s: %s", ui.Bold.Render(what), fmt.Sprintf(format, args...))
}

// note is something worth a look: hint says what to do, if anything.
func (d *doctor) note(what, detail, hint string) {
	d.notes++
	d.say(d.a.out.Warn, what, detail, hint)
}

// problem is something wrong: hint says how to fix it.
func (d *doctor) problem(what, detail, hint string) {
	d.problems++
	d.say(d.a.out.Fail, what, detail, hint)
}

func (d *doctor) say(show func(string, ...any), what, detail, hint string) {
	show("%s: %s", ui.Bold.Render(what), detail)
	if hint != "" {
		d.a.out.Hint("  %s", hint)
	}
}

func (d *doctor) run() {
	a := d.a
	defer d.remotes() // they need no config of their own
	switch {
	case a.cfgErr != nil:
		d.problem("config.toml", a.cfgErr.Error(), "Fix it (zimon config edit), then run zimon doctor again for the rest.")
		return
	case !a.cfgFound:
		d.ok("config.toml", "none yet, so the defaults (zimon init writes one)")
	default:
		d.ok("config.toml", "%s", ui.Tilde(a.cfgPath))
	}
	s := d.libraryToml()
	d.folder("library folder", a.cfg.LibraryDir)
	d.folder("state folder", a.cfg.StateDir)
	if m := d.database(); m != nil {
		d.catalog(m.Store)
		if s != nil {
			d.wanted(m, s)
		}
		d.files(m)
	}
	d.kiwixServe()
	d.locks()
}

// libraryToml parses library.toml, returning it (nil when it has a mistake).
func (d *doctor) libraryToml() *setup.Setup {
	path := d.a.setupPath()
	doc, err := setup.Open(path)
	if err != nil {
		d.problem("library.toml", err.Error(), "")
		return nil
	}
	s, err := doc.Setup()
	switch werr := doc.Writable(); {
	case err != nil:
		d.problem("library.toml", err.Error(), "Fix it by hand; zimon refuses to change the library until then.")
		return nil
	case !doc.Exists():
		d.ok("library.toml", "none yet (zimon add writes it)")
	case doc.Editable() != nil:
		d.note("library.toml", doc.Editable().Error(), "zimon reads it, but add, remove and level can't change it.")
	case errors.Is(werr, setup.ErrReadOnly):
		d.ok("library.toml", "%s, read-only: kept elsewhere (as by NixOS), so add, remove and level leave it alone", ui.Tilde(path))
	case werr != nil:
		d.problem("library.toml", werr.Error(), "")
	default:
		d.ok("library.toml", "%s", ui.Tilde(path))
	}
	return s
}

// folder checks that dir can be written, or made.
func (d *doctor) folder(what, dir string) {
	at := config.NearestDir(dir)
	f, err := os.CreateTemp(at, ".zimon-doctor-*")
	if err != nil {
		d.problem(what, fmt.Sprintf("%s can't be written: %v", ui.Tilde(at), err), "Fix its owner or permissions, or point the config elsewhere.")
		return
	}
	f.Close()
	_ = os.Remove(f.Name())
	if at != dir {
		d.ok(what, "%s, not made yet (zimon makes it when it needs it)", ui.Tilde(dir))
		return
	}
	d.ok(what, "%s", ui.Tilde(dir))
}

// database opens the database and checks it, returning the library (nil
// when there is no database yet, or it can't be used).
func (d *doctor) database() *library.Manager {
	a := d.a
	if _, err := os.Stat(a.cfg.DBPath()); errors.Is(err, os.ErrNotExist) {
		d.ok("database", "none yet (zimon makes it when it needs it)")
		return nil
	}
	st, err := a.store()
	if err != nil {
		d.problem("database", err.Error(), "")
		return nil
	}
	if err := st.Check(d.ctx); err != nil {
		d.problem("database", "damaged: "+err.Error(),
			"Delete "+ui.Tilde(a.cfg.DBPath())+" and run zimon apply: the ZIMs in the library folder are checked and kept.")
		return nil
	}
	d.ok("database", "%s", ui.Tilde(a.cfg.DBPath()))
	cols := a.userCollectionsPath()
	if _, err := os.Stat(cols); err == nil {
		if _, err := collections.Load(cols); err != nil {
			d.problem("collections.toml", err.Error(), "Fix it by hand: zimon needs it to know your collections.")
			return nil
		}
		d.ok("collections.toml", "%s", ui.Tilde(cols))
	}
	m, err := a.manager()
	if err != nil {
		d.problem("library", err.Error(), "")
		return nil
	}
	return m
}

const refreshHint = "zimon refresh fetches it (commands do too, when they need it)."

func (d *doctor) catalog(st *store.Store) {
	synced, _, count, err := st.CatalogState(d.ctx)
	switch age := time.Since(synced); {
	case err != nil:
		d.problem("catalog", err.Error(), "")
	case count == 0:
		d.note("catalog", "not fetched yet", refreshHint)
	case !library.CatalogDue(synced, count, d.a.cfg.CatalogMaxAge.Duration):
		d.ok("catalog", "%s entries, fetched %s ago", ui.Num(int64(count)), ui.Ago(age))
	case synced.IsZero(): // a source was dropped: every catalog is fetched in full next
		d.note("catalog", fmt.Sprintf("%s entries, due for a refresh", ui.Num(int64(count))), refreshHint)
	default:
		d.note("catalog", fmt.Sprintf("%s entries, fetched %s ago", ui.Num(int64(count)), ui.Ago(age)), refreshHint)
	}
}

// wanted checks what library.toml asks for: its problems, the budget, and
// the free space for what zimon apply would download.
func (d *doctor) wanted(m *library.Manager, s *setup.Setup) {
	w, err := m.Wanted(d.ctx, s)
	if err != nil {
		d.problem("library.toml", err.Error(), "")
		return
	}
	for _, pr := range setup.Summarize(w.Problems) {
		d.note("library.toml", pr, "")
	}
	if total := w.Total(); s.Budget > 0 && total > s.Budget {
		d.note("budget", fmt.Sprintf("library.toml asks for about %s, over its %s budget", ui.Size(total), ui.Size(s.Budget)),
			"zimon budget shows what takes the room; zimon suggest picks levels that fit.")
	}
	p, err := m.Plan(d.ctx, library.PlanOptions{Setup: s, Wanted: w})
	if err != nil {
		d.problem("library", err.Error(), "")
		return
	}
	free, known := m.FreeSpace()
	switch need := p.Download(); {
	case !known:
		d.ok("space", "%s on disk; the free space is not known here", config.FormatBytes(p.Current))
	case need > free:
		d.note("space", fmt.Sprintf("the rest of the library needs about %s, but %s is free", ui.Size(need), ui.Size(free)),
			"Free some space, or ask for less (zimon budget, zimon level).")
	default:
		d.ok("space", "%s on disk, %s free", config.FormatBytes(p.Current), ui.Size(free))
	}
}

// files checks the library folder against the database: files gone,
// files zimon doesn't know, library.xml, and what prune would delete.
func (d *doctor) files(m *library.Manager) {
	all, err := m.Store.Files(d.ctx)
	if err != nil {
		d.problem("library", err.Error(), "")
		return
	}
	known := map[string]bool{}
	var gone []string
	for _, f := range all {
		known[f.Path] = true
		if _, err := os.Stat(filepath.Join(m.Cfg.LibraryDir, f.Path)); f.State == store.StateActive && err != nil {
			gone = append(gone, f.Key().String())
		}
	}
	if len(gone) > 0 {
		d.problem("library", fmt.Sprintf("%s missing from the library folder: %s", ui.Count(len(gone), "file is", "files are"), strings.Join(gone, ", ")),
			"zimon remove NAME --keep-file forgets one; zimon add NAME then gets it again.")
	} else {
		d.ok("library", "%s on disk, as the database says", ui.Count(len(all), "file", "files"))
	}
	entries, _ := os.ReadDir(m.Cfg.LibraryDir)
	var strangers []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".zim") && !known[e.Name()] {
			strangers = append(strangers, e.Name())
		}
	}
	if len(strangers) > 0 {
		d.note("library folder", fmt.Sprintf("%s zimon doesn't know: %s", ui.Count(len(strangers), "ZIM", "ZIMs"), strings.Join(strangers, ", ")),
			"zimon add ./FILE takes one into the library (zimon add NAME, for a release from the catalog); or delete it.")
	}
	switch matches, err := m.LibraryXMLMatches(d.ctx); {
	case errors.Is(err, os.ErrNotExist) && len(all) > 0:
		d.note("library.xml", "missing", "zimon serve writes it when it starts.")
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		d.problem("library.xml", err.Error(), "")
	case !matches:
		d.problem("library.xml", "lists other files than the database has",
			"Delete "+ui.Tilde(m.Cfg.LibraryXMLPath())+": zimon serve writes it again when it starts.")
	default:
		d.ok("library.xml", "lists the library's files")
	}
	if plan, err := task.PrunePlan(d.ctx, m); err == nil && len(plan) > 0 {
		d.note("leftovers", fmt.Sprintf("%s from old versions and stopped downloads", ui.Size(api.Total(plan))), "zimon prune shows and deletes them.")
	}
}

func (d *doctor) kiwixServe() {
	f := d.a.kiwixFetcher()
	bin, err := f.Locate(d.a.cfg.Serve.KiwixServePath, d.a.cfg.StateDir)
	switch {
	case errors.Is(err, kiwix.ErrNotFetched):
		pin, _ := f.Pin()
		d.note("kiwix-serve", "not installed",
			fmt.Sprintf("zimon serve fetches kiwix-serve %s from Kiwix (%s) when it starts, or install kiwix-tools.", kiwix.Version, ui.Size(pin.Size)))
		return
	case err != nil:
		d.note("kiwix-serve", err.Error(), "zimon serve needs it: "+kiwix.InstallHint+"; or set serve.kiwix_serve_path.")
		return
	}
	ctx, cancel := context.WithTimeout(d.ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	version, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if err != nil || version == "" {
		hint := ""
		if bin == kiwix.OwnPath(d.a.cfg.StateDir) {
			hint = "Delete " + ui.Tilde(filepath.Dir(bin)) + ": zimon serve fetches it again."
		}
		d.problem("kiwix-serve", fmt.Sprintf("%s does not run: %v", bin, err), hint)
		return
	}
	d.ok("kiwix-serve", "%s (%s)", ui.Tilde(bin), version)
}

// locks says who holds the state lock, and whether zimon serve runs here.
func (d *doctor) locks() {
	for _, l := range []struct{ what, path, held, free string }{
		{"state lock", d.a.cfg.LockPath(), "held by zimon (pid %d): a change is under way", "free"},
		{"zimon serve", d.a.cfg.ServeLockPath(), "runs here (pid %d)", "not running here"},
	} {
		switch pid, err := lock.Holder(l.path); {
		case err != nil:
			d.problem(l.what, err.Error(), "")
		case pid != 0:
			d.ok(l.what, l.held, pid)
		default:
			d.ok(l.what, "%s", l.free)
		}
	}
}

// remotes checks that each remote answers. It asks them all at once, so
// remotes that are down cost one wait, not one each.
func (d *doctor) remotes() {
	path, remotes, err := d.a.remotes()
	if err != nil {
		d.problem("remotes.toml", err.Error(), "")
		return
	}
	slices.SortFunc(remotes, func(x, y remote.Remote) int { return strings.Compare(x.Name, y.Name) })
	type answer struct {
		version string
		err     error
	}
	answers := make([]answer, len(remotes))
	ctx, cancel := context.WithTimeout(d.ctx, 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i, r := range remotes {
		wg.Go(func() {
			v, ssh, err := d.a.askVersion(ctx, r)
			switch {
			case err != nil && ssh != nil && ssh.reason() != "":
				err = fmt.Errorf("does not answer: %w (%s)", err, ssh.reason())
			case err != nil && ssh != nil:
				err = fmt.Errorf("does not answer: %w", err)
			}
			answers[i] = answer{v, err}
		})
	}
	wg.Wait()
	for i, r := range remotes {
		what := "remote " + r.Name
		if err := answers[i].err; err != nil {
			// The client's errors start with the remote's name, which what says.
			d.note(what, strings.TrimPrefix(err.Error(), r.Name+" "), "zimon remote test "+r.Name+" says more; "+ui.Tilde(path)+" has its address.")
		} else {
			d.ok(what, "answers: %s", answers[i].version)
		}
	}
}
