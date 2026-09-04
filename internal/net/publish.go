//go:build linux

package net

import (
	"fmt"
	"os"
	"os/exec"

	"gocker/internal/state"
)

func enableRouteLocalnet() {
	_ = os.WriteFile("/proc/sys/net/ipv4/conf/lo/route_localnet", []byte("1\n"), 0644)
	_ = os.WriteFile("/proc/sys/net/ipv4/conf/"+BridgeName+"/route_localnet", []byte("1\n"), 0644)
}

func iptables(args ...string) error {
	cmd := exec.Command("iptables", args...)
	return cmd.Run()
}

func ensureRule(check, add []string) error {
	if iptables(check...) == nil {
		return nil
	}
	if err := iptables(add...); err != nil {
		return fmt.Errorf("iptables %v: %v", add, err)
	}
	return nil
}

func publishSpecs(ip string, p state.PortMap) (pre, out []string) {
	pre = DNATPreroutingArgs(p.Host, ip, p.Container)
	out = DNATOutputArgs(p.Host, ip, p.Container)
	return pre, out
}

// AddPublishRules installs TCP DNAT on nat PREROUTING and OUTPUT (localhost).
func AddPublishRules(containerIP string, ports []state.PortMap) error {
	if containerIP == "" || len(ports) == 0 {
		return nil
	}
	enableRouteLocalnet()
	for _, p := range ports {
		pre, out := publishSpecs(containerIP, p)
		if err := ensureRule(append([]string{"-t", "nat", "-C", "PREROUTING"}, pre...), append([]string{"-t", "nat", "-A", "PREROUTING"}, pre...)); err != nil {
			return fmt.Errorf("publish %d:%d PREROUTING: %v", p.Host, p.Container, err)
		}
		if err := ensureRule(append([]string{"-t", "nat", "-C", "OUTPUT"}, out...), append([]string{"-t", "nat", "-A", "OUTPUT"}, out...)); err != nil {
			return fmt.Errorf("publish %d:%d OUTPUT: %v", p.Host, p.Container, err)
		}
	}
	return nil
}

// RemovePublishRules deletes DNAT rules. Missing rules are ignored.
func RemovePublishRules(containerIP string, ports []state.PortMap) {
	if containerIP == "" || len(ports) == 0 {
		return
	}
	for _, p := range ports {
		pre, out := publishSpecs(containerIP, p)
		_ = iptables(append([]string{"-t", "nat", "-D", "PREROUTING"}, pre...)...)
		_ = iptables(append([]string{"-t", "nat", "-D", "OUTPUT"}, out...)...)
	}
}
