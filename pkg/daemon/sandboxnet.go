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
	"strconv"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/modelproxy"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// SandboxPortBlockSize is how many ports the block reserves. A power of
// two, and the base is required to be a multiple of it (checkPortBase),
// so the whole block is one aligned range: a single nftables
// "dport base-base+255", one GCP firewall rule, one security-group entry
// -- and, on an ACL that masks ports rather than ranging them, a single
// mask. Adding a service takes the next free offset and changes no rule
// anywhere.
//
// 256 is far more than the services there are, deliberately: the
// point of a block is that it is sized once.
const SandboxPortBlockSize = 256

// Offsets within the block. Written out rather than assigned in
// registration order, because a port that moves is a port some live
// sandbox is still holding the old number for: a sandbox's environment is
// written once, when it is created (see actionsCache.grant), so an
// offset that shifts when a service is added breaks every sandbox that
// outlived the change.
//
// Three bands, so that adding one kind of service never moves another:
//
//	0-15     the singleton services
//	16-47    one per model-proxy vendor, in modelproxy.Vendors() order
//	48-255   unassigned
//
// A name with no offset here is a programming error, not a reason to fall
// back to an ephemeral port: an ephemeral port is outside the block, so
// no firewall rule covers it and the service would be unreachable
// anyway -- just less obviously. sandboxPortOffsets fails loudly instead.
const (
	OffsetGitProxy     = 0
	OffsetPackageCache = 1
	OffsetActionsCache = 2
	// OffsetCapabilityPorts is the relay capability grants' ports are
	// forwarded through (pkg/capability/portforward).
	OffsetCapabilityPorts = 3
	// OffsetCIAPI is the listener a CI job holding api grants calls
	// grain's API through (pkg/capability/ciapi).
	OffsetCIAPI = 4

	OffsetModelProxyBase = 16
	OffsetModelProxyEnd  = 48
)

// CapabilityPortsService is the capability port relay's name on the
// fabric.
const CapabilityPortsService = "capability ports"

// CIAPIService is the CI API listener's name on the fabric.
const CIAPIService = "ci api"

// sandboxPortOffsets is the name-to-offset table, with the model proxies
// derived from modelproxy.Vendors() rather than listed.
//
// Derived because startModelProxy's own Serve call is inside a loop over
// that list: a fourth vendor appears there with no edit, and it should
// appear here the same way rather than needing a hand-written line that
// somebody will forget. Its offset is its index in that list, which is
// stable for as long as the list is append-only -- reordering Vendors()
// moves ports, and is the one edit to that slice worth thinking twice
// about.
func SandboxPortOffsets() (map[string]int, error) {
	offsets := map[string]int{
		"git proxy":     OffsetGitProxy,
		"package cache": OffsetPackageCache,
		"Actions cache": OffsetActionsCache,

		CapabilityPortsService: OffsetCapabilityPorts,
		CIAPIService:           OffsetCIAPI,
	}
	for i, vendor := range modelproxy.Vendors() {
		offset := OffsetModelProxyBase + i
		if offset >= OffsetModelProxyEnd {
			return nil, fmt.Errorf("sandboxnet: %d model-proxy vendors do not fit the band %d-%d reserved for them",
				len(modelproxy.Vendors()), OffsetModelProxyBase, OffsetModelProxyEnd-1)
		}
		offsets[vendor.Name+" proxy"] = offset
	}
	return offsets, nil
}

// checkPortBase rejects a base that would put the block somewhere it
// cannot safely live. Zero is not a base at all -- it selects the
// ephemeral behaviour this package had before the block existed -- and is
// handled by the caller.
func checkPortBase(base int) error {
	if base%SandboxPortBlockSize != 0 {
		return fmt.Errorf("sandbox port base %d is not a multiple of %d, so the block is not an aligned range",
			base, SandboxPortBlockSize)
	}
	if base <= 0 || base+SandboxPortBlockSize > 65536 {
		return fmt.Errorf("sandbox port base %d leaves no room for %d ports below 65536",
			base, SandboxPortBlockSize)
	}
	return nil
}

