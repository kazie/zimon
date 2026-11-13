package server

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/backend"
	"github.com/kazie/zimon/internal/collections"
	"github.com/kazie/zimon/internal/library"
)

// authFunc guards an API handler; see tokenAuth and adminAuth.
type authFunc func(http.HandlerFunc) http.Handler

// apiRoutes adds the HTTP API under /api/v1. Everything but the status
// needs a token: "Authorization: Bearer zimon_…" (see `zimon token`).
func (s *Server) apiRoutes(mux *http.ServeMux, auth authFunc) {
	mux.HandleFunc("GET /api/v1/status", s.status)
	mux.Handle("GET /api/v1/info", auth(s.apiInfo))
	mux.Handle("GET /api/v1/items", auth(s.apiItems))
	mux.Handle("GET /api/v1/collections", auth(s.apiCollections))
	mux.Handle("GET /api/v1/collections/{id}", auth(s.apiCollection))
	mux.Handle("GET /api/v1/search", auth(s.apiSearch))
	mux.Handle("GET /api/v1/entries/{name}", auth(s.apiEntries))
	mux.Handle("GET /api/v1/categories", auth(s.apiFacets("categories")))
	mux.Handle("GET /api/v1/languages", auth(s.apiFacets("languages")))
	mux.Handle("GET /api/v1/budget", auth(s.apiBudget))
	mux.Handle("GET /api/v1/plan", auth(s.apiPlan))
	mux.Handle("GET /api/v1/files/{uuid}", auth(s.apiFile))
	s.jobRoutes(mux, auth)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "no such API endpoint")
	})
}

