package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/collections"
	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/fakekiwix"
	"github.com/kazie/zimon/internal/library"
	"github.com/kazie/zimon/internal/lock"
	"github.com/kazie/zimon/internal/store"
)

// liveServer is a running server whose library downloads from fakekiwix,
// with a token for the API.
type liveServer struct {
	t     *testing.T
	s     *Server
	base  string
	token string
}

func newLiveServer(t *testing.T, before ...func(*library.Manager)) *liveServer {
	t.Helper()
	fk := fakekiwix.New([]fakekiwix.Book{
		{Name: "devdocs_en_go", Title: "Go Docs", Language: "eng", Date: "2026-10-12", Size: 100 << 10},
		{Name: "wiki_en_all", Flavour: "mini", Title: "Wiki", Language: "eng", Date: "2026-10-01", Size: 100 << 10},
		{Name: "wiki_en_all", Flavour: "maxi", Title: "Wiki", Language: "eng", Date: "2026-10-01", Size: 200 << 10},
	})
	fk.PieceLength = 64 << 10
	up := httptest.NewServer(fk)
	t.Cleanup(up.Close)
	dir := t.TempDir()
	cfg := config.Default()
	cfg.LibraryDir, cfg.StateDir = filepath.Join(dir, "library"), filepath.Join(dir, "state")
	cfg.CatalogURL = up.URL + "/catalog/v2"
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := &library.Manager{Cfg: cfg, Store: st, HTTP: up.Client(), SetupPath: filepath.Join(dir, "library.toml"),
		Backoff: func(int) time.Duration { return 0 },
		Collections: []collections.Collection{{ID: "docs", Title: "Docs", Items: []collections.Item{
			{Name: "devdocs_en_go", Tier: collections.Core},
			{Name: "wiki_en_all", Tier: collections.Core},
		}}}}
	if _, _, _, err := library.RefreshCatalog(context.Background(), st, up.Client(), cfg.CatalogURLs()); err != nil {
		t.Fatal(err)
	}
	for _, f := range before {
		f(m)
	}
	s := New(m, "")
	token, hash := NewToken()
	if err := st.AddToken(context.Background(), "test", hash); err != nil {
		t.Fatal(err)
	}
	return &liveServer{t: t, s: s, base: serve(t, s), token: token}
}

// do sends an API request and decodes the JSON answer into out (if set).
func (l *liveServer) do(method, path, body string, out any) int {
	l.t.Helper()
	req, _ := http.NewRequest(method, l.base+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+l.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		l.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			l.t.Fatalf("%s %s: %v", method, path, err)
		}
	}
	return resp.StatusCode
}

// events follows /api/v1/events until the test ends.
func (l *liveServer) events() <-chan api.Event {
	l.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	l.t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, l.base+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+l.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		l.t.Fatalf("events: %v %v", resp, err)
	}
	ch := make(chan api.Event, 256)
	go func() {
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var ev api.Event
				if json.Unmarshal([]byte(data), &ev) == nil {
					ch <- ev
				}
			}
		}
	}()
	return ch
}

// finished waits for job id to end and returns it.
func (l *liveServer) finished(id int64) api.Job {
	l.t.Helper()
	var j api.Job
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		l.do(http.MethodGet, "/api/v1/jobs/"+itoa(id), "", &j)
		if j.Finished != "" {
			return j
		}
	}
	l.t.Fatalf("job %d did not finish: %+v", id, j)
	return j
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestJobAddWithEvents(t *testing.T) {
	l := newLiveServer(t)
	evs := l.events()
	var j api.Job
	if code := l.do(http.MethodPost, "/api/v1/add", `{"keys":["devdocs_en_go"]}`, &j); code != http.StatusAccepted || j.State != "queued" || j.By != "test" {
		t.Fatalf("add: %d %+v", code, j)
	}
	var types []string
	timeout := time.After(10 * time.Second)
	for done := false; !done; {
		select {
		case ev := <-evs:
			if ev.Job == j.ID {
				types = append(types, ev.Type)
				done = ev.Type == "finished"
			}
		case <-timeout:
			t.Fatalf("events so far: %v", types)
		}
	}
	if !strings.HasPrefix(strings.Join(types, " "), "queued started") || !strings.Contains(strings.Join(types, " "), "progress") {
		t.Errorf("events: %v", types)
	}
	j = l.finished(j.ID)
	if j.State != "done" || !strings.Contains(strings.Join(j.Log, "\n"), "devdocs_en_go: devdocs_en_go_") {
		t.Errorf("job: %+v", j)
	}
	var items []struct{ Key, State string }
	l.do(http.MethodGet, "/api/v1/items", "", &items)
	if len(items) != 1 || items[0].State != "up_to_date" {
		t.Errorf("items: %+v", items)
	}

	// Verify and prune run as jobs too.
	l.do(http.MethodPost, "/api/v1/verify", "", &j)
	if j = l.finished(j.ID); j.State != "done" || !strings.Contains(strings.Join(j.Log, " "), "All 1 files match") {
		t.Errorf("verify: %+v", j)
	}
}

