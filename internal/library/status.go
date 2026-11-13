package library

import (
	"context"
	"maps"

	"github.com/kazie/zimon/internal/collections"
	"github.com/kazie/zimon/internal/setup"
)

// CollectionStatus is one collection with its sizes and your level.
type CollectionStatus struct {
	Collection *collections.Collection
	Sizes      [3]int64          // approximate size at each level (indexed by Level)
	Level      collections.Level // yours, when Mine
	Mine       bool              // in library.toml
}

// CollectionStatus lists every collection with its sizes and your level.
func (m *Manager) CollectionStatus(ctx context.Context) ([]CollectionStatus, error) {
	s, err := m.LoadSetup(ctx)
	if err != nil {
		return nil, err
	}
	sizes, err := m.Resolver().Sizes(ctx, m.Collections, s.Region)
	if err != nil {
		return nil, err
	}
	out := make([]CollectionStatus, len(m.Collections))
	for i := range m.Collections {
		c := &m.Collections[i]
		level, mine := s.Collections[c.ID]
		out[i] = CollectionStatus{Collection: c, Sizes: sizes[c.ID], Level: level, Mine: mine}
	}
	return out, nil
}

// Use is how much one collection (or the hand-picked [items]) takes.
type Use struct {
	Via    string            // a collection id, or setup.ViaItems
	Level  collections.Level // for collections
	Disk   int64             // on disk now
	Wanted int64             // when complete at its level
}

// BudgetReport is the library's size against its budget. A ZIM in several
// collections counts in each of them.
type BudgetReport struct {
	Uses   []Use // subscribed collections by id, then hand-picked
	Extra  int64 // on disk but not wanted (apply deletes it)
	Disk   int64 // everything on disk
	Total  int64 // everything wanted, when complete
	Budget int64 // 0 = none
}

// Budget reports how much the library takes and would take.
func (m *Manager) Budget(ctx context.Context) (*BudgetReport, error) {
	s, err := m.LoadSetup(ctx)
	if err != nil {
		return nil, err
	}
	w, err := m.Wanted(ctx, s)
	if err != nil {
		return nil, err
	}
	items, err := m.ItemsFor(ctx, w)
	if err != nil {
		return nil, err
	}
	rep := &BudgetReport{Total: w.Total(), Budget: s.Budget}
	for _, it := range items {
		if it.File != nil {
			rep.Disk += it.File.Size
			if !it.Wanted {
				rep.Extra += it.File.Size
			}
		}
	}
	per := map[string]*Use{}
	for _, id := range s.CollectionIDs() {
		per[id] = &Use{Via: id, Level: s.Collections[id]}
	}
	per[setup.ViaItems] = &Use{Via: setup.ViaItems}
	for _, want := range w.Names {
		var disk int64
		if want.Entry != nil {
			if it := items[want.Entry.Key()]; it != nil && it.File != nil {
				disk = it.File.Size
			}
		}
		for _, via := range want.Via {
			if u := per[via]; u != nil {
				u.Disk += disk
				u.Wanted += want.Size()
			}
		}
	}
	for _, id := range s.CollectionIDs() {
		rep.Uses = append(rep.Uses, *per[id])
	}
	if u := per[setup.ViaItems]; u.Disk > 0 || u.Wanted > 0 {
		rep.Uses = append(rep.Uses, *u)
	}
	return rep, nil
}

// SuggestLevels picks levels for ids, most important first, so the whole
// library fits limit: all start light, then each in turn goes to standard,
// then each to deep, while everything still fits. The rest of library.toml
// counts as it is. ok is false when even light does not fit; total is then
// the size at light.
func (m *Manager) SuggestLevels(ctx context.Context, ids []string, limit int64) (levels map[string]collections.Level, total int64, ok bool, err error) {
	s, err := m.LoadSetup(ctx)
	if err != nil {
		return nil, 0, false, err
	}
	r := m.Resolver() // shared: every trial looks up the same names
	levels = map[string]collections.Level{}
	for _, id := range ids {
		levels[id] = collections.Light
	}
	size := func() (int64, error) {
		trial := *s
		trial.Collections = maps.Clone(s.Collections)
		maps.Copy(trial.Collections, levels)
		w, err := r.Resolve(ctx, &trial)
		if err != nil {
			return 0, err
		}
		return w.Total(), nil
	}
	if total, err = size(); err != nil || total > limit {
		return nil, total, false, err
	}
	for _, target := range []collections.Level{collections.Standard, collections.Deep} {
		for _, id := range ids {
			old := levels[id]
			levels[id] = target
			n, err := size()
			if err != nil {
				return nil, 0, false, err
			}
			if n > limit {
				levels[id] = old
				continue
			}
			total = n
		}
	}
	return levels, total, true, nil
}

// CollectionDetail is what a collection holds at a level: yours, or
// standard when it is not in your library.
type CollectionDetail struct {
	CollectionStatus
	Shown collections.Level // the level Items are for
	Items []DetailItem      // every tier, giants too
}

// DetailItem is one ZIM a collection lists.
type DetailItem struct {
	setup.Expanded
	Off   bool  // switched off in library.toml
	Local *Item // where it stands here; nil when zimon doesn't know it
}

// CollectionDetail looks up the collection id and what it holds. Language
// variants the catalog lacks are left out: there is no ifixit_sv_all.
func (m *Manager) CollectionDetail(ctx context.Context, id string) (*CollectionDetail, error) {
	c, err := collections.Get(m.Collections, id)
	if err != nil {
		return nil, err
	}
	s, err := m.LoadSetup(ctx)
	if err != nil {
		return nil, err
	}
	level, mine := s.Collections[c.ID]
	shown := level
	if !mine {
		shown = collections.Standard
	}
	r := m.Resolver() // one resolver: the lookups below share it
	sizes, err := r.Sizes(ctx, []collections.Collection{*c}, s.Region)
	if err != nil {
		return nil, err
	}
	xs, err := r.Expand(ctx, c, shown, s.Region, true)
	if err != nil {
		return nil, err
	}
	w, err := r.Resolve(ctx, s)
	if err != nil {
		return nil, err
	}
	items, err := m.ItemsFor(ctx, w)
	if err != nil {
		return nil, err
	}
	d := &CollectionDetail{CollectionStatus: CollectionStatus{Collection: c, Sizes: sizes[c.ID], Level: level, Mine: mine}, Shown: shown}
	for _, x := range xs {
		if x.Gone && x.Variant {
			continue
		}
		di := DetailItem{Expanded: x}
		if choice, ok := s.Items[x.Name]; ok && choice.Kind == setup.ChoiceOff {
			di.Off = true
		}
		if x.Entry != nil {
			di.Local = items[x.Entry.Key()]
		}
		d.Items = append(d.Items, di)
	}
	return d, nil
}
