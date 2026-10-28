package netshim

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// guestScript is the guest's half of this package: what GuestConfig
// derives is only worth anything if the thing that reads it back applies
// it, and that thing is a shell script in the guest image rather than
// Go. It is exercised here, against a fake "ip", because the alternative
// is finding out from a booted guest on a cluster -- which is how the
// gap this fixes was found in the first place.
const guestScript = "../../deploy/guest-image/overlay-common/usr/local/libexec/granule-configure-routes"

// fakeIP records every invocation it is given, one line per call, and
// answers "route show" with whatever table the test says the guest has.
//
// FAKEIP_NO_LINK makes the interface permanently absent and
// FAKEIP_LINK_AFTER makes it absent for that many "link show" calls and
// present afterwards -- a device that has not been probed *yet*, which
// is the case the script waits for rather than gives up on.
// FAKEIP_FAIL_REPLACE fails any "route replace" whose arguments contain
// it, the way the kernel fails one whose nexthop is not reachable.
// FAKEIP_ROUTES6 is the IPv6 table "-6 route show" answers with, as
// FAKEIP_ROUTES is the IPv4 one.
const fakeIP = `#!/bin/sh
printf '%s\n' "$*" >> "$FAKEIP_LOG"
case "$*" in
*"link show"*)
	[ -z "${FAKEIP_NO_LINK:-}" ] || exit 1
	if [ -n "${FAKEIP_LINK_AFTER:-}" ]; then
		tries=0
		[ ! -f "${FAKEIP_LOG}.linktries" ] || tries=$(cat "${FAKEIP_LOG}.linktries")
		tries=$((tries + 1))
		printf '%s\n' "${tries}" > "${FAKEIP_LOG}.linktries"
		[ "${tries}" -gt "${FAKEIP_LINK_AFTER}" ] || exit 1
	fi
	;;
*"route replace"*)
	if [ -n "${FAKEIP_FAIL_REPLACE:-}" ]; then
		case "$*" in
		*"${FAKEIP_FAIL_REPLACE}"*) exit 1 ;;
		esac
	fi
	;;
*"-6 route show"*) [ -z "${FAKEIP_ROUTES6:-}" ] || printf '%s\n' "${FAKEIP_ROUTES6}" ;;
*"route show"*) [ -z "${FAKEIP_ROUTES:-}" ] || printf '%s\n' "${FAKEIP_ROUTES}" ;;
esac
exit 0
`

// runGuestScript runs granule-configure-routes against a command line of
// the test's choosing, with a fake "ip" on its PATH, and returns the
// calls it made together with the script's own verdict.
func runGuestScript(t *testing.T, cmdline string, env ...string) ([]string, error) {
	t.Helper()

	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("creating %s: %v", bin, err)
	}
	if err := os.WriteFile(filepath.Join(bin, "ip"), []byte(fakeIP), 0o755); err != nil {
		t.Fatalf("writing the fake ip: %v", err)
	}
	cmdlinePath := filepath.Join(dir, "cmdline")
	if err := os.WriteFile(cmdlinePath, []byte(cmdline+"\n"), 0o644); err != nil {
		t.Fatalf("writing the fake cmdline: %v", err)
	}
	logPath := filepath.Join(dir, "ip.log")

	cmd := exec.Command("sh", guestScript)
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GRANULE_CMDLINE_FILE="+cmdlinePath,
		"FAKEIP_LOG="+logPath,
	)
	cmd.Env = append(cmd.Env, env...)
	out, runErr := cmd.CombinedOutput()
	t.Logf("granule-configure-routes said:\n%s", out)

	logged, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil, runErr
	}
	if err != nil {
		t.Fatalf("reading the fake ip's log: %v", err)
	}
	var calls []string
	for _, line := range strings.Split(strings.TrimSpace(string(logged)), "\n") {
		if line != "" {
			calls = append(calls, line)
		}
	}
	return calls, runErr
}

