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

	"gocker/internal/ns"
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
	_ = gockerCommand("rm", "-f", containerID).Run()
}

func waitUntil(timeout time.Duration, fn func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func overlayDir(containerID string) string {
	return filepath.Join(state.ContainersDir, containerID)
}

func stateFile(containerID string) string {
	return filepath.Join(state.ContainersDir, containerID+".json")
}

func parseCPUStat(data []byte) (usageUsec, nrThrottled int64) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		var v int64
		fmt.Sscanf(fields[1], "%d", &v)
		switch fields[0] {
		case "usage_usec":
			usageUsec = v
		case "nr_throttled":
			nrThrottled = v
		}
	}
	return
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
		cmd = exec.Command(binaryPath, "run", "--network=none", "/bin/busybox", "true")
	} else {
		cmd = exec.Command("sudo", binaryPath, "run", "--network=none", "/bin/busybox", "true")
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
		cmd = exec.Command(binaryPath, "run", "--network=none", "/bin/hostname")
	} else {
		cmd = exec.Command("sudo", binaryPath, "run", "--network=none", "/bin/hostname")
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

	cmd := gockerCommand("run", "-d", "--network=none", "/bin/busybox", "sleep", "10")
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
		cmd = exec.Command(binaryPath, "run", "--network=none", "/bin/busybox", "sh", "-c", script)
	} else {
		cmd = exec.Command("sudo", binaryPath, "run", "--network=none", "/bin/busybox", "sh", "-c", script)
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
	cmd := gockerCommand("run", "-d", "--network=none", "/bin/busybox", "sleep", "15")
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

	writeCmd := gockerCommand("run", "--network=none", "/bin/busybox", "sh", "-c", "echo from-a > /"+marker)
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

	checkCmd := gockerCommand("run", "--network=none", "/bin/busybox", "sh", "-c",
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

func TestUnknownNetworkMode(t *testing.T) {
	if _, err := os.Stat("./gocker"); os.IsNotExist(err) {
		t.Skip("gocker binary not found. Run 'make build' first.")
	}
	cmd := exec.Command("./gocker", "run", "--rootless", "--network", "host", "/bin/true")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected failure for --network=host; got:\n%s", out)
	}
	if !strings.Contains(string(out), "network") {
		t.Errorf("expected a network-mode error, got:\n%s", out)
	}
}

func TestStopSetsStopped(t *testing.T) {
	requireLinuxRuntime(t)

	cmd := gockerCommand("run", "-d", "--network=none", "/bin/busybox", "sleep", "30")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("start: %v\n%s", err, output)
	}
	id := parseStartedContainerID(output)
	if id == "" {
		t.Fatalf("no container ID in:\n%s", output)
	}
	defer stopAndRemove(id)

	if err := waitForPath(stateFile(id), 5*time.Second); err != nil {
		t.Fatal(err)
	}

	if out, err := gockerCommand("stop", id).CombinedOutput(); err != nil {
		t.Fatalf("stop: %v\n%s", err, out)
	}

	ctr, err := state.Load(id)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if ctr.Status != "stopped" {
		t.Errorf("status=%q, want stopped", ctr.Status)
	}
	if err := syscall.Kill(ctr.PID, 0); err == nil {
		t.Errorf("pid %d still running after stop", ctr.PID)
	}
}

func TestRmRefusesRunningThenDeletes(t *testing.T) {
	requireLinuxRuntime(t)

	cmd := gockerCommand("run", "-d", "--network=none", "/bin/busybox", "sleep", "30")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("start: %v\n%s", err, output)
	}
	id := parseStartedContainerID(output)
	if id == "" {
		t.Fatalf("no container ID in:\n%s", output)
	}
	defer stopAndRemove(id)

	if err := waitForPath(stateFile(id), 5*time.Second); err != nil {
		t.Fatal(err)
	}

	out, err := gockerCommand("rm", id).CombinedOutput()
	if err == nil {
		t.Fatalf("rm of running container should fail; got:\n%s", out)
	}
	if !strings.Contains(string(out), "running") {
		t.Errorf("expected running-container error, got:\n%s", out)
	}
	if _, err := os.Stat(stateFile(id)); err != nil {
		t.Fatalf("state should remain after refused rm: %v", err)
	}

	if out, err := gockerCommand("stop", id).CombinedOutput(); err != nil {
		t.Fatalf("stop: %v\n%s", err, out)
	}
	if out, err := gockerCommand("rm", id).CombinedOutput(); err != nil {
		t.Fatalf("rm after stop: %v\n%s", err, out)
	}

	if _, err := os.Stat(stateFile(id)); !os.IsNotExist(err) {
		t.Errorf("state file still present: %v", err)
	}
	if _, err := os.Stat(overlayDir(id)); !os.IsNotExist(err) {
		t.Errorf("overlay dir still present: %v", err)
	}
}

