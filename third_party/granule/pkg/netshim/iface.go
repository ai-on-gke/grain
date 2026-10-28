package netshim

import (
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// AutoExternalIface is the ExternalIface value (and NETSHIM_EXTERNAL_IFACE,
// and granule's -external-iface) that asks netshim to find the sandbox's
// primary interface itself rather than be told its name. The empty string
// means the same thing.
//
// It is the default because "eth0" is a convention, not a guarantee: a
// Multus or secondary-network pod, a runtime or CNI that names the pod's
// interface something else (net1, a veth-named interface), or a
// container with several networks attached where eth0 is not the one
// carrying the default route all break it. The interface the namespace
// actually routes out of is the one the guest has to take over.
const AutoExternalIface = "auto"

// IsAutoExternalIface reports whether name asks for the external
// interface to be found rather than naming it.
func IsAutoExternalIface(name string) bool {
	name = strings.TrimSpace(name)
	return name == "" || name == AutoExternalIface
}

// ResolveExternalIface returns cfg with ExternalIface naming a real
// interface. An explicit name is returned untouched, exactly as given --
// whether it exists is DiscoverIdentity's to report, as it always has
// been. Otherwise the namespace is asked (see pickExternalIface).
//
// It must run inside the sandbox's network namespace: that is the only
// place its links and routes can be seen, which is why granule resolves
// it in the VM container rather than in "granule vm create" on the host.
func (c Config) ResolveExternalIface() (Config, error) {
	if !IsAutoExternalIface(c.ExternalIface) {
		return c, nil
	}

	// With IPv6 off the namespace is read as though it had none, so
	// the interface chosen is the one an IPv4-only netshim would choose.
	v6 := c.IPv6Enabled()

	links, err := netlink.LinkList()
	if err != nil {
		return Config{}, fmt.Errorf("finding the external interface: listing links: %w", err)
	}
	cands := make([]ifaceCandidate, 0, len(links))
	for _, l := range links {
		attrs := l.Attrs()
		addrs, err := netlink.AddrList(l, netlink.FAMILY_V4)
		if err != nil {
			return Config{}, fmt.Errorf("finding the external interface: reading addresses on %s: %w", attrs.Name, err)
		}
		addressed := len(addrs) > 0
		if !addressed && v6 {
			addrs6, err := addrList6(l)
			if err != nil {
				return Config{}, fmt.Errorf("finding the external interface: reading IPv6 addresses on %s: %w", attrs.Name, err)
			}
			addressed = globalIPv6(addrs6) != nil
		}
		cands = append(cands, ifaceCandidate{
			Name:      attrs.Name,
			Index:     attrs.Index,
			Loopback:  attrs.Flags&net.FlagLoopback != 0,
			Addressed: addressed,
		})
	}
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		return Config{}, fmt.Errorf("finding the external interface: listing routes: %w", err)
	}
	if v6 {
		routes6, err := routeList6(nil)
		if err != nil {
			return Config{}, fmt.Errorf("finding the external interface: listing IPv6 routes: %w", err)
		}
		routes = append(routes, routes6...)
	}

	name, err := pickExternalIface(cands, routes, c.ownLinks())
	if err != nil {
		return Config{}, err
	}
	c.ExternalIface = name
	return c, nil
}

// ownLinks is the interfaces netshim itself creates in the namespace,
// none of which can ever be the external interface. The control bridge
// in particular holds an IPv4 address once Setup has run, so without
// this a second run (a restarted VM container in a surviving pod) would
// see two addressed interfaces where the first saw one.
func (c Config) ownLinks() []string {
	return []string{c.Bridge, c.TapName(), c.ControlTapName()}
}

// ifaceCandidate is what pickExternalIface needs to know about one link,
// separated from netlink.Link so the choice can be tested against a
// made-up namespace.
type ifaceCandidate struct {
	Name     string
	Index    int
	Loopback bool

	// Addressed is whether the link holds a global address of either
	// family: any IPv4 address, or an IPv6 one globalIPv6 would take.
	// A link-local IPv6 address never counts, since every interface has
	// one -- netshim's own control bridge included.
	Addressed bool
}

