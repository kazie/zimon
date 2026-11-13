package ui

import (
	"slices"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/library"
	"github.com/kazie/zimon/internal/setup"
	"github.com/kazie/zimon/internal/store"
)

func TestSizes(t *testing.T) {
	for _, c := range []struct {
		n                 int64
		size, dash, freed string
	}{
		{0, "?", "–", ""},
		{-1, "?", "–", ""},
		{999, "999 B", "999 B", " (freed 999 B)"},
		{2_000_000, "2.00 MB", "2.00 MB", " (freed 2.00 MB)"},
		{14_386_799_616, "14.4 GB", "14.4 GB", " (freed 14.4 GB)"},
		{127_000_000_000, "127 GB", "127 GB", " (freed 127 GB)"},
	} {
		if got := Size(c.n); got != c.size {
			t.Errorf("Size(%d) = %q, want %q", c.n, got, c.size)
		}
		if got := ansi.Strip(SizeOrDash(c.n)); got != c.dash {
			t.Errorf("SizeOrDash(%d) = %q, want %q", c.n, got, c.dash)
		}
		if got := ansi.Strip(Freed(c.n)); got != c.freed {
			t.Errorf("Freed(%d) = %q, want %q", c.n, got, c.freed)
		}
	}
}

func TestPlural(t *testing.T) {
	for n, want := range map[int]string{0: "s", 1: "", 2: "s", 11: "s"} {
		if got := Plural(n); got != want {
			t.Errorf("Plural(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestNum(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 21673012: "21,673,012", 123456: "123,456", -123: "-123", -1234: "-1,234"} {
		if got := Num(n); got != want {
			t.Errorf("Num(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestRemoveQuestion(t *testing.T) {
	if got := RemoveQuestion("devdocs_en_go", 6_000_000); got != "Delete devdocs_en_go (6.00 MB)?" {
		t.Errorf("got %q", got)
	}
	if got := RemoveQuestion("devdocs_en_go", 0); got != "Remove devdocs_en_go from your library?" {
		t.Errorf("got %q", got)
	}
}

func TestPlanParts(t *testing.T) {
	add := library.Step{Kind: library.StepAdd, Bytes: 3_000_000}
	rm := library.Step{Kind: library.StepRemove, Freed: 2_000_000}
	for _, c := range []struct {
		steps []library.Step
		want  []string
	}{
		{nil, nil},
		{[]library.Step{add}, []string{"download about 3.00 MB"}},
		{[]library.Step{rm}, []string{"delete 2.00 MB"}},
		{[]library.Step{add, rm, add}, []string{"download about 6.00 MB", "delete 2.00 MB"}},
	} {
		pl := &library.Plan{Steps: c.steps}
		got := PlanParts(pl.Download(), pl.Freed())
		if !slices.Equal(got, c.want) {
			t.Errorf("PlanParts(%v) = %q, want %q", c.steps, got, c.want)
		}
	}
}

func TestStateText(t *testing.T) {
	latest := &catalog.Entry{Name: "devdocs_en_go", Date: "2026-11-12"}
	nopic := &catalog.Entry{Name: "wikivoyage_en_all", Flavour: "nopic"}
	reason := "a very long reason that goes on and on until it is far wider than the sixty cells allowed"
	for _, c := range []struct {
		name         string
		it           *library.Item
		marker, text string
	}{
		{"nil", nil, " ", ""},
		{"not in library", &library.Item{State: library.NotInLibrary}, " ", "not in library"},
		{"missing", &library.Item{State: library.Missing}, "○", "missing"},
		{"up to date", &library.Item{State: library.UpToDate}, "✓", "up to date"},
		{"update", &library.Item{State: library.UpdateAvailable, Entry: latest}, "↑", "update available → 2026-11-12"},
		{"gone", &library.Item{State: library.Orphaned}, "!", "gone upstream"},
		{"unwanted", &library.Item{State: library.Unwanted}, "×", "not wanted (zimon apply removes it)"},
		{"switch", &library.Item{State: library.Unwanted, Want: &setup.Want{Entry: nopic}}, "×",
			"not wanted (zimon apply switches to wikivoyage_en_all:nopic)"},
		{"downloading", &library.Item{State: library.Downloading, Download: &store.Download{Size: 200, VerifiedBytes: 50}}, "↓",
			"downloading 25%"},
		{"no size yet", &library.Item{State: library.Downloading, Download: &store.Download{}}, "↓", "downloading"},
		{"stopped", &library.Item{State: library.Downloading, Download: &store.Download{Size: 100, Error: reason + "\nsecond line"}}, "↓",
			"downloading 0% (stopped: " + ansi.Truncate(reason, 60, "…") + ")"},
	} {
		var it *api.Item // as the CLI and the API see it
		if c.it != nil {
			j := api.ItemOf(c.it, "")
			it = &j
		}
		if got := ansi.Strip(Marker(it)); got != c.marker {
			t.Errorf("%s: Marker = %q, want %q", c.name, got, c.marker)
		}
		if got := ansi.Strip(StateText(it)); got != c.text {
			t.Errorf("%s: StateText = %q, want %q", c.name, got, c.text)
		}
	}
}

func TestAgo(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Second: "moments", time.Minute: "1 minute", 90 * time.Minute: "1 hour",
		49 * time.Hour: "2 days",
	} {
		if got := Ago(d); got != want {
			t.Errorf("Ago(%v) = %q, want %q", d, got, want)
		}
	}
}
