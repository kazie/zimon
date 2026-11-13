package cli

import (
	"strings"

	"charm.land/lipgloss/v2"
)

func joinStyled(items []string, style lipgloss.Style) string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = style.Render(s)
	}
	return strings.Join(out, ", ")
}
