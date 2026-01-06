package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	stateDir      = "/var/lib/gocker"
	containersDir = "/var/lib/gocker/containers"
	ipamFile      = "/var/lib/gocker/ipam.json"
	bridgeName    = "gocker0"
	bridgeIP      = "10.0.0.1"
	bridgeCIDR    = "10.0.0.1/24"
	containerNet  = "10.0.0.0/24"
)

// ContainerState represents the state of a container
type ContainerState struct {
	ID          string    `json:"id"`
	PID         int       `json:"pid"`
	Status      string    `json:"status"` // "running", "stopped", "exited"
	CreatedAt   time.Time `json:"created_at"`
	Command     []string  `json:"command"`
	VethHost    string    `json:"veth_host,omitempty"`
	VethPeer    string    `json:"veth_peer,omitempty"`
	ContainerIP string    `json:"container_ip,omitempty"`
	LogFile     string    `json:"log_file"`
	Detached    bool      `json:"detached"`
	CgroupPath  string    `json:"cgroup_path,omitempty"`
	RootfsPath  string    `json:"rootfs_path,omitempty"`
}

// IPAMState tracks allocated IPs for containers
type IPAMState struct {
	AllocatedIPs map[string]string `json:"allocated_ips"` // containerID -> IP
	NextIP       int               `json:"next_ip"`       // last octet for next allocation (2-254)
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

	// Skip root check for "child" command
	// "child" runs in a user namespace where it appears as non-root
	if os.Args[1] != "child" {
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
	fmt.Println()
	fmt.Println("Run options:")
	fmt.Println("  --cpu-limit <limit>       CPU limit (e.g., '1' for 1 CPU, '0.5' for 50% of one CPU, 'max' for unlimited)")
	fmt.Println("  --memory-limit <limit>    Memory limit (e.g., '512M', '1G', 'max' for unlimited)")
	fmt.Println("  --volume, -v <host:container>  Mount a host directory into the container")
	fmt.Println("  --detach, -d              Run container in background")
	fmt.Println("  --rootfs <path>           Path to rootfs directory (default: ./rootfs)")
}

// generateContainerID generates a unique container ID
// Uses random bytes at the start to ensure unique veth interface names
func generateContainerID() string {
	randomBytes := make([]byte, 4)
	rand.Read(randomBytes)
	return hex.EncodeToString(randomBytes) + fmt.Sprintf("%d", time.Now().UnixNano())
}

// resolveRootfsPath resolves the rootfs path to an absolute path
// Priority: 1) explicit --rootfs flag, 2) ./rootfs relative to executable, 3) ./rootfs relative to cwd
func resolveRootfsPath(explicitPath string) (string, error) {
	if explicitPath != "" {
		absPath, err := filepath.Abs(explicitPath)
		if err != nil {
			return "", fmt.Errorf("failed to resolve rootfs path: %v", err)
		}
		if _, err := os.Stat(absPath); os.IsNotExist(err) {
			return "", fmt.Errorf("rootfs not found at %s", absPath)
		}
		return absPath, nil
	}

	// Try relative to executable first
	execPath, err := os.Executable()
	if err == nil {
		execDir := filepath.Dir(execPath)
		rootfsPath := filepath.Join(execDir, "rootfs")
		if _, err := os.Stat(rootfsPath); err == nil {
			return rootfsPath, nil
		}
	}

	// Fall back to current working directory
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current directory: %v", err)
	}
	rootfsPath := filepath.Join(cwd, "rootfs")
	if _, err := os.Stat(rootfsPath); os.IsNotExist(err) {
		return "", fmt.Errorf("rootfs not found. Run 'make setup' or specify --rootfs <path>")
	}
	return rootfsPath, nil
}

// ============================================================================
// State management with file locking
// ============================================================================

// lockFile acquires an exclusive lock on a file
func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

// unlockFile releases the lock on a file
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// ensureStateDir ensures the state directory exists
func ensureStateDir() error {
	if err := os.MkdirAll(containersDir, 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %v", err)
	}
	return nil
}

// saveContainerState saves container state to disk with file locking
func saveContainerState(state *ContainerState) error {
	if err := ensureStateDir(); err != nil {
		return err
	}

	stateFile := filepath.Join(containersDir, state.ID+".json")

	// Open file with exclusive lock
	f, err := os.OpenFile(stateFile, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open state file: %v", err)
	}
	defer f.Close()

	if err := lockFile(f); err != nil {
		return fmt.Errorf("failed to lock state file: %v", err)
	}
	defer unlockFile(f)

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal container state: %v", err)
	}

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("failed to write container state: %v", err)
	}

	return nil
}

