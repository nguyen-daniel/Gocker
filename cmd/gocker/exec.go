//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"gocker/internal/cgroup"
	"gocker/internal/ns"
	"gocker/internal/state"
)

func execContainer(id string, command []string) {
	ctr, err := state.Load(id)
	must(err)

	displayID := displayContainer(ctr)
	if ctr.Status != "running" || syscall.Kill(ctr.PID, 0) != nil {
		must(fmt.Errorf("container %s is not running", displayID))
	}

	argv := append([]string(nil), command...)
	argv[0] = lookPathInContainer(ctr.PID, argv[0])

	nsNames := []string{"ipc", "uts", "net", "pid", "mnt"}
	nsFiles := make([]*os.File, 0, len(nsNames))
	for _, n := range nsNames {
		p := fmt.Sprintf("/proc/%d/ns/%s", ctr.PID, n)
		f, err := os.Open(p)
		if err != nil {
			must(fmt.Errorf("open %s: %v", p, err))
		}
		nsFiles = append(nsFiles, f)
	}
	defer func() {
		for _, f := range nsFiles {
			_ = f.Close()
		}
	}()
	fds := make([]int, len(nsFiles))
	for i, f := range nsFiles {
		fds[i] = int(f.Fd())
	}

	if ctr.CgroupPath != "" {
		if err := cgroup.Add(ctr.CgroupPath, os.Getpid()); err != nil {
			must(fmt.Errorf("join cgroup: %v", err))
		}
	}

	// Cap drop + seccomp are per-thread; stay on this thread through clone.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := ns.DropTeachingCaps(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: teaching cap drop: %v\n", err)
	}
	if err := ns.InstallTeachingSeccomp(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: teaching seccomp: %v\n", err)
	}

	env := execEnv()
	err = ns.ExecJoined(fds, argv, env)
	if err != nil {
		if ee, ok := err.(*ns.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		must(err)
	}
}

func lookPathInContainer(pid int, name string) string {
	if name == "" || strings.Contains(name, "/") {
		return name
	}
	root := fmt.Sprintf("/proc/%d/root", pid)
	for _, dir := range []string{"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin"} {
		p := filepath.Join(root, dir, name)
		st, err := os.Stat(p)
		if err == nil && !st.IsDir() {
			return filepath.Join(dir, name)
		}
	}
	return name
}

func execEnv() []string {
	env := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
		"HOSTNAME=gocker-container",
	}
	if term := os.Getenv("TERM"); term != "" {
		env = append(env, "TERM="+term)
	}
	return env
}
