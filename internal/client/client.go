// Package client talks to zimon serve's HTTP API. `zimon --remote NAME`
// uses it for an https:// remote.
package client

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/remote"
	"github.com/kazie/zimon/internal/store"
)

// Client is one remote's API.
type Client struct {
	name  string   // the remote's, for messages
	base  *url.URL // the API is under base/api/v1
	token string
	hc    *http.Client
}

// New is a client for r, using hc.
func New(r remote.Remote, hc *http.Client) (*Client, error) {
	u, err := remote.ParseHTTP(r.URL)
	if err != nil {
		return nil, err
	}
	return &Client{name: r.Name, base: u, token: r.Token, hc: hc}, nil
}

// Name is the remote's name.
func (c *Client) Name() string { return c.name }

// Error is a request the server turned down.
type Error struct {
	Status int
	Body   api.Error
}

func (e *Error) Error() string { return e.Body.Error }

// NotFound reports whether err is the server's 404, and returns it.
func NotFound(err error) (*Error, bool) {
	e, ok := errors.AsType[*Error](err)
	return e, ok && e.Status == http.StatusNotFound
}

// Info returns the server's zimon and library, and checks that it speaks
// this zimon's API version.
func (c *Client) Info(ctx context.Context) (api.Info, error) {
	var info api.Info
	err := c.get(ctx, "info", nil, &info)
	if _, ok := NotFound(err); ok {
		return info, fmt.Errorf("%s has no zimon API at %s; give the address of zimon serve (or of the proxy in front of it)", c.name, c.base)
	}
	if err != nil {
		return info, err
	}
	if info.API != api.Version {
		return info, fmt.Errorf("%s runs zimon %s, which speaks API %d, and this zimon API %d: update the older one",
			c.name, info.Version, info.API, api.Version)
	}
	return info, nil
}

// Items is everything the server's zimon knows of, as `zimon list --json`.
func (c *Client) Items(ctx context.Context) ([]api.Item, error) {
	var out []api.Item
	return out, c.get(ctx, "items", nil, &out)
}

// Search searches the server's catalog. total is how many match, of which
// it returns q.Limit (all for 0).
func (c *Client) Search(ctx context.Context, q api.Query) (entries []api.Entry, total int, err error) {
	v := url.Values{"q": {q.Text}, "lang": q.Languages, "limit": {strconv.Itoa(max(q.Limit, 0))}}
	for k, s := range map[string]string{"category": q.Category, "flavour": q.Flavour, "prefix": q.Prefix} {
		if s != "" {
			v.Set(k, s)
		}
	}
	if q.MaxSize > 0 {
		v.Set("max_size", strconv.FormatInt(q.MaxSize, 10))
	}
	resp, err := c.do(ctx, http.MethodGet, "search", v, nil, &entries)
	if err != nil {
		return nil, 0, err
	}
	total, err = strconv.Atoi(resp.Header.Get("X-Total-Count"))
	if err != nil {
		total = len(entries)
	}
	return entries, total, nil
}

// Entries is every flavour of name in the server's catalog. For a name not
// in it, there are none, and similar has names like it.
func (c *Client) Entries(ctx context.Context, name string) (entries []api.Entry, similar []string, err error) {
	err = c.get(ctx, "entries/"+url.PathEscape(name), nil, &entries)
	if e, ok := NotFound(err); ok {
		return nil, e.Body.Similar, nil
	}
	return entries, nil, err
}

// Facets lists the catalog's "categories" or "languages".
func (c *Client) Facets(ctx context.Context, what string) ([]api.Facet, error) {
	var out []api.Facet
	return out, c.get(ctx, url.PathEscape(what), nil, &out)
}

// Collections lists every collection with its sizes and your level.
func (c *Client) Collections(ctx context.Context) ([]api.Collection, error) {
	var out []api.Collection
	return out, c.get(ctx, "collections", nil, &out)
}

