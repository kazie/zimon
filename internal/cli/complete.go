package cli

import (
	"cmp"
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/backend"
	"github.com/kazie/zimon/internal/setup"
	"github.com/kazie/zimon/internal/ui"
)

// Shell completion runs without the usual setup. It asks an https://
// remote's API (giving up after a few seconds), or the library here, never
// creating a database and never fetching the catalog.

// complete runs list against the library completion asks about.
func (a *app) complete(cmd *cobra.Command, list func(ctx context.Context, be backend.Backend) []string) ([]string, cobra.ShellCompDirective) {
	return completeOn(cmd, a.completionBackend(), list)
}

// completeOn runs list against be (nil: nothing to ask), giving up after a
// few seconds.
func completeOn(cmd *cobra.Command, be backend.Backend, list func(ctx context.Context, be backend.Backend) []string) ([]string, cobra.ShellCompDirective) {
	if be == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := context.WithTimeout(cmp.Or(cmd.Context(), context.Background()), 5*time.Second)
	defer cancel()
	return list(ctx, be), cobra.ShellCompDirectiveNoFileComp
}

// completionBackend is the remote's API, or the library here if it has a
// database; nil when there is nothing to ask.
func (a *app) completionBackend() backend.Backend {
	if a.cfg == nil && a.loadConfig() != nil {
		return nil
	}
	if a.remote != nil {
		be, err := a.remoteBackend()
		if err != nil {
			return nil
		}
		return be
	}
	if _, err := os.Stat(a.cfg.DBPath()); err != nil {
		return nil // never create one
	}
	return a.local() // its reads never go online
}

func (a *app) completeLocalKeys(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return a.complete(cmd, localKeys)
}

func localKeys(ctx context.Context, be backend.Backend) []string {
	items, _ := be.Items(ctx)
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Key + "\t" + it.State.String()
	}
	return out
}

// collectionIDs completes "@id" with titles; only yours when mine.
func collectionIDs(ctx context.Context, be backend.Backend, mine bool) []string {
	cols, _ := be.Collections(ctx)
	var out []string
	for _, c := range cols {
		if c.Mine || !mine {
			out = append(out, "@"+c.ID+"\t"+c.Title)
		}
	}
	return out
}

func (a *app) completeCollectionArg(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return a.complete(cmd, func(ctx context.Context, be backend.Backend) []string { return collectionIDs(ctx, be, false) })
}

func (a *app) completeAddArgs(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return a.complete(cmd, func(ctx context.Context, be backend.Backend) []string {
		if strings.HasPrefix(toComplete, "@") {
			return collectionIDs(ctx, be, false)
		}
		names := catalogNames(ctx, be, toComplete)
		if toComplete == "" {
			names = append(collectionIDs(ctx, be, false), names...)
		}
		return names
	})
}

func (a *app) completeRemoveArgs(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return a.complete(cmd, func(ctx context.Context, be backend.Backend) []string {
		return append(collectionIDs(ctx, be, true), localKeys(ctx, be)...)
	})
}

func (a *app) completeLevelArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		return a.completeAddArgs(cmd, args, toComplete)
	case 1:
		out := levelNames()
		if strings.HasPrefix(args[0], "@") {
			return out, cobra.ShellCompDirectiveNoFileComp
		}
		out = append(out, setup.On, setup.Off)
		flavours, _ := a.complete(cmd, func(ctx context.Context, be backend.Backend) []string {
			fam, _, _ := be.Entries(ctx, args[0])
			var out []string
			for _, e := range fam {
				out = append(out, cmp.Or(e.Flavour, setup.Default)+"\t"+ui.Size(e.Size))
			}
			return out
		})
		return append(out, flavours...), cobra.ShellCompDirectiveNoFileComp
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

func (a *app) completeCatalogNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return a.complete(cmd, func(ctx context.Context, be backend.Backend) []string { return catalogNames(ctx, be, toComplete) })
}

// catalogNames completes catalog names starting with prefix (any, for ""),
// in the library's languages.
func catalogNames(ctx context.Context, be backend.Backend, prefix string) []string {
	info, err := be.Info(ctx)
	if err != nil {
		return nil
	}
	entries, _, _ := be.Search(ctx, api.Query{Prefix: prefix, Languages: info.Languages, Limit: 500, NoState: true})
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Key + "\t" + e.Title
	}
	return out
}

func (a *app) completeCategories(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return a.complete(cmd, func(ctx context.Context, be backend.Backend) []string {
		fs, _ := be.Facets(ctx, "categories")
		out := make([]string, len(fs))
		for i, f := range fs {
			out[i] = f.Value
		}
		return out
	})
}

func (a *app) completeLanguages(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return a.complete(cmd, func(ctx context.Context, be backend.Backend) []string {
		fs, _ := be.Facets(ctx, "languages")
		out := make([]string, len(fs))
		for i, f := range fs {
			out[i] = f.Value + "\t" + strconv.Itoa(f.Count) + " entries"
		}
		return out
	})
}
