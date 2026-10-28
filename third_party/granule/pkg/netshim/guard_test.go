package netshim

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

var (
	guardGuestMAC = net.HardwareAddr{0x02, 0x42, 0xac, 0x11, 0x00, 0x05}
	guardGuestIP  = net.IPv4(172, 17, 0, 5).To4()
	// What a hostile guest wants to be: the bridge's gateway, and a
	// neighbouring sandbox.
	guardGatewayIP = net.IPv4(172, 17, 0, 1).To4()
	guardPeerMAC   = net.HardwareAddr{0x02, 0x42, 0xac, 0x11, 0x00, 0x06}
)

// guardEthernet is an Ethernet header from src, broadcast.
func guardEthernet(src net.HardwareAddr, ethertype uint16) []byte {
	frame := []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	frame = append(frame, src...)
	return binary.BigEndian.AppendUint16(frame, ethertype)
}

// guardIPv4 is an IPv4 frame from srcMAC/srcIP carrying marker. The
// protocol is 253, reserved for experimentation, so nothing on the
// receiving side tries to make sense of it.
func guardIPv4(srcMAC net.HardwareAddr, srcIP net.IP, marker string) []byte {
	frame := guardEthernet(srcMAC, unix.ETH_P_IP)
	hdr := []byte{0x45, 0, 0, 0, 0, 0, 0, 0, 64, 253, 0, 0}
	binary.BigEndian.PutUint16(hdr[2:], uint16(20+len(marker)))
	frame = append(frame, hdr...)
	frame = append(frame, srcIP...)
	frame = append(frame, guardGatewayIP...)
	return append(frame, marker...)
}

// guardARP is an ARP reply sent from ethSrc, claiming that spa is at sha,
// with marker in the trailer after it.
func guardARP(ethSrc, sha net.HardwareAddr, spa net.IP, marker string) []byte {
	frame := guardEthernet(ethSrc, unix.ETH_P_ARP)
	frame = append(frame, 0x00, 0x01, 0x08, 0x00, 6, 4, 0x00, 0x02)
	frame = append(frame, sha...)
	frame = append(frame, spa...)
	frame = append(frame, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff)
	frame = append(frame, guardGatewayIP...)
	return append(frame, marker...)
}

