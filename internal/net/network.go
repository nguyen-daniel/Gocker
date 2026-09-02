//go:build linux

package net

import (
	"fmt"
	stdnet "net"
	"os"
	"os/exec"
	"strings"
)

const (
	BridgeName   = "gocker0"
	BridgeIP     = "10.0.0.1"
	BridgeCIDR   = "10.0.0.1/24"
	ContainerNet = "10.0.0.0/24"
)

func EnsureBridge() error {
	if _, err := stdnet.InterfaceByName(BridgeName); err == nil {
		cmd := exec.Command("ip", "link", "set", BridgeName, "up")
		cmd.Run()
		if err := setupNATRules(); err != nil {
			fmt.Fprintf(os.Stderr, "  - Warning: Failed to set up NAT: %v\n", err)
		}
		return nil
	}

	fmt.Fprintln(os.Stderr, "  - Creating bridge gocker0...")

	cmd := exec.Command("ip", "link", "add", "name", BridgeName, "type", "bridge")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create bridge: %v", err)
	}

	cmd = exec.Command("ip", "addr", "add", BridgeCIDR, "dev", BridgeName)
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "  - Note: Bridge IP configuration: %v\n", err)
	}

	cmd = exec.Command("ip", "link", "set", BridgeName, "up")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to bring up bridge: %v", err)
	}

	cmd = exec.Command("sysctl", "-w", "net.ipv4.ip_forward=1")
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "  - Warning: Failed to enable IP forwarding: %v\n", err)
	}

	if err := setupNATRules(); err != nil {
		fmt.Fprintf(os.Stderr, "  - Warning: Failed to set up NAT: %v\n", err)
	}

	fmt.Fprintln(os.Stderr, "  - Bridge gocker0 created and configured")
	return nil
}

func setupNATRules() error {
	defaultInterface, err := getDefaultInterface()
	if err != nil {
		return fmt.Errorf("could not determine default interface: %v", err)
	}

	checkCmd := exec.Command("iptables", "-t", "nat", "-C", "POSTROUTING", "-s", ContainerNet, "-o", defaultInterface, "-j", "MASQUERADE")
	if checkCmd.Run() != nil {
		cmd := exec.Command("iptables", "-t", "nat", "-A", "POSTROUTING", "-s", ContainerNet, "-o", defaultInterface, "-j", "MASQUERADE")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to add MASQUERADE rule: %v", err)
		}
	}

	checkCmd = exec.Command("iptables", "-C", "FORWARD", "-i", BridgeName, "-o", defaultInterface, "-j", "ACCEPT")
	if checkCmd.Run() != nil {
		cmd := exec.Command("iptables", "-A", "FORWARD", "-i", BridgeName, "-o", defaultInterface, "-j", "ACCEPT")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to add FORWARD rule (out): %v", err)
		}
	}

	checkCmd = exec.Command("iptables", "-C", "FORWARD", "-i", defaultInterface, "-o", BridgeName, "-j", "ACCEPT")
	if checkCmd.Run() != nil {
		cmd := exec.Command("iptables", "-A", "FORWARD", "-i", defaultInterface, "-o", BridgeName, "-j", "ACCEPT")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to add FORWARD rule (in): %v", err)
		}
	}

	return nil
}

func teardownNATRules() {
	defaultInterface, err := getDefaultInterface()
	if err != nil {
		return
	}
	exec.Command("iptables", "-t", "nat", "-D", "POSTROUTING", "-s", ContainerNet, "-o", defaultInterface, "-j", "MASQUERADE").Run()
	exec.Command("iptables", "-D", "FORWARD", "-i", BridgeName, "-o", defaultInterface, "-j", "ACCEPT").Run()
	exec.Command("iptables", "-D", "FORWARD", "-i", defaultInterface, "-o", BridgeName, "-j", "ACCEPT").Run()
}

func SetupContainer(containerID string, childPid int, quiet bool) (vethHost, vethPeer, containerIP string, err error) {
	containerIP, err = AllocateIP(containerID)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to allocate IP: %v", err)
	}

	shortID := containerID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	vethHost = fmt.Sprintf("veth%s", shortID)
	vethPeer = fmt.Sprintf("vethc%s", shortID)

	if len(vethHost) > 15 {
		vethHost = vethHost[:15]
	}
	if len(vethPeer) > 15 {
		vethPeer = vethPeer[:15]
	}

	if !quiet {
		fmt.Fprintf(os.Stderr, "  - Creating veth pair: %s <-> %s\n", vethHost, vethPeer)
	}
	cmd := exec.Command("ip", "link", "add", vethHost, "type", "veth", "peer", "name", vethPeer)
	if err := cmd.Run(); err != nil {
		ReleaseIP(containerID)
		return "", "", "", fmt.Errorf("failed to create veth pair: %v", err)
	}

	cmd = exec.Command("ip", "link", "set", vethHost, "master", BridgeName)
	if err := cmd.Run(); err != nil {
		CleanupVeth(vethHost)
		ReleaseIP(containerID)
		return "", "", "", fmt.Errorf("failed to attach veth to bridge: %v", err)
	}

	cmd = exec.Command("ip", "link", "set", vethHost, "up")
	if err := cmd.Run(); err != nil {
		CleanupVeth(vethHost)
		ReleaseIP(containerID)
		return "", "", "", fmt.Errorf("failed to bring up host veth: %v", err)
	}

	if !quiet {
		fmt.Fprintf(os.Stderr, "  - Moving %s into container namespace (IP: %s)\n", vethPeer, containerIP)
	}
	netnsPath := fmt.Sprintf("/proc/%d/ns/net", childPid)
	cmd = exec.Command("ip", "link", "set", vethPeer, "netns", netnsPath)
	if err := cmd.Run(); err != nil {
		CleanupVeth(vethHost)
		ReleaseIP(containerID)
		return "", "", "", fmt.Errorf("failed to move veth into container namespace: %v", err)
	}

	if !quiet {
		fmt.Fprintln(os.Stderr, "  - Network setup complete")
	}
	return vethHost, vethPeer, containerIP, nil
}

func CleanupVeth(vethHost string) {
	if vethHost == "" {
		return
	}
	exec.Command("ip", "link", "delete", vethHost).Run()
}

func CleanupContainer(containerID, vethHost string) {
	CleanupVeth(vethHost)
	ReleaseIP(containerID)
}

func getDefaultInterface() (string, error) {
	cmd := exec.Command("ip", "route", "show", "default")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "default") && strings.Contains(line, "dev") {
			parts := strings.Fields(line)
			for i, part := range parts {
				if part == "dev" && i+1 < len(parts) {
					return parts[i+1], nil
				}
			}
		}
	}

	return "", fmt.Errorf("could not find default interface")
}
