package net

import (
	"reflect"
	"testing"
)

func TestParsePublish(t *testing.T) {
	p, err := ParsePublish("8080:80")
	if err != nil {
		t.Fatalf("ParsePublish: %v", err)
	}
	if p.Host != 8080 || p.Container != 80 {
		t.Errorf("got %+v", p)
	}
	if p.String() != "8080:80" {
		t.Errorf("String=%q", p.String())
	}

	cases := []string{"", "80", "80:90:1", "8080:80/udp", "8000-8080:80", "0:80", "80:0", "foo:80"}
	for _, c := range cases {
		if _, err := ParsePublish(c); err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}

func TestDNATRuleArgs(t *testing.T) {
	pre := DNATPreroutingArgs(8080, "10.0.0.2", 80)
	wantPre := []string{"-p", "tcp", "--dport", "8080", "-j", "DNAT", "--to-destination", "10.0.0.2:80"}
	if !reflect.DeepEqual(pre, wantPre) {
		t.Errorf("PREROUTING=%v", pre)
	}
	out := DNATOutputArgs(8080, "10.0.0.2", 80)
	wantOut := []string{"-p", "tcp", "-d", "127.0.0.1", "--dport", "8080", "-j", "DNAT", "--to-destination", "10.0.0.2:80"}
	if !reflect.DeepEqual(out, wantOut) {
		t.Errorf("OUTPUT=%v", out)
	}
}
