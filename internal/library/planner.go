package library

import (
	"context"
	"maps"
	"slices"
	"sort"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/setup"
	"github.com/kazie/zimon/internal/store"
)

// Plan works out what to download and delete.
func (m *Manager) Plan(ctx context.Context, opt PlanOptions) (*Plan, error) {
	s, w, err := m.planFor(ctx, opt)
	if err != nil {
		return nil, err
	}
	pl, err := m.newPlanner(ctx, opt)
	if err != nil {
		return nil, err
	}
	for _, name := range slices.Sorted(maps.Keys(w.Names)) {
		if pl.in(name) {
			pl.want(name, w.Names[name])
		}
	}
	pl.unwanted(w)
	if opt.RemovalsOnly {
		pl.onlyRemovals()
	}
	if opt.Only == nil && hasTypo(w, func(name string) bool { return len(pl.byName[name]) > 0 }) {
		pl.holdDeletions()
	}
	pl.admit(s)

	p := pl.p
	sort.SliceStable(p.Steps, func(i, j int) bool { return p.Steps[i].Kind < p.Steps[j].Kind })
	// "Not in the catalog" says less than "keeping your copy" for those,
	// but a source that could not be checked is not gone from a catalog.
	p.Gone = slices.DeleteFunc(p.Gone, func(name string) bool {
		return slices.Contains(w.Problems, setup.Problem{Kind: setup.SourceUnchecked, Name: name, Source: s.Sources[name]})
	})
	p.Problems = slices.DeleteFunc(slices.Clone(w.Problems), func(pr setup.Problem) bool {
		return pr.Kind == setup.NotInCatalog && slices.Contains(p.Gone, pr.Name)
	})
	return p, nil
}

// planFor returns the setup to plan for and what it wants: opt's, or
// library.toml's.
func (m *Manager) planFor(ctx context.Context, opt PlanOptions) (s *setup.Setup, w *setup.Wanted, err error) {
	if s = opt.Setup; s == nil {
		if s, err = m.LoadSetup(ctx); err != nil {
			return nil, nil, err
		}
	}
	if w = opt.Wanted; w == nil {
		if w, err = m.Wanted(ctx, s); err != nil {
			return nil, nil, err
		}
	}
	return s, w, nil
}

// planner is a Plan in the making: what is on disk, and the steps so far.
type planner struct {
	m        *Manager
	opt      PlanOptions
	p        *Plan
	active   map[catalog.Key]store.File       // the active file of each key
	versions map[catalog.Key][]store.File     // every file of each key
	partials map[catalog.Key][]store.Download // partial downloads of each key
	byName   map[string][]catalog.Key         // keys with a file or partial, per name
	switched map[catalog.Key]bool             // old flavours a switch deletes
	adds     []Step                           // new ZIMs, admitted last (see admit)
}

func (m *Manager) newPlanner(ctx context.Context, opt PlanOptions) (*planner, error) {
	files, err := m.Store.Files(ctx)
	if err != nil {
		return nil, err
	}
	downloads, err := m.Store.Downloads(ctx)
	if err != nil {
		return nil, err
	}
	pl := &planner{m: m, opt: opt, p: &Plan{},
		active:   map[catalog.Key]store.File{},
		versions: map[catalog.Key][]store.File{},
		partials: map[catalog.Key][]store.Download{},
		byName:   map[string][]catalog.Key{},
		switched: map[catalog.Key]bool{},
	}
	for _, f := range files {
		pl.versions[f.Key()] = append(pl.versions[f.Key()], f)
		if f.State == store.StateActive {
			pl.active[f.Key()] = f
			pl.p.Current += f.Size
		}
		pl.note(f.Key())
	}
	for _, d := range downloads {
		pl.partials[d.Key()] = append(pl.partials[d.Key()], d)
		pl.note(d.Key())
	}
	return pl, nil
}

