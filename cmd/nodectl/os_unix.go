//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// detach starts the child in its own session so it survives the caller.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

func interrupt(pid int) error { return syscall.Kill(pid, syscall.SIGINT) }

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }
