//go:build linux

package net

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"gocker/internal/state"
)

const ipamFile = "/var/lib/gocker/ipam.json"

func lockIPAMFile() (*os.File, error) {
	if err := state.EnsureDir(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(ipamFile, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open IPAM file: %v", err)
	}
	if err := state.LockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("failed to lock IPAM file: %v", err)
	}
	return f, nil
}

func readIPAMFile(f *os.File) (*IPAMState, error) {
	if _, err := f.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("failed to seek IPAM file: %v", err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("failed to read IPAM file: %v", err)
	}
	if len(data) == 0 {
		return emptyIPAM(), nil
	}
	var ipam IPAMState
	if err := json.Unmarshal(data, &ipam); err != nil {
		return nil, fmt.Errorf("failed to parse IPAM state: %v", err)
	}
	if ipam.AllocatedIPs == nil {
		ipam.AllocatedIPs = make(map[string]string)
	}
	if ipam.NextIP < 2 {
		ipam.NextIP = 2
	}
	return &ipam, nil
}

func writeIPAMFile(f *os.File, ipam *IPAMState) error {
	data, err := json.MarshalIndent(ipam, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal IPAM state: %v", err)
	}
	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("failed to truncate IPAM file: %v", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return fmt.Errorf("failed to seek IPAM file: %v", err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("failed to write IPAM file: %v", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("failed to sync IPAM file: %v", err)
	}
	return nil
}

func LoadIPAM() (*IPAMState, error) {
	f, err := lockIPAMFile()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	defer state.UnlockFile(f)
	return readIPAMFile(f)
}

func SaveIPAM(ipam *IPAMState) error {
	f, err := lockIPAMFile()
	if err != nil {
		return err
	}
	defer f.Close()
	defer state.UnlockFile(f)
	if ipam.AllocatedIPs == nil {
		ipam.AllocatedIPs = make(map[string]string)
	}
	return writeIPAMFile(f, ipam)
}

func AllocateIP(containerID string) (string, error) {
	f, err := lockIPAMFile()
	if err != nil {
		return "", err
	}
	defer f.Close()
	defer state.UnlockFile(f)

	ipam, err := readIPAMFile(f)
	if err != nil {
		return "", err
	}

	if ip, exists := ipam.AllocatedIPs[containerID]; exists {
		return ip, nil
	}

	ip, octet, ok := FindFreeIP(ipam)
	if !ok {
		return "", fmt.Errorf("no available IP addresses in pool")
	}
	ipam.AllocatedIPs[containerID] = ip
	ipam.NextIP = octet + 1
	if err := writeIPAMFile(f, ipam); err != nil {
		return "", err
	}
	return ip, nil
}

func ReleaseIP(containerID string) error {
	f, err := lockIPAMFile()
	if err != nil {
		return err
	}
	defer f.Close()
	defer state.UnlockFile(f)

	ipam, err := readIPAMFile(f)
	if err != nil {
		return err
	}

	delete(ipam.AllocatedIPs, containerID)
	if err := writeIPAMFile(f, ipam); err != nil {
		return err
	}
	if len(ipam.AllocatedIPs) == 0 {
		teardownNATRules()
	}
	return nil
}
