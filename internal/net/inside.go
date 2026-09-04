//go:build linux

package net

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gocker/internal/state"
)

// ConfigureInside sets up the network interface inside the container.
// It waits for the parent to set up the veth and reads the IP from the state file.
func ConfigureInside() error {
	containerID := os.Getenv("GOCKER_CONTAINER_ID")
	if containerID == "" {
		return fmt.Errorf("GOCKER_CONTAINER_ID not set")
	}

	ipCmd := "/usr/bin/ip"
	if _, err := os.Stat(ipCmd); os.IsNotExist(err) {
		ipCmd = "/sbin/ip"
		if _, err := os.Stat(ipCmd); os.IsNotExist(err) {
			ipCmd = "ip"
		}
	}

	cmd := exec.Command(ipCmd, "link", "set", "lo", "up")
	cmd.Run()

	// Isolated net ns with loopback only. Skip the veth wait (up to 5s).
	if os.Getenv("GOCKER_NETWORK") == "none" {
		return nil
	}

	var foundVeth string
	for i := 0; i < 50; i++ {
		show := exec.Command(ipCmd, "link", "show", "type", "veth")
		output, err := show.Output()
		if err == nil {
			lines := strings.Split(string(output), "\n")
			for _, line := range lines {
				if strings.Contains(line, "veth") {
					parts := strings.Fields(line)
					if len(parts) >= 2 {
						name := strings.TrimSuffix(parts[1], ":")
						if idx := strings.Index(name, "@"); idx != -1 {
							name = name[:idx]
						}
						if strings.HasPrefix(name, "veth") {
							foundVeth = name
							break
						}
					}
				}
			}
		}
		if foundVeth != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if foundVeth == "" {
		return fmt.Errorf("no veth interface found after waiting")
	}

	teachf("  - Found container veth interface: %s\n", foundVeth)

	var containerIP string
	stateFile := filepath.Join(state.ContainersDir, containerID+".json")
	for i := 0; i < 50; i++ {
		data, err := os.ReadFile(stateFile)
		if err == nil {
			var ctr state.ContainerState
			if json.Unmarshal(data, &ctr) == nil && ctr.ContainerIP != "" {
				containerIP = ctr.ContainerIP
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	if containerIP == "" {
		return fmt.Errorf("container IP not found in state file")
	}

	cmd = exec.Command(ipCmd, "link", "set", foundVeth, "up")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to bring up container veth: %v", err)
	}

	containerCIDR := containerIP + "/24"
	cmd = exec.Command(ipCmd, "addr", "add", containerCIDR, "dev", foundVeth)
	if err := cmd.Run(); err != nil {
		teachf("  - Note: IP assignment: %v\n", err)
	}

	cmd = exec.Command(ipCmd, "route", "add", "default", "via", BridgeIP, "dev", foundVeth)
	if err := cmd.Run(); err != nil {
		teachf("  - Note: Route setup: %v\n", err)
	}

	teachf("  - Container IP: %s\n", containerIP)
	teachln("  - Network configuration complete")

	return nil
}
