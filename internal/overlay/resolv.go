//go:build linux

package overlay

import (
	"fmt"
	"os"
	"path/filepath"
)

// CopyResolvConf copies the host's /etc/resolv.conf into the overlay merged
// root so bridge containers can resolve names. Follows a symlink so
// systemd-resolved stub contents are copied rather than a dangling link.
func CopyResolvConf(merged string) error {
	data, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return fmt.Errorf("read host resolv.conf: %v", err)
	}
	etc := filepath.Join(merged, "etc")
	if err := os.MkdirAll(etc, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %v", etc, err)
	}
	dst := filepath.Join(etc, "resolv.conf")
	if err := os.WriteFile(dst, data, 0644); err != nil {
		return fmt.Errorf("write %s: %v", dst, err)
	}
	return nil
}