func TestRmForceRunning(t *testing.T) {
	requireLinuxRuntime(t)

	cmd := gockerCommand("run", "-d", "--network=none", "/bin/busybox", "sleep", "30")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("start: %v\n%s", err, output)
	}
	id := parseStartedContainerID(output)
	if id == "" {
		t.Fatalf("no container ID in:\n%s", output)
	}

	if err := waitForPath(stateFile(id), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if out, err := gockerCommand("rm", "-f", id).CombinedOutput(); err != nil {
		t.Fatalf("rm -f: %v\n%s", err, out)
	}
	if _, err := os.Stat(stateFile(id)); !os.IsNotExist(err) {
		t.Errorf("state file still present after rm -f: %v", err)
	}
}

func TestLogsContents(t *testing.T) {
	requireLinuxRuntime(t)

	marker := fmt.Sprintf("GOCKER_LOG_%d", time.Now().UnixNano())
	cmd := gockerCommand("run", "-d", "--network=none", "/bin/busybox", "sh", "-c",
		"echo "+marker+"; sleep 8")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("start: %v\n%s", err, output)
	}
	id := parseStartedContainerID(output)
	if id == "" {
		t.Fatalf("no container ID in:\n%s", output)
	}
	defer stopAndRemove(id)

	if err := waitForPath(stateFile(id), 5*time.Second); err != nil {
		t.Fatal(err)
	}

	ok := waitUntil(8*time.Second, func() bool {
		out, err := gockerCommand("logs", id).CombinedOutput()
		return err == nil && strings.Contains(string(out), marker)
	})
	if !ok {
		out, _ := gockerCommand("logs", id).CombinedOutput()
		t.Fatalf("logs did not contain %s; got:\n%s", marker, out)
	}
}

func TestLogsFollow(t *testing.T) {
	requireLinuxRuntime(t)

	cmd := gockerCommand("run", "-d", "--network=none", "/bin/busybox", "sh", "-c",
		"echo FIRST_FOLLOW; sleep 2; echo SECOND_FOLLOW")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("start: %v\n%s", err, output)
	}
	id := parseStartedContainerID(output)
	if id == "" {
		t.Fatalf("no container ID in:\n%s", output)
	}
	defer stopAndRemove(id)

	if err := waitForPath(stateFile(id), 5*time.Second); err != nil {
		t.Fatal(err)
	}

	logsCmd := gockerCommand("logs", "-f", id)
	out, err := logsCmd.CombinedOutput()
	got := string(out)
	if err != nil {
		t.Fatalf("logs -f: %v\n%s", err, got)
	}
	if !strings.Contains(got, "FIRST_FOLLOW") || !strings.Contains(got, "SECOND_FOLLOW") {
		t.Errorf("logs -f missing output:\n%s", got)
	}
}

