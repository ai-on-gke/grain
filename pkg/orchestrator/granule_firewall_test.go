package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestGranuleFirewall_SanitizeNFTName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"g-t1-1", "g_t1_1"},
		{"vm_123", "vm_123"},
		{"sandbox-none-test.1", "sandbox_none_test_1"},
		{"foo@bar#baz", "foo_bar_baz"},
	}
	for _, tc := range cases {
		got := sanitizeNFTName(tc.in)
		if got != tc.want {
			t.Errorf("sanitizeNFTName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// withHostFirewall points the host firewall at nft and nets for one
// test, recording every nft invocation (its stdin, then its argv).
func withHostFirewall(t *testing.T, nft nftRunner, nets func(ctx context.Context, container string) ([]guestNetwork, error)) *[]string {
	t.Helper()
	var calls []string
	prevNFT, prevNets, prevPorts := hostNFT, hostGuestNetworks, hostGuestPorts
	t.Cleanup(func() { hostNFT, hostGuestNetworks, hostGuestPorts = prevNFT, prevNets, prevPorts })
	hostGuestPorts = fixedPorts("veth1d9c4eb")
	hostNFT = func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
		calls = append(calls, stdin+strings.Join(args, " "))
		return nft(ctx, stdin, args...)
	}
	hostGuestNetworks = nets
	return &calls
}

func fixedPorts(ports ...string) func(ctx context.Context, container string) ([]string, error) {
	return func(ctx context.Context, container string) ([]string, error) { return ports, nil }
}

func fixedNetworks(out string) func(ctx context.Context, container string) ([]guestNetwork, error) {
	return func(ctx context.Context, container string) ([]guestNetwork, error) { return parseGuestNetworks(out) }
}

// A VM whose host rules cannot be installed is refused, for every way
// that can happen -- never logged and handed over with only the guest's
// own route table between it and the internet (#2394).
func TestGranuleFirewall_HostIsolationFailsClosed(t *testing.T) {
	ctx := context.Background()
	ok := func(ctx context.Context, stdin string, args ...string) ([]byte, error) { return nil, nil }
	cases := map[string]struct {
		nft   nftRunner
		nets  func(ctx context.Context, container string) ([]guestNetwork, error)
		ports func(ctx context.Context, container string) ([]string, error)
		cfg   GranuleConfig
	}{
		"no nft or no privilege to run it": {
			nft: func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
				return nil, errors.New("nft binary not found")
			},
			nets: fixedNetworks("172.18.0.2|16|172.18.0.1|||"),
		},
		"no address to write a rule for": {
			nft: ok,
			nets: func(ctx context.Context, container string) ([]guestNetwork, error) {
				return nil, errors.New("no such container")
			},
		},
		"no bridge port to jump from": {
			nft:  ok,
			nets: fixedNetworks("172.18.0.2|16|172.18.0.1|||"),
			ports: func(ctx context.Context, container string) ([]string, error) {
				return nil, errors.New("granule-vm-g-t1-1 is on 1 docker network(s), and only 0 host bridge port(s) were found for it")
			},
		},
		"a git proxy that is not an address": {
			nft:  ok,
			nets: fixedNetworks("172.18.0.2|16|172.18.0.1|||"),
			cfg:  GranuleConfig{GitProxyHost: "proxy.example"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			withHostFirewall(t, tc.nft, tc.nets)
			if tc.ports != nil {
				hostGuestPorts = tc.ports
			}
			k := NewGranuleSandboxes(tc.cfg)
			if err := enforceHostThenGuest(ctx, k); err == nil || !strings.Contains(err.Error(), "firewall") {
				t.Fatalf("enforceGuestNetwork = %v, want a refusal naming the host firewall", err)
			}
		})
	}
}

// enforceHostThenGuest runs enforceGuestNetwork for a NetworkNone VM with
// a guest runner that would succeed at everything, so whatever it
// returns is the host side's verdict.
func enforceHostThenGuest(ctx context.Context, k *GranuleSandboxes) error {
	return k.enforceGuestNetwork(ctx, "g-t1-1", okRunner{}, Shape{NetworkAccess: model.NetworkNone}, netip.Addr{})
}

type okRunner struct{}

func (okRunner) Run(ctx context.Context, argv []string, stdin string) (string, string, int) {
	return "", "", 0
}

// The ruleset is one `nft -f -` transaction that allows the VM's
// gateways and the git proxy (nothing link-local, and not the rest of
// the segment its neighbours are on), drops the rest for both address
// families, and jumps to it from every address the VM's container holds
// -- IPv6 included -- on the input hook as well as forward, so the
// host's own addresses are filtered too.
func TestGranuleFirewall_HostIsolationRuleset(t *testing.T) {
	calls := withHostFirewall(t,
		func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
			if args[0] == "-a" && args[3] == "inet" && args[5] == "forward" {
				return []byte("table inet grain_network_none {\n\tchain forward {\n" +
					"\t\tip saddr 172.18.0.2 jump vm_g_t1_10 # handle 7\n" +
					"\t\tip saddr 172.18.0.20 jump vm_other # handle 8\n" +
					"\t\tip6 saddr fd00::2 jump vm_g_t1_9 # handle 9\n" +
					"\t\tip saddr 172.18.0.3 jump vm_g_t1_1 # handle 10\n\t}\n}\n"), nil
			}
			if args[0] == "-a" && args[3] == "inet" && args[5] == "input" {
				return []byte("table inet grain_network_none {\n\tchain input {\n" +
					"\t\tip saddr 172.18.0.2 jump vm_g_t1_10 # handle 11\n" +
					"\t\tip saddr 172.18.0.21 jump vm_other # handle 12\n\t}\n}\n"), nil
			}
			if args[0] == "-a" && args[3] == "bridge" && args[5] == "forward" {
				return []byte("table bridge grain_network_none {\n\tchain forward {\n" +
					"\t\tip saddr 172.18.0.3 jump vm_g_t1_1 # handle 13\n\t}\n}\n"), nil
			}
			if args[0] == "-a" && args[3] == "bridge" && args[5] == "prerouting" {
				return []byte("table bridge grain_network_none {\n\tchain prerouting {\n" +
					"\t\tiifname \"veth1d9c4eb\" ether type != arp jump vm_g_t1_7 # handle 14\n" +
					"\t\tiifname \"veth1d9c4e\" ether type != arp jump vm_other # handle 15\n" +
					"\t\tiifname \"veth2222222\" ether type != arp jump vm_g_t1_1 # handle 16\n\t}\n}\n"), nil
			}
			return nil, nil
		},
		fixedNetworks("172.18.0.2|16|172.18.0.1|fd00::2|64|fd00::1\n"))
	k := NewGranuleSandboxes(GranuleConfig{GitProxyHost: "172.18.0.1"})
	if err := k.enforceGranuleHostIsolation(context.Background(), "g-t1-1", netip.Addr{}); err != nil {
		t.Fatal(err)
	}

	var deleted []string
	var script string
	for _, c := range *calls {
		if strings.HasPrefix(c, "delete rule") {
			f := strings.Fields(c)
			deleted = append(deleted, f[2]+" "+f[4]+" "+f[len(f)-1])
		}
		if strings.HasSuffix(c, "-f -") {
			script = c
		}
	}
	// 7, 9 and 11 hold this VM's addresses, 10 jumps to its chain; 8 and
	// 12 are neither, however alike their addresses look; 13 jumps to its
	// chain in the bridge table. 14 is from this VM's port, left by
	// whatever last held it, 16 jumps to its chain from a port it no
	// longer has, and 15 is a port name 14's is only a prefix of. Each is
	// deleted from the chain it was listed in.
	if got, want := strings.Join(deleted, ","),
		"inet forward 7,inet forward 9,inet forward 10,inet input 11,bridge forward 13,bridge prerouting 14,bridge prerouting 16"; got != want {
		t.Errorf("stale jumps deleted = %s, want %s", got, want)
	}
	for _, want := range []string{
		"flush chain inet grain_network_none vm_g_t1_1\n",
		"add chain inet grain_network_none input { type filter hook input priority -10; policy accept; }\n",
		"add chain bridge grain_network_none forward { type filter hook forward priority -10; policy accept; }\n",
		"flush chain bridge grain_network_none vm_g_t1_1\n",
		"add rule bridge grain_network_none vm_g_t1_1 ip daddr 172.18.0.1 accept\n",
		"add rule bridge grain_network_none vm_g_t1_1 counter drop\n",
		"add rule bridge grain_network_none forward ip saddr 172.18.0.2 jump vm_g_t1_1\n",
		"add rule bridge grain_network_none forward ip6 saddr fd00::2 jump vm_g_t1_1\n",
		"add rule inet grain_network_none vm_g_t1_1 ip daddr 172.18.0.1 accept\n",
		"add rule inet grain_network_none vm_g_t1_1 ip6 daddr fd00::1 accept\n",
		"add rule inet grain_network_none vm_g_t1_1 icmpv6 type { nd-neighbor-solicit, nd-neighbor-advert } accept\n",
		"add rule inet grain_network_none vm_g_t1_1 counter drop\n",
		"add rule inet grain_network_none forward ip saddr 172.18.0.2 jump vm_g_t1_1\n",
		"add rule inet grain_network_none forward ip6 saddr fd00::2 jump vm_g_t1_1\n",
		"add rule inet grain_network_none input ip saddr 172.18.0.2 jump vm_g_t1_1\n",
		"add rule inet grain_network_none input ip6 saddr fd00::2 jump vm_g_t1_1\n",
		"add chain bridge grain_network_none prerouting { type filter hook prerouting priority -10; policy accept; }\n",
		"add rule bridge grain_network_none prerouting iifname \"veth1d9c4eb\" ether type != arp jump vm_g_t1_1\n",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("ruleset is missing %q:\n%s", want, script)
		}
	}
	// Not the segment: a neighbouring sandbox on the same docker network
	// is not somewhere a NetworkNone guest may reach.
	for _, segment := range []string{"172.18.0.0/16", "fd00::/64"} {
		if strings.Contains(script, segment) {
			t.Errorf("ruleset allows the VM's whole segment (%s):\n%s", segment, script)
		}
	}
	// The git proxy here is the gateway, already accepted once per table.
	if n := strings.Count(script, "daddr 172.18.0.1 accept"); n != len(nftFamilies) {
		t.Errorf("ruleset accepts the gateway %d times, want once per table (%d):\n%s", n, len(nftFamilies), script)
	}
	// Nothing link-local: the control network never crosses the host's
	// hooks, so an accept for it could only reach somewhere else.
	for _, linkLocal := range []string{"169.254.", "fe80:"} {
		if strings.Contains(script, linkLocal) {
			t.Errorf("ruleset allows a link-local destination (%s):\n%s", linkLocal, script)
		}
	}
	// The port's jump is the one that does not trust the source address,
	// so it must not name one; the source-address jumps stay out of the
	// port's base chain.
	if strings.Contains(script, "prerouting ip") {
		t.Errorf("ruleset jumps from bridge prerouting by address:\n%s", script)
	}
	if strings.LastIndex(script, "counter drop") > strings.Index(script, "jump vm_g_t1_1") {
		t.Errorf("the jump is added before the drop it leads to:\n%s", script)
	}
}

