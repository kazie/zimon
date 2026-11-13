// Package task builds and carries out the changes and chores zimon does to a
// library (add, level, remove, apply, update, budget, hold, unhold, prune,
// verify, refresh), telling how they go as api.Events. The CLI runs them here and
// shows the events as they come; zimon serve runs them as jobs and sends
// the events to its API's clients, which show them the same way.
package task

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/collections"
	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/library"
	"github.com/kazie/zimon/internal/meta4"
	"github.com/kazie/zimon/internal/setup"
	"github.com/kazie/zimon/internal/store"
)

// Kind is what a kind of task is: how it runs, and what it needs first.
type Kind struct {
	Change   bool // edits library.toml and makes the disk match; else a chore
	Locks    bool // changes files, so runs under the state lock
	EditOnly bool // only edits library.toml (as add with no_download does: see EditOnly)
	Fresh    bool // looks for the newest of everything: refreshes the catalog and checks every source, however recent
}

// kinds are the tasks there are. verify and refresh don't lock: like the
// commands, they need not wait.
var kinds = map[string]Kind{
	"add": {Change: true, Locks: true}, "level": {Change: true, Locks: true},
	"remove": {Change: true, Locks: true}, "apply": {Change: true, Locks: true},
	"update": {Change: true, Locks: true, Fresh: true},
	"budget": {Change: true, Locks: true, EditOnly: true},
	"hold":   {Change: true, Locks: true, EditOnly: true}, "unhold": {Change: true, Locks: true, EditOnly: true},
	"prune": {Locks: true}, "verify": {}, "refresh": {Fresh: true},
}

// Of is kind's Kind; the zero Kind for one there is not.
func Of(kind string) Kind { return kinds[kind] }

// Kinds lists the kinds there are, sorted.
func Kinds() []string { return slices.Sorted(maps.Keys(kinds)) }

// Emit hears how a task goes.
type Emit func(api.Event)

// BadRequest is a mistake in a request, such as an unknown collection.
type BadRequest struct{ error }

func (b BadRequest) Unwrap() error { return b.error }

func bad(format string, args ...any) error { return BadRequest{fmt.Errorf(format, args...)} }

// Change builds the change kind asks for, with a summary in words. Build it
// again right before carrying it out: the library may have changed.
func Change(ctx context.Context, m *library.Manager, kind string, req api.Request) (library.Change, string, error) {
	switch kind {
	case "apply":
		return library.Change{}, "apply library.toml", nil
	case "update":
		keys, err := m.LocalKeys(ctx, req.Names)
		if err != nil {
			return library.Change{}, "", BadRequest{err}
		}
		var names []string
		for _, k := range keys {
			if !slices.Contains(names, k.Name) {
				names = append(names, k.Name)
			}
		}
		return library.Change{Names: names, Unattended: true}, strings.TrimSpace("update " + strings.Join(req.Names, " ")), nil
	case "add":
		return addChange(ctx, m, req)
	case "level":
		return levelChange(ctx, m, req)
	case "remove":
		if len(req.Targets) == 0 {
			return library.Change{}, "", bad("give targets: @collection, a name or name:flavour")
		}
		ch, err := m.RemoveTargets(ctx, req.Targets, req.KeepFiles)
		if err != nil {
			return library.Change{}, "", BadRequest{err}
		}
		return ch, "remove " + strings.Join(req.Targets, " "), nil
	case "budget":
		if req.Budget == nil {
			return library.Change{}, "", bad("give a budget in bytes (0 for none)")
		}
		n := *req.Budget
		summary := "budget off"
		if n > 0 {
			summary = "budget " + config.FormatBytes(n)
		}
		return library.Change{Edit: func(d *setup.Doc) error { d.SetBudget(n); return nil }}, summary, nil
	case "hold", "unhold":
		ch, err := m.HoldChange(ctx, req.Names, kind == "hold")
		if err != nil {
			return library.Change{}, "", BadRequest{err}
		}
		return ch, kind + " " + strings.Join(req.Names, " "), nil
	}
	return library.Change{}, "", bad("unknown change %q", kind)
}

