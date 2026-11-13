package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/collections"
	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/setup"
	"github.com/kazie/zimon/internal/ui"
)

func newCollectionsCmd(a *app) *cobra.Command {
	var levels []string
	for _, l := range collections.Levels {
		levels = append(levels, fmt.Sprintf("  %-10s %s", l, l.Blurb()))
	}
	cmd := &cobra.Command{
		Use:     "collections",
		Aliases: []string{"cols"},
		Short:   "Show curated collections of ZIMs and their sizes",
		Long: `Show the curated collections (Linux, repair, medicine, Japan, games, ...)
and how big each is at every level:

` + strings.Join(levels, "\n") + `

Giants (Stack Overflow, Khan Academy, all of Gutenberg, ...) are never added
by a level; add one with 'zimon level <name> on'. Your own collections go in
collections.toml next to library.toml.`,
		Example: `  zimon collections
  zimon collections show linux
  zimon add @linux --level deep`,
		Args:        cobra.NoArgs,
		Annotations: onRemote,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if err := a.freshCatalog(ctx, false); err != nil {
				return err
			}
			rows, err := a.be.Collections(ctx)
			if err != nil {
				return err
			}
			t := ui.NewTable("", "COLLECTION", "TITLE", "YOURS", "LIGHT", "STANDARD", "DEEP")
			t.Flex = 2
			t.Right = []int{4, 5, 6}
			for _, r := range rows {
				mark, yours := " ", ""
				if r.Mine {
					mark, yours = ui.Green.Render("✓"), ui.Green.Render(r.Level)
				}
				cells := []string{mark, ui.Bold.Render("@" + r.ID), r.Title, yours}
				for _, l := range levelNames() {
					cell := ui.SizeOrDash(r.Sizes[l])
					if r.Mine && l == r.Level {
						cell = ui.Green.Render(cell)
					}
					cells = append(cells, cell)
				}
				t.Add(cells...)
			}
			t.Render(a.out.Out, a.out.Width())
			a.out.Hint("Details: zimon collections show <name>   ·   add one: zimon add @<name> --level standard")
			return nil
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:               "show <collection>",
		Short:             "Show what a collection contains at each level",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: a.completeCollectionArg,
		Annotations:       onRemote,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := a.freshCatalog(ctx, false); err != nil {
				return err
			}
			d, err := a.be.Collection(ctx, strings.TrimPrefix(args[0], "@"))
			if err != nil {
				return err
			}
			a.showCollection(d)
			return nil
		},
	})
	return cmd
}

func (a *app) showCollection(d *api.CollectionDetail) {
	a.out.Println(ui.Bold.Render(d.Title) + ui.Dim.Render(" · @"+d.ID))
	if d.Blurb != "" {
		a.out.Println(d.Blurb)
	}
	var levels []string
	for _, l := range levelNames() {
		txt := l + " " + ui.SizeOrDash(d.Sizes[l])
		if d.Mine && l == d.Level {
			txt = ui.Green.Render(txt + " (yours)")
		}
		levels = append(levels, txt)
	}
	a.out.Println(ui.Dim.Render("Levels: ") + strings.Join(levels, ui.Dim.Render(" · ")))
	if !d.Mine {
		a.out.Println(ui.Dim.Render("Not in your library; showing the standard level."))
	}
	a.out.Println("")

	t := ui.NewTable("", "NAME", "SIZE", "TIER", "WHY IT IS HERE")
	t.Flex = 4
	t.Right = []int{2}
	giants := false
	for _, x := range d.Items {
		name, size, mark := x.Name, "", " "
		note := x.Note
		if x.Key != "" {
			name, size, mark = x.Key, ui.Size(x.Size), ui.StateMarker(x.State)
		}
		switch {
		case x.NeedsRegion:
			note = ui.Dim.Render(`set region = "<country>" in library.toml for this map`)
		case x.Gone:
			note = ui.Dim.Render("not in the Kiwix catalog right now")
		case x.Off:
			note = ui.Dim.Render("switched off in library.toml · ") + note
		}
		tier := x.Tier
		if !x.Included {
			if x.Tier == collections.Giant.String() {
				giants = true
				tier = "giant, opt-in"
			}
			tier, name = ui.Dim.Render(tier), ui.Dim.Render(name)
		} else {
			name = ui.Bold.Render(name)
		}
		t.Add(mark, name, size, tier, note)
	}
	t.Render(a.out.Out, a.out.Width())
	if d.Mine {
		a.out.Hint("Change the level: zimon level @%s %s", d.ID, strings.Join(levelNames(), "|"))
	} else {
		a.out.Hint("Add it: zimon add @%s --level standard", d.ID)
	}
	if giants {
		a.out.Hint("Add a giant on its own: zimon level <name> on")
	}
}

