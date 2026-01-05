package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	stateDir      = "/var/lib/gocker"
	containersDir = "/var/lib/gocker/containers"
)

// ContainerState represents the state of a container
type ContainerState struct {
	ID        string    `json:"id"`
	PID       int       `json:"pid"`
	Status    string    `json:"status"` // "running", "stopped", "exited"
	CreatedAt time.Time `json:"created_at"`
	Command   []string  `json:"command"`
	VethHost  string    `json:"veth_host,omitempty"`
	LogFile   string    `json:"log_file"`
	Detached  bool      `json:"detached"`
}

// must is a helper function that exits the program if an error occurs
func must(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	// Skip root check for "child" and "gui" commands
	// - "child" runs in a user namespace where it appears as non-root
	// - "gui" doesn't need root (it will use sudo internally for operations)
	if os.Args[1] != "child" && os.Args[1] != "gui" {
		// Check for root permissions (required for namespace operations)
		if os.Geteuid() != 0 {
			fmt.Println("Error: This program must be run with sudo/root permissions")
			os.Exit(1)
		}
	}

	switch os.Args[1] {
	case "run":
		run()
	case "child":
		child()
	case "ps":
		listContainers()
	case "stop":
		if len(os.Args) < 3 {
			fmt.Println("Error: container ID required")
			fmt.Println("Usage: gocker stop <container-id>")
			os.Exit(1)
		}
		stopContainer(os.Args[2])
	case "rm":
		if len(os.Args) < 3 {
			fmt.Println("Error: container ID required")
			fmt.Println("Usage: gocker rm <container-id>")
			os.Exit(1)
		}
		removeContainer(os.Args[2])
	case "logs":
		if len(os.Args) < 3 {
			fmt.Println("Error: container ID required")
			fmt.Println("Usage: gocker logs <container-id>")
			os.Exit(1)
		}
		showLogs(os.Args[2])
	case "gui":
		// Launch GUI mode
		// GUI doesn't need root - it will use sudo internally for operations that require it
		// Check if we're running as root and warn (GUI needs X11 access which root doesn't have)
		if os.Geteuid() == 0 {
			fmt.Fprintf(os.Stderr, "Warning: Running GUI as root may cause X11 display issues.\n")
			fmt.Fprintf(os.Stderr, "Consider running without sudo: ./gocker gui\n")
			fmt.Fprintf(os.Stderr, "The GUI will use sudo internally for container operations.\n\n")
		}
		gui := NewGockerGUI()
		gui.Run()
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Usage: gocker <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  run     Run a new container")
	fmt.Println("  ps      List all containers")
	fmt.Println("  stop    Stop a running container")
	fmt.Println("  rm      Remove a container")
	fmt.Println("  logs    Show container logs")
	fmt.Println("  gui     Launch graphical user interface")
	fmt.Println()
	fmt.Println("Run options:")
	fmt.Println("  --cpu-limit <limit>       CPU limit (e.g., '1' for 1 CPU, '0.5' for 50% of one CPU, 'max' for unlimited)")
	fmt.Println("  --memory-limit <limit>   Memory limit (e.g., '512M', '1G', 'max' for unlimited)")
	fmt.Println("  --volume, -v <host:container>  Mount a host directory into the container")
	fmt.Println("  --detach, -d             Run container in background")
}

// generateContainerID generates a unique container ID
func generateContainerID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// ensureStateDir ensures the state directory exists
func ensureStateDir() error {
	if err := os.MkdirAll(containersDir, 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %v", err)
	}
	return nil
}

// saveContainerState saves container state to disk
func saveContainerState(state *ContainerState) error {
	if err := ensureStateDir(); err != nil {
		return err
	}

	stateFile := filepath.Join(containersDir, state.ID+".json")
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal container state: %v", err)
	}

	if err := os.WriteFile(stateFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write container state: %v", err)
	}

	return nil
}

// loadContainerState loads container state from disk
func loadContainerState(containerID string) (*ContainerState, error) {
	stateFile := filepath.Join(containersDir, containerID+".json")
	data, err := os.ReadFile(stateFile)
	if err != nil {
		return nil, fmt.Errorf("container not found: %s", containerID)
	}

	var state ContainerState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse container state: %v", err)
	}

	return &state, nil
}

