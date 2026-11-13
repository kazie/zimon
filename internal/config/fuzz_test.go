package config

import (
	"testing"
	"time"
)

// FuzzParseBytes: a size from a config file or the command line parses to
// a byte count that is never negative, or fails.
func FuzzParseBytes(f *testing.F) {
	for _, s := range []string{"500MB", "2G", "1.5GiB", "1024", " 20 mb ", ".5k", "1e3", "99999999999999999999TB"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if n, err := ParseBytes(s); err == nil && n < 0 {
			t.Fatalf("ParseBytes(%q) = %d", s, n)
		}
	})
}

// FuzzByteSize: saving a size and reading it back never changes it.
func FuzzByteSize(f *testing.F) {
	for _, n := range []int64{0, 1, 1000, 1 << 20, 250e9, 1<<53 + 1, 1<<63 - 1} {
		f.Add(n)
	}
	f.Fuzz(func(t *testing.T, n int64) {
		if n < 0 {
			return
		}
		text, err := ByteSize(n).MarshalText()
		if err != nil {
			t.Fatal(err)
		}
		var back ByteSize
		if err := back.UnmarshalText(text); err != nil || int64(back) != n {
			t.Fatalf("%d writes as %q, which reads as %d, %v", n, text, back, err)
		}
	})
}

// FuzzDuration: saving a duration and reading it back never changes it.
func FuzzDuration(f *testing.F) {
	for _, d := range []time.Duration{0, time.Second, 24 * time.Hour, 1500 * time.Millisecond, -time.Minute} {
		f.Add(int64(d))
	}
	f.Fuzz(func(t *testing.T, n int64) {
		text, err := Duration{time.Duration(n)}.MarshalText()
		if err != nil {
			t.Fatal(err)
		}
		var back Duration
		if err := back.UnmarshalText(text); err != nil || back.Duration != time.Duration(n) {
			t.Fatalf("%d writes as %q, which reads as %d, %v", n, text, back.Duration, err)
		}
	})
}
