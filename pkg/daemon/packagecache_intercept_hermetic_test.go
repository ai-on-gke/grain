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
// docker bridge -- and against nothing else. This is the test grain CI
// runs (.grain/workflows/tests.yml, the `package cache` job); its
// neighbour in packagecache_intercept_real_test.go asks the same four
// questions of the real Debian archive and is run by hand.
//
// The four questions are the ones an operator would ask from inside a
// fresh sandbox, and every part of each of them happens outside the
// process under test: the kernel refuses a privileged bind to an
// unprivileged daemon, a container's own resolver is docker's until
// something overwrites it, and a certificate this deployment's CA minted
// is worth nothing until the trust store in that container has been
// rebuilt around it. So startPackageCacheFrom binds the listeners,
// unchanged and unmocked, on this machine's docker bridge, and
// orchestrator.ConfigurePackageCacheTransparent -- the function a
// dispatch calls -- configures a container on that bridge.
//
// What is different here is only where the answers come from. Nothing in
// this test reaches the internet, which is the rule the `package cache`
// job is built on: its verdict has to be about this commit rather than
// about whether a CI sandbox can reach Docker Hub or an archive today.
// Three things would otherwise have reached out, and each has a stand-in
// on this machine's own addresses:
//
//   - The archive. deb.debian.org is still what the sandbox asks for and
//     what the resolver intercepts; where those requests are *fetched*
//     from is pkgcache.Config's AptUpstream, pointed at an httptest
//     server holding one index and two .debs (packageCacheUpstreams is
//     how startPackageCache takes it).
//   - The upstream resolver. A name this service does not intercept is
//     forwarded to whatever /etc/resolv.conf says, which is the half of
//     interception that breaks a sandbox catastrophically rather than
//     invisibly, so it is tested -- against a nameserver of this test's
//     own on loopback (DNSConfig.UpstreamDNS, likewise a field pkgcache
//     already has).
//   - The sandbox image. There is no `docker pull` and no `FROM debian`:
//     the container is imported from a tar this test builds out of this
//     machine's own /bin/sh and the handful of coreutils
//     ConfigurePackageCacheTransparent's scripts run, plus one static Go
//     binary compiled here that does the asking. That binary is curl and
//     getent both -- see interceptProbeSource.
//
// It needs a dockerd and the privilege to bind 53, 443 and 80 (that is
// the whole subject), so it is gated on GRAIN_REAL_INTERCEPT_TESTS like
// its neighbour rather than running inside `go test ./...` (put the
// binary in /var/tmp rather than /tmp, which systemd mounts nosuid so the
// kernel ignores file capabilities from setcap):
//
//	go test -c -o /var/tmp/grain.test ./pkg/daemon
//	sudo setcap cap_net_bind_service=+ep /var/tmp/grain.test
//	GRAIN_REAL_INTERCEPT_TESTS=1 /var/tmp/grain.test -test.v \
//	    -test.run TestInterceptionServesASandboxFromStandInArchives
//
// Nothing outside the container and the test's own temporary directory is
// touched: the listeners are this test's, not a daemon's, and both the
// container and the image are removed however the test ends.

