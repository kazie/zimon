// Package server is `zimon serve`: a portal page for the library, and a
// proxy in front of kiwix-serve, which it runs on a private port and
// restarts when it stops.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/download"
	"github.com/kazie/zimon/internal/kiwix"
	"github.com/kazie/zimon/internal/library"
	"github.com/kazie/zimon/internal/lock"
)

// Server serves the portal and passes everything else to kiwix-serve.
type Server struct {
	Manager *library.Manager
	Logf    func(format string, args ...any) // what happens to kiwix-serve; nil: quiet
	Version string                           // zimon's, for GET /api/v1/info
	// Fetch gets kiwix-serve when it isn't installed; Run keeps trying until
	// it works. nil: books can't be opened until zimon serve starts again.
	Fetch func(ctx context.Context) (string, error)

	bin      atomic.Pointer[string]          // kiwix-serve; "" while it is missing
	fetchErr atomic.Pointer[error]           // why Fetch gave up
	retry    func(attempt int) time.Duration // wait before fetch number attempt+1

	start     func(ctx context.Context, addr string) (*proc, error) // runs kiwix-serve; tests replace it
	backoff   func(attempt int) time.Duration                       // wait before restart number attempt
	upstream  atomic.Pointer[url.URL]                               // kiwix-serve, nil while it is down
	adminPort atomic.Int32                                          // the admin page's port, 0 if none
	proxy     *httputil.ReverseProxy
	icons     icons
	jobs      jobs
	sessions  sessions
}

// proc is a running kiwix-serve.
type proc struct {
	done chan struct{} // closed when it exits
	err  error         // why it exited, once done is closed
}

// New is a Server for m, running kiwix-serve from bin ("" if missing).
func New(m *library.Manager, bin string) *Server {
	s := &Server{Manager: m, backoff: download.Backoff, retry: fetchRetry}
	s.bin.Store(&bin)
	s.start = s.startKiwix
	s.jobs.wake = make(chan struct{}, 1)
	s.jobs.subs = map[chan api.Event]struct{}{}
	s.sessions.m = map[string]session{}
	s.sessions.codes = map[string]time.Time{}
	s.sessions.fails = map[string][]time.Time{}
	upstream := http.DefaultTransport.(*http.Transport).Clone()
	upstream.MaxIdleConnsPerHost = 32 // a browser loads a page's parts side by side; 2 makes most of them reconnect
	s.proxy = &httputil.ReverseProxy{
		Transport: upstream,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(pr.In.Context().Value(upstreamKey{}).(*url.URL))
			pr.SetXForwarded()
		},
		ModifyResponse: localRedirects,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() != nil {
				return // the reader left (closed the tab, skipped in a video)
			}
			s.logf("kiwix-serve did not answer %s: %v", r.URL.Path, err)
			s.unavailable(w, http.StatusBadGateway)
		},
	}
	return s
}

type upstreamKey struct{}

// Handler routes requests on the main port: the portal at /, zimon's own
// files under /_zimon/, the API under /api/ (tokens only), kiwix-serve's
// library page at /library, and everything else (books, search, the
// catalog) to kiwix-serve.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.portal)
	s.staticRoutes(mux)
	s.apiRoutes(mux, s.tokenAuth)
	mux.HandleFunc("/admin", s.adminElsewhere)
	mux.HandleFunc("/admin/", s.adminElsewhere)
	mux.HandleFunc("GET /library", func(w http.ResponseWriter, r *http.Request) {
		r2 := r.Clone(r.Context())
		r2.URL.Path, r2.URL.RawPath = "/", ""
		s.forward(w, r2)
	})
	mux.HandleFunc("/", s.forward)
	return mux
}

// AdminHandler routes requests on the admin port: the admin page, zimon's
// own files, and the API, which here also takes the admin page's session.
// No book is served here.
func (s *Server) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	s.adminRoutes(mux)
	s.staticRoutes(mux)
	s.apiRoutes(mux, s.adminAuth)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Books on the main port are the same site: they must not frame
		// the admin page and trick clicks.
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) staticRoutes(mux *http.ServeMux) {
	mux.Handle("GET /_zimon/", http.StripPrefix("/_zimon/", http.FileServerFS(static)))
	mux.HandleFunc("GET /_zimon/icon/{uuid}", s.icon)
}

// keepJobs is how many jobs the history keeps.
const keepJobs = 1000

