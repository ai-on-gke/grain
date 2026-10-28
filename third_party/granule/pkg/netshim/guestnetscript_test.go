package netshim

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// guestNetScript is the other half of what a guest does with the command
// line this package writes: granule-configure-routes applies the routes,
// and this applies the address they are reached over, for a guest whose
// kernel has no CONFIG_IP_PNP to do it itself. It gets exactly one run
// per boot -- nothing retries the unit -- and a guest it gives up on has
// no address at all for the rest of its life, a failure only visible
// over vsock. Worth knowing here rather than from a cluster.
const guestNetScript = "../../deploy/guest-image/overlay-debian/usr/local/libexec/granule-configure-net"

// fakeIPConfig stands in for klibc's ipconfig(8): it records the spec it
// was handed and, unless the test says otherwise, addresses the device
// by creating the file the fake "ip" answers "addr show" from.
//
// FAKEIPCONFIG_FAIL fails that many calls outright, the way the real one
// fails against a device that has not been probed ("SIOCGIFINDEX: No
// such device", exit 1, within the millisecond).
// FAKEIPCONFIG_QUIET_FAIL exits 0 without addressing anything and
// FAKEIPCONFIG_LOUD_SUCCESS addresses the device and exits 1 anyway --
// the two ways the exit status and the outcome come apart.
const fakeIPConfig = `#!/bin/sh
printf '%s\n' "$*" >> "$FAKEIPCONFIG_LOG"
calls=0
[ ! -f "${FAKEIPCONFIG_LOG}.calls" ] || calls=$(cat "${FAKEIPCONFIG_LOG}.calls")
calls=$((calls + 1))
printf '%s\n' "${calls}" > "${FAKEIPCONFIG_LOG}.calls"
if [ "${calls}" -le "${FAKEIPCONFIG_FAIL:-0}" ]; then
	echo "SIOCGIFINDEX: No such device" >&2
	exit 1
fi
[ -z "${FAKEIPCONFIG_QUIET_FAIL:-}" ] || exit 0
: > "$FAKE_ADDR_FILE"
[ -z "${FAKEIPCONFIG_LOUD_SUCCESS:-}" ] || exit 1
exit 0
`

// fakeNetIP records every call and answers "addr show" with an address
// once something has addressed the device.
const fakeNetIP = `#!/bin/sh
printf '%s\n' "$*" >> "$FAKEIP_LOG"
case "$*" in
*"addr show"*)
	[ ! -f "$FAKE_ADDR_FILE" ] || echo "2: eth0    inet 10.244.0.5/24 scope global eth0"
	;;
esac
exit 0
`

// netScriptCase is one run of granule-configure-net against a guest of
// the test's construction: a command line, a /sys/class/net, and an
// ipconfig that behaves however the case needs it to.
type netScriptCase struct {
	name    string
	cmdline string
	// devices exist in the fake /sys/class/net before the script
	// starts; whatever "during" creates turns up while it is running.
	devices []string
	during  func(t *testing.T, sysfs string)
	// addressed makes the fake "ip" report the interface as already
	// configured, which is what the bundled kernel's CONFIG_IP_PNP
	// leaves behind.
	addressed bool
	env       []string

	wantIPConfig []string
	wantErr      bool
}

type netScriptRun struct {
	ip       []string // calls made to the fake "ip"
	ipconfig []string // specs handed to the fake ipconfig
	err      error    // the script's own verdict
}

