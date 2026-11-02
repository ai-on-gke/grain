// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package orchestrator

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/granule"
)

var (
	// nftLock serializes nftables rule changes on the host.
	nftLock sync.Mutex

	// nftFallbackPaths lists standard fallback directories where nft may be installed
	// when /usr/sbin or /sbin are missing from an unprivileged user's $PATH.
	nftFallbackPaths = []string{
		"/usr/sbin/nft",
		"/sbin/nft",
		"/usr/local/sbin/nft",
	}
)

// findNFTBinary locates the nft binary using exec.LookPath with fallback paths.
func findNFTBinary() (string, error) {
	if p, err := exec.LookPath("nft"); err == nil {
		return p, nil
	}
	for _, p := range nftFallbackPaths {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && (fi.Mode()&0111 != 0) {
			return p, nil
		}
	}
	return "", fmt.Errorf("nft binary not found")
}

// sanitizeNFTName converts a VM name into a valid nftables chain/identifier name.
func sanitizeNFTName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// guestNetwork is one address a VM's traffic can leave its container
// from: the address itself, the segment it is on and that segment's
// gateway. A container has one per docker network per address family.
type guestNetwork struct {
	IP      net.IP
	CIDR    *net.IPNet
	Gateway net.IP
}

// guestNetworksTemplate prints one line per docker network the container
// is on: its IPv4 address, prefix and gateway, then the same for IPv6.
// Separated by '|' rather than spaces because any of the six may be
// empty -- a network with no IPv6 has three of them blank -- and
// strings.Fields would silently shift the rest left.
const guestNetworksTemplate = `{{range .NetworkSettings.Networks}}` +
	`{{.IPAddress}}|{{.IPPrefixLen}}|{{.Gateway}}|{{.GlobalIPv6Address}}|{{.GlobalIPv6PrefixLen}}|{{.IPv6Gateway}}` +
	"{{\"\\n\"}}{{end}}"

// inspectGuestNetworks asks docker for every address container holds,
// IPv4 and IPv6, across every network it is attached to. It is an error
// for there to be none: a VM whose addresses cannot be found is one no
// host rule can be written for.
func inspectGuestNetworks(ctx context.Context, container string) ([]guestNetwork, error) {
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", guestNetworksTemplate, container).Output()
	if err != nil {
		return nil, fmt.Errorf("docker inspect %s: %w", container, err)
	}
	nets, err := parseGuestNetworks(string(out))
	if err != nil {
		return nil, fmt.Errorf("docker inspect %s: %w", container, err)
	}
	return nets, nil
}

// parseGuestNetworks reads guestNetworksTemplate's output.
func parseGuestNetworks(out string) ([]guestNetwork, error) {
	var nets []guestNetwork
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) != 6 {
			return nil, fmt.Errorf("unexpected network settings %q", line)
		}
		for _, family := range [][]string{fields[0:3], fields[3:6]} {
			ipStr, prefixStr, gwStr := unsetAddr(family[0]), family[1], unsetAddr(family[2])
			if ipStr == "" {
				continue
			}
			ip := net.ParseIP(ipStr)
			if ip == nil {
				return nil, fmt.Errorf("invalid container IP %q", ipStr)
			}
			_, cidr, err := net.ParseCIDR(ipStr + "/" + prefixStr)
			if err != nil {
				return nil, fmt.Errorf("parse CIDR %s/%s: %w", ipStr, prefixStr, err)
			}
			n := guestNetwork{IP: ip, CIDR: cidr}
			if gwStr != "" {
				if n.Gateway = net.ParseIP(gwStr); n.Gateway == nil {
					return nil, fmt.Errorf("invalid gateway %q", gwStr)
				}
			}
			nets = append(nets, n)
		}
	}
	if len(nets) == 0 {
		return nil, fmt.Errorf("no addresses on any of its networks (%q)", strings.TrimSpace(out))
	}
	return nets, nil
}

// unsetAddr is one address field of guestNetworksTemplate's output with
// docker's spellings of "no address" read as the empty string. Docker 29
// moved these fields to netip.Addr, whose zero value a template prints as
// "invalid IP" rather than nothing -- so a container with no IPv6, which
// is nearly every one, reads "|invalid IP|0|invalid IP" there. Taken as an
// address, that failed every NetworkNone Acquire on such a host before a
// single rule was written. "<nil>" is the same absence from a net.IP.
func unsetAddr(field string) string {
	switch field {
	case "invalid IP", "<nil>":
		return ""
	}
	return field
}

// guestLinksScript prints one line per network interface in the
// container's namespace: its name, its ifindex and its iflink -- for a
// veth, the ifindex of its peer in whichever namespace that peer is in.
// Run with docker exec, since this daemon can neither enter the VM
// container's namespace nor read its /proc/<pid>/root. It is only the
// fallback to containerNetnsPorts: in a granule VM container sh is
// granule-shim, which runs it in the guest, whose interfaces are virtio
// and never the container's veth.
const guestLinksScript = `cd /sys/class/net && for i in *; do echo "$i $(cat "$i/ifindex") $(cat "$i/iflink")"; done`

