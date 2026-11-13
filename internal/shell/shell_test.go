package shell

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestQuote(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"kiwix-serve", "--port=8080", "/srv/my zims/library.xml", "it's"},
			`kiwix-serve --port=8080 '/srv/my zims/library.xml' 'it'\''s'`},
		{[]string{"zimon", "add", "@linux", "wikipedia_en_all:mini"}, "zimon add @linux wikipedia_en_all:mini"},
		{[]string{"search", "stack exchange", "", "$HOME", "a;b", "*"}, `search 'stack exchange' '' '$HOME' 'a;b' '*'`},
		{[]string{`a\b`, `end\`, "=ls", "x=1"}, `'a'\\'b' 'end'\\'' '=ls' x=1`},
		{nil, ""},
	} {
		if got := Quote(c.args); got != c.want {
			t.Errorf("Quote(%q)\n got %s\nwant %s", c.args, got, c.want)
		}
	}
}

// Real shells split the quoted line back into the same words: sh always, and
// bash, zsh and fish when they are installed.
func TestQuoteRoundTrip(t *testing.T) {
	args := []string{"plain", "two words", "", "it's", `"double"`, "$HOME", "`date`", "a\nb", "tab\there",
		"*", "-v", `back\slash`, `end\`, `\'`, "=ls", "x=1", "~", "%1", "(x)", "{a,b}"}
	scripts := map[string]string{
		"sh":   `for a in ` + Quote(args) + `; do printf '%s\0' "$a"; done`,
		"bash": `for a in ` + Quote(args) + `; do printf '%s\0' "$a"; done`,
		"zsh":  `for a in ` + Quote(args) + `; do printf '%s\0' "$a"; done`,
		"fish": `for a in ` + Quote(args) + `; printf '%s\0' "$a"; end`,
	}
	for name, script := range scripts {
		bin, err := exec.LookPath(name)
		if err != nil {
			if name == "sh" {
				t.Fatal("no sh")
			}
			continue
		}
		out, err := exec.Command(bin, "-c", script).Output()
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
		if !slices.Equal(got, args) {
			t.Errorf("%s split %s\ninto %q", name, Quote(args), got)
		}
	}
}
