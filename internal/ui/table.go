package ui

import (
	"io"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Table is a simple aligned table without borders. One column can be marked
// flexible: it shrinks (with an ellipsis) to fit the terminal width.
type Table struct {
	Headers []string
	Rows    [][]string
	Flex    int   // index of the column that shrinks; -1 for none
	Right   []int // right-aligned columns
	MinFlex int
}

// Cell fits s into exactly w cells: truncated with "…", padded on the right
// (or on the left when right-aligned).
func Cell(s string, w int, right bool) string {
	s = ansi.Truncate(s, w, "…")
	pad := strings.Repeat(" ", max(0, w-ansi.StringWidth(s)))
	if right {
		return pad + s
	}
	return s + pad
}

// NewTable starts a table with the given headers.
func NewTable(headers ...string) *Table {
	return &Table{Headers: headers, Flex: -1, MinFlex: 12}
}

// Add appends a row. Cells may contain ANSI styling.
func (t *Table) Add(cells ...string) { t.Rows = append(t.Rows, cells) }

// Render writes the table, fitting it into width (0 = no limit).
func (t *Table) Render(w io.Writer, width int) {
	const gap = 2
	widths := make([]int, len(t.Headers))
	for i, h := range t.Headers {
		widths[i] = ansi.StringWidth(h)
	}
	for _, r := range t.Rows {
		for i, c := range r {
			if i < len(widths) {
				widths[i] = max(widths[i], ansi.StringWidth(c))
			}
		}
	}
	if width > 0 && t.Flex >= 0 && t.Flex < len(widths) {
		total := gap * (len(widths) - 1)
		for _, cw := range widths {
			total += cw
		}
		if over := total - width; over > 0 {
			widths[t.Flex] = max(t.MinFlex, widths[t.Flex]-over)
		}
	}
	right := map[int]bool{}
	for _, i := range t.Right {
		right[i] = true
	}
	line := func(cells []string, style *lipgloss.Style) string {
		var b strings.Builder
		for i := range widths {
			c := ""
			if i < len(cells) {
				c = cells[i]
			}
			if style != nil {
				c = style.Render(ansi.Truncate(c, widths[i], "…"))
			}
			if i == len(widths)-1 && !right[i] {
				b.WriteString(ansi.Truncate(c, widths[i], "…"))
			} else {
				b.WriteString(Cell(c, widths[i], right[i]))
			}
			if i < len(widths)-1 {
				b.WriteString(strings.Repeat(" ", gap))
			}
		}
		return strings.TrimRight(b.String(), " ") + "\n"
	}
	_, _ = io.WriteString(w, line(t.Headers, &Header))
	for _, r := range t.Rows {
		_, _ = io.WriteString(w, line(r, nil))
	}
}
