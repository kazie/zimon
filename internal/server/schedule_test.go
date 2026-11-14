package server

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/library"
	"github.com/kazie/zimon/internal/store"
)

func TestNextRun(t *testing.T) {
	loc := time.FixedZone("CET", 3600)
	at := func(s string) time.Time {
		t, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
		if err != nil {
			panic(err)
		}
		return t
	}
	for _, c := range []struct {
		name, now, last, every, want string
	}{
		{"first run: the next 03:30", "2026-11-09 10:00", "", config.ScheduleWeekly, "2026-11-10 03:30"},
		{"first run, before 03:30", "2026-11-09 01:00", "", config.ScheduleDaily, "2026-11-09 03:30"},
		{"daily, after a late run", "2026-11-09 10:00", "2026-11-09 03:39", config.ScheduleDaily, "2026-11-10 03:30"},
		{"weekly", "2026-11-09 10:00", "2026-11-09 03:35", config.ScheduleWeekly, "2026-11-16 03:30"},
		{"monthly: on the 1st", "2026-11-09 10:00", "2026-11-09 03:30", config.ScheduleMonthly, "2026-12-01 03:30"},
		{"missed while down: due at once", "2026-11-20 10:00", "2026-11-09 03:30", config.ScheduleWeekly, "2026-11-16 03:30"},
		{"monthly from the 31st", "2027-01-31 10:00", "2027-01-31 03:30", config.ScheduleMonthly, "2027-02-01 03:30"},
		{"monthly, after February", "2027-02-01 10:00", "2027-02-01 03:30", config.ScheduleMonthly, "2027-03-01 03:30"},
	} {
		var last time.Time
		if c.last != "" {
			last = at(c.last)
		}
		if got := nextRun(at(c.now), last, c.every, 3, 30); !got.Equal(at(c.want)) {
			t.Errorf("%s: %s, want %s", c.name, got.Format("2006-01-02 15:04"), c.want)
		}
		// The last run is stored in UTC; the time is still local.
		if got := nextRun(at(c.now), last.UTC(), c.every, 3, 30); !got.Equal(at(c.want)) {
			t.Errorf("%s, last in UTC: %s, want %s", c.name, got.In(loc).Format("2006-01-02 15:04"), c.want)
		}
	}

	// A run caught up late counts as its slot's, so the schedule keeps its
	// days: a weekly Monday run caught up on Wednesday stays on Mondays.
	if got := latestSlot(at("2026-11-18 14:00"), at("2026-11-16 03:30"), config.ScheduleWeekly, 3, 30); !got.Equal(at("2026-11-16 03:30")) {
		t.Errorf("caught up: %s", got.Format("2006-01-02 15:04"))
	}
	// After weeks down, it counts as the latest slot, not the first missed one.
	if got := latestSlot(at("2026-12-10 14:00"), at("2026-11-16 03:30"), config.ScheduleWeekly, 3, 30); !got.Equal(at("2026-12-07 03:30")) {
		t.Errorf("after weeks down: %s", got.Format("2006-01-02 15:04"))
	}
}

// The scheduler on a fresh server (never run) runs at the first slot, retries
// a failed run an hour later, and lets an update already running count.
func TestSchedulerTicks(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)
	m.Cfg.Schedule = config.ScheduleConfig{Update: config.ScheduleDaily, Time: "03:30"}
	s := New(m, "")
	start := time.Date(2026, 11, 9, 1, 0, 0, 0, time.Local)
	sc := &scheduler{s: s, next: nextRun(start, time.Time{}, config.ScheduleDaily, 3, 30), jitter: 5 * time.Minute}
	queued := func() []string {
		var out []string
		list, _ := m.Store.Jobs(ctx, 50)
		for _, j := range list {
			out = append(out, j.Kind+" "+j.State)
		}
		return out
	}
	for now := start; now.Before(start.Add(3 * time.Hour)); now = now.Add(time.Minute) {
		sc.tick(ctx, now)
	}
	if got := queued(); len(got) != 1 || got[0] != "update queued" {
		t.Fatalf("after the first slot: %q", got)
	}
	if v, _ := m.Store.Get(ctx, kvLastUpdate); v == "" {
		t.Error("the run was not recorded")
	}

	// It failed: retried an hour after, not before.
	if err := m.Store.FinishJob(ctx, sc.pending, store.JobFailed, "Kiwix is down"); err != nil {
		t.Fatal(err)
	}
	// The job's times are the real clock's: so is the rest, with the next
	// slot a day off, so only the retry can be due.
	failedAt := time.Now()
	sc.next = failedAt.Add(24 * time.Hour)
	sc.tick(ctx, failedAt.Add(30*time.Minute))
	if got := queued(); len(got) != 1 {
		t.Errorf("retried too soon: %q", got)
	}
	sc.tick(ctx, failedAt.Add(retryAfter+time.Minute))
	if got := queued(); len(got) != 2 || got[0] != "update queued" {
		t.Errorf("not retried: %q", got)
	}

	// The next day's slot, with an update still queued: that one counts.
	sc.tick(ctx, sc.next.Add(sc.jitter))
	if got := queued(); len(got) != 2 {
		t.Errorf("a second update was queued: %q", got)
	}
}

