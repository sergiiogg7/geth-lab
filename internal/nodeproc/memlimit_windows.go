//go:build windows

package nodeproc

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// MemLimit is a Windows Job Object that caps the committed memory of a process.
//
// Why a Job Object: Windows has no cgroups and no `ulimit -v`; a job is the supported way to give a process tree a
// memory ceiling (the same idea as a Docker --memory limit). JOB_OBJECT_LIMIT_JOB_MEMORY limits COMMITTED memory
// (private bytes), the number Go's runtime asks the OS for, so when the node needs more the allocation fails and the Go
// runtime dies with "fatal error: runtime: out of memory". Nothing is "killed" by the OS; the process aborts itself.
//
// Java analogy: -Xmx enforced from outside the JVM, except that here the limit is hard and exceeding it is fatal.
type MemLimit struct {
	job   windows.Handle
	Bytes uint64
}

// LimitMemory creates a job with the given limit and assigns the process to it. With killOnClose the process dies when the
// returned MemLimit is closed (tests use it so that a crashed test cannot leave a node behind); without it the limit
// stays on the process after the handle is closed (what `nodectl start -mem-mb` needs, since nodectl exits right away).
func LimitMemory(pid int, mb int, killOnClose bool) (*MemLimit, error) {
	if mb <= 0 {
		return nil, fmt.Errorf("memory limit must be positive")
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("CreateJobObject: %w", err)
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_JOB_MEMORY
	if killOnClose {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	}
	info.JobMemoryLimit = uintptr(uint64(mb) << 20)
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("SetInformationJobObject: %w", err)
	}
	// PROCESS_SET_QUOTA | PROCESS_TERMINATE is what AssignProcessToJobObject requires.
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("OpenProcess(%d): %w", pid, err)
	}
	defer windows.CloseHandle(ph)
	if err := windows.AssignProcessToJobObject(job, ph); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("AssignProcessToJobObject: %w", err)
	}
	return &MemLimit{job: job, Bytes: uint64(mb) << 20}, nil
}

// PeakBytes is the highest committed memory the job has used so far.
func (m *MemLimit) PeakBytes() (uint64, error) {
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := windows.QueryInformationJobObject(m.job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err != nil {
		return 0, err
	}
	return uint64(info.PeakJobMemoryUsed), nil
}

// Close releases the handle (and kills the process when it was created with killOnClose).
func (m *MemLimit) Close() error { return windows.CloseHandle(m.job) }
