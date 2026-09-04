//go:build linux

package overlay

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"gocker/internal/state"
)

func BaseDir(containerID string) string {
	return filepath.Join(state.ContainersDir, containerID)
}

func CreateDirs(containerID string) error {
	base := BaseDir(containerID)
	for _, name := range []string{"upper", "work", "merged"} {
		if err := os.MkdirAll(filepath.Join(base, name), 0755); err != nil {
			return fmt.Errorf("mkdir overlay %s: %v", name, err)
		}
	}
	return nil
}

func CleanupDirs(containerID string) {
	if containerID == "" {
		return
	}
	base := BaseDir(containerID)
	_ = syscall.Unmount(filepath.Join(base, "merged"), syscall.MNT_DETACH)
	_ = os.RemoveAll(base)
}

func Mount(lower, overlayBase string) (merged string, err error) {
	upper := filepath.Join(overlayBase, "upper")
	work := filepath.Join(overlayBase, "work")
	merged = filepath.Join(overlayBase, "merged")
	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", lower, upper, work)
	if err := syscall.Mount("overlay", merged, "overlay", 0, opts); err != nil {
		return "", fmt.Errorf("overlay mount: %v", err)
	}
	return merged, nil
}

func Pivot(newRoot string) error {
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
		if os.Getenv("GOCKER_QUIET") != "1" {
			fmt.Fprintf(os.Stderr, "  - Note: rmdir /.pivot_old: %v\n", err)
		}
	}
	return nil
}

// ResolveRootfs resolves the rootfs path to an absolute path.
// Priority: 1) explicit --rootfs flag, 2) ./rootfs relative to executable, 3) ./rootfs relative to cwd.
func ResolveRootfs(explicitPath string) (string, error) {
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

	execPath, err := os.Executable()
	if err == nil {
		execDir := filepath.Dir(execPath)
		rootfsPath := filepath.Join(execDir, "rootfs")
		if _, err := os.Stat(rootfsPath); err == nil {
			return rootfsPath, nil
		}
	}

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

// EnsureDevices creates /dev/null and /dev/zero on the OverlayFS merged
// view before pivot_root. Prefer mknod (writes land in this container's upper
// dir); bind-mount host devices if mknod is blocked (nodev). The child's mount
// namespace is already MS_PRIVATE so a bind does not leak onto the host.
func EnsureDevices(rootfsPath string) error {
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		if os.Getenv("GOCKER_QUIET") != "1" {
			fmt.Fprintf(os.Stderr, "  - Note: MS_PRIVATE on /: %v\n", err)
		}
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
