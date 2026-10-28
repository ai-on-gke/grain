package netshim

import (
	"net"
	"os"
	"strings"
	"testing"
)

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{envVM, envBridge, envControlCIDR, envExternalIface, envTapOwner, envIPv6} {
		os.Unsetenv(k)
	}
}

func TestFromEnv_DefaultVMName(t *testing.T) {
	clearEnv(t)
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VM != "vm" || cfg.TapName() != "tap-vm" {
		t.Fatalf("default VM = %q, tap = %q", cfg.VM, cfg.TapName())
	}
}

func TestFromEnv_Defaults(t *testing.T) {
	clearEnv(t)
	t.Setenv(envVM, "web")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}

	if cfg.VM != "web" {
		t.Errorf("VM = %q, want web", cfg.VM)
	}
	if got, want := cfg.TapName(), "tap-web"; got != want {
		t.Errorf("TapName() = %q, want %q", got, want)
	}
	if got, want := cfg.ControlTapName(), "ctl-web"; got != want {
		t.Errorf("ControlTapName() = %q, want %q", got, want)
	}
	if cfg.Bridge != defaultBridge {
		t.Errorf("Bridge = %q, want default %q", cfg.Bridge, defaultBridge)
	}
	if cfg.ExternalIface != defaultExternalIface {
		t.Errorf("ExternalIface = %q, want default %q", cfg.ExternalIface, defaultExternalIface)
	}
	if got, want := cfg.ControlAddr.String(), "169.254.100.1"; got != want {
		t.Errorf("ControlAddr = %q, want %q", got, want)
	}
	if cfg.ControlNet == nil {
		t.Errorf("ControlNet = nil, want the default control subnet")
	}
}

// TestFromEnv_ControlDisabled covers the explicit opt-out: an empty
// NETSHIM_CONTROL_CIDR means no control link at all, as opposed to an
// unset one meaning "use the default".
func TestFromEnv_ControlDisabled(t *testing.T) {
	clearEnv(t)
	t.Setenv(envVM, "web")
	t.Setenv(envControlCIDR, "")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.ControlNet != nil || cfg.ControlAddr != nil {
		t.Errorf("control link = %v/%v, want none", cfg.ControlAddr, cfg.ControlNet)
	}
}

func TestFromEnv_Errors(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		// tapPrefix ("tap-") + 12 chars = 16, one over the 15-byte
		// interface name limit.
		{"name too long", map[string]string{envVM: "abcdefghijkl"}},
		{"bad control CIDR", map[string]string{envVM: "web", envControlCIDR: "nonsense"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if _, err := FromEnv(); err == nil {
				t.Errorf("FromEnv() error = nil, want an error")
			}
		})
	}
}

// TestEnabled covers the gate the VM container uses to tell a
// netshim-managed sandbox from a bare "granule run", which has no tap to
// attach to and no identity to take over.
func TestEnabled(t *testing.T) {
	clearEnv(t)
	if Enabled() {
		t.Errorf("Enabled() = true with %s unset, want false", envVM)
	}
	t.Setenv(envVM, "web")
	if !Enabled() {
		t.Errorf("Enabled() = false with %s set, want true", envVM)
	}
}

func TestGuestConfig(t *testing.T) {
	clearEnv(t)
	t.Setenv(envVM, "web")
	// Docker's embedded resolver, which the guest cannot reach.
	withResolvConf(t, "nameserver 127.0.0.11\n")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	mac, err := net.ParseMAC("02:42:ac:11:00:02")
	if err != nil {
		t.Fatalf("ParseMAC: %v", err)
	}
	id := Identity{
		Iface:   "eth0",
		MAC:     mac,
		IP:      net.IPv4(172, 17, 0, 2).To4(),
		Mask:    net.CIDRMask(16, 32),
		Gateway: net.IPv4(172, 17, 0, 1).To4(),
		MTU:     1450,
	}
	cfg = cfg.ResolveDNS(id)

	g := GuestConfig(cfg, id)

	// The guest must present the MAC the runtime assigned, or the
	// segment sees a second endpoint appear behind an authorized port.
	want := "tap=tap-web,mac=02:42:ac:11:00:02,mtu=1450"
	if len(g.Nets) != 2 {
		t.Fatalf("Nets = %q, want the spliced NIC plus the control NIC", g.Nets)
	}
	if g.Nets[0] != want {
		t.Errorf("Nets[0] = %q, want %q", g.Nets[0], want)
	}
	if g.Nets[1] != "tap=ctl-web" {
		t.Errorf("Nets[1] = %q, want tap=ctl-web", g.Nets[1])
	}
	// The trailing field is dns0: NETSHIM_DNS is unset here, so DNS is
	// auto, and ResolveDNS fell back to FallbackDNS because docker's
	// embedded resolver is the namespace's own loopback. See
	// TestResolveDNS for the rest of that.
	if wantIP := "ip=172.17.0.2::172.17.0.1:255.255.0.0::eth0:off:8.8.8.8"; g.IPParam != wantIP {
		t.Errorf("IPParam = %q, want %q", g.IPParam, wantIP)
	}
	if g.ControlIP != "169.254.100.2" {
		t.Errorf("ControlIP = %q, want 169.254.100.2", g.ControlIP)
	}
}