func levelChange(ctx context.Context, m *library.Manager, req api.Request) (library.Change, string, error) {
	if req.Target == "" || req.Choice == "" {
		return library.Change{}, "", bad("give a target (@collection or a ZIM name) and a choice")
	}
	summary := "level " + req.Target + " " + req.Choice
	if id, ok := strings.CutPrefix(req.Target, "@"); ok {
		if _, err := collections.Get(m.Collections, id); err != nil {
			return library.Change{}, "", BadRequest{err}
		}
		l, ok := collections.ParseLevel(req.Choice)
		if !ok {
			return library.Change{}, "", bad("%q: a collection's level is %s", req.Choice, collections.LevelNames)
		}
		return library.LevelChange(id, l), summary, nil
	}
	if name, flavour, ok := strings.Cut(req.Target, ":"); ok {
		value := req.Choice
		if value == setup.On && flavour != "" { // "this flavour, on" is the flavour
			value = flavour
		}
		return library.Change{}, "", bad("give the name without a flavour: zimon level %s %s", name, value)
	}
	if err := m.CheckChoice(ctx, req.Target, req.Choice); err != nil {
		return library.Change{}, "", BadRequest{err}
	}
	return library.ItemChange(req.Target, req.Choice), summary, nil
}

func addChange(ctx context.Context, m *library.Manager, req api.Request) (library.Change, string, error) {
	if len(req.Keys) == 0 && len(req.Collections) == 0 && len(req.Sources) == 0 {
		return library.Change{}, "", bad("nothing to add: give keys, collections or sources")
	}
	var what []string
	levels := map[string]collections.Level{}
	for _, id := range slices.Sorted(maps.Keys(req.Collections)) {
		if _, err := collections.Get(m.Collections, id); err != nil {
			return library.Change{}, "", BadRequest{err}
		}
		lv := cmp.Or(req.Collections[id], collections.Standard.String())
		l, ok := collections.ParseLevel(lv)
		if !ok {
			return library.Change{}, "", bad("level %q: use %s", lv, collections.LevelNames)
		}
		levels[id] = l
		what = append(what, "@"+id)
	}
	var entries []catalog.Entry
	for _, arg := range req.Keys {
		k, exact := catalog.SplitKey(arg)
		fam, err := m.Store.EntriesByName(ctx, k.Name)
		if err != nil {
			return library.Change{}, "", err
		}
		if len(fam) == 0 {
			return library.Change{}, "", bad("%q is not in the catalog", k.Name)
		}
		// As the add command: name:flavour, or a bare name with one flavour.
		var e *catalog.Entry
		for i := range fam {
			if (exact && fam[i].Flavour == k.Flavour) || (!exact && len(fam) == 1) {
				e = &fam[i]
			}
		}
		if e == nil {
			var keys []string
			for _, f := range fam {
				keys = append(keys, f.Key().Exact())
			}
			return library.Change{}, "", bad("give one of: %s", strings.Join(keys, ", "))
		}
		entries = append(entries, *e)
		what = append(what, e.Key().String())
	}
	var found []*library.Found
	for _, src := range req.Sources {
		f, err := m.AddSource(ctx, src.From, src.Name, src.SHA256)
		if err != nil {
			return library.Change{}, "", BadRequest{err}
		}
		found = append(found, f)
		entries = append(entries, f.Entry)
		what = append(what, f.Entry.Name)
	}
	ch, err := m.AddChange(ctx, entries, levels)
	ch.Sources = found
	if err != nil || req.Budget == nil {
		return ch, "add " + strings.Join(what, " "), err
	}
	edit, n := ch.Edit, *req.Budget
	ch.Edit = func(d *setup.Doc) error {
		d.SetBudget(n)
		return edit(d)
	}
	return ch, "add " + strings.Join(what, " ") + ", budget " + config.FormatBytes(n), nil
}

