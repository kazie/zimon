//go:build !unix

package remote

import (
	"errors"
	"os"
	"os/exec"
)

// Exec runs argv (ssh) in the foreground and exits with its exit code.
func Exec(argv []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		os.Exit(exit.ExitCode())
	}
	if err == nil {
		os.Exit(0)
	}
	return err
}
