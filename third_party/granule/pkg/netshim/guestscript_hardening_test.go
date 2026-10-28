package netshim

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// guestDNSScript writes the guest's resolv.conf from the nameservers on
// the kernel command line, as root, on every boot.
const guestDNSScript = "../../deploy/guest-image/overlay-common/usr/local/libexec/granule-configure-dns"

// The routes are split on commas and nothing else: a route entry that
// happens to be a glob must reach ip as written, not as the names of
// whatever files sit in the script's working directory (this package's
// own directory, here, which has plenty to match).
func TestGuestScriptDoesNotGlobTheRoutes(t *testing.T) {
	got, err := runGuestScript(t, "console=ttyS0 ip=10.244.0.5::10.244.0.1:255.255.255.0::eth0:off granule.routes=*",
		"FAKEIP_FAIL_REPLACE=*")
	if err == nil {
		t.Fatalf("a route ip refused was reported as installed")
	}
	want := "route replace * dev eth0 scope link"
	for _, call := range got {
		if call == want {
			return
		}
	}
	t.Fatalf("ip calls = %q, want one to be %q", got, want)
}

func runDNSScript(t *testing.T, cmdline string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	cmdlinePath := filepath.Join(dir, "cmdline")
	if err := os.WriteFile(cmdlinePath, []byte(cmdline+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolv := filepath.Join(dir, "resolv.conf")
	cmd := exec.Command("sh", guestDNSScript)
	cmd.Env = append(os.Environ(),
		"GRANULE_CMDLINE_FILE="+cmdlinePath,
		"GRANULE_RESOLV_CONF="+resolv,
	)
	out, err := cmd.CombinedOutput()
	t.Logf("granule-configure-dns said:\n%s", out)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(resolv)
	if os.IsNotExist(err) {
		return "", nil
	}
	return string(data), err
}

func TestGuestDNSScriptWritesTheNameserversItWasGiven(t *testing.T) {
	got, err := runDNSScript(t, "console=ttyS0 ip=10.0.0.5::10.0.0.1:255.255.255.0::eth0:off:10.0.0.53:0.0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if got != "nameserver 10.0.0.53\n" {
		t.Fatalf("resolv.conf = %q", got)
	}
}

// What the file holds is what every lookup in the guest believes, so a
// field that is not an address -- a resolv.conf keyword smuggled in where
// a nameserver goes -- is left out rather than written down.
func TestGuestDNSScriptWritesOnlyAddresses(t *testing.T) {
	got, err := runDNSScript(t, "console=ttyS0 ip=10.0.0.5::10.0.0.1:255.255.255.0::eth0:off:options\tndots:15:10.0.0.53")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "options") || strings.Contains(got, "ndots") {
		t.Fatalf("resolv.conf took a field that is not an address: %q", got)
	}
	got, err = runDNSScript(t, "console=ttyS0 ip=10.0.0.5::10.0.0.1:255.255.255.0::eth0:off:search,evil.example:10.0.0.53")
	if err != nil {
		t.Fatal(err)
	}
	if got != "nameserver 10.0.0.53\n" {
		t.Fatalf("resolv.conf = %q, want only the address", got)
	}
}

// A resolver read off a Kubernetes pod's resolv.conf comes with the pod's
// search list and ndots, which are what make a short Service name
// resolve; they travel beside ip= and land in the same file.
func TestGuestDNSScriptWritesSearchAndOptions(t *testing.T) {
	got, err := runDNSScript(t, "console=ttyS0 ip=10.0.0.5::10.0.0.1:255.255.255.0::eth0:off:34.118.224.10 "+
		"granule.dns_search=default.svc.cluster.local,svc.cluster.local,cluster.local granule.dns_options=ndots:5")
	if err != nil {
		t.Fatal(err)
	}
	want := "nameserver 34.118.224.10\nsearch default.svc.cluster.local svc.cluster.local cluster.local\noptions ndots:5\n"
	if got != want {
		t.Fatalf("resolv.conf = %q, want %q", got, want)
	}
}

// The search and options lines are resolv.conf syntax too, so anything
// that is not a domain name or an ndots option stays out -- and a domain
// that is a glob is checked as written rather than expanded.
func TestGuestDNSScriptWritesOnlyNamesAndNdots(t *testing.T) {
	got, err := runDNSScript(t, "console=ttyS0 ip=10.0.0.5::10.0.0.1:255.255.255.0::eth0:off:10.0.0.53 "+
		"granule.dns_search=*,ok.example,bad\\nnameserver granule.dns_options=rotate,ndots:2,ndots:x")
	if err != nil {
		t.Fatal(err)
	}
	want := "nameserver 10.0.0.53\nsearch ok.example\noptions ndots:2\n"
	if got != want {
		t.Fatalf("resolv.conf = %q, want %q", got, want)
	}
}

// A v6-only guest has no ip= at all: its nameservers, search list and
// options all come off granule.dns= and its companions.
func TestGuestDNSScriptWritesGranuleDNSWithoutIP(t *testing.T) {
	got, err := runDNSScript(t, "console=ttyS0 granule.ip6=fd00:10:244::5/64,fe80::5 granule.dns=fd00:10:96::a "+
		"granule.dns_search=default.svc.cluster.local,svc.cluster.local,cluster.local granule.dns_options=ndots:5")
	if err != nil {
		t.Fatal(err)
	}
	want := "nameserver fd00:10:96::a\nsearch default.svc.cluster.local svc.cluster.local cluster.local\noptions ndots:5\n"
	if got != want {
		t.Fatalf("resolv.conf = %q, want %q", got, want)
	}
}

// On a dual-stack guest ip= carries the IPv4 half for images that
// predate granule.dns=; this one prefers granule.dns=, which has the
// whole list in the pod's order, and does not write ip='s twice.
func TestGuestDNSScriptPrefersGranuleDNS(t *testing.T) {
	got, err := runDNSScript(t, "console=ttyS0 ip=10.244.0.5::10.244.0.1:255.255.255.0::eth0:off:10.96.0.10 "+
		"granule.dns=fd00:10:96::a,10.96.0.10 granule.dns_search=cluster.local")
	if err != nil {
		t.Fatal(err)
	}
	want := "nameserver fd00:10:96::a\nnameserver 10.96.0.10\nsearch cluster.local\n"
	if got != want {
		t.Fatalf("resolv.conf = %q, want %q", got, want)
	}
}

// granule.dns= is held to what ip= is: addresses only, and no more than
// the three a resolver reads.
func TestGuestDNSScriptWritesOnlyAddressesFromGranuleDNS(t *testing.T) {
	got, err := runDNSScript(t, "console=ttyS0 granule.dns=*,options,fe80::1%eth0,nameserver,deadbeef,::1:x,"+
		"2001:db8::53,10.0.0.53,::ffff:10.0.0.54,fd00::53")
	if err != nil {
		t.Fatal(err)
	}
	want := "nameserver 2001:db8::53\nnameserver 10.0.0.53\nnameserver ::ffff:10.0.0.54\n"
	if got != want {
		t.Fatalf("resolv.conf = %q, want %q", got, want)
	}
}

// Without a nameserver on the command line the file is the image's own, and a search
// list for a resolver this script did not write is not added to it.
func TestGuestDNSScriptNeedsANameserverForSearch(t *testing.T) {
	got, err := runDNSScript(t, "console=ttyS0 ip=10.0.0.5::10.0.0.1:255.255.255.0::eth0:off granule.dns_search=cluster.local")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("resolv.conf = %q, want it left alone", got)
	}
	got, err = runDNSScript(t, "console=ttyS0 granule.ip6=fd00:10:244::5/64 granule.dns_search=cluster.local")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("resolv.conf = %q, want it left alone", got)
	}
}