// pickExternalIface chooses the sandbox's primary interface out of a
// namespace's links and its routes, of either family:
//
//  1. the interface carrying the default route -- the one the runtime
//     configured as the way out, and so the one whose identity the rest
//     of the network knows this sandbox by. With several default routes
//     of one family the one with the lowest metric wins, as it does for
//     the kernel; a tie between different interfaces (including a
//     multipath route across several) is ambiguous. The IPv4 default
//     (0.0.0.0/0) and the IPv6 one (::/0) are chosen separately, and
//     have to agree when both exist: a pod whose two families leave by
//     different interfaces has no one interface to take over.
//  2. with no default route of either family, the one non-loopback
//     interface holding a global address of either family.
//
// Anything else is an error naming what it found, since guessing wrong
// hands the guest somebody else's address. own names netshim's own
// links, which are never candidates.
func pickExternalIface(links []ifaceCandidate, routes []netlink.Route, own []string) (string, error) {
	byIndex := make(map[int]ifaceCandidate, len(links))
	for _, l := range links {
		byIndex[l.Index] = l
	}
	excluded := func(l ifaceCandidate) bool {
		if l.Loopback || l.Name == "lo" {
			return true
		}
		for _, o := range own {
			if o != "" && l.Name == o {
				return true
			}
		}
		return false
	}

	var routes4, routes6 []netlink.Route
	for _, r := range routes {
		if routeFamily(r) == netlink.FAMILY_V6 {
			routes6 = append(routes6, r)
		} else {
			routes4 = append(routes4, r)
		}
	}
	via4, err := defaultRouteIface(routes4, byIndex, excluded, "IPv4")
	if err != nil {
		return "", err
	}
	via6, err := defaultRouteIface(routes6, byIndex, excluded, "IPv6")
	if err != nil {
		return "", err
	}
	switch {
	case via4 != "" && via6 != "" && via4 != via6:
		return "", fmt.Errorf("finding the external interface: the IPv4 default route is on %s but the IPv6 one is on %s; "+
			"name the one the guest should take over with %s (granule vm create -external-iface)",
			via4, via6, envExternalIface)
	case via4 != "":
		return via4, nil
	case via6 != "":
		return via6, nil
	}

	var addressed []string
	for _, l := range links {
		if excluded(l) || !l.Addressed {
			continue
		}
		addressed = appendUnique(addressed, l.Name)
	}
	switch len(addressed) {
	case 1:
		return addressed[0], nil
	case 0:
		return "", fmt.Errorf("finding the external interface: no default route and no non-loopback interface with an IPv4 or global IPv6 address; "+
			"name the one the guest should take over with %s (granule vm create -external-iface)", envExternalIface)
	default:
		sort.Strings(addressed)
		return "", fmt.Errorf("finding the external interface: no default route, and more than one interface has an IPv4 or global IPv6 address (%s); "+
			"name the one the guest should take over with %s (granule vm create -external-iface)",
			strings.Join(addressed, ", "), envExternalIface)
	}
}

// defaultRouteIface is the interface carrying the best default route
// among routes, all of one family (named by family, for the error), or
// "" when none of them is a default route through a candidate link.
func defaultRouteIface(routes []netlink.Route, byIndex map[int]ifaceCandidate, excluded func(ifaceCandidate) bool, family string) (string, error) {
	best := -1
	var onDefault []string
	for _, r := range routes {
		if !isDefaultDst(r.Dst) {
			continue
		}
		// An unreachable or blackhole default is not a way out, and
		// IPv6 has one of its own on lo when nothing else routes.
		if r.Type != 0 && r.Type != unix.RTN_UNICAST {
			continue
		}
		var idx []int
		if r.LinkIndex != 0 {
			idx = append(idx, r.LinkIndex)
		}
		for _, nh := range r.MultiPath {
			idx = append(idx, nh.LinkIndex)
		}
		var names []string
		for _, i := range idx {
			l, ok := byIndex[i]
			if !ok || excluded(l) {
				continue
			}
			names = appendUnique(names, l.Name)
		}
		if len(names) == 0 {
			continue
		}
		switch {
		case best == -1 || r.Priority < best:
			best, onDefault = r.Priority, names
		case r.Priority == best:
			for _, n := range names {
				onDefault = appendUnique(onDefault, n)
			}
		}
	}
	switch len(onDefault) {
	case 0:
		return "", nil
	case 1:
		return onDefault[0], nil
	default:
		sort.Strings(onDefault)
		return "", fmt.Errorf("finding the external interface: the %s default route is carried by more than one interface (%s); "+
			"name the one the guest should take over with %s (granule vm create -external-iface)",
			family, strings.Join(onDefault, ", "), envExternalIface)
	}
}

func appendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

// Discover is DiscoverIdentity for a Config: it resolves the external
// interface first, so a Config left on AutoExternalIface reads the same
// interface Setup splices. Identity.Iface is the name it settled on.
//
// It also records the MTU the guest's tap already has (Identity.TapMTU),
// if the tap exists. Like the rest of discovery, that only reads.
func Discover(cfg Config) (Identity, error) {
	cfg, err := cfg.ResolveExternalIface()
	if err != nil {
		return Identity{}, err
	}
	id, err := cfg.identity()
	if err != nil {
		return Identity{}, err
	}
	if tap, err := netlink.LinkByName(cfg.TapName()); err == nil {
		id.TapMTU = tap.Attrs().MTU
	}
	return id, nil
}
