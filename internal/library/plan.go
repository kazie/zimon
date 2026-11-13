package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/setup"
	"github.com/kazie/zimon/internal/store"
)

// StepKind is what a plan step does.
type StepKind int

// The step kinds, in the order a plan runs them.
const (
	StepRemove StepKind = iota // delete a key's files and partial downloads
	StepUpdate                 // install a newer release of a key on disk
	StepSwitch                 // install another flavour, then delete the old one
	StepAdd                    // install a key that is not on disk yet
)

// Step is one change a plan makes.
type Step struct {
	Kind    StepKind
	Key     catalog.Key    // the key added, updated or switched to, or the one removed
	To      *catalog.Entry // the release to install (not for StepRemove)
	From    []store.File   // active files replaced or deleted
	Bytes   int64          // left to download, approximately
	Freed   int64          // bytes deleted, every version and partial download included
	Partial int64          // of Freed, partial downloads (removals only)
	Via     []string       // why it is wanted (collection ids, setup.ViaItems)
}

// DeferReason says why a plan leaves a step for later.
type DeferReason int

// The reasons a step waits.
const (
	DeferApply   DeferReason = iota // a switch or removal outside this command's job: zimon apply does it
	DeferBudget                     // a new ZIM that would take the library over its budget
	DeferAsk                        // a new ZIM, and library.toml says new_items = "ask"
	DeferProblem                    // a deletion while library.toml has a problem (a typo, say): fix it first
	DeferHeld                       // an update or switch of a ZIM in [hold]: the release on disk stays
)

// Deferred is a step a plan leaves for later.
type Deferred struct {
	Step
	Reason DeferReason
}

// Plan is what it takes to make the library match library.toml.
type Plan struct {
	Steps    []Step
	Deferred []Deferred
	Gone     []string // wanted names gone from the catalog whose copies on disk stay
	Problems []setup.Problem
	Current  int64 // size of the library's active files now
	After    int64 // size after the plan, approximately
	Budget   int64 // 0 = none
}

// Download is roughly how much the plan downloads.
func (p *Plan) Download() (n int64) {
	for _, s := range p.Steps {
		n += s.Bytes
	}
	return n
}

// Freed is how much the plan deletes.
func (p *Plan) Freed() (n int64) {
	for _, s := range p.Steps {
		n += s.Freed
	}
	return n
}

// PlanOptions narrow a plan.
type PlanOptions struct {
	Setup  *setup.Setup    // plan for this setup (nil: library.toml)
	Wanted *setup.Wanted   // what Setup wants, if already worked out
	Only   map[string]bool // only these ZIM names (nil: all)
	// Unattended is update's mode: newer releases, plus wanted ZIMs that are
	// not on disk as the new_items policy allows. Switches and removals are
	// deferred to apply. Names in Only count as approved and skip the policy.
	Unattended bool
	// RemovalsOnly is remove's mode: everything but removals is deferred, so
	// removing never downloads anything as a side effect.
	RemovalsOnly bool
	// Drop are keys to delete now, even an old flavour that would otherwise
	// wait for its switch (which remove's mode defers).
	Drop []catalog.Key
}

// growth is how much a step changes the library's size.
func growth(st Step) int64 {
	var old int64
	for _, f := range st.From {
		old += f.Size
	}
	if st.To == nil {
		return -old
	}
	return st.To.ApproxSize - old
}

// Confirmed returns the steps of p that were also in confirmed (same kind,
// key and release): what the user agreed to, minus anything that changed
// while they were deciding.
func (p *Plan) Confirmed(confirmed *Plan) []Step {
	same := func(a, b Step) bool {
		if a.Kind != b.Kind || a.Key != b.Key {
			return false
		}
		return a.To == nil || b.To != nil && a.To.UUID == b.To.UUID
	}
	var out []Step
	for _, st := range p.Steps {
		if slices.ContainsFunc(confirmed.Steps, func(c Step) bool { return same(st, c) }) {
			out = append(out, st)
		}
	}
	return out
}

// ID identifies what p does: each step's kind, key and release, and the
// files it replaces or deletes. A caller that showed a plan can check with
// it that the plan it is about to carry out is still the same.
func (p *Plan) ID() string {
	h := sha256.New()
	for _, st := range p.Steps {
		to := ""
		if st.To != nil {
			to = st.To.UUID
		}
		fmt.Fprintf(h, "%d %s %s", st.Kind, st.Key, to)
		for _, f := range st.From {
			fmt.Fprintf(h, " %s", f.UUID)
		}
		fmt.Fprintln(h)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// install carries out an add, update or switch step. A switch deletes the
// old flavour only once the new one is verified.
func (m *Manager) install(ctx context.Context, st Step, obs Observer) (*store.File, []Removed, error) {
	f, err := m.Install(ctx, *st.To, obs)
	if err != nil || st.Kind != StepSwitch {
		return f, nil, err
	}
	var old []catalog.Key
	for _, of := range st.From {
		old = append(old, of.Key())
	}
	removed, err := m.Remove(ctx, old, false)
	return f, removed, err
}

// FreeSpace is the free space on the library's filesystem, looking at the
// nearest existing parent when the library dir does not exist yet.
func (m *Manager) FreeSpace() (int64, bool) { return freeBytes(config.NearestDir(m.Cfg.LibraryDir)) }
