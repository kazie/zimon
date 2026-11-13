package meta4

import (
	"bytes"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

// FuzzParse: a .meta4 from the network parses or fails cleanly, and what it
// returns is safe to act on: a plain file name, a size, well-formed hashes,
// a piece list that covers the file, and mirrors best first.
func FuzzParse(f *testing.F) {
	if data, err := os.ReadFile("testdata/ja.stackoverflow.com_mul_all_2024-12.zim.meta4"); err == nil {
		f.Add(data)
	}
	f.Add([]byte(`<metalink xmlns="urn:ietf:params:xml:ns:metalink"><file name="a.zim"><size>3</size>` +
		`<pieces length="2" type="sha-1"><hash>da39a3ee5e6b4b0d3255bfef95601890afd80709</hash>` +
		`<hash>da39a3ee5e6b4b0d3255bfef95601890afd80709</hash></pieces>` +
		`<url priority="2">https://b/a.zim</url><url priority="1">https://a/a.zim</url><url>https://c/a.zim</url></file></metalink>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := Parse(bytes.NewReader(data))
		if err != nil {
			return
		}
		if m.Name == "" || m.Name == "." || m.Name == ".." || strings.ContainsAny(m.Name, `/\`) || m.Size <= 0 {
			t.Fatalf("unsafe file %q, size %d", m.Name, m.Size)
		}
		if b, err := hex.DecodeString(m.SHA256); m.SHA256 != "" && (err != nil || len(b) != 32) {
			t.Fatalf("sha-256 %q", m.SHA256)
		}
		// Either no pieces, or a list that passes the checks.
		if (m.PieceLength != 0 || len(m.Pieces) != 0) && !piecesOK(m) {
			t.Fatalf("%d pieces of %d bytes for %d bytes: %q", len(m.Pieces), m.PieceLength, m.Size, m.Pieces)
		}
		for i := 1; i < len(m.Mirrors); i++ {
			a, b := m.Mirrors[i-1].Priority, m.Mirrors[i].Priority
			if b != 0 && (a == 0 || a > b) {
				t.Fatalf("mirrors out of order: %+v", m.Mirrors)
			}
		}
	})
}
