package netshim

import (
	"net"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"
)

func cidr(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("ParseCIDR(%q): %v", s, err)
	}
	return n
}

func routeStrings(routes []Route) string {
	s := make([]string, len(routes))
	for i, r := range routes {
		s[i] = r.String()
	}
	return strings.Join(s, ",")
}

func TestNormalizeDst(t *testing.T) {
	for _, tc := range []struct {
		dst    string // "" for a nil Dst
		family int
		want   string // "" for nil
	}{
		{"", netlink.FAMILY_V4, "0.0.0.0/0"},
		{"", netlink.FAMILY_V6, "::/0"},
		{"10.2.0.7/24", netlink.FAMILY_V4, "10.2.0.0/24"},
		{"0.0.0.0/0", netlink.FAMILY_V4, "0.0.0.0/0"},
		{"::/0", netlink.FAMILY_V6, "::/0"},
		{"fd00:10:244:1::5/64", netlink.FAMILY_V6, "fd00:10:244:1::/64"},
		{"2001:db8::1/128", netlink.FAMILY_V6, "2001:db8::1/128"},
		// The guest's kernel installs its own link-local route.
		{"fe80::/64", netlink.FAMILY_V6, ""},
		// But a link-local destination of any other shape is a route.
		{"fe80::1/128", netlink.FAMILY_V6, "fe80::1/128"},
		{"ff00::/8", netlink.FAMILY_V6, ""},
		// Families never cross.
		{"10.2.0.0/24", netlink.FAMILY_V6, ""},
		{"fd00::/8", netlink.FAMILY_V4, ""},
	} {
		var dst *net.IPNet
		if tc.dst != "" {
			dst = cidr(t, tc.dst)
		}
		got := normalizeDst(dst, tc.family)
		gotS := ""
		if got != nil {
			gotS = got.String()
		}
		if gotS != tc.want {
			t.Errorf("normalizeDst(%q, %d) = %q, want %q", tc.dst, tc.family, gotS, tc.want)
		}
	}
}

// TestCarriedRoutes_IPv6 is a kind-shaped IPv6 table as a dump returns
// it: the pod subnet on-link, a link-local gateway for everything else,
// and the entries a guest must not be handed.
func TestCarriedRoutes_IPv6(t *testing.T) {
	gw := net.ParseIP("fe80::ecee:eeff:feee:eeee")
	routes := []netlink.Route{
		{Dst: cidr(t, "::/0"), Gw: gw, Family: netlink.FAMILY_V6, Type: unix.RTN_UNICAST},
		{Dst: cidr(t, "fe80::/64"), Family: netlink.FAMILY_V6, Type: unix.RTN_UNICAST},
		{Dst: cidr(t, "ff00::/8"), Family: netlink.FAMILY_V6, Type: unix.RTN_MULTICAST},
		{Dst: cidr(t, "fd00:10:244::/56"), Gw: gw, Family: netlink.FAMILY_V6, Type: unix.RTN_UNICAST},
		{Dst: cidr(t, "fd00:10:244:1::/64"), Family: netlink.FAMILY_V6, Type: unix.RTN_UNICAST},
		{Dst: cidr(t, "2001:db8::/32"), Family: netlink.FAMILY_V6, Type: unix.RTN_BLACKHOLE},
	}
	want := "fd00:10:244:1::/64,fd00:10:244::/56@fe80::ecee:eeff:feee:eeee,::/0@fe80::ecee:eeff:feee:eeee"
	if got := routeStrings(carriedRoutes(routes, netlink.FAMILY_V6)); got != want {
		t.Errorf("carriedRoutes(v6) = %q, want %q", got, want)
	}
}

// TestCarriedRoutes_IPv4Unchanged pins the IPv4 table to what it was
// before carriedRoutes learned a family.
func TestCarriedRoutes_IPv4Unchanged(t *testing.T) {
	gw := net.IPv4(10, 28, 0, 1)
	routes := []netlink.Route{
		{Dst: cidr(t, "0.0.0.0/0"), Gw: gw},
		{Dst: cidr(t, "10.28.0.0/24"), Gw: gw},
		{Dst: cidr(t, "10.28.0.1/32")},
		{Dst: cidr(t, "10.28.0.255/32"), Type: unix.RTN_BROADCAST},
	}
	want := "10.28.0.1/32,10.28.0.0/24@10.28.0.1,0.0.0.0/0@10.28.0.1"
	if got := routeStrings(carriedRoutes(routes, netlink.FAMILY_V4)); got != want {
		t.Errorf("carriedRoutes(v4) = %q, want %q", got, want)
	}
}

