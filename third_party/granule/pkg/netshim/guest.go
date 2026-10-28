package netshim

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net"
	"sort"
	"strings"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// routeParamKey is the kernel command-line parameter the guest's routes
// travel in. The kernel itself has nothing to do with it -- it takes no
// route parameter of any kind, which is the whole problem -- so this is
// namespaced like every other parameter a kernel passes through to
// userspace, and read there by granule-configure-routes (see
// deploy/guest-image/).
const (
	routeParamKey = "granule.routes"
	mtuParamKey   = "granule.mtu"

	// ip6ParamKey and route6ParamKey are the IPv6 half's equivalents of
	// ip= and granule.routes=, read by granule-configure-ip6 and
	// granule-configure-routes. They are parameters of their own rather
	// than more of the IPv4 ones so that a guest image predating them
	// (contract < 3) ignores them, instead of handing "::/0" to a v4
	// route pass whose failure would then skip its prune.
	ip6ParamKey    = "granule.ip6"
	route6ParamKey = "granule.routes6"

	// maxCmdline is the kernel's COMMAND_LINE_SIZE on x86 and arm64. A
	// longer command line is truncated by the kernel without a word, so
	// WithGuestParams says so instead.
	maxCmdline = 2048

	// dnsParamKey carries the guest's nameservers in either family, in
	// order, where ip='s dns0/dns1 cannot: an IPv6 address is made of
	// the colons that separate ip='s fields, and a v6-only guest has no
	// ip= at all. dnsSearchParamKey and dnsOptionsParamKey carry the
	// search list and resolver options that go into the guest's
	// resolv.conf beside the nameservers. All three are read by
	// granule-configure-dns.
	dnsParamKey        = "granule.dns"
	dnsSearchParamKey  = "granule.dns_search"
	dnsOptionsParamKey = "granule.dns_options"
)

// Identity is the network identity the container runtime assigned to this
// network namespace: the address, MAC and MTU it put on the external
// interface. The guest takes this identity over wholesale, so that from
// outside the namespace there is still exactly one endpoint, with the
// address and MAC the runtime allocated -- see Setup.
type Identity struct {
	// Iface is the interface the identity was read from, i.e. the one
	// the runtime configured (Config.ExternalIface).
	Iface string

	MAC net.HardwareAddr

	// IP is the interface's IPv4 address, or nil when it has none -- a
	// v6-only pod. Mask, Gateway and Routes are the rest of the IPv4
	// half, and are empty with it.
	IP net.IP
	// Mask is IP's netmask, and Gateway the namespace's default route.
	// Gateway may be nil if the namespace has no default route, which is
	// unusual but not fatal: the guest simply gets no default route
	// either -- and so, since this is the only thing that ever gives it
	// one, no way off its own segment. A guest in that state boots and
	// answers perfectly well; see defaultGateway.
	Mask    net.IPMask
	Gateway net.IP
	MTU     int

	// TapMTU is the MTU the guest's tap already has, or 0 if there is
	// no such tap yet (see Discover). When it equals MTU, GuestConfig
	// leaves mtu= off the --net, because cloud-hypervisor sets a tap's
	// MTU with SIOCSIFMTU whenever it is given one, and that ioctl needs
	// CAP_NET_ADMIN even on a tap it is allowed to open. Left off, it
	// reads the MTU from the tap instead and still advertises it to the
	// guest (VIRTIO_NET_F_MTU). That is what lets a VM container attach
	// to a tap netshim built, with no capabilities of its own.
	TapMTU int

	// Routes is the interface's whole IPv4 routing table, in the order
	// the guest has to reinstall it in (see carriedRoutes).
	//
	// Address, netmask and gateway describe a bridge CNI's world
	// exactly, and a point-to-point one's not at all: there the subnet
	// is deliberately *not* on-link, and the route saying so is the one
	// part of the identity a netmask cannot express. Carrying the table
	// itself is what makes the two cases the same case.
	Routes []Route

	// IP6 is the interface's global IPv6 address -- a ULA counts, as it
	// is routed rather than link-scoped -- and Prefix6 its prefix length.
	// IP6 is nil when the interface has no such address, and the IPv6
	// half is then not part of the identity at all, whatever else is
	// recorded beside it.
	IP6     net.IP
	Prefix6 int

	// LinkLocal6 is the interface's fe80:: address, nil when it has none.
	// The guest is to take this exact address over rather than derive one
	// of its own: a point-to-point CNI's host side may already hold it in
	// its neighbour cache.
	LinkLocal6 net.IP

	// Routes6 is the interface's IPv6 main-table routes, chosen and
	// ordered as Routes is (see carriedRoutes). Only read when IP6 is
	// set. Gateways here are commonly link-local, which is fine: every
	// carried route is installed on the guest's one interface.
	Routes6 []Route
}

// HasIPv4 reports whether the identity has an IPv4 half.
func (id Identity) HasIPv4() bool { return id.IP != nil }

// HasIPv6 reports whether the identity has an IPv6 half: a global (or
// ULA) address. A link-local address alone carries nothing worth taking
// over.
func (id Identity) HasIPv6() bool { return id.IP6 != nil }

// Addresses renders the identity's addresses for a log line: the IPv4
// one and the global IPv6 one, each with its prefix length, whichever of
// the two it has.
func (id Identity) Addresses() string {
	var addrs []string
	if id.HasIPv4() {
		addrs = append(addrs, fmt.Sprintf("%s/%d", id.IP, maskBits(id.Mask)))
	}
	if id.HasIPv6() {
		addrs = append(addrs, fmt.Sprintf("%s/%d", id.IP6, id.Prefix6))
	}
	return strings.Join(addrs, " ")
}

// withoutIPv6 is the identity with its IPv6 half discarded, which is
// what NETSHIM_IPV6=off hands on.
func (id Identity) withoutIPv6() Identity {
	id.IP6, id.Prefix6, id.LinkLocal6, id.Routes6 = nil, 0, nil, nil
	return id
}

// Route is one entry of that table, in the only two shapes a container's
// own routes ever take: a destination reached directly over the
// interface, or one reached through a gateway.
type Route struct {
	// Dst is the destination prefix, masked to its network address. A
	// default route's is 0.0.0.0/0.
	Dst *net.IPNet

	// Gw is the gateway the destination is reached through, or nil for
	// a destination that is on-link.
	Gw net.IP
}

// String renders a route the way the guest is given it:
// "<destination>" for an on-link route, "<destination>@<gateway>" for a
// gatewayed one.
func (r Route) String() string {
	if r.Dst == nil {
		return ""
	}
	if r.Gw == nil {
		return r.Dst.String()
	}
	return r.Dst.String() + "@" + r.Gw.String()
}

// Netmask renders Mask in the dotted-decimal form the kernel's ip= boot
// parameter expects.
func (id Identity) Netmask() string {
	return net.IP(id.Mask).To4().String()
}

// DiscoverIdentity reads the runtime-assigned identity off iface. It only
// reads, so it needs no capabilities of its own -- which is what lets the
// VM container work out its own guest configuration from inside the
// shared namespace, rather than having it handed down as environment by
// whatever created the sandbox (see cmd/granule-shim's applyNetshimNet).
//
// It reads both families. The interface needs an IPv4 address or a
// global IPv6 one, and either half may be missing; it is an error only
// when both are.
func DiscoverIdentity(iface string) (Identity, error) {
	link, err := netlink.LinkByName(iface)
	if err != nil {
		return Identity{}, fmt.Errorf("looking up %s: %w", iface, err)
	}
	attrs := link.Attrs()

	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return Identity{}, fmt.Errorf("reading addresses on %s: %w", iface, err)
	}
	addrs6, err := addrList6(link)
	if err != nil {
		return Identity{}, fmt.Errorf("reading IPv6 addresses on %s: %w", iface, err)
	}
	global6 := globalIPv6(addrs6)
	if len(addrs) == 0 && global6 == nil {
		return Identity{}, fmt.Errorf("no IPv4 or IPv6 address found on %s", iface)
	}

	id := Identity{
		Iface: iface,
		MAC:   attrs.HardwareAddr,
		MTU:   attrs.MTU,
	}

	if len(addrs) > 0 {
		// The first address is the interface's primary one, which is what
		// a container runtime assigns and therefore what the guest takes
		// over. A namespace whose interface carries several is not
		// something any of the supported runtimes produce.
		id.IP = addrs[0].IP.To4()
		id.Mask = addrs[0].Mask

		routes, err := netlink.RouteList(link, netlink.FAMILY_V4)
		if err != nil {
			return Identity{}, fmt.Errorf("reading routes on %s: %w", iface, err)
		}
		id.Gateway = defaultGateway(routes)
		id.Routes = carriedRoutes(routes, netlink.FAMILY_V4)
	}

	id.LinkLocal6 = linkLocalIPv6(addrs6)
	if global6 != nil {
		id.IP6 = global6.IP.To16()
		id.Prefix6, _ = global6.Mask.Size()

		routes6, err := routeList6(link)
		if err != nil {
			return Identity{}, fmt.Errorf("reading IPv6 routes on %s: %w", iface, err)
		}
		id.Routes6 = carriedRoutes(routes6, netlink.FAMILY_V6)
	}

	return id, nil
}

