package overlay

import (
	"fmt"
	"path/filepath"
	"strings"
)

// MountPoint joins an absolute container path onto the OverlayFS merged
// directory. filepath.Join(merged, "/data") drops merged on Unix because Join
// treats an absolute element as a new root; we join a cleaned relative path
// and reject anything that would escape merged.
func MountPoint(merged, containerPath string) (string, error) {
	if containerPath == "" {
		return "", fmt.Errorf("container path cannot be empty")
	}
	if !filepath.IsAbs(containerPath) {
		return "", fmt.Errorf("container path must be absolute: %s", containerPath)
	}

	cleaned := filepath.Clean(containerPath)
	rel := strings.TrimPrefix(cleaned, string(filepath.Separator))
	if rel == "" {
		return "", fmt.Errorf("container path cannot be overlay root: %s", containerPath)
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("container path escapes overlay: %s", containerPath)
	}

	mountPoint := filepath.Join(merged, rel)

	absMerged, err := filepath.Abs(merged)
	if err != nil {
		return "", fmt.Errorf("resolve overlay path: %v", err)
	}
	absMerged = filepath.Clean(absMerged)

	absMount, err := filepath.Abs(mountPoint)
	if err != nil {
		return "", fmt.Errorf("resolve mount point: %v", err)
	}
	absMount = filepath.Clean(absMount)

	relToMerged, err := filepath.Rel(absMerged, absMount)
	if err != nil || relToMerged == ".." || strings.HasPrefix(relToMerged, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("container path escapes overlay: %s", containerPath)
	}
	return absMount, nil
}
