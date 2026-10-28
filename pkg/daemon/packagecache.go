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

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/pkgcache"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// packageCache is this process's cache: the store, and where a sandbox
// reaches the service in front of it.
type PackageCache struct {
	// cache is the store as this wiring administers it -- stats, flush and
	// its bound -- and dir where it keeps its files. The Server in front
	// of it holds it as a pkgcache.Store; nothing past the builder below
	// names the *pkgcache.Cache (docs/design/grit-split.md#GS-1).
	cache pkgcache.Admin
	dir   string
	// credentials is what registry pulls authenticate with; its
	// Connections are set once the store is open (SetGitConnections).
	credentials *gitproxy.CredentialSet
	Ca          *pkgcache.CA
	dnsServer   *pkgcache.DNSServer
	Url         string
	DnsIP       string
	// dnsServing is whether interception is up as a whole: the DNS
	// server bound and answering on dnsIP, *and* the two listeners its
	// answers send a fetch to bound on that same address (see
	// startTransparentServers). A sandbox is told to resolve through
	// dnsIP only when all three are there: a sandbox pointed at a
	// nameserver nobody is listening on has no name resolution at all,
	// and one whose deb.debian.org resolves to an address with nothing
	// on port 443 cannot fetch a package at all -- either is far worse
	// than a sandbox with no interception (see sandboxDNS).
	DnsServing bool
}

// packageCacheDNSAddrs is where the DNS server binds, and the address
// each sandbox's /etc/resolv.conf is pointed at.
//
// Both come out of the one function, deliberately: the bug this exists
// to prevent is the two being written as independent literals and
// drifting apart, which is exactly what left every sandbox resolving
// through 172.17.0.1 while the server sat on 127.0.0.1:53. The bind is
// the advertised address itself rather than the wildcard, so the
// resolver is reachable from sandboxes and from nowhere else.
func PackageCacheDNSAddrs(advertise string) (listenAddr, dnsIP string) {
	dnsIP = "172.17.0.1"
	if advertise != "" {
		dnsIP = advertise
	}
	return net.JoinHostPort(dnsIP, "53"), dnsIP
}

// packageCacheTransparentAddrs is where the cache answers the fetches
// interception sends it: port 443 and port 80 of the very address the
// resolver hands out.
//
// A DNS answer is only half of interception. Once a sandbox resolves
// deb.debian.org to 172.17.0.1, the fetch that follows arrives on
// 172.17.0.1:443 -- every archive, index and registry this service
// serves speaks TLS there, which is why the apt configuration grain
// writes sends https for those hosts DIRECT rather than through the
// proxy (pkg/pkgcache/sandbox.go) -- or, for a plain http:// source, on
// 172.17.0.1:80. Neither of those is the cache's own HTTP service: that
// is on an ephemeral port, which a client that resolved a hostname has
// no way of knowing about.
//
// Both are bound on the advertised address rather than on the wildcard,
// exactly as the resolver is: what has to be reachable is that address,
// and a wildcard bind would take the machine's own 80 and 443 away from
// whatever else wants them -- a deployment installed before the
// 0.0.0.0:4242 default may still serve its UI on 127.0.0.1:80, and the
// UI server starts before this does.
func PackageCacheTransparentAddrs(advertise string) (httpsAddr, httpAddr string) {
	return net.JoinHostPort(advertise, "443"), net.JoinHostPort(advertise, "80")
}

// transparentServers is a started pair: where each half actually bound,
// and how to stop them both. The addresses are read back off the
// listeners rather than repeated from the request, so that what gets
// logged is what a sandbox can reach -- the same rule
// packageCacheDNSAddrs exists for.
type transparentServers struct {
	HttpsAddr string
	HttpAddr  string
	Shutdown  func(context.Context) error
}

