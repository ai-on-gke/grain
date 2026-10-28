package netshim

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"syscall"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
)

// Setup wires up the guest's networking: it is spliced directly onto the
// container's own network segment and inherits its identity, so from
// outside the namespace there is still exactly one endpoint, with the
// address and MAC the runtime allocated.
//
// Nothing here rewrites or routes a packet. Once the splice is programmed
// the kernel moves frames between the external interface and the tap on
// its own; the one thing it checks is that what the guest sends speaks
// as the identity it was given, so a guest cannot ARP-spoof the rest of
// a shared segment (see spliceGuest). Setup runs inline before the VM boots, or in an
// optional init container, leaving state that lives with the namespace.
//
// Two things it deliberately does not do: it never writes
// net.ipv4.ip_forward (there is no routing), and it installs no nftables
// rules (there is no NAT). netshim therefore needs only CAP_NET_ADMIN and
// access to /dev/net/tun, not a fully privileged container.
//
// The external interface keeps its address: the splice steals the
// interface's ingress, so the namespace's own stack can never receive a
// reply and cannot hold a connection over it, and leaving the address in
// place keeps the namespace looking normal to anything that inspects it
// -- including the VM container, which reads its own guest configuration
// back off that interface (DiscoverIdentity).
//
// It is idempotent, so a retried init container converges on the same end
// state rather than erroring on things that already exist.
func Setup(cfg Config) error {
	cfg, err := cfg.ResolveExternalIface()
	if err != nil {
		return err
	}
	id, err := cfg.identity()
	if err != nil {
		return err
	}
	log.Printf("%s identity: %s mac %s mtu %d", id.Iface, id.Addresses(), id.MAC, id.MTU)

	// The tap has to match the external interface's MTU exactly. A
	// splice is a wire: there is no bridge or router in between to
	// fragment an oversized frame or to answer with an ICMP
	// "fragmentation needed", so a mismatch here silently blackholes
	// large packets on any segment that isn't 1500 (a VXLAN overlay, or
	// most CNIs).
	tap, err := ensureTapMTU(cfg.TapName(), id.MTU, cfg.TapUID, cfg.TapGID)
	if err != nil {
		return err
	}

	ext, err := netlink.LinkByName(cfg.ExternalIface)
	if err != nil {
		return fmt.Errorf("looking up %s: %w", cfg.ExternalIface, err)
	}

	if err := spliceGuest(ext, tap, id); err != nil {
		return err
	}
	var held []string
	if id.HasIPv4() {
		held = append(held, id.IP.String())
	}
	if id.HasIPv6() {
		held = append(held, id.IP6.String())
		if id.LinkLocal6 != nil {
			held = append(held, id.LinkLocal6.String())
		}
	}
	log.Printf("spliced %s <-> %s, guest held to %s mac %s", cfg.ExternalIface, cfg.TapName(), strings.Join(held, " "), id.MAC)

	if cfg.ControlNet == nil {
		log.Printf("no control link configured; the memory agent is unavailable (exec still uses vsock)")
		return nil
	}

	// The control link is a second, private segment purely for traffic
	// between this namespace and the guest. It cannot share the spliced
	// interface: that interface's *egress* still goes to the veth peer,
	// not to the tap, so anything the namespace sends over it reaches
	// the container network rather than the guest. And the guest now
	// holds the namespace's own address, so dialing that address from
	// in here reaches the local stack instead. A separate NIC is the
	// only path back in.
	if err := ensureBridge(cfg.Bridge, cfg.ControlAddr, cfg.ControlNet); err != nil {
		return err
	}
	if err := ensureTap(cfg.ControlTapName(), cfg.Bridge, cfg.TapUID, cfg.TapGID); err != nil {
		return err
	}
	if err := guardControlLink(cfg.ControlTapName(), cfg.ControlAddr); err != nil {
		return err
	}
	log.Printf("control link %s on %s via %s", cfg.ControlAddr, cfg.Bridge, cfg.ControlTapName())

	return nil
}

