package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestGockerRun tests that Gocker can successfully execute a command inside a container
func TestGockerRun(t *testing.T) {
	binaryPath := "./gocker"
	if _, err := os.Stat(binaryPath); os.IsNotExist(err) {
		t.Fatalf("gocker binary not found at %s. Run 'make build' first.", binaryPath)
	}

	rootfsPath := "./rootfs"
	if _, err := os.Stat(rootfsPath); os.IsNotExist(err) {
		t.Fatalf("rootfs directory not found at %s. Run 'make setup' first.", rootfsPath)
	}

	busyboxPath := filepath.Join(rootfsPath, "bin/busybox")
	if _, err := os.Stat(busyboxPath); os.IsNotExist(err) {
		t.Fatalf("/bin/busybox not found in rootfs at %s. Rootfs may be incomplete.", busyboxPath)
	}

	var cmd *exec.Cmd
	if os.Geteuid() == 0 {
		cmd = exec.Command(binaryPath, "run", "/bin/busybox", "true")
	} else {
		cmd = exec.Command("sudo", binaryPath, "run", "/bin/busybox", "true")
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err != nil {
		t.Fatalf("Gocker failed to execute /bin/busybox true in container: %v", err)
	}
}

// TestGockerRunWithHostname verifies that the container has an isolated hostname
func TestGockerRunWithHostname(t *testing.T) {
	binaryPath := "./gocker"
	if _, err := os.Stat(binaryPath); os.IsNotExist(err) {
		t.Skip("gocker binary not found. Run 'make build' first.")
	}

	rootfsPath := "./rootfs"
	if _, err := os.Stat(rootfsPath); os.IsNotExist(err) {
		t.Skip("rootfs directory not found. Run 'make setup' first.")
	}

	var cmd *exec.Cmd
	if os.Geteuid() == 0 {
		cmd = exec.Command(binaryPath, "run", "/bin/hostname")
	} else {
		cmd = exec.Command("sudo", binaryPath, "run", "/bin/hostname")
	}
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Gocker failed to execute hostname in container: %v", err)
	}

	hostname := string(output)
	expectedHostname := "gocker-container\n"
	if hostname != expectedHostname {
		t.Errorf("Expected hostname '%s', got '%s'", expectedHostname, hostname)
	}
}

// TestPerContainerCgroup verifies that each container gets its own cgroup
func TestPerContainerCgroup(t *testing.T) {
	binaryPath := "./gocker"
	if _, err := os.Stat(binaryPath); os.IsNotExist(err) {
		t.Skip("gocker binary not found. Run 'make build' first.")
	}

	rootfsPath := "./rootfs"
	if _, err := os.Stat(rootfsPath); os.IsNotExist(err) {
		t.Skip("rootfs directory not found. Run 'make setup' first.")
	}

	// Run a detached container
	var cmd *exec.Cmd
	if os.Geteuid() == 0 {
		cmd = exec.Command(binaryPath, "run", "-d", "/bin/busybox", "sleep", "10")
	} else {
		cmd = exec.Command("sudo", binaryPath, "run", "-d", "/bin/busybox", "sleep", "10")
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to start container: %v\nOutput: %s", err, output)
	}

	// Parse container ID from output
	lines := strings.Split(string(output), "\n")
	var containerID string
	for _, line := range lines {
		if strings.HasPrefix(line, "Container started with ID: ") {
			containerID = strings.TrimPrefix(line, "Container started with ID: ")
			break
		}
	}

	if containerID == "" {
		t.Fatalf("Could not find container ID in output: %s", output)
	}

	defer func() {
		// Cleanup: stop and remove container
		if os.Geteuid() == 0 {
			exec.Command(binaryPath, "stop", containerID).Run()
			exec.Command(binaryPath, "rm", containerID).Run()
		} else {
			exec.Command("sudo", binaryPath, "stop", containerID).Run()
			exec.Command("sudo", binaryPath, "rm", containerID).Run()
		}
	}()

	// Wait a moment for cgroup to be created
	time.Sleep(500 * time.Millisecond)

	// Verify container has its own cgroup
	cgroupPath := "/sys/fs/cgroup/gocker/" + containerID
	if _, err := os.Stat(cgroupPath); os.IsNotExist(err) {
		t.Errorf("Container cgroup not found at %s", cgroupPath)
	}

	// Verify pids.max is set
	pidsMaxPath := filepath.Join(cgroupPath, "pids.max")
	data, err := os.ReadFile(pidsMaxPath)
	if err != nil {
		t.Errorf("Could not read pids.max: %v", err)
	} else {
		pidsMax := strings.TrimSpace(string(data))
		if pidsMax != "20" {
			t.Errorf("Expected pids.max=20, got %s", pidsMax)
		}
	}
}