func TestJobPreviewAndBadRequests(t *testing.T) {
	l := newLiveServer(t)
	var preview struct {
		Plan  api.Plan
		Edits bool
	}
	if code := l.do(http.MethodPost, "/api/v1/add?preview=1", `{"collections":{"docs":"light"}}`, &preview); code != http.StatusOK ||
		!preview.Edits || len(preview.Plan.Steps) != 2 || preview.Plan.Steps[1].Key != "wiki_en_all:mini" {
		t.Errorf("preview: %d %+v", code, preview)
	}
	if _, err := os.Stat(l.s.Manager.SetupPath); !os.IsNotExist(err) {
		t.Error("a preview wrote library.toml")
	}
	for body, path := range map[string]string{
		`{"collections":{"nope":"light"}}`:  "/api/v1/add",
		`{"keys":["nope_en_all"]}`:          "/api/v1/add",
		`{"collections":{"docs":"huge"}}`:   "/api/v1/add",
		`{"target":"@docs","choice":"big"}`: "/api/v1/level",
		`{"targets":["devdocs_en_go"]}`:     "/api/v1/remove", // not in the library
		`{"wat":1}`:                         "/api/v1/add",
	} {
		var e struct{ Error string }
		if code := l.do(http.MethodPost, path, body, &e); code != http.StatusBadRequest || e.Error == "" {
			t.Errorf("%s %s: %d %+v", path, body, code, e)
		}
	}
	var jobs []api.Job
	if l.do(http.MethodGet, "/api/v1/jobs", "", &jobs); len(jobs) != 0 {
		t.Errorf("bad requests made jobs: %+v", jobs)
	}
}

// A level change checks the flavour as the CLI does, and "off" needs no
// catalog entry.
func TestJobLevelChecksTheChoice(t *testing.T) {
	l := newLiveServer(t)
	var e struct{ Error string }
	if code := l.do(http.MethodPost, "/api/v1/level", `{"target":"devdocs_en_go","choice":"maxi"}`, &e); code != http.StatusBadRequest ||
		!strings.Contains(e.Error, `no "maxi" variant`) {
		t.Errorf("a flavour it doesn't come in: %d %+v", code, e)
	}
	var j api.Job
	if code := l.do(http.MethodPost, "/api/v1/level", `{"target":"gone_en_all","choice":"off"}`, &j); code != http.StatusAccepted {
		t.Errorf("off for a ZIM not in the catalog: %d", code)
	}
}

// A bare name with several flavours needs one picked, as with the add
// command; "name:" is the one without a flavour.
func TestJobAddNeedsAFlavour(t *testing.T) {
	l := newLiveServer(t)
	var e struct{ Error string }
	if code := l.do(http.MethodPost, "/api/v1/add?preview=1", `{"keys":["wiki_en_all"]}`, &e); code != http.StatusBadRequest ||
		!strings.Contains(e.Error, "wiki_en_all:mini") {
		t.Errorf("a bare name with two flavours: %d %+v", code, e)
	}
	if code := l.do(http.MethodPost, "/api/v1/add?preview=1", `{"keys":["devdocs_en_go:"]}`, nil); code != http.StatusOK {
		t.Errorf("name: for the one without a flavour: %d", code)
	}
}