// globalIPv6 is the address, out of a link's IPv6 ones, that the guest
// takes over: the first with universe scope that is not a privacy
// (temporary) address, deprecated, or one that failed duplicate address
// detection. A ULA (fd00::/8, what kind uses) counts: it is routed, not
// link-scoped. An address still tentative is taken anyway -- the runtime
// assigned it, and its own DAD stops mattering once the guest holds it.
// nil when there is no such address.
func globalIPv6(addrs []netlink.Addr) *netlink.Addr {
	const unwanted = unix.IFA_F_TEMPORARY | unix.IFA_F_DEPRECATED | unix.IFA_F_DADFAILED
	for i, a := range addrs {
		if a.IPNet == nil || a.IP.To4() != nil || a.IP.To16() == nil {
			continue
		}
		if a.Scope != unix.RT_SCOPE_UNIVERSE || a.Flags&unwanted != 0 {
			continue
		}
		if !a.IP.IsGlobalUnicast() {
			continue
		}
		return &addrs[i]
	}
	return nil
}

// linkLocalIPv6 is the first fe80::/10 address among addrs, or nil.
func linkLocalIPv6(addrs []netlink.Addr) net.IP {
	for _, a := range addrs {
		if a.IPNet == nil || a.IP.To4() != nil {
			continue
		}
		if a.IP.IsLinkLocalUnicast() {
			return a.IP.To16()
		}
	}
	return nil
}

// addrList6 and routeList6 read a link's IPv6 addresses and routes (all
// links' routes for a nil link), treating a kernel built or booted
// without IPv6 as one with nothing to read rather than as a failure: an
// IPv4 pod on such a host has to keep working exactly as it always has.
func addrList6(link netlink.Link) ([]netlink.Addr, error) {
	addrs, err := netlink.AddrList(link, netlink.FAMILY_V6)
	if isNoIPv6(err) {
		return nil, nil
	}
	return addrs, err
}

func routeList6(link netlink.Link) ([]netlink.Route, error) {
	routes, err := netlink.RouteList(link, netlink.FAMILY_V6)
	if isNoIPv6(err) {
		return nil, nil
	}
	return routes, err
}

func isNoIPv6(err error) bool {
	return errors.Is(err, unix.EAFNOSUPPORT) || errors.Is(err, unix.EPROTONOSUPPORT)
}

