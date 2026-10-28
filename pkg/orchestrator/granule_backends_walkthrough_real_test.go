package orchestrator_test

// TestGranuleBackendsWalkthroughOnRealVMs is the walkthrough harness for
// docs/test-plans/2026-10-02.md §5 (report:
// docs/test-runs/2026-10-02-sandbox-backends-addressing.md). It drives
// GranuleSandboxes the way runDaemon builds it under
// -granule-sandbox-cidr and -granule-command -- a real sandboxaddr pool on
// a docker network grain creates, and granule reached through a wrapper
// command rather than off PATH -- against real granule, docker and
// cloud-hypervisor VMs, and logs what each step saw:
//
//   - the address docker gave each VM container against the one the pool
//     allocated, and the one the guest itself reports;
//   - a NetworkNone guest sending from a second, spoofed address on its
//     segment to the internet, to a NetworkOpen neighbour and to a host
//     port that is not a sandbox service (each must be dropped), next to
//     the same guest's real address reaching the sandbox service port;
//   - `docker network rm` of grain's network between acquires, after
//     which the next acquire has to bring the network back;
//   - every granule invocation, from the wrapper's own log, so create,
//     exists (`vm list`), list and delete can be seen going through it.
//
// It needs root-capable nft (sudo -n), /dev/kvm, docker, and a guest
// image with a "debian" user, sudo, curl and iproute2, so it is opt-in
// rather than part of the real-vm job:
//
//	GRAIN_BACKENDS_WALKTHROUGH=1 \
//	  GRAIN_BACKENDS_WALKTHROUGH_IMAGE=ghcr.io/bwsalmon/grain/guest:main \
//	  go test ./pkg/orchestrator -run TestGranuleBackendsWalkthroughOnRealVMs -v -timeout 30m

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/granule"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/sandboxaddr"
)

