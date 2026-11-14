package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// login posts a token to the sign-in form, from addr.
func login(h http.Handler, token, addr string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(url.Values{"token": {token}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = addr
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// withCookie sends a request with the session cookie from a sign-in.
func withCookie(h http.Handler, method, path string, signedIn *httptest.ResponseRecorder, header ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	for _, c := range signedIn.Result().Cookies() {
		r.AddCookie(c)
	}
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// apiGetFrom is apiGet from addr.
func apiGetFrom(t *testing.T, h http.Handler, token, path, addr string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	r.RemoteAddr = addr
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// newAdmin is newAPI with the admin port's handler.
func newAdmin(t *testing.T) (*Server, http.Handler, string) {
	s, _, token := newAPI(t)
	return s, s.AdminHandler(), token
}

func TestAdminSignIn(t *testing.T) {
	s, h, token := newAdmin(t)
	if w := get(t, h, http.MethodGet, "/admin"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `name="token"`) {
		t.Errorf("signed out: %d", w.Code)
	}
	w := login(h, token, "192.0.2.1:1234")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin" {
		t.Fatalf("sign in: %d", w.Code)
	}
	c := w.Result().Cookies()[0]
	if c.Name != sessionCookie || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Secure {
		t.Errorf("cookie: %+v", c)
	}
	if page := withCookie(h, http.MethodGet, "/admin", w); !strings.Contains(page.Body.String(), "admin.js") {
		t.Errorf("signed in: %d", page.Code)
	}

	// The session works for the API; changes need the extra header.
	if r := withCookie(h, http.MethodGet, "/api/v1/items", w); r.Code != http.StatusOK {
		t.Errorf("items with a session: %d", r.Code)
	}
	if r := withCookie(h, http.MethodPost, "/api/v1/apply", w); r.Code != http.StatusForbidden {
		t.Errorf("change without X-Requested-With: %d", r.Code)
	}
	if r := withCookie(h, http.MethodPost, "/api/v1/apply", w, "X-Requested-With", "zimon"); r.Code != http.StatusAccepted || !strings.Contains(r.Body.String(), `"by":"test"`) {
		t.Errorf("change with it: %d %s", r.Code, r.Body.String())
	}

	// A book page can't frame the admin page.
	if r := withCookie(h, http.MethodGet, "/admin", w); r.Header().Get("X-Frame-Options") != "DENY" ||
		!strings.Contains(r.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Errorf("/admin can be framed: %v", r.Header())
	}

	// The main port, which serves books, ignores the session, and only
	// points to the admin port.
	main := s.Handler()
	if r := withCookie(main, http.MethodGet, "/api/v1/items", w); r.Code != http.StatusUnauthorized {
		t.Errorf("items with a session on the main port: %d", r.Code)
	}
	if r := withCookie(main, http.MethodGet, "/admin", w); r.Code != http.StatusNotFound || strings.Contains(r.Body.String(), "admin.js") {
		t.Errorf("/admin on the main port: %d", r.Code)
	}
	// A page on another port of the same host can't post changes.
	if r := withCookie(h, http.MethodPost, "/api/v1/apply", w, "X-Requested-With", "zimon", "Sec-Fetch-Site", "same-site"); r.Code != http.StatusForbidden {
		t.Errorf("change from another port: %d", r.Code)
	}
	if r := withCookie(h, http.MethodPost, "/api/v1/apply", w, "X-Requested-With", "zimon", "Origin", "http://example.com:8080"); r.Code != http.StatusForbidden {
		t.Errorf("change from another origin: %d", r.Code)
	}
	// Nor read through the session, though the admin page and a typed URL can.
	if r := withCookie(h, http.MethodGet, "/api/v1/plan", w, "Sec-Fetch-Site", "same-site"); r.Code != http.StatusForbidden {
		t.Errorf("read from another port: %d", r.Code)
	}
	for _, site := range []string{"same-origin", "none"} {
		if r := withCookie(h, http.MethodGet, "/api/v1/items", w, "Sec-Fetch-Site", site); r.Code != http.StatusOK {
			t.Errorf("read, Sec-Fetch-Site %s: %d", site, r.Code)
		}
	}

	// Signing out ends the session.
	withCookie(h, http.MethodPost, "/admin/logout", w)
	if r := withCookie(h, http.MethodGet, "/api/v1/items", w); r.Code != http.StatusUnauthorized {
		t.Errorf("after sign-out: %d", r.Code)
	}

	// Behind an HTTPS proxy, the cookie is Secure.
	r := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader("token="+url.QueryEscape(token)))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Forwarded-Proto", "https")
	sw := httptest.NewRecorder()
	h.ServeHTTP(sw, r)
	if c := sw.Result().Cookies(); len(c) == 0 || !c[0].Secure {
		t.Errorf("cookie behind https: %+v", c)
	}
}

// Removing a token ends the sessions it signed in.
func TestAdminSessionEndsWithItsToken(t *testing.T) {
	s, h, token := newAdmin(t)
	w := login(h, "  "+token+"\n", "192.0.2.1:1") // pasted with spaces around it
	if w.Code != http.StatusSeeOther {
		t.Fatalf("sign in: %d", w.Code)
	}
	if r := withCookie(h, http.MethodGet, "/api/v1/items", w); r.Code != http.StatusOK {
		t.Fatalf("items: %d", r.Code)
	}
	if ok, err := s.Manager.Store.RemoveToken(context.Background(), "test"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if r := withCookie(h, http.MethodGet, "/api/v1/items", w); r.Code != http.StatusUnauthorized {
		t.Errorf("items after the token was removed: %d", r.Code)
	}
}

func TestAdminSignInLimits(t *testing.T) {
	s, h, token := newAdmin(t)
	for range maxFails {
		if w := login(h, "zimon_wrong", "192.0.2.1:1"); w.Code != http.StatusUnauthorized {
			t.Fatalf("wrong token: %d", w.Code)
		}
	}
	if w := login(h, token, "192.0.2.1:1"); w.Code != http.StatusTooManyRequests {
		t.Errorf("after %d wrong tokens: %d", maxFails, w.Code)
	}
	if w := login(h, token, "192.0.2.2:1"); w.Code != http.StatusSeeOther {
		t.Errorf("another address: %d", w.Code)
	}
	// Behind a proxy, the last X-Forwarded-For entry (the one the proxy
	// added) tells readers apart; from anyone else, the header is ignored.
	// The peer stays part of it: a client on the network naming another
	// address only counts against itself.
	for _, c := range []struct{ peer, header, want string }{
		{"127.0.0.1:5000", "10.0.0.1, 198.51.100.8", "127.0.0.1 for 198.51.100.8"},
		{"10.88.0.5:5000", "198.51.100.9", "10.88.0.5 for 198.51.100.9"},       // a proxy in another container
		{"192.168.1.20:5000", "192.168.1.10", "192.168.1.20 for 192.168.1.10"}, // not 192.168.1.10's count
		{"203.0.113.5:5000", "198.51.100.8", "203.0.113.5"},
		{"127.0.0.1:5000", "", "127.0.0.1"},
	} {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.RemoteAddr = c.peer
		if c.header != "" {
			r.Header.Set("X-Forwarded-For", c.header)
		}
		if got := clientAddr(r); got != c.want {
			t.Errorf("clientAddr from %s with %q = %s, want %s", c.peer, c.header, got, c.want)
		}
	}

	// Wrong API tokens count too.
	for range maxFails {
		apiGetFrom(t, s.Handler(), "zimon_wrong", "/api/v1/items", "192.0.2.3:1")
	}
	if w := apiGetFrom(t, s.Handler(), token, "/api/v1/items", "192.0.2.3:1"); w.Code != http.StatusTooManyRequests {
		t.Errorf("API after %d wrong tokens: %d", maxFails, w.Code)
	}

	// Old failures are forgotten.
	s.signIn.mu.Lock()
	s.signIn.fails["192.0.2.9"] = []time.Time{time.Now().Add(-2 * failWindow)}
	s.sweepLocked()
	_, kept := s.signIn.fails["192.0.2.9"]
	s.signIn.mu.Unlock()
	if kept {
		t.Error("an old failure was kept")
	}
}

func TestAdminLoginLink(t *testing.T) {
	s := New(newManager(t), "")
	h := s.AdminHandler()
	link := s.LoginLink()
	w := get(t, h, http.MethodGet, link)
	if w.Code != http.StatusSeeOther || len(w.Result().Cookies()) == 0 {
		t.Fatalf("link: %d", w.Code)
	}
	// No token to remove can end it, so it lasts a day, not thirty.
	if c := w.Result().Cookies()[0]; c.MaxAge != int(localTTL.Seconds()) {
		t.Errorf("the link's session lasts %ds, want %ds", c.MaxAge, int(localTTL.Seconds()))
	}
	if r := withCookie(h, http.MethodGet, "/api/v1/jobs", w); r.Code != http.StatusOK {
		t.Errorf("session from the link: %d", r.Code)
	}
	if again := get(t, h, http.MethodGet, link); again.Code != http.StatusUnauthorized {
		t.Errorf("the link worked twice: %d", again.Code)
	}
}

// TestAdminElsewhere checks what /admin on the books' port says: a link to
// the admin port, but not through a reverse proxy, where it has its own host.
func TestAdminElsewhere(t *testing.T) {
	for _, c := range []struct {
		name      string
		adminPort int32
		header    string
		want      string
		wantNot   string
	}{
		{"no admin page", 0, "", "has no admin page", "<a "},
		{"direct", 8081, "", `href="http://zim.lan:8081/admin"`, "proxy, a host name"},
		{"behind nginx", 8081, "X-Forwarded-Proto", "a host name of its own", ":8081"},
		{"behind Caddy", 8081, "X-Forwarded-For", "a host name of its own", ":8081"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _, _ := newAPI(t)
			s.adminPort.Store(c.adminPort)
			r := httptest.NewRequest(http.MethodGet, "http://zim.lan:8080/admin", nil)
			if c.header != "" {
				r.Header.Set(c.header, "https")
			}
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			body := w.Body.String()
			if w.Code != http.StatusNotFound || !strings.Contains(body, c.want) || strings.Contains(body, c.wantNot) {
				t.Errorf("%d %s", w.Code, body)
			}
		})
	}
}

// A session is in the database: a new server on the same library (after a
// restart or an update) still knows it.
func TestAdminSessionOutlivesTheServer(t *testing.T) {
	s, h, token := newAdmin(t)
	w := login(h, token, "192.0.2.1:1234")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("sign in: %d", w.Code)
	}
	again := New(s.Manager, "").AdminHandler()
	if r := withCookie(again, http.MethodGet, "/api/v1/items", w); r.Code != http.StatusOK {
		t.Errorf("after a restart: %d", r.Code)
	}
	withCookie(again, http.MethodPost, "/admin/logout", w)
	if r := withCookie(h, http.MethodGet, "/api/v1/items", w); r.Code != http.StatusUnauthorized {
		t.Errorf("signed out on one server, still in on the other: %d", r.Code)
	}
}

// A reverse proxy that checks who is asking can sign them in with a token
// in X-Zimon-Admin-Token, on the admin port only. Changes still have to
// come from the admin page.
func TestAdminProxyToken(t *testing.T) {
	s, h, token := newAdmin(t)
	viaProxy := func(h http.Handler, method, path, proxyToken string, header ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.RemoteAddr = "127.0.0.1:4321"
		r.Header.Set("X-Forwarded-For", "198.51.100.7")
		r.Header.Set(adminTokenHeader, proxyToken)
		for i := 0; i+1 < len(header); i += 2 {
			r.Header.Set(header[i], header[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	tests := []struct {
		name, method, path, token string
		header                    []string
		want                      int
		body                      string
	}{
		{name: "the page, signed in", method: "GET", path: "/admin", token: token, want: 200, body: "admin.js"},
		{name: "a read from the page", method: "GET", path: "/api/v1/items", token: token,
			header: []string{"Sec-Fetch-Site", "same-origin"}, want: 200},
		{name: "a read that doesn't say where from", method: "GET", path: "/api/v1/items", token: token, want: 403},
		{name: "a read with the proxy's own Authorization", method: "GET", path: "/api/v1/items", token: token,
			header: []string{"Sec-Fetch-Site", "same-origin", "Authorization", "Bearer sso.jwt.from-the-proxy"}, want: 200},
		{name: "a change from the page", method: "POST", path: "/api/v1/apply", token: token,
			header: []string{"X-Requested-With", "zimon", "Sec-Fetch-Site", "same-origin"}, want: 202, body: `"by":"test"`},
		{name: "a change without X-Requested-With", method: "POST", path: "/api/v1/apply", token: token, want: 403},
		{name: "a change from another site", method: "POST", path: "/api/v1/apply", token: token,
			header: []string{"X-Requested-With", "zimon", "Sec-Fetch-Site", "cross-site"}, want: 403},
		{name: "a wrong token", method: "GET", path: "/admin", token: "zimon_wrong", want: 200, body: `name="token"`},
		{name: "signing out", method: "POST", path: "/admin/logout", token: token, want: 409, body: "sign out there"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := viaProxy(h, tt.method, tt.path, tt.token, tt.header...)
			if w.Code != tt.want || !strings.Contains(w.Body.String(), tt.body) {
				t.Errorf("got %d %.80q, want %d with %q", w.Code, w.Body.String(), tt.want, tt.body)
			}
		})
	}
	// The main port serves books, and ignores the header.
	if w := viaProxy(s.Handler(), "GET", "/api/v1/items", token); w.Code != http.StatusUnauthorized {
		t.Errorf("the header on the main port: %d", w.Code)
	}

	// A proxy token that stopped working is the proxy's mistake: it counts
	// against nobody, and a sign-in on the page still works beside it.
	for range maxFails + 2 {
		viaProxy(h, "GET", "/admin", "zimon_removed")
	}
	s.signIn.mu.Lock()
	fails := len(s.signIn.fails)
	s.signIn.mu.Unlock()
	if fails != 0 {
		t.Errorf("a broken proxy token counted as %d wrong tries", fails)
	}
	signedIn := login(h, token, "127.0.0.1:4321")
	if w := withCookie(h, http.MethodGet, "/admin", signedIn, adminTokenHeader, "zimon_removed"); !strings.Contains(w.Body.String(), "admin.js") {
		t.Errorf("sign in beside a broken proxy token: %d %.80q", w.Code, w.Body.String())
	}
}
