package net

import (
	"fmt"
	"strconv"
	"strings"
)

// PortMap is a TCP host:container publish mapping.
type PortMap struct {
	Host      int `json:"host"`
	Container int `json:"container"`
}

// ParsePublish parses HOST:CONTAINER (TCP only). No UDP, no ranges, no host IP.
func ParsePublish(spec string) (PortMap, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return PortMap{}, fmt.Errorf("invalid -p format %q (want HOST:CONTAINER)", spec)
	}
	if strings.Contains(spec, "/") {
		return PortMap{}, fmt.Errorf("TCP only: %q (no protocol suffix)", spec)
	}
	parts := strings.Split(spec, ":")
	if len(parts) != 2 {
		return PortMap{}, fmt.Errorf("invalid -p format %q (want HOST:CONTAINER)", spec)
	}
	if strings.Contains(parts[0], "-") || strings.Contains(parts[1], "-") {
		return PortMap{}, fmt.Errorf("port ranges are not supported: %q", spec)
	}
	host, err := strconv.Atoi(parts[0])
	if err != nil {
		return PortMap{}, fmt.Errorf("invalid host port %q", parts[0])
	}
	ctr, err := strconv.Atoi(parts[1])
	if err != nil {
		return PortMap{}, fmt.Errorf("invalid container port %q", parts[1])
	}
	if host < 1 || host > 65535 || ctr < 1 || ctr > 65535 {
		return PortMap{}, fmt.Errorf("ports must be 1-65535: %q", spec)
	}
	return PortMap{Host: host, Container: ctr}, nil
}

func (p PortMap) String() string {
	return fmt.Sprintf("%d:%d", p.Host, p.Container)
}

func toDest(ip string, containerPort int) string {
	return fmt.Sprintf("%s:%d", ip, containerPort)
}

// DNATPreroutingArgs is the iptables spec after -A/-D/-C (nat PREROUTING).
func DNATPreroutingArgs(hostPort int, destIP string, destPort int) []string {
	return []string{"-p", "tcp", "--dport", strconv.Itoa(hostPort), "-j", "DNAT", "--to-destination", toDest(destIP, destPort)}
}

// DNATOutputArgs is the iptables spec after -A/-D/-C (nat OUTPUT, localhost).
func DNATOutputArgs(hostPort int, destIP string, destPort int) []string {
	return []string{"-p", "tcp", "-d", "127.0.0.1", "--dport", strconv.Itoa(hostPort), "-j", "DNAT", "--to-destination", toDest(destIP, destPort)}
}
