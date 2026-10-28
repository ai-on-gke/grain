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

package pkgcache

// The HTTP service itself: one handler answering both shapes of request
// a package client makes of us:
//
// 1. Transparent requests intercepted via DNS: standard upstream URLs
//    (deb.debian.org, registry.npmjs.org, pypi.org, files.pythonhosted.org,
//    proxy.golang.org, registry-1.docker.io, ghcr.io, gcr.io) served over
//    HTTP (port 80) and HTTPS (port 443 with dynamic certificates issued
//    by the internal Grain Root CA).
// 2. Legacy requests against this service's own paths: /pypi/, /v2/,
//    /npm/, /go/, and absolute-form HTTP proxy requests for apt archives.
//
// A CONNECT is the one request neither half can serve -- an https:// URL
// fetched through something that thinks this is its proxy. It is tunnelled
// straight to the archive, uncached, when the archive is one this service
// serves, and refused otherwise: see connect.go for why a refusal is not
// the safe answer it looks like.
//
// Everything both halves share is here: which requests may be served
// from disk, how one upstream fetch is made and streamed back, and what
// a refusal looks like. Refusals are written as plain sentences rather
// than as bare statuses because of who reads them -- apt and pip both
// print the proxy's response body when a fetch fails, so this is the
// one place that gets to explain itself to whoever is staring at a
// failed `apt-get install` inside a sandbox.