// startTransparentServers binds those two listeners and serves handler
// on both, TLS on 443 under the cache's own CA. It reports the first
// bind that failed, having released the other.
//
// Both or neither, deliberately. They are advertised together with the
// resolver (see dnsServing) because a sandbox cannot be told to
// intercept some schemes and not others: resolv.conf names a nameserver,
// and from that moment every fetch of an intercepted domain -- http and
// https alike -- goes wherever that nameserver says. Half of the
// interception is a sandbox whose plain-HTTP archive fetches fail
// outright, where no interception at all would have left it downloading
// from the internet as it always did.
// It takes the two addresses rather than working them out from the
// advertised one, so that a test can drive it on ephemeral ports;
// packageCacheTransparentAddrs is where the real pair comes from.
func StartTransparentServers(httpsAddr, httpAddr string, handler http.Handler, ca *pkgcache.CA) (*transparentServers, error) {
	httpsLn, err := net.Listen("tcp", httpsAddr)
	if err != nil {
		return nil, err
	}
	httpLn, err := net.Listen("tcp", httpAddr)
	if err != nil {
		_ = httpsLn.Close()
		return nil, err
	}
	httpsSrv := &http.Server{Handler: handler, TLSConfig: ca.TLSConfig()}
	httpSrv := &http.Server{Handler: handler}
	started := &transparentServers{
		HttpsAddr: httpsLn.Addr().String(),
		HttpAddr:  httpLn.Addr().String(),
		Shutdown: func(ctx context.Context) error {
			_ = httpsSrv.Shutdown(ctx)
			_ = httpSrv.Shutdown(ctx)
			return nil
		},
	}
	go func() {
		if err := httpsSrv.ServeTLS(httpsLn, "", ""); err != nil && err != http.ErrServerClosed {
			log.Printf("grain daemon: package cache HTTPS on %s: %v", started.HttpsAddr, err)
		}
	}()
	go func() {
		if err := httpSrv.Serve(httpLn); err != nil && err != http.ErrServerClosed {
			log.Printf("grain daemon: package cache transparent HTTP on %s: %v", started.HttpAddr, err)
		}
	}()
	return started, nil
}

// packageCacheUpstreams is where the cache fetches what it does not
// already hold, for the callers that do not want the public archives.
//
// Two of those. An installation that already fronts Debian, PyPI, npm,
// the Go module proxy, Docker Hub, ghcr.io or gcr.io with a mirror of
// its own -- a caching proxy, an internal apt-mirror, a regional mirror
// on a private network, a pull-through registry -- would rather this
// cache filled itself from there than from the internet, and says so
// with the -package-cache-* flags
// (packageCacheUpstreamsFromConfig, which is what startPackageCache
// passes). And a test that drives interception end to end would
// otherwise be a test of this machine's egress as much as of the
// feature: both halves of interception reach outside this process -- an
// archive fetch to the archive, a name this service does not intercept
// to whatever /etc/resolv.conf says -- so cmd/grain's interception test
// points them at stand-ins on loopback instead.
//
// The fields are exactly pkgcache's own, and are documented there.
type packageCacheUpstreams struct {
	// AptUpstream is pkgcache.Config.AptUpstream: where an intercepted
	// archive hostname's fetches actually go, keyed by the hostname a
	// sandbox asked for. An archive not named here is fetched from
	// itself.
	AptUpstream map[string]string
	// PyPIIndex is pkgcache.Config.PyPIIndex: the simple index this
	// service proxies, DefaultPyPIIndex when empty.
	PyPIIndex string
	// NPMRegistry is pkgcache.Config.NPMRegistry: the npm registry this
	// service proxies, DefaultNPMRegistry when empty.
	NPMRegistry string
	// GoProxy is pkgcache.Config.GoProxy: the module proxy this service
	// forwards to, DefaultGoProxy when empty.
	GoProxy string
	// DockerRegistry is pkgcache.Config.DockerRegistry: the registry
	// this service mirrors for Docker Hub, DefaultDockerRegistry when
	// empty.
	DockerRegistry string
	// GHCRRegistry and GCRRegistry are pkgcache.Config's fields of the
	// same names: the registries this service mirrors for ghcr.io and
	// gcr.io, DefaultGHCRRegistry and DefaultGCRRegistry when empty.
	// They take exactly DockerRegistry's shape -- one URL each, and a
	// pull-through cache of the installation's own behind it.
	GHCRRegistry string
	GCRRegistry  string
	// DNS is pkgcache.DNSConfig.UpstreamDNS: the resolvers a name this
	// service does not intercept is forwarded to. Empty means the host's
	// own, which is what a deployment runs with -- there is no flag for
	// this one, only the test.
	DNS []string
}

// packageCacheUpstreamsFromConfig is the upstreams a `grain daemon`
// command line asked for: the mirrors named by the -package-cache-*
// flags, and nothing for the resolvers, which stay this host's own.
//
// checkConfig has already refused a start over anything malformed here
// (checkPackageCacheUpstreams), so an entry that is not a "host=url"
// pair can only be a caller that did not go through it, and is dropped
// rather than guessed at -- parseKeyValues' own rule.
func PackageCacheUpstreamsFromConfig(cfg Config) packageCacheUpstreams {
	return packageCacheUpstreams{
		AptUpstream:    ParseKeyValues(cfg.PackageCacheAptUpstream),
		PyPIIndex:      strings.TrimSpace(cfg.PackageCachePyPIIndex),
		NPMRegistry:    strings.TrimSpace(cfg.PackageCacheNPMRegistry),
		GoProxy:        strings.TrimSpace(cfg.PackageCacheGoProxy),
		DockerRegistry: strings.TrimSpace(cfg.PackageCacheDockerRegistry),
		GHCRRegistry:   strings.TrimSpace(cfg.PackageCacheGHCRRegistry),
		GCRRegistry:    strings.TrimSpace(cfg.PackageCacheGCRRegistry),
	}
}