import (
	"archive/tar"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// What the stand-in archive holds, under the names a real one would use:
// one index, which is forwarded and never stored, and two packages,
// which are stored. Two rather than one because a sandbox reaches an
// intercepted archive over both schemes -- https on 443 and plain http
// on 80 -- and the two listeners are bound together and can break apart.
const (
	standInIndexPath    = "/debian/dists/trixie/InRelease"
	standInHTTPSDebPath = "/debian/pool/main/c/cpio/cpio_2.15+grain1_amd64.deb"
	standInHTTPDebPath  = "/debian/pool/main/h/hello/hello_2.10+grain1_amd64.deb"
	// The two ways a sandbox reaches an intercepted archive: the name is
	// Debian's, the address is this deployment, and which of the two
	// listeners answers is the scheme's doing.
	interceptedArchiveHTTPS = "https://deb.debian.org"
	interceptedArchiveHTTP  = "http://deb.debian.org"
)

// forwardedName is a name the cache does not intercept, and
// forwardedAddr is what this test's stand-in nameserver answers for it.
// The address is from TEST-NET-1 (RFC 5737), so that nothing can reach
// it even if something tried.
const (
	forwardedName = "an-address-this-deployment-does-not-serve.grain.test"
	forwardedAddr = "192.0.2.10"
)

func TestInterceptionServesASandboxFromStandInArchives(t *testing.T) {
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

	archive := startStandInArchive(t)
	resolver := startStandInResolver(t)

	// The daemon's own startup path, with the config a container-sandboxes
	// deployment has -- and with the two upstreams pointed at this
	// machine instead of at the internet.
	cfg := Config{
		DataDir:               t.TempDir(),
		PackageCacheGB:        1,
		ContainerSandboxes:    true,
		ContainerGitProxyHost: gateway,
		PackageCacheEnabled:   true,
	}
	// The fabric is built from that config exactly as run() builds it, so
	// the cache's own URL and its interception pair describe one machine.
	pc, stop, err := startPackageCacheFrom(cfg, NewDirect(SandboxAdvertiseHost(cfg)), packageCacheUpstreams{
		AptUpstream: map[string]string{"deb.debian.org": archive.srv.URL},
		DNS:         []string{resolver.addr},
	}, nil)
	if err != nil {
		t.Fatalf("startPackageCacheFrom: %v", err)
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

	image := buildInterceptProbeImage(t)
	sandbox := startInterceptTestContainer(t, image, "/probe", "idle")

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
	if got := sandboxResolvesTo(t, sandbox, "deb.debian.org"); got != gateway {
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
	if got := sandboxResolvesTo(t, sandbox, forwardedName); got != forwardedAddr {
		t.Errorf("%s resolves to %q inside the sandbox, want %s from the upstream nameserver -- a "+
			"non-intercepted name has to be forwarded and answered with the real address "+
			"(ZeroEgress is false)", forwardedName, got, forwardedAddr)
	} else {
		t.Logf("%s resolves to %s, forwarded upstream", forwardedName, forwardedAddr)
	}

	// 3. An https fetch of an intercepted domain is served by this cache,
	// under a certificate it minted, which the sandbox's trust store
	// accepts. The body is the proof of the first part: the root of the
	// service answers with what it is, so a sandbox that believes it is
	// talking to deb.debian.org and is told "grain proxy cache" has
	// been intercepted, decrypted and served here.
	root := sandboxFetch(t, sandbox, interceptedArchiveHTTPS+"/")
	if root.status != http.StatusOK || !strings.Contains(root.body, "grain proxy cache") {
		t.Errorf("GET %s/ from the sandbox answered %d:\n%s\nwant the proxy cache's own "+
			"description -- it was not served here", interceptedArchiveHTTPS, root.status, root.body)
	} else {
		t.Logf("GET %s/ was served by the cache, under its own CA", interceptedArchiveHTTPS)
	}

	// 4. And an archive fetch through it works, over both schemes: the
	// index forwarded and never stored, the packages stored, and a second
	// fetch of a package answered without asking the archive again.
	before := aptEntries(t, pc)
	index := sandboxFetch(t, sandbox, interceptedArchiveHTTPS+standInIndexPath)
	if index.status != http.StatusOK || index.sha256 != archive.sha256(standInIndexPath) {
		t.Errorf("fetching the archive's InRelease through the cache answered %d with %s, want 200 "+
			"with %s -- the index is forwarded to the archive unchanged",
			index.status, index.sha256, archive.sha256(standInIndexPath))
	}
	if after := aptEntries(t, pc); after != before {
		t.Errorf("the cache holds %d apt entries after fetching an index and held %d before -- an "+
			"index must never be stored", after, before)
	}

	for _, fetch := range []struct {
		what string
		url  string
		path string
	}{
		{"https, on the 443 listener", interceptedArchiveHTTPS + standInHTTPSDebPath, standInHTTPSDebPath},
		{"plain http, on the 80 listener", interceptedArchiveHTTP + standInHTTPDebPath, standInHTTPDebPath},
	} {
		held := aptEntries(t, pc)
		got := sandboxFetch(t, sandbox, fetch.url)
		if got.status != http.StatusOK || got.sha256 != archive.sha256(fetch.path) {
			t.Errorf("fetching a package over %s answered %d with %s, want 200 with the archive's own "+
				"%s", fetch.what, got.status, got.sha256, archive.sha256(fetch.path))
			continue
		}
		if after := aptEntries(t, pc); after != held+1 {
			t.Errorf("the cache holds %d apt entries after that package was fetched over %s and held "+
				"%d before -- it went somewhere else", after, fetch.what, held)
		}
		again := sandboxFetch(t, sandbox, fetch.url)
		if again.sha256 != got.sha256 {
			t.Errorf("fetching that package over %s a second time answered %s, want the same %s",
				fetch.what, again.sha256, got.sha256)
		}
		if hits := archive.hits(fetch.path); hits != 1 {
			t.Errorf("the archive was asked for that package %d times over %s, want 1 -- the second "+
				"fetch has to be served from the store", hits, fetch.what)
		} else {
			t.Logf("a package fetched over %s was stored, and the second fetch of it never left this "+
				"deployment", fetch.what)
		}
	}
}

// standInArchive is a distribution archive on this machine's loopback:
// whatever deb.debian.org is pointed at for the length of this test. It
// counts what was actually fetched from it, which is how a cache hit is
// told from a second download.
type standInArchive struct {
	srv   *httptest.Server
	files map[string][]byte

	mu     sync.Mutex
	counts map[string]int
}

func startStandInArchive(t *testing.T) *standInArchive {
	t.Helper()
	archive := &standInArchive{
		files: map[string][]byte{
			standInIndexPath:    []byte("Origin: grain\nSuite: trixie\nDate: whenever\n"),
			standInHTTPSDebPath: []byte("the bytes of a package fetched over https\n"),
			standInHTTPDebPath:  []byte("the bytes of a package fetched over plain http\n"),
		},
		counts: map[string]int{},
	}
	archive.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := archive.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		archive.mu.Lock()
		archive.counts[r.URL.Path]++
		archive.mu.Unlock()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}))
	t.Cleanup(archive.srv.Close)
	return archive
}

