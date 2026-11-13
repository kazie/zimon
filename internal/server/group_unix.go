//go:build unix

package server

import (
	"os/exec"
	"syscall"
)

// ownGroup puts cmd in its own process group, so a Ctrl-C in the terminal
// reaches zimon only.
func ownGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
