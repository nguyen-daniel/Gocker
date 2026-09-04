//go:build linux

package net

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestNLAPutAlign(t *testing.T) {
	b := nlaPutString(nil, unix.IFLA_IFNAME, "gocker0")
	if len(b)%unix.NLA_ALIGNTO != 0 {
		t.Errorf("nlaPutString len %d not aligned to %d", len(b), unix.NLA_ALIGNTO)
	}
	if got := nlaTypeAt(b); got != unix.IFLA_IFNAME {
		t.Errorf("type=%d, want IFLA_IFNAME", got)
	}
	if nlaPutStringLen("veth") != len(nlaPutString(nil, unix.IFLA_IFNAME, "veth")) {
		t.Error("nlaPutStringLen mismatch")
	}
}

func TestParseNLErrorAck(t *testing.T) {
	h := unix.NlMsghdr{Len: unix.NLMSG_HDRLEN + 4, Type: unix.NLMSG_ERROR}
	b := append(marshalNlHdr(h), 0, 0, 0, 0)
	if err := parseNLError(b); err != nil {
		t.Errorf("ACK: %v", err)
	}
}
