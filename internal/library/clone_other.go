//go:build !linux

package library

import "errors"

// cloneOf can't clone here: the caller copies instead.
func cloneOf(_, _ string) error { return errors.ErrUnsupported }
