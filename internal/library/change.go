package library

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/collections"
	"github.com/kazie/zimon/internal/setup"
	"github.com/kazie/zimon/internal/store"
)

// Change is one "edit library.toml, then make the disk match": what add,
// remove, level, apply, suggest and update do, from the CLI, the browser or
// the web admin. Build one with AddChange, RemoveChange, LevelChange or
// ItemChange, or leave Edit nil to plan without editing (apply, update).
type Change struct {
	Edit         func(*setup.Doc) error // nil: no edit
	Names        []string               // ZIM names always in scope, e.g. the ones named on the command line
	Collections  []string               // collection ids whose ZIMs are in scope
	Drop         []catalog.Key          // keys to delete now (see PlanOptions)
	Unattended   bool                   // update's mode (see PlanOptions)
	RemovalsOnly bool                   // remove's mode (see PlanOptions)
	KeepFiles    bool                   // removals forget files but leave them on disk
	Sources      []*Found               // releases of [sources] the change adds: planned with, recorded when it is carried out
}

// AddChange adds collections at their levels and entries in exactly their
// flavour, as hand-picked [items], and an entry's source to [sources].
func (m *Manager) AddChange(ctx context.Context, entries []catalog.Entry, cols map[string]collections.Level) (Change, error) {
	keys := make([]catalog.Key, len(entries))
	for i, e := range entries {
		keys[i] = e.Key()
	}
	choices, err := m.choices(ctx, keys)
	if err != nil {
		return Change{}, err
	}
	ch := Change{Collections: slices.Sorted(maps.Keys(cols))}
	for _, k := range keys {
		ch.Names = append(ch.Names, k.Name)
	}
	ch.Edit = func(d *setup.Doc) error {
		for id, l := range cols {
			d.SetCollection(id, l)
		}
		for i, k := range keys {
			d.SetItem(k.Name, choices[i])
			if src := entries[i].Source; src != "" {
				d.SetSource(k.Name, src)
			}
		}
		return nil
	}
	return ch, nil
}

// RemoveChange takes collections and keys out of the library. A wanted key's
// name leaves [items], or is set "off" while a collection still wants it; an
// unwanted key (an old flavour, say) only loses its files. Nothing is
// downloaded as a side effect.
func (m *Manager) RemoveChange(ctx context.Context, ids []string, keys []catalog.Key, keepFiles bool) (Change, error) {
	items, err := m.Items(ctx)
	if err != nil {
		return Change{}, err
	}
	unwant := map[string]*setup.Want{}
	ch := Change{RemovalsOnly: true, KeepFiles: keepFiles, Drop: keys}
	for _, k := range keys {
		if !slices.Contains(ch.Names, k.Name) {
			ch.Names = append(ch.Names, k.Name)
		}
		it := items[k]
		if it == nil || !it.Wanted {
			continue // an old flavour: Drop removes its files
		}
		if it.Want.Entry == nil && keepsOtherCopy(items, k, keys) {
			continue // gone upstream, where every copy is wanted: the others stay
		}
		unwant[k.Name] = it.Want
	}
	ch.Edit = func(d *setup.Doc) error {
		for _, id := range ids {
			d.DeleteCollection(id)
		}
		for name, want := range unwant {
			unwantIn(d, want, name, ids)
			d.ForgetName(name)
		}
		return nil
	}
	return ch, nil
}

// keepsOtherCopy reports whether another flavour of k's name is on disk and
// not among the keys being removed. A partial download doesn't count: one
// of a ZIM gone upstream can never finish, and prune deletes it.
func keepsOtherCopy(items map[catalog.Key]*Item, k catalog.Key, removing []catalog.Key) bool {
	for ok, it := range items {
		if ok.Name == k.Name && ok != k && it.File != nil && !slices.Contains(removing, ok) {
			return true
		}
	}
	return false
}

// LevelChange puts collection id at level l (adding it if needed).
func LevelChange(id string, l collections.Level) Change {
	return Change{Edit: func(d *setup.Doc) error { d.SetCollection(id, l); return nil }, Collections: []string{id}}
}

