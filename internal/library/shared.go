package library

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/setup"
	"github.com/kazie/zimon/internal/store"
)

// RefreshCatalog fetches the catalogs at urls into st, unless none has
// changed since the last fetch (ETags). It returns how many entries the
// cache holds and whether they changed. The first catalog (Kiwix's) must
// answer; another one that fails keeps what the cache had from it, and
// problems says why. A catalog that has not changed keeps its entries
// without being fetched again, though an entry it lost to another
// catalog's newer release of the same ZIM comes back only when it changes
// (or when a forgotten source clears the ETags: Store.KeepSources).
func RefreshCatalog(ctx context.Context, st *store.Store, hc *http.Client, urls []string) (count int, changed bool, problems []error, err error) {
	_, stored, count, err := st.CatalogState(ctx)
	if err != nil {
		return 0, false, nil, err
	}
	was, before := parseETags(stored)
	fresh := count == 0 || !slices.Equal(was, urls) // a new cache, or another set of catalogs
	etags := before
	if fresh {
		etags = map[string]string{} // fetch them all
	}
	results := make([]*catalog.Result, len(urls))
	errs := make([]error, len(urls))
	var wg sync.WaitGroup
	for i, u := range urls { // side by side: the wait is the slowest one's, not the sum
		wg.Go(func() {
			// Only Kiwix's catalog is never empty: another may well be.
			results[i], errs[i] = (&catalog.Client{HTTP: hc, BaseURL: u, MayBeEmpty: i > 0}).Fetch(ctx, etags[u])
		})
	}
	wg.Wait()
	unchanged := true
	for i, u := range urls {
		switch err := errs[i]; {
		case err != nil && (i == 0 || ctx.Err() != nil):
			if len(urls) > 1 {
				err = fmt.Errorf("%s: %w", u, err)
			}
			return count, false, problems, err
		case err != nil:
			problems = append(problems, fmt.Errorf("catalog %s: %w", u, err))
			results[i] = &catalog.Result{NotModified: true} // keep its entries; fetch it in full next time
		}
		unchanged = unchanged && results[i].NotModified
	}
	if unchanged {
		return count, false, problems, st.TouchCatalog(ctx)
	}
	var entries []catalog.Entry
	changed = fresh
	for i, u := range urls {
		got := results[i].Entries
		// Fetched in full, a catalog with the ETag it had is the same as before.
		changed = changed || !results[i].NotModified && (results[i].ETag == "" || results[i].ETag != before[u])
		if results[i].NotModified {
			if got, err = st.EntriesOfCatalog(ctx, u); err != nil {
				return count, false, problems, err
			}
		}
		for _, e := range got {
			e.Catalog = u
			entries = append(entries, e)
		}
		etags[u] = results[i].ETag
	}
	if err := st.ReplaceCatalog(ctx, entries, formatETags(etags, urls)); err != nil {
		return count, false, problems, err
	}
	_, _, count, err = st.CatalogState(ctx)
	return count, changed, problems, err
}

// The catalogs' ETags are kept as one string: a "URL\tETag" line for
// each catalog, in order.
func formatETags(etags map[string]string, urls []string) string {
	lines := make([]string, len(urls))
	for i, u := range urls {
		lines[i] = u + "\t" + etags[u]
	}
	return strings.Join(lines, "\n")
}

// parseETags reads formatETags: the catalogs, in order, and their ETags.
func parseETags(s string) (urls []string, etags map[string]string) {
	etags = map[string]string{}
	for line := range strings.SplitSeq(s, "\n") {
		if u, etag, ok := strings.Cut(line, "\t"); ok {
			urls = append(urls, u)
			etags[u] = etag
		}
	}
	return urls, etags
}

// CatalogCheck is what EnsureCatalog did.
type CatalogCheck struct {
	Refreshed bool      // it fetched the catalog (or found it unchanged)
	Count     int       // entries in the cache
	Changed   bool      // the fetch brought a new catalog
	Stale     error     // why the refresh failed; the cached copy is used
	Problems  []error   // catalogs other than the first that failed: what the cache had from them is kept
	Synced    time.Time // when the cached copy was fetched
}