// carriedRoutes selects the routes worth handing the guest out of a dump
// of the external interface's table, and orders them so that they can be
// installed from first to last.
//
// On-link routes come first, because every gatewayed route depends on
// one: "10.244.0.0/24 via 10.244.0.1" can only be installed once
// 10.244.0.1 is itself reachable, and on a point-to-point CNI the only
// thing making it reachable is the "10.244.0.1/32 dev eth0" route in the
// same table. Installing them in the order the kernel happens to dump
// them would fail on that one route about half the time.
//
// Within each half the most specific prefix comes first, for the same
// reason one step further out (a gateway of one route reached through
// another) and for one more: a kernel dumps a table in an order of its
// own -- this one comes back with the default route ahead of the subnet
// route it is less specific than -- and a guest's routing table is not
// something to have depend on that.
//
// Anything that is not a plain unicast route is dropped: a blackhole,
// broadcast or local entry describes the namespace's own stack rather
// than a path off the interface, and means nothing to a guest that only
// took the interface over.
//
// family is netlink.FAMILY_V4 or netlink.FAMILY_V6, the family routes
// were dumped in. An IPv6 table also loses its fe80::/64, which the
// guest's kernel installs itself as soon as it has a link-local address,
// and anything multicast. Its gateways are kept as they are, link-local
// ones included.
func carriedRoutes(routes []netlink.Route, family int) []Route {
	var onLink, viaGateway []Route
	for _, r := range routes {
		// A dump fills Type in; a Route a caller built itself may not,
		// and zero is not RTN_UNICAST, so an unset Type is taken at
		// face value rather than as a reason to drop the route.
		if r.Type != 0 && r.Type != unix.RTN_UNICAST {
			continue
		}
		dst := normalizeDst(r.Dst, family)
		if dst == nil {
			continue
		}
		route := Route{Dst: dst, Gw: gatewayIn(r.Gw, family)}
		if route.Gw == nil {
			onLink = append(onLink, route)
			continue
		}
		viaGateway = append(viaGateway, route)
	}
	sortBySpecificity(onLink)
	sortBySpecificity(viaGateway)
	return append(onLink, viaGateway...)
}

// gatewayIn is gw in family's own representation, or nil when it is not
// an address of that family.
func gatewayIn(gw net.IP, family int) net.IP {
	if family == netlink.FAMILY_V6 {
		if gw == nil || gw.To4() != nil {
			return nil
		}
		return gw.To16()
	}
	return gw.To4()
}

// sortBySpecificity orders routes longest prefix first, and destinations
// of equal length by address, so that the same table always renders the
// same way.
func sortBySpecificity(routes []Route) {
	sort.SliceStable(routes, func(i, j int) bool {
		iOnes, _ := routes[i].Dst.Mask.Size()
		jOnes, _ := routes[j].Dst.Mask.Size()
		if iOnes != jOnes {
			return iOnes > jOnes
		}
		return bytes.Compare(routes[i].Dst.IP, routes[j].Dst.IP) < 0
	})
}

// normalizeDst turns a dumped route's destination into a masked prefix
// of family (netlink.FAMILY_V4 or netlink.FAMILY_V6), or nil for a
// destination this cannot describe or the guest is not to be given. A
// nil Dst is a default route -- 0.0.0.0/0 or ::/0: the kernel leaves
// RTA_DST off a route whose prefix length is zero (see defaultGateway),
// and while a dump has that filled back in, a hand-built Route does not.
func normalizeDst(dst *net.IPNet, family int) *net.IPNet {
	if family == netlink.FAMILY_V6 {
		return normalizeDst6(dst)
	}
	if dst == nil {
		return &net.IPNet{IP: net.IPv4zero.To4(), Mask: net.CIDRMask(0, 32)}
	}
	ip := dst.IP.To4()
	ones, bits := dst.Mask.Size()
	if ip == nil || bits != 32 {
		return nil
	}
	mask := net.CIDRMask(ones, 32)
	return &net.IPNet{IP: ip.Mask(mask), Mask: mask}
}

// fe80Prefix is the link-local route every IPv6 interface has, and which
// the guest's kernel installs for itself.
var fe80Prefix = &net.IPNet{IP: net.ParseIP("fe80::"), Mask: net.CIDRMask(64, 128)}

func normalizeDst6(dst *net.IPNet) *net.IPNet {
	if dst == nil {
		return &net.IPNet{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)}
	}
	ones, bits := dst.Mask.Size()
	if dst.IP.To4() != nil || dst.IP.To16() == nil || bits != 128 {
		return nil
	}
	mask := net.CIDRMask(ones, 128)
	ip := dst.IP.To16().Mask(mask)
	if ip.IsMulticast() {
		return nil
	}
	if ones == 64 && ip.Equal(fe80Prefix.IP) {
		return nil
	}
	return &net.IPNet{IP: ip, Mask: mask}
}

// routeFamily is the address family r belongs to. A dump sets Family; a
// Route a caller built itself may not, and is then read off its
// destination or gateway, falling back to IPv4 for a route that names
// neither -- which is what such a route always meant here.
func routeFamily(r netlink.Route) int {
	if r.Family == netlink.FAMILY_V4 || r.Family == netlink.FAMILY_V6 {
		return r.Family
	}
	for _, ip := range []net.IP{ipOf(r.Dst), r.Gw} {
		if ip == nil {
			continue
		}
		if ip.To4() != nil {
			return netlink.FAMILY_V4
		}
		return netlink.FAMILY_V6
	}
	for _, nh := range r.MultiPath {
		if nh.Gw != nil && nh.Gw.To4() == nil {
			return netlink.FAMILY_V6
		}
	}
	return netlink.FAMILY_V4
}

func ipOf(n *net.IPNet) net.IP {
	if n == nil {
		return nil
	}
	return n.IP
}

// defaultGateway returns the gateway of the first default route in
// routes, or nil if none of them is one.
//
// It matches on the destination's prefix length rather than on a nil
// Dst, because a nil Dst is not what a route read back off the kernel
// has. The kernel leaves RTA_DST off a route message whose prefix length
// is zero, and the netlink library fills that absence back in the way
// iproute2 does -- deserializeRoute synthesizes 0.0.0.0/0 rather than
// leaving Dst nil -- so every route in a dump carries a non-nil Dst and
// a "Dst == nil" test can only ever match a Route a caller built itself.
//
// That is not a hypothetical: testing "Dst == nil" is what left every
// guest without a default route (the ip= parameter GuestConfig derives
// has an empty gateway field when Identity.Gateway is nil), and so with
// no egress off its own segment at all, while every other part of the
// identity it took over was correct.
func defaultGateway(routes []netlink.Route) net.IP {
	for _, r := range routes {
		if r.Gw == nil || !isDefaultDst(r.Dst) {
			continue
		}
		return r.Gw.To4()
	}
	return nil
}

// isDefaultDst reports whether dst is a default route's destination:
// either absent altogether, or the all-zero address with a zero-length
// prefix (0.0.0.0/0).
func isDefaultDst(dst *net.IPNet) bool {
	if dst == nil {
		return true
	}
	ones, _ := dst.Mask.Size()
	return ones == 0 && dst.IP.IsUnspecified()
}

// Guest is the guest-side half of a netshim setup: what to hand
// cloud-hypervisor as --net, and the kernel ip= parameter that makes the
// guest configure the identity it has taken over. It is derived rather
// than configured, so the same values come out whether the sandbox was
// created by docker, by a kubelet, or by hand.
type Guest struct {
	// Nets holds one --net value per guest NIC: the spliced interface
	// first, then the control link if there is one.
	Nets []string

	// IPParam is the kernel ip= parameter configuring the first NIC.
	// The control NIC is not covered by it -- the kernel's own ip=
	// handling configures a single interface -- so a guest that wants
	// the control link has to bring its own second address up itself.
	// Empty for a v6-only identity: ip= is IPv4 only.
	IPParam string

	// IP6Param is granule.ip6=<global>/<plen>[,<link-local>], the IPv6
	// half of the identity, and Route6Param granule.routes6=, its routes
	// in granule.routes='s grammar. Both are empty when the identity has
	// no IPv6 half, which leaves an IPv4-only guest's command line
	// exactly what it was before IPv6 existed.
	IP6Param    string
	Route6Param string

	// RouteParam is the kernel parameter carrying the routes the
	// runtime installed on the external interface, which the guest
	// reinstalls at boot over the table ip= alone leaves it with.
	// Empty when the interface had no routes to carry.
	//
	// It is a parameter of granule's own rather than more of ip=,
	// because ip= cannot express this: it has one netmask and no route
	// list, so a guest configured from it alone treats its whole subnet
	// as on-link -- true on a bridge CNI, and precisely what a
	// point-to-point CNI arranges for it not to be.
	RouteParam string

	// MTUParam is the kernel parameter carrying the discovered MTU of
	// the external interface, which the guest configures on its interface
	// at boot. Empty when the interface MTU is <= 0.
	MTUParam string

	// DNSParam is the kernel parameter carrying the whole nameserver
	// list, in either family (granule.dns=). Empty when IPParam carries
	// all of it already -- IPv4 nameservers only -- so an IPv4 guest's
	// command line is what it was before IPv6 existed.
	DNSParam string

	// DNSSearchParam and DNSOptionsParam are the kernel parameters
	// carrying the search list and resolver options (ndots) the guest's
	// resolv.conf gets beside its nameservers. Empty when
	// there are none, which is always the case for nameservers named
	// explicitly or for FallbackDNS: only a resolver read off the
	// sandbox's own resolv.conf comes with them (Config.ResolveDNS).
	DNSSearchParam  string
	DNSOptionsParam string

	// ControlIP is the address the guest is expected to configure on the
	// control NIC, and therefore where this namespace reaches it (for
	// "granule exec" and the memory agent). Empty when there is no
	// control link.
	ControlIP string
}

