package cli

import (
	"cmp"
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/setup"
	"github.com/kazie/zimon/internal/ui"
)

// change is a change to ask the backend for, plus how to talk to the user
// about it.
type change struct {
	kind        string // add, level, remove, apply or update
	req         api.Request
	nothing     string // what to say when there is nothing to do
	ask         bool   // confirm before downloading too, not only before deleting
	yes, dryRun bool
}

// applyChange previews the change, shows the plan, asks, and then has the
// backend carry out what was confirmed, showing how it goes.
func (a *app) applyChange(ctx context.Context, ch change) error {
	pv, err := a.be.Preview(ctx, ch.kind, ch.req)
	if err != nil {
		return err
	}
	p, keep := &pv.Plan, ch.req.KeepFiles
	a.showPlan(p, keep)
	defer a.showDeferred(p)
	if len(p.Steps) == 0 && !pv.Edits {
		if !p.Held() { // else showDeferred says what waits, and why
			a.out.Success("%s", cmp.Or(ch.nothing, "Nothing to do: your library matches library.toml."))
		}
		if !ch.dryRun {
			a.be.Tidy(ctx, (&shower{a: a}).show)
		}
		return nil
	}
	if ch.dryRun {
		a.out.Hint("Nothing changed (dry run).")
		return nil
	}
	if len(p.Steps) > 0 {
		if ok, err := a.confirmPlan(p, ch); err != nil || !ok {
			return err
		}
	}
	req := ch.req
	req.Plan = p.ID
	sh := &shower{a: a}
	err = a.be.Run(ctx, ch.kind, req, sh.show)
	sh.finish()
	if errors.Is(err, context.Canceled) {
		return err
	}
	if sh.failed > 0 {
		a.out.Fail("%d of %d changes failed. They stay in library.toml: zimon apply retries them, zimon remove drops them.",
			sh.failed, sh.steps)
		return errSilent
	}
	return err
}

// saveOnly edits library.toml without downloading anything.
func (a *app) saveOnly(ctx context.Context, kind string, req api.Request, next string) error {
	if err := a.be.Run(ctx, kind, req, (&shower{a: a}).show); err != nil {
		return err
	}
	a.out.Hint("%s", next)
	return nil
}

// shower shows how a change or chore goes, from its events: the same for
// one running here and a job on a remote.
type shower struct {
	a       *app
	catalog bool // show catalog fetches (refresh)
	prune   bool // count what is freed instead of listing it (prune)

	bar           *ui.Progress
	steps, failed int
	freed         int64
	verified, bad int
}

func (s *shower) finish() {
	if s.bar != nil {
		s.bar.Finish()
		s.bar = nil
	}
}

func (s *shower) show(ev api.Event) {
	out := s.a.out
	switch ev.Type {
	case api.EvProgress:
		switch ev.Phase {
		case api.PhaseStart:
			s.finish()
			s.bar = out.NewProgress(ev.Key, ev.Total, ev.Done)
			if ev.Done > 0 {
				s.bar.Note("resuming")
			}
		case api.PhaseVerify:
			if s.bar != nil {
				s.bar.Note("verifying…")
			}
		case api.PhaseDone:
			s.finish()
		default:
			if s.bar != nil {
				s.bar.Set(ev.Done)
			}
		}
		return
	case api.EvNote:
		if s.bar != nil && out.ErrTTY {
			s.bar.Finish()
		}
		out.Warn("%s", ev.Line)
		if s.bar != nil {
			s.bar.Note("")
		}
		return
	}
	s.finish()
	switch ev.Type {
	case api.EvStep:
		s.steps++
		if ev.Step.Error != "" && !ev.Step.Canceled {
			s.failed++
		}
		s.report(ev.Step)
	case api.EvSaved:
		if ev.State == api.SavedUnchanged {
			out.Success("%s already says so", ui.Bold.Render(ui.Tilde(ev.Line)))
		} else {
			out.Success("Saved %s", ui.Bold.Render(ui.Tilde(ev.Line)))
		}
	case api.EvPruned:
		s.freed += ev.Total
		if !s.prune {
			out.Hint("Removed %s %s", ev.Reason, ev.Line)
		}
	case api.EvVerified:
		s.verified++
		if ev.Error == "" {
			out.Success("%s %s", ui.Bold.Render(ev.Key), ui.Dim.Render("ok"))
		} else {
			s.bad++
			out.Fail("%s: %s", ui.Bold.Render(ev.Key), ev.Error)
		}
	case api.EvCatalog:
		switch {
		case ev.State == api.CatalogRefreshing:
			if !s.catalog { // refresh says how it went instead
				out.Hint("Refreshing the Kiwix catalog…")
			}
		case !s.catalog:
		case ev.State == api.CatalogChanged:
			out.Success("Catalog updated: %s entries", ui.Bold.Render(ui.Num(ev.Total)))
		default:
			out.Success("Catalog unchanged (%s entries)", ui.Num(ev.Total))
		}
	case api.EvWaiting:
		if s.a.remote == nil {
			out.Hint("Waiting for %s to finish… (Ctrl-C to stop)", ev.Line)
		} else {
			out.Hint("Waiting for %s to finish…", ev.Line)
		}
	}
}

