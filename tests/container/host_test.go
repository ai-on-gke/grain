package container

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Older setup.sh deployments serve the UI on 127.0.0.1:80, and the package
// cache's DNS interception binds 53, 443 and 80 whatever -ui-addr says
// (pkg/cli/packagecache.go), which is why the unit passes
// --cap-add NET_BIND_SERVICE unconditionally. The UI's port is what this
// test binds, because it is the one this container can be observed
// serving on.
//
// systemd's AmbientCapabilities used to grant that, and has no equivalent
// for a container: a non-root process gets no capability from --cap-add
// alone. Dockerfile gives the grain binary the matching *file* capability
// instead, and this is the pair of runs that shows that is what does the
// work -- the same image, the same unprivileged uid, the same port,
// differing only in whether CAP_NET_BIND_SERVICE is in the container's
// bounding set for that file capability to be raised from.
//
// The failing half is asserted on what is observable -- it never serves --
// rather than on a particular error, because the kernel has two ways to
// refuse and which one it picks is not the point: it may reject the bind
// (EACCES), or refuse the exec outright, since a binary carrying an
// effective file capability the bounding set does not allow is a
// "capability-dumb binary" the kernel declines to run at all rather than
// run under-privileged. Either way the deployment does not come up, which
// is the fact worth pinning.
//
// Bridged rather than host networking for both, so this needs no port 80
// on the machine running the tests -- the bind under test happens inside
// the container either way.
//
// Both halves pin net.ipv4.ip_unprivileged_port_start, and without that
// this test quietly stops testing anything. Docker 28 began setting it to
// 0 inside every container it starts, which makes port 80 unprivileged
// there and lets the capability-less half bind it happily -- so the
// failing half passes, on a machine whose docker is new enough, for a
// reason that has nothing to do with the file capability. Pinning it back
// to the kernel default is what keeps the two runs differing only in the
// capability, which is the whole claim.
func TestAPrivilegedPortBindsWithTheCapabilityAndNotWithout(t *testing.T) {
	requireImage(t)
	root := t.TempDir()

	privileged := []string{"--sysctl", "net.ipv4.ip_unprivileged_port_start=1024"}

	granted := start(t, filepath.Join(root, "granted"), options{
		bindPort: 80,
		network:  []string{"--network", "bridge"},
		runArgs:  append([]string{"--cap-add", "NET_BIND_SERVICE"}, privileged...),
	})
	status, raw := granted.api(t, http.MethodGet, "/api/config", nil)
	if status != http.StatusOK {
		t.Fatalf("with the capability, port 80 served %d: %s", status, raw)
	}
	granted.stop()

	dropped := newDaemon(t, filepath.Join(root, "dropped"), options{
		bindPort: 80,
		network:  []string{"--network", "bridge"},
		runArgs:  append([]string{"--cap-drop", "ALL"}, privileged...),
	})
	// Not start(): this one is expected never to serve, so what is awaited
	// is the container giving up rather than an API answering.
	dropped.run(t)

	var logs string
	waitFor(t, "the capability-less container to give up on port 80", 60*time.Second, func() error {
		if dropped.running() {
			return errNothingYet
		}
		logs = dropped.logs()
		return nil
	})

	if served, _, err := call(dropped.Base, http.MethodGet, "/api/config", "", nil, 5*time.Second); err == nil {
		t.Fatalf("it served %s (%d) anyway", dropped.Base, served)
	}
	// Logged, not asserted on: which refusal the kernel picked is worth
	// reading in a CI log, and is not what this pins down.
	t.Logf("refused port 80 without the capability, saying: %s", logs)
}