func TestGranuleBackendsWalkthroughOnRealVMs(t *testing.T) {
	if os.Getenv("GRAIN_BACKENDS_WALKTHROUGH") == "" {
		t.Skip("GRAIN_BACKENDS_WALKTHROUGH not set; this is the 2026-10-02 §5 walkthrough harness, run by hand")
	}
	image := os.Getenv("GRAIN_BACKENDS_WALKTHROUGH_IMAGE")
	if image == "" {
		t.Fatal("GRAIN_BACKENDS_WALKTHROUGH_IMAGE is required")
	}
	t.Cleanup(orchestrator.UseRealHostFirewall())
	ctx := context.Background()

	// granule reached through a wrapper that logs its argv, the way a
	// deployment names `limactl shell ... granule` or a docker exec.
	granuleDir := buildGranule(t)
	wrapDir := t.TempDir()
	wrapLog := filepath.Join(wrapDir, "argv.log")
	wrapper := filepath.Join(wrapDir, "granule-wrapper")
	script := fmt.Sprintf("#!/bin/sh\necho \"$*\" >> %s\nexec %s \"$@\"\n", wrapLog, filepath.Join(granuleDir, "granule"))
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// granule itself must not be on PATH: anything that bypassed the
	// wrapper would then fail rather than quietly succeed.
	if p, err := exec.LookPath("granule"); err == nil {
		t.Logf("note: a granule is on PATH at %s; the wrapper log is what shows the route taken", p)
	}

	pool, err := sandboxaddr.NewPool("10.189.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	network := fmt.Sprintf("grain-walk-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", network).Run() })

	service := serveOnWildcard(t, "grain-sandbox-service")
	other := serveOnWildcard(t, "not-a-sandbox-service")

	stateDir := t.TempDir()
	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		Command:           granule.Command{wrapper},
		CreateArgs:        []string{"-kontur-image", image, "-guest-port", "22", "-guest-user", "debian", "-disk-mode=overlay"},
		SSHUser:           "debian",
		Workspace:         "/home/debian",
		ReadyTimeout:      4 * time.Minute,
		ReadyPollInterval: time.Second,
		Addresses:         pool,
		DockerNetwork:     network,
		DefaultCPUs:       1,
		DefaultMemoryMB:   1536,
		SandboxServicePorts: func() []orchestrator.SandboxServicePort {
			return []orchestrator.SandboxServicePort{{Proto: "tcp", Port: service}}
		},
	})

	tryAcquire := func(slot string, access model.NetworkAccess) (orchestrator.Sandbox, func(string) (string, bool), error) {
		t.Helper()
		start := time.Now()
		sb, err := k.Acquire(ctx, slot, orchestrator.Shape{NetworkAccess: access})
		if err != nil {
			return nil, nil, err
		}
		t.Logf("%s %s: acquired in %s", slot, access, time.Since(start).Round(time.Millisecond))
		tools, err := sb.Tools(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for i := range tools {
			if tools[i].Name == "run_command" {
				tool := tools[i]
				return sb, func(cmd string) (string, bool) {
					res := tool.Handler(ctx, map[string]any{"command": cmd})
					return res.Text, !res.IsError
				}, nil
			}
		}
		t.Fatal("no run_command tool")
		return nil, nil, nil
	}
	acquire := func(slot string, access model.NetworkAccess) (orchestrator.Sandbox, func(string) (string, bool)) {
		t.Helper()
		sb, run, err := tryAcquire(slot, access)
		if err != nil {
			t.Fatalf("%s %s: Acquire: %v", slot, access, err)
		}
		return sb, run
	}
	inspect := func(slot string) string {
		name, _ := k.VMNameFor(slot)
		out, _ := exec.Command("docker", "inspect", "-f",
			`{{range $n, $e := .NetworkSettings.Networks}}{{$n}}={{$e.IPAddress}} gw={{$e.Gateway}} {{end}}`,
			"granule-vm-"+name).CombinedOutput()
		return strings.TrimSpace(string(out))
	}

	// --- 1. addresses: allocation, docker, guest -----------------------
	//
	// NetworkNone first: on a host where its firewall cannot be installed
	// the acquire is refused (fail closed), and the rest of the walk still
	// has something to say, so a refusal is reported rather than fatal.
	none, runNone, noneErr := tryAcquire("w1-1", model.NetworkNone)
	if noneErr != nil {
		t.Errorf("w1-1 NetworkNone: Acquire refused: %v", noneErr)
	}
	open, runOpen := acquire("w2-1", model.NetworkOpen)
	var openAddr string
	for _, slot := range []string{"w1-1", "w2-1"} {
		if slot == "w1-1" && noneErr != nil {
			continue
		}
		want, _ := pool.Addr(slot)
		run := runNone
		if slot == "w2-1" {
			run = runOpen
			openAddr = want.String()
		}
		guest, _ := run("ip -4 -o addr show dev eth0")
		d := inspect(slot)
		t.Logf("%s: allocated %s; docker inspect %s; guest %q", slot, want, d, strings.TrimSpace(guest))
		if !strings.Contains(d, network+"="+want.String()+" ") {
			t.Errorf("%s: docker has %q, want %s=%s", slot, d, network, want)
		}
		if !strings.Contains(guest, want.String()+"/") {
			t.Errorf("%s: guest has %q, want %s", slot, guest, want)
		}
	}
	gateway := pool.Prefix().Addr().Next().String()

	ruleset, err := exec.Command("sudo", "-n", "nft", "list", "ruleset").CombinedOutput()
	t.Logf("sudo nft list ruleset (err=%v), grain tables only:\n%s", err, grainTables(string(ruleset)))
	netInspect, _ := exec.Command("docker", "network", "inspect", "-f",
		`{{.Name}} driver={{.Driver}} subnet={{range .IPAM.Config}}{{.Subnet}}{{end}} options={{.Options}}`, network).CombinedOutput()
	t.Logf("docker network: %s", strings.TrimSpace(string(netInspect)))

	// --- 2. NetworkNone, real and spoofed source -----------------------
	probes := []struct {
		name, cmd string
		want      bool
	}{
		{"real source -> sandbox service port", fmt.Sprintf("curl -sS -m5 http://%s:%d/", gateway, service), true},
		{"real source -> other host port", fmt.Sprintf("curl -sS -m4 http://%s:%d/", gateway, other), false},
		{"real source -> NetworkOpen neighbour ssh", fmt.Sprintf("curl -sS -m4 telnet://%s:22 </dev/null", openAddr), false},
		{"add spoofed address", "sudo -n ip addr add 10.189.200.200/16 dev eth0 && sudo -n ip route replace default via " + gateway + " dev eth0 && ip -4 -o addr show dev eth0", true},
		{"spoofed source -> internet 1.1.1.1", "curl -sS -m4 --interface 10.189.200.200 http://1.1.1.1/ -o /dev/null -w '%{http_code}'", false},
		{"spoofed source -> other host port", fmt.Sprintf("curl -sS -m4 --interface 10.189.200.200 http://%s:%d/", gateway, other), false},
		{"spoofed source -> host's non-gateway address", "curl -sS -m4 --interface 10.189.200.200 http://" + hostPrimaryAddr(t) + fmt.Sprintf(":%d/", other), false},
		{"spoofed source -> neighbour, curl --interface", fmt.Sprintf("curl -sS -m4 --interface 10.189.200.200 telnet://%s:22 </dev/null", openAddr), false},
	}
	for _, p := range probes {
		if noneErr != nil {
			break
		}
		out, ok := runNone(p.cmd)
		verdict := "as expected"
		if ok != p.want {
			verdict = "UNEXPECTED"
			t.Errorf("NetworkNone probe %q: succeeded=%v, want %v: %s", p.name, ok, p.want, out)
		}
		t.Logf("NetworkNone probe %q: succeeded=%v (%s)\n%s", p.name, ok, verdict, strings.TrimSpace(out))
	}
	// The open guest, for contrast: it does reach the internet.
	out, ok := runOpen("curl -sS -m6 -o /dev/null -w '%{http_code}' http://1.1.1.1/")
	t.Logf("NetworkOpen probe internet 1.1.1.1: succeeded=%v %s", ok, strings.TrimSpace(out))

	// --- 3. release, docker network rm, acquire again ------------------
	for _, sb := range []orchestrator.Sandbox{none, open} {
		if sb == nil {
			continue
		}
		if err := sb.Release(ctx); err != nil {
			t.Errorf("Release: %v", err)
		}
	}
	rm, err := exec.Command("docker", "network", "rm", network).CombinedOutput()
	t.Logf("docker network rm %s: err=%v %s", network, err, strings.TrimSpace(string(rm)))
	start := time.Now()
	again, err := k.Acquire(ctx, "w3-1", orchestrator.Shape{NetworkAccess: model.NetworkOpen})
	if err != nil {
		t.Errorf("first Acquire after docker network rm failed after %s: %v", time.Since(start).Round(time.Millisecond), err)
		start = time.Now()
		again, err = k.Acquire(ctx, "w3-1", orchestrator.Shape{NetworkAccess: model.NetworkOpen})
		t.Logf("second Acquire after docker network rm: err=%v after %s", err, time.Since(start).Round(time.Millisecond))
	} else {
		t.Logf("first Acquire after docker network rm succeeded in %s; %s", time.Since(start).Round(time.Millisecond), inspect("w3-1"))
	}
	if again != nil {
		_ = again.Release(ctx)
	}

	// --- 4. what went through the wrapper ------------------------------
	list, err := k.ReapOrphans(ctx)
	t.Logf("ReapOrphans: %v %v", list, err)
	argv, _ := os.ReadFile(wrapLog)
	t.Logf("granule invocations through -granule-command:\n%s", argv)
	for _, verb := range []string{"vm create", "vm list", "vm delete"} {
		if !strings.Contains(string(argv), verb) {
			t.Errorf("no %q went through the wrapper", verb)
		}
	}
}