// loadContainerState loads container state from disk with file locking
func loadContainerState(containerID string) (*ContainerState, error) {
	// Support partial container ID matching
	fullID, err := resolveContainerID(containerID)
	if err != nil {
		return nil, err
	}

	stateFile := filepath.Join(containersDir, fullID+".json")

	f, err := os.Open(stateFile)
	if err != nil {
		return nil, fmt.Errorf("container not found: %s", containerID)
	}
	defer f.Close()

	if err := lockFile(f); err != nil {
		return nil, fmt.Errorf("failed to lock state file: %v", err)
	}
	defer unlockFile(f)

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("failed to read state file: %v", err)
	}

	var state ContainerState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse container state: %v", err)
	}

	return &state, nil
}

// resolveContainerID resolves a partial container ID to the full ID
func resolveContainerID(partialID string) (string, error) {
	if err := ensureStateDir(); err != nil {
		return "", err
	}

	files, err := os.ReadDir(containersDir)
	if err != nil {
		return "", fmt.Errorf("failed to read containers directory: %v", err)
	}

	var matches []string
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		fullID := strings.TrimSuffix(file.Name(), ".json")
		if strings.HasPrefix(fullID, partialID) {
			matches = append(matches, fullID)
		}
	}

	if len(matches) == 0 {
		return "", fmt.Errorf("container not found: %s", partialID)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("ambiguous container ID: %s matches multiple containers", partialID)
	}
	return matches[0], nil
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

// ============================================================================
// IPAM (IP Address Management)
// ============================================================================

// loadIPAM loads the IPAM state from disk
func loadIPAM() (*IPAMState, error) {
	if err := ensureStateDir(); err != nil {
		return nil, err
	}

	data, err := os.ReadFile(ipamFile)
	if os.IsNotExist(err) {
		// Initialize new IPAM state
		return &IPAMState{
			AllocatedIPs: make(map[string]string),
			NextIP:       2, // Start at 10.0.0.2
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read IPAM file: %v", err)
	}

	var state IPAMState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse IPAM state: %v", err)
	}
	if state.AllocatedIPs == nil {
		state.AllocatedIPs = make(map[string]string)
	}
	return &state, nil
}

// saveIPAM saves the IPAM state to disk
func saveIPAM(state *IPAMState) error {
	if err := ensureStateDir(); err != nil {
		return err
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal IPAM state: %v", err)
	}

	if err := os.WriteFile(ipamFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write IPAM file: %v", err)
	}
	return nil
}

// allocateIP allocates an IP address for a container
func allocateIP(containerID string) (string, error) {
	ipam, err := loadIPAM()
	if err != nil {
		return "", err
	}

	// Check if container already has an IP
	if ip, exists := ipam.AllocatedIPs[containerID]; exists {
		return ip, nil
	}

	// Find next available IP
	for ipam.NextIP <= 254 {
		ip := fmt.Sprintf("10.0.0.%d", ipam.NextIP)

		// Check if IP is already allocated
		inUse := false
		for _, allocatedIP := range ipam.AllocatedIPs {
			if allocatedIP == ip {
				inUse = true
				break
			}
		}

		if !inUse {
			ipam.AllocatedIPs[containerID] = ip
			ipam.NextIP++
			if err := saveIPAM(ipam); err != nil {
				return "", err
			}
			return ip, nil
		}
		ipam.NextIP++
	}

	return "", fmt.Errorf("no available IP addresses in pool")
}

// releaseIP releases an IP address for a container
func releaseIP(containerID string) error {
	ipam, err := loadIPAM()
	if err != nil {
		return err
	}

	delete(ipam.AllocatedIPs, containerID)
	return saveIPAM(ipam)
}

// ============================================================================
// Bridge and Network Setup
// ============================================================================

