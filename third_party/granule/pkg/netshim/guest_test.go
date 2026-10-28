package netshim

import (
	"fmt"
	"net"
	"os"
	"testing"

	"github.com/vishvananda/netlink"
)

// TestDiscoverIdentity_NoAddress confirms a namespace whose external
// interface has not been addressed yet fails loudly, rather than handing
// the guest an empty identity to take over.
func TestDiscoverIdentity_NoAddress(t *testing.T) {
	requireRoot(t)

	name := fmt.Sprintf("na-%d", os.Getpid()%10000)
	t.Cleanup(func() {
		if link, err := netlink.LinkByName(name); err == nil {
			netlink.LinkDel(link)
		}
	})
	if _, err := ensureTapMTU(name, 1500, 0, 0); err != nil {
		t.Fatalf("ensureTapMTU: %v", err)
	}
	if _, err := DiscoverIdentity(name); err == nil {
		t.Errorf("DiscoverIdentity() error = nil, want an error for an unaddressed interface")
	}
}

// TestDiscoverIdentity_Gateway is the half of the identity every other
// test here left uncovered, and the one a guest's whole egress path
// hangs off: the namespace's default route, which the guest can only
// learn through the ip= parameter derived from it.
//
// It reads the route back off the kernel rather than constructing a
// netlink.Route, because the bug it guards against is precisely the
// difference between the two: a dump fills in a destination of 0.0.0.0/0
// for a route the kernel sent no RTA_DST for, so the "Dst == nil" test
// this used to make matched nothing a container ever has, and every
// guest booted with an empty gateway field and no route off its own
// segment.
func TestDiscoverIdentity_Gateway(t *testing.T) {
	requireRoot(t)

	suffix := os.Getpid() % 10000
	extName := fmt.Sprintf("ge-%d", suffix)
	peerName := fmt.Sprintf("gp-%d", suffix)
	vmName := fmt.Sprintf("g%d", suffix)

	t.Cleanup(func() {
		if link, err := netlink.LinkByName(extName); err == nil {
			netlink.LinkDel(link)
		}
	})

	// Both ends go up: a link with no carrier takes no route, and the
	// default route below is the entire point of this test.
	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: extName},
		PeerName:  peerName,
	}
	if err := netlink.LinkAdd(veth); err != nil {
		t.Fatalf("creating veth pair: %v", err)
	}
	for _, n := range []string{extName, peerName} {
		link, err := netlink.LinkByName(n)
		if err != nil {
			t.Fatalf("looking up %s: %v", n, err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			t.Fatalf("bringing up %s: %v", n, err)
		}
	}
	ext, err := netlink.LinkByName(extName)
	if err != nil {
		t.Fatalf("looking up %s: %v", extName, err)
	}
	addr, err := netlink.ParseAddr("172.31.252.2/24")
	if err != nil {
		t.Fatalf("ParseAddr: %v", err)
	}
	if err := netlink.AddrAdd(ext, addr); err != nil {
		t.Fatalf("addressing %s: %v", extName, err)
	}

	// The same shape a container runtime leaves behind: an on-link
	// gateway at the segment's first address, reached over this
	// interface.
	//
	// The metric is only there so this can run in a namespace that
	// already has a default route of its own -- which is every namespace
	// this test is ever run in outside a container, and where a second
	// one at the same metric is rejected outright. It does not weaken
	// what is being tested: DiscoverIdentity dumps the routes on this
	// interface alone, so the namespace's own default (on some other
	// link) is not in the list either way.
	gw := net.IPv4(172, 31, 252, 1).To4()
	if err := netlink.RouteAdd(&netlink.Route{
		LinkIndex: ext.Attrs().Index,
		Gw:        gw,
		Priority:  4242,
	}); err != nil {
		t.Fatalf("adding the default route: %v", err)
	}

	id, err := DiscoverIdentity(extName)
	if err != nil {
		t.Fatalf("DiscoverIdentity: %v", err)
	}
	if id.Gateway == nil {
		t.Fatalf("Identity.Gateway = nil, want %s -- the guest gets no default route without it", gw)
	}
	if got := id.Gateway.String(); got != gw.String() {
		t.Errorf("Identity.Gateway = %q, want %q", got, gw)
	}

	// And the whole reason the gateway is discovered at all: it has to
	// reach the guest, in the one place the guest can act on it.
	guest := GuestConfig(Config{VM: vmName, ExternalIface: extName}, id)
	if want := "ip=172.31.252.2::172.31.252.1:255.255.255.0::eth0:off"; guest.IPParam != want {
		t.Errorf("Guest.IPParam = %q, want %q", guest.IPParam, want)
	}
}