func TestDetachedReaperCleansUp(t *testing.T) {
	requireLinuxRuntime(t)

	cmd := gockerCommand("run", "-d", "/bin/busybox", "sleep", "2")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("start: %v\n%s", err, output)
	}
	id := parseStartedContainerID(output)
	if id == "" {
		t.Fatalf("no container ID in:\n%s", output)
	}
	defer stopAndRemove(id)

	if err := waitForPath(stateFile(id), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	ctr, err := state.Load(id)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cgroupPath := ctr.CgroupPath
	veth := ctr.VethHost

	ok := waitUntil(15*time.Second, func() bool {
		return syscall.Kill(ctr.PID, 0) != nil
	})
	if !ok {
		t.Fatalf("pid %d still running after sleep 2", ctr.PID)
	}

	ok = waitUntil(10*time.Second, func() bool {
		latest, err := state.Load(id)
		if err != nil {
			return false
		}
		if latest.Status == "running" {
			return false
		}
		if _, err := os.Stat(cgroupPath); err == nil {
			return false
		}
		if veth != "" {
			if _, err := os.Stat("/sys/class/net/" + veth); err == nil {
				return false
			}
		}
		return true
	})
	if !ok {
		latest, _ := state.Load(id)
		status := ""
		if latest != nil {
			status = latest.Status
		}
		_, cgroupErr := os.Stat(cgroupPath)
		vethState := "none"
		if veth != "" {
			if _, err := os.Stat("/sys/class/net/" + veth); err == nil {
				vethState = "still present"
			} else {
				vethState = "gone"
			}
		}
		t.Fatalf("reaper did not clean up: status=%q cgroup_err=%v veth=%s", status, cgroupErr, vethState)
	}
}

func TestCPUAndMemoryLimitsApplied(t *testing.T) {
	requireLinuxRuntime(t)

	cmd := gockerCommand("run", "-d", "--network=none",
		"--cpu-limit", "0.5", "--memory-limit", "32M",
		"/bin/busybox", "sleep", "20")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("start: %v\n%s", err, output)
	}
	id := parseStartedContainerID(output)
	if id == "" {
		t.Fatalf("no container ID in:\n%s", output)
	}
	defer stopAndRemove(id)

	if err := waitForPath(stateFile(id), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	ctr, err := state.Load(id)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := waitForPath(ctr.CgroupPath, 5*time.Second); err != nil {
		t.Fatal(err)
	}

	cpuMax, err := os.ReadFile(filepath.Join(ctr.CgroupPath, "cpu.max"))
	if err != nil {
		t.Skipf("cpu.max not readable (no cpu controller?): %v", err)
	}
	if got := strings.TrimSpace(string(cpuMax)); got != "50000 100000" {
		t.Errorf("cpu.max=%q, want %q", got, "50000 100000")
	}

	memMax, err := os.ReadFile(filepath.Join(ctr.CgroupPath, "memory.max"))
	if err != nil {
		t.Skipf("memory.max not readable (no memory controller?): %v", err)
	}
	if got := strings.TrimSpace(string(memMax)); got != "33554432" {
		t.Errorf("memory.max=%q, want 33554432 (32M)", got)
	}
}

func TestCPULimitEnforcement(t *testing.T) {
	requireLinuxRuntime(t)

	cmd := gockerCommand("run", "-d", "--network=none", "--cpu-limit", "0.2",
		"/bin/busybox", "sh", "-c", "while true; do :; done")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("start: %v\n%s", err, output)
	}
	id := parseStartedContainerID(output)
	if id == "" {
		t.Fatalf("no container ID in:\n%s", output)
	}
	defer stopAndRemove(id)

	if err := waitForPath(stateFile(id), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	ctr, err := state.Load(id)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cpuMaxPath := filepath.Join(ctr.CgroupPath, "cpu.max")
	if err := waitForPath(cpuMaxPath, 5*time.Second); err != nil {
		t.Skipf("cpu.max not available: %v", err)
	}
	cpuMax, err := os.ReadFile(cpuMaxPath)
	if err != nil {
		t.Fatalf("read cpu.max: %v", err)
	}
	if got := strings.TrimSpace(string(cpuMax)); got != "20000 100000" {
		t.Fatalf("cpu.max=%q, want 20000 100000", got)
	}

	time.Sleep(3 * time.Second)
	statPath := filepath.Join(ctr.CgroupPath, "cpu.stat")
	data, err := os.ReadFile(statPath)
	if err != nil {
		t.Skipf("cpu.stat not readable: %v", err)
	}
	usage, throttled := parseCPUStat(data)
	t.Logf("cpu.stat after ~3s spin: usage_usec=%d nr_throttled=%d\n%s", usage, throttled, data)
	if throttled == 0 && usage > 2_500_000 {
		t.Errorf("cpu.max was set to 0.2 but nr_throttled=0 and usage_usec=%d (~full core); quota may not be enforced", usage)
	}
	if throttled == 0 {
		t.Log("nr_throttled=0 after 3s; quota file is set. Throttle counters can lag on lightly loaded hosts.")
	}
}

