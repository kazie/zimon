package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/collections"
	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/library"
	"github.com/kazie/zimon/internal/ui"
)

func newAddCmd(a *app) *cobra.Command {
	var noDownload, yes bool
	var level, name, sha string
	cmd := &cobra.Command{
		Use:   "add <name[:flavour] | @collection | URL | file.zim>...",
		Short: "Add ZIMs or whole collections to your library and download them",
		Long: `Add ZIMs, or whole collections like @linux, to library.toml and download
them. 'zimon update' keeps them up to date. Downloads resume where they
stopped, so Ctrl-C is safe: run the same add again.

Without a flavour, zimon asks which one you want (on a terminal), or uses the
only one there is. For a collection, --level says how deep it goes: light,
standard (the default) or deep. On a terminal zimon shows the size of each
level and asks. See 'zimon collections' for what there is.

A ZIM that is in no catalog comes from a URL or a file: one you built with
zimit or mwoffliner, or got elsewhere. library.toml's [sources] keeps where
it came from, and update looks there for a new release. A file is linked into
the library folder: cloned where the disk can, else copied. A file's ZIM is
named after its own name, a URL's after its file name; --name chooses another.
--sha256 checks it.`,
		Example: `  zimon add wikipedia_en_100
  zimon add wikipedia_en_all:mini superuser.com_en_all
  zimon add @linux --level deep
  zimon add @japan @anime-manga
  zimon add ~/zims/mysite_en_all_2026-11.zim
  zimon add https://example.org/zims/mysite_en_all.zim --sha256 9f86d0…`,
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: a.completeAddArgs,
		Annotations:       onRemote,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := a.freshCatalog(ctx, false); err != nil {
				return err
			}
			req := api.Request{Collections: map[string]string{}}
			var rest []string // not sources
			for _, arg := range args {
				if src, ok, err := a.sourceArg(arg); err != nil {
					return err
				} else if ok {
					req.Sources = append(req.Sources, api.Source{From: src, Name: name, SHA256: sha})
				} else {
					rest = append(rest, arg)
				}
			}
			if (name != "" || sha != "") && len(req.Sources) != 1 {
				return errors.New("--name and --sha256 go with one URL or file")
			}
			var cols []api.Collection // with their sizes, once asked for
			for _, arg := range rest {
				if id, ok := strings.CutPrefix(arg, "@"); ok {
					l, ok, err := a.pickLevel(ctx, id, level, &cols)
					if err != nil || !ok {
						return cmp.Or(err, errSilent)
					}
					req.Collections[id] = l.String()
					continue
				}
				e, err := a.pickEntry(ctx, arg)
				if err != nil {
					return err
				}
				if e == nil {
					return errSilent
				}
				req.Keys = append(req.Keys, e.ExactKey())
			}
			if noDownload {
				req.NoDownload = true
				return a.saveOnly(ctx, "add", req, "Download it with: zimon apply")
			}
			err := a.applyChange(ctx, change{kind: "add", req: req, ask: len(req.Collections) > 0, yes: yes})
			if err == nil {
				a.out.Hint("Read it in a browser: zimon serve")
			}
			return err
		},
	}
	cmd.Flags().StringVar(&level, "level", "", "how deep collections go: "+collections.LevelNames)
	cmd.Flags().BoolVar(&noDownload, "no-download", false, "only add to library.toml; download later")
	cmd.Flags().StringVar(&name, "name", "", "the name of a ZIM from a URL or file")
	cmd.Flags().StringVar(&sha, "sha256", "", "the sha-256 a ZIM from a URL must have")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	_ = cmd.RegisterFlagCompletionFunc("level", cobra.FixedCompletions(levelNames(), cobra.ShellCompDirectiveNoFileComp))
	return cmd
}

// sourceArg tells whether arg is a URL or a file to add a ZIM from, and
// makes a file's path absolute. A file works only here, not on a remote.
func (a *app) sourceArg(arg string) (string, bool, error) {
	switch {
	case config.IsURL(arg):
		return arg, true, nil
	case !strings.Contains(arg, "/") && !strings.HasSuffix(arg, ".zim"):
		return "", false, nil
	case a.remote != nil:
		return "", false, fmt.Errorf("%s: a file here can't be added on %s; add it from a URL, or on the server itself", arg, a.remote.Name)
	}
	p, err := config.ExpandHome(arg)
	if err == nil {
		p, err = filepath.Abs(p)
	}
	return p, true, err
}

