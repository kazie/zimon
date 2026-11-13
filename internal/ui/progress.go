package ui

import (
	"fmt"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/kazie/zimon/internal/config"
)

// Progress draws a single updating progress line on a TTY, or prints a plain
// line every 10% (or 30 s) otherwise, which suits systemd journals.
type Progress struct {
	o      *Output
	label  string
	total  int64
	mu     sync.Mutex
	last   time.Time
	lastPc int
	// rate estimate
	sampleAt    time.Time
	sampleBytes int64
	rate        float64
	done        int64
	note        string
}

// NewProgress starts a progress display for label with total bytes.
func (o *Output) NewProgress(label string, total, already int64) *Progress {
	p := &Progress{o: o, label: label, total: total, done: already,
		sampleAt: time.Now(), sampleBytes: already, lastPc: -1}
	p.draw(true)
	return p
}

// Set updates the byte count.
func (p *Progress) Set(done int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done = done
	p.draw(false)
}

// Note shows a short message next to the bar (e.g. "verifying", "mirror: se").
func (p *Progress) Note(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.note = s
	if p.o.ErrTTY {
		p.draw(true)
	}
}

// Finish clears the bar.
func (p *Progress) Finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.o.ErrTTY {
		p.o.clearLine()
	}
}

func (p *Progress) draw(force bool) {
	now := time.Now()
	if dt := now.Sub(p.sampleAt).Seconds(); dt >= 0.5 {
		inst := float64(p.done-p.sampleBytes) / dt
		if p.rate == 0 {
			p.rate = inst
		} else {
			p.rate = 0.7*p.rate + 0.3*inst
		}
		p.sampleAt, p.sampleBytes = now, p.done
	}
	if !p.o.ErrTTY {
		pc := Percent(p.done, p.total)
		if force || pc/10 > p.lastPc/10 || now.Sub(p.last) >= 30*time.Second {
			p.last, p.lastPc = now, pc
			line := fmt.Sprintf("%s: %d%% (%s / %s)", p.label, pc, config.FormatBytes(p.done), config.FormatBytes(p.total))
			if p.rate > 0 {
				line += ", " + config.FormatBytes(int64(p.rate)) + "/s"
			}
			if p.note != "" {
				line += ", " + p.note
			}
			fmt.Fprintln(p.o.Err, line)
		}
		return
	}
	if !force && now.Sub(p.last) < 100*time.Millisecond {
		return
	}
	p.last = now
	width := p.o.Width()
	if width <= 0 {
		width = 80
	}
	stats := Stats(p.done, p.total)
	if p.rate > 0 {
		stats += "  " + config.FormatBytes(int64(p.rate)) + "/s"
		if remaining := p.total - p.done; remaining > 0 {
			stats += "  " + formatETA(time.Duration(float64(remaining)/p.rate*float64(time.Second)))
		}
	}
	if p.note != "" {
		stats += "  " + Dim.Render(p.note)
	}
	label := ansi.Truncate(p.label, max(10, width/3), "…")
	barWidth := width - ansi.StringWidth(label) - ansi.StringWidth(stats) - 3
	bar := ""
	if barWidth >= 10 {
		bar = " " + Bar(min(barWidth, 40), p.done, p.total)
	}
	line := Bold.Render(label) + bar + stats
	p.o.clearLine()
	fmt.Fprint(p.o.Err, ansi.Truncate(line, width-1, ""))
}

func formatETA(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm", h, m)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}