// Check checks a request for kind before it is queued, so that a mistake
// is answered at once rather than by a failed job.
func Check(ctx context.Context, m *library.Manager, kind string, req api.Request) (summary string, err error) {
	switch {
	case kind == "add" && len(req.Sources) > 0:
		// Sources are looked at by the job, not while the request waits:
		// here only their form.
		var names []string
		for _, src := range req.Sources {
			if _, err := setup.CheckSource(src.From); err != nil {
				return "", BadRequest{err}
			}
			if _, err := meta4.ParseSHA256(src.SHA256); err != nil {
				return "", bad("sha256 %v", err)
			}
			names = append(names, cmp.Or(src.Name, src.From))
		}
		rest := req
		rest.Sources = nil
		if len(rest.Keys) == 0 && len(rest.Collections) == 0 {
			return "add " + strings.Join(names, " "), nil
		}
		summary, err := Check(ctx, m, kind, rest)
		return summary + " " + strings.Join(names, " "), err
	case Of(kind).Change:
		_, summary, err = Change(ctx, m, kind, req)
		return summary, err
	case kind == "verify":
		if _, err := m.LocalKeys(ctx, req.Names); err != nil {
			return "", BadRequest{err}
		}
		return strings.TrimSpace("verify " + strings.Join(req.Names, " ")), nil
	}
	return kind, nil
}

// EditOnly reports whether a request only edits library.toml.
func EditOnly(kind string, req api.Request) bool {
	return Of(kind).EditOnly || kind == "add" && req.NoDownload
}

// Fetches reports whether a request may download, and so wants a fresh
// catalog first.
func Fetches(kind string, req api.Request) bool {
	return Of(kind).Change && !EditOnly(kind, req)
}

// Preview plans the change kind asks for, without changing anything.
func Preview(ctx context.Context, m *library.Manager, kind string, req api.Request) (*library.Preview, error) {
	if k := Of(kind); !k.Change || k.EditOnly {
		return nil, bad("%s has no plan to preview", kind)
	}
	if _, err := m.CheckSources(ctx, Of(kind).Fresh); err != nil { // Run says what it couldn't check
		return nil, err
	}
	ch, _, err := Change(ctx, m, kind, req)
	if err != nil {
		return nil, err
	}
	return m.Preview(ctx, ch)
}

// Run carries out kind. Run a kind that Locks under the state lock.
func Run(ctx context.Context, m *library.Manager, kind string, req api.Request, emit Emit) error {
	switch kind {
	case "prune":
		return prune(ctx, m, req, emit)
	case "verify":
		return verify(ctx, m, req, emit)
	case "refresh":
		if err := EnsureCatalog(ctx, m, true, true, emit); err != nil {
			return err
		}
		return sources(ctx, m, true, emit)
	}
	if !EditOnly(kind, req) {
		if err := m.ForgetSources(ctx); err != nil {
			return err
		}
		if err := sources(ctx, m, Of(kind).Fresh, emit); err != nil {
			return err
		}
	}
	ch, _, err := Change(ctx, m, kind, req)
	if err != nil {
		return err
	}
	if EditOnly(kind, req) {
		return save(ctx, m, ch, emit)
	}
	return apply(ctx, m, ch, req.Plan, emit)
}

// save edits library.toml, telling emit.
func save(ctx context.Context, m *library.Manager, ch library.Change, emit Emit) error {
	saved, err := m.Save(ctx, ch)
	if err != nil {
		return err
	}
	state := api.SavedUnchanged
	if saved {
		state = api.Saved
	}
	emit(api.Event{Type: api.EvSaved, Line: m.SetupPath, State: state})
	return nil
}

// preview plans ch. With want (a plan id from an earlier preview), it
// fails unless the plan is still the one that was shown.
func preview(ctx context.Context, m *library.Manager, ch library.Change, want string) (*library.Preview, error) {
	p, err := m.Preview(ctx, ch)
	if err == nil && want != "" && p.ID() != want {
		err = errors.New("the plan has changed since it was shown; look at it again")
	}
	return p, err
}

// installs hears how installs go, for their step results.
type installs struct {
	verified string
	old      []api.Removed // older releases deleted after the new one is in
}

// observer passes events on to emit, noting what the step result needs.
func (in *installs) observer(emit Emit) library.Observer {
	return observer(emit, func(ev library.Event) {
		switch ev.Kind {
		case library.EvDone:
			in.verified = ev.Msg
		case library.EvRemoved:
			in.old = append(in.old, api.Removed{Key: ev.Key.String(), Path: ev.File, Size: ev.Total})
		}
	})
}

// result fills sr in from what was heard and the installed file f (nil if
// none), and forgets it for the next install.
func (in *installs) result(m *library.Manager, sr *api.StepResult, f *store.File) {
	sr.OldVersions, sr.Verified = in.old, cmp.Or(in.verified, "already installed")
	if f != nil {
		sr.Path, sr.Size = filepath.Join(m.Cfg.LibraryDir, f.Path), f.Size
	}
	*in = installs{}
}