// A change confirmed from a preview runs only if the plan is still the one
// shown.
func TestJobRunsOnlyThePlanShown(t *testing.T) {
	l := newLiveServer(t)
	var preview struct{ Plan api.Plan }
	l.do(http.MethodPost, "/api/v1/add?preview=1", `{"keys":["devdocs_en_go"]}`, &preview)
	if preview.Plan.ID == "" {
		t.Fatal("the preview has no plan id")
	}
	var j api.Job
	l.do(http.MethodPost, "/api/v1/add", `{"keys":["devdocs_en_go"],"plan":"0000"}`, &j)
	if j = l.finished(j.ID); j.State != "failed" || !strings.Contains(j.Error, "plan has changed") {
		t.Errorf("a stale plan ran: %+v", j)
	}
	l.do(http.MethodPost, "/api/v1/add", `{"keys":["devdocs_en_go"],"plan":"`+preview.Plan.ID+`"}`, &j)
	if j = l.finished(j.ID); j.State != "done" {
		t.Errorf("the plan shown: %+v", j)
	}
}

// When a listener fails, Run stops and says why.
func TestRunStopsWhenAListenerFails(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	admin.Close() // Serve fails at once
	done := make(chan error, 1)
	go func() { done <- New(newManager(t), "").Run(context.Background(), ln, admin) }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Run returned no error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run kept going")
	}
}

// Only one server runs jobs and the schedule for a state directory.
func TestOneServerPerStateDir(t *testing.T) {
	m := newManager(t)
	serve(t, New(m, ""))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := New(m, "").Run(context.Background(), ln, nil); err == nil || !strings.Contains(err.Error(), "another zimon serve") {
		t.Errorf("second server: %v", err)
	}
}

