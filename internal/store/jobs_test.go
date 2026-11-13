package store

import (
	"context"
	"slices"
	"testing"
)

func TestJobs(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	a, err := s.AddJob(ctx, "apply", "apply library.toml", "laptop")
	if err != nil || a.ID == 0 || a.State != JobQueued || a.Created.IsZero() {
		t.Fatalf("add: %+v, %v", a, err)
	}
	b, _ := s.AddJob(ctx, "update", "update", "laptop")
	if err := s.StartJob(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"Removed x", "y → y.zim", "two\nlines"} {
		if err := s.AddJobLog(ctx, a.ID, line); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := s.JobByID(ctx, a.ID); got.State != JobRunning || len(got.Log) != 3 {
		t.Errorf("the log while it runs: %+v", got)
	}
	if err := s.FinishJob(ctx, a.ID, JobDone, ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.JobByID(ctx, a.ID)
	if err != nil || got.State != JobDone || !slices.Equal(got.Log, []string{"Removed x", "y → y.zim", "two lines"}) || got.Started.IsZero() || got.Finished.IsZero() {
		t.Errorf("job: %+v, %v", got, err)
	}

	// A server stopping leaves the queued one interrupted.
	if err := s.InterruptJobs(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := s.Jobs(ctx, 10)
	if err != nil || len(list) != 2 || list[0].ID != b.ID || list[0].State != JobInterrupted || list[1].State != JobDone {
		t.Errorf("jobs: %+v, %v", list, err)
	}
	if j, err := s.JobByID(ctx, 99); j != nil || err != nil {
		t.Errorf("unknown job: %+v, %v", j, err)
	}

	// Only the latest jobs are kept.
	c, _ := s.AddJob(ctx, "verify", "verify", "")
	if err := s.PruneJobs(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.Jobs(ctx, 10); len(list) != 2 || list[0].ID != c.ID || list[1].ID != b.ID {
		t.Errorf("after pruning to 2: %+v", list)
	}
}
