// Package kiwix finds, fetches and starts kiwix-serve.
package kiwix

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// errNotInstalled means find found no kiwix-serve.
var errNotInstalled = errors.New("kiwix-serve not found")

// InstallHint says how to install kiwix-serve, for when zimon can't fetch it.
const InstallHint = "install kiwix-tools: your distro's kiwix-tools package, Kiwix's static build from " +
	DownloadURL + ", or nix shell nixpkgs#kiwix-tools"

// find returns the kiwix-serve binary: the configured path, else the one on
// PATH, else the one Fetch put in stateDir.
func find(configured, stateDir string) (string, error) {
	if configured != "" {
		if _, err := os.Stat(configured); err != nil {
			return "", fmt.Errorf("serve.kiwix_serve_path: %w", err)
		}
		return configured, nil
	}
	if p, err := exec.LookPath("kiwix-serve"); err == nil {
		return p, nil
	}
	if p, ok := own(stateDir); ok {
		return p, nil
	}
	return "", errNotInstalled
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