import (
	"fmt"
	"hash"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RegistryCredentials resolves a bearer or basic-auth token for an OCI
// registry repository (owner/repo). *gitproxy.CredentialSet implements it,
// keeping pkg/pkgcache free of pkg/gitproxy so grit can link it cleanly.
type RegistryCredentials interface {
	PackageToken(owner, repo string) (string, bool)
}

// PackageUsername is the HTTP basic-auth username sent alongside a GitHub
// package token (matching gitproxy.PackageUsername).
const PackageUsername = "x-access-token"

// DefaultAptHosts is the set of archive hosts the apt half of the cache
// will fetch from, and so also the set a sandbox's apt is pointed here
// for (SandboxConfig's own apt.conf lists them one by one).
//
// It is an allowlist rather than "whatever the client asks for", and
// that is the whole security story of this service. A sandbox running
// with model.NetworkNone has had its default route taken away precisely
// so that the only things it can reach are this deployment's own
// services; an HTTP proxy in that set that forwarded to any host named
// in a request would hand every such sandbox a general-purpose egress
// path and quietly undo it. Distribution archives are what a package
// cache is for, so distribution archives are what it will fetch.
//
// An entry beginning with a dot matches any host under that domain,
// which is how the dozens of regional Ubuntu mirrors
// (us.archive.ubuntu.com, and so on) are covered without listing them.
var DefaultAptHosts = []string{
	"deb.debian.org",
	"security.debian.org",
	"ftp.debian.org",
	"archive.debian.org",
	"archive.ubuntu.com",
	"security.ubuntu.com",
	"ports.ubuntu.com",
	"dl-cdn.alpinelinux.org",
	"download.docker.com",
	"packages.cloud.google.com",
	"apt.releases.hashicorp.com",
	"pkgs.tailscale.com",
	"pkgs.k8s.io",
	"apt.kubernetes.io",
	"deb.nodesource.com",
	"packages.microsoft.com",
	"ppa.launchpadcontent.net",
	"ppa.launchpad.net",
	".archive.ubuntu.com",
	".debian.org",
	".ubuntu.com",
	".alpinelinux.org",
}

// DefaultOCIRegistries is the standard set of container registries intercepted and cached.
var DefaultOCIRegistries = []string{
	"registry-1.docker.io",
	"docker.io",
	"ghcr.io",
	"gcr.io",
	"quay.io",
	"registry.k8s.io",
	"k8s.gcr.io",
	"pkg.dev",
	".pkg.dev",
}

// Config is what a Server needs.
//
// The upstream fields -- AptUpstream, PyPIIndex, NPMRegistry, GoProxy,
// DockerRegistry, GHCRRegistry and GCRRegistry -- are where an
// ecosystem's fetches actually go.
// Each is empty on a deployment that fetches from the public archive,
// index or registry, which is what the defaults name; a deployment that
// already fronts one of them with a mirror of its own says so with
// `grain daemon`'s -package-cache-* flags, which is the only thing that
// sets them outside the tests (pkg/cli/packagecache.go).
type Config struct {
	// Cache is where objects are stored. Required.
	Cache Store
	// Credentials is the GitHub credential ladder (gitproxy.CredentialSet),
	// used to auto-authenticate OCI registry operations (such as ghcr.io).
	Credentials RegistryCredentials
	// AllowRegistryPush lets Credentials authenticate writes to an
	// upstream registry -- pushes, and token requests for any action
	// beyond pull -- on a sandbox's behalf. Off, which is what
	// `grain daemon` leaves it, the daemon's credential only ever buys a
	// pull: every sandbox can reach this server, so a push token minted
	// with that credential would let any of them overwrite any image the
	// credential covers (#2377). A sandbox that has to publish brings a
	// credential of its own (the package-publish capability), and a
	// request carrying its own Authorization is forwarded untouched.
	AllowRegistryPush bool
	// Restricted is the sandboxes with no network (model.NetworkNone),
	// by the address their requests arrive from -- the same set the
	// resolver is given (DNSConfig.Restricted). Each of them may only
	// read through this service: no git push, no registry upload or
	// delete, and a tunnel only to a host that serves nothing but
	// downloads (restrict.go). That holds AllowRegistryPush too, which
	// never makes the daemon's credential available to one of them. Nil
	// means no client is restricted.
	Restricted *ClientSet
	// JobTokens, when set, identifies CI job pods reaching the cluster
	// package cache by per-job token or pod IP, enforcing ReadOnly on
	// networkAccess: none jobs and separating trusted and untrusted Go
	// build-cache action entries.
	JobTokens *JobTokenSet
	// SplitGoCacheTrust separates Go build-cache action entries into
	// "trusted" and "untrusted" namespaces even when JobTokens is nil.
	SplitGoCacheTrust bool
	// Client makes the upstream fetches. Nil means a client of this
	// package's own, with a timeout long enough for a large package on a
	// slow link and short enough that a hung mirror does not hold a
	// sandbox's apt open forever.
	Client *http.Client
	// AptHosts overrides DefaultAptHosts -- the archive hosts the apt
	// half will fetch from.
	AptHosts []string
	// AptScheme overrides the upstream scheme for APT fetching ("http" by default).
	AptScheme string
	// AptUpstream sends the archives named in it somewhere other than
	// themselves: the key is the hostname a sandbox asked for
	// (deb.debian.org), the value a base URL its requests are fetched
	// from, with the request's own path appended
	// (http://mirror.internal/debian-proxy). A host not named here is
	// fetched from itself over AptScheme, which is what every deployment
	// does today.
	//
	// It is the apt half's equivalent of PyPIIndex and DockerRegistry: a
	// deployment fronting Debian with a mirror of its own names it here,
	// and the tests point an intercepted archive hostname at a stand-in
	// on loopback so that a test of the interception is not also a test
	// of whether this machine can reach Debian.
	AptUpstream map[string]string
	// PyPIIndex overrides DefaultPyPIIndex: the simple index /pypi/simple/
	// proxies. A deployment with an internal mirror names it here; the
	// tests point it at a loopback server.
	PyPIIndex string
	// PyPIFileHosts overrides DefaultPyPIFileHosts -- where an artifact
	// linked from that index may be fetched from.
	PyPIFileHosts []string
	// PyPIFileScheme is how those hosts are reached, "https" when empty.
	// It exists for the tests, which serve an index over plain HTTP.
	PyPIFileScheme string
	// DockerRegistry overrides DefaultDockerRegistry: the registry /v2/
	// mirrors for Docker Hub. A deployment with a registry of its own in front of Docker
	// Hub names it here; the tests point it at a loopback server.
	DockerRegistry string
	// GHCRRegistry overrides DefaultGHCRRegistry: the registry /v2/ghcr/
	// mirrors for GitHub Container Registry (ghcr.io).
	GHCRRegistry string
	// GCRRegistry overrides DefaultGCRRegistry: the registry /v2/gcr/
	// mirrors for Google Container Registry (gcr.io).
	GCRRegistry string
	// NPMRegistry overrides DefaultNPMRegistry: the registry /npm/
	// proxies.
	NPMRegistry string
	// GoProxy overrides DefaultGoProxy: the module proxy /go/ forwards
	// to. A deployment running an Athens of its own names it here.
	GoProxy string
	// GoSumDBHosts overrides DefaultGoSumDBHosts -- the checksum
	// databases /go/sumdb/ may fetch from.
	GoSumDBHosts []string
	// GitHubURL overrides DefaultGitHubURL: the upstream GitHub URL /github/
	// and intercepted GitHub requests proxy to.
	GitHubURL string
	// GitHubHosts overrides DefaultGitHubFileHosts -- the GitHub hosts
	// intercepted and served.
	GitHubHosts []string
	// PlaywrightURL overrides DefaultPlaywrightURL: the upstream Playwright CDN URL.
	PlaywrightURL string
	// PlaywrightHosts overrides DefaultPlaywrightHosts -- the Playwright CDN hosts
	// intercepted and served.
	PlaywrightHosts []string
	// Log is where refusals and upstream failures are reported. Nil
	// means the standard logger. Successful fetches are not logged at
	// all: a run installing a toolchain makes hundreds of them, and a
	// journal full of them says nothing an operator wants.
	Log func(format string, args ...any)
}

// Server answers the package cache's own port.
type Server struct {
	cache             Store
	credentials       RegistryCredentials
	allowPush         bool
	restricted        *ClientSet
	jobTokens         *JobTokenSet
	splitGoCacheTrust bool
	client            *http.Client
	aptHosts          []string
	aptScheme         string
	aptUpstream       map[string]string
	pypiIndexURL      string
	pypiHosts         []string
	fileScheme        string
	dockerRegistryURL string
	ghcrRegistryURL   string
	gcrRegistryURL    string
	npmRegistryURL    string
	goProxyURL        string
	goSumDBList       []string
	githubURLVal      string
	githubHosts       []string
	playwrightURLVal  string
	playwrightHosts   []string
	// dockerClient, ghcrClient and gcrClient are client with the registry's
	// token dance wrapped around them (docker.go). Everything else uses
	// client itself.
	dockerClient *http.Client
	ghcrClient   *http.Client
	ghcrAuth     *dockerAuth
	gcrClient    *http.Client
	ociClients   map[string]*http.Client
	ociClientsMu sync.Mutex
	logf         func(format string, args ...any)
	// fetches coalesces concurrent misses on one object, so that ten
	// sandboxes starting the same job fetch it once between them
	// (coalesce.go).
	fetches fetchGroup
}

// NewServer wires a handler over cache.
func NewServer(cfg Config) *Server {
	s := &Server{
		cache:             cfg.Cache,
		credentials:       cfg.Credentials,
		allowPush:         cfg.AllowRegistryPush,
		restricted:        cfg.Restricted,
		jobTokens:         cfg.JobTokens,
		splitGoCacheTrust: cfg.SplitGoCacheTrust || cfg.JobTokens != nil,
		client:            cfg.Client,
		aptHosts:          cfg.AptHosts,
		aptScheme:         strings.TrimSpace(cfg.AptScheme),
		aptUpstream:       normalizedHostMap(cfg.AptUpstream),
		pypiIndexURL:      strings.TrimSpace(cfg.PyPIIndex),
		pypiHosts:         cfg.PyPIFileHosts,
		fileScheme:        strings.TrimSpace(cfg.PyPIFileScheme),
		dockerRegistryURL: strings.TrimSpace(cfg.DockerRegistry),
		ghcrRegistryURL:   strings.TrimSpace(cfg.GHCRRegistry),
		gcrRegistryURL:    strings.TrimSpace(cfg.GCRRegistry),
		npmRegistryURL:    strings.TrimSpace(cfg.NPMRegistry),
		goProxyURL:        strings.TrimSpace(cfg.GoProxy),
		goSumDBList:       cfg.GoSumDBHosts,
		githubURLVal:      strings.TrimSpace(cfg.GitHubURL),
		githubHosts:       cfg.GitHubHosts,
		playwrightURLVal:  strings.TrimSpace(cfg.PlaywrightURL),
		playwrightHosts:   cfg.PlaywrightHosts,
		logf:              cfg.Log,
	}
	if s.client == nil {
		s.client = &http.Client{Timeout: 10 * time.Minute}
	}
	if len(s.aptHosts) == 0 {
		s.aptHosts = DefaultAptHosts
	}
	if s.fileScheme == "" {
		s.fileScheme = "https"
	}
	if s.logf == nil {
		s.logf = log.Printf
	}
	s.dockerClient, _ = newDockerClient(s.client, s.dockerRegistry(), s.credentials)
	s.ghcrClient, s.ghcrAuth = newDockerClient(s.client, s.ghcrRegistry(), s.credentials)
	s.gcrClient, _ = newDockerClient(s.client, s.gcrRegistry(), s.credentials)
	return s
}

func (s *Server) dockerScheme() string {
	if s.dockerRegistryURL != "" {
		if u, err := url.Parse(s.dockerRegistryURL); err == nil && u.Scheme != "" {
			return u.Scheme
		}
	}
	return "https"
}

// ociClient returns the upstream registry URL and authenticating client for an OCI registry host.
func (s *Server) ociClient(host string) (registryURL string, client *http.Client) {
	switch host {
	case "registry-1.docker.io", "docker.io":
		return s.dockerRegistry(), s.dockerClient
	case "ghcr.io":
		return s.ghcrRegistry(), s.ghcrClient
	case "gcr.io":
		return s.gcrRegistry(), s.gcrClient
	default:
		regURL := s.dockerScheme() + "://" + host
		s.ociClientsMu.Lock()
		defer s.ociClientsMu.Unlock()
		if s.ociClients == nil {
			s.ociClients = make(map[string]*http.Client)
		}
		c, ok := s.ociClients[host]
		if !ok {
			c, _ = newDockerClient(s.client, regURL, s.credentials)
			s.ociClients[host] = c
		}
		return regURL, c
	}
}

// newDockerClient is client with the registry auth transport in front of
// it. Everything else about it -- the timeout, whatever transport a
// caller supplied -- is client's own, so a test that points the cache at
// a loopback registry gets its own transport back with only the token
// dance added.
func newDockerClient(client *http.Client, registry string, credentials RegistryCredentials) (*http.Client, *dockerAuth) {
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	host := ""
	if u, err := url.Parse(registry); err == nil {
		host = u.Host
	}
	out := *client
	auth := &dockerAuth{base: base, host: host, registryURL: registry, client: client, credentials: credentials}
	out.Transport = auth
	return &out, auth
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.jobTokens.RequireIdentification() {
		if _, ok := s.jobTokens.Lookup(r); !ok {
			s.refuse(w, r, http.StatusUnauthorized, "grit's package cache serves CI job pods only.")
			return
		}
	}
	restricted := s.restrictedClient(r)
	if restricted && r.Method != http.MethodConnect {
		if reason := restrictedRefusal(r); reason != "" {
			s.refuse(w, r, http.StatusForbidden, reason)
			return
		}
	}
	if r.Method == http.MethodConnect {
		// An https:// fetch through a proxy. Nothing this cache can store,
		// but a tunnel to an archive it serves beats the hard failure a
		// refusal is (connect.go).
		s.serveCONNECT(w, r, restricted)
		return
	}

	// Absolute-form request URI: this is a proxy request, and apt is the
	// only thing configured to make one.
	if r.URL.IsAbs() && r.URL.Host != "" {
		if hostAllowed(r.URL.Host, s.githubFileHosts()) || (s.githubURLVal != "" && r.URL.Host == hostOnly(s.githubURLVal)) {
			s.serveTransparentGitHub(w, r, r.URL.Host)
			return
		}
		if hostAllowed(r.URL.Host, s.playwrightFileHosts()) || (s.playwrightURLVal != "" && r.URL.Host == hostOnly(s.playwrightURLVal)) {
			s.serveTransparentPlaywright(w, r, r.URL.Host)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			s.refuse(w, r, http.StatusMethodNotAllowed,
				fmt.Sprintf("this is grain's package cache: it answers GET and HEAD, not %s.", r.Method))
			return
		}
		s.serveAPT(w, r)
		return
	}

	if r.URL.Path == "/" || r.URL.Path == "" {
		s.describe(w)
		return
	}

	reqHost := r.Host
	if h, _, err := net.SplitHostPort(reqHost); err == nil {
		reqHost = h
	}
	reqHost = strings.ToLower(strings.TrimSpace(reqHost))

	// Check if this is a transparent request matching an intercepted ecosystem domain
	if reqHost != "" && !strings.HasPrefix(reqHost, "127.") && reqHost != "localhost" && reqHost != "172.17.0.1" && reqHost != "0.0.0.0" {
		switch {
		case hostAllowed(reqHost, DefaultOCIRegistries):
			regURL, client := s.ociClient(reqHost)
			s.serveDockerDirect(w, r, regURL, client)
			return
		case reqHost == "auth.docker.io":
			s.serveDirectPassthrough(w, r, "https://auth.docker.io"+r.URL.RequestURI())
			return
		case reqHost == "github.com" || hostAllowed(reqHost, s.githubFileHosts()):
			s.serveTransparentGitHub(w, r, reqHost)
			return
		case reqHost == "registry.npmjs.org" || reqHost == "registry.yarnpkg.com":
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				s.refuse(w, r, http.StatusMethodNotAllowed, fmt.Sprintf("this is grain's package cache: it answers GET and HEAD, not %s.", r.Method))
				return
			}
			s.serveTransparentNPM(w, r, reqHost)
			return
		case reqHost == "pypi.org" || reqHost == "pypi.python.org":
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				s.refuse(w, r, http.StatusMethodNotAllowed, fmt.Sprintf("this is grain's package cache: it answers GET and HEAD, not %s.", r.Method))
				return
			}
			if strings.HasPrefix(r.URL.Path, "/simple") || r.URL.Path == "/simple" {
				s.serveTransparentPyPIIndex(w, r)
			} else {
				s.serveTransparentPyPIFile(w, r, reqHost)
			}
			return
		case reqHost == "files.pythonhosted.org" || strings.HasSuffix(reqHost, ".pythonhosted.org"):
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				s.refuse(w, r, http.StatusMethodNotAllowed, fmt.Sprintf("this is grain's package cache: it answers GET and HEAD, not %s.", r.Method))
				return
			}
			s.serveTransparentPyPIFile(w, r, reqHost)
			return
		case reqHost == "proxy.golang.org":
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				s.refuse(w, r, http.StatusMethodNotAllowed, fmt.Sprintf("this is grain's package cache: it answers GET and HEAD, not %s.", r.Method))
				return
			}
			s.serveTransparentGoProxy(w, r)
			return
		case reqHost == "sum.golang.org" || hostAllowed(reqHost, s.goSumDBHosts()):
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				s.refuse(w, r, http.StatusMethodNotAllowed, fmt.Sprintf("this is grain's package cache: it answers GET and HEAD, not %s.", r.Method))
				return
			}
			s.serveTransparentGoSumDB(w, r, reqHost)
			return
		case hostAllowed(reqHost, s.playwrightFileHosts()):
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				s.refuse(w, r, http.StatusMethodNotAllowed, fmt.Sprintf("this is grain's package cache: it answers GET and HEAD, not %s.", r.Method))
				return
			}
			s.serveTransparentPlaywright(w, r, reqHost)
			return
		case hostAllowed(reqHost, s.aptHosts):
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				s.refuse(w, r, http.StatusMethodNotAllowed, fmt.Sprintf("this is grain's package cache: it answers GET and HEAD, not %s.", r.Method))
				return
			}
			s.serveTransparentAPT(w, r, reqHost)
			return
		}
	}

	// Legacy Docker mirror and GitHub path routing (Docker supports write
	// operations; GitHub only git's own POSTs -- githubMethodAllowed)
	if strings.HasPrefix(r.URL.Path, dockerAPIPrefix) || r.URL.Path == strings.TrimSuffix(dockerAPIPrefix, "/") {
		s.serveDocker(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, githubPrefix) || r.URL.Path == strings.TrimSuffix(githubPrefix, "/") {
		s.serveGitHub(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, goCachePrefix) || r.URL.Path == strings.TrimSuffix(goCachePrefix, "/") {
		s.serveGoCache(w, r)
		return
	}

	// All other legacy endpoints only answer GET and HEAD.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		s.refuse(w, r, http.StatusMethodNotAllowed,
			fmt.Sprintf("this is grain's package cache: it answers GET and HEAD, not %s.", r.Method))
		return
	}
	switch {
	case strings.HasPrefix(r.URL.Path, pypiSimplePrefix), r.URL.Path == strings.TrimSuffix(pypiSimplePrefix, "/"):
		s.servePyPIIndex(w, r)
		return
	case strings.HasPrefix(r.URL.Path, pypiFilesPrefix):
		s.servePyPIFile(w, r)
		return
	case strings.HasPrefix(r.URL.Path, npmPrefix), r.URL.Path == strings.TrimSuffix(npmPrefix, "/"):
		s.serveNPM(w, r)
		return
	case strings.HasPrefix(r.URL.Path, goModulePrefix), r.URL.Path == strings.TrimSuffix(goModulePrefix, "/"):
		s.serveGoModule(w, r)
		return
	case strings.HasPrefix(r.URL.Path, playwrightPrefix), r.URL.Path == strings.TrimSuffix(playwrightPrefix, "/"):
		s.servePlaywright(w, r)
		return
	default:
		s.refuse(w, r, http.StatusNotFound,
			"this is grain's package cache. It serves a Python package index under /pypi/simple/, mirrors Docker Hub, "+
				"ghcr.io and gcr.io under /v2/, serves an npm registry under /npm/ and a Go module proxy under /go/, "+
				"and transparently proxies and caches apt, PyPI, npm, Go and OCI container registries. Nothing else is here.")
	}
}