// ensureBridge ensures the gocker0 bridge exists and is configured
func ensureBridge() error {
	// Check if bridge already exists
	if _, err := net.InterfaceByName(bridgeName); err == nil {
		// Bridge exists, verify it's up
		cmd := exec.Command("ip", "link", "set", bridgeName, "up")
		cmd.Run() // Ignore error, bridge might already be up
		return nil
	}

	fmt.Fprintln(os.Stderr, "  - Creating bridge gocker0...")

	// Create bridge
	cmd := exec.Command("ip", "link", "add", "name", bridgeName, "type", "bridge")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create bridge: %v", err)
	}

	// Set bridge IP
	cmd = exec.Command("ip", "addr", "add", bridgeCIDR, "dev", bridgeName)
	if err := cmd.Run(); err != nil {
		// IP might already be set, continue
		fmt.Fprintf(os.Stderr, "  - Note: Bridge IP configuration: %v\n", err)
	}

	// Bring bridge up
	cmd = exec.Command("ip", "link", "set", bridgeName, "up")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to bring up bridge: %v", err)
	}

	// Enable IP forwarding
	cmd = exec.Command("sysctl", "-w", "net.ipv4.ip_forward=1")
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "  - Warning: Failed to enable IP forwarding: %v\n", err)
	}

	// Setup NAT (idempotent)
	if err := setupNATRules(); err != nil {
		fmt.Fprintf(os.Stderr, "  - Warning: Failed to set up NAT: %v\n", err)
	}

	fmt.Fprintln(os.Stderr, "  - Bridge gocker0 created and configured")
	return nil
}

// setupNATRules sets up iptables NAT rules idempotently
func setupNATRules() error {
	defaultInterface, err := getDefaultInterface()
	if err != nil {
		return fmt.Errorf("could not determine default interface: %v", err)
	}

	// Check if MASQUERADE rule exists
	checkCmd := exec.Command("iptables", "-t", "nat", "-C", "POSTROUTING", "-s", containerNet, "-o", defaultInterface, "-j", "MASQUERADE")
	if checkCmd.Run() != nil {
		// Rule doesn't exist, add it
		cmd := exec.Command("iptables", "-t", "nat", "-A", "POSTROUTING", "-s", containerNet, "-o", defaultInterface, "-j", "MASQUERADE")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to add MASQUERADE rule: %v", err)
		}
	}

	// Check if FORWARD rules exist (gocker0 -> default interface)
	checkCmd = exec.Command("iptables", "-C", "FORWARD", "-i", bridgeName, "-o", defaultInterface, "-j", "ACCEPT")
	if checkCmd.Run() != nil {
		cmd := exec.Command("iptables", "-A", "FORWARD", "-i", bridgeName, "-o", defaultInterface, "-j", "ACCEPT")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to add FORWARD rule (out): %v", err)
		}
	}

	// Check if FORWARD rules exist (default interface -> gocker0)
	checkCmd = exec.Command("iptables", "-C", "FORWARD", "-i", defaultInterface, "-o", bridgeName, "-j", "ACCEPT")
	if checkCmd.Run() != nil {
		cmd := exec.Command("iptables", "-A", "FORWARD", "-i", defaultInterface, "-o", bridgeName, "-j", "ACCEPT")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to add FORWARD rule (in): %v", err)
		}
	}

	return nil
}

// setupContainerNetwork creates a veth pair and connects it to the bridge
func setupContainerNetwork(containerID string, childPid int, quiet bool) (vethHost, vethPeer, containerIP string, err error) {
	// Allocate IP for this container
	containerIP, err = allocateIP(containerID)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to allocate IP: %v", err)
	}

	// Generate unique interface names (truncate to avoid >15 char limit)
	shortID := containerID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	vethHost = fmt.Sprintf("veth%s", shortID)
	vethPeer = fmt.Sprintf("vethc%s", shortID)

	// Ensure interface names are <= 15 characters
	if len(vethHost) > 15 {
		vethHost = vethHost[:15]
	}
	if len(vethPeer) > 15 {
		vethPeer = vethPeer[:15]
	}

	// Create veth pair
	if !quiet {
		fmt.Fprintf(os.Stderr, "  - Creating veth pair: %s <-> %s\n", vethHost, vethPeer)
	}
	cmd := exec.Command("ip", "link", "add", vethHost, "type", "veth", "peer", "name", vethPeer)
	if err := cmd.Run(); err != nil {
		releaseIP(containerID)
		return "", "", "", fmt.Errorf("failed to create veth pair: %v", err)
	}

	// Attach host end to bridge
	cmd = exec.Command("ip", "link", "set", vethHost, "master", bridgeName)
	if err := cmd.Run(); err != nil {
		cleanupVeth(vethHost)
		releaseIP(containerID)
		return "", "", "", fmt.Errorf("failed to attach veth to bridge: %v", err)
	}

	// Bring up the host end
	cmd = exec.Command("ip", "link", "set", vethHost, "up")
	if err := cmd.Run(); err != nil {
		cleanupVeth(vethHost)
		releaseIP(containerID)
		return "", "", "", fmt.Errorf("failed to bring up host veth: %v", err)
	}

	// Move peer end into the container's network namespace
	if !quiet {
		fmt.Fprintf(os.Stderr, "  - Moving %s into container namespace (IP: %s)\n", vethPeer, containerIP)
	}
	netnsPath := fmt.Sprintf("/proc/%d/ns/net", childPid)
	cmd = exec.Command("ip", "link", "set", vethPeer, "netns", netnsPath)
	if err := cmd.Run(); err != nil {
		cleanupVeth(vethHost)
		releaseIP(containerID)
		return "", "", "", fmt.Errorf("failed to move veth into container namespace: %v", err)
	}

	if !quiet {
		fmt.Fprintln(os.Stderr, "  - Network setup complete")
	}
	return vethHost, vethPeer, containerIP, nil
}

