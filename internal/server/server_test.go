package server

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/collections"
	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/library"
	"github.com/kazie/zimon/internal/store"
)

// newManager is a library with three ZIMs on disk (as database rows): one
// the docs collection wants, one hand-picked, and one nothing wants.
func newManager(t *testing.T) *library.Manager {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.LibraryDir = filepath.Join(dir, "library")
	cfg.StateDir = filepath.Join(dir, "state")
	cfg.Languages = []string{"eng"}
	cfg.CatalogURL = "http://127.0.0.1:1/catalog/v2" // nothing there: tests never reach the real catalog
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	entries := []catalog.Entry{
		{UUID: "u1", Name: "devdocs_en_go", Title: "Go Docs", Summary: "The Go documentation", Language: "eng", Date: "2026-10-12"},
		{UUID: "u2", Name: "wiki_en_all", Flavour: "maxi", Title: "Wiki", Language: "eng", Date: "2026-10-01"},
		{UUID: "u3", Name: "wikipedia_ja_top", Flavour: "nopic", Title: "ウィキペディア", Language: "jpn", Date: "2026-09-01"},
	}
	if err := st.ReplaceCatalog(ctx, entries, ""); err != nil {
		t.Fatal(err)
	}
	paths := []string{"devdocs_en_go_2026-10.zim", "wiki_en_all_maxi_2026-10.zim", "wikipedia_ja_top_nopic_2026-09.zim"}
	for i, e := range entries {
		f := store.FileFromEntry(e)
		f.ZimUUID, f.Path, f.Size = "zim-"+e.UUID, paths[i], int64(i+1)<<20
		if err := st.InstallFile(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	m := &library.Manager{Cfg: cfg, Store: st, SetupPath: filepath.Join(dir, "library.toml"),
		Collections: []collections.Collection{{ID: "docs", Title: "Docs", Blurb: "Programming references",
			Items: []collections.Item{{Name: "devdocs_en_go", Tier: collections.Core, Note: "Go's standard library"}}}}}
	toml := "[collections]\ndocs = \"standard\"\n[items]\n\"wiki_en_all\" = \"maxi\"\n"
	if err := os.WriteFile(m.SetupPath, []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	return m
}

func get(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}

func TestPortal(t *testing.T) {
	s := New(newManager(t), "kiwix-serve")
	s.upstream.Store(&url.URL{Scheme: "http", Host: "127.0.0.1:1"}) // up
	w := get(t, s.Handler(), "GET", "/")
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("%d %s\n%s", w.Code, w.Header().Get("Content-Type"), body)
	}
	for _, want := range []string{
		"<h2>Docs</h2>", "Programming references", "Go&#39;s standard library", // the collection, its blurb and note
		`href="/viewer#devdocs_en_go"`, `href="/random?content=devdocs_en_go"`, // links without the date
		"<h2>Other</h2>", `href="/viewer#wiki_en_all_maxi"`, "ウィキペディア", // hand-picked and unwanted books
		`/_zimon/icon/u1`,
		`<option selected>eng</option><option>jpn</option>`, // the configured language first
		"3 books, 6.29 MB",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("portal lacks %q", want)
		}
	}
	if strings.Contains(body, `class="problem"`) {
		t.Error("problem banner while kiwix-serve is up")
	}

	// Without kiwix-serve, the page says how to get it.
	s = New(newManager(t), "")
	if body := get(t, s.Handler(), "GET", "/").Body.String(); !strings.Contains(body, "kiwix-serve is not installed") {
		t.Errorf("no install hint:\n%s", body)
	}
}

// A book two collections want shows in both, each with its own note.
func TestPortalNotesPerCollection(t *testing.T) {
	m := newManager(t)
	m.Collections = append(m.Collections, collections.Collection{ID: "learn", Title: "Learn",
		Items: []collections.Item{{Name: "devdocs_en_go", Tier: collections.Core, Note: "for the Go tour"}}})
	if err := os.WriteFile(m.SetupPath, []byte("[collections]\ndocs = \"light\"\nlearn = \"light\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := get(t, New(m, "").Handler(), "GET", "/").Body.String()
	_, rest, ok1 := strings.Cut(body, "<h2>Docs</h2>")
	docs, learn, ok2 := strings.Cut(rest, "<h2>Learn</h2>")
	if !ok1 || !ok2 || !strings.Contains(docs, "Go&#39;s standard library") || strings.Contains(docs, "Go tour") ||
		!strings.Contains(learn, "for the Go tour") || strings.Contains(learn, "standard library") {
		t.Errorf("notes:\n%s", body)
	}
}

func TestIcon(t *testing.T) {
	png := []byte("\x89PNG fake icon")
	var hits atomic.Int32
	catalogSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/catalog/v2/illustration/u1/" || r.URL.Query().Get("size") != "48" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	defer catalogSrv.Close()
	m := newManager(t)
	m.Cfg.CatalogURL = catalogSrv.URL + "/catalog/v2"
	h := New(m, "").Handler()

	for range 2 { // fetched once, then from the cache
		if w := get(t, h, "GET", "/_zimon/icon/u1"); w.Code != http.StatusOK || w.Body.String() != string(png) || w.Header().Get("Content-Type") != "image/png" {
			t.Fatalf("icon: %d %q", w.Code, w.Body.String())
		}
	}
	for range 2 { // a missing icon is not asked for again right away
		if w := get(t, h, "GET", "/_zimon/icon/u2"); w.Code != http.StatusNotFound {
			t.Errorf("missing icon: %d", w.Code)
		}
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("catalog asked %d times, want 2", n)
	}
	if w := get(t, h, "GET", "/_zimon/icon/..%2f..%2fsecret"); w.Code != http.StatusNotFound {
		t.Errorf("odd id: %d", w.Code)
	}
}

func TestBookName(t *testing.T) {
	for path, want := range map[string]string{
		"wikipedia_en_all_maxi_2026-08.zim": "wikipedia_en_all_maxi",
		"sub/devdocs_en_go_2026-10.zim":     "devdocs_en_go",
		"mine.zim":                          "mine",
		"odd_2026-1.zim":                    "odd_2026-1",
	} {
		if got := BookName(path); got != want {
			t.Errorf("BookName(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestProxy(t *testing.T) {
	var lastPath atomic.Value
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastPath.Store(r.URL.RequestURI())
		switch r.URL.Path {
		case "/content/x/":
			http.Redirect(w, r, "http://"+r.Host+"/content/x/index.html", http.StatusFound) // as kiwix-serve does
		case "/elsewhere":
			http.Redirect(w, r, "https://example.org/", http.StatusFound)
		default:
			_, _ = io.WriteString(w, "kiwix "+r.URL.Path)
		}
	}))
	defer up.Close()
	s := New(newManager(t), "kiwix-serve")
	h := s.Handler()

	if w := get(t, h, "GET", "/content/x/index.html"); w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Errorf("while kiwix-serve is down: %d", w.Code)
	}
	u, _ := url.Parse(up.URL)
	s.upstream.Store(u)
	if w := get(t, h, "GET", "/content/x/index.html"); w.Body.String() != "kiwix /content/x/index.html" {
		t.Errorf("book page: %d %q", w.Code, w.Body.String())
	}
	if w := get(t, h, "GET", "/content/x/"); w.Header().Get("Location") != "/content/x/index.html" {
		t.Errorf("redirect to %q, want a path on this server", w.Header().Get("Location"))
	}
	if w := get(t, h, "GET", "/elsewhere"); w.Header().Get("Location") != "https://example.org/" {
		t.Errorf("an outside redirect was changed: %q", w.Header().Get("Location"))
	}
	if w := get(t, h, "GET", "/search?pattern=go&books.filter.lang=eng"); lastPath.Load() != "/search?pattern=go&books.filter.lang=eng" || w.Code != http.StatusOK {
		t.Errorf("search went to %v", lastPath.Load())
	}
	if get(t, h, "GET", "/library"); lastPath.Load() != "/" {
		t.Errorf("/library went to %v, want kiwix-serve's /", lastPath.Load())
	}
	if w := get(t, h, "POST", "/content/x/index.html"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", w.Code)
	}
	if w := get(t, h, "GET", "/_zimon/portal.css"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "--accent") {
		t.Errorf("static file: %d", w.Code)
	}
}

func TestStatus(t *testing.T) {
	for _, c := range []struct {
		bin  string
		up   bool
		want string
	}{{"", false, "missing"}, {"kiwix-serve", false, "down"}, {"kiwix-serve", true, "up"}} {
		s := New(newManager(t), c.bin)
		if c.up {
			s.upstream.Store(&url.URL{Scheme: "http", Host: "127.0.0.1:1"})
		}
		w := get(t, s.Handler(), "GET", "/api/v1/status")
		var got api.Status
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK || !got.OK || got.Kiwix != c.want || got.API != api.Version {
			t.Errorf("%+v: %d %s", c, w.Code, w.Body.String())
		}
	}
}

// serve runs s on a free port until the test ends.
func serve(t *testing.T, s *Server) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, ln, nil) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	})
	base := "http://" + ln.Addr().String()
	waitFor(t, "the server to answer", func() bool { // Run has done its start-up then
		resp, err := http.Get(base + "/api/v1/status")
		if err == nil {
			resp.Body.Close()
		}
		return err == nil && resp.StatusCode == http.StatusOK
	})
	return base
}

