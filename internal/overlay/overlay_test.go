//go:build linux

package overlay

import (
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
