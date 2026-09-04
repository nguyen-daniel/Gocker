//go:build linux

package overlay

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRootfs(t *testing.T) {
	absPath, err := ResolveRootfs("./rootfs")
	if err != nil {
		t.Fatalf("Failed to resolve ./rootfs: %v", err)
	}
	if !filepath.IsAbs(absPath) {
		t.Errorf("Resolved path is not absolute: %s", absPath)
	}

	_, err = ResolveRootfs("/nonexistent/rootfs")
	if err == nil {
		t.Errorf("Expected error for non-existent path, got nil")
	}

	absPath, err = ResolveRootfs("")
	if err != nil {
		t.Fatalf("Failed to resolve default rootfs: %v", err)
	}
	if !filepath.IsAbs(absPath) {
		t.Errorf("Default resolved path is not absolute: %s", absPath)
	}
}

func TestCopyResolvConf(t *testing.T) {
	if _, err := os.Stat("/etc/resolv.conf"); err != nil {
		t.Skip("host /etc/resolv.conf missing")
	}
	merged := t.TempDir()
	if err := CopyResolvConf(merged); err != nil {
		t.Fatalf("CopyResolvConf: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(merged, "etc", "resolv.conf"))
	if err != nil {
		t.Fatalf("read copied resolv.conf: %v", err)
	}
	if len(data) == 0 {
		t.Error("copied resolv.conf is empty")
	}
}
