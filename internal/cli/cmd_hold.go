package cli

import (
	"context"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/backend"
	"github.com/kazie/zimon/internal/library"
	"github.com/kazie/zimon/internal/ui"
)

func newHoldCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "hold [name...]",
		Short: "Keep the release of a ZIM you have; update skips it",
		Long: `Keep the release of a ZIM that is on disk now: update and apply leave it as
it is, newer releases and flavour changes included, until zimon unhold.
The hold is saved in library.toml's [hold] table, with the release's date.

Without names, list the held ZIMs.`,
		Example: `  zimon hold wikipedia_en_all
  zimon hold
  zimon unhold wikipedia_en_all`,
		ValidArgsFunction: a.completeLocalKeys,
		Annotations:       onRemote,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if len(args) == 0 {
				return a.showHeld(ctx)
			}
			return a.saveOnly(ctx, "hold", api.Request{Names: args}, "update and apply keep this release. Let it change again with: zimon unhold "+names(args))
		},
	}
}

func newUnholdCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:               "unhold <name>...",
		Short:             "Let a held ZIM update again",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: a.completeHeld,
		Annotations:       onRemote,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.saveOnly(cmd.Context(), "unhold", api.Request{Names: args}, "Bring it up to date with: zimon update "+names(args))
		},
	}
}

// names are the ZIM names of args ("name" or "name:flavour").
func names(args []string) string {
	out := make([]string, len(args))
	for i, arg := range args {
		out[i], _, _ = strings.Cut(arg, ":")
	}
	return strings.Join(out, " ")
}

// showHeld lists the held ZIMs.
func (a *app) showHeld(ctx context.Context) error {
	if err := a.freshCatalog(ctx, false); err != nil {
		return err
	}
	items, err := a.be.Items(ctx)
	if err != nil {
		return err
	}
	t := ui.NewTable("", "NAME", "TITLE", "DATE", "STATUS")
	t.Flex = 2
	n := 0
	for _, it := range items {
		if it.State == library.Held {
			t.Add(ui.Marker(&it), ui.Bold.Render(it.Key), it.Title, it.Date, ui.StateText(&it))
			n++
		}
	}
	if n == 0 {
		a.out.Status("Nothing is held.")
		a.out.Hint("Keep a ZIM's release with: zimon hold NAME")
		return nil
	}
	t.Render(a.out.Out, a.out.Width())
	return nil
}

func (a *app) completeHeld(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return a.complete(cmd, func(ctx context.Context, be backend.Backend) []string {
		items, _ := be.Items(ctx)
		var out []string
		for _, it := range items {
			if c := it.Name + "\t" + it.Date; it.State == library.Held && !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
		return out
	})
}