// TestGuestConfig_TapMTU covers the one case where mtu= is left off: a
// tap that already has the right MTU. Passing mtu= then would only make
// cloud-hypervisor call SIOCSIFMTU, which a VM container without
// NET_ADMIN is not allowed to do, on a tap netshim already set up.
func TestGuestConfig_TapMTU(t *testing.T) {
	mac, err := net.ParseMAC("02:42:ac:11:00:02")
	if err != nil {
		t.Fatalf("ParseMAC: %v", err)
	}
	cases := []struct {
		name   string
		tapMTU int
		want   string
	}{
		{"tap already matches", 1450, "tap=tap-web,mac=02:42:ac:11:00:02"},
		{"tap differs", 1500, "tap=tap-web,mac=02:42:ac:11:00:02,mtu=1450"},
		{"no tap yet", 0, "tap=tap-web,mac=02:42:ac:11:00:02,mtu=1450"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := Identity{
				Iface:  "eth0",
				MAC:    mac,
				IP:     net.IPv4(172, 17, 0, 2).To4(),
				Mask:   net.CIDRMask(16, 32),
				MTU:    1450,
				TapMTU: tc.tapMTU,
			}
			g := GuestConfig(Config{VM: "web", ExternalIface: "eth0"}, id)
			if g.Nets[0] != tc.want {
				t.Errorf("Nets[0] = %q, want %q", g.Nets[0], tc.want)
			}
			// The guest is told its MTU on the command line either way.
			if g.MTUParam != "granule.mtu=1450" {
				t.Errorf("MTUParam = %q, want granule.mtu=1450", g.MTUParam)
			}
		})
	}
}

func TestWithIPParam(t *testing.T) {
	const derived = "ip=172.17.0.2::172.17.0.1:255.255.0.0::eth0:off"
	tests := []struct {
		name    string
		cmdline string
		want    string
	}{
		{"appended", "console=ttyS0 root=/dev/vda ro", "console=ttyS0 root=/dev/vda ro " + derived},
		{"empty cmdline", "", derived},
		{
			// An explicit ip= is an operator overriding the derived
			// identity on purpose, so it must survive untouched.
			"operator override wins",
			"console=ttyS0 ip=10.0.0.5::10.0.0.1:255.255.255.0::eth0:off",
			"console=ttyS0 ip=10.0.0.5::10.0.0.1:255.255.255.0::eth0:off",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := WithIPParam(tc.cmdline, derived); got != tc.want {
				t.Errorf("WithIPParam() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestControlGuestIP(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   net.IP
		want string
	}{
		{"standard IP", net.ParseIP("169.254.100.1"), "169.254.100.2"},
		{"overflow last byte", net.ParseIP("169.254.100.255"), "169.254.101.0"},
		{"nil input", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ControlGuestIP(tc.in)
			if tc.want == "" {
				if got != nil {
					t.Errorf("ControlGuestIP(%v) = %v, want nil", tc.in, got)
				}
				return
			}
			if got == nil || got.String() != tc.want {
				t.Errorf("ControlGuestIP(%v) = %v, want %s", tc.in, got, tc.want)
			}
		})
	}
}

// The nameservers reach the guest on its own ip= parameter, which is what
// makes a deployment able to name the resolver its network actually has
// -- rather than every guest booting with whatever /etc/resolv.conf the
// machine that built the image happened to have, on an address nothing in
// the guest can route to.
func TestFromEnv_TapOwner(t *testing.T) {
	cases := []struct {
		raw      string
		uid, gid uint32
		wantErr  bool
	}{
		{"", 0, 0, false},
		{"65534:65534", 65534, 65534, false},
		{" 1000:2000 ", 1000, 2000, false},
		{"65534", 0, 0, true},
		{"nobody:nogroup", 0, 0, true},
		{"1000:", 0, 0, true},
		{"-1:0", 0, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(envTapOwner, tc.raw)
			cfg, err := FromEnv()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("FromEnv() with %s=%q: error = nil, want one", envTapOwner, tc.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("FromEnv() error = %v", err)
			}
			if cfg.TapUID != tc.uid || cfg.TapGID != tc.gid {
				t.Errorf("tap owner = %d:%d, want %d:%d", cfg.TapUID, cfg.TapGID, tc.uid, tc.gid)
			}
		})
	}
}

func TestFromEnv_DNS(t *testing.T) {
	tests := []struct {
		name string
		// set is whether NETSHIM_DNS is in the environment at all, which
		// is a different thing from it being empty.
		set   bool
		value string
		want  string
		auto  bool
	}{
		// Auto is resolved later, by ResolveDNS, inside the sandbox.
		{name: "unset means the default, auto", auto: true},
		{name: "auto", set: true, value: "auto", auto: true},
		{name: "one", set: true, value: "10.0.0.53", want: ":10.0.0.53"},
		{name: "two", set: true, value: "10.0.0.53,10.0.1.53", want: ":10.0.0.53:10.0.1.53"},
		// Explicitly empty is the opt-out, the same shape
		// NETSHIM_CONTROL_CIDR has: the guest keeps whatever resolver its
		// image ships with, and the parameter stays the length it has
		// always been.
		{name: "explicitly empty", set: true, value: "", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envVM, "web")
			if tc.set {
				t.Setenv(envDNS, tc.value)
			}
			cfg, err := FromEnv()
			if err != nil {
				t.Fatalf("FromEnv() error = %v", err)
			}
			if got := DNSFields(cfg.DNS); got != tc.want {
				t.Errorf("DNSFields(cfg.DNS) = %q, want %q", got, tc.want)
			}
			if cfg.DNSAuto != tc.auto {
				t.Errorf("DNSAuto = %v, want %v", cfg.DNSAuto, tc.auto)
			}
		})
	}
}

