package zim

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// FuzzParse: any bytes give a header or an error, never a panic, and the
// UUID round-trips through the catalog's format.
func FuzzParse(f *testing.F) {
	hdr := make([]byte, HeaderSize)
	binary.LittleEndian.PutUint32(hdr, Magic)
	binary.LittleEndian.PutUint16(hdr[4:], 6)
	copy(hdr[8:], "0123456789abcdef")
	f.Add(hdr)
	f.Add(hdr[:HeaderSize-1])
	f.Add([]byte("not a zim"))
	f.Fuzz(func(t *testing.T, b []byte) {
		h, err := Parse(b)
		if err != nil {
			return
		}
		u, err := ParseUUID(h.UUID)
		if err != nil || !bytes.Equal(u, b[8:24]) {
			t.Fatalf("uuid %q round-trips to %x, %v", h.UUID, u, err)
		}
	})
}

// FuzzReadFile: whatever is on disk, reading a header fails cleanly.
func FuzzReadFile(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("ZIM\x04"))
	f.Fuzz(func(t *testing.T, b []byte) {
		path := filepath.Join(t.TempDir(), "x.zim")
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		if h, err := ReadFile(path); err == nil && len(b) < HeaderSize {
			t.Fatalf("read a header %+v from %d bytes", h, len(b))
		}
	})
}
