package catalog

import (
	"bytes"
	"net/url"
	"os"
	"testing"
	"time"
)

// FuzzParse: a feed from the network parses or fails cleanly, and every
// entry it returns is usable: an id, a name, a download link and a real date.
func FuzzParse(f *testing.F) {
	if data, err := os.ReadFile("testdata/entries.xml"); err == nil {
		f.Add(data)
	}
	f.Add([]byte(`<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>urn:uuid:a</id><name>a</name>` +
		`<updated>2026-01-01T00:00:00Z</updated><link rel="http://opds-spec.org/acquisition/open-access" ` +
		`type="application/x-zim" href="/a.meta4" length="9"/></entry></feed>`))
	base, _ := url.Parse("https://opds.library.kiwix.org/catalog/v2/entries")
	f.Fuzz(func(t *testing.T, data []byte) {
		entries, err := Parse(bytes.NewReader(data), base)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.UUID == "" || e.Name == "" || e.Meta4URL == "" || e.ApproxSize < 0 {
				t.Fatalf("unusable entry %+v", e)
			}
			if _, err := time.Parse(time.DateOnly, e.Date); e.Date != "" && err != nil {
				t.Fatalf("date %q of %+v", e.Date, e)
			}
		}
	})
}

// FuzzKey: parsing a key and writing it back gives the same key.
func FuzzKey(f *testing.F) {
	for _, s := range []string{"wikipedia_en_all", "wikipedia_en_all:maxi", " a:b ", "a:", ":b", "a:b:c"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		k := ParseKey(s)
		if back := ParseKey(k.String()); back != k {
			t.Fatalf("%q: %+v writes as %q, which parses as %+v", s, k, k.String(), back)
		}
	})
}
