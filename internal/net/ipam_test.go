//go:build linux

package net

import (
	"strings"
	"testing"
	"time"
)

func TestIPAM(t *testing.T) {
	testContainerID := "test-container-ipam-" + time.Now().Format("20060102150405")

	ip1, err := AllocateIP(testContainerID)
	if err != nil {
		t.Fatalf("Failed to allocate IP: %v", err)
	}
	if ip1 == "" {
		t.Fatalf("Allocated IP is empty")
	}
	if !strings.HasPrefix(ip1, "10.0.0.") {
		t.Errorf("Allocated IP is not in expected range: %s", ip1)
	}

	ip2, err := AllocateIP(testContainerID)
	if err != nil {
		t.Fatalf("Failed to re-allocate IP: %v", err)
	}
	if ip1 != ip2 {
		t.Errorf("Re-allocated IP differs: %s vs %s", ip1, ip2)
	}

	if err := ReleaseIP(testContainerID); err != nil {
		t.Fatalf("Failed to release IP: %v", err)
	}

	ipam, err := LoadIPAM()
	if err != nil {
		t.Fatalf("Failed to load IPAM: %v", err)
	}
	if _, exists := ipam.AllocatedIPs[testContainerID]; exists {
		t.Errorf("IP was not released from IPAM state")
	}
}

func TestIPAMReuse(t *testing.T) {
	keepID := "test-ipam-reuse-keep-" + time.Now().Format("20060102150405.000")
	newID := "test-ipam-reuse-new-" + time.Now().Format("20060102150405.000")

	keepIP, err := AllocateIP(keepID)
	if err != nil {
		t.Fatalf("allocate keep: %v", err)
	}
	defer ReleaseIP(keepID)

	holeID := "test-ipam-reuse-hole-" + time.Now().Format("20060102150405.000")
	holeIP, err := AllocateIP(holeID)
	if err != nil {
		t.Fatalf("allocate hole: %v", err)
	}
	if err := ReleaseIP(holeID); err != nil {
		t.Fatalf("release hole: %v", err)
	}

	ipam, err := LoadIPAM()
	if err != nil {
		t.Fatalf("load IPAM: %v", err)
	}
	ipam.NextIP = 255
	if err := SaveIPAM(ipam); err != nil {
		t.Fatalf("save NextIP=255: %v", err)
	}

	newIP, err := AllocateIP(newID)
	if err != nil {
		t.Fatalf("allocate after wrap: %v", err)
	}
	defer ReleaseIP(newID)

	if newIP == keepIP {
		t.Errorf("reused in-use IP %s", keepIP)
	}
	if newIP == "" || !strings.HasPrefix(newIP, "10.0.0.") {
		t.Errorf("unexpected reused IP %q", newIP)
	}
	t.Logf("keep=%s hole=%s reused=%s", keepIP, holeIP, newIP)
}
