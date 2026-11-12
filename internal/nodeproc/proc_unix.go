//go:build !windows

package nodeproc

import (
	"os"
	"os/exec"
)

func prepare(cmd *exec.Cmd) {}

func interrupt(cmd *exec.Cmd) error { return cmd.Process.Signal(os.Interrupt) }
