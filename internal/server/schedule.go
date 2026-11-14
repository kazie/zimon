package server

import (
	"context"
	"math/rand/v2"
	"net/http"
	"slices"
	"time"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/store"
)

// kvLastUpdate remembers when the schedule last ran update, so a run missed
// while the server was down happens when it is back.
const kvLastUpdate = "schedule_last_update"

// maxJitter spreads scheduled runs, so servers set to the same time don't
// all ask Kiwix at once.
const maxJitter = 10 * time.Minute

// retryAfter is how long the schedule waits to try again after a scheduled
// update fails (Kiwix down, say).
const retryAfter = time.Hour

// slot is the first hour:minute at or after from, in from's time zone.
func slot(from time.Time, hour, minute int) time.Time {
	t := time.Date(from.Year(), from.Month(), from.Day(), hour, minute, 0, 0, from.Location())
	if t.Before(from) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

// addPeriod is t one schedule period later. Monthly runs are on the 1st:
// a day kept from month to month would slip to the 28th after February.
func addPeriod(t time.Time, every string) time.Time {
	switch every {
	case config.ScheduleDaily:
		return t.AddDate(0, 0, 1)
	case config.ScheduleWeekly:
		return t.AddDate(0, 0, 7)
	}
	y, m, _ := t.Date()
	return time.Date(y, m+1, 1, t.Hour(), t.Minute(), 0, 0, t.Location())
}

// nextRun is when the schedule (every, at hour:minute in now's time zone)
// runs update next, given the slot of the last run (zero: never). That may
// be in the past (the server was down, or the machine asleep): a missed
// run, due now.
func nextRun(now, last time.Time, every string, hour, minute int) time.Time {
	if last.IsZero() {
		return slot(now, hour, minute)
	}
	// A run happens up to maxJitter after its slot; going back a little
	// finds the slot it belonged to.
	base := slot(last.In(now.Location()).Add(-maxJitter-time.Minute), hour, minute)
	return slot(addPeriod(base, every), hour, minute)
}

// latestSlot is the last slot on the schedule at or before now, starting
// from due: what a run at now counts as, so a late (caught up) run doesn't
// move the schedule.
func latestSlot(now, due time.Time, every string, hour, minute int) time.Time {
	for {
		next := slot(addPeriod(due, every), hour, minute)
		if next.After(now) {
			return due
		}
		due = next
	}
}

// scheduler is the state of the schedule loop.
type scheduler struct {
	s       *Server
	next    time.Time     // the next slot
	jitter  time.Duration // how long after it the run starts
	pending int64         // the last scheduled update job, until it succeeds
}

// schedule queues update jobs as the config says, until ctx ends. It looks
// at the clock every minute rather than sleeping until the next run, so a
// machine waking from sleep catches up.
func (s *Server) schedule(ctx context.Context) {
	if s.Manager.Cfg.Schedule.Update == config.ScheduleOff {
		return
	}
	sc := &scheduler{s: s, next: s.nextScheduled(s.lastUpdate(ctx)), jitter: rand.N(maxJitter), pending: lastFailedRun(ctx, s.Manager.Store)}
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		sc.tick(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// firstCatalog fetches the catalog while there is none (a new library):
// the admin page needs it for sizes and search, and a scheduled update may
// be days away, or off. A fetch that fails (the network not up yet at boot)
// is tried again every s.catalogRetry; an update or refresh already queued
// or running counts as the fetch.
func (s *Server) firstCatalog(ctx context.Context) {
	for {
		_, _, count, err := s.Manager.Store.CatalogState(ctx)
		switch {
		case err != nil:
			s.logf("Reading the catalog: %v", err)
		case count > 0:
			return
		case s.activeJob(ctx, "refresh", "update") == 0: // both fetch the catalog
			if _, err := s.enqueue(ctx, "refresh", "first catalog", "zimon serve", s.taskJob("refresh", api.Request{}, false)); err != nil {
				s.logf("Fetching the catalog: %v", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.catalogRetry):
		}
	}
}

// activeJob is the id of a queued or running job of one of kinds, or 0.
func (s *Server) activeJob(ctx context.Context, kinds ...string) int64 {
	jobs, err := s.Manager.Store.Jobs(ctx, 20)
	if err != nil {
		return 0
	}
	for _, j := range jobs {
		if slices.Contains(kinds, j.Kind) && (j.State == store.JobQueued || j.State == store.JobRunning) {
			return j.ID
		}
	}
	return 0
}

// tick does what is due at now: the scheduled run, or a retry.
func (sc *scheduler) tick(ctx context.Context, now time.Time) {
	s := sc.s
	every := s.Manager.Cfg.Schedule.Update
	hour, minute := s.Manager.Cfg.Schedule.At()
	if !now.Before(sc.next.Add(sc.jitter)) {
		sc.run(ctx)
		done := latestSlot(now, sc.next.In(now.Location()), every, hour, minute)
		_ = s.Manager.Store.Set(ctx, kvLastUpdate, done.UTC().Format(time.RFC3339))
		sc.next = nextRun(now, done, every, hour, minute)
		sc.jitter = rand.N(maxJitter)
		return
	}
	if sc.pending == 0 {
		return
	}
	j, err := s.Manager.Store.JobByID(ctx, sc.pending)
	switch {
	case err != nil || j == nil:
	case j.State == store.JobFailed || j.State == store.JobInterrupted:
		due := j.Finished // interrupted: the server stopped, so go again now
		if j.State == store.JobFailed {
			due = due.Add(retryAfter)
		}
		if !now.Before(due) {
			sc.run(ctx)
		}
	case j.State != store.JobQueued && j.State != store.JobRunning:
		sc.pending = 0 // done, or canceled by someone
	}
}

// run queues an update, unless one is already queued or running: that one
// counts as the scheduled run.
func (sc *scheduler) run(ctx context.Context) {
	s := sc.s
	if sc.pending = s.activeJob(ctx, "update"); sc.pending != 0 {
		return
	}
	rec, err := s.enqueue(ctx, "update", "scheduled update", "schedule", s.taskJob("update", api.Request{}, true))
	if err != nil {
		s.logf("scheduled update: %v", err)
		return
	}
	sc.pending = rec.ID
}

// lastFailedRun is the latest update if the schedule queued it and it
// failed or was interrupted (0 if not), so that a restart still retries it.
func lastFailedRun(ctx context.Context, st *store.Store) int64 {
	jobs, _ := st.Jobs(ctx, 50)
	for _, j := range jobs {
		if j.Kind != "update" {
			continue
		}
		if j.By == "schedule" && (j.State == store.JobFailed || j.State == store.JobInterrupted) {
			return j.ID
		}
		return 0
	}
	return 0
}

// lastUpdate is the slot of the last scheduled run (zero: never).
func (s *Server) lastUpdate(ctx context.Context) time.Time {
	v, _ := s.Manager.Store.Get(ctx, kvLastUpdate)
	last, _ := time.Parse(time.RFC3339, v)
	return last
}

// nextScheduled is when update runs next, after the last run's slot.
func (s *Server) nextScheduled(last time.Time) time.Time {
	sc := s.Manager.Cfg.Schedule
	hour, minute := sc.At()
	return nextRun(time.Now(), last, sc.Update, hour, minute)
}

// GET /api/v1/schedule: how often update runs, and when it runs next.
func (s *Server) apiSchedule(w http.ResponseWriter, r *http.Request) {
	sc := s.Manager.Cfg.Schedule
	out := map[string]string{"update": sc.Update, "time": sc.Time}
	last := s.lastUpdate(r.Context())
	if !last.IsZero() {
		out["last"] = api.Time(last)
	}
	if sc.Update != config.ScheduleOff {
		next := s.nextScheduled(last)
		if now := time.Now(); next.Before(now) {
			next = now // a missed run is due now
		}
		out["next"] = api.Time(next)
	}
	writeJSON(w, http.StatusOK, out)
}