// TestDiscover_TapMTU reads a real tap's MTU back through Discover: mtu=
// stays on the --net while the tap disagrees with the external interface,
// and comes off once they match. That is the state Setup leaves behind,
// and the one in which a VM container needs no NET_ADMIN to attach. It
// also checks the other half of that: the tap carries the owner it was
// created for, so a non-root VM container is allowed to open it.
func TestDiscover_TapMTU(t *testing.T) {
	requireRoot(t)

	suffix := os.Getpid() % 10000
	extName := fmt.Sprintf("me-%d", suffix)
	vmName := fmt.Sprintf("m%d", suffix)
	cfg := Config{VM: vmName, ExternalIface: extName}

	t.Cleanup(func() {
		for _, n := range []string{extName, cfg.TapName()} {
			if link, err := netlink.LinkByName(n); err == nil {
				netlink.LinkDel(link)
			}
		}
	})

	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: extName, MTU: 1460},
		PeerName:  fmt.Sprintf("mp-%d", suffix),
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

	for _, tc := range []struct {
		tapMTU int
		want   string
	}{
		{1500, ",mtu=1460"},
		{1460, ""},
	} {
		tap, err := ensureTapMTU(cfg.TapName(), tc.tapMTU, 65534, 65533)
		if err != nil {
			t.Fatalf("ensureTapMTU: %v", err)
		}
		if tt, ok := tap.(*netlink.Tuntap); !ok || tt.Owner != 65534 || tt.Group != 65533 {
			t.Errorf("tap = %#v, want a tap owned by 65534:65533", tap)
		}
		id, err := Discover(cfg)
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		if id.TapMTU != tc.tapMTU {
			t.Errorf("Identity.TapMTU = %d, want %d", id.TapMTU, tc.tapMTU)
		}
		want := fmt.Sprintf("tap=%s,mac=%s%s", cfg.TapName(), id.MAC, tc.want)
		if got := GuestConfig(cfg, id).Nets[0]; got != want {
			t.Errorf("tap MTU %d: Nets[0] = %q, want %q", tc.tapMTU, got, want)
		}
	}
}

