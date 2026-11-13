package server

import (
	"cmp"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/config"
)

// icons fetches each book's 48px icon from its catalog the first time
// the portal shows it, and keeps it in the state dir, so the portal works
// offline afterwards. (kiwix-serve only knows icons library.xml carries.)
type icons struct {
	mu     sync.Mutex
	missed map[string]time.Time // recent failures, not retried for a while
}

const iconRetry = time.Hour

func (s *Server) icon(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	if !catalog.SafeID(id) { // nothing that could leave the icons folder
		http.NotFound(w, r)
		return
	}
	// Fetch it even if the reader leaves the page: it is kept for next time.
	data := s.iconData(context.WithoutCancel(r.Context()), id)
	if data == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "max-age=604800") // a release's icon doesn't change
	_, _ = w.Write(data)
}

// iconData is the icon of catalog entry id: from the cache, else fetched
// and kept. It is nil when there is none to be had right now.
func (s *Server) iconData(ctx context.Context, id string) []byte {
	path := filepath.Join(s.Manager.Cfg.StateDir, "icons", id+".png")
	if data, err := os.ReadFile(path); err == nil {
		return data
	}
	// Anyone may ask: only the library's books are fetched, or remembered.
	if f, err := s.Manager.Store.FileByUUID(ctx, id); err != nil || f == nil {
		return nil
	}
	s.icons.mu.Lock()
	if t, ok := s.icons.missed[id]; ok && time.Since(t) < iconRetry {
		s.icons.mu.Unlock()
		return nil
	}
	s.icons.mu.Unlock()

	data := s.fetchIcon(ctx, id)
	if data == nil {
		s.icons.mu.Lock()
		if s.icons.missed == nil {
			s.icons.missed = map[string]time.Time{}
		}
		if len(s.icons.missed) >= 1000 { // the misses that still count
			for old, t := range s.icons.missed {
				if time.Since(t) >= iconRetry {
					delete(s.icons.missed, old)
				}
			}
		}
		s.icons.missed[id] = time.Now()
		s.icons.mu.Unlock()
		return nil
	}
	if err := config.WriteFileAtomic(path, data, 0o644); err != nil {
		s.logf("keeping book icon: %v", err) // shown anyway, just fetched again next time
	}
	return data
}

// fetchIcon asks a book's catalog for its icon (Kiwix's for a release no
// catalog lists any more); nil when it has none, or can't be reached, and
// for a ZIM of [sources], which no catalog has.
func (s *Server) fetchIcon(ctx context.Context, id string) []byte {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	base := s.Manager.Cfg.CatalogURL
	if e, err := s.Manager.Store.EntryByUUID(ctx, id); err == nil && e != nil {
		if e.Source != "" {
			return nil
		}
		base = cmp.Or(e.Catalog, base)
	}
	u := strings.TrimRight(base, "/") + "/illustration/" + id + "/?size=48"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	resp, err := s.Manager.Client().Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "image/png") {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10+1))
	if err != nil || len(data) > 256<<10 {
		return nil // a 48px icon is a few kB
	}
	return data
}