// guestLink is one line of guestLinksScript's output.
type guestLink struct {
	index, peerIdx int
}

// hostSysfsNet is where this process reads the host's network interfaces.
// The daemon runs in the host's network namespace (--network host, or on
// the host itself), so this is the host's view, not a container's.
var hostSysfsNet = "/sys/class/net"

// inspectGuestPorts finds the host end of every veth container is
// attached to its docker networks by: the bridge ports its frames enter
// the host through, which granuleIsolationRuleset jumps from by name. It
// is an error for there to be fewer than it has networks -- a network
// whose port cannot be found is one a spoofed source address would
// still leave the host from -- so a container on a network with no veth
// at all (macvlan, ipvlan) is refused rather than half-filtered.
func inspectGuestPorts(ctx context.Context, container string) ([]string, error) {
	countCmd := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{len .NetworkSettings.Networks}}", container)
	countCmd.Env = dockerClientEnv()
	countOut, err := countCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker inspect %s: %w", container, err)
	}
	networks, err := strconv.Atoi(strings.TrimSpace(string(countOut)))
	if err != nil {
		return nil, fmt.Errorf("docker inspect %s: network count %q: %w", container, strings.TrimSpace(string(countOut)), err)
	}
	// The kernel's answer first: it does not depend on anything inside
	// the container, which for a granule VM is the guest (see
	// netnsBridgePorts). Only when it cannot be had -- not Linux, or a
	// daemon in a pid namespace of its own that cannot see the
	// container's processes -- is the container asked instead.
	ports, nsErr := containerNetnsPorts(ctx, container)
	if nsErr == nil && len(ports) >= networks {
		return ports, nil
	}
	if nsErr == nil {
		nsErr = fmt.Errorf("%d bridge port(s) in its network namespace (%q)", len(ports), strings.Join(ports, ", "))
	}
	linksCmd := exec.CommandContext(ctx, "docker", "exec", container, "sh", "-c", guestLinksScript)
	linksCmd.Env = dockerClientEnv()
	out, err := linksCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("listing %s's interfaces: %w (and from the host: %v)", container, err, nsErr)
	}
	links, err := parseGuestLinks(string(out))
	if err != nil {
		return nil, fmt.Errorf("listing %s's interfaces: %w", container, err)
	}
	ports, err = hostPortsFor(hostSysfsNet, links)
	if err != nil {
		return nil, err
	}
	if len(ports) < networks {
		return nil, fmt.Errorf("%s is on %d docker network(s), and only %d host bridge port(s) were found for it (%q; from the host: %v)",
			container, networks, len(ports), strings.Join(ports, ", "), nsErr)
	}
	return ports, nil
}

// containerNetnsPorts is netnsBridgePorts for container's own process,
// which docker names by its pid in the host's pid namespace.
//
// That pid is only trusted once this process's /proc shows it in the
// container's own cgroup, before and after the kernel is asked: a daemon
// in a pid namespace of its own reads the number as one of its own
// processes or none, and a pid reused in between would be somebody
// else's namespace -- the wrong ports firewalled, and the VM's left open.
// Either is refused rather than answered.
func containerNetnsPorts(ctx context.Context, container string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.Id}} {{.State.Pid}}", container)
	cmd.Env = dockerClientEnv()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker inspect %s: %w", container, err)
	}
	f := strings.Fields(string(out))
	if len(f) != 2 {
		return nil, fmt.Errorf("docker inspect %s: unexpected %q", container, strings.TrimSpace(string(out)))
	}
	id := f[0]
	pid, err := strconv.Atoi(f[1])
	if err != nil || pid <= 0 || len(id) < 12 {
		return nil, fmt.Errorf("docker inspect %s: no running process (%q)", container, strings.TrimSpace(string(out)))
	}
	if err := pidInContainer(pid, id); err != nil {
		return nil, err
	}
	ports, err := hostNetnsPorts(pid)
	if err != nil {
		return nil, err
	}
	if err := pidInContainer(pid, id); err != nil {
		return nil, err
	}
	return ports, nil
}

// procRoot is where pidInContainer reads processes' cgroups.
var procRoot = "/proc"

// pidInContainer reports whether pid's cgroup is container id's, which
// docker's cgroup paths name in full under either cgroup driver
// (".../docker-<id>.scope" or ".../docker/<id>").
func pidInContainer(pid int, id string) error {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return fmt.Errorf("reading pid %d's cgroup: %w", pid, err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		_, path, ok := strings.Cut(line, "::")
		if !ok {
			if i := strings.LastIndex(line, ":"); i >= 0 {
				path, ok = line[i+1:], true
			}
		}
		if !ok {
			continue
		}
		for _, part := range strings.Split(path, "/") {
			if part == id || part == "docker-"+id+".scope" {
				return nil
			}
		}
	}
	return fmt.Errorf("pid %d is not in container %s's cgroup (%q)", pid, id[:12], strings.TrimSpace(string(b)))
}

