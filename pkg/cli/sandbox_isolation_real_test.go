package cli

// TestSandboxIsolationProbesOnRealGranuleVMs is the walkthrough harness
// for docs/test-plans/2026-10-01.md §6 (report:
// docs/test-runs/2026-10-01-sandbox-isolation-port-block.md). It builds
// the sandbox side of a dispatch the way runDaemon does -- this
// package's own startPackageCache behind a sandbox Fabric, its resolver
// attached to a pkgcache.ClientSet, and an orchestrator.GranuleSandboxes
// handed both -- acquires one NetworkOpen and one NetworkNone VM, points
// each at the package cache the way a dispatch does, and runs
// sandboxProbeScript in both. Every probe's raw output is logged, so the
// report's matrix is read straight off `go test -v`.
//
// It needs root (nft, and the resolver's and transparent listeners'
// ports 53/443/80), /dev/kvm, docker, a granule binary on PATH and the
// guest image named in GRAIN_ISOLATION_PROBE_IMAGE, so it is opt-in on
// GRAIN_ISOLATION_PROBE=1 rather than run by any CI job:
//
//	go test -c -o /tmp/grain.test ./cmd/grain
//	sudo env PATH=/path/to/granule:$PATH GRAIN_ISOLATION_PROBE=1 \
//	  GRAIN_ISOLATION_PROBE_IMAGE=ghcr.io/bwsalmon/grain/guest:latest \
//	  GRAIN_ISOLATION_PROBE_GATEWAY=<docker0 address> \
//	  /tmp/grain.test -test.run TestSandboxIsolationProbes -test.v -test.timeout 20m

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/daemon"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/pkgcache"
)

// sandboxProbeScript runs inside a guest as its ordinary user, with $1
// the docker gateway the deployment's services are bound on and $2 the
// package cache's URL. Each probe prints one "## name" header followed by
// what it saw, and never exits early: a probe that cannot run says so.
const sandboxProbeScript = `GW="$1"; CACHE="$2"
PATH=/usr/sbin:/sbin:$PATH
p() { echo "## $1"; }
p "uname"; uname -r
p "routes"; ip -4 route; ip -6 route 2>/dev/null
p "addrs"; ip -br addr
p "resolv.conf"; cat /etc/resolv.conf
p "status"; grep -E '^(CapEff|CapBnd|NoNewPrivs)' /proc/self/status
p "metadata"; curl -sS -m3 -o /dev/null -w 'http=%{http_code}\n' http://169.254.169.254/ 2>&1; echo "exit=$?"
p "metadata-with-route-restored"
sudo ip route add default via "$GW" 2>/dev/null && restored=1
curl -sS -m3 -o /dev/null -w 'http=%{http_code}\n' http://169.254.169.254/ 2>&1; echo "exit=$?"
p "internet-with-route-restored (1.1.1.1)"; curl -sS -m5 -o /dev/null -w 'http=%{http_code}\n' http://1.1.1.1/ 2>&1; echo "exit=$?"
[ -n "$restored" ] && sudo ip route del default via "$GW" 2>/dev/null
p "dns example.com (system resolver)"; timeout 6 getent ahosts example.com | head -1; echo "exit=${PIPESTATUS[0]}"
p "dns AAAA example.com"; timeout 6 getent ahostsv6 example.com | head -1; echo "exit=${PIPESTATUS[0]}"
p "dns over IPv6 to the gateway resolver"
gw6=$(ip -6 route show default 2>/dev/null | awk '{print $3; exit}')
if [ -n "$gw6" ]; then timeout 6 python3 - "$gw6" <<'PY'
import socket, struct, sys
q = struct.pack(">HHHHHH", 0x1234, 0x0100, 1, 0, 0, 0) + b"".join(bytes([len(l)]) + l.encode() for l in "example.com".split(".")) + b"\x00" + struct.pack(">HH", 28, 1)
s = socket.socket(socket.AF_INET6, socket.SOCK_DGRAM); s.settimeout(4)
s.sendto(q, (sys.argv[1], 53)); r = s.recv(512)
print("rcode=%d answers=%d" % (r[3] & 15, struct.unpack(">H", r[6:8])[0]))
PY
echo "exit=$?"; else echo "no IPv6 default route in the guest"; fi
p "dns api.github.com"; timeout 6 getent hosts api.github.com; echo "exit=$?"
p "dns deb.debian.org"; timeout 6 getent hosts deb.debian.org; echo "exit=$?"
p "internet https://example.com"; curl -sS -m5 -o /dev/null -w 'http=%{http_code}\n' https://example.com/ 2>&1; echo "exit=$?"
p "internet http://1.1.1.1"; curl -sS -m5 -o /dev/null -w 'http=%{http_code}\n' http://1.1.1.1/ 2>&1; echo "exit=$?"
p "github api"; curl -sS -m5 -o /dev/null -w 'http=%{http_code}\n' https://api.github.com/zen 2>&1; echo "exit=$?"
p "github api through the package cache URL"; curl -sS -m5 -o /dev/null -w 'http=%{http_code}\n' "$CACHE/github/repos/golang/go" 2>&1; echo "exit=$?"
p "POST to a GitHub API path through the package cache URL"; curl -sS -m5 -o /dev/null -w 'http=%{http_code}\n' -X POST -d '{}' "$CACHE/github/repos/golang/go/issues" 2>&1; echo "exit=$?"
p "package via interception (deb.debian.org Release)"; curl -sS -m20 -o /dev/null -w 'http=%{http_code}\n' https://deb.debian.org/debian/dists/bookworm/Release 2>&1; echo "exit=$?"
p "package via cache URL (npm registry)"; curl -sS -m20 -o /dev/null -w 'http=%{http_code}\n' "$CACHE/npm/left-pad" 2>&1; echo "exit=$?"
p "gateway ARP spoof (gratuitous ARP claiming $GW from this guest's MAC)"
sudo python3 - "$GW" <<'PY'
import socket, sys, fcntl, struct
import subprocess
r = subprocess.run(["ip", "-o", "route", "get", sys.argv[1]], capture_output=True, text=True).stdout.split()
ifname = r[r.index("dev") + 1]
mac = bytes.fromhex(open("/sys/class/net/%s/address" % ifname).read().strip().replace(":", ""))
gw = socket.inet_aton(sys.argv[1])
s = socket.socket(socket.AF_PACKET, socket.SOCK_RAW); s.bind((ifname, 0))
arp = struct.pack(">HHBBH", 1, 0x0800, 6, 4, 2) + mac + gw + b"\xff"*6 + gw
s.send(b"\xff"*6 + mac + b"\x08\x06" + arp)
forged = b"\x02\x00\x00\xde\xad\x01"
s.send(b"\xff"*6 + forged + b"\x08\x06" + struct.pack(">HHBBH", 1, 0x0800, 6, 4, 2) + forged + gw + b"\xff"*6 + gw)
print("sent spoofed ARP replies on %s (own MAC %s, forged source MAC 02:00:00:de:ad:01)" % (ifname, mac.hex(":")))
PY
echo "exit=$?"
p "end"`

