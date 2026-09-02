//go:build linux

package cgroup

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

func Create(containerID string) (string, error) {
	cgroupPath := fmt.Sprintf("/sys/fs/cgroup/gocker/%s", containerID)

	if err := os.MkdirAll("/sys/fs/cgroup/gocker", 0755); err != nil {
		return "", fmt.Errorf("failed to create parent cgroup directory: %v", err)
	}

	if err := enableControllers("/sys/fs/cgroup/gocker"); err != nil {
		fmt.Fprintf(os.Stderr, "  - Note: Could not enable cgroup controllers: %v\n", err)
	}

	if err := os.MkdirAll(cgroupPath, 0755); err != nil {
		return "", fmt.Errorf("failed to create container cgroup directory: %v", err)
	}

	return cgroupPath, nil
}

func enableControllers(cgroupPath string) error {
	controllersFile := filepath.Join(cgroupPath, "cgroup.subtree_control")
	return os.WriteFile(controllersFile, []byte("+cpu +memory +pids"), 0644)
}

func Setup(cgroupPath string, cpuLimit, memoryLimit string) error {
	pidsMaxPath := filepath.Join(cgroupPath, "pids.max")
	if err := os.WriteFile(pidsMaxPath, []byte("20"), 0644); err != nil {
		return fmt.Errorf("failed to set pids.max: %v", err)
	}
	fmt.Fprintln(os.Stderr, "  - Process limit set to 20")

	if cpuLimit != "" && cpuLimit != "max" {
		cpuMax, err := ParseCPULimit(cpuLimit)
		if err != nil {
			return fmt.Errorf("failed to parse CPU limit: %v", err)
		}

		cpuMaxPath := filepath.Join(cgroupPath, "cpu.max")
		if err := os.WriteFile(cpuMaxPath, []byte(cpuMax), 0644); err != nil {
			return fmt.Errorf("failed to set cpu.max: %v", err)
		}
		fmt.Fprintf(os.Stderr, "  - CPU limit: %s\n", cpuLimit)
	}

	if memoryLimit != "" && memoryLimit != "max" {
		memoryMax, err := ParseMemoryLimit(memoryLimit)
		if err != nil {
			return fmt.Errorf("failed to parse memory limit: %v", err)
		}

		memoryMaxPath := filepath.Join(cgroupPath, "memory.max")
		if err := os.WriteFile(memoryMaxPath, []byte(memoryMax), 0644); err != nil {
			return fmt.Errorf("failed to set memory.max: %v", err)
		}
		fmt.Fprintf(os.Stderr, "  - Memory limit: %s\n", memoryLimit)
	}

	return nil
}

func Add(cgroupPath string, pid int) error {
	cgroupProcsPath := filepath.Join(cgroupPath, "cgroup.procs")
	return os.WriteFile(cgroupProcsPath, []byte(strconv.Itoa(pid)), 0644)
}

func Cleanup(cgroupPath string) error {
	if cgroupPath == "" {
		return nil
	}

	err := os.Remove(cgroupPath)
	if err != nil && !os.IsNotExist(err) {
		return nil
	}
	return nil
}
