package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestCell(t *testing.T) {
	for _, c := range []struct {
		s     string
		w     int
		right bool
		want  string
	}{
		{"abc", 5, false, "abc  "},
		{"abc", 5, true, "  abc"},
		{"abcdef", 4, false, "abc…"},
		{"ウィキ", 6, false, "ウィキ"},
		{"ウィキペディア", 6, false, "ウィ… "}, // a wide rune that does not fit leaves a gap
		{Bold.Render("abc"), 4, false, Bold.Render("abc") + " "},
	} {
		if got := Cell(c.s, c.w, c.right); got != c.want {
			t.Errorf("Cell(%q, %d, %v) = %q, want %q", c.s, c.w, c.right, got, c.want)
		}
	}
}

func render(t *testing.T, tb *Table, width int) []string {
	t.Helper()
	var b strings.Builder
	tb.Render(&b, width)
	return strings.Split(strings.TrimSuffix(ansi.Strip(b.String()), "\n"), "\n")
}

func TestTableRender(t *testing.T) {
	tb := NewTable("", "NAME", "TITLE", "SIZE", "DATE")
	tb.Flex = 2
	tb.Right = []int{3}
	tb.Add(Green.Render("✓"), Bold.Render("devdocs_en_go"), "Go Docs, the standard library reference", "6.00 MB", "2026-10-12")
	tb.Add(" ", "wikipedia_ja_top:maxi", "ウィキペディアのベスト", "4.22 GB", "2026-10-20")

	// Unlimited width: columns as wide as their widest cell, the last one not padded.
	want := []string{
		"   NAME                   TITLE                                       SIZE  DATE",
		"✓  devdocs_en_go          Go Docs, the standard library reference  6.00 MB  2026-10-12",
		"   wikipedia_ja_top:maxi  ウィキペディアのベスト                   4.22 GB  2026-10-20",
	}
	if got := render(t, tb, 0); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("unlimited:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// A narrower terminal shrinks only the flex column, with an ellipsis.
	for _, w := range []int{80, 70, 60} {
		lines := render(t, tb, w)
		for _, l := range lines {
			if lw := ansi.StringWidth(l); lw > w {
				t.Errorf("width %d: %d wide: %q", w, lw, l)
			}
			if !strings.HasSuffix(l, "DATE") && !strings.HasSuffix(l, "2026-10-12") && !strings.HasSuffix(l, "2026-10-20") {
				t.Errorf("width %d: last column cut: %q", w, l)
			}
		}
		if !strings.Contains(lines[1], "…") {
			t.Errorf("width %d: title not shortened: %q", w, lines[1])
		}
	}

	// The flex column never goes below MinFlex; then the line is allowed to overflow.
	lines := render(t, tb, 20)
	if !strings.Contains(lines[1], "  Go Docs, th…  6.00 MB") {
		t.Errorf("title not cut to MinFlex (%d) at width 20: %q", tb.MinFlex, lines[1])
	}
}

func TestTableWithoutFlex(t *testing.T) {
	tb := NewTable("FILE", "SIZE")
	tb.Right = []int{1}
	tb.Add("a-rather-long-file-name.zim", "1.00 MB")
	tb.Add(".partial/x.part", "9 B")
	lines := render(t, tb, 10) // no flex column: nothing is cut
	want := []string{
		"FILE                            SIZE",
		"a-rather-long-file-name.zim  1.00 MB",
		".partial/x.part                  9 B",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}
