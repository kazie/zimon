package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/kazie/zimon/internal/catalog"
)

func TestFamiliesAndEntriesByName(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.ReplaceCatalog(ctx, sample, ""); err != nil {
		t.Fatal(err)
	}
	fams, err := s.Families(ctx, []string{"wikipedia_en_all", "superuser.com_en_all", "nope"})
	if err != nil {
		t.Fatal(err)
	}
	flavours := func(es []catalog.Entry) []string {
		var out []string
		for _, e := range es {
			out = append(out, e.Flavour)
		}
		return out
	}
	if len(fams) != 2 || !slices.Equal(flavours(fams["wikipedia_en_all"]), []string{"maxi", "mini"}) ||
		!slices.Equal(flavours(fams["superuser.com_en_all"]), []string{""}) {
		t.Errorf("families = %+v", fams)
	}
	if fams, err := s.Families(ctx, nil); err != nil || len(fams) != 0 {
		t.Errorf("no names: %v, %v", fams, err)
	}
	es, err := s.EntriesByName(ctx, "wikipedia_en_all")
	if err != nil || !slices.Equal(flavours(es), []string{"maxi", "mini"}) || es[0].ApproxSize != 127e9 {
		t.Errorf("EntriesByName = %+v, %v", es, err)
	}
}

func TestReplaceCatalogKeepsNewestPerKey(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	older := catalog.Entry{UUID: "old", Name: "devdocs_en_go", Date: "2026-01-01"}
	newer := catalog.Entry{UUID: "new", Name: "devdocs_en_go", Date: "2026-02-01"}
	if err := s.ReplaceCatalog(ctx, []catalog.Entry{newer, older}, ""); err != nil {
		t.Fatal(err)
	}
	if es, _ := s.EntriesByName(ctx, "devdocs_en_go"); len(es) != 1 || es[0].UUID != "new" {
		t.Errorf("entries = %+v", es)
	}
}

func TestTouchCatalog(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.ReplaceCatalog(ctx, sample, `"e"`); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(ctx, kvCatalogSynced, "2020-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	synced, etag, count, err := s.CatalogState(ctx)
	if err != nil || time.Since(synced) > time.Minute || etag != `"e"` || count != len(sample) {
		t.Errorf("state after touch: %v %q %d %v", synced, etag, count, err)
	}
}

func TestFacets(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.ReplaceCatalog(ctx, sample, ""); err != nil {
		t.Fatal(err)
	}
	cats, err := s.Categories(ctx)
	if err != nil || !slices.Equal(cats, []Facet{{"wikipedia", 3}, {"stack_exchange", 2}}) {
		t.Errorf("categories = %v, %v", cats, err)
	}
	// "eng,jpn" counts for both; ties sort by name.
	langs, err := s.Languages(ctx)
	if err != nil || !slices.Equal(langs, []Facet{{"eng", 4}, {"jpn", 1}, {"swe", 1}}) {
		t.Errorf("languages = %v, %v", langs, err)
	}
}

func TestFileLifecycle(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	f := FileFromEntry(sample[0])
	f.Path, f.Size, f.SHA256 = "wikipedia_en_all_maxi_2026-08.zim", 127, "abc"
	if f.Key() != (catalog.Key{Name: "wikipedia_en_all", Flavour: "maxi"}) || f.Title != "Wikipedia" || f.Date != "2026-08-25" {
		t.Fatalf("FileFromEntry = %+v", f)
	}
	if err := s.InstallFile(ctx, f); err != nil {
		t.Fatal(err)
	}
	other := File{UUID: "x", Name: "devdocs_en_go", Path: "devdocs_en_go_2026-10.zim"}
	if err := s.InstallFile(ctx, other); err != nil {
		t.Fatal(err)
	}
	active, err := s.ActiveFiles(ctx)
	if err != nil || len(active) != 2 || active[0].Name != "devdocs_en_go" || active[1].SHA256 != "abc" {
		t.Fatalf("active = %+v, %v", active, err)
	}

	if err := s.MarkVerified(ctx, f.UUID); err != nil {
		t.Fatal(err)
	}
	var verified string
	if err := s.db.QueryRowContext(ctx, "SELECT verified_at FROM files WHERE uuid = ?", f.UUID).Scan(&verified); err != nil || parseTime(verified).IsZero() {
		t.Errorf("verified_at = %q, %v", verified, err)
	}

	// A new file at the same path replaces the row that claimed it.
	again := other
	again.UUID = "y"
	if err := s.InstallFile(ctx, again); err != nil {
		t.Fatal(err)
	}
	if files, _ := s.FilesByKey(ctx, other.Key()); len(files) != 1 || files[0].UUID != "y" {
		t.Errorf("same path: %+v", files)
	}

	if err := s.DeleteFile(ctx, f.UUID); err != nil {
		t.Fatal(err)
	}
	if files, _ := s.Files(ctx); len(files) != 1 || files[0].UUID != "y" {
		t.Errorf("after delete: %+v", files)
	}
}

func TestDownloadLifecycle(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for _, d := range []Download{
		{UUID: "a", Name: "wikipedia_en_all", Flavour: "maxi", Filename: "a.zim", Size: 10, Meta4: []byte("<a/>")},
		{UUID: "b", Name: "devdocs_en_go", Filename: "b.zim", Size: 20, Meta4: []byte("<b/>")},
	} {
		if _, err := s.StartDownload(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Checkpoint(ctx, "a", 5, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDownloadError(ctx, "a", "mirror said 503"); err != nil {
		t.Fatal(err)
	}
	ds, err := s.Downloads(ctx)
	if err != nil || len(ds) != 2 {
		t.Fatalf("downloads = %+v, %v", ds, err)
	}
	a := ds[slices.IndexFunc(ds, func(d Download) bool { return d.UUID == "a" })]
	if a.Key() != (catalog.Key{Name: "wikipedia_en_all", Flavour: "maxi"}) || a.VerifiedBytes != 5 || a.Error != "mirror said 503" || a.Meta4 != nil {
		t.Errorf("a = %+v", a)
	}
	// A checkpoint clears the error.
	if err := s.Checkpoint(ctx, "a", 6, nil); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.DownloadByUUID(ctx, "a"); d == nil || d.Error != "" || d.VerifiedBytes != 6 {
		t.Errorf("after checkpoint: %+v", d)
	}
	if err := s.DeleteDownload(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if d, err := s.DownloadByUUID(ctx, "a"); d != nil || err != nil {
		t.Errorf("deleted download: %+v, %v", d, err)
	}
}