// describe answers the service's root with what it is, for the operator
// who has found the port and pointed a browser at it.
func (s *Server) describe(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, "grain proxy cache\n\n"+
		"  apt        transparently proxies Debian and Ubuntu distribution archives\n"+
		"  pypi       transparently proxies pypi.org and files.pythonhosted.org\n"+
		"  docker     transparently mirrors Docker Hub, ghcr.io and gcr.io\n"+
		"  npm        transparently proxies registry.npmjs.org\n"+
		"  go         transparently proxies proxy.golang.org and sum.golang.org\n"+
		"  github     transparently proxies and caches github.com repositories\n"+
		"  playwright transparently proxies and caches Playwright browser CDN downloads\n\n"+
		"Packages are cached; indexes are passed through. grain points every sandbox here\n"+
		"automatically when the proxy cache is enabled in Settings -> Sandbox.\n")
}

// refuse answers one request this service will not serve, and says why
// in a sentence the person reading their package manager's output can
// act on.
func (s *Server) refuse(w http.ResponseWriter, r *http.Request, status int, reason string) {
	s.logf("package cache: refused %s %s: %s", r.Method, requestTarget(r), reason)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintln(w, reason)
}

// requestTarget is what to call a request in a log line: the absolute
// URL for a proxy request, the host and port for a CONNECT (whose target
// is an authority and whose path is empty), the path for our own.
func requestTarget(r *http.Request) string {
	if r.Method == http.MethodConnect {
		if host := strings.TrimSpace(r.Host); host != "" {
			return host
		}
		return r.URL.Host
	}
	if r.URL.IsAbs() {
		return r.URL.String()
	}
	return r.URL.Path
}