func levelNames() []string {
	var out []string
	for _, l := range collections.Levels {
		out = append(out, l.String())
	}
	return out
}

func newLevelCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "level <@collection | name> <level | flavour | on | off | default>",
		Short: "Change how deep a collection goes, or which flavour of a ZIM you get",
		Long: `Change library.toml and make the library match it, after showing what will
be downloaded and deleted.

For a collection, give ` + collections.LevelNames + `. For a single ZIM, give a
flavour (mini, nopic, maxi, ...), a level (picks the flavour like a
collection would), on (add it, e.g. a giant), off (leave it out even when a
collection wants it) or default (the variant without a flavour).

A flavour switch downloads and verifies the new flavour before deleting the
old one, so a failed download never leaves you with nothing.`,
		Example: `  zimon level @linux deep
  zimon level wikipedia_en_all maxi
  zimon level stackoverflow.com_en_all on
  zimon level minecraft.wiki_en_all off`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: a.completeLevelArgs,
		Annotations:       onRemote,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := a.freshCatalog(ctx, false); err != nil {
				return err
			}
			return a.applyChange(ctx, change{kind: "level", req: api.Request{Target: args[0], Choice: args[1]}, ask: true, yes: yes})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

func newApplyCmd(a *app) *cobra.Command {
	var dryRun, yes bool
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Make the library match library.toml",
		Long: `Download what library.toml asks for and delete what it no longer does, after
showing the plan. Also switches flavours when a level or [items] choice
changed, and installs newer releases.

Use it after editing library.toml by hand, or on a fresh machine with only
library.toml: ZIMs already in the library folder are checked and kept instead
of downloaded again.`,
		Example: `  zimon apply --dry-run
  zimon apply
  zimon apply --yes             # no questions`,
		Args:        cobra.NoArgs,
		Annotations: onRemote,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if err := a.freshCatalog(ctx, false); err != nil {
				return err
			}
			return a.applyChange(ctx, change{kind: "apply", ask: true, yes: yes, dryRun: dryRun})
		},
	}
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "only show what would change")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

func newBudgetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "budget [size | off]",
		Short: "Show disk use per collection, or set a size budget",
		Long: `Without arguments, show how much each collection uses on disk and would use
at its level, against the budget and the free disk space.

With a size, set the budget in library.toml. Unattended updates then add new
ZIMs only while the library stays within it (see new_items in library.toml),
and plans show when a change would go over it.`,
		Example: `  zimon budget
  zimon budget 250GB
  zimon budget off`,
		Args:        cobra.MaximumNArgs(1),
		Annotations: onRemote,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := a.freshCatalog(ctx, false); err != nil {
				return err
			}
			if len(args) == 1 {
				var n int64
				if args[0] != "off" {
					var err error
					if n, err = config.ParseBytes(args[0]); err != nil {
						return err
					}
				}
				next := "Plans and unattended updates now respect it."
				if n <= 0 { // off, or 0
					next = "Budget removed."
				}
				if err := a.saveOnly(ctx, "budget", api.Request{Budget: &n}, next); err != nil {
					return err
				}
			}
			return a.showBudget(ctx)
		},
	}
}

