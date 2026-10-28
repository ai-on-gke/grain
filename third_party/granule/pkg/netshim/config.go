// Package netshim splices a single cloud-hypervisor VM directly onto the
// network segment its container runtime already put the sandbox on, so
// the guest takes over the address and MAC that runtime assigned. It is
// run before the VM boots, normally inside the same container. It can also
// run in a separate init container for deployments that separate network
// setup privileges from the VM. Its state lives with the network namespace.
//
// One VM per network namespace is the whole model: a pod runs a single VM
// container, and that VM is the namespace's one endpoint, so from outside
// it behaves like an ordinary container -- Services, "-p", "--network"
// membership -- because all of those are properties of the sandbox rather
// than of anything netshim installs.
package netshim

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

const (
	envVM            = "NETSHIM_VM"
	envControlCIDR   = "NETSHIM_CONTROL_CIDR"
	envBridge        = "NETSHIM_BRIDGE"
	envExternalIface = "NETSHIM_EXTERNAL_IFACE"
	envDNS           = "NETSHIM_DNS"
	envTapOwner      = "NETSHIM_TAP_OWNER"
	envIPv6          = "NETSHIM_IPV6"
	envControllerIPs = "NETSHIM_CONTROLLER_IPS"
	envAccessPorts   = "NETSHIM_ACCESS_PORTS"

	defaultControlCIDR   = "169.254.100.1/24"
	defaultBridge        = "granule0"
	defaultExternalIface = AutoExternalIface

	// DefaultDNS is the guest's nameserver setting when nothing names one:
	// AutoDNS, the sandbox's own resolver whenever the guest can reach
	// it. In a Kubernetes pod that is the cluster DNS ClusterIP, with the
	// pod's search domains and ndots, so Service names resolve in the
	// guest as they would have in the pod; on a restricted, air-gapped or
	// split-horizon network it is whatever resolver that network handed
	// the sandbox. Where the sandbox's resolver is one a guest on a tap
	// cannot reach -- docker's embedded 127.0.0.11, on a user-defined
	// network, or a link-local cloud metadata resolver such as GCE's
	// 169.254.169.254 that the host copied in -- it is FallbackDNS, the
	// public resolver every guest was pointed at before. See ResolveDNS.
	//
	// It is a default rather than a fact: a deployment names its own
	// resolver instead (NETSHIM_DNS, granule's -dns), and one that wants
	// the guest image's own /etc/resolv.conf left alone passes the empty
	// string. A VM spec saved before AutoDNS existed carries the 8.8.8.8
	// it was created with, and keeps resolving through it.
	DefaultDNS = AutoDNS

	// maxDNS is how many nameservers the guest can be told about:
	// MAXNS, the most glibc and musl read out of a resolv.conf, so a
	// fourth would be written down and never asked.
	maxDNS = 3

	// maxDNS4 is how many of them can be IPv4: the ip= boot parameter,
	// which carries the IPv4 ones to a guest image that predates
	// granule.dns=, has exactly two fields for them (dns0 and dns1).
	maxDNS4 = 2

	// tapPrefix is prepended to the VM's name to derive its tap device
	// name. Linux interface names are capped at 15 bytes, which bounds
	// how long a VM name may be.
	tapPrefix = "tap-"

	// controlTapPrefix names the control link's tap. It is the same
	// length as tapPrefix so both derived names fit the same VM name
	// budget.
	controlTapPrefix = "ctl-"

	maxIfaceName = 15
)

