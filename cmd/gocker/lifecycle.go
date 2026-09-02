//go:build linux

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gocker/internal/cgroup"
	gockernet "gocker/internal/net"
	"gocker/internal/overlay"
	"gocker/internal/state"
)

func listContainers() {
	if err := state.EnsureDir(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}

	files, err := os.ReadDir(state.ContainersDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading containers directory: %v\n", err)
		return
	}

	if len(files) == 0 {
		fmt.Println("No containers found")
		return
	}

	fmt.Printf("%-14s %-10s %-10s %-16s %-30s %s\n", "CONTAINER ID", "STATUS", "PID", "IP", "CREATED", "COMMAND")
	fmt.Println(strings.Repeat("-", 120))

	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".json") {
			continue
		}

		containerID := strings.TrimSuffix(file.Name(), ".json")
		ctr, err := state.Load(containerID)
		if err != nil {
			continue
		}

		status := ctr.Status
		if status == "running" {
			if err := syscall.Kill(ctr.PID, 0); err != nil {
				status = "exited"
				state.UpdateStatus(containerID, "exited")
				gockernet.CleanupContainer(containerID, ctr.VethHost)
				cgroup.Cleanup(ctr.CgroupPath)
			}
		}

		command := strings.Join(ctr.Command, " ")
		if len(command) > 30 {
			command = command[:27] + "..."
		}

		displayID := containerID
		if len(displayID) > 12 {
			displayID = displayID[:12]
		}

		containerIP := ctr.ContainerIP
		if containerIP == "" {
			containerIP = "-"
		}

		created := ctr.CreatedAt.Format("2006-01-02 15:04:05")
		fmt.Printf("%-14s %-10s %-10d %-16s %-30s %s\n", displayID, status, ctr.PID, containerIP, created, command)
	}
}

func stopContainer(containerID string) {
	ctr, err := state.Load(containerID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	displayID := ctr.ID
	if len(displayID) > 12 {
		displayID = displayID[:12]
	}

	if ctr.Status != "running" {
		fmt.Printf("Container %s is not running (status: %s)\n", displayID, ctr.Status)
		return
	}

	if err := syscall.Kill(ctr.PID, 0); err != nil {
		fmt.Printf("Container %s is not running\n", displayID)
		state.UpdateStatus(ctr.ID, "exited")
		gockernet.CleanupContainer(ctr.ID, ctr.VethHost)
		cgroup.Cleanup(ctr.CgroupPath)
		return
	}

	fmt.Printf("Stopping container %s (PID: %d)...\n", displayID, ctr.PID)
	if err := syscall.Kill(ctr.PID, syscall.SIGTERM); err != nil {
		fmt.Fprintf(os.Stderr, "Error stopping container: %v\n", err)
		os.Exit(1)
	}

	time.Sleep(2 * time.Second)

	if err := syscall.Kill(ctr.PID, 0); err == nil {
		fmt.Println("Container did not stop gracefully, sending SIGKILL...")
		syscall.Kill(ctr.PID, syscall.SIGKILL)
		time.Sleep(500 * time.Millisecond)
	}

	gockernet.CleanupContainer(ctr.ID, ctr.VethHost)
	cgroup.Cleanup(ctr.CgroupPath)

	if err := state.UpdateStatus(ctr.ID, "stopped"); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to update container status: %v\n", err)
	}

	fmt.Printf("Container %s stopped\n", displayID)
}

func removeContainer(containerID string) {
	ctr, err := state.Load(containerID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	displayID := ctr.ID
	if len(displayID) > 12 {
		displayID = displayID[:12]
	}

	if ctr.Status == "running" {
		if err := syscall.Kill(ctr.PID, 0); err == nil {
			fmt.Fprintf(os.Stderr, "Error: Cannot remove running container %s. Stop it first with 'gocker stop %s'\n", displayID, displayID)
			os.Exit(1)
		}
	}

	gockernet.CleanupContainer(ctr.ID, ctr.VethHost)
	cgroup.Cleanup(ctr.CgroupPath)
	overlay.CleanupDirs(ctr.ID)

	stateFile := filepath.Join(state.ContainersDir, ctr.ID+".json")
	if err := os.Remove(stateFile); err != nil {
		fmt.Fprintf(os.Stderr, "Error removing container state: %v\n", err)
		os.Exit(1)
	}

	if ctr.LogFile != "" {
		if err := os.Remove(ctr.LogFile); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Warning: Failed to remove log file: %v\n", err)
		}
	}

	fmt.Printf("Container %s removed\n", displayID)
}

func showLogs(containerID string) {
	ctr, err := state.Load(containerID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if ctr.LogFile == "" {
		displayID := ctr.ID
		if len(displayID) > 12 {
			displayID = displayID[:12]
		}
		fmt.Fprintf(os.Stderr, "Error: No log file found for container %s\n", displayID)
		os.Exit(1)
	}

	logFile, err := os.Open(ctr.LogFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening log file: %v\n", err)
		os.Exit(1)
	}
	defer logFile.Close()

	if _, err := io.Copy(os.Stdout, logFile); err != nil {
		fmt.Fprintf(os.Stderr, "Error reading log file: %v\n", err)
		os.Exit(1)
	}
}
