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
	"unicode"

	"gocker/internal/cgroup"
	gockernet "gocker/internal/net"
	"gocker/internal/ns"
	"gocker/internal/overlay"
	"gocker/internal/state"
)

type runOptions struct {
	cpuLimit    string
	memoryLimit string
	rootfsPath  string
	network     string
	name        string
	volumes     []string
	detached    bool
	quiet       bool
	command     []string
}

func parseRunFlags(args []string) (runOptions, error) {
	var opt runOptions
	opt.network = "bridge"
	var rest []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		needVal := func(flag string) (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s requires a value", flag)
			}
			i++
			return args[i], nil
		}
		switch {
		case arg == "--cpu-limit":
			v, err := needVal(arg)
			if err != nil {
				return opt, err
			}
			opt.cpuLimit = v
		case arg == "--memory-limit":
			v, err := needVal(arg)
			if err != nil {
				return opt, err
			}
			opt.memoryLimit = v
		case arg == "--volume" || arg == "-v":
			v, err := needVal(arg)
			if err != nil {
				return opt, err
			}
			opt.volumes = append(opt.volumes, v)
		case arg == "--detach" || arg == "-d":
			opt.detached = true
		case arg == "--quiet" || arg == "-q":
			opt.quiet = true
		case arg == "--rootless":
			// Consumed so it is not treated as the container command.
			// Root check is handled in main() via allowUnprivileged().
		case arg == "--rootfs":
			v, err := needVal(arg)
			if err != nil {
				return opt, err
			}
			opt.rootfsPath = v
		case arg == "--name":
			v, err := needVal(arg)
			if err != nil {
				return opt, err
			}
			opt.name = v
		case arg == "--network":
			v, err := needVal(arg)
			if err != nil {
				return opt, err
			}
			opt.network = v
		case strings.HasPrefix(arg, "--network="):
			opt.network = strings.TrimPrefix(arg, "--network=")
		case arg == "--":
			rest = append(rest, args[i+1:]...)
			i = len(args)
		default:
			rest = append(rest, arg)
		}
	}

	opt.command = rest
	if opt.network == "" {
		opt.network = "bridge"
	}
	if opt.network != "bridge" && opt.network != "none" {
		return opt, fmt.Errorf("unknown --network mode %q (supported: bridge, none)", opt.network)
	}
	if opt.name != "" && !validContainerName(opt.name) {
		return opt, fmt.Errorf("invalid container name %q", opt.name)
	}
	return opt, nil
}

func validContainerName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i, r := range name {
		ok := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.'
		if !ok {
			return false
		}
		if i == 0 && (r == '.' || r == '-') {
			return false
		}
	}
	return true
}

func logv(quiet bool, format string, args ...interface{}) {
	if quiet {
		return
	}
	fmt.Fprintf(os.Stderr, format, args...)
}

