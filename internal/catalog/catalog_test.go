package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
)

func TestParseFixture(t *testing.T) {
	f, err := os.Open("testdata/entries.xml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	base, _ := url.Parse("https://opds.library.kiwix.org/catalog/v2/entries")
	entries, err := Parse(f, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 6 {
		t.Fatalf("got %d entries, want 6", len(entries))
	}
	byKey := map[string]Entry{}
	for _, e := range entries {
		byKey[e.Key().String()] = e
	}
	e, ok := byKey["wikipedia_en_all:maxi"]
	if !ok {
		t.Fatalf("wikipedia_en_all:maxi missing; have %v", byKey)
	}
	if e.Title != "Wikipedia" || e.Language != "eng" || e.Category != "wikipedia" || e.Creator != "Wikipedia" || e.Publisher != "openZIM" {
		t.Errorf("unexpected fields: %+v", e)
	}
	if e.Date != "2026-08-25" {
		t.Errorf("Date = %q", e.Date)
	}
	if e.ApproxSize != 127418088448 {
		t.Errorf("ApproxSize = %d", e.ApproxSize)
	}
	if e.Meta4URL != "https://lb.download.kiwix.org/zim/wikipedia/wikipedia_en_all_maxi_2026-08.zim.meta4" {
		t.Errorf("Meta4URL = %q", e.Meta4URL)
	}
	if len(e.UUID) != 36 {
		t.Errorf("UUID = %q", e.UUID)
	}
	if e100, ok := byKey["wikipedia_en_100"]; !ok || e100.Flavour != "" {
		t.Errorf("wikipedia_en_100 should have an empty flavour: %+v", e100)
	}
}

func TestParseKey(t *testing.T) {
	for _, c := range []struct {
		in   string
		want Key
	}{
		{"wikipedia_en_all", Key{Name: "wikipedia_en_all"}},
		{"wikipedia_en_all:maxi", Key{Name: "wikipedia_en_all", Flavour: "maxi"}},
		{" superuser.com_en_all\t", Key{Name: "superuser.com_en_all"}}, // spaces around are trimmed
	} {
		in, want := c.in, c.want
		if got := ParseKey(in); got != want {
			t.Errorf("ParseKey(%q) = %+v, want %+v", in, got, want)
		}
		if want.String() != ParseKey(want.String()).String() {
			t.Errorf("round trip of %v failed", want)
		}
	}
}

func TestFetchETag(t *testing.T) {
	feed, err := os.ReadFile("testdata/entries.xml")
	if err != nil {
		t.Fatal(err)
	}
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Query().Get("count") != "-1" {
			t.Errorf("count = %q, want -1", r.URL.Query().Get("count"))
		}
		w.Header().Set("ETag", `"v1"`)
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write(feed)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/catalog/v2/"}
	res, err := c.Fetch(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if res.NotModified || len(res.Entries) != 6 || res.ETag != `"v1"` {
		t.Fatalf("first fetch: %+v", res)
	}
	res, err = c.Fetch(context.Background(), res.ETag)
	if err != nil {
		t.Fatal(err)
	}
	if !res.NotModified {
		t.Fatalf("second fetch should be NotModified")
	}
	if hits != 2 {
		t.Errorf("hits = %d", hits)
	}
}

func TestFetchRejectsHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><title>Access confirmation</title></html>"))
	}))
	defer srv.Close()
	_, err := (&Client{BaseURL: srv.URL}).Fetch(context.Background(), "")
	if err == nil {
		t.Fatal("expected an error for an HTML bot-check page")
	}
}
