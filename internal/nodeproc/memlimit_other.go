//go:build !windows

package nodeproc

import "errors"

// MemLimit is only implemented on Windows (Job Objects). On Linux the equivalent is a cgroup memory limit
// (systemd-run --scope -p MemoryMax=700M, or a container), which needs privileges this lab does not assume.
type MemLimit struct{ Bytes uint64 }

// ErrMemLimitUnsupported is returned by LimitMemory on this platform.
var ErrMemLimitUnsupported = errors.New("memory limit via job object is only implemented on Windows")

// LimitMemory is a stub on this platform.
func LimitMemory(pid int, mb int, killOnClose bool) (*MemLimit, error) {
	return nil, ErrMemLimitUnsupported
}

// PeakBytes is a stub.
func (m *MemLimit) PeakBytes() (uint64, error) { return 0, ErrMemLimitUnsupported }

// Close is a stub.
func (m *MemLimit) Close() error { return nil }