// pickLevel is the --level flag, or on a terminal the user's pick from the
// levels with their sizes (from cols, read into it the first time), or
// standard. ok is false when the user cancelled.
func (a *app) pickLevel(ctx context.Context, id, flag string, cols *[]api.Collection) (l collections.Level, ok bool, err error) {
	if flag != "" {
		if l, ok = collections.ParseLevel(flag); !ok {
			return l, false, fmt.Errorf("--level %q: use %s", flag, collections.LevelNames)
		}
		return l, true, nil
	}
	if !a.out.InTTY {
		return collections.Standard, true, nil
	}
	if *cols == nil {
		if *cols, err = a.be.Collections(ctx); err != nil {
			return l, false, err
		}
	}
	i := slices.IndexFunc(*cols, func(c api.Collection) bool { return c.ID == id })
	if i < 0 {
		return collections.Standard, true, nil // the change says there is no such collection
	}
	var opts []string
	for _, l := range collections.Levels {
		opts = append(opts, fmt.Sprintf("%-8s %9s  %s", l, ui.SizeOrDash((*cols)[i].Sizes[l.String()]), ui.Dim.Render(l.Blurb())))
	}
	n, err := a.out.Choose(fmt.Sprintf("How deep should %s go?", (*cols)[i].Title), opts)
	if err != nil || n < 0 {
		if err == nil {
			a.out.Status("Cancelled.")
		}
		return l, false, err
	}
	return collections.Levels[n], true, nil
}

// pickEntry resolves an argument to one catalog entry, asking for a flavour if needed.
// It returns nil (and prints why) when the user cancels.
func (a *app) pickEntry(ctx context.Context, arg string) (*api.Entry, error) {
	entries, picked, err := a.lookup(ctx, arg)
	if err != nil || picked != nil {
		return picked, err
	}
	name := entries[0].Name
	if !a.out.InTTY {
		var names []string
		for _, e := range entries {
			names = append(names, fmt.Sprintf("%s (%s)", e.Key, ui.Size(e.Size)))
		}
		return nil, fmt.Errorf("%s comes in several flavours; pick one:\n  %s", name, strings.Join(names, "\n  "))
	}
	var opts []string
	for _, e := range entries {
		feats, _ := catalog.Features(e.Tags)
		opts = append(opts, fmt.Sprintf("%-8s %9s  %s  %s", flavour(e.Key), ui.Size(e.Size), e.Date, ui.Dim.Render(strings.Join(feats, ", "))))
	}
	i, err := a.out.Choose(fmt.Sprintf("Which flavour of %s?", name), opts)
	if err != nil {
		return nil, err
	}
	if i < 0 {
		a.out.Status("Cancelled.")
		return nil, nil
	}
	return &entries[i], nil
}

