// Package ui renders zimon's terminal output: styles, tables, progress and prompts.
// Colour is downsampled (or dropped) automatically for pipes, NO_COLOR and --no-color.
package ui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"golang.org/x/term"

	"github.com/kazie/zimon/internal/config"
)

// Styles shared by commands. Basic ANSI colours follow the user's terminal theme.
var (
	Bold   = lipgloss.NewStyle().Bold(true)
	Dim    = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	Green  = lipgloss.NewStyle().Foreground(lipgloss.Green)
	Yellow = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	Red    = lipgloss.NewStyle().Foreground(lipgloss.Red)
	Cyan   = lipgloss.NewStyle().Foreground(lipgloss.Cyan)
	Header = lipgloss.NewStyle().Bold(true).Underline(true)
)

// Output is where a command writes. Out gets data (tables, details); Err gets
// status lines, progress and prompts, so piping stdout stays clean.
type Output struct {
	Out    io.Writer
	Err    io.Writer
	In     *bufio.Reader
	OutTTY bool // stdout is a terminal
	ErrTTY bool // stderr is a terminal: progress bars allowed
	InTTY  bool // stdin is a terminal: prompts allowed

	ctx context.Context
	ctl io.Writer // raw stderr, for cursor control that colour stripping must not remove
}

// New builds an Output for the process's stdio.
func New(noColor bool) *Output {
	out := colorprofile.NewWriter(os.Stdout, os.Environ())
	errw := colorprofile.NewWriter(os.Stderr, os.Environ())
	if noColor {
		out.Profile = colorprofile.NoTTY
		errw.Profile = colorprofile.NoTTY
	}
	return &Output{
		Out:    out,
		Err:    errw,
		In:     bufio.NewReader(os.Stdin),
		OutTTY: term.IsTerminal(int(os.Stdout.Fd())),
		ErrTTY: term.IsTerminal(int(os.Stderr.Fd())),
		InTTY:  term.IsTerminal(int(os.Stdin.Fd())),
		ctl:    os.Stderr,
	}
}

// clearLine returns the cursor to the start of the status line and erases it.
func (o *Output) clearLine() {
	w := o.ctl
	if w == nil {
		w = o.Err
	}
	fmt.Fprint(w, "\r\x1b[2K")
}

// Width is the terminal width for stdout (80 when unknown, unlimited when not a TTY).
func (o *Output) Width() int {
	if !o.OutTTY {
		return 0
	}
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	return 80
}

// Printf writes to stdout.
func (o *Output) Printf(format string, a ...any) { fmt.Fprintf(o.Out, format, a...) }

// Println writes to stdout.
func (o *Output) Println(a ...any) { fmt.Fprintln(o.Out, a...) }

// Status prints a status line to stderr.
func (o *Output) Status(format string, a ...any) {
	fmt.Fprintln(o.Err, fmt.Sprintf(format, a...))
}

// OK prefixes msg with a green ✓.
func OK(msg string) string { return Green.Render("✓") + " " + msg }

// Warning prefixes msg with a yellow !.
func Warning(msg string) string { return Yellow.Render("!") + " " + msg }

// Failure prefixes msg with a red ✗.
func Failure(msg string) string { return Red.Render("✗") + " " + msg }

// Success prints "✓ message".
func (o *Output) Success(format string, a ...any) { fmt.Fprintln(o.Err, OK(fmt.Sprintf(format, a...))) }

// Warn prints "! message".
func (o *Output) Warn(format string, a ...any) {
	fmt.Fprintln(o.Err, Warning(fmt.Sprintf(format, a...)))
}

// Fail prints "✗ message".
func (o *Output) Fail(format string, a ...any) {
	fmt.Fprintln(o.Err, Failure(fmt.Sprintf(format, a...)))
}

// Hint prints a dimmed hint line.
func (o *Output) Hint(format string, a ...any) {
	fmt.Fprintln(o.Err, Dim.Render(fmt.Sprintf(format, a...)))
}

// Interrupt makes prompts give up (with the context's error) when ctx is
// cancelled, e.g. by Ctrl-C. Without it, prompts wait for input forever.
func (o *Output) Interrupt(ctx context.Context) { o.ctx = ctx }

type line struct {
	text string
	err  error
}

