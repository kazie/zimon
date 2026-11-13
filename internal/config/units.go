package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Duration is a time.Duration that reads and writes as "24h", "90m", ...
// It is a struct so a bare number (catalog_max_age = 3600: seconds?
// nanoseconds?) is rejected instead of silently read as nanoseconds.
type Duration struct{ time.Duration }

// MarshalText writes d like "24h0m0s".
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// UnmarshalText reads a Go duration such as "24h" or "90m".
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(strings.TrimSpace(string(b)))
	if err != nil {
		return fmt.Errorf("%w (write durations as strings, e.g. \"24h\")", err)
	}
	d.Duration = v
	return nil
}

// ByteSize is a byte count that reads and writes as "20MB", "1.5GiB", ...
type ByteSize int64

// MarshalText writes the exact value, in the largest unit that divides it
// evenly ("20MB", "1GiB"), so saving a config never changes it.
func (b ByteSize) MarshalText() ([]byte, error) {
	for _, u := range []struct {
		name string
		size int64
	}{{"TiB", 1 << 40}, {"TB", 1e12}, {"GiB", 1 << 30}, {"GB", 1e9}, {"MiB", 1 << 20}, {"MB", 1e6}, {"KiB", 1 << 10}, {"kB", 1e3}} {
		if b != 0 && int64(b)%u.size == 0 {
			return []byte(strconv.FormatInt(int64(b)/u.size, 10) + u.name), nil
		}
	}
	return []byte(strconv.FormatInt(int64(b), 10)), nil
}

// UnmarshalText reads a size as ParseBytes does.
func (b *ByteSize) UnmarshalText(text []byte) error {
	v, err := ParseBytes(string(text))
	if err != nil {
		return err
	}
	*b = ByteSize(v)
	return nil
}

var byteUnits = map[string]int64{
	"": 1, "b": 1,
	"k": 1e3, "kb": 1e3, "m": 1e6, "mb": 1e6, "g": 1e9, "gb": 1e9, "t": 1e12, "tb": 1e12,
	"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40,
}

// ParseBytes parses sizes like "500MB", "2G", "1.5GiB" or "1024".
// Units without "i" are decimal (1 MB = 1,000,000 bytes), units with "i" are binary.
func ParseBytes(s string) (int64, error) {
	s = strings.TrimSpace(s)
	i := strings.IndexFunc(s, func(r rune) bool { return !unicode.IsDigit(r) && r != '.' })
	num, unit := s, ""
	if i >= 0 {
		num, unit = s[:i], strings.ToLower(strings.TrimSpace(s[i:]))
	}
	mult, ok := byteUnits[unit]
	if !ok || num == "" {
		return 0, fmt.Errorf("invalid size %q (try 500MB, 2GB or 1.5GiB)", s)
	}
	if n, err := strconv.ParseInt(num, 10, 64); err == nil { // exact, so saving never changes it
		if n > math.MaxInt64/mult {
			return 0, errTooLarge(s)
		}
		return n * mult, nil
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	v := math.Round(f * float64(mult))
	if v >= math.MaxInt64 {
		return 0, errTooLarge(s)
	}
	return int64(v), nil
}

func errTooLarge(s string) error { return fmt.Errorf("size %q is too large", s) }

// Size is FormatBytes, or "?" for a size not known (0 or less).
func Size(n int64) string {
	if n <= 0 {
		return "?"
	}
	return FormatBytes(n)
}

// FormatBytes renders a byte count with decimal units: 333 MB, 127 GB.
func FormatBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := float64(unit), 0
	for f := float64(n) / unit; f >= unit && exp < 4; f /= unit {
		div *= unit
		exp++
	}
	v := float64(n) / div
	suffix := "kMGTP"[exp : exp+1]
	if v >= 100 {
		return fmt.Sprintf("%.0f %sB", v, suffix)
	}
	if v >= 10 {
		return fmt.Sprintf("%.1f %sB", v, suffix)
	}
	return fmt.Sprintf("%.2f %sB", v, suffix)
}
