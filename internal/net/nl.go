//go:build linux

package net

import (
	"encoding/binary"
	"errors"
	"fmt"
	stdnet "net"
	"unsafe"

	"golang.org/x/sys/unix"
)

const vethInfoPeer = 1 // linux/veth.h VETH_INFO_PEER

func nlaAlign(n int) int {
	return (n + unix.NLA_ALIGNTO - 1) & ^(unix.NLA_ALIGNTO - 1)
}

func marshalBytes(ptr unsafe.Pointer, n int) []byte {
	b := make([]byte, n)
	copy(b, unsafe.Slice((*byte)(ptr), n))
	return b
}

func marshalIfInfo(m unix.IfInfomsg) []byte {
	return marshalBytes(unsafe.Pointer(&m), unix.SizeofIfInfomsg)
}

func marshalIfAddr(m unix.IfAddrmsg) []byte {
	return marshalBytes(unsafe.Pointer(&m), unix.SizeofIfAddrmsg)
}

func marshalRtMsg(m unix.RtMsg) []byte {
	return marshalBytes(unsafe.Pointer(&m), unix.SizeofRtMsg)
}

func marshalNlHdr(h unix.NlMsghdr) []byte {
	return marshalBytes(unsafe.Pointer(&h), unix.SizeofNlMsghdr)
}

func nlaPut(b []byte, typ uint16, data []byte) []byte {
	nlaLen := uint16(unix.NLA_HDRLEN + len(data))
	pad := nlaAlign(int(nlaLen))
	start := len(b)
	b = append(b, make([]byte, pad)...)
	hdr := unix.NlAttr{Len: nlaLen, Type: typ}
	copy(b[start:], marshalBytes(unsafe.Pointer(&hdr), unix.NLA_HDRLEN))
	copy(b[start+unix.NLA_HDRLEN:], data)
	return b
}

func nlaPutString(b []byte, typ uint16, s string) []byte {
	return nlaPut(b, typ, append([]byte(s), 0))
}

func nlaPutUint32(b []byte, typ uint16, v uint32) []byte {
	var d [4]byte
	nativePutUint32(d[:], v)
	return nlaPut(b, typ, d[:])
}

func nativePutUint32(b []byte, v uint32) {
	*(*uint32)(unsafe.Pointer(&b[0])) = v
}

func isExist(err error) bool {
	return errors.Is(err, unix.EEXIST)
}

func nlRequest(msgType, flags uint16, payload []byte) error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return err
	}
	defer unix.Close(fd)

	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return err
	}

	h := unix.NlMsghdr{
		Len:   uint32(unix.NLMSG_HDRLEN + len(payload)),
		Type:  msgType,
		Flags: flags | unix.NLM_F_REQUEST | unix.NLM_F_ACK,
		Seq:   1,
	}
	msg := append(marshalNlHdr(h), payload...)
	if err := unix.Sendto(fd, msg, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return err
	}

	buf := make([]byte, 8192)
	n, _, err := unix.Recvfrom(fd, buf, 0)
	if err != nil {
		return err
	}
	return parseNLError(buf[:n])
}

func parseNLError(b []byte) error {
	if len(b) < unix.NLMSG_HDRLEN+4 {
		return fmt.Errorf("short netlink response")
	}
	h := (*unix.NlMsghdr)(unsafe.Pointer(&b[0]))
	if h.Type != unix.NLMSG_ERROR {
		return nil
	}
	errno := *(*int32)(unsafe.Pointer(&b[unix.NLMSG_HDRLEN]))
	if errno == 0 {
		return nil
	}
	if errno < 0 {
		errno = -errno
	}
	return unix.Errno(errno)
}

func linkIndex(name string) (int, error) {
	ifi, err := stdnet.InterfaceByName(name)
	if err != nil {
		return 0, fmt.Errorf("link %s: %v", name, err)
	}
	return ifi.Index, nil
}

func LinkAddBridge(name string) error {
	payload := marshalIfInfo(unix.IfInfomsg{Family: unix.AF_UNSPEC})
	payload = nlaPutString(payload, unix.IFLA_IFNAME, name)
	inner := nlaPutString(nil, unix.IFLA_INFO_KIND, "bridge")
	payload = nlaPut(payload, unix.IFLA_LINKINFO|unix.NLA_F_NESTED, inner)
	err := nlRequest(unix.RTM_NEWLINK, unix.NLM_F_CREATE|unix.NLM_F_EXCL, payload)
	if isExist(err) {
		return nil
	}
	return err
}

