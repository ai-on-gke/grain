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

package orchestrator

// The two docker halves of the package cache, driven end to end against a
// real dockerd in a real sandbox -- the claims packagecache_test.go and
// pkg/pkgcache's own tests cannot make between them: that an image pull
// goes through the cache, and that a container this sandbox *builds*
// fetches its packages through it without being handed a proxy for the
// whole internet.
//
// What those already cover is every piece in isolation: that the merge
// keeps the guest image's own `bip` and `default-address-pools`, that the
// service answers /v2/ and does the 401-and-token dance, that the scripts
// put their arguments in the right order. What none of them can say is
// whether the pieces add up to a sandbox that actually pulls an image
// through the cache -- and the two ways that can fail are both invisible
// from a unit test:
//
//   - dockerd applies only some of daemon.json on SIGHUP. Writing the
//     file and reloading is what configureDockerCache does; whether the
//     daemon that is already running picks the mirror up, rather than
//     leaving it for a boot that never comes in a sandbox that lives for
//     one run, is a question about dockerd (the answer is yes for adding
//     a mirror, and no for taking one away again -- which is why the
//     cleanup below restarts docker rather than reloading it).
//   - a mirror dockerd will not use is a mirror that costs nothing and
//     says nothing. dockerd falls back to the registry itself for
//     anything a mirror answers badly, so every mistake in this path --
//     a plain-HTTP mirror missing from `insecure-registries`, a /v2/ ping
//     that challenges for credentials, a blob served with the wrong
//     length -- shows up as a pull that works and a cache that stays
//     empty.
//
// So this test writes the configuration the way a dispatch does
// (ConfigurePackageCache, unchanged and unmocked), then pulls an image
// with the real `docker` CLI and reads what the cache saw.
//
// Docker Hub itself is not involved, on purpose. The cache proxies
// whatever Config.DockerRegistry names, so the registry here is a
// stand-in on loopback serving one small image built in memory -- which
// keeps this test hermetic on a CI sandbox whose egress is not
// guaranteed (.grain/workflows/tests.yml's own header on
// packages.cloud.google.com), and lets it assert on a pull nobody else
// could have warmed a cache with. Everything between dockerd and that
// registry is the real thing: the mirror configuration, the /v2/ API,
// the token dance, the blob store.
//
// It owns this machine's /etc while it runs -- it writes the same files
// into it that a dispatched sandbox gets, and it stops and starts
// dockerd -- so it is gated on GRAIN_REAL_DOCKER_CACHE_TESTS rather than
// running inside `go test ./...`. Everything it touches is restored
// before it returns.

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/pkgcache"
)

// TestPackageCachePullsThroughARealDockerd is the whole of it: configure,
// check the file and the daemon, pull twice, and check the cache filled
// up in between.
func TestPackageCachePullsThroughARealDockerd(t *testing.T) {
	requireRealDockerSandbox(t)
	host := &thisMachine{}
	// Registered before anything is written, so a failure half way
	// through still hands the machine back as it was found.
	restorePackageCacheConfig(t, host)

	dockerImg := buildStandInImage(t, "library/grain-package-cache-probe", "v1")
	ghcrImg := buildStandInImage(t, "bwsalmon/grain-ghcr-probe", "v1")
	gcrImg := buildStandInImage(t, "google-samples/grain-gcr-probe", "v1")

	dockerReg := startStandInRegistry(t, dockerImg)
	ghcrReg := startStandInRegistry(t, ghcrImg)
	gcrReg := startStandInRegistry(t, gcrImg)

	cache, err := pkgcache.NewCache(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("opening a cache directory: %v", err)
	}
	seen := &recordingCache{handler: pkgcache.NewServer(pkgcache.Config{
		Cache:          cache,
		DockerRegistry: dockerReg.URL,
		GHCRRegistry:   ghcrReg.URL,
		GCRRegistry:    gcrReg.URL,
		Log:            t.Logf,
	})}
	// Loopback: dockerd runs on this same machine, which is what makes a
	// sandbox a sandbox -- its daemon is inside it.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for the cache: %v", err)
	}
	service := &http.Server{Handler: seen}
	go service.Serve(listener)
	t.Cleanup(func() { service.Close() })
	base := "http://" + listener.Addr().String()

	configured, err := ConfigurePackageCache(context.Background(), host, base)
	if err != nil {
		t.Fatalf("ConfigurePackageCache against this machine: %v", err)
	}
	if !configured {
		t.Fatal("ConfigurePackageCache reported doing nothing to a sandbox it can run commands in")
	}

	assertDaemonJSONKeptTheGuestsOwn(t, host, base)
	assertDockerdUsesTheMirror(t, base)
	assertDockerClientHasNoDefaultProxy(t, host)

	type probe struct {
		ref      string
		registry *standInRegistry
	}
	probes := []probe{
		{ref: "grain-package-cache-probe:v1", registry: dockerReg},
		{ref: "ghcr.io/bwsalmon/grain-ghcr-probe:v1", registry: ghcrReg},
		{ref: "gcr.io/google-samples/grain-gcr-probe:v1", registry: gcrReg},
	}

	for _, p := range probes {
		removeImage(t, p.ref)
		if out, err := dockerCLI(t, 2*time.Minute, "system", "prune", "-af"); err != nil {
			t.Fatalf("emptying dockerd store before pull: %v\n%s", err, out)
		}
		seen.reset()
		if out, err := dockerCLI(t, 3*time.Minute, "pull", p.ref); err != nil {
			t.Fatalf("pulling %s through mirror at %s: %v\n%s\n\nwhat cache was asked for:\n%s",
				p.ref, base, err, out, seen.report())
		}
		if p.registry.tokenFetches() == 0 {
			t.Errorf("registry for %s was never asked for a token", p.ref)
		}
		firstBlobs := seen.statuses("/blobs/")
		if len(firstBlobs) == 0 {
			t.Fatalf("dockerd pulled %s without asking mirror for a blob:\n%s", p.ref, seen.report())
		}
		if !contains(firstBlobs, "miss") {
			t.Errorf("no blob was stored on first pull of %s (statuses %v):\n%s", p.ref, firstBlobs, seen.report())
		}

		// Second pull
		seen.reset()
		removeImage(t, p.ref)
		if out, err := dockerCLI(t, 2*time.Minute, "system", "prune", "-af"); err != nil {
			t.Fatalf("emptying dockerd store between pulls: %v\n%s", err, out)
		}
		if out, err := dockerCLI(t, 3*time.Minute, "pull", p.ref); err != nil {
			t.Fatalf("pulling %s a second time: %v\n%s\n\nwhat cache was asked for:\n%s", p.ref, err, out, seen.report())
		}
		secondBlobs := seen.statuses("/blobs/")
		if len(secondBlobs) == 0 {
			t.Fatalf("second pull of %s asked mirror for no blobs:\n%s", p.ref, seen.report())
		}
		if contains(secondBlobs, "miss") || !contains(secondBlobs, "hit") {
			t.Errorf("second pull of %s blob requests were %v, want all hits:\n%s", p.ref, secondBlobs, seen.report())
		}
	}
}

