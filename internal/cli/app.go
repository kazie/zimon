package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/backend"
	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/collections"
	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/kiwix"
	"github.com/kazie/zimon/internal/library"
	"github.com/kazie/zimon/internal/remote"
	"github.com/kazie/zimon/internal/store"
	"github.com/kazie/zimon/internal/ui"
)

// app is the state shared by all commands.
type app struct {
	version  string
	commit   string // short, "" when unknown
	cfgPath  string
	verbose  bool
	noColor  bool
	cfg      *config.Config
	cfgFound bool
	cfgErr   error // why config.toml didn't load: only doctor runs on, with the defaults
	out      *ui.Output
	st       *store.Store
	hc       *http.Client
	hcOnce   sync.Once
	backoff  func(attempt int) time.Duration // wait before retrying a mirror; nil = the downloader's default
	kiwix    kiwix.Fetcher                   // where serve fetches kiwix-serve from when it isn't installed
	remote   *remote.Remote                  // an https:// remote the command runs on; nil: here
	be       backend.Backend                 // set up before a command runs
	mgr      *library.Manager                // once made; see manager
}

// needsSetup reports whether cmd should run the first-run wizard when there
// is no config yet. init, config, help, completion, man, remote and doctor
// (and their subcommands) work without one, as does bare `zimon`.
func needsSetup(cmd *cobra.Command) bool {
	if !cmd.HasParent() {
		return false
	}
	for c := cmd; c.HasParent(); c = c.Parent() {
		switch c.Name() {
		case "init", "config", "help", "completion", "man", "remote", "doctor", cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd:
			return false
		}
	}
	return true
}

func (a *app) setup(cmd *cobra.Command) error {
	a.out = ui.New(a.noColor || os.Getenv("NO_COLOR") != "")
	a.out.Interrupt(cmd.Context())
	if err := a.loadConfig(); err != nil {
		if cmd.Name() != "doctor" {
			return err
		}
		a.cfgErr, a.cfg = err, config.Default() // doctor says what is wrong
	}
	if n := cmd.Name(); n == cobra.ShellCompRequestCmd || n == cobra.ShellCompNoDescRequestCmd {
		return nil // completion has a backend of its own (complete.go)
	}
	if a.remote != nil {
		if err := a.checkRemote(cmd); err != nil {
			return err
		}
		be, err := a.remoteBackend()
		if err != nil {
			return err
		}
		a.be = be
		_, err = be.Info(cmd.Context()) // checks the token and the API version
		return err
	}
	a.be = a.local()
	if !a.cfgFound && a.out.InTTY && needsSetup(cmd) {
		a.out.Status("%s", ui.Bold.Render("Welcome to zimon! Let's set up a library first."))
		if err := a.runInit(false); err != nil {
			return err
		}
		a.out.Status("")
	}
	return nil
}

// loadConfig (re)reads the config file, defaulting the path.
func (a *app) loadConfig() error {
	if a.cfgPath == "" {
		a.cfgPath = config.Path()
	}
	cfg, found, err := config.Load(a.cfgPath)
	if err != nil {
		return err
	}
	a.cfg, a.cfgFound = cfg, found
	a.mgr = nil // made from the old config
	return nil
}

func (a *app) close() {
	if a.st != nil {
		a.st.Close()
		a.st = nil
	}
}

// store opens the database on first use.
func (a *app) store() (*store.Store, error) {
	if a.st == nil {
		st, err := store.Open(a.cfg.DBPath())
		if err != nil {
			return nil, err
		}
		a.st = st
	}
	return a.st, nil
}

func (a *app) httpClient() *http.Client {
	a.hcOnce.Do(func() {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.ResponseHeaderTimeout = 30 * time.Second
		a.hc = &http.Client{Transport: userAgent{tr, "zimon/" + a.version + " (+https://github.com/kazie/zimon)"}}
	})
	return a.hc
}

// manager is the library here, made on first use.
func (a *app) manager() (*library.Manager, error) {
	if a.mgr != nil {
		return a.mgr, nil
	}
	st, err := a.store()
	if err != nil {
		return nil, err
	}
	cols, err := collections.Load(a.userCollectionsPath())
	if err != nil {
		return nil, err
	}
	m := &library.Manager{Cfg: a.cfg, Store: st, HTTP: a.httpClient(), SetupPath: a.setupPath(), Collections: cols, Backoff: a.backoff}
	a.mgr = m
	return m, nil
}

// userCollectionsPath is collections.toml, next to library.toml.
func (a *app) userCollectionsPath() string {
	return filepath.Join(filepath.Dir(a.setupPath()), "collections.toml")
}

// setupPath is library.toml: library_file, or next to the config file.
func (a *app) setupPath() string {
	if a.cfg.LibraryFile != "" {
		return a.cfg.LibraryFile
	}
	return filepath.Join(filepath.Dir(a.cfgPath), "library.toml")
}

type userAgent struct {
	next http.RoundTripper
	ua   string
}

func (u userAgent) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("User-Agent") == "" {
		r = r.Clone(r.Context())
		r.Header.Set("User-Agent", u.ua)
	}
	return u.next.RoundTrip(r)
}

// lookup resolves "name" or "name:flavour". It returns every flavour of the
// name, and the one the argument picks: the named flavour, or the only one
// there is (nil when a choice is needed). Unknown names get suggestions.
func (a *app) lookup(ctx context.Context, arg string) (family []api.Entry, picked *api.Entry, err error) {
	k, named := catalog.SplitKey(arg)
	entries, similar, err := a.be.Entries(ctx, k.Name)
	if err != nil {
		return nil, nil, err
	}
	if len(entries) == 0 {
		msg := fmt.Sprintf("%q is not in the catalog", k.Name)
		if len(similar) > 0 {
			msg += "\n  did you mean: " + joinStyled(similar, ui.Bold)
		}
		return nil, nil, errors.New(msg)
	}
	if !named {
		if len(entries) == 1 {
			return entries, &entries[0], nil
		}
		return entries, nil, nil
	}
	for i := range entries {
		if entries[i].Flavour == k.Flavour {
			return entries, &entries[i], nil
		}
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Key)
	}
	return nil, nil, fmt.Errorf("%s has no flavour %q\n  available: %s", k.Name, k.Flavour, joinStyled(names, ui.Bold))
}
