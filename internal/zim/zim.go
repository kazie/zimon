// Package zim reads the fixed 80-byte header of a ZIM file.
// See https://wiki.openzim.org/wiki/ZIM_file_format#Header
package zim

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Magic is the ZIM magic number (little-endian 72173914).
const Magic uint32 = 0x044D495A

// HeaderSize is the size of the fixed header.
const HeaderSize = 80

// Header is the part of the ZIM header zimon cares about.
type Header struct {
	MajorVersion uint16
	UUID         string // formatted like the Kiwix catalog: 8-4-4-4-12 hex
}

// ErrNotZIM is returned when the magic number does not match.
var ErrNotZIM = errors.New("not a ZIM file (bad magic number)")

// Parse decodes a header from its first 80 bytes.
func Parse(b []byte) (*Header, error) {
	if len(b) < HeaderSize {
		return nil, fmt.Errorf("ZIM header too short: %d bytes", len(b))
	}
	if binary.LittleEndian.Uint32(b[0:4]) != Magic {
		return nil, ErrNotZIM
	}
	return &Header{
		MajorVersion: binary.LittleEndian.Uint16(b[4:6]),
		UUID:         FormatUUID(b[8:24]),
	}, nil
}

// ReadFile reads the header of the ZIM at path.
func ReadFile(path string) (*Header, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b := make([]byte, HeaderSize)
	if _, err := io.ReadFull(f, b); err != nil {
		return nil, fmt.Errorf("read ZIM header: %w", err)
	}
	return Parse(b)
}

// FormatUUID renders 16 bytes as 8-4-4-4-12 hex.
func FormatUUID(u []byte) string {
	h := hex.EncodeToString(u)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// ParseUUID is the inverse of FormatUUID.
func ParseUUID(s string) ([]byte, error) {
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(b) != 16 {
		return nil, fmt.Errorf("invalid UUID %q", s)
	}
	return b, nil
}
