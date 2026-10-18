//go:build linux

package runner

import (
	"errors"
	"syscall"
	"unsafe"
)

const (
	pPID    = 1
	wExited = 0x4
	wNoWait = 0x01000000
)

// waitExited blocks until the child pid has exited without reaping it. While
// the zombie exists the kernel cannot reuse its pid or process group id, so
// signalling -pid afterwards cannot hit an unrelated group.
func waitExited(pid int) error {
	// siginfo_t is 128 bytes on every Linux architecture.
	var info [128]byte
	for {
		_, _, errno := syscall.Syscall6(syscall.SYS_WAITID, pPID, uintptr(pid),
			uintptr(unsafe.Pointer(&info[0])), wExited|wNoWait, 0, 0)
		switch {
		case errno == 0:
			return nil
		case errors.Is(errno, syscall.EINTR):
			continue
		default:
			return errno
		}
	}
}
