//go:build linux

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gocker/internal/cgroup"
	gockernet "gocker/internal/net"
	"gocker/internal/ns"
	"gocker/internal/overlay"
	"gocker/internal/state"
)

func run() {
	var cpuLimit, memoryLimit, rootfsPath string
	var volumes []string
	var detached bool
	args := os.Args[2:]
	var remainingArgs []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--cpu-limit" {
			if i+1 < len(args) {
				cpuLimit = args[i+1]
				i++
			}
		} else if arg == "--memory-limit" {
			if i+1 < len(args) {
				memoryLimit = args[i+1]
				i++
			}
		} else if arg == "--volume" || arg == "-v" {
			if i+1 < len(args) {
				volumes = append(volumes, args[i+1])
				i++
			}
		} else if arg == "--detach" || arg == "-d" {
			detached = true
		} else if arg == "--rootless" {
			// Consumed here so it is not treated as the container command.
			// Root check is handled in main() via allowUnprivileged().
		} else if arg == "--rootfs" {
			if i+1 < len(args) {
				rootfsPath = args[i+1]
				i++
			}
		} else {
			remainingArgs = append(remainingArgs, arg)
		}
	}

	if len(remainingArgs) == 0 {
		fmt.Println("Error: command required")
		fmt.Println("Usage: gocker run [options] <command> [args...]")
		os.Exit(1)
	}

	resolvedRootfs, err := overlay.ResolveRootfs(rootfsPath)
	if err != nil {
		must(err)
	}

	containerID := generateContainerID()

	if err := overlay.CreateDirs(containerID); err != nil {
		must(fmt.Errorf("failed to create overlay dirs: %v", err))
	}

	cgroupPath, err := cgroup.Create(containerID)
	if err != nil {
		overlay.CleanupDirs(containerID)
		must(fmt.Errorf("failed to create cgroup: %v", err))
	}

	fmt.Fprintln(os.Stderr, "Setting up cgroups v2 for resource limits...")
	if err := cgroup.Setup(cgroupPath, cpuLimit, memoryLimit); err != nil {
		cgroup.Cleanup(cgroupPath)
		overlay.CleanupDirs(containerID)
		must(err)
	}

	os.Setenv("GOCKER_CONTAINER_ID", containerID)
	os.Setenv("GOCKER_ROOTFS", resolvedRootfs)
	os.Setenv("GOCKER_OVERLAY_DIR", overlay.BaseDir(containerID))
	os.Setenv("GOCKER_CGROUP_PATH", cgroupPath)
	if len(volumes) > 0 {
		os.Setenv("GOCKER_VOLUMES", strings.Join(volumes, "|"))
	}

	logFile := filepath.Join(state.Dir, "logs", containerID+".log")
	if err := os.MkdirAll(filepath.Dir(logFile), 0755); err != nil {
		cgroup.Cleanup(cgroupPath)
		overlay.CleanupDirs(containerID)
		must(fmt.Errorf("failed to create logs directory: %v", err))
	}

	logWriter, err := os.Create(logFile)
	if err != nil {
		cgroup.Cleanup(cgroupPath)
		overlay.CleanupDirs(containerID)
		must(fmt.Errorf("failed to create log file: %v", err))
	}
	defer logWriter.Close()

	if !detached {
		fmt.Fprintf(os.Stderr, "Running %v as PID %d\n", remainingArgs, os.Getpid())
	}
	fmt.Fprintln(os.Stderr, "Creating isolated namespaces...")
	fmt.Fprintln(os.Stderr, "  - UTS namespace (hostname isolation)")
	fmt.Fprintln(os.Stderr, "  - PID namespace (process ID isolation)")
	fmt.Fprintln(os.Stderr, "  - Mount namespace (filesystem isolation)")
	fmt.Fprintln(os.Stderr, "  - Network namespace (network isolation)")

	includeUser := os.Geteuid() != 0 || allowUnprivileged()
	if includeUser {
		fmt.Fprintln(os.Stderr, "  - User namespace (optional; uid/gid mapped)")
	}

	cmd := exec.Command("/proc/self/exe", append([]string{"child"}, remainingArgs...)...)

	// Set up I/O. Detached children must inherit the log *os.File only.
	// Wiring os.Stdout/os.Stderr (or a MultiWriter) makes exec.Cmd use a pipe
	// plus a copy goroutine; Wait() then blocks until the child exits, and
	// closing that pipe when the parent exits can kill the child (the reaper
	// then removes the cgroup the integration tests look for).
	if detached {
		cmd.Stdin = nil
		cmd.Stdout = logWriter
		cmd.Stderr = logWriter
	} else {
		cmd.Stdin = os.Stdin
		cmd.Stdout = io.MultiWriter(logWriter, os.Stdout)
		cmd.Stderr = io.MultiWriter(logWriter, os.Stderr)
	}

	cmd.SysProcAttr = ns.SysProcAttr(includeUser)
	if includeUser {
		fmt.Fprintf(os.Stderr, "  - User namespace: mapping container UID 0 -> host UID %d\n", os.Geteuid())
	} else {
		fmt.Fprintln(os.Stderr, "  - Running as root (user namespace not enabled; 4 namespaces)")
	}

	if err := cmd.Start(); err != nil {
		cgroup.Cleanup(cgroupPath)
		overlay.CleanupDirs(containerID)
		must(err)
	}

	childPid := cmd.Process.Pid

	if err := cgroup.Add(cgroupPath, childPid); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to add process to cgroup: %v\n", err)
	}

	var parentOutput io.Writer
	if detached {
		parentOutput = io.MultiWriter(logWriter, os.Stderr)
	} else {
		parentOutput = logWriter
	}

	fmt.Fprintf(parentOutput, "  - Child PID: %d\n", childPid)

	if err := gockernet.EnsureBridge(); err != nil {
		fmt.Fprintf(parentOutput, "Warning: Failed to set up bridge: %v\n", err)
	}

	if !detached {
		fmt.Fprintln(logWriter, "Setting up network namespace...")
	} else {
		fmt.Fprintln(os.Stderr, "Setting up network namespace...")
	}

	vethHost, vethPeer, containerIP, err := gockernet.SetupContainer(containerID, childPid, !detached)
	if err != nil {
		if detached {
			fmt.Fprintf(os.Stderr, "Warning: Failed to set up network: %v\n", err)
		} else {
			fmt.Fprintf(logWriter, "Warning: Failed to set up network: %v\n", err)
		}
	}

	ctr := &state.ContainerState{
		ID:          containerID,
		PID:         childPid,
		Status:      "running",
		CreatedAt:   time.Now(),
		Command:     remainingArgs,
		VethHost:    vethHost,
		VethPeer:    vethPeer,
		ContainerIP: containerIP,
		LogFile:     logFile,
		Detached:    detached,
		CgroupPath:  cgroupPath,
		RootfsPath:  resolvedRootfs,
	}
	if err := state.Save(ctr); err != nil {
		fmt.Fprintf(parentOutput, "Warning: Failed to save container state: %v\n", err)
	}

	if detached {
		fmt.Printf("Container started with ID: %s\n", containerID)
		fmt.Printf("Use 'gocker logs %s' to view logs\n", containerID)
		if err := startDetachedReaper(containerID); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to start detached reaper: %v\n", err)
		}
		return
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	cleanup := func() {
		state.UpdateStatus(containerID, "exited")
		gockernet.CleanupContainer(containerID, vethHost)
		cgroup.Cleanup(cgroupPath)
	}

	done := make(chan bool, 1)
	go func() {
		select {
		case <-sigChan:
			fmt.Fprintf(os.Stderr, "\nReceived interrupt, cleaning up...\n")
			cmd.Process.Signal(syscall.SIGTERM)
			time.Sleep(500 * time.Millisecond)
			cmd.Process.Kill()
			cleanup()
			os.Exit(130)
		case <-done:
			return
		}
	}()

	waitErr := cmd.Wait()
	done <- true
	signal.Stop(sigChan)

	cleanup()

	if waitErr != nil {
		os.Exit(cmd.ProcessState.ExitCode())
	}
}