func (pl *planner) note(k catalog.Key) {
	if !slices.Contains(pl.byName[k.Name], k) {
		pl.byName[k.Name] = append(pl.byName[k.Name], k)
	}
}

func (pl *planner) in(name string) bool { return pl.opt.Only == nil || pl.opt.Only[name] }

func (pl *planner) deferTo(st Step, why DeferReason) {
	pl.p.Deferred = append(pl.p.Deferred, Deferred{Step: st, Reason: why})
}

// change plans a switch or a removal, which update (unattended) leaves to apply.
func (pl *planner) change(st Step) {
	if pl.opt.Unattended {
		pl.deferTo(st, DeferApply)
	} else {
		pl.p.Steps = append(pl.p.Steps, st)
	}
}

// removal is the step that deletes k: every version and partial download.
func (pl *planner) removal(k catalog.Key) Step {
	st := Step{Kind: StepRemove, Key: k}
	if f, ok := pl.active[k]; ok {
		st.From = []store.File{f}
	}
	for _, f := range pl.versions[k] {
		st.Freed += f.Size
	}
	for _, d := range pl.partials[k] {
		st.Partial += fileSize(pl.m.partPath(d.UUID))
	}
	st.Freed += st.Partial
	return st
}

// remaining is roughly how much of e is left to download.
func (pl *planner) remaining(e *catalog.Entry) int64 {
	for _, d := range pl.partials[e.Key()] {
		if d.UUID == e.UUID {
			return d.Size - d.VerifiedBytes
		}
	}
	return e.ApproxSize
}

// want plans one wanted name: nothing to do, an update, a switch from the
// flavours on disk, or an add. A held name keeps what is on disk: its
// update or switch waits (an add goes ahead, there being nothing to keep).
func (pl *planner) want(name string, want *setup.Want) {
	if want.Entry == nil {
		// Gone upstream: every copy stays, except those asked to go.
		if slices.ContainsFunc(pl.byName[name], func(k catalog.Key) bool { return !slices.Contains(pl.opt.Drop, k) }) {
			pl.p.Gone = append(pl.p.Gone, name)
		}
		return
	}
	k := want.Entry.Key()
	st := Step{Key: k, To: want.Entry, Bytes: pl.remaining(want.Entry), Via: want.Via}
	cur, onDisk := pl.active[k]
	switch others := pl.otherFlavours(k); {
	case onDisk && cur.UUID == want.Entry.UUID:
	case onDisk:
		if newer(want.Entry, &cur) {
			st.Kind, st.From = StepUpdate, []store.File{cur}
			pl.unlessHeld(want, st, func(st Step) { pl.p.Steps = append(pl.p.Steps, st) })
		}
	case len(others) > 0:
		st.Kind = StepSwitch
		for _, ok := range others {
			pl.switched[ok] = true // held or not, unwanted leaves it to the switch
			r := pl.removal(ok)
			st.From = append(st.From, r.From...)
			st.Freed += r.Freed
		}
		pl.unlessHeld(want, st, pl.change)
	default:
		st.Kind = StepAdd
		pl.adds = append(pl.adds, st)
	}
}

// unlessHeld plans st with plan, or defers it when want is held.
func (pl *planner) unlessHeld(want *setup.Want, st Step, plan func(Step)) {
	if want.Held {
		pl.deferTo(st, DeferHeld)
	} else {
		plan(st)
	}
}

// otherFlavours lists the flavours on disk that a switch to k replaces:
// every other one of its name, except those in Drop, which go regardless.
func (pl *planner) otherFlavours(k catalog.Key) []catalog.Key {
	var out []catalog.Key
	for _, ok := range pl.byName[k.Name] {
		if _, onDisk := pl.active[ok]; ok != k && onDisk && !slices.Contains(pl.opt.Drop, ok) {
			out = append(out, ok)
		}
	}
	return out
}

