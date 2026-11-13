package cli

import (
	"testing"
	"time"
)

func TestFixManPage(t *testing.T) {
	date := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct{ in, want string }{
		{`.TH ZIMON 1 "2031-05-06" "zimon" "A friendly manager"`, `.TH ZIMON 1 "2026-11-09" "zimon" "A friendly manager"`},
		{`\fB--y --yes\fP`, `\fB-y, --yes\fP`},
		{`\fB--v --verbose\fP`, `\fB-v, --verbose\fP`},
		{`\fB--config\fP`, `\fB--config\fP`},
		{`\fB--no-color\fP`, `\fB--no-color\fP`},
		{"keep 2031-05-06 in text", "keep 2031-05-06 in text"},
	} {
		if got := fixManPage(c.in, date); got != c.want {
			t.Errorf("fixManPage(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNoColorFlag(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{"--no-color", "list"}, true},
		{[]string{"list", "--no-color"}, true},
		{[]string{"list"}, false},
		{[]string{"remote", "add", "--", "--no-color"}, false},
	} {
		if got := noColorFlag(c.args); got != c.want {
			t.Errorf("noColorFlag(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}
