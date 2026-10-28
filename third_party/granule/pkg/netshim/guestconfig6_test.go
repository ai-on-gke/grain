package netshim

import (
	"bytes"
	"log"
	"net"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
)

// guestGoldenBase is the command line every golden case below starts
// from: what the bundled guest boots with before netshim adds anything.
const guestGoldenBase = "console=ttyS0 root=/dev/vda rw"

// goldenConfig is a netshim configuration with everything that reaches
// the command line turned on: a control link, and a resolver read off
// resolv.conf, search list and options included.
func goldenConfig(t *testing.T) Config {
	t.Helper()
	_, ctl, err := net.ParseCIDR("169.254.100.0/24")
	if err != nil {
		t.Fatal(err)
	}
	return Config{
		VM:            "web",
		ExternalIface: "eth0",
		ControlNet:    ctl,
		ControlAddr:   net.IPv4(169, 254, 100, 1).To4(),
		DNS:           []net.IP{net.IPv4(10, 96, 0, 10).To4()},
		DNSSearch:     []string{"default.svc.cluster.local", "svc.cluster.local"},
		DNSOptions:    []string{"ndots:5"},
	}
}

func goldenV4(t *testing.T) Identity {
	gw := net.IPv4(10, 244, 0, 1).To4()
	return Identity{
		Iface:   "eth0",
		MAC:     net.HardwareAddr{0x02, 0x42, 0xac, 0x11, 0x00, 0x05},
		MTU:     1460,
		IP:      net.IPv4(10, 244, 0, 5).To4(),
		Mask:    net.CIDRMask(24, 32),
		Gateway: gw,
		Routes: carriedRoutes([]netlink.Route{
			{Dst: cidr(t, "0.0.0.0/0"), Gw: gw},
			{Dst: cidr(t, "10.244.0.0/24"), Gw: gw},
			{Dst: cidr(t, "10.244.0.1/32")},
		}, netlink.FAMILY_V4),
	}
}

// withV6 adds the IPv6 half a kind dual-stack pod has: a ULA, the pod's
// link-local address, and a default route via the host veth's
// link-local address.
func withV6(t *testing.T, id Identity) Identity {
	gw := net.ParseIP("fe80::1")
	id.IP6 = net.ParseIP("fd00:10:244::5")
	id.Prefix6 = 64
	id.LinkLocal6 = net.ParseIP("fe80::42:acff:fe11:5")
	id.Routes6 = carriedRoutes([]netlink.Route{
		{Dst: nil, Gw: gw},
		{Dst: cidr(t, "fd00:10:244::/64")},
		{Dst: cidr(t, "fe80::/64")},
	}, netlink.FAMILY_V6)
	return id
}

// TestGuestConfig_Golden pins the whole command line GuestConfig and
// WithGuestParams produce for each family mix, byte for byte. The
// IPv4-only case is the one that must never move: it is what every guest
// booted before IPv6 existed was given, and contract-2 images still
// boot on it.
func TestGuestConfig_Golden(t *testing.T) {
	v4 := goldenV4(t)
	dual := withV6(t, v4)
	v6 := withV6(t, Identity{Iface: v4.Iface, MAC: v4.MAC, MTU: v4.MTU})

	for _, tc := range []struct {
		name string
		id   Identity
		want string
	}{
		{
			name: "IPv4 only",
			id:   v4,
			want: guestGoldenBase +
				" ip=10.244.0.5::10.244.0.1:255.255.255.0::eth0:off:10.96.0.10" +
				" granule.routes=10.244.0.1/32,10.244.0.0/24@10.244.0.1,0.0.0.0/0@10.244.0.1" +
				" granule.mtu=1460" +
				" granule.dns_search=default.svc.cluster.local,svc.cluster.local" +
				" granule.dns_options=ndots:5",
		},
		{
			// No ip= at all: it is IPv4 only, and an ip= naming no
			// address would have the kernel try DHCP.
			name: "IPv6 only",
			id:   v6,
			want: guestGoldenBase +
				" granule.ip6=fd00:10:244::5/64,fe80::42:acff:fe11:5" +
				" granule.routes6=fd00:10:244::/64,::/0@fe80::1" +
				" granule.mtu=1460" +
				" granule.dns=10.96.0.10" +
				" granule.dns_search=default.svc.cluster.local,svc.cluster.local" +
				" granule.dns_options=ndots:5",
		},
		{
			name: "dual stack",
			id:   dual,
			want: guestGoldenBase +
				" ip=10.244.0.5::10.244.0.1:255.255.255.0::eth0:off:10.96.0.10" +
				" granule.routes=10.244.0.1/32,10.244.0.0/24@10.244.0.1,0.0.0.0/0@10.244.0.1" +
				" granule.ip6=fd00:10:244::5/64,fe80::42:acff:fe11:5" +
				" granule.routes6=fd00:10:244::/64,::/0@fe80::1" +
				" granule.mtu=1460" +
				" granule.dns_search=default.svc.cluster.local,svc.cluster.local" +
				" granule.dns_options=ndots:5",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := GuestConfig(goldenConfig(t), tc.id)
			if got := WithGuestParams(guestGoldenBase, g); got != tc.want {
				t.Errorf("command line:\n got %s\nwant %s", got, tc.want)
			}
			wantNets := []string{"tap=tap-web,mac=02:42:ac:11:00:05,mtu=1460", "tap=ctl-web"}
			if strings.Join(g.Nets, " ") != strings.Join(wantNets, " ") {
				t.Errorf("Nets = %q, want %q", g.Nets, wantNets)
			}
			if g.ControlIP != "169.254.100.2" {
				t.Errorf("ControlIP = %q, want 169.254.100.2", g.ControlIP)
			}
		})
	}
}

