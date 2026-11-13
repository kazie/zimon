package setup

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/collections"
)

// ViaItems marks a ZIM picked by hand in [items].
const ViaItems = "items"

// Families looks up every flavour of each name in the catalog.
type Families interface {
	Families(ctx context.Context, names []string) (map[string][]catalog.Entry, error)
}

// Want is one ZIM the setup asks for.
type Want struct {
	Entry *catalog.Entry // the release and flavour to have; nil when gone from the catalog
	Via   []string       // collection ids, and ViaItems when hand-picked
	Held  bool           // in [hold]: the release on disk stays, whatever the catalog has
}

// Size is the approximate download size, 0 when unknown.
func (w *Want) Size() int64 {
	if w.Entry == nil {
		return 0
	}
	return w.Entry.ApproxSize
}

// Wanted is everything a setup asks for. Names gone from the catalog are
// wanted too (Entry nil): whatever copy is on disk stays.
type Wanted struct {
	Names    map[string]*Want // by ZIM name: one flavour per name
	Problems []Problem
}

// Total is the approximate size of everything wanted.
func (w *Wanted) Total() int64 {
	var n int64
	for _, want := range w.Names {
		n += want.Size()
	}
	return n
}

// ProblemKind says what is wrong with part of a setup.
type ProblemKind int

// The problems a setup can have.
const (
	UnknownCollection ProblemKind = iota // Collection is not a collection
	NotInCatalog                         // Name (of Collection, or hand-picked) is not in the catalog
	NoSuchFlavour                        // Name has no Flavour ("" means no variant without a flavour)
	NeedsRegion                          // Collection has a {region} item and no region is set
	SourceUnchecked                      // Name comes from Source ([sources]), which zimon could not check yet
)

// Problem is something a setup asks for that cannot be had.
type Problem struct {
	Kind       ProblemKind
	Collection string // "" for [items]
	Name       string
	Flavour    string
	Region     string // the region a {region} item was looked up with
	Source     string // SourceUnchecked: where [sources] says it comes from
}

func (p Problem) String() string {
	prefix := ""
	if p.Collection != "" {
		prefix = p.Collection + ": "
	}
	switch p.Kind {
	case UnknownCollection:
		return fmt.Sprintf("unknown collection %q (see: zimon collections)", p.Collection)
	case SourceUnchecked:
		return fmt.Sprintf("%s comes from %s, which zimon could not check (see: zimon refresh)", p.Name, p.Source)
	case NotInCatalog:
		if p.Region != "" {
			return fmt.Sprintf("%s%s is not in the Kiwix catalog: is region %q right?", prefix, p.Name, p.Region)
		}
		return prefix + p.Name + " is not in the Kiwix catalog right now"
	case NoSuchFlavour:
		if p.Flavour == "" {
			return prefix + p.Name + " has no variant without a flavour"
		}
		return fmt.Sprintf("%s%s has no %q flavour", prefix, p.Name, p.Flavour)
	default:
		return prefix + "set region in library.toml to include " + p.Name
	}
}

// Summarize renders problems for people, folding several missing items of
// one collection into a single line.
func Summarize(ps []Problem) []string {
	var out []string
	gone := map[string][]string{}
	var order []string
	for _, p := range ps {
		if p.Kind == NotInCatalog && p.Collection != "" && p.Region == "" {
			if gone[p.Collection] == nil {
				order = append(order, p.Collection)
			}
			gone[p.Collection] = append(gone[p.Collection], p.Name)
			continue
		}
		out = append(out, p.String())
	}
	for _, c := range order {
		names := gone[c]
		if len(names) == 1 {
			out = append(out, Problem{Kind: NotInCatalog, Collection: c, Name: names[0]}.String())
			continue
		}
		list := names
		if len(list) > 4 {
			list = append(slices.Clone(list[:4]), "…")
		}
		out = append(out, fmt.Sprintf("%s: %d items are not in the Kiwix catalog right now (%s)", c, len(names), strings.Join(list, ", ")))
	}
	return out
}

