package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
	fmt.Fprintln(os.Stderr, "  - Network namespace (network isolation)")
	fmt.Fprintln(os.Stderr, "  - User namespace (user ID isolation)")

	cmd := exec.Command("/proc/self/exe", append([]string{"child"}, os.Args[2:]...)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUTS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNS | syscall.CLONE_NEWNET | syscall.CLONE_NEWUSER,
	}

	// Start the command (don't wait for it to finish yet)
	if err := cmd.Start(); err != nil {
		must(err)
	}

	// Get the child's PID (in parent namespace, before CLONE_NEWPID takes effect)
	childPid := cmd.Process.Pid

	// Set up user namespace mappings
	// Write UID/GID mappings to /proc/<pid>/uid_map and /proc/<pid>/gid_map
	// This must be done by the parent process (running as root) before the child
	// performs any privileged operations. The child will block on privileged
	// operations until the mapping is written.
	fmt.Fprintln(os.Stderr, "Setting up user namespace mappings...")
	if err := setupUserNamespace(childPid); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to set up user namespace: %v\n", err)
		// Continue even if user namespace setup fails
	}

	// Set up network namespace for the container
	fmt.Fprintln(os.Stderr, "Setting up network namespace...")
	vethHost, err := setupNetwork(childPid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to set up network: %v\n", err)
		// Continue even if network setup fails
	} else {
		// Ensure cleanup on exit
		defer cleanupNetwork(vethHost, "")
	}

	// Wait for the command to finish
	must(cmd.Wait())
}

// setupNetwork creates a veth pair and configures networking for the container
// Returns the host veth interface name for cleanup
func setupNetwork(childPid int) (string, error) {
	// Generate unique interface names based on child PID
	vethHost := fmt.Sprintf("veth%d", childPid)
	vethContainer := fmt.Sprintf("vethc%d", childPid)

	// Create veth pair
	fmt.Fprintf(os.Stderr, "  - Creating veth pair: %s <-> %s\n", vethHost, vethContainer)
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
	fmt.Fprintf(os.Stderr, "  - Moving %s into container namespace\n", vethContainer)
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
		fmt.Fprintf(os.Stderr, "  - Setting up NAT via %s for internet connectivity\n", defaultInterface)
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

	fmt.Fprintln(os.Stderr, "  - Network setup complete")
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
// This maps container UID 0 (root) to an unprivileged user on the host for security
// The mapping files can only be written once, so we need to write all mappings at once
func setupUserNamespace(childPid int) error {
	// Determine the host UID/GID to map container root (0) to
	// If running as root, map to UID 1000 (typical first user)
	// Otherwise, map to the current user
	hostUID := os.Getuid()
	hostGID := os.Getgid()
	
	if hostUID == 0 {
		// Running as root - map container root to unprivileged user (1000)
		// This provides security: container root is not host root
		hostUID = 1000
		hostGID = 1000
		fmt.Fprintf(os.Stderr, "  - Mapping container root (UID 0) to host UID %d (unprivileged)\n", hostUID)
	} else {
		fmt.Fprintf(os.Stderr, "  - Mapping container root (UID 0) to host UID %d\n", hostUID)
	}

	// Write UID mapping: container UID 0 -> host UID (hostUID)
	// Format: container_id host_id count
	// We map a range to allow for system users in the container
	// Map container UIDs 0-1000 to host UIDs hostUID to hostUID+1000
	uidMapPath := fmt.Sprintf("/proc/%d/uid_map", childPid)
	uidMap := fmt.Sprintf("0 %d 1001\n", hostUID)
	if err := os.WriteFile(uidMapPath, []byte(uidMap), 0644); err != nil {
		return fmt.Errorf("failed to write uid_map: %v", err)
	}

	// Write GID mapping: container GID 0 -> host GID (hostGID)
	// Map container GIDs 0-1000 to host GIDs hostGID to hostGID+1000
	gidMapPath := fmt.Sprintf("/proc/%d/gid_map", childPid)
	gidMap := fmt.Sprintf("0 %d 1001\n", hostGID)
	if err := os.WriteFile(gidMapPath, []byte(gidMap), 0644); err != nil {
		return fmt.Errorf("failed to write gid_map: %v", err)
	}

	fmt.Fprintf(os.Stderr, "  - User namespace mapping complete (container root -> host UID %d)\n", hostUID)
	fmt.Fprintf(os.Stderr, "  - Mapped container UIDs 0-1000 to host UIDs %d-%d\n", hostUID, hostUID+1000)
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
	
	// Verify user namespace mapping
	// In the container namespace, we should see ourselves as UID 0 (root)
	containerUID := os.Getuid()
	containerGID := os.Getgid()
	fmt.Fprintf(os.Stderr, "Container UID: %d, GID: %d (root in container namespace)\n", containerUID, containerGID)

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

	must(cmd.Run())
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