// TestSpliceGuest_HoldsGuestToItsIdentity is the ARP-spoofing guarantee
// itself: on the guest's side of the splice, what speaks as the guest's
// own MAC and address reaches the network, and what claims to be anybody
// else -- the gateway, a neighbour, a forged source MAC -- never does.
//
// Proving a negative needs something to wait for, so every round writes
// the forged frames to the tap first and a legitimate one last. The
// splice is a single in-order path, so a forged frame the filters let
// through arrives before the legitimate one does; once that one has
// arrived, anything forged that has not is dropped rather than late.
func TestSpliceGuest_HoldsGuestToItsIdentity(t *testing.T) {
	requireRoot(t)

	const (
		vethName = "guard-vm"
		peerName = "guard-net"
		tapName  = "guard-tap"
	)

	t.Cleanup(func() {
		for _, n := range []string{vethName, tapName} {
			if link, err := netlink.LinkByName(n); err == nil {
				netlink.LinkDel(link)
			}
		}
	})

	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: vethName},
		PeerName:  peerName,
	}
	if err := netlink.LinkAdd(veth); err != nil {
		t.Fatalf("creating veth pair: %v", err)
	}
	vethLink, err := netlink.LinkByName(vethName)
	if err != nil {
		t.Fatalf("looking up %s: %v", vethName, err)
	}
	peerLink, err := netlink.LinkByName(peerName)
	if err != nil {
		t.Fatalf("looking up %s: %v", peerName, err)
	}
	tapLink, err := ensureTapMTU(tapName, vethLink.Attrs().MTU, 0, 0)
	if err != nil {
		t.Fatalf("ensureTapMTU: %v", err)
	}
	for _, l := range []netlink.Link{vethLink, peerLink} {
		if err := netlink.LinkSetUp(l); err != nil {
			t.Fatalf("bringing up %s: %v", l.Attrs().Name, err)
		}
	}

	id := Identity{MAC: guardGuestMAC, IP: guardGuestIP}
	// Twice, as Setup may be: the second run has to converge on the
	// same filters rather than stacking a second set.
	for i := 0; i < 2; i++ {
		if err := spliceGuest(vethLink, tapLink, id); err != nil {
			t.Fatalf("spliceGuest (run %d): %v", i+1, err)
		}
	}
	filters, err := netlink.FilterList(tapLink, netlink.MakeHandle(0xffff, 0))
	if err != nil {
		t.Fatalf("listing filters on %s: %v", tapName, err)
	}
	if len(filters) != 4 {
		t.Errorf("%s has %d ingress filters after two runs, want 4 (three allows and a drop)", tapName, len(filters))
	}

	tapFD := openTapFD(t, tapName)
	peerFD := openPacketSocket(t, peerLink.Attrs().Index)
	waitSpliceReady(t, []splicedPair{{from: vethName, to: tapName}}, []string{tapName, peerName})

	forged := map[string][]byte{
		"forged-arp-gateway": guardARP(guardGuestMAC, guardGuestMAC, guardGatewayIP, "forged-arp-gateway"),
		"forged-arp-sha":     guardARP(guardGuestMAC, guardPeerMAC, guardGuestIP, "forged-arp-sha"),
		"forged-arp-ethsrc":  guardARP(guardPeerMAC, guardPeerMAC, guardGuestIP, "forged-arp-ethsrc"),
		"forged-ip-src":      guardIPv4(guardGuestMAC, guardGatewayIP, "forged-ip-src"),
		"forged-ip-mac":      guardIPv4(guardPeerMAC, guardGuestIP, "forged-ip-mac"),
		"forged-ipv6":        append(guardEthernet(guardGuestMAC, unix.ETH_P_IPV6), make([]byte, 40)...),
		"forged-other":       append(guardEthernet(guardGuestMAC, 0x88b5), []byte("forged-other")...),
	}
	// The IPv6 frame carries no marker of its own; give it one.
	forged["forged-ipv6"] = append(forged["forged-ipv6"], "forged-ipv6"...)

	allowed := map[string][]byte{
		"allowed-ip":        guardIPv4(guardGuestMAC, guardGuestIP, "allowed-ip"),
		"allowed-arp":       guardARP(guardGuestMAC, guardGuestMAC, guardGuestIP, "allowed-arp"),
		"allowed-arp-probe": guardARP(guardGuestMAC, guardGuestMAC, net.IPv4zero.To4(), "allowed-arp-probe"),
	}

	for marker, frame := range allowed {
		send := func() error {
			for _, f := range forged {
				if _, err := unix.Write(tapFD, pad(f)); err != nil {
					return err
				}
			}
			_, err := unix.Write(tapFD, pad(frame))
			return err
		}
		leaked, ok := awaitFrameNoneOf(peerFD, send, []byte(marker), forged)
		if !ok {
			t.Errorf("%s written to tap %s never arrived on %s\n%s",
				marker, tapName, peerName, describeLinks(tapName, vethName, peerName))
		}
		for _, l := range leaked {
			t.Errorf("%s reached %s; the guard should have dropped it", l, peerName)
		}
	}
}

// pad pads frame to the 60-byte Ethernet minimum.
func pad(frame []byte) []byte {
	for len(frame) < 60 {
		frame = append(frame, 0)
	}
	return frame
}

// awaitFrameNoneOf is awaitFrame that also reports which of forbidden's
// markers turned up on fd before want did.
func awaitFrameNoneOf(fd int, send func() error, want []byte, forbidden map[string][]byte) ([]string, bool) {
	seen := map[string]bool{}
	buf := make([]byte, 2048)
	deadline := time.Now().Add(spliceWaitBudget)
	var nextSend time.Time
	report := func() []string {
		var out []string
		for m := range seen {
			out = append(out, m)
		}
		return out
	}

	for time.Now().Before(deadline) {
		if now := time.Now(); !now.Before(nextSend) {
			switch err := send(); {
			case err == nil:
				nextSend = now.Add(spliceResendInterval)
			case transient(err):
				time.Sleep(2 * time.Millisecond)
				continue
			default:
				return report(), false
			}
		}
		n, err := unix.Read(fd, buf)
		switch {
		case transient(err):
			time.Sleep(2 * time.Millisecond)
			continue
		case err != nil:
			return report(), false
		}
		for m := range forbidden {
			if bytes.Contains(buf[:n], []byte(m)) {
				seen[m] = true
			}
		}
		if bytes.Contains(buf[:n], want) {
			return report(), true
		}
	}
	return report(), false
}
