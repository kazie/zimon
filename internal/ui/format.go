package ui

import (
	"strconv"
	"strings"
)

// Num formats 1234567 as 1,234,567.
func Num(n int64) string {
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	if n < 0 {
		b.WriteByte('-')
		s = s[1:]
	}
	pre := len(s) % 3
	b.WriteString(s[:pre])
	for i := pre; i < len(s); i += 3 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
