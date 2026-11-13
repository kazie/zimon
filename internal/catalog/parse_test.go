package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func TestFeatures(t *testing.T) {
	feats, plain := Features("wikipedia; _category:wikipedia;_pictures:no;_videos:yes;_details:no;_ftindex:yes;medicine;;")
	if want := []string{"no pictures", "videos", "intros only", "full-text search"}; !slices.Equal(feats, want) {
		t.Errorf("features = %q, want %q", feats, want)
	}
	if want := []string{"wikipedia", "medicine"}; !slices.Equal(plain, want) {
		t.Errorf("plain tags = %q, want %q", plain, want)
	}
}

func TestDateOnly(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{"2026-08-25T00:00:00Z"}, "2026-08-25"},
		{[]string{"2026-08-25T01:00:00+02:00"}, "2026-08-24"}, // in UTC
		{[]string{"2026-08-25"}, "2026-08-25"},
		{[]string{"  ", "2026-09-01T10:00:00Z"}, "2026-09-01"}, // falls back to issued
		{[]string{"2026"}, ""},
		{nil, ""},
	} {
		if got := DateOnly(c.in...); got != c.want {
			t.Errorf("DateOnly(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseLinks(t *testing.T) {
	feed := `<feed xmlns="http://www.w3.org/2005/Atom">
<entry><id>urn:uuid:a</id><name>a_en_all</name><updated>2026-01-01T00:00:00Z</updated>
  <link rel="http://opds-spec.org/acquisition/open-access" type="text/html" href="/no"/>
  <link rel="http://opds-spec.org/acquisition/open-access" type="application/x-zim" href="/zim/a.zim.meta4" length="42"/></entry>
<entry><id>urn:uuid:b</id><name>b_en_all</name></entry>
<entry><id>urn:uuid:c</id><name>c_en_all</name>
  <link rel="http://opds-spec.org/acquisition/open-access" type="application/x-zim" href="https://mirror/c.zim.meta4" length="-9"/></entry>
</feed>`
	base, _ := url.Parse("https://catalog.example/catalog/v2/entries")
	es, err := Parse(strings.NewReader(feed), base)
	if err != nil {
		t.Fatal(err)
	}
	// b has no download link, so it is left out.
	if len(es) != 2 || es[0].Meta4URL != "https://catalog.example/zim/a.zim.meta4" || es[0].ApproxSize != 42 ||
		es[1].Meta4URL != "https://mirror/c.zim.meta4" || es[1].ApproxSize != 0 {
		t.Errorf("entries = %+v", es)
	}
	// Without a base, links stay as they are.
	if es, _ := Parse(strings.NewReader(feed), nil); len(es) != 2 || es[0].Meta4URL != "/zim/a.zim.meta4" {
		t.Errorf("no base: %+v", es)
	}
	if _, err := Parse(strings.NewReader("<feed><entry>"), nil); err == nil {
		t.Error("a broken feed parsed")
	}
}

func TestFetchErrors(t *testing.T) {
	for name, reply := range map[string]struct {
		status int
		body   string
	}{
		"server error": {http.StatusServiceUnavailable, "down"},
		"empty feed":   {http.StatusOK, `<feed xmlns="http://www.w3.org/2005/Atom"></feed>`},
		"not xml":      {http.StatusOK, `{"json": true}`},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(reply.status)
			_, _ = w.Write([]byte(reply.body))
		}))
		if _, err := (&Client{HTTP: srv.Client(), BaseURL: srv.URL}).Fetch(context.Background(), ""); err == nil {
			t.Errorf("%s: no error", name)
		}
		srv.Close()
	}
}

// An id, name or flavour that could climb out of a folder is not trusted:
// a partial download is named after the id, a ZIM after its name.
func TestEntrySafe(t *testing.T) {
	for _, c := range []struct {
		e    Entry
		safe bool
	}{
		{Entry{UUID: "5c3aefb3-2c18-cc42-0d6b-02ef82e851eb", Name: "superuser.com_en_all", Flavour: "maxi"}, true},
		{Entry{UUID: "a", Name: "a_en_all"}, true},
		{Entry{UUID: "../../home/u/notes", Name: "a_en_all"}, false},
		{Entry{UUID: "a", Name: "../../x"}, false},
		{Entry{UUID: "a", Name: "a/b"}, false},
		{Entry{UUID: "a", Name: ".hidden"}, false},
		{Entry{UUID: "a", Name: "a_en_all", Flavour: "../m"}, false},
		{Entry{UUID: "", Name: "a_en_all"}, false},
	} {
		if got := c.e.Safe(); got != c.safe {
			t.Errorf("%+v: Safe() = %v, want %v", c.e, got, c.safe)
		}
	}
}