// CatalogDue reports whether the cached catalog, fetched at synced with
// count entries, is due for a refresh: never fetched, or older than maxAge.
// (Forgetting a source clears synced, so that is due too.)
func CatalogDue(synced time.Time, count int, maxAge time.Duration) bool {
	return count == 0 || time.Since(synced) >= maxAge
}

// EnsureCatalog refreshes the catalog from cfg's catalogs when it is older
// than catalog_max_age, or always with force; refreshing, if set, hears
// just before it fetches. When the fetch fails but a cached copy exists, it
// carries on with the cache and says why in Stale; with no cache, that is
// an error.
func EnsureCatalog(ctx context.Context, st *store.Store, hc *http.Client, cfg *config.Config, force bool, refreshing func()) (CatalogCheck, error) {
	synced, _, count, err := st.CatalogState(ctx)
	if err != nil {
		return CatalogCheck{}, err
	}
	out := CatalogCheck{Count: count, Synced: synced}
	if !force && !CatalogDue(synced, count, cfg.CatalogMaxAge.Duration) {
		return out, nil
	}
	if refreshing != nil {
		refreshing()
	}
	n, changed, problems, err := RefreshCatalog(ctx, st, hc, cfg.CatalogURLs())
	out.Problems = problems
	if err != nil {
		if count == 0 || ctx.Err() != nil {
			return out, err
		}
		out.Stale = err
		return out, nil
	}
	out.Refreshed, out.Count, out.Changed = true, n, changed
	return out, nil
}

// CheckChoice checks an [items] choice for name (a flavour, on, off or
// default) against the catalog. Off needs no catalog entry: it also turns
// off a ZIM gone upstream.
func (m *Manager) CheckChoice(ctx context.Context, name, value string) error {
	c, err := setup.ParseChoice(value)
	if err != nil || c.Kind == setup.ChoiceOff {
		return err
	}
	fam, err := m.Store.EntriesByName(ctx, name)
	if err != nil {
		return err
	}
	if len(fam) == 0 {
		return fmt.Errorf("%s is not in the catalog (see: zimon search %s)", name, name)
	}
	if c.Pick(fam, nil) != nil {
		return nil
	}
	var there []string
	for _, e := range fam {
		there = append(there, cmp.Or(e.Flavour, setup.Default)+" "+config.Size(e.ApproxSize))
	}
	return fmt.Errorf("%s has no %q variant; there is: %s", name, value, strings.Join(there, ", "))
}

// RemoveTargets builds the change that removes targets as people write
// them: "@collection" (which library.toml must have), "name" for every
// flavour of it here, or "name:flavour".
func (m *Manager) RemoveTargets(ctx context.Context, targets []string, keepFiles bool) (Change, error) {
	s, err := m.LoadSetup(ctx)
	if err != nil {
		return Change{}, err
	}
	var ids, names []string
	for _, t := range targets {
		if id, ok := strings.CutPrefix(t, "@"); ok {
			if _, ok := s.Collections[id]; !ok {
				return Change{}, fmt.Errorf("@%s is not in library.toml (see: zimon collections)", id)
			}
			ids = append(ids, id)
		} else {
			names = append(names, t)
		}
	}
	keys, err := m.LocalKeys(ctx, names)
	if err != nil {
		return Change{}, err
	}
	return m.RemoveChange(ctx, ids, keys, keepFiles)
}

// LocalKeys turns names as people write them into the keys zimon knows
// locally (wanted, on disk or downloading): "name:flavour" is that key, a
// bare name every flavour of it.
func (m *Manager) LocalKeys(ctx context.Context, args []string) ([]catalog.Key, error) {
	if len(args) == 0 {
		return nil, nil
	}
	items, err := m.Items(ctx)
	if err != nil {
		return nil, err
	}
	sorted := SortedItems(items)
	var keys []catalog.Key
	for _, arg := range args {
		k, exact := catalog.SplitKey(arg)
		found := false
		for _, it := range sorted {
			if it.Key.Name == k.Name && (!exact || it.Key == k) {
				keys, found = append(keys, it.Key), true
			}
		}
		if !found {
			return nil, fmt.Errorf("%s is not in your library (see: zimon list)", arg)
		}
	}
	return keys, nil
}
