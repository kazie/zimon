//go:build !unix

package kiwix

import (
	"os"
	"os/exec"
)

// Exec runs kiwix-serve in the foreground and waits for it.
func Exec(bin string, args []string) error {
	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