// updateContainerStatus updates the container status
func updateContainerStatus(containerID string, status string) error {
	state, err := loadContainerState(containerID)
	if err != nil {
		return err
	}

	state.Status = status
	return saveContainerState(state)
}

func run() {
	// Parse flags for resource limits, volumes, and detached mode
	// We need to manually parse flags since they come before the command
	var cpuLimit, memoryLimit string
	var volumes []string
	var detached bool
	args := os.Args[2:]
	var remainingArgs []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--cpu-limit" {
			if i+1 < len(args) {
				cpuLimit = args[i+1]
				i++ // Skip the value
			}
		} else if arg == "--memory-limit" {
			if i+1 < len(args) {
				memoryLimit = args[i+1]
				i++ // Skip the value
			}
		} else if arg == "--volume" || arg == "-v" {
			if i+1 < len(args) {
				volumes = append(volumes, args[i+1])
				i++ // Skip the value
			}
		} else if arg == "--detach" || arg == "-d" {
			detached = true
		} else {
			remainingArgs = append(remainingArgs, arg)
		}
	}

	if len(remainingArgs) == 0 {
		fmt.Println("Error: command required")
		fmt.Println("Usage: gocker run [options] <command> [args...]")
		os.Exit(1)
	}

	// Generate container ID
	containerID := generateContainerID()

	// Set environment variables to pass limits and volumes to child process
	if cpuLimit != "" {
		os.Setenv("GOCKER_CPU_LIMIT", cpuLimit)
	}
	if memoryLimit != "" {
		os.Setenv("GOCKER_MEMORY_LIMIT", memoryLimit)
	}
	if len(volumes) > 0 {
		// Join volumes with a delimiter (|) to pass multiple volumes
		os.Setenv("GOCKER_VOLUMES", strings.Join(volumes, "|"))
	}
	os.Setenv("GOCKER_CONTAINER_ID", containerID)

	// Create log file for container
	logFile := filepath.Join(stateDir, "logs", containerID+".log")
	if err := os.MkdirAll(filepath.Dir(logFile), 0755); err != nil {
		must(fmt.Errorf("failed to create logs directory: %v", err))
	}

	logWriter, err := os.Create(logFile)
	if err != nil {
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
	fmt.Fprintln(os.Stderr, "  - User namespace (user ID isolation)")

	cmd := exec.Command("/proc/self/exe", append([]string{"child"}, remainingArgs...)...)

	// In detached mode, redirect stdin/stdout/stderr to log file
	if detached {
		cmd.Stdin = nil
		cmd.Stdout = io.MultiWriter(logWriter, os.Stdout)
		cmd.Stderr = io.MultiWriter(logWriter, os.Stderr)
	} else {
		cmd.Stdin = os.Stdin
		cmd.Stdout = io.MultiWriter(logWriter, os.Stdout)
		cmd.Stderr = io.MultiWriter(logWriter, os.Stderr)
	}

	// Set up user namespace mappings using Go's built-in support
	// This ensures mappings are applied before the child process starts
	// The kernel will give the child full capabilities in the new user namespace
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUTS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNS | syscall.CLONE_NEWNET | syscall.CLONE_NEWUSER,
		UidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getuid(), Size: 1},
		},
		GidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getgid(), Size: 1},
		},
	}
	fmt.Fprintf(os.Stderr, "  - User namespace: mapping container UID 0 -> host UID %d\n", os.Getuid())

	// Start the command (don't wait for it to finish yet)
	if err := cmd.Start(); err != nil {
		must(err)
	}

	// Get the child's PID (in parent namespace, before CLONE_NEWPID takes effect)
	childPid := cmd.Process.Pid

	// In interactive mode, redirect parent messages to log file only to avoid
	// interfering with the child's shell output
	var parentOutput io.Writer
	if detached {
		parentOutput = io.MultiWriter(logWriter, os.Stderr)
	} else {
		// In interactive mode, only write to log file to keep shell output clean
		parentOutput = logWriter
	}

	fmt.Fprintf(parentOutput, "  - Child PID: %d\n", childPid)

	// Set up network namespace for the container
	// In interactive mode, suppress verbose messages to avoid interfering with shell output
	if !detached {
		// Suppress network setup messages in interactive mode
		fmt.Fprintln(logWriter, "Setting up network namespace...")
	} else {
		fmt.Fprintln(os.Stderr, "Setting up network namespace...")
	}
	vethHost, err := setupNetwork(childPid, !detached)
	if err != nil {
		if detached {
			fmt.Fprintf(os.Stderr, "Warning: Failed to set up network: %v\n", err)
		} else {
			fmt.Fprintf(logWriter, "Warning: Failed to set up network: %v\n", err)
		}
		// Continue even if network setup fails
	}

	// Save container state
	state := &ContainerState{
		ID:        containerID,
		PID:       childPid,
		Status:    "running",
		CreatedAt: time.Now(),
		Command:   remainingArgs,
		VethHost:  vethHost,
		LogFile:   logFile,
		Detached:  detached,
	}
	if err := saveContainerState(state); err != nil {
		fmt.Fprintf(parentOutput, "Warning: Failed to save container state: %v\n", err)
	}

	if detached {
		fmt.Printf("Container started with ID: %s\n", containerID)
		fmt.Printf("Use 'gocker logs %s' to view logs\n", containerID)
		return
	}

	// Cleanup function
	cleanup := func() {
		updateContainerStatus(containerID, "exited")
		if vethHost != "" {
			cleanupNetwork(vethHost, "")
		}
	}
	defer cleanup()

	// Wait for the command to finish
	if err := cmd.Wait(); err != nil {
		// Command exited with error, but we still need to update status
		updateContainerStatus(containerID, "exited")
		os.Exit(cmd.ProcessState.ExitCode())
	}
}

