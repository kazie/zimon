package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
)

// The init form, answered in Huh's accessible mode: a line for the
// directory, then option numbers to toggle languages, and 0 to finish.
func TestInitForm(t *testing.T) {
	num := func(code string) string {
		i := slices.IndexFunc(initLanguages, func(l struct{ code, name string }) bool { return l.code == code })
		if i < 0 {
			t.Fatalf("%s is not offered", code)
		}
		return fmt.Sprint(i + 1)
	}
	cases := []struct {
		name      string
		langs     []string // the defaults
		input     []string // one answer per line
		wantDir   string
		wantLangs []string
	}{
		{"keep the languages", []string{"eng"}, []string{"~/zim", "0"}, "~/zim", []string{"eng"}},
		{"add one", []string{"eng"}, []string{"/srv/zim", num("swe"), "0"}, "/srv/zim", []string{"eng", "swe"}},
		{"none means all", []string{"eng"}, []string{"~/zim", num("eng"), "0"}, "~/zim", nil},
		{"an empty directory is asked again", []string{"eng"}, []string{" ", "~/zim", "0"}, "~/zim", []string{"eng"}},
		// A language the list lacks is offered after it, already picked.
		{"keep an unlisted one", []string{"nob"}, []string{"~/zim", "0"}, "~/zim", []string{"nob"}},
		{"drop an unlisted one", []string{"nob"}, []string{"~/zim", fmt.Sprint(len(initLanguages) + 1), "0"}, "~/zim", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, langs := "~/.local/share/zimon/library", slices.Clone(c.langs)
			// One byte at a time, so each prompt reads only its own line.
			in := iotest.OneByteReader(strings.NewReader(strings.Join(c.input, "\n") + "\n"))
			if err := initForm(&dir, &langs).WithAccessible(true).WithInput(in).WithOutput(io.Discard).Run(); err != nil {
				t.Fatal(err)
			}
			slices.Sort(langs)
			if dir != c.wantDir || !slices.Equal(langs, c.wantLangs) {
				t.Errorf("got %q %q, want %q %q", dir, langs, c.wantDir, c.wantLangs)
			}
		})
	}
}

func TestPickedLanguages(t *testing.T) {
	for _, c := range []struct {
		codes []string
		want  string
	}{
		{nil, "None picked: all languages. Others: zimon config edit."},
		{[]string{"eng"}, "Picked: English. Others: zimon config edit."},
		{[]string{"swe", "eng", "nob"}, "Picked: Swedish, English, nob. Others: zimon config edit."},
	} {
		if got := pickedLanguages(c.codes); got != c.want {
			t.Errorf("pickedLanguages(%q) = %q, want %q", c.codes, got, c.want)
		}
	}
}
