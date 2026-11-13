package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"slices"
	"sync"
	"time"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/lock"
	"github.com/kazie/zimon/internal/store"
	"github.com/kazie/zimon/internal/task"
)

// jobs runs changes asked for through the API one at a time, and tells
// subscribers (/api/v1/events) how they go. A job that changes the library
// runs under the state lock, so never at the same time as a zimon command.
type jobs struct {
	mu      sync.Mutex
	queue   []*job
	running *job
	wake    chan struct{}               // a job was queued
	subs    map[chan api.Event]struct{} // event subscribers
	stopped <-chan struct{}             // closed when the server stops
}

// job is one queued or running change.
type job struct {
	id     int64
	run    func(ctx context.Context, j *job) error
	locks  bool               // takes the state lock while it runs
	cancel context.CancelFunc // set once it leaves the queue
	s      *Server

	mu     sync.Mutex
	lastAt map[string]time.Time // last progress event per key, for throttling
}

// enqueue records a job and queues run. by is the token's name. A job of
// a kind that changes files (see task.Kind) runs under the state lock.
func (s *Server) enqueue(ctx context.Context, kind, summary, by string, run func(context.Context, *job) error) (*store.Job, error) {
	rec, err := s.Manager.Store.AddJob(ctx, kind, summary, by)
	if err != nil {
		return nil, err
	}
	s.publish(api.Event{Type: api.EvQueued, Job: rec.ID}) // before the worker can say "started"
	s.jobs.mu.Lock()
	s.jobs.queue = append(s.jobs.queue, &job{id: rec.ID, run: run, locks: task.Of(kind).Locks, s: s, lastAt: map[string]time.Time{}})
	s.jobs.mu.Unlock()
	select {
	case s.jobs.wake <- struct{}{}:
	default:
	}
	return rec, nil
}

// work runs queued jobs until ctx ends. Jobs left in the queue then are
// marked interrupted.
func (s *Server) work(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			_ = s.Manager.Store.InterruptJobs(context.WithoutCancel(ctx))
			return
		}
		s.jobs.mu.Lock()
		var j *job
		var jctx context.Context
		if len(s.jobs.queue) > 0 {
			j, s.jobs.queue = s.jobs.queue[0], s.jobs.queue[1:]
			jctx, j.cancel = context.WithCancel(ctx)
			s.jobs.running = j
		}
		s.jobs.mu.Unlock()
		if j == nil {
			select {
			case <-ctx.Done():
			case <-s.jobs.wake:
			}
			continue
		}
		s.runJob(ctx, jctx, j)
		j.cancel()
		s.jobs.mu.Lock()
		s.jobs.running = nil
		s.jobs.mu.Unlock()
	}
}

// runJob runs j with jctx, which ends when the job is canceled or ctx (the
// server) ends.
func (s *Server) runJob(ctx, jctx context.Context, j *job) {
	_ = s.Manager.Store.StartJob(ctx, j.id)
	s.publish(api.Event{Type: api.EvStarted, Job: j.id})

	var err error
	unlock := func() {}
	if j.locks {
		unlock, err = lock.Acquire(jctx, s.Manager.Cfg.LockPath(), func(who string) {
			s.publish(api.Event{Type: api.EvWaiting, Job: j.id, Line: who})
		})
	}
	if err == nil {
		err = j.safeRun(jctx)
		unlock()
	}
	state, msg := store.JobDone, ""
	switch {
	case errors.Is(err, context.Canceled):
		state = store.JobCanceled
		if ctx.Err() != nil {
			state = store.JobInterrupted
		}
	case err != nil:
		state, msg = store.JobFailed, err.Error()
	}
	_ = s.Manager.Store.FinishJob(context.WithoutCancel(ctx), j.id, state, msg)
	s.publish(api.Event{Type: api.EvFinished, Job: j.id, State: state, Line: msg})
}

// cancelJob stops a queued or running job; it reports whether there was one.
func (s *Server) cancelJob(ctx context.Context, id int64) bool {
	s.jobs.mu.Lock()
	if r := s.jobs.running; r != nil && r.id == id {
		r.cancel()
		s.jobs.mu.Unlock()
		return true
	}
	i := slices.IndexFunc(s.jobs.queue, func(j *job) bool { return j.id == id })
	if i >= 0 {
		s.jobs.queue = slices.Delete(s.jobs.queue, i, i+1)
	}
	s.jobs.mu.Unlock()
	if i < 0 {
		return false
	}
	// Written outside the lock: publish, on every progress event, takes it.
	_ = s.Manager.Store.FinishJob(ctx, id, store.JobCanceled, "")
	s.publish(api.Event{Type: api.EvFinished, Job: id, State: store.JobCanceled})
	return true
}

// safeRun runs the job, turning a panic into its error so that one bad
// job doesn't take the server (and kiwix-serve) down.
func (j *job) safeRun(ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			j.s.logf("job %d: %v\n%s", j.id, r, debug.Stack())
			err = fmt.Errorf("zimon bug, please report it: %v", r)
		}
	}()
	return j.run(ctx, j)
}

// emit adds ev's LogLine to the job's log (and sends that as a log event,
// which the admin page shows), then sends ev to subscribers. Progress goes
// out at most four times a second per file, and always its start and end.
func (j *job) emit(ev api.Event) {
	ev.Job = j.id
	if line := ev.LogLine(); line != "" {
		if err := j.s.Manager.Store.AddJobLog(context.Background(), j.id, line); err != nil {
			j.s.logf("job %d: %v", j.id, err)
		}
		j.s.publish(api.Event{Type: api.EvLog, Job: j.id, Line: line})
	}
	switch ev.Type {
	case api.EvLog:
		return
	case api.EvProgress:
		now := time.Now()
		j.mu.Lock()
		skip := ev.Phase == "" && now.Sub(j.lastAt[ev.Key]) < 250*time.Millisecond
		if !skip {
			j.lastAt[ev.Key] = now
		}
		j.mu.Unlock()
		if skip {
			return
		}
	}
	j.s.publish(ev)
}

// publish sends ev to every subscriber that keeps up; a slow one misses it
// (GET /api/v1/jobs has the full story).
func (s *Server) publish(ev api.Event) {
	s.jobs.mu.Lock()
	defer s.jobs.mu.Unlock()
	for ch := range s.jobs.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// GET /api/v1/events: job events as Server-Sent Events, until the client
// leaves or the server stops.
func (s *Server) apiEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported here")
		return
	}
	ch := make(chan api.Event, 64)
	s.jobs.mu.Lock()
	s.jobs.subs[ch] = struct{}{}
	s.jobs.mu.Unlock()
	defer func() {
		s.jobs.mu.Lock()
		delete(s.jobs.subs, ch)
		s.jobs.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // nginx: pass events on at once
	_, _ = fmt.Fprint(w, ": zimon events\n\n")
	flusher.Flush()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case ev := <-ch:
			data, _ := json.Marshal(ev)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
		case <-ping.C:
			_, _ = fmt.Fprint(w, ": ping\n\n") // keeps proxies from closing an idle stream
		case <-r.Context().Done():
			return
		case <-s.jobs.stopped:
			return
		}
		flusher.Flush()
	}
}
