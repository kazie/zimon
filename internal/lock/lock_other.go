//go:build !unix

package lock

import "context"

// Holder knows of no holder here: there is no flock.
func Holder(string) (int, error) { return 0, nil }

// Acquire does nothing here: there is no flock.
func Acquire(context.Context, string, func(string)) (func(), error) { return func() {}, nil }
