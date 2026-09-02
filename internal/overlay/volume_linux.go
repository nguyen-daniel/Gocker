//go:build linux

package overlay

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// MountVolumes bind-mounts host:container specs onto the overlay merged dir.
func MountVolumes(volumesStr string, merged string) error {
	volumes := strings.Split(volumesStr, "|")

	for _, volume := range volumes {
		volume = strings.TrimSpace(volume)
		if volume == "" {
			continue
		}

		parts := strings.Split(volume, ":")
		if len(parts) != 2 {
			return fmt.Errorf("invalid volume format: %s (expected host:container)", volume)
		}

		hostPath := strings.TrimSpace(parts[0])
		containerPath := strings.TrimSpace(parts[1])

		if hostPath == "" || containerPath == "" {
			return fmt.Errorf("invalid volume format: %s (host and container paths cannot be empty)", volume)
		}

		hostInfo, err := os.Stat(hostPath)
		if err != nil {
			return fmt.Errorf("host path does not exist: %s: %v", hostPath, err)
		}

		mountPoint, err := MountPoint(merged, containerPath)
		if err != nil {
			return err
		}

		if err := os.MkdirAll(filepath.Dir(mountPoint), 0755); err != nil {
			return fmt.Errorf("failed to create parent directories for mount point %s: %v", mountPoint, err)
		}

		if hostInfo.IsDir() {
			if err := os.MkdirAll(mountPoint, 0755); err != nil {
				return fmt.Errorf("failed to create mount point directory %s: %v", mountPoint, err)
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(mountPoint), 0755); err != nil {
				return fmt.Errorf("failed to create parent directory for file mount point %s: %v", mountPoint, err)
			}
			if _, err := os.Stat(mountPoint); os.IsNotExist(err) {
				f, err := os.Create(mountPoint)
				if err != nil {
					return fmt.Errorf("failed to create file mount point %s: %v", mountPoint, err)
				}
				f.Close()
			}
		}

		flags := syscall.MS_BIND | syscall.MS_REC
		if err := syscall.Mount(hostPath, mountPoint, "", uintptr(flags), ""); err != nil {
			return fmt.Errorf("failed to bind mount %s to %s: %v", hostPath, mountPoint, err)
		}

		if err := syscall.Mount("", mountPoint, "", syscall.MS_PRIVATE|syscall.MS_REC, ""); err != nil {
			fmt.Fprintf(os.Stderr, "  - Warning: Failed to set mount propagation for %s: %v\n", mountPoint, err)
		}

		fmt.Fprintf(os.Stderr, "  - Mounted %s -> %s\n", hostPath, containerPath)
	}

	return nil
}
