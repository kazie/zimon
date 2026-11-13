package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestBarClamps(t *testing.T) {
	for _, c := range []struct {
		done, total int64
		pc          int
	}{{0, 100, 0}, {50, 100, 50}, {100, 100, 100}, {150, 100, 100}, {-5, 100, 0}, {10, 0, 0}} {
		bar, pc := Bar(20, c.done, c.total), Percent(c.done, c.total)
		if pc != c.pc || ansi.StringWidth(bar) != 20 {
			t.Errorf("Bar(20, %d, %d) = width %d, %d%%; want 20, %d%%", c.done, c.total, ansi.StringWidth(bar), pc, c.pc)
		}
	}
}

func TestProgressOverrunDoesNotPanic(_ *testing.T) {
	var buf strings.Builder
	o := &Output{Out: &buf, Err: &buf, ErrTTY: true}
	p := o.NewProgress("x", 100, 0)
	p.Set(250) // the file on disk grew past its recorded size
	p.Finish()
}

func TestClearLineSurvivesNoColor(t *testing.T) {
	var text, ctl strings.Builder
	o := &Output{Out: &text, Err: &text, ErrTTY: true, ctl: &ctl}
	p := o.NewProgress("x", 100, 0)
	p.Finish()
	if !strings.Contains(ctl.String(), "\x1b[2K") {
		t.Errorf("the erase-line code must go to the raw terminal; got %q", ctl.String())
	}
}