// object is one thing the cache can fetch and possibly keep.
type object struct {
	// kind is which half of the cache it belongs to.
	kind string
	// url is where it comes from.
	url string
	// key is what a stored copy is filed under, and the url when empty.
	// It exists for the one case where two URLs are the same bytes: a
	// container layer is named by the digest of its own content, so the
	// same layer under two repositories is one entry (docker.go).
	key string
	// cacheable says whether a copy may be kept. False for every
	// mutable thing -- a package index, a redirect, an error -- and for
	// a request that is not a plain whole-body GET.
	cacheable bool
	// client fetches it, and the service's own when nil. The Docker half
	// passes one with the registry's token dance wrapped around it.
	client *http.Client
	// digest, when set, is the content digest the body must hash to
	// before a copy is kept (docker.go). A body that does not is still
	// passed to the client, which checks it for itself, but is never
	// stored: the key names no upstream, so a wrong copy would be served
	// in answer to every registry's request for that digest.
	digest string
	// fallbackURL is an alternative upstream URL (e.g. a public mirror) to try
	// if the primary upstream returns 429 Too Many Requests, a 5xx error, or fails.
	fallbackURL string
	// fallbackClient is the HTTP client to use for fallbackURL.
	fallbackClient *http.Client
}

// storeKey is what a copy of obj is filed under.
func (o object) storeKey() string {
	if o.key != "" {
		return o.key
	}
	return o.url
}