// describePackageCacheUpstreams is what the daemon logs about them at
// startup: one clause per ecosystem pointed somewhere other than its
// public home, and "" when none of them is.
//
// It is logged because the failure mode of this feature is silence. A
// mirror named in a unit file that this build does not read -- a
// misspelled flag dropped by readDaemonFlags, a host the apt half never
// serves -- looks exactly like a mirror that is working, from outside
// the process, until somebody notices the mirror's own logs are empty.
func DescribePackageCacheUpstreams(up packageCacheUpstreams) string {
	var parts []string
	for _, host := range SortedKeys(up.AptUpstream) {
		parts = append(parts, fmt.Sprintf("%s from %s", host, up.AptUpstream[host]))
	}
	for _, named := range []struct{ what, url string }{
		{"PyPI", up.PyPIIndex},
		{"npm", up.NPMRegistry},
		{"Go modules", up.GoProxy},
		{"Docker Hub", up.DockerRegistry},
		{"ghcr.io", up.GHCRRegistry},
		{"gcr.io", up.GCRRegistry},
	} {
		if named.url != "" {
			parts = append(parts, fmt.Sprintf("%s from %s", named.what, named.url))
		}
	}
	return strings.Join(parts, ", ")
}

// startSandboxDNS builds the interception resolver and binds it on
// listenAddr, answering with interceptIP for the intercepted domains
// and forwarding everything else upstream -- except for a sandbox with
// model.NetworkNone, which its backend names in restricted and whose
// other queries are answered NXDOMAIN.
//
// ZeroEgress stays false because one resolver serves every sandbox, and
// a sandbox with the open network resolves the rest of the internet
// through it too.
func StartSandboxDNS(listenAddr, interceptIP string, upstreamDNS []string, restricted *pkgcache.ClientSet) (*pkgcache.DNSServer, error) {
	server, err := pkgcache.NewDNSServer(pkgcache.DNSConfig{
		ListenAddr:  listenAddr,
		InterceptIP: net.ParseIP(interceptIP),
		ZeroEgress:  false,
		Restricted:  restricted,
		UpstreamDNS: upstreamDNS,
		Log:         func(format string, args ...any) { log.Printf("grain daemon: "+format, args...) },
	})
	if err != nil {
		return nil, err
	}
	if err := server.Start(); err != nil {
		return server, err
	}
	return server, nil
}

// packageCacheDir resolves the directory holding the package cache:
// an explicit -package-cache-dir if set, otherwise <sandbox-dir>/cache/packages
// when -sandbox-dir is set, falling back to <data-dir>/cache/packages.
func packageCacheDir(cfg Config) string {
	if cfg.PackageCacheDir != "" {
		return cfg.PackageCacheDir
	}
	if cfg.SandboxDir != "" {
		return filepath.Join(cfg.SandboxDir, "cache", "packages")
	}
	return filepath.Join(cfg.DataDir, "cache", "packages")
}

// startPackageCache opens the package cache and binds its listeners,
// fetching from wherever this deployment's -package-cache-* flags say --
// the public archives unless it named a mirror of its own -- and
// resolving through this host's own resolvers.
//
// restricted is the sandboxes with model.NetworkNone, as the sandbox
// backend names them (orchestrator.DNSEgressPolicy). Both the resolver
// and the HTTP service read it, so a sandbox named there is answered
// NXDOMAIN outside the intercepted domains and may only read through
// the cache. It is made before the cache is, and handed to the backend
// before there is a cache at all: a sandbox restricted then is
// restricted from the cache's first request. Nil restricts nobody.
func StartPackageCache(cfg Config, fabric Fabric, restricted *pkgcache.ClientSet) (*PackageCache, func(context.Context) error, error) {
	return startPackageCacheFrom(cfg, fabric, PackageCacheUpstreamsFromConfig(cfg), restricted)
}