// Endpoint is one published service: what it is called, what a sandbox
// is told to reach it at, and where its listener actually is.
//
// URL and Bound differ exactly when this deployment's sandboxes cannot
// reach this process's own loopback -- see Direct. Keeping both is what
// lets a startup line say "serving at <what a sandbox uses> (bound
// <where it really is>)" without either being inferred from the other.
type Endpoint struct {
	// Name is what this service calls itself in a log line: "git proxy",
	// "claude proxy", "package cache", "Actions cache". It is the name
	// Serve reports a serve error under, so it reads the way the
	// hand-written log line it replaced did.
	Name string
	// URL is what a sandbox is handed -- scheme, host and port.
	URL string
	// Bound is the address the listener is actually on, as the listener
	// itself reports it rather than as the request for it was spelled.
	Bound string
}

// Fabric publishes the services this deployment's sandboxes reach.
//
// It is an interface with one implementation today because the second
// one is the point: a deployment whose sandboxes are built somewhere
// other than this machine (a remote docker daemon, a VM in another
// cloud) has to publish the same set of services over a route that is
// not "an address on this host". Every caller here asks for a URL and is
// given one, which is the whole of what it needs to know.
type Fabric interface {
	// Serve starts handler on a listener of the fabric's choosing and
	// returns the endpoint a sandbox reaches it at, together with the
	// shutdown for that listener.
	//
	// The returned shutdown is the listener's alone: a caller that owns
	// other things to stop composes them itself, the way each of these
	// services already did with its own *http.Server.
	Serve(name string, handler http.Handler) (Endpoint, func(context.Context) error, error)

	// Host is the address this deployment's sandboxes reach it at, and
	// empty where they share this process's own loopback and there is no
	// address to name.
	//
	// It exists for the listeners Serve cannot place: the package cache's
	// interception pair is on ports 443 and 80 because the protocol says
	// so, at the address a sandbox's resolver was pointed at, so what it
	// needs is a host and not a URL. Everything with a port to spare
	// should take one from Serve and never ask this.
	//
	// It is here rather than left to sandboxAdvertiseHost(cfg) at the
	// point of use so that the fixed-port listeners and the published
	// ones cannot end up describing different machines -- the same
	// one-function rule packageCacheDNSAddrs keeps between the resolver's
	// bind and the address it hands out.
	Host() string

	// Endpoints is everything Serve has published so far, in the order
	// it was published.
	//
	// Nothing in this process needs the whole set yet; a fabric that
	// has to make these reachable from another machine does, and it
	// should not have to be told about a new service to carry it. That
	// is the property this refactor is for.
	Endpoints() []Endpoint
}

// Direct is the fabric for a deployment whose sandboxes reach this
// process over ordinary host networking: they are on this machine, or on
// a network that routes to an address of it.
//
// advertise is that address, and empty means there is none to name --
// HostSandboxes, which shares this process's own network namespace and
// so reaches 127.0.0.1 for real. The bind follows from it: loopback only
// when nothing has to reach in from outside, every interface when
// something does. That is what each of the four call sites did
// individually, preserved here exactly, so a deployment that was working
// before this file existed is served by the same listeners on the same
// kind of address.
type Direct struct {
	advertise string

	// base is where this deployment's port block starts, and 0 means
	// there is no block: every listener takes an ephemeral port, which is
	// what this package did before the block existed and what
	// -sandbox-port-base=0 restores.
	base int
	// offsets is sandboxPortOffsets' table, nil when base is 0.
	offsets map[string]int

	// via is the address a sandbox is handed for each service named in
	// it, in place of advertise -- see AdvertiseVia. Nil for every
	// deployment that has not put anything in front of this process.
	via map[string]string

	mu        sync.Mutex
	published []Endpoint
}

// NewDirect returns a Direct advertising advertise, which is
// sandboxAdvertiseHost's answer for this deployment: the address a
// sandbox reaches this host at, or empty where a sandbox shares this
// process's own loopback.
//
// Its listeners take ephemeral ports. Callers that want the fixed block
// use NewDirectWithPortBlock; this one stays for the tests and callers
// that only care where a listener is advertised, not which port it has.
func NewDirect(advertise string) *Direct { return &Direct{advertise: advertise} }