// cleanupVeth removes a veth interface
func cleanupVeth(vethHost string) {
	if vethHost == "" {
		return
	}
	exec.Command("ip", "link", "delete", vethHost).Run()
}

// cleanupContainerNetwork cleans up networking for a container
func cleanupContainerNetwork(containerID, vethHost string) {
	cleanupVeth(vethHost)
	releaseIP(containerID)
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

// ============================================================================
// Per-container Cgroups
// ============================================================================

// createContainerCgroup creates a per-container cgroup
func createContainerCgroup(containerID string) (string, error) {
	cgroupPath := fmt.Sprintf("/sys/fs/cgroup/gocker/%s", containerID)

	// Ensure parent directory exists
	if err := os.MkdirAll("/sys/fs/cgroup/gocker", 0755); err != nil {
		return "", fmt.Errorf("failed to create parent cgroup directory: %v", err)
	}

	// Enable controllers on parent
	if err := enableCgroupControllers("/sys/fs/cgroup/gocker"); err != nil {
		// Non-fatal, controllers might already be enabled or not available
		fmt.Fprintf(os.Stderr, "  - Note: Could not enable cgroup controllers: %v\n", err)
	}

	// Create container-specific cgroup
	if err := os.MkdirAll(cgroupPath, 0755); err != nil {
		return "", fmt.Errorf("failed to create container cgroup directory: %v", err)
	}

	return cgroupPath, nil
}

// enableCgroupControllers enables cpu, memory, pids controllers on a cgroup
func enableCgroupControllers(cgroupPath string) error {
	controllersFile := filepath.Join(cgroupPath, "cgroup.subtree_control")
	return os.WriteFile(controllersFile, []byte("+cpu +memory +pids"), 0644)
}

// setupContainerCgroup configures cgroup limits for a container
func setupContainerCgroup(cgroupPath string, cpuLimit, memoryLimit string) error {
	// Set maximum processes limit to 20
	pidsMaxPath := filepath.Join(cgroupPath, "pids.max")
	if err := os.WriteFile(pidsMaxPath, []byte("20"), 0644); err != nil {
		return fmt.Errorf("failed to set pids.max: %v", err)
	}
	fmt.Fprintln(os.Stderr, "  - Process limit set to 20")

	// Set CPU limit if specified
	if cpuLimit != "" && cpuLimit != "max" {
		cpuMax, err := parseCPULimit(cpuLimit)
		if err != nil {
			return fmt.Errorf("failed to parse CPU limit: %v", err)
		}

		cpuMaxPath := filepath.Join(cgroupPath, "cpu.max")
		if err := os.WriteFile(cpuMaxPath, []byte(cpuMax), 0644); err != nil {
			return fmt.Errorf("failed to set cpu.max: %v", err)
		}
		fmt.Fprintf(os.Stderr, "  - CPU limit: %s\n", cpuLimit)
	}

	// Set memory limit if specified
	if memoryLimit != "" && memoryLimit != "max" {
		memoryMax, err := parseMemoryLimit(memoryLimit)
		if err != nil {
			return fmt.Errorf("failed to parse memory limit: %v", err)
		}

		memoryMaxPath := filepath.Join(cgroupPath, "memory.max")
		if err := os.WriteFile(memoryMaxPath, []byte(memoryMax), 0644); err != nil {
			return fmt.Errorf("failed to set memory.max: %v", err)
		}
		fmt.Fprintf(os.Stderr, "  - Memory limit: %s\n", memoryLimit)
	}

	return nil
}

// addToCgroup adds a PID to a cgroup
func addToCgroup(cgroupPath string, pid int) error {
	cgroupProcsPath := filepath.Join(cgroupPath, "cgroup.procs")
	return os.WriteFile(cgroupProcsPath, []byte(strconv.Itoa(pid)), 0644)
}

// cleanupContainerCgroup removes a container's cgroup
func cleanupContainerCgroup(cgroupPath string) error {
	if cgroupPath == "" {
		return nil
	}

	// Try to remove the cgroup directory
	// This will only succeed if there are no processes in it
	err := os.Remove(cgroupPath)
	if err != nil && !os.IsNotExist(err) {
		// Non-fatal, cgroup might still have processes
		return nil
	}
	return nil
}

// parseCPULimit parses CPU limit string and returns the cgroup v2 cpu.max format
func parseCPULimit(cpuLimit string) (string, error) {
	if cpuLimit == "" || cpuLimit == "max" {
		return "max 100000", nil
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

// ============================================================================
// Main run/child logic
// ============================================================================

func run() {
	// Parse flags for resource limits, volumes, and detached mode
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

	// Resolve rootfs path
	resolvedRootfs, err := resolveRootfsPath(rootfsPath)
	if err != nil {
		must(err)
	}

	// Generate container ID
	containerID := generateContainerID()

	// Create per-container cgroup
	cgroupPath, err := createContainerCgroup(containerID)
	if err != nil {
		must(fmt.Errorf("failed to create cgroup: %v", err))
	}

	// Configure cgroup limits
	fmt.Fprintln(os.Stderr, "Setting up cgroups v2 for resource limits...")
	if err := setupContainerCgroup(cgroupPath, cpuLimit, memoryLimit); err != nil {
		cleanupContainerCgroup(cgroupPath)
		must(err)
	}

	// Set environment variables to pass to child process
	os.Setenv("GOCKER_CONTAINER_ID", containerID)
	os.Setenv("GOCKER_ROOTFS", resolvedRootfs)
	os.Setenv("GOCKER_CGROUP_PATH", cgroupPath)
	if len(volumes) > 0 {
		os.Setenv("GOCKER_VOLUMES", strings.Join(volumes, "|"))
	}

	// Create log file for container
	logFile := filepath.Join(stateDir, "logs", containerID+".log")
	if err := os.MkdirAll(filepath.Dir(logFile), 0755); err != nil {
		cleanupContainerCgroup(cgroupPath)
		must(fmt.Errorf("failed to create logs directory: %v", err))
	}

	logWriter, err := os.Create(logFile)
	if err != nil {
		cleanupContainerCgroup(cgroupPath)
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

	// Set up I/O
	if detached {
		cmd.Stdin = nil
		cmd.Stdout = io.MultiWriter(logWriter, os.Stdout)
		cmd.Stderr = io.MultiWriter(logWriter, os.Stderr)
	} else {
		cmd.Stdin = os.Stdin
		cmd.Stdout = io.MultiWriter(logWriter, os.Stdout)
		cmd.Stderr = io.MultiWriter(logWriter, os.Stderr)
	}

	// Set up namespace cloneflags
	// When running as root, skip user namespace (not needed and complicates chroot)
	// User namespaces are primarily useful for unprivileged/rootless containers
	cloneFlags := syscall.CLONE_NEWUTS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNS | syscall.CLONE_NEWNET

	if os.Geteuid() == 0 {
		// Running as root - no user namespace needed
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Cloneflags: uintptr(cloneFlags),
		}
		fmt.Fprintln(os.Stderr, "  - Running as root (no user namespace needed)")
	} else {
		// Running unprivileged - use user namespace with mapping
		cloneFlags |= syscall.CLONE_NEWUSER
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Cloneflags: uintptr(cloneFlags),
			UidMappings: []syscall.SysProcIDMap{
				{ContainerID: 0, HostID: os.Getuid(), Size: 1},
			},
			GidMappings: []syscall.SysProcIDMap{
				{ContainerID: 0, HostID: os.Getgid(), Size: 1},
			},
		}
		fmt.Fprintf(os.Stderr, "  - User namespace: mapping container UID 0 -> host UID %d\n", os.Getuid())
	}

	// Start the command
	if err := cmd.Start(); err != nil {
		cleanupContainerCgroup(cgroupPath)
		must(err)
	}

	childPid := cmd.Process.Pid

	// Add child to cgroup
	if err := addToCgroup(cgroupPath, childPid); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to add process to cgroup: %v\n", err)
	}

	// Set up parent output
	var parentOutput io.Writer
	if detached {
		parentOutput = io.MultiWriter(logWriter, os.Stderr)
	} else {
		parentOutput = logWriter
	}

	fmt.Fprintf(parentOutput, "  - Child PID: %d\n", childPid)

	// Ensure bridge exists
	if err := ensureBridge(); err != nil {
		fmt.Fprintf(parentOutput, "Warning: Failed to set up bridge: %v\n", err)
	}

	// Set up network namespace for the container
	if !detached {
		fmt.Fprintln(logWriter, "Setting up network namespace...")
	} else {
		fmt.Fprintln(os.Stderr, "Setting up network namespace...")
	}

	vethHost, vethPeer, containerIP, err := setupContainerNetwork(containerID, childPid, !detached)
	if err != nil {
		if detached {
			fmt.Fprintf(os.Stderr, "Warning: Failed to set up network: %v\n", err)
		} else {
			fmt.Fprintf(logWriter, "Warning: Failed to set up network: %v\n", err)
		}
	}

	// Save container state (child reads IP from state file)
	state := &ContainerState{
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
	if err := saveContainerState(state); err != nil {
		fmt.Fprintf(parentOutput, "Warning: Failed to save container state: %v\n", err)
	}

	if detached {
		fmt.Printf("Container started with ID: %s\n", containerID)
		fmt.Printf("Use 'gocker logs %s' to view logs\n", containerID)
		return
	}

	// Set up signal handling for cleanup on Ctrl-C
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Cleanup function
	cleanup := func() {
		updateContainerStatus(containerID, "exited")
		cleanupContainerNetwork(containerID, vethHost)
		cleanupContainerCgroup(cgroupPath)
	}

	// Handle signals in a goroutine
	done := make(chan bool, 1)
	go func() {
		select {
		case <-sigChan:
			fmt.Fprintf(os.Stderr, "\nReceived interrupt, cleaning up...\n")
			// Kill the child process
			cmd.Process.Signal(syscall.SIGTERM)
			time.Sleep(500 * time.Millisecond)
			cmd.Process.Kill()
			cleanup()
			os.Exit(130)
		case <-done:
			return
		}
	}()

	// Wait for the command to finish
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

	// Get rootfs path from environment
	rootfsPath := os.Getenv("GOCKER_ROOTFS")
	if rootfsPath == "" {
		rootfsPath = "./rootfs"
	}

	// Configure network inside the container namespace
	fmt.Fprintln(os.Stderr, "Configuring container network...")
	if err := configureContainerNetwork(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to configure container network: %v\n", err)
	}

	// Mount volumes before chroot
	volumesStr := os.Getenv("GOCKER_VOLUMES")
	if volumesStr != "" {
		fmt.Fprintln(os.Stderr, "Mounting volumes...")
		if err := mountVolumes(volumesStr, rootfsPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to mount volumes: %v\n", err)
		}
	}

	// Set hostname for the container
	fmt.Fprintln(os.Stderr, "Setting hostname to 'gocker-container'...")
	must(syscall.Sethostname([]byte("gocker-container")))

	// Create filesystem jail using chroot
	fmt.Fprintf(os.Stderr, "Creating filesystem jail with chroot (%s)...\n", rootfsPath)
	must(syscall.Chroot(rootfsPath))

	// Change to root directory after chroot
	must(os.Chdir("/"))

	// Mount proc filesystem
	fmt.Fprintln(os.Stderr, "Mounting proc filesystem...")
	must(syscall.Mount("proc", "proc", "proc", 0, ""))
	defer syscall.Unmount("proc", 0)

	// Get the command to execute
	command := "/bin/sh"
	args := []string{}
	if len(os.Args) > 2 {
		command = os.Args[2]
		if len(os.Args) > 3 {
			args = os.Args[3:]
		}
	}

	// Set PATH environment variable for the container
	os.Setenv("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")

	// Execute the user's command
	fmt.Fprintf(os.Stderr, "Executing command: %s %v\n", command, args)
	cmd := exec.Command(command, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	// For interactive shells, ensure we have a TTY
	if command == "/bin/sh" && len(args) == 0 {
		cmd.Args = []string{command, "-i"}
	}

	must(cmd.Run())
}

// configureContainerNetwork sets up the network interface inside the container
// It waits for the parent to set up the veth and reads the IP from the state file
func configureContainerNetwork() error {
	containerID := os.Getenv("GOCKER_CONTAINER_ID")
	if containerID == "" {
		return fmt.Errorf("GOCKER_CONTAINER_ID not set")
	}

	ipCmd := "/usr/bin/ip"
	if _, err := os.Stat(ipCmd); os.IsNotExist(err) {
		ipCmd = "/sbin/ip"
		if _, err := os.Stat(ipCmd); os.IsNotExist(err) {
			ipCmd = "ip"
		}
	}

	// Bring up loopback first
	cmd := exec.Command(ipCmd, "link", "set", "lo", "up")
	cmd.Run() // Ignore error

	// Wait for veth interface to appear (parent moves it after we start)
	var foundVeth string
	for i := 0; i < 50; i++ { // Wait up to 5 seconds
		cmd := exec.Command(ipCmd, "link", "show", "type", "veth")
		output, err := cmd.Output()
		if err == nil {
			lines := strings.Split(string(output), "\n")
			for _, line := range lines {
				if strings.Contains(line, "veth") {
					parts := strings.Fields(line)
					if len(parts) >= 2 {
						name := strings.TrimSuffix(parts[1], ":")
						// Strip @ifN suffix (e.g., "vethc123@if5" -> "vethc123")
						if idx := strings.Index(name, "@"); idx != -1 {
							name = name[:idx]
						}
						if strings.HasPrefix(name, "veth") {
							foundVeth = name
							break
						}
					}
				}
			}
		}
		if foundVeth != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if foundVeth == "" {
		return fmt.Errorf("no veth interface found after waiting")
	}

	fmt.Fprintf(os.Stderr, "  - Found container veth interface: %s\n", foundVeth)

	// Wait for state file to have our IP (parent writes it after network setup)
	var containerIP string
	stateFile := filepath.Join(containersDir, containerID+".json")
	for i := 0; i < 50; i++ { // Wait up to 5 seconds
		data, err := os.ReadFile(stateFile)
		if err == nil {
			var state ContainerState
			if json.Unmarshal(data, &state) == nil && state.ContainerIP != "" {
				containerIP = state.ContainerIP
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	if containerIP == "" {
		return fmt.Errorf("container IP not found in state file")
	}

	// Bring up the interface
	cmd = exec.Command(ipCmd, "link", "set", foundVeth, "up")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to bring up container veth: %v", err)
	}

	// Assign IP address to container interface
	containerCIDR := containerIP + "/24"
	cmd = exec.Command(ipCmd, "addr", "add", containerCIDR, "dev", foundVeth)
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "  - Note: IP assignment: %v\n", err)
	}

	// Set up default route through the bridge
	cmd = exec.Command(ipCmd, "route", "add", "default", "via", bridgeIP, "dev", foundVeth)
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "  - Note: Route setup: %v\n", err)
	}

	fmt.Fprintf(os.Stderr, "  - Container IP: %s\n", containerIP)
	fmt.Fprintln(os.Stderr, "  - Network configuration complete")

	return nil
}

// mountVolumes mounts host directories into the container rootfs
func mountVolumes(volumesStr string, rootfsPath string) error {
	volumes := strings.Split(volumesStr, "|")

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

		if !filepath.IsAbs(containerPath) {
			return fmt.Errorf("container path must be absolute: %s", containerPath)
		}

		hostInfo, err := os.Stat(hostPath)
		if err != nil {
			return fmt.Errorf("host path does not exist: %s: %v", hostPath, err)
		}

		mountPoint := filepath.Join(rootfsPath, containerPath)

		if err := os.MkdirAll(filepath.Dir(mountPoint), 0755); err != nil {
			return fmt.Errorf("failed to create parent directories for mount point %s: %v", mountPoint, err)
		}

		if hostInfo.IsDir() {
			if err := os.MkdirAll(mountPoint, 0755); err != nil {
				return fmt.Errorf("failed to create mount point directory %s: %v", mountPoint, err)
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(mountPoint), 0755); err != nil {
				return fmt.Errorf("failed to create parent directory for file mount point %s: %v", mountPoint, err)
			}
			if _, err := os.Stat(mountPoint); os.IsNotExist(err) {
				if f, err := os.Create(mountPoint); err != nil {
					return fmt.Errorf("failed to create file mount point %s: %v", mountPoint, err)
				} else {
					f.Close()
				}
			}
		}

		flags := syscall.MS_BIND | syscall.MS_REC
		if err := syscall.Mount(hostPath, mountPoint, "", uintptr(flags), ""); err != nil {
			return fmt.Errorf("failed to bind mount %s to %s: %v", hostPath, mountPoint, err)
		}

		if err := syscall.Mount("", mountPoint, "", syscall.MS_PRIVATE|syscall.MS_REC, ""); err != nil {
			fmt.Fprintf(os.Stderr, "  - Warning: Failed to set mount propagation for %s: %v\n", mountPoint, err)
		}

		fmt.Fprintf(os.Stderr, "  - Mounted %s -> %s\n", hostPath, containerPath)
	}

	return nil
}

// ============================================================================
// Container lifecycle commands
// ============================================================================

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

	fmt.Printf("%-14s %-10s %-10s %-16s %-30s %s\n", "CONTAINER ID", "STATUS", "PID", "IP", "CREATED", "COMMAND")
	fmt.Println(strings.Repeat("-", 120))

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
				status = "exited"
				updateContainerStatus(containerID, "exited")
			}
		}

		command := strings.Join(state.Command, " ")
		if len(command) > 30 {
			command = command[:27] + "..."
		}

		displayID := containerID
		if len(displayID) > 12 {
			displayID = displayID[:12]
		}

		containerIP := state.ContainerIP
		if containerIP == "" {
			containerIP = "-"
		}

		created := state.CreatedAt.Format("2006-01-02 15:04:05")
		fmt.Printf("%-14s %-10s %-10d %-16s %-30s %s\n", displayID, status, state.PID, containerIP, created, command)
	}
}