func (a *standInArchive) hits(path string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.counts[path]
}

// sha256 is what a sandbox that really got this file would report.
func (a *standInArchive) sha256(path string) string {
	sum := sha256.Sum256(a.files[path])
	return hex.EncodeToString(sum[:])
}

// standInResolver is the nameserver the cache's own resolver forwards to
// -- this deployment's upstream DNS, for the length of this test. It
// answers for one name and nothing else, so that a test of "a
// non-intercepted name is still answered" is not also a test of whether
// this machine can resolve anything on the internet.
type standInResolver struct {
	addr string
	conn *net.UDPConn
}

func startStandInResolver(t *testing.T) *standInResolver {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("binding the stand-in upstream nameserver: %v", err)
	}
	resolver := &standInResolver{addr: conn.LocalAddr().String(), conn: conn}
	t.Cleanup(func() { _ = conn.Close() })
	go resolver.serve()
	return resolver
}

func (r *standInResolver) serve() {
	buf := make([]byte, 4096)
	for {
		n, from, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if answer := standInDNSAnswer(buf[:n]); len(answer) > 0 {
			_, _ = r.conn.WriteToUDP(answer, from)
		}
	}
}

// standInDNSAnswer answers an A query for forwardedName with
// forwardedAddr, an empty NOERROR for any other question about that name
// (a Go resolver asks for AAAA as well, and an error there is an error
// for the lookup), and NXDOMAIN for every other name.
func standInDNSAnswer(query []byte) []byte {
	var msg dnsmessage.Message
	if err := msg.Unpack(query); err != nil || len(msg.Questions) == 0 {
		return nil
	}
	question := msg.Questions[0]
	name := strings.TrimSuffix(strings.ToLower(question.Name.String()), ".")
	response := dnsmessage.Message{
		Header: dnsmessage.Header{
			ID:                 msg.Header.ID,
			Response:           true,
			OpCode:             msg.Header.OpCode,
			Authoritative:      true,
			RecursionDesired:   msg.Header.RecursionDesired,
			RecursionAvailable: true,
		},
		Questions: []dnsmessage.Question{question},
	}
	switch {
	case name != forwardedName:
		response.Header.RCode = dnsmessage.RCodeNameError
	case question.Type == dnsmessage.TypeA:
		ip := net.ParseIP(forwardedAddr).To4()
		response.Answers = []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{
				Name:  question.Name,
				Type:  dnsmessage.TypeA,
				Class: dnsmessage.ClassINET,
				TTL:   60,
			},
			Body: &dnsmessage.AResource{A: [4]byte{ip[0], ip[1], ip[2], ip[3]}},
		}}
	}
	packed, err := response.Pack()
	if err != nil {
		return nil
	}
	return packed
}