// With the deployment's service ports known, the gateway and the git
// proxy are accepted on those ports alone, by protocol -- so sshd or a
// local HTTP proxy listening on the wildcard address is not reachable
// there -- and nowhere is the address accepted whole.
func TestGranuleFirewall_HostIsolationRulesetNarrowsToServicePorts(t *testing.T) {
	calls := withHostFirewall(t,
		func(ctx context.Context, stdin string, args ...string) ([]byte, error) { return nil, nil },
		fixedNetworks("172.18.0.2|16|172.18.0.1|fd00::2|64|fd00::1\n"))
	asked := 0
	k := NewGranuleSandboxes(GranuleConfig{
		GitProxyHost: "10.0.0.9",
		SandboxServicePorts: func() []SandboxServicePort {
			asked++
			return []SandboxServicePort{
				{"tcp", 9744}, {"tcp", 9728}, {"udp", 53}, {"tcp", 53}, {"tcp", 443}, {"tcp", 80},
				{"tcp", 9728}, {"sctp", 1}, {"tcp", 0},
			}
		},
	})
	if err := k.enforceGranuleHostIsolation(context.Background(), "g-t1-1", netip.Addr{}); err != nil {
		t.Fatal(err)
	}
	if asked != 1 {
		t.Errorf("SandboxServicePorts asked %d times, want once per chain written", asked)
	}
	var script string
	for _, c := range *calls {
		if strings.HasSuffix(c, "-f -") {
			script = c
		}
	}
	for _, family := range nftFamilies {
		for _, addr := range []string{"ip daddr 172.18.0.1", "ip6 daddr fd00::1", "ip daddr 10.0.0.9"} {
			for _, match := range []string{"tcp dport { 53, 80, 443, 9728, 9744 }", "udp dport { 53 }"} {
				want := fmt.Sprintf("add rule %s grain_network_none vm_g_t1_1 %s %s accept\n", family, addr, match)
				if !strings.Contains(script, want) {
					t.Errorf("ruleset is missing %q:\n%s", want, script)
				}
			}
		}
	}
	if strings.Contains(script, "daddr 172.18.0.1 accept") || strings.Contains(script, "daddr fd00::1 accept") ||
		strings.Contains(script, "daddr 10.0.0.9 accept") {
		t.Errorf("ruleset accepts a host address on every port:\n%s", script)
	}
	if strings.Contains(script, "sctp") || strings.Contains(script, " 0,") {
		t.Errorf("ruleset accepts a port no rule can name:\n%s", script)
	}

	// A deployment serving nothing yet accepts nothing at the gateway,
	// rather than all of it.
	got := granuleIsolationRuleset("vm_x", []guestNetwork{{IP: net.ParseIP("172.18.0.2"), Gateway: net.ParseIP("172.18.0.1")}},
		nil, nil, nil, false)
	if strings.Contains(got, "daddr 172.18.0.1") {
		t.Errorf("a deployment with no service ports accepts the gateway:\n%s", got)
	}
}

