//go:build linux

package ns

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ExecJoined joins the given namespace file descriptors (already opened),
// then forks so the grandchild is born in the container PID namespace and
// execs argv.
//
// A multithreaded Go process cannot setns(CLONE_NEWPID) (EINVAL). We clone a
// single-thread child, setns there (including pid), fork again, and execve
// in the grandchild. The children only issue raw syscalls.

// ExitError is a non-zero wait status from the exec'd command.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit status %d", e.Code)
}

func (e *ExitError) ExitCode() int {
	return e.Code
}

func ExecJoined(nsFDs []int, argv, envv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("command required")
	}
	if len(nsFDs) > 8 {
		return fmt.Errorf("too many namespace fds")
	}

	var fds [8]int
	nfd := copy(fds[:], nsFDs)

	argv0, err := syscall.BytePtrFromString(argv[0])
	if err != nil {
		return err
	}
	argvp, err := syscall.SlicePtrFromStrings(argv)
	if err != nil {
		return err
	}
	envp, err := syscall.SlicePtrFromStrings(envv)
	if err != nil {
		return err
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	pid, err := cloneSetnsExec(fds, nfd, argv0, argvp, envp)
	if err != nil {
		return fmt.Errorf("clone for setns: %v", err)
	}

	var ws syscall.WaitStatus
	if _, err := syscall.Wait4(pid, &ws, 0, nil); err != nil {
		return fmt.Errorf("wait exec: %v", err)
	}
	if ws.Exited() {
		if code := ws.ExitStatus(); code != 0 {
			return &ExitError{Code: code}
		}
		return nil
	}
	if ws.Signaled() {
		return &ExitError{Code: 128 + int(ws.Signal())}
	}
	return fmt.Errorf("exec: unexpected wait status %d", ws)
}

func cloneSetnsExec(fds [8]int, nfd int, argv0 *byte, argv, envv []*byte) (int, error) {
	r1, _, errno := unix.RawSyscall(unix.SYS_CLONE, uintptr(unix.SIGCHLD), 0, 0)
	if errno != 0 {
		return 0, errno
	}
	if r1 != 0 {
		return int(r1), nil
	}
	setnsForkExec(&fds[0], nfd, argv0, argv, envv)
	unix.RawSyscall(unix.SYS_EXIT_GROUP, 127, 0, 0)
	return 0, nil
}

func setnsForkExec(fds *int, nfd int, argv0 *byte, argv, envv []*byte) {
	for i := 0; i < nfd; i++ {
		fd := *(*int)(unsafe.Pointer(uintptr(unsafe.Pointer(fds)) + uintptr(i)*unsafe.Sizeof(int(0))))
		_, _, e := unix.RawSyscall(unix.SYS_SETNS, uintptr(fd), 0, 0)
		if e != 0 {
			unix.RawSyscall(unix.SYS_EXIT_GROUP, 127, 0, 0)
		}
	}

	// After setns(pid): this process stays in the old PID ns. The next
	// child is born inside the container PID namespace.
	r1, _, e := unix.RawSyscall(unix.SYS_CLONE, uintptr(unix.SIGCHLD), 0, 0)
	if e != 0 {
		unix.RawSyscall(unix.SYS_EXIT_GROUP, 127, 0, 0)
	}
	if r1 == 0 {
		unix.RawSyscall(unix.SYS_EXECVE,
			uintptr(unsafe.Pointer(argv0)),
			uintptr(unsafe.Pointer(&argv[0])),
			uintptr(unsafe.Pointer(&envv[0])))
		unix.RawSyscall(unix.SYS_EXIT_GROUP, 127, 0, 0)
	}

	var ws uint32
	unix.RawSyscall6(unix.SYS_WAIT4, r1, uintptr(unsafe.Pointer(&ws)), 0, 0, 0, 0)
	unix.RawSyscall(unix.SYS_EXIT_GROUP, uintptr(waitExitCode(ws)), 0, 0)
}

func waitExitCode(ws uint32) int {
	if ws&0x7f == 0 {
		return int((ws >> 8) & 0xff)
	}
	sig := int(ws & 0x7f)
	if sig != 0 {
		return 128 + sig
	}
	return 1
}
