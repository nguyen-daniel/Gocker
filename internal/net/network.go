//go:build linux

package net

import (
	"bufio"
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

func teachingQuiet() bool {
	return os.Getenv("GOCKER_QUIET") == "1"
}

func teachf(format string, args ...interface{}) {
	if teachingQuiet() {
		return
	}
	fmt.Fprintf(os.Stderr, format, args...)
}

func teachln(s string) {
	if teachingQuiet() {
		return
	}
	fmt.Fprintln(os.Stderr, s)
}

func EnsureBridge() error {
	if _, err := stdnet.InterfaceByName(BridgeName); err == nil {
		_ = LinkSetUp(BridgeName)
		if err := setupNATRules(); err != nil {
			return fmt.Errorf("failed to set up NAT: %v", err)
		}
		return nil
	}

	teachln("  - Creating bridge gocker0...")

	if err := LinkAddBridge(BridgeName); err != nil {
		return fmt.Errorf("failed to create bridge: %v", err)
	}
	if err := AddrAdd(BridgeName, BridgeIP, 24); err != nil {
		teachf("  - Note: Bridge IP configuration: %v\n", err)
	}
	if err := LinkSetUp(BridgeName); err != nil {
		return fmt.Errorf("failed to bring up bridge: %v", err)
	}
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0644); err != nil {
		teachf("  - Warning: Failed to enable IP forwarding: %v\n", err)
	}
	if err := setupNATRules(); err != nil {
		return fmt.Errorf("failed to set up NAT: %v", err)
	}

	teachln("  - Bridge gocker0 created and configured")
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

func VethNames(containerID string) (vethHost, vethPeer string) {
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
	return vethHost, vethPeer
}

func SetupContainer(containerID string, childPid int, vethHost, vethPeer, containerIP string, quiet bool) error {
	if !quiet {
		fmt.Fprintf(os.Stderr, "  - Creating veth pair: %s <-> %s\n", vethHost, vethPeer)
	}
	if err := LinkAddVeth(vethHost, vethPeer); err != nil {
		return fmt.Errorf("failed to create veth pair: %v", err)
	}

	if err := LinkSetMaster(vethHost, BridgeName); err != nil {
		CleanupVeth(vethHost)
		return fmt.Errorf("failed to attach veth to bridge: %v", err)
	}
	if err := LinkSetUp(vethHost); err != nil {
		CleanupVeth(vethHost)
		return fmt.Errorf("failed to bring up host veth: %v", err)
	}

	if !quiet {
		fmt.Fprintf(os.Stderr, "  - Moving %s into container namespace (IP: %s)\n", vethPeer, containerIP)
	}
	nsFile, err := os.Open(fmt.Sprintf("/proc/%d/ns/net", childPid))
	if err != nil {
		CleanupVeth(vethHost)
		return fmt.Errorf("failed to open container netns: %v", err)
	}
	defer nsFile.Close()
	if err := LinkSetNsFd(vethPeer, int(nsFile.Fd())); err != nil {
		CleanupVeth(vethHost)
		return fmt.Errorf("failed to move veth into container namespace: %v", err)
	}

	if !quiet {
		fmt.Fprintln(os.Stderr, "  - Network setup complete")
	}
	return nil
}

func CleanupVeth(vethHost string) {
	if vethHost == "" {
		return
	}
	_ = LinkDel(vethHost)
}

func CleanupContainer(containerID, vethHost string) {
	CleanupVeth(vethHost)
	ReleaseIP(containerID)
}

func getDefaultInterface() (string, error) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return "", err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return "", fmt.Errorf("empty /proc/net/route")
	}
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		if fields[1] == "00000000" && fields[0] != "" && fields[0] != "*" {
			return fields[0], nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("could not find default interface")
}