// unwanted plans removing the keys with something on disk that
// library.toml no longer asks for, and those in Drop.
func (pl *planner) unwanted(w *setup.Wanted) {
	for _, name := range slices.Sorted(maps.Keys(pl.byName)) {
		if !pl.in(name) {
			continue
		}
		want := w.Names[name]
		gone := want != nil && want.Entry == nil
		for _, k := range pl.byName[name] {
			switch {
			case gone && !slices.Contains(pl.opt.Drop, k):
				// gone upstream: keep every copy not asked to go
			case pl.switched[k], !gone && want != nil && want.Entry.Key() == k:
				// the switch deletes it; stale partials of the wanted key are prune's
			default:
				pl.change(pl.removal(k))
			}
		}
	}
}

// typo reports whether a problem is a likely mistake in library.toml that
// the user can fix there, so planning around it could delete what they
// still want: an unknown collection, a region that is missing or names no
// map, or an [items] name or flavour the catalog doesn't have. onDisk says
// whether a name has a copy here: such a name is gone upstream rather than
// misspelled, and its copies stay anyway. A collection's ZIM or flavour
// gone upstream keeps that ZIM's copies too, so it holds nothing else.
func typo(pr setup.Problem, onDisk func(name string) bool) bool {
	switch pr.Kind {
	case setup.UnknownCollection, setup.NeedsRegion:
		return true
	case setup.NoSuchFlavour:
		return pr.Collection == ""
	case setup.NotInCatalog:
		return (pr.Collection == "" || pr.Region != "") && !onDisk(pr.Name)
	case setup.SourceUnchecked: // a mistyped place, or one down: either way nothing is known of it
		return !onDisk(pr.Name)
	}
	return false
}

// hasTypo reports whether w has a problem that is likely a typo (see typo).
func hasTypo(w *setup.Wanted, onDisk func(name string) bool) bool {
	return slices.ContainsFunc(w.Problems, func(pr setup.Problem) bool { return typo(pr, onDisk) })
}

// holdDeletions defers every step that deletes (removals, and switches,
// which delete the old flavour) until library.toml's problems are fixed.
// It applies to whole-library plans: apply runs unattended too (--yes), and
// a typo there must not cost files. A command that names what it changes
// (remove, level) is not held back.
func (pl *planner) holdDeletions() {
	pl.deferWhere(func(st Step) bool { return st.Kind == StepRemove || st.Kind == StepSwitch }, DeferProblem)
}

// onlyRemovals defers everything but removals: remove's mode.
func (pl *planner) onlyRemovals() {
	pl.deferWhere(func(st Step) bool { return st.Kind != StepRemove }, DeferApply)
	for _, st := range pl.adds {
		pl.deferTo(st, DeferApply)
	}
	pl.adds = nil
}

// deferWhere defers the planned steps that match, for why.
func (pl *planner) deferWhere(match func(Step) bool, why DeferReason) {
	pl.p.Steps = slices.DeleteFunc(pl.p.Steps, func(st Step) bool {
		m := match(st)
		if m {
			pl.deferTo(st, why)
		}
		return m
	})
}

// admit works out the size after the plan, adding the new ZIMs last. For
// update (unattended, and not limited to some names) they follow
// library.toml's new_items policy and budget.
func (pl *planner) admit(s *setup.Setup) {
	p := pl.p
	p.Budget = s.Budget
	p.After = p.Current
	for _, st := range p.Steps {
		p.After += growth(st)
	}
	policy := pl.opt.Unattended && pl.opt.Only == nil
	for _, st := range pl.adds {
		switch {
		case policy && s.NewItems == setup.NewAsk:
			pl.deferTo(st, DeferAsk)
		case policy && s.NewItems == setup.NewWithinBudget && s.Budget > 0 && p.After+growth(st) > s.Budget:
			pl.deferTo(st, DeferBudget)
		default:
			p.Steps = append(p.Steps, st)
			p.After += growth(st)
		}
	}
}