func child() {
	fmt.Fprintf(os.Stderr, "Running in child process with PID %d\n", os.Getpid())

	containerUID := syscall.Getuid()
	containerGID := syscall.Getgid()
	fmt.Fprintf(os.Stderr, "Container UID: %d, GID: %d\n", containerUID, containerGID)

	rootfsPath := os.Getenv("GOCKER_ROOTFS")
	if rootfsPath == "" {
		rootfsPath = "./rootfs"
	}
	overlayBase := os.Getenv("GOCKER_OVERLAY_DIR")
	if overlayBase == "" {
		must(fmt.Errorf("GOCKER_OVERLAY_DIR not set"))
	}

	fmt.Fprintln(os.Stderr, "Configuring container network...")
	if err := gockernet.ConfigureInside(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to configure container network: %v\n", err)
	}

	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		fmt.Fprintf(os.Stderr, "  - Note: MS_PRIVATE on /: %v\n", err)
	}

	fmt.Fprintf(os.Stderr, "Mounting OverlayFS (lower=%s, dirs=%s)...\n", rootfsPath, overlayBase)
	merged, err := overlay.Mount(rootfsPath, overlayBase)
	must(err)

	volumesStr := os.Getenv("GOCKER_VOLUMES")
	if volumesStr != "" {
		fmt.Fprintln(os.Stderr, "Mounting volumes...")
		if err := overlay.MountVolumes(volumesStr, merged); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to mount volumes: %v\n", err)
		}
	}

	fmt.Fprintln(os.Stderr, "Setting hostname to 'gocker-container'...")
	must(syscall.Sethostname([]byte("gocker-container")))

	if err := overlay.EnsureDevices(merged); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to set up jail device nodes: %v\n", err)
	}

	fmt.Fprintf(os.Stderr, "Entering OverlayFS jail with pivot_root (%s)...\n", merged)
	must(overlay.Pivot(merged))

	fmt.Fprintln(os.Stderr, "Mounting proc filesystem...")
	must(syscall.Mount("proc", "proc", "proc", 0, ""))
	defer syscall.Unmount("proc", 0)

	command := "/bin/sh"
	args := []string{}
	if len(os.Args) > 2 {
		command = os.Args[2]
		if len(os.Args) > 3 {
			args = os.Args[3:]
		}
	}

	os.Setenv("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")

	fmt.Fprintf(os.Stderr, "Executing command: %s %v\n", command, args)
	cmd := exec.Command(command, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	if command == "/bin/sh" && len(args) == 0 {
		cmd.Args = []string{command, "-i"}
	}

	must(cmd.Run())
}

func startDetachedReaper(containerID string) error {
	exe, err := os.Executable()
	if err != nil {
		exe = "/proc/self/exe"
	}
	cmd := exec.Command(exe, "reap", containerID)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Start()
}

func reapContainer(containerID string) {
	ctr, err := state.Load(containerID)
	if err != nil {
		return
	}
	pid := ctr.PID
	for {
		if err := syscall.Kill(pid, 0); err != nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	ctr, err = state.Load(containerID)
	if err != nil {
		return
	}
	if ctr.Status == "running" {
		_ = state.UpdateStatus(ctr.ID, "exited")
	}
	gockernet.CleanupContainer(ctr.ID, ctr.VethHost)
	cgroup.Cleanup(ctr.CgroupPath)
}
