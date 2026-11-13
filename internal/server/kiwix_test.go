package server

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kazie/zimon/internal/store"
	"github.com/kazie/zimon/internal/zim"
)

// TestRealKiwixServe runs the whole server with the real kiwix-serve on a
// tiny ZIM (made with zimwriterfs): book pages, redirects, search, random
// articles and the portal, all through zimon. It needs kiwix-serve on PATH,
// as in the dev shell and `nix flake check`.
func TestRealKiwixServe(t *testing.T) {
	bin, err := exec.LookPath("kiwix-serve")
	if err != nil {
		t.Skip("kiwix-serve is not on PATH")
	}
	ctx := context.Background()
	m := newManager(t)
	const name = "hello_en_all_2026-11.zim"
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(m.Cfg.LibraryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.Cfg.LibraryDir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	hdr, err := zim.ReadFile(filepath.Join(m.Cfg.LibraryDir, name))
	if err != nil {
		t.Fatal(err)
	}
	f := store.File{UUID: "hello", ZimUUID: hdr.UUID, Name: "hello_en_all", Path: name, Size: int64(len(data)),
		Title: "Hello", Summary: "A tiny test ZIM", Language: "eng", Date: "2026-11-08"}
	if err := m.Store.InstallFile(ctx, f); err != nil {
		t.Fatal(err)
	}
	// Only the real ZIM: the other test rows have no files behind them.
	for _, other := range []string{"u1", "u2", "u3"} {
		if err := m.Store.DeleteFile(ctx, other); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.WriteLibraryXML(ctx); err != nil {
		t.Fatal(err)
	}

	s := New(m, bin)
	s.Logf = t.Logf
	base := serve(t, s)
	fetch := func(path string) (int, string) {
		t.Helper()
		resp, err := http.Get(base + path) // follows redirects, which must stay on this server
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	waitFor(t, "kiwix-serve", func() bool {
		_, body := fetch("/api/v1/status")
		return strings.Contains(body, `"kiwix":"up"`)
	})

	for _, c := range []struct{ path, want string }{
		{"/content/hello_en_all/", "Hello zimon"},
		{"/random?content=hello_en_all", "cooking"},
		{"/search?pattern=cooking&books.filter.lang=eng", "/content/hello_en_all"},
		{"/", `/viewer#hello_en_all`},
		{"/viewer", "<html"},
		{"/library", "<html"},
	} {
		if code, body := fetch(c.path); code != http.StatusOK || !strings.Contains(body, c.want) {
			t.Errorf("%s: %d, want %q in:\n%.400s", c.path, code, c.want, body)
		}
	}
}