// setupNetwork creates a veth pair and configures networking for the container
// Returns the host veth interface name for cleanup
// quiet: if true, suppresses output messages (for interactive mode)
func setupNetwork(childPid int, quiet bool) (string, error) {
	// Generate unique interface names based on child PID
	vethHost := fmt.Sprintf("veth%d", childPid)
	vethContainer := fmt.Sprintf("vethc%d", childPid)

	// Create veth pair
	if !quiet {
		fmt.Fprintf(os.Stderr, "  - Creating veth pair: %s <-> %s\n", vethHost, vethContainer)
	}
	cmd := exec.Command("ip", "link", "add", vethHost, "type", "veth", "peer", "name", vethContainer)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to create veth pair: %v", err)
	}

	// Bring up the host end
	cmd = exec.Command("ip", "link", "set", vethHost, "up")
	if err := cmd.Run(); err != nil {
		cleanupNetwork(vethHost, vethContainer)
		return "", fmt.Errorf("failed to bring up host veth: %v", err)
	}

	// Move container end into the container's network namespace
	if !quiet {
		fmt.Fprintf(os.Stderr, "  - Moving %s into container namespace\n", vethContainer)
	}
	netnsPath := fmt.Sprintf("/proc/%d/ns/net", childPid)
	cmd = exec.Command("ip", "link", "set", vethContainer, "netns", netnsPath)
	if err := cmd.Run(); err != nil {
		cleanupNetwork(vethHost, vethContainer)
		return "", fmt.Errorf("failed to move veth into container namespace: %v", err)
	}

	// Configure IP address for host end
	hostIP := "10.0.0.1/24"
	cmd = exec.Command("ip", "addr", "add", hostIP, "dev", vethHost)
	if err := cmd.Run(); err != nil {
		// IP might already be set, continue
		fmt.Fprintf(os.Stderr, "  - Note: Host IP configuration: %v\n", err)
	}

	// Enable IP forwarding
	cmd = exec.Command("sysctl", "-w", "net.ipv4.ip_forward=1")
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "  - Warning: Failed to enable IP forwarding: %v\n", err)
	}

	// Set up NAT using iptables for internet connectivity
	// Find the default interface (usually the one with a default route)
	defaultInterface, err := getDefaultInterface()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  - Warning: Could not determine default interface: %v\n", err)
		fmt.Fprintf(os.Stderr, "  - NAT will not be configured. Container will have local network only.\n")
	} else {
		if !quiet {
			fmt.Fprintf(os.Stderr, "  - Setting up NAT via %s for internet connectivity\n", defaultInterface)
		}
		// Enable NAT masquerading
		cmd = exec.Command("iptables", "-t", "nat", "-A", "POSTROUTING", "-s", "10.0.0.0/24", "-o", defaultInterface, "-j", "MASQUERADE")
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "  - Warning: Failed to set up NAT (iptables may not be available): %v\n", err)
		}
		// Allow forwarding from veth to default interface
		cmd = exec.Command("iptables", "-A", "FORWARD", "-i", vethHost, "-o", defaultInterface, "-j", "ACCEPT")
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "  - Warning: Failed to set up forwarding rule: %v\n", err)
		}
		// Allow forwarding from default interface to veth
		cmd = exec.Command("iptables", "-A", "FORWARD", "-i", defaultInterface, "-o", vethHost, "-j", "ACCEPT")
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "  - Warning: Failed to set up forwarding rule: %v\n", err)
		}
	}

	// Only print completion message if not in quiet mode (interactive)
	if !quiet {
		fmt.Fprintln(os.Stderr, "  - Network setup complete")
	}
	return vethHost, nil
}