// serve is the one path both halves of this service take: answer from
// disk if we have it, otherwise fetch it once, stream it to the client
// and keep a copy on the way past.
func (s *Server) serve(w http.ResponseWriter, r *http.Request, obj object) {
	cacheable := obj.cacheable && (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.Header.Get("Range") == ""
	if cacheable {
		if s.serveFromCache(w, r, obj) {
			return
		}
		// A miss. If another request is already fetching this exact
		// object, wait for it and look again rather than fetching a
		// second copy of it -- see coalesce.go for what is shared and
		// what deliberately is not.
		key := flightKey(obj)
		if wait, leader := s.fetches.begin(key); leader {
			defer s.fetches.end(key)
			if s.serveFromCache(w, r, obj) {
				return
			}
		} else {
			select {
			case <-wait:
			case <-r.Context().Done():
				return
			}
			if s.serveFromCache(w, r, obj) {
				return
			}
			// The shared fetch stored nothing. Fetch it ourselves,
			// without becoming the leader anything else waits on.
		}
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, obj.url, r.Body)
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, fmt.Sprintf("grain's package cache cannot fetch %s: %v", obj.url, err))
		return
	}
	req.ContentLength = r.ContentLength
	copyRequestHeaders(req, r)
	if u, err := url.Parse(obj.url); err == nil && u.Host != "" {
		req.Host = u.Host
	}
	client := obj.client
	if client == nil {
		client = s.client
	}
	resp, err := client.Do(req)
	if (err != nil || (resp != nil && (resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode >= 500 && resp.StatusCode != http.StatusNotImplemented)))) && obj.fallbackURL != "" {
		fallbackClient := obj.fallbackClient
		if fallbackClient == nil {
			fallbackClient = client
		}
		fbReq, fbErr := http.NewRequestWithContext(r.Context(), r.Method, obj.fallbackURL, r.Body)
		if fbErr == nil {
			fbReq.ContentLength = r.ContentLength
			copyRequestHeaders(fbReq, r)
			if u, err := url.Parse(obj.fallbackURL); err == nil && u.Host != "" {
				fbReq.Host = u.Host
			}
			// The primary's answer is only discarded once the fallback has
			// produced one of its own: a fallback that cannot be reached
			// leaves the client with the primary's status and body, not
			// with that status over a body already closed underneath it.
			if fbResp, fbErr := fallbackClient.Do(fbReq); fbErr == nil {
				if resp != nil {
					resp.Body.Close()
				}
				resp = fbResp
				err = nil
			}
		}
	}
	if err != nil {
		s.logf("package cache: fetching %s: %v", obj.url, err)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, "grain's package cache could not reach %s: %v\n", obj.url, err)
		return
	}
	defer resp.Body.Close()

	copyResponseHeaders(w.Header(), resp.Header)
	var entry ObjectWriter
	if cacheable && resp.StatusCode == http.StatusOK {
		if entry, err = s.cache.NewWriter(obj.kind, obj.storeKey()); err != nil {
			// Cache the next one instead. A cache that cannot write is a
			// cache that is not helping, never a fetch that fails.
			s.logf("package cache: not storing %s: %v", obj.url, err)
			entry = nil
		}
	}
	var body io.Reader = resp.Body
	var digest hash.Hash
	if entry != nil && obj.digest != "" {
		if digest = digestHash(obj.digest); digest == nil {
			// A digest this package cannot check is a body it cannot vouch for.
			entry.Abort()
			entry = nil
		} else {
			body = io.TeeReader(resp.Body, digest)
		}
	}
	status := "miss"
	if entry == nil {
		status = "pass"
	}
	w.Header().Set(cacheStatusHeader, status)
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		if entry != nil {
			entry.Abort()
		}
		return
	}
	_, complete := copyBody(w, entry, body, resp.ContentLength)
	if entry == nil {
		return
	}
	if entry.Oversize() {
		// Said once per fetch rather than suppressed after the first,
		// because the repetition is the diagnosis: an operator seeing
		// this line every time a job runs is looking at an object being
		// pulled from upstream on every build, and the only fix is a
		// bigger per-entry limit.
		s.logf("package cache: %s is larger than the %dMB a single entry may be -- not caching it, so it is fetched from upstream every time it is asked for",
			obj.url, s.cache.MaxEntryBytes()/(1<<20))
		return
	}
	if !complete {
		entry.Abort()
		return
	}
	if digest != nil && !digestMatches(obj.digest, digest) {
		// Where the bytes really came from: the fallback, or the end of
		// a redirect. Its query is left out, as a blob redirect's is
		// usually a signed CDN token.
		from := obj.url
		if resp.Request != nil && resp.Request.URL != nil {
			u := *resp.Request.URL
			u.RawQuery, u.User = "", nil
			from = u.String()
		}
		s.logf("package cache: %s answered with bytes that do not hash to %s -- not caching them", from, obj.digest)
		entry.Abort()
		return
	}
	entry.Commit(Meta{URL: obj.url, ContentType: resp.Header.Get("Content-Type")})
}