// report prints how one step went.
func (s *shower) report(r *api.StepResult) {
	out := s.a.out
	switch {
	case r.Canceled:
		k, _ := catalog.SplitKey(r.Key)
		out.Hint("Stopped. %s stays in library.toml, so zimon apply (or the next update) resumes where it left off;", r.Key)
		out.Hint("zimon remove %s drops it.", k.Name)
	case r.Error != "":
		out.Fail("%s: %s", r.Key, r.Error)
	case r.Kind == api.StepRemove:
		msg := "Removed " + ui.Bold.Render(r.Key) + ui.Freed(api.Total(r.Removed))
		if r.KeptFiles {
			msg += ui.Dim.Render("; files left in " + ui.Tilde(s.libraryDir()))
		}
		out.Success("%s", msg)
	default:
		out.Success("%s %s %s", ui.Bold.Render(r.Key), ui.Dim.Render("→"),
			ui.Tilde(r.Path)+ui.Dim.Render(" ("+ui.Size(r.Size)+", "+r.Verified+")"))
		for _, rm := range r.Removed {
			out.Success("Removed %s%s", cmp.Or(rm.Key, filepath.Base(rm.Path)), ui.Freed(rm.Size))
		}
	}
	for _, rm := range r.OldVersions {
		out.Success("Removed old version %s%s", rm.Path, ui.Freed(rm.Size))
	}
}

func (s *shower) libraryDir() string {
	info, err := s.a.be.Info(context.Background()) // known by now: no request
	if err != nil {
		return "the library"
	}
	return info.LibraryDir
}

// confirmPlan asks before running a plan: always when it deletes (refusing
// without a terminal unless --yes), and before downloading when ch.ask.
func (a *app) confirmPlan(p *api.Plan, ch change) (bool, error) {
	if ch.req.KeepFiles {
		if n := partials(p); n > 0 { // the partial downloads do go
			return a.confirmDelete(ch.yes, "Forget these ZIMs and delete "+ui.Size(n)+" of partial downloads?")
		}
		if ch.yes { // no file is deleted, so ask the way an add does
			return true, nil
		}
		return a.out.Confirm("Forget these ZIMs? Their files stay on disk.", true)
	}
	q := "Go ahead?"
	if parts := ui.PlanParts(p.Download, p.Freed); len(parts) > 0 {
		s := strings.Join(parts, " and ")
		q = strings.ToUpper(s[:1]) + s[1:] + "?"
	}
	switch {
	case p.Deletes():
		return a.confirmDelete(ch.yes, q)
	case ch.ask && !ch.yes:
		return a.out.Confirm(q, true)
	}
	return true, nil
}

// partials is how many bytes of partial downloads p's removals delete.
func partials(p *api.Plan) (n int64) {
	for _, st := range p.Steps {
		if st.Kind == api.StepRemove {
			n += st.Partial
		}
	}
	return n
}

// showPlan prints the plan's problems, steps and totals. With keep (remove
// --keep-file) its removals forget ZIMs, and delete only partial downloads.
func (a *app) showPlan(p *api.Plan, keep bool) {
	for _, msg := range p.Problems {
		a.out.Warn("%s", msg)
	}
	for _, name := range p.Gone {
		a.out.Warn("%s is no longer in the Kiwix catalog; keeping your copy", ui.Bold.Render(name))
	}
	if len(p.Steps) == 0 {
		return
	}
	t := ui.NewTable("", "NAME", "CHANGE", "SIZE", "WHY")
	t.Flex = 4
	t.Right = []int{3}
	for _, st := range p.Steps {
		t.Add(stepRow(st, keep)...)
	}
	t.Render(a.out.Out, a.out.Width())
	var parts []string
	if !keep {
		parts = ui.PlanParts(p.Download, p.Freed)
	} else if n := partials(p); n > 0 {
		parts = []string{"delete " + ui.Size(n)}
	}
	size := "library " + config.FormatBytes(p.Current) + " → " + config.FormatBytes(p.After)
	if p.Budget > 0 {
		size += " of " + ui.Size(p.Budget)
		if p.After > p.Budget {
			size += " " + ui.Yellow.Render("(over budget by "+ui.Size(p.After-p.Budget)+")")
		}
	}
	parts = append(parts, size)
	if p.Free > 0 {
		f := ui.Size(p.Free) + " free"
		if p.Download > p.Free {
			f = ui.Red.Render(f)
		}
		parts = append(parts, f)
	}
	a.out.Hint("%s", strings.Join(parts, " · "))
}