func TestGuestScriptAppliesCarriedRoutes(t *testing.T) {
	const ptpCmdline = "console=ttyS0 root=/dev/vda rw " +
		"ip=10.244.0.5::10.244.0.1:255.255.255.0::eth0:off " +
		"granule.routes=10.244.0.1/32,10.244.0.0/24@10.244.0.1,0.0.0.0/0@10.244.0.1"

	for _, tc := range []struct {
		name    string
		cmdline string
		env     []string
		want    []string
		wantErr bool
	}{
		{
			// A pod on kind's ptp CNI, where the guest's own netmask is
			// the wrong answer: the gateway's host route goes in first
			// (nothing else makes the gateway reachable), and the
			// subnet's own route then replaces, in place, the on-link
			// one "ip=" left behind.
			name:    "point-to-point",
			cmdline: ptpCmdline,
			env: []string{"FAKEIP_ROUTES=10.244.0.1 dev eth0 scope link\n" +
				"10.244.0.0/24 via 10.244.0.1 dev eth0\n" +
				"default via 10.244.0.1 dev eth0"},
			want: []string{
				"link show dev eth0",
				"route replace 10.244.0.1/32 dev eth0 scope link",
				"route replace 10.244.0.0/24 via 10.244.0.1 dev eth0",
				"route replace 0.0.0.0/0 via 10.244.0.1 dev eth0",
				"-4 route show dev eth0",
			},
		},
		{
			// docker's own bridge, which CI and the docker backend
			// exercise: the CNI's routes are the routes "ip=" already
			// installed, so every one of these replaces what is already
			// there with itself and nothing is dropped. This case
			// changing is the regression to watch for.
			name: "bridge",
			cmdline: "console=ttyS0 root=/dev/vda rw " +
				"ip=172.17.0.2::172.17.0.1:255.255.0.0::eth0:off " +
				"granule.routes=172.17.0.0/16,0.0.0.0/0@172.17.0.1",
			env: []string{"FAKEIP_ROUTES=172.17.0.0/16 dev eth0 proto kernel scope link src 172.17.0.2\n" +
				"default via 172.17.0.1 dev eth0"},
			want: []string{
				"link show dev eth0",
				"route replace 172.17.0.0/16 dev eth0 scope link",
				"route replace 0.0.0.0/0 via 172.17.0.1 dev eth0",
				"-4 route show dev eth0",
			},
		},
		{
			// MTU discovered from the host-side spliced tap and propagated
			// via granule.mtu is applied to eth0 before routes are installed.
			name: "routes and mtu",
			cmdline: "console=ttyS0 root=/dev/vda rw " +
				"ip=10.244.0.5::10.244.0.1:255.255.255.0::eth0:off " +
				"granule.routes=10.244.0.1/32,10.244.0.0/24@10.244.0.1,0.0.0.0/0@10.244.0.1 " +
				"granule.mtu=1460",
			env: []string{"FAKEIP_ROUTES=10.244.0.1 dev eth0 scope link\n" +
				"10.244.0.0/24 via 10.244.0.1 dev eth0\n" +
				"default via 10.244.0.1 dev eth0"},
			want: []string{
				"link show dev eth0",
				"link set dev eth0 mtu 1460",
				"route replace 10.244.0.1/32 dev eth0 scope link",
				"route replace 10.244.0.0/24 via 10.244.0.1 dev eth0",
				"route replace 0.0.0.0/0 via 10.244.0.1 dev eth0",
				"-4 route show dev eth0",
			},
		},
		{
			// MTU only without granule.routes configures the interface MTU
			// without touching or pruning the routing table.
			name: "mtu only",
			cmdline: "console=ttyS0 root=/dev/vda rw " +
				"ip=10.244.0.5::10.244.0.1:255.255.255.0::eth0:off " +
				"granule.mtu=1440",
			want: []string{
				"link show dev eth0",
				"link set dev eth0 mtu 1440",
			},
		},
		{
			// An interface other than eth0 with MTU.
			name: "an interface other than eth0 with mtu",
			cmdline: "console=ttyS0 ip=10.244.0.5::10.244.0.1:255.255.255.0::eth1:off " +
				"granule.routes=0.0.0.0/0@10.244.0.1 " +
				"granule.mtu=1460",
			want: []string{
				"link show dev eth1",
				"link set dev eth1 mtu 1460",
				"route replace 0.0.0.0/0 via 10.244.0.1 dev eth1",
				"-4 route show dev eth1",
			},
		},
		{
			// A CNI whose table has no entry for the prefix the address
			// implies -- the shape Calico and Cilium use, a gateway on
			// a link-local address nothing else routes to. There is no
			// route to replace the on-link subnet route with, so it is
			// deleted instead: left in place it is more specific than
			// the default route and would swallow every peer.
			name: "a route the container network does not have",
			cmdline: "console=ttyS0 " +
				"ip=10.244.0.5::169.254.1.1:255.255.255.0::eth0:off " +
				"granule.routes=169.254.1.1/32,0.0.0.0/0@169.254.1.1",
			env: []string{"FAKEIP_ROUTES=169.254.1.1 dev eth0 scope link\n" +
				"10.244.0.0/24 dev eth0 proto kernel scope link src 10.244.0.5\n" +
				"default via 169.254.1.1 dev eth0"},
			want: []string{
				"link show dev eth0",
				"route replace 169.254.1.1/32 dev eth0 scope link",
				"route replace 0.0.0.0/0 via 169.254.1.1 dev eth0",
				"-4 route show dev eth0",
				"route del 10.244.0.0/24 dev eth0",
			},
		},
		{
			// The interface is read back out of the "ip=" parameter
			// rather than assumed, for a caller who wrote their own.
			name: "an interface other than eth0",
			cmdline: "console=ttyS0 ip=10.244.0.5::10.244.0.1:255.255.255.0::eth1:off " +
				"granule.routes=0.0.0.0/0@10.244.0.1",
			want: []string{
				"link show dev eth1",
				"route replace 0.0.0.0/0 via 10.244.0.1 dev eth1",
				"-4 route show dev eth1",
			},
		},
		{
			// No netshim in front of this guest, or an operator's own
			// "ip=", which takes the routes with it: the guest's table
			// is not this script's to touch.
			name:    "no routes on the command line",
			cmdline: "console=ttyS0 root=/dev/vda rw ip=192.168.1.5::192.168.1.1:255.255.255.0::eth0:off",
			want:    nil,
		},
		{
			// A NIC that has not been probed yet, which on a loaded
			// host is what "no eth0" usually means: a command line
			// carrying granule.routes= is one netshim wrote, so the
			// device is coming. Waiting a second for it is the
			// difference between a routed guest and one that spends
			// its whole life with no default route, since nothing
			// reruns this.
			name:    "an interface that has not been probed yet",
			cmdline: ptpCmdline,
			env: []string{
				"FAKEIP_LINK_AFTER=1",
				"GRANULE_NET_DEVICE_TIMEOUT=30",
			},
			want: []string{
				"link show dev eth0",
				"link show dev eth0",
				"route replace 10.244.0.1/32 dev eth0 scope link",
				"route replace 10.244.0.0/24 via 10.244.0.1 dev eth0",
				"route replace 0.0.0.0/0 via 10.244.0.1 dev eth0",
				"-4 route show dev eth0",
			},
		},
		{
			// A guest with no such NIC at all, waited for and still
			// not there. It is a failure and says so: this is the only
			// thing that ever gives such a guest the container
			// network's routes.
			name:    "no interface to route over",
			cmdline: ptpCmdline,
			env:     []string{"FAKEIP_NO_LINK=1", "GRANULE_NET_DEVICE_TIMEOUT=0"},
			want:    []string{"link show dev eth0"},
			wantErr: true,
		},
		{
			// An install that failed takes the pruning pass with it.
			// Every prefix that pass deletes is one the container
			// network does not have *according to the list that just
			// failed to install* -- so pruning after a failure deletes
			// the routes the guest does have, the default route
			// included, and leaves it reachable from nowhere rather
			// than merely reachable on its own segment.
			name:    "a route that will not install",
			cmdline: ptpCmdline,
			env: []string{
				"FAKEIP_FAIL_REPLACE=0.0.0.0/0",
				"FAKEIP_ROUTES=10.244.0.1 dev eth0 scope link\n" +
					"10.244.0.0/24 dev eth0 proto kernel scope link src 10.244.0.5",
			},
			want: []string{
				"link show dev eth0",
				"route replace 10.244.0.1/32 dev eth0 scope link",
				"route replace 10.244.0.0/24 via 10.244.0.1 dev eth0",
				"route replace 0.0.0.0/0 via 10.244.0.1 dev eth0",
			},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runGuestScript(t, tc.cmdline, tc.env...)
			if (err != nil) != tc.wantErr {
				t.Fatalf("granule-configure-routes: %v, wantErr %v", err, tc.wantErr)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("ip calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("ip call %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
