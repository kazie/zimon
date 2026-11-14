package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kazie/zimon/internal/catalog"
)

func open(t testing.TB) *Store {
	t.Helper()
	return openAt(t, filepath.Join(t.TempDir(), "zimon.db"))
}

// openAt opens the database at path, for a test that opens it again.
func openAt(t testing.TB, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

var sample = []catalog.Entry{
	{UUID: "u1", Name: "wikipedia_en_all", Flavour: "maxi", Title: "Wikipedia", Summary: "The free encyclopedia", Language: "eng", Category: "wikipedia", Date: "2026-08-25", ApproxSize: 127e9, Meta4URL: "https://x/wikipedia_en_all_maxi_2026-08.zim.meta4"},
	{UUID: "u2", Name: "wikipedia_en_all", Flavour: "mini", Title: "Wikipedia", Summary: "The free encyclopedia", Language: "eng", Category: "wikipedia", Date: "2026-09-09", ApproxSize: 14e9},
	{UUID: "u3", Name: "wikipedia_sv_all", Flavour: "maxi", Title: "Wikipedia", Summary: "Den fria encyklopedin", Language: "swe", Category: "wikipedia", Date: "2026-09-02", ApproxSize: 41e9},
	{UUID: "u4", Name: "superuser.com_en_all", Title: "Super User", Summary: "Q&A for computer enthusiasts", Language: "eng", Category: "stack_exchange", Date: "2026-10-19", ApproxSize: 3e9},
	{UUID: "u5", Name: "ja.stackoverflow.com_mul_all", Title: "スタック・オーバーフロー", Language: "eng,jpn", Category: "stack_exchange", Date: "2024-12-06", ApproxSize: 177e6},
}

func TestCheck(t *testing.T) {
	s := open(t)
	if err := s.ReplaceCatalog(context.Background(), sample, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Check(context.Background()); err != nil {
		t.Errorf("a sound database: %v", err)
	}
}

func TestCatalogSearch(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.ReplaceCatalog(ctx, sample, `"etag1"`); err != nil {
		t.Fatal(err)
	}
	synced, etag, count, err := s.CatalogState(ctx)
	if err != nil || etag != `"etag1"` || count != 5 || synced.IsZero() {
		t.Fatalf("state: %v %q %d %v", synced, etag, count, err)
	}

	keys := func(es []catalog.Entry) []string {
		var out []string
		for _, e := range es {
			out = append(out, e.Key().String())
		}
		return out
	}
	cases := []struct {
		q    Query
		want []string
	}{
		{Query{Text: "wiki"}, []string{"wikipedia_en_all:maxi", "wikipedia_en_all:mini", "wikipedia_sv_all:maxi"}},
		{Query{Text: "wikipedia en"}, []string{"wikipedia_en_all:maxi", "wikipedia_en_all:mini"}},
		{Query{Text: "encyclopedia", Languages: []string{"swe"}}, nil},
		{Query{Text: "wikipedia", Languages: []string{"swe"}}, []string{"wikipedia_sv_all:maxi"}},
		{Query{Languages: []string{"jpn"}}, []string{"ja.stackoverflow.com_mul_all"}},
		{Query{Category: "stack_exchange", MaxSize: 1e9}, []string{"ja.stackoverflow.com_mul_all"}},
		{Query{Text: "super user"}, []string{"superuser.com_en_all"}},
		{Query{Flavour: "mini"}, []string{"wikipedia_en_all:mini"}},
		{Query{Text: `"; DROP TABLE`}, nil},
	}
	for _, c := range cases {
		got, err := s.Search(ctx, c.q)
		if err != nil {
			t.Fatalf("%+v: %v", c.q, err)
		}
		if g := keys(got); len(g) != len(c.want) || (len(g) > 0 && !sameSet(g, c.want)) {
			t.Errorf("Search(%+v) = %v, want %v", c.q, g, c.want)
		}
	}

	// Replacing drops entries that disappeared upstream.
	if err := s.ReplaceCatalog(ctx, sample[:1], `"etag2"`); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Search(ctx, Query{Text: "super"}); len(got) != 0 {
		t.Errorf("stale FTS rows after replace: %v", keys(got))
	}
	k := catalog.Key{Name: "wikipedia_en_all", Flavour: "maxi"}
	if es, _ := s.EntriesByKeys(ctx, []catalog.Key{k, {Name: "superuser.com_en_all"}}); len(es) != 1 || es[k].UUID != "u1" {
		t.Errorf("EntriesByKeys = %+v", es)
	}
}

func TestFilesSupersede(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	k := catalog.Key{Name: "wikipedia_en_100"}
	old := File{UUID: "old", Name: k.Name, Path: "wikipedia_en_100_2026-08.zim", Size: 10, Date: "2026-08-20"}
	if err := s.InstallFile(ctx, old); err != nil {
		t.Fatal(err)
	}
	newer := File{UUID: "new", Name: k.Name, Path: "wikipedia_en_100_2026-09.zim", Size: 11, Date: "2026-09-20"}
	if err := s.InstallFile(ctx, newer); err != nil {
		t.Fatal(err)
	}
	active, err := s.ActiveFile(ctx, k)
	if err != nil || active == nil || active.UUID != "new" {
		t.Fatalf("active = %+v, %v", active, err)
	}
	all, _ := s.FilesByKey(ctx, k)
	if len(all) != 2 || all[0].UUID != "new" || all[1].State != StateSuperseded {
		t.Fatalf("files = %+v", all)
	}
}

func TestDownloadCheckpoint(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	d, err := s.StartDownload(ctx, Download{UUID: "u", Name: "n", Filename: "n.zim", Size: 100, Meta4: []byte("<x/>")})
	if err != nil || d.VerifiedBytes != 0 {
		t.Fatal(d, err)
	}
	if err := s.Checkpoint(ctx, "u", 64, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	// Starting again returns the existing row with its progress.
	d, err = s.StartDownload(ctx, Download{UUID: "u", Name: "n", Filename: "other.zim", Size: 100, Meta4: []byte("<y/>")})
	if err != nil || d.VerifiedBytes != 64 || string(d.HashState) != "\x01\x02\x03" || d.Filename != "n.zim" {
		t.Fatalf("resume row = %+v, %v", d, err)
	}
}

func sameSet(a, b []string) bool {
	m := map[string]int{}
	for _, x := range a {
		m[x]++
	}
	for _, x := range b {
		m[x]--
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}

func TestSuggestionsAndCompletion(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.ReplaceCatalog(ctx, sample, ""); err != nil {
		t.Fatal(err)
	}
	for _, typo := range []string{"wikipdia_en_all", "wikipedia_en_alll", "wikipedia_en_al"} {
		names, err := s.SimilarNames(ctx, typo, 3)
		if err != nil || len(names) == 0 || names[0] != "wikipedia_en_all" {
			t.Errorf("SimilarNames(%q) = %v, %v; want wikipedia_en_all first", typo, names, err)
		}
	}
	for prefix, want := range map[string]int{"wikipedia_e": 2, "wikipedia_en_all:m": 2, "wikipedia_en_all:mi": 1, "super": 1, "x": 0} {
		got, err := s.EntriesWithPrefix(ctx, prefix, nil, 50)
		if err != nil || len(got) != want {
			t.Errorf("EntriesWithPrefix(%q) = %d entries, want %d (%v)", prefix, len(got), want, err)
		}
	}
	// "_" in the prefix is literal, not a LIKE wildcard.
	if got, _ := s.EntriesWithPrefix(ctx, "wikipedia_sv", nil, 50); len(got) != 1 {
		t.Errorf("prefix with underscore: %d entries", len(got))
	}
}

func TestOpenOddPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "odd#dir?x%y", "zimon.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Set(context.Background(), "k", "v"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database not at %s: %v", path, err)
	}
}

func TestOpenVersion(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		version int // set before reopening; 0 leaves it alone
		wantErr bool
	}{
		{"reopen", 0, false},
		{"another schema", 12345, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "zimon.db")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if tc.version != 0 {
				if _, err := s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", tc.version)); err != nil {
					t.Fatal(err)
				}
			}
			s.Close()

			s, err = Open(path)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "another schema") {
					s.Close()
					t.Fatalf("got %v, want the another-schema error", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			// Every table is there after a reopen.
			for _, table := range []string{"kv", "catalog_entries", "catalog_fts", "files", "downloads", "tokens", "jobs"} {
				if _, err := s.db.ExecContext(ctx, "SELECT count(*) FROM "+table); err != nil {
					t.Errorf("table %s: %v", table, err)
				}
			}
		})
	}
}

func TestConcurrentFirstOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zimon.db")
	errs := make(chan error, 4)
	for range 4 {
		go func() {
			s, err := Open(path)
			if err == nil {
				s.Close()
			}
			errs <- err
		}()
	}
	for range 4 {
		if err := <-errs; err != nil {
			t.Errorf("concurrent open: %v", err)
		}
	}
}

// Released databases carry schemaVersion; without migrations, another one
// would make zimon refuse every existing library. Change the schema only
// together with a migration from these.
func TestSchemaIsReleased(t *testing.T) {
	const v010 = 1269520913 // v0.1.0
	if schemaVersion != v010 {
		t.Fatalf("schemaVersion is %d, v0.1.0's is %d: the schema (or its line count) changed", schemaVersion, v010)
	}
}
