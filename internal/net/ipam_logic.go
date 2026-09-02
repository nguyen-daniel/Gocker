package net

import "fmt"

// IPAMState tracks allocated IPs for containers.
type IPAMState struct {
	AllocatedIPs map[string]string `json:"allocated_ips"` // containerID -> IP
	NextIP       int               `json:"next_ip"`       // last octet for next allocation (2-254)
}

func emptyIPAM() *IPAMState {
	return &IPAMState{
		AllocatedIPs: make(map[string]string),
		NextIP:       2,
	}
}

func ipAllocated(ipam *IPAMState, ip string) bool {
	for _, allocatedIP := range ipam.AllocatedIPs {
		if allocatedIP == ip {
			return true
		}
	}
	return false
}

// FindFreeIP picks the next unused address in 10.0.0.2–.254.
// Walks from NextIP through 254, then scans 2–254 for holes after wrap.
func FindFreeIP(ipam *IPAMState) (ip string, octet int, ok bool) {
	start := ipam.NextIP
	if start < 2 {
		start = 2
	}
	for octet = start; octet <= 254; octet++ {
		ip = fmt.Sprintf("10.0.0.%d", octet)
		if !ipAllocated(ipam, ip) {
			return ip, octet, true
		}
	}
	for octet = 2; octet <= 254; octet++ {
		ip = fmt.Sprintf("10.0.0.%d", octet)
		if !ipAllocated(ipam, ip) {
			return ip, octet, true
		}
	}
	return "", 0, false
}
