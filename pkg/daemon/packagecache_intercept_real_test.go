// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package daemon

// Interception, driven end to end against a real container on a real
// docker bridge: the claim every other test in this repository has to
// take on trust.
//
// The unit tests either side of this one pin the arithmetic -- that the
// address the resolver hands out is the address the listeners bind, that
// a sandbox is left alone when any of the three is missing. What none of
// them can say is whether the three add up to a sandbox that resolves
// deb.debian.org to this host and gets a package back, because every
// piece of that happens outside the process: the kernel refuses a
// privileged bind to an unprivileged daemon, a container's own resolver
// is docker's until something overwrites it, and a TLS certificate this
// deployment's CA minted is worth nothing until the trust store in that
// container has been rebuilt around it. The feature was merged, twice,
// without anybody being able to run it.
//
// So this test is the real thing throughout. startPackageCache binds the
// listeners, unchanged and unmocked, on the address a container-sandboxes
// deployment would advertise -- this machine's own docker bridge, which
// is what 172.17.0.1 is on a deployment host. The sandbox is a container
// on that bridge, and it is configured by
// orchestrator.ConfigurePackageCacheTransparent, which is the function a
// dispatch calls. Then the four questions an operator would ask from
// inside a fresh sandbox are asked from inside this one.
//
// It needs root (53, 443 and 80 are privileged -- that is the whole
// subject), a dockerd, and egress to the archive it fetches through, so
// it is gated on GRAIN_REAL_INTERCEPT_TESTS rather than running inside
// `go test ./...`. This is the version for a person to run: what it
// answers that nothing else can is that a *real* Debian archive, fetched
// by a real apt over a real TLS interception, works. grain CI runs its
// neighbour instead -- packagecache_intercept_hermetic_test.go, the same
// four questions against stand-ins on this machine's own addresses --
// because a job whose verdict depends on reaching deb.debian.org today
// is a job that reports on the internet rather than on the commit.
//
//	go test -c -o /var/tmp/grain.test ./pkg/daemon
//	sudo GRAIN_REAL_INTERCEPT_TESTS=1 /var/tmp/grain.test -test.v \
//	    -test.run TestInterceptionServesARealSandboxesOwnFetches
//
// or, without sudo, the way a deployment's own daemon holds those ports
// and the way CI runs the hermetic one (in /var/tmp rather than /tmp,
// which systemd mounts nosuid so the kernel ignores file capabilities from
// setcap):
//
//	sudo setcap cap_net_bind_service=+ep /var/tmp/grain.test
//	GRAIN_REAL_INTERCEPT_TESTS=1 /var/tmp/grain.test -test.v \
//	    -test.run TestInterceptionServesARealSandboxesOwnFetches
//
// Nothing outside the container and the test's own temporary directory is
// touched: the listeners are this test's, not a daemon's, and the
// container is removed however the test ends.

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// interceptTestImage is the sandbox stand-in: a Debian with the two
// tools the questions below are asked with. It is built before
// interception is configured, over the internet, on purpose -- what is
// under test is the fetching a configured sandbox does, not apt's
// ability to install curl.
const interceptTestImage = "grain-intercept-test:latest"

const interceptTestDockerfile = `FROM debian:trixie
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates \
	&& rm -rf /var/lib/apt/lists/*
`

