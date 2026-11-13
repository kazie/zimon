// Package cli wires zimon's cobra commands to the library packages.
package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"syscall"

	"charm.land/fang/v2"
	"github.com/spf13/cobra"

	"github.com/kazie/zimon/internal/client"
	"github.com/kazie/zimon/internal/ui"
)

// errSilent signals that the command already reported its failure.
var errSilent = errors.New("")

// Execute runs zimon and returns the process exit code.
func Execute(version string) int {
	return run(&app{version: resolveVersion(version)})
}

// run is Execute with a prepared app; the script tests use it to set options.
func run(a *app) int {
	if c, ok := parseRemoteCall(os.Args[1:]); ok {
		a.out = ui.New(os.Getenv("NO_COLOR") != "")
		r, err := a.remoteFor(c)
		if err != nil {
			a.out.Fail("%v", err)
			return 1
		}
		if !r.HTTP() {
			a.out.Fail("%v", a.forward(c, r)) // forward returns only when ssh could not start
			return 1
		}
		a.remote = &r // the command runs here, on r's library
	}
	root := newRootCmd(a)
	var cmdErr error
	keepErrors(root, &cmdErr)
	// Fang's help and Bubble Tea read NO_COLOR, not our flag.
	if noColorFlag(os.Args[1:]) {
		_ = os.Setenv("NO_COLOR", "1")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// After the first Ctrl-C zimon winds down (checkpoints, cleanup); a second
	// one gets the default behaviour and exits at once.
	go func() { <-ctx.Done(); stop() }()

	// Fang draws the help. Errors stay zimon's (below), so they look like
	// every other failure, and so do signals and the man page (cmd_man.go).
	err := fang.Execute(ctx, root,
		fang.WithVersion(a.version),
		fang.WithoutManpage(),
		fang.WithColorSchemeFunc(fang.AnsiColorScheme), // follows the terminal's theme, like ui
		fang.WithErrorHandler(func(io.Writer, fang.Styles, error) {}),
	)
	a.close()
	err = cmp.Or(err, cmdErr)
	if err == nil {
		return 0
	}
	if d, ok := errors.AsType[*client.Detached](err); ok {
		if d.Job == 0 {
			a.out.Hint("The change may have started on %s all the same: see zimon jobs", a.remote.Name)
		} else {
			a.out.Hint("Job %d carries on, on %s. Show it again: zimon jobs %d --follow   Stop it: zimon jobs %d --cancel",
				d.Job, a.remote.Name, d.Job, d.Job)
		}
		return 130
	}
	if errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "Interrupted.")
		return 130
	}
	if !errors.Is(err, errSilent) {
		out := a.out
		if out == nil {
			out = ui.New(false)
		}
		out.Fail("%v", err)
	}
	return 1
}

// keepErrors keeps each command's error (and setup's) in *err, for run to
// print, and lets the command return nil. Fang styles an error it sees by
// asking the terminal for its colours, which a terminal that doesn't answer
// (some ssh sessions) makes wait for seconds, for an error zimon prints
// itself. Cobra's own errors (a wrong flag, too many arguments) still go
// through fang.
func keepErrors(c *cobra.Command, err *error) {
	if pre := c.PersistentPreRunE; pre != nil {
		c.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
			*err = pre(cmd, args)
			return nil
		}
	}
	if run := c.RunE; run != nil {
		c.RunE = func(cmd *cobra.Command, args []string) error {
			if *err == nil { // else setup failed: nothing to run
				*err = run(cmd, args)
			}
			return nil
		}
	}
	for _, sub := range c.Commands() {
		keepErrors(sub, err)
	}
}

// noColorFlag reports whether args ask for --no-color before any "--".
func noColorFlag(args []string) bool {
	i := slices.Index(args, "--no-color")
	return i >= 0 && !slices.Contains(args[:i], "--")
}

func newRootCmd(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:   "zimon",
		Short: "A friendly manager for Kiwix ZIM files",
		Long: `zimon finds, downloads, verifies, updates and serves Kiwix ZIM files:
offline copies of Wikipedia, Stack Exchange, Project Gutenberg and more.

  zimon collections               curated sets: Linux, repair, Japan, ...
  zimon add @linux                add one (light, standard or deep)
  zimon search wikipedia          find something specific
  zimon add wikipedia_en_all:mini download it and keep it up to date
  zimon update                    fetch newer releases
  zimon serve                     read it in a browser via kiwix-serve
  zimon browse                    pick ZIMs interactively

What you want is kept in library.toml; edit it and run 'zimon apply'.`,
		Version:       a.version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return a.setup(cmd)
		},
	}
	root.SetVersionTemplate("zimon {{.Version}}\n")
	root.PersistentFlags().StringVar(&a.cfgPath, "config", "", "config file (default $ZIMON_CONFIG or ~/.config/zimon/config.toml)")
	root.PersistentFlags().BoolVarP(&a.verbose, "verbose", "v", false, "print more detail")
	root.PersistentFlags().BoolVar(&a.noColor, "no-color", false, "disable colours")
	// parseRemoteCall reads --remote before cobra; the flag is here for help and completion.
	root.PersistentFlags().String("remote", "", "run the command on this remote (see zimon remote; default $ZIMON_REMOTE)")
	_ = root.RegisterFlagCompletionFunc("remote", a.completeRemotes)

	root.AddGroup(
		&cobra.Group{ID: "find", Title: "Find ZIMs:"},
		&cobra.Group{ID: "manage", Title: "Manage your library:"},
		&cobra.Group{ID: "other", Title: "Other:"},
	)
	add := func(group string, cmds ...*cobra.Command) {
		for _, c := range cmds {
			c.GroupID = group
			root.AddCommand(c)
		}
	}
	add("find", newSearchCmd(a), newInfoCmd(a), newCollectionsCmd(a), newRefreshCmd(a), newBrowseCmd(a))
	add("manage", newAddCmd(a), newListCmd(a), newUpdateCmd(a), newApplyCmd(a), newLevelCmd(a), newRemoveCmd(a), newHoldCmd(a), newUnholdCmd(a), newPullCmd(a),
		newBudgetCmd(a), newSuggestCmd(a), newVerifyCmd(a), newPruneCmd(a), newServeCmd(a), newJobsCmd(a))
	add("other", newInitCmd(a), newConfigCmd(a), newDoctorCmd(a), newRemoteCmd(a), newTokenCmd(a))
	root.AddCommand(newManCmd())
	root.SetHelpCommandGroupID("other")
	root.SetCompletionCommandGroupID("other")
	return root
}
