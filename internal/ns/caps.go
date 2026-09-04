//go:build linux

package ns

import (
	"fmt"
	"syscall"
	"unsafe"
)

// Teaching-only capability drop. This is not a production profile:
// CAP_SYS_ADMIN is kept (see honesty in README). Seccomp + no_new_privs
// are applied separately after pivot_root (InstallTeachingSeccomp).
//
// We clear a handful of "you do not need these in a container" caps from
// the bounding set (PR_CAPBSET_DROP) and from the effective/permitted/
// inheritable sets (capset). Docker's default list is larger; this is a
// demo of the mechanism.

const (
	prCapbsetDrop = 24
	capVersion3   = 0x20080522
)

// Linux capability numbers (uapi/linux/capability.h).
const (
	capSysModule    = 16
	capSysRawio     = 17
	capSysPacct     = 20
	capSysBoot      = 22
	capSysTime      = 25
	capMacOverride  = 32
	capMacAdmin     = 33
	capSyslog       = 34
	capWakeAlarm    = 35
	capBlockSuspend = 36
)

// TeachingDropCaps is the extra caps we drop. Kept as a list so tests can
// check /proc/self/status without duplicating numbers.
var TeachingDropCaps = []int{
	capSysModule,
	capSysRawio,
	capSysPacct,
	capSysBoot,
	capSysTime,
	capMacOverride,
	capMacAdmin,
	capSyslog,
	capWakeAlarm,
	capBlockSuspend,
}

type capHeader struct {
	version uint32
	pid     int32
}

type capData struct {
	effective   uint32
	permitted   uint32
	inheritable uint32
}

func capWordMask(capn int) (word int, mask uint32) {
	return capn / 32, uint32(1) << (uint(capn) % 32)
}

// DropTeachingCaps drops extra capabilities from this process.
// Failures are returned; callers should treat this as best-effort teaching.
func DropTeachingCaps() error {
	for _, c := range TeachingDropCaps {
		_, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prCapbsetDrop, uintptr(c), 0)
		if errno != 0 {
			return fmt.Errorf("PR_CAPBSET_DROP %d: %v", c, errno)
		}
	}

	hdr := capHeader{version: capVersion3, pid: 0}
	var data [2]capData
	_, _, errno := syscall.RawSyscall(syscall.SYS_CAPGET, uintptr(unsafe.Pointer(&hdr)), uintptr(unsafe.Pointer(&data[0])), 0)
	if errno != 0 {
		return fmt.Errorf("capget: %v", errno)
	}

	for _, c := range TeachingDropCaps {
		word, mask := capWordMask(c)
		if word < 0 || word >= len(data) {
			continue
		}
		data[word].effective &^= mask
		data[word].permitted &^= mask
		data[word].inheritable &^= mask
	}

	hdr = capHeader{version: capVersion3, pid: 0}
	_, _, errno = syscall.RawSyscall(syscall.SYS_CAPSET, uintptr(unsafe.Pointer(&hdr)), uintptr(unsafe.Pointer(&data[0])), 0)
	if errno != 0 {
		return fmt.Errorf("capset: %v", errno)
	}
	return nil
}