func (a *app) showBudget(ctx context.Context) error {
	rep, err := a.be.Budget(ctx)
	if err != nil {
		return err
	}
	t := ui.NewTable("WHAT", "LEVEL", "ON DISK", "AT ITS LEVEL")
	t.Flex = 0
	t.Right = []int{2, 3}
	for _, u := range rep.Uses {
		what, level := ui.Bold.Render("@"+u.Via), u.Level
		if u.Via == setup.ViaItems {
			what, level = ui.Bold.Render("hand-picked"), ""
		}
		t.Add(what, level, config.FormatBytes(u.Disk), config.FormatBytes(u.Wanted))
	}
	if rep.Extra > 0 {
		t.Add(ui.Dim.Render("not wanted (apply deletes)"), "", config.FormatBytes(rep.Extra), "")
	}
	t.Render(a.out.Out, a.out.Width())
	line := "Library " + config.FormatBytes(rep.Disk) + " on disk, " + config.FormatBytes(rep.Total) + " when complete"
	if rep.Budget > 0 {
		line += " · budget " + ui.Size(rep.Budget)
		if rep.Total > rep.Budget {
			line += " " + ui.Yellow.Render("(over by "+ui.Size(rep.Total-rep.Budget)+")")
		}
	} else {
		line += " · no budget (set one: zimon budget 250GB)"
	}
	if rep.Free > 0 {
		line += " · " + ui.Size(rep.Free) + " free"
	}
	a.out.Hint("%s", line)
	if len(rep.Uses) > 1 {
		a.out.Hint("A ZIM in several collections counts in each of them.")
	}
	return nil
}

func newSuggestCmd(a *app) *cobra.Command {
	var budget string
	var yes bool
	cmd := &cobra.Command{
		Use:   "suggest @collection... [--budget size]",
		Short: "Pick levels for collections that fit a size budget",
		Long: `Given collections in order of importance, start them all at light and then
raise each in turn (first to standard, then to deep) while the whole library
still fits the budget. Shows the result and the plan, and applies it after you
confirm. The rest of library.toml counts towards the budget as it is.`,
		Example: `  zimon suggest --budget 250GB @linux @anime-manga @games @science
  zimon suggest @japan @world-view      # with the budget from library.toml`,
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: a.completeCollectionArg,
		Annotations:       onRemote,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := a.freshCatalog(ctx, false); err != nil {
				return err
			}
			var limit int64 // 0: the budget in library.toml
			if budget != "" {
				var err error
				if limit, err = config.ParseBytes(budget); err != nil {
					return err
				}
				if limit <= 0 {
					return errors.New("give a budget (--budget 250GB) or set one: zimon budget 250GB")
				}
			}
			var ids []string
			for _, arg := range args {
				if id := strings.TrimPrefix(arg, "@"); !slices.Contains(ids, id) {
					ids = append(ids, id)
				}
			}
			sg, err := a.be.Suggest(ctx, ids, limit)
			if err != nil {
				return err
			}
			if !sg.Fits {
				return fmt.Errorf("even at light these need about %s, over the %s budget; drop some collections or raise the budget", ui.Size(sg.Total), ui.Size(sg.Budget))
			}
			t := ui.NewTable("COLLECTION", "LEVEL")
			t.Flex = -1
			for _, id := range ids {
				t.Add(ui.Bold.Render("@"+id), sg.Levels[id])
			}
			t.Render(a.out.Out, a.out.Width())
			a.out.Hint("Library about %s of %s when complete.", ui.Size(sg.Total), ui.Size(sg.Budget))
			req := api.Request{Collections: sg.Levels}
			if budget != "" {
				req.Budget = &limit
			}
			return a.applyChange(ctx, change{kind: "add", req: req, ask: true, yes: yes})
		},
	}
	cmd.Flags().StringVar(&budget, "budget", "", "size budget, e.g. 250GB (saved to library.toml)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}