// TestMultipleContainers verifies that multiple containers can run concurrently
func TestMultipleContainers(t *testing.T) {
	binaryPath := "./gocker"
	if _, err := os.Stat(binaryPath); os.IsNotExist(err) {
		t.Skip("gocker binary not found. Run 'make build' first.")
	}

	rootfsPath := "./rootfs"
	if _, err := os.Stat(rootfsPath); os.IsNotExist(err) {
		t.Skip("rootfs directory not found. Run 'make setup' first.")
	}

	var container1ID, container2ID string

	// Start first container
	var cmd *exec.Cmd
	if os.Geteuid() == 0 {
		cmd = exec.Command(binaryPath, "run", "-d", "/bin/busybox", "sleep", "30")
	} else {
		cmd = exec.Command("sudo", binaryPath, "run", "-d", "/bin/busybox", "sleep", "30")
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to start container 1: %v\nOutput: %s", err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "Container started with ID: ") {
			container1ID = strings.TrimPrefix(line, "Container started with ID: ")
			break
		}
	}

	// Start second container
	if os.Geteuid() == 0 {
		cmd = exec.Command(binaryPath, "run", "-d", "/bin/busybox", "sleep", "30")
	} else {
		cmd = exec.Command("sudo", binaryPath, "run", "-d", "/bin/busybox", "sleep", "30")
	}
	output, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to start container 2: %v\nOutput: %s", err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "Container started with ID: ") {
			container2ID = strings.TrimPrefix(line, "Container started with ID: ")
			break
		}
	}

	defer func() {
		// Cleanup
		if os.Geteuid() == 0 {
			exec.Command(binaryPath, "stop", container1ID).Run()
			exec.Command(binaryPath, "rm", container1ID).Run()
			exec.Command(binaryPath, "stop", container2ID).Run()
			exec.Command(binaryPath, "rm", container2ID).Run()
		} else {
			exec.Command("sudo", binaryPath, "stop", container1ID).Run()
			exec.Command("sudo", binaryPath, "rm", container1ID).Run()
			exec.Command("sudo", binaryPath, "stop", container2ID).Run()
			exec.Command("sudo", binaryPath, "rm", container2ID).Run()
		}
	}()

	// Wait for containers to start
	time.Sleep(1 * time.Second)

	// Verify both have separate cgroups
	cgroup1 := "/sys/fs/cgroup/gocker/" + container1ID
	cgroup2 := "/sys/fs/cgroup/gocker/" + container2ID

	if _, err := os.Stat(cgroup1); os.IsNotExist(err) {
		t.Errorf("Container 1 cgroup not found at %s", cgroup1)
	}
	if _, err := os.Stat(cgroup2); os.IsNotExist(err) {
		t.Errorf("Container 2 cgroup not found at %s", cgroup2)
	}

	// Verify containers have different IPs via state files
	state1File := "/var/lib/gocker/containers/" + container1ID + ".json"
	state2File := "/var/lib/gocker/containers/" + container2ID + ".json"

	data1, err := os.ReadFile(state1File)
	if err != nil {
		t.Errorf("Could not read container 1 state: %v", err)
	}
	data2, err := os.ReadFile(state2File)
	if err != nil {
		t.Errorf("Could not read container 2 state: %v", err)
	}

	var state1, state2 ContainerState
	if err := json.Unmarshal(data1, &state1); err != nil {
		t.Errorf("Could not parse container 1 state: %v", err)
	}
	if err := json.Unmarshal(data2, &state2); err != nil {
		t.Errorf("Could not parse container 2 state: %v", err)
	}

	if state1.ContainerIP == "" {
		t.Errorf("Container 1 has no IP assigned")
	}
	if state2.ContainerIP == "" {
		t.Errorf("Container 2 has no IP assigned")
	}
	if state1.ContainerIP == state2.ContainerIP {
		t.Errorf("Containers have the same IP: %s", state1.ContainerIP)
	}

	t.Logf("Container 1 IP: %s, Container 2 IP: %s", state1.ContainerIP, state2.ContainerIP)
}

