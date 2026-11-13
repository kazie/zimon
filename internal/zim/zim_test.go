package zim

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

// The fixture is the first 80 bytes of wikipedia_cr_all_nopic_2026-11.zim,
// whose catalog UUID is 5c3aefb3-2c18-cc42-0d6b-02ef82e851eb.
func TestRealHeader(t *testing.T) {
	h, err := ReadFile("testdata/wikipedia_cr_all_nopic_2026-11.header")
	if err != nil {
		t.Fatal(err)
	}
	if h.UUID != "5c3aefb3-2c18-cc42-0d6b-02ef82e851eb" {
		t.Errorf("UUID = %s", h.UUID)
	}
	if h.MajorVersion != 6 {
		t.Errorf("MajorVersion = %d", h.MajorVersion)
	}
}

func TestNotZIM(t *testing.T) {
	if _, err := Parse(bytes.Repeat([]byte{0}, HeaderSize)); !errors.Is(err, ErrNotZIM) {
		t.Fatalf("err = %v, want ErrNotZIM", err)
	}
	if _, err := Parse([]byte("short")); err == nil {
		t.Fatal("expected an error for a short header")
	}
}

func TestUUIDRoundTrip(t *testing.T) {
	b, err := os.ReadFile("testdata/wikipedia_cr_all_nopic_2026-11.header")
	if err != nil {
		t.Fatal(err)
	}
	s := FormatUUID(b[8:24])
	back, err := ParseUUID(s)
	if err != nil || !bytes.Equal(back, b[8:24]) {
		t.Fatalf("round trip failed: %v", err)
	}
}