func runGuestNetScript(t *testing.T, tc netScriptCase) netScriptRun {
	t.Helper()

	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	sysfs := filepath.Join(dir, "sys", "class", "net")
	for _, d := range []string{bin, sysfs} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("creating %s: %v", d, err)
		}
	}
	for _, dev := range tc.devices {
		if err := os.MkdirAll(filepath.Join(sysfs, dev), 0o755); err != nil {
			t.Fatalf("creating the fake %s: %v", dev, err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "ip"), []byte(fakeNetIP), 0o755); err != nil {
		t.Fatalf("writing the fake ip: %v", err)
	}
	ipconfig := filepath.Join(dir, "ipconfig")
	if err := os.WriteFile(ipconfig, []byte(fakeIPConfig), 0o755); err != nil {
		t.Fatalf("writing the fake ipconfig: %v", err)
	}
	cmdlinePath := filepath.Join(dir, "cmdline")
	if err := os.WriteFile(cmdlinePath, []byte(tc.cmdline+"\n"), 0o644); err != nil {
		t.Fatalf("writing the fake cmdline: %v", err)
	}
	addrFile := filepath.Join(dir, "addressed")
	if tc.addressed {
		if err := os.WriteFile(addrFile, nil, 0o644); err != nil {
			t.Fatalf("marking the fake interface addressed: %v", err)
		}
	}

	ipLog := filepath.Join(dir, "ip.log")
	ipconfigLog := filepath.Join(dir, "ipconfig.log")

	cmd := exec.Command("sh", guestNetScript)
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GRANULE_CMDLINE_FILE="+cmdlinePath,
		"GRANULE_SYSFS_NET="+sysfs,
		"GRANULE_IPCONFIG="+ipconfig,
		// No sleeping between attempts, and none at all for a device
		// the case never intends to create. The cases that do want the
		// waiting exercised set the timeout themselves.
		"GRANULE_NET_RETRY_DELAY=0",
		"GRANULE_NET_DEVICE_TIMEOUT=0",
		"FAKEIP_LOG="+ipLog,
		"FAKEIPCONFIG_LOG="+ipconfigLog,
		"FAKE_ADDR_FILE="+addrFile,
	)
	cmd.Env = append(cmd.Env, tc.env...)

	stderr := &strings.Builder{}
	cmd.Stdout = stderr
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting granule-configure-net: %v", err)
	}
	if tc.during != nil {
		tc.during(t, sysfs)
	}
	run := netScriptRun{err: cmd.Wait()}
	t.Logf("granule-configure-net said:\n%s", stderr)

	run.ip = readCallLog(t, ipLog)
	run.ipconfig = readCallLog(t, ipconfigLog)
	return run
}

func readCallLog(t *testing.T, path string) []string {
	t.Helper()

	logged, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var calls []string
	for _, line := range strings.Split(strings.TrimSpace(string(logged)), "\n") {
		if line != "" {
			calls = append(calls, line)
		}
	}
	return calls
}