// GuestConfig derives the guest configuration for a VM from the identity
// discovered on the external interface.
func GuestConfig(cfg Config, id Identity) Guest {
	// mac= is what makes the takeover invisible from outside: the guest
	// presents the very MAC the runtime put on the veth, so the segment
	// sees the same single endpoint it always did rather than a second
	// one appearing behind an authorized port.
	net0 := fmt.Sprintf("tap=%s,mac=%s", cfg.TapName(), id.MAC)
	if id.TapMTU != id.MTU {
		net0 += fmt.Sprintf(",mtu=%d", id.MTU)
	}
	g := Guest{Nets: []string{net0}}

	if id.HasIPv4() {
		gw := ""
		if id.Gateway != nil {
			gw = id.Gateway.String()
		}
		g.IPParam = fmt.Sprintf("ip=%s::%s:%s::eth0:off%s", id.IP, gw, id.Netmask(), DNSFields(cfg.DNS))
		g.RouteParam = routeParam(routeParamKey, id.Routes)
	}
	if id.HasIPv6() {
		g.IP6Param = ip6Param(id)
		g.Route6Param = routeParam(route6ParamKey, id.Routes6)
	}
	if id.MTU > 0 {
		g.MTUParam = fmt.Sprintf("%s=%d", mtuParamKey, id.MTU)
	}
	g.DNSParam = dnsParam(cfg.DNS, g.IPParam != "")
	// A search list without a nameserver to search through would be
	// written nowhere: granule-configure-dns leaves the image's own file
	// alone when the command line names no nameserver.
	if len(cfg.DNS) > 0 {
		g.DNSSearchParam = dnsSearchParam(cfg.DNSSearch)
		g.DNSOptionsParam = dnsOptionsParam(cfg.DNSOptions)
	}

	if cfg.ControlNet != nil {
		g.Nets = append(g.Nets, fmt.Sprintf("tap=%s", cfg.ControlTapName()))
		g.ControlIP = ControlGuestIP(cfg.ControlAddr).String()
	}

	return g
}

// ControlGuestIP returns the address the guest is expected to hold on the
// control link, one past the address this namespace holds on it (so the
// default 169.254.100.1/24 control bridge puts the guest at
// 169.254.100.2).
func ControlGuestIP(bridgeAddr net.IP) net.IP {
	ip4 := bridgeAddr.To4()
	if ip4 == nil {
		return nil
	}
	next := make(net.IP, 4)
	copy(next, ip4)
	for i := 3; i >= 0; i-- {
		next[i]++
		if next[i] != 0 {
			break
		}
	}
	return next
}

func maskBits(m net.IPMask) int {
	ones, _ := m.Size()
	return ones
}

// routeParam renders routes as the kernel parameter the guest reads
// them back out of: comma-separated entries, each "<destination>" or
// "<destination>@<gateway>", in the order they have to be installed in.
// Empty for an interface with no routes, which is a guest with nothing
// to reinstall rather than one to hand an empty list to.
//
// The preferred source address is deliberately not carried, though a
// point-to-point CNI sets one on the routes it installs: the guest's
// interface holds exactly one address -- the one it took over -- so the
// kernel picks that same address for these routes anyway.
func routeParam(key string, routes []Route) string {
	if len(routes) == 0 {
		return ""
	}
	specs := make([]string, 0, len(routes))
	for _, r := range routes {
		if s := r.String(); s != "" {
			specs = append(specs, s)
		}
	}
	if len(specs) == 0 {
		return ""
	}
	return key + "=" + strings.Join(specs, ",")
}

