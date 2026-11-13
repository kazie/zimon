package setup

import (
	"errors"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/kazie/zimon/internal/collections"
)

const fuzzSeedDoc = `# my library, in git
budget = "100GB"   # for now
region = 'sweden'

[collections]
# the things I care about
linux = "light"   # bump later
"anime-manga" = "deep"

[items]
"wikipedia_en_all" = 'mini'
"stackoverflow.com_en_all" = "on"   # yes, all of it
`

// Names zimon itself writes: collection ids and ZIM names.
var saneName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// FuzzDocEdits checks the line editor behind add, level, remove and budget.
// On any input it changes only the lines it must. On a valid library.toml,
// with the names and values zimon writes, the result is valid too and says
// exactly what the edit asked for, and nothing else changes meaning.
func FuzzDocEdits(f *testing.F) {
	f.Add(Template, []byte{0, 2, 4, 1, 3}, "linux", "maxi")
	f.Add(fuzzSeedDoc, []byte{0, 5, 2, 3, 9, 1}, "linux", "nopic")
	f.Add(fuzzSeedDoc, []byte{3, 2, 14}, "wikipedia_en_all", "deep")
	f.Add("", []byte{4, 0, 2}, "a", "on")
	f.Add("budget = \"1GB\"\n[items]\n", []byte{19, 2}, "x_en_all", "off")
	f.Add("items.x = \"on\"\n", []byte{2}, "y", "on")
	f.Add("region = \"\"\"\n[items]\n\"\"\"\n", []byte{2}, "y", "on")
	f.Fuzz(func(t *testing.T, text string, ops []byte, key, value string) {
		d := newDoc("library.toml", text)
		before, err := d.Setup()
		valid := err == nil
		flat := newDoc("library.toml", text).editable()
		sane := saneName.MatchString(key)
		choice := "on"
		if saneName.MatchString(value) {
			choice = value
		}
		for _, op := range ops[:min(len(ops), 8)] {
			old := slices.Clone(d.lines)
			expect := edit(d, op, key, choice)
			checkLines(t, old, d.lines)
			if !flat {
				// Refused: nothing changed, and Setup (so Save) says why.
				if !slices.Equal(old, d.lines) {
					t.Fatalf("edited a file it cannot edit safely:\n%s", d.Bytes())
				}
				if _, err := d.Setup(); !errors.Is(err, ErrNotFlat) {
					t.Fatalf("refused edit not reported: %v", err)
				}
				continue
			}
			if !valid || !sane {
				continue
			}
			after, err := d.Setup()
			if err != nil {
				t.Fatalf("edit %d (%q, %q) broke a valid file: %v\nbefore:\n%s\nafter:\n%s",
					op, key, choice, err, joinLines(old), d.Bytes())
			}
			expect(t, before, after)
			before = after
		}
	})
}

// FuzzParse: any input parses or fails cleanly, and what parses keeps its
// meaning when written back (choices and levels round-trip).
func FuzzParse(f *testing.F) {
	f.Add(Template)
	f.Add(fuzzSeedDoc)
	f.Add("new_items = \"ask\"\nbudget = \"1.5GiB\"\n[items]\n\"a\" = \"default\"\n\"b\" = \" deep \"\n")
	f.Fuzz(func(t *testing.T, text string) {
		s, err := Parse([]byte(text))
		if err != nil {
			return
		}
		if s.Budget < 0 || !slices.Contains([]string{NewWithinBudget, NewAsk, NewAlways}, s.NewItems) {
			t.Fatalf("parsed %+v", s)
		}
		for name, c := range s.Items {
			if back, err := ParseChoice(c.String()); err != nil || back != c {
				t.Fatalf("%s: choice %+v round-trips to %+v, %v", name, c, back, err)
			}
		}
	})
}

// edit applies op and returns a check that after is before plus the edit.
func edit(d *Doc, op byte, key, choice string) func(t *testing.T, before, after *Setup) {
	switch op % 5 {
	case 0:
		l := collections.Levels[int(op/5)%len(collections.Levels)]
		d.SetCollection(key, l)
		return func(t *testing.T, before, after *Setup) {
			want := maps.Clone(before.Collections)
			want[key] = l
			sameSetup(t, after, before, want, before.Items, before.Budget)
		}
	case 1:
		d.DeleteCollection(key)
		return func(t *testing.T, before, after *Setup) {
			want := maps.Clone(before.Collections)
			delete(want, key)
			sameSetup(t, after, before, want, before.Items, before.Budget)
		}
	case 2:
		d.SetItem(key, choice)
		return func(t *testing.T, before, after *Setup) {
			want := maps.Clone(before.Items)
			want[key], _ = ParseChoice(choice)
			sameSetup(t, after, before, before.Collections, want, before.Budget)
		}
	case 3:
		d.DeleteItem(key)
		return func(t *testing.T, before, after *Setup) {
			want := maps.Clone(before.Items)
			delete(want, key)
			sameSetup(t, after, before, before.Collections, want, before.Budget)
		}
	default:
		budget := []int64{0, 250e9, 1536 << 20, 123456789}[int(op/5)%4]
		d.SetBudget(budget)
		return func(t *testing.T, before, after *Setup) {
			sameSetup(t, after, before, before.Collections, before.Items, budget)
		}
	}
}

func sameSetup(t *testing.T, got, before *Setup, cols map[string]collections.Level, items map[string]Choice, budget int64) {
	t.Helper()
	if !maps.Equal(got.Collections, cols) || !maps.Equal(got.Items, items) || got.Budget != budget ||
		got.Region != before.Region || got.NewItems != before.NewItems {
		t.Fatalf("got %+v\nwant collections %v, items %v, budget %d (before: %+v)", got, cols, items, budget, before)
	}
}

// checkLines: an edit changes one line in place, inserts up to three (a
// table header, a blank line and the key), or deletes one. Every other line
// stays as it was.
func checkLines(t *testing.T, old, cur []string) {
	t.Helper()
	ok := false
	switch n := len(cur) - len(old); {
	case n == 0:
		diff := 0
		for i := range old {
			if old[i] != cur[i] {
				diff++
			}
		}
		ok = diff <= 1
	case n > 0 && n <= 3:
		ok = subsequence(old, cur)
	case n == -1:
		ok = subsequence(cur, old)
	}
	if !ok {
		t.Fatalf("lines changed too much:\nbefore:\n%s\nafter:\n%s", joinLines(old), joinLines(cur))
	}
}

func subsequence(short, long []string) bool {
	i := 0
	for _, l := range long {
		if i < len(short) && short[i] == l {
			i++
		}
	}
	return i == len(short)
}

func joinLines(lines []string) string { return strings.Join(lines, "\n") + "\n" }
