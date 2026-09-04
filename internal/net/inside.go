//go:build linux

package net

import (
	"fmt"
	"io"
	"os"
	"strconv"
)

// ConfigureInside sets up lo (always) and the container veth (bridge mode).
// The parent moves the veth into this netns and writes one byte on the sync
// fd before the child configures the named interface — no poll loop.
func ConfigureInside() error {
	if err := LinkSetUp("lo"); err != nil {
		teachf("  - Note: loopback up: %v\n", err)
	}

	if os.Getenv("GOCKER_NETWORK") == "none" {
		return nil
	}

	if err := waitNetSync(); err != nil {
		return err
	}

	foundVeth := os.Getenv("GOCKER_VETH_PEER")
	containerIP := os.Getenv("GOCKER_CONTAINER_IP")
	if foundVeth == "" || containerIP == "" {
		return fmt.Errorf("missing GOCKER_VETH_PEER or GOCKER_CONTAINER_IP")
	}

	teachf("  - Found container veth interface: %s\n", foundVeth)

	if err := LinkSetUp(foundVeth); err != nil {
		return fmt.Errorf("failed to bring up container veth: %v", err)
	}
	if err := AddrAdd(foundVeth, containerIP, 24); err != nil {
		teachf("  - Note: IP assignment: %v\n", err)
	}
	if err := RouteAddDefault(foundVeth, BridgeIP); err != nil {
		teachf("  - Note: Route setup: %v\n", err)
	}

	teachf("  - Container IP: %s\n", containerIP)
	teachln("  - Network configuration complete")
	return nil
}

func waitNetSync() error {
	fdStr := os.Getenv("GOCKER_NET_SYNC_FD")
	if fdStr == "" {
		return fmt.Errorf("GOCKER_NET_SYNC_FD not set")
	}
	fd, err := strconv.Atoi(fdStr)
	if err != nil {
		return fmt.Errorf("GOCKER_NET_SYNC_FD: %v", err)
	}
	f := os.NewFile(uintptr(fd), "net-sync")
	if f == nil {
		return fmt.Errorf("net-sync fd %d", fd)
	}
	defer f.Close()
	buf := make([]byte, 1)
	n, err := f.Read(buf)
	if n == 1 {
		return nil
	}
	if err == io.EOF {
		return fmt.Errorf("network setup aborted")
	}
	if err != nil {
		return fmt.Errorf("wait for veth: %v", err)
	}
	return fmt.Errorf("wait for veth: short read")
}