func TestGlobalIPv6(t *testing.T) {
	addr := func(s string, scope, flags int) netlink.Addr {
		ip, n, err := net.ParseCIDR(s)
		if err != nil {
			t.Fatalf("ParseCIDR(%q): %v", s, err)
		}
		n.IP = ip
		return netlink.Addr{IPNet: n, Scope: scope, Flags: flags}
	}
	ll := addr("fe80::42:acff:fe11:5/64", unix.RT_SCOPE_LINK, unix.IFA_F_PERMANENT)
	for _, tc := range []struct {
		name  string
		addrs []netlink.Addr
		want  string
	}{
		{"link-local only", []netlink.Addr{ll}, ""},
		{"ULA counts", []netlink.Addr{ll, addr("fd00:10:244:1::5/64", unix.RT_SCOPE_UNIVERSE, unix.IFA_F_PERMANENT)}, "fd00:10:244:1::5/64"},
		{"tentative is taken", []netlink.Addr{addr("2001:db8::5/64", unix.RT_SCOPE_UNIVERSE, unix.IFA_F_TENTATIVE)}, "2001:db8::5/64"},
		{"skips temporary, deprecated and dadfailed", []netlink.Addr{
			addr("2001:db8::a/64", unix.RT_SCOPE_UNIVERSE, unix.IFA_F_TEMPORARY),
			addr("2001:db8::b/64", unix.RT_SCOPE_UNIVERSE, unix.IFA_F_DEPRECATED),
			addr("2001:db8::c/64", unix.RT_SCOPE_UNIVERSE, unix.IFA_F_DADFAILED|unix.IFA_F_TENTATIVE),
			addr("2001:db8::d/64", unix.RT_SCOPE_UNIVERSE, 0),
		}, "2001:db8::d/64"},
	} {
		got := ""
		if a := globalIPv6(tc.addrs); a != nil {
			got = a.IPNet.String()
		}
		if got != tc.want {
			t.Errorf("%s: globalIPv6 = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := linkLocalIPv6([]netlink.Addr{addr("fd00::5/64", unix.RT_SCOPE_UNIVERSE, 0), ll}); got.String() != "fe80::42:acff:fe11:5" {
		t.Errorf("linkLocalIPv6 = %v, want fe80::42:acff:fe11:5", got)
	}
}

// v6Veth creates a veth pair named name/peer in the test namespace, both
// ends up, with each of addrs on name. IPv6 addresses go on nodad, so
// they are usable at once rather than after a second of DAD.
func v6Veth(t *testing.T, name, peer string, addrs ...string) netlink.Link {
	t.Helper()
	t.Cleanup(func() {
		if link, err := netlink.LinkByName(name); err == nil {
			netlink.LinkDel(link)
		}
	})
	if err := netlink.LinkAdd(&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: name}, PeerName: peer}); err != nil {
		t.Fatalf("creating veth pair: %v", err)
	}
	for _, n := range []string{name, peer} {
		link, err := netlink.LinkByName(n)
		if err != nil {
			t.Fatalf("looking up %s: %v", n, err)
		}
		// The link-local address is the test's to choose, as a CNI that
		// assigns one does, so the kernel is kept from adding its own.
		if n == name {
			if err := netlink.LinkSetIP6AddrGenMode(link, nl.IN6_ADDR_GEN_MODE_NONE); err != nil {
				t.Fatalf("setting addrgenmode none on %s: %v", n, err)
			}
		}
		if err := netlink.LinkSetUp(link); err != nil {
			t.Fatalf("bringing up %s: %v", n, err)
		}
	}
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatalf("looking up %s: %v", name, err)
	}
	for _, s := range addrs {
		a, err := netlink.ParseAddr(s)
		if err != nil {
			t.Fatalf("ParseAddr(%q): %v", s, err)
		}
		if a.IP.To4() == nil {
			a.Flags |= unix.IFA_F_NODAD
		}
		if err := netlink.AddrAdd(link, a); err != nil {
			t.Fatalf("adding %s to %s: %v", s, name, err)
		}
	}
	return link
}