func TestInterceptionServesARealSandboxesOwnFetches(t *testing.T) {
	if os.Getenv("GRAIN_REAL_INTERCEPT_TESTS") != "1" {
		t.Skip("set GRAIN_REAL_INTERCEPT_TESTS=1 to run this: it binds 53, 443 and 80 on this " +
			"machine's docker bridge and starts a container on it")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("docker is not on PATH: %v", err)
	}

	gateway := dockerBridgeGateway(t)
	requirePrivilegedBind(t, gateway)
	t.Logf("advertising this machine's docker bridge at %s, as a -container-git-proxy-host would", gateway)

	// The daemon's own startup path, with the config a container-sandboxes
	// deployment has.
	cfg := Config{
		DataDir:               t.TempDir(),
		PackageCacheGB:        1,
		ContainerSandboxes:    true,
		ContainerGitProxyHost: gateway,
		PackageCacheEnabled:   true,
	}
	// The fabric is built from that config exactly as run() builds it, so
	// the cache's own URL and its interception pair describe one machine.
	pc, stop, err := StartPackageCache(cfg, NewDirect(SandboxAdvertiseHost(cfg)), nil)
	if err != nil {
		t.Fatalf("startPackageCache: %v", err)
	}
	defer stop(context.Background())

	if !pc.DnsServing {
		t.Fatal("startPackageCache came up without interception: with the capability to bind 53, 443 " +
			"and 80 this is the failure the whole feature has been sitting behind -- read the log " +
			"line above for which bind failed")
	}
	if pc.DnsIP != gateway {
		t.Fatalf("interception advertises %s, want the bridge address %s", pc.DnsIP, gateway)
	}

	image := buildInterceptTestImage(t)
	sandbox := startInterceptTestContainer(t, image)

	// Exactly what a dispatch does about the package cache.
	configured, err := orchestrator.ConfigurePackageCacheTransparent(
		context.Background(), sandbox, pc.Url, pc.Ca.RootCertPEM(), pc.DnsIP)
	if err != nil {
		t.Fatalf("configuring the sandbox: %v", err)
	}
	if !configured {
		t.Fatal("ConfigurePackageCacheTransparent configured nothing")
	}

	// 1. An intercepted domain resolves to this deployment.
	if got := sandboxResolves(t, sandbox, "deb.debian.org"); got != gateway {
		t.Errorf("deb.debian.org resolves to %q inside the sandbox, want %s -- the resolver is not "+
			"being reached, or is not answering with itself", got, gateway)
	} else {
		t.Logf("deb.debian.org resolves to %s inside the sandbox, which is this cache", got)
	}

	// 2. And everything else still resolves to what it really is. This is
	// the half that breaks catastrophically rather than invisibly: the
	// sandbox's resolv.conf was replaced, so a resolver that forwards
	// nothing takes github.com, the agent's own API and every other host
	// a run touches away with it.
	if got := sandboxResolves(t, sandbox, "example.com"); got == "" || got == gateway {
		t.Errorf("example.com resolves to %q inside the sandbox -- a non-intercepted name has to be "+
			"forwarded upstream and answered with the real address (ZeroEgress is false)", got)
	} else {
		t.Logf("example.com resolves to %s, forwarded upstream", got)
	}

	// 3. An https fetch of an intercepted domain is served by this cache,
	// under a certificate it minted, which the sandbox's trust store
	// accepts. The body is the proof of the first part: the root of the
	// service answers with what it is, so a sandbox that believes it is
	// talking to deb.debian.org and is told "grain proxy cache" has
	// been intercepted, decrypted and served here.
	root := sandboxRun(t, sandbox, "curl -sS https://deb.debian.org/")
	if !strings.Contains(root, "grain proxy cache") {
		t.Errorf("curl https://deb.debian.org/ from the sandbox returned:\n%s\nwant the proxy "+
			"cache's own description -- it was not served here", root)
	} else {
		t.Logf("curl https://deb.debian.org/ was served by the cache, under its own CA")
	}

	// 4. And a real archive fetch through it works: an index forwarded
	// (indexes are never stored) and a package stored. apt is the client
	// that matters, so apt is what asks -- over the very configuration
	// ConfigurePackageCacheTransparent just wrote, https DIRECT and all.
	code := sandboxRun(t, sandbox,
		"curl -sS -o /dev/null -w '%{http_code}' https://deb.debian.org/debian/dists/trixie/InRelease")
	if strings.TrimSpace(code) != "200" {
		t.Errorf("fetching trixie's InRelease through the cache answered %s, want 200", code)
	}
	before := aptEntries(t, pc)
	out := sandboxRun(t, sandbox, "apt-get update && apt-get install -y --no-install-recommends cpio")
	if !strings.Contains(out, "Setting up cpio") {
		t.Errorf("installing a package inside the sandbox did not finish:\n%s", out)
	}
	if after := aptEntries(t, pc); after <= before {
		t.Errorf("the cache holds %d apt entries after that install and held %d before -- the "+
			"packages went somewhere else", after, before)
	} else {
		t.Logf("the cache stored %d apt entries fetching that package", after-before)
	}
}

// dockerBridgeGateway is this machine's docker bridge address: what a
// container reaches the host at, and so what a container-sandboxes
// deployment advertises (172.17.0.1 on a default docker).
func dockerBridgeGateway(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("docker", "network", "inspect", "bridge",
		"--format", "{{range .IPAM.Config}}{{.Gateway}}{{end}}").CombinedOutput()
	if err != nil {
		t.Skipf("asking docker for the bridge gateway: %v\n%s", err, out)
	}
	gateway := strings.TrimSpace(string(out))
	if net.ParseIP(gateway) == nil {
		t.Skipf("docker's bridge has no gateway address here (%q), so there is nothing for a "+
			"container to reach this test at", gateway)
	}
	return gateway
}

