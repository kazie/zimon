package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/kazie/zimon/internal/tui"
)

func newBrowseCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "browse",
		Short: "Browse the catalog interactively and pick ZIMs",
		Long: `Browse the Kiwix catalog in an interactive screen.

  ↑/↓ or j/k   move              /      filter as you type
  tab          next category     l      toggle your languages / all
  a or enter   download          d      delete from your library
  u            update            c      collections
  ?            every key         q      quit

On a download still waiting its turn, d cancels it instead.

In the collections view, 1, 2 and 3 set a collection to light, standard or
deep (after showing what that downloads and deletes), and x removes it.

Downloads run in the background while you keep browsing. If you quit early
they resume the next time you add, update or apply. On an https:// remote
they are jobs there, and carry on.`,
		Args:        cobra.NoArgs,
		Annotations: onRemote,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !a.out.InTTY || !a.out.OutTTY {
				return errors.New("browse needs an interactive terminal; try 'zimon search' instead")
			}
			ctx := cmd.Context()
			if err := a.freshCatalog(ctx, false); err != nil {
				return err
			}
			d := tui.Deps{Backend: a.be}
			if a.remote != nil {
				d.Remote = a.remote.Name
			}
			return tui.Run(ctx, d)
		},
	}
}