// TestGuestConfig_IPv6WithoutLinkLocal: the syntax allows the link-local
// field to be absent, and the deriving end leaves it off rather than
// writing an empty one when the interface somehow has none.
func TestGuestConfig_IPv6WithoutLinkLocal(t *testing.T) {
	id := withV6(t, Identity{Iface: "eth0", MTU: 1500})
	id.LinkLocal6 = nil
	id.Routes6 = nil
	g := GuestConfig(Config{VM: "web", ExternalIface: "eth0"}, id)
	if want := "granule.ip6=fd00:10:244::5/64"; g.IP6Param != want {
		t.Errorf("IP6Param = %q, want %q", g.IP6Param, want)
	}
	if g.Route6Param != "" || g.IPParam != "" || g.RouteParam != "" {
		t.Errorf("IPParam %q RouteParam %q Route6Param %q, want all empty", g.IPParam, g.RouteParam, g.Route6Param)
	}
}

func TestWithGuestParams_IPv6(t *testing.T) {
	dual := Guest{
		IPParam:     "ip=10.244.0.5::10.244.0.1:255.255.255.0::eth0:off",
		RouteParam:  "granule.routes=0.0.0.0/0@10.244.0.1",
		IP6Param:    "granule.ip6=fd00::5/64,fe80::5",
		Route6Param: "granule.routes6=::/0@fe80::1",
		MTUParam:    "granule.mtu=1460",
	}
	v6only := Guest{
		IP6Param:    dual.IP6Param,
		Route6Param: dual.Route6Param,
		MTUParam:    dual.MTUParam,
	}
	for _, tc := range []struct {
		name    string
		cmdline string
		guest   Guest
		want    string
	}{
		{
			// An operator's ip= takes the IPv6 half with it: the
			// operator addressed the guest, and the namespace's v6
			// address belongs to an identity they overrode.
			name:    "operator ip= suppresses both families",
			cmdline: "console=ttyS0 ip=192.168.1.5::192.168.1.1:255.255.255.0::eth0:off",
			guest:   dual,
			want:    "console=ttyS0 ip=192.168.1.5::192.168.1.1:255.255.255.0::eth0:off",
		},
		{
			name:    "operator granule.ip6= suppresses both families",
			cmdline: "console=ttyS0 granule.ip6=2001:db8::9/64",
			guest:   dual,
			want:    "console=ttyS0 granule.ip6=2001:db8::9/64",
		},
		{
			name:    "operator granule.ip6= suppresses a v6-only identity",
			cmdline: "console=ttyS0 granule.ip6=2001:db8::9/64",
			guest:   v6only,
			want:    "console=ttyS0 granule.ip6=2001:db8::9/64",
		},
		{
			name:    "operator ip= suppresses a v6-only identity",
			cmdline: "console=ttyS0 ip=dhcp",
			guest:   v6only,
			want:    "console=ttyS0 ip=dhcp",
		},
		{
			// granule.routes= is not a prefix of granule.routes6=, nor
			// the other way round: an operator's v4 routes leave the
			// derived v6 ones in.
			name:    "operator routes of one family keep the other's",
			cmdline: "console=ttyS0 granule.routes=0.0.0.0/0@10.0.0.1",
			guest:   dual,
			want: "console=ttyS0 granule.routes=0.0.0.0/0@10.0.0.1" +
				" ip=10.244.0.5::10.244.0.1:255.255.255.0::eth0:off" +
				" granule.ip6=fd00::5/64,fe80::5 granule.routes6=::/0@fe80::1 granule.mtu=1460",
		},
		{
			name:    "v6 only",
			cmdline: "console=ttyS0",
			guest:   v6only,
			want:    "console=ttyS0 granule.ip6=fd00::5/64,fe80::5 granule.routes6=::/0@fe80::1 granule.mtu=1460",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := WithGuestParams(tc.cmdline, tc.guest); got != tc.want {
				t.Errorf("WithGuestParams(%q) =\n %q\nwant\n %q", tc.cmdline, got, tc.want)
			}
		})
	}
}

// TestWithGuestParams_WarnsPastTheKernelLimit: the kernel truncates a
// command line past COMMAND_LINE_SIZE without a word, so this has to say
// it instead -- and say nothing for one that fits.
func TestWithGuestParams_WarnsPastTheKernelLimit(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	g := Guest{IPParam: "ip=10.244.0.5::10.244.0.1:255.255.255.0::eth0:off"}
	WithGuestParams("console=ttyS0", g)
	if buf.Len() != 0 {
		t.Errorf("warned about a short command line: %s", buf.String())
	}

	long := "console=ttyS0 " + strings.Repeat("x", maxCmdline)
	WithGuestParams(long, g)
	if !strings.Contains(buf.String(), "past the kernel's 2048") {
		t.Errorf("no warning for a %d-byte command line; logged %q", len(long), buf.String())
	}
}