func LinkAddVeth(host, peer string) error {
	payload := marshalIfInfo(unix.IfInfomsg{Family: unix.AF_UNSPEC})
	payload = nlaPutString(payload, unix.IFLA_IFNAME, host)

	peerInfo := marshalIfInfo(unix.IfInfomsg{Family: unix.AF_UNSPEC})
	peerInfo = nlaPutString(peerInfo, unix.IFLA_IFNAME, peer)
	infoData := nlaPut(nil, vethInfoPeer|unix.NLA_F_NESTED, peerInfo)
	linkinfo := nlaPutString(nil, unix.IFLA_INFO_KIND, "veth")
	linkinfo = nlaPut(linkinfo, unix.IFLA_INFO_DATA|unix.NLA_F_NESTED, infoData)
	payload = nlaPut(payload, unix.IFLA_LINKINFO|unix.NLA_F_NESTED, linkinfo)

	err := nlRequest(unix.RTM_NEWLINK, unix.NLM_F_CREATE|unix.NLM_F_EXCL, payload)
	if isExist(err) {
		return nil
	}
	return err
}

func LinkSetUp(name string) error {
	idx, err := linkIndex(name)
	if err != nil {
		return err
	}
	ifi := unix.IfInfomsg{
		Family: unix.AF_UNSPEC,
		Index:  int32(idx),
		Flags:  unix.IFF_UP,
		Change: unix.IFF_UP,
	}
	return nlRequest(unix.RTM_NEWLINK, 0, marshalIfInfo(ifi))
}

func LinkSetMaster(name, master string) error {
	idx, err := linkIndex(name)
	if err != nil {
		return err
	}
	midx, err := linkIndex(master)
	if err != nil {
		return err
	}
	payload := marshalIfInfo(unix.IfInfomsg{Family: unix.AF_UNSPEC, Index: int32(idx)})
	payload = nlaPutUint32(payload, unix.IFLA_MASTER, uint32(midx))
	return nlRequest(unix.RTM_NEWLINK, 0, payload)
}

func LinkSetNsFd(name string, nsFd int) error {
	idx, err := linkIndex(name)
	if err != nil {
		return err
	}
	payload := marshalIfInfo(unix.IfInfomsg{Family: unix.AF_UNSPEC, Index: int32(idx)})
	payload = nlaPutUint32(payload, unix.IFLA_NET_NS_FD, uint32(nsFd))
	return nlRequest(unix.RTM_NEWLINK, 0, payload)
}

func LinkDel(name string) error {
	idx, err := linkIndex(name)
	if err != nil {
		return nil
	}
	ifi := unix.IfInfomsg{Family: unix.AF_UNSPEC, Index: int32(idx)}
	err = nlRequest(unix.RTM_DELLINK, 0, marshalIfInfo(ifi))
	if err != nil && !errors.Is(err, unix.ENODEV) {
		return err
	}
	return nil
}

func AddrAdd(name, ip string, prefix uint8) error {
	idx, err := linkIndex(name)
	if err != nil {
		return err
	}
	ip4 := stdnet.ParseIP(ip).To4()
	if ip4 == nil {
		return fmt.Errorf("not IPv4: %s", ip)
	}
	msg := unix.IfAddrmsg{
		Family:    unix.AF_INET,
		Prefixlen: prefix,
		Scope:     unix.RT_SCOPE_UNIVERSE,
		Index:     uint32(idx),
	}
	payload := marshalIfAddr(msg)
	payload = nlaPut(payload, unix.IFA_LOCAL, ip4)
	payload = nlaPut(payload, unix.IFA_ADDRESS, ip4)
	err = nlRequest(unix.RTM_NEWADDR, unix.NLM_F_CREATE|unix.NLM_F_EXCL, payload)
	if isExist(err) {
		return nil
	}
	return err
}

func RouteAddDefault(dev, gateway string) error {
	idx, err := linkIndex(dev)
	if err != nil {
		return err
	}
	gw := stdnet.ParseIP(gateway).To4()
	if gw == nil {
		return fmt.Errorf("not IPv4: %s", gateway)
	}
	msg := unix.RtMsg{
		Family:   unix.AF_INET,
		Table:    unix.RT_TABLE_MAIN,
		Protocol: unix.RTPROT_BOOT,
		Scope:    unix.RT_SCOPE_UNIVERSE,
		Type:     unix.RTN_UNICAST,
	}
	payload := marshalRtMsg(msg)
	payload = nlaPut(payload, unix.RTA_GATEWAY, gw)
	payload = nlaPutUint32(payload, unix.RTA_OIF, uint32(idx))
	err = nlRequest(unix.RTM_NEWROUTE, unix.NLM_F_CREATE|unix.NLM_F_EXCL, payload)
	if isExist(err) {
		return nil
	}
	return err
}

// nlaPutStringLen is used by tests to check alignment without creating links.
func nlaPutStringLen(name string) int {
	return len(nlaPutString(nil, unix.IFLA_IFNAME, name))
}

func nlaTypeAt(b []byte) uint16 {
	if len(b) < 4 {
		return 0
	}
	return binary.LittleEndian.Uint16(b[2:])
}
