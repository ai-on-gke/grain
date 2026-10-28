package netshim

import (
	"encoding/binary"
	"net"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

var (
	guardGuestIP6 = net.ParseIP("2001:db8:1::5")
	guardGuestLL6 = net.ParseIP("fe80::42:acff:fe11:5")
	// What a hostile guest wants to be on IPv6: the router, which on
	// most segments speaks from a link-local address, a neighbouring
	// sandbox, and a link-local address that is not its own.
	guardGatewayLL6 = net.ParseIP("fe80::1")
	guardPeerIP6    = net.ParseIP("2001:db8:1::6")
	guardForeignLL6 = net.ParseIP("fe80::dead:beef")
	// The multicast groups NDP and MLD send to. The guard does not look
	// at the destination; these are only there to make the frames real.
	allNodes6   = net.ParseIP("ff02::1")
	allRouters6 = net.ParseIP("ff02::2")
	mldv2Group6 = net.ParseIP("ff02::16")
)

// guardIPv6 is an IPv6 frame from srcMAC/src to dst whose first next
// header is nh and whose payload is payload. marker goes in the Ethernet
// trailer, past the payload length, so it can be added to any frame --
// an option-less NA included -- without changing what the guard sees.
func guardIPv6(srcMAC net.HardwareAddr, src, dst net.IP, nh uint8, payload []byte, marker string) []byte {
	frame := guardEthernet(srcMAC, unix.ETH_P_IPV6)
	frame = append(frame, 0x60, 0, 0, 0)
	frame = binary.BigEndian.AppendUint16(frame, uint16(len(payload)))
	frame = append(frame, nh, 255)
	frame = append(frame, src.To16()...)
	frame = append(frame, dst.To16()...)
	frame = append(frame, payload...)
	return append(frame, marker...)
}

// icmp6 is an ICMPv6 message of type typ whose body (after the 4-byte
// type, code and checksum) is body. The checksum is left zero: nothing on
// the receiving side of the test checks it, and the guard does not.
func icmp6(typ uint8, body ...[]byte) []byte {
	msg := []byte{typ, 0, 0, 0}
	for _, b := range body {
		msg = append(msg, b...)
	}
	return msg
}

// ndOpt is a link-layer address NDP option (1 source, 2 target).
func ndOpt(typ uint8, mac net.HardwareAddr) []byte {
	return append([]byte{typ, 1}, mac...)
}

// neighAdvert is an NA with the override flag for target, with opts.
func neighAdvert(target net.IP, opts ...[]byte) []byte {
	return icmp6(icmp6NeighAdvert, append([][]byte{{0x20, 0, 0, 0}, target.To16()}, opts...)...)
}

// neighSolicit is an NS for target, with opts.
func neighSolicit(target net.IP, opts ...[]byte) []byte {
	return icmp6(icmp6NeighSolicit, append([][]byte{{0, 0, 0, 0}, target.To16()}, opts...)...)
}

// extHeader puts payload behind an 8-byte extension header whose next
// header is next, carrying a router alert option and padding, the way MLD sends it.
func extHeader(next uint8, payload []byte) []byte {
	return append([]byte{next, 0, 5, 2, 0, 0, 1, 0}, payload...)
}

// mldv2Report is an MLDv2 report joining one solicited-node group.
func mldv2Report() []byte {
	record := append([]byte{4, 0, 0, 0}, net.ParseIP("ff02::1:ff11:5").To16()...)
	return icmp6(icmp6MLDv2Report, []byte{0, 0, 0, 1}, record)
}

// mldv1Report is an MLDv1 report for the same group.
func mldv1Report() []byte {
	return icmp6(icmp6MLDv1Report, []byte{0, 0, 0, 0}, net.ParseIP("ff02::1:ff11:5").To16())
}

// udp6 is a UDP datagram to the discard port.
func udp6() []byte {
	return []byte{0x30, 0x39, 0, 9, 0, 8, 0, 0}
}

// guardRig is a veth pair standing in for the CNI, and a tap standing in
// for the guest's NIC, for spliceGuest to join.
type guardRig struct {
	veth, peer, tap netlink.Link
	tapFD, peerFD   int
}

func newGuardRig(t *testing.T, vethName, peerName, tapName string) guardRig {
	t.Helper()
	t.Cleanup(func() {
		for _, n := range []string{vethName, tapName} {
			if link, err := netlink.LinkByName(n); err == nil {
				netlink.LinkDel(link)
			}
		}
	})
	if err := netlink.LinkAdd(&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: vethName}, PeerName: peerName}); err != nil {
		t.Fatalf("creating veth pair: %v", err)
	}
	var r guardRig
	var err error
	if r.veth, err = netlink.LinkByName(vethName); err != nil {
		t.Fatalf("looking up %s: %v", vethName, err)
	}
	if r.peer, err = netlink.LinkByName(peerName); err != nil {
		t.Fatalf("looking up %s: %v", peerName, err)
	}
	if r.tap, err = ensureTapMTU(tapName, r.veth.Attrs().MTU, 0, 0); err != nil {
		t.Fatalf("ensureTapMTU: %v", err)
	}
	for _, l := range []netlink.Link{r.veth, r.peer} {
		if err := netlink.LinkSetUp(l); err != nil {
			t.Fatalf("bringing up %s: %v", l.Attrs().Name, err)
		}
	}
	return r
}