// waitFor polls fn until it is true or 10 seconds pass.
func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if fn() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestSupervisorRestartsKiwix(t *testing.T) {
	s := New(newManager(t), "kiwix-serve")
	s.backoff = func(int) time.Duration { return 10 * time.Millisecond }
	var starts atomic.Int32
	crash := make(chan func(), 1)
	s.start = func(ctx context.Context, addr string) (*proc, error) {
		n := starts.Add(1)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, err
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "run "+strconv.Itoa(int(n)))
		})}
		go func() { _ = srv.Serve(ln) }()
		p := &proc{done: make(chan struct{})}
		var once sync.Once
		stop := func() { once.Do(func() { srv.Close(); close(p.done) }) }
		go func() { <-ctx.Done(); stop() }() // as exec.CommandContext does
		select {
		case crash <- stop:
		default:
		}
		return p, nil
	}
	base := serve(t, s)
	read := func() string {
		resp, err := http.Get(base + "/content/x")
		if err != nil {
			return ""
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	waitFor(t, "kiwix-serve to start", func() bool { return read() == "run 1" })
	(<-crash)()
	waitFor(t, "kiwix-serve to start again", func() bool { return read() == "run 2" })
	if n := starts.Load(); n != 2 {
		t.Errorf("started %d times", n)
	}
}
