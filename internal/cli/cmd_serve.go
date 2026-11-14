package cli

import (
	"cmp"
	"context"
	"errors"
	"net"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/kazie/zimon/internal/kiwix"
	"github.com/kazie/zimon/internal/server"
	"github.com/kazie/zimon/internal/shell"
	"github.com/kazie/zimon/internal/ui"
)

func newServeCmd(a *app) *cobra.Command {
	var port, adminPort int
	var address string
	var printOnly, plain bool
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve your library in a browser",
		Long: `Serve your library: a start page with every book, grouped by your
collections, and one search box across all of them. Books open in
kiwix-serve, which zimon runs behind the scenes and restarts if it stops.
Books added, updated or removed by zimon show up without a restart, and
book URLs leave out the date, so bookmarks survive updates.

By default only this computer can reach it (127.0.0.1). Use --address all, or
serve.address = "all" in the config, to share it on your network.

The admin page has its own port (--admin-port, by default the next one), so
scripts inside books can't reach it.

--plain runs kiwix-serve alone in zimon's place, without the start page.

kiwix-serve comes from kiwix-tools: the one on your PATH, or else Kiwix's
own build, which zimon serve fetches once into the state directory.`,
		Example: `  zimon serve
  zimon serve --address all    # share it on your network
  zimon serve --port 8888
  zimon serve --plain          # just kiwix-serve
  zimon serve --print          # show the kiwix-serve command --plain runs`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			m, err := a.manager()
			if err != nil {
				return err
			}
			if err := m.EnsureLibraryXML(ctx); err != nil {
				return err
			}
			files, err := m.Store.ActiveFiles(ctx)
			if err != nil {
				return err
			}
			if port == 0 {
				port = a.cfg.Serve.Port
			}
			if address == "" {
				address = a.cfg.Serve.Address
			}
			if adminPort == 0 {
				adminPort = cmp.Or(a.cfg.Serve.AdminPort, port+1)
			}
			opts := kiwix.Options{Library: a.cfg.LibraryXMLPath(), Address: address, Port: port}
			if printOnly {
				bin, err := a.kiwixFetcher().Locate(a.cfg.Serve.KiwixServePath, a.cfg.StateDir)
				if err != nil && !errors.Is(err, kiwix.ErrNotFetched) { // that one is where --plain fetches it
					bin = "kiwix-serve"
				}
				a.out.Println(shell.Quote(append([]string{bin}, kiwix.Args(opts)...)))
				return nil
			}
			bin, fetch, err := a.kiwixServe(ctx, plain)
			if err != nil {
				return err
			}
			if len(files) == 0 {
				a.out.Warn("Your library is empty; books show up as you add them.")
			}

			var ln, adminLn net.Listener
			if !plain {
				// Listen before saying so, so a port in use is reported plainly.
				if ln, err = net.Listen("tcp", listenAddr(address, port)); err != nil {
					return err
				}
				// Books matter more than the admin page: serve them anyway.
				if adminLn, err = net.Listen("tcp", listenAddr(address, adminPort)); err != nil {
					a.out.Warn("No admin page: %v (set another port with --admin-port)", err)
					adminLn = nil
				}
			}
			host := address
			if host == "" || host == "all" || host == "ipv4" || host == "ipv6" {
				host = "localhost"
			}
			url := "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/"
			adminURL := "http://" + net.JoinHostPort(host, strconv.Itoa(adminPort))
			a.out.Success("Serving %d ZIM%s at %s %s", len(files), ui.Plural(len(files)), ui.Bold.Render(url), ui.Dim.Render("(Ctrl-C to stop)"))
			local := loopback(address)
			if local {
				a.out.Hint("Only this computer can reach it; --address all shares it on your network.")
			}
			if plain {
				a.close() // release the database before exec
				return kiwix.Exec(bin, kiwix.Args(opts))
			}
			srv := server.New(m, bin)
			if fetch != nil {
				srv.Fetch = fetch
				a.out.Hint("Fetching kiwix-serve %s from Kiwix; books open once it is here.", kiwix.Version)
			}
			srv.Version, srv.Commit = a.version, a.commit
			srv.Logf = func(format string, args ...any) { a.out.Hint(format, args...) }
			switch {
			case adminLn == nil:
			case local && a.out.ErrTTY: // not into a service log
				// The link's code is a secret only this terminal sees, and works once.
				a.out.Hint("Admin page, signed in (this link works once): %s", adminURL+srv.LoginLink())
			default:
				a.out.Hint("Admin page: %s/admin (sign in with a token from: zimon token add NAME)", adminURL)
			}
			return srv.Run(ctx, ln, adminLn)
		},
	}
	cmd.Flags().IntVarP(&port, "port", "p", 0, "port to listen on (default from config, 8080)")
	cmd.Flags().IntVar(&adminPort, "admin-port", 0, "port of the admin page (default from config, or --port + 1)")
	cmd.Flags().StringVar(&address, "address", "", "address to listen on: an IP, or all for every interface (default from config, 127.0.0.1)")
	cmd.Flags().BoolVar(&plain, "plain", false, "run kiwix-serve alone, without zimon's start page")
	cmd.Flags().BoolVar(&printOnly, "print", false, "print the kiwix-serve command --plain runs, and exit")
	return cmd
}

