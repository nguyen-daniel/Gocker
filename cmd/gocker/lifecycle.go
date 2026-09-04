//go:build linux

package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
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

	// Laptop-width table (~80 cols). CREATED is omitted so interview terminals
	// do not wrap a 130-column header.
	fmt.Printf("%-12s %-10s %-8s %-7s %-15s %s\n", "ID", "NAME", "STATUS", "PID", "IP", "COMMAND")
	fmt.Println(strings.Repeat("-", 80))

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
		if len(command) > 22 {
			command = command[:19] + "..."
		}

		displayID := shortID(ctr.ID)

		name := ctr.Name
		if name == "" {
			name = "-"
		} else if len(name) > 10 {
			name = name[:7] + "..."
		}

		containerIP := ctr.ContainerIP
		if containerIP == "" {
			containerIP = "-"
		}

		fmt.Printf("%-12s %-10s %-8s %-7d %-15s %s\n", displayID, name, status, ctr.PID, containerIP, command)
	}
}

func stopContainer(containerID string) {
	ctr, err := state.Load(containerID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	displayID := displayContainer(ctr)

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

func removeContainer(containerID string, force bool) {
	ctr, err := state.Load(containerID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	displayID := displayContainer(ctr)

	running := ctr.Status == "running" && syscall.Kill(ctr.PID, 0) == nil
	if running {
		if !force {
			fmt.Fprintf(os.Stderr, "Error: Cannot remove running container %s. Stop it first with 'gocker stop %s' or use 'gocker rm -f'\n", displayID, displayID)
			os.Exit(1)
		}
		_ = syscall.Kill(ctr.PID, syscall.SIGKILL)
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if syscall.Kill(ctr.PID, 0) != nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
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

func showLogs(containerID string, follow bool) {
	ctr, err := state.Load(containerID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if ctr.LogFile == "" {
		fmt.Fprintf(os.Stderr, "Error: No log file found for container %s\n", displayContainer(ctr))
		os.Exit(1)
	}

	logFile, err := os.Open(ctr.LogFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening log file: %v\n", err)
		os.Exit(1)
	}
	defer logFile.Close()

	if !follow {
		if _, err := io.Copy(os.Stdout, logFile); err != nil {
			fmt.Fprintf(os.Stderr, "Error reading log file: %v\n", err)
			os.Exit(1)
		}
		return
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	buf := make([]byte, 4096)
	for {
		select {
		case <-sigChan:
			return
		default:
		}

		n, err := logFile.Read(buf)
		if n > 0 {
			_, _ = os.Stdout.Write(buf[:n])
		}
		if err == nil {
			continue
		}
		if err != io.EOF {
			fmt.Fprintf(os.Stderr, "Error reading log file: %v\n", err)
			os.Exit(1)
		}

		alive, loadErr := containerStillRunning(ctr.ID)
		if loadErr != nil || !alive {
			n, _ := logFile.Read(buf)
			if n > 0 {
				_, _ = os.Stdout.Write(buf[:n])
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func containerStillRunning(containerID string) (bool, error) {
	ctr, err := state.Load(containerID)
	if err != nil {
		return false, err
	}
	if ctr.Status != "running" {
		return false, nil
	}
	if err := syscall.Kill(ctr.PID, 0); err != nil {
		return false, nil
	}
	return true, nil
}

func displayContainer(ctr *state.ContainerState) string {
	if ctr.Name != "" {
		return ctr.Name
	}
	return shortID(ctr.ID)
}