// NewDirectWithPortBlock is NewDirect with every service on a known port:
// base plus the offset sandboxPortOffsets assigns its name.
//
// A base of 0 is the ephemeral behaviour NewDirect gives, so a deployment
// can opt out with -sandbox-port-base=0 and get exactly what it had
// before this existed.
//
// What the block buys, beyond one firewall rule: the ports stop moving.
// Every listener used to take an ephemeral port, so a daemon restart
// renumbered all six -- and a sandbox is handed its URLs once, when it is
// created, so every sandbox that outlived a restart was holding addresses
// that had moved. A fixed block is what makes a restart survivable for
// sandboxes that are still running.
func NewDirectWithPortBlock(advertise string, base int) (*Direct, error) {
	if base == 0 {
		return NewDirect(advertise), nil
	}
	if err := checkPortBase(base); err != nil {
		return nil, err
	}
	offsets, err := SandboxPortOffsets()
	if err != nil {
		return nil, err
	}
	return &Direct{advertise: advertise, base: base, offsets: offsets}, nil
}

// PortBlock is the range this fabric's listeners live in, for the one
// firewall rule that has to allow them, and 0,0 for a deployment with no
// block. Reported at startup so an operator has the range without
// working it out from a flag and a constant.
func (d *Direct) PortBlock() (first, last int) {
	if d.base == 0 {
		return 0, 0
	}
	return d.base, d.base + SandboxPortBlockSize - 1
}

// port is the port name must bind, and 0 for "any free one".
func (d *Direct) Port(name string) (int, error) {
	if d.base == 0 {
		return 0, nil
	}
	offset, ok := d.offsets[name]
	if !ok {
		return 0, fmt.Errorf("sandboxnet: no port offset is reserved for %q -- "+
			"add one to sandboxPortOffsets rather than letting it take an ephemeral "+
			"port outside the block, which no firewall rule covers", name)
	}
	return d.base + offset, nil
}

// K8sServiceFrontedNames are the services a -k8s-sandbox-service fronts:
// the git proxy and one model proxy per vendor, in Serve's own names.
//
// Those are the ones a sandbox cannot do its task without -- a run
// adopted by the next daemon pod still has a pull request to push and
// model calls to make -- and the ones that authenticate by the token a
// sandbox was given rather than by where it called from. The two caches
// are left on this pod's own address on purpose: the package cache tells
// a sandbox with no network apart by its source address
// (pkgcache.ClientSet), and a Service whose traffic is masqueraded on its
// way through would turn that refusal into a pass.
func K8sServiceFrontedNames() []string {
	names := []string{"git proxy"}
	for _, vendor := range modelproxy.Vendors() {
		names = append(names, vendor.Name+" proxy")
	}
	return names
}

// AdvertiseVia hands sandboxes host for each service in names, instead
// of this fabric's own advertised address. It is for an address that
// outlives this process -- a Kubernetes Service's cluster IP, which the
// next daemon pod inherits and this pod's own IP is not -- and so it
// needs the port block: a Service forwards fixed ports, and an
// ephemeral one would be a port it does not carry.
//
// Called before Serve, since a URL already handed out is not reissued.
// The listeners themselves are unchanged: they still bind every
// interface of this pod, which is where the Service delivers to.
func (d *Direct) AdvertiseVia(host string, names []string) error {
	if d.base == 0 {
		return fmt.Errorf("sandboxnet: advertising through %s needs the port block (-sandbox-port-base), "+
			"since what forwards to these services forwards fixed ports", host)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.via == nil {
		d.via = map[string]string{}
	}
	for _, name := range names {
		d.via[name] = host
	}
	return nil
}

// Host implements Fabric: the advertised address itself, which under
// Direct is what a sandbox reaches this host at.
func (d *Direct) Host() string { return d.advertise }

// bindHost is loopback when no address has to be reachable from outside
// this process's network namespace, and the wildcard when one does.
func (d *Direct) bindHost() string {
	if d.advertise == "" {
		return "127.0.0.1"
	}
	return "0.0.0.0"
}

// Serve implements Fabric.
func (d *Direct) Serve(name string, handler http.Handler) (Endpoint, func(context.Context) error, error) {
	port, err := d.Port(name)
	if err != nil {
		return Endpoint{}, nil, err
	}
	// A port the block reserved and something else already holds fails
	// here rather than falling back to an ephemeral one. The caller's own
	// handling is already right for that: startModelProxies reports the
	// vendor whose listener would not bind and serves the other two, and
	// both caches and the git proxy are logged-and-left-nil at their call
	// sites. A silent fallback would put the service on a port outside
	// the block, which no firewall rule covers -- so it would be
	// unreachable anyway, with nothing said about why.
	ln, err := net.Listen("tcp", net.JoinHostPort(d.bindHost(), strconv.Itoa(port)))
	if err != nil {
		return Endpoint{}, nil, err
	}
	// Bound the read phase for request headers and reclaim idle keep-alive
	// connections, preventing slowloris connection exhaustion from slow or
	// malicious sandboxes.
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("grain daemon: %s: %v", name, err)
		}
	}()

	endpoint := Endpoint{Name: name, Bound: ln.Addr().String()}
	if d.advertise == "" {
		// The bound address is the reachable one, port and all, so it is
		// named rather than rebuilt: a sandbox sharing this namespace
		// reaches exactly what this listener is on.
		endpoint.URL = "http://" + ln.Addr().String()
	} else {
		// The wildcard this bound on is not an address anything can be
		// told to use, so only the port is carried over from it.
		port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
		d.mu.Lock()
		host, ok := d.via[name]
		d.mu.Unlock()
		if !ok {
			host = d.advertise
		}
		endpoint.URL = "http://" + net.JoinHostPort(host, port)
	}

	d.mu.Lock()
	d.published = append(d.published, endpoint)
	d.mu.Unlock()
	return endpoint, srv.Shutdown, nil
}