func addDefault6(t *testing.T, link netlink.Link, gw string) {
	t.Helper()
	if err := netlink.RouteAdd(&netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       &net.IPNet{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)},
		Gw:        net.ParseIP(gw),
		Priority:  4242,
	}); err != nil {
		t.Fatalf("adding the IPv6 default route: %v", err)
	}
}

// TestDiscoverIdentity_IPv6Only reads a v6-only namespace's identity off
// the kernel, and checks that Setup splices it and the guest is given it
// as granule.ip6= and granule.routes6=, with no ip= at all.
func TestDiscoverIdentity_IPv6Only(t *testing.T) {
	requireRoot(t)

	link := v6Veth(t, "v6o-ext", "v6o-peer", "fe80::42:acff:fe11:5/64", "fd00:10:244:1::5/64")
	addDefault6(t, link, "fe80::1")

	id, err := DiscoverIdentity("v6o-ext")
	if err != nil {
		t.Fatalf("DiscoverIdentity: %v", err)
	}
	if id.HasIPv4() || id.IP != nil || id.Routes != nil || id.Gateway != nil {
		t.Errorf("IPv4 half = %v %v %v %v, want empty", id.IP, id.Mask, id.Gateway, id.Routes)
	}
	if got := id.IP6.String(); got != "fd00:10:244:1::5" || id.Prefix6 != 64 {
		t.Errorf("IP6 = %s/%d, want fd00:10:244:1::5/64", got, id.Prefix6)
	}
	if got := id.LinkLocal6.String(); got != "fe80::42:acff:fe11:5" {
		t.Errorf("LinkLocal6 = %s, want fe80::42:acff:fe11:5", got)
	}
	if got, want := routeStrings(id.Routes6), "fd00:10:244:1::/64,::/0@fe80::1"; got != want {
		t.Errorf("Routes6 = %q, want %q", got, want)
	}

	// The auto interface resolves to the one carrying ::/0.
	cfg := Config{VM: "v6o", Bridge: "v6o-br", IPv6: IPv6Auto}
	resolved, err := cfg.ResolveExternalIface()
	if err != nil {
		t.Fatalf("ResolveExternalIface: %v", err)
	}
	if resolved.ExternalIface != "v6o-ext" {
		t.Errorf("ResolveExternalIface = %q, want v6o-ext", resolved.ExternalIface)
	}

	// Discover and Setup take it, and the guest is given the IPv6 half
	// alone: no ip=, which is IPv4 only.
	cfg.ExternalIface = "v6o-ext"
	t.Cleanup(func() {
		for _, name := range []string{cfg.TapName(), cfg.ControlTapName(), cfg.Bridge} {
			if l, err := netlink.LinkByName(name); err == nil {
				_ = netlink.LinkDel(l)
			}
		}
	})
	if err := Setup(cfg); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if _, err := netlink.LinkByName(cfg.TapName()); err != nil {
		t.Errorf("Setup made no %s: %v", cfg.TapName(), err)
	}
	discovered, err := Discover(cfg)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	g := GuestConfig(cfg, discovered)
	if g.IPParam != "" || g.RouteParam != "" {
		t.Errorf("Guest.IPParam = %q, RouteParam = %q, want both empty", g.IPParam, g.RouteParam)
	}
	if want := "granule.ip6=fd00:10:244:1::5/64,fe80::42:acff:fe11:5"; g.IP6Param != want {
		t.Errorf("Guest.IP6Param = %q, want %q", g.IP6Param, want)
	}
	if want := "granule.routes6=fd00:10:244:1::/64,::/0@fe80::1"; g.Route6Param != want {
		t.Errorf("Guest.Route6Param = %q, want %q", g.Route6Param, want)
	}

	cfg.IPv6 = IPv6Off
	if _, err := Discover(cfg); err == nil || !strings.Contains(err.Error(), "no IPv4 address found on v6o-ext") {
		t.Errorf("Discover with IPv6 off: error = %v, want no IPv4 address", err)
	}
}

