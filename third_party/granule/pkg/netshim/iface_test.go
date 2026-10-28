package netshim

import (
	"net"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func TestIsAutoExternalIface(t *testing.T) {
	for name, want := range map[string]bool{"": true, "auto": true, " auto ": true, "eth0": false, "net1": false} {
		if got := IsAutoExternalIface(name); got != want {
			t.Errorf("IsAutoExternalIface(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestResolveExternalIface_ExplicitWins covers the compatibility promise:
// a named interface -- including the "eth0" every spec saved before auto
// existed carries -- is used as given, without the namespace being read.
func TestResolveExternalIface_ExplicitWins(t *testing.T) {
	for _, name := range []string{"eth0", "does-not-exist"} {
		cfg, err := Config{VM: "vm", ExternalIface: name}.ResolveExternalIface()
		if err != nil || cfg.ExternalIface != name {
			t.Errorf("ResolveExternalIface(%q) = %q, %v; want it unchanged", name, cfg.ExternalIface, err)
		}
	}
}

func TestPickExternalIface(t *testing.T) {
	_, defaultDst, _ := net.ParseCIDR("0.0.0.0/0")
	_, subnet, _ := net.ParseCIDR("10.2.0.0/24")
	gw := net.IPv4(10, 2, 0, 1)

	lo := ifaceCandidate{Name: "lo", Index: 1, Loopback: true, Addressed: true}
	eth0 := ifaceCandidate{Name: "eth0", Index: 2, Addressed: true}
	net1 := ifaceCandidate{Name: "net1", Index: 3, Addressed: true}
	veth := ifaceCandidate{Name: "veth3f2a", Index: 4, Addressed: true}
	bare := ifaceCandidate{Name: "eth9", Index: 5}
	bridge := ifaceCandidate{Name: "granule0", Index: 6, Addressed: true}
	tap := ifaceCandidate{Name: "tap-vm", Index: 7}
	own := Config{VM: "vm", Bridge: "granule0"}.ownLinks()

	def := func(idx, metric int) netlink.Route {
		return netlink.Route{LinkIndex: idx, Gw: gw, Priority: metric}
	}

	// IPv6, as a dump hands it over: Family set and ::/0 filled in.
	_, default6, _ := net.ParseCIDR("::/0")
	_, subnet6, _ := net.ParseCIDR("fd00:10:244:1::/64")
	gw6 := net.ParseIP("fe80::1")
	def6 := func(idx, metric int) netlink.Route {
		return netlink.Route{LinkIndex: idx, Dst: default6, Gw: gw6, Priority: metric, Family: netlink.FAMILY_V6, Type: unix.RTN_UNICAST}
	}
	v6only := ifaceCandidate{Name: "net6", Index: 8, Addressed: true}

	for _, tc := range []struct {
		name    string
		links   []ifaceCandidate
		routes  []netlink.Route
		want    string
		wantErr []string
	}{
		{
			name:   "eth0 with the default route",
			links:  []ifaceCandidate{lo, eth0},
			routes: []netlink.Route{def(2, 0), {LinkIndex: 2, Dst: subnet}},
			want:   "eth0",
		},
		{
			name:   "not called eth0",
			links:  []ifaceCandidate{lo, veth},
			routes: []netlink.Route{def(4, 0)},
			want:   "veth3f2a",
		},
		{
			name:   "default route on the second network",
			links:  []ifaceCandidate{lo, eth0, net1},
			routes: []netlink.Route{{LinkIndex: 2, Dst: subnet}, def(3, 0)},
			want:   "net1",
		},
		{
			name:   "explicit 0.0.0.0/0 destination",
			links:  []ifaceCandidate{lo, eth0, net1},
			routes: []netlink.Route{{LinkIndex: 3, Dst: defaultDst, Gw: gw}},
			want:   "net1",
		},
		{
			name:   "lowest metric wins",
			links:  []ifaceCandidate{lo, eth0, net1},
			routes: []netlink.Route{def(2, 200), def(3, 100)},
			want:   "net1",
		},
		{
			name:    "equal-metric default routes are ambiguous",
			links:   []ifaceCandidate{lo, eth0, net1},
			routes:  []netlink.Route{def(2, 0), def(3, 0)},
			wantErr: []string{"eth0, net1", "NETSHIM_EXTERNAL_IFACE", "-external-iface"},
		},
		{
			name:  "multipath default route is ambiguous",
			links: []ifaceCandidate{lo, eth0, net1},
			routes: []netlink.Route{{MultiPath: []*netlink.NexthopInfo{
				{LinkIndex: 2, Gw: gw}, {LinkIndex: 3, Gw: gw},
			}}},
			wantErr: []string{"eth0, net1"},
		},
		{
			name:   "netshim's own links never count",
			links:  []ifaceCandidate{lo, bridge, tap, net1},
			routes: []netlink.Route{def(7, 0), def(3, 0)},
			want:   "net1",
		},
		{
			name:   "no default route, one addressed interface",
			links:  []ifaceCandidate{lo, bare, net1, bridge, tap},
			routes: []netlink.Route{{LinkIndex: 3, Dst: subnet}},
			want:   "net1",
		},
		{
			name:    "no default route, several addressed interfaces",
			links:   []ifaceCandidate{lo, net1, eth0, bridge},
			wantErr: []string{"no default route", "eth0, net1", "NETSHIM_EXTERNAL_IFACE", "-external-iface"},
		},
		{
			name:    "nothing addressed",
			links:   []ifaceCandidate{lo, bare, bridge},
			wantErr: []string{"no non-loopback interface", "NETSHIM_EXTERNAL_IFACE"},
		},
		{
			name:   "default route through a link that is gone",
			links:  []ifaceCandidate{lo, net1},
			routes: []netlink.Route{def(42, 0)},
			want:   "net1",
		},
		{
			name:   "v6-only: the interface carrying ::/0",
			links:  []ifaceCandidate{lo, eth0, net1},
			routes: []netlink.Route{def6(3, 1024), {LinkIndex: 2, Dst: subnet6, Family: netlink.FAMILY_V6}},
			want:   "net1",
		},
		{
			name:   "dual-stack defaults on the same interface",
			links:  []ifaceCandidate{lo, eth0, net1},
			routes: []netlink.Route{def(2, 0), def6(2, 1024)},
			want:   "eth0",
		},
		{
			name:    "dual-stack defaults on different interfaces",
			links:   []ifaceCandidate{lo, eth0, net1},
			routes:  []netlink.Route{def(2, 0), def6(3, 1024)},
			wantErr: []string{"IPv4 default route is on eth0", "IPv6 one is on net1", "NETSHIM_EXTERNAL_IFACE"},
		},
		{
			name:    "equal-metric IPv6 default routes are ambiguous",
			links:   []ifaceCandidate{lo, eth0, net1},
			routes:  []netlink.Route{def6(2, 1024), def6(3, 1024)},
			wantErr: []string{"IPv6 default route", "eth0, net1"},
		},
		{
			name:   "an unreachable IPv6 default on lo is no way out",
			links:  []ifaceCandidate{lo, eth0},
			routes: []netlink.Route{def(2, 0), {LinkIndex: 1, Dst: default6, Family: netlink.FAMILY_V6, Type: unix.RTN_UNREACHABLE}},
			want:   "eth0",
		},
		{
			name:    "IPv6 default inferred from a hand-built route's gateway",
			links:   []ifaceCandidate{lo, eth0, net1},
			routes:  []netlink.Route{def(2, 0), {LinkIndex: 3, Gw: gw6}},
			wantErr: []string{"IPv4 default route is on eth0", "IPv6 one is on net1"},
		},
		{
			name:  "no default route, one interface with a global IPv6 address",
			links: []ifaceCandidate{lo, bare, v6only, bridge},
			want:  "net6",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pickExternalIface(tc.links, tc.routes, own)
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("pickExternalIface = %q, want an error", got)
				}
				for _, w := range tc.wantErr {
					if !strings.Contains(err.Error(), w) {
						t.Errorf("error %q does not mention %q", err, w)
					}
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("pickExternalIface = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