func TestGranuleFirewall_ParseGuestNetworks(t *testing.T) {
	nets, err := parseGuestNetworks("172.18.0.2|16|172.18.0.1|||\n10.9.0.4|24||fd00::4|64|\n")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range nets {
		got = append(got, fmt.Sprintf("%s %s %v", n.IP, n.CIDR, n.Gateway))
	}
	want := "172.18.0.2 172.18.0.0/16 172.18.0.1|10.9.0.4 10.9.0.0/24 <nil>|fd00::4 fd00::/64 <nil>"
	if strings.Join(got, "|") != want {
		t.Errorf("parseGuestNetworks = %q, want %q", strings.Join(got, "|"), want)
	}
	// Docker 29's rendering of a container with no IPv6: its unset
	// netip.Addr fields print as "invalid IP" (captured from docker
	// 29.8.1, docs/test-runs/2026-10-01-sandbox-isolation-port-block.md).
	nets, err = parseGuestNetworks("10.240.0.2|24|10.240.0.1|invalid IP|0|invalid IP\n")
	if err != nil {
		t.Fatalf("parseGuestNetworks on docker 29's output: %v", err)
	}
	if len(nets) != 1 || nets[0].IP.String() != "10.240.0.2" || nets[0].Gateway.String() != "10.240.0.1" {
		t.Errorf("parseGuestNetworks on docker 29's output = %+v, want just 10.240.0.2 via 10.240.0.1", nets)
	}
	for _, bad := range []string{"", "\n", "172.18.0.2 16 172.18.0.1", "nope|16||||", "invalid IP|0|invalid IP|invalid IP|0|invalid IP\n"} {
		if _, err := parseGuestNetworks(bad); err == nil {
			t.Errorf("parseGuestNetworks(%q) = nil error, want one", bad)
		}
	}
}

