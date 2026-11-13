//go:build !unix

package server

import "os/exec"

func ownGroup(*exec.Cmd) {}