// splice runs spliceGuest twice, as Setup may be run, checks that it
// converged on wantFilters filters on the tap, and opens both ends.
func (r *guardRig) splice(t *testing.T, id Identity, wantFilters int) []netlink.Filter {
	t.Helper()
	for i := 0; i < 2; i++ {
		if err := spliceGuest(r.veth, r.tap, id); err != nil {
			t.Fatalf("spliceGuest (run %d): %v", i+1, err)
		}
	}
	filters, err := netlink.FilterList(r.tap, netlink.MakeHandle(0xffff, 0))
	if err != nil {
		t.Fatalf("listing filters on %s: %v", r.tap.Attrs().Name, err)
	}
	if len(filters) != wantFilters {
		t.Errorf("%s has %d ingress filters after two runs, want %d", r.tap.Attrs().Name, len(filters), wantFilters)
	}
	r.tapFD = openTapFD(t, r.tap.Attrs().Name)
	r.peerFD = openPacketSocket(t, r.peer.Attrs().Index)
	waitSpliceReady(t, []splicedPair{{from: r.veth.Attrs().Name, to: r.tap.Attrs().Name}},
		[]string{r.tap.Attrs().Name, r.peer.Attrs().Name})
	return filters
}

// check is the forged-first, legitimate-last method of
// TestSpliceGuest_HoldsGuestToItsIdentity: for each allowed frame, every
// forged one is written to the tap and then the allowed one, and once the
// allowed one has reached the peer, no forged one may have.
func (r *guardRig) check(t *testing.T, forged, allowed map[string][]byte) {
	t.Helper()
	for marker, frame := range allowed {
		send := func() error {
			for _, f := range forged {
				if _, err := unix.Write(r.tapFD, pad(f)); err != nil {
					return err
				}
			}
			_, err := unix.Write(r.tapFD, pad(frame))
			return err
		}
		leaked, ok := awaitFrameNoneOf(r.peerFD, send, []byte(marker), forged)
		if !ok {
			t.Errorf("%s written to tap %s never arrived on %s\n%s", marker, r.tap.Attrs().Name,
				r.peer.Attrs().Name, describeLinks(r.tap.Attrs().Name, r.veth.Attrs().Name, r.peer.Attrs().Name))
		}
		for _, l := range leaked {
			t.Errorf("%s reached %s; the guard should have dropped it", l, r.peer.Attrs().Name)
		}
	}
}

