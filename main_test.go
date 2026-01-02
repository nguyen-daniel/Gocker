package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestGockerRun tests that Gocker can successfully execute a command inside a container
// This is an integration test that verifies the entire containerization flow:
// 1. Namespace creation (UTS, PID, Mount)
// 2. Cgroups setup
// 3. Chroot filesystem isolation
// 4. Command execution
func TestGockerRun(t *testing.T) {
	// Ensure the gocker binary exists (should be built by make test)
	binaryPath := "./gocker"
	if _, err := os.Stat(binaryPath); os.IsNotExist(err) {
		t.Fatalf("gocker binary not found at %s. Run 'make build' first.", binaryPath)
	}

	// Ensure rootfs exists (required for chroot-based filesystem isolation)
	// The rootfs provides the minimal Linux environment that the container uses
	rootfsPath := "./rootfs"
	if _, err := os.Stat(rootfsPath); os.IsNotExist(err) {
		t.Fatalf("rootfs directory not found at %s. Run 'make setup' first.", rootfsPath)
	}

	// Ensure /bin/busybox exists in the rootfs (Alpine Linux uses busybox)
	// In Alpine, commands like 'true' are available through busybox
	busyboxPath := filepath.Join(rootfsPath, "bin/busybox")
	if _, err := os.Stat(busyboxPath); os.IsNotExist(err) {
		t.Fatalf("/bin/busybox not found in rootfs at %s. Rootfs may be incomplete.", busyboxPath)
	}

	// Execute gocker run /bin/busybox true
	// /bin/busybox true is a simple command that exits with status 0
	// In Alpine Linux, 'true' is available as a busybox applet, not a standalone binary
	// This tests that the container can successfully:
	// - Create namespaces (requires root, so test must run with sudo)
	// - Set up cgroups
	// - Chroot into rootfs
	// - Mount /proc
	// - Execute commands inside the isolated environment
	cmd := exec.Command("sudo", binaryPath, "run", "/bin/busybox", "true")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Note: This test requires sudo because:
	// 1. Linux namespaces (CLONE_NEWUTS, CLONE_NEWPID, CLONE_NEWNS) require root privileges
	// 2. Cgroups v2 operations require root to create /sys/fs/cgroup/gocker and write limits
	// 3. Chroot requires root to change the root filesystem
	// 4. Mounting /proc requires root privileges
	// Without root access, container isolation cannot be established
	err := cmd.Run()
	if err != nil {
		t.Fatalf("Gocker failed to execute /bin/busybox true in container: %v", err)
	}
}

// TestGockerRunWithHostname verifies that the container has an isolated hostname
// This tests the UTS namespace isolation
func TestGockerRunWithHostname(t *testing.T) {
	binaryPath := "./gocker"
	if _, err := os.Stat(binaryPath); os.IsNotExist(err) {
		t.Skip("gocker binary not found. Run 'make build' first.")
	}

	rootfsPath := "./rootfs"
	if _, err := os.Stat(rootfsPath); os.IsNotExist(err) {
		t.Skip("rootfs directory not found. Run 'make setup' first.")
	}

	// Execute hostname command inside container
	// The hostname should be "gocker-container" as set in the child() function
	cmd := exec.Command("sudo", binaryPath, "run", "/bin/hostname")
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