// Config holds everything netshim needs to wire up one namespace's single
// VM.
type Config struct {
	// VM is the name of the guest this namespace runs. It must match the
	// name the VM container was given, since both derive the same tap
	// device name from it.
	VM string

	// ControlAddr and ControlNet are the address netshim holds on the
	// control link, and its subnet. Both nil when the control link is
	// disabled (NETSHIM_CONTROL_CIDR set to the empty string), which
	// leaves the guest reachable only from the container network.
	ControlAddr net.IP
	ControlNet  *net.IPNet

	// Bridge is the name of the Linux bridge holding the control link's
	// host end. Nothing on the guest's own data path goes through it:
	// that is the splice.
	Bridge string

	// ExternalIface is the sandbox's primary interface, the one carrying
	// the address the guest takes over. AutoExternalIface (or empty), the
	// default, means the interface carrying the namespace's default route;
	// ResolveExternalIface turns it into a name.
	ExternalIface string

	// DNS is the nameservers the guest is told to resolve through, at
	// most maxDNS of them in either family, in order, parsed from
	// NETSHIM_DNS. They reach the guest on its granule.dns= boot
	// parameter, and the IPv4 ones on ip= as well (GuestConfig), where
	// granule-configure-dns writes them into /etc/resolv.conf. Empty
	// leaves the guest resolving through whatever its image ships with.
	DNS []net.IP

	// DNSAuto is set when NETSHIM_DNS asked for AutoDNS (or was unset),
	// and DNS is still to be filled in from the namespace's own
	// resolv.conf by ResolveDNS.
	DNSAuto bool

	// DNSSearch and DNSOptions are the search list and resolver options
	// (only ever "ndots:n") the guest's resolv.conf is given beside DNS,
	// on the granule.dns_search= and granule.dns_options= boot
	// parameters. Only ResolveDNS sets them, from the namespace's own
	// resolv.conf.
	DNSSearch  []string
	DNSOptions []string

	// TapUID and TapGID own the taps netshim creates, parsed from
	// NETSHIM_TAP_OWNER ("UID:GID"; both 0, root, when unset). The
	// kernel lets a process attach to an owned tap without CAP_NET_ADMIN
	// only if its effective UID and GID match these. So a VM container
	// that runs as non-root with no capabilities, beside a separate
	// netshim, needs them set to the UID and GID it runs as. A tap
	// always has an owner here: the netlink library sets TUNSETOWNER
	// and TUNSETGROUP on every tap it creates.
	TapUID, TapGID uint32

	// IPv6 is NETSHIM_IPV6: IPv6Auto (or empty), the default, takes over
	// the namespace's IPv6 identity along with its IPv4 one, and IPv6Off
	// discards the IPv6 half once it has been read, which is the
	// IPv4-only behaviour every netshim had before IPv6 was discovered at
	// all.
	IPv6 string

	// ControllerIPs are the IPv4 or IPv6 addresses of the sandbox access
	// controller that the pod-side wrapper dials out to, parsed from
	// NETSHIM_CONTROLLER_IPS. When set, Setup drops any frame from the
	// guest tap addressed to these IPs and passes frames from these IPs
	// arriving on the external interface to the namespace's own network
	// stack rather than redirecting them to the guest tap.
	ControllerIPs []net.IP

	// AccessPorts are the TCP ports the sandbox access server listens on
	// in this namespace, parsed from NETSHIM_ACCESS_PORTS. TCP segments
	// arriving on the external interface for these ports are passed to
	// the namespace's own stack, from any source, instead of to the
	// guest; the access server authenticates every connection itself
	// (TLS 1.3 and the per-sandbox token).
	AccessPorts []uint16
}

// The NETSHIM_IPV6 values (and VMSpec.IPv6, and granule's -ipv6).
const (
	IPv6Auto = "auto"
	IPv6Off  = "off"
)

// ValidateIPv6 checks a NETSHIM_IPV6 or VMSpec.IPv6 value: IPv6Auto,
// IPv6Off, or empty for the default.
func ValidateIPv6(v string) error {
	switch strings.TrimSpace(v) {
	case "", IPv6Auto, IPv6Off:
		return nil
	}
	return fmt.Errorf("want %q or %q", IPv6Auto, IPv6Off)
}

// IPv6Enabled reports whether the namespace's IPv6 identity is taken
// over at all, i.e. whether NETSHIM_IPV6 is anything but off.
func (c Config) IPv6Enabled() bool {
	return strings.TrimSpace(c.IPv6) != IPv6Off
}

// identity is DiscoverIdentity on the (already resolved) external
// interface, with NETSHIM_IPV6 applied, refused if that leaves the guest
// nothing to take over.
func (c Config) identity() (Identity, error) {
	id, err := DiscoverIdentity(c.ExternalIface)
	if err != nil {
		return Identity{}, err
	}
	if !c.IPv6Enabled() {
		id = id.withoutIPv6()
		if !id.HasIPv4() {
			return Identity{}, fmt.Errorf("no IPv4 address found on %s, and %s=%s discards its IPv6 one", c.ExternalIface, envIPv6, IPv6Off)
		}
	}
	return id, nil
}

