package cli

import (
	"cmp"
	"errors"
	"net"
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

kiwix-serve comes from kiwix-tools; it is not bundled with zimon.`,
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
			bin, findErr := kiwix.Find(a.cfg.Serve.KiwixServePath)
			if printOnly {
				if findErr != nil {
					bin = "kiwix-serve"
				}
				a.out.Println(shell.Quote(append([]string{bin}, kiwix.Args(opts)...)))
				return nil
			}
			if errors.Is(findErr, kiwix.ErrNotInstalled) {
				if plain {
					a.out.Fail("kiwix-serve is not installed")
				} else {
					a.out.Warn("kiwix-serve is not installed, so books can't be opened yet")
				}
				a.out.Hint("  Install kiwix-tools: your distro's kiwix-tools package, the static binaries from")
				a.out.Hint("  https://download.kiwix.org/release/kiwix-tools/, or: nix shell nixpkgs#kiwix-tools")
				a.out.Hint("  Or set serve.kiwix_serve_path in %s", ui.Tilde(a.cfgPath))
				if plain {
					return errSilent
				}
				bin = ""
			} else if findErr != nil {
				return findErr
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
			srv.Version = a.version
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