func stopContainer(containerID string) {
	state, err := loadContainerState(containerID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	displayID := state.ID
	if len(displayID) > 12 {
		displayID = displayID[:12]
	}

	if state.Status != "running" {
		fmt.Printf("Container %s is not running (status: %s)\n", displayID, state.Status)
		return
	}

	// Check if process is still running
	if err := syscall.Kill(state.PID, 0); err != nil {
		fmt.Printf("Container %s is not running\n", displayID)
		updateContainerStatus(state.ID, "exited")
		cleanupContainerNetwork(state.ID, state.VethHost)
		cleanupContainerCgroup(state.CgroupPath)
		return
	}

	// Send SIGTERM to stop the container
	fmt.Printf("Stopping container %s (PID: %d)...\n", displayID, state.PID)
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

	// Cleanup
	cleanupContainerNetwork(state.ID, state.VethHost)
	cleanupContainerCgroup(state.CgroupPath)

	// Update status
	if err := updateContainerStatus(state.ID, "stopped"); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to update container status: %v\n", err)
	}

	fmt.Printf("Container %s stopped\n", displayID)
}

func removeContainer(containerID string) {
	state, err := loadContainerState(containerID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	displayID := state.ID
	if len(displayID) > 12 {
		displayID = displayID[:12]
	}

	// Check if container is running
	if state.Status == "running" {
		if err := syscall.Kill(state.PID, 0); err == nil {
			fmt.Fprintf(os.Stderr, "Error: Cannot remove running container %s. Stop it first with 'gocker stop %s'\n", displayID, displayID)
			os.Exit(1)
		}
	}

	// Cleanup network and cgroup (in case they weren't cleaned up on stop)
	cleanupContainerNetwork(state.ID, state.VethHost)
	cleanupContainerCgroup(state.CgroupPath)

	// Remove state file
	stateFile := filepath.Join(containersDir, state.ID+".json")
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

	fmt.Printf("Container %s removed\n", displayID)
}

func showLogs(containerID string) {
	state, err := loadContainerState(containerID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if state.LogFile == "" {
		displayID := state.ID
		if len(displayID) > 12 {
			displayID = displayID[:12]
		}
		fmt.Fprintf(os.Stderr, "Error: No log file found for container %s\n", displayID)
		os.Exit(1)
	}

	logFile, err := os.Open(state.LogFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening log file: %v\n", err)
		os.Exit(1)
	}
	defer logFile.Close()

	if _, err := io.Copy(os.Stdout, logFile); err != nil {
		fmt.Fprintf(os.Stderr, "Error reading log file: %v\n", err)
		os.Exit(1)
	}
}