// startPackageCacheFrom is startPackageCache with somewhere else to
// fetch from: see packageCacheUpstreams.
func startPackageCacheFrom(cfg Config, fabric Fabric, upstreams packageCacheUpstreams, restricted *pkgcache.ClientSet) (*PackageCache, func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }
	dir := packageCacheDir(cfg)
	cache, err := pkgcache.NewCache(dir, int64(cfg.PackageCacheGB)<<30)
	if err != nil {
		return nil, noop, err
	}
	var credentials *gitproxy.CredentialSet
	if cfg.DataDir != "" {
		if creds, err := gitproxy.LoadCredentialSet(filepath.Join(cfg.DataDir, "secrets", "github")); err == nil {
			credentials = creds
		}
	}
	handler := pkgcache.NewServer(pkgcache.Config{
		Cache:          cache,
		Credentials:    credentials,
		Restricted:     restricted,
		AptUpstream:    upstreams.AptUpstream,
		PyPIIndex:      upstreams.PyPIIndex,
		NPMRegistry:    upstreams.NPMRegistry,
		GoProxy:        upstreams.GoProxy,
		DockerRegistry: upstreams.DockerRegistry,
		GHCRRegistry:   upstreams.GHCRRegistry,
		GCRRegistry:    upstreams.GCRRegistry,
		Log:            func(format string, args ...any) { log.Printf("grain daemon: "+format, args...) },
	})
	if mirrors := DescribePackageCacheUpstreams(upstreams); mirrors != "" {
		log.Printf("grain daemon: package cache: fetching %s rather than from the public archives. What a "+
			"sandbox asks for does not change -- it still resolves and requests the archive, index or "+
			"registry itself, and this service is what goes to the mirror", mirrors)
	}

	ca, err := pkgcache.NewCA()
	if err != nil {
		log.Printf("grain daemon: generating package cache internal CA: %v", err)
	}

	// The cache's own HTTP service goes through the fabric like every
	// other sandbox-facing service. The interception half below does not:
	// its two listeners are on fixed ports at the address a sandbox's
	// resolver has been pointed at, so what it needs is that address and
	// not a URL -- see sandboxnet.go's own header, and
	// packageCacheDNSAddrs for why the resolver and the address it hands
	// out come out of one function.
	advertise := fabric.Host()
	endpoint, stopServer, err := fabric.Serve("package cache", handler)
	if err != nil {
		return nil, noop, err
	}
	url := endpoint.URL

	// The interception half: the resolver, and the two listeners its
	// answers send a fetch to. All three are privileged ports (53, 443,
	// 80), so on a deployment whose daemon does not run as root the whole
	// of this rests on CAP_NET_BIND_SERVICE -- which scripts/setup.sh
	// grants unconditionally, in both installer modes, for these three
	// binds rather than for the UI's own port.
	//
	// Every failure here is logged rather than fatal, and it is logged
	// either way round: a silent success is indistinguishable from a
	// silent failure from outside the process, and this is the only thing
	// an operator has to go on. Nothing fails a dispatch -- a deployment
	// that cannot intercept still has a package cache, reached the
	// explicit way through the apt proxy, the pip index and the npm and
	// Go registries above.
	listenAddr, dnsIP := PackageCacheDNSAddrs(advertise)
	httpsAddr, httpAddr := PackageCacheTransparentAddrs(dnsIP)
	var (
		dnsServer           *pkgcache.DNSServer
		dnsErr              error
		transparent         *transparentServers
		transparentErr      error
		transparentShutdown = noop
	)
	interceptionPossible := ca != nil
	if interceptionPossible {
		transparent, transparentErr = StartTransparentServers(httpsAddr, httpAddr, handler, ca)
		if transparentErr == nil {
			transparentShutdown = transparent.Shutdown
			dnsServer, dnsErr = StartSandboxDNS(listenAddr, dnsIP, upstreams.DNS, restricted)
			if dnsErr != nil {
				// Nothing will ever be sent to them, and holding 443 and
				// 80 of a host's docker bridge for a feature that is not
				// running is a thing to have to discover.
				_ = transparentShutdown(context.Background())
				transparentShutdown = noop
			}
		}
	}
	dnsServing := interceptionPossible && transparentErr == nil && dnsErr == nil
	switch {
	case dnsServing:
		log.Printf("grain daemon: package cache: intercepting DNS on %s, serving what it redirects on "+
			"%s and %s -- sandboxes resolve package and registry domains to %s and are fetched through "+
			"this cache under its own CA", dnsServer.Addr(), transparent.HttpsAddr, transparent.HttpAddr, dnsIP)
	case !interceptionPossible:
		log.Printf("grain daemon: package cache: no internal CA, so no DNS interception -- sandboxes " +
			"keep their own resolver and are served over plain HTTP by explicit configuration")
	case transparentErr != nil:
		log.Printf("grain daemon: package cache: binding the transparent listeners on %s and %s: %v "+
			"-- sandboxes keep their own resolver and no transparent caching happens. Ports 443 and 80 "+
			"are privileged: the daemon needs CAP_NET_BIND_SERVICE, and nothing else on this host may "+
			"hold either port on that address (a -ui-addr of 0.0.0.0:80 does)",
			httpsAddr, httpAddr, transparentErr)
	default:
		log.Printf("grain daemon: package cache: binding the DNS interception server on %s: %v "+
			"-- sandboxes keep their own resolver and fetch from the internet, and no transparent "+
			"caching happens (port 53 is privileged: the daemon needs CAP_NET_BIND_SERVICE)",
			listenAddr, dnsErr)
	}

	shutdown := func(ctx context.Context) error {
		_ = stopServer(ctx)
		_ = transparentShutdown(ctx)
		if dnsServer != nil {
			_ = dnsServer.Close()
		}
		return nil
	}

	return &PackageCache{credentials: credentials,
		cache:      cache,
		dir:        cache.Dir(),
		Ca:         ca,
		dnsServer:  dnsServer,
		Url:        url,
		DnsIP:      dnsIP,
		DnsServing: dnsServing,
	}, shutdown, nil
}

