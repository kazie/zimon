package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"charm.land/huh/v2"
	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"

	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/ui"
)

func newInitCmd(a *app) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up zimon: library location and languages",
		Long: `Create the config file. On a terminal it asks where to keep ZIM files and
which languages to show by default; elsewhere it writes the defaults.

An existing config is kept: on a terminal zimon asks first, elsewhere it
refuses unless --force.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if a.cfgFound && !force {
				if !a.out.InTTY { // no one to ask
					return fmt.Errorf("%s already exists; zimon init --force overwrites it", ui.Tilde(a.cfgPath))
				}
				ok, err := a.out.Confirm(fmt.Sprintf("%s already exists. Overwrite it?", ui.Tilde(a.cfgPath)), false)
				if err != nil || !ok {
					return err
				}
			}
			return a.runInit(true)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config without asking")
	return cmd
}

func (a *app) runInit(announce bool) error {
	cfg := *a.cfg
	dir, langs := ui.Tilde(cfg.LibraryDir), slices.Clone(cfg.Languages)
	// The form needs a terminal on both ends; otherwise ask line by line
	// (and without a terminal at all, take the defaults).
	if a.out.InTTY && a.out.ErrTTY {
		err := initForm(&dir, &langs).WithOutput(os.Stderr).Run()
		if errors.Is(err, huh.ErrUserAborted) {
			return context.Canceled
		} else if err != nil {
			return err
		}
	} else {
		var err error
		if dir, err = a.out.Ask("Where should ZIM files live?", dir); err != nil {
			return err
		}
		answer, err := a.out.Ask("Languages to show by default (comma-separated ISO 639-3, or 'all')?", strings.Join(langs, ","))
		if err != nil {
			return err
		}
		langs = nil
		if strings.TrimSpace(answer) != "all" {
			for l := range strings.SplitSeq(answer, ",") {
				if l = strings.TrimSpace(l); l != "" {
					langs = append(langs, l)
				}
			}
		}
	}
	// Relative answers are relative to here, not to wherever zimon runs later.
	dir, err := config.ExpandHome(dir)
	if err != nil {
		return err
	}
	if cfg.LibraryDir, err = filepath.Abs(dir); err != nil {
		return err
	}
	cfg.Languages = langs
	// Check before saving, so a bad answer can't leave a config that every
	// later command refuses to load.
	if err := cfg.Normalize(); err != nil {
		return err
	}
	if err := cfg.Save(a.cfgPath); err != nil {
		return err
	}
	a.cfg, a.cfgFound = &cfg, true
	if err := os.MkdirAll(a.cfg.LibraryDir, 0o755); err != nil {
		return err
	}
	a.out.Success("Wrote %s", ui.Bold.Render(ui.Tilde(a.cfgPath)))
	a.out.Success("Library: %s", ui.Bold.Render(ui.Tilde(a.cfg.LibraryDir)))
	a.out.Success("Languages: %s", ui.Bold.Render(cmp.Or(strings.Join(a.cfg.Languages, ", "), "all")))
	if announce {
		a.out.Hint("Next: zimon search <words>   or   zimon browse")
	}
	return nil
}

// configHelp lists every setting in config.toml; a test checks that none is
// left out.
const configHelp = `Print the effective configuration as TOML.

Settings:
  library_dir       where ZIM files and library.xml live
  state_dir         where the database and lock file live
  library_file      what the library should contain (default: library.toml next to config.toml)
  languages         default language filter for search and browse ([] = all;
                    default: your system language plus English)
  keep_versions     versions of each ZIM to keep after an update (default 1)
  catalog_url       Kiwix OPDS v2 root
  catalogs          more OPDS catalogs, e.g. ["https://other.example/catalog/v2"]
  catalog_max_age   refresh the catalog when older than this (default 24h)
  download.rate_limit  max download speed, e.g. "20MB" (per second), 0 = unlimited
  serve.address     where zimon serve listens: 127.0.0.1 (default, this machine
                    only), all (every network interface) or one IP
  serve.port, serve.kiwix_serve_path   zimon serve's port (8080) and the kiwix-serve binary
  serve.admin_port  the admin page's port, on the same address (default: port + 1)
  remote.ssh_command  the ssh program for remotes, with any options (ssh)
  schedule.update   how often zimon serve updates the library: off (default), daily,
                    weekly or monthly
  schedule.time     at what time, HH:MM local time (default 03:30)

The config file is $ZIMON_CONFIG, or ~/.config/zimon/config.toml.`

func newConfigCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show the effective configuration",
		Long:  configHelp,
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			data, err := toml.Marshal(a.cfg)
			if err != nil {
				return err
			}
			src := a.cfgPath
			if !a.cfgFound {
				src += " (not created yet; showing defaults)"
			}
			a.out.Hint("# %s", src)
			a.out.Printf("%s", data)
			return nil
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "path",
		Short: "Print the config file path",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			a.out.Println(a.cfgPath)
			return nil
		},
	}, &cobra.Command{
		Use:   "edit",
		Short: "Open the config file in $EDITOR",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if !a.cfgFound {
				if err := a.cfg.Save(a.cfgPath); err != nil {
					return err
				}
			}
			parts := strings.Fields(cmp.Or(os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi"))
			c := exec.Command(parts[0], append(parts[1:], a.cfgPath)...)
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := c.Run(); err != nil {
				return err
			}
			if _, _, err := config.Load(a.cfgPath); err != nil {
				return fmt.Errorf("the edited config has a problem: %w", err)
			}
			return nil
		},
	})
	return cmd
}
