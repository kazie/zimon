package meta4

import (
	"os"
	"strings"
	"testing"
)

func TestParseKiwixMeta4(t *testing.T) {
	f, err := os.Open("testdata/ja.stackoverflow.com_mul_all_2024-12.zim.meta4")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "ja.stackoverflow.com_mul_all_2024-12.zim" {
		t.Errorf("Name = %q", m.Name)
	}
	if m.Size != 177768448 {
		t.Errorf("Size = %d", m.Size)
	}
	if m.SHA256 != "4451ba25001220850298e864c02b14a0aaade32a750592e02044a9a072cf9f63" {
		t.Errorf("SHA256 = %q", m.SHA256)
	}
	if m.PieceLength != 4194304 || len(m.Pieces) != 43 {
		t.Errorf("pieces: length %d, count %d", m.PieceLength, len(m.Pieces))
	}
	if len(m.Mirrors) != 6 {
		t.Fatalf("got %d mirrors", len(m.Mirrors))
	}
	if !strings.Contains(m.Mirrors[0].URL, "accum.se") || m.Mirrors[0].Priority != 1 || m.Mirrors[5].Priority != 6 {
		t.Errorf("mirrors not sorted by priority: %+v", m.Mirrors)
	}
}

func TestParseRejectsUnsafeName(t *testing.T) {
	doc := `<metalink xmlns="urn:ietf:params:xml:ns:metalink"><file name="../evil.zim"><size>10</size></file></metalink>`
	if _, err := Parse(strings.NewReader(doc)); err == nil {
		t.Fatal("expected an error for a path-traversal file name")
	}
}

// A broken piece list counts as none: a download neither fails every piece
// nor holds an absurd piece in memory. The sha-256, if any, still applies.
func TestParseBrokenPieces(t *testing.T) {
	const sha1 = "da39a3ee5e6b4b0d3255bfef95601890afd80709"
	for name, pieces := range map[string]string{
		"too few":    `<pieces length="4" type="sha-1"><hash>` + sha1 + `</hash></pieces>`,
		"bad hash":   `<pieces length="8" type="sha-1"><hash>` + sha1 + `</hash><hash>zz</hash></pieces>`,
		"huge piece": `<pieces length="1099511627776" type="sha-1"><hash>` + sha1 + `</hash></pieces>`,
		"no length":  `<pieces type="sha-1"><hash>` + sha1 + `</hash></pieces>`,
	} {
		doc := `<metalink xmlns="urn:ietf:params:xml:ns:metalink"><file name="a.zim"><size>10</size>` + pieces + `</file></metalink>`
		m, err := Parse(strings.NewReader(doc))
		if err != nil || m.PieceLength != 0 || m.Pieces != nil {
			t.Errorf("%s: %+v, %v; want no pieces", name, m, err)
		}
	}
}

func TestParseMirrorOrder(t *testing.T) {
	doc := `<metalink xmlns="urn:ietf:params:xml:ns:metalink"><file name="a.zim"><size>10</size>
	<url>https://unspecified/</url><url priority="9223372036854775807">https://max/</url>
	<url priority="2">https://two/</url><url priority="-9223372036854775808">https://min/</url></file></metalink>`
	m, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, mi := range m.Mirrors {
		got = append(got, strings.Trim(strings.TrimPrefix(mi.URL, "https://"), "/"))
	}
	if strings.Join(got, " ") != "min two max unspecified" {
		t.Errorf("mirror order: %v", got)
	}
}
