//go:build linux

package overlay

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestMountVolumesBind(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("bind-mount requires root (CAP_SYS_ADMIN)")
	}

	host := t.TempDir()
	merged := t.TempDir()
	markerPath := filepath.Join(host, "marker.txt")
	if err := os.WriteFile(markerPath, []byte("from-host"), 0644); err != nil {
		t.Fatalf("write host file: %v", err)
	}

	spec := host + ":/vol"
	if err := MountVolumes(spec, merged); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "not permitted") || strings.Contains(msg, "permission denied") {
			t.Skipf("bind-mount not permitted: %v", err)
		}
		t.Fatalf("MountVolumes: %v", err)
	}
	mountPoint := filepath.Join(merged, "vol")
	defer syscall.Unmount(mountPoint, syscall.MNT_DETACH)

	got, err := os.ReadFile(filepath.Join(mountPoint, "marker.txt"))
	if err != nil {
		t.Fatalf("read bind-mounted file: %v", err)
	}
	if string(got) != "from-host" {
		t.Errorf("got %q, want from-host", got)
	}

	if err := os.WriteFile(filepath.Join(mountPoint, "from-merged.txt"), []byte("ok"), 0644); err != nil {
		t.Fatalf("write through bind mount: %v", err)
	}
	if _, err := os.Stat(filepath.Join(host, "from-merged.txt")); err != nil {
		t.Errorf("write did not appear on host: %v", err)
	}
}
