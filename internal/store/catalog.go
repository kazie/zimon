package store

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/store/gen"
)

const (
	kvCatalogETag   = "catalog_etag"
	kvCatalogSynced = "catalog_synced_at"
)

// ReplaceCatalog swaps the whole cached catalog in one transaction. It keeps
// one entry per name+flavour (the newest; the first of equals), which the
// rest of zimon relies on. Releases of [sources] stay, and win: a name with
// a source gets no catalog entries, and an entry of a release a source has
// (by UUID) is left out.
func (s *Store) ReplaceCatalog(ctx context.Context, entries []catalog.Entry, etag string) error {
	latest := make(map[catalog.Key]catalog.Entry, len(entries))
	var keys []catalog.Key // in the order given, so the first catalog's entry wins a clash of UUIDs
	for _, e := range entries {
		cur, ok := latest[e.Key()]
		if !ok {
			keys = append(keys, e.Key())
		}
		if !ok || e.Date > cur.Date {
			e.Source = ""
			latest[e.Key()] = e
		}
	}
	return s.tx(ctx, func(q *gen.Queries) error {
		if err := q.DeleteCatalog(ctx); err != nil {
			return err
		}
		names, err := q.EntryNames(ctx) // what is left: sources' releases
		if err != nil {
			return err
		}
		sourced := map[string]bool{}
		for _, n := range names {
			sourced[n] = true
		}
		for _, k := range keys {
			if e := latest[k]; !sourced[e.Name] {
				if err := q.InsertCatalogEntry(ctx, gen.InsertCatalogEntryParams(e)); err != nil {
					return err
				}
			}
		}
		if err := q.RebuildIndex(ctx); err != nil {
			return err
		}
		if err := q.SetKV(ctx, gen.SetKVParams{Key: kvCatalogETag, Value: etag}); err != nil {
			return err
		}
		return q.SetKV(ctx, gen.SetKVParams{Key: kvCatalogSynced, Value: now()})
	})
}

// entry is a row as a catalog.Entry.
func entry(r gen.CatalogEntry) catalog.Entry {
	return catalog.Entry{UUID: r.UUID, Name: r.Name, Flavour: r.Flavour, Title: r.Title, Summary: r.Summary,
		Language: r.Language, Category: r.Category, Tags: r.Tags, ArticleCount: r.ArticleCount,
		MediaCount: r.MediaCount, Creator: r.Creator, Publisher: r.Publisher, Date: r.Date, Meta4URL: r.Meta4URL,
		ApproxSize: r.ApproxSize, Source: r.Source, Catalog: r.Catalog}
}

// TouchCatalog records that the catalog was checked and found unchanged.
func (s *Store) TouchCatalog(ctx context.Context) error { return s.Set(ctx, kvCatalogSynced, now()) }

// CatalogState says when the catalog was last synced, with which ETag, and
// how many entries it has (sources' releases left out).
func (s *Store) CatalogState(ctx context.Context) (synced time.Time, etag string, count int, err error) {
	v, err := s.Get(ctx, kvCatalogSynced)
	if err != nil {
		return
	}
	synced = parseTime(v)
	if etag, err = s.Get(ctx, kvCatalogETag); err != nil {
		return
	}
	n, err := s.q.CountCatalog(ctx)
	return synced, etag, int(n), err
}

// Query filters catalog searches. Zero values mean "no filter".
type Query struct {
	Text      string
	AnyWord   bool     // match entries with any word of Text instead of all
	Languages []string // match any
	Category  string
	Flavour   string
	MaxSize   int64
	Limit     int
}

// Search returns catalog entries matching q, best matches first.
func (s *Store) Search(ctx context.Context, q Query) ([]catalog.Entry, error) {
	limit := int64(q.Limit)
	if limit <= 0 {
		limit = -1 // no limit
	}
	langs := jsonList(q.Languages)
	if match := ftsQuery(q.Text, q.AnyWord); match != "" {
		return each(entry)(s.q.SearchText(ctx, gen.SearchTextParams{Match: match, Langs: langs, Category: q.Category,
			Flavour: q.Flavour, MaxSize: q.MaxSize, Lim: limit}))
	}
	return each(entry)(s.q.SearchAll(ctx, gen.SearchAllParams{Langs: langs, Category: q.Category, Flavour: q.Flavour,
		MaxSize: q.MaxSize, Lim: limit}))
}

// EntriesOfCatalog returns the entries that came from the catalog at url.
func (s *Store) EntriesOfCatalog(ctx context.Context, url string) ([]catalog.Entry, error) {
	return each(entry)(s.q.EntriesOfCatalog(ctx, url))
}