// Run serves on ln, and the admin page on admin (nil: none), with
// kiwix-serve alongside, until ctx is done or a listener fails.
func (s *Server) Run(ctx context.Context, ln, admin net.Listener) error {
	// Jobs and the schedule belong to one server per state directory.
	unlockServe, ok := lock.Try(s.Manager.Cfg.ServeLockPath())
	if !ok {
		return fmt.Errorf("another zimon serve is running with %s", s.Manager.Cfg.StateDir)
	}
	defer unlockServe()
	// No other zimon serve uses this state directory now, so what older
	// releases fetched (and fetches that were stopped) can go.
	for _, p := range kiwix.Old(s.Manager.Cfg.StateDir) {
		if err := os.RemoveAll(p); err != nil {
			s.logf("%v", err)
		}
	}
	// Jobs a previous run left behind will not run now; say so in their history.
	bg := context.WithoutCancel(ctx)
	if err := s.Manager.Store.InterruptJobs(bg); err != nil {
		return err
	}
	if err := s.Manager.Store.PruneJobs(bg, keepJobs); err != nil {
		return err
	}
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	if admin != nil {
		if a, ok := admin.Addr().(*net.TCPAddr); ok {
			s.adminPort.Store(int32(a.Port))
		}
	}

	var wg sync.WaitGroup
	switch {
	case s.kiwixBin() != "":
		wg.Go(func() { s.supervise(ctx) })
	case s.Fetch != nil:
		wg.Go(func() {
			if s.fetchKiwix(ctx) {
				s.supervise(ctx)
			}
		})
	}
	s.jobs.stopped = ctx.Done()
	wg.Go(func() { s.work(ctx) })
	wg.Go(func() { s.schedule(ctx) })
	var servers []*http.Server
	serve := func(h http.Handler, l net.Listener) {
		srv := newHTTPServer(h)
		servers = append(servers, srv)
		wg.Go(func() {
			if err := srv.Serve(l); !errors.Is(err, http.ErrServerClosed) {
				stop(err) // the other listener and everything else stop too
			}
		})
	}
	serve(s.Handler(), ln)
	if admin != nil {
		serve(s.AdminHandler(), admin)
	}
	<-ctx.Done()
	shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, srv := range servers {
		_ = srv.Shutdown(shut) // lets requests in progress finish
	}
	wg.Wait() // kiwix-serve has exited too
	if err := context.Cause(ctx); !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func newHTTPServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No WriteTimeout: a book's video or a big download may take a while.
	}
}

// kiwixBin is kiwix-serve, or "" while it is missing.
func (s *Server) kiwixBin() string { return *s.bin.Load() }

// fetchRetry waits a minute after the first failed fetch, doubling up to an
// hour: the archive is 10–20 MB.
func fetchRetry(attempt int) time.Duration { return min(time.Minute<<min(attempt, 6), time.Hour) }

// fetchTries is how often fetchKiwix tries: over about a day with fetchRetry.
const fetchTries = 30

// fetchKiwix gets kiwix-serve with Fetch, trying again for a while, until it
// works (true), gives up or ctx ends (false).
func (s *Server) fetchKiwix(ctx context.Context) bool {
	for attempt := 0; ; attempt++ {
		bin, err := s.Fetch(ctx)
		if err == nil {
			s.logf("Fetched kiwix-serve: %s", bin)
			s.bin.Store(&bin)
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		if attempt+1 >= fetchTries {
			s.fetchErr.Store(&err)
			s.logf("Fetching kiwix-serve failed (%v); giving up until zimon serve starts again", err)
			return false
		}
		wait := s.retry(attempt)
		s.logf("Fetching kiwix-serve failed (%v); trying again in %s", err, wait)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return false
		}
	}
}

// supervise keeps kiwix-serve running: it starts it, waits until it
// answers, and starts it again (after a growing pause) when it stops. It
// returns once ctx is done and kiwix-serve has exited.
func (s *Server) supervise(ctx context.Context) {
	for attempt := 0; ctx.Err() == nil; attempt++ {
		addr, err := freeAddr()
		var p *proc
		if err == nil {
			p, err = s.start(ctx, addr)
		}
		if err == nil {
			began := time.Now()
			if s.waitReady(ctx, addr, p) {
				s.upstream.Store(&url.URL{Scheme: "http", Host: addr})
				if attempt > 0 {
					s.logf("kiwix-serve is back")
				}
			}
			<-p.done // ctx ending stops it too
			err = p.err
			s.upstream.Store(nil)
			if time.Since(began) > time.Minute {
				attempt = 0 // it ran fine for a while: no need to wait long
			}
		}
		if ctx.Err() != nil {
			return
		}
		wait := s.backoff(attempt)
		s.logf("kiwix-serve stopped (%v); starting it again in %s", err, wait)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
		}
	}
}