// serveFromCache answers obj from disk if there is a copy of it, and
// reports whether it did.
func (s *Server) serveFromCache(w http.ResponseWriter, r *http.Request, obj object) bool {
	f, meta, ok := s.cache.Open(obj.kind, obj.storeKey())
	if !ok {
		return false
	}
	defer f.Close()
	if meta.ContentType != "" {
		w.Header().Set("Content-Type", meta.ContentType)
	}
	w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	w.Header().Set(cacheStatusHeader, "hit")
	if r.Method != http.MethodHead {
		io.Copy(w, f)
	}
	return true
}

// serveDirectPassthrough passes an upstream request through untouched.
func (s *Server) serveDirectPassthrough(w http.ResponseWriter, r *http.Request, upstreamURL string) {
	s.proxyDocker(w, r, upstreamURL)
}

// cacheStatusHeader says what this service did with a request -- hit,
// miss (fetched and stored) or pass (fetched, not storable). It exists
// for the same reason every cache in front of an HTTP service has one:
// without it, "is the cache actually being used" is a question only
// answerable by watching a network.
const cacheStatusHeader = "X-Grain-Package-Cache"

// hopByHop are the headers that describe *this* connection rather than
// the response travelling over it, and so must not be copied from one
// to the other.
var hopByHop = map[string]bool{
	"connection":          true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"proxy-connection":    true,
	"te":                  true,
	"trailer":             true,
	"transfer-encoding":   true,
	"upgrade":             true,
}