// grainTables is the part of an nft ruleset that grain installed.
func grainTables(ruleset string) string {
	var b strings.Builder
	in := false
	for _, line := range strings.Split(ruleset, "\n") {
		if strings.HasPrefix(line, "table ") {
			in = strings.Contains(line, "grain")
		}
		if in {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// hostPrimaryAddr is the host's address on its default route.
func hostPrimaryAddr(t *testing.T) string {
	out, err := exec.Command("sh", "-c", "ip -4 route get 1.1.1.1 | awk '{for(i=1;i<NF;i++) if($i==\"src\") print $(i+1)}'").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// TestStandingSandboxRestartOnRealVMWalkthrough is the same plan's step 6:
// a standing sandbox served on a real granule VM is killed from outside
// (its VM container, docker kill), and the supervisor's reconcile passes
// -- run back to back, standing in for the reconcile tick -- have to
// notice and restart it while GetActive keeps answering. Every GetActive
// call's latency is measured from a poller running the whole time.
func TestStandingSandboxRestartOnRealVMWalkthrough(t *testing.T) {
	if os.Getenv("GRAIN_BACKENDS_WALKTHROUGH") == "" {
		t.Skip("GRAIN_BACKENDS_WALKTHROUGH not set; this is the 2026-10-02 §5 walkthrough harness, run by hand")
	}
	image := os.Getenv("GRAIN_BACKENDS_WALKTHROUGH_IMAGE")
	if image == "" {
		t.Fatal("GRAIN_BACKENDS_WALKTHROUGH_IMAGE is required")
	}
	ctx := context.Background()
	granuleDir := buildGranule(t)
	t.Setenv("PATH", granuleDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	stateDir := t.TempDir()
	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:          stateDir,
		CreateArgs:        []string{"-guest-port", "22", "-guest-user", "debian", "-disk-mode=overlay"},
		SSHUser:           "debian",
		Workspace:         "/home/debian",
		ReadyTimeout:      4 * time.Minute,
		ReadyPollInterval: time.Second,
		DefaultCPUs:       1,
		DefaultMemoryMB:   1536,
	})
	sup := orchestrator.NewStandingSandboxSupervisor(nil)
	deps := orchestrator.Deps{Sandboxes: k}
	desired := []model.StandingSandbox{{Name: "walk", Image: image, Command: "sleep infinity", Enabled: true, CPUs: 1, MemoryMB: 1536}}
	t.Cleanup(func() { _ = sup.Reconcile(context.Background(), deps, nil, time.Now()) })

	// The poller: GetActive's latency for the whole test.
	stop := make(chan struct{})
	type sample struct {
		at  time.Duration
		lat time.Duration
	}
	samples := make(chan []sample, 1)
	begin := time.Now()
	go func() {
		var got []sample
		for {
			select {
			case <-stop:
				samples <- got
				return
			case <-time.After(20 * time.Millisecond):
			}
			s := time.Now()
			sup.GetActive("walk")
			got = append(got, sample{at: s.Sub(begin), lat: time.Since(s)})
		}
	}()

	start := time.Now()
	if err := sup.Reconcile(ctx, deps, desired, time.Now()); err != nil {
		t.Fatal(err)
	}
	active, ok := sup.GetActive("walk")
	if !ok || active.Failed || active.Instance == nil {
		t.Fatalf("standing sandbox not running after the first reconcile: %+v", active)
	}
	t.Logf("first launch took %s", time.Since(start).Round(time.Millisecond))

	name, _ := k.VMNameFor("walk")
	out, err := exec.Command("docker", "kill", "granule-vm-"+name).CombinedOutput()
	t.Logf("docker kill granule-vm-%s: err=%v %s", name, err, strings.TrimSpace(string(out)))
	killed := time.Now()
	passes := 0
	for time.Since(killed) < 6*time.Minute {
		passes++
		ps := time.Now()
		if err := sup.Reconcile(ctx, deps, desired, time.Now()); err != nil {
			t.Fatal(err)
		}
		a, _ := sup.GetActive("walk")
		t.Logf("reconcile pass %d took %s: failed=%v restarts=%d lastError=%q", passes, time.Since(ps).Round(time.Millisecond), a.Failed, a.Restarts, a.LastError)
		if a.Restarts >= 1 && !a.Failed {
			t.Logf("restarted %s after the kill, over %d reconcile passes", time.Since(killed).Round(time.Millisecond), passes)
			break
		}
	}
	close(stop)
	got := <-samples
	var worst sample
	for _, s := range got {
		if s.lat > worst.lat {
			worst = s
		}
	}
	t.Logf("GetActive polled %d times over %s; slowest %s at +%s", len(got), time.Since(begin).Round(time.Millisecond), worst.lat, worst.at.Round(time.Millisecond))
	if a, _ := sup.GetActive("walk"); a == nil || a.Restarts < 1 || a.Failed {
		t.Errorf("the killed standing sandbox was not restarted: %+v", a)
	}
	if worst.lat > time.Second {
		t.Errorf("GetActive blocked for %s during a launch", worst.lat)
	}
}