// forgedIPv6 is every frame a guest with the IPv6 identity
// guardGuestIP6/guardGuestLL6 must not get onto the segment.
func forgedIPv6() map[string][]byte {
	me, ll, mac := guardGuestIP6, guardGuestLL6, guardGuestMAC
	ra := icmp6(134, []byte{64, 0, 0x07, 0x08}, make([]byte, 8), ndOpt(ndOptSourceLLAddr, mac))
	f := map[string][]byte{
		// Neighbour advertisement spoofing: the router's address, from
		// the router's address or our own, with or without options.
		"forged-na-gateway": guardIPv6(mac, guardGatewayLL6, allNodes6, ipv6ICMP,
			neighAdvert(guardGatewayLL6, ndOpt(ndOptTargetLLAddr, mac)), "forged-na-gateway"),
		"forged-na-gw-ours": guardIPv6(mac, ll, allNodes6, ipv6ICMP,
			neighAdvert(guardGatewayLL6, ndOpt(ndOptTargetLLAddr, mac)), "forged-na-gw-ours"),
		"forged-na-gw-noopt": guardIPv6(mac, ll, allNodes6, ipv6ICMP,
			neighAdvert(guardGatewayLL6), "forged-na-gw-noopt"),
		"forged-na-peer": guardIPv6(mac, me, allNodes6, ipv6ICMP,
			neighAdvert(guardPeerIP6, ndOpt(ndOptTargetLLAddr, mac)), "forged-na-peer"),
		// Our own address, pointed at another sandbox's MAC.
		"forged-na-tlla": guardIPv6(mac, me, allNodes6, ipv6ICMP,
			neighAdvert(me, ndOpt(ndOptTargetLLAddr, guardPeerMAC)), "forged-na-tlla"),
		// Everything right but the Ethernet source.
		"forged-na-ethsrc": guardIPv6(guardPeerMAC, me, allNodes6, ipv6ICMP,
			neighAdvert(me, ndOpt(ndOptTargetLLAddr, mac)), "forged-na-ethsrc"),
		// NS, which plants the sender in the receiver's cache.
		"forged-ns-src": guardIPv6(mac, guardPeerIP6, allNodes6, ipv6ICMP,
			neighSolicit(guardGatewayLL6, ndOpt(ndOptSourceLLAddr, mac)), "forged-ns-src"),
		"forged-ns-slla": guardIPv6(mac, me, allNodes6, ipv6ICMP,
			neighSolicit(guardGatewayLL6, ndOpt(ndOptSourceLLAddr, guardPeerMAC)), "forged-ns-slla"),
		// Router messages, from the guest's own link-local address,
		// which is where a router speaks from.
		"forged-ra-plain": guardIPv6(mac, ll, allNodes6, ipv6ICMP, ra, "forged-ra-plain"),
		"forged-rs":       guardIPv6(mac, ll, allRouters6, ipv6ICMP, icmp6(133, make([]byte, 4)), "forged-rs"),
		"forged-redirect": guardIPv6(mac, ll, allNodes6, ipv6ICMP, icmp6(137, make([]byte, 4), guardGatewayLL6.To16(), guardPeerIP6.To16()), "forged-redirect"),
		// The RA again, moved off the fixed offset by extension headers.
		"forged-ra-hbh":     guardIPv6(mac, ll, allNodes6, ipv6HopByHop, extHeader(ipv6ICMP, ra), "forged-ra-hbh"),
		"forged-ra-dstopts": guardIPv6(mac, ll, allNodes6, ipv6DestOptions, extHeader(ipv6ICMP, ra), "forged-ra-dstopts"),
		"forged-ra-routing": guardIPv6(mac, ll, allNodes6, ipv6Routing, extHeader(ipv6ICMP, ra), "forged-ra-routing"),
		// An MLD report is only let through from the link-local address
		// or ::, and the hop-by-hop header otherwise is not.
		"forged-mld-global": guardIPv6(mac, me, mldv2Group6, ipv6HopByHop, extHeader(ipv6ICMP, mldv2Report()), "forged-mld-global"),
		// Anybody else's address.
		"forged-udp-peer":  guardIPv6(mac, guardPeerIP6, guardGatewayLL6, unix.IPPROTO_UDP, udp6(), "forged-udp-peer"),
		"forged-udp-ll":    guardIPv6(mac, guardForeignLL6, guardGatewayLL6, unix.IPPROTO_UDP, udp6(), "forged-udp-ll"),
		"forged-udp-mac":   guardIPv6(guardPeerMAC, me, guardGatewayLL6, unix.IPPROTO_UDP, udp6(), "forged-udp-mac"),
		"forged-udp-unspc": guardIPv6(mac, net.IPv6unspecified, guardGatewayLL6, unix.IPPROTO_UDP, udp6(), "forged-udp-unspc"),
		"forged-vlan":      append(guardEthernet(mac, unix.ETH_P_8021Q), []byte("forged-vlan")...),
	}
	return f
}