func TestParseDNS(t *testing.T) {
	for spec, want := range map[string]string{
		"2001:4860:4860::8888":         "2001:4860:4860::8888",
		"fd00:10:96::a, 10.96.0.10":    "fd00:10:96::a 10.96.0.10",
		"10.0.0.53,fd00::53,10.0.1.53": "10.0.0.53 fd00::53 10.0.1.53",
		"fd00::1,fd00::2,fd00::3":      "fd00::1 fd00::2 fd00::3",
		"::ffff:10.0.0.53":             "10.0.0.53",
		" , ":                          "",
	} {
		dns, err := ParseDNS(spec)
		if err != nil {
			t.Errorf("ParseDNS(%q) error = %v", spec, err)
			continue
		}
		if got := JoinIPs(dns); got != want {
			t.Errorf("ParseDNS(%q) = %q, want %q", spec, got, want)
		}
	}
}

func TestParseDNS_Rejects(t *testing.T) {
	// A fourth nameserver is past what a guest resolver reads (MAXNS),
	// and a third IPv4 one has nowhere to go on ip=, which has two
	// fields; a name is no address at all -- all refused rather than
	// quietly dropped, since the guest is where the mistake would
	// otherwise surface, as lookups that hang.
	for _, spec := range []string{"nameserver", "8.8.8.8,1.1.1.1,9.9.9.9", "fd00::1,fd00::2,fd00::3,fd00::4", "fe80::1%eth0"} {
		if _, err := ParseDNS(spec); err == nil {
			t.Errorf("ParseDNS(%q) = nil error, want one", spec)
		}
	}
}

func TestFromEnv_IPv6(t *testing.T) {
	for _, tc := range []struct {
		env     string
		set     bool
		want    string
		enabled bool
	}{
		{set: false, want: IPv6Auto, enabled: true},
		{env: "", set: true, want: IPv6Auto, enabled: true},
		{env: "auto", set: true, want: IPv6Auto, enabled: true},
		{env: "off", set: true, want: IPv6Off, enabled: false},
		{env: " off ", set: true, want: IPv6Off, enabled: false},
	} {
		clearEnv(t)
		if tc.set {
			t.Setenv(envIPv6, tc.env)
		}
		cfg, err := FromEnv()
		if err != nil {
			t.Fatalf("%s=%q: FromEnv() error = %v", envIPv6, tc.env, err)
		}
		if cfg.IPv6 != tc.want || cfg.IPv6Enabled() != tc.enabled {
			t.Errorf("%s=%q: IPv6 = %q (enabled %v), want %q (enabled %v)",
				envIPv6, tc.env, cfg.IPv6, cfg.IPv6Enabled(), tc.want, tc.enabled)
		}
	}

	clearEnv(t)
	t.Setenv(envIPv6, "on")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), envIPv6) {
		t.Errorf("%s=on: FromEnv() error = %v, want one naming %s", envIPv6, err, envIPv6)
	}
	clearEnv(t)
}