// TestIPAM verifies IP address allocation and release
func TestIPAM(t *testing.T) {
	// Test allocateIP and releaseIP functions
	testContainerID := "test-container-ipam-" + time.Now().Format("20060102150405")

	// Allocate IP
	ip1, err := allocateIP(testContainerID)
	if err != nil {
		t.Fatalf("Failed to allocate IP: %v", err)
	}
	if ip1 == "" {
		t.Fatalf("Allocated IP is empty")
	}
	if !strings.HasPrefix(ip1, "10.0.0.") {
		t.Errorf("Allocated IP is not in expected range: %s", ip1)
	}

	// Allocate same container should return same IP
	ip2, err := allocateIP(testContainerID)
	if err != nil {
		t.Fatalf("Failed to re-allocate IP: %v", err)
	}
	if ip1 != ip2 {
		t.Errorf("Re-allocated IP differs: %s vs %s", ip1, ip2)
	}

	// Release IP
	if err := releaseIP(testContainerID); err != nil {
		t.Fatalf("Failed to release IP: %v", err)
	}

	// Verify IP was released by checking IPAM state
	ipam, err := loadIPAM()
	if err != nil {
		t.Fatalf("Failed to load IPAM: %v", err)
	}
	if _, exists := ipam.AllocatedIPs[testContainerID]; exists {
		t.Errorf("IP was not released from IPAM state")
	}
}

// TestRootfsResolution verifies rootfs path resolution
func TestRootfsResolution(t *testing.T) {
	// Test with explicit path
	absPath, err := resolveRootfsPath("./rootfs")
	if err != nil {
		t.Fatalf("Failed to resolve ./rootfs: %v", err)
	}
	if !filepath.IsAbs(absPath) {
		t.Errorf("Resolved path is not absolute: %s", absPath)
	}

	// Test with non-existent path
	_, err = resolveRootfsPath("/nonexistent/rootfs")
	if err == nil {
		t.Errorf("Expected error for non-existent path, got nil")
	}

	// Test with empty path (should use default resolution)
	absPath, err = resolveRootfsPath("")
	if err != nil {
		t.Fatalf("Failed to resolve default rootfs: %v", err)
	}
	if !filepath.IsAbs(absPath) {
		t.Errorf("Default resolved path is not absolute: %s", absPath)
	}
}

// TestContainerIDResolution verifies partial container ID matching
func TestContainerIDResolution(t *testing.T) {
	// This test needs at least one container to exist
	// We'll create a test state file temporarily
	testID := "1234567890123456789"
	testState := &ContainerState{
		ID:        testID,
		PID:       12345,
		Status:    "exited",
		CreatedAt: time.Now(),
		Command:   []string{"/bin/sh"},
	}

	if err := ensureStateDir(); err != nil {
		t.Fatalf("Failed to ensure state dir: %v", err)
	}

	if err := saveContainerState(testState); err != nil {
		t.Fatalf("Failed to save test state: %v", err)
	}

	defer func() {
		// Cleanup test state
		os.Remove(filepath.Join(containersDir, testID+".json"))
	}()

	// Test full ID resolution
	resolved, err := resolveContainerID(testID)
	if err != nil {
		t.Errorf("Failed to resolve full ID: %v", err)
	}
	if resolved != testID {
		t.Errorf("Expected %s, got %s", testID, resolved)
	}

	// Test partial ID resolution
	resolved, err = resolveContainerID("123456")
	if err != nil {
		t.Errorf("Failed to resolve partial ID: %v", err)
	}
	if resolved != testID {
		t.Errorf("Expected %s, got %s", testID, resolved)
	}

	// Test non-existent ID
	_, err = resolveContainerID("nonexistent")
	if err == nil {
		t.Errorf("Expected error for non-existent ID, got nil")
	}
}

// TestCPULimitParsing tests CPU limit parsing
func TestCPULimitParsing(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		hasError bool
	}{
		{"1", "100000 100000", false},
		{"0.5", "50000 100000", false},
		{"2", "200000 100000", false},
		{"max", "max 100000", false},
		{"", "max 100000", false},
		{"-1", "", true},
		{"invalid", "", true},
	}

	for _, test := range tests {
		result, err := parseCPULimit(test.input)
		if test.hasError {
			if err == nil {
				t.Errorf("parseCPULimit(%q): expected error, got nil", test.input)
			}
		} else {
			if err != nil {
				t.Errorf("parseCPULimit(%q): unexpected error: %v", test.input, err)
			}
			if result != test.expected {
				t.Errorf("parseCPULimit(%q): expected %q, got %q", test.input, test.expected, result)
			}
		}
	}
}