func TestGuestNetScriptAddressesTheGuest(t *testing.T) {
	const cmdline = "console=ttyS0 root=/dev/vda rw " +
		"ip=10.244.0.5::10.244.0.1:255.255.255.0::eth0:off " +
		"granule.routes=10.244.0.1/32,0.0.0.0/0@10.244.0.1"
	const spec = "10.244.0.5::10.244.0.1:255.255.255.0::eth0:off"

	for _, tc := range []netScriptCase{
		{
			// The ordinary GUEST_KERNEL_PACKAGE boot: nothing acted on
			// "ip=", the device is there, ipconfig addresses it.
			name:         "a device that is already there",
			cmdline:      cmdline,
			devices:      []string{"eth0"},
			wantIPConfig: []string{spec},
		},
		{
			// The bundled kernel's boot: CONFIG_IP_PNP addressed the
			// interface before userspace started, so there is nothing
			// to do, and ipconfig is not run against a live interface
			// for no gain.
			name:         "a device the kernel already addressed",
			cmdline:      cmdline,
			devices:      []string{"eth0"},
			addressed:    true,
			wantIPConfig: nil,
		},
		{
			// ipconfig gives a device it cannot find no time at all,
			// and this is the only thing that will ever address this
			// guest, so a failure is worth asking again rather than a
			// whole boot spent unreachable.
			name:         "an ipconfig that fails and then works",
			cmdline:      cmdline,
			devices:      []string{"eth0"},
			env:          []string{"FAKEIPCONFIG_FAIL=2"},
			wantIPConfig: []string{spec, spec, spec},
		},
		{
			// Whether an address landed is the question, not what
			// ipconfig's exit status was.
			name:         "an ipconfig that addresses the device and fails anyway",
			cmdline:      cmdline,
			devices:      []string{"eth0"},
			env:          []string{"FAKEIPCONFIG_LOUD_SUCCESS=1"},
			wantIPConfig: []string{spec},
		},
		{
			// And the other way round: an exit status of 0 with no
			// address to show for it is not success either.
			name:         "an ipconfig that succeeds without addressing anything",
			cmdline:      cmdline,
			devices:      []string{"eth0"},
			env:          []string{"FAKEIPCONFIG_QUIET_FAIL=1"},
			wantIPConfig: []string{spec, spec, spec},
			wantErr:      true,
		},
		{
			// Attempts run out eventually, and then this says so
			// loudly rather than exiting 0 on a guest with no address.
			name:         "an ipconfig that never works",
			cmdline:      cmdline,
			devices:      []string{"eth0"},
			env:          []string{"FAKEIPCONFIG_FAIL=99"},
			wantIPConfig: []string{spec, spec, spec},
			wantErr:      true,
		},
		{
			// GRANULE_NET_ATTEMPTS says how many.
			name:         "as many attempts as the guest was configured for",
			cmdline:      cmdline,
			devices:      []string{"eth0"},
			env:          []string{"FAKEIPCONFIG_FAIL=99", "GRANULE_NET_ATTEMPTS=5"},
			wantIPConfig: []string{spec, spec, spec, spec, spec},
			wantErr:      true,
		},
		{
			// No device, waited for and still not there. ipconfig is
			// not asked at all: it would fail instantly and say
			// nothing useful about why.
			name:         "a device that never appears",
			cmdline:      cmdline,
			wantIPConfig: nil,
			wantErr:      true,
		},
		{
			// The interface is read back out of the spec rather than
			// assumed, for a caller who wrote their own "ip=".
			name:         "an interface other than eth0",
			cmdline:      "console=ttyS0 ip=10.244.0.5::10.244.0.1:255.255.255.0::eth2:off",
			devices:      []string{"eth2"},
			wantIPConfig: []string{"10.244.0.5::10.244.0.1:255.255.255.0::eth2:off"},
		},
		{
			// No "ip=" at all: a guest nobody asked to address
			// statically, which is not this script's to touch.
			name:         "no ip= on the command line",
			cmdline:      "console=ttyS0 root=/dev/vda rw",
			devices:      []string{"eth0"},
			wantIPConfig: nil,
		},
		{
			// The race the waiting exists for: the unit ran before the
			// device was probed, which "udevadm settle" does not
			// prevent -- it returns success the moment udevd is not
			// yet answering, which is that unit's own instant. A
			// device that turns up a moment later is configured rather
			// than given up on.
			name:    "a device that has not been probed yet",
			cmdline: cmdline,
			during: func(t *testing.T, sysfs string) {
				time.Sleep(200 * time.Millisecond)
				if err := os.MkdirAll(filepath.Join(sysfs, "eth0"), 0o755); err != nil {
					t.Errorf("creating the fake eth0: %v", err)
				}
			},
			env:          []string{"GRANULE_NET_DEVICE_TIMEOUT=60"},
			wantIPConfig: []string{spec},
		},
		{
			// An ip= parameter is present but ipconfig is missing:
			// fail loudly rather than silently exiting 0 without
			// configuring an address.
			name:         "a guest with no ipconfig installed",
			cmdline:      cmdline,
			devices:      []string{"eth0"},
			env:          []string{"GRANULE_IPCONFIG=/nonexistent/ipconfig"},
			wantIPConfig: nil,
			wantErr:      true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := runGuestNetScript(t, tc)
			if (run.err != nil) != tc.wantErr {
				t.Fatalf("granule-configure-net: %v, wantErr %v\nip calls:\n%s",
					run.err, tc.wantErr, strings.Join(run.ip, "\n"))
			}
			if strings.Join(run.ipconfig, "\n") != strings.Join(tc.wantIPConfig, "\n") {
				t.Errorf("ipconfig calls:\n%s\nwant:\n%s",
					strings.Join(run.ipconfig, "\n"), strings.Join(tc.wantIPConfig, "\n"))
			}
		})
	}
}