// TestPackageCacheReachesAChildContainerBuild is the other half of the
// docker story, and the one 2026-09-18 got wrong: a `RUN pip install` in
// somebody's Dockerfile, fetched through this deployment's cache, without
// the cache becoming that container's proxy for the whole internet.
//
// What a container inherits from the sandbox around it is nothing. The
// files ConfigurePackageCache writes are in an /etc the container does not
// have, and the image is one grain does not own. So the settings are
// handed in at the build, out of /etc/grain/package-cache.env -- the same
// file and the same `--build-arg` line docs/package-cache.md tells a run
// to use -- and a probe compiled into the image reports, from inside the
// build, what it could and could not reach.
//
// It asserts the failure modes rather than the happy path, because the
// happy path is not where this has gone wrong before. Inside one real
// build, on a real dockerd:
//
//   - the four per-ecosystem settings arrive, and pip's index, npm's
//     registry and the Go module proxy all answer from the cache;
//   - an https:// URL still works, which is the `deb https://…` source
//     that became a CONNECT the cache refused outright with 405 (task 277;
//     the cache tunnels one to an archive it serves now, but not to a host
//     it has never heard of, and a build fetches plenty of those);
//   - a plain-HTTP fetch of a host the cache does not serve still works,
//     which is the 403 from an allowlist the Dockerfile's author never
//     heard of (task 277 again);
//   - and no HTTP_PROXY, http_proxy or any of their spellings is in the
//     build's environment at all.
//
// Then the same build is run again with that proxy deliberately passed
// in, and must fail. Without that half the test above it would pass just
// as well against a build that was never in any danger.
//
// Nothing here reaches the internet, for the reason the test above does
// not: the upstreams are stand-ins on this machine's docker bridge
// address, which is what a container can reach the host at.
func TestPackageCacheReachesAChildContainerBuild(t *testing.T) {
	requireRealDockerSandbox(t)
	host := &thisMachine{}
	restorePackageCacheConfig(t, host)

	// The bridge gateway: the address a container on docker's default
	// network reaches this machine at, and so the address every stand-in
	// below has to listen on. Read from the daemon rather than assumed,
	// since this deployment's guest image moves the bridge off 172.17/16
	// (daemon.json's own `bip`).
	gateway := bridgeGateway(t)

	// The probe is built before anything is configured, so that the `go
	// env -w GOPROXY` a moment later cannot be what compiles it. GOPROXY=off
	// keeps it honest: a module with no dependencies needs no network, and
	// this says so rather than trusting it.
	buildContext := t.TempDir()
	buildProbe(t, buildContext)

	// One stand-in serving all three upstreams the cache proxies, by path:
	// PyPI's simple index (with a link to an artifact on itself, which the
	// cache rewrites back through itself), npm's registry, and the Go
	// module proxy.
	upstream := listenOn(t, gateway)
	upstreamAddr := upstream.Addr().String()
	serveStandInUpstreams(t, upstream, upstreamAddr)

	// A plain-HTTP host that is not a distribution archive -- the cache's
	// allowlist has never heard of it and 403s it -- and an https:// one,
	// which a proxied client can only reach by a CONNECT the cache refuses.
	// Both are addressed by name from inside the container, because every
	// HTTP client exempts a loopback or literal-address target from some of
	// its proxy handling and this test is about what a proxy would do.
	plain := listenOn(t, gateway)
	go http.Serve(plain, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, childProbeBody)
	}))
	secure := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, childProbeBody)
	}))
	secure.Listener.Close()
	secure.Listener = listenOn(t, gateway)
	secure.StartTLS()
	t.Cleanup(secure.Close)

	cacheDir := t.TempDir()
	cache, err := pkgcache.NewCache(cacheDir, 0)
	if err != nil {
		t.Fatalf("opening a cache directory: %v", err)
	}
	seen := &recordingCache{handler: pkgcache.NewServer(pkgcache.Config{
		Cache:     cache,
		PyPIIndex: "http://" + upstreamAddr + "/simple",
		// The host without its port: the cache compares a link's host to
		// this list with the port stripped off.
		PyPIFileHosts:  []string{gateway},
		PyPIFileScheme: "http",
		NPMRegistry:    "http://" + upstreamAddr + "/npm-registry",
		GoProxy:        "http://" + upstreamAddr + "/mod",
		Log:            t.Logf,
	})}
	cacheListener := listenOn(t, gateway)
	service := &http.Server{Handler: seen}
	go service.Serve(cacheListener)
	t.Cleanup(func() { service.Close() })
	base := "http://" + cacheListener.Addr().String()

	// What the probe needs that does not come from a build arg: the two
	// hosts it must still be able to reach on its own, and where their
	// names really listen (the image has no resolver, and this is what a
	// container's /etc/hosts would say).
	writeProbeConfig(t, buildContext, childProbeConfig{
		Plain:  "http://mirror.example.org:" + portOf(t, plain.Addr().String()) + "/hello",
		Secure: "https://example.com:" + portOf(t, secure.Listener.Addr().String()) + "/hello",
		Dial: map[string]string{
			"mirror.example.org": plain.Addr().String(),
			"example.com":        secure.Listener.Addr().String(),
		},
		Body: childProbeBody,
	})
	writeProbeCA(t, buildContext, secure.Certificate())

	if configured, err := ConfigurePackageCache(context.Background(), host, base); err != nil {
		t.Fatalf("ConfigurePackageCache against this machine: %v", err)
	} else if !configured {
		t.Fatal("ConfigurePackageCache reported doing nothing to a sandbox it can run commands in")
	}

	// Read back out of the sandbox's /etc, not out of the value that was
	// written there: the claim is about the file a run finds.
	args := buildArgsFromEnvFile(t, readAsRoot(t, host, pkgcache.ChildContainerEnvPath))
	if len(args) != 8 {
		t.Fatalf("%s turned into %v, want a --build-arg for each of the four settings",
			pkgcache.ChildContainerEnvPath, args)
	}

	const tag = "grain-child-container-probe:v1"
	t.Cleanup(func() { removeImage(t, tag) })
	build := append([]string{"build", "--no-cache", "--progress=plain", "-t", tag}, args...)
	out, err := dockerCLI(t, 5*time.Minute, append(build, buildContext)...)
	if err != nil {
		t.Fatalf("a build given %s could not reach the cache, or could not reach anything else:\n%s\n\n"+
			"what the cache was asked for:\n%s", pkgcache.ChildContainerEnvPath, out, seen.report())
	}
	t.Logf("the build said:\n%s", out)

	// The cache served the build, rather than the build quietly fetching
	// from the internet and saying so.
	for _, want := range []string{"/pypi/simple/demo/", "/pypi/files/", "/npm/demo", "/go/example.com/demo/@v/list"} {
		if len(seen.statuses(want)) == 0 {
			t.Errorf("the build did not fetch %s through the cache:\n%s", want, seen.report())
		}
	}
	// And was never asked to be anything else. A CONNECT or a fetch of the
	// host it does not serve would mean the build had been handed a proxy
	// after all.
	for _, call := range seen.paths() {
		if strings.HasPrefix(call, http.MethodConnect+" ") || strings.Contains(call, "mirror.example.org") {
			t.Errorf("the cache was used as a proxy rather than as four endpoints: %s\n%s", call, seen.report())
		}
	}

	// The other half: the same build, handed the cache as a proxy the way
	// ~/.docker/config.json used to hand it to every container. Both of the
	// fetches the test above asserts must break, which is what makes that
	// assertion worth making.
	seen.reset()
	proxied := append(append([]string{}, build...),
		"--build-arg", "HTTP_PROXY="+base, "--build-arg", "http_proxy="+base,
		"--build-arg", "HTTPS_PROXY="+base, "--build-arg", "https_proxy="+base)
	out, err = dockerCLI(t, 5*time.Minute, append(proxied, buildContext)...)
	if err == nil {
		t.Fatalf("a build proxied through the package cache reached the whole internet, so nothing above is "+
			"being asserted:\n%s", out)
	}
	if !strings.Contains(out, "403") {
		t.Errorf("a plain-HTTP fetch of a host the cache does not serve did not get the allowlist's 403:\n%s", out)
	}
	// A refused CONNECT reaches a client as its status line alone, which is
	// why this one is matched on the text. 403 rather than the 405 this was
	// until 2026-09-18: the cache tunnels a CONNECT naming an archive it
	// serves, on 443, and refuses every other one by the same allowlist a
	// plain-HTTP fetch is held to -- and this probe's https host is not an
	// archive and is not on 443.
	if !strings.Contains(out, "Forbidden") {
		t.Errorf("an https:// fetch through the cache did not get the CONNECT refusal's 403:\n%s", out)
	}
}