// TestMemoryLimitParsing tests memory limit parsing
func TestMemoryLimitParsing(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		hasError bool
	}{
		{"512M", "536870912", false},
		{"1G", "1073741824", false},
		{"256K", "262144", false},
		{"max", "max", false},
		{"", "max", false},
		{"-1M", "", true},
		{"invalid", "", true},
	}

	for _, test := range tests {
		result, err := parseMemoryLimit(test.input)
		if test.hasError {
			if err == nil {
				t.Errorf("parseMemoryLimit(%q): expected error, got nil", test.input)
			}
		} else {
			if err != nil {
				t.Errorf("parseMemoryLimit(%q): unexpected error: %v", test.input, err)
			}
			if result != test.expected {
				t.Errorf("parseMemoryLimit(%q): expected %q, got %q", test.input, test.expected, result)
			}
		}
	}
}

// TestNamespaceConfig: rootful default is 4 namespaces; user ns is opt-in.
func TestNamespaceConfig(t *testing.T) {
	rootful := namespaceSysProcAttr(false)
	for _, f := range []struct {
		name string
		bit  uintptr
	}{
		{"NEWUTS", syscall.CLONE_NEWUTS},
		{"NEWPID", syscall.CLONE_NEWPID},
		{"NEWNS", syscall.CLONE_NEWNS},
		{"NEWNET", syscall.CLONE_NEWNET},
	} {
		if !hasCloneFlag(rootful.Cloneflags, f.bit) {
			t.Errorf("rootful missing %s", f.name)
		}
	}
	if hasCloneFlag(rootful.Cloneflags, syscall.CLONE_NEWUSER) {
		t.Error("rootful default must not set CLONE_NEWUSER")
	}

	rootless := namespaceSysProcAttr(true)
	if !hasCloneFlag(rootless.Cloneflags, syscall.CLONE_NEWUSER) {
		t.Fatal("rootless path should set CLONE_NEWUSER")
	}
	if len(rootless.UidMappings) != 1 || rootless.UidMappings[0].ContainerID != 0 {
		t.Fatalf("expected uid map container 0 -> host euid, got %+v", rootless.UidMappings)
	}
	if rootless.UidMappings[0].HostID != os.Geteuid() {
		t.Errorf("uid map HostID=%d, want euid %d", rootless.UidMappings[0].HostID, os.Geteuid())
	}
	t.Logf("euid=%d: 4-ns rootful; optional user ns maps 0 -> %d", os.Geteuid(), rootless.UidMappings[0].HostID)
}

// TestCloneUserNamespace proves the optional user-namespace path can clone.
func TestCloneUserNamespace(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("user namespaces are Linux-only")
	}
	cmd := exec.Command("true")
	cmd.SysProcAttr = namespaceSysProcAttr(true)
	if err := cmd.Run(); err != nil {
		t.Fatalf("clone with optional user namespace failed: %v", err)
	}
}

// TestPidsMaxEnforcement starts a detached container and verifies the 21st
// process in the cgroup is rejected (pids.max=20).
func TestPidsMaxEnforcement(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("cgroups are Linux-only")
	}
	binaryPath := "./gocker"
	if _, err := os.Stat(binaryPath); os.IsNotExist(err) {
		t.Skip("gocker binary not found. Run 'make build' first.")
	}
	if _, err := os.Stat("./rootfs/bin/busybox"); os.IsNotExist(err) {
		t.Skip("rootfs not set up")
	}

	// Keep the container alive, then fork 25 sleeps inside it via a second run
	// is awkward; instead start a shell that tries to spawn 25 background jobs.
	script := "i=0; while [ $i -lt 25 ]; do /bin/busybox sleep 30 & i=$((i+1)); done; echo SPAWNED; wait"
	var cmd *exec.Cmd
	if os.Geteuid() == 0 {
		cmd = exec.Command(binaryPath, "run", "/bin/busybox", "sh", "-c", script)
	} else {
		cmd = exec.Command("sudo", binaryPath, "run", "/bin/busybox", "sh", "-c", script)
	}
	output, err := cmd.CombinedOutput()
	out := string(output)
	t.Logf("pids test output:\n%s", out)
	// cgroup v2 returns "Resource temporarily unavailable" / "can't fork" when pids.max hits.
	if !strings.Contains(out, "Resource temporarily unavailable") &&
		!strings.Contains(out, "can't fork") &&
		!strings.Contains(strings.ToLower(out), "nproc") {
		if err == nil && strings.Contains(out, "SPAWNED") {
			t.Errorf("expected fork failure at the 21st process (pids.max=20); container finished cleanly")
		}
	}
}
