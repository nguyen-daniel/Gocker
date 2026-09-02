package net

import (
	"fmt"
	"testing"
)

func TestFindFreeIP(t *testing.T) {
	ipam := &IPAMState{
		AllocatedIPs: map[string]string{"a": "10.0.0.2"},
		NextIP:       2,
	}
	ip, octet, ok := FindFreeIP(ipam)
	if !ok || ip != "10.0.0.3" || octet != 3 {
		t.Errorf("sequential: got ip=%s octet=%d ok=%v, want 10.0.0.3 / 3", ip, octet, ok)
	}

	ipam = &IPAMState{
		AllocatedIPs: map[string]string{"keep": "10.0.0.2"},
		NextIP:       255,
	}
	ip, octet, ok = FindFreeIP(ipam)
	if !ok || ip != "10.0.0.3" || octet != 3 {
		t.Errorf("wrap scan: got ip=%s octet=%d ok=%v, want 10.0.0.3 / 3", ip, octet, ok)
	}

	full := make(map[string]string, 253)
	for i := 2; i <= 254; i++ {
		full[fmt.Sprintf("c%d", i)] = fmt.Sprintf("10.0.0.%d", i)
	}
	ipam = &IPAMState{AllocatedIPs: full, NextIP: 255}
	if _, _, ok = FindFreeIP(ipam); ok {
		t.Error("expected no free IP when 2–254 are all allocated")
	}
}
