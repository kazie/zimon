package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/backend"
	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/client"
	"github.com/kazie/zimon/internal/collections"
	"github.com/kazie/zimon/internal/library"
	"github.com/kazie/zimon/internal/task"
	"github.com/kazie/zimon/internal/ui"
)

func newPullCmd(a *app) *cobra.Command {
	var yes bool
	var level string
	cmd := &cobra.Command{
		Use:   "pull <remote> <name[:flavour] | @collection>...",
		Short: "Copy ZIMs from the library of a zimon serve",
		Long: `Copy ZIMs from the library of a zimon serve (an https:// remote, see
'zimon remote') into this one: over the home network, say, before you leave
with a laptop. zimon adds them to library.toml, as 'zimon add' does, and
downloads them from the remote only. Each is checked against Kiwix's
.meta4 when it is the release in this catalog and Kiwix can be reached, else
against the remote's sha-256 (which it checked against Kiwix's).

A collection keeps the level it has here, or comes at the level the remote
has it; --level says otherwise. What the remote does not have stays in
library.toml, and 'zimon apply' downloads it from Kiwix. Ctrl-C is safe: run the same pull again to
resume.`,
		Example: `  zimon pull home @linux
  zimon pull home @programming --level light
  zimon pull home wikipedia_en_all:maxi --yes`,
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: a.completePullArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.pull(cmd.Context(), args[0], args[1:], level, yes)
		},
	}
	cmd.Flags().StringVar(&level, "level", "", "how deep collections go: "+collections.LevelNames+" (default: yours, else the remote's)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	_ = cmd.RegisterFlagCompletionFunc("level", cobra.FixedCompletions(levelNames(), cobra.ShellCompDirectiveNoFileComp))
	return cmd
}

func (a *app) pull(ctx context.Context, name string, targets []string, level string, yes bool) error {
	c, err := a.pullClient(ctx, name)
	if err != nil {
		return err
	}
	if err := a.freshCatalog(ctx, false); err != nil {
		return err
	}
	items, err := c.Items(ctx)
	if err != nil {
		return err
	}
	there := api.ItemsByKey(items) // the remote's files
	// Only files on its disk (they have an id), with an id and name fit for
	// a file name here.
	maps.DeleteFunc(there, func(_ string, it *api.Item) bool { r := it.Release(); return !r.Safe() })
	req, err := a.pullRequest(ctx, c, there, targets, level)
	if err != nil {
		return err
	}
	pv, err := a.be.Preview(ctx, "add", req)
	if err != nil {
		return err
	}
	here, err := a.be.Items(ctx)
	if err != nil {
		return err
	}
	pp := pullPlan(pv.Plan, there, api.ItemsByKey(here), c)
	p := &pp.Plan
	a.showPlan(p, false)
	// What waits for zimon apply, once library.toml says so.
	leftover := func() {
		a.showDeferred(p)
		hint := func(what string, keys []string) {
			if len(keys) > 0 {
				a.out.Hint("%s %s %s: zimon apply downloads %s from Kiwix", name, what, strings.Join(keys, ", "), them(len(keys)))
			}
		}
		hint("does not have", pp.lacks)
		hint("has no newer release of", pp.older)
	}
	if len(pp.files) == 0 && !pv.Edits {
		a.out.Success("Nothing to pull from %s.", name)
		leftover()
		return nil
	}
	switch {
	case len(pp.drops) > 0:
		a.out.Warn("library.toml will no longer want %s: zimon apply deletes %s", strings.Join(pp.drops, ", "), them(len(pp.drops)))
		if ok, err := a.confirmDelete(yes, "Go ahead?"); err != nil || !ok {
			return err
		}
	case len(pp.files) > 0 && len(req.Collections) > 0 && !yes: // asks as add does
		if ok, err := a.out.Confirm("Download "+ui.Size(p.Download)+" from "+name+"?", true); err != nil || !ok {
			return err
		}
	}
	req.Plan = pv.Plan.ID
	sh := &shower{a: a}
	err = a.local().Pull(ctx, req, pp.files, sh.show)
	sh.finish()
	switch {
	case errors.Is(err, context.Canceled):
		a.out.Hint("Stopped. Run the same zimon pull again to resume.")
		return err
	case sh.failed > 0:
		leftover()
		a.out.Fail("%d of %d downloads failed. They stay in library.toml: run the same zimon pull again, or zimon apply to download from Kiwix.",
			sh.failed, sh.steps)
		return errSilent
	case err != nil:
		return err
	}
	if len(pp.files) == 0 {
		a.out.Success("Nothing to download from %s.", name)
	}
	leftover()
	return nil
}

// pullClient is the API of the https:// remote called name, checked.
func (a *app) pullClient(ctx context.Context, name string) (*client.Client, error) {
	c, err := a.httpRemote(name)
	if err != nil {
		return nil, err
	}
	_, err = c.Info(ctx) // checks the token and the API version
	return c, err
}

// httpRemote is the API of the https:// remote called name.
func (a *app) httpRemote(name string) (*client.Client, error) {
	r, _, _, err := a.findRemote(name)
	if err != nil {
		return nil, err
	}
	if !r.HTTP() {
		return nil, fmt.Errorf("%s is an ssh:// remote; zimon pull needs a zimon serve, reached at https://", name)
	}
	return client.New(r, a.httpClient())
}

