// Command checkcollections checks every built-in collection item against the
// live Kiwix catalog and exits non-zero when one is missing, so a rename at
// Kiwix shows up in CI instead of as a silently shorter collection.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/collections"
	"github.com/kazie/zimon/internal/config"
)

// Languages tried for {lang} items: the item is fine if any of them exists.
var probeLangs = []string{"en", "fr", "de", "es", "sv", "ja"}

func main() { os.Exit(run()) }

// run does the check and returns the exit code: 0 when every item is in the
// catalog, 1 when some are missing, 2 when the catalog could not be fetched.
func run() int {
	url := flag.String("catalog", config.DefaultCatalogURL, "OPDS catalog root")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	res, err := (&catalog.Client{HTTP: http.DefaultClient, BaseURL: *url}).Fetch(ctx, "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "fetch catalog:", err)
		return 2
	}
	families := map[string][]catalog.Entry{}
	for _, e := range res.Entries {
		families[e.Name] = append(families[e.Name], e)
	}

	var missing []string
	items := 0
	for _, c := range collections.Builtin() {
		for _, it := range c.Items {
			items++
			names := it.Names(probeLangs, "sweden")
			if fb := it.Fallback("sweden"); fb != "" {
				names = append(names, fb)
			}
			found := slices.ContainsFunc(names, func(n string) bool {
				return collections.Pick(families[n], collections.Standard, it.Flavour) != nil
			})
			if !found {
				missing = append(missing, fmt.Sprintf("%s: %s", c.ID, catalog.Key{Name: it.Name, Flavour: it.Flavour}))
			}
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "%d of %d collection items are not in the catalog:\n  %s\n", len(missing), items, strings.Join(missing, "\n  "))
		return 1
	}
	fmt.Printf("All %d collection items are in the catalog (%d entries).\n", items, len(res.Entries))
	return 0
}