func TestSandboxIsolationProbesOnRealGranuleVMs(t *testing.T) {
	if os.Getenv("GRAIN_ISOLATION_PROBE") == "" {
		t.Skip("GRAIN_ISOLATION_PROBE not set; this is the §6 walkthrough harness, run by hand as root on a KVM host")
	}
	if os.Geteuid() != 0 {
		t.Skip("needs root: nft, and ports 53/443/80 for the sandbox resolver")
	}
	image := os.Getenv("GRAIN_ISOLATION_PROBE_IMAGE")
	gateway := os.Getenv("GRAIN_ISOLATION_PROBE_GATEWAY")
	if image == "" || gateway == "" {
		t.Fatal("GRAIN_ISOLATION_PROBE_IMAGE and GRAIN_ISOLATION_PROBE_GATEWAY are required")
	}
	ctx := context.Background()
	dataDir := t.TempDir()

	fabric, err := daemon.NewDirectWithPortBlock(gateway, DefaultSandboxPortBase)
	if err != nil {
		t.Fatal(err)
	}
	cfg := daemon.Config{DataDir: dataDir, PackageCacheGB: 1}
	egress := &pkgcache.ClientSet{}
	pc, stop, err := daemon.StartPackageCache(cfg, fabric, egress)
	if err != nil {
		t.Fatalf("starting the package cache: %v", err)
	}
	t.Cleanup(func() { _ = stop(context.Background()) })
	if !pc.DnsServing {
		t.Fatalf("the package cache's resolver is not serving on %s", pc.DnsIP)
	}
	// What a NetworkNone guest is checked against: the git proxy's
	// segment, served here the way runDaemon serves it.
	if _, _, err := fabric.Serve("git proxy", nil); err != nil {
		t.Fatalf("binding the git proxy's port: %v", err)
	}

	servicePorts := &daemon.SandboxServicePorts{}
	servicePorts.Serve(fabric)
	servicePorts.WithPackageCache(pc)

	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:     t.TempDir(),
		CreateArgs:   []string{"-kontur-image", image, "-guest-port", "22", "-guest-user", "debian", "-disk-mode=overlay"},
		SSHUser:      "debian",
		Workspace:    "/home/debian",
		ReadyTimeout: 4 * time.Minute, ReadyPollInterval: time.Second,
		GitProxyHost: gateway,
		DNSEgress:    egress,
		// The ports runDaemon hands it: the fabric's and the cache's.
		SandboxServicePorts: servicePorts.Ports,
	})

	var commanders []orchestrator.SandboxCommander
	// Run-shaped slot names: netshim's tap naming leaves nine bytes.
	for i, access := range []model.NetworkAccess{model.NetworkOpen, model.NetworkNone} {
		slot := []string{"9001-1", "9002-1"}[i]
		sb, err := k.Acquire(ctx, slot, orchestrator.Shape{NetworkAccess: access})
		if err != nil {
			t.Errorf("%s: Acquire: %v", access, err)
			continue
		}
		t.Cleanup(func() { _ = sb.Release(context.Background()) })
		configured, err := orchestrator.ConfigurePackageCacheTransparent(ctx, sb, pc.Url, pc.Ca.RootCertPEM(), pc.DnsIP)
		t.Logf("%s: package cache configured=%v err=%v", access, configured, err)
		cmd, ok := sb.(orchestrator.SandboxCommander)
		if !ok {
			t.Fatalf("%T is not a SandboxCommander", sb)
		}
		stdout, stderr, code := cmd.RunInSandbox(ctx, []string{"bash", "-c", sandboxProbeScript, "probe", gateway, pc.Url}, "")
		t.Logf("==== %s probe (exit %d) ====\n%s\n---- stderr ----\n%s", access, code, stdout, strings.TrimSpace(stderr))
		commanders = append(commanders, cmd)
	}
	// Whether either guest's spoofed ARP replies reached a neighbour: the
	// first guest's view of the gateway's MAC, after both have sent them,
	// against the gateway's own.
	if len(commanders) > 0 {
		out, _, _ := commanders[0].RunInSandbox(ctx, []string{"sh", "-c", "PATH=/usr/sbin:/sbin:$PATH; ping -c1 -W1 " + gateway + " >/dev/null; ip neigh show " + gateway}, "")
		t.Logf("first guest's neighbour entry for the gateway after the spoofs: %s", strings.TrimSpace(out))
	}
	if os.Getenv("GRAIN_ISOLATION_PROBE_HOLD") != "" {
		t.Log("holding for host-side inspection")
		time.Sleep(120 * time.Second)
	}
}

