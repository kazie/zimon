package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/library"
)

// Size formats a byte count, or "?" when unknown.
func Size(n int64) string { return config.Size(n) }

// SizeOrDash is Size, or a dimmed dash for nothing.
func SizeOrDash(n int64) string {
	if n <= 0 {
		return Dim.Render("–")
	}
	return Size(n)
}

// Plural is "s" unless n is 1.
func Plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// Count is n with the word for one or for many: "1 file is", "3 files are".
func Count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// Ago is a time span as people say it: "5 minutes", "3 hours", "2 days".
func Ago(d time.Duration) string {
	for _, u := range []struct {
		size time.Duration
		name string
	}{{24 * time.Hour, "day"}, {time.Hour, "hour"}, {time.Minute, "minute"}} {
		if n := int(d / u.size); n >= 1 {
			return fmt.Sprintf("%d %s%s", n, u.name, Plural(n))
		}
	}
	return "moments"
}

// PlanParts says what a plan costs: "download about 1.2 GB", "delete 300 MB".
func PlanParts(download, freed int64) []string {
	var parts []string
	if download > 0 {
		parts = append(parts, "download about "+Size(download))
	}
	if freed > 0 {
		parts = append(parts, "delete "+Size(freed))
	}
	return parts
}

// Freed is " (freed 1.2 GB)", dimmed, or "" for nothing freed.
func Freed(n int64) string {
	if n <= 0 {
		return ""
	}
	return Dim.Render(" (freed " + Size(n) + ")")
}

// RemoveQuestion asks before removing what: "Delete X (1.2 GB)?", or
// "Remove X from your library?" when no bytes would be deleted.
func RemoveQuestion(what string, bytes int64) string {
	if bytes > 0 {
		return "Delete " + what + " (" + Size(bytes) + ")?"
	}
	return "Remove " + what + " from your library?"
}

// Marker is the one-character status column used in tables.
func Marker(it *api.Item) string {
	if it == nil {
		return " "
	}
	return StateMarker(it.State)
}

// StateMarker is Marker for a state.
func StateMarker(s library.State) string {
	switch s {
	case library.UpToDate:
		return Green.Render("✓")
	case library.UpdateAvailable:
		return Yellow.Render("↑")
	case library.Downloading:
		return Cyan.Render("↓")
	case library.Missing:
		return Yellow.Render("○")
	case library.Orphaned:
		return Red.Render("!")
	case library.Unwanted:
		return Dim.Render("×")
	case library.Held:
		return Cyan.Render("=")
	default:
		return " "
	}
}

// StateText describes where an item stands, with the details that matter:
// the newer release's date, download progress, why a download stopped.
func StateText(it *api.Item) string {
	if it == nil {
		return ""
	}
	s := it.State.String()
	switch it.State {
	case library.UpToDate:
		s = Green.Render(s)
	case library.UpdateAvailable:
		s = Yellow.Render(s) + Dim.Render(" → "+it.Latest)
	case library.Missing:
		s = Yellow.Render(s)
	case library.Unwanted:
		hint := " (zimon apply removes it)"
		if it.SwitchTo != "" {
			hint = " (zimon apply switches to " + it.SwitchTo + ")"
		}
		s = Dim.Render(s + hint)
	case library.Downloading:
		if it.DownloadSize > 0 {
			s += " " + strconv.Itoa(Percent(it.Downloaded, it.DownloadSize)) + "%"
		}
		s = Cyan.Render(s)
		if it.DownloadError != "" {
			reason, _, _ := strings.Cut(it.DownloadError, "\n")
			s += Dim.Render(" (stopped: " + ansi.Truncate(reason, 60, "…") + ")")
		}
	case library.Orphaned:
		s = Red.Render(s)
	case library.Held:
		s = Cyan.Render(s)
		switch {
		case it.SwitchTo != "":
			s += Dim.Render(" (zimon unhold switches to " + it.SwitchTo + ")")
		case it.Latest > it.Date:
			s += Dim.Render(" (" + it.Latest + " is out)")
		}
	default:
		s = Dim.Render(s)
	}
	return s
}