// apply plans ch and carries it out. With want (a plan id from an earlier
// preview), it does so only if the plan is still the one that was shown.
func apply(ctx context.Context, m *library.Manager, ch library.Change, want string, emit Emit) error {
	p, err := preview(ctx, m, ch, want)
	if err != nil {
		return err
	}
	for _, msg := range setup.Summarize(p.Problems) {
		emit(api.Event{Type: api.EvLog, Line: "! " + msg})
	}
	var in installs
	res, err := m.Apply(ctx, ch, p, in.observer(emit), func(r library.StepResult) {
		sr := &api.StepResult{Kind: api.StepKind(r.Step.Kind), Key: r.Step.Key.String(), Removed: api.RemovedOf(r.Removed)}
		if r.Err != nil {
			sr.Error, sr.Canceled = r.Err.Error(), errors.Is(r.Err, context.Canceled)
		}
		in.result(m, sr, r.File)
		sr.KeptFiles = ch.KeepFiles && r.Step.Kind == library.StepRemove && len(r.Step.From) > 0
		emit(api.Event{Type: api.EvStep, Step: sr})
	})
	if res == nil {
		return err
	}
	if res.Saved {
		emit(api.Event{Type: api.EvSaved, Line: m.SetupPath, State: api.Saved})
	}
	if err != nil {
		return err
	}
	cleanedUp(emit, res.Pruned, res.PruneErr)
	for _, d := range p.Deferred {
		emit(api.Event{Type: api.EvLog, Line: fmt.Sprintf("Waiting (%s): %s %s", api.WaitReason(d.Reason), api.StepKind(d.Kind), d.Key)})
	}
	if len(res.Results) == 0 && !res.Saved && len(p.Deferred) == 0 {
		emit(api.Event{Type: api.EvLog, Line: "Nothing to do: the library matches library.toml"})
	}
	if res.Failed > 0 {
		return fmt.Errorf("%d of %d changes failed", res.Failed, len(res.Results))
	}
	return nil
}

// emitPruned tells emit what was deleted.
func emitPruned(emit Emit, removed []library.Removed) {
	for _, r := range removed {
		emit(api.Event{Type: api.EvPruned, Line: r.Path, Reason: r.Reason, Total: r.Size})
	}
}

// emitNotes tells emit about problems worth a word that stop nothing.
func emitNotes(emit Emit, problems []error) {
	for _, p := range problems {
		emit(api.Event{Type: api.EvNote, Line: p.Error()})
	}
}

// cleanedUp tells emit what a cleanup after a change deleted, and why it
// stopped, if it did: a note, as the change itself went fine.
func cleanedUp(emit Emit, removed []library.Removed, err error) {
	emitPruned(emit, removed)
	if err != nil {
		emitNotes(emit, []error{fmt.Errorf("cleanup: %w", err)})
	}
}

// Tidy deletes stale partial downloads and old versions, telling emit.
func Tidy(ctx context.Context, m *library.Manager, emit Emit) {
	removed, err := m.Prune(ctx, false)
	cleanedUp(emit, removed, err)
}

// PrunePlan is what prune would delete.
func PrunePlan(ctx context.Context, m *library.Manager) ([]api.Removed, error) {
	plan, err := m.Prune(ctx, true)
	return api.RemovedOf(plan), err
}

// prune deletes what PrunePlan lists, or only req.Paths of it.
func prune(ctx context.Context, m *library.Manager, req api.Request, emit Emit) error {
	var removed []library.Removed
	var err error
	if len(req.Paths) == 0 {
		removed, err = m.Prune(ctx, false)
	} else {
		confirmed := make([]library.Removed, len(req.Paths))
		for i, p := range req.Paths {
			confirmed[i].Path = p
		}
		removed, err = m.PruneConfirmed(ctx, confirmed)
	}
	emitPruned(emit, removed)
	return err
}