// EntryByUUID returns the entry of the release uuid, or nil.
func (s *Store) EntryByUUID(ctx context.Context, uuid string) (*catalog.Entry, error) {
	return one(each(entry)(s.q.EntryByUUID(ctx, uuid)))
}

// EntriesByName returns all flavours of a name, ordered by flavour.
func (s *Store) EntriesByName(ctx context.Context, name string) ([]catalog.Entry, error) {
	return each(entry)(s.q.EntriesByName(ctx, name))
}

// Families returns every flavour of each name, by name. Names not in the
// catalog are absent.
func (s *Store) Families(ctx context.Context, names []string) (map[string][]catalog.Entry, error) {
	out := make(map[string][]catalog.Entry, len(names))
	if len(names) == 0 {
		return out, nil
	}
	es, err := each(entry)(s.q.Families(ctx, jsonList(names)))
	for _, e := range es {
		out[e.Name] = append(out[e.Name], e)
	}
	return out, err
}

// EntriesByKeys returns the catalog entries for keys. Keys not in the catalog are absent.
func (s *Store) EntriesByKeys(ctx context.Context, keys []catalog.Key) (map[catalog.Key]catalog.Entry, error) {
	out := make(map[catalog.Key]catalog.Entry, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	pairs := make([][2]string, len(keys))
	for i, k := range keys {
		pairs[i] = [2]string{k.Name, k.Flavour}
	}
	es, err := each(entry)(s.q.EntriesByKeys(ctx, jsonList(pairs)))
	for _, e := range es {
		out[e.Key()] = e
	}
	return out, err
}

// EntriesWithPrefix returns entries whose "name[:flavour]" starts with prefix,
// for shell completion.
func (s *Store) EntriesWithPrefix(ctx context.Context, prefix string, langs []string, limit int) ([]catalog.Entry, error) {
	return each(entry)(s.q.EntriesWithPrefix(ctx, gen.EntriesWithPrefixParams{Prefix: prefix, Langs: jsonList(langs),
		Lim: int64(limit)}))
}

// SimilarNames suggests names for a typo: candidates sharing any word with
// name (from the FTS index), closest spelling first.
func (s *Store) SimilarNames(ctx context.Context, name string, limit int) ([]string, error) {
	es, err := s.Search(ctx, Query{Text: name, AnyWord: true, Limit: 500})
	if err != nil {
		return nil, err
	}
	dist := map[string]int{} // also the set of names seen
	var names []string
	for _, e := range es {
		if _, ok := dist[e.Name]; !ok {
			dist[e.Name] = editDistance(name, e.Name)
			names = append(names, e.Name)
		}
	}
	slices.SortStableFunc(names, func(a, b string) int { return cmp.Compare(dist[a], dist[b]) })
	return names[:min(limit, len(names))], nil
}

// editDistance is the Levenshtein distance between a and b.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// Facet is a value with how many catalog entries have it.
type Facet struct {
	Value string
	Count int
}

// Categories lists catalog categories by size.
func (s *Store) Categories(ctx context.Context) ([]Facet, error) {
	return each(func(r gen.CategoriesRow) Facet { return Facet{r.Category, int(r.N)} })(s.q.Categories(ctx))
}

// Languages lists catalog languages by size. Multi-language entries count once per language.
func (s *Store) Languages(ctx context.Context) ([]Facet, error) {
	langs, err := s.q.EntryLanguages(ctx)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, l := range langs {
		for code := range strings.SplitSeq(l, ",") {
			if code = strings.TrimSpace(code); code != "" {
				counts[code]++
			}
		}
	}
	out := make([]Facet, 0, len(counts))
	for v, c := range counts {
		out = append(out, Facet{v, c})
	}
	sortFacets(out)
	return out, nil
}

// ftsQuery turns free text into an FTS5 query: every word (or at
// least one, with anyWord) must match as a prefix. Words of one or two letters ("en", "go")
// match whole tokens only, unless they are all there is, so "wikipedia en"
// does not match "encyclopedia".
func ftsQuery(text string, anyWord bool) string {
	words := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	var parts []string
	for _, w := range words {
		if utf8.RuneCountInString(w) <= 2 && len(words) > 1 {
			parts = append(parts, `"`+w+`"`)
		} else {
			parts = append(parts, `"`+w+`"*`)
		}
	}
	if anyWord {
		return strings.Join(parts, " OR ")
	}
	return strings.Join(parts, " AND ")
}

func sortFacets(fs []Facet) {
	slices.SortFunc(fs, func(a, b Facet) int {
		if c := cmp.Compare(b.Count, a.Count); c != 0 {
			return c
		}
		return cmp.Compare(a.Value, b.Value)
	})
}