// getDefaultInterface finds the default network interface
func getDefaultInterface() (string, error) {
	cmd := exec.Command("ip", "route", "show", "default")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	// Parse output like "default via 192.168.1.1 dev eth0"
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "default") && strings.Contains(line, "dev") {
			parts := strings.Fields(line)
			for i, part := range parts {
				if part == "dev" && i+1 < len(parts) {
					return parts[i+1], nil
				}
			}
		}
	}

	return "", fmt.Errorf("could not find default interface")
}

// setupUserNamespace writes UID/GID mappings to /proc/<pid>/uid_map and /proc/<pid>/gid_map
// The mapping format is: container_id host_id count
// This means container UID X maps to host UID Y
// The child process inherits the parent's UID (0 when running as root), so we need to
// map that host UID to container UID 0 for the child to appear as root in the container.
func setupUserNamespace(childPid int) error {
	// Get the current UID/GID - this is what the child process inherited
	hostUID := os.Getuid()
	hostGID := os.Getgid()

	fmt.Fprintf(os.Stderr, "  - Mapping container root (UID 0) to host UID %d\n", hostUID)

	// Disable setgroups before writing gid_map (required for user namespaces)
	// This prevents the child from changing its supplementary groups
	setgroupsPath := fmt.Sprintf("/proc/%d/setgroups", childPid)
	if err := os.WriteFile(setgroupsPath, []byte("deny\n"), 0644); err != nil {
		// setgroups might not exist on all systems, continue if it fails
		fmt.Fprintf(os.Stderr, "  - Note: Could not disable setgroups: %v\n", err)
	}

	// Write GID mapping: container GID 0 -> host GID
	// Format: container_id host_id count
	gidMapPath := fmt.Sprintf("/proc/%d/gid_map", childPid)
	gidMap := fmt.Sprintf("0 %d 1\n", hostGID)
	if err := os.WriteFile(gidMapPath, []byte(gidMap), 0644); err != nil {
		return fmt.Errorf("failed to write gid_map: %v", err)
	}

	// Write UID mapping: container UID 0 -> host UID
	// Format: container_id host_id count
	// The child inherited host UID 0 (root), so we map container 0 to host 0
	// This allows the child to appear as UID 0 in the container namespace
	uidMapPath := fmt.Sprintf("/proc/%d/uid_map", childPid)
	uidMap := fmt.Sprintf("0 %d 1\n", hostUID)
	if err := os.WriteFile(uidMapPath, []byte(uidMap), 0644); err != nil {
		return fmt.Errorf("failed to write uid_map: %v", err)
	}

	// Verify the mapping was written correctly
	if data, err := os.ReadFile(uidMapPath); err == nil {
		fmt.Fprintf(os.Stderr, "  - Verified uid_map: %s", string(data))
	} else {
		fmt.Fprintf(os.Stderr, "  - Warning: Could not verify uid_map: %v\n", err)
	}

	fmt.Fprintf(os.Stderr, "  - User namespace mapping complete (container root -> host UID %d)\n", hostUID)
	return nil
}

