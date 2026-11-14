// Package backend is where a library lives, for the CLI and the browser:
// here (Local), or on a zimon serve reached over https (Remote). Both
// answer with the API's types, so whatever shows them shows them the same
// way for both.
package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/client"
	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/library"
	"github.com/kazie/zimon/internal/store"
	"github.com/kazie/zimon/internal/task"
)

// Backend is a library to look at and change.
type Backend interface {
	Info(ctx context.Context) (api.Info, error)
	Items(ctx context.Context) ([]api.Item, error)
	// Search returns q.Limit matches (all for 0), and how many there are.
	Search(ctx context.Context, q api.Query) ([]api.Entry, int, error)
	// Entries returns every flavour of name; for a name not in the catalog
	// none, and names like it.
	Entries(ctx context.Context, name string) (entries []api.Entry, similar []string, err error)
	// Facets lists the catalog's "categories" or "languages".
	Facets(ctx context.Context, what string) ([]api.Facet, error)
	Collections(ctx context.Context) ([]api.Collection, error)
	Collection(ctx context.Context, id string) (*api.CollectionDetail, error)
	Budget(ctx context.Context) (api.Budget, error)
	Jobs(ctx context.Context, limit int) ([]api.Job, error)
	Job(ctx context.Context, id int64) (*api.Job, error)

	// Preview plans the change kind asks for (not budget: it has no plan).
	Preview(ctx context.Context, kind string, req api.Request) (*api.Preview, error)
	// Run carries out a change or chore (see package task), telling emit
	// how it goes. On a remote it is a job there; if ctx ends first, the
	// job carries on and Run returns a *client.Detached.
	Run(ctx context.Context, kind string, req api.Request, emit func(api.Event)) error
	// Tidy deletes stale partial downloads, if nobody is busy. A remote's
	// server tidies up by itself.
	Tidy(ctx context.Context, emit func(api.Event))
	PrunePlan(ctx context.Context) ([]api.Removed, error)
	Suggest(ctx context.Context, ids []string, limit int64) (api.Suggestion, error)
	// Follow shows a remote's job until it finishes; Cancel stops it.
	Follow(ctx context.Context, id int64, emit func(api.Event)) error
	Cancel(ctx context.Context, id int64) error
}

// Local is the library on this machine.
type Local struct {
	Manager func() (*library.Manager, error) // opens the library on first use
	Version string                           // zimon's
	Commit  string                           // zimon's, short; "" when unknown
	// Lock takes the state lock, calling onWait if another zimon holds it;
	// TryLock takes it only if it is free.
	Lock    func(ctx context.Context, onWait func(who string)) (unlock func(), err error)
	TryLock func() (unlock func(), ok bool)

	once   sync.Once
	sem    chan struct{} // guards held and unlock; a Run waiting for it can give up
	held   int           // Runs holding the state lock
	unlock func()        // releases it when held drops to 0
}

// free is the free disk space to show: the library disk's, unless the demo
// tapes pin it (ZIMON_DEMO_FREE_SPACE, so recordings don't change with it).
func (l *Local) free(m *library.Manager) int64 {
	if n, err := config.ParseBytes(os.Getenv("ZIMON_DEMO_FREE_SPACE")); err == nil && n > 0 {
		return n
	}
	n, _ := m.FreeSpace()
	return n
}

// Info implements Backend.
func (l *Local) Info(ctx context.Context) (api.Info, error) {
	m, err := l.Manager()
	if err != nil {
		return api.Info{}, err
	}
	synced, _, count, err := m.Store.CatalogState(ctx)
	return api.Info{Version: l.Version, Commit: l.Commit, API: api.Version, LibraryDir: m.Cfg.LibraryDir, LibraryFile: m.SetupPath,
		Languages: m.Cfg.Languages, Entries: count, Synced: api.Time(synced)}, err
}

// Items implements Backend.
func (l *Local) Items(ctx context.Context) ([]api.Item, error) {
	m, err := l.Manager()
	if err != nil {
		return nil, err
	}
	items, err := m.Items(ctx)
	if err != nil {
		return nil, err
	}
	return api.Items(items, m.Cfg.LibraryDir), nil
}

// Search implements Backend.
func (l *Local) Search(ctx context.Context, q api.Query) ([]api.Entry, int, error) {
	m, err := l.Manager()
	if err != nil {
		return nil, 0, err
	}
	es, err := search(ctx, m, q)
	if err != nil {
		return nil, 0, err
	}
	total := len(es)
	if q.Limit > 0 && len(es) > q.Limit {
		es = es[:q.Limit]
	}
	var items map[catalog.Key]*library.Item
	if !q.NoState {
		if items, err = m.Items(ctx); err != nil {
			return nil, 0, err
		}
	}
	return api.Entries(es, items), total, nil
}

