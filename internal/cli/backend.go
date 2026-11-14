package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/kazie/zimon/internal/backend"
	"github.com/kazie/zimon/internal/client"
	"github.com/kazie/zimon/internal/lock"
	"github.com/kazie/zimon/internal/task"
)

// onRemote marks a command (in its Annotations) that works on an https://
// remote.
var onRemote = map[string]string{"remote": "api"}

// checkRemote fails for a command that can't run on an https:// remote.
func (a *app) checkRemote(cmd *cobra.Command) error {
	if a.remote == nil || cmd.Annotations["remote"] == "api" {
		return nil
	}
	return fmt.Errorf("zimon %s does not work on an https:// remote (%s); run it there, or through an ssh:// remote",
		cmd.Name(), a.remote.Name)
}

// local is the library here, as a backend.
func (a *app) local() *backend.Local {
	return &backend.Local{
		Manager: a.manager,
		Version: a.version,
		Commit:  a.commit,
		Lock: func(ctx context.Context, onWait func(string)) (func(), error) {
			return lock.Acquire(ctx, a.cfg.LockPath(), onWait)
		},
		TryLock: func() (func(), bool) { return lock.Try(a.cfg.LockPath()) },
	}
}

// remoteBackend is the library of the https:// remote the command runs on.
func (a *app) remoteBackend() (*backend.Remote, error) {
	c, err := client.New(*a.remote, a.httpClient())
	if err != nil {
		return nil, err
	}
	return backend.NewRemote(c), nil
}

// freshCatalog refreshes the catalog when it is older than
// catalog_max_age (always, with force) before a command uses it. If that
// fails but a cached copy exists, it warns and carries on with the cache.
// A remote's server keeps its own catalog fresh.
func (a *app) freshCatalog(ctx context.Context, force bool) error {
	if a.remote != nil {
		return nil
	}
	m, err := a.manager()
	if err != nil {
		return err
	}
	return task.EnsureCatalog(ctx, m, force, false, (&shower{a: a}).show)
}