// requirePrivilegedBind skips unless this process can bind a privileged
// port on addr -- which is the subject of the test, so it is asked as a
// question about this process rather than about its uid. Root is one
// answer; CAP_NET_BIND_SERVICE, which is what setup.sh grants the daemon
// and what the image's file capability gives the binary, is the other and
// the one a deployment actually runs under.
func requirePrivilegedBind(t *testing.T, addr string) {
	t.Helper()
	probe, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(addr), Port: 53})
	if err != nil {
		t.Skipf("this process cannot bind %s:53 (%v) -- run the test binary as root, or give it "+
			"CAP_NET_BIND_SERVICE the way a deployment's daemon has it (not on nosuid /tmp): "+
			"sudo setcap cap_net_bind_service=+ep /var/tmp/grain.test", addr, err)
	}
	probe.Close()
}

func buildInterceptTestImage(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/Dockerfile", []byte(interceptTestDockerfile), 0o644); err != nil {
		t.Fatalf("writing the test image's Dockerfile: %v", err)
	}
	cmd := exec.Command("docker", "build", "-t", interceptTestImage, dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("building the sandbox stand-in image needs an internet this machine does not "+
			"have: %v\n%s", err, out)
	}
	return interceptTestImage
}

// startInterceptTestContainer runs the container and presents it as the
// Sandbox orchestrator writes into. argv is what it runs while the test
// asks its questions of it -- anything that stays up and does nothing;
// `sleep` when a caller has one, which the image with no base image
// behind it (packagecache_intercept_hermetic_test.go) does not.
func startInterceptTestContainer(t *testing.T, image string, argv ...string) *interceptSandbox {
	t.Helper()
	if len(argv) == 0 {
		argv = []string{"sleep", "600"}
	}
	name := fmt.Sprintf("grain-intercept-test-%d", time.Now().UnixNano())
	out, err := exec.Command("docker", append([]string{"run", "-d", "--rm", "--name", name, image}, argv...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("starting the sandbox container: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command("docker", "rm", "-f", name).CombinedOutput(); err != nil {
			t.Logf("removing the sandbox container: %v\n%s", err, out)
		}
	})
	return &interceptSandbox{name: name}
}

// interceptSandbox is one docker container, reached the way a sandbox
// backend reaches its own: a command and its stdin in, stdout, stderr
// and an exit code out.
type interceptSandbox struct{ name string }

func (s *interceptSandbox) Name() string { return s.name }

func (s *interceptSandbox) Tools(context.Context) ([]mcp.Tool, error) { return nil, nil }

func (s *interceptSandbox) ConfigureGitCredentials(context.Context, string, string, mcp.GitIdentity) error {
	return nil
}

func (s *interceptSandbox) Release(context.Context) error { return nil }

func (s *interceptSandbox) RunInSandbox(ctx context.Context, argv []string, stdin string) (string, string, int) {
	args := append([]string{"exec", "-i", s.name}, argv...)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		return stdout.String(), stderr.String() + err.Error(), 1
	}
	return stdout.String(), stderr.String(), code
}

// sandboxRun runs one shell command inside the sandbox and returns what
// it printed, failing the test if it did not succeed.
func sandboxRun(t *testing.T, sandbox *interceptSandbox, script string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	stdout, stderr, code := sandbox.RunInSandbox(ctx, []string{"sh", "-c", script}, "")
	if code != 0 {
		t.Fatalf("`%s` inside the sandbox exited %d:\n%s\n%s", script, code, stdout, stderr)
	}
	return stdout + stderr
}

// sandboxResolves is `getent hosts <name>` inside the sandbox, reduced
// to the first address it answered with -- which is what every client in
// there will use, and the thing the resolv.conf grain wrote decides.
func sandboxResolves(t *testing.T, sandbox *interceptSandbox, name string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	stdout, stderr, code := sandbox.RunInSandbox(ctx, []string{"getent", "hosts", name}, "")
	if code != 0 {
		t.Errorf("getent hosts %s inside the sandbox exited %d: %s%s", name, code, stdout, stderr)
		return ""
	}
	fields := strings.Fields(stdout)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// aptEntries is how many apt packages the cache holds.
func aptEntries(t *testing.T, pc *PackageCache) int {
	t.Helper()
	stats, err := pc.PackageCacheStats()
	if err != nil {
		t.Fatalf("reading the cache's own figures: %v", err)
	}
	for _, stat := range stats {
		if stat.Kind == "apt" {
			return stat.Entries
		}
	}
	return 0
}