// allowedIPv6 is every frame a guest with that identity has to be able
// to send.
func allowedIPv6() map[string][]byte {
	me, ll, mac := guardGuestIP6, guardGuestLL6, guardGuestMAC
	frag := append([]byte{unix.IPPROTO_UDP, 0, 0, 1, 0, 0, 0x12, 0x34}, udp6()...)
	return map[string][]byte{
		"allowed-na-global": guardIPv6(mac, me, allNodes6, ipv6ICMP,
			neighAdvert(me, ndOpt(ndOptTargetLLAddr, mac)), "allowed-na-global"),
		"allowed-na-ll": guardIPv6(mac, ll, allNodes6, ipv6ICMP,
			neighAdvert(ll, ndOpt(ndOptTargetLLAddr, mac)), "allowed-na-ll"),
		// Linux's answer to a unicast NS carries no options.
		"allowed-na-noopt": guardIPv6(mac, ll, guardGatewayLL6, ipv6ICMP,
			neighAdvert(ll), "allowed-na-noopt"),
		"allowed-ns-global": guardIPv6(mac, me, allNodes6, ipv6ICMP,
			neighSolicit(guardGatewayLL6, ndOpt(ndOptSourceLLAddr, mac)), "allowed-ns-global"),
		"allowed-ns-ll": guardIPv6(mac, ll, allNodes6, ipv6ICMP,
			neighSolicit(guardGatewayLL6, ndOpt(ndOptSourceLLAddr, mac)), "allowed-ns-ll"),
		"allowed-dad": guardIPv6(mac, net.IPv6unspecified, net.ParseIP("ff02::1:ff00:5"), ipv6ICMP,
			neighSolicit(me), "allowed-dad"),
		"allowed-mldv2-ll": guardIPv6(mac, ll, mldv2Group6, ipv6HopByHop,
			extHeader(ipv6ICMP, mldv2Report()), "allowed-mldv2-ll"),
		"allowed-mldv2-unspec": guardIPv6(mac, net.IPv6unspecified, mldv2Group6, ipv6HopByHop,
			extHeader(ipv6ICMP, mldv2Report()), "allowed-mldv2-unspec"),
		"allowed-mldv1": guardIPv6(mac, ll, net.ParseIP("ff02::1:ff11:5"), ipv6HopByHop,
			extHeader(ipv6ICMP, mldv1Report()), "allowed-mldv1"),
		"allowed-udp-global": guardIPv6(mac, me, guardGatewayLL6, unix.IPPROTO_UDP, udp6(), "allowed-udp-global"),
		"allowed-udp-ll":     guardIPv6(mac, ll, guardGatewayLL6, unix.IPPROTO_UDP, udp6(), "allowed-udp-ll"),
		"allowed-udp-frag":   guardIPv6(mac, me, guardGatewayLL6, 44, frag, "allowed-udp-frag"),
		"allowed-echo":       guardIPv6(mac, me, guardGatewayLL6, ipv6ICMP, icmp6(128, []byte{0, 1, 0, 1}), "allowed-echo"),
	}
}