// A failed Acquire's cleanup still removes the VM's host rules when the
// Acquire was interrupted: a cancelled ctx would stop exec.CommandContext
// from running nft at all, leaving a chain that drops traffic from an
// address docker hands to the next container.
func TestGranuleFirewall_DeleteQuietlyCleansUpOnCancelledContext(t *testing.T) {
	var ranOnCancelled bool
	calls := withHostFirewall(t, func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
		if ctx.Err() != nil {
			ranOnCancelled = true
		}
		return nil, nil
	}, fixedNetworks("172.18.0.2|16|172.18.0.1|||\n"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	k := NewGranuleSandboxes(GranuleConfig{StateDir: t.TempDir()})
	k.deleteQuietly(ctx, "g-1-1")

	if len(*calls) == 0 {
		t.Fatal("deleteQuietly ran no nft commands to remove the VM's chain")
	}
	if ranOnCancelled {
		t.Errorf("deleteQuietly ran nft on the caller's cancelled context, so the cleanup would never execute: %q", *calls)
	}
}

func TestGranuleFirewall_FindNFTBinary(t *testing.T) {
	// 1. Test fallback when PATH does not contain nft but fallback paths do:
	t.Run("FallbackLocations", func(t *testing.T) {
		tmpDir := t.TempDir()
		fakeNFT := filepath.Join(tmpDir, "nft")
		if err := os.WriteFile(fakeNFT, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
			t.Fatal(err)
		}

		origFallback := nftFallbackPaths
		defer func() { nftFallbackPaths = origFallback }()
		nftFallbackPaths = []string{fakeNFT}

		// Ensure PATH does not contain the fake NFT
		t.Setenv("PATH", "/nonexistent")

		got, err := findNFTBinary()
		if err != nil {
			t.Fatalf("findNFTBinary() unexpected error: %v", err)
		}
		if got != fakeNFT {
			t.Errorf("findNFTBinary() = %q, want %q", got, fakeNFT)
		}
	})

	// 2. Test lookup failure when neither PATH nor fallback paths contain nft:
	t.Run("NotFound", func(t *testing.T) {
		origFallback := nftFallbackPaths
		defer func() { nftFallbackPaths = origFallback }()
		nftFallbackPaths = []string{"/nonexistent/path/to/nft"}

		t.Setenv("PATH", "/nonexistent")

		_, err := findNFTBinary()
		if err == nil {
			t.Errorf("findNFTBinary() expected error, got nil")
		}
	})

	// 3. Test system nft lookup if present in standard fallback locations:
	t.Run("SystemLookup", func(t *testing.T) {
		p, err := findNFTBinary()
		if err == nil {
			if fi, statErr := os.Stat(p); statErr != nil || fi.IsDir() || (fi.Mode()&0111 == 0) {
				t.Errorf("findNFTBinary() returned non-executable or invalid path: %q", p)
			}
		}
	})
}

// TestCheckAllocatedAddress covers the invariant the address pool buys the
// firewall: the VM holds the address this deployment gave it.
func TestCheckAllocatedAddress(t *testing.T) {
	v4 := func(s string) guestNetwork { return guestNetwork{IP: net.ParseIP(s)} }

	// A deployment that allocates no addresses has nothing to check, which
	// is every deployment before -granule-sandbox-cidr existed.
	if err := checkAllocatedAddress(netip.Addr{}, []guestNetwork{v4("172.17.0.3")}); err != nil {
		t.Errorf("no pool should mean no check, got %v", err)
	}

	allocated := netip.MustParseAddr("10.128.0.32")
	if err := checkAllocatedAddress(allocated, []guestNetwork{v4("10.128.0.32")}); err != nil {
		t.Errorf("the allocated address was held and still rejected: %v", err)
	}
	// Held alongside others -- an IPv6 address on the same network, or a
	// second docker network -- is still held.
	if err := checkAllocatedAddress(allocated, []guestNetwork{
		v4("10.128.0.32"), {IP: net.ParseIP("fd00::2")},
	}); err != nil {
		t.Errorf("the allocated address was held beside an IPv6 one and rejected: %v", err)
	}

	// Docker ignored --ip, or this is a stale container on a recycled
	// address: the deployment no longer knows where this sandbox is.
	err := checkAllocatedAddress(allocated, []guestNetwork{v4("172.17.0.3")})
	if err == nil {
		t.Fatal("a VM holding a different address was accepted")
	}
	for _, want := range []string{"10.128.0.32", "172.17.0.3", "--ip"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	// No addresses at all is a mismatch too, not a pass by vacuity.
	if err := checkAllocatedAddress(allocated, nil); err == nil {
		t.Error("a VM with no addresses was accepted against an allocation")
	}
}

// The host end of a VM container's veth is found by the two ends naming
// each other, and only when it is a bridge port: shared ifindexes in
// some other namespace, a link with no peer, and a non-port veth are not
// taken for it.
func TestGranuleFirewall_HostPortsFor(t *testing.T) {
	sysfs := t.TempDir()
	link := func(name string, index, iflink int, port bool) {
		dir := filepath.Join(sysfs, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for file, v := range map[string]int{"ifindex": index, "iflink": iflink} {
			if err := os.WriteFile(filepath.Join(dir, file), []byte(fmt.Sprintf("%d\n", v)), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if port {
			if err := os.Mkdir(filepath.Join(dir, "brport"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	link("lo", 1, 1, false)
	link("docker0", 4, 4, false)
	link("veth1d9c4eb", 5, 2, true)   // the container's eth0 (ifindex 2)
	link("vethbbbbbbb", 7, 3, true)   // its eth1 (ifindex 3)
	link("vethccccccc", 9, 2, true)   // another container's eth0 -- also ifindex 2
	link("vethnotport", 11, 6, false) // a veth on no bridge

	links, err := parseGuestLinks("eth0 2 5\neth1 3 7\nlo 1 1\ntap0 4 4\nvm0 6 11\nbr0 8 9\n")
	if err != nil {
		t.Fatal(err)
	}
	ports, err := hostPortsFor(sysfs, links)
	if err != nil {
		t.Fatal(err)
	}
	// eth0 and eth1 are this container's; br0's iflink of 9 is
	// vethccccccc's ifindex, but vethccccccc's peer is not ifindex 8, and
	// vm0's peer is no bridge port.
	if got, want := strings.Join(ports, ","), "veth1d9c4eb,vethbbbbbbb"; got != want {
		t.Errorf("hostPortsFor = %s, want %s", got, want)
	}

	for _, bad := range []string{"eth0 2", "eth0 two 5", "eth0 2 5 extra"} {
		if _, err := parseGuestLinks(bad); err == nil {
			t.Errorf("parseGuestLinks(%q) = nil error, want one", bad)
		}
	}
}

// Stale rules from a port this VM now holds, or jumps to this VM's chain,
// are matched field-exactly in `nft -a list` output.
func TestGranuleFirewall_JumpRuleMatchesPorts(t *testing.T) {
	line := strings.Fields(`iifname "veth1d9c4eb" ether type != arp jump vm_other # handle 4`)
	if !jumpRuleMatches(line, "vm_g_t1_1", nil, []string{"veth1d9c4eb"}) {
		t.Error("a rule from this VM's port was not matched")
	}
	if jumpRuleMatches(line, "vm_g_t1_1", nil, []string{"veth1d9c4e"}) {
		t.Error("a rule from a port whose name merely starts with this VM's was matched")
	}
}

// When nft cannot be run on the host (e.g. sudo requires a password because
// sudoers is missing or shadowed), teardown cleanup stops after the first
// failed probe rather than firing 7 more doomed sudo invocations, and
// subsequent NetworkOpen teardowns do not invoke sudo at all until a
// NetworkNone enforcement succeeds.
func TestGranuleFirewall_CleanupStopsOnUnusableNFT(t *testing.T) {
	ctx := context.Background()
	allowNFT := false
	calls := withHostFirewall(t, func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
		if !allowNFT {
			return nil, errors.New("exit status 1 (sudo: a password is required)")
		}
		return nil, nil
	}, fixedNetworks("172.18.0.2|16|172.18.0.1|||\n"))

	k := NewGranuleSandboxes(GranuleConfig{StateDir: t.TempDir()})
	k.cleanupGranuleHostIsolation(ctx, "g-t1-1")
	if len(*calls) != 1 {
		t.Fatalf("first cleanup with unusable nft ran %d nft commands, want 1: %v", len(*calls), *calls)
	}

	// Subsequent teardowns on the same unconfigured host do not invoke sudo at all.
	k.cleanupGranuleHostIsolation(ctx, "g-t2-1")
	if len(*calls) != 1 {
		t.Fatalf("second cleanup with unusable nft ran %d total nft commands, want still 1: %v", len(*calls), *calls)
	}

	// A NetworkNone enforcement still fails closed while nft is unusable.
	if err := k.enforceGranuleHostIsolation(ctx, "g-t3-1", netip.Addr{}); err == nil {
		t.Fatal("enforceGranuleHostIsolation succeeded while nft was unusable")
	}

	// Once host sudoers is configured and enforceGranuleHostIsolation succeeds,
	// teardown cleanup runs again.
	allowNFT = true
	*calls = nil
	if err := k.enforceGranuleHostIsolation(ctx, "g-t3-1", netip.Addr{}); err != nil {
		t.Fatalf("enforceGranuleHostIsolation after enabling nft: %v", err)
	}
	*calls = nil
	k.cleanupGranuleHostIsolation(ctx, "g-t3-1")
	if len(*calls) == 0 {
		t.Fatal("cleanupGranuleHostIsolation ran no nft commands after enforceGranuleHostIsolation succeeded")
	}
}