// ip6Param renders the IPv6 half of id as granule.ip6=: the global
// address with its prefix length, then the link-local address the guest
// is to hold verbatim rather than derive. The device is not named: like
// granule.routes=, it is the one ip= names, or eth0 when there is no ip=.
func ip6Param(id Identity) string {
	v := fmt.Sprintf("%s=%s/%d", ip6ParamKey, id.IP6, id.Prefix6)
	if id.LinkLocal6 != nil {
		v += "," + id.LinkLocal6.String()
	}
	return v
}

// WithIPParam appends ipParam to a kernel command line, unless it already
// configures an interface itself. An explicit ip= in CHV_CMDLINE is an
// operator overriding the derived identity on purpose, so it wins.
func WithIPParam(cmdline, ipParam string) string {
	if ipParam == "" {
		return cmdline
	}
	if hasParam(cmdline, "ip=") {
		return cmdline
	}
	return appendParam(cmdline, ipParam)
}

// WithGuestParams appends everything GuestConfig derived for the guest's
// first NIC to a kernel command line: the ip= that addresses it, the
// granule.routes= that gives it the table the runtime really installed,
// their IPv6 equivalents granule.ip6= and granule.routes6=, the
// granule.mtu= that sets its interface MTU, and the granule.dns=,
// granule.dns_search= and granule.dns_options= that make its resolv.conf.
//
// An explicit ip= or granule.ip6= in CHV_CMDLINE takes all of them away,
// of both families, not just the one it names. They describe one
// interface between them, so handing a hand-addressed guest the
// namespace's routes or MTU -- or the other family's address -- would be
// applying part of a configuration nobody asked for, and the part likelier
// to be wrong, since an operator overriding the identity is by definition
// not the identity these were read off.
func WithGuestParams(cmdline string, g Guest) string {
	if g.IPParam == "" && g.IP6Param == "" {
		return cmdline
	}
	if hasParam(cmdline, "ip=") || hasParam(cmdline, ip6ParamKey+"=") {
		return cmdline
	}
	out := cmdline
	for _, p := range []struct{ key, param string }{
		{"ip", g.IPParam},
		{routeParamKey, g.RouteParam},
		{ip6ParamKey, g.IP6Param},
		{route6ParamKey, g.Route6Param},
		{mtuParamKey, g.MTUParam},
		{dnsParamKey, g.DNSParam},
		{dnsSearchParamKey, g.DNSSearchParam},
		{dnsOptionsParamKey, g.DNSOptionsParam},
	} {
		if p.param != "" && !hasParam(out, p.key+"=") {
			out = appendParam(out, p.param)
		}
	}
	if len(out) > maxCmdline {
		log.Printf("warning: the guest's kernel command line is %d bytes, past the kernel's %d; the kernel truncates it, and whatever falls off the end -- most likely the network parameters appended last -- is lost", len(out), maxCmdline)
	}
	return out
}

func hasParam(cmdline, prefix string) bool {
	for _, field := range strings.Fields(cmdline) {
		if strings.HasPrefix(field, prefix) {
			return true
		}
	}
	return false
}

func appendParam(cmdline, param string) string {
	if cmdline == "" {
		return param
	}
	return cmdline + " " + param
}

// DNSFields renders the IPv4 nameservers among dns, the first maxDNS4 of
// them, as the trailing dns0/dns1 fields of an ip= boot parameter --
// ":8.8.8.8" for one, ":8.8.8.8:1.1.1.1" for two -- or the empty string
// for none, which leaves the parameter exactly as short as it always
// was. An IPv6 nameserver has no place there (ip='s fields are colon
// separated); it reaches the guest on granule.dns= instead.
//
// The guest is what acts on them: nothing in the kernel's own ip=
// handling writes /etc/resolv.conf, so the guest's granule-configure-dns
// reads them back off /proc/cmdline and writes the file itself (see
// deploy/guest-image/overlay-common). Passing them here rather than
// baking a resolver into the guest image is what makes the resolver a
// per-boot setting: one image, and each deployment names the nameserver
// its own network actually has.
func DNSFields(dns []net.IP) string {
	var b strings.Builder
	n := 0
	for _, ip := range dns {
		ip4 := ip.To4()
		if ip4 == nil || n == maxDNS4 {
			continue
		}
		b.WriteString(":")
		b.WriteString(ip4.String())
		n++
	}
	return b.String()
}