// The container cannot reboot the machine, shut it down, or restart the
// service it is, so it asks for all three.
//
// -reboot-cmd (added for exactly this) and its two neighbours -shutdown-cmd
// and -restart-cmd (grain/task-40) point the System pane's Power tab at a
// `touch` of a file under the mounted data directory; write_control_units
// installs the systemd .path units out on the host that turn those into the
// real `systemctl reboot`/`poweroff`/`restart grain-daemon.service`. This is
// the half inside the container: pressing a button has to produce that file
// on the *host* filesystem, not just inside a container about to vanish --
// and each button has to produce its own, since the three files are the
// only thing telling the host which of the three was asked for.
func TestThePowerButtonsWriteTheirControlFilesOnTheHost(t *testing.T) {
	requireImage(t)
	root := t.TempDir()

	control := filepath.Join(root, "data", "control")
	if err := os.MkdirAll(control, 0o755); err != nil {
		t.Fatalf("laying out the control directory: %v", err)
	}
	buttons := []struct {
		name    string
		flag    string
		route   string
		file    string
		offered func(config) bool
	}{
		{"reboot", "-reboot-cmd", "/api/host/reboot", filepath.Join(control, "reboot"),
			func(c config) bool { return c.RebootEnabled }},
		{"shutdown", "-shutdown-cmd", "/api/host/shutdown", filepath.Join(control, "shutdown"),
			func(c config) bool { return c.ShutdownEnabled }},
		{"restart", "-restart-cmd", "/api/host/restart", filepath.Join(control, "restart"),
			func(c config) bool { return c.RestartEnabled }},
	}

	var flags []string
	for _, b := range buttons {
		flags = append(flags, b.flag, "touch", b.flag, b.file)
	}
	d := start(t, root, options{flags: flags})

	// The UI only offers a button when the daemon says it can.
	_, raw := d.api(t, http.MethodGet, "/api/config", nil)
	var got config
	decode(t, raw, &got)
	for _, b := range buttons {
		if !b.offered(got) {
			t.Fatalf("the daemon does not offer the %s button: %s", b.name, raw)
		}
	}

	for _, b := range buttons {
		status, raw := d.api(t, http.MethodPost, b.route, nil)
		if status != http.StatusOK {
			t.Fatalf("POST %s: %d %s", b.route, status, raw)
		}
		waitFor(t, b.file+" to appear on the host", 60*time.Second, func() error {
			if _, err := os.Stat(b.file); err != nil {
				return err
			}
			return nil
		})
	}
}

// The other half, out on the host.
//
// PathModified (rather than PathExists) is what write_control_units
// watches these files with, and the reasoning depends on two behaviours
// worth pinning down rather than assuming: that `touch` on a file that
// does not exist yet triggers it, and that a service which *deletes* the
// request before acting re-arms for the next one instead of either looping
// or going deaf. A leftover request turning into a reboot on the next boot
// is the failure this shape exists to avoid, so it matters that it is this
// shape and not the other one.
func TestAPathUnitTurnsThatControlFileIntoACommand(t *testing.T) {
	requireImage(t)
	requireSystemd(t)

	root := t.TempDir()
	control := filepath.Join(root, "control")
	if err := os.MkdirAll(control, 0o777); err != nil {
		t.Fatalf("laying out the control directory: %v", err)
	}
	// systemd's own units read these as root; the temporary directory
	// above is this account's alone until it is opened up.
	if err := os.Chmod(control, 0o777); err != nil {
		t.Fatalf("opening up the control directory: %v", err)
	}
	request := filepath.Join(control, "request")
	marker := filepath.Join(control, "acted")
	unit := "grain-e2e-control-" + randomSuffix(t, 8)

	t.Cleanup(func() {
		_ = exec.Command("sudo", "systemctl", "stop", unit+".path").Run()
		_ = exec.Command("sudo", "rm", "-f",
			"/etc/systemd/system/"+unit+".path",
			"/etc/systemd/system/"+unit+".service").Run()
		_ = exec.Command("sudo", "systemctl", "daemon-reload").Run()
	})

	writeUnit(t, unit+".path", `[Unit]
Description=grain container e2e control channel

[Path]
PathModified=`+request+`
Unit=`+unit+`.service
`)
	writeUnit(t, unit+".service", `[Unit]
Description=grain container e2e control channel action

[Service]
Type=oneshot
ExecStart=/bin/rm -f `+request+`
ExecStart=/bin/sh -c 'echo acted >> `+marker+`'
`)
	sudo(t, "systemctl", "daemon-reload")
	sudo(t, "systemctl", "start", unit+".path")

	// First request: the file does not exist yet, so this is a create.
	touch(t, request)
	waitFor(t, "the path unit to act on the first request", 60*time.Second, func() error {
		_, err := os.Stat(marker)
		return err
	})
	waitFor(t, "the service to consume the request", 60*time.Second, func() error {
		if _, err := os.Stat(request); err == nil {
			return errNothingYet
		}
		return nil
	})

	// Second request, after the first was consumed -- the case that
	// matters for a deployment that reboots or restarts more than once.
	touch(t, request)
	waitFor(t, "the path unit to act on a second request", 60*time.Second, func() error {
		acted, err := os.ReadFile(marker)
		if err != nil {
			return err
		}
		if countLines(string(acted), "acted") != 2 {
			return errNothingYet
		}
		return nil
	})
}
