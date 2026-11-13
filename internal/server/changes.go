package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/setup"
	"github.com/kazie/zimon/internal/store"
	"github.com/kazie/zimon/internal/task"
)

// jobRoutes adds the API that changes the library. Every change and chore
// runs as a job (see jobs, and package task); the changes with a plan also
// answer ?preview=1 with it, without changing anything.
func (s *Server) jobRoutes(mux *http.ServeMux, auth authFunc) {
	for _, kind := range task.Kinds() {
		mux.Handle("POST /api/v1/"+kind, auth(s.apiStart(kind)))
	}
	mux.Handle("GET /api/v1/prune", auth(s.apiPrunePlan))
	mux.Handle("GET /api/v1/suggest", auth(s.apiSuggest))
	mux.Handle("GET /api/v1/jobs", auth(s.apiJobs))
	mux.Handle("GET /api/v1/schedule", auth(s.apiSchedule))
	mux.Handle("GET /api/v1/jobs/{id}", auth(s.apiJob))
	mux.Handle("POST /api/v1/jobs/{id}/cancel", auth(s.apiCancel))
	mux.Handle("GET /api/v1/events", auth(s.apiEvents))
}

// apiStart handles POST /api/v1/KIND: it checks the request and starts
// the job, or with ?preview=1 answers with the plan.
func (s *Server) apiStart(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req api.Request
		if err := decodeBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		for _, src := range req.Sources {
			if !config.IsURL(src.From) {
				writeError(w, http.StatusBadRequest, src.From+": the API adds ZIMs from URLs; add a file with zimon add on the server itself")
				return
			}
		}
		ctx := r.Context()
		if r.URL.Query().Get("preview") != "" {
			// The job refreshes an old catalog before it runs (update
			// always): so does the preview, or the job's plan would differ
			// from the one shown.
			if err := s.ensureCatalog(ctx, task.Of(kind).Fresh); err != nil {
				writeError(w, http.StatusBadGateway, err.Error())
				return
			}
			pv, err := s.library().Preview(ctx, kind, req)
			if err != nil {
				writeChangeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, pv)
			return
		}
		summary, err := task.Check(ctx, s.Manager, kind, req)
		if err != nil {
			writeChangeError(w, err)
			return
		}
		s.startJob(w, r, kind, summary, s.taskJob(kind, req, false))
	}
}

// taskJob carries out kind as a job. A change that downloads refreshes an
// old catalog first (update always); with strict, the job fails when that
// fails, rather than carry on with the cached copy.
func (s *Server) taskJob(kind string, req api.Request, strict bool) func(context.Context, *job) error {
	return func(ctx context.Context, j *job) error {
		if task.Fetches(kind, req) {
			if err := task.EnsureCatalog(ctx, s.Manager, task.Of(kind).Fresh, strict, j.emit); err != nil {
				return err
			}
		}
		return task.Run(ctx, s.Manager, kind, req, j.emit)
	}
}

// ensureCatalog refreshes an old catalog (always, with force) outside a
// job, carrying on with the cached copy if that fails.
func (s *Server) ensureCatalog(ctx context.Context, force bool) error {
	return task.EnsureCatalog(ctx, s.Manager, force, false, func(api.Event) {})
}

// GET /api/v1/prune: what prune would delete. Send the paths back with
// POST /api/v1/prune to delete only those.
func (s *Server) apiPrunePlan(w http.ResponseWriter, r *http.Request) {
	if err := s.ensureCatalog(r.Context(), false); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	plan, err := s.library().PrunePlan(r.Context())
	answer(w, plan, err)
}

// GET /api/v1/suggest?c=linux&c=games&budget=250000000000: levels for the
// collections, most important first, that fit the budget (default: the
// one in library.toml).
func (s *Server) apiSuggest(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.ParseInt(q.Get("budget"), 10, 64)
	sg, err := s.library().Suggest(r.Context(), q["c"], limit)
	if err != nil {
		writeChangeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sg)
}

// startJob queues run and answers 202 with the job.
func (s *Server) startJob(w http.ResponseWriter, r *http.Request, kind, summary string, run func(context.Context, *job) error) {
	rec, err := s.enqueue(r.Context(), kind, summary, tokenName(r), run)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Location", "/api/v1/jobs/"+strconv.FormatInt(rec.ID, 10))
	writeJSON(w, http.StatusAccepted, api.JobOf(*rec))
}

func decodeBody(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("request body: %w", err)
	}
	return nil
}

func writeChangeError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	if errors.As(err, new(task.BadRequest)) || errors.Is(err, setup.ErrNotFlat) || errors.Is(err, setup.ErrReadOnly) {
		code = http.StatusBadRequest
	}
	writeError(w, code, err.Error())
}

// GET /api/v1/jobs?limit=50: the latest jobs, newest first (50 by
// default, at most 1000).
func (s *Server) apiJobs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	jobs, err := s.library().Jobs(r.Context(), limit) // which says what 0 and too many mean
	answer(w, jobs, err)
}

func (s *Server) jobFor(w http.ResponseWriter, r *http.Request) *store.Job {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "no such job")
		return nil
	}
	j, err := s.Manager.Store.JobByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil
	}
	if j == nil {
		writeError(w, http.StatusNotFound, "no such job")
	}
	return j
}

// GET /api/v1/jobs/{id}
func (s *Server) apiJob(w http.ResponseWriter, r *http.Request) {
	if j := s.jobFor(w, r); j != nil {
		writeJSON(w, http.StatusOK, api.JobOf(*j))
	}
}

// POST /api/v1/jobs/{id}/cancel: stop a queued or running job. Downloads
// in progress keep what they have, and resume next time.
func (s *Server) apiCancel(w http.ResponseWriter, r *http.Request) {
	j := s.jobFor(w, r)
	if j == nil {
		return
	}
	if !s.cancelJob(r.Context(), j.ID) {
		writeError(w, http.StatusConflict, "the job is not queued or running")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"canceling": j.ID})
}
