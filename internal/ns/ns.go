//go:build linux

package ns

import (
	"os"
	"syscall"
)

// SysProcAttr builds clone flags for the container child.
// Default (rootful) path: 5 namespaces — UTS, PID, mount, network, IPC.
// User namespace is optional: enabled when not root, or --rootless /
// GOCKER_ALLOW_UNPRIVILEGED=1, with uid/gid maps (container 0 -> host euid).
func SysProcAttr(includeUser bool) *syscall.SysProcAttr {
	flags := syscall.CLONE_NEWUTS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNS | syscall.CLONE_NEWNET | syscall.CLONE_NEWIPC
	attr := &syscall.SysProcAttr{Cloneflags: uintptr(flags)}
	if includeUser {
		attr.Cloneflags |= syscall.CLONE_NEWUSER
		attr.UidMappings = []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Geteuid(), Size: 1},
		}
		attr.GidMappings = []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getegid(), Size: 1},
		}
	}
	return attr
}

func HasCloneFlag(flags uintptr, flag uintptr) bool {
	return flags&flag == flag
}