// pullRequest is the add that brings targets here: collections at the
// level asked for, else the one they have here, else the remote's; ZIMs in
// the flavour the remote has.
func (a *app) pullRequest(ctx context.Context, c *client.Client, there map[string]*api.Item, targets []string, level string) (api.Request, error) {
	if level != "" && !slices.ContainsFunc(targets, func(t string) bool { return strings.HasPrefix(t, "@") }) {
		return api.Request{}, errors.New("--level is for collections (@id); a ZIM comes in the flavour the remote has")
	}
	var mine, theirs []api.Collection // read on first use
	levelOf := func(cols *[]api.Collection, read func(context.Context) ([]api.Collection, error), id string) (lv string, known bool, err error) {
		if *cols == nil {
			if *cols, err = read(ctx); err != nil {
				return "", false, err
			}
		}
		i := slices.IndexFunc(*cols, func(col api.Collection) bool { return col.ID == id })
		if i < 0 || !(*cols)[i].Mine {
			return "", i >= 0, nil
		}
		return (*cols)[i].Level, true, nil
	}
	req := api.Request{Collections: map[string]string{}}
	for _, arg := range targets {
		if id, ok := strings.CutPrefix(arg, "@"); ok {
			lv, known, err := levelOf(&mine, a.be.Collections, id)
			switch {
			case err != nil:
				return req, err
			case !known:
				return req, fmt.Errorf("no collection @%s (see: zimon collections)", id)
			case level != "":
				lv = level
			case lv == "":
				if lv, _, err = levelOf(&theirs, c.Collections, id); err != nil {
					return req, err
				}
				if lv == "" {
					return req, fmt.Errorf("%s has no @%s in its library; pull it at a level with --level", c.Name(), id)
				}
			}
			req.Collections[id] = lv
			continue
		}
		k, exact := catalog.SplitKey(arg)
		var found []string
		for _, it := range there {
			if it.Name == k.Name && (!exact || it.Flavour == k.Flavour) {
				found = append(found, it.Key)
			}
		}
		slices.Sort(found)
		switch len(found) {
		case 0:
			return req, fmt.Errorf("%s has no %s on disk (see: zimon --remote %s list)", c.Name(), arg, c.Name())
		case 1:
			req.Keys = append(req.Keys, there[found[0]].Release().Key().Exact())
		default:
			return req, fmt.Errorf("%s has several flavours of %s; give one of: %s", c.Name(), arg, strings.Join(found, ", "))
		}
	}
	return req, nil
}

func them(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

// pulling is the part of an add's plan that a pull carries out.
type pulling struct {
	api.Plan // the downloads from the remote, with its sizes and dates
	files    []task.Pulled
	lacks    []string // keys the remote does not have
	older    []string // keys it has no newer release of
	drops    []string // keys library.toml will no longer want, which zimon apply deletes
}

// pullPlan is the part of the add's plan p that the remote can bring: the
// ZIMs it has (there), in a release newer than this library's (here).
// Deletions (a lower level, the other side of a flavour switch) wait for
// zimon apply.
func pullPlan(p api.Plan, there, here map[string]*api.Item, c *client.Client) pulling {
	pp := pulling{Plan: p, drops: p.Deleted()}
	pp.Steps, pp.Download, pp.Freed = []api.Step{}, 0, 0 // a pull deletes nothing
	hc := c.FileClient()
	for _, st := range p.Steps {
		it := there[st.Key]
		switch {
		case st.Kind == api.StepRemove:
			continue
		case it == nil:
			pp.lacks = append(pp.lacks, st.Key)
			continue
		case st.Kind == api.StepUpdate && it.Date <= st.FromDate:
			pp.older = append(pp.older, st.Key)
			continue
		}
		step := st
		step.Date, step.Bytes = it.Date, it.Size
		if h := here[st.Key]; h != nil && h.DownloadUUID == it.UUID { // what is left of it
			step.Bytes -= h.Downloaded
		}
		if st.Kind == api.StepSwitch { // the new flavour comes now; the old one goes with zimon apply
			step.Kind, step.From, step.Freed = api.StepAdd, nil, 0
		}
		pp.Steps = append(pp.Steps, step)
		pp.Download += step.Bytes
		pp.files = append(pp.files, task.Pulled{Release: it.Release(),
			Source: library.Source{From: c.Name(), URL: c.FileURL(it.UUID), HTTP: hc, Name: filepath.Base(it.Path), Size: it.Size, SHA256: it.SHA256, Pull: true}})
	}
	pp.After = p.Current + pp.Download
	return pp
}

// completePullArgs completes a remote, then what it has: ZIMs on its disk
// and the collections in its library.
func (a *app) completePullArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return a.remoteNames(true), cobra.ShellCompDirectiveNoFileComp
	}
	var be backend.Backend
	if c, err := a.httpRemote(args[0]); err == nil {
		be = backend.NewRemote(c)
	}
	return completeOn(cmd, be, func(ctx context.Context, be backend.Backend) []string {
		if strings.HasPrefix(toComplete, "@") {
			return collectionIDs(ctx, be, true)
		}
		items, _ := be.Items(ctx)
		var out []string
		for _, it := range items {
			if it.UUID != "" {
				out = append(out, it.Key+"\t"+cmp.Or(it.Title, ui.Size(it.Size)))
			}
		}
		return out
	})
}