func TestMemoryLimitEnforcement(t *testing.T) {
	requireLinuxRuntime(t)

	script := "mount -t tmpfs -o size=256m tmpfs /tmp && dd if=/dev/zero of=/tmp/hog bs=1M count=200 && echo UNBOUNDED"
	cmd := gockerCommand("run", "--network=none", "--memory-limit", "64M",
		"/bin/busybox", "sh", "-c", script)
	output, err := cmd.CombinedOutput()
	out := string(output)
	t.Logf("memory test output (err=%v):\n%s", err, out)

	if strings.Contains(out, "Permission denied") || strings.Contains(out, "Operation not permitted") {
		t.Skip("tmpfs mount not permitted inside the container; cannot prove memory.max")
	}
	ran := strings.Contains(out, "Executing command") || strings.Contains(out, "records in") ||
		strings.Contains(out, "Killed") || strings.Contains(out, "UNBOUNDED")
	if !ran {
		if strings.Contains(out, "memory.max") || strings.Contains(out, "cgroup") {
			t.Skipf("memory controller not usable: %v\n%s", err, out)
		}
		t.Fatalf("container did not reach the memory hog: %v\n%s", err, out)
	}
	if strings.Contains(out, "UNBOUNDED") {
		t.Fatal("200MB tmpfs write succeeded under memory.max=64M")
	}
	if err == nil && !strings.Contains(out, "Killed") && !strings.Contains(out, "Cannot allocate") {
		t.Errorf("expected OOM/kill or allocate failure under 64M; got success:\n%s", out)
	}
}

func TestNetworkNoneHasNoVeth(t *testing.T) {
	requireLinuxRuntime(t)

	cmd := gockerCommand("run", "-d", "--network=none", "/bin/busybox", "sleep", "10")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("start: %v\n%s", err, output)
	}
	id := parseStartedContainerID(output)
	if id == "" {
		t.Fatalf("no container ID in:\n%s", output)
	}
	defer stopAndRemove(id)

	if err := waitForPath(stateFile(id), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	ctr, err := state.Load(id)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if ctr.ContainerIP != "" || ctr.VethHost != "" {
		t.Errorf("expected no veth/IP with --network=none; ip=%q veth=%q", ctr.ContainerIP, ctr.VethHost)
	}
}