// Collection is what the collection id holds.
func (c *Client) Collection(ctx context.Context, id string) (*api.CollectionDetail, error) {
	var out api.CollectionDetail
	if err := c.get(ctx, "collections/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Budget is what the library takes against its budget.
func (c *Client) Budget(ctx context.Context) (api.Budget, error) {
	var out api.Budget
	return out, c.get(ctx, "budget", nil, &out)
}

// Jobs lists the latest jobs, newest first.
func (c *Client) Jobs(ctx context.Context, limit int) ([]api.Job, error) {
	var out []api.Job
	return out, c.get(ctx, "jobs", url.Values{"limit": {strconv.Itoa(limit)}}, &out)
}

// Job is the job id, with its log.
func (c *Client) Job(ctx context.Context, id int64) (*api.Job, error) {
	var out api.Job
	if err := c.get(ctx, "jobs/"+strconv.FormatInt(id, 10), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	_, err := c.do(ctx, http.MethodGet, path, query, nil, out)
	return err
}

// do sends a request to the API's path, with body as JSON (nil: none), and
// decodes the answer into out. Failures come back in words that name the
// remote; a request the server turned down is an *Error.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) (*http.Response, error) {
	u := c.base.JoinPath("api/v1", path)
	u.RawQuery = query.Encode()
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(data)
	}
	resp, err := c.send(ctx, method, u, rd, "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.name, err)
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if ct != "application/json" { // a proxy's error page, say
		return nil, fmt.Errorf("%s answered %s with %s, not zimon's API; is %s the address of zimon serve?",
			c.name, resp.Status, cmp.Or(ct, "nothing"), c.base)
	}
	if resp.StatusCode >= 300 {
		e := &Error{Status: resp.StatusCode}
		if err := json.Unmarshal(data, &e.Body); err != nil || e.Body.Error == "" {
			e.Body.Error = resp.Status
		}
		if resp.StatusCode == http.StatusUnauthorized {
			e.Body.Error = fmt.Sprintf("%s turned down the token (%s); make one there with zimon token add, then add the remote again", c.name, e.Body.Error)
		}
		return resp, e
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp, fmt.Errorf("%s: %s: %w", c.name, path, err)
		}
	}
	return resp, nil
}

// send sends a request to u with the token, saying in words when the
// server does not answer.
func (c *Client) send(ctx context.Context, method string, u *url.URL, body io.Reader, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", accept)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s does not answer: %w", c.name, unwrapURL(err))
	}
	return resp, nil
}

// FileURL is where the server serves the ZIM with this uuid (GET
// /api/v1/files/UUID, with Range support).
func (c *Client) FileURL(uuid string) string {
	return c.base.JoinPath("api/v1/files", url.PathEscape(uuid)).String()
}

// FileClient is an http.Client for FileURL: it sends the token with each
// request to the server, and to nowhere else (nor over plain http, when the
// server is https).
func (c *Client) FileClient() *http.Client {
	hc := *c.hc
	hc.Transport = bearer{cmp.Or(c.hc.Transport, http.DefaultTransport), c.base.Scheme, c.base.Host, c.token}
	return &hc
}

// bearer adds the token to requests for scheme://host.
type bearer struct {
	next         http.RoundTripper
	scheme, host string
	token        string
}

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != b.scheme || req.URL.Host != b.host {
		return b.next.RoundTrip(req)
	}
	req = req.Clone(req.Context()) // a RoundTripper must not change the request
	req.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(req)
}

// unwrapURL drops url.Error's "Get "https://…": " prefix: the message
// names the remote already.
func unwrapURL(err error) error {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		return ue.Err
	}
	return err
}