// readLine reads one line from stdin, or returns early when the prompt is
// interrupted. The read runs on its own goroutine so Ctrl-C works mid-prompt;
// it ends with the line, so nothing is left reading stdin afterwards (which
// would steal keys from the browse screen). Only an interrupted prompt leaves
// it behind, and then zimon is exiting.
func (o *Output) readLine() (string, error) {
	ch := make(chan line, 1)
	go func() {
		text, err := o.In.ReadString('\n')
		ch <- line{strings.TrimSpace(text), err}
	}()
	var done <-chan struct{} // nil: never interrupted
	if o.ctx != nil {
		done = o.ctx.Done()
	}
	select {
	case l := <-ch:
		if l.err != nil && l.text == "" {
			fmt.Fprintln(o.Err)
			if errors.Is(l.err, io.EOF) {
				return "", context.Canceled // Ctrl-D: same as giving up
			}
			return "", l.err
		}
		return l.text, nil
	case <-done:
		fmt.Fprintln(o.Err)
		return "", o.ctx.Err()
	}
}

// Confirm asks a yes/no question on stderr. Without a TTY it returns def.
// It returns an error only when the prompt was interrupted.
func (o *Output) Confirm(question string, def bool) (bool, error) {
	if !o.InTTY {
		return def, nil
	}
	choices := "[y/N]"
	if def {
		choices = "[Y/n]"
	}
	fmt.Fprintf(o.Err, "%s %s ", Bold.Render(question), Dim.Render(choices))
	answer, err := o.readLine()
	if err != nil {
		return false, err
	}
	switch strings.ToLower(answer) {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return def, nil
	}
}

// Choose shows numbered options and returns the picked index, or -1 if the
// user gave up or there is no TTY.
func (o *Output) Choose(question string, options []string) (int, error) {
	if !o.InTTY || len(options) == 0 {
		return -1, nil
	}
	fmt.Fprintln(o.Err, Bold.Render(question))
	for i, opt := range options {
		fmt.Fprintf(o.Err, "  %s %s\n", Cyan.Render(fmt.Sprintf("%d)", i+1)), opt)
	}
	for {
		fmt.Fprintf(o.Err, "%s ", Dim.Render(fmt.Sprintf("Pick 1-%d (enter to cancel):", len(options))))
		answer, err := o.readLine()
		if err != nil {
			return -1, err
		}
		if answer == "" {
			return -1, nil
		}
		var n int
		if _, err := fmt.Sscanf(answer, "%d", &n); err == nil && n >= 1 && n <= len(options) {
			return n - 1, nil
		}
	}
}

// Ask prompts for a line of text, returning def when the answer is empty or there is no TTY.
func (o *Output) Ask(question, def string) (string, error) {
	if !o.InTTY {
		return def, nil
	}
	fmt.Fprintf(o.Err, "%s %s ", Bold.Render(question), Dim.Render("["+def+"]"))
	answer, err := o.readLine()
	if err != nil {
		return def, err
	}
	if answer == "" {
		return def, nil
	}
	return answer, nil
}

// AskSecret prompts for a secret, such as a token, without showing it. On a
// terminal it shows a dot per character; with only stdin a terminal it
// shows nothing. Without a terminal it reads a line from stdin, so a secret
// can be piped in.
func (o *Output) AskSecret(question string) (string, error) {
	var secret string
	var err error
	switch {
	case o.InTTY && o.ErrTTY:
		err = huh.NewForm(huh.NewGroup(huh.NewInput().Title(question).EchoMode(huh.EchoModePassword).Value(&secret))).
			WithTheme(huh.ThemeFunc(huh.ThemeBase16)).WithShowHelp(false).WithOutput(os.Stderr).Run()
		if errors.Is(err, huh.ErrUserAborted) {
			err = context.Canceled
		}
	case o.InTTY:
		fmt.Fprintf(o.Err, "%s ", Bold.Render(question))
		var b []byte
		b, err = term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(o.Err)
		secret = string(b)
	default:
		secret, err = o.In.ReadString('\n')
		if errors.Is(err, io.EOF) {
			err = nil
		}
	}
	return strings.TrimSpace(secret), err
}

func fraction(done, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return min(max(float64(done)/float64(total), 0), 1)
}

// Percent is done/total as 0-100, clamped.
func Percent(done, total int64) int { return int(fraction(done, total) * 100) }

// Bar renders a width-cell progress bar for done/total.
func Bar(width int, done, total int64) string {
	filled := int(fraction(done, total) * float64(width))
	return Cyan.Render(strings.Repeat("━", filled)) + Dim.Render(strings.Repeat("━", width-filled))
}

// Stats is the text next to a progress bar: " 42%  1.20 GB / 3.00 GB".
func Stats(done, total int64) string {
	return fmt.Sprintf(" %3d%%  %s / %s", Percent(done, total), config.FormatBytes(done), config.FormatBytes(total))
}