// cleanupNetwork removes veth interfaces and NAT rules
func cleanupNetwork(vethHost, vethContainer string) {
	if vethHost == "" {
		return
	}

	fmt.Fprintf(os.Stderr, "Cleaning up network interfaces...\n")

	// Remove iptables rules (best effort, may fail if already removed)
	defaultInterface, err := getDefaultInterface()
	if err == nil {
		exec.Command("iptables", "-t", "nat", "-D", "POSTROUTING", "-s", "10.0.0.0/24", "-o", defaultInterface, "-j", "MASQUERADE").Run()
		exec.Command("iptables", "-D", "FORWARD", "-i", vethHost, "-o", defaultInterface, "-j", "ACCEPT").Run()
		exec.Command("iptables", "-D", "FORWARD", "-i", defaultInterface, "-o", vethHost, "-j", "ACCEPT").Run()
	}

	// Remove host veth (container end is automatically removed when namespace is destroyed)
	if err := exec.Command("ip", "link", "delete", vethHost).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "  - Note: Interface cleanup: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "  - Removed interface: %s\n", vethHost)
	}
}

// parseCPULimit parses CPU limit string and returns the cgroup v2 cpu.max format
// Format: "quota period" in microseconds
// Examples: "1" -> "100000 100000" (1 CPU), "0.5" -> "50000 100000" (50% of 1 CPU), "max" -> "max"
func parseCPULimit(cpuLimit string) (string, error) {
	if cpuLimit == "" || cpuLimit == "max" {
		return "max", nil
	}

	cpu, err := strconv.ParseFloat(cpuLimit, 64)
	if err != nil {
		return "", fmt.Errorf("invalid CPU limit format: %v", err)
	}

	if cpu <= 0 {
		return "", fmt.Errorf("CPU limit must be positive")
	}

	// cgroup v2 uses microseconds
	// period is typically 100000 microseconds (100ms)
	// quota = cpu * period
	period := 100000
	quota := int64(float64(period) * cpu)

	return fmt.Sprintf("%d %d", quota, period), nil
}

// parseMemoryLimit parses memory limit string and returns bytes as string
// Examples: "512M" -> "536870912", "1G" -> "1073741824", "max" -> "max"
func parseMemoryLimit(memoryLimit string) (string, error) {
	if memoryLimit == "" || memoryLimit == "max" {
		return "max", nil
	}

	memoryLimit = strings.TrimSpace(memoryLimit)
	memoryLimit = strings.ToUpper(memoryLimit)

	var multiplier int64 = 1
	if strings.HasSuffix(memoryLimit, "K") {
		multiplier = 1024
		memoryLimit = strings.TrimSuffix(memoryLimit, "K")
	} else if strings.HasSuffix(memoryLimit, "M") {
		multiplier = 1024 * 1024
		memoryLimit = strings.TrimSuffix(memoryLimit, "M")
	} else if strings.HasSuffix(memoryLimit, "G") {
		multiplier = 1024 * 1024 * 1024
		memoryLimit = strings.TrimSuffix(memoryLimit, "G")
	}

	value, err := strconv.ParseInt(memoryLimit, 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid memory limit format: %v", err)
	}

	if value <= 0 {
		return "", fmt.Errorf("memory limit must be positive")
	}

	bytes := value * multiplier
	return strconv.FormatInt(bytes, 10), nil
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

	// Get CPU limit from environment variable
	cpuLimitStr := os.Getenv("GOCKER_CPU_LIMIT")
	if cpuLimitStr != "" {
		cpuMax, err := parseCPULimit(cpuLimitStr)
		if err != nil {
			return fmt.Errorf("failed to parse CPU limit: %v", err)
		}

		cpuMaxPath := filepath.Join(cgroupPath, "cpu.max")
		if err := os.WriteFile(cpuMaxPath, []byte(cpuMax), 0644); err != nil {
			return fmt.Errorf("failed to set cpu.max: %v", err)
		}

		if cpuMax == "max" {
			fmt.Fprintln(os.Stderr, "  - CPU limit: unlimited")
		} else {
			fmt.Fprintf(os.Stderr, "  - CPU limit: %s\n", cpuLimitStr)
		}
	}

	// Get memory limit from environment variable
	memoryLimitStr := os.Getenv("GOCKER_MEMORY_LIMIT")
	if memoryLimitStr != "" {
		memoryMax, err := parseMemoryLimit(memoryLimitStr)
		if err != nil {
			return fmt.Errorf("failed to parse memory limit: %v", err)
		}

		memoryMaxPath := filepath.Join(cgroupPath, "memory.max")
		if err := os.WriteFile(memoryMaxPath, []byte(memoryMax), 0644); err != nil {
			return fmt.Errorf("failed to set memory.max: %v", err)
		}

		if memoryMax == "max" {
			fmt.Fprintln(os.Stderr, "  - Memory limit: unlimited")
		} else {
			fmt.Fprintf(os.Stderr, "  - Memory limit: %s\n", memoryLimitStr)
		}
	}

	return nil
}