// Preview is the plan of the change kind would make.
func (c *Client) Preview(ctx context.Context, kind string, req api.Request) (*api.Preview, error) {
	var out api.Preview
	if _, err := c.do(ctx, http.MethodPost, kind, url.Values{"preview": {"1"}}, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PrunePlan is what prune would delete.
func (c *Client) PrunePlan(ctx context.Context) ([]api.Removed, error) {
	var out []api.Removed
	return out, c.get(ctx, "prune", nil, &out)
}

// Suggest picks levels for collections ids that fit limit (0: the budget
// in library.toml).
func (c *Client) Suggest(ctx context.Context, ids []string, limit int64) (api.Suggestion, error) {
	var out api.Suggestion
	return out, c.get(ctx, "suggest", url.Values{"c": ids, "budget": {strconv.FormatInt(limit, 10)}}, &out)
}

// Cancel stops a queued or running job.
func (c *Client) Cancel(ctx context.Context, id int64) error {
	_, err := c.do(ctx, http.MethodPost, "jobs/"+strconv.FormatInt(id, 10)+"/cancel", nil, nil, nil)
	return err
}

// Detached is the error of Run and Follow when ctx ends first: the job
// carries on. Job is 0 when Run stopped before it heard the job's id.
type Detached struct{ Job int64 }

func (d *Detached) Error() string { return "detached from job " + strconv.FormatInt(d.Job, 10) }

// Unwrap makes a Detached a context.Canceled.
func (d *Detached) Unwrap() error { return context.Canceled }

// Run starts the job kind asks for and follows it until it finishes,
// passing its events to emit. It returns the job's error.
func (c *Client) Run(ctx context.Context, kind string, req api.Request, emit func(api.Event)) error {
	sub, stop := context.WithCancel(ctx) // closes the stream when Run returns
	defer stop()
	events, err := c.events(sub) // first: the job's early events must not be missed
	if err != nil {
		return err
	}
	var j api.Job
	if _, err := c.do(ctx, http.MethodPost, kind, nil, req, &j); err != nil {
		if ctx.Err() != nil {
			return &Detached{} // the server may have started the job all the same
		}
		return err
	}
	return c.follow(ctx, j.ID, events, emit)
}

// Follow follows the job id until it finishes, passing its events to emit.
func (c *Client) Follow(ctx context.Context, id int64, emit func(api.Event)) error {
	sub, stop := context.WithCancel(ctx) // closes the stream when Follow returns
	defer stop()
	events, err := c.events(sub)
	if err != nil {
		return err
	}
	return c.follow(ctx, id, events, emit)
}

// pollEvery is how long follow waits without hearing from the job before
// it asks for the job itself, in case the event that it finished went
// missing (a slow reader misses events). It gives up after pollTries
// failures in a row.
var pollEvery = 5 * time.Second

const pollTries = 6

func (c *Client) follow(ctx context.Context, id int64, events <-chan api.Event, emit func(api.Event)) error {
	poll := time.NewTicker(pollEvery)
	defer poll.Stop()
	failed := 0
	for {
		select {
		case <-ctx.Done():
			return &Detached{Job: id}
		case ev, ok := <-events:
			if !ok {
				events = nil // the stream ended: polling finds out how the job went
				continue
			}
			if ev.Job != id {
				continue
			}
			if ev.Type == api.EvFinished {
				return jobError(c.name, ev.State, ev.Line)
			}
			poll.Reset(pollEvery) // the stream works: no need to ask yet
			emit(ev)
		case <-poll.C:
			j, err := c.Job(ctx, id)
			switch {
			case ctx.Err() != nil:
				return &Detached{Job: id}
			case err != nil:
				if failed++; failed >= pollTries {
					return fmt.Errorf("lost touch with job %d (%w); it may still run: zimon jobs %d --follow", id, err, id)
				}
			case j.Finished != "":
				drain(events, id, emit) // the last steps may still be on their way
				return jobError(c.name, j.State, j.Error)
			default:
				failed = 0
			}
		}
	}
}

// drain passes on job id's events that are already on their way, waiting
// a moment for its last ones, up to the one that says it finished.
func drain(events <-chan api.Event, id int64, emit func(api.Event)) {
	timeout := time.After(time.Second)
	for events != nil {
		select {
		case ev, ok := <-events:
			if !ok || (ev.Job == id && ev.Type == api.EvFinished) {
				return
			}
			if ev.Job == id {
				emit(ev)
			}
		case <-timeout:
			return
		}
	}
}

// jobError is the error a job ended with, or nil when it is done.
func jobError(name, state, msg string) error {
	switch state {
	case store.JobDone:
		return nil
	case store.JobCanceled:
		return fmt.Errorf("the job was canceled on %s", name)
	case store.JobInterrupted:
		return fmt.Errorf("%s stopped while the job ran; downloads resume next time", name)
	}
	return errors.New(cmp.Or(msg, "the job failed"))
}

// events subscribes to the server's events until ctx ends.
func (c *Client) events(ctx context.Context) (<-chan api.Event, error) {
	resp, err := c.send(ctx, http.MethodGet, c.base.JoinPath("api/v1/events"), nil, "text/event-stream")
	if err != nil {
		return nil, err
	}
	if ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); resp.StatusCode != http.StatusOK || ct != "text/event-stream" {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: no event stream (%s %s)", c.name, resp.Status, ct)
	}
	ch := make(chan api.Event, 256)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		for sc.Scan() {
			data, ok := strings.CutPrefix(sc.Text(), "data: ")
			if !ok {
				continue // event names repeat the type; comments keep the line open
			}
			var ev api.Event
			if json.Unmarshal([]byte(data), &ev) != nil {
				continue
			}
			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}
