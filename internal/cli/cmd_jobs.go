package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/store"
	"github.com/kazie/zimon/internal/ui"
)

func newJobsCmd(a *app) *cobra.Command {
	var limit int
	var follow, cancel bool
	cmd := &cobra.Command{
		Use:   "jobs [id]",
		Short: "Show what zimon serve did, or is doing, for its API and admin page",
		Long: `zimon serve makes the changes asked for through its API and admin page (and
its scheduled updates) one at a time, as jobs. This lists them, newest first,
or with an id shows one with its log.

Run it on the server, or here with --remote NAME. On a remote, a change
runs as a job there; Ctrl-C stops showing it, but the job carries on.
--follow shows it again until it ends, and --cancel stops it.`,
		Example: `  zimon jobs
  zimon --remote nas jobs 12
  zimon --remote nas jobs 12 --follow`,
		Args:        cobra.MaximumNArgs(1),
		Annotations: onRemote,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if len(args) == 1 {
				id, err := strconv.ParseInt(strings.TrimPrefix(args[0], "#"), 10, 64)
				if err != nil {
					return err
				}
				switch {
				case cancel:
					if err := a.be.Cancel(ctx, id); err != nil {
						return err
					}
					a.out.Success("Job %d stops. Downloads keep what they have, and resume next time.", id)
					return nil
				case follow:
					return a.followJob(ctx, id)
				}
				j, err := a.be.Job(ctx, id)
				if err != nil {
					return err
				}
				a.showJob(j)
				return nil
			}
			if follow || cancel {
				return errors.New("give the job's id: zimon jobs ID --follow (see: zimon jobs)")
			}
			jobs, err := a.be.Jobs(ctx, limit)
			if err != nil {
				return err
			}
			if len(jobs) == 0 {
				a.out.Status("No jobs yet.")
				a.out.Hint("Changes made through zimon serve (its admin page, API and schedule) show up here.")
				return nil
			}
			t := ui.NewTable("ID", "STATE", "WHAT", "BY", "QUEUED")
			t.Flex = 2
			for _, j := range jobs {
				t.Add(strconv.FormatInt(j.ID, 10), jobState(j.State), j.Summary, j.By, localTime(j.Created))
			}
			t.Render(a.out.Out, a.out.Width())
			a.out.Hint("One with its log: zimon jobs <id>")
			return nil
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "show at most this many jobs")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "show a remote's job as it runs, until it ends")
	cmd.Flags().BoolVar(&cancel, "cancel", false, "stop a remote's queued or running job")
	cmd.MarkFlagsMutuallyExclusive("follow", "cancel")
	return cmd
}

// followJob shows job id as it runs, and then how it ended.
func (a *app) followJob(ctx context.Context, id int64) error {
	j, err := a.be.Job(ctx, id)
	if err != nil {
		return err
	}
	if j.Finished == "" {
		a.out.Hint("Job %d: %s %s", id, j.Summary, ui.Dim.Render("(Ctrl-C stops showing it; the job carries on)"))
		sh := &shower{a: a}
		err = a.be.Follow(ctx, id, sh.show)
		sh.finish()
		if errors.Is(err, context.Canceled) {
			return err
		}
		now, jerr := a.be.Job(ctx, id)
		switch {
		case jerr != nil:
			return cmp.Or(err, jerr)
		case now.Finished == "": // Follow gave up on it
			return cmp.Or(err, fmt.Errorf("job %d is still %s", id, now.State))
		}
		j = now
	}
	if j.State != store.JobDone {
		return fmt.Errorf("job %d %s: %s", id, j.State, cmp.Or(j.Error, j.Summary))
	}
	a.out.Success("Job %d done: %s", id, j.Summary)
	return nil
}

func (a *app) showJob(j *api.Job) {
	a.out.Println(ui.Bold.Render("Job "+strconv.FormatInt(j.ID, 10)) + "  " + j.Summary + "  " + jobState(j.State))
	when := []string{"queued " + localTime(j.Created)}
	if j.By != "" {
		when = append([]string{"by " + j.By}, when...)
	}
	if j.Started != "" {
		when = append(when, "started "+localTime(j.Started))
	}
	if j.Finished != "" {
		when = append(when, "ended "+localTime(j.Finished))
	}
	a.out.Hint("%s", strings.Join(when, " · "))
	for _, line := range j.Log {
		a.out.Println("  " + line)
	}
	if j.Error != "" {
		a.out.Fail("%s", j.Error)
	}
}

// jobState colours a job's state.
func jobState(s string) string {
	switch s {
	case store.JobDone:
		return ui.Green.Render(s)
	case store.JobFailed:
		return ui.Red.Render(s)
	case store.JobRunning:
		return ui.Cyan.Render(s)
	case store.JobCanceled, store.JobInterrupted:
		return ui.Yellow.Render(s)
	}
	return ui.Dim.Render(s)
}

// localTime is an API time in the local time zone, to the minute.
func localTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return when(t)
}

// when is a time as people read it here: local, to the minute.
func when(t time.Time) string { return t.Local().Format("2006-01-02 15:04") }