func child() {
	fmt.Fprintf(os.Stderr, "Running in child process with PID %d\n", os.Getpid())

	// User namespace mapping is already applied by the kernel via SysProcAttr.UidMappings/GidMappings
	// The child process starts with the correct UID/GID and full capabilities in the user namespace
	containerUID := syscall.Getuid()
	containerGID := syscall.Getgid()
	fmt.Fprintf(os.Stderr, "Container UID: %d, GID: %d\n", containerUID, containerGID)

	// Limit resources using cgroups v2
	must(limitResources())

	// Set hostname for the container
	fmt.Fprintln(os.Stderr, "Setting hostname to 'gocker-container'...")
	must(syscall.Sethostname([]byte("gocker-container")))

	// Configure network inside the container namespace
	// This must be done before chroot, as we need access to /proc and network tools
	fmt.Fprintln(os.Stderr, "Configuring container network...")
	if err := configureContainerNetwork(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to configure container network: %v\n", err)
		// Continue even if network configuration fails
	}

	// Mount volumes before chroot
	// Volumes must be mounted before chroot so they're accessible in the container
	volumesStr := os.Getenv("GOCKER_VOLUMES")
	if volumesStr != "" {
		fmt.Fprintln(os.Stderr, "Mounting volumes...")
		if err := mountVolumes(volumesStr); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to mount volumes: %v\n", err)
			// Continue even if volume mounting fails
		}
	}

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

	// Set PATH environment variable for the container
	// This ensures commands like ls, ps, hostname can be found
	os.Setenv("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")

	// Execute the user's command
	fmt.Fprintf(os.Stderr, "Executing command: %s %v\n", command, args)
	cmd := exec.Command(command, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ() // Use the environment with PATH set

	// For interactive shells, ensure we have a TTY
	// This helps with prompt display and input handling
	if command == "/bin/sh" && len(args) == 0 {
		// Force interactive mode
		cmd.Args = []string{command, "-i"}
	}

	must(cmd.Run())
}

// mountVolumes mounts host directories into the container rootfs
// This must be done before chroot so the mounts are accessible in the container
// Format: "host:container|host2:container2" (multiple volumes separated by |)
func mountVolumes(volumesStr string) error {
	volumes := strings.Split(volumesStr, "|")
	rootfsPath := "./rootfs"

	for _, volume := range volumes {
		volume = strings.TrimSpace(volume)
		if volume == "" {
			continue
		}

		// Parse volume specification: host:container
		parts := strings.Split(volume, ":")
		if len(parts) != 2 {
			return fmt.Errorf("invalid volume format: %s (expected host:container)", volume)
		}

		hostPath := strings.TrimSpace(parts[0])
		containerPath := strings.TrimSpace(parts[1])

		if hostPath == "" || containerPath == "" {
			return fmt.Errorf("invalid volume format: %s (host and container paths cannot be empty)", volume)
		}

		// Ensure container path is absolute
		if !filepath.IsAbs(containerPath) {
			return fmt.Errorf("container path must be absolute: %s", containerPath)
		}

		// Check if host path exists
		hostInfo, err := os.Stat(hostPath)
		if err != nil {
			return fmt.Errorf("host path does not exist: %s: %v", hostPath, err)
		}

		// Create mount point in rootfs (before chroot)
		// The mount point path relative to rootfs
		mountPoint := filepath.Join(rootfsPath, containerPath)

		// Create parent directories if they don't exist
		if err := os.MkdirAll(filepath.Dir(mountPoint), 0755); err != nil {
			return fmt.Errorf("failed to create parent directories for mount point %s: %v", mountPoint, err)
		}

		// Create the mount point directory if it doesn't exist
		// If host path is a directory, create a directory; if it's a file, create parent and touch the file
		if hostInfo.IsDir() {
			if err := os.MkdirAll(mountPoint, 0755); err != nil {
				return fmt.Errorf("failed to create mount point directory %s: %v", mountPoint, err)
			}
		} else {
			// For files, ensure parent directory exists and create the file
			if err := os.MkdirAll(filepath.Dir(mountPoint), 0755); err != nil {
				return fmt.Errorf("failed to create parent directory for file mount point %s: %v", mountPoint, err)
			}
			// Create an empty file if it doesn't exist
			if _, err := os.Stat(mountPoint); os.IsNotExist(err) {
				if f, err := os.Create(mountPoint); err != nil {
					return fmt.Errorf("failed to create file mount point %s: %v", mountPoint, err)
				} else {
					f.Close()
				}
			}
		}

		// Perform bind mount
		// MS_BIND: Create a bind mount
		// MS_REC: Recursive bind mount (mount subtree)
		// MS_PRIVATE: Make this mount private (don't propagate mount events to parent)
		flags := syscall.MS_BIND | syscall.MS_REC
		if err := syscall.Mount(hostPath, mountPoint, "", uintptr(flags), ""); err != nil {
			return fmt.Errorf("failed to bind mount %s to %s: %v", hostPath, mountPoint, err)
		}

		// Set mount propagation to private
		// This prevents mount/unmount events in the container from affecting the host
		if err := syscall.Mount("", mountPoint, "", syscall.MS_PRIVATE|syscall.MS_REC, ""); err != nil {
			// If setting mount propagation fails, log a warning but continue
			fmt.Fprintf(os.Stderr, "  - Warning: Failed to set mount propagation for %s: %v\n", mountPoint, err)
		}

		fmt.Fprintf(os.Stderr, "  - Mounted %s -> %s\n", hostPath, containerPath)
	}

	return nil
}

