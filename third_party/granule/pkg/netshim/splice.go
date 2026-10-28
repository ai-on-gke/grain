package netshim

import (
	"errors"
	"fmt"
	"net"
	"syscall"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// qdiscAddAttempts bounds how many times addIngressQdisc retries on
// EBUSY. Adding an ingress qdisc races with anything else touching the
// same device's qdisc tree (including the kernel finishing a previous
// delete), and the kernel reports that race as EBUSY rather than
// blocking, so a bounded retry is the documented way to handle it.
const qdiscAddAttempts = 5

// splice cross-connects two links so that every frame arriving on either
// one is transmitted out the other: a wire, not a switch. It is what puts
// the guest directly onto the container's own network segment without a
// bridge in between.
//
// Each direction is an ingress qdisc plus a match-everything filter whose
// action is mirred/egress-redirect. "Ingress" selects frames *arriving*
// on the device; "egress redirect" hands them to the other device's
// transmit path. Redirect (as opposed to mirror) reuses the same skb
// rather than cloning it, so nothing is copied and the frame never leaves
// the kernel.
//
// Because there is no forwarding database involved, both ends may carry
// the same MAC address -- which is the entire point: it lets the guest
// boot with the address and MAC the container runtime assigned to the
// veth, so nothing upstream sees a new endpoint appear. A bridge cannot
// do this, since one MAC on two of its ports would flap its FDB.
//
// splice is idempotent: each direction's ingress qdisc is deleted before
// being re-added, which takes that qdisc's filters with it, so a rerun
// (the same way Kubernetes may retry a failed init container) reinstalls
// the same end state rather than accumulating duplicate filters.
func splice(a, b netlink.Link) error {
	for _, dir := range [][2]netlink.Link{{a, b}, {b, a}} {
		from, to := dir[0], dir[1]
		if err := addIngressQdisc(from); err != nil {
			return err
		}
		if err := addRedirectFilter(from, to); err != nil {
			return err
		}
	}
	return nil
}

// spliceGuest is splice for the one splice that has a guest on the end of
// it: ext -> tap is the same unconditional wire, but tap -> ext only
// carries frames that speak as the identity the guest was given, and
// drops everything else before it reaches the container's network.
//
// That network is a shared segment -- under the docker backend, docker's
// own "bridge", with every other VM container on the host and the
// gateway the git and model proxies answer on -- and the guest is not
// trusted. Unfiltered, a guest could ARP for the gateway's address (or
// another sandbox's) with its own MAC, poison the neighbour caches of
// everything else on the segment, and read or rewrite traffic that was
// never addressed to it; or forge a source MAC and steal another port's
// entry in the bridge's forwarding database. Neither the bridge nor
// docker stops either one, so it is stopped here, where every frame the
// guest sends has to pass and where nothing inside the guest can reach:
// these filters live in the VM container's namespace, on the far side of
// the hypervisor.
//
// What passes, and only that:
//
//   - IPv4 from the guest's MAC with the guest's address as source;
//   - ARP from the guest's MAC whose sender hardware address is that MAC
//     and whose sender protocol address is the guest's address -- or
//     0.0.0.0, the RFC 5227 probe a guest's own duplicate-address check
//     sends, which claims nothing and so poisons nothing;
//   - when the identity has an IPv6 half, and only then, IPv6 from the
//     guest's MAC and one of its own IPv6 addresses, with neighbour
//     discovery held to those addresses and router messages refused (see
//     ipv6GuardRules).
//
// VLAN-tagged frames and every other ethertype are dropped, as is all of
// IPv6 when the identity has no IPv6 half: the guest then has no IPv6
// address of its own to send from, and an open IPv6 path would hand it
// neighbour-advertisement spoofing -- the same attack as ARP's -- and
// rogue router advertisements on any segment that carries IPv6. Likewise
// a v6-only identity gets no IPv4 or ARP rules, so all of that is
// dropped.
func spliceGuest(ext, tap netlink.Link, id Identity) error {
	mac := id.MAC
	ip4 := id.IP.To4()
	var ip6 net.IP
	if id.IP6.To4() == nil {
		ip6 = id.IP6.To16()
	}
	if len(mac) != 6 || (ip4 == nil && ip6 == nil) {
		return fmt.Errorf("guarding %s: need a 6-byte MAC and an IPv4 or IPv6 address, have %s, %s and %s",
			tap.Attrs().Name, mac, id.IP, id.IP6)
	}

	if err := addIngressQdisc(ext); err != nil {
		return err
	}
	if err := addRedirectFilter(ext, tap); err != nil {
		return err
	}
	if err := addIngressQdisc(tap); err != nil {
		return err
	}
	var rules []guardRule
	if ip4 != nil {
		rules = append(rules, ipv4GuardRules(mac, ip4)...)
	}
	if ip6 != nil {
		var ll6 net.IP
		if id.LinkLocal6.To4() == nil {
			ll6 = id.LinkLocal6.To16()
		}
		rules = append(rules, ipv6GuardRules(mac, ip6, ll6)...)
	}
	return addGuardFilters(tap, ext, rules)
}

// guardRule is one of addGuardFilters' filters: frames of protocol that
// match every one of keys are redirected to the external interface, or
// dropped there and then if drop is set.
type guardRule struct {
	what     string
	protocol uint16
	keys     []netlink.TcU32Key
	drop     bool
}

func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func be16(b []byte) uint32 { return uint32(b[0])<<8 | uint32(b[1]) }

// keys concatenates groups of u32 keys into a slice of its own, so that
// no two rules ever share (and append into) the same backing array.
func keys(groups ...[]netlink.TcU32Key) []netlink.TcU32Key {
	var out []netlink.TcU32Key
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

// srcMACKeys matches an Ethernet source MAC: bytes 0-3 at -8, bytes 4-5
// in the top half of the word at -4 (whose bottom half is the ethertype).
// See addGuardFilters for why the offsets are negative.
func srcMACKeys(mac net.HardwareAddr) []netlink.TcU32Key {
	return []netlink.TcU32Key{
		{Off: -8, Val: be32(mac[0:4]), Mask: 0xffffffff},
		{Off: -4, Val: be16(mac[4:6]) << 16, Mask: 0xffff0000},
	}
}

// ipv4GuardRules is what an identity's IPv4 half lets through: IPv4
// from its address, ARP from its address, and ARP probes.
func ipv4GuardRules(mac net.HardwareAddr, ip net.IP) []guardRule {
	// ARP for IPv4 over Ethernet (htype 1, ptype 0x0800, hlen 6,
	// plen 4), whose sender hardware address is mac and whose sender
	// protocol address is spa: sha fills bytes 8-13 and spa 14-17, so
	// the word at 12 holds the end of one and the start of the other.
	arpFrom := func(spa net.IP) []netlink.TcU32Key {
		return keys(srcMACKeys(mac), []netlink.TcU32Key{
			{Off: 0, Val: 0x00010800, Mask: 0xffffffff},
			{Off: 4, Val: 0x06040000, Mask: 0xffff0000},
			{Off: 8, Val: be32(mac[0:4]), Mask: 0xffffffff},
			{Off: 12, Val: be16(mac[4:6])<<16 | be16(spa[0:2]), Mask: 0xffffffff},
			{Off: 16, Val: be16(spa[2:4]) << 16, Mask: 0xffff0000},
		})
	}
	return []guardRule{
		{"IPv4 from the guest's address", unix.ETH_P_IP, keys(srcMACKeys(mac),
			[]netlink.TcU32Key{{Off: 12, Val: be32(ip), Mask: 0xffffffff}}), false},
		{"ARP from the guest's address", unix.ETH_P_ARP, arpFrom(ip), false},
		{"ARP probes", unix.ETH_P_ARP, arpFrom(net.IPv4zero.To4()), false},
	}
}

// IPv6 next-header values and ICMPv6 types ipv6GuardRules names.
const (
	ipv6HopByHop    = 0
	ipv6Routing     = 43
	ipv6DestOptions = 60
	ipv6ICMP        = 58

	icmp6MLDv1Report  = 131
	icmp6NeighSolicit = 135
	icmp6NeighAdvert  = 136
	icmp6MLDv2Report  = 143

	ndOptSourceLLAddr = 1
	ndOptTargetLLAddr = 2

	// naLenNoOptions is the payload length of an NA with no options:
	// the ICMPv6 header, the flags and the target.
	naLenNoOptions = 24
)

// icmp6Refused is every NDP and router ICMPv6 type the guest may not
// send beyond what the allow rules above it take: router solicitation,
// router advertisement, neighbour solicitation and advertisement,
// redirect, router renumbering, SEND's certification path messages and
// multicast router discovery. RS goes with the rest because the guest
// takes no router advertisements and has nothing to solicit.
var icmp6Refused = []uint8{133, 134, 135, 136, 137, 138, 148, 149, 151, 152, 153}

// ipv6GuardRules is what an identity's IPv6 half lets through, as rules
// to install in order: the neighbour discovery and MLD the guest needs,
// then drops for every other NDP or router message and for the
// extension headers that could hide one from a fixed offset, and then
// anything at all from the guest's own addresses -- its global address
// ip and its link-local address ll, which may be nil.
//
// Offsets are from the IPv6 header: payload length in the top half of
// the word at 4 and next header in its third byte, the source address
// at 8-23, the ICMPv6 type at 40 when no extension header comes first,
// an NS or NA's target at 48-63, and its first option at 64.
//
// The link-layer option checks look at the first option only. Linux
// always puts that option first and alone, and a receiver uses the
// first of a duplicated option; besides, the source-MAC match already
// keeps a mismatched option from steering traffic anywhere but to the
// guest. What the option check stops is the guest pointing a
// neighbour's entry for its own address at another sandbox's MAC.
//
// Fragments (next header 44) are let through with the rest: large UDP
// needs them, and receivers drop fragmented NDP themselves (RFC 6980).
// Hop-by-hop, routing and destination-options headers are not, past the
// one MLD shape, since nothing makes a receiver drop NDP behind them.
func ipv6GuardRules(mac net.HardwareAddr, ip, ll net.IP) []guardRule {
	ours := []net.IP{ip}
	if ll != nil {
		ours = append(ours, ll)
	}

	nextHeader := func(nh uint8) []netlink.TcU32Key {
		return []netlink.TcU32Key{{Off: 4, Val: uint32(nh) << 8, Mask: 0x0000ff00}}
	}
	addrAt := func(off int32, a net.IP) []netlink.TcU32Key {
		var k []netlink.TcU32Key
		for i := int32(0); i < 16; i += 4 {
			k = append(k, netlink.TcU32Key{Off: off + i, Val: be32(a[i : i+4]), Mask: 0xffffffff})
		}
		return k
	}
	src := func(a net.IP) []netlink.TcU32Key { return addrAt(8, a) }
	icmpType := func(off int32, t uint8) []netlink.TcU32Key {
		return []netlink.TcU32Key{{Off: off, Val: uint32(t) << 24, Mask: 0xff000000}}
	}
	// A source or target link-layer address option, eight bytes long
	// (length 1), carrying mac.
	llOption := func(optType uint8) []netlink.TcU32Key {
		return []netlink.TcU32Key{
			{Off: 64, Val: uint32(optType)<<24 | 1<<16 | be16(mac[0:2]), Mask: 0xffffffff},
			{Off: 68, Val: be32(mac[2:6]), Mask: 0xffffffff},
		}
	}
	allow := func(what string, k ...[]netlink.TcU32Key) guardRule {
		return guardRule{what, unix.ETH_P_IPV6, keys(append([][]netlink.TcU32Key{srcMACKeys(mac)}, k...)...), false}
	}
	drop := func(what string, k ...[]netlink.TcU32Key) guardRule {
		r := allow(what, k...)
		r.drop = true
		return r
	}

	var rules []guardRule
	for _, a := range ours {
		rules = append(rules,
			allow("NA for "+a.String(), nextHeader(ipv6ICMP), src(a),
				icmpType(40, icmp6NeighAdvert), addrAt(48, a), llOption(ndOptTargetLLAddr)),
			// Linux answers a unicast NS (a reachability probe) with an
			// NA that carries no options at all. With no link-layer
			// address in it, it can change no one's neighbour entry, and
			// its target is held to the guest's own address regardless.
			allow("option-less NA for "+a.String(),
				[]netlink.TcU32Key{{Off: 4, Val: naLenNoOptions<<16 | ipv6ICMP<<8, Mask: 0xffffff00}},
				src(a), icmpType(40, icmp6NeighAdvert), addrAt(48, a)),
			allow("NS from "+a.String(), nextHeader(ipv6ICMP), src(a),
				icmpType(40, icmp6NeighSolicit), llOption(ndOptSourceLLAddr)),
		)
	}
	// A duplicate-address probe, which claims nothing, like the ARP probe
	// from 0.0.0.0.
	rules = append(rules, allow("DAD probes", nextHeader(ipv6ICMP), src(net.IPv6unspecified),
		icmpType(40, icmp6NeighSolicit)))

	// MLD reports, which travel behind an 8-byte hop-by-hop header
	// (next header 58, length 0) carrying the router alert option.
	mldFrom := []net.IP{net.IPv6unspecified}
	if ll != nil {
		mldFrom = append([]net.IP{ll}, mldFrom...)
	}
	for _, a := range mldFrom {
		for _, t := range []uint8{icmp6MLDv2Report, icmp6MLDv1Report} {
			rules = append(rules, allow(fmt.Sprintf("MLD reports (type %d) from %s", t, a),
				nextHeader(ipv6HopByHop), src(a),
				[]netlink.TcU32Key{{Off: 40, Val: ipv6ICMP << 24, Mask: 0xffff0000}},
				icmpType(48, t)))
		}
	}

	for _, t := range icmp6Refused {
		rules = append(rules, drop(fmt.Sprintf("ICMPv6 type %d", t), nextHeader(ipv6ICMP), icmpType(40, t)))
	}
	for _, nh := range []uint8{ipv6HopByHop, ipv6Routing, ipv6DestOptions} {
		rules = append(rules, drop(fmt.Sprintf("IPv6 next header %d", nh), nextHeader(nh)))
	}

	for _, a := range ours {
		rules = append(rules, allow("IPv6 from "+a.String(), src(a)))
	}
	return rules
}

// addGuardFilters installs spliceGuest's tap -> ext direction: one filter
// per rule, in order, each redirecting what it matches to ext or dropping
// it, then a final match-everything filter that drops whatever none of
// them took. Lower priorities run first, and the first filter that
// matches decides the frame, so the drop only ever sees what nothing
// above it accepted.
//
// Offsets are relative to the network header, which is where a frame on
// a device's ingress starts once the kernel has parsed its Ethernet
// header off; the header itself is still there, ahead of it, which is
// why the source MAC is read at -8 (the six bytes after the destination,
// ending two bytes short of the payload, behind the ethertype). Every key
// is a 32-bit word at a 4-byte-aligned offset, as u32 requires, masked
// down where the field it checks does not fill the word.
func addGuardFilters(tap, ext netlink.Link, rules []guardRule) error {
	for i, r := range rules {
		action := netlink.Action(netlink.NewMirredAction(ext.Attrs().Index))
		verb := "allowing"
		if r.drop {
			action = &netlink.GenericAction{ActionAttrs: netlink.ActionAttrs{Action: netlink.TC_ACT_SHOT}}
			verb = "dropping"
		}
		filter := &netlink.U32{
			FilterAttrs: netlink.FilterAttrs{
				LinkIndex: tap.Attrs().Index,
				Parent:    netlink.MakeHandle(0xffff, 0),
				Priority:  uint16(i + 1),
				Protocol:  r.protocol,
			},
			Sel: &netlink.TcU32Sel{
				Flags: netlink.TC_U32_TERMINAL,
				Keys:  r.keys,
			},
			Actions: []netlink.Action{action},
		}
		if err := netlink.FilterAdd(filter); err != nil {
			return fmt.Errorf("%s %s from %s to %s: %w",
				verb, r.what, tap.Attrs().Name, ext.Attrs().Name, err)
		}
	}

	drop := &netlink.U32{
		FilterAttrs: netlink.FilterAttrs{
			LinkIndex: tap.Attrs().Index,
			Parent:    netlink.MakeHandle(0xffff, 0),
			Priority:  uint16(len(rules) + 1),
			Protocol:  unix.ETH_P_ALL,
		},
		Actions: []netlink.Action{&netlink.GenericAction{
			ActionAttrs: netlink.ActionAttrs{Action: netlink.TC_ACT_SHOT},
		}},
	}
	if err := netlink.FilterAdd(drop); err != nil {
		return fmt.Errorf("dropping everything else from %s: %w", tap.Attrs().Name, err)
	}
	return nil
}

// addIngressQdisc (re)creates link's ingress qdisc, first removing any
// existing one so the caller starts from a known-empty filter list.
// guardControlLink holds what the guest sends on its control NIC to the
// one thing the control link is for: IPv4 addressed to this namespace's
// own end of it, addr (the memory agent's listener), and the ARP that
// takes to resolve. Everything else is dropped at the control tap's
// ingress, before the bridge or this namespace's stack ever sees it.
//
// Without it the control link was a route out. The guest names addr as a
// gateway and sends to any destination; whether this namespace then
// forwards it is net.ipv4.ip_forward's call, and that is not netshim's
// to make -- a new network namespace copies it from the node's, which a
// Kubernetes node (or a docker host) has set to 1 -- so the guest could
// reach whatever this namespace routes to from a source address nobody
// allocated it, past the identity spliceGuest holds it to on its real NIC.
// IPv6 has the same shape with the bridge's link-local address and is
// dropped with the rest: the control link is IPv4 only.
//
// A filter rather than a sysctl for the same reason as spliceGuest's:
// /proc/sys is read-only in an unprivileged container, and tc needs only
// CAP_NET_ADMIN.
func guardControlLink(tapName string, addr net.IP) error {
	ip := addr.To4()
	if ip == nil {
		return fmt.Errorf("guarding %s: the control address %s is not IPv4", tapName, addr)
	}
	tap, err := netlink.LinkByName(tapName)
	if err != nil {
		return fmt.Errorf("looking up control tap %s: %w", tapName, err)
	}
	if err := addIngressQdisc(tap); err != nil {
		return err
	}

	pass := func() []netlink.Action {
		return []netlink.Action{&netlink.GenericAction{
			ActionAttrs: netlink.ActionAttrs{Action: netlink.TC_ACT_OK},
		}}
	}
	allowed := []struct {
		what     string
		protocol uint16
		keys     []netlink.TcU32Key
	}{
		{"IPv4 to " + ip.String(), unix.ETH_P_IP, []netlink.TcU32Key{
			{Off: 16, Val: uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3]), Mask: 0xffffffff},
		}},
		{"ARP", unix.ETH_P_ARP, []netlink.TcU32Key{{Off: 0, Val: 0, Mask: 0}}},
	}
	for i, a := range allowed {
		filter := &netlink.U32{
			FilterAttrs: netlink.FilterAttrs{
				LinkIndex: tap.Attrs().Index,
				Parent:    netlink.MakeHandle(0xffff, 0),
				Priority:  uint16(i + 1),
				Protocol:  a.protocol,
			},
			Sel: &netlink.TcU32Sel{
				Flags: netlink.TC_U32_TERMINAL,
				Keys:  a.keys,
			},
			Actions: pass(),
		}
		if err := netlink.FilterAdd(filter); err != nil {
			return fmt.Errorf("allowing %s from %s: %w", a.what, tapName, err)
		}
	}

	drop := &netlink.U32{
		FilterAttrs: netlink.FilterAttrs{
			LinkIndex: tap.Attrs().Index,
			Parent:    netlink.MakeHandle(0xffff, 0),
			Priority:  uint16(len(allowed) + 1),
			Protocol:  unix.ETH_P_ALL,
		},
		Actions: []netlink.Action{&netlink.GenericAction{
			ActionAttrs: netlink.ActionAttrs{Action: netlink.TC_ACT_SHOT},
		}},
	}
	if err := netlink.FilterAdd(drop); err != nil {
		return fmt.Errorf("dropping everything else from %s: %w", tapName, err)
	}
	return nil
}