// childProbeBody is what the two stand-in hosts answer with, so the probe
// can tell an answer from a proxy's explanation of why there is none.
const childProbeBody = "the host answered for itself"

// childProbeConfig is what the probe cannot be told by a build arg: the
// two hosts it must reach on its own, and where their names listen.
type childProbeConfig struct {
	Plain  string            `json:"plain"`
	Secure string            `json:"secure"`
	Dial   map[string]string `json:"dial"`
	Body   string            `json:"body"`
}

// bridgeGateway is the address a container on docker's default network
// reaches this machine at.
func bridgeGateway(t *testing.T) string {
	t.Helper()
	out, err := dockerCLI(t, time.Minute, "network", "inspect", "bridge",
		"--format", "{{range .IPAM.Config}}{{.Gateway}}{{end}}")
	if err != nil {
		t.Fatalf("asking docker where its bridge gateway is: %v\n%s", err, out)
	}
	gateway := strings.TrimSpace(out)
	if net.ParseIP(gateway) == nil {
		t.Fatalf("docker's bridge gateway is %q, which is not an address a container could reach this machine at", gateway)
	}
	return gateway
}

// listenOn opens a port on the bridge gateway address -- reachable from a
// container, and from nothing outside this machine.
func listenOn(t *testing.T, gateway string) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", gateway+":0")
	if err != nil {
		t.Fatalf("listening on the docker bridge at %s: %v", gateway, err)
	}
	t.Cleanup(func() { listener.Close() })
	return listener
}

func portOf(t *testing.T, hostPort string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		t.Fatalf("splitting %q: %v", hostPort, err)
	}
	return port
}

// serveStandInUpstreams is PyPI, npm and the Go module proxy in one
// handler, which is all three of them this cache actually needs: an index
// with one package in it, a packument, and a version list.
func serveStandInUpstreams(t *testing.T, listener net.Listener, addr string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/simple/demo/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!DOCTYPE html><html><body>`+
			`<a href="http://%s/packages/demo-1.0-py3-none-any.whl#sha256=abc">demo-1.0-py3-none-any.whl</a>`+
			`</body></html>`, addr)
	})
	mux.HandleFunc("/packages/demo-1.0-py3-none-any.whl", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		fmt.Fprint(w, "the wheel bytes")
	})
	mux.HandleFunc("/npm-registry/demo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"name":"demo","dist-tags":{"latest":"1.0.0"}}`)
	})
	mux.HandleFunc("/mod/example.com/demo/@v/list", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "v1.0.0\n")
	})
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
}

// buildProbe compiles the program that does the asking from inside the
// build, and writes the Dockerfile that runs it.
//
// FROM scratch and a static binary, so that this test pulls no base image
// and depends on no registry: the image is the probe, its configuration
// and the certificate of the https:// server it has to reach.
//
// The ARG lines are the whole point of the exercise. A build argument
// reaches a RUN only if the Dockerfile declares it -- which is why grain
// cannot put these settings into somebody else's build on its own, and
// why the file it writes says so in its own header.
func buildProbe(t *testing.T, buildContext string) {
	t.Helper()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module grainprobe\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatalf("writing the probe's go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "main.go"), []byte(childProbeSource), 0o644); err != nil {
		t.Fatalf("writing the probe's source: %v", err)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(buildContext, "probe"), ".")
	build.Dir = source
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOFLAGS=", "GOPROXY=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compiling the probe that runs inside the build: %v\n%s", err, out)
	}
	dockerfile := "FROM scratch\n" +
		"COPY probe /probe\n" +
		"COPY probe.json /probe.json\n" +
		"COPY ca.pem /ca.pem\n" +
		"ARG PIP_INDEX_URL\n" +
		"ARG PIP_TRUSTED_HOST\n" +
		"ARG NPM_CONFIG_REGISTRY\n" +
		"ARG GOPROXY\n" +
		"RUN [\"/probe\"]\n"
	if err := os.WriteFile(filepath.Join(buildContext, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatalf("writing the Dockerfile: %v", err)
	}
}

