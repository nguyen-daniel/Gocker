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

	// Skip root check for "child" (runs inside the container namespaces),
	// "reap" (detached supervisor spawned by a privileged run), and for
	// explicit rootless/unprivileged mode used by tests.
	if os.Args[1] != "child" && os.Args[1] != "reap" && os.Geteuid() != 0 && !allowUnprivileged() {
		fmt.Println("Error: This program must be run with sudo/root permissions")
		fmt.Println("Hint: set GOCKER_ALLOW_UNPRIVILEGED=1 or pass --rootless to exercise user namespaces without root")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		run()
	case "child":
		child()
	case "reap":
		if len(os.Args) < 3 {
			fmt.Println("Error: container ID required")
			os.Exit(1)
		}
		reapContainer(os.Args[2])
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
	fmt.Println("  --rootless                Allow unprivileged run (user namespace; network/cgroups may fail)")
}

// allowUnprivileged reports whether the CLI may run without euid 0.
// Used to test the user-namespace path in CI without a full rootful stack.
func allowUnprivileged() bool {
	if os.Getenv("GOCKER_ALLOW_UNPRIVILEGED") == "1" {
		return true
	}
	for _, arg := range os.Args[2:] {
		if arg == "--rootless" {
			return true
		}
	}
	return false
}

// namespaceSysProcAttr builds clone flags for the container child.
// Default (rootful) path: 4 namespaces — UTS, PID, mount, network.
// User namespace is optional: enabled when not root, or --rootless /
// GOCKER_ALLOW_UNPRIVILEGED=1, with uid/gid maps (container 0 -> host euid).
func namespaceSysProcAttr(includeUser bool) *syscall.SysProcAttr {
	flags := syscall.CLONE_NEWUTS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNS | syscall.CLONE_NEWNET
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

func hasCloneFlag(flags uintptr, flag uintptr) bool {
	return flags&flag == flag
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

// lockIPAMFile opens ipam.json and takes an exclusive flock, matching
// container state files so concurrent allocate/release cannot race.
func lockIPAMFile() (*os.File, error) {
	if err := ensureStateDir(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(ipamFile, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open IPAM file: %v", err)
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("failed to lock IPAM file: %v", err)
	}
	return f, nil
}

func emptyIPAM() *IPAMState {
	return &IPAMState{
		AllocatedIPs: make(map[string]string),
		NextIP:       2, // Start at 10.0.0.2
	}
}

func readIPAMFile(f *os.File) (*IPAMState, error) {
	if _, err := f.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("failed to seek IPAM file: %v", err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("failed to read IPAM file: %v", err)
	}
	if len(data) == 0 {
		return emptyIPAM(), nil
	}
	var state IPAMState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse IPAM state: %v", err)
	}
	if state.AllocatedIPs == nil {
		state.AllocatedIPs = make(map[string]string)
	}
	if state.NextIP < 2 {
		state.NextIP = 2
	}
	return &state, nil
}

func writeIPAMFile(f *os.File, state *IPAMState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal IPAM state: %v", err)
	}
	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("failed to truncate IPAM file: %v", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return fmt.Errorf("failed to seek IPAM file: %v", err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("failed to write IPAM file: %v", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("failed to sync IPAM file: %v", err)
	}
	return nil
}

// loadIPAM loads the IPAM state from disk (short lock for a consistent snapshot).
func loadIPAM() (*IPAMState, error) {
	f, err := lockIPAMFile()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	defer unlockFile(f)
	return readIPAMFile(f)
}

// saveIPAM saves the IPAM state to disk under flock.
func saveIPAM(state *IPAMState) error {
	f, err := lockIPAMFile()
	if err != nil {
		return err
	}
	defer f.Close()
	defer unlockFile(f)
	if state.AllocatedIPs == nil {
		state.AllocatedIPs = make(map[string]string)
	}
	return writeIPAMFile(f, state)
}

func ipAllocated(ipam *IPAMState, ip string) bool {
	for _, allocatedIP := range ipam.AllocatedIPs {
		if allocatedIP == ip {
			return true
		}
	}
	return false
}

// findFreeIP picks the next unused address in 10.0.0.2–.254.
// Walks from NextIP through 254, then scans 2–254 for holes after wrap.
func findFreeIP(ipam *IPAMState) (ip string, octet int, ok bool) {
	start := ipam.NextIP
	if start < 2 {
		start = 2
	}
	for octet = start; octet <= 254; octet++ {
		ip = fmt.Sprintf("10.0.0.%d", octet)
		if !ipAllocated(ipam, ip) {
			return ip, octet, true
		}
	}
	for octet = 2; octet <= 254; octet++ {
		ip = fmt.Sprintf("10.0.0.%d", octet)
		if !ipAllocated(ipam, ip) {
			return ip, octet, true
		}
	}
	return "", 0, false
}

// allocateIP allocates an IP address for a container
func allocateIP(containerID string) (string, error) {
	f, err := lockIPAMFile()
	if err != nil {
		return "", err
	}
	defer f.Close()
	defer unlockFile(f)

	ipam, err := readIPAMFile(f)
	if err != nil {
		return "", err
	}

	if ip, exists := ipam.AllocatedIPs[containerID]; exists {
		return ip, nil
	}

	ip, octet, ok := findFreeIP(ipam)
	if !ok {
		return "", fmt.Errorf("no available IP addresses in pool")
	}
	ipam.AllocatedIPs[containerID] = ip
	ipam.NextIP = octet + 1
	if err := writeIPAMFile(f, ipam); err != nil {
		return "", err
	}
	return ip, nil
}

// releaseIP releases an IP address for a container
func releaseIP(containerID string) error {
	f, err := lockIPAMFile()
	if err != nil {
		return err
	}
	defer f.Close()
	defer unlockFile(f)

	ipam, err := readIPAMFile(f)
	if err != nil {
		return err
	}

	delete(ipam.AllocatedIPs, containerID)
	if err := writeIPAMFile(f, ipam); err != nil {
		return err
	}
	if len(ipam.AllocatedIPs) == 0 {
		teardownNATRules()
	}
	return nil
}

// ============================================================================
// Bridge and Network Setup
// ============================================================================

// ensureBridge ensures the gocker0 bridge exists and is configured
func ensureBridge() error {
	// Check if bridge already exists
	if _, err := net.InterfaceByName(bridgeName); err == nil {
		// Bridge exists, verify it's up and restore NAT if we tore it down
		// when the last container exited.
		cmd := exec.Command("ip", "link", "set", bridgeName, "up")
		cmd.Run() // Ignore error, bridge might already be up
		if err := setupNATRules(); err != nil {
			fmt.Fprintf(os.Stderr, "  - Warning: Failed to set up NAT: %v\n", err)
		}
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

// teardownNATRules deletes the MASQUERADE/FORWARD rules installed by setupNATRules.
// Idempotent: missing rules are ignored. Called when the last allocated IP is released.
func teardownNATRules() {
	defaultInterface, err := getDefaultInterface()
	if err != nil {
		return
	}
	exec.Command("iptables", "-t", "nat", "-D", "POSTROUTING", "-s", containerNet, "-o", defaultInterface, "-j", "MASQUERADE").Run()
	exec.Command("iptables", "-D", "FORWARD", "-i", bridgeName, "-o", defaultInterface, "-j", "ACCEPT").Run()
	exec.Command("iptables", "-D", "FORWARD", "-i", defaultInterface, "-o", bridgeName, "-j", "ACCEPT").Run()
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
// OverlayFS + pivot_root
// ============================================================================

func overlayBaseDir(containerID string) string {
	return filepath.Join(containersDir, containerID)
}

func createOverlayDirs(containerID string) error {
	base := overlayBaseDir(containerID)
	for _, name := range []string{"upper", "work", "merged"} {
		if err := os.MkdirAll(filepath.Join(base, name), 0755); err != nil {
			return fmt.Errorf("mkdir overlay %s: %v", name, err)
		}
	}
	return nil
}

func cleanupOverlayDirs(containerID string) {
	if containerID == "" {
		return
	}
	base := overlayBaseDir(containerID)
	_ = syscall.Unmount(filepath.Join(base, "merged"), syscall.MNT_DETACH)
	_ = os.RemoveAll(base)
}

func mountOverlay(lower, overlayBase string) (merged string, err error) {
	upper := filepath.Join(overlayBase, "upper")
	work := filepath.Join(overlayBase, "work")
	merged = filepath.Join(overlayBase, "merged")
	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", lower, upper, work)
	if err := syscall.Mount("overlay", merged, "overlay", 0, opts); err != nil {
		return "", fmt.Errorf("overlay mount: %v", err)
	}
	return merged, nil
}

func pivotRoot(newRoot string) error {
	putOld := filepath.Join(newRoot, ".pivot_old")
	if err := os.MkdirAll(putOld, 0700); err != nil {
		return fmt.Errorf("mkdir put_old: %v", err)
	}
	if err := syscall.PivotRoot(newRoot, putOld); err != nil {
		return fmt.Errorf("pivot_root: %v", err)
	}
	if err := os.Chdir("/"); err != nil {
		return fmt.Errorf("chdir /: %v", err)
	}
	if err := syscall.Unmount("/.pivot_old", syscall.MNT_DETACH); err != nil {
		return fmt.Errorf("unmount old root: %v", err)
	}
	if err := os.Remove("/.pivot_old"); err != nil {
		fmt.Fprintf(os.Stderr, "  - Note: rmdir /.pivot_old: %v\n", err)
	}
	return nil
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

	// Resolve rootfs path
	resolvedRootfs, err := resolveRootfsPath(rootfsPath)
	if err != nil {
		must(err)
	}

	// Generate container ID
	containerID := generateContainerID()

	if err := createOverlayDirs(containerID); err != nil {
		must(fmt.Errorf("failed to create overlay dirs: %v", err))
	}

	// Create per-container cgroup
	cgroupPath, err := createContainerCgroup(containerID)
	if err != nil {
		cleanupOverlayDirs(containerID)
		must(fmt.Errorf("failed to create cgroup: %v", err))
	}

	// Configure cgroup limits
	fmt.Fprintln(os.Stderr, "Setting up cgroups v2 for resource limits...")
	if err := setupContainerCgroup(cgroupPath, cpuLimit, memoryLimit); err != nil {
		cleanupContainerCgroup(cgroupPath)
		cleanupOverlayDirs(containerID)
		must(err)
	}

	// Set environment variables to pass to child process
	os.Setenv("GOCKER_CONTAINER_ID", containerID)
	os.Setenv("GOCKER_ROOTFS", resolvedRootfs)
	os.Setenv("GOCKER_OVERLAY_DIR", overlayBaseDir(containerID))
	os.Setenv("GOCKER_CGROUP_PATH", cgroupPath)
	if len(volumes) > 0 {
		os.Setenv("GOCKER_VOLUMES", strings.Join(volumes, "|"))
	}

	// Create log file for container
	logFile := filepath.Join(stateDir, "logs", containerID+".log")
	if err := os.MkdirAll(filepath.Dir(logFile), 0755); err != nil {
		cleanupContainerCgroup(cgroupPath)
		cleanupOverlayDirs(containerID)
		must(fmt.Errorf("failed to create logs directory: %v", err))
	}

	logWriter, err := os.Create(logFile)
	if err != nil {
		cleanupContainerCgroup(cgroupPath)
		cleanupOverlayDirs(containerID)
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

	cmd.SysProcAttr = namespaceSysProcAttr(includeUser)
	if includeUser {
		fmt.Fprintf(os.Stderr, "  - User namespace: mapping container UID 0 -> host UID %d\n", os.Geteuid())
	} else {
		fmt.Fprintln(os.Stderr, "  - Running as root (user namespace not enabled; 4 namespaces)")
	}

	// Start the command
	if err := cmd.Start(); err != nil {
		cleanupContainerCgroup(cgroupPath)
		cleanupOverlayDirs(containerID)
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
		if err := startDetachedReaper(containerID); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to start detached reaper: %v\n", err)
		}
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

	// Get rootfs path (shared OverlayFS lower) from environment
	rootfsPath := os.Getenv("GOCKER_ROOTFS")
	if rootfsPath == "" {
		rootfsPath = "./rootfs"
	}
	overlayBase := os.Getenv("GOCKER_OVERLAY_DIR")
	if overlayBase == "" {
		must(fmt.Errorf("GOCKER_OVERLAY_DIR not set"))
	}

	// Configure network inside the container namespace (host tools, before jail)
	fmt.Fprintln(os.Stderr, "Configuring container network...")
	if err := configureContainerNetwork(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to configure container network: %v\n", err)
	}

	// Stop mount propagation so the overlay does not leak onto the host.
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		fmt.Fprintf(os.Stderr, "  - Note: MS_PRIVATE on /: %v\n", err)
	}

	fmt.Fprintf(os.Stderr, "Mounting OverlayFS (lower=%s, dirs=%s)...\n", rootfsPath, overlayBase)
	merged, err := mountOverlay(rootfsPath, overlayBase)
	must(err)

	// Bind-mount volumes onto the overlay (not the shared lower).
	volumesStr := os.Getenv("GOCKER_VOLUMES")
	if volumesStr != "" {
		fmt.Fprintln(os.Stderr, "Mounting volumes...")
		if err := mountVolumes(volumesStr, merged); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to mount volumes: %v\n", err)
		}
	}

	// Set hostname for the container
	fmt.Fprintln(os.Stderr, "Setting hostname to 'gocker-container'...")
	must(syscall.Sethostname([]byte("gocker-container")))

	// Alpine docker-export rootfs has no /dev/null or /dev/zero. Create them
	// on the overlay so mknod writes go to this container's upper dir.
	if err := ensureJailDevices(merged); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to set up jail device nodes: %v\n", err)
	}

	fmt.Fprintf(os.Stderr, "Entering OverlayFS jail with pivot_root (%s)...\n", merged)
	must(pivotRoot(merged))

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

// linuxMkdev encodes a device number for mknod(2) (new_encode_dev).
func linuxMkdev(major, minor uint32) int {
	return int((minor & 0xff) | (major << 8) | ((minor &^ 0xff) << 12))
}

func isCharDevice(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// ensureJailDevices creates /dev/null and /dev/zero on the OverlayFS merged
// view before pivot_root. Prefer mknod (writes land in this container's upper
// dir); bind-mount host devices if mknod is blocked (nodev). The child's mount
// namespace is already MS_PRIVATE so a bind does not leak onto the host.
func ensureJailDevices(rootfsPath string) error {
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		// Non-fatal: some environments reject remounting /.
		fmt.Fprintf(os.Stderr, "  - Note: MS_PRIVATE on /: %v\n", err)
	}

	devDir := filepath.Join(rootfsPath, "dev")
	if err := os.MkdirAll(devDir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %v", devDir, err)
	}

	nodes := []struct {
		name         string
		major, minor uint32
	}{
		{"null", 1, 3},
		{"zero", 1, 5},
	}
	for _, n := range nodes {
		dest := filepath.Join(devDir, n.name)
		if isCharDevice(dest) {
			continue
		}
		_ = os.Remove(dest)
		if err := syscall.Mknod(dest, syscall.S_IFCHR|0666, linuxMkdev(n.major, n.minor)); err != nil {
			f, createErr := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY, 0666)
			if createErr != nil && !os.IsExist(createErr) {
				return fmt.Errorf("%s: mknod: %v; create: %v", dest, err, createErr)
			}
			if f != nil {
				f.Close()
			}
			host := filepath.Join("/dev", n.name)
			if bindErr := syscall.Mount(host, dest, "", syscall.MS_BIND, ""); bindErr != nil {
				return fmt.Errorf("%s: mknod (%v) and bind-mount (%v) failed", dest, err, bindErr)
			}
			continue
		}
		_ = os.Chmod(dest, 0666)
	}
	return nil
}

// startDetachedReaper launches a session-leader helper that waits for the
// container PID to exit, then updates status and releases veth/cgroup/IP.
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

// reapContainer polls until the container process is gone, then cleans up.
func reapContainer(containerID string) {
	state, err := loadContainerState(containerID)
	if err != nil {
		return
	}
	pid := state.PID
	for {
		if err := syscall.Kill(pid, 0); err != nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	state, err = loadContainerState(containerID)
	if err != nil {
		return
	}
	if state.Status == "running" {
		_ = updateContainerStatus(state.ID, "exited")
	}
	cleanupContainerNetwork(state.ID, state.VethHost)
	cleanupContainerCgroup(state.CgroupPath)
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

		// Check if process is still running; if not, reap leaks (veth/IP/cgroup).
		status := state.Status
		if status == "running" {
			if err := syscall.Kill(state.PID, 0); err != nil {
				status = "exited"
				updateContainerStatus(containerID, "exited")
				cleanupContainerNetwork(containerID, state.VethHost)
				cleanupContainerCgroup(state.CgroupPath)
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

	// Cleanup network, cgroup, and OverlayFS dirs (in case they weren't cleaned up on stop)
	cleanupContainerNetwork(state.ID, state.VethHost)
	cleanupContainerCgroup(state.CgroupPath)
	cleanupOverlayDirs(state.ID)

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