func addIngressQdisc(link netlink.Link) error {
	name := link.Attrs().Name
	qdisc := &netlink.Ingress{
		QdiscAttrs: netlink.QdiscAttrs{
			LinkIndex: link.Attrs().Index,
			Handle:    netlink.MakeHandle(0xffff, 0),
			Parent:    netlink.HANDLE_INGRESS,
		},
	}

	if err := netlink.QdiscDel(qdisc); err != nil && !isNoSuchQdisc(err) {
		return fmt.Errorf("removing existing ingress qdisc on %s: %w", name, err)
	}

	var err error
	for i := 0; i < qdiscAddAttempts; i++ {
		if err = netlink.QdiscAdd(qdisc); err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EBUSY) {
			break
		}
	}
	return fmt.Errorf("adding ingress qdisc on %s: %w", name, err)
}

// addRedirectFilter installs the filter that does the actual splicing:
// every frame (ETH_P_ALL, matched unconditionally) arriving on from is
// stolen and transmitted out to.
//
// The classifier is u32 with no selectors, which matches everything. The
// "matchall" classifier expresses that more directly, but it needs
// CONFIG_NET_CLS_MATCHALL, which plenty of minimal kernels leave out --
// and the failure is a bare ENOENT from the filter add, with nothing to
// say the classifier is the missing piece. u32 is effectively always
// present, and is what kata-containers uses for the same job.
//
// The qdisc's parent is HANDLE_INGRESS, but a filter attached to that
// qdisc names it by handle -- ffff:0 -- instead. Passing HANDLE_INGRESS
// here would silently fail to attach.
func addRedirectFilter(from, to netlink.Link) error {
	filter := &netlink.U32{
		FilterAttrs: netlink.FilterAttrs{
			LinkIndex: from.Attrs().Index,
			Parent:    netlink.MakeHandle(0xffff, 0),
			Priority:  1,
			Protocol:  unix.ETH_P_ALL,
		},
		Actions: []netlink.Action{netlink.NewMirredAction(to.Attrs().Index)},
	}
	if err := netlink.FilterAdd(filter); err != nil {
		return fmt.Errorf("redirecting %s to %s: %w",
			from.Attrs().Name, to.Attrs().Name, err)
	}
	return nil
}

// isNoSuchQdisc reports whether err is the kernel saying there was no
// ingress qdisc to delete, which for an idempotent setup is success.
// Kernels differ on which errno they use for it, so both are accepted; a
// delete that failed for some other reason still surfaces loudly, as the
// add that follows it then fails with EEXIST.
func isNoSuchQdisc(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.EINVAL)
}
