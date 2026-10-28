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

func guardIPv4To(srcMAC net.HardwareAddr, srcIP, dstIP net.IP, marker string) []byte {
	frame := guardEthernet(srcMAC, unix.ETH_P_IP)
	hdr := []byte{0x45, 0, 0, 0, 0, 0, 0, 0, 64, 253, 0, 0}
	binary.BigEndian.PutUint16(hdr[2:], uint16(20+len(marker)))
	frame = append(frame, hdr...)
	frame = append(frame, srcIP.To4()...)
	frame = append(frame, dstIP.To4()...)
	return append(frame, marker...)
}

func guardARPFor(srcMAC net.HardwareAddr, spa, tpa net.IP, op uint16, marker string) []byte {
	frame := guardEthernet(srcMAC, unix.ETH_P_ARP)
	frame = append(frame, 0x00, 0x01, 0x08, 0x00, 6, 4)
	frame = binary.BigEndian.AppendUint16(frame, op)
	frame = append(frame, srcMAC...)
	frame = append(frame, spa.To4()...)
	frame = append(frame, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff)
	frame = append(frame, tpa.To4()...)
	return append(frame, marker...)
}

// TestSpliceGuestWithControllers_BlocksGuestAndIsolatesControllerTraffic proves
// hard security requirement 1 at the packet filter layer:
//   - Even when the guest has open egress on its spliced NIC (it can send to
//     guardGatewayIP), any IPv4 packet or ARP request the guest sends toward
//     controllerIP is dropped at tap ingress and never reaches the network.
//   - Any IPv4 packet or ARP frame arriving from controllerIP on ext is passed
//     to the host/pod stack and never redirected into the guest's tap.
func TestSpliceGuestWithControllers_BlocksGuestAndIsolatesControllerTraffic(t *testing.T) {
	requireRoot(t)

	const (
		vethName = "ctl-ext"
		peerName = "ctl-peer"
		tapName  = "ctl-guest-tap"
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

	controllerIP := net.IPv4(172, 17, 0, 99).To4()
	id := Identity{MAC: guardGuestMAC, IP: guardGuestIP}
	if err := spliceGuestWithControllers(vethLink, tapLink, id, []net.IP{controllerIP}); err != nil {
		t.Fatalf("spliceGuestWithControllers: %v", err)
	}

	tapFD := openTapFD(t, tapName)
	peerFD := openPacketSocket(t, peerLink.Attrs().Index)
	waitSpliceReady(t, nil, []string{vethName, tapName, peerName})

	// 1. Guest -> network: traffic to controllerIP (IPv4 and ARP) must be
	// dropped at tap ingress, while legitimate traffic to guardGatewayIP passes.
	blockedFromGuest := map[string][]byte{
		"guest-to-controller-ip":  guardIPv4To(guardGuestMAC, guardGuestIP, controllerIP, "guest-to-controller-ip"),
		"guest-to-controller-arp": guardARPFor(guardGuestMAC, guardGuestIP, controllerIP, 1, "guest-to-controller-arp"),
	}
	allowedFromGuest := guardIPv4To(guardGuestMAC, guardGuestIP, guardGatewayIP, "guest-to-gateway-ok")

	sendFromGuest := func() error {
		for _, f := range blockedFromGuest {
			if _, err := unix.Write(tapFD, pad(f)); err != nil {
				return err
			}
		}
		_, err := unix.Write(tapFD, pad(allowedFromGuest))
		return err
	}
	leakedToNet, ok := awaitFrameNoneOf(peerFD, sendFromGuest, []byte("guest-to-gateway-ok"), blockedFromGuest)
	if !ok {
		t.Fatalf("allowed guest frame never reached %s", peerName)
	}
	for _, l := range leakedToNet {
		t.Errorf("guest frame %s reached %s; tap ingress filter should have dropped it", l, peerName)
	}

	// 2. Network -> guest: frames from controllerIP arriving on ext must be
	// passed to the host stack (not redirected to tap), while frames from
	// guardGatewayIP still reach tap.
	hiddenFromGuest := map[string][]byte{
		"controller-to-pod-ip":  guardIPv4To(guardPeerMAC, controllerIP, guardGuestIP, "controller-to-pod-ip"),
		"controller-to-pod-arp": guardARPFor(guardPeerMAC, controllerIP, guardGuestIP, 1, "controller-to-pod-arp"),
	}
	visibleToGuest := guardIPv4To(guardPeerMAC, guardGatewayIP, guardGuestIP, "gateway-to-guest-ok")

	sendFromNet := func() error {
		for _, f := range hiddenFromGuest {
			if _, err := unix.Write(peerFD, pad(f)); err != nil {
				return err
			}
		}
		_, err := unix.Write(peerFD, pad(visibleToGuest))
		return err
	}
	leakedToGuest, ok := awaitFrameNoneOf(tapFD, sendFromNet, []byte("gateway-to-guest-ok"), hiddenFromGuest)
	if !ok {
		t.Fatalf("gateway frame written to %s never reached guest tap %s", peerName, tapName)
	}
	for _, l := range leakedToGuest {
		t.Errorf("controller frame %s was redirected into guest tap %s; ext filter should have kept it in the pod namespace", l, tapName)
	}
}

func guardTCPTo(srcMAC net.HardwareAddr, srcIP, dstIP net.IP, dstPort uint16, marker string) []byte {
	frame := guardEthernet(srcMAC, unix.ETH_P_IP)
	hdr := []byte{0x45, 0, 0, 0, 0, 0, 0, 0, 64, unix.IPPROTO_TCP, 0, 0}
	binary.BigEndian.PutUint16(hdr[2:], uint16(20+20+len(marker)))
	frame = append(frame, hdr...)
	frame = append(frame, srcIP.To4()...)
	frame = append(frame, dstIP.To4()...)
	tcp := make([]byte, 20)
	binary.BigEndian.PutUint16(tcp[0:], 40000)
	binary.BigEndian.PutUint16(tcp[2:], dstPort)
	tcp[12] = 5 << 4
	frame = append(frame, tcp...)
	return append(frame, marker...)
}

// TestSpliceGuestWithAccess_KeepsAccessPortInPod proves that TCP addressed
// to the sandbox access port reaches the pod's own stack (where
// granule-shim listens) from any source -- granule relay's pod IP is not
// known in advance -- and is never redirected into the guest, while other
// TCP still reaches the guest.
func TestSpliceGuestWithAccess_KeepsAccessPortInPod(t *testing.T) {
	requireRoot(t)

	const (
		vethName = "acc-ext"
		peerName = "acc-peer"
		tapName  = "acc-guest-tap"
		port     = 7443
	)
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
	vethLink, err := netlink.LinkByName(vethName)
	if err != nil {
		t.Fatal(err)
	}
	peerLink, err := netlink.LinkByName(peerName)
	if err != nil {
		t.Fatal(err)
	}
	tapLink, err := ensureTapMTU(tapName, vethLink.Attrs().MTU, 0, 0)
	if err != nil {
		t.Fatalf("ensureTapMTU: %v", err)
	}
	for _, l := range []netlink.Link{vethLink, peerLink} {
		if err := netlink.LinkSetUp(l); err != nil {
			t.Fatal(err)
		}
	}
	id := Identity{MAC: guardGuestMAC, IP: guardGuestIP}
	if err := spliceGuestWithAccess(vethLink, tapLink, id, nil, []uint16{port}); err != nil {
		t.Fatalf("spliceGuestWithAccess: %v", err)
	}

	tapFD := openTapFD(t, tapName)
	peerFD := openPacketSocket(t, peerLink.Attrs().Index)
	waitSpliceReady(t, nil, []string{vethName, tapName, peerName})

	relayIP := net.IPv4(10, 8, 3, 44).To4()
	hidden := map[string][]byte{
		"relay-to-access-port": guardTCPTo(guardPeerMAC, relayIP, guardGuestIP, port, "relay-to-access-port"),
		"other-to-access-port": guardTCPTo(guardPeerMAC, guardGatewayIP, guardGuestIP, port, "other-to-access-port"),
	}
	visible := guardTCPTo(guardPeerMAC, relayIP, guardGuestIP, 8080, "tcp-to-guest-ok")
	send := func() error {
		for _, f := range hidden {
			if _, err := unix.Write(peerFD, pad(f)); err != nil {
				return err
			}
		}
		_, err := unix.Write(peerFD, pad(visible))
		return err
	}
	leaked, ok := awaitFrameNoneOf(tapFD, send, []byte("tcp-to-guest-ok"), hidden)
	if !ok {
		t.Fatalf("TCP to a non-access port never reached guest tap %s", tapName)
	}
	for _, l := range leaked {
		t.Errorf("access-port frame %s was redirected into guest tap %s", l, tapName)
	}
}
