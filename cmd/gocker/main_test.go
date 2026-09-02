//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"gocker/internal/state"
)

func requireLinuxRuntime(t *testing.T) {
	t.Helper()
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
		ctr, stateErr := state.Load(containerID)
		alive := ""
		if stateErr == nil {
			if killErr := syscall.Kill(ctr.PID, 0); killErr != nil {
				alive = fmt.Sprintf(" (pid %d not running: %v)", ctr.PID, killErr)
			} else {
				alive = fmt.Sprintf(" (pid %d still running)", ctr.PID)
			}
		}
		t.Fatalf("%v%s\nrun -d output:\n%s", err, alive, output)
	}

	if ctr, err := state.Load(containerID); err != nil {
		t.Errorf("Could not load container state: %v", err)
	} else if err := syscall.Kill(ctr.PID, 0); err != nil {
		t.Errorf("detached container pid %d died; cgroup would be reaped: %v", ctr.PID, err)
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

	var state1, state2 state.ContainerState
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

func TestPidsMaxEnforcement(t *testing.T) {
	binaryPath := "./gocker"
	if _, err := os.Stat(binaryPath); os.IsNotExist(err) {
		t.Skip("gocker binary not found. Run 'make build' first.")
	}
	if _, err := os.Stat("./rootfs/bin/busybox"); os.IsNotExist(err) {
		t.Skip("rootfs not set up")
	}

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
	forkFailed := strings.Contains(out, "Resource temporarily unavailable") ||
		strings.Contains(out, "can't fork") ||
		strings.Contains(strings.ToLower(out), "nproc")
	if !forkFailed {
		t.Errorf("expected fork failure at the 21st process (pids.max=20); got:\n%s", out)
	}
}

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

	stateFile := filepath.Join(state.ContainersDir, containerID+".json")
	if err := waitForPath(stateFile, 5*time.Second); err != nil {
		t.Fatalf("%v", err)
	}
	ctr, err := state.Load(containerID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if err := syscall.Kill(ctr.PID, 0); err != nil {
		t.Fatalf("container pid %d died immediately after parent exit: %v", ctr.PID, err)
	}

	cgroupPath := "/sys/fs/cgroup/gocker/" + containerID
	if err := waitForPath(cgroupPath, 5*time.Second); err != nil {
		t.Fatalf("%v", err)
	}

	time.Sleep(2 * time.Second)
	if err := syscall.Kill(ctr.PID, 0); err != nil {
		t.Fatalf("container pid %d died shortly after parent exit: %v", ctr.PID, err)
	}
	if _, err := os.Stat(cgroupPath); err != nil {
		t.Fatalf("cgroup disappeared while container should still be running: %v", err)
	}
}

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

	uppers, _ := filepath.Glob(filepath.Join(state.ContainersDir, "*", "upper", marker))
	if len(uppers) == 0 {
		t.Errorf("expected marker in a per-container upper dir under %s", state.ContainersDir)
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
