package cli

// Pointing the package cache at mirrors of this installation's own:
// the -package-cache-* flags, what they refuse, and -- the part worth a
// test rather than an assertion about a struct -- that a cache started
// from them really does fetch from where they say.
//
// The upstream fields have been in pkgcache.Config since the
// interception test needed a stand-in archive (grain/task-332), and
// until this nothing but a test could set one: a deployment fronting
// Debian, PyPI, npm, the module proxy, Docker Hub, ghcr.io or gcr.io
// with a mirror of its own had no way to say so. So what these tests
// are really about is the wiring between a command line and
// pkgcache.Config, which is the only part of it that was missing.
//
// Everything here runs against httptest servers on loopback and asks
// for no privilege: startPackageCache's interception half fails to bind
// 53, 443 and 80 in a test and says so in the log, exactly as it does
// on a deployment without CAP_NET_BIND_SERVICE, and the cache's own
// service comes up regardless. That is the half these flags are about.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/pkgcache"
)

// mirrorPaths is what the stand-in mirror holds: one object per
// ecosystem, under the path that ecosystem's half of the cache builds
// when its upstream is the mirror's own prefix.
var mirrorPaths = map[string]string{
	"/debian/pool/main/h/hello/hello_2.10_amd64.deb": "the .deb from the apt mirror",
	"/pypi/simple/hello/":                            "<html><body>the index from the PyPI mirror</body></html>",
	"/npm/hello":                                     `{"name":"hello","_from":"the npm mirror"}`,
	"/goproxy/example.com/m/@v/list":                 "v1.0.0 from the Go mirror",
	"/registry/v2/library/hello/manifests/latest":    "the manifest from the Docker mirror",
	"/ghcr/v2/bwsalmon/grain/manifests/latest":       "the manifest from the ghcr.io mirror",
	"/gcr/v2/distroless/base/manifests/latest":       "the manifest from the gcr.io mirror",
}

// startMirror serves those paths and records what was asked for, so
// that a test can tell "the mirror answered" from "the assertion passed
// because both sides happen to say the same thing".
//
// The record is taken under a mutex and handed back as a copy: the
// fetches a test makes through the cache are sequential, but they are
// served on the http.Server's own goroutines, and a slice appended to
// there and read here is a race whether or not this run's scheduling
// happens to hide it.
func startMirror(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Path)
		mu.Unlock()
		body, ok := mirrorPaths[r.URL.Path]
		if !ok {
			http.Error(w, "the stand-in mirror does not hold "+r.URL.Path, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), asked...)
	}
}

func TestAPackageCacheFetchesFromTheMirrorsTheFlagsName(t *testing.T) {
	t.Parallel()
	mirror, asked := startMirror(t)

	// The command line an installation with a mirror of its own writes,
	// through the config a `grain daemon` builds from it.
	cfg := daemon.Config{
		DataDir:                    t.TempDir(),
		PackageCacheGB:             1,
		PackageCacheAptUpstream:    []string{"deb.debian.org=" + mirror.URL + "/debian"},
		PackageCachePyPIIndex:      mirror.URL + "/pypi/simple",
		PackageCacheNPMRegistry:    mirror.URL + "/npm",
		PackageCacheGoProxy:        mirror.URL + "/goproxy",
		PackageCacheDockerRegistry: mirror.URL + "/registry",
		PackageCacheGHCRRegistry:   mirror.URL + "/ghcr",
		PackageCacheGCRRegistry:    mirror.URL + "/gcr",
	}
	if err := checkPackageCacheUpstreams(cfg); err != nil {
		t.Fatalf("a deployment naming seven reachable mirrors should start: %v", err)
	}
	pc, stop, err := daemon.StartPackageCache(cfg, daemon.NewDirect(""), nil)
	if err != nil {
		t.Fatalf("startPackageCache: %v", err)
	}
	defer stop(context.Background())

	// apt is the one asked for the way apt asks: an absolute-form
	// request through the proxy, naming the archive a sandbox still
	// resolves and still believes it is talking to.
	proxyURL, err := url.Parse(pc.Url)
	if err != nil {
		t.Fatalf("parsing the cache's own address %q: %v", pc.Url, err)
	}
	apt := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
	}
	assertServes(t, apt, "http://deb.debian.org/pool/main/h/hello/hello_2.10_amd64.deb", "the .deb from the apt mirror")

	// The other six are ordinary requests against the cache's own
	// service, on the paths this deployment points pip, npm, go and
	// dockerd at. The three registries are told apart by the /v2/ prefix
	// a pull names: an unprefixed repository is Docker Hub's, and
	// /v2/ghcr/ and /v2/gcr/ are the other two (parseDockerTarget).
	direct := &http.Client{Timeout: 30 * time.Second}
	for _, tc := range []struct{ what, path, want string }{
		{"pypi", "/pypi/simple/hello/", "the index from the PyPI mirror"},
		{"npm", "/npm/hello", "the npm mirror"},
		{"go", "/go/example.com/m/@v/list", "v1.0.0 from the Go mirror"},
		{"docker", "/v2/library/hello/manifests/latest", "the manifest from the Docker mirror"},
		{"ghcr", "/v2/ghcr/bwsalmon/grain/manifests/latest", "the manifest from the ghcr.io mirror"},
		{"gcr", "/v2/gcr/distroless/base/manifests/latest", "the manifest from the gcr.io mirror"},
	} {
		t.Run(tc.what, func(t *testing.T) {
			assertServes(t, direct, pc.Url+tc.path, tc.want)
		})
	}

	if got := len(asked()); got != len(mirrorPaths) {
		t.Errorf("the mirror was asked for %d paths (%v), want one per ecosystem (%d) -- an assertion that "+
			"passed without the mirror being reached is not a test of this wiring",
			got, asked(), len(mirrorPaths))
	}
}

