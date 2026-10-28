package netshim

import (
	"encoding/binary"
	"net"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// controlIPv4 is an IPv4 frame from the guest's control NIC to dst,
// carrying marker.
func controlIPv4(dst net.IP, marker string) []byte {
	frame := guardEthernet(guardGuestMAC, unix.ETH_P_IP)
	hdr := []byte{0x45, 0, 0, 0, 0, 0, 0, 0, 64, 253, 0, 0}
	binary.BigEndian.PutUint16(hdr[2:], uint16(20+len(marker)))
	frame = append(frame, hdr...)
	frame = append(frame, net.IPv4(169, 254, 112, 2).To4()...)
	frame = append(frame, dst.To4()...)
	return append(frame, marker...)
}

// TestGuardControlLink_OnlyReachesTheControlAddress is what keeps the
// control link from being a route: what the guest sends on it reaches
// this namespace only when it is addressed to the namespace's own end of
// the link (or is the ARP that takes), and never when it names the
// namespace as a gateway to somewhere else.
func TestGuardControlLink_OnlyReachesTheControlAddress(t *testing.T) {
	requireRoot(t)

	const (
		bridgeName = "ctlguard-br"
		tapName    = "ctlguard-tap"
	)
	t.Cleanup(func() {
		for _, n := range []string{tapName, bridgeName} {
			if link, err := netlink.LinkByName(n); err == nil {
				netlink.LinkDel(link)
			}
		}
	})

	ctlAddr := net.IPv4(169, 254, 112, 1).To4()
	_, ctlNet, err := net.ParseCIDR("169.254.112.1/24")
	if err != nil {
		t.Fatalf("ParseCIDR: %v", err)
	}
	if err := ensureBridge(bridgeName, ctlAddr, ctlNet); err != nil {
		t.Fatalf("ensureBridge: %v", err)
	}
	if err := ensureTap(tapName, bridgeName, 0, 0); err != nil {
		t.Fatalf("ensureTap: %v", err)
	}
	// Twice, as Setup may be: the second run has to converge on the
	// same filters rather than stacking a second set.
	for i := 0; i < 2; i++ {
		if err := guardControlLink(tapName, ctlAddr); err != nil {
			t.Fatalf("guardControlLink (run %d): %v", i+1, err)
		}
	}
	tapLink, err := netlink.LinkByName(tapName)
	if err != nil {
		t.Fatalf("looking up %s: %v", tapName, err)
	}
	filters, err := netlink.FilterList(tapLink, netlink.MakeHandle(0xffff, 0))
	if err != nil {
		t.Fatalf("listing filters on %s: %v", tapName, err)
	}
	if len(filters) != 3 {
		t.Errorf("%s has %d ingress filters after two runs, want 3 (two allows and a drop)", tapName, len(filters))
	}

	bridge, err := netlink.LinkByName(bridgeName)
	if err != nil {
		t.Fatalf("looking up %s: %v", bridgeName, err)
	}
	tapFD := openTapFD(t, tapName)
	brFD := openPacketSocket(t, bridge.Attrs().Index)
	waitSpliceReady(t, nil, []string{tapName, bridgeName})

	forged := map[string][]byte{
		"routed-ipv4":    controlIPv4(net.IPv4(10, 0, 0, 1), "routed-ipv4"),
		"routed-imds":    controlIPv4(net.IPv4(169, 254, 169, 254), "routed-imds"),
		"control-ipv6":   append(append(guardEthernet(guardGuestMAC, unix.ETH_P_IPV6), make([]byte, 40)...), "control-ipv6"...),
		"control-other":  append(guardEthernet(guardGuestMAC, 0x88b5), []byte("control-other")...),
		"routed-ipv4-bc": controlIPv4(net.IPv4(169, 254, 112, 255), "routed-ipv4-bc"),
	}
	allowed := map[string][]byte{
		"to-control-addr": controlIPv4(ctlAddr, "to-control-addr"),
		"control-arp":     guardARP(guardGuestMAC, guardGuestMAC, net.IPv4(169, 254, 112, 2).To4(), "control-arp"),
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
		leaked, ok := awaitFrameNoneOf(brFD, send, []byte(marker), forged)
		if !ok {
			t.Errorf("%s written to tap %s never arrived on %s\n%s",
				marker, tapName, bridgeName, describeLinks(tapName, bridgeName))
		}
		for _, l := range leaked {
			t.Errorf("%s reached %s; the control link guard should have dropped it", l, bridgeName)
		}
	}
}
