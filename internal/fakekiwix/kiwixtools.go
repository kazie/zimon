package fakekiwix

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// KiwixToolsArchive is the kiwix-tools archive the fake serves at
// /release/kiwix-tools/<KiwixToolsArchive>.
const KiwixToolsArchive = "kiwix-tools_fake-3.8.2.tar.gz"

// KiwixTools is that archive: a kiwix-serve that prints its arguments, beside
// a kiwix-manage, in a release folder as Kiwix's archives have them. It is
// the same every time, so tests can pin its sha-256.
func KiwixTools() []byte { return slices.Clone(kiwixTools()) }

var kiwixTools = sync.OnceValue(func() []byte {
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, f := range []struct{ name, body string }{
		{"kiwix-manage", "#!/bin/sh\necho kiwix-manage\n"},
		{"kiwix-serve", "#!/bin/sh\necho \"kiwix-serve $*\"\n"},
	} {
		_ = tw.WriteHeader(&tar.Header{Name: "kiwix-tools_fake-3.8.2/" + f.name, Mode: 0o755, Size: int64(len(f.body)),
			ModTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(f.body))
	}
	tw.Close()
	gz.Close()
	return b.Bytes()
})

// BreakKiwixTools makes the kiwix-tools archive arrive with one byte wrong
// (broken true), or whole again.
func (s *Server) BreakKiwixTools(broken bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.badTools = broken
}

// serveKiwixTools serves KiwixTools; each download counts as a hit.
func (s *Server) serveKiwixTools(w http.ResponseWriter, r *http.Request) {
	if strings.TrimPrefix(r.URL.Path, "/release/kiwix-tools/") != KiwixToolsArchive {
		http.NotFound(w, r)
		return
	}
	body := KiwixTools()
	s.mu.Lock()
	s.hits["kiwix-tools"]++
	if s.badTools {
		body[len(body)/2] ^= 0xff
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/gzip")
	_, _ = w.Write(body)
}
