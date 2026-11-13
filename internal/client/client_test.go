package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/remote"
)

// The scenarios in internal/cli run the client against a real zimon serve;
// these are the answers that one never gives.
func TestInfoFailures(t *testing.T) {
	for _, c := range []struct {
		name, ctype, body string
		code              int
		want              string
	}{
		{"newer API", "application/json", `{"version":"9.0","api":99}`, 200,
			"nas runs zimon 9.0, which speaks API 99, and this zimon API 1: update the older one"},
		{"not zimon", "text/html", "<html>", 200, "nas answered 200 OK with text/html, not zimon's API"},
		{"a proxy's error page", "text/html", "<html>", 502, "nas answered 502 Bad Gateway with text/html"},
		{"no API there", "application/json", `{"error":"no such API endpoint"}`, 404, "nas has no zimon API at "},
		{"wrong token", "application/json", `{"error":"unknown token"}`, 401,
			"nas turned down the token (unknown token); make one there with zimon token add"},
		{"an error without words", "application/json", `{}`, 500, "500 Internal Server Error"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/zimon/api/v1/info" || r.Header.Get("Authorization") != "Bearer tok" {
					t.Errorf("asked %s with %q", r.URL.Path, r.Header.Get("Authorization"))
				}
				w.Header().Set("Content-Type", c.ctype)
				w.WriteHeader(c.code)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			cl, err := New(remote.Remote{Name: "nas", URL: srv.URL + "/zimon/", Token: "tok"}, srv.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = cl.Info(context.Background())
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("Info() = %v, want %q", err, c.want)
			}
		})
	}
}

func TestDown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	cl, err := New(remote.Remote{Name: "nas", URL: srv.URL}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cl.Items(context.Background()); err == nil || !strings.HasPrefix(err.Error(), "nas does not answer: ") || strings.Contains(err.Error(), "Get ") {
		t.Errorf("Items() = %v", err)
	}
}

// fakeJobs answers POST /api/v1/add with job 7, and streams events, for
// the tests of Run. With finish false, the stream never says the job
// finished, and GET /api/v1/jobs/7 says it failed, or with down, fails.
func fakeJobs(t *testing.T, finish bool, down ...bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	started := make(chan struct{})
	mux.HandleFunc("GET /api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(": zimon events\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-started:
		case <-r.Context().Done():
			return
		}
		lines := []string{
			`{"type":"progress","job":6,"key":"other","done":1}`,
			`{"type":"progress","job":7,"phase":"start","key":"devdocs_en_go","total":10}`,
			`{"type":"log","job":7,"line":"a line for the log"}`,
		}
		if finish {
			lines = append(lines, `{"type":"finished","job":7,"state":"done"}`)
		}
		for _, l := range lines {
			_, _ = w.Write([]byte("event: x\ndata: " + l + "\n\n"))
		}
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	mux.HandleFunc("POST /api/v1/add", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":7,"state":"queued"}`))
		close(started)
	})
	mux.HandleFunc("GET /api/v1/jobs/7", func(w http.ResponseWriter, _ *http.Request) {
		if len(down) > 0 && down[0] {
			http.Error(w, "proxy trouble", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":7,"state":"failed","error":"1 of 1 changes failed","finished":"2026-11-09T10:00:00Z"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestRun(t *testing.T) {
	defer func(d time.Duration) { pollEvery = d }(pollEvery)
	pollEvery = 20 * time.Millisecond
	for _, c := range []struct {
		name         string
		finish, down bool
		want         string // the error; "" for none
	}{
		{"finished event", true, false, ""},
		{"finished event missed", false, false, "1 of 1 changes failed"},
		{"the job cannot be asked for", false, true, "lost touch with job 7 (nas answered 502 Bad Gateway with text/plain"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := fakeJobs(t, c.finish, c.down)
			cl, err := New(remote.Remote{Name: "nas", URL: srv.URL}, srv.Client())
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			err = cl.Run(context.Background(), "add", api.Request{Keys: []string{"devdocs_en_go"}}, func(ev api.Event) {
				got = append(got, ev.Type+" "+ev.Key)
			})
			if (err == nil) != (c.want == "") || (err != nil && !strings.HasPrefix(err.Error(), c.want)) {
				t.Errorf("Run() = %v, want %q", err, c.want)
			}
			// Only job 7's events, and up to the end of the stream.
			if want := []string{"progress devdocs_en_go", "log "}; !slices.Equal(got, want) {
				t.Errorf("events %q, want %q", got, want)
			}
		})
	}
}

func TestRunDetaches(t *testing.T) {
	srv := fakeJobs(t, false)
	cl, err := New(remote.Remote{Name: "nas", URL: srv.URL}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	err = cl.Run(ctx, "add", api.Request{}, func(ev api.Event) {
		if ev.Phase == api.PhaseStart {
			cancel() // Ctrl-C once the job runs
		}
	})
	d, ok := errors.AsType[*Detached](err)
	if !ok || d.Job != 7 || !errors.Is(err, context.Canceled) {
		t.Errorf("Run() = %v, want detached from job 7", err)
	}
}

// FileClient sends the token to the server only, not where it redirects.
func TestFileToken(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Header.Get("Authorization")))
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/zimon/api/v1/files/u 1":
			_, _ = w.Write([]byte(r.Header.Get("Authorization")))
		default:
			http.Redirect(w, r, other.URL, http.StatusFound)
		}
	}))
	defer srv.Close()
	cl, err := New(remote.Remote{Name: "nas", URL: srv.URL + "/zimon", Token: "tok"}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	hc := cl.FileClient()
	for _, c := range []struct{ uuid, want string }{
		{"u 1", "Bearer tok"},
		{"moved", ""},
	} {
		resp, err := hc.Get(cl.FileURL(c.uuid))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != c.want {
			t.Errorf("%s: Authorization %q, want %q", c.uuid, body, c.want)
		}
	}
}

// The token never goes over plain http to an https server's host.
func TestFileTokenScheme(t *testing.T) {
	var got []string
	next := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = append(got, r.URL.Scheme+" "+r.Header.Get("Authorization"))
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Request: r}, nil
	})
	cl, err := New(remote.Remote{Name: "nas", URL: "https://nas.example", Token: "tok"}, &http.Client{Transport: next})
	if err != nil {
		t.Fatal(err)
	}
	hc := cl.FileClient()
	for _, u := range []string{cl.FileURL("u"), "http://nas.example/api/v1/files/u"} {
		resp, err := hc.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if want := []string{"https Bearer tok", "http "}; !slices.Equal(got, want) {
		t.Errorf("sent %q, want %q", got, want)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