// TapName is the name of the tap device netshim splices onto the external
// interface, and which the guest's first NIC is backed by.
func (c Config) TapName() string {
	return tapPrefix + c.VM
}

// ControlTapName is the name of the tap netshim creates for the control
// link -- the private path between this namespace and the guest that
// survives the guest taking over the namespace's own address.
func (c Config) ControlTapName() string {
	return controlTapPrefix + c.VM
}

// FromEnv builds a Config from the process environment and validates it.
// It needs neither an address nor a port for the VM: docker (or the CNI)
// already chose the address, and ports are published on the sandbox the
// ordinary way rather than forwarded by rules of netshim's own.
func FromEnv() (Config, error) {
	cfg := Config{
		Bridge:        getEnvDefault(envBridge, defaultBridge),
		ExternalIface: getEnvDefault(envExternalIface, defaultExternalIface),
	}

	dns, auto, err := dnsFromEnv()
	if err != nil {
		return Config{}, err
	}
	cfg.DNS, cfg.DNSAuto = dns, auto

	if cfg.TapUID, cfg.TapGID, err = tapOwnerFromEnv(); err != nil {
		return Config{}, err
	}

	cfg.IPv6 = strings.TrimSpace(getEnvDefault(envIPv6, IPv6Auto))
	if err := ValidateIPv6(cfg.IPv6); err != nil {
		return Config{}, fmt.Errorf("%s=%q: %w", envIPv6, cfg.IPv6, err)
	}

	if rawControllers := strings.TrimSpace(os.Getenv(envControllerIPs)); rawControllers != "" {
		cips, err := ParseControllerIPs(rawControllers)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", envControllerIPs, err)
		}
		cfg.ControllerIPs = cips
	}
	if rawPorts := strings.TrimSpace(os.Getenv(envAccessPorts)); rawPorts != "" {
		ports, err := ParseAccessPorts(rawPorts)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", envAccessPorts, err)
		}
		cfg.AccessPorts = ports
	}

	cfg.VM = strings.TrimSpace(os.Getenv(envVM))
	if cfg.VM == "" {
		cfg.VM = "vm"
	}
	if len(cfg.TapName()) > maxIfaceName {
		return Config{}, fmt.Errorf("%s: VM %q name too long: tap device name %q exceeds %d characters",
			envVM, cfg.VM, cfg.TapName(), maxIfaceName)
	}

	// An explicitly empty NETSHIM_CONTROL_CIDR disables the control link
	// altogether, for a guest that only ever needs to be reached from
	// the container network. The memory agent needs the control link;
	// "granule exec" uses vsock and is unaffected.
	raw, ok := os.LookupEnv(envControlCIDR)
	if ok && raw == "" {
		return cfg, nil
	}
	addr, ipnet, err := net.ParseCIDR(getEnvDefault(envControlCIDR, defaultControlCIDR))
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", envControlCIDR, err)
	}
	if addr.To4() == nil {
		return Config{}, fmt.Errorf("%s: %s is not an IPv4 address", envControlCIDR, addr)
	}
	if len(cfg.ControlTapName()) > maxIfaceName {
		return Config{}, fmt.Errorf("%s: VM %q name too long: control tap device name %q exceeds %d characters",
			envVM, cfg.VM, cfg.ControlTapName(), maxIfaceName)
	}
	cfg.ControlAddr = addr.To4()
	cfg.ControlNet = ipnet

	return cfg, nil
}

// Enabled reports the legacy NETSHIM_VM selector. With CHV_NET_MODE unset,
// this identifies existing split-container deployments, which must not
// suddenly start requiring network setup privileges in the VM container.
func Enabled() bool {
	return strings.TrimSpace(os.Getenv(envVM)) != ""
}

