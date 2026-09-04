//go:build linux

package ns

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Teaching seccomp + no_new_privs. This is NOT Docker's default profile:
// a short deny list of "you do not need these in a container" syscalls,
// installed after pivot_root. CAP_SYS_ADMIN is still kept (see caps.go).
//
// Denied: mount/umount2, reboot, kexec_load/kexec_file_load, unshare, bpf,
// pivot_root, swapon/swapoff, init_module/finit_module/delete_module.
// Not argument-aware (clone with NEW* flags is not blocked).

func teachingDeniedSyscalls() []uint32 {
	return []uint32{
		unix.SYS_MOUNT,
		unix.SYS_UMOUNT2,
		unix.SYS_REBOOT,
		unix.SYS_KEXEC_LOAD,
		unix.SYS_KEXEC_FILE_LOAD,
		unix.SYS_UNSHARE,
		unix.SYS_BPF,
		unix.SYS_PIVOT_ROOT,
		unix.SYS_SWAPON,
		unix.SYS_SWAPOFF,
		unix.SYS_INIT_MODULE,
		unix.SYS_FINIT_MODULE,
		unix.SYS_DELETE_MODULE,
	}
}

func seccompAuditArch() uint32 {
	switch runtime.GOARCH {
	case "amd64":
		return unix.AUDIT_ARCH_X86_64
	case "arm64":
		return unix.AUDIT_ARCH_AARCH64
	default:
		return 0
	}
}

func bpfStmt(code uint16, k uint32) unix.SockFilter {
	return unix.SockFilter{Code: code, K: k}
}

func bpfJump(code uint16, k uint32, jt, jf uint8) unix.SockFilter {
	return unix.SockFilter{Code: code, Jt: jt, Jf: jf, K: k}
}

func teachingSeccompFilter() []unix.SockFilter {
	ldAbs := uint16(unix.BPF_LD | unix.BPF_W | unix.BPF_ABS)
	jmpJeq := uint16(unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K)
	retK := uint16(unix.BPF_RET | unix.BPF_K)
	deny := unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)
	allow := uint32(unix.SECCOMP_RET_ALLOW)

	var f []unix.SockFilter
	arch := seccompAuditArch()
	if arch != 0 {
		f = append(f,
			bpfStmt(ldAbs, 4), // seccomp_data.arch
			bpfJump(jmpJeq, arch, 1, 0),
			bpfStmt(retK, unix.SECCOMP_RET_KILL_THREAD),
		)
	}
	f = append(f, bpfStmt(ldAbs, 0)) // seccomp_data.nr
	for _, nr := range teachingDeniedSyscalls() {
		f = append(f,
			bpfJump(jmpJeq, nr, 0, 1),
			bpfStmt(retK, deny),
		)
	}
	f = append(f, bpfStmt(retK, allow))
	return f
}

// InstallTeachingSeccomp sets PR_SET_NO_NEW_PRIVS and a SECCOMP_MODE_FILTER
// deny list. Call after pivot_root, alongside DropTeachingCaps.
func InstallTeachingSeccomp() error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("PR_SET_NO_NEW_PRIVS: %v", err)
	}
	filter := teachingSeccompFilter()
	prog := unix.SockFprog{
		Len:    uint16(len(filter)),
		Filter: &filter[0],
	}
	if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&prog)), 0, 0); err != nil {
		return fmt.Errorf("PR_SET_SECCOMP: %v", err)
	}
	return nil
}