// ItemChange sets name's [items] choice: a flavour, a level, on, off or default.
func ItemChange(name, choice string) Change {
	return Change{Edit: func(d *setup.Doc) error { d.SetItem(name, choice); return nil }, Names: []string{name}}
}

// HoldChange holds names (written as "name" or "name:flavour"; a hold is
// for the whole name) at the release on disk, or with hold false lets them
// update again. It only edits library.toml.
func (m *Manager) HoldChange(ctx context.Context, args []string, hold bool) (Change, error) {
	if len(args) == 0 {
		return Change{}, errors.New("give the names of ZIMs in your library")
	}
	s, err := m.LoadSetup(ctx)
	if err != nil {
		return Change{}, err
	}
	w, err := m.Wanted(ctx, s)
	if err != nil {
		return Change{}, err
	}
	items, err := m.ItemsFor(ctx, w)
	if err != nil {
		return Change{}, err
	}
	dates := map[string]string{} // name → the release kept
	for _, arg := range args {
		k, _ := catalog.SplitKey(arg)
		name := k.Name
		if _, ok := dates[name]; ok {
			continue
		}
		if !hold {
			if !s.Held(name) {
				return Change{}, fmt.Errorf("%s is not held (see: zimon hold)", name)
			}
			dates[name] = ""
			continue
		}
		var on *Item // what is kept: the wanted flavour's file, else another's
		for _, it := range SortedItems(items) {
			if it.Key.Name == name && it.File != nil && it.Want != nil && (on == nil || it.Wanted) {
				on = it
			}
		}
		if on == nil {
			return Change{}, fmt.Errorf("%s has no file in your library to hold (see: zimon list)", name)
		}
		dates[name] = cmp.Or(on.File.Date, "undated")
	}
	return Change{Edit: func(d *setup.Doc) error {
		for _, name := range slices.Sorted(maps.Keys(dates)) { // the same file every time
			if hold {
				d.SetHold(name, dates[name])
			} else {
				d.DeleteHold(name)
			}
		}
		return nil
	}}, nil
}

// Preview is a change planned against a copy of library.toml with the edit
// applied. Nothing is saved or downloaded.
type Preview struct {
	*Plan
	Edits bool            // applying it rewrites library.toml
	scope map[string]bool // names planned for; nil: all
}

// Preview plans ch. For an edit, Problems lists only what the edit brings up
// that was not there before.
func (m *Manager) Preview(ctx context.Context, ch Change) (*Preview, error) {
	doc, err := m.OpenSetup(ctx)
	if err != nil {
		return nil, err
	}
	before, err := doc.Setup()
	if err != nil {
		return nil, err
	}
	edits := false
	if ch.Edit != nil {
		if err := doc.Editable(); err != nil {
			return nil, err
		}
		if edits, err = edit(doc, ch.Edit); err != nil {
			return nil, err
		}
	}
	s, err := doc.Setup()
	if err != nil {
		return nil, err
	}
	r := m.resolver(ch.Sources) // one resolver: before and after share the catalog lookups
	after, err := r.Resolve(ctx, s)
	if err != nil {
		return nil, err
	}
	var scope map[string]bool
	var old *setup.Wanted
	if ch.Edit != nil || len(ch.Names) > 0 || len(ch.Collections) > 0 {
		scope = map[string]bool{}
		for _, n := range ch.Names {
			scope[n] = true
		}
		if ch.Edit != nil {
			if old, err = r.Resolve(ctx, before); err != nil {
				return nil, err
			}
			widenScope(scope, old, after, ch.Collections)
		}
	}
	p, err := m.Plan(ctx, PlanOptions{Setup: s, Wanted: after, Only: scope, Drop: ch.Drop, Unattended: ch.Unattended, RemovalsOnly: ch.RemovalsOnly})
	if err != nil {
		return nil, err
	}
	if old != nil {
		p.Problems = slices.DeleteFunc(p.Problems, func(pr setup.Problem) bool { return slices.Contains(old.Problems, pr) })
	}
	return &Preview{Plan: p, Edits: edits, scope: scope}, nil
}

