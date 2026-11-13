package store

import (
	"context"
	"strings"
	"time"

	"github.com/kazie/zimon/internal/store/gen"
)

// Job states.
const (
	JobQueued      = "queued"
	JobRunning     = "running"
	JobDone        = "done"
	JobFailed      = "failed"
	JobCanceled    = "canceled"
	JobInterrupted = "interrupted" // the server stopped while it was queued or running
)

// Job is a change the server ran (or will run) for the API.
type Job struct {
	ID       int64
	Kind     string
	Summary  string
	By       string
	State    string
	Error    string
	Log      []string
	Created  time.Time
	Started  time.Time // zero until it runs
	Finished time.Time // zero until it ends
}

// AddJob records a queued job.
func (s *Store) AddJob(ctx context.Context, kind, summary, by string) (*Job, error) {
	r, err := s.q.AddJob(ctx, gen.AddJobParams{Kind: kind, Summary: summary, By: by, Queued: JobQueued, Now: now()})
	if err != nil {
		return nil, err
	}
	j := job(r)
	return &j, nil
}

// StartJob marks a job running.
func (s *Store) StartJob(ctx context.Context, id int64) error {
	return s.q.StartJob(ctx, gen.StartJobParams{ID: id, Running: JobRunning, Now: now()})
}

// AddJobLog adds a line to a job's log as it runs, so the log is there
// while it runs and after a crash.
func (s *Store) AddJobLog(ctx context.Context, id int64, line string) error {
	return s.q.AddJobLog(ctx, gen.AddJobLogParams{ID: id, Line: strings.ReplaceAll(line, "\n", " ")}) // one line each
}

// FinishJob records how a job ended.
func (s *Store) FinishJob(ctx context.Context, id int64, state, errMsg string) error {
	return s.q.FinishJob(ctx, gen.FinishJobParams{ID: id, State: state, Error: errMsg, Now: now()})
}

// PruneJobs forgets all but the latest keep jobs.
func (s *Store) PruneJobs(ctx context.Context, keep int) error {
	return s.q.PruneJobs(ctx, int64(keep))
}

// InterruptJobs marks jobs that were queued or running when the server
// stopped.
func (s *Store) InterruptJobs(ctx context.Context) error {
	return s.q.InterruptJobs(ctx, gen.InterruptJobsParams{Interrupted: JobInterrupted, Queued: JobQueued, Running: JobRunning, Now: now()})
}

// Jobs lists the latest jobs, newest first.
func (s *Store) Jobs(ctx context.Context, limit int) ([]Job, error) {
	return each(job)(s.q.Jobs(ctx, int64(limit)))
}

// JobByID returns a job, or nil.
func (s *Store) JobByID(ctx context.Context, id int64) (*Job, error) {
	return one(each(job)(s.q.JobByID(ctx, id)))
}

func job(r gen.Job) Job {
	j := Job{ID: r.ID, Kind: r.Kind, Summary: r.Summary, By: r.By, State: r.State, Error: r.Error,
		Created: parseTime(r.CreatedAt), Started: parseTime(r.StartedAt), Finished: parseTime(r.FinishedAt)}
	if r.Log != "" {
		j.Log = strings.Split(r.Log, "\n")
	}
	return j
}