func verify(ctx context.Context, m *library.Manager, req api.Request, emit Emit) error {
	keys, err := m.LocalKeys(ctx, req.Names)
	if err != nil {
		return BadRequest{err}
	}
	results, err := m.Verify(ctx, keys, observer(emit, nil))
	mismatched := 0
	for _, r := range results {
		ev := api.Event{Type: api.EvVerified, Key: r.File.Key().String()}
		if r.Err != nil {
			mismatched++
			ev.Error = r.Err.Error()
		}
		emit(ev)
	}
	switch {
	case err != nil:
		return err
	case mismatched > 0:
		return fmt.Errorf("%d of %d files do not match their sha-256", mismatched, len(results))
	case len(results) > 0:
		emit(api.Event{Type: api.EvLog, Line: fmt.Sprintf("All %d files match their sha-256", len(results))})
	}
	return nil
}

// observer passes downloads and checks on to emit, and every library
// event to also (if set).
func observer(emit Emit, also func(library.Event)) library.Observer {
	return func(ev library.Event) {
		if also != nil {
			also(ev)
		}
		key := ev.Key.String()
		switch ev.Kind {
		case library.EvStart:
			emit(api.Event{Type: api.EvProgress, Phase: api.PhaseStart, Key: key, Done: ev.Done, Total: ev.Total})
		case library.EvProgress:
			emit(api.Event{Type: api.EvProgress, Key: key, Done: ev.Done, Total: ev.Total})
		case library.EvVerify:
			emit(api.Event{Type: api.EvProgress, Phase: api.PhaseVerify, Key: key, Done: ev.Done, Total: ev.Total})
		case library.EvDone:
			emit(api.Event{Type: api.EvProgress, Phase: api.PhaseDone, Key: key, Done: ev.Done, Total: ev.Total})
		case library.EvNote:
			emit(api.Event{Type: api.EvNote, Key: key, Line: ev.Msg})
		}
	}
}

// EnsureCatalog refreshes the catalog when it is older than
// catalog_max_age, or always with force, and tells emit how it went. If
// that fails, it carries on with the cached copy, unless strict.
func EnsureCatalog(ctx context.Context, m *library.Manager, force, strict bool, emit Emit) error {
	res, err := library.EnsureCatalog(ctx, m.Store, m.HTTP, m.Cfg, force,
		func() { emit(api.Event{Type: api.EvCatalog, State: api.CatalogRefreshing}) })
	switch {
	case err != nil:
		return fmt.Errorf("refresh the catalog: %w", err)
	case res.Stale != nil && strict:
		return fmt.Errorf("refresh the catalog: %w", res.Stale)
	case res.Stale != nil:
		emit(api.Event{Type: api.EvNote, Line: fmt.Sprintf("Could not refresh the catalog, using the cached copy from %s: %v",
			res.Synced.Local().Format("2006-01-02 15:04"), res.Stale)})
	case res.Refreshed:
		state := api.CatalogUnchanged
		if res.Changed {
			state = api.CatalogChanged
		}
		emit(api.Event{Type: api.EvCatalog, State: state, Total: int64(res.Count)})
	}
	emitNotes(emit, res.Problems)
	return nil
}

// sources looks at library.toml's [sources] for releases not seen yet, or
// with all (update, refresh) for new releases of every one, telling emit
// what it could not check. A change looks before it plans, so a source
// added by hand is planned for.
func sources(ctx context.Context, m *library.Manager, all bool, emit Emit) error {
	problems, err := m.CheckSources(ctx, all)
	emitNotes(emit, problems)
	return err
}

// Suggest picks levels for the collections ids, most important first, so
// the library fits limit (0: the budget in library.toml).
func Suggest(ctx context.Context, m *library.Manager, ids []string, limit int64) (api.Suggestion, error) {
	if limit <= 0 {
		s, err := m.LoadSetup(ctx)
		if err != nil {
			return api.Suggestion{}, err
		}
		limit = s.Budget
	}
	if limit <= 0 {
		return api.Suggestion{}, bad("give a budget (--budget 250GB) or set one: zimon budget 250GB")
	}
	for _, id := range ids {
		if _, err := collections.Get(m.Collections, id); err != nil {
			return api.Suggestion{}, BadRequest{err}
		}
	}
	levels, total, ok, err := m.SuggestLevels(ctx, ids, limit)
	out := api.Suggestion{Levels: map[string]string{}, Budget: limit, Total: total, Fits: ok}
	for id, l := range levels {
		out.Levels[id] = l.String()
	}
	return out, err
}
