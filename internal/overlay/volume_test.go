package overlay

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func requireUnixPaths(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("volume jail uses Unix filepath.Join; absolute container paths are not Windows-absolute")
	}
}

func TestMountPointAbsoluteStaysUnderOverlay(t *testing.T) {
	requireUnixPaths(t)

	merged := "/var/lib/gocker/containers/testhost/merged"
	got, err := MountPoint(merged, "/data")
	if err != nil {
		t.Fatalf("MountPoint: %v", err)
	}

	// This is the jailbreak: Join drops merged when the next element is absolute.
	naive := filepath.Join(merged, "/data")
	if naive != "/data" {
		t.Logf("note: filepath.Join did not drop the prefix (got %q)", naive)
	}
	if got == "/data" {
		t.Fatalf("absolute container path escaped onto the host: %s", got)
	}

	want := filepath.Join(merged, "data")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	rel, err := filepath.Rel(merged, got)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("mount point %q escapes overlay %q (rel=%q err=%v)", got, merged, rel, err)
	}
}

func TestMountPointDotDotStaysUnderOverlay(t *testing.T) {
	requireUnixPaths(t)

	merged := "/var/lib/gocker/containers/testhost/merged"
	got, err := MountPoint(merged, "/../../etc/passwd")
	if err != nil {
		t.Fatalf("MountPoint: %v", err)
	}
	if got == "/etc/passwd" {
		t.Fatal("dot-dot container path escaped onto host /etc/passwd")
	}
	want := filepath.Join(merged, "etc", "passwd")
	if got != want {
		t.Errorf("got %q, want %q (cleaned path must stay under merged)", got, want)
	}
	rel, err := filepath.Rel(merged, got)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("mount point %q escapes overlay %q (rel=%q err=%v)", got, merged, rel, err)
	}
}

func TestMountPointRejectsRelativeAndRoot(t *testing.T) {
	requireUnixPaths(t)

	merged := "/var/lib/gocker/containers/testhost/merged"
	if _, err := MountPoint(merged, "data"); err == nil {
		t.Error("expected error for relative container path")
	}
	if _, err := MountPoint(merged, "/"); err == nil {
		t.Error("expected error for overlay root")
	}
	if _, err := MountPoint(merged, ""); err == nil {
		t.Error("expected error for empty container path")
	}
}
