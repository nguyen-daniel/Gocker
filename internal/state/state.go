//go:build linux

package state

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	Dir           = "/var/lib/gocker"
	ContainersDir = "/var/lib/gocker/containers"
)

// ContainerState represents the state of a container.
type ContainerState struct {
	ID          string    `json:"id"`
	PID         int       `json:"pid"`
	Status      string    `json:"status"` // "running", "stopped", "exited"
	CreatedAt   time.Time `json:"created_at"`
	Command     []string  `json:"command"`
	VethHost    string    `json:"veth_host,omitempty"`
	VethPeer    string    `json:"veth_peer,omitempty"`
	ContainerIP string    `json:"container_ip,omitempty"`
	LogFile     string    `json:"log_file"`
	Detached    bool      `json:"detached"`
	CgroupPath  string    `json:"cgroup_path,omitempty"`
	RootfsPath  string    `json:"rootfs_path,omitempty"`
}

func LockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

func UnlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

func EnsureDir() error {
	if err := os.MkdirAll(ContainersDir, 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %v", err)
	}
	return nil
}

func Save(ctr *ContainerState) error {
	if err := EnsureDir(); err != nil {
		return err
	}

	stateFile := filepath.Join(ContainersDir, ctr.ID+".json")

	f, err := os.OpenFile(stateFile, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open state file: %v", err)
	}
	defer f.Close()

	if err := LockFile(f); err != nil {
		return fmt.Errorf("failed to lock state file: %v", err)
	}
	defer UnlockFile(f)

	data, err := json.MarshalIndent(ctr, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal container state: %v", err)
	}

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("failed to write container state: %v", err)
	}

	return nil
}

func Load(containerID string) (*ContainerState, error) {
	fullID, err := ResolveID(containerID)
	if err != nil {
		return nil, err
	}

	stateFile := filepath.Join(ContainersDir, fullID+".json")

	f, err := os.Open(stateFile)
	if err != nil {
		return nil, fmt.Errorf("container not found: %s", containerID)
	}
	defer f.Close()

	if err := LockFile(f); err != nil {
		return nil, fmt.Errorf("failed to lock state file: %v", err)
	}
	defer UnlockFile(f)

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("failed to read state file: %v", err)
	}

	var ctr ContainerState
	if err := json.Unmarshal(data, &ctr); err != nil {
		return nil, fmt.Errorf("failed to parse container state: %v", err)
	}

	return &ctr, nil
}

func ResolveID(partialID string) (string, error) {
	if err := EnsureDir(); err != nil {
		return "", err
	}

	files, err := os.ReadDir(ContainersDir)
	if err != nil {
		return "", fmt.Errorf("failed to read containers directory: %v", err)
	}

	var matches []string
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		fullID := strings.TrimSuffix(file.Name(), ".json")
		if strings.HasPrefix(fullID, partialID) {
			matches = append(matches, fullID)
		}
	}

	if len(matches) == 0 {
		return "", fmt.Errorf("container not found: %s", partialID)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("ambiguous container ID: %s matches multiple containers", partialID)
	}
	return matches[0], nil
}

func UpdateStatus(containerID string, status string) error {
	ctr, err := Load(containerID)
	if err != nil {
		return err
	}

	ctr.Status = status
	return Save(ctr)
}
