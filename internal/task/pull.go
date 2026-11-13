package task

import (
	"cmp"
	"context"
	"errors"
	"fmt"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/library"
)

// Pulled is a ZIM to copy from another zimon (zimon pull): that zimon's
// release (its UUID, date and title), which may be older than the catalog's.
type Pulled struct {
	Release catalog.Entry
	Source  library.Source
}

// Pull adds what req asks for to library.toml, as add does, then
// downloads files from another zimon instead of from Kiwix. With req.Plan
// (the id of the add's plan, from which files were picked), it does so only
// if that plan still holds. Run it under the state lock.
func Pull(ctx context.Context, m *library.Manager, req api.Request, files []Pulled, emit Emit) error {
	ch, _, err := Change(ctx, m, "add", req)
	if err != nil {
		return err
	}
	if _, err := preview(ctx, m, ch, req.Plan); err != nil {
		return err
	}
	saved, err := m.Save(ctx, ch)
	if err != nil {
		return err
	}
	if saved { // a pull run again to resume says nothing about it
		emit(api.Event{Type: api.EvSaved, Line: m.SetupPath, State: api.Saved})
	}
	failed := 0
	for _, p := range files {
		sr := pullOne(ctx, m, p, emit)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if sr == nil {
			continue
		}
		if sr.Error != "" {
			failed++
		}
		emit(api.Event{Type: api.EvStep, Step: sr})
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d downloads failed", failed, len(files))
	}
	return nil
}

// pullOne downloads p and tells how it went, or returns nil, with a note,
// when this library has a release as new by now, or a download of the
// catalog's newer release is under way.
func pullOne(ctx context.Context, m *library.Manager, p Pulled, emit Emit) *api.StepResult {
	k := p.Release.Key()
	sr := &api.StepResult{Kind: api.StepAdd, Key: k.String()}
	note := func(format string, args ...any) *api.StepResult {
		emit(api.Event{Type: api.EvNote, Key: sr.Key, Line: sr.Key + ": " + fmt.Sprintf(format, args...)})
		return nil
	}
	fail := func(err error) *api.StepResult {
		sr.Error = err.Error()
		return sr
	}
	active, err := m.Store.ActiveFile(ctx, k)
	if err != nil {
		return fail(err)
	}
	if active != nil {
		if active.UUID != p.Release.UUID && active.Date >= p.Release.Date {
			return note("keeping this library's copy from %s, as new as the one to pull", active.Date)
		}
		sr.Kind = api.StepUpdate
	}
	e := p.Release
	known, err := m.Store.EntriesByKeys(ctx, []catalog.Key{k})
	if err != nil {
		return fail(err)
	}
	if c, ok := known[k]; ok { // the catalog's entry, for the language, summary and so on
		e = c
		e.Title = cmp.Or(p.Release.Title, c.Title)
		if c.UUID != p.Release.UUID { // another release: what the catalog says of its own does not hold
			e.UUID, e.Date, e.Meta4URL, e.ArticleCount, e.MediaCount, e.ApproxSize = p.Release.UUID, p.Release.Date, "", 0, 0, 0
		}
	}
	var in installs
	f, err := m.Pull(ctx, e, p.Source, in.observer(emit))
	switch {
	case errors.Is(err, library.ErrNewerPartial):
		return note("not pulled: %v; zimon apply finishes it", err)
	case err != nil:
		return fail(err)
	}
	in.result(m, sr, f)
	return sr
}