// A weekly run missed while the server was down is queued as soon as it
// starts: its slot, plus jitter, is long past.
func TestScheduledUpdate(t *testing.T) {
	ctx := context.Background()
	l := newLiveServer(t, func(m *library.Manager) {
		m.Cfg.Schedule = config.ScheduleConfig{Update: config.ScheduleWeekly, Time: "03:30"}
		if err := m.Store.Set(ctx, kvLastUpdate, time.Now().AddDate(0, 0, -10).UTC().Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	})
	var jobs []api.Job
	waitFor(t, "the missed update", func() bool {
		l.do(http.MethodGet, "/api/v1/jobs", "", &jobs)
		return len(jobs) > 0
	})
	if len(jobs) != 1 || jobs[0].Kind != "update" || jobs[0].By != "schedule" {
		t.Fatalf("jobs: %+v", jobs)
	}
	if j := l.finished(jobs[0].ID); j.State != "done" {
		t.Errorf("scheduled update: %+v", j)
	}
	var sched map[string]string
	l.do(http.MethodGet, "/api/v1/schedule", "", &sched)
	if sched["update"] != "weekly" || sched["last"] == "" || sched["next"] <= sched["last"] {
		t.Errorf("schedule: %v", sched)
	}
}

// A scheduled run cut short by a restart is run again as soon as the
// server is back, not a period later.
func TestSchedulerRetriesAfterRestart(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)
	m.Cfg.Schedule = config.ScheduleConfig{Update: config.ScheduleWeekly, Time: "03:30"}
	rec, err := m.Store.AddJob(ctx, "update", "scheduled update", "schedule")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Store.InterruptJobs(ctx); err != nil { // as Run does at start
		t.Fatal(err)
	}
	if got := lastFailedRun(ctx, m.Store); got != rec.ID {
		t.Fatalf("lastFailedRun = %d, want %d", got, rec.ID)
	}
	now := time.Now()
	sc := &scheduler{s: New(m, ""), next: now.Add(24 * time.Hour), pending: lastFailedRun(ctx, m.Store)}
	sc.tick(ctx, now)
	if list, _ := m.Store.Jobs(ctx, 10); len(list) != 2 || list[0].State != store.JobQueued {
		t.Errorf("not run again at start: %+v", list)
	}

	// An update someone else ran since counts: nothing to retry.
	other, _ := m.Store.AddJob(ctx, "update", "update", "laptop")
	_ = m.Store.FinishJob(ctx, other.ID, store.JobDone, "")
	if got := lastFailedRun(ctx, m.Store); got != 0 {
		t.Errorf("lastFailedRun after a later update = %d", got)
	}
}

// A new library has no catalog: zimon serve fetches it as a job when it
// starts, tries again while that fails (the tests' catalog is unreachable),
// and leaves a library with a catalog alone.
func TestFirstCatalog(t *testing.T) {
	ctx := context.Background()
	for _, empty := range []bool{true, false} {
		m := newManager(t)
		if empty {
			if err := m.Store.ReplaceCatalog(ctx, nil, ""); err != nil {
				t.Fatal(err)
			}
		}
		s := New(m, "")
		s.catalogRetry = 20 * time.Millisecond
		serve(t, s)
		refreshes := func() []store.Job {
			jobs, _ := m.Store.Jobs(ctx, 50)
			return slices.DeleteFunc(jobs, func(j store.Job) bool { return j.Kind != "refresh" })
		}
		if empty {
			waitFor(t, "a second try", func() bool { return len(refreshes()) >= 2 })
			for _, j := range refreshes() {
				if j.By != "zimon serve" || j.Summary != "first catalog" {
					t.Errorf("job %+v, want the first catalog, by zimon serve", j)
				}
			}
			continue
		}
		time.Sleep(100 * time.Millisecond)
		if got := refreshes(); len(got) != 0 {
			t.Errorf("with a catalog: %+v, want none", got)
		}
	}
}