// widenScope adds every name after wants differently from before, plus
// everything collections want, so adding one again fetches what is still
// missing.
func widenScope(scope map[string]bool, before, after *setup.Wanted, collections []string) {
	for n, w := range after.Names {
		b := before.Names[n]
		if b == nil || (b.Entry == nil) != (w.Entry == nil) || b.Entry != nil && b.Entry.UUID != w.Entry.UUID {
			scope[n] = true
		}
		if slices.ContainsFunc(w.Via, func(v string) bool { return slices.Contains(collections, v) }) {
			scope[n] = true
		}
	}
	for n := range before.Names {
		if after.Names[n] == nil {
			scope[n] = true
		}
	}
}

// commit saves ch's edit (saved says whether that wrote library.toml) and
// returns the steps of a fresh plan that were also in the preview: what the
// user agreed to, minus anything that changed while they were deciding (a
// new release, another zimon's edit).
func (m *Manager) commit(ctx context.Context, ch Change, p *Preview) (steps []Step, saved bool, err error) {
	if saved, err = m.Save(ctx, ch); err != nil {
		return nil, false, err
	}
	if len(p.Steps) == 0 {
		return nil, saved, nil
	}
	fresh, err := m.Plan(ctx, PlanOptions{Only: p.scope, Drop: ch.Drop, Unattended: ch.Unattended, RemovalsOnly: ch.RemovalsOnly})
	if err != nil {
		return nil, saved, err
	}
	return fresh.Confirmed(p.Plan), saved, nil
}

// Save carries out ch's edit of library.toml, after recording the sources
// it adds, and reports whether library.toml changed. Call it under the
// state lock.
func (m *Manager) Save(ctx context.Context, ch Change) (saved bool, err error) {
	if err := m.NoteSources(ctx, ch.Sources); err != nil || ch.Edit == nil {
		return false, err
	}
	return m.Edit(ctx, ch.Edit)
}

// StepResult is what running one step did.
type StepResult struct {
	Step    Step
	File    *store.File // the installed file (adds, updates, switches)
	Removed []Removed   // what was deleted: the step's files, or a switch's old flavour
	Err     error
}

// Applied is what Apply did.
type Applied struct {
	Saved    bool // the edit changed library.toml
	Results  []StepResult
	Failed   int
	Pruned   []Removed // stale partial downloads cleaned up afterwards
	PruneErr error
}

// Apply saves ch's edit and carries out the preview's steps that still hold.
// Removals go first and together, so library.xml (and kiwix-serve) reloads
// once; then each install, one at a time, carrying on after a failure.
// report, if set, hears about every step as it finishes; obs follows the
// downloads. Apply stops early only when ctx is cancelled. Call it under
// the state lock.
func (m *Manager) Apply(ctx context.Context, ch Change, p *Preview, obs Observer, report func(StepResult)) (*Applied, error) {
	steps, saved, err := m.commit(ctx, ch, p)
	if err != nil {
		return nil, err
	}
	out := &Applied{Saved: saved}
	done := func(r StepResult) {
		if r.Err != nil {
			out.Failed++
		}
		out.Results = append(out.Results, r)
		if report != nil {
			report(r)
		}
	}
	var gone []catalog.Key
	for _, st := range steps {
		if st.Kind == StepRemove {
			gone = append(gone, st.Key)
		}
	}
	if len(gone) > 0 {
		removed, err := m.Remove(ctx, gone, ch.KeepFiles)
		for _, st := range steps {
			if st.Kind == StepRemove {
				r := StepResult{Step: st, Err: err}
				for _, rm := range removed {
					if rm.Key == st.Key {
						r.Removed = append(r.Removed, rm)
					}
				}
				done(r)
			}
		}
	}
	for _, st := range steps {
		if st.Kind == StepRemove {
			continue
		}
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		f, removed, err := m.install(ctx, st, obs)
		done(StepResult{Step: st, File: f, Removed: removed, Err: err})
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
	}
	out.Pruned, out.PruneErr = m.Prune(ctx, false)
	return out, nil
}
