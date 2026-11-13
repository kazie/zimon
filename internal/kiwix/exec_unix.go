//go:build unix

package kiwix

import (
	"os"
	"syscall"
)

// Exec replaces the current process with kiwix-serve, so signals and exit
// codes behave exactly as if kiwix-serve had been started directly (good for
// systemd units).
func Exec(bin string, args []string) error {
	return syscall.Exec(bin, append([]string{bin}, args...), os.Environ())
}