// configureContainerNetwork sets up the network interface inside the container
// This runs before chroot, so we can use the host's ip command
func configureContainerNetwork() error {
	pid := os.Getpid()

	// Find the veth interface in this namespace (it will be named vethc<pid>)
	vethName := fmt.Sprintf("vethc%d", pid)

	// Use absolute path to ip command (available on host before chroot)
	ipCmd := "/usr/bin/ip"
	if _, err := os.Stat(ipCmd); os.IsNotExist(err) {
		// Try alternative locations
		ipCmd = "/sbin/ip"
		if _, err := os.Stat(ipCmd); os.IsNotExist(err) {
			ipCmd = "ip" // Fall back to PATH
		}
	}

	// First, try to find any veth interface (in case naming differs)
	cmd := exec.Command(ipCmd, "link", "show", "type", "veth")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to list veth interfaces: %v", err)
	}

	// Parse output to find veth interface name
	lines := strings.Split(string(output), "\n")
	var foundVeth string
	for _, line := range lines {
		if strings.Contains(line, "veth") {
			// Extract interface name (format: "2: vethc123: <...>")
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				name := strings.TrimSuffix(parts[1], ":")
				if strings.HasPrefix(name, "veth") {
					foundVeth = name
					break
				}
			}
		}
	}

	if foundVeth == "" {
		// Try the expected name
		foundVeth = vethName
	}

	fmt.Fprintf(os.Stderr, "  - Found container veth interface: %s\n", foundVeth)

	// Bring up the interface
	cmd = exec.Command(ipCmd, "link", "set", foundVeth, "up")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to bring up container veth: %v", err)
	}

	// Assign IP address to container interface (10.0.0.2/24)
	containerIP := "10.0.0.2/24"
	cmd = exec.Command(ipCmd, "addr", "add", containerIP, "dev", foundVeth)
	if err := cmd.Run(); err != nil {
		// IP might already be set
		fmt.Fprintf(os.Stderr, "  - Note: IP assignment: %v\n", err)
	}

	// Set up default route through the host veth (10.0.0.1)
	cmd = exec.Command(ipCmd, "route", "add", "default", "via", "10.0.0.1", "dev", foundVeth)
	if err := cmd.Run(); err != nil {
		// Route might already exist
		fmt.Fprintf(os.Stderr, "  - Note: Route setup: %v\n", err)
	}

	fmt.Fprintf(os.Stderr, "  - Container IP: %s\n", containerIP)
	fmt.Fprintln(os.Stderr, "  - Network configuration complete")

	return nil
}