// search runs q against m's catalog. Text matches come back all, for the
// caller to count; names that start with a prefix only q.Limit of them.
func search(ctx context.Context, m *library.Manager, q api.Query) ([]catalog.Entry, error) {
	if q.Prefix != "" {
		limit := q.Limit
		if limit <= 0 {
			limit = -1 // SQLite: no limit
		}
		return m.Store.EntriesWithPrefix(ctx, q.Prefix, q.Languages, limit)
	}
	return m.Store.Search(ctx, store.Query{Text: q.Text, Languages: q.Languages, Category: q.Category, Flavour: q.Flavour, MaxSize: q.MaxSize})
}

// Entries implements Backend.
func (l *Local) Entries(ctx context.Context, name string) ([]api.Entry, []string, error) {
	m, err := l.Manager()
	if err != nil {
		return nil, nil, err
	}
	es, err := m.Store.EntriesByName(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	if len(es) == 0 {
		similar, _ := m.Store.SimilarNames(ctx, name, 5)
		return nil, similar, nil
	}
	items, err := m.Items(ctx)
	if err != nil {
		return nil, nil, err
	}
	return api.Entries(es, items), nil, nil
}

// Facets implements Backend.
func (l *Local) Facets(ctx context.Context, what string) ([]api.Facet, error) {
	m, err := l.Manager()
	if err != nil {
		return nil, err
	}
	return facets(ctx, m.Store, what)
}

// facets lists st's "categories" or "languages".
func facets(ctx context.Context, st *store.Store, what string) ([]api.Facet, error) {
	var fs []store.Facet
	var err error
	switch what {
	case "categories":
		fs, err = st.Categories(ctx)
	case "languages":
		fs, err = st.Languages(ctx)
	default:
		return nil, fmt.Errorf("no facet %q", what)
	}
	out := make([]api.Facet, len(fs))
	for i, f := range fs {
		out[i] = api.Facet{Value: f.Value, Count: f.Count}
	}
	return out, err
}

// Collections implements Backend.
func (l *Local) Collections(ctx context.Context) ([]api.Collection, error) {
	m, err := l.Manager()
	if err != nil {
		return nil, err
	}
	status, err := m.CollectionStatus(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]api.Collection, len(status))
	for i, cs := range status {
		out[i] = api.CollectionOf(cs)
	}
	return out, nil
}

// Collection implements Backend.
func (l *Local) Collection(ctx context.Context, id string) (*api.CollectionDetail, error) {
	m, err := l.Manager()
	if err != nil {
		return nil, err
	}
	d, err := m.CollectionDetail(ctx, id)
	if err != nil {
		return nil, err
	}
	out := api.CollectionDetailOf(d)
	return &out, nil
}

// Budget implements Backend.
func (l *Local) Budget(ctx context.Context) (api.Budget, error) {
	m, err := l.Manager()
	if err != nil {
		return api.Budget{}, err
	}
	rep, err := m.Budget(ctx)
	if err != nil {
		return api.Budget{}, err
	}
	return api.BudgetOf(rep, l.free(m)), nil
}

// Jobs implements Backend.
func (l *Local) Jobs(ctx context.Context, limit int) ([]api.Job, error) {
	m, err := l.Manager()
	if err != nil {
		return nil, err
	}
	if limit <= 0 { // as the API
		limit = 50
	}
	limit = min(limit, 1000)
	jobs, err := m.Store.Jobs(ctx, limit)
	out := make([]api.Job, len(jobs))
	for i, j := range jobs {
		out[i] = api.JobOf(j)
	}
	return out, err
}

// Job implements Backend.
func (l *Local) Job(ctx context.Context, id int64) (*api.Job, error) {
	m, err := l.Manager()
	if err != nil {
		return nil, err
	}
	j, err := m.Store.JobByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if j == nil {
		return nil, fmt.Errorf("no job %d (see: zimon jobs)", id)
	}
	out := api.JobOf(*j)
	return &out, nil
}

// Preview implements Backend.
func (l *Local) Preview(ctx context.Context, kind string, req api.Request) (*api.Preview, error) {
	m, err := l.Manager()
	if err != nil {
		return nil, err
	}
	p, err := task.Preview(ctx, m, kind, req)
	if err != nil {
		return nil, err
	}
	return &api.Preview{Plan: api.PlanOf(p.Plan, l.free(m)), Edits: p.Edits}, nil
}