func writeProbeConfig(t *testing.T, buildContext string, cfg childProbeConfig) {
	t.Helper()
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("encoding the probe's configuration: %v", err)
	}
	if err := os.WriteFile(filepath.Join(buildContext, "probe.json"), encoded, 0o644); err != nil {
		t.Fatalf("writing the probe's configuration: %v", err)
	}
}

func writeProbeCA(t *testing.T, buildContext string, cert *x509.Certificate) {
	t.Helper()
	encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	if err := os.WriteFile(filepath.Join(buildContext, "ca.pem"), encoded, 0o644); err != nil {
		t.Fatalf("writing the https server's certificate: %v", err)
	}
}

// buildArgsFromEnvFile turns the file a sandbox is given into the flags
// docs/package-cache.md tells a run to turn it into -- the `grep -v '^#' |
// sed 's/^/--build-arg /'` of that page, said in Go.
func buildArgsFromEnvFile(t *testing.T, content string) []string {
	t.Helper()
	var args []string
	for i, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, "=") {
			t.Fatalf("line %d of %s is neither a comment nor an assignment: %q",
				i+1, pkgcache.ChildContainerEnvPath, line)
		}
		args = append(args, "--build-arg", line)
	}
	return args
}

// childProbeSource is the program that runs inside the build. It reports
// every check rather than stopping at the first failure, so that a build
// which fails says all of what it could not do -- which is what the
// proxied half of the test reads its two refusals out of.
const childProbeSource = `package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
)

type config struct {
	Plain  string            ` + "`json:\"plain\"`" + `
	Secure string            ` + "`json:\"secure\"`" + `
	Dial   map[string]string ` + "`json:\"dial\"`" + `
	Body   string            ` + "`json:\"body\"`" + `
}

var failed bool

func fail(format string, args ...any) {
	failed = true
	fmt.Printf("FAIL: "+format+"\n", args...)
}

func main() {
	raw, err := os.ReadFile("/probe.json")
	if err != nil {
		fmt.Println("FAIL: reading /probe.json:", err)
		os.Exit(1)
	}
	var cfg config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		fmt.Println("FAIL: parsing /probe.json:", err)
		os.Exit(1)
	}

	// Nothing may have handed this build a proxy for everything.
	for _, name := range []string{
		"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy",
		"ALL_PROXY", "all_proxy", "FTP_PROXY", "ftp_proxy",
	} {
		if value := os.Getenv(name); value != "" {
			fail("%s=%s is set in this build: one proxy for the whole internet", name, value)
		}
	}

	pool := x509.NewCertPool()
	if ca, err := os.ReadFile("/ca.pem"); err == nil {
		pool.AppendCertsFromPEM(ca)
	}
	client := &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{RootCAs: pool},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if host, _, err := net.SplitHostPort(addr); err == nil {
				if to, ok := cfg.Dial[host]; ok {
					addr = to
				}
			}
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}

	get := func(what, target string) string {
		resp, err := client.Get(target)
		if err != nil {
			fail("%s: GET %s: %v", what, target, err)
			return ""
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("%s: GET %s -> %d\n", what, target, resp.StatusCode)
		if resp.StatusCode != http.StatusOK {
			fail("%s: GET %s answered %d: %s", what, target, resp.StatusCode, strings.TrimSpace(string(body)))
			return ""
		}
		return string(body)
	}

	// The four settings this build was given, each used the way the tool
	// it belongs to would use it.
	index := os.Getenv("PIP_INDEX_URL")
	if index == "" {
		fail("PIP_INDEX_URL is not set in this build, so nothing reached it")
	} else {
		page := get("pip index", strings.TrimSuffix(index, "/")+"/demo/")
		if link := regexp.MustCompile(` + "`" + `href="([^"]+)"` + "`" + `).FindStringSubmatch(page); link != nil {
			if base, err := url.Parse(index); err == nil {
				if href, err := base.Parse(link[1]); err == nil {
					if wheel := get("pip download", href.String()); wheel != "" && !strings.Contains(wheel, "wheel") {
						fail("pip download: the artifact was %q", wheel)
					}
				}
			}
		} else if page != "" {
			fail("pip index: no download link in %q", page)
		}
	}
	if host := os.Getenv("PIP_TRUSTED_HOST"); host == "" {
		fail("PIP_TRUSTED_HOST is not set in this build, so pip would refuse a plain-HTTP index")
	}
	if registry := os.Getenv("NPM_CONFIG_REGISTRY"); registry == "" {
		fail("NPM_CONFIG_REGISTRY is not set in this build")
	} else if doc := get("npm registry", strings.TrimSuffix(registry, "/")+"/demo"); doc != "" && !strings.Contains(doc, "dist-tags") {
		fail("npm registry: the packument was %q", doc)
	}
	if proxy := os.Getenv("GOPROXY"); proxy == "" {
		fail("GOPROXY is not set in this build")
	} else {
		first := strings.TrimSuffix(strings.Split(proxy, ",")[0], "/")
		if list := get("go modules", first+"/example.com/demo/@v/list"); list != "" && !strings.Contains(list, "v1.0.0") {
			fail("go modules: the version list was %q", list)
		}
	}

	// And the two things a proxied build could not do.
	if body := get("plain http", cfg.Plain); body != "" && strings.TrimSpace(body) != cfg.Body {
		fail("plain http: %s answered %q rather than for itself", cfg.Plain, body)
	}
	if body := get("https", cfg.Secure); body != "" && strings.TrimSpace(body) != cfg.Body {
		fail("https: %s answered %q rather than for itself", cfg.Secure, body)
	}

	if failed {
		os.Exit(1)
	}
	fmt.Println("the build reached the cache, and everything else it asked for")
}
`

