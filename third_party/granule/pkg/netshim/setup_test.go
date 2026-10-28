package netshim

import (
	"fmt"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
)

// requireNetnsTestsEnv names the environment variable that turns
// requireRoot's skips into failures. Set it to "required" in any
// automated run that is *supposed* to exercise the kernel, so a
// misconfigured environment reports itself instead of quietly passing.
const requireNetnsTestsEnv = "GRANULE_NETNS_TESTS"

// requireRoot skips tests that need to create real network interfaces and
// tc filters, which requires CAP_NET_ADMIN (in practice, root) and access
// to /dev/net/tun -- the netlink library creates a tap by opening that
// device rather than over rtnetlink, so a root run without it fails deep
// inside link creation rather than here.
//
// It also requires the private network namespace TestMain re-execs into.
// That is what lets every test below name its interfaces plainly instead
// of deriving them from a pid, so running one in a shared namespace is
// not a degraded mode to fall back on: it would create "splice-tap" on
// whatever machine happens to be running the tests.
//
// Skipping is the right default: these tests cannot run on a developer's
// machine without sudo. It is also a trap, because `go test ./...`
// reports a package whose every kernel-touching test skipped as "ok",
// and skips are invisible without -v -- so a green run says nothing
// about whether the splice actually carries a packet. Setting
// GRANULE_NETNS_TESTS=required turns that silence into a failure.
func requireRoot(t *testing.T) {
	t.Helper()

	var reason string
	switch {
	case os.Geteuid() != 0:
		reason = fmt.Sprintf("requires root/CAP_NET_ADMIN to manipulate network interfaces (euid %d)", os.Geteuid())
	case !inOwnNetns():
		reason = fmt.Sprintf("requires a network namespace of its own: %v", netnsSetupErr)
	case !tunDeviceAvailable():
		reason = "requires /dev/net/tun, which the netlink library opens to create a tap"
	default:
		return
	}

	if os.Getenv(requireNetnsTestsEnv) == "required" {
		t.Fatalf("%s=required, but this test cannot run here: %s", requireNetnsTestsEnv, reason)
	}
	t.Skip(reason)
}

func tunDeviceAvailable() bool {
	f, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// TestSetup exercises the whole setup against the real network stack: a
// veth pair stands in for the interface a container runtime would have
// created and addressed, and Setup has to discover that identity, build a
// tap matching its MTU, splice the two, and stand up the control link
// beside them. Run twice, to confirm a retried init container converges
// rather than erroring or stacking duplicate filters.
func TestSetup(t *testing.T) {
	requireRoot(t)

	const (
		extName    = "setup-ext"
		peerName   = "setup-net"
		vmName     = "setup"
		bridgeName = "setup-br"

		mtu = 1450
	)

	t.Cleanup(func() {
		names := []string{extName, bridgeName, "tap-" + vmName, "ctl-" + vmName}
		for _, n := range names {
			if link, err := netlink.LinkByName(n); err == nil {
				netlink.LinkDel(link)
			}
		}
	})

	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: extName, MTU: mtu},
		PeerName:  peerName,
	}
	if err := netlink.LinkAdd(veth); err != nil {
		t.Fatalf("creating veth pair: %v", err)
	}
	ext, err := netlink.LinkByName(extName)
	if err != nil {
		t.Fatalf("looking up %s: %v", extName, err)
	}
	addr, err := netlink.ParseAddr("172.31.253.2/24")
	if err != nil {
		t.Fatalf("ParseAddr: %v", err)
	}
	if err := netlink.AddrAdd(ext, addr); err != nil {
		t.Fatalf("addressing %s: %v", extName, err)
	}
	if err := netlink.LinkSetUp(ext); err != nil {
		t.Fatalf("bringing up %s: %v", extName, err)
	}

	_, ctlNet, err := net.ParseCIDR("169.254.111.1/24")
	if err != nil {
		t.Fatalf("ParseCIDR: %v", err)
	}
	cfg := Config{
		VM:            vmName,
		Bridge:        bridgeName,
		ExternalIface: extName,
		ControlAddr:   net.IPv4(169, 254, 111, 1).To4(),
		ControlNet:    ctlNet,
	}

	for i := 0; i < 2; i++ {
		if err := Setup(cfg); err != nil {
			t.Fatalf("Setup (run %d) error = %v", i+1, err)
		}
	}

	// The identity the guest will take over has to be what the runtime
	// actually put on the interface.
	id, err := DiscoverIdentity(extName)
	if err != nil {
		t.Fatalf("DiscoverIdentity: %v", err)
	}
	if got := id.IP.String(); got != "172.31.253.2" {
		t.Errorf("Identity.IP = %q, want 172.31.253.2", got)
	}
	if id.MTU != mtu {
		t.Errorf("Identity.MTU = %d, want %d", id.MTU, mtu)
	}
	if got := id.Netmask(); got != "255.255.255.0" {
		t.Errorf("Identity.Netmask() = %q, want 255.255.255.0", got)
	}

	// The tap's MTU must match the segment's exactly: a splice is a
	// wire, with nothing in between to fragment an oversized frame.
	tap, err := netlink.LinkByName("tap-" + vmName)
	if err != nil {
		t.Fatalf("looking up tap: %v", err)
	}
	if tap.Attrs().MTU != mtu {
		t.Errorf("tap MTU = %d, want %d (the external interface's)", tap.Attrs().MTU, mtu)
	}

	// One redirect inbound; outbound, the guest's guard: three allows
	// and the drop behind them (see spliceGuest).
	for name, want := range map[string]int{extName: 1, "tap-" + vmName: 4} {
		link, err := netlink.LinkByName(name)
		if err != nil {
			t.Fatalf("looking up %s: %v", name, err)
		}
		filters, err := netlink.FilterList(link, netlink.MakeHandle(0xffff, 0))
		if err != nil {
			t.Fatalf("listing filters on %s: %v", name, err)
		}
		if len(filters) != want {
			t.Errorf("%s has %d ingress filters, want exactly %d", name, len(filters), want)
		}
	}

	// The control link is a separate segment reachable from this
	// namespace, since the guest now answers to the namespace's own
	// address.
	bridge, err := netlink.LinkByName(bridgeName)
	if err != nil {
		t.Fatalf("looking up control bridge: %v", err)
	}
	addrs, err := netlink.AddrList(bridge, netlink.FAMILY_V4)
	if err != nil {
		t.Fatalf("listing control bridge addresses: %v", err)
	}
	if len(addrs) != 1 || addrs[0].IP.String() != "169.254.111.1" {
		t.Errorf("control bridge addresses = %v, want [169.254.111.1]", addrs)
	}
	ctlTap, err := netlink.LinkByName("ctl-" + vmName)
	if err != nil {
		t.Fatalf("looking up control tap: %v", err)
	}
	if ctlTap.Attrs().MasterIndex != bridge.Attrs().Index {
		t.Errorf("control tap master = %d, want the control bridge (%d)",
			ctlTap.Attrs().MasterIndex, bridge.Attrs().Index)
	}

	// Neither end of the control link generates an IPv6 link-local
	// address: it is IPv4 only, and one would only send DAD, RS and MLD
	// into the guard's drop.
	for _, name := range []string{bridgeName, "ctl-" + vmName} {
		mode, err := os.ReadFile("/proc/sys/net/ipv6/conf/" + name + "/addr_gen_mode")
		if os.IsNotExist(err) {
			continue // no IPv6 in this kernel
		}
		if err != nil {
			t.Fatalf("reading %s's addr_gen_mode: %v", name, err)
		}
		if got := strings.TrimSpace(string(mode)); got != "1" {
			t.Errorf("%s addr_gen_mode = %s, want 1 (none)", name, got)
		}
	}
}