// Jobs wait for a zimon command holding the lock, and can be canceled
// while they wait or sit in the queue.
func TestJobCancel(t *testing.T) {
	l := newLiveServer(t)
	unlock, err := lock.Acquire(context.Background(), l.s.Manager.Cfg.LockPath(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	evs := l.events()
	var first, second api.Job
	l.do(http.MethodPost, "/api/v1/apply", "", &first)
	l.do(http.MethodPost, "/api/v1/refresh", "", &second)
	for ev := range evs {
		if ev.Job == first.ID && ev.Type == "waiting" {
			break
		}
	}
	for _, id := range []int64{second.ID, first.ID} {
		if code := l.do(http.MethodPost, "/api/v1/jobs/"+itoa(id)+"/cancel", "", nil); code != http.StatusAccepted {
			t.Errorf("cancel %d: %d", id, code)
		}
		if j := l.finished(id); j.State != "canceled" {
			t.Errorf("job %d: %+v", id, j)
		}
	}
	if code := l.do(http.MethodPost, "/api/v1/jobs/"+itoa(first.ID)+"/cancel", "", nil); code != http.StatusConflict {
		t.Errorf("cancel a finished job: %d", code)
	}
}

// Jobs a stopped server left queued are marked interrupted, not run.
func TestJobsInterruptedByRestart(t *testing.T) {
	m := newManager(t)
	left, err := m.Store.AddJob(context.Background(), "apply", "apply", "test")
	if err != nil {
		t.Fatal(err)
	}
	serve(t, New(m, ""))
	waitFor(t, "the left-over job to be marked", func() bool {
		j, _ := m.Store.JobByID(context.Background(), left.ID)
		return j.State == store.JobInterrupted
	})
}

func TestEventsNeedAToken(t *testing.T) {
	w := httptest.NewRecorder()
	New(newManager(t), "").Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/events", bytes.NewReader(nil)))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("events without a token: %d", w.Code)
	}
}

// With Kiwix out of reach, an update asked for carries on with the cached
// catalog, but a scheduled one fails, so the schedule retries it.
func TestScheduledUpdateNeedsTheCatalog(t *testing.T) {
	l := newLiveServer(t, func(m *library.Manager) { m.Cfg.CatalogURL = "http://127.0.0.1:1/catalog/v2" })
	var j api.Job
	l.do(http.MethodPost, "/api/v1/update", `{}`, &j)
	if j = l.finished(j.ID); j.State != store.JobDone || !strings.Contains(strings.Join(j.Log, "\n"), "using the cached copy") {
		t.Errorf("update: %+v", j)
	}
	rec, err := l.s.enqueue(context.Background(), "update", "scheduled update", "schedule",
		l.s.taskJob("update", api.Request{}, true))
	if err != nil {
		t.Fatal(err)
	}
	if j = l.finished(rec.ID); j.State != store.JobFailed || !strings.Contains(j.Error, "refresh the catalog") {
		t.Errorf("scheduled update: %+v", j)
	}
}

// A job that panics fails; the server carries on.
func TestJobPanicFails(t *testing.T) {
	l := newLiveServer(t)
	rec, err := l.s.enqueue(context.Background(), "verify", "verify", "test", func(context.Context, *job) error { panic("boom") })
	if err != nil {
		t.Fatal(err)
	}
	if j := l.finished(rec.ID); j.State != store.JobFailed || !strings.Contains(j.Error, "boom") {
		t.Errorf("job: %+v", j)
	}
	var j api.Job
	l.do(http.MethodPost, "/api/v1/verify", `{}`, &j)
	if j = l.finished(j.ID); j.State != store.JobDone {
		t.Errorf("the next job: %+v", j)
	}
}

// What the CLI needs for an https remote: the change types it has no other
// way to ask for, structured events, and the plans for prune and suggest.
func TestJobsForTheCLI(t *testing.T) {
	l := newLiveServer(t)
	evs := l.events()
	var j api.Job
	if code := l.do(http.MethodPost, "/api/v1/add", `{"keys":["devdocs_en_go:"],"no_download":true}`, &j); code != http.StatusAccepted {
		t.Fatalf("add without downloading: %d", code)
	}
	if j = l.finished(j.ID); j.State != "done" || !slices.Contains(j.Log, "Saved library.toml") {
		t.Errorf("add without downloading: %+v", j)
	}
	if _, err := os.Stat(filepath.Join(l.s.Manager.Cfg.LibraryDir, "devdocs_en_go_2026-10.zim")); !os.IsNotExist(err) {
		t.Errorf("add without downloading downloaded: %v", err)
	}
	l.do(http.MethodPost, "/api/v1/budget", `{"budget":1000000000}`, &j)
	if j = l.finished(j.ID); j.State != "done" || j.Summary != "budget 1.00 GB" {
		t.Errorf("budget: %+v", j)
	}
	l.do(http.MethodPost, "/api/v1/apply", "", &j)
	if j = l.finished(j.ID); j.State != "done" {
		t.Fatalf("apply: %+v", j)
	}
	var step *api.StepResult
	for step == nil {
		select {
		case ev := <-evs:
			if ev.Job == j.ID && ev.Type == api.EvStep {
				step = ev.Step
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no step event")
		}
	}
	if step.Key != "devdocs_en_go" || step.Kind != api.StepAdd || step.Verified == "" || !strings.HasSuffix(step.Path, ".zim") {
		t.Errorf("step: %+v", step)
	}

	l.do(http.MethodPost, "/api/v1/verify", `{"names":["devdocs_en_go"]}`, &j)
	if j = l.finished(j.ID); j.State != "done" {
		t.Errorf("verify by name: %+v", j)
	}
	var e api.Error
	if code := l.do(http.MethodPost, "/api/v1/update", `{"names":["nope"]}`, &e); code != http.StatusBadRequest || !strings.Contains(e.Error, "not in your library") {
		t.Errorf("update of a ZIM not here: %d %+v", code, e)
	}
	if code := l.do(http.MethodPost, "/api/v1/verify", `{"names":["nope"]}`, &e); code != http.StatusBadRequest || !strings.Contains(e.Error, "not in your library") {
		t.Errorf("verify of a ZIM not here: %d %+v", code, e)
	}
	if code := l.do(http.MethodPost, "/api/v1/budget?preview=1", `{"budget":1}`, &e); code != http.StatusBadRequest {
		t.Errorf("a preview of budget: %d", code)
	}
	var plan []api.Removed
	if code := l.do(http.MethodGet, "/api/v1/prune", "", &plan); code != http.StatusOK || len(plan) != 0 {
		t.Errorf("prune plan: %d %+v", code, plan)
	}
	var sg api.Suggestion
	if code := l.do(http.MethodGet, "/api/v1/suggest?c=docs", "", &sg); code != http.StatusOK || !sg.Fits || sg.Budget != 1e9 || sg.Levels["docs"] != "deep" {
		t.Errorf("suggest within library.toml's budget: %d %+v", code, sg)
	}
	if code := l.do(http.MethodGet, "/api/v1/suggest?c=nope&budget=1000", "", &e); code != http.StatusBadRequest {
		t.Errorf("suggest for no such collection: %d", code)
	}
}