// TestPackageCacheLeavesASandboxWithNoRunningDockerdAlone is the
// best-effort half of configureDockerCache, against a real systemd with a
// real stopped docker: a sandbox whose daemon is not running must come
// back configured and working rather than failed, with the file on disk
// for whatever starts later.
//
// Split from the test above rather than folded into it because it stops
// this machine's docker, and a `go test` that dies between the stop and
// the start would otherwise take the pull with it.
func TestPackageCacheLeavesASandboxWithNoRunningDockerdAlone(t *testing.T) {
	requireRealDockerSandbox(t)
	host := &thisMachine{}
	restorePackageCacheConfig(t, host)

	// Nothing starts it again here: restorePackageCacheConfig's own
	// cleanup restarts docker, and t.Cleanup runs last-registered-first,
	// so it is the last thing this test does.
	if out, err := exec.Command("sudo", "-n", "systemctl", "stop", "docker.socket", "docker").CombinedOutput(); err != nil {
		t.Fatalf("stopping docker: %v\n%s", err, out)
	}

	base := "http://127.0.0.1:41234"
	configured, err := ConfigurePackageCache(context.Background(), host, base)
	if err != nil {
		t.Fatalf("configuring a sandbox whose dockerd is not running: %v", err)
	}
	if !configured {
		t.Error("ConfigurePackageCache reported doing nothing")
	}
	if got := readAsRoot(t, host, pkgcache.DockerConfigPath); !strings.Contains(got, base) {
		t.Errorf("%s does not name the cache, so a dockerd starting later would not use it:\n%s", pkgcache.DockerConfigPath, got)
	}
}

