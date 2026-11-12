//go:build windows

package main

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

const (
	createNewProcessGroup = 0x00000200
	createNoWindow        = 0x08000000 // own (hidden) console
	ctrlBreakEvent        = 1
	synchronize           = 0x00100000
)

var kernel32 = syscall.NewLazyDLL("kernel32.dll")

// detach gives the child its own process group and its own hidden console.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | createNoWindow}
}

// interrupt attaches to the console of pid and broadcasts Ctrl-Break to it.
// Only the node (and our own process, which ignores it) lives on that console.
func interrupt(pid int) error {
	kernel32.NewProc("FreeConsole").Call()
	if r, _, e := kernel32.NewProc("AttachConsole").Call(uintptr(pid)); r == 0 {
		return e
	}
	defer kernel32.NewProc("FreeConsole").Call()
	kernel32.NewProc("SetConsoleCtrlHandler").Call(0, 1) // ignore Ctrl-C here
	signal.Ignore(os.Interrupt)                          // Go maps Ctrl-Break to os.Interrupt
	if r, _, e := kernel32.NewProc("GenerateConsoleCtrlEvent").Call(ctrlBreakEvent, 0); r == 0 {
		return e
	}
	return nil
}

// alive: can we open the process with SYNCHRONIZE and is it still unsignaled?
func alive(pid int) bool {
	h, err := syscall.OpenProcess(synchronize, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	ev, _ := syscall.WaitForSingleObject(h, 0)
	return ev == syscall.WAIT_TIMEOUT
}