// TestSpliceGuest_HoldsGuestToItsIdentityV6 is the IPv6 half of
// TestSpliceGuest_HoldsGuestToItsIdentity, by the same method: a guest
// with an IPv6 identity can send as that identity, take part in
// neighbour discovery and MLD for it, and nothing else -- no NA or NS for
// anybody else's address or MAC, no router messages, nothing hidden
// behind an extension header, and nothing from an address that is not
// its own.
func TestSpliceGuest_HoldsGuestToItsIdentityV6(t *testing.T) {
	requireRoot(t)

	t.Run("v6-only", func(t *testing.T) {
		r := newGuardRig(t, "guard6-vm", "guard6-net", "guard6-tap")
		// Per address (global, link-local): NA, option-less NA, NS. Then
		// the DAD probe, MLDv2 and v1 from link-local and ::, 11 refused
		// ICMPv6 types, 3 extension headers, one source match per
		// address, and the final drop.
		r.splice(t, Identity{MAC: guardGuestMAC, IP6: guardGuestIP6, LinkLocal6: guardGuestLL6}, 6+1+4+11+3+2+1)

		forged := forgedIPv6()
		// A v6-only identity has no IPv4 half, so no IPv4 or ARP at all.
		forged["forged-v4"] = guardIPv4(guardGuestMAC, guardGuestIP, "forged-v4")
		forged["forged-arp-own"] = guardARP(guardGuestMAC, guardGuestMAC, guardGuestIP, "forged-arp-own")
		forged["forged-arp-probe"] = guardARP(guardGuestMAC, guardGuestMAC, net.IPv4zero.To4(), "forged-arp-probe")
		r.check(t, forged, allowedIPv6())
	})

	t.Run("dual-stack", func(t *testing.T) {
		r := newGuardRig(t, "guard46-vm", "guard46-net", "guard46-tap")
		r.splice(t, Identity{MAC: guardGuestMAC, IP: guardGuestIP, IP6: guardGuestIP6, LinkLocal6: guardGuestLL6},
			3+6+1+4+11+3+2+1)

		forged := forgedIPv6()
		forged["forged-arp-gateway"] = guardARP(guardGuestMAC, guardGuestMAC, guardGatewayIP, "forged-arp-gateway")
		forged["forged-ip-src"] = guardIPv4(guardGuestMAC, guardGatewayIP, "forged-ip-src")
		allowed := allowedIPv6()
		allowed["allowed-ip"] = guardIPv4(guardGuestMAC, guardGuestIP, "allowed-ip")
		allowed["allowed-arp"] = guardARP(guardGuestMAC, guardGuestMAC, guardGuestIP, "allowed-arp")
		r.check(t, forged, allowed)
	})

	t.Run("no link-local", func(t *testing.T) {
		r := newGuardRig(t, "guard6n-vm", "guard6n-net", "guard6n-tap")
		r.splice(t, Identity{MAC: guardGuestMAC, IP6: guardGuestIP6}, 3+1+2+11+3+1+1)

		// Without a link-local address in the identity, the guest's own
		// link-local is as foreign as anybody's; only :: is left for MLD.
		forged := forgedIPv6()
		allowed := allowedIPv6()
		for _, m := range []string{"allowed-na-ll", "allowed-na-noopt", "allowed-ns-ll", "allowed-mldv2-ll", "allowed-mldv1", "allowed-udp-ll"} {
			forged[m] = allowed[m]
			delete(allowed, m)
		}
		r.check(t, forged, allowed)
	})
}

// TestSpliceGuest_IPv4IdentityDropsAllIPv6 is the regression test for
// the rule that keeps the IPv6 guard from being opened carelessly: an
// identity without an IPv6 half gets no IPv6 filter at all, so on a
// segment that carries IPv6 every 0x86DD frame the guest sends is still
// dropped -- including ones the IPv6 guard would let through for an
// identity that had those addresses.
func TestSpliceGuest_IPv4IdentityDropsAllIPv6(t *testing.T) {
	requireRoot(t)

	r := newGuardRig(t, "guard4-vm", "guard4-net", "guard4-tap")
	// The segment carries IPv6: the far side of the veth has the
	// addresses a dual-stack network would give its gateway.
	for _, s := range []string{"fe80::1/64", "2001:db8:1::1/64"} {
		a, err := netlink.ParseAddr(s)
		if err != nil {
			t.Fatalf("ParseAddr(%q): %v", s, err)
		}
		a.Flags |= unix.IFA_F_NODAD
		if err := netlink.AddrAdd(r.peer, a); err != nil {
			t.Fatalf("adding %s to %s: %v", s, r.peer.Attrs().Name, err)
		}
	}

	filters := r.splice(t, Identity{MAC: guardGuestMAC, IP: guardGuestIP}, 4)
	for _, f := range filters {
		if p := f.Attrs().Protocol; p == unix.ETH_P_IPV6 {
			t.Errorf("a v4-only identity has an IPv6 filter: %s", describeFilter(f))
		}
	}

	forged := forgedIPv6()
	for m, f := range allowedIPv6() {
		forged["v4only-"+m] = append(f[:len(f)-len(m):len(f)-len(m)], "v4only-"+m...)
	}
	r.check(t, forged, map[string][]byte{
		"allowed-ip": guardIPv4(guardGuestMAC, guardGuestIP, "allowed-ip"),
	})
}