// TestReloadDockerScriptIsSilentWithNoDockerAtAll runs the reload script
// itself with nothing on PATH -- which is what an image with no docker
// and no systemd in it looks like from inside this script -- and with a
// systemd whose docker unit is inactive.
//
// Both are ordinary sandboxes rather than broken ones (the Kubernetes
// backend's pods, a container sandbox that has no daemon of its own), and
// what they must not do is fail a dispatch or log a line about it. Run
// here rather than asserted about the script's text for the reason
// TestWriteAsRootScriptActuallyWritesTheFile exists: the bug this shape
// of script has had before was in what it did, not in what it said.
func TestReloadDockerScriptIsSilentWithNoDockerAtAll(t *testing.T) {
	run := func(dir string) (string, error) {
		cmd := exec.Command("sh", "-c", reloadDockerScript, "sh")
		// PATH is the whole of the difference: with only dir on it, the
		// script finds whatever dir holds and nothing else. `id` is
		// linked into each of these directories rather than reached
		// through the real PATH, since a PATH with /usr/bin on it would
		// find the systemctl this is meant not to have.
		cmd.Env = append(os.Environ(), "PATH="+dir)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// A sandbox with no docker and no systemd in it at all: the script's
	// own `command -v systemctl` is the whole of its answer.
	empty := t.TempDir()
	linkTool(t, empty, "id")
	out, err := run(empty)
	if err != nil {
		t.Errorf("a sandbox with no systemctl at all failed the reload: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("a sandbox with no systemctl printed %q", out)
	}

	// A systemd that is there and a docker that is not running. The stub
	// fails `is-active` the way systemctl does and records anything else
	// it is asked to do, so a reload sent to a dead daemon is visible
	// rather than merely unlikely.
	inactive := t.TempDir()
	linkTool(t, inactive, "id")
	log := inactive + "/calls"
	writePATHStub(t, inactive, "systemctl", "#!/bin/sh\n"+
		"echo \"$@\" >> "+log+"\n"+
		"case \"$*\" in *is-active*) exit 3;; esac\n")
	writePATHStub(t, inactive, "sudo", "#!/bin/sh\n[ \"$1\" = -n ] && shift\nexec \"$@\"\n")
	out, err = run(inactive)
	if err != nil {
		t.Errorf("a sandbox whose dockerd is not running failed the reload: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("a sandbox whose dockerd is not running printed %q", out)
	}
	if calls, err := os.ReadFile(log); err == nil && strings.Contains(string(calls), "reload") {
		t.Errorf("a stopped docker was sent a reload anyway: %q", calls)
	}
}

// linkTool puts the real binary named into a directory meant for PATH,
// for the handful of ordinary commands a script needs whatever else is
// missing from the sandbox it is running in.
func linkTool(t *testing.T, dir, name string) {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not on PATH, and this script needs one: %v", name, err)
	}
	if err := os.Symlink(path, dir+"/"+name); err != nil {
		t.Fatalf("linking %s into %s: %v", name, dir, err)
	}
}

// writePATHStub drops an executable of its own on a directory meant for PATH.
//
// It is written beside its path and installed into place rather than
// written there directly: a parallel test's fork can inherit the open
// write descriptor, and exec of a file still open for writing fails
// with "text file busy" (writeSweepKubectl does the same).
func writePATHStub(t *testing.T, dir, name, script string) {
	t.Helper()
	src := dir + "/" + name + ".src"
	if err := os.WriteFile(src, []byte(script), 0o644); err != nil {
		t.Fatalf("writing a %s stub: %v", name, err)
	}
	if out, err := exec.Command("install", "-m", "0755", src, dir+"/"+name).CombinedOutput(); err != nil {
		t.Fatalf("installing a %s stub: %v: %s", name, err, out)
	}
}

// requireRealDockerSandbox is the gate: opt in, and then every piece this
// test drives for real, each with its own skip reason so a machine
// missing exactly one says which.
func requireRealDockerSandbox(t *testing.T) {
	t.Helper()
	if os.Getenv("GRAIN_REAL_DOCKER_CACHE_TESTS") == "" {
		t.Skip("GRAIN_REAL_DOCKER_CACHE_TESTS not set; this suite owns /etc/docker and dockerd on the machine it runs on, and runs in .grain/workflows/tests.yml's package-cache job")
	}
	if runtime.GOOS != "linux" {
		t.Skip("the sandbox docker configuration this drives is Linux's")
	}
	for _, bin := range []string{"docker", "systemctl", "sudo"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH: %v", bin, err)
		}
	}
	if out, err := exec.Command("sudo", "-n", "true").CombinedOutput(); err != nil {
		t.Skipf("this account has no passwordless sudo, which is how a sandbox's /etc is written: %v\n%s", err, out)
	}
	if out, err := exec.Command("sudo", "-n", "systemctl", "is-active", "docker").CombinedOutput(); err != nil {
		t.Skipf("docker is not running under systemd here, so there is no reload to observe: %v\n%s", err, out)
	}
	if out, err := exec.Command("docker", "version").CombinedOutput(); err != nil {
		t.Skipf("docker does not answer: %v\n%s", err, out)
	}
}

// thisMachine presents the machine this test runs on as the Sandbox
// orchestrator writes into.
//
// Which is not a fiction: a grain CI job runs in a sandbox, of the same
// guest image a dispatched run gets, with the same dockerd inside it and
// the same /etc/docker/daemon.json that image ships. Running the commands
// here rather than over SSH costs this test the transport -- which every
// other sandbox test in this repository covers -- and buys it the one
// thing a transport cannot fake, which is a docker daemon on the far end.
type thisMachine struct{}

func (thisMachine) Name() string { return "the machine this test runs on" }

func (thisMachine) Tools(context.Context) ([]mcp.Tool, error) { return nil, nil }

func (thisMachine) ConfigureGitCredentials(context.Context, string, string, mcp.GitIdentity) error {
	return nil
}

func (thisMachine) Release(context.Context) error { return nil }

func (thisMachine) RunInSandbox(ctx context.Context, argv []string, stdin string) (string, string, int) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
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

// restorePackageCacheConfig remembers everything a dispatch changes --
// the files it writes, and the two configuration files it has `go env -w`
// and `npm config set` merge a line into -- and puts them all back,
// content or absence, when the test ends.
//
// The last two matter as much as the first three here, and for a reason
// peculiar to this suite: the "sandbox" it configures is the machine the
// job itself is running on, so a GOPROXY left pointing at a loopback
// port that dies with this test is a GOPROXY the rest of the job would
// inherit. Each is found by asking the tool where its own file is rather
// than by guessing, which is the same reason ConfigurePackageCache asks
// the tool to write it.
//
// docker is restarted rather than reloaded afterwards, which is a fact
// about dockerd worth writing down: a SIGHUP adds a registry mirror to a
// running daemon (which is the whole of what this suite asserts) and does
// not take one away again. A reload here would hand the rest of the job a
// docker still pointed at a cache that has gone with this test's
// t.TempDir.
func restorePackageCacheConfig(t *testing.T, host *thisMachine) {
	t.Helper()
	type saved struct {
		path    string
		content string
		existed bool
	}
	var files []saved
	for _, path := range []string{pkgcache.AptConfigPath, pkgcache.PipConfigPath, pkgcache.GHCRHostsConfigPath,
		pkgcache.GCRHostsConfigPath, pkgcache.ChildContainerEnvPath, pkgcache.DockerConfigPath} {
		content := readAsRoot(t, host, path)
		files = append(files, saved{path: path, content: content, existed: content != ""})
	}
	var owned []saved
	userPaths := toolConfigPaths(t, host)
	for _, path := range userPaths {
		content, err := os.ReadFile(path)
		owned = append(owned, saved{path: path, content: string(content), existed: err == nil})
	}
	t.Cleanup(func() {
		for _, file := range files {
			if file.existed {
				if _, stderr, code := host.RunInSandbox(context.Background(),
					[]string{"sh", "-c", writeAsRootScript, "sh", file.path, "0644"}, file.content); code != 0 {
					t.Errorf("putting %s back: %s", file.path, stderr)
				}
				continue
			}
			if out, err := exec.Command("sudo", "-n", "rm", "-f", file.path).CombinedOutput(); err != nil {
				t.Errorf("removing %s: %v\n%s", file.path, err, out)
			}
		}
		// These are this account's own, not root's, so they go back the
		// same way they were read.
		for _, file := range owned {
			if !file.existed {
				if err := os.Remove(file.path); err != nil && !os.IsNotExist(err) {
					t.Errorf("removing %s: %v", file.path, err)
				}
				continue
			}
			if err := os.WriteFile(file.path, []byte(file.content), 0o644); err != nil {
				t.Errorf("putting %s back: %v", file.path, err)
			}
		}
		restartDocker(t)
	})
}

// toolConfigPaths is where `go env -w` and `npm config set` record what
// ConfigurePackageCache asks them to, on this machine and for this
// account. A tool that is not installed contributes nothing, which is the
// same thing ConfigurePackageCache does about it.
func toolConfigPaths(t *testing.T, host *thisMachine) []string {
	t.Helper()
	var paths []string
	for _, argv := range [][]string{{"go", "env", "GOENV"}, {"npm", "config", "get", "userconfig"}} {
		stdout, _, code := host.RunInSandbox(context.Background(),
			append([]string{"sh", "-c", configureToolScript, "sh"}, argv...), "")
		if code != 0 {
			t.Fatalf("asking %s where it keeps its configuration: exit %d", argv[0], code)
		}
		if path := strings.TrimSpace(stdout); path != "" && filepath.IsAbs(path) {
			paths = append(paths, path)
		}
	}
	return paths
}

// restartDocker puts dockerd back on the configuration that has just
// been restored, and is also what starts it again for the test that
// stopped it.
//
// `reset-failed` first because systemd rate-limits a unit that is started
// too often (five times in ten seconds by default) and this suite stops
// and starts docker more than once in a `go test` run: without it the
// last restart in a run is the one that fails, and it fails after the
// test it belongs to has already passed.
func restartDocker(t *testing.T) {
	t.Helper()
	_ = exec.Command("sudo", "-n", "systemctl", "reset-failed", "docker.service", "docker.socket").Run()
	for _, unit := range []string{"docker.socket", "docker"} {
		if out, err := exec.Command("sudo", "-n", "systemctl", "restart", unit).CombinedOutput(); err != nil {
			t.Errorf("restarting %s on the configuration this test found: %v\n%s", unit, err, out)
		}
	}
}

// readAsRoot is the sandbox's own read path, which answers empty for a
// file that is not there.
func readAsRoot(t *testing.T, host *thisMachine, path string) string {
	t.Helper()
	stdout, stderr, code := host.RunInSandbox(context.Background(),
		[]string{"sh", "-c", readAsRootScript, "sh", path}, "")
	if code != 0 {
		t.Fatalf("reading %s: %s", path, stderr)
	}
	return stdout
}

// assertDaemonJSONKeptTheGuestsOwn reads the file back out of /etc rather
// than the string that was written into it: the claim is about what a
// sandbox ends up holding.
func assertDaemonJSONKeptTheGuestsOwn(t *testing.T, host *thisMachine, base string) {
	t.Helper()
	content := readAsRoot(t, host, pkgcache.DockerConfigPath)
	var doc struct {
		BIP                string `json:"bip"`
		DefaultAddressPool []struct {
			Base string `json:"base"`
		} `json:"default-address-pools"`
		Mirrors            []string `json:"registry-mirrors"`
		InsecureRegistries []string `json:"insecure-registries"`
	}
	if err := json.Unmarshal([]byte(content), &doc); err != nil {
		t.Fatalf("%s is not JSON after the merge: %v\n%s", pkgcache.DockerConfigPath, err, content)
	}
	// The guest image's own bridge configuration, which is what this
	// merge exists to preserve: a sandbox that came back with its docker
	// bridge moved onto a colliding subnet would be a far worse bug than
	// a slow pull.
	if doc.BIP == "" || len(doc.DefaultAddressPool) == 0 {
		t.Errorf("the merge lost the guest image's own docker bridge configuration:\n%s", content)
	}
	if len(doc.Mirrors) == 0 || doc.Mirrors[0] != base {
		t.Errorf("registry-mirrors = %v, want %s at its head:\n%s", doc.Mirrors, base, content)
	}
	if !contains(doc.InsecureRegistries, strings.TrimPrefix(base, "http://")) {
		t.Errorf("insecure-registries = %v -- dockerd will not speak plain HTTP to a mirror it has not been told to trust", doc.InsecureRegistries)
	}
}

// assertDockerClientHasNoDefaultProxy is the other way round from what it
// looks like: it asserts that configuring the cache left the docker
// *client* alone.
//
// A default proxy in ~/.docker/config.json is passed into every container
// the sandbox builds or runs, as http_proxy for the whole internet. This
// service is an allowlisted plain-HTTP archive cache, so a container that
// inherits it fetches every host it was never told about through an
// allowlist that 403s them -- and, until the tunnel in pkgcache/connect.go,
// fetched an https:// apt source through a CONNECT refused with 405. That
// is precisely what happened to every containerized build in this
// deployment on 2026-09-18. The sandbox's own apt is pointed here per host
// instead, where the narrowness survives.
func assertDockerClientHasNoDefaultProxy(t *testing.T, host *thisMachine) {
	t.Helper()
	paths := []string{"/root/.docker/config.json"}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths, filepath.Join(home, ".docker", "config.json"))
	}
	for _, path := range paths {
		content := strings.TrimSpace(readAsRoot(t, host, path))
		if content == "" {
			continue
		}
		var doc struct {
			Proxies map[string]struct {
				HTTPProxy  string `json:"httpProxy"`
				HTTPSProxy string `json:"httpsProxy"`
			} `json:"proxies"`
		}
		if err := json.Unmarshal([]byte(content), &doc); err != nil {
			// Somebody else's file in a shape this does not parse is not
			// this test's business; what it must never contain is a proxy
			// grain put there, and grain no longer writes this file at all.
			continue
		}
		for name, proxy := range doc.Proxies {
			if strings.Contains(proxy.HTTPProxy, cacheAddressMarker) || strings.Contains(proxy.HTTPSProxy, cacheAddressMarker) {
				t.Errorf("%s points docker's %q proxy at a loopback address (%+v): every container this sandbox "+
					"runs would inherit it as http_proxy for the whole internet", path, name, proxy)
			}
		}
	}
}

