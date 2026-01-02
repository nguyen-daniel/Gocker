package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

// must is a helper function that exits the program if an error occurs
func must(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func main() {
	// Check for root permissions (required for namespace operations)
	if os.Geteuid() != 0 {
		fmt.Println("Error: This program must be run with sudo/root permissions")
		os.Exit(1)
	}

	if len(os.Args) < 2 {
		fmt.Println("Usage: gocker run <command>")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		run()
	case "child":
		child()
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func run() {
	fmt.Fprintf(os.Stderr, "Running %v as PID %d\n", os.Args[2:], os.Getpid())
	fmt.Fprintln(os.Stderr, "Creating isolated namespaces...")
	fmt.Fprintln(os.Stderr, "  - UTS namespace (hostname isolation)")
	fmt.Fprintln(os.Stderr, "  - PID namespace (process ID isolation)")
	fmt.Fprintln(os.Stderr, "  - Mount namespace (filesystem isolation)")

	cmd := exec.Command("/proc/self/exe", append([]string{"child"}, os.Args[2:]...)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUTS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNS,
	}

	must(cmd.Run())
}

func limitResources() error {
	fmt.Fprintln(os.Stderr, "Setting up cgroups v2 for resource limits...")
	cgroupPath := "/sys/fs/cgroup/gocker"

	// Create the cgroup directory
	if err := os.MkdirAll(cgroupPath, 0755); err != nil {
		return fmt.Errorf("failed to create cgroup directory: %v", err)
	}

	// Write current PID to cgroup.procs
	pid := os.Getpid()
	cgroupProcsPath := filepath.Join(cgroupPath, "cgroup.procs")
	if err := os.WriteFile(cgroupProcsPath, []byte(strconv.Itoa(pid)), 0644); err != nil {
		return fmt.Errorf("failed to write PID to cgroup.procs: %v", err)
	}

	// Set maximum processes limit to 20
	pidsMaxPath := filepath.Join(cgroupPath, "pids.max")
	if err := os.WriteFile(pidsMaxPath, []byte("20"), 0644); err != nil {
		return fmt.Errorf("failed to set pids.max: %v", err)
	}

	fmt.Fprintln(os.Stderr, "  - Process limit set to 20")
	return nil
}

func child() {
	fmt.Fprintf(os.Stderr, "Running in child process with PID %d\n", os.Getpid())

	// Limit resources using cgroups v2
	must(limitResources())

	// Set hostname for the container
	fmt.Fprintln(os.Stderr, "Setting hostname to 'gocker-container'...")
	must(syscall.Sethostname([]byte("gocker-container")))

	// Create filesystem jail using chroot
	fmt.Fprintln(os.Stderr, "Creating filesystem jail with chroot...")
	must(syscall.Chroot("./rootfs"))

	// Change to root directory after chroot
	must(os.Chdir("/"))

	// Mount proc filesystem
	fmt.Fprintln(os.Stderr, "Mounting proc filesystem...")
	must(syscall.Mount("proc", "proc", "proc", 0, ""))
	defer syscall.Unmount("proc", 0)

	// Get the command to execute (default to /bin/sh if none provided)
	command := "/bin/sh"
	args := []string{}
	if len(os.Args) > 2 {
		command = os.Args[2]
		if len(os.Args) > 3 {
			args = os.Args[3:]
		}
	}

	// Execute the user's command
	fmt.Fprintf(os.Stderr, "Executing command: %s %v\n", command, args)
	cmd := exec.Command(command, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	must(cmd.Run())
}