// parseGuestLinks reads guestLinksScript's output.
func parseGuestLinks(out string) ([]guestLink, error) {
	var links []guestLink
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(f) != 3 {
			return nil, fmt.Errorf("unexpected interface line %q", line)
		}
		index, err1 := strconv.Atoi(f[1])
		peer, err2 := strconv.Atoi(f[2])
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("unexpected interface line %q", line)
		}
		links = append(links, guestLink{index: index, peerIdx: peer})
	}
	return links, nil
}

// portName is what an interface name has to look like to be written into
// an nft script. The kernel allows nearly anything but '/', ':' and
// whitespace; docker's veths are "veth" and seven hex digits.
var portName = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,15}$`)

// hostPortsFor finds, under sysfs (the host's /sys/class/net), the host
// end of each of links that is one: an interface whose ifindex is the
// link's iflink, whose own iflink is the link's ifindex -- the two ends
// of one veth pair name each other -- and which is enslaved to a bridge.
// Host ifindexes are unique, so that names one interface at most, and the
// cross-check rules out a link whose peer is in some namespace other than
// the host's (or that has no peer at all: loopback, a tap, a bridge,
// whose iflink is their own ifindex) happening to share a number with
// one.
func hostPortsFor(sysfs string, links []guestLink) ([]string, error) {
	byIndex := map[int]string{}
	entries, err := os.ReadDir(sysfs)
	if err != nil {
		return nil, fmt.Errorf("listing the host's interfaces: %w", err)
	}
	for _, e := range entries {
		if idx, err := readSysfsInt(filepath.Join(sysfs, e.Name(), "ifindex")); err == nil {
			byIndex[idx] = e.Name()
		}
	}
	var ports []string
	for _, l := range links {
		if l.peerIdx == l.index {
			continue
		}
		name, ok := byIndex[l.peerIdx]
		if !ok {
			continue
		}
		if back, err := readSysfsInt(filepath.Join(sysfs, name, "iflink")); err != nil || back != l.index {
			continue
		}
		if _, err := os.Stat(filepath.Join(sysfs, name, "brport")); err != nil {
			continue
		}
		if !portName.MatchString(name) {
			return nil, fmt.Errorf("host bridge port %q is not a name this firewall can write a rule for", name)
		}
		ports = append(ports, name)
	}
	return ports, nil
}

func readSysfsInt(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

// nftRunner runs nft with args, handing it stdin. It is a variable so the
// tests in this package can see what would have been run without a host
// that lets them change its firewall.
type nftRunner func(ctx context.Context, stdin string, args ...string) ([]byte, error)

// runNFT is the nftRunner this daemon uses: nft itself when this process
// can change the host firewall on its own -- root, or an nft carrying
// CAP_NET_ADMIN, which is how the container image grants it (a file
// capability on /usr/sbin/nft, raised from setup.sh's --cap-add
// NET_ADMIN) -- and `sudo -n nft` when it cannot, which is a host-mode
// install whose account setup.sh gave that one command
// (write_host_nft_sudoers). Both are tried rather than guessed
// between, because there is no cheap way to ask which of the two this
// process is short of; the error returned is the last one, with what nft
// or sudo said.
func runNFT(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	nftPath, err := findNFTBinary()
	if err != nil {
		return nil, err
	}
	run := func(name string, argv ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, argv...)
		if stdin != "" {
			cmd.Stdin = strings.NewReader(stdin)
		}
		return cmd.CombinedOutput()
	}
	out, err := run(nftPath, args...)
	if err == nil || os.Geteuid() == 0 {
		return out, wrapNFTError(err, out)
	}
	sudo, lookErr := exec.LookPath("sudo")
	if lookErr != nil {
		return out, wrapNFTError(err, out)
	}
	out, err = run(sudo, append([]string{"-n", nftPath}, args...)...)
	return out, wrapNFTError(err, out)
}

func wrapNFTError(err error, out []byte) error {
	if err == nil {
		return nil
	}
	if detail := strings.TrimSpace(string(out)); detail != "" {
		return fmt.Errorf("%w (%s)", err, detail)
	}
	return err
}

// interfacePrefixes is every prefix this process's network namespace
// has an address in -- the host's, since the daemon runs in it (see
// hostSysfsNet).
func interfacePrefixes() ([]netip.Prefix, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []netip.Prefix
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		addr, ok := netip.AddrFromSlice(ipnet.IP)
		if !ok {
			continue
		}
		bits, _ := ipnet.Mask.Size()
		out = append(out, netip.PrefixFrom(addr.Unmap(), bits))
	}
	return out, nil
}

// Host-side variables the tests in this package replace; production
// leaves them as they are.
var (
	hostConnectedPrefixes           = interfacePrefixes
	hostNFT               nftRunner = runNFT
	hostGuestNetworks               = inspectGuestNetworks
	hostGuestPorts                  = inspectGuestPorts
	hostNetnsPorts                  = netnsBridgePorts
)

// nftTable is the table every granule VM's chain lives in -- open-network
// VMs' too, under the name it had before they had one.
const nftTable = "grain_network_none"

// nftBase is one base chain a NetworkNone VM's chain is jumped to from:
// the family of the nftTable it lives in, and the hook it is attached to,
// which is also its name.
type nftBase struct{ family, hook string }

// nftBases are every base chain granuleIsolationRuleset installs, and
// nftFamilies the tables they are in -- one per family, each holding its
// own copy of the VM's chain, since a jump cannot cross tables.
// nftPortBase is the one of them jumped from by bridge port rather than
// by source address.
var (
	nftPortBase = nftBase{"bridge", "prerouting"}
	nftBases    = []nftBase{{"inet", "forward"}, {"inet", "input"}, {"bridge", "forward"}, nftPortBase}
	nftFamilies = []string{"inet", "bridge"}
)

// granuleIsolationRuleset is the nft script that cuts vm's container off
// from everything but what a NetworkNone sandbox is meant to reach: the
// gateway of each segment it is on (the docker gateway this deployment's
// git and model proxies and the package cache's resolver are bound on,
// under netshim's flat mode) and the git proxy -- and it drops everything
// else, IPv4 and IPv6 alike, since the table is inet and the final rule
// names no family.
//
// The VM's chain is jumped to from four base chains (nftBases). It
// takes the first three by the source address of what is sent:
//
//   - inet forward, for anything routed off the host: the internet, and
//     any network other than the VM's own.
//   - bridge forward, for the other containers on the VM's own docker
//     network, which is why the accept is the gateway address and not the
//     segment. VM containers share docker's default bridge unless
//     CreateArgs names another network, so a segment-wide accept let a
//     NetworkNone guest reach a NetworkOpen neighbour's ports -- an egress
//     path through whatever that neighbour would relay. Frames between
//     two ports of one bridge are switched rather than routed, so they
//     reach the inet forward hook only when br_netfilter is loaded, and
//     docker 29 no longer loads it for a network with inter-container
//     traffic on; the bridge family sees them either way.
//   - inet input, for anything addressed to the host itself. The forward
//     hooks never see that traffic, so before this chain was jumped to
//     from input every service listening on any of the host's addresses
//     was reachable from a NetworkNone sandbox -- its public interface's
//     included -- and any one of them that fetches URLs is a way out. Now
//     the host is reachable only at the gateway address the sandbox
//     services are advertised on.
//
// The source address is only the guest's word, though. The guest's
// account has passwordless root, so it can send from any address on its
// segment that it likes, and nothing it sent from an address other than
// the container's was jumped to the chain at all -- a spoofed packet was
// forwarded to the internet, NATed on its way out like any other.
// netshim drops such frames inside the VM's container (spliceGuest), but
// only in an image built since it learned to, and a NetworkNone sandbox
// should not rest on which image a host happens to run. So the
// fourth base chain, bridge prerouting, jumps by where a frame came from
// rather than what it says: every frame that enters the bridge through
// one of the container's own ports (ports, the host end of its veths),
// whatever its addresses, is taken to the chain before the bridge
// decides whether to switch it to a neighbour or hand it up to the host
// to be delivered or routed. That is the only hook the veth is visible
// in -- by the inet hooks a frame has left the bridge, and its input
// interface is the bridge every container on the segment shares. ARP is
// the one ethertype let past: the chain would drop it, having no IPv4
// header to accept, and the guest could no longer resolve the gateway.
// Spoofing *that* is netshim's to stop, inside the VM's container
// (spliceGuest).
//
// The accept at the gateway and the git proxy is narrowed to services:
// the ports this deployment serves sandboxes on (GranuleConfig's
// SandboxServicePorts), by protocol. The address alone is not enough,
// because a service bound to the wildcard address -- sshd, a local HTTP
// proxy, anything listening on 0.0.0.0 -- is listening on the gateway
// too, and one of them that fetches URLs is a way out. allPorts is a
// deployment that named no ports, whose gateway is accepted whole, as it
// was before this package knew them; services empty with allPorts unset
// accepts nothing there at all. What the resolver on one of those ports
// will forward is restrictGuestDNS's to narrow.
//
// The ICMPv6 neighbour discovery accept is what keeps the gateway
// reachable over IPv6 at all: a guest's solicitation for the gateway can
// leave from its global address to a solicited-node multicast group, which
// is not the gateway's address and would otherwise reach the drop on the
// input hook. IPv4's equivalent is ARP, which an inet table never sees.
//
// Nothing link-local is allowed, the granule control network
// (169.254.100.0/24) included. The control link lives entirely inside
// the VM's own container netns and never crosses any of the host's
// hooks, so an accept for it here could only ever match traffic leaving
// the container for some other link-local destination -- a neighbouring
// VM's control bridge, or a metadata service reached over an on-link
// route -- which is exactly what a sandbox with no network must not
// reach.
//
// It is one script, and is applied with `nft -f` as one transaction, so
// it either lands whole or not at all: there is no state in which the
// jump is installed and the drop is not, or the reverse, for a caller
// to have to reason about. The VM's chain is flushed before it is
// filled, so applying it again replaces rather than duplicates.
//
// fence is the other shape the same chain takes: a model.NetworkOpen VM
// (granuleOpenFence). Nil is NetworkNone, and the chain ends in a drop.
func granuleIsolationRuleset(chain string, nets []guestNetwork, ports []string, gitProxy net.IP, services []SandboxServicePort, allPorts bool, fence *openFence) string {
	matches := []string{""}
	if !allPorts {
		matches = serviceMatches(services)
	}
	var b strings.Builder
	for _, family := range nftFamilies {
		fmt.Fprintf(&b, "add table %s %s\n", family, nftTable)
		for _, base := range nftBases {
			if base.family == family {
				fmt.Fprintf(&b, "add chain %s %s %s { type filter hook %s priority -10; policy accept; }\n",
					family, nftTable, base.hook, base.hook)
			}
		}
		fmt.Fprintf(&b, "add chain %s %s %s\n", family, nftTable, chain)
		fmt.Fprintf(&b, "flush chain %s %s %s\n", family, nftTable, chain)
		allow := func(addr net.IP) {
			for _, match := range matches {
				fmt.Fprintf(&b, "add rule %s %s %s %s daddr %s %saccept\n", family, nftTable, chain, ipFamily(addr), addr, match)
			}
		}
		v6 := false
		for _, n := range nets {
			if n.IP.To4() == nil {
				v6 = true
			}
			if n.Gateway != nil {
				allow(n.Gateway)
			}
		}
		if gitProxy != nil && !isGateway(gitProxy, nets) {
			allow(gitProxy)
		}
		if v6 {
			fmt.Fprintf(&b, "add rule %s %s %s icmpv6 type { nd-neighbor-solicit, nd-neighbor-advert } accept\n",
				family, nftTable, chain)
		}
		if fence != nil {
			fence.write(&b, family, chain, nets)
			continue
		}
		fmt.Fprintf(&b, "add rule %s %s %s counter drop\n", family, nftTable, chain)
	}
	for _, base := range nftBases {
		if base == nftPortBase {
			continue
		}
		for _, n := range nets {
			fmt.Fprintf(&b, "add rule %s %s %s %s saddr %s jump %s\n",
				base.family, nftTable, base.hook, ipFamily(n.IP), n.IP, chain)
		}
	}
	for _, port := range ports {
		fmt.Fprintf(&b, "add rule %s %s %s iifname %q ether type != arp jump %s\n",
			nftPortBase.family, nftTable, nftPortBase.hook, port, chain)
	}
	return b.String()
}

// openFence is what a model.NetworkOpen VM's chain drops in place of
// NetworkNone's "everything": the destinations an open sandbox has no
// business reaching even though it may reach the internet.
//
// "Open" is the internet beyond this host, not this host or the networks
// it sits on. Without a chain at all an open guest -- running whatever
// its task's repository runs -- could reach every service listening on
// any of the host's addresses (the UI on a non-loopback -ui-addr, sshd,
// a capability server's serverUrl: docs/capabilities-spec.md §3.2.4),
// the neighbouring containers and VMs on its own segment, every machine
// on the host's LAN or VPC subnet, and the cloud metadata server with
// the host's service-account identity behind it. So the chain, after
// the same service-port accepts at the gateway and git proxy that a
// NetworkNone chain makes, drops by destination:
//
//   - link-local (169.254.0.0/16, fe80::/10): the metadata server, and
//     anything else served on-link;
//   - loopback, which no route should deliver but nothing obliges a
//     kernel to refuse a forged one;
//   - every prefix the host is directly connected to (Connected): its
//     own addresses, docker's bridges and their containers, and its
//     LAN or VPC subnet's peers;
//   - the VM's own segments, in case the daemon's view of the host's
//     interfaces (net.InterfaceAddrs) is not the host's.
//
// and returns, rather than accepting, for everything else -- so the
// rest of the host's own firewall (docker's NAT among it) still decides
// what happens to it.
//
// Everything here matches on the destination, never the source, so the
// guest's root cannot step round it by spoofing: bridge prerouting
// jumps by port, and a forged source still names the destination the
// frame is going to.
//
// DNS (port 53) is let through to anywhere first. A guest with no
// package cache resolves through whatever its image or granule's own
// default names, which can be the host's LAN resolver or, on a GCE
// host, the metadata server itself; resolution is the one thing on
// those addresses an open sandbox has a use for, and a resolver is no
// way out for a guest that already has the internet.
type openFence struct {
	// Connected is every prefix the host has an address in.
	Connected []netip.Prefix
}

// openFenceAlways is dropped for every open VM, whatever the host's
// interfaces are.
var openFenceAlways = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fe80::/10"),
}

// prefixes is every destination f drops for nets, by family, masked,
// sorted, and with any prefix another one already covers left out: nft
// refuses an anonymous set holding two overlapping intervals, and two
// prefixes either nest or do not overlap at all. Sorted so one host
// always writes the same ruleset.
func (f *openFence) prefixes(nets []guestNetwork) (v4, v6 []string) {
	all := append(append([]netip.Prefix(nil), openFenceAlways...), f.Connected...)
	for _, n := range nets {
		if n.CIDR == nil {
			continue
		}
		addr, ok := netip.AddrFromSlice(n.CIDR.IP)
		if !ok {
			continue
		}
		bits, _ := n.CIDR.Mask.Size()
		all = append(all, netip.PrefixFrom(addr.Unmap(), bits))
	}
	seen := map[netip.Prefix]bool{}
	var p4, p6 []netip.Prefix
	for _, p := range all {
		if !p.IsValid() {
			continue
		}
		seen[p.Masked()] = true
	}
	for p := range seen {
		covered := false
		for q := range seen {
			if q.Bits() < p.Bits() && q.Contains(p.Addr()) {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		if p.Addr().Is4() {
			p4 = append(p4, p)
		} else {
			p6 = append(p6, p)
		}
	}
	cmp := func(a, b netip.Prefix) int {
		if c := a.Addr().Compare(b.Addr()); c != 0 {
			return c
		}
		return a.Bits() - b.Bits()
	}
	slices.SortFunc(p4, cmp)
	slices.SortFunc(p6, cmp)
	for _, p := range p4 {
		v4 = append(v4, p.String())
	}
	for _, p := range p6 {
		v6 = append(v6, p.String())
	}
	return v4, v6
}

// write appends f's rules to one family's copy of chain, for a VM on
// nets.
func (f *openFence) write(b *strings.Builder, family, chain string, nets []guestNetwork) {
	v4, v6 := f.prefixes(nets)
	fmt.Fprintf(b, "add rule %s %s %s meta l4proto { tcp, udp } th dport 53 accept\n", family, nftTable, chain)
	if len(v4) > 0 {
		fmt.Fprintf(b, "add rule %s %s %s ip daddr { %s } counter drop\n", family, nftTable, chain, strings.Join(v4, ", "))
	}
	if len(v6) > 0 {
		fmt.Fprintf(b, "add rule %s %s %s ip6 daddr { %s } counter drop\n", family, nftTable, chain, strings.Join(v6, ", "))
	}
}

// serviceMatches is services as nft match fragments, one per protocol
// with a port to accept, each ending in a space so it reads straight
// into a rule before its verdict. Sorted and deduplicated, so one
// provider's answer always writes the same ruleset, and a port listed
// twice is not a set nft refuses for holding it twice. A protocol other
// than tcp or udp, or a port outside 1-65535, is not a thing a rule can
// accept and is left out.
func serviceMatches(services []SandboxServicePort) []string {
	byProto := map[string][]int{}
	for _, s := range services {
		if (s.Proto != "tcp" && s.Proto != "udp") || s.Port < 1 || s.Port > 65535 {
			continue
		}
		if !slices.Contains(byProto[s.Proto], s.Port) {
			byProto[s.Proto] = append(byProto[s.Proto], s.Port)
		}
	}
	var out []string
	for _, proto := range []string{"tcp", "udp"} {
		ports := byProto[proto]
		if len(ports) == 0 {
			continue
		}
		slices.Sort(ports)
		list := make([]string, len(ports))
		for i, p := range ports {
			list[i] = strconv.Itoa(p)
		}
		out = append(out, fmt.Sprintf("%s dport { %s } ", proto, strings.Join(list, ", ")))
	}
	return out
}

// ipFamily is the nft payload protocol an address is matched with.
func ipFamily(ip net.IP) string {
	if ip.To4() != nil {
		return "ip"
	}
	return "ip6"
}

// isGateway reports whether ip is the gateway of one of nets, so already
// accepted by the ruleset without a rule of its own.
func isGateway(ip net.IP, nets []guestNetwork) bool {
	for _, n := range nets {
		if n.Gateway != nil && n.Gateway.Equal(ip) {
			return true
		}
	}
	return false
}

// enforceGranuleHostIsolation installs host nftables firewall rules to enforce
// model.NetworkNone outside the guest VM for the given granule VM.
//
// This is the half of NetworkNone the guest cannot undo. The guest's
// missing default route (guestNetworkNoneCommand) is a promise the guest
// keeps, and the account every tool call runs as has passwordless root,
// so anything a task runs can put the route back; these rules sit in the
// host's forward path, where nothing inside the VM reaches.
//
// Every failure is returned rather than logged: no nft, no way to run it
// with the privilege it needs, no address to write a rule for, or a
// ruleset nft refused. The caller fails the Acquire on any of them,
// because a sandbox whose only isolation is the guest's own cooperation
// is not a sandbox with no network (#2394).
//
// open writes a model.NetworkOpen VM's chain instead (openFence): the
// same accepts, then drops for this host, its networks and link-local
// in place of the drop of everything.
func (k *GranuleSandboxes) enforceGranuleHostIsolation(ctx context.Context, vmName string, addr netip.Addr, open bool) error {
	var gitProxy net.IP
	if k.cfg.GitProxyHost != "" {
		if gitProxy = net.ParseIP(k.cfg.GitProxyHost); gitProxy == nil {
			return fmt.Errorf("the git proxy host %q is not an IP address, so no host firewall rule can allow it", k.cfg.GitProxyHost)
		}
	}
	nets, err := hostGuestNetworks(ctx, granule.PodName(vmName))
	if err != nil {
		return fmt.Errorf("finding the addresses to firewall: %w", err)
	}
	if err := checkAllocatedAddress(addr, nets); err != nil {
		return err
	}
	ports, err := hostGuestPorts(ctx, granule.PodName(vmName))
	if err != nil {
		return fmt.Errorf("finding the bridge ports to firewall: %w", err)
	}
	var fence *openFence
	if open {
		connected, err := hostConnectedPrefixes()
		if err != nil {
			return fmt.Errorf("finding the networks this host is on: %w", err)
		}
		fence = &openFence{Connected: connected}
	}

	nftLock.Lock()
	defer nftLock.Unlock()

	k.hostNFTUnusable = false
	chainName := "vm_" + sanitizeNFTName(vmName)

	// Clean up any stale jump rule first -- one left by an earlier VM of
	// this name, or by whatever last held these addresses or ports:
	ips := make([]string, 0, len(nets))
	for _, n := range nets {
		ips = append(ips, n.IP.String())
	}
	cleanupGranuleHostIsolationLocked(ctx, chainName, ips, ports)

	var services []SandboxServicePort
	allPorts := k.cfg.SandboxServicePorts == nil
	accepted := "every port"
	if !allPorts {
		services = k.cfg.SandboxServicePorts()
		accepted = strings.TrimSpace(strings.Join(serviceMatches(services), "; "))
		if accepted == "" {
			accepted = "no port"
		}
	}
	if _, err := hostNFT(ctx, granuleIsolationRuleset(chainName, nets, ports, gitProxy, services, allPorts, fence), "-f", "-"); err != nil {
		return fmt.Errorf("installing nftables chain %s: %w", chainName, err)
	}

	shape := "isolation"
	if open {
		shape = "open-network fence"
	}
	log.Printf("orchestrator: granule VM %q: installed host nftables %s rules outside the guest VM (chain %s for %s, bridge ports %s; the gateway accepts %s)",
		vmName, shape, chainName, strings.Join(ips, ", "), strings.Join(ports, ", "), accepted)
	return nil
}

// checkAllocatedAddress confirms the VM actually holds the address this
// deployment allocated it, for a deployment that allocates them at all
// (GranuleConfig.Addresses). The zero addr is one that does not, and has
// nothing to check.
//
// A check rather than a substitution, which is the whole design decision
// here. It would be tempting to write the rules straight from the
// allocation and skip the inspect: the address is known before the VM
// boots, so the rules would not depend on docker answering. But the
// allocation is one IPv4 address, and these rules filter per source
// address -- so any other address the container holds, an IPv6 one in
// particular, would be left out of the chain and therefore not filtered at
// all. A NetworkNone sandbox that could still reach the internet over IPv6
// is a worse outcome than a rule that has to wait for an inspect, so the
// inspect stays authoritative for *what to filter* and the allocation
// becomes authoritative for *what should be there*.
//
// Fatal when it fails, like every other failure in this function. A
// mismatch means docker did not honour the --ip the VM was created with --
// a network it was not attached to, a subnet that does not contain the
// pool, a stale container holding a recycled address -- and in every one of
// those the deployment's model of where its sandboxes are is wrong. That is
// worth failing an Acquire over rather than filtering an address nobody
// chose and reporting success.
func checkAllocatedAddress(addr netip.Addr, nets []guestNetwork) error {
	if !addr.IsValid() {
		return nil
	}
	held := make([]string, 0, len(nets))
	for _, n := range nets {
		got, ok := netip.AddrFromSlice(n.IP)
		if !ok {
			continue
		}
		if got.Unmap() == addr {
			return nil
		}
		held = append(held, got.Unmap().String())
	}
	return fmt.Errorf("this deployment allocated %s to this VM, and its container holds %s instead -- "+
		"docker did not honour the --ip it was created with, so nothing here knows where this sandbox "+
		"actually is", addr, strings.Join(held, ", "))
}

// DNSEgressPolicy is the part of a sandbox resolver (pkgcache.DNSServer)
// that knows which of its clients have no network: RestrictClient makes
// it answer everything outside its allowlist from that address with
// NXDOMAIN rather than forwarding it upstream, and UnrestrictClient
// undoes one RestrictClient.
type DNSEgressPolicy interface {
	RestrictClient(ip net.IP)
	UnrestrictClient(ip net.IP)
}

// restrictGuestDNS tells cfg.DNSEgress that vmName has model.NetworkNone.
//
// It is the half of NetworkNone that neither the guest's missing default
// route nor the host's nftables chain can give: the sandbox resolver is
// on the docker gateway, which both of them leave reachable on purpose,
// and a resolver that forwards whatever it is asked is a channel to any
// authoritative nameserver on the internet -- data goes out in the name
// queried and comes back in the answer. The resolver sees the address a
// query arrived from, which is the VM's container address (the same one
// enforceGranuleHostIsolation's own rules match), so that is what it is
// told to restrict -- every one of them, IPv6 included, since a query sent
// over IPv6 arrives from the container's IPv6 address and a resolver told
// only about the IPv4 one would forward it.
func (k *GranuleSandboxes) restrictGuestDNS(ctx context.Context, vmName string) error {
	if k.cfg.DNSEgress == nil {
		return nil
	}
	lookup := k.guestIPs
	if lookup == nil {
		lookup = func(ctx context.Context, vmName string) ([]net.IP, error) {
			nets, err := hostGuestNetworks(ctx, granule.PodName(vmName))
			if err != nil {
				return nil, err
			}
			ips := make([]net.IP, 0, len(nets))
			for _, n := range nets {
				ips = append(ips, n.IP)
			}
			return ips, nil
		}
	}
	ips, err := lookup(ctx, vmName)
	if err != nil {
		return err
	}
	if len(ips) == 0 {
		return fmt.Errorf("no address found for %s", vmName)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.dnsRestricted == nil {
		k.dnsRestricted = map[string][]net.IP{}
	}
	for _, old := range k.dnsRestricted[vmName] {
		k.cfg.DNSEgress.UnrestrictClient(old)
	}
	for _, ip := range ips {
		k.cfg.DNSEgress.RestrictClient(ip)
	}
	k.dnsRestricted[vmName] = ips
	return nil
}

// releaseGuestDNS undoes restrictGuestDNS for vmName, if it was done.
func (k *GranuleSandboxes) releaseGuestDNS(vmName string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	ips, ok := k.dnsRestricted[vmName]
	if !ok {
		return
	}
	delete(k.dnsRestricted, vmName)
	if k.cfg.DNSEgress != nil {
		for _, ip := range ips {
			k.cfg.DNSEgress.UnrestrictClient(ip)
		}
	}
}

// cleanupGranuleHostIsolation removes host nftables firewall rules for the
// given granule VM, and its restriction at the sandbox resolver.
func (k *GranuleSandboxes) cleanupGranuleHostIsolation(ctx context.Context, vmName string) {
	k.releaseGuestDNS(vmName)

	nftLock.Lock()
	defer nftLock.Unlock()

	if k.hostNFTUnusable {
		return
	}
	chainName := "vm_" + sanitizeNFTName(vmName)
	if !cleanupGranuleHostIsolationLocked(ctx, chainName, nil, nil) {
		k.hostNFTUnusable = true
	}
}

// nftCannotRun reports whether err means nft cannot be invoked by this
// process at all (missing binary, missing CAP_NET_ADMIN without sudoers, or
// sudo requiring a password), as opposed to a missing table or chain.
func nftCannotRun(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"a password is required",
		"operation not permitted",
		"not in the sudoers file",
		"may not run sudo",
		"no tty present",
		"nft binary not found",
		"permission denied",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// cleanupGranuleHostIsolationLocked deletes every jump to chainName, and
// every jump from any of ips or ports, out of each of nftBases, then the
// chain itself from each table. Best-effort: none of it existing is the
// ordinary case, for a VM that never had NetworkNone -- and so is a host
// whose tables predate the input and bridge chains, until the first
// NetworkNone VM since adds them. Returns false when nft cannot be run at
// all so callers can avoid repeating doomed sudo invocations.
func cleanupGranuleHostIsolationLocked(ctx context.Context, chainName string, ips, ports []string) bool {
	for _, base := range nftBases {
		out, err := hostNFT(ctx, "", "-a", "list", "chain", base.family, nftTable, base.hook)
		if err != nil {
			if nftCannotRun(err) {
				return false
			}
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if !jumpRuleMatches(fields, chainName, ips, ports) {
				continue
			}
			for i, field := range fields {
				if field == "handle" && i+1 < len(fields) {
					if _, err := hostNFT(ctx, "", "delete", "rule", base.family, nftTable, base.hook, "handle", fields[i+1]); err != nil {
						log.Printf("orchestrator: deleting stale nftables jump %q: %v", strings.TrimSpace(line), err)
					}
				}
			}
		}
	}

	// Flush and delete the VM chain:
	for _, family := range nftFamilies {
		_, _ = hostNFT(ctx, "", "flush", "chain", family, nftTable, chainName)
		_, _ = hostNFT(ctx, "", "delete", "chain", family, nftTable, chainName)
	}
	return true
}

// jumpRuleMatches reports whether one `nft -a list chain` line, split
// into fields, is a jump to chainName or a rule matching a source in ips
// or an input port in ports. Field-exact rather than a substring match,
// so vm_g_t1_1 is not taken for vm_g_t1_10, 10.0.0.1 not for 10.0.0.10,
// and veth1 not for veth10.
func jumpRuleMatches(fields []string, chainName string, ips, ports []string) bool {
	for i, f := range fields {
		if i+1 >= len(fields) {
			break
		}
		next := fields[i+1]
		switch f {
		case "jump":
			if next == chainName {
				return true
			}
		case "saddr":
			if slices.Contains(ips, next) {
				return true
			}
		case "iifname":
			if slices.Contains(ports, strings.Trim(next, `"`)) {
				return true
			}
		}
	}
	return false
}
