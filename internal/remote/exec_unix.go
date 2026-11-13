//go:build unix

package remote

import (
	"os"
	"os/exec"
	"syscall"
)

// Exec replaces zimon with argv (ssh), so the terminal, signals and the
// remote command's exit code pass straight through.
func Exec(argv []string) error {
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	return syscall.Exec(bin, argv, os.Environ())
}