// Endpoints implements Fabric. The slice is a copy: a caller ranging
// over it while another service is still starting gets a stable answer
// rather than a race.
func (d *Direct) Endpoints() []Endpoint {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]Endpoint(nil), d.published...)
}

// sandboxServicePorts answers orchestrator.GranuleConfig's
// SandboxServicePorts: the only ports a NetworkNone guest's host firewall
// chain accepts at the docker gateway.
//
// Read off what is actually listening rather than listed: every port the
// fabric has published (git proxy, model proxies, both caches -- whatever
// Serve was asked for, so a seventh service is covered with no edit
// here), and the package cache's resolver and interception pair only
// while all three are serving. A port nothing of grain's is bound to is
// left out on purpose: with the package cache's interception down, 80
// and 443 on the gateway are whatever else on this host holds them --
// a -ui-addr of 0.0.0.0:80, for one.
//
// The granule backend is built before either of those exists, so it is
// handed this and asks it when a NetworkNone VM's chain is written;
// serve and withPackageCache fill it in as the daemon starts them, before
// anything is dispatched.
type SandboxServicePorts struct {
	mu     sync.Mutex
	fabric Fabric
	cache  *PackageCache
}

func (s *SandboxServicePorts) Serve(fabric Fabric) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fabric = fabric
}

func (s *SandboxServicePorts) WithPackageCache(cache *PackageCache) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache = cache
}

func (s *SandboxServicePorts) Ports() []orchestrator.SandboxServicePort {
	s.mu.Lock()
	fabric, cache := s.fabric, s.cache
	s.mu.Unlock()
	var out []orchestrator.SandboxServicePort
	if fabric != nil {
		for _, endpoint := range fabric.Endpoints() {
			_, portStr, err := net.SplitHostPort(endpoint.Bound)
			if err != nil {
				continue
			}
			if port, err := strconv.Atoi(portStr); err == nil {
				out = append(out, orchestrator.SandboxServicePort{Proto: "tcp", Port: port})
			}
		}
	}
	if cache != nil && cache.DnsServing {
		out = append(out,
			orchestrator.SandboxServicePort{Proto: "udp", Port: 53},
			orchestrator.SandboxServicePort{Proto: "tcp", Port: 53},
			orchestrator.SandboxServicePort{Proto: "tcp", Port: 80},
			orchestrator.SandboxServicePort{Proto: "tcp", Port: 443},
		)
	}
	return out
}

// EndpointPorts returns the TCP ports bound by every HTTP service published on
// the underlying Fabric (git proxy, package cache, Actions cache, capability
// ports, model proxies). Used by off-cluster K8sGranuleSandboxes to tunnel
// guest 127.0.0.1:<port> to controller 127.0.0.1:<port> over `granule forward`.
func (s *SandboxServicePorts) EndpointPorts() []int {
	s.mu.Lock()
	fabric := s.fabric
	s.mu.Unlock()
	if fabric == nil {
		return nil
	}
	var out []int
	for _, endpoint := range fabric.Endpoints() {
		_, portStr, err := net.SplitHostPort(endpoint.Bound)
		if err != nil {
			continue
		}
		if port, err := strconv.Atoi(portStr); err == nil && port > 0 {
			out = append(out, port)
		}
	}
	return out
}