// containerProbeScript is sandboxProbeScript's counterpart for a
// -container-sandboxes container: no route to restore and no package
// cache, but a process limit and a capability set of its own to show.
const containerProbeScript = `GW="$1"
p() { echo "## $1"; }
p "status"; grep -E '^(CapEff|CapBnd|NoNewPrivs)' /proc/self/status
p "pids.max"; cat /sys/fs/cgroup/pids.max 2>&1
p "fork until refused"
python3 - <<'PY'
import os, time
pids = []
try:
    for i in range(6000):
        p = os.fork()
        if p == 0:
            time.sleep(30)
            os._exit(0)
        pids.append(p)
except OSError as e:
    print("fork refused after %d children: %s" % (len(pids), e))
else:
    print("forked all %d children" % len(pids))
for p in pids:
    os.kill(p, 9)
    os.waitpid(p, 0)
PY
p "routes"; ip -4 route
p "metadata"; curl -sS -m3 -o /dev/null -w 'http=%{http_code}\n' http://169.254.169.254/ 2>&1; echo "exit=$?"
p "internet https://example.com"; curl -sS -m5 -o /dev/null -w 'http=%{http_code}\n' https://example.com/ 2>&1; echo "exit=$?"
p "raw socket (ARP spoof needs CAP_NET_RAW)"
python3 -c 'import socket; socket.socket(socket.AF_PACKET, socket.SOCK_RAW); print("raw socket opened")' 2>&1 | tail -1
p "setuid escalation"; ls -l /usr/bin/su 2>/dev/null; su -c id root </dev/null 2>&1 | tail -1
p "end"`

func TestSandboxIsolationProbesOnLocalContainers(t *testing.T) {
	if os.Getenv("GRAIN_ISOLATION_PROBE") == "" {
		t.Skip("GRAIN_ISOLATION_PROBE not set; this is the §6 walkthrough harness, run by hand")
	}
	image := os.Getenv("GRAIN_ISOLATION_PROBE_CONTAINER_IMAGE")
	if image == "" {
		t.Skip("GRAIN_ISOLATION_PROBE_CONTAINER_IMAGE not set")
	}
	ctx := context.Background()
	c := orchestrator.NewContainerSandboxes(orchestrator.ContainerConfig{Image: image, Workspace: "/home/agent"})
	if _, err := c.Acquire(ctx, "9003-1", orchestrator.Shape{NetworkAccess: model.NetworkNone}); err != nil {
		t.Logf("NetworkNone Acquire refused, as it must be: %v", err)
	} else {
		t.Error("NetworkNone Acquire on a local container succeeded")
	}
	sb, err := c.Acquire(ctx, "9004-1", orchestrator.Shape{NetworkAccess: model.NetworkOpen})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = sb.Release(context.Background()) })
	cmd, ok := sb.(orchestrator.SandboxCommander)
	if !ok {
		t.Fatalf("%T is not a SandboxCommander", sb)
	}
	stdout, stderr, code := cmd.RunInSandbox(ctx, []string{"bash", "-c", containerProbeScript, "probe", os.Getenv("GRAIN_ISOLATION_PROBE_GATEWAY")}, "")
	t.Logf("==== container probe (exit %d) ====\n%s\n---- stderr ----\n%s", code, stdout, strings.TrimSpace(stderr))
}