// copyRequestHeaders forwards the request headers that change what
// an archive answers with, git headers, and authentication for OCI registries.
func copyRequestHeaders(req *http.Request, from *http.Request) {
	for _, name := range []string{"Accept", "Range", "If-Modified-Since", "If-None-Match", "User-Agent", "Authorization", "Content-Type", "Git-Protocol", "Docker-Upload-UUID"} {
		if v := from.Header.Get(name); v != "" {
			req.Header.Set(name, v)
		}
	}
	req.Header.Set("Accept-Encoding", "identity")
}

// copyResponseHeaders passes an upstream response's headers on, minus
// the hop-by-hop ones and minus Content-Length, which the caller sets
// for itself.
func copyResponseHeaders(dst, src http.Header) {
	for name, values := range src {
		if hopByHop[strings.ToLower(name)] || strings.EqualFold(name, "Content-Length") {
			continue
		}
		for _, v := range values {
			dst.Add(name, v)
		}
	}
}

// hostAllowed reports whether host is one this service will fetch from,
// against a list whose entries are either exact hosts or a leading-dot
// domain suffix. The port is ignored: an archive on a non-standard port
// is the same archive.
func hostAllowed(host string, allowed []string) bool {
	host = hostKey(host)
	for _, entry := range allowed {
		entry = strings.ToLower(entry)
		if strings.HasPrefix(entry, ".") {
			if strings.HasSuffix(host, entry) || host == strings.TrimPrefix(entry, ".") {
				return true
			}
			continue
		}
		if host == entry {
			return true
		}
	}
	return false
}