// startKiwix runs kiwix-serve on addr (127.0.0.1:port), serving the
// library. It exits with zimon, even if zimon is killed.
func (s *Server) startKiwix(ctx context.Context, addr string) (*proc, error) {
	_, portText, _ := net.SplitHostPort(addr)
	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, err
	}
	args := kiwix.Args(kiwix.Options{Library: s.Manager.Cfg.LibraryXMLPath(), Address: "127.0.0.1", Port: port, AttachPID: os.Getpid()})
	cmd := exec.CommandContext(ctx, s.kiwixBin(), args...)
	cmd.Stdout = io.Discard // it announces its private address, which nobody should use
	cmd.Stderr = logWriter{s}
	ownGroup(cmd) // Ctrl-C is for zimon, which then stops kiwix-serve itself
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &proc{done: make(chan struct{})}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	return p, nil
}

// waitReady waits until addr accepts connections. It gives up only when the
// process exits or ctx ends: a big library can take a while to open.
func (s *Server) waitReady(ctx context.Context, addr string, p *proc) bool {
	slow := time.After(15 * time.Second)
	for {
		if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			c.Close()
			return true
		}
		select {
		case <-p.done:
			return false
		case <-ctx.Done():
			return false
		case <-slow:
			s.logf("kiwix-serve is slow to start; waiting for it")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// freeAddr finds a free port on 127.0.0.1.
func freeAddr() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer ln.Close()
	return ln.Addr().String(), nil
}

// forward passes a request to kiwix-serve. Only reading is allowed: the
// library is changed through zimon, never through kiwix-serve.
func (s *Server) forward(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "read only", http.StatusMethodNotAllowed)
		return
	}
	u := s.upstream.Load()
	if u == nil {
		s.unavailable(w, http.StatusServiceUnavailable)
		return
	}
	s.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), upstreamKey{}, u)))
}

// localRedirects turns kiwix-serve's absolute redirects, which name its
// private address, into paths on this server.
func localRedirects(resp *http.Response) error {
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || loc.Host != resp.Request.URL.Host {
		return nil
	}
	loc.Scheme, loc.Host = "", ""
	resp.Header.Set("Location", loc.String())
	return nil
}

// fetching reports whether Fetch is getting kiwix-serve: it is missing, and
// Fetch hasn't given up.
func (s *Server) fetching() bool {
	return s.kiwixBin() == "" && s.Fetch != nil && s.fetchErr.Load() == nil
}

// kiwixProblem says why books can't be read right now, or "" if they can.
func (s *Server) kiwixProblem() string {
	switch {
	case s.fetching():
		return "kiwix-serve is being fetched from Kiwix; books open once it is here."
	case s.kiwixBin() == "":
		why := "kiwix-serve is not installed"
		if err := s.fetchErr.Load(); err != nil {
			why = fmt.Sprintf("Fetching kiwix-serve failed (%v)", *err)
		}
		return why + ", so books can't be opened. To fix it, " + kiwix.InstallHint + "; then restart zimon serve."
	case s.upstream.Load() == nil:
		return "kiwix-serve is starting; books open in a moment."
	}
	return ""
}

func (s *Server) unavailable(w http.ResponseWriter, code int) {
	w.Header().Set("Retry-After", "2")
	http.Error(w, s.kiwixProblem(), code)
}

// status is the health check: the server answers, and says whether
// kiwix-serve does.
func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	kiwixState := "up"
	switch {
	case s.kiwixBin() == "" && !s.fetching():
		kiwixState = "missing"
	case s.upstream.Load() == nil:
		kiwixState = "down"
	}
	writeJSON(w, http.StatusOK, api.Status{OK: true, Kiwix: kiwixState, API: api.Version})
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// logWriter passes kiwix-serve's error output to Logf.
type logWriter struct{ s *Server }

func (l logWriter) Write(p []byte) (int, error) {
	l.s.logf("kiwix-serve: %s", strings.TrimRight(string(p), "\r\n"))
	return len(p), nil
}
