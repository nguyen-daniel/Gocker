//go:build linux

package ns

import (
	"testing"
)

func TestTeachingSeccompFilter(t *testing.T) {
	f := teachingSeccompFilter()
	if len(f) < 5 {
		t.Fatalf("filter too short: %d", len(f))
	}
	denied := teachingDeniedSyscalls()
	if len(denied) < 5 {
		t.Fatalf("denied list too short: %d", len(denied))
	}
	if seccompAuditArch() == 0 && (testing.Verbose()) {
		t.Log("no AUDIT_ARCH for this GOARCH; filter skips the arch check")
	}
}