// ServesAptHost reports whether the apt half will fetch from host,
// against the archive hosts a Config named (nil for DefaultAptHosts).
//
// Exported for the caller that has to decide about a hostname before a
// Server exists: a deployment naming an upstream mirror for an archive
// this service does not serve (Config.AptUpstream, and the daemon flag
// behind it) has configured something nothing will ever reach, and
// saying so while the daemon is starting is cheaper than an operator
// working it out from fetches that went to Debian anyway.
func ServesAptHost(host string, aptHosts []string) bool {
	if len(aptHosts) == 0 {
		aptHosts = DefaultAptHosts
	}
	return hostAllowed(host, aptHosts)
}

// hostKey is a hostname as this service compares one: lower case, and
// without the port, since an archive on a non-standard port is the same
// archive.
func hostKey(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[i:], "]") {
		host = host[:i]
	}
	return host
}

// normalizedHostMap is a host-keyed configuration map with its keys put
// through hostKey, so that a deployment naming "Deb.Debian.org" and a
// sandbox asking for "deb.debian.org:80" meet.
func normalizedHostMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for host, value := range in {
		if value = strings.TrimSpace(value); value != "" {
			out[hostKey(host)] = value
		}
	}
	return out
}

// hostOnly extracts host without port from a URL string or host:port.
func hostOnly(raw string) string {
	raw = strings.TrimSpace(raw)
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		raw = u.Host
	}
	if h, _, err := net.SplitHostPort(raw); err == nil {
		return h
	}
	return raw
}