// sandboxResolvesTo is one name resolved from inside the sandbox,
// reduced to the first address it answered with -- which is what every
// client in there will use, and the thing the resolv.conf grain wrote
// decides.
//
// The probe asks with Go's own resolver rather than with getent, which
// is what the by-hand test uses: a container built out of static
// binaries has no libc resolver in it. Both read /etc/resolv.conf, which
// is the file under test.
func sandboxResolvesTo(t *testing.T, sandbox *interceptSandbox, name string) string {
	t.Helper()
	for _, line := range strings.Split(probeRun(t, sandbox, 1*time.Minute, "resolve", name), "\n") {
		if address, ok := strings.CutPrefix(strings.TrimSpace(line), "address: "); ok {
			return address
		}
	}
	return ""
}

// probeResult is one HTTP fetch made from inside the sandbox.
type probeResult struct {
	status int
	sha256 string
	body   string
}

// sandboxFetch is one GET from inside the sandbox, over the sandbox's
// own trust store and the sandbox's own resolver.
func sandboxFetch(t *testing.T, sandbox *interceptSandbox, url string) probeResult {
	t.Helper()
	out := probeRun(t, sandbox, 2*time.Minute, "get", url)
	result := probeResult{}
	rest := out
	if head, body, ok := strings.Cut(out, probeBodyMarker+"\n"); ok {
		rest, result.body = head, body
	}
	for _, line := range strings.Split(rest, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "status: "):
			result.status, _ = strconv.Atoi(strings.TrimPrefix(line, "status: "))
		case strings.HasPrefix(line, "sha256: "):
			result.sha256 = strings.TrimPrefix(line, "sha256: ")
		}
	}
	if result.status == 0 {
		t.Fatalf("GET %s from inside the sandbox reported no status:\n%s", url, out)
	}
	return result
}

// probeRun runs the probe inside the sandbox and returns what it
// printed, failing the test if it did not succeed.
func probeRun(t *testing.T, sandbox *interceptSandbox, timeout time.Duration, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	stdout, stderr, code := sandbox.RunInSandbox(ctx, append([]string{"/probe"}, args...), "")
	if code != 0 {
		t.Fatalf("`probe %s` inside the sandbox exited %d:\n%s\n%s",
			strings.Join(args, " "), code, stdout, stderr)
	}
	return stdout + stderr
}

// buildInterceptProbeImage builds the sandbox stand-in: a container
// image with no base image behind it, so that this test pulls nothing
// and depends on no registry.
//
// What goes in is the least that can be configured by
// ConfigurePackageCacheTransparent and then asked a question: this
// machine's /bin/sh and the coreutils its scripts run (with the shared
// libraries they are linked against), a stand-in for the trust store
// rebuild, and the probe.
func buildInterceptProbeImage(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	probe := compileInterceptProbe(t, dir)
	rootfs := filepath.Join(dir, "rootfs.tar")
	writeInterceptRootfs(t, rootfs, probe)

	image := fmt.Sprintf("grain-intercept-probe:%d", time.Now().UnixNano())
	out, err := exec.Command("docker", "import",
		"--change", "ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		rootfs, image).CombinedOutput()
	if err != nil {
		t.Fatalf("importing the sandbox stand-in image: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command("docker", "rmi", "-f", image).CombinedOutput(); err != nil {
			t.Logf("removing the sandbox stand-in image: %v\n%s", err, out)
		}
	})
	return image
}