// TestSetup_NoControlLink covers the opt-out path: everything on the
// data path still gets built, and nothing of the control link does.
func TestSetup_NoControlLink(t *testing.T) {
	requireRoot(t)

	const (
		extName    = "noctl-ext"
		peerName   = "noctl-net"
		vmName     = "noctl"
		bridgeName = "noctl-br"
	)

	t.Cleanup(func() {
		for _, n := range []string{extName, "tap-" + vmName, "ctl-" + vmName} {
			if link, err := netlink.LinkByName(n); err == nil {
				netlink.LinkDel(link)
			}
		}
	})

	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: extName},
		PeerName:  peerName,
	}
	if err := netlink.LinkAdd(veth); err != nil {
		t.Fatalf("creating veth pair: %v", err)
	}
	ext, err := netlink.LinkByName(extName)
	if err != nil {
		t.Fatalf("looking up %s: %v", extName, err)
	}
	addr, err := netlink.ParseAddr("172.31.254.2/24")
	if err != nil {
		t.Fatalf("ParseAddr: %v", err)
	}
	if err := netlink.AddrAdd(ext, addr); err != nil {
		t.Fatalf("addressing %s: %v", extName, err)
	}
	if err := netlink.LinkSetUp(ext); err != nil {
		t.Fatalf("bringing up %s: %v", extName, err)
	}

	cfg := Config{
		VM:            vmName,
		Bridge:        bridgeName,
		ExternalIface: extName,
	}
	if err := Setup(cfg); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}

	if _, err := netlink.LinkByName("tap-" + vmName); err != nil {
		t.Errorf("spliced tap missing: %v", err)
	}
	if _, err := netlink.LinkByName("ctl-" + vmName); err == nil {
		t.Errorf("control tap exists, want none when the control link is disabled")
	}
}