func getEnvDefault(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// tapOwnerFromEnv reads NETSHIM_TAP_OWNER, "UID:GID" in numbers. Both
// halves are required: a tap always gets a group as well as an owner, and
// a group left at 0 would shut out a non-root VM container as surely as
// the wrong owner would.
func tapOwnerFromEnv() (uid, gid uint32, err error) {
	raw := strings.TrimSpace(os.Getenv(envTapOwner))
	if raw == "" {
		return 0, 0, nil
	}
	u, g, ok := strings.Cut(raw, ":")
	if !ok {
		return 0, 0, fmt.Errorf("%s: %q is not UID:GID", envTapOwner, raw)
	}
	uid64, err := strconv.ParseUint(u, 10, 32)
	if err != nil {
		return 0, 0, fmt.Errorf("%s: UID %q: %w", envTapOwner, u, err)
	}
	gid64, err := strconv.ParseUint(g, 10, 32)
	if err != nil {
		return 0, 0, fmt.Errorf("%s: GID %q: %w", envTapOwner, g, err)
	}
	return uint32(uid64), uint32(gid64), nil
}

// dnsFromEnv reads NETSHIM_DNS. Unset means DefaultDNS, which is AutoDNS
// and reported as auto with no nameservers yet (ResolveDNS fills them
// in); set to the empty string means no nameservers at all, the same
// "explicitly empty disables it" shape NETSHIM_CONTROL_CIDR already has
// -- which is how a guest whose image ships a resolver of its own keeps
// it.
func dnsFromEnv() (dns []net.IP, auto bool, err error) {
	raw, ok := os.LookupEnv(envDNS)
	if !ok {
		raw = DefaultDNS
	}
	if IsAutoDNS(raw) {
		return nil, true, nil
	}
	dns, err = ParseDNS(raw)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", envDNS, err)
	}
	return dns, false, nil
}

// ParseDNS parses a comma-separated list of nameserver addresses, as
// NETSHIM_DNS and granule's own -dns both spell them. The empty string
// (or a list of nothing but separators and spaces) parses to no
// nameservers, which is how both spell "leave the guest's resolver
// alone".
//
// Either family, at most maxDNS (3) in all and at most maxDNS4 (2) of
// them IPv4: the list travels to the guest whole on granule.dns=, and
// its IPv4 part on the ip= boot parameter's dns0/dns1 fields as well,
// which are neither more nor other than two. Rejected here rather than
// silently truncated -- an operator who listed one resolver too many is
// owed the news that the guest would never ask it.
func ParseDNS(spec string) ([]net.IP, error) {
	var dns []net.IP
	v4 := 0
	for _, field := range strings.Split(spec, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		ip := net.ParseIP(field)
		if ip == nil {
			return nil, fmt.Errorf("invalid nameserver address %q", field)
		}
		if ip4 := ip.To4(); ip4 != nil {
			ip = ip4
			v4++
		}
		dns = append(dns, ip)
	}
	if len(dns) > maxDNS {
		return nil, fmt.Errorf("at most %d nameservers fit a guest's resolv.conf, got %d", maxDNS, len(dns))
	}
	if v4 > maxDNS4 {
		return nil, fmt.Errorf("at most %d IPv4 nameservers fit the guest's ip= boot parameter, got %d", maxDNS4, v4)
	}
	return dns, nil
}

// ParseAccessPorts parses a comma-separated list of TCP ports for
// NETSHIM_ACCESS_PORTS.
func ParseAccessPorts(spec string) ([]uint16, error) {
	var ports []uint16
	for _, f := range strings.Split(spec, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		n, err := strconv.ParseUint(f, 10, 16)
		if err != nil || n == 0 {
			return nil, fmt.Errorf("invalid TCP port %q", f)
		}
		ports = append(ports, uint16(n))
	}
	return ports, nil
}

// ParseControllerIPs parses a comma-separated list of IPv4 or IPv6 addresses
// for NETSHIM_CONTROLLER_IPS.
func ParseControllerIPs(spec string) ([]net.IP, error) {
	var ips []net.IP
	seen := make(map[string]bool)
	for _, field := range strings.Split(spec, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		ip := net.ParseIP(field)
		if ip == nil {
			return nil, fmt.Errorf("invalid controller IP address %q", field)
		}
		if ip4 := ip.To4(); ip4 != nil {
			ip = ip4
		} else {
			ip = ip.To16()
		}
		key := string(ip)
		if seen[key] {
			continue
		}
		seen[key] = true
		ips = append(ips, ip)
	}
	return ips, nil
}
