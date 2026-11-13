package main

import (
	"strings"
	"testing"
)

func TestSummary(t *testing.T) {
	for _, c := range []struct {
		name, profile, want string
	}{
		{
			name: "duplicate blocks count once, covered if any run did",
			profile: `mode: atomic
example.org/m/a/a.go:1.1,2.2 3 0
example.org/m/a/a.go:3.1,4.2 1 2
example.org/m/a/a.go:1.1,2.2 3 1
example.org/m/b/b.go:1.1,2.2 4 0
example.org/m/a/a.go:5.1,6.2 4 0
`,
			want: "a      50.0%\nb       0.0%\ntotal  33.3%\n",
		},
		{
			name:    "empty",
			profile: "mode: set\n",
			want:    "total   0.0%\n",
		},
	} {
		pkgs, err := summarize(strings.NewReader(c.profile))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var b strings.Builder
		write(&b, pkgs, "example.org/m/")
		if b.String() != c.want {
			t.Errorf("%s:\ngot\n%s\nwant\n%s", c.name, b.String(), c.want)
		}
	}
}

func TestSummaryRejectsGarbage(t *testing.T) {
	for _, profile := range []string{"mode: set\nnot a profile\n", "a.go:1.1,2.2 x 1\n", "a.go 1 1\n"} {
		if _, err := summarize(strings.NewReader(profile)); err == nil {
			t.Errorf("no error for %q", profile)
		}
	}
}
