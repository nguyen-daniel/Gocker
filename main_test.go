package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func requireLinuxRuntime(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux namespaces/cgroups")
	}
	if _, err := os.Stat("./gocker"); os.IsNotExist(err) {
		t.Skip("gocker binary not found. Run 'make build' first.")
	}
	if _, err := os.Stat("./rootfs"); os.IsNotExist(err) {
		t.Skip("rootfs directory not found. Run 'make setup' first.")
	}
}

func gockerCommand(args ...string) *exec.Cmd {
	if os.Geteuid() == 0 {
		return exec.Command("./gocker", args...)
	}
	return exec.Command("sudo", append([]string{"./gocker"}, args...)...)
}

func parseStartedContainerID(output []byte) string {
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Container started with ID: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Container started with ID: "))
		}
	}
	return ""
}

func waitForPath(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", path)
}

func stopAndRemove(containerID string) {
	if containerID == "" {
		return
	}
	_ = gockerCommand("stop", containerID).Run()
	_ = gockerCommand("rm", containerID).Run()
}

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
	requireLinuxRuntime(t)

	cmd := gockerCommand("run", "-d", "/bin/busybox", "sleep", "10")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to start container: %v\nOutput: %s", err, output)
	}

	containerID := parseStartedContainerID(output)
	if containerID == "" {
		t.Fatalf("Could not find container ID in output: %s", output)
	}
	defer stopAndRemove(containerID)

	cgroupPath := "/sys/fs/cgroup/gocker/" + containerID
	if err := waitForPath(cgroupPath, 5*time.Second); err != nil {
		state, stateErr := loadContainerState(containerID)
		alive := ""
		if stateErr == nil {
			if killErr := syscall.Kill(state.PID, 0); killErr != nil {
				alive = fmt.Sprintf(" (pid %d not running: %v)", state.PID, killErr)
			} else {
				alive = fmt.Sprintf(" (pid %d still running)", state.PID)
			}
		}
		t.Fatalf("%v%s\nrun -d output:\n%s", err, alive, output)
	}

	if state, err := loadContainerState(containerID); err != nil {
		t.Errorf("Could not load container state: %v", err)
	} else if err := syscall.Kill(state.PID, 0); err != nil {
		t.Errorf("detached container pid %d died; cgroup would be reaped: %v", state.PID, err)
	}

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
	requireLinuxRuntime(t)

	cmd := gockerCommand("run", "-d", "/bin/busybox", "sleep", "30")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to start container 1: %v\nOutput: %s", err, output)
	}
	container1ID := parseStartedContainerID(output)
	if container1ID == "" {
		t.Fatalf("Could not find container 1 ID in output: %s", output)
	}

	cmd = gockerCommand("run", "-d", "/bin/busybox", "sleep", "30")
	output, err = cmd.CombinedOutput()
	if err != nil {
		stopAndRemove(container1ID)
		t.Fatalf("Failed to start container 2: %v\nOutput: %s", err, output)
	}
	container2ID := parseStartedContainerID(output)
	if container2ID == "" {
		stopAndRemove(container1ID)
		t.Fatalf("Could not find container 2 ID in output: %s", output)
	}
	defer stopAndRemove(container1ID)
	defer stopAndRemove(container2ID)

	cgroup1 := "/sys/fs/cgroup/gocker/" + container1ID
	cgroup2 := "/sys/fs/cgroup/gocker/" + container2ID
	if err := waitForPath(cgroup1, 5*time.Second); err != nil {
		t.Errorf("Container 1 cgroup: %v", err)
	}
	if err := waitForPath(cgroup2, 5*time.Second); err != nil {
		t.Errorf("Container 2 cgroup: %v", err)
	}

	state1File := "/var/lib/gocker/containers/" + container1ID + ".json"
	state2File := "/var/lib/gocker/containers/" + container2ID + ".json"
	if err := waitForPath(state1File, 5*time.Second); err != nil {
		t.Fatalf("Container 1 state: %v", err)
	}
	if err := waitForPath(state2File, 5*time.Second); err != nil {
		t.Fatalf("Container 2 state: %v", err)
	}

	data1, err := os.ReadFile(state1File)
	if err != nil {
		t.Fatalf("Could not read container 1 state: %v", err)
	}
	data2, err := os.ReadFile(state2File)
	if err != nil {
		t.Fatalf("Could not read container 2 state: %v", err)
	}

	var state1, state2 ContainerState
	if err := json.Unmarshal(data1, &state1); err != nil {
		t.Fatalf("Could not parse container 1 state: %v", err)
	}
	if err := json.Unmarshal(data2, &state2); err != nil {
		t.Fatalf("Could not parse container 2 state: %v", err)
	}

	if err := syscall.Kill(state1.PID, 0); err != nil {
		t.Errorf("container 1 pid %d died: %v", state1.PID, err)
	}
	if err := syscall.Kill(state2.PID, 0); err != nil {
		t.Errorf("container 2 pid %d died: %v", state2.PID, err)
	}

	if state1.ContainerIP == "" {
		t.Errorf("Container 1 has no IP assigned")
	}
	if state2.ContainerIP == "" {
		t.Errorf("Container 2 has no IP assigned")
	}
	if state1.ContainerIP != "" && state1.ContainerIP == state2.ContainerIP {
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

// TestFindFreeIP covers wrap-around reuse when NextIP is past 254.
func TestFindFreeIP(t *testing.T) {
	ipam := &IPAMState{
		AllocatedIPs: map[string]string{"a": "10.0.0.2"},
		NextIP:       2,
	}
	ip, octet, ok := findFreeIP(ipam)
	if !ok || ip != "10.0.0.3" || octet != 3 {
		t.Errorf("sequential: got ip=%s octet=%d ok=%v, want 10.0.0.3 / 3", ip, octet, ok)
	}

	ipam = &IPAMState{
		AllocatedIPs: map[string]string{"keep": "10.0.0.2"},
		NextIP:       255,
	}
	ip, octet, ok = findFreeIP(ipam)
	if !ok || ip != "10.0.0.3" || octet != 3 {
		t.Errorf("wrap scan: got ip=%s octet=%d ok=%v, want 10.0.0.3 / 3", ip, octet, ok)
	}

	full := make(map[string]string, 253)
	for i := 2; i <= 254; i++ {
		full[fmt.Sprintf("c%d", i)] = fmt.Sprintf("10.0.0.%d", i)
	}
	ipam = &IPAMState{AllocatedIPs: full, NextIP: 255}
	if _, _, ok = findFreeIP(ipam); ok {
		t.Error("expected no free IP when 2–254 are all allocated")
	}
}

// TestIPAMReuse allocates after NextIP is past 254 and must not reuse an in-use address.
func TestIPAMReuse(t *testing.T) {
	keepID := "test-ipam-reuse-keep-" + time.Now().Format("20060102150405.000")
	newID := "test-ipam-reuse-new-" + time.Now().Format("20060102150405.000")

	keepIP, err := allocateIP(keepID)
	if err != nil {
		t.Fatalf("allocate keep: %v", err)
	}
	defer releaseIP(keepID)

	holeID := "test-ipam-reuse-hole-" + time.Now().Format("20060102150405.000")
	holeIP, err := allocateIP(holeID)
	if err != nil {
		t.Fatalf("allocate hole: %v", err)
	}
	if err := releaseIP(holeID); err != nil {
		t.Fatalf("release hole: %v", err)
	}

	ipam, err := loadIPAM()
	if err != nil {
		t.Fatalf("load IPAM: %v", err)
	}
	ipam.NextIP = 255
	if err := saveIPAM(ipam); err != nil {
		t.Fatalf("save NextIP=255: %v", err)
	}

	newIP, err := allocateIP(newID)
	if err != nil {
		t.Fatalf("allocate after wrap: %v", err)
	}
	defer releaseIP(newID)

	if newIP == keepIP {
		t.Errorf("reused in-use IP %s", keepIP)
	}
	if newIP == "" || !strings.HasPrefix(newIP, "10.0.0.") {
		t.Errorf("unexpected reused IP %q", newIP)
	}
	t.Logf("keep=%s hole=%s reused=%s", keepIP, holeIP, newIP)
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

// TestPidsMaxEnforcement runs a shell that tries to spawn 25 background sleeps
// and verifies the 21st process in the cgroup is rejected (pids.max=20).
// Requires /dev/null in the jail so busybox can actually background jobs.
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
	t.Logf("pids test output (err=%v):\n%s", err, out)
	if strings.Contains(out, "can't open '/dev/null'") {
		t.Errorf("jail is missing /dev/null; background jobs cannot prove pids.max")
	}
	// cgroup v2 returns "Resource temporarily unavailable" / "can't fork" when pids.max hits.
	forkFailed := strings.Contains(out, "Resource temporarily unavailable") ||
		strings.Contains(out, "can't fork") ||
		strings.Contains(strings.ToLower(out), "nproc")
	if !forkFailed {
		t.Errorf("expected fork failure at the 21st process (pids.max=20); got:\n%s", out)
	}
}

// TestDetachedSurvivesParentExit proves `gocker run -d` returns without waiting
// for the child, and that the child keeps running after the parent exits.
func TestDetachedSurvivesParentExit(t *testing.T) {
	requireLinuxRuntime(t)

	start := time.Now()
	cmd := gockerCommand("run", "-d", "/bin/busybox", "sleep", "15")
	output, err := cmd.CombinedOutput()
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Failed to start detached container: %v\nOutput: %s", err, output)
	}
	if elapsed > 3*time.Second {
		t.Errorf("gocker run -d took %v; parent should exit immediately without waiting for sleep 15", elapsed)
	}

	containerID := parseStartedContainerID(output)
	if containerID == "" {
		t.Fatalf("Could not find container ID in output: %s", output)
	}
	defer stopAndRemove(containerID)

	stateFile := filepath.Join(containersDir, containerID+".json")
	if err := waitForPath(stateFile, 5*time.Second); err != nil {
		t.Fatalf("%v", err)
	}
	state, err := loadContainerState(containerID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if err := syscall.Kill(state.PID, 0); err != nil {
		t.Fatalf("container pid %d died immediately after parent exit: %v", state.PID, err)
	}

	cgroupPath := "/sys/fs/cgroup/gocker/" + containerID
	if err := waitForPath(cgroupPath, 5*time.Second); err != nil {
		t.Fatalf("%v", err)
	}

	time.Sleep(2 * time.Second)
	if err := syscall.Kill(state.PID, 0); err != nil {
		t.Fatalf("container pid %d died shortly after parent exit: %v", state.PID, err)
	}
	if _, err := os.Stat(cgroupPath); err != nil {
		t.Fatalf("cgroup disappeared while container should still be running: %v", err)
	}
}

// TestOverlayWriteIsolation writes a file in container A and asserts it is
// absent from the shared Alpine lower dir and from a second container B.
func TestOverlayWriteIsolation(t *testing.T) {
	requireLinuxRuntime(t)

	marker := fmt.Sprintf("gocker-overlay-isol-%d", time.Now().UnixNano())
	lowerPath := filepath.Join("rootfs", marker)
	_ = os.Remove(lowerPath)

	writeCmd := gockerCommand("run", "/bin/busybox", "sh", "-c", "echo from-a > /"+marker)
	if output, err := writeCmd.CombinedOutput(); err != nil {
		t.Fatalf("container A write failed: %v\nOutput: %s", err, output)
	}

	if _, err := os.Stat(lowerPath); err == nil {
		t.Errorf("write leaked into shared lower rootfs at %s", lowerPath)
		_ = os.Remove(lowerPath)
	}

	uppers, _ := filepath.Glob(filepath.Join(containersDir, "*", "upper", marker))
	if len(uppers) == 0 {
		t.Errorf("expected marker in a per-container upper dir under %s", containersDir)
	} else {
		t.Logf("marker landed in %s", uppers[0])
	}

	checkCmd := gockerCommand("run", "/bin/busybox", "sh", "-c",
		fmt.Sprintf("if [ -f /%s ]; then echo LEAK; exit 1; else echo ISOLATED; fi", marker))
	output, err := checkCmd.CombinedOutput()
	out := string(output)
	if err != nil {
		t.Fatalf("container B check failed: %v\nOutput: %s", err, out)
	}
	if !strings.Contains(out, "ISOLATED") {
		t.Errorf("container B should not see A's write; got:\n%s", out)
	}
}
