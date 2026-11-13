package server

import (
	"errors"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kazie/zimon/internal/store"
)

// The admin page has its own port: books are served from the main one, and
// a script inside a book must not be able to reach the admin page or use
// its session. Browsers send cookies to every port of a host, so the main
// port ignores the session and takes API tokens only.
//
// The admin page signs in with a token and keeps a session cookie. Sessions
// live in memory: a restart signs everyone out.
const (
	sessionCookie = "zimon_session"
	sessionTTL    = 30 * 24 * time.Hour
	codeTTL       = 10 * time.Minute
	maxFails      = 5 // wrong tokens per address…
	failWindow    = 10 * time.Minute
	maxTracked    = 10000 // addresses with wrong tokens kept in memory
)

var (
	loginTmpl     = template.Must(template.ParseFS(webFS, "web/login.html"))
	elsewhereTmpl = template.Must(template.New("").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>zimon admin</title><link rel="stylesheet" href="/_zimon/portal.css"></head>
<body><main><p>{{if .Proxied}}The admin page has an address of its own, so scripts inside books can't
reach it: through this reverse proxy, a host name of its own.
{{else if .Link}}The admin page has its own port, so scripts inside books can't reach it:
<a href="{{.Link}}">{{.Link}}</a>. Behind a reverse proxy, give it its own host name.
{{else}}This server has no admin page.{{end}}</p></main></body></html>
`))
)

type sessions struct {
	mu    sync.Mutex
	m     map[string]session     // by cookie value
	codes map[string]time.Time   // one-time sign-in codes, until when they work
	fails map[string][]time.Time // wrong tokens, by address
}

type session struct {
	name    string // the token's name, or "local" for a one-time link
	hash    string // the token's hash: removing the token ends the session
	expires time.Time
}

// adminRoutes adds the admin page, for the admin port's mux.
func (s *Server) adminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	})
	mux.HandleFunc("GET /admin", s.adminPage)
	mux.HandleFunc("POST /admin/login", s.adminLogin)
	mux.HandleFunc("GET /admin/login", s.adminCodeLogin)
	mux.HandleFunc("POST /admin/logout", s.adminLogout)
}

// adminElsewhere answers /admin on the main port with a link to the admin
// port. Through a reverse proxy that link would be wrong (the admin page
// has a host name of its own there), so it only says so.
func (s *Server) adminElsewhere(w http.ResponseWriter, r *http.Request) {
	var page struct {
		Link    string
		Proxied bool
	}
	if port := s.adminPort.Load(); port != 0 {
		page.Proxied = proxied(r)
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		page.Link = "http://" + net.JoinHostPort(host, strconv.Itoa(int(port))) + "/admin"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_ = elsewhereTmpl.Execute(w, page)
}

// proxied reports whether r came through a reverse proxy, which says so in
// a forwarding header (nginx and Caddy both set X-Forwarded-For). Only a
// hint depends on it, so a faked header does no harm.
func proxied(r *http.Request) bool {
	for _, h := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	return false
}

// LoginLink makes a one-time path that signs in without a token, for
// `zimon serve` to print when only this computer can reach it.
func (s *Server) LoginLink() string {
	code := randomID()
	s.sessions.mu.Lock()
	s.sweepLocked()
	s.sessions.codes[code] = time.Now().Add(codeTTL)
	s.sessions.mu.Unlock()
	return "/admin/login?code=" + code
}

// session is the signed-in session a request carries, if any.
func (s *Server) session(r *http.Request) (session, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return session{}, false
	}
	s.sessions.mu.Lock()
	ss, ok := s.sessions.m[c.Value]
	if ok && time.Now().After(ss.expires) {
		delete(s.sessions.m, c.Value)
		ok = false
	}
	s.sessions.mu.Unlock()
	if ok && ss.hash != "" {
		// The token may have been removed since (zimon token remove).
		tok, err := s.Manager.Store.TokenByHash(r.Context(), ss.hash)
		if err != nil || tok == nil {
			return session{}, false
		}
	}
	return ss, ok
}

// sweepLocked forgets expired sessions and codes, and old wrong tokens.
func (s *Server) sweepLocked() {
	now := time.Now()
	for id, ss := range s.sessions.m {
		if now.After(ss.expires) {
			delete(s.sessions.m, id)
		}
	}
	for code, until := range s.sessions.codes {
		if now.After(until) {
			delete(s.sessions.codes, code)
		}
	}
	for addr := range s.sessions.fails {
		s.recentFailsLocked(addr)
	}
}

func (s *Server) adminPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.session(r); !ok {
		s.loginPage(w, http.StatusOK, "")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	data, _ := webFS.ReadFile("web/admin.html")
	_, _ = w.Write(data)
}

func (s *Server) loginPage(w http.ResponseWriter, code int, problem string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_ = loginTmpl.Execute(w, map[string]string{"Problem": problem})
}

// POST /admin/login: sign in with a token from `zimon token add`.
func (s *Server) adminLogin(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		s.loginPage(w, http.StatusForbidden, "Sign in from this page.")
		return
	}
	hash := hashToken(strings.TrimSpace(r.PostFormValue("token")))
	tok, err := s.checkToken(r, hash)
	switch {
	case errors.Is(err, errTooManyFails):
		s.loginPage(w, http.StatusTooManyRequests, "Too many wrong tokens. Try again in a few minutes.")
		return
	case err != nil:
		s.loginPage(w, http.StatusInternalServerError, err.Error())
		return
	case tok == nil:
		s.loginPage(w, http.StatusUnauthorized, "That token does not work. Make one with: zimon token add NAME")
		return
	}
	s.startSession(w, r, session{name: tok.Name, hash: hash})
}

// GET /admin/login?code=…: a one-time link from `zimon serve`.
func (s *Server) adminCodeLogin(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	s.sessions.mu.Lock()
	until, ok := s.sessions.codes[code]
	delete(s.sessions.codes, code) // once only, used or not
	s.sessions.mu.Unlock()
	if !ok || time.Now().After(until) {
		s.loginPage(w, http.StatusUnauthorized, "That sign-in link has been used or is too old. Sign in with a token instead.")
		return
	}
	s.startSession(w, r, session{name: "local"})
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, ss session) {
	id := randomID()
	ss.expires = time.Now().Add(sessionTTL)
	s.sessions.mu.Lock()
	s.sweepLocked()
	s.sessions.m[id] = ss
	s.sessions.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: id, Path: "/", MaxAge: int(sessionTTL.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", // behind an HTTPS proxy
	})
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// POST /admin/logout
func (s *Server) adminLogout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "sign out from the admin page", http.StatusForbidden)
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.sessions.mu.Lock()
		delete(s.sessions.m, c.Value)
		s.sessions.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// sameOrigin reports whether a browser sent the request from one of this
// server's own pages. Browsers send cookies to every port of a host, so
// this, not SameSite, keeps a page on the main port from posting here.
// Requests without these headers don't come from a browser, and pass.
func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin"
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		return err == nil && u.Host == r.Host
	}
	return true
}

// clientAddr is who is asking, for the wrong-token limit: the peer, and
// when the peer is a proxy on this machine or a private network (in another
// container, say), the last X-Forwarded-For entry with it, which the proxy
// added (Caddy does by default; nginx with proxy_set_header X-Forwarded-For
// $proxy_add_x_forwarded_for). The peer is part of it, so a client on the
// network that fakes the header only ever counts against itself, never
// locks out the address it names.
func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		if fwd := r.Header.Values("X-Forwarded-For"); len(fwd) > 0 {
			list := strings.Split(fwd[len(fwd)-1], ",")
			if last := strings.TrimSpace(list[len(list)-1]); last != "" {
				return host + " for " + last
			}
		}
	}
	return host
}

var errTooManyFails = errors.New("too many wrong tokens; try again in a few minutes")

// checkToken finds the token with this hash, or nil. A wrong one counts
// against the request's address, and past maxFails within failWindow that
// address gets errTooManyFails, right token or not.
func (s *Server) checkToken(r *http.Request, hash string) (*store.Token, error) {
	addr := clientAddr(r)
	if s.tooManyFails(addr) {
		return nil, errTooManyFails
	}
	tok, err := s.Manager.Store.UseToken(r.Context(), hash)
	if err == nil && tok == nil {
		s.failed(addr)
	}
	return tok, err
}

func (s *Server) tooManyFails(addr string) bool {
	s.sessions.mu.Lock()
	defer s.sessions.mu.Unlock()
	return s.recentFailsLocked(addr) >= maxFails
}

// recentFailsLocked counts addr's wrong tokens within failWindow, and
// forgets older ones.
func (s *Server) recentFailsLocked(addr string) int {
	recent := s.sessions.fails[addr][:0]
	for _, t := range s.sessions.fails[addr] {
		if time.Since(t) < failWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) == 0 {
		delete(s.sessions.fails, addr)
	} else {
		s.sessions.fails[addr] = recent
	}
	return len(recent)
}

func (s *Server) failed(addr string) {
	s.sessions.mu.Lock()
	s.sweepLocked()
	if _, ok := s.sessions.fails[addr]; ok || len(s.sessions.fails) < maxTracked {
		s.sessions.fails[addr] = append(s.sessions.fails[addr], time.Now())
	}
	s.sessions.mu.Unlock()
}