func run() {
	opt, err := parseRunFlags(os.Args[2:])
	must(err)

	if len(opt.command) == 0 {
		fmt.Println("Error: command required")
		fmt.Println("Usage: gocker run [options] <command> [args...]")
		os.Exit(1)
	}

	if opt.quiet {
		os.Setenv("GOCKER_QUIET", "1")
	}
	os.Setenv("GOCKER_NETWORK", opt.network)

	resolvedRootfs, err := overlay.ResolveRootfs(opt.rootfsPath)
	if err != nil {
		must(err)
	}

	if opt.name != "" {
		taken, err := state.NameTaken(opt.name)
		must(err)
		if taken {
			must(fmt.Errorf("container name already in use: %s", opt.name))
		}
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

	logv(opt.quiet, "Setting up cgroups v2 for resource limits...\n")
	if err := cgroup.Setup(cgroupPath, opt.cpuLimit, opt.memoryLimit); err != nil {
		cgroup.Cleanup(cgroupPath)
		overlay.CleanupDirs(containerID)
		must(err)
	}

	os.Setenv("GOCKER_CONTAINER_ID", containerID)
	os.Setenv("GOCKER_ROOTFS", resolvedRootfs)
	os.Setenv("GOCKER_OVERLAY_DIR", overlay.BaseDir(containerID))
	os.Setenv("GOCKER_CGROUP_PATH", cgroupPath)
	if len(opt.volumes) > 0 {
		os.Setenv("GOCKER_VOLUMES", strings.Join(opt.volumes, "|"))
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

	if !opt.detached {
		logv(opt.quiet, "Running %v as PID %d\n", opt.command, os.Getpid())
	}
	logv(opt.quiet, "Creating isolated namespaces...\n")
	logv(opt.quiet, "  - UTS namespace (hostname isolation)\n")
	logv(opt.quiet, "  - PID namespace (process ID isolation)\n")
	logv(opt.quiet, "  - Mount namespace (filesystem isolation)\n")
	logv(opt.quiet, "  - Network namespace (network isolation)\n")

	includeUser := os.Geteuid() != 0 || allowUnprivileged()
	if includeUser {
		logv(opt.quiet, "  - User namespace (optional; uid/gid mapped)\n")
	}

	cmd := exec.Command("/proc/self/exe", append([]string{"child"}, opt.command...)...)

	// Set up I/O. Detached children must inherit the log *os.File only.
	// Wiring os.Stdout/os.Stderr (or a MultiWriter) makes exec.Cmd use a pipe
	// plus a copy goroutine; Wait() then blocks until the child exits, and
	// closing that pipe when the parent exits can kill the child (the reaper
	// then removes the cgroup the integration tests look for).
	if opt.detached {
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
		logv(opt.quiet, "  - User namespace: mapping container UID 0 -> host UID %d\n", os.Geteuid())
	} else {
		logv(opt.quiet, "  - Running as root (user namespace not enabled; 4 namespaces)\n")
	}

	if err := cmd.Start(); err != nil {
		cgroup.Cleanup(cgroupPath)
		overlay.CleanupDirs(containerID)
		must(err)
	}

	childPid := cmd.Process.Pid

	if err := cgroup.Add(cgroupPath, childPid); err != nil {
		logv(opt.quiet, "Warning: Failed to add process to cgroup: %v\n", err)
	}

	var parentOutput io.Writer
	if opt.detached {
		if opt.quiet {
			parentOutput = logWriter
		} else {
			parentOutput = io.MultiWriter(logWriter, os.Stderr)
		}
	} else {
		parentOutput = logWriter
	}

	fmt.Fprintf(parentOutput, "  - Child PID: %d\n", childPid)

	abort := func(vethHost string, cause error) {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		gockernet.CleanupContainer(containerID, vethHost)
		cgroup.Cleanup(cgroupPath)
		overlay.CleanupDirs(containerID)
		_ = os.Remove(filepath.Join(state.ContainersDir, containerID+".json"))
		_ = os.Remove(logFile)
		must(cause)
	}

	var vethHost, vethPeer, containerIP string
	if opt.network == "none" {
		logv(opt.quiet, "Skipping host network (--network=none)\n")
	} else {
		if err := gockernet.EnsureBridge(); err != nil {
			abort("", fmt.Errorf("bridge/NAT setup failed: %v (use --network=none to skip)", err))
		}

		logv(opt.quiet, "Setting up network namespace...\n")
		var netErr error
		vethHost, vethPeer, containerIP, netErr = gockernet.SetupContainer(containerID, childPid, opt.detached || opt.quiet)
		if netErr != nil {
			abort(vethHost, fmt.Errorf("network setup failed: %v (use --network=none to skip)", netErr))
		}
	}

	ctr := &state.ContainerState{
		ID:          containerID,
		Name:        opt.name,
		PID:         childPid,
		Status:      "running",
		CreatedAt:   time.Now(),
		Command:     opt.command,
		VethHost:    vethHost,
		VethPeer:    vethPeer,
		ContainerIP: containerIP,
		LogFile:     logFile,
		Detached:    opt.detached,
		CgroupPath:  cgroupPath,
		RootfsPath:  resolvedRootfs,
	}
	if err := state.Save(ctr); err != nil {
		fmt.Fprintf(parentOutput, "Warning: Failed to save container state: %v\n", err)
	}

	if opt.detached {
		fmt.Printf("Container started with ID: %s\n", containerID)
		if !opt.quiet {
			fmt.Printf("Use 'gocker logs %s' to view logs\n", containerID)
		}
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
	quiet := os.Getenv("GOCKER_QUIET") == "1"
	logv(quiet, "Running in child process with PID %d\n", os.Getpid())

	containerUID := syscall.Getuid()
	containerGID := syscall.Getgid()
	logv(quiet, "Container UID: %d, GID: %d\n", containerUID, containerGID)

	rootfsPath := os.Getenv("GOCKER_ROOTFS")
	if rootfsPath == "" {
		rootfsPath = "./rootfs"
	}
	overlayBase := os.Getenv("GOCKER_OVERLAY_DIR")
	if overlayBase == "" {
		must(fmt.Errorf("GOCKER_OVERLAY_DIR not set"))
	}

	logv(quiet, "Configuring container network...\n")
	if err := gockernet.ConfigureInside(); err != nil {
		logv(quiet, "Warning: Failed to configure container network: %v\n", err)
	}

	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		logv(quiet, "  - Note: MS_PRIVATE on /: %v\n", err)
	}

	logv(quiet, "Mounting OverlayFS (lower=%s, dirs=%s)...\n", rootfsPath, overlayBase)
	merged, err := overlay.Mount(rootfsPath, overlayBase)
	must(err)

	volumesStr := os.Getenv("GOCKER_VOLUMES")
	if volumesStr != "" {
		logv(quiet, "Mounting volumes...\n")
		if err := overlay.MountVolumes(volumesStr, merged); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to mount volumes: %v\n", err)
		}
	}

	logv(quiet, "Setting hostname to 'gocker-container'...\n")
	must(syscall.Sethostname([]byte("gocker-container")))

	if err := overlay.EnsureDevices(merged); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to set up jail device nodes: %v\n", err)
	}

	logv(quiet, "Entering OverlayFS jail with pivot_root (%s)...\n", merged)
	must(overlay.Pivot(merged))

	logv(quiet, "Mounting proc filesystem...\n")
	must(syscall.Mount("proc", "proc", "proc", 0, ""))
	defer syscall.Unmount("proc", 0)

	// Teaching-only: drop extra caps after the jail is up. Not a production
	// profile (no seccomp). Volume/tmpfs mounts in the *command* still work
	// because we keep CAP_SYS_ADMIN.
	if err := ns.DropTeachingCaps(); err != nil {
		logv(quiet, "Warning: teaching cap drop: %v\n", err)
	}

	command := "/bin/sh"
	args := []string{}
	if len(os.Args) > 2 {
		command = os.Args[2]
		if len(os.Args) > 3 {
			args = os.Args[3:]
		}
	}

	os.Setenv("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")

	logv(quiet, "Executing command: %s %v\n", command, args)
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
