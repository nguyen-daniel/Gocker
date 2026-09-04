//go:build linux

package ns

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestNamespaceConfig(t *testing.T) {
	rootful := SysProcAttr(false)
	for _, f := range []struct {
		name string
		bit  uintptr
	}{
		{"NEWUTS", syscall.CLONE_NEWUTS},
		{"NEWPID", syscall.CLONE_NEWPID},
		{"NEWNS", syscall.CLONE_NEWNS},
		{"NEWNET", syscall.CLONE_NEWNET},
		{"NEWIPC", syscall.CLONE_NEWIPC},
	} {
		if !HasCloneFlag(rootful.Cloneflags, f.bit) {
			t.Errorf("rootful missing %s", f.name)
		}
	}
	if HasCloneFlag(rootful.Cloneflags, syscall.CLONE_NEWUSER) {
		t.Error("rootful default must not set CLONE_NEWUSER")
	}

	rootless := SysProcAttr(true)
	if !HasCloneFlag(rootless.Cloneflags, syscall.CLONE_NEWUSER) {
		t.Fatal("rootless path should set CLONE_NEWUSER")
	}
	if len(rootless.UidMappings) != 1 || rootless.UidMappings[0].ContainerID != 0 {
		t.Fatalf("expected uid map container 0 -> host euid, got %+v", rootless.UidMappings)
	}
	if rootless.UidMappings[0].HostID != os.Geteuid() {
		t.Errorf("uid map HostID=%d, want euid %d", rootless.UidMappings[0].HostID, os.Geteuid())
	}
	t.Logf("euid=%d: 5-ns rootful; optional user ns maps 0 -> %d", os.Geteuid(), rootless.UidMappings[0].HostID)
}

func TestCloneUserNamespace(t *testing.T) {
	cmd := exec.Command("true")
	cmd.SysProcAttr = SysProcAttr(true)
	if err := cmd.Run(); err != nil {
		t.Fatalf("clone with optional user namespace failed: %v", err)
	}
}