// showDeferred tells what the plan left for later, and why.
func (a *app) showDeferred(p *api.Plan) {
	byReason := map[string][]string{}
	for _, d := range p.Deferred {
		key := d.Key
		if d.Reason == api.WaitHeld && len(d.From) > 0 { // a switch: what is held is the flavour on disk
			key = d.From[0]
		}
		byReason[d.Reason] = append(byReason[d.Reason], key)
	}
	for _, r := range []string{api.WaitBudget, api.WaitAsk} {
		names := byReason[r]
		if len(names) == 0 {
			continue
		}
		verb := "wait"
		if len(names) == 1 {
			verb = "waits"
		}
		why := `new_items = "ask"`
		if r == api.WaitBudget {
			why = "over the " + ui.Size(p.Budget) + " budget"
		}
		a.out.Status("%s %d new ZIM%s %s for your OK (%s): %s", ui.Yellow.Render("○"), len(names), ui.Plural(len(names)), verb, why, strings.Join(names, ", "))
		a.out.Hint("  Download %s with: zimon apply", them(len(names)))
	}
	if names := byReason[api.WaitApply]; len(names) > 0 {
		a.out.Hint("%d more change%s to match library.toml (%s): zimon apply", len(names), ui.Plural(len(names)), strings.Join(names, ", "))
	}
	if names := byReason[api.WaitHeld]; len(names) > 0 {
		a.out.Status("%s Held at the release on disk: %s", ui.Cyan.Render("="), strings.Join(names, ", "))
		a.out.Hint("  Let it change again with: zimon unhold NAME")
	}
	if names := byReason[api.WaitProblem]; len(names) > 0 {
		a.out.Warn("Nothing is deleted while library.toml has the problems above. Waiting: %s", strings.Join(names, ", "))
		a.out.Hint("  Fix library.toml, then run zimon apply again.")
	}
}

// viaText says why a ZIM is wanted, in words.
func viaText(via []string) string {
	out := make([]string, len(via))
	for i, v := range via {
		if v == setup.ViaItems {
			v = "hand-picked"
		}
		out[i] = v
	}
	return strings.Join(out, ", ")
}

// flavour is a key's flavour in words.
func flavour(key string) string {
	k, _ := catalog.SplitKey(key)
	return cmp.Or(k.Flavour, setup.Default)
}

func stepRow(st api.Step, keep bool) []string {
	why := viaText(st.Via)
	name := ui.Bold.Render(st.Key)
	if keep && st.Kind == api.StepRemove {
		if len(st.From) == 0 { // nothing on disk but a partial download, which goes
			return []string{ui.Red.Render("−"), name, "delete", ui.Size(st.Partial), ui.Dim.Render("partial download")}
		}
		note := "the file stays on disk"
		if st.Partial > 0 {
			note += "; a partial download (" + ui.Size(st.Partial) + ") is deleted"
		}
		return []string{ui.Dim.Render("−"), name, "forget", ui.Size(st.FromSize), ui.Dim.Render(note)}
	}
	date := cmp.Or(st.Date, "undated")
	switch st.Kind {
	case api.StepAdd:
		return []string{ui.Green.Render("+"), name, "new · " + date, ui.Size(st.Bytes), why}
	case api.StepUpdate:
		return []string{ui.Yellow.Render("↑"), name, cmp.Or(st.FromDate, "undated") + " → " + ui.Green.Render(date), ui.Size(st.Bytes), why}
	case api.StepSwitch:
		from := make([]string, len(st.From))
		for i, f := range st.From {
			from[i] = flavour(f)
		}
		change := strings.Join(from, ", ") + " → " + ui.Green.Render(flavour(st.Key))
		return []string{ui.Cyan.Render("⇄"), name, change, ui.Size(st.Bytes), why + ui.Dim.Render(" · frees "+ui.Size(st.Freed))}
	default:
		return []string{ui.Red.Render("−"), name, "delete", ui.Size(st.Freed), ui.Dim.Render("not in library.toml")}
	}
}
