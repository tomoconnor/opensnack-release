// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ec2

import (
	"encoding/binary"
	"fmt"
	"net/netip"
)

// parseCIDR accepts only canonical IPv4 CIDRs ("10.0.0.0/16", not
// "10.0.0.1/16"), as EC2 does.
func parseCIDR(s string) (netip.Prefix, bool) {
	p, err := netip.ParsePrefix(s)
	if err != nil || !p.Addr().Is4() || p.Masked() != p {
		return netip.Prefix{}, false
	}
	return p, true
}

func overlaps(a, b netip.Prefix) bool { return a.Overlaps(b) }

// contains reports whether outer contains all of inner.
func contains(outer, inner netip.Prefix) bool {
	return outer.Bits() <= inner.Bits() && outer.Contains(inner.Addr())
}

func ip4(a netip.Addr) uint32 {
	b := a.As4()
	return binary.BigEndian.Uint32(b[:])
}

func addr4(n uint32) netip.Addr {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], n)
	return netip.AddrFrom4(b)
}

// subnetSize is the number of addresses in an IPv4 prefix.
func subnetSize(p netip.Prefix) int { return 1 << (32 - p.Bits()) }

// reservedInSubnet is how many addresses AWS keeps in every subnet: the
// network address, the VPC router, DNS, one for future use, and broadcast.
const reservedInSubnet = 5

// usable reports whether ip can be assigned to an interface in the subnet:
// inside it, and not one of the first four or the last address.
func usable(subnet netip.Prefix, ip netip.Addr) bool {
	if !ip.Is4() || !subnet.Contains(ip) {
		return false
	}
	off := ip4(ip) - ip4(subnet.Addr())
	return off >= 4 && int(off) < subnetSize(subnet)-1
}

// allocateIP returns the lowest usable address in subnet that isn't in used.
func allocateIP(subnet netip.Prefix, used map[string]bool) (netip.Addr, error) {
	base := ip4(subnet.Addr())
	for off := uint32(4); int(off) < subnetSize(subnet)-1; off++ {
		a := addr4(base + off)
		if !used[a.String()] {
			return a, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("no free addresses in %s", subnet)
}