// sandboxURL is what a dispatch hands to orchestrator.Config.PackageCache.
func (p *PackageCache) sandboxURL(live *LiveConfig) func(context.Context) string {
	return func(context.Context) string {
		if p == nil || p.Url == "" {
			return ""
		}
		if !live.Current().PackageCacheEnabled {
			return ""
		}
		return p.Url
	}
}

// sandboxCA returns the CA certificate PEM if package cache is enabled.
func (p *PackageCache) sandboxCA(live *LiveConfig) func(context.Context) []byte {
	return func(context.Context) []byte {
		if p == nil || p.Ca == nil {
			return nil
		}
		if !live.Current().PackageCacheEnabled {
			return nil
		}
		return p.Ca.RootCertPEM()
	}
}

// sandboxDNS returns the DNS intercept IP if package cache is enabled
// and the DNS server is actually serving on it.
//
// Never write a resolv.conf pointing at a nameserver that is not
// running: SandboxResolvConf replaces the sandbox's file rather than
// adding to it, so a dnsIP nobody answers on takes name resolution away
// from the sandbox entirely. Returning "" here leaves the sandbox's own
// resolver alone, which costs only the interception.
func (p *PackageCache) SandboxDNS(live *LiveConfig) func(context.Context) string {
	return func(context.Context) string {
		if p == nil || p.DnsIP == "" || !p.DnsServing {
			return ""
		}
		if !live.Current().PackageCacheEnabled {
			return ""
		}
		return p.DnsIP
	}
}

// PackageCacheStats implements ui.PackageCache.
func (p *PackageCache) PackageCacheStats() ([]ui.PackageCacheStats, error) {
	if p == nil || p.cache == nil {
		return nil, nil
	}
	stats, err := p.cache.Stats()
	if err != nil {
		return nil, err
	}
	out := make([]ui.PackageCacheStats, 0, len(stats))
	for _, s := range stats {
		out = append(out, ui.PackageCacheStats{Kind: s.Kind, Entries: s.Entries, Bytes: s.Bytes})
	}
	return out, nil
}

// PackageCacheEntryCount implements ui.PackageCache.
func (p *PackageCache) PackageCacheEntryCount() int {
	if p == nil || p.cache == nil {
		return 0
	}
	return p.cache.Entries()
}

// FlushPackageCache implements ui.PackageCache.
func (p *PackageCache) FlushPackageCache(kind string) (int, int64, error) {
	if p == nil || p.cache == nil {
		return 0, 0, nil
	}
	entries, freed, err := p.cache.Flush(kind)
	if err == nil {
		what := kind
		if what == "" {
			what = "every"
		}
		log.Printf("grain daemon: package cache: flushed %s package(s) -- %d entries, %d bytes", what, entries, freed)
	}
	return entries, freed, err
}

// uiPackageCache is p as the UI takes one.
func (p *PackageCache) uiPackageCache() ui.PackageCache {
	if p == nil || p.cache == nil {
		return nil
	}
	return p
}

// SetGitConnections tells the cache's registry pulls which credential
// the deployment's git connections give each repository. Safe on a nil
// cache, or one with no credentials.
func (p *PackageCache) SetGitConnections(r gitproxy.ConnectionResolver) {
	if p == nil || p.credentials == nil {
		return
	}
	p.credentials.SetConnections(r)
}
