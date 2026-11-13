// Package kiwix finds and starts kiwix-serve.
package kiwix

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// ErrNotInstalled means kiwix-serve could not be found.
var ErrNotInstalled = errors.New("kiwix-serve not found on PATH")

// Find returns the kiwix-serve binary: the configured path, else the one on PATH.
func Find(configured string) (string, error) {
	if configured != "" {
		if _, err := os.Stat(configured); err != nil {
			return "", fmt.Errorf("serve.kiwix_serve_path: %w", err)
		}
		return configured, nil
	}
	p, err := exec.LookPath("kiwix-serve")
	if err != nil {
		return "", ErrNotInstalled
	}
	return p, nil
}

// Options for kiwix-serve.
type Options struct {
	Library   string // library.xml
	Address   string // an IP, or all/ipv4/ipv6; empty leaves it to kiwix-serve (all)
	Port      int
	AttachPID int // exit when this process does (0: never)
}

// Args builds the kiwix-serve command line. --nodatealiases gives every book
// a URL without the date, so links keep working after zimon updates a ZIM.
func Args(o Options) []string {
	args := []string{
		"--library",
		"--monitorLibrary",
		"--nodatealiases",
		"--skipInvalid",
		"--port=" + strconv.Itoa(o.Port),
	}
	if o.Address != "" {
		args = append(args, "--address="+o.Address)
	}
	if o.AttachPID > 0 {
		args = append(args, "--attachToProcess="+strconv.Itoa(o.AttachPID))
	}
	return append(args, o.Library)
}