// compileInterceptProbe builds the static binary that does the asking
// from inside the sandbox, and returns where it is.
func compileInterceptProbe(t *testing.T, dir string) string {
	t.Helper()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module grainprobe\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatalf("writing the probe's go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "main.go"), []byte(interceptProbeSource), 0o644); err != nil {
		t.Fatalf("writing the probe's source: %v", err)
	}
	binary := filepath.Join(dir, "probe")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = source
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOFLAGS=", "GOPROXY=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compiling the probe that runs inside the sandbox: %v\n%s", err, out)
	}
	return binary
}

// interceptRootfsBinaries is what has to be in the container besides the
// probe: /bin/sh for `docker exec sh -c`, and every command
// orchestrator's own configuration scripts run (writeAsRootScript,
// writeInPlaceScript, readAsRootScript, configureToolScript).
//
// `sudo`, `systemctl`, `go` and `npm` are deliberately absent: each of
// those paths is the one a script takes when the command is missing, and
// a sandbox that is already root takes it.
var interceptRootfsBinaries = []string{
	"/bin/sh",
	"/usr/bin/id",
	"/usr/bin/dirname",
	"/usr/bin/mkdir",
	"/usr/bin/install",
	"/usr/bin/cat",
}

// interceptRootfsUpdateCACertificates stands in for Debian's
// update-ca-certificates, which is a package and a perl script away from
// a container this size. It does the only part of that program's job
// this test depends on, and the part a real sandbox depends on too:
// everything dropped in /usr/local/share/ca-certificates -- which is
// where pkgcache.CACertPath puts grain's root -- becomes the bundle a
// TLS client reads.
const interceptRootfsUpdateCACertificates = `#!/bin/sh
set -eu
mkdir -p /etc/ssl/certs
: > /etc/ssl/certs/ca-certificates.crt
for cert in /usr/local/share/ca-certificates/*.crt; do
	[ -f "$cert" ] || continue
	cat "$cert" >> /etc/ssl/certs/ca-certificates.crt
done
`

// writeInterceptRootfs writes the tar `docker import` turns into the
// image: the binaries above at the paths they have here, the shared
// libraries they are linked against at the paths their loader will look
// for them, and the few files a container needs to be one.
func writeInterceptRootfs(t *testing.T, path, probe string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating the rootfs tar: %v", err)
	}
	defer file.Close()
	archive := tar.NewWriter(file)

	written := map[string]bool{}
	addFile := func(name, source string, mode int64) {
		if written[name] {
			return
		}
		written[name] = true
		content, err := os.ReadFile(source)
		if err != nil {
			t.Fatalf("reading %s for the sandbox stand-in image: %v", source, err)
		}
		addRootfsEntry(t, archive, written, name, content, mode)
	}

	for _, binary := range interceptRootfsBinaries {
		resolved, err := filepath.EvalSymlinks(binary)
		if err != nil {
			t.Skipf("this machine has no %s to build a sandbox stand-in out of: %v", binary, err)
		}
		addFile(binary, resolved, 0o755)
		for _, library := range sharedLibraries(t, resolved) {
			addFile(library, library, 0o755)
		}
	}
	addFile("/probe", probe, 0o755)
	addRootfsEntry(t, archive, written, "/usr/sbin/update-ca-certificates",
		[]byte(interceptRootfsUpdateCACertificates), 0o755)
	// A pure-Go resolver reads nsswitch.conf to decide whether to ask a
	// nameserver at all, so the container says what a Debian one does.
	addRootfsEntry(t, archive, written, "/etc/nsswitch.conf", []byte("hosts: files dns\n"), 0o644)
	addRootfsEntry(t, archive, written, "/etc/hosts", []byte("127.0.0.1 localhost\n"), 0o644)
	for _, dir := range []string{"/etc/ssl/certs", "/usr/local/share/ca-certificates", "/tmp"} {
		addRootfsDir(t, archive, written, dir, 0o755)
	}

	if err := archive.Close(); err != nil {
		t.Fatalf("finishing the rootfs tar: %v", err)
	}
}

