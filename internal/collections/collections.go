// Package collections holds curated sets of ZIMs ("linux", "repair", ...)
// with depth levels: light, standard and deep. The built-in sets ship inside
// zimon; users can add their own in collections.toml.
package collections

import (
	_ "embed"
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"

	"golang.org/x/text/language"

	"github.com/pelletier/go-toml/v2"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/config"
)

// Level is how deep a collection goes.
type Level int

// The levels, shallowest first (see Blurb).
const (
	Light Level = iota
	Standard
	Deep
)

// Levels lists every level, shallowest first.
var Levels = []Level{Light, Standard, Deep}

func (l Level) String() string {
	switch l {
	case Light:
		return "light"
	case Deep:
		return "deep"
	default:
		return "standard"
	}
}

// Blurb says what a level brings, in a few words.
func (l Level) Blurb() string {
	switch l {
	case Light:
		return "the core items, smallest versions"
	case Deep:
		return "everything except the giants, with pictures"
	default:
		return "core and more, text-only where there is a choice"
	}
}

// LevelNames is "light, standard or deep", for messages.
const LevelNames = "light, standard or deep"

// ParseLevel reads "light", "standard" or "deep".
func ParseLevel(s string) (Level, bool) {
	for _, l := range Levels {
		if s == l.String() {
			return l, true
		}
	}
	return Standard, false
}

// Tier says from which level on an item is included. Giants are never
// included by a level; each one is an explicit opt-in.
type Tier int

// The tiers: Core from light on, More from standard on, DeepTier at deep,
// and Giant never by level.
const (
	Core Tier = iota
	More
	DeepTier
	Giant
)

func (t Tier) String() string {
	return [...]string{"core", "more", "deep", "giant"}[t]
}

// Includes reports whether level l takes items of tier t.
func (l Level) Includes(t Tier) bool { return t != Giant && int(t) <= int(l) }

// Item is one entry of a collection.
type Item struct {
	// Name is a catalog name, or a pattern with {lang} (each configured
	// language's two-letter code) or {region} (the setup's region).
	Name    string
	Note    string
	Flavour string   // fixed flavour for every level (optional)
	Except  []string // language codes {lang} skips, e.g. "en" where English is a giant
	Tier    Tier
}

// Collection is a curated set of ZIMs.
type Collection struct {
	ID    string
	Title string
	Blurb string
	Items []Item
}

// Find returns the collection with id, or nil.
func Find(cols []Collection, id string) *Collection {
	for i := range cols {
		if cols[i].ID == id {
			return &cols[i]
		}
	}
	return nil
}

// Get returns the collection with id, or an error that lists them all.
func Get(cols []Collection, id string) (*Collection, error) {
	if c := Find(cols, id); c != nil {
		return c, nil
	}
	ids := make([]string, len(cols))
	for i, c := range cols {
		ids[i] = "@" + c.ID
	}
	return nil, fmt.Errorf("no collection %q; there are: %s", id, strings.Join(ids, " "))
}

// Names expands the item's name for langs (two-letter codes) and region.
// A {lang} item with no candidate in langs falls back to English, so the
// caller should try the returned names in order and keep those that exist.
func (it Item) Names(langs []string, region string) []string {
	name := it.Name
	if strings.Contains(name, "{region}") {
		if region == "" {
			return nil
		}
		name = strings.ReplaceAll(name, "{region}", region)
	}
	if !strings.Contains(name, "{lang}") {
		return []string{name}
	}
	var out []string
	for _, l := range langs {
		if !slices.Contains(it.Except, l) {
			out = append(out, strings.ReplaceAll(name, "{lang}", l))
		}
	}
	return out
}