// listContainers lists all containers
func listContainers() {
	if err := ensureStateDir(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}

	files, err := os.ReadDir(containersDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading containers directory: %v\n", err)
		return
	}

	if len(files) == 0 {
		fmt.Println("No containers found")
		return
	}

	fmt.Printf("%-20s %-10s %-10s %-30s %s\n", "CONTAINER ID", "STATUS", "PID", "CREATED", "COMMAND")
	fmt.Println(strings.Repeat("-", 100))

	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".json") {
			continue
		}

		containerID := strings.TrimSuffix(file.Name(), ".json")
		state, err := loadContainerState(containerID)
		if err != nil {
			continue
		}

		// Check if process is still running
		status := state.Status
		if status == "running" {
			if err := syscall.Kill(state.PID, 0); err != nil {
				// Process is not running, update status
				status = "exited"
				updateContainerStatus(containerID, "exited")
			}
		}

		command := strings.Join(state.Command, " ")
		if len(command) > 40 {
			command = command[:37] + "..."
		}

		created := state.CreatedAt.Format("2006-01-02 15:04:05")
		fmt.Printf("%-20s %-10s %-10d %-30s %s\n", containerID[:12], status, state.PID, created, command)
	}
}

// stopContainer stops a running container
func stopContainer(containerID string) {
	state, err := loadContainerState(containerID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if state.Status != "running" {
		fmt.Printf("Container %s is not running (status: %s)\n", containerID[:12], state.Status)
		return
	}

	// Check if process is still running
	if err := syscall.Kill(state.PID, 0); err != nil {
		fmt.Printf("Container %s is not running\n", containerID[:12])
		updateContainerStatus(containerID, "exited")
		return
	}

	// Send SIGTERM to stop the container
	fmt.Printf("Stopping container %s (PID: %d)...\n", containerID[:12], state.PID)
	if err := syscall.Kill(state.PID, syscall.SIGTERM); err != nil {
		fmt.Fprintf(os.Stderr, "Error stopping container: %v\n", err)
		os.Exit(1)
	}

	// Wait a bit for graceful shutdown
	time.Sleep(2 * time.Second)

	// Check if still running, send SIGKILL if needed
	if err := syscall.Kill(state.PID, 0); err == nil {
		fmt.Println("Container did not stop gracefully, sending SIGKILL...")
		syscall.Kill(state.PID, syscall.SIGKILL)
		time.Sleep(500 * time.Millisecond)
	}

	// Cleanup network
	if state.VethHost != "" {
		cleanupNetwork(state.VethHost, "")
	}

	// Update status
	if err := updateContainerStatus(containerID, "stopped"); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to update container status: %v\n", err)
	}

	fmt.Printf("Container %s stopped\n", containerID[:12])
}

// removeContainer removes a container
func removeContainer(containerID string) {
	state, err := loadContainerState(containerID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Check if container is running
	if state.Status == "running" {
		if err := syscall.Kill(state.PID, 0); err == nil {
			fmt.Fprintf(os.Stderr, "Error: Cannot remove running container %s. Stop it first with 'gocker stop %s'\n", containerID[:12], containerID[:12])
			os.Exit(1)
		}
	}

	// Remove state file
	stateFile := filepath.Join(containersDir, containerID+".json")
	if err := os.Remove(stateFile); err != nil {
		fmt.Fprintf(os.Stderr, "Error removing container state: %v\n", err)
		os.Exit(1)
	}

	// Remove log file if it exists
	if state.LogFile != "" {
		if err := os.Remove(state.LogFile); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Warning: Failed to remove log file: %v\n", err)
		}
	}

	fmt.Printf("Container %s removed\n", containerID[:12])
}

// showLogs displays container logs
func showLogs(containerID string) {
	state, err := loadContainerState(containerID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if state.LogFile == "" {
		fmt.Fprintf(os.Stderr, "Error: No log file found for container %s\n", containerID[:12])
		os.Exit(1)
	}

	logFile, err := os.Open(state.LogFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening log file: %v\n", err)
		os.Exit(1)
	}
	defer logFile.Close()

	// Copy log file contents to stdout
	if _, err := io.Copy(os.Stdout, logFile); err != nil {
		fmt.Fprintf(os.Stderr, "Error reading log file: %v\n", err)
		os.Exit(1)
	}
}
