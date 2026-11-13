// Package setup reads and edits library.toml, the file that says what the
// library should contain (collections at a level, plus hand-picked ZIMs), and
// works out from it which ZIMs are wanted.
package setup

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/collections"
	"github.com/kazie/zimon/internal/config"
)

// What update does with wanted ZIMs that are not on disk yet.
const (
	NewWithinBudget = "within-budget" // download them while the library stays within budget
	NewAsk          = "ask"           // only list them; `zimon apply` downloads them
	NewAlways       = "always"        // always download them (free space is still checked)
)

// Keywords for [items], besides a level name or a flavour.
const (
	On      = "on"      // want it, at the collection's or the standard flavour
	Off     = "off"     // skip it, even if a collection wants it
	Default = "default" // the variant without a flavour, when there are others too
)

// ChoiceKind is what an [items] value asks for.
type ChoiceKind int

// The kinds of choice: the keywords on, off and default, a level, or a flavour.
const (
	ChoiceOn ChoiceKind = iota
	ChoiceOff
	ChoiceDefault
	ChoiceLevel
	ChoiceFlavour
)

// Choice is a parsed [items] value.
type Choice struct {
	Kind    ChoiceKind
	Level   collections.Level // for ChoiceLevel
	Flavour string            // for ChoiceFlavour
}

// ParseChoice reads an [items] value: on, off, default, a level or a flavour.
func ParseChoice(s string) (Choice, error) {
	s = strings.TrimSpace(s)
	switch s {
	case "":
		return Choice{}, fmt.Errorf("give a flavour, a level (%s), %q, %q or %q", collections.LevelNames, On, Off, Default)
	case On:
		return Choice{Kind: ChoiceOn}, nil
	case Off:
		return Choice{Kind: ChoiceOff}, nil
	case Default:
		return Choice{Kind: ChoiceDefault}, nil
	}
	if l, ok := collections.ParseLevel(s); ok {
		return Choice{Kind: ChoiceLevel, Level: l}, nil
	}
	return Choice{Kind: ChoiceFlavour, Flavour: s}, nil
}

func (c Choice) String() string {
	switch c.Kind {
	case ChoiceOn:
		return On
	case ChoiceOff:
		return Off
	case ChoiceDefault:
		return Default
	case ChoiceLevel:
		return c.Level.String()
	}
	return c.Flavour
}

// Pick chooses the entry c asks for from a ZIM family. current is what the
// collections already chose (On keeps it). It returns nil for Off, and when
// the family lacks the asked-for variant.
func (c Choice) Pick(fam []catalog.Entry, current *catalog.Entry) *catalog.Entry {
	switch c.Kind {
	case ChoiceOff:
		return nil
	case ChoiceDefault:
		if i := slices.IndexFunc(fam, func(e catalog.Entry) bool { return e.Flavour == "" }); i >= 0 {
			return &fam[i]
		}
		return nil
	case ChoiceOn:
		if current != nil {
			return current
		}
		return collections.Pick(fam, collections.Standard, "")
	case ChoiceLevel:
		return collections.Pick(fam, c.Level, "")
	}
	return collections.Pick(fam, collections.Standard, c.Flavour)
}

// Setup is the parsed library.toml.
type Setup struct {
	Budget      int64  // bytes; 0 = no budget
	NewItems    string // NewWithinBudget, NewAsk or NewAlways
	Region      string // for {region} items, e.g. "sweden"
	Collections map[string]collections.Level
	Items       map[string]Choice
	Hold        map[string]string // ZIM name → a note, the date of the release kept: update and apply leave it as it is
	Sources     map[string]string // ZIM name → where it comes from instead of a catalog: a URL, or a file's absolute path
}

// CollectionIDs returns the subscribed collection ids, sorted.
func (s *Setup) CollectionIDs() []string { return slices.Sorted(maps.Keys(s.Collections)) }

// ItemNames returns the [items] names, sorted.
func (s *Setup) ItemNames() []string { return slices.Sorted(maps.Keys(s.Items)) }

// Held reports whether name is in [hold].
func (s *Setup) Held(name string) bool {
	_, ok := s.Hold[name]
	return ok
}

type rawSetup struct {
	Budget      config.ByteSize   `toml:"budget"`
	NewItems    string            `toml:"new_items"`
	Region      string            `toml:"region"`
	Collections map[string]string `toml:"collections"`
	Items       map[string]string `toml:"items"`
	Hold        map[string]string `toml:"hold"`
	Sources     map[string]string `toml:"sources"`
}

// CheckSource checks where a [sources] ZIM comes from: an http(s) URL, or a
// file's absolute path (file true), as UTF-8 for library.toml.
func CheckSource(from string) (file bool, err error) {
	switch file = !config.IsURL(from); {
	case !utf8.ValidString(from):
		return file, fmt.Errorf("%q: library.toml can only keep a UTF-8 name; rename the file", from)
	case file && !filepath.IsAbs(from):
		return file, fmt.Errorf("%s: give an http(s) URL or a file's absolute path", from)
	}
	return file, nil
}

// CheckSourceName checks the name of a ZIM from [sources]: what [items],
// commands and file names use. It is one word: letters, digits and . _ -
func CheckSourceName(name string) error {
	if !catalog.SafeName(name) {
		return errors.New("a ZIM name is letters, digits and . _ -, starting with a letter or digit (like mysite_en_all)")
	}
	return nil
}

// Parse reads library.toml content.
func Parse(data []byte) (*Setup, error) {
	var raw rawSetup
	if err := config.DecodeStrict(data, &raw); err != nil {
		return nil, err
	}
	s := &Setup{
		Budget:      int64(raw.Budget),
		NewItems:    raw.NewItems,
		Region:      strings.TrimSpace(raw.Region),
		Collections: map[string]collections.Level{},
		Items:       map[string]Choice{},
		Hold:        map[string]string{},
		Sources:     map[string]string{},
	}
	switch s.NewItems {
	case "":
		s.NewItems = NewWithinBudget
	case NewWithinBudget, NewAsk, NewAlways:
	default:
		return nil, fmt.Errorf("new_items = %q: use %q, %q or %q", s.NewItems, NewWithinBudget, NewAsk, NewAlways)
	}
	if s.Budget < 0 {
		return nil, errors.New("budget must not be negative")
	}
	for id, v := range raw.Collections {
		l, ok := collections.ParseLevel(v)
		if !ok {
			return nil, fmt.Errorf("[collections] %s = %q: use %s", id, v, collections.LevelNames)
		}
		s.Collections[id] = l
	}
	for name, v := range raw.Items {
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("[items]: an empty ZIM name")
		}
		c, err := ParseChoice(v)
		if err != nil {
			return nil, fmt.Errorf("[items] %q = %q: %w", name, v, err)
		}
		s.Items[name] = c
	}
	for name, v := range raw.Hold {
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("[hold]: an empty ZIM name")
		}
		s.Hold[name] = v
	}
	for name, v := range raw.Sources {
		if err := CheckSourceName(name); err != nil {
			return nil, fmt.Errorf("[sources] %q: %w", name, err)
		}
		if _, err := CheckSource(v); err != nil {
			return nil, fmt.Errorf("[sources] %q: %w", name, err)
		}
		s.Sources[name] = v
	}
	return s, nil
}