// Run implements Backend.
func (l *Local) Run(ctx context.Context, kind string, req api.Request, emit func(api.Event)) error {
	return l.with(ctx, task.Of(kind).Locks, emit, func(m *library.Manager) error {
		return task.Run(ctx, m, kind, req, emit)
	})
}

// Pull copies files from another zimon (see task.Pull).
func (l *Local) Pull(ctx context.Context, req api.Request, files []task.Pulled, emit func(api.Event)) error {
	return l.with(ctx, true, emit, func(m *library.Manager) error {
		return task.Pull(ctx, m, req, files, emit)
	})
}

// with runs f on the library, under the state lock if locked.
func (l *Local) with(ctx context.Context, locked bool, emit func(api.Event), f func(*library.Manager) error) error {
	m, err := l.Manager()
	if err != nil {
		return err
	}
	if locked {
		unlock, err := l.lock(ctx, emit)
		if err != nil {
			return err
		}
		defer unlock()
	}
	return f(m)
}

// lock takes the state lock for one Run. Runs at the same time (the
// browser's download and removal, say) share it, so one does not wait
// behind the other.
func (l *Local) lock(ctx context.Context, emit func(api.Event)) (func(), error) {
	l.once.Do(func() { l.sem = make(chan struct{}, 1) })
	waiting := func(who string) { emit(api.Event{Type: api.EvWaiting, Line: who}) }
	select {
	case l.sem <- struct{}{}:
	default: // another Run here is taking the lock, and may wait for another zimon
		waiting("another change here")
		select {
		case l.sem <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	defer func() { <-l.sem }()
	if l.held == 0 {
		unlock, err := l.Lock(ctx, waiting)
		if err != nil {
			return nil, err
		}
		l.unlock = unlock
	}
	l.held++
	// A release waits for sem only briefly: a Run holds it long only while
	// waiting for another zimon, which it does only when none holds a share.
	return func() {
		l.sem <- struct{}{}
		defer func() { <-l.sem }()
		if l.held--; l.held == 0 {
			l.unlock()
		}
	}, nil
}

// Tidy implements Backend.
func (l *Local) Tidy(ctx context.Context, emit func(api.Event)) {
	m, err := l.Manager()
	if err != nil {
		return
	}
	// Only if nobody else is busy: no reason to wait for them.
	if unlock, ok := l.TryLock(); ok {
		defer unlock()
		task.Tidy(ctx, m, emit)
	}
}

// PrunePlan implements Backend.
func (l *Local) PrunePlan(ctx context.Context) ([]api.Removed, error) {
	m, err := l.Manager()
	if err != nil {
		return nil, err
	}
	return task.PrunePlan(ctx, m)
}

// Suggest implements Backend.
func (l *Local) Suggest(ctx context.Context, ids []string, limit int64) (api.Suggestion, error) {
	m, err := l.Manager()
	if err != nil {
		return api.Suggestion{}, err
	}
	return task.Suggest(ctx, m, ids, limit)
}

var errLocalJob = errors.New("zimon serve runs jobs; follow or cancel one with --remote, or on its admin page")

// Follow implements Backend.
func (*Local) Follow(context.Context, int64, func(api.Event)) error { return errLocalJob }

// Cancel implements Backend.
func (*Local) Cancel(context.Context, int64) error { return errLocalJob }

// Remote is the library of a zimon serve, through its API.
type Remote struct {
	*client.Client
	info *api.Info // once asked for
}

// NewRemote is the library behind c.
func NewRemote(c *client.Client) *Remote { return &Remote{Client: c} }

// Info is the server's zimon and library, asked for once.
func (r *Remote) Info(ctx context.Context) (api.Info, error) {
	if r.info == nil {
		info, err := r.Client.Info(ctx)
		if err != nil {
			return info, err
		}
		r.info = &info
	}
	return *r.info, nil
}

// Job implements Backend.
func (r *Remote) Job(ctx context.Context, id int64) (*api.Job, error) {
	j, err := r.Client.Job(ctx, id)
	if _, ok := client.NotFound(err); ok {
		return nil, fmt.Errorf("no job %d on %s (see: zimon jobs)", id, r.Name())
	}
	return j, err
}

// Tidy implements Backend.
func (*Remote) Tidy(context.Context, func(api.Event)) {}
