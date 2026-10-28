package netshim

import (
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// gkeResolvConf is a GKE pod's resolv.conf: the cluster DNS ClusterIP,
// the pod's search list, and ndots:5.
const gkeResolvConf = `search default.svc.cluster.local svc.cluster.local cluster.local c.proj.internal google.internal
nameserver 34.118.224.10
options ndots:5
`

// kindV6ResolvConf is a pod's resolv.conf on a v6-only kind cluster:
// the kube-dns ClusterIP is a ULA.
const kindV6ResolvConf = `search default.svc.cluster.local svc.cluster.local cluster.local
nameserver fd00:10:96::a
options ndots:5
`

// kindDualResolvConf is a dual-stack pod's, with a cluster DNS address
// of each family, IPv6 first.
const kindDualResolvConf = `search default.svc.cluster.local svc.cluster.local cluster.local
nameserver fd00:10:96::a
nameserver 10.96.0.10
options ndots:5
`

// The identities the DNS tests hand a guest: one of each family mix.
var (
	dnsV4Identity = Identity{
		Iface: "eth0", MAC: net.HardwareAddr{2, 0, 0, 0, 0, 7},
		IP: net.IPv4(10, 28, 0, 7).To4(), Mask: net.CIDRMask(24, 32),
		Gateway: net.IPv4(10, 28, 0, 1).To4(), MTU: 1460,
	}
	dnsV6Identity = Identity{
		Iface: "eth0", MAC: net.HardwareAddr{2, 0, 0, 0, 0, 7}, MTU: 1460,
		IP6: net.ParseIP("fd00:10:244::7"), Prefix6: 64, LinkLocal6: net.ParseIP("fe80::7"),
	}
	dnsDualIdentity = Identity{
		Iface: "eth0", MAC: dnsV4Identity.MAC, MTU: 1460,
		IP: dnsV4Identity.IP, Mask: dnsV4Identity.Mask, Gateway: dnsV4Identity.Gateway,
		IP6: dnsV6Identity.IP6, Prefix6: 64, LinkLocal6: dnsV6Identity.LinkLocal6,
	}
)

// The nameservers a guest is given out of the sandbox's resolv.conf are
// the ones it can route to from a tap spliced onto the sandbox's
// interface: a ClusterIP yes, the namespace's own loopback and the host's
// link-local stubs no, and only in a family the guest has an address in.
func TestGuestResolvers(t *testing.T) {
	tests := []struct {
		name string
		conf string
		id   *Identity // nil for dnsV4Identity
		want string
	}{
		{name: "kubernetes ClusterIP kept", conf: gkeResolvConf, want: "34.118.224.10"},
		{name: "docker embedded resolver dropped", conf: "nameserver 127.0.0.11\noptions ndots:0\n", want: ""},
		{name: "systemd-resolved stub dropped", conf: "nameserver 127.0.0.53\n", want: ""},
		{name: "GCE metadata resolver dropped", conf: "nameserver 169.254.169.254\nsearch c.proj.internal\n", want: ""},
		{name: "IPv6 dropped from an IPv4 guest", conf: "nameserver fd00::53\nnameserver 2001:4860:4860::8888\n", want: ""},
		{name: "IPv6 dropped, IPv4 kept", conf: "nameserver fd00::53\nnameserver 10.0.0.53\n", want: "10.0.0.53"},
		{name: "kind v6-only ClusterIP kept", conf: kindV6ResolvConf, id: &dnsV6Identity, want: "fd00:10:96::a"},
		{name: "IPv4 dropped from an IPv6 guest", conf: kindDualResolvConf, id: &dnsV6Identity, want: "fd00:10:96::a"},
		{name: "dual stack keeps both, in order", conf: kindDualResolvConf, id: &dnsDualIdentity, want: "fd00:10:96::a 10.96.0.10"},
		{name: "kind v6-only ClusterIP dropped from an IPv4 guest", conf: kindV6ResolvConf, want: ""},
		{
			name: "IPv6 loopback, unspecified and link-local dropped, global and ULA kept",
			conf: "nameserver ::1\nnameserver ::\nnameserver fe80::1\nnameserver febf::1\nnameserver 2001:db8::53\nnameserver fd00::53\n",
			id:   &dnsV6Identity,
			want: "2001:db8::53 fd00::53",
		},
		{
			name: "at most maxDNS in all",
			conf: "nameserver fd00::1\nnameserver 10.0.0.53\nnameserver fd00::2\nnameserver fd00::3\n",
			id:   &dnsDualIdentity,
			want: "fd00::1 10.0.0.53 fd00::2",
		},
		{
			name: "at most maxDNS4 IPv4, an IPv6 one after them still kept",
			conf: "nameserver 10.0.0.53\nnameserver 10.0.1.53\nnameserver 10.0.2.53\nnameserver fd00::53\n",
			id:   &dnsDualIdentity,
			want: "10.0.0.53 10.0.1.53 fd00::53",
		},
		{
			name: "unreachable ones skipped, not counted",
			conf: "nameserver 127.0.0.11\nnameserver 10.0.0.53\nnameserver 169.254.169.254\nnameserver 10.0.1.53\n",
			want: "10.0.0.53 10.0.1.53",
		},
		{
			name: "at most maxDNS",
			conf: "nameserver 10.0.0.53\nnameserver 10.0.1.53\nnameserver 10.0.2.53\n",
			want: "10.0.0.53 10.0.1.53",
		},
		{name: "comments and junk ignored", conf: "# nameserver 10.9.9.9\nnameserver not-an-ip\nnameserver 10.0.0.53 # corp\n", want: "10.0.0.53"},
		{name: "empty file", conf: "", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := dnsV4Identity
			if tc.id != nil {
				id = *tc.id
			}
			if got := JoinIPs(GuestResolvers(ParseResolvConf([]byte(tc.conf)), id)); got != tc.want {
				t.Errorf("GuestResolvers() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseResolvConf_SearchAndOptions(t *testing.T) {
	rc := ParseResolvConf([]byte(gkeResolvConf))
	wantSearch := []string{"default.svc.cluster.local", "svc.cluster.local", "cluster.local", "c.proj.internal", "google.internal"}
	if !reflect.DeepEqual(rc.Search, wantSearch) {
		t.Errorf("Search = %q, want %q", rc.Search, wantSearch)
	}
	if !reflect.DeepEqual(rc.Options, []string{"ndots:5"}) {
		t.Errorf("Options = %q, want [ndots:5]", rc.Options)
	}

	// The last search or domain line wins, as in glibc; ndots past what
	// any resolver honours is clamped; other options are not carried.
	rc = ParseResolvConf([]byte("search a.example\ndomain b.example\noptions rotate ndots:99 timeout:1\n"))
	if !reflect.DeepEqual(rc.Search, []string{"b.example"}) {
		t.Errorf("Search = %q, want [b.example]", rc.Search)
	}
	if !reflect.DeepEqual(rc.Options, []string{"ndots:15"}) {
		t.Errorf("Options = %q, want [ndots:15]", rc.Options)
	}
}

// The search list rides the kernel command line, so it is held to the
// limits a guest resolver would have applied anyway, and to names.
func TestGuestSearch(t *testing.T) {
	long := strings.Repeat("a", 100) + ".example"
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{name: "kept", in: []string{"svc.cluster.local", "cluster.local"}, want: []string{"svc.cluster.local", "cluster.local"}},
		{name: "not names dropped", in: []string{"ok.example", "a,b", "x=y", "."}, want: []string{"ok.example"}},
		{name: "at most six", in: []string{"a", "b", "c", "d", "e", "f", "g"}, want: []string{"a", "b", "c", "d", "e", "f"}},
		{name: "at most 256 bytes", in: []string{long, long, long, "c.example"}, want: []string{long, long}},
		{name: "none", in: nil, want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := guestSearch(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("guestSearch(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func withResolvConf(t *testing.T, conf string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "resolv.conf")
	if conf != "" {
		if err := os.WriteFile(path, []byte(conf), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := resolvConfPath
	resolvConfPath = path
	t.Cleanup(func() { resolvConfPath = old })
}

// End to end from NETSHIM_DNS to the guest's kernel parameters: auto
// (and unset) hand the guest the sandbox's own resolver, search list and
// ndots when it can reach them and the fallback for its family alone when
// it cannot; anything named explicitly is used as it is, with the
// sandbox's resolv.conf unread.
func TestResolveDNS(t *testing.T) {
	tests := []struct {
		name string
		// set is whether NETSHIM_DNS is in the environment at all.
		set    bool
		value  string
		conf   string    // "" for no resolv.conf at all
		id     *Identity // nil for dnsV4Identity
		want   string    // DNSFields: the ip= nameservers
		dns    string    // granule.dns=, "" for none
		search string
		opts   string
	}{
		{
			name: "unset in a kubernetes pod", conf: gkeResolvConf, want: ":34.118.224.10",
			search: "granule.dns_search=default.svc.cluster.local,svc.cluster.local,cluster.local,c.proj.internal,google.internal",
			opts:   "granule.dns_options=ndots:5",
		},
		{
			name: "auto in a kubernetes pod", set: true, value: "auto", conf: gkeResolvConf, want: ":34.118.224.10",
			search: "granule.dns_search=default.svc.cluster.local,svc.cluster.local,cluster.local,c.proj.internal,google.internal",
			opts:   "granule.dns_options=ndots:5",
		},
		{name: "corporate resolver, no search", set: true, value: "auto", conf: "nameserver 10.1.2.3\nnameserver 10.1.2.4\n", want: ":10.1.2.3:10.1.2.4"},
		// Docker user-defined network: the embedded resolver is the
		// namespace's loopback, and its search list is no use with a
		// resolver it was not written for.
		{name: "docker embedded resolver falls back", set: true, value: "auto", conf: "nameserver 127.0.0.11\nsearch corp.example\noptions ndots:0\n", want: ":" + FallbackDNS4},
		{name: "GCE metadata resolver falls back", conf: "nameserver 169.254.169.254\nsearch c.proj.internal google.internal\n", want: ":" + FallbackDNS4},
		{name: "IPv6 only falls back", conf: "nameserver fd00::53\n", want: ":" + FallbackDNS4},
		{name: "no resolv.conf falls back", want: ":" + FallbackDNS4},
		{
			name: "kind v6-only pod", conf: kindV6ResolvConf, id: &dnsV6Identity,
			dns:    "granule.dns=fd00:10:96::a",
			search: "granule.dns_search=default.svc.cluster.local,svc.cluster.local,cluster.local",
			opts:   "granule.dns_options=ndots:5",
		},
		{
			// ip= carries the IPv4 half, for a guest image that
			// predates granule.dns=; granule.dns= the whole list.
			name: "kind dual-stack pod", conf: kindDualResolvConf, id: &dnsDualIdentity,
			want:   ":10.96.0.10",
			dns:    "granule.dns=fd00:10:96::a,10.96.0.10",
			search: "granule.dns_search=default.svc.cluster.local,svc.cluster.local,cluster.local",
			opts:   "granule.dns_options=ndots:5",
		},
		{
			// docker's embedded resolver on a v6-only network: the
			// guest has no IPv4 to reach 8.8.8.8 over.
			name: "v6-only docker falls back to IPv6", conf: "nameserver 127.0.0.11\n", id: &dnsV6Identity,
			dns: "granule.dns=" + FallbackDNS6,
		},
		{name: "v6-only, no resolv.conf, falls back to IPv6", id: &dnsV6Identity, dns: "granule.dns=" + FallbackDNS6},
		{name: "dual stack falls back to IPv4", conf: "nameserver 127.0.0.11\n", id: &dnsDualIdentity, want: ":" + FallbackDNS4},
		{name: "explicit IPv6", set: true, value: "2001:db8::53", conf: gkeResolvConf, dns: "granule.dns=2001:db8::53"},
		{
			// Named, so used as named: there is no ip= to put the
			// IPv4 one on, and granule.dns= carries both.
			name: "explicit mixed on a v6-only guest", set: true, value: "10.0.0.53,2001:db8::53", id: &dnsV6Identity,
			want: ":10.0.0.53", dns: "granule.dns=10.0.0.53,2001:db8::53",
		},
		{name: "explicit wins", set: true, value: "10.0.0.53", conf: gkeResolvConf, want: ":10.0.0.53"},
		{name: "old specs' explicit 8.8.8.8 kept", set: true, value: "8.8.8.8", conf: gkeResolvConf, want: ":8.8.8.8"},
		{name: "explicitly empty leaves the image alone", set: true, value: "", conf: gkeResolvConf, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envVM, "web")
			if tc.set {
				t.Setenv(envDNS, tc.value)
			} else {
				os.Unsetenv(envDNS)
			}
			withResolvConf(t, tc.conf)
			cfg, err := FromEnv()
			if err != nil {
				t.Fatalf("FromEnv() error = %v", err)
			}
			id := dnsV4Identity
			if tc.id != nil {
				id = *tc.id
			}
			cfg = cfg.ResolveDNS(id)
			if got := DNSFields(cfg.DNS); got != tc.want {
				t.Errorf("DNSFields(cfg.DNS) = %q, want %q", got, tc.want)
			}
			g := GuestConfig(cfg, id)
			if g.DNSParam != tc.dns {
				t.Errorf("DNSParam = %q, want %q", g.DNSParam, tc.dns)
			}
			if g.DNSSearchParam != tc.search {
				t.Errorf("DNSSearchParam = %q, want %q", g.DNSSearchParam, tc.search)
			}
			if g.DNSOptionsParam != tc.opts {
				t.Errorf("DNSOptionsParam = %q, want %q", g.DNSOptionsParam, tc.opts)
			}
			cmdline := WithGuestParams("console=ttyS0", g)
			for _, p := range []string{tc.dns, tc.search, tc.opts} {
				if p != "" && !strings.Contains(cmdline, " "+p) {
					t.Errorf("cmdline %q is missing %q", cmdline, p)
				}
			}
		})
	}
}

// An operator's own ip= takes the derived DNS parameters away with the
// rest: they complete a resolv.conf for nameservers that ip= no longer
// carries.
func TestWithGuestParams_OperatorIPDropsDNSParams(t *testing.T) {
	g := Guest{
		IPParam:         "ip=10.0.0.7::10.0.0.1:255.255.255.0::eth0:off:10.0.0.53",
		DNSSearchParam:  "granule.dns_search=cluster.local",
		DNSOptionsParam: "granule.dns_options=ndots:5",
	}
	const operator = "console=ttyS0 ip=192.168.1.5::192.168.1.1:255.255.255.0::eth0:off"
	if got := WithGuestParams(operator, g); got != operator {
		t.Errorf("WithGuestParams() = %q, want %q", got, operator)
	}
}

func TestValidateDNS(t *testing.T) {
	for _, ok := range []string{"auto", " auto ", "", "8.8.8.8", "10.0.0.53,10.0.1.53", "fd00::53", "fd00:10:96::a,10.96.0.10"} {
		if err := ValidateDNS(ok); err != nil {
			t.Errorf("ValidateDNS(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"Auto", "auto,8.8.8.8", "nameserver", "10.0.0.1,10.0.0.2,10.0.0.3", "fd00::1,fd00::2,fd00::3,10.0.0.1"} {
		if err := ValidateDNS(bad); err == nil {
			t.Errorf("ValidateDNS(%q) = nil, want an error", bad)
		}
	}
}

// The fallback is the guest's family's: IPv4 whenever it has IPv4, so a
// dual-stack guest gets what every guest got before, and IPv6 only for a
// v6-only one, which could not route to 8.8.8.8.
func TestFallbackDNS(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   Identity
		want string
	}{
		{"IPv4 only", dnsV4Identity, FallbackDNS4},
		{"dual stack", dnsDualIdentity, FallbackDNS4},
		{"IPv6 only", dnsV6Identity, FallbackDNS6},
		{"no address at all", Identity{}, FallbackDNS4},
	} {
		if got := FallbackDNS(tc.id).String(); got != tc.want {
			t.Errorf("%s: FallbackDNS() = %s, want %s", tc.name, got, tc.want)
		}
	}
}

// granule.dns= is there for what ip= cannot say, and only then: an IPv4
// guest's nameservers are on ip= already, and its command line stays
// what it was before granule.dns= existed.
func TestDNSParam(t *testing.T) {
	v4 := []net.IP{net.IPv4(10, 0, 0, 53).To4(), net.IPv4(10, 0, 1, 53).To4()}
	mixed := []net.IP{net.ParseIP("fd00::53"), net.IPv4(10, 0, 0, 53).To4()}
	for _, tc := range []struct {
		name string
		dns  []net.IP
		onIP bool
		want string
	}{
		{"none", nil, true, ""},
		{"IPv4 on ip=", v4, true, ""},
		{"IPv4 with no ip=", v4, false, "granule.dns=10.0.0.53,10.0.1.53"},
		{"mixed on ip=", mixed, true, "granule.dns=fd00::53,10.0.0.53"},
	} {
		if got := dnsParam(tc.dns, tc.onIP); got != tc.want {
			t.Errorf("%s: dnsParam() = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := DNSFields(mixed); got != ":10.0.0.53" {
		t.Errorf("DNSFields(%v) = %q, want only the IPv4 one", mixed, got)
	}
}