// ensureTapMTU creates the tap if it does not exist, sets its MTU and
// brings it up, returning the link. Unlike ensureTap it attaches the tap
// to no bridge: the spliced tap's only peer is the external interface.
func ensureTapMTU(name string, mtu int, uid, gid uint32) (netlink.Link, error) {
	if err := createTap(name, uid, gid); err != nil {
		return nil, err
	}

	link, err := netlink.LinkByName(name)
	if err != nil {
		return nil, fmt.Errorf("looking up tap %s: %w", name, err)
	}
	if err := netlink.LinkSetMTU(link, mtu); err != nil {
		return nil, fmt.Errorf("setting tap %s MTU to %d: %w", name, mtu, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return nil, fmt.Errorf("bringing up tap %s: %w", name, err)
	}
	return link, nil
}

func ensureBridge(name string, addr net.IP, subnet *net.IPNet) error {
	if !linkExists(name) {
		br := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: name}}
		if err := netlink.LinkAdd(br); err != nil {
			return fmt.Errorf("creating bridge %s: %w", name, err)
		}
	}

	link, err := netlink.LinkByName(name)
	if err != nil {
		return fmt.Errorf("looking up bridge %s: %w", name, err)
	}
	noLinkLocal6(link)

	ones, _ := subnet.Mask.Size()
	nlAddr, err := netlink.ParseAddr(fmt.Sprintf("%s/%d", addr, ones))
	if err != nil {
		return fmt.Errorf("parsing bridge address %s/%d: %w", addr, ones, err)
	}
	if err := netlink.AddrAdd(link, nlAddr); err != nil && !isEExist(err) {
		return fmt.Errorf("assigning %s to bridge %s: %w", nlAddr, name, err)
	}

	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("bringing up bridge %s: %w", name, err)
	}
	return nil
}

func ensureTap(name, bridge string, uid, gid uint32) error {
	if err := createTap(name, uid, gid); err != nil {
		return err
	}

	link, err := netlink.LinkByName(name)
	if err != nil {
		return fmt.Errorf("looking up tap %s: %w", name, err)
	}
	noLinkLocal6(link)
	bridgeLink, err := netlink.LinkByName(bridge)
	if err != nil {
		return fmt.Errorf("looking up bridge %s: %w", bridge, err)
	}
	if err := netlink.LinkSetMaster(link, bridgeLink); err != nil {
		return fmt.Errorf("attaching tap %s to bridge %s: %w", name, bridge, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("bringing up tap %s: %w", name, err)
	}
	return nil
}

// createTap creates a persistent tap owned by uid and gid, unless one by
// that name already exists. Ownership is set only at creation: an
// existing tap keeps the owner it was created with.
func createTap(name string, uid, gid uint32) error {
	if linkExists(name) {
		return nil
	}
	tap := &netlink.Tuntap{
		LinkAttrs: netlink.LinkAttrs{Name: name},
		Mode:      netlink.TUNTAP_MODE_TAP,
		Owner:     uid,
		Group:     gid,
	}
	if err := netlink.LinkAdd(tap); err != nil {
		return fmt.Errorf("creating tap %s: %w", name, err)
	}
	return nil
}

// noLinkLocal6 sets addrgenmode none on one of the control link's own
// interfaces, so that the kernel never gives it a link-local address and
// never sends DAD, router solicitations or MLD from it. The control link
// is IPv4 only and its guard drops what the guest sends over IPv6 anyway;
// this is only noise reduction. It is netlink rather than disable_ipv6,
// whose /proc/sys is read-only in an unprivileged container, and it has
// to come before the link is first brought up -- once it is up, the
// address is already there.
//
// Best effort, and silent on failure: a namespace with no IPv6 at all
// refuses the request, and has nothing to quieten either.
func noLinkLocal6(link netlink.Link) {
	_ = netlink.LinkSetIP6AddrGenMode(link, nl.IN6_ADDR_GEN_MODE_NONE)
}

func linkExists(name string) bool {
	_, err := net.InterfaceByName(name)
	return err == nil
}

func isEExist(err error) bool {
	return errors.Is(err, syscall.EEXIST) || errors.Is(err, os.ErrExist)
}