// assertServes reads one fetch through the cache and insists the body
// came from the mirror rather than from anywhere else.
func assertServes(t *testing.T, client *http.Client, target, want string) {
	t.Helper()
	resp, err := client.Get(target)
	if err != nil {
		t.Fatalf("GET %s: %v", target, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s: %v", target, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", target, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if !strings.Contains(string(body), want) {
		t.Errorf("GET %s served %q, want something holding %q -- the fetch did not come from the mirror",
			target, strings.TrimSpace(string(body)), want)
	}
}

func TestAPackageCacheWithNoMirrorsNamedFetchesFromThePublicArchives(t *testing.T) {
	t.Parallel()
	// Every deployment that exists today, and nothing here may reach the
	// internet to prove it: what is asserted is that the config carries
	// no upstream at all, which is what pkgcache reads as "the defaults".
	up := daemon.PackageCacheUpstreamsFromConfig(daemon.Config{DataDir: t.TempDir(), PackageCacheGB: 1})
	if len(up.AptUpstream) != 0 || up.PyPIIndex != "" || up.NPMRegistry != "" || up.GoProxy != "" ||
		up.DockerRegistry != "" || up.GHCRRegistry != "" || up.GCRRegistry != "" {
		t.Errorf("a command line naming no mirror produced %+v, want every upstream empty", up)
	}
	if got := daemon.DescribePackageCacheUpstreams(up); got != "" {
		t.Errorf("describePackageCacheUpstreams = %q, want nothing to log", got)
	}
}

func TestPackageCacheUpstreamsCarryEveryFlagThroughToTheCache(t *testing.T) {
	t.Parallel()
	up := daemon.PackageCacheUpstreamsFromConfig(daemon.Config{
		PackageCacheAptUpstream: []string{
			"deb.debian.org=http://mirror.internal/debian",
			" security.debian.org = http://mirror.internal/security ",
		},
		PackageCachePyPIIndex:      " http://mirror.internal/pypi/simple ",
		PackageCacheNPMRegistry:    "http://mirror.internal/npm",
		PackageCacheGoProxy:        "http://athens.internal",
		PackageCacheDockerRegistry: "https://registry.internal",
		PackageCacheGHCRRegistry:   "https://registry.internal/ghcr",
		PackageCacheGCRRegistry:    " https://registry.internal/gcr ",
	})
	if got, want := up.AptUpstream["deb.debian.org"], "http://mirror.internal/debian"; got != want {
		t.Errorf("AptUpstream[deb.debian.org] = %q, want %q", got, want)
	}
	// pkgcache lower-cases the key and trims the value itself
	// (normalizedHostMap), so what matters here is only that an entry
	// written with spaces around it survived the trip from the flag.
	if got := up.AptUpstream["security.debian.org"]; !strings.Contains(got, "mirror.internal/security") {
		t.Errorf("a second -package-cache-apt-upstream produced %q, want the mirror it named", got)
	}
	if got, want := up.PyPIIndex, "http://mirror.internal/pypi/simple"; got != want {
		t.Errorf("PyPIIndex = %q, want %q", got, want)
	}
	if got, want := up.NPMRegistry, "http://mirror.internal/npm"; got != want {
		t.Errorf("NPMRegistry = %q, want %q", got, want)
	}
	if got, want := up.GoProxy, "http://athens.internal"; got != want {
		t.Errorf("GoProxy = %q, want %q", got, want)
	}
	if got, want := up.DockerRegistry, "https://registry.internal"; got != want {
		t.Errorf("DockerRegistry = %q, want %q", got, want)
	}
	if got, want := up.GHCRRegistry, "https://registry.internal/ghcr"; got != want {
		t.Errorf("GHCRRegistry = %q, want %q", got, want)
	}
	if got, want := up.GCRRegistry, "https://registry.internal/gcr"; got != want {
		t.Errorf("GCRRegistry = %q, want %q", got, want)
	}

	// What an operator reads in the journal, which is the only sign from
	// outside the process that a mirror took effect at all.
	described := daemon.DescribePackageCacheUpstreams(up)
	for _, want := range []string{"deb.debian.org from http://mirror.internal/debian", "PyPI from",
		"npm from", "Go modules from", "Docker Hub from https://registry.internal",
		"ghcr.io from https://registry.internal/ghcr", "gcr.io from https://registry.internal/gcr"} {
		if !strings.Contains(described, want) {
			t.Errorf("describePackageCacheUpstreams = %q, want it to name %q", described, want)
		}
	}
}

func TestCheckConfigRefusesAMirrorNothingWouldEverFetchFrom(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		with func(*daemon.Config)
		want string
	}{
		{
			"an apt upstream that is not a pair",
			func(c *daemon.Config) { c.PackageCacheAptUpstream = []string{"http://mirror.internal/debian"} },
			"is not an \"archive-host=base-url\" pair",
		},
		{
			"an apt upstream with no host",
			func(c *daemon.Config) { c.PackageCacheAptUpstream = []string{"=http://mirror.internal/debian"} },
			"is not an \"archive-host=base-url\" pair",
		},
		{
			"an apt upstream for an archive this cache does not serve",
			func(c *daemon.Config) {
				c.PackageCacheAptUpstream = []string{"packages.example.com=http://mirror.internal/x"}
			},
			"which this cache does not serve",
		},
		{
			"an apt upstream that is not a URL",
			func(c *daemon.Config) { c.PackageCacheAptUpstream = []string{"deb.debian.org=mirror.internal/debian"} },
			"is not an http:// or https:// URL",
		},
		{
			"a PyPI index that is not a URL",
			func(c *daemon.Config) { c.PackageCachePyPIIndex = "mirror.internal/pypi/simple" },
			"-package-cache-pypi-index",
		},
		{
			"an npm registry with no host",
			func(c *daemon.Config) { c.PackageCacheNPMRegistry = "http:///npm" },
			"-package-cache-npm-registry",
		},
		{
			"a Go proxy that is not HTTP at all",
			func(c *daemon.Config) { c.PackageCacheGoProxy = "file:///srv/athens" },
			"-package-cache-go-proxy",
		},
		{
			"a Docker registry that is not a URL",
			func(c *daemon.Config) { c.PackageCacheDockerRegistry = "registry.internal" },
			"-package-cache-docker-registry",
		},
		{
			"a ghcr.io registry that is not a URL",
			func(c *daemon.Config) { c.PackageCacheGHCRRegistry = "registry.internal/ghcr" },
			"-package-cache-ghcr-registry",
		},
		{
			"a gcr.io registry with no host",
			func(c *daemon.Config) { c.PackageCacheGCRRegistry = "https:///gcr" },
			"-package-cache-gcr-registry",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := startable()
			tc.with(&cfg)
			err := checkConfig(cfg)
			if err == nil {
				t.Fatalf("checkConfig accepted %s: a mirror nothing can fetch from is a cache that goes on "+
					"fetching from the internet with nothing to say so", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("checkConfig said %q, want it to name %q so an operator knows which flag to fix",
					err, tc.want)
			}
		})
	}
}

func TestCheckConfigAcceptsTheMirrorsAnInstallationActuallyHas(t *testing.T) {
	t.Parallel()
	cfg := startable()
	cfg.PackageCacheAptUpstream = []string{
		"deb.debian.org=http://mirror.internal/debian",
		// A regional mirror matches by suffix rather than by name
		// (DefaultAptHosts' ".debian.org"), and so does an archive on a
		// port of its own.
		"ftp.uk.debian.org=https://mirror.internal:8443/debian",
		"archive.ubuntu.com=http://mirror.internal/ubuntu",
	}
	cfg.PackageCachePyPIIndex = "http://mirror.internal/pypi/simple"
	cfg.PackageCacheNPMRegistry = "http://mirror.internal/npm"
	cfg.PackageCacheGoProxy = "http://athens.internal"
	cfg.PackageCacheDockerRegistry = "https://registry.internal"
	cfg.PackageCacheGHCRRegistry = "https://registry.internal/ghcr"
	cfg.PackageCacheGCRRegistry = "https://registry.internal/gcr"
	if err := checkConfig(cfg); err != nil {
		t.Fatalf("checkConfig refused a deployment with mirrors it can reach: %v", err)
	}
}

// The apt half's served list and its upstream list are different lists,
// and a deployment naming a mirror for a host in neither has configured
// something no sandbox will ever provoke. ServesAptHost is what says so
// while the daemon is starting.
func TestServesAptHostAnswersForTheHostsSandboxesAskFor(t *testing.T) {
	t.Parallel()
	for host, want := range map[string]bool{
		"deb.debian.org":          true,
		"DEB.debian.org:80":       true,
		"ftp.uk.debian.org":       true,
		"archive.ubuntu.com":      true,
		"packages.example.com":    false,
		"deb.debian.org.evil.com": false,
	} {
		if got := pkgcache.ServesAptHost(host, nil); got != want {
			t.Errorf("ServesAptHost(%q) = %v, want %v", host, got, want)
		}
	}
}