// Fallback is the English name of a {lang} item, used when none of the
// configured languages has one. It is "" for other items or when English is
// excluded.
func (it Item) Fallback(region string) string {
	if !strings.Contains(it.Name, "{lang}") || slices.Contains(it.Except, "en") {
		return ""
	}
	names := Item{Name: it.Name}.Names([]string{"en"}, region)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// Candidates lists every ZIM name cols may ask for in langs (ISO 639-3)
// and region, fallbacks included, sorted and without repeats.
func Candidates(cols []*Collection, langs []string, region string) []string {
	codes := LangCodes(langs)
	var names []string
	for _, c := range cols {
		for _, it := range c.Items {
			names = append(names, it.Names(codes, region)...)
			if fb := it.Fallback(region); fb != "" {
				names = append(names, fb)
			}
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// LangCodes turns ISO 639-3 codes from the config ("eng", "swe") into the
// two-letter codes ZIM names use ("en", "sv"). No languages means English.
func LangCodes(iso3 []string) []string {
	var out []string
	for _, l := range iso3 {
		b, err := language.ParseBase(l)
		if err != nil {
			continue
		}
		if code := b.String(); !slices.Contains(out, code) {
			out = append(out, code)
		}
	}
	if len(out) == 0 {
		out = []string{"en"}
	}
	return out
}

// Pick chooses the flavour of a ZIM family (all flavours of one name) for
// level: light prefers mini, standard nopic and deep maxi, each falling back
// to the smallest (light, standard) or biggest (deep) there is. A fixed
// flavour wins; nil means the family lacks it.
func Pick(family []catalog.Entry, level Level, fixed string) *catalog.Entry {
	if len(family) == 0 {
		return nil
	}
	find := func(flavour string) *catalog.Entry {
		for i := range family {
			if family[i].Flavour == flavour {
				return &family[i]
			}
		}
		return nil
	}
	if fixed != "" {
		return find(fixed)
	}
	var prefer []string
	switch level {
	case Light:
		prefer = []string{"mini", "nopic"}
	case Standard:
		prefer = []string{"nopic"}
	case Deep:
		prefer = []string{"maxi"}
	}
	for _, f := range prefer {
		if e := find(f); e != nil {
			return e
		}
	}
	// By size, where a size not known (0) never wins: the budget couldn't
	// count it.
	size := func(e *catalog.Entry) int64 {
		if e.ApproxSize <= 0 {
			return math.MaxInt64
		}
		return e.ApproxSize
	}
	best := &family[0]
	for i := range family {
		e := &family[i]
		if (level == Deep && e.ApproxSize > best.ApproxSize) || (level != Deep && size(e) < size(best)) {
			best = e
		}
	}
	return best
}

//go:embed builtin.toml
var builtinTOML []byte

// Builtin returns the collections that ship with zimon. Callers must not
// change the result; it is parsed once and shared.
var Builtin = sync.OnceValue(func() []Collection {
	cols, err := parse(builtinTOML, false)
	if err != nil {
		panic("collections/builtin.toml: " + err.Error()) // caught by the tests
	}
	return cols
})

// Load returns the built-in collections plus the user's from path (a
// missing file is fine). A user collection with a built-in id replaces it.
func Load(path string) ([]Collection, error) {
	cols := slices.Clone(Builtin())
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cols, nil
	}
	if err != nil {
		return nil, err
	}
	user, err := parse(data, true)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, u := range user {
		if c := Find(cols, u.ID); c != nil {
			*c = u
		} else {
			cols = append(cols, u)
		}
	}
	return cols, nil
}

type fileItem struct {
	Name    string   `toml:"name"`
	Note    string   `toml:"note"`
	Flavour string   `toml:"flavour"`
	Except  []string `toml:"except"`
}

type fileCollection struct {
	ID    string     `toml:"id"`
	Title string     `toml:"title"`
	Blurb string     `toml:"blurb"`
	Core  []fileItem `toml:"core"`
	More  []fileItem `toml:"more"`
	Deep  []fileItem `toml:"deep"`
	Giant []fileItem `toml:"giant"`
}

var (
	idRe         = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	nameRe       = regexp.MustCompile(`^[A-Za-z0-9._{}-]+$`)
	placeholders = strings.NewReplacer("{lang}", "", "{region}", "")
)

// parse reads collections, strictly (unknown keys, keys in another case)
// for a user's file; the built-in one is checked by the tests instead, and
// every zimon reads it.
func parse(data []byte, strict bool) ([]Collection, error) {
	var f struct {
		Collection []fileCollection `toml:"collection"`
	}
	decode := toml.Unmarshal
	if strict {
		decode = config.DecodeStrict
	}
	if err := decode(data, &f); err != nil {
		return nil, err
	}
	var out []Collection
	seen := map[string]bool{}
	for _, fc := range f.Collection {
		if !idRe.MatchString(fc.ID) {
			return nil, fmt.Errorf("collection id %q: use lowercase letters, digits and dashes", fc.ID)
		}
		if seen[fc.ID] {
			return nil, fmt.Errorf("collection %q is defined twice", fc.ID)
		}
		seen[fc.ID] = true
		c := Collection{ID: fc.ID, Title: fc.Title, Blurb: fc.Blurb}
		for tier, items := range [][]fileItem{fc.Core, fc.More, fc.Deep, fc.Giant} {
			for _, fi := range items {
				rest := placeholders.Replace(fi.Name)
				if !nameRe.MatchString(fi.Name) || strings.ContainsAny(rest, "{}") {
					return nil, fmt.Errorf("collection %q: bad item name %q", fc.ID, fi.Name)
				}
				c.Items = append(c.Items, Item{Name: fi.Name, Note: fi.Note, Flavour: fi.Flavour, Except: fi.Except, Tier: Tier(tier)})
			}
		}
		if c.Title == "" {
			c.Title = c.ID
		}
		out = append(out, c)
	}
	return out, nil
}