// TestDefaultGateway covers the same decision without a kernel, over
// both shapes a default route can arrive in -- the synthesized
// 0.0.0.0/0 a dump produces, and the nil destination a hand-built Route
// carries -- plus the routes that must not be mistaken for one.
func TestDefaultGateway(t *testing.T) {
	_, zeroNet, err := net.ParseCIDR("0.0.0.0/0")
	if err != nil {
		t.Fatalf("ParseCIDR: %v", err)
	}
	_, onLink, err := net.ParseCIDR("172.17.0.0/16")
	if err != nil {
		t.Fatalf("ParseCIDR: %v", err)
	}

	gw := net.IPv4(172, 17, 0, 1).To4()

	for _, tc := range []struct {
		name   string
		routes []netlink.Route
		want   string
	}{
		{
			name: "as the kernel reports it",
			routes: []netlink.Route{
				{Dst: onLink},
				{Dst: zeroNet, Gw: gw},
			},
			want: "172.17.0.1",
		},
		{
			name:   "with no destination at all",
			routes: []netlink.Route{{Gw: gw}},
			want:   "172.17.0.1",
		},
		{
			name:   "on-link routes only",
			routes: []netlink.Route{{Dst: onLink}},
			want:   "",
		},
		{
			name:   "a default route with no gateway",
			routes: []netlink.Route{{Dst: zeroNet}},
			want:   "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ""
			if ip := defaultGateway(tc.routes); ip != nil {
				got = ip.String()
			}
			if got != tc.want {
				t.Errorf("defaultGateway() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestGuestConfig_GKEPodTable pins the whole derivation against a table
// read off a real pod on a real GKE cluster, rather than one this
// package made up:
//
//	default via 10.28.0.1 dev eth0
//	10.28.0.0/24 via 10.28.0.1 dev eth0 src 10.28.0.15
//	10.28.0.1 dev eth0 scope link src 10.28.0.15
//
// That is GKE's `ptp` plugin over `host-local` IPAM -- the CNI on the
// most common managed Kubernetes, and the one the pod-to-pod gap was
// measured on in both directions (see the README's "Validated on GKE").
// The parameters below are the ones the guest holding that pod's
// address actually booted with there.
//
// The routes go in the order a dump hands them over, which is not the
// order they can be installed in: the gateway's own host route is last
// here and has to come out first, since nothing else makes 10.28.0.1
// reachable for the two routes that go via it.
func TestGuestConfig_GKEPodTable(t *testing.T) {
	mustCIDR := func(s string) *net.IPNet {
		t.Helper()
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			t.Fatalf("ParseCIDR(%q): %v", s, err)
		}
		return n
	}

	gw := net.IPv4(10, 28, 0, 1).To4()
	id := Identity{
		Iface:   "eth0",
		IP:      net.IPv4(10, 28, 0, 15).To4(),
		Mask:    net.CIDRMask(24, 32),
		Gateway: gw,
		MTU:     1460,
		Routes: carriedRoutes([]netlink.Route{
			{Dst: mustCIDR("0.0.0.0/0"), Gw: gw},
			{Dst: mustCIDR("10.28.0.0/24"), Gw: gw},
			{Dst: mustCIDR("10.28.0.1/32")},
		}, netlink.FAMILY_V4),
	}

	g := GuestConfig(Config{VM: "web", ExternalIface: "eth0"}, id)
	if want := "ip=10.28.0.15::10.28.0.1:255.255.255.0::eth0:off"; g.IPParam != want {
		t.Errorf("Guest.IPParam = %q, want %q", g.IPParam, want)
	}
	want := "granule.routes=10.28.0.1/32,10.28.0.0/24@10.28.0.1,0.0.0.0/0@10.28.0.1"
	if g.RouteParam != want {
		t.Errorf("Guest.RouteParam = %q, want %q", g.RouteParam, want)
	}
	if wantMTU := "granule.mtu=1460"; g.MTUParam != wantMTU {
		t.Errorf("Guest.MTUParam = %q, want %q", g.MTUParam, wantMTU)
	}
}

func TestWithGuestParams(t *testing.T) {
	cases := []struct {
		name    string
		cmdline string
		guest   Guest
		want    string
	}{
		{
			name:    "appends ip, routes and mtu",
			cmdline: "console=ttyS0 root=/dev/vda rw",
			guest: Guest{
				IPParam:    "ip=10.244.0.5::10.244.0.1:255.255.255.0::eth0:off",
				RouteParam: "granule.routes=10.244.0.1/32,0.0.0.0/0@10.244.0.1",
				MTUParam:   "granule.mtu=1460",
			},
			want: "console=ttyS0 root=/dev/vda rw ip=10.244.0.5::10.244.0.1:255.255.255.0::eth0:off granule.routes=10.244.0.1/32,0.0.0.0/0@10.244.0.1 granule.mtu=1460",
		},
		{
			name:    "empty cmdline",
			cmdline: "",
			guest: Guest{
				IPParam:    "ip=10.244.0.5::10.244.0.1:255.255.255.0::eth0:off",
				RouteParam: "granule.routes=0.0.0.0/0@10.244.0.1",
				MTUParam:   "granule.mtu=1440",
			},
			want: "ip=10.244.0.5::10.244.0.1:255.255.255.0::eth0:off granule.routes=0.0.0.0/0@10.244.0.1 granule.mtu=1440",
		},
		{
			name:    "operator override ip suppresses routes and mtu",
			cmdline: "console=ttyS0 ip=192.168.1.5::192.168.1.1:255.255.255.0::eth0:off quiet",
			guest: Guest{
				IPParam:    "ip=10.244.0.5::10.244.0.1:255.255.255.0::eth0:off",
				RouteParam: "granule.routes=10.244.0.1/32,0.0.0.0/0@10.244.0.1",
				MTUParam:   "granule.mtu=1460",
			},
			want: "console=ttyS0 ip=192.168.1.5::192.168.1.1:255.255.255.0::eth0:off quiet",
		},
		{
			name:    "existing routes and mtu not duplicated",
			cmdline: "console=ttyS0 granule.routes=existing granule.mtu=1500",
			guest: Guest{
				IPParam:    "ip=10.244.0.5::10.244.0.1:255.255.255.0::eth0:off",
				RouteParam: "granule.routes=10.244.0.1/32",
				MTUParam:   "granule.mtu=1460",
			},
			want: "console=ttyS0 granule.routes=existing granule.mtu=1500 ip=10.244.0.5::10.244.0.1:255.255.255.0::eth0:off",
		},
		{
			name:    "empty mtu and empty routes",
			cmdline: "console=ttyS0",
			guest: Guest{
				IPParam: "ip=10.244.0.5::10.244.0.1:255.255.255.0::eth0:off",
			},
			want: "console=ttyS0 ip=10.244.0.5::10.244.0.1:255.255.255.0::eth0:off",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := WithGuestParams(tc.cmdline, tc.guest)
			if got != tc.want {
				t.Errorf("WithGuestParams(%q, %+v) = %q, want %q", tc.cmdline, tc.guest, got, tc.want)
			}
		})
	}
}
