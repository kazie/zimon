package ui

import (
	"bufio"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// testOutput is an Output writing to buffers, reading answers from input.
func testOutput(input string, tty bool) (*Output, *strings.Builder, *strings.Builder) {
	var out, errw strings.Builder
	return &Output{Out: &out, Err: &errw, In: bufio.NewReader(strings.NewReader(input)), InTTY: tty}, &out, &errw
}

func TestNew(t *testing.T) {
	for _, noColor := range []bool{false, true} {
		o := New(noColor)
		if o.Out == nil || o.Err == nil || o.In == nil {
			t.Fatalf("New(%v) left a stream nil", noColor)
		}
	}
}

func TestMessages(t *testing.T) {
	o, out, errw := testOutput("", false)
	o.Printf("%d items\n", 3)
	o.Println("data")
	o.Status("plain %s", "status")
	o.Success("done %d", 1)
	o.Warn("careful")
	o.Fail("broken: %v", errors.New("disk"))
	o.Hint("try %s", "this")
	if got, want := out.String(), "3 items\ndata\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	want := "plain status\n✓ done 1\n! careful\n✗ broken: disk\ntry this\n"
	if got := ansi.Strip(errw.String()); got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
	if o.Width() != 0 {
		t.Errorf("Width = %d without a terminal, want 0 (unlimited)", o.Width())
	}
}

func TestConfirm(t *testing.T) {
	for _, c := range []struct {
		input string
		tty   bool
		def   bool
		want  bool
	}{
		{"", false, true, true},
		{"", false, false, false},
		{"y\n", false, false, false}, // no terminal: never reads
		{"y\n", true, false, true},
		{"YES\n", true, false, true},
		{"n\n", true, true, false},
		{"\n", true, true, true},
		{"maybe\n", true, false, false},
	} {
		o, _, errw := testOutput(c.input, c.tty)
		got, err := o.Confirm("Go ahead?", c.def)
		if err != nil || got != c.want {
			t.Errorf("Confirm(%q, tty %v, default %v) = %v, %v; want %v", c.input, c.tty, c.def, got, err, c.want)
		}
		if c.tty {
			hint := "[y/N]"
			if c.def {
				hint = "[Y/n]"
			}
			if s := ansi.Strip(errw.String()); !strings.HasPrefix(s, "Go ahead? "+hint+" ") {
				t.Errorf("prompt = %q", s)
			}
		}
	}

	// End of input (Ctrl-D) is giving up.
	o, _, _ := testOutput("", true)
	if _, err := o.Confirm("Go ahead?", true); !errors.Is(err, context.Canceled) {
		t.Errorf("Confirm at EOF: err = %v, want context.Canceled", err)
	}
}

func TestChoose(t *testing.T) {
	opts := []string{"light", "standard", "deep"}
	for _, c := range []struct {
		input string
		tty   bool
		want  int
	}{
		{"2\n", false, -1},
		{"2\n", true, 1},
		{"0\nnine\n4\n3\n", true, 2}, // asks again until the answer is in range
		{"\n", true, -1},
	} {
		o, _, errw := testOutput(c.input, c.tty)
		got, err := o.Choose("How deep?", opts)
		if err != nil || got != c.want {
			t.Errorf("Choose(%q, tty %v) = %d, %v; want %d", c.input, c.tty, got, err, c.want)
		}
		if c.tty && !strings.Contains(ansi.Strip(errw.String()), "How deep?\n  1) light\n  2) standard\n  3) deep\nPick 1-3 (enter to cancel): ") {
			t.Errorf("menu = %q", ansi.Strip(errw.String()))
		}
	}
	o, _, _ := testOutput("1\n", true)
	if got, _ := o.Choose("Nothing?", nil); got != -1 {
		t.Errorf("Choose with no options = %d, want -1", got)
	}
}

func TestAsk(t *testing.T) {
	for _, c := range []struct {
		input string
		tty   bool
		want  string
	}{
		{"~/zim\n", false, "~/media"},
		{"~/zim\n", true, "~/zim"},
		{"  spaced  \n", true, "spaced"},
		{"\n", true, "~/media"},
	} {
		o, _, _ := testOutput(c.input, c.tty)
		if got, err := o.Ask("Where?", "~/media"); err != nil || got != c.want {
			t.Errorf("Ask(%q, tty %v) = %q, %v; want %q", c.input, c.tty, got, err, c.want)
		}
	}
}

// Without a terminal, AskSecret reads a piped line and prints no prompt.
func TestAskSecretPiped(t *testing.T) {
	for _, c := range []struct{ input, want string }{
		{"s3cret\n", "s3cret"},
		{"  s3cret \n", "s3cret"},
		{"s3cret", "s3cret"}, // no newline
		{"", ""},
		{"first\nsecond\n", "first"},
	} {
		o, out, errw := testOutput(c.input, false)
		got, err := o.AskSecret("Token?")
		if err != nil || got != c.want {
			t.Errorf("AskSecret(%q) = %q, %v; want %q", c.input, got, err, c.want)
		}
		if out.Len()+errw.Len() > 0 {
			t.Errorf("AskSecret(%q) printed %q %q", c.input, out, errw)
		}
	}
}

func TestPromptInterrupted(t *testing.T) {
	r, w := io.Pipe() // input that never comes
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	o := &Output{Out: io.Discard, Err: io.Discard, In: bufio.NewReader(r), InTTY: true}
	o.Interrupt(ctx)
	time.AfterFunc(10*time.Millisecond, cancel)
	if _, err := o.Ask("Where?", "here"); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// Without a terminal, progress is a plain line every 10%, for logs.
func TestProgressPlain(t *testing.T) {
	o, _, errw := testOutput("", false)
	p := o.NewProgress("devdocs_en_go", 1000, 0)
	for done := int64(50); done <= 1000; done += 50 {
		p.Set(done)
	}
	p.Note("verifying…")
	p.Finish()
	lines := strings.Split(strings.TrimSpace(errw.String()), "\n")
	if len(lines) != 11 {
		t.Fatalf("%d lines, want 11 (0%%, 10%%, ..., 100%%):\n%s", len(lines), errw.String())
	}
	if lines[0] != "devdocs_en_go: 0% (0 B / 1.00 kB)" || !strings.HasPrefix(lines[10], "devdocs_en_go: 100% (1.00 kB / 1.00 kB)") {
		t.Errorf("first and last lines: %q, %q", lines[0], lines[10])
	}

	// Resuming starts from what is already there, and notes show up.
	o, _, errw = testOutput("", false)
	p = o.NewProgress("x", 100, 40)
	p.Note("resuming")
	p.Set(100)
	if got := strings.TrimSpace(errw.String()); !strings.HasPrefix(got, "x: 40% (40 B / 100 B)\nx: 100% (100 B / 100 B)") || !strings.Contains(got, ", resuming") {
		t.Errorf("got %q", got)
	}
}

// On a terminal, progress is one line redrawn in place, cut to the width.
func TestProgressTTY(t *testing.T) {
	var errw, ctl strings.Builder
	o := &Output{Out: io.Discard, Err: &errw, ErrTTY: true, ctl: &ctl}
	lastLine := func() string {
		s := ansi.Strip(errw.String())
		return s[strings.LastIndex(s, "wikipedia_en_all:maxi"):]
	}
	p := o.NewProgress("wikipedia_en_all:maxi", 2_000_000, 0)
	p.sampleAt = time.Now().Add(-time.Second) // pretend a second went by, for a rate
	p.last = time.Time{}
	p.Set(1_000_000)
	line := lastLine()
	for _, want := range []string{"wikipedia_en_all:maxi ━", " 50%  1.00 MB / 2.00 MB  ", "B/s  0:01"} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q lacks %q", line, want)
		}
	}
	if w := ansi.StringWidth(line); w >= 80 {
		t.Errorf("line is %d wide, want under 80", w)
	}
	if !strings.Contains(ctl.String(), "\x1b[2K") {
		t.Error("the line is not erased before it is redrawn")
	}
	p.Note("verifying…")
	if line := lastLine(); !strings.HasSuffix(line, "  verifying…") {
		t.Errorf("line %q lacks the note", line)
	}
}

func TestStatsAndETA(t *testing.T) {
	if got := Stats(500, 1000); got != "  50%  500 B / 1.00 kB" {
		t.Errorf("Stats = %q", got)
	}
	for d, want := range map[time.Duration]string{
		0:                               "0:00",
		90 * time.Second:                "1:30",
		59*time.Minute + 59*time.Second: "59:59",
		2*time.Hour + 5*time.Minute:     "2h05m",
	} {
		if got := formatETA(d); got != want {
			t.Errorf("formatETA(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for path, want := range map[string]string{
		home:                                "~",
		filepath.Join(home, "zim", "a.zim"): "~/zim/a.zim",
		home + "other":                      home + "other",
		"/srv/zim":                          "/srv/zim",
	} {
		if got := Tilde(path); got != want {
			t.Errorf("Tilde(%q) = %q, want %q", path, got, want)
		}
	}
	t.Setenv("HOME", "/")
	if got := Tilde("/srv/zim"); got != "/srv/zim" {
		t.Errorf("with HOME=/: %q", got)
	}
}
