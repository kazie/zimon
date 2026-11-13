package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/library"
)

// apiGet sends an API request with token ("" for none).
func apiGet(t *testing.T, h http.Handler, token, path string, header ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("%d %s: %v", w.Code, w.Body.String(), err)
	}
	return v
}

func newAPI(t *testing.T) (*Server, http.Handler, string) {
	t.Helper()
	s := New(newManager(t), "")
	s.Version = "test"
	token, hash := NewToken()
	if err := s.Manager.Store.AddToken(context.Background(), "test", hash); err != nil {
		t.Fatal(err)
	}
	return s, s.Handler(), token
}

func TestAPIAuth(t *testing.T) {
	_, h, token := newAPI(t)
	if w := apiGet(t, h, "", "/api/v1/items"); w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") == "" {
		t.Errorf("no token: %d", w.Code)
	}
	if w := apiGet(t, h, "zimon_wrong", "/api/v1/items"); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", w.Code)
	}
	if w := apiGet(t, h, token, "/api/v1/items"); w.Code != http.StatusOK {
		t.Errorf("token: %d %s", w.Code, w.Body.String())
	}
	if w := apiGet(t, h, "", "/api/v1/status"); w.Code != http.StatusOK {
		t.Errorf("status needs no token: %d", w.Code)
	}
	// Unknown API paths are not passed on to kiwix-serve.
	if w := apiGet(t, h, token, "/api/v1/nope"); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "error") {
		t.Errorf("unknown endpoint: %d %s", w.Code, w.Body.String())
	}
	if !strings.HasPrefix(token, tokenPrefix) || hashToken(token) == token {
		t.Errorf("token %q", token)
	}
}

func TestAPIRead(t *testing.T) {
	_, h, token := newAPI(t)
	items := decode[[]struct{ Key, State string }](t, apiGet(t, h, token, "/api/v1/items"))
	if len(items) != 3 || items[0].Key != "devdocs_en_go" || items[0].State != "up_to_date" {
		t.Errorf("items = %+v", items)
	}

	cols := decode[[]api.Collection](t, apiGet(t, h, token, "/api/v1/collections"))
	if _, ok := cols[0].Sizes["deep"]; len(cols) != 1 || cols[0].ID != "docs" || !cols[0].Mine || cols[0].Level != "standard" || !ok {
		t.Errorf("collections = %+v", cols)
	}

	found := decode[[]struct{ Key, State string }](t, apiGet(t, h, token, "/api/v1/search?q=wiki&lang=eng"))
	if len(found) != 1 || found[0].Key != "wiki_en_all:maxi" || found[0].State != "up_to_date" {
		t.Errorf("search = %+v", found)
	}
	if w := apiGet(t, h, token, "/api/v1/entries/devdocs_en_go"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"title":"Go Docs"`) {
		t.Errorf("entries: %d %s", w.Code, w.Body.String())
	}
	if w := apiGet(t, h, token, "/api/v1/entries/nope"); w.Code != http.StatusNotFound {
		t.Errorf("unknown entry: %d", w.Code)
	}
	budget := decode[struct {
		Uses []api.Use
		Disk int64
	}](t, apiGet(t, h, token, "/api/v1/budget"))
	if len(budget.Uses) != 2 || budget.Uses[0].Via != "docs" || budget.Uses[1].Via != "items" || budget.Disk != 6<<20 {
		t.Errorf("budget = %+v", budget)
	}

	// wikipedia_ja_top is on disk but not wanted: apply would remove it.
	plan := decode[api.Plan](t, apiGet(t, h, token, "/api/v1/plan"))
	if len(plan.Steps) != 1 || plan.Steps[0].Kind != "remove" || plan.Steps[0].Key != "wikipedia_ja_top:nopic" || plan.Current != 6<<20 {
		t.Errorf("plan = %+v", plan)
	}
}

// TestAPIClient covers what the client of an https:// remote asks for.
func TestAPIClient(t *testing.T) {
	_, h, token := newAPI(t)
	if e := decode[api.Error](t, apiGet(t, h, token, "/api/v1/entries/devdocs_en_og")); !slices.Contains(e.Similar, "devdocs_en_go") {
		t.Errorf("a near miss: %+v", e)
	}

	// Search says how many match in all, and returns up to limit.
	w := apiGet(t, h, token, "/api/v1/search?limit=1")
	if found := decode[[]api.Entry](t, w); len(found) != 1 || w.Header().Get("X-Total-Count") != "3" {
		t.Errorf("search with a limit: %d entries, total %q", len(found), w.Header().Get("X-Total-Count"))
	}
	if found := decode[[]api.Entry](t, apiGet(t, h, token, "/api/v1/search?flavour=nopic")); len(found) != 1 || found[0].Key != "wikipedia_ja_top:nopic" {
		t.Errorf("search by flavour = %+v", found)
	}

	info := decode[api.Info](t, apiGet(t, h, token, "/api/v1/info"))
	if info.API != api.Version || info.Version != "test" || info.Entries != 3 || info.LibraryDir == "" || !slices.Equal(info.Languages, []string{"eng"}) {
		t.Errorf("info = %+v", info)
	}

	col := decode[api.CollectionDetail](t, apiGet(t, h, token, "/api/v1/collections/docs"))
	if col.ID != "docs" || !col.Mine || col.Shown != "standard" || len(col.Items) != 1 ||
		col.Items[0].Key != "devdocs_en_go" || !col.Items[0].Included || col.Items[0].State != library.UpToDate {
		t.Errorf("collection = %+v", col)
	}
	if w := apiGet(t, h, token, "/api/v1/collections/nope"); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "there are: @docs") {
		t.Errorf("unknown collection: %d %s", w.Code, w.Body.String())
	}
}

func TestAPIFile(t *testing.T) {
	s, h, token := newAPI(t)
	if w := apiGet(t, h, token, "/api/v1/files/u1"); w.Code != http.StatusNotFound {
		t.Errorf("file not on disk: %d", w.Code)
	}
	if err := os.MkdirAll(s.Manager.Cfg.LibraryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Manager.Cfg.LibraryDir, "devdocs_en_go_2026-10.zim"), []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := apiGet(t, h, token, "/api/v1/files/u1", "Range", "bytes=4-")
	if w.Code != http.StatusPartialContent || w.Body.String() != "456789" ||
		!strings.Contains(w.Header().Get("Content-Disposition"), "devdocs_en_go_2026-10.zim") {
		t.Errorf("range: %d %q %v", w.Code, w.Body.String(), w.Header())
	}
	if w := apiGet(t, h, token, "/api/v1/files/nope"); w.Code != http.StatusNotFound {
		t.Errorf("unknown file: %d", w.Code)
	}
}
