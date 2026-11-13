package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/ui"
)

func newRefreshCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "refresh",
		Short: "Download the latest Kiwix catalog",
		Long: `Download the Kiwix catalog. zimon also does this on its own when the cached
copy is older than catalog_max_age (24h by default).`,
		Args:        cobra.NoArgs,
		Annotations: onRemote,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.be.Run(cmd.Context(), "refresh", api.Request{}, (&shower{a: a, catalog: true}).show)
		},
	}
}

type searchFlags struct {
	langs    []string
	allLangs bool
	category string
	flavour  string
	maxSize  string
	limit    int
}

func newSearchCmd(a *app) *cobra.Command {
	var f searchFlags
	cmd := &cobra.Command{
		Use:   "search [words...]",
		Short: "Search the Kiwix catalog",
		Long: `Search titles, descriptions, names and tags in the catalog. Every word must
match (as a prefix). Results use your default languages unless --lang or
--all-langs is given.

  ✓ installed   ↑ update available   ↓ downloading   ○ wanted, not downloaded`,
		Example: `  zimon search wikipedia
  zimon search stack exchange --max-size 1GB
  zimon search --category gutenberg --lang fra`,
		Annotations: onRemote,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := a.freshCatalog(ctx, false); err != nil {
				return err
			}
			info, err := a.be.Info(ctx)
			if err != nil {
				return err
			}
			q := api.Query{Text: strings.Join(args, " "), Languages: info.Languages, Category: f.category, Flavour: f.flavour, Limit: f.limit}
			if len(f.langs) > 0 {
				q.Languages = f.langs
			}
			if f.allLangs {
				q.Languages = nil
			}
			if f.maxSize != "" {
				n, err := config.ParseBytes(f.maxSize)
				if err != nil {
					return err
				}
				q.MaxSize = n
			}
			shown, total, err := a.be.Search(ctx, q)
			if err != nil {
				return err
			}
			if total == 0 {
				a.out.Status("No matches.")
				if len(q.Languages) > 0 {
					a.out.Hint("Searched %s only. Try --all-langs.", strings.Join(q.Languages, ", "))
				}
				return nil
			}
			renderEntries(a, shown)
			footer := fmt.Sprintf("%d result%s", total, ui.Plural(total))
			if len(shown) < total {
				footer += fmt.Sprintf(", showing %d (--limit 0 for all)", len(shown))
			}
			if len(q.Languages) > 0 {
				footer += " · languages: " + strings.Join(q.Languages, ", ")
			}
			footer += " · zimon info <name> for details"
			a.out.Hint("%s", footer)
			return nil
		},
	}
	cmd.Flags().StringSliceVarP(&f.langs, "lang", "l", nil, "language codes to include, e.g. eng,swe")
	cmd.Flags().BoolVarP(&f.allLangs, "all-langs", "A", false, "search every language")
	cmd.Flags().StringVarP(&f.category, "category", "c", "", "only this category (wikipedia, stack_exchange, gutenberg, ...)")
	cmd.Flags().StringVarP(&f.flavour, "flavour", "f", "", "only this flavour (maxi, nopic, mini, ...)")
	cmd.Flags().StringVar(&f.maxSize, "max-size", "", "only ZIMs up to this size, e.g. 500MB or 2GB")
	cmd.Flags().IntVarP(&f.limit, "limit", "n", 40, "show at most this many results (0 = all)")
	_ = cmd.RegisterFlagCompletionFunc("category", a.completeCategories)
	_ = cmd.RegisterFlagCompletionFunc("lang", a.completeLanguages)
	return cmd
}

func renderEntries(a *app, entries []api.Entry) {
	t := ui.NewTable("", "NAME", "TITLE", "LANG", "SIZE", "DATE")
	t.Flex = 2
	t.Right = []int{4}
	for _, e := range entries {
		t.Add(ui.StateMarker(e.State), ui.Bold.Render(e.Key), e.Title, e.Language, ui.Size(e.Size), e.Date)
	}
	t.Render(a.out.Out, a.out.Width())
}

func newInfoCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "info <name[:flavour]>",
		Short: "Show details and flavours of a ZIM",
		Example: `  zimon info wikipedia_en_all
  zimon info superuser.com_en_all`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: a.completeCatalogNames,
		Annotations:       onRemote,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := a.freshCatalog(ctx, false); err != nil {
				return err
			}
			all, picked, err := a.lookup(ctx, args[0])
			if err != nil {
				return err
			}
			_, named := catalog.SplitKey(args[0]) // highlight the flavour asked for
			list, err := a.be.Items(ctx)
			if err != nil {
				return err
			}
			items := api.ItemsByKey(list)
			e := all[0]
			if picked != nil {
				e = *picked
			}
			o := a.out
			o.Println(ui.Bold.Render(e.Title) + "  " + ui.Dim.Render(e.Name))
			if e.Summary != "" {
				o.Println(e.Summary)
			}
			o.Println()
			feats, tags := catalog.Features(e.Tags)
			row := func(label, value string) {
				if value != "" {
					o.Printf("  %s %s\n", ui.Dim.Render(fmt.Sprintf("%-10s", label)), value)
				}
			}
			row("Language", e.Language)
			row("Category", e.Category)
			row("Creator", e.Creator)
			row("Publisher", e.Publisher)
			row("Contains", strings.Join(feats, ", "))
			row("Tags", strings.Join(tags, ", "))
			o.Println()

			t := ui.NewTable("", "FLAVOUR", "SIZE", "DATE", "ARTICLES", "MEDIA", "STATUS")
			t.Right = []int{2, 4, 5}
			for _, fe := range all {
				flav := flavour(fe.Key) // "default" for none, as zimon level takes it
				if named && fe.Key == e.Key {
					flav = ui.Cyan.Render(flav)
				} else {
					flav = ui.Bold.Render(flav)
				}
				t.Add(ui.Marker(items[fe.Key]), flav, ui.Size(fe.Size), fe.Date, ui.Num(fe.ArticleCount), ui.Num(fe.MediaCount), ui.StateText(items[fe.Key]))
			}
			t.Render(o.Out, o.Width())
			o.Println()
			hint := e.Key
			if picked == nil {
				hint = e.Name + ":<flavour>"
			}
			a.out.Hint("Add it with: zimon add %s", hint)
			return nil
		},
	}
}