// TestSetup_AutoExternalIface covers a namespace whose primary interface
// is not called eth0, next to a second addressed interface that does not
// carry the default route: auto has to pick the routed one, and say so
// rather than guess once nothing routes.
func TestSetup_AutoExternalIface(t *testing.T) {
	requireRoot(t)

	const (
		extName    = "net1"
		sideName   = "side0"
		vmName     = "auto"
		bridgeName = "auto-br"

		mtu = 1400
	)

	t.Cleanup(func() {
		for _, n := range []string{extName, sideName, bridgeName, "tap-" + vmName, "ctl-" + vmName} {
			if link, err := netlink.LinkByName(n); err == nil {
				netlink.LinkDel(link)
			}
		}
	})

	addVeth := func(name, peer, cidr string, linkMTU int) netlink.Link {
		t.Helper()
		veth := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: name, MTU: linkMTU}, PeerName: peer}
		if err := netlink.LinkAdd(veth); err != nil {
			t.Fatalf("creating veth pair %s: %v", name, err)
		}
		for _, n := range []string{name, peer} {
			l, err := netlink.LinkByName(n)
			if err != nil {
				t.Fatalf("looking up %s: %v", n, err)
			}
			if err := netlink.LinkSetUp(l); err != nil {
				t.Fatalf("bringing up %s: %v", n, err)
			}
		}
		link, err := netlink.LinkByName(name)
		if err != nil {
			t.Fatalf("looking up %s: %v", name, err)
		}
		addr, err := netlink.ParseAddr(cidr)
		if err != nil {
			t.Fatalf("ParseAddr: %v", err)
		}
		if err := netlink.AddrAdd(link, addr); err != nil {
			t.Fatalf("addressing %s: %v", name, err)
		}
		return link
	}
	ext := addVeth(extName, "net1-peer", "172.31.252.2/24", mtu)
	side := addVeth(sideName, "side0-peer", "172.31.251.2/24", 1500)

	defRoute := &netlink.Route{LinkIndex: ext.Attrs().Index, Gw: net.IPv4(172, 31, 252, 1)}
	if err := netlink.RouteAdd(defRoute); err != nil {
		t.Fatalf("adding default route via %s: %v", extName, err)
	}

	_, ctlNet, err := net.ParseCIDR("169.254.112.1/24")
	if err != nil {
		t.Fatalf("ParseCIDR: %v", err)
	}
	for _, iface := range []string{"", AutoExternalIface} {
		cfg := Config{
			VM:            vmName,
			Bridge:        bridgeName,
			ExternalIface: iface,
			ControlAddr:   net.IPv4(169, 254, 112, 1).To4(),
			ControlNet:    ctlNet,
		}
		// Twice: the second run sees the control bridge Setup addressed,
		// which must not count as a candidate.
		for i := 0; i < 2; i++ {
			if err := Setup(cfg); err != nil {
				t.Fatalf("Setup(ExternalIface=%q) run %d: %v", iface, i+1, err)
			}
		}
		id, err := Discover(cfg)
		if err != nil {
			t.Fatalf("Discover(ExternalIface=%q): %v", iface, err)
		}
		if id.Iface != extName || id.IP.String() != "172.31.252.2" || id.MTU != mtu {
			t.Errorf("Discover(ExternalIface=%q) = %s %s mtu %d, want %s 172.31.252.2 mtu %d",
				iface, id.Iface, id.IP, id.MTU, extName, mtu)
		}
		if id.Gateway.String() != "172.31.252.1" {
			t.Errorf("Discover gateway = %v, want 172.31.252.1", id.Gateway)
		}
	}

	tap, err := netlink.LinkByName("tap-" + vmName)
	if err != nil {
		t.Fatalf("looking up tap: %v", err)
	}
	if tap.Attrs().MTU != mtu {
		t.Errorf("tap MTU = %d, want %d (%s's, not %s's)", tap.Attrs().MTU, mtu, extName, sideName)
	}
	for name, want := range map[string]int{extName: 1, sideName: 0} {
		link, err := netlink.LinkByName(name)
		if err != nil {
			t.Fatalf("looking up %s: %v", name, err)
		}
		filters, err := netlink.FilterList(link, netlink.MakeHandle(0xffff, 0))
		if err != nil && want != 0 {
			t.Fatalf("listing filters on %s: %v", name, err)
		}
		if len(filters) != want {
			t.Errorf("%s has %d ingress filters, want %d", name, len(filters), want)
		}
	}

	// With no default route, two addressed interfaces are ambiguous,
	// and the error has to say which and how to choose.
	cfg := Config{VM: vmName, Bridge: bridgeName}
	if err := netlink.RouteDel(defRoute); err != nil {
		t.Fatalf("deleting default route: %v", err)
	}
	_, err = cfg.ResolveExternalIface()
	if err == nil || !strings.Contains(err.Error(), extName) || !strings.Contains(err.Error(), sideName) ||
		!strings.Contains(err.Error(), "NETSHIM_EXTERNAL_IFACE") {
		t.Errorf("ResolveExternalIface with two addressed interfaces = %v, want an error naming both", err)
	}

	// An explicit name still wins, default route or not.
	if got, err := (Config{VM: vmName, Bridge: bridgeName, ExternalIface: sideName}).ResolveExternalIface(); err != nil || got.ExternalIface != sideName {
		t.Errorf("explicit %s resolved to %q, %v", sideName, got.ExternalIface, err)
	}

	// Down to one addressed interface, that one is it.
	if err := netlink.LinkDel(side); err != nil {
		t.Fatalf("deleting %s: %v", sideName, err)
	}
	got, err := cfg.ResolveExternalIface()
	if err != nil || got.ExternalIface != extName {
		t.Errorf("ResolveExternalIface with one addressed interface = %q, %v; want %s", got.ExternalIface, err, extName)
	}
}