// tokenAuth lets a request through with a valid token. Too many wrong
// tokens from one address lock it out for a while, as on the sign-in page.
func (s *Server) tokenAuth(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r)
		if token == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="zimon"`)
			writeError(w, http.StatusUnauthorized, "a token is needed: Authorization: Bearer <token> (see: zimon token add)")
			return
		}
		tok, err := s.checkToken(r, hashToken(token))
		switch {
		case errors.Is(err, errTooManyFails):
			writeError(w, http.StatusTooManyRequests, err.Error())
			return
		case err != nil:
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		case tok == nil:
			w.Header().Set("WWW-Authenticate", `Bearer realm="zimon", error="invalid_token"`)
			writeError(w, http.StatusUnauthorized, "unknown token")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), tokenKey{}, tok.Name)))
	})
}

// adminAuth is tokenAuth that also takes the admin page's session, on the
// admin port. A change from the page must come from the page itself
// (sameOrigin) and carry "X-Requested-With: zimon", which a plain form
// cannot send.
func (s *Server) adminAuth(h http.HandlerFunc) http.Handler {
	withToken := s.tokenAuth(h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bearer(r) != "" {
			withToken.ServeHTTP(w, r)
			return
		}
		ss, ok := s.session(r)
		if !ok {
			withToken.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && (r.Header.Get("X-Requested-With") != "zimon" || !sameOrigin(r)) {
			writeError(w, http.StatusForbidden, "changes come from the admin page, with X-Requested-With: zimon")
			return
		}
		// A read too: a page on another port (a book's) makes the browser
		// send the cookie, and some reads look at the network ([sources]).
		if site := r.Header.Get("Sec-Fetch-Site"); site == "same-site" || site == "cross-site" {
			writeError(w, http.StatusForbidden, "the admin page's API answers the admin page")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), tokenKey{}, ss.name)))
	})
}

type tokenKey struct{}

// tokenName is the name of the token a request came with.
func tokenName(r *http.Request) string {
	name, _ := r.Context().Value(tokenKey{}).(string)
	return name
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, api.Error{Error: msg})
}

// library answers reads and previews the same way the CLI does here. Jobs
// don't Run through it: runJob takes the state lock itself.
func (s *Server) library() *backend.Local {
	return &backend.Local{Manager: func() (*library.Manager, error) { return s.Manager, nil }, Version: s.Version}
}

// answer writes v, or err as a server error.
func answer(w http.ResponseWriter, v any, err error) {
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// GET /api/v1/info: this zimon and its library. A client checks the API
// version here.
func (s *Server) apiInfo(w http.ResponseWriter, r *http.Request) {
	info, err := s.library().Info(r.Context())
	answer(w, info, err)
}

// GET /api/v1/items: everything zimon knows locally, as `zimon list --json`.
func (s *Server) apiItems(w http.ResponseWriter, r *http.Request) {
	items, err := s.library().Items(r.Context())
	answer(w, items, err)
}

// GET /api/v1/collections: every collection, its sizes and your level.
func (s *Server) apiCollections(w http.ResponseWriter, r *http.Request) {
	cols, err := s.library().Collections(r.Context())
	answer(w, cols, err)
}

// GET /api/v1/collections/{id}: what a collection holds, at your level or
// standard.
func (s *Server) apiCollection(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := collections.Get(s.Manager.Collections, id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	d, err := s.library().Collection(r.Context(), id)
	answer(w, d, err)
}

// GET /api/v1/search?q=…&lang=eng&lang=jpn&category=…&flavour=…&max_size=…&limit=50:
// the catalog, best matches first. X-Total-Count says how many match in
// all; limit=0 returns them all. With prefix=… instead of q, names that
// start with it.
func (s *Server) apiSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	maxSize, _ := strconv.ParseInt(q.Get("max_size"), 10, 64)
	limit, err := strconv.Atoi(q.Get("limit")) // 0: all
	if err != nil || limit < 0 {
		limit = 50
	}
	limit = min(limit, 1000)
	es, total, err := s.library().Search(r.Context(), api.Query{
		Text: q.Get("q"), Prefix: q.Get("prefix"), Languages: q["lang"], Category: q.Get("category"), Flavour: q.Get("flavour"),
		MaxSize: maxSize, Limit: limit,
	})
	if err == nil {
		w.Header().Set("X-Total-Count", strconv.Itoa(total))
	}
	answer(w, es, err)
}

// GET /api/v1/categories and /api/v1/languages: the catalog's facets, with
// how many entries have each.
func (s *Server) apiFacets(what string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fs, err := s.library().Facets(r.Context(), what)
		answer(w, fs, err)
	}
}

// GET /api/v1/entries/{name}: every flavour of a ZIM in the catalog. A
// name not in it gets 404, with names like it.
func (s *Server) apiEntries(w http.ResponseWriter, r *http.Request) {
	es, similar, err := s.library().Entries(r.Context(), r.PathValue("name"))
	if err == nil && len(es) == 0 {
		writeJSON(w, http.StatusNotFound, api.Error{Error: "not in the catalog", Similar: similar})
		return
	}
	answer(w, es, err)
}

// GET /api/v1/budget: what the library takes, by collection, against the
// budget and the free disk.
func (s *Server) apiBudget(w http.ResponseWriter, r *http.Request) {
	b, err := s.library().Budget(r.Context())
	answer(w, b, err)
}

// GET /api/v1/plan: what `zimon apply` would do now.
func (s *Server) apiPlan(w http.ResponseWriter, r *http.Request) {
	if err := s.ensureCatalog(r.Context(), false); err != nil { // as apply will
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	pv, err := s.library().Preview(r.Context(), "apply", api.Request{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pv.Plan)
}

// GET /api/v1/files/{uuid}: a ZIM on disk, with Range support, so another
// zimon can take a copy (resumable, and checked against Kiwix's sha-256).
func (s *Server) apiFile(w http.ResponseWriter, r *http.Request) {
	f, err := s.Manager.Store.FileByUUID(r.Context(), r.PathValue("uuid"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if f == nil {
		writeError(w, http.StatusNotFound, "no such file")
		return
	}
	fh, err := os.Open(filepath.Join(s.Manager.Cfg.LibraryDir, f.Path))
	if err != nil {
		writeError(w, http.StatusNotFound, "the file is not on disk")
		return
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(f.Path)}))
	if f.SHA256 != "" {
		w.Header().Set("X-Zimon-Sha256", f.SHA256) // hex, as Kiwix's .meta4 has it
	}
	http.ServeContent(w, r, filepath.Base(f.Path), st.ModTime(), fh)
}
