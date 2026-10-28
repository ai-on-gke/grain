package netshim

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// guestIP6Script is what applies granule.ip6=: the IPv6 half of the
// identity, which no kernel parameter of the kernel's own can carry. Like
// the route script it gets one run per boot, and a guest it gets wrong has
// no IPv6 -- or, on a v6-only pod, no network at all.
const guestIP6Script = "../../deploy/guest-image/overlay-common/usr/local/libexec/granule-configure-ip6"

func assertCalls(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("ip calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestGuestScriptAppliesCarriedRoutes6 runs granule-configure-routes'
// IPv6 pass: the same two passes as IPv4 with "ip -6", and a prune that
// maps ::/0 to default, strips /128, always keeps fe80::/64 and leaves
// the kernel's typed routes (multicast ff00::/8) alone.
func TestGuestScriptAppliesCarriedRoutes6(t *testing.T) {
	const v4 = "ip=10.244.0.5::10.244.0.1:255.255.255.0::eth0:off " +
		"granule.routes=10.244.0.1/32,0.0.0.0/0@10.244.0.1"
	const v6 = "granule.ip6=fd00:10:244::5/64,fe80::42:acff:fe11:5 " +
		"granule.routes6=fe80::1/128,fd00:10:244::/56@fe80::1,::/0@fe80::1"
	const v4Table = "FAKEIP_ROUTES=10.244.0.1 dev eth0 scope link\n" +
		"10.244.0.0/24 dev eth0 proto kernel scope link src 10.244.0.5\n" +
		"default via 10.244.0.1 dev eth0"
	const v6Table = "FAKEIP_ROUTES6=fe80::1 dev eth0 metric 1024 pref medium\n" +
		"fd00:10:244::/64 dev eth0 proto kernel metric 256 pref medium\n" +
		"fd00:10:244::/56 via fe80::1 dev eth0 metric 1024 pref medium\n" +
		"fe80::/64 dev eth0 proto kernel metric 256 pref medium\n" +
		"multicast ff00::/8 dev eth0 proto kernel metric 256 pref medium\n" +
		"default via fe80::1 dev eth0 metric 1024 pref medium"

	v4Calls := []string{
		"route replace 10.244.0.1/32 dev eth0 scope link",
		"route replace 0.0.0.0/0 via 10.244.0.1 dev eth0",
	}
	v4Prune := []string{
		"-4 route show dev eth0",
		"route del 10.244.0.0/24 dev eth0",
	}
	v6Calls := []string{
		"-6 route replace fe80::1/128 dev eth0 scope link",
		"-6 route replace fd00:10:244::/56 via fe80::1 dev eth0",
		"-6 route replace ::/0 via fe80::1 dev eth0",
	}
	v6Prune := []string{
		"-6 route show dev eth0",
		"-6 route del fd00:10:244::/64 dev eth0",
	}
	cat := func(parts ...[]string) []string {
		var out []string
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}

	for _, tc := range []struct {
		name    string
		cmdline string
		env     []string
		want    []string
		wantErr bool
	}{
		{
			// Each family's table ends up exactly the carried one: the
			// on-link /64 the address implies goes, as v4's /24 does,
			// and the kernel's fe80::/64 stays.
			name:    "dual stack",
			cmdline: "console=ttyS0 " + v4 + " " + v6,
			env:     []string{v4Table, v6Table},
			want:    cat([]string{"link show dev eth0"}, v4Calls, v4Prune, v6Calls, v6Prune),
		},
		{
			// No ip= at all, so the device is eth0 by default.
			name:    "v6 only",
			cmdline: "console=ttyS0 " + v6,
			env:     []string{v6Table},
			want:    cat([]string{"link show dev eth0"}, v6Calls, v6Prune),
		},
		{
			// An IPv6 route that will not install skips the IPv6 prune
			// and fails the unit -- and leaves the IPv4 prune alone.
			name:    "a v6 failure does not block the v4 prune",
			cmdline: "console=ttyS0 " + v4 + " " + v6,
			env:     []string{v4Table, v6Table, "FAKEIP_FAIL_REPLACE=::/0"},
			want:    cat([]string{"link show dev eth0"}, v4Calls, v4Prune, v6Calls),
			wantErr: true,
		},
		{
			name:    "a v4 failure does not block the v6 prune",
			cmdline: "console=ttyS0 " + v4 + " " + v6,
			env:     []string{v4Table, v6Table, "FAKEIP_FAIL_REPLACE=0.0.0.0/0"},
			want:    cat([]string{"link show dev eth0"}, v4Calls, v6Calls, v6Prune),
			wantErr: true,
		},
		{
			// A /32 in IPv6 is a prefix like any other, not a host
			// route to strip.
			name:    "a v6 /32 is not a host route",
			cmdline: "console=ttyS0 granule.routes6=2001:db8::/32",
			env:     []string{"FAKEIP_ROUTES6=2001:db8::/32 dev eth0 metric 1024 pref medium"},
			want: []string{
				"link show dev eth0",
				"-6 route replace 2001:db8::/32 dev eth0 scope link",
				"-6 route show dev eth0",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runGuestScript(t, tc.cmdline, tc.env...)
			if (err != nil) != tc.wantErr {
				t.Fatalf("granule-configure-routes: %v, wantErr %v", err, tc.wantErr)
			}
			assertCalls(t, got, tc.want)
		})
	}
}

type ip6Run struct {
	calls  []string
	sysctl map[string]string // knob -> value written under conf/<dev>
	out    string
	err    error
}

// runIP6Script runs granule-configure-ip6 with the route tests' fake ip,
// a fake /proc/sys and, unless noIPv6, a fake /proc/net/if_inet6.
func runIP6Script(t *testing.T, cmdline string, noIPv6 bool, env ...string) ip6Run {
	t.Helper()

	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	conf := filepath.Join(dir, "sys", "net", "ipv6", "conf")
	for _, d := range []string{bin, filepath.Join(conf, "eth0"), filepath.Join(conf, "eth1")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "ip"), []byte(fakeIP), 0o755); err != nil {
		t.Fatal(err)
	}
	cmdlinePath := filepath.Join(dir, "cmdline")
	if err := os.WriteFile(cmdlinePath, []byte(cmdline+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ifInet6 := filepath.Join(dir, "if_inet6")
	if !noIPv6 {
		if err := os.WriteFile(ifInet6, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	logPath := filepath.Join(dir, "ip.log")

	cmd := exec.Command("sh", guestIP6Script)
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GRANULE_CMDLINE_FILE="+cmdlinePath,
		"GRANULE_IF_INET6="+ifInet6,
		"GRANULE_SYSCTL_DIR="+filepath.Join(dir, "sys"),
		"GRANULE_NET_DEVICE_TIMEOUT=0",
		"FAKEIP_LOG="+logPath,
	)
	cmd.Env = append(cmd.Env, env...)
	out, runErr := cmd.CombinedOutput()
	t.Logf("granule-configure-ip6 said:\n%s", out)

	run := ip6Run{out: string(out), err: runErr, sysctl: map[string]string{}}
	if logged, err := os.ReadFile(logPath); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(logged)), "\n") {
			if line != "" {
				run.calls = append(run.calls, line)
			}
		}
	}
	for _, dev := range []string{"eth0", "eth1"} {
		entries, _ := os.ReadDir(filepath.Join(conf, dev))
		for _, e := range entries {
			v, _ := os.ReadFile(filepath.Join(conf, dev, e.Name()))
			run.sysctl[dev+"/"+e.Name()] = strings.TrimSpace(string(v))
		}
	}
	return run
}

func TestGuestIP6Script(t *testing.T) {
	const ip6 = "granule.ip6=fd00:10:244::5/64,fe80::42:acff:fe11:5"
	noRA := map[string]string{"eth0/accept_ra": "0", "eth0/autoconf": "0", "eth0/use_tempaddr": "0"}

	for _, tc := range []struct {
		name       string
		cmdline    string
		noIPv6     bool
		env        []string
		want       []string
		wantSysctl map[string]string
		wantErr    bool
		wantOut    string
	}{
		{
			// The order is the point: no address of the kernel's own
			// (addrgenmode, then the flush for the one "ip=" already
			// got generated), the pod's link-local, then its global
			// address, both without DAD, then up.
			name:    "dual stack",
			cmdline: "console=ttyS0 ip=10.244.0.5::10.244.0.1:255.255.255.0::eth0:off " + ip6,
			want: []string{
				"link show dev eth0",
				"link set dev eth0 addrgenmode none",
				"-6 addr flush dev eth0 scope link",
				"-6 addr replace fe80::42:acff:fe11:5/64 dev eth0 nodad",
				"-6 addr replace fd00:10:244::5/64 dev eth0 nodad",
				"link set dev eth0 up",
			},
			wantSysctl: noRA,
		},
		{
			// No ip= to name the device, so it is eth0 -- and nothing
			// but this brings it up.
			name:    "v6 only",
			cmdline: "console=ttyS0 " + ip6,
			want: []string{
				"link show dev eth0",
				"link set dev eth0 addrgenmode none",
				"-6 addr flush dev eth0 scope link",
				"-6 addr replace fe80::42:acff:fe11:5/64 dev eth0 nodad",
				"-6 addr replace fd00:10:244::5/64 dev eth0 nodad",
				"link set dev eth0 up",
			},
			wantSysctl: noRA,
		},
		{
			// With granule.routes6= saying what is on-link, the global
			// address installs no route of its own: the kernel's
			// metric-256 on-link /64 would outrank the carried
			// "fd00:10:244::/64 via fd00:10:244::1" on a point-to-point
			// CNI, and the guest would solicit neighbours nobody answers.
			name:    "carried routes: no prefix route of the address's own",
			cmdline: "console=ttyS0 " + ip6 + " granule.routes6=fd00:10:244::1/128,fd00:10:244::/64@fd00:10:244::1",
			want: []string{
				"link show dev eth0",
				"link set dev eth0 addrgenmode none",
				"-6 addr flush dev eth0 scope link",
				"-6 addr replace fe80::42:acff:fe11:5/64 dev eth0 nodad",
				"-6 addr replace fd00:10:244::5/64 dev eth0 nodad noprefixroute",
				"link set dev eth0 up",
			},
			wantSysctl: noRA,
		},
		{
			name:    "the device ip= names",
			cmdline: "ip=10.0.0.5::10.0.0.1:255.255.255.0::eth1:off granule.ip6=2001:db8::5/64",
			want: []string{
				"link show dev eth1",
				"link set dev eth1 addrgenmode none",
				"-6 addr flush dev eth1 scope link",
				"-6 addr replace 2001:db8::5/64 dev eth1 nodad",
				"link set dev eth1 up",
			},
			wantSysctl: map[string]string{"eth1/accept_ra": "0", "eth1/autoconf": "0", "eth1/use_tempaddr": "0"},
		},
		{
			name:       "no granule.ip6=",
			cmdline:    "console=ttyS0 ip=10.244.0.5::10.244.0.1:255.255.255.0::eth0:off",
			wantSysctl: map[string]string{},
		},
		{
			// A kernel without IPv6: one line naming the address it
			// cannot take, and success, so the IPv4 half still counts.
			name:       "a kernel with no IPv6",
			cmdline:    "console=ttyS0 " + ip6,
			noIPv6:     true,
			wantSysctl: map[string]string{},
			wantOut:    "granule-configure-ip6: this guest's kernel has no IPv6, so it cannot take over fd00:10:244::5/64",
		},
		{
			name:       "no device to configure",
			cmdline:    "console=ttyS0 " + ip6,
			env:        []string{"FAKEIP_NO_LINK=1"},
			want:       []string{"link show dev eth0"},
			wantSysctl: map[string]string{},
			wantErr:    true,
		},
		{
			name:    "a device that has not been probed yet",
			cmdline: "console=ttyS0 granule.ip6=2001:db8::5/64",
			env:     []string{"FAKEIP_LINK_AFTER=1", "GRANULE_NET_DEVICE_TIMEOUT=30"},
			want: []string{
				"link show dev eth0",
				"link show dev eth0",
				"link set dev eth0 addrgenmode none",
				"-6 addr flush dev eth0 scope link",
				"-6 addr replace 2001:db8::5/64 dev eth0 nodad",
				"link set dev eth0 up",
			},
			wantSysctl: noRA,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := runIP6Script(t, tc.cmdline, tc.noIPv6, tc.env...)
			if (run.err != nil) != tc.wantErr {
				t.Fatalf("granule-configure-ip6: %v, wantErr %v", run.err, tc.wantErr)
			}
			assertCalls(t, run.calls, tc.want)
			if len(run.sysctl) != len(tc.wantSysctl) {
				t.Errorf("sysctls written: %v, want %v", run.sysctl, tc.wantSysctl)
			}
			for k, v := range tc.wantSysctl {
				if run.sysctl[k] != v {
					t.Errorf("%s = %q, want %q", k, run.sysctl[k], v)
				}
			}
			if tc.wantOut != "" {
				if lines := strings.Split(strings.TrimSpace(run.out), "\n"); len(lines) != 1 || !strings.HasPrefix(lines[0], tc.wantOut) {
					t.Errorf("output = %q, want one line starting %q", run.out, tc.wantOut)
				}
			}
		})
	}
}