// TestDiscoverIdentity_DualStack reads both halves off a dual-stack
// interface, and checks that the IPv4 half, and so the guest
// configuration, is exactly what an IPv4-only interface gives.
func TestDiscoverIdentity_DualStack(t *testing.T) {
	requireRoot(t)

	link := v6Veth(t, "ds-ext", "ds-peer", "172.31.251.2/24", "fe80::42:acff:fe11:7/64", "2001:db8:1::7/64")
	if err := netlink.RouteAdd(&netlink.Route{
		LinkIndex: link.Attrs().Index,
		Gw:        net.IPv4(172, 31, 251, 1).To4(),
		Priority:  4242,
	}); err != nil {
		t.Fatalf("adding the IPv4 default route: %v", err)
	}
	addDefault6(t, link, "fe80::1")

	cfg := Config{VM: "ds", Bridge: "ds-br", ExternalIface: AutoExternalIface}
	resolved, err := cfg.ResolveExternalIface()
	if err != nil {
		t.Fatalf("ResolveExternalIface: %v", err)
	}
	if resolved.ExternalIface != "ds-ext" {
		t.Errorf("ResolveExternalIface = %q, want ds-ext", resolved.ExternalIface)
	}

	id, err := Discover(cfg)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got := id.IP.String(); got != "172.31.251.2" || id.Gateway.String() != "172.31.251.1" {
		t.Errorf("IPv4 half = %s gw %s, want 172.31.251.2 gw 172.31.251.1", got, id.Gateway)
	}
	if got, want := routeStrings(id.Routes), "172.31.251.0/24,0.0.0.0/0@172.31.251.1"; got != want {
		t.Errorf("Routes = %q, want %q", got, want)
	}
	if got := id.IP6.String(); got != "2001:db8:1::7" || id.Prefix6 != 64 {
		t.Errorf("IP6 = %s/%d, want 2001:db8:1::7/64", got, id.Prefix6)
	}
	if got := id.LinkLocal6.String(); got != "fe80::42:acff:fe11:7" {
		t.Errorf("LinkLocal6 = %s, want fe80::42:acff:fe11:7", got)
	}
	if got, want := routeStrings(id.Routes6), "2001:db8:1::/64,::/0@fe80::1"; got != want {
		t.Errorf("Routes6 = %q, want %q", got, want)
	}

	g := GuestConfig(resolved, id)
	if want := "ip=172.31.251.2::172.31.251.1:255.255.255.0::eth0:off"; g.IPParam != want {
		t.Errorf("Guest.IPParam = %q, want %q", g.IPParam, want)
	}
	if want := "granule.routes=172.31.251.0/24,0.0.0.0/0@172.31.251.1"; g.RouteParam != want {
		t.Errorf("Guest.RouteParam = %q, want %q", g.RouteParam, want)
	}
	if want := "granule.ip6=2001:db8:1::7/64,fe80::42:acff:fe11:7"; g.IP6Param != want {
		t.Errorf("Guest.IP6Param = %q, want %q", g.IP6Param, want)
	}
	if want := "granule.routes6=2001:db8:1::/64,::/0@fe80::1"; g.Route6Param != want {
		t.Errorf("Guest.Route6Param = %q, want %q", g.Route6Param, want)
	}

	// Off hands on the IPv4 half alone.
	cfg.IPv6 = IPv6Off
	off, err := Discover(cfg)
	if err != nil {
		t.Fatalf("Discover with IPv6 off: %v", err)
	}
	if off.IP6 != nil || off.LinkLocal6 != nil || off.Routes6 != nil || off.Prefix6 != 0 {
		t.Errorf("IPv6 off: v6 half = %v/%d %v %v, want empty", off.IP6, off.Prefix6, off.LinkLocal6, off.Routes6)
	}
	if !off.IP.Equal(id.IP) || routeStrings(off.Routes) != routeStrings(id.Routes) {
		t.Errorf("IPv6 off changed the IPv4 half: %v %v", off.IP, off.Routes)
	}
}
