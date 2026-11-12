//go:build windows

package nodeproc

import (
	"os/exec"
	"syscall"
)

const (
	createNewProcessGroup = 0x00000200
	ctrlBreakEvent        = 1
)

// prepare puts the child in its own process group so a Ctrl-Break aimed at it
// does not also hit the test process.
func prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// interrupt sends CTRL_BREAK_EVENT to the child's process group. Go programs
// (including geth) map it to os.Interrupt, the same signal as Ctrl-C.
// It needs the parent to own a console; when it has none the call fails and the
// caller should fall back to the lab_shutdown RPC.
func interrupt(cmd *exec.Cmd) error {
	dll := syscall.NewLazyDLL("kernel32.dll")
	proc := dll.NewProc("GenerateConsoleCtrlEvent")
	r, _, err := proc.Call(uintptr(ctrlBreakEvent), uintptr(cmd.Process.Pid))
	if r == 0 {
		return err
	}
	return nil
}
