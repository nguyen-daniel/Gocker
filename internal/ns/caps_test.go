//go:build linux

package ns

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestDropTeachingCaps(t *testing.T) {
	if os.Getenv("GOCKER_TEST_DROP_CAPS") == "1" {
		if err := DropTeachingCaps(); err != nil {
			fmt.Fprintf(os.Stderr, "DropTeachingCaps: %v\n", err)
			os.Exit(1)
		}
		data, err := os.ReadFile("/proc/self/status")
		if err != nil {
			fmt.Fprintf(os.Stderr, "read status: %v\n", err)
			os.Exit(2)
		}
		os.Stdout.Write(data)
		os.Exit(0)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestDropTeachingCaps$")
	cmd.Env = append(os.Environ(), "GOCKER_TEST_DROP_CAPS=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("subprocess: %v\n%s", err, out)
	}

	for _, field := range []string{"CapBnd", "CapPrm", "CapEff"} {
		for _, c := range TeachingDropCaps {
			has, err := procStatusHasCap(out, field, c)
			if err != nil {
				t.Fatalf("parse %s: %v\n%s", field, err, out)
			}
			if has {
				t.Errorf("%s still has teaching cap %d", field, c)
			}
		}
	}
}

func procStatusHasCap(status []byte, field string, capn int) (bool, error) {
	prefix := field + ":"
	for _, line := range strings.Split(string(status), "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		hex := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		v, err := strconv.ParseUint(hex, 16, 64)
		if err != nil {
			return false, err
		}
		return v&(1<<uint(capn)) != 0, nil
	}
	return false, fmt.Errorf("%s not found", field)
}
