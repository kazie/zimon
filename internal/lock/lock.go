// Package lock is zimon's state lock: one zimon at a time changes the
// library (and library.toml), whether a timer-driven update, a command or
// a job of the web admin.
package lock

import "context"

// Try takes the lock at path only if it is free right now.
func Try(path string) (unlock func(), ok bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	unlock, err := Acquire(ctx, path, func(string) { cancel() }) // busy: give up
	return unlock, err == nil
}