// cacheAddressMarker is what a proxy line naming this test's own cache
// looks like -- it listens on 127.0.0.1 with a port the kernel chose.
const cacheAddressMarker = "127.0.0.1:"

// assertDockerdUsesTheMirror asks the daemon rather than the file, which
// is the difference this test exists for: a mirror only dockerd's next
// start would read is no use to a sandbox that lives for one run.
func assertDockerdUsesTheMirror(t *testing.T, base string) {
	t.Helper()
	out, err := dockerCLI(t, time.Minute, "info")
	if err != nil {
		t.Fatalf("docker info: %v\n%s", err, out)
	}
	var mirrors []string
	inSection := false
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Registry Mirrors:") {
			inSection = true
			continue
		}
		if !inSection {
			continue
		}
		// The section ends at the next line indented no further than the
		// heading was -- `docker info` puts its mirrors one level in.
		if !strings.HasPrefix(line, "  ") || strings.HasSuffix(strings.TrimSpace(line), ":") {
			break
		}
		mirrors = append(mirrors, strings.TrimSpace(line))
	}
	if len(mirrors) == 0 {
		t.Fatalf("`docker info` lists no registry mirror at all, so the reload did not reach the running daemon:\n%s", out)
	}
	// dockerd prints a mirror as a URL with a trailing slash.
	for _, mirror := range mirrors {
		if strings.TrimSuffix(mirror, "/") == base {
			return
		}
	}
	t.Fatalf("`docker info` lists %v under Registry Mirrors, want %s among them", mirrors, base)
}

func dockerCLI(t *testing.T, timeout time.Duration, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	return string(out), err
}