func TestVolumeBindMount(t *testing.T) {
	requireLinuxRuntime(t)

	host := t.TempDir()
	if err := os.WriteFile(filepath.Join(host, "hello.txt"), []byte("from-host"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cmd := gockerCommand("run", "--network=none", "-v", host+":/data",
		"/bin/busybox", "cat", "/data/hello.txt")
	output, err := cmd.CombinedOutput()
	out := string(output)
	if err != nil {
		t.Fatalf("run -v: %v\n%s", err, out)
	}
	if !strings.Contains(out, "from-host") {
		t.Errorf("bind mount did not show host file:\n%s", out)
	}
}

func TestQuietSuppressesTeachingLogs(t *testing.T) {
	requireLinuxRuntime(t)

	cmd := gockerCommand("run", "-q", "--network=none", "/bin/busybox", "echo", "hello-quiet")
	output, err := cmd.CombinedOutput()
	out := string(output)
	if err != nil {
		t.Fatalf("run -q: %v\n%s", err, out)
	}
	if !strings.Contains(out, "hello-quiet") {
		t.Errorf("missing command output:\n%s", out)
	}
	if strings.Contains(out, "Creating isolated namespaces") {
		t.Errorf("--quiet still printed teaching logs:\n%s", out)
	}
}

func TestDefaultIsDemoQuiet(t *testing.T) {
	requireLinuxRuntime(t)

	cmd := gockerCommand("run", "--network=none", "/bin/busybox", "echo", "hello-default")
	output, err := cmd.CombinedOutput()
	out := string(output)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "hello-default") {
		t.Errorf("missing command output:\n%s", out)
	}
	if strings.Contains(out, "Creating isolated namespaces") {
		t.Errorf("default run printed teaching logs (want --teach for that):\n%s", out)
	}
	if !strings.Contains(out, "id ") {
		t.Errorf("foreground run should print a short id on stderr:\n%s", out)
	}
}

func TestTeachPrintsLogs(t *testing.T) {
	requireLinuxRuntime(t)

	cmd := gockerCommand("run", "--teach", "--network=none", "/bin/busybox", "echo", "hello-teach")
	output, err := cmd.CombinedOutput()
	out := string(output)
	if err != nil {
		t.Fatalf("run --teach: %v\n%s", err, out)
	}
	if !strings.Contains(out, "hello-teach") {
		t.Errorf("missing command output:\n%s", out)
	}
	if !strings.Contains(out, "Creating isolated namespaces") {
		t.Errorf("--teach should print teaching logs:\n%s", out)
	}
}

func testGockerBinary(t *testing.T) string {
	t.Helper()
	for _, p := range []string{"./gocker", "../gocker"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("gocker binary not found. Run 'make build' first.")
	return ""
}

func TestHelpExitsZeroWithoutRoot(t *testing.T) {
	bin := testGockerBinary(t)

	for _, args := range [][]string{
		{"--help"},
		{"-h"},
		{"run", "--help"},
		{"ps", "--help"},
		{"stop", "--help"},
		{"rm", "--help"},
		{"logs", "--help"},
	} {
		cmd := exec.Command(bin, args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("gocker %s: %v\n%s", strings.Join(args, " "), err, out)
			continue
		}
		if !strings.Contains(string(out), "Usage:") {
			t.Errorf("gocker %s: expected Usage, got:\n%s", strings.Join(args, " "), out)
		}
		if strings.Contains(string(out), "Unknown command") {
			t.Errorf("gocker %s treated as unknown command:\n%s", strings.Join(args, " "), out)
		}
		if strings.Contains(string(out), "must be run with sudo") {
			t.Errorf("gocker %s required root:\n%s", strings.Join(args, " "), out)
		}
	}

	out, err := exec.Command(bin, "run", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("run --help: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "--teach") {
		t.Errorf("run --help should mention --teach:\n%s", out)
	}
}

func TestNameStopRm(t *testing.T) {
	requireLinuxRuntime(t)

	name := fmt.Sprintf("gockername%d", time.Now().UnixNano())
	cmd := gockerCommand("run", "-d", "--network=none", "--name", name, "/bin/busybox", "sleep", "20")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("start: %v\n%s", err, output)
	}
	id := parseStartedContainerID(output)
	if id == "" {
		t.Fatalf("no container ID in:\n%s", output)
	}
	defer stopAndRemove(id)

	if err := waitForPath(stateFile(id), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if out, err := gockerCommand("stop", name).CombinedOutput(); err != nil {
		t.Fatalf("stop by name: %v\n%s", err, out)
	}
	ctr, err := state.Load(name)
	if err != nil {
		t.Fatalf("load by name: %v", err)
	}
	if ctr.Status != "stopped" || ctr.Name != name {
		t.Errorf("status=%q name=%q", ctr.Status, ctr.Name)
	}
	if out, err := gockerCommand("rm", name).CombinedOutput(); err != nil {
		t.Fatalf("rm by name: %v\n%s", err, out)
	}
}

func TestTeachingCapsDroppedInContainer(t *testing.T) {
	requireLinuxRuntime(t)

	cmd := gockerCommand("run", "--network=none", "/bin/busybox", "cat", "/proc/self/status")
	output, err := cmd.CombinedOutput()
	out := string(output)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}

	dropped := 0
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "CapBnd:") {
			continue
		}
		hex := strings.TrimSpace(strings.TrimPrefix(line, "CapBnd:"))
		var v uint64
		fmt.Sscanf(hex, "%x", &v)
		for _, c := range ns.TeachingDropCaps {
			if v&(1<<uint(c)) != 0 {
				t.Errorf("container CapBnd still has teaching cap %d", c)
			} else {
				dropped++
			}
		}
	}
	if dropped == 0 {
		t.Errorf("CapBnd not found in container status (teaching cap drop may have been skipped):\n%s", out)
	}
}
