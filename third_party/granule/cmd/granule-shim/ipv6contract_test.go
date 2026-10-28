package main

import (
	"net"
	"strings"
	"testing"

	"github.com/bwsalmon/granule/pkg/guestimage"
	"github.com/bwsalmon/granule/pkg/netshim"
)

// TestIPv6ContractWarning: a guest image older than ContractIPv6 ignores
// granule.ip6=, so booting one into a namespace with IPv6 is worth a
// warning -- and for a v6-only identity, the warning has to say the guest
// will have no network at all.
func TestIPv6ContractWarning(t *testing.T) {
	v4 := netshim.Identity{Iface: "eth0", IP: net.IPv4(10, 0, 0, 5).To4(), Mask: net.CIDRMask(24, 32)}
	dual := v4
	dual.IP6, dual.Prefix6 = net.ParseIP("fd00::5"), 64
	v6 := netshim.Identity{Iface: "eth0", IP6: dual.IP6, Prefix6: 64}

	old := guestimage.BootContract{State: guestimage.ContractSupported, Version: guestimage.ContractIPv6 - 1, Path: "/disk/guest-contract"}
	undeclared := guestimage.BootContract{State: guestimage.ContractUndeclared, Version: guestimage.ContractUndeclaredVersion, Path: "/disk/guest-contract"}
	current := guestimage.BootContract{State: guestimage.ContractSupported, Version: guestimage.ContractIPv6, Path: "/disk/guest-contract"}

	for _, tc := range []struct {
		name     string
		contract guestimage.BootContract
		id       netshim.Identity
		want     string // "" for no warning
	}{
		{"IPv4 only on an old image", old, v4, ""},
		{"dual stack on a current image", current, dual, ""},
		{"v6 only on a current image", current, v6, ""},
		{"dual stack on an old image", old, dual, "this guest image does not configure IPv6, so it will have none"},
		{"dual stack on an undeclared image", undeclared, dual, "this guest image does not configure IPv6, so it will have none"},
		{"v6 only on an old image", old, v6, "this guest will have no network at all"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ipv6ContractWarning(tc.contract, tc.id)
			if tc.want == "" {
				if got != "" {
					t.Errorf("warning = %q, want none", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("warning = %q, want it to say %q", got, tc.want)
			}
		})
	}
}