func newListCmd(a *app) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:         "list",
		Aliases:     []string{"ls"},
		Short:       "Show your ZIMs and whether they are up to date",
		Args:        cobra.NoArgs,
		Annotations: onRemote,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if err := a.freshCatalog(ctx, false); err != nil {
				return err
			}
			items, err := a.be.Items(ctx)
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(a.out.Out)
				enc.SetIndent("", "  ")
				return enc.Encode(items)
			}
			if len(items) == 0 {
				a.out.Status("Nothing here yet.")
				a.out.Hint("Find something with: zimon collections   zimon search <words>   or   zimon browse")
				return nil
			}
			info, err := a.be.Info(ctx)
			if err != nil {
				return err
			}
			t := ui.NewTable("", "NAME", "TITLE", "SIZE", "DATE", "STATUS")
			t.Flex = 2
			t.Right = []int{3}
			var onDisk int64
			updates, apply := 0, 0
			for _, it := range items {
				sz, date := "", it.Latest
				switch {
				case it.Path != "": // on disk
					sz, date = ui.Size(it.Size), it.Date
					onDisk += it.Size
				case it.LatestSize > 0:
					sz = ui.Size(it.LatestSize)
				}
				switch it.State {
				case library.UpdateAvailable:
					updates++
				case library.Missing, library.Unwanted:
					apply++
				}
				t.Add(ui.Marker(&it), ui.Bold.Render(it.Key), it.Title, sz, date, ui.StateText(&it))
			}
			t.Render(a.out.Out, a.out.Width())
			a.out.Hint("%d ZIM%s · %s on disk · %s", len(items), ui.Plural(len(items)), config.FormatBytes(onDisk), ui.Tilde(info.LibraryDir))
			if updates > 0 {
				a.out.Status("%s %d update%s available: %s", ui.Yellow.Render("↑"), updates, ui.Plural(updates), ui.Bold.Render("zimon update"))
			}
			if apply > 0 {
				a.out.Status("%s library.toml asks for changes: %s", ui.Yellow.Render("○"), ui.Bold.Render("zimon apply"))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func newUpdateCmd(a *app) *cobra.Command {
	var check, yes bool
	cmd := &cobra.Command{
		Use:   "update [name...]",
		Short: "Download newer releases of your ZIMs",
		Long: `Refresh the catalog, then download newer releases of the ZIMs library.toml
asks for (or only the ones named). Each new file is verified before it
replaces the old one; older versions beyond keep_versions are deleted
afterwards. Interrupted downloads resume.

Wanted ZIMs that are not on disk yet (say, a collection gained an item) are
downloaded as library.toml's new_items says: by default only while the
library stays within its budget. update never deletes or switches flavours;
'zimon apply' does that.

For unattended runs (systemd timers, cron) use --yes.`,
		Example: `  zimon update --check
  zimon update
  zimon update --yes            # no questions, for timers`,
		ValidArgsFunction: a.completeLocalKeys,
		Annotations:       onRemote,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := a.freshCatalog(ctx, true); err != nil {
				return err
			}
			return a.applyChange(ctx, change{kind: "update", req: api.Request{Names: args}, ask: true, yes: yes, dryRun: check,
				nothing: "Everything is up to date."})
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only show what would be updated")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

func newRemoveCmd(a *app) *cobra.Command {
	var keepFile, yes bool
	cmd := &cobra.Command{
		Use:     "remove <name[:flavour] | @collection>...",
		Aliases: []string{"rm"},
		Short:   "Remove ZIMs or collections from your library and delete their files",
		Long: `Remove ZIMs or collections from library.toml and delete their files. A ZIM
that another collection (or your [items]) still wants stays. Removing one
ZIM of a collection marks it "off" in library.toml, so the collection does
not bring it back.`,
		Example: `  zimon remove wikipedia_en_100
  zimon remove @games
  zimon remove superuser.com_en_all --keep-file`,
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: a.completeRemoveArgs,
		Annotations:       onRemote,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.applyChange(cmd.Context(), change{kind: "remove", req: api.Request{Targets: args, KeepFiles: keepFile}, yes: yes})
		},
	}
	cmd.Flags().BoolVar(&keepFile, "keep-file", false, "forget the ZIMs but leave the files on disk")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

func newVerifyCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:               "verify [name[:flavour]...]",
		Short:             "Check files on disk against their sha-256 again",
		Long:              "Read every ZIM (or the ones named) from disk and compare it with the sha-256 recorded when it was downloaded.",
		ValidArgsFunction: a.completeLocalKeys,
		Annotations:       onRemote,
		RunE: func(cmd *cobra.Command, args []string) error {
			sh := &shower{a: a}
			err := a.be.Run(cmd.Context(), "verify", api.Request{Names: args}, sh.show)
			sh.finish()
			switch {
			case sh.bad > 0:
				a.out.Hint("Re-download a damaged file with: zimon remove <name> --yes && zimon add <name>")
				return errSilent
			case err == nil && sh.verified == 0:
				a.out.Status("No files to verify.")
			}
			return err
		},
	}
}

func newPruneCmd(a *app) *cobra.Command {
	var dryRun, yes bool
	cmd := &cobra.Command{
		Use:         "prune",
		Short:       "Delete old versions and leftover partial downloads",
		Args:        cobra.NoArgs,
		Annotations: onRemote,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if err := a.freshCatalog(ctx, false); err != nil {
				return err
			}
			plan, err := a.be.PrunePlan(ctx)
			if err != nil {
				return err
			}
			if len(plan) == 0 {
				a.out.Success("Nothing to prune.")
				return nil
			}
			info, err := a.be.Info(ctx)
			if err != nil {
				return err
			}
			t := ui.NewTable("FILE", "SIZE", "WHY")
			t.Flex = 0
			t.Right = []int{1}
			paths := make([]string, len(plan))
			for i, r := range plan {
				paths[i] = r.Path
				p := r.Path
				if rel, err := filepath.Rel(info.LibraryDir, p); err == nil && filepath.IsLocal(rel) {
					p = rel
				}
				t.Add(p, ui.Size(r.Size), r.Reason)
			}
			t.Render(a.out.Out, a.out.Width())
			a.out.Hint("in %s", ui.Tilde(info.LibraryDir))
			if dryRun {
				return nil
			}
			if ok, err := a.confirmDelete(yes, fmt.Sprintf("Delete these (%s)?", ui.Size(api.Total(plan)))); err != nil || !ok {
				return err
			}
			sh := &shower{a: a, prune: true}
			if err := a.be.Run(ctx, "prune", api.Request{Paths: paths}, sh.show); err != nil {
				return err
			}
			a.out.Success("Freed %s", ui.Size(sh.freed))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "only show what would be deleted")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

// confirmDelete asks before deleting, unless yes. Without a terminal it
// refuses rather than deleting unasked.
func (a *app) confirmDelete(yes bool, question string) (bool, error) {
	if yes {
		return true, nil
	}
	if !a.out.InTTY {
		return false, errors.New("refusing to delete without confirmation; pass --yes")
	}
	return a.out.Confirm(question, false)
}