// addRootfsEntry writes one file and the directories above it.
func addRootfsEntry(t *testing.T, archive *tar.Writer, written map[string]bool, name string, content []byte, mode int64) {
	t.Helper()
	addRootfsDir(t, archive, written, filepath.Dir(name), 0o755)
	header := &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     strings.TrimPrefix(name, "/"),
		Mode:     mode,
		Size:     int64(len(content)),
		ModTime:  time.Unix(0, 0),
	}
	if err := archive.WriteHeader(header); err != nil {
		t.Fatalf("writing %s into the rootfs tar: %v", name, err)
	}
	if _, err := archive.Write(content); err != nil {
		t.Fatalf("writing %s into the rootfs tar: %v", name, err)
	}
}

// addRootfsDir writes one directory and the directories above it.
func addRootfsDir(t *testing.T, archive *tar.Writer, written map[string]bool, name string, mode int64) {
	t.Helper()
	name = filepath.Clean(name)
	if name == "/" || name == "." || written[name] {
		return
	}
	written[name] = true
	addRootfsDir(t, archive, written, filepath.Dir(name), mode)
	header := &tar.Header{
		Typeflag: tar.TypeDir,
		Name:     strings.TrimPrefix(name, "/") + "/",
		Mode:     mode,
		ModTime:  time.Unix(0, 0),
	}
	if err := archive.WriteHeader(header); err != nil {
		t.Fatalf("writing the directory %s into the rootfs tar: %v", name, err)
	}
}

// sharedLibraries is what a binary is dynamically linked against, the
// loader included, at the absolute paths the loader will look for them
// at -- `ldd` said in Go. A static binary has none and is not an error.
func sharedLibraries(t *testing.T, binary string) []string {
	t.Helper()
	out, err := exec.Command("ldd", binary).CombinedOutput()
	if err != nil {
		// `ldd` says "not a dynamic executable" and exits non-zero for a
		// static binary, which is a fine thing for one of these to be.
		if strings.Contains(string(out), "not a dynamic executable") {
			return nil
		}
		t.Skipf("ldd %s: %v\n%s -- this machine cannot be taken apart into a container", binary, err, out)
	}
	var libraries []string
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		for i, field := range fields {
			// "libc.so.6 => /lib/x86_64-linux-gnu/libc.so.6 (0x...)", and
			// the loader itself, which is named with no "=>" before it.
			if field == "=>" && i+1 < len(fields) && strings.HasPrefix(fields[i+1], "/") {
				libraries = append(libraries, fields[i+1])
				break
			}
			if i == 0 && strings.HasPrefix(field, "/") {
				libraries = append(libraries, field)
				break
			}
		}
	}
	return libraries
}

// probeBodyMarker separates what the probe reports about a response from
// the response itself.
const probeBodyMarker = "--- body ---"

// interceptProbeSource is the program the sandbox asks its questions
// with: curl and getent in one static binary, so that the container
// needs no base image and no package install.
//
// It reports rather than judges -- the test does the judging -- and it
// does both fetches the ordinary way, through the default HTTP client
// and the system trust store, because what is under test is whether an
// ordinary client in a configured sandbox is served by the cache.
const interceptProbeSource = `package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: probe idle | resolve <name> | get <url>")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "idle":
		// The container's own process: something for docker exec to
		// happen inside of, and nothing else.
		time.Sleep(30 * time.Minute)
	case "resolve":
		addresses, err := net.LookupHost(os.Args[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "resolving %s: %v\n", os.Args[2], err)
			os.Exit(1)
		}
		for _, address := range addresses {
			fmt.Printf("address: %s\n", address)
		}
	case "get":
		client := &http.Client{Timeout: 2 * time.Minute}
		response, err := client.Get(os.Args[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "GET %s: %v\n", os.Args[2], err)
			os.Exit(1)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reading %s: %v\n", os.Args[2], err)
			os.Exit(1)
		}
		sum := sha256.Sum256(body)
		fmt.Printf("status: %d\n", response.StatusCode)
		fmt.Printf("bytes: %d\n", len(body))
		fmt.Printf("sha256: %s\n", hex.EncodeToString(sum[:]))
		fmt.Printf("` + probeBodyMarker + `\n%s", body)
	default:
		fmt.Fprintf(os.Stderr, "probe: no such command %q\n", os.Args[1])
		os.Exit(2)
	}
}
`