// listenAddr turns serve.address (as kiwix-serve takes it: an IP, all, ipv4
// or ipv6) into an address for net.Listen.
func listenAddr(address string, port int) string {
	p := strconv.Itoa(port)
	switch address {
	case "", "all":
		return ":" + p
	case "ipv4":
		return "0.0.0.0:" + p
	case "ipv6":
		return "[::]:" + p
	}
	return net.JoinHostPort(address, p)
}

// kiwixFetcher is a.kiwix with zimon's HTTP client, rate limit and backoff.
func (a *app) kiwixFetcher() kiwix.Fetcher {
	f := a.kiwix
	f.Client, f.RateLimit, f.Backoff = a.httpClient(), int64(a.cfg.Download.RateLimit), a.backoff
	return f
}

// kiwixServe finds kiwix-serve for serve. When it isn't installed, --plain
// fetches it now, since it takes zimon's place; otherwise it returns fetch,
// for the server to fetch it with. It says so when kiwix-serve can't be had.
func (a *app) kiwixServe(ctx context.Context, plain bool) (bin string, fetch func(context.Context) (string, error), err error) {
	f, stateDir := a.kiwixFetcher(), a.cfg.StateDir
	bin, err = f.Locate(a.cfg.Serve.KiwixServePath, stateDir)
	hints := func() {
		a.out.Hint("  To fix it, %s", kiwix.InstallHint)
		a.out.Hint("  Or set serve.kiwix_serve_path in %s", ui.Tilde(a.cfgPath))
	}
	switch {
	case errors.Is(err, kiwix.ErrNotFetched) && plain:
		pin, _ := f.Pin()
		a.out.Status("Fetching kiwix-serve %s from Kiwix (%s)…", kiwix.Version, ui.Size(pin.Size))
		p := a.out.NewProgress("kiwix-serve", pin.Size, 0)
		bin, err = f.Fetch(ctx, stateDir, p.Set)
		p.Finish()
		switch {
		case ctx.Err() != nil:
			return "", nil, ctx.Err()
		case err != nil:
			a.out.Fail("kiwix-serve is not installed, and fetching it failed: %v", err)
			hints()
			return "", nil, errSilent
		}
		a.out.Success("kiwix-serve %s is in %s", kiwix.Version, ui.Tilde(filepath.Dir(bin)))
		return bin, nil, nil
	case errors.Is(err, kiwix.ErrNotFetched):
		return "", func(ctx context.Context) (string, error) { return f.Fetch(ctx, stateDir, nil) }, nil
	case errors.Is(err, kiwix.ErrUnsupported):
		if plain {
			a.out.Fail("%v", err)
			hints()
			return "", nil, errSilent
		}
		a.out.Warn("%v, so books can't be opened", err)
		hints()
		return "", nil, nil
	}
	return bin, nil, err
}