// Resolver works out what a setup wants. It remembers catalog lookups, so
// use one per operation (the catalog does not change in between).
type Resolver struct {
	Collections []collections.Collection
	Languages   []string // ISO 639-3, from the config
	Catalog     Families

	cache map[string][]catalog.Entry // nil entry: not in the catalog
}

// families looks names up, asking the catalog only for new ones.
func (r *Resolver) families(ctx context.Context, names []string) (map[string][]catalog.Entry, error) {
	if r.cache == nil {
		r.cache = map[string][]catalog.Entry{}
	}
	var missing []string
	for _, n := range names {
		if _, ok := r.cache[n]; !ok {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		got, err := r.Catalog.Families(ctx, missing)
		if err != nil {
			return nil, err
		}
		for _, n := range missing {
			r.cache[n] = got[n]
		}
	}
	out := make(map[string][]catalog.Entry, len(names))
	for _, n := range names {
		if fam := r.cache[n]; len(fam) > 0 {
			out[n] = fam
		}
	}
	return out, nil
}

// Expanded is one collection item resolved against the catalog.
type Expanded struct {
	Item        collections.Item
	Name        string
	Entry       *catalog.Entry // nil when Gone, NeedsRegion, or the item's flavour is missing
	Gone        bool           // not in the catalog
	Variant     bool           // a {lang} variant (missing ones are normal: there is no ifixit_sv_all)
	NeedsRegion bool           // a {region} item while no region is set
}

// Expand resolves c's items at level (every tier, giants too, when all is
// set) against the catalog.
func (r *Resolver) Expand(ctx context.Context, c *collections.Collection, level collections.Level, region string, all bool) ([]Expanded, error) {
	fams, err := r.families(ctx, r.candidates([]*collections.Collection{c}, nil, region))
	if err != nil {
		return nil, err
	}
	return r.expand(c, level, region, all, fams), nil
}

func (r *Resolver) expand(c *collections.Collection, level collections.Level, region string, all bool, fams map[string][]catalog.Entry) []Expanded {
	langs := collections.LangCodes(r.Languages)
	var out []Expanded
	for _, it := range c.Items {
		if !all && !level.Includes(it.Tier) {
			continue
		}
		if strings.Contains(it.Name, "{region}") && region == "" {
			out = append(out, Expanded{Item: it, Name: it.Name, NeedsRegion: true})
			continue
		}
		names := it.Names(langs, region)
		variant := strings.Contains(it.Name, "{lang}")
		present := slices.DeleteFunc(slices.Clone(names), func(n string) bool { return len(fams[n]) == 0 })
		if fb := it.Fallback(region); len(present) == 0 && fb != "" && len(fams[fb]) > 0 {
			present = []string{fb}
		}
		for _, n := range present {
			out = append(out, Expanded{Item: it, Name: n, Entry: collections.Pick(fams[n], level, it.Flavour), Variant: variant})
		}
		for _, n := range names {
			if !slices.Contains(present, n) {
				out = append(out, Expanded{Item: it, Name: n, Gone: true, Variant: variant})
			}
		}
	}
	return out
}

// candidates lists every name a set of collections and items may need.
func (r *Resolver) candidates(cols []*collections.Collection, items []string, region string) []string {
	names := append(collections.Candidates(cols, r.Languages, region), items...)
	slices.Sort(names)
	return slices.Compact(names)
}

// Resolve works out what s wants.
func (r *Resolver) Resolve(ctx context.Context, s *Setup) (*Wanted, error) {
	w := &Wanted{Names: map[string]*Want{}}
	var cols []*collections.Collection
	for _, id := range s.CollectionIDs() {
		c := collections.Find(r.Collections, id)
		if c == nil {
			w.Problems = append(w.Problems, Problem{Kind: UnknownCollection, Collection: id})
			continue
		}
		cols = append(cols, c)
	}
	fams, err := r.families(ctx, r.candidates(cols, s.ItemNames(), s.Region))
	if err != nil {
		return nil, err
	}
	// A source's release counts only while [sources] still says it comes
	// from there: one from a place since changed is not planned for.
	for name, fam := range fams {
		if len(fam) > 0 && fam[0].Source != "" && fam[0].Source != s.Sources[name] {
			delete(fams, name)
		}
	}

	for _, c := range cols {
		for _, x := range r.expand(c, s.Collections[c.ID], s.Region, false, fams) {
			switch {
			case x.NeedsRegion:
				w.Problems = append(w.Problems, Problem{Kind: NeedsRegion, Collection: c.ID, Name: x.Name})
			case x.Gone:
				// Wanted all the same, so a copy on disk stays. A missing
				// language variant is normal and not worth a word.
				w.merge(x.Name, nil, c.ID)
				if !x.Variant {
					pr := Problem{Kind: NotInCatalog, Collection: c.ID, Name: x.Name}
					if strings.Contains(x.Item.Name, "{region}") {
						pr.Region = s.Region
					}
					w.Problems = append(w.Problems, pr)
				}
			case x.Entry == nil:
				// The flavour the collection names is gone upstream: as for a
				// gone ZIM, a copy on disk stays.
				w.merge(x.Name, nil, c.ID)
				w.Problems = append(w.Problems, Problem{Kind: NoSuchFlavour, Collection: c.ID, Name: x.Name, Flavour: x.Item.Flavour})
			default:
				w.merge(x.Name, x.Entry, c.ID)
			}
		}
	}

	for _, name := range s.ItemNames() {
		choice := s.Items[name]
		if choice.Kind == ChoiceOff {
			delete(w.Names, name)
			continue
		}
		fam := fams[name]
		if len(fam) == 0 {
			w.merge(name, nil, ViaItems) // asked for by name: any copy on disk stays
			pr := Problem{Kind: NotInCatalog, Name: name}
			if src := s.Sources[name]; src != "" {
				pr = Problem{Kind: SourceUnchecked, Name: name, Source: src}
			}
			w.Problems = append(w.Problems, pr)
			continue
		}
		var current *catalog.Entry
		if want := w.Names[name]; want != nil {
			current = want.Entry
		}
		e := choice.Pick(fam, current)
		if e == nil {
			w.Problems = append(w.Problems, Problem{Kind: NoSuchFlavour, Name: name, Flavour: choice.Flavour})
			continue
		}
		w.merge(name, nil, ViaItems)
		w.Names[name].Entry = e // [items] decides, even over a deeper collection choice
	}
	for name := range s.Hold {
		if want := w.Names[name]; want != nil {
			want.Held = true
		}
	}
	return w, nil
}

// merge records that via wants e (nil: gone upstream). When two sources
// want different flavours of one name, the bigger (deeper) one wins.
func (w *Wanted) merge(name string, e *catalog.Entry, via string) {
	want := w.Names[name]
	if want == nil {
		want = &Want{Entry: e}
		w.Names[name] = want
	} else if e != nil && (want.Entry == nil || e.ApproxSize > want.Entry.ApproxSize) {
		want.Entry = e
	}
	if !slices.Contains(want.Via, via) {
		want.Via = append(want.Via, via)
	}
}

// Sizes is the approximate size of each collection at each level (indexed
// by Level), by id.
func (r *Resolver) Sizes(ctx context.Context, cols []collections.Collection, region string) (map[string][3]int64, error) {
	ptrs := make([]*collections.Collection, len(cols))
	for i := range cols {
		ptrs[i] = &cols[i]
	}
	fams, err := r.families(ctx, r.candidates(ptrs, nil, region))
	if err != nil {
		return nil, err
	}
	out := map[string][3]int64{}
	for _, c := range ptrs {
		var sizes [3]int64
		for _, l := range collections.Levels {
			seen := map[string]bool{}
			for _, x := range r.expand(c, l, region, false, fams) {
				if x.Entry != nil && !seen[x.Name] {
					seen[x.Name] = true
					sizes[l] += x.Entry.ApproxSize
				}
			}
		}
		out[c.ID] = sizes
	}
	return out, nil
}