// removeImage drops an image if it is there and says nothing if it is
// not -- a pull only proves something against a machine that does not
// already hold the answer.
func removeImage(t *testing.T, ref string) {
	t.Helper()
	if out, err := dockerCLI(t, time.Minute, "rmi", "-f", ref); err != nil && !strings.Contains(out, "No such image") {
		t.Logf("removing %s before the pull: %v\n%s", ref, err, out)
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// recordingCache wraps the cache's handler and remembers what it was
// asked for and what it answered, which is how this test reads a hit:
// the header is sent to dockerd, and dockerd says nothing about it.
type recordingCache struct {
	handler http.Handler
	mu      sync.Mutex
	calls   []cacheCall
}

type cacheCall struct {
	method, path, status string
	code                 int
}

func (rc *recordingCache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	recorder := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
	rc.handler.ServeHTTP(recorder, r)
	rc.mu.Lock()
	defer rc.mu.Unlock()
	// The whole request rather than its path when it arrived in
	// absolute form (a proxy request) or as a CONNECT (a tunnel), because
	// for those two the host is the interesting half and the path says
	// nothing. Neither shape reaches this service in an ordinary run --
	// which is exactly what TestPackageCacheReachesAChildContainerBuild
	// reads out of this.
	path := r.URL.Path
	if r.Method == http.MethodConnect {
		path = r.Host
	} else if r.URL.IsAbs() {
		path = r.URL.String()
	}
	rc.calls = append(rc.calls, cacheCall{
		method: r.Method, path: path,
		status: w.Header().Get("X-Grain-Package-Cache"), code: recorder.code,
	})
}

// statuses are the cache statuses of every GET whose path holds section,
// in order. HEADs are left out: they are never storable and so are always
// a pass, whatever is on disk.
func (rc *recordingCache) statuses(section string) []string {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	var out []string
	for _, call := range rc.calls {
		if call.method == http.MethodGet && strings.Contains(call.path, section) {
			out = append(out, call.status)
		}
	}
	return out
}

// paths is every request the cache was asked for, as "METHOD path" --
// which is how a test asks what the cache was *not* used for.
func (rc *recordingCache) paths() []string {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	var out []string
	for _, call := range rc.calls {
		out = append(out, call.method+" "+call.path)
	}
	return out
}

func (rc *recordingCache) reset() {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.calls = nil
}

func (rc *recordingCache) report() string {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	var b strings.Builder
	for _, call := range rc.calls {
		fmt.Fprintf(&b, "  %s %s -> %d %s\n", call.method, call.path, call.code, call.status)
	}
	if b.Len() == 0 {
		return "  (the cache was never asked for anything)\n"
	}
	return b.String()
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

// Hijack passes the connection through, because the cache answers a
// CONNECT for an archive it serves by tunnelling over it (pkgcache's
// connect.go) and a wrapper that swallowed the Hijacker would make every
// tunnel look like a cache that cannot make one.
func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := s.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("the response writer under this recorder cannot be hijacked")
	}
	s.code = http.StatusOK
	return hijacker.Hijack()
}

// standInImage is one small image, built here rather than fetched: a
// single-file layer, its config, and the manifest tying them together,
// each under the digest of its own bytes.
type standInImage struct {
	repository string
	tag        string
	manifest   []byte
	blobs      map[string][]byte
	mediaTypes map[string]string
}

func buildStandInImage(t *testing.T, repository, tag string) *standInImage {
	t.Helper()
	var uncompressed bytes.Buffer
	tw := tar.NewWriter(&uncompressed)
	body := []byte("pulled through grain's package cache for " + repository + "\n")
	if err := tw.WriteHeader(&tar.Header{
		Name: "grain-package-cache", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("writing the layer's tar header: %v", err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatalf("writing the layer: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing the layer's tar: %v", err)
	}
	// A layer is named twice over: dockerd verifies the compressed bytes
	// against the manifest's digest and the uncompressed ones against the
	// config's diff_id, so both have to be the real thing.
	diffID := digestOf(uncompressed.Bytes())
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(uncompressed.Bytes()); err != nil {
		t.Fatalf("compressing the layer: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing the layer's gzip: %v", err)
	}
	layer := compressed.Bytes()

	config, err := json.Marshal(map[string]any{
		"architecture": runtime.GOARCH,
		"os":           "linux",
		"config":       map[string]any{},
		"rootfs":       map[string]any{"type": "layers", "diff_ids": []string{diffID}},
	})
	if err != nil {
		t.Fatalf("building the image config: %v", err)
	}
	manifest, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     manifestMediaType,
		"config": map[string]any{
			"mediaType": configMediaType,
			"size":      len(config),
			"digest":    digestOf(config),
		},
		"layers": []any{map[string]any{
			"mediaType": layerMediaType,
			"size":      len(layer),
			"digest":    digestOf(layer),
		}},
	})
	if err != nil {
		t.Fatalf("building the manifest: %v", err)
	}
	return &standInImage{
		repository: repository,
		tag:        tag,
		manifest:   manifest,
		blobs:      map[string][]byte{digestOf(config): config, digestOf(layer): layer},
		mediaTypes: map[string]string{digestOf(config): configMediaType, digestOf(layer): layerMediaType},
	}
}

const (
	manifestMediaType = "application/vnd.docker.distribution.manifest.v2+json"
	configMediaType   = "application/vnd.docker.container.image.v1+json"
	layerMediaType    = "application/vnd.docker.image.rootfs.diff.tar.gzip"
)

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// standInRegistry is an OCI registry's shape without the real registry: it refuses an
// unauthenticated request with the same 401 and challenge, hands out an
// anonymous pull token to anyone who follows it, and serves the images
// configured on it.
//
// It counts what it served, which is the other half of "the second pull
// was a hit": a cache that answered from disk is a registry that was not
// asked.
type standInRegistry struct {
	*httptest.Server
	images map[string]*standInImage

	mu     sync.Mutex
	tokens int
	blobs  int
}

func startStandInRegistry(t *testing.T, images ...*standInImage) *standInRegistry {
	t.Helper()
	byRepo := map[string]*standInImage{}
	for _, img := range images {
		byRepo[img.repository] = img
	}
	reg := &standInRegistry{images: byRepo}
	reg.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			reg.mu.Lock()
			reg.tokens++
			reg.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"token":%q}`, "anonymous-pull-"+r.URL.Query().Get("scope"))
			return
		}
		if r.Header.Get("Authorization") == "" {
			scopeRepo := ""
			for repo := range byRepo {
				if strings.Contains(r.URL.Path, repo) {
					scopeRepo = repo
					break
				}
			}
			w.Header().Set("Www-Authenticate", fmt.Sprintf(
				`Bearer realm="%s/token",service="grain-stand-in",scope="repository:%s:pull"`,
				reg.Server.URL, scopeRepo))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/v2/" || r.URL.Path == "/v2" {
			w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
			fmt.Fprint(w, "{}")
			return
		}
		for repo, image := range byRepo {
			if r.URL.Path == "/v2/"+repo+"/manifests/"+image.tag ||
				r.URL.Path == "/v2/"+repo+"/manifests/"+digestOf(image.manifest) {
				w.Header().Set("Content-Type", manifestMediaType)
				w.Header().Set("Docker-Content-Digest", digestOf(image.manifest))
				w.Header().Set("Content-Length", fmt.Sprint(len(image.manifest)))
				w.Write(image.manifest)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/v2/"+repo+"/blobs/") {
				digest := strings.TrimPrefix(r.URL.Path, "/v2/"+repo+"/blobs/")
				blob, ok := image.blobs[digest]
				if !ok {
					http.Error(w, "no such blob", http.StatusNotFound)
					return
				}
				reg.mu.Lock()
				reg.blobs++
				reg.mu.Unlock()
				w.Header().Set("Content-Type", image.mediaTypes[digest])
				w.Header().Set("Docker-Content-Digest", digest)
				w.Header().Set("Content-Length", fmt.Sprint(len(blob)))
				w.Write(blob)
				return
			}
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(reg.Close)
	return reg
}

// tokenFetches is how often the registry handed out an anonymous pull
// token. It is the cache that fetches them, not dockerd: dockerd speaks
// to a mirror unauthenticated, and everything upstream of that is this
// service's to deal with.
func (r *standInRegistry) tokenFetches() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tokens
}
