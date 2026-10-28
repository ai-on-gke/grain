package cli

import (
	"context"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/modelproxy"
	"github.com/ai-on-gke/grain/pkg/ports"
)

// TestDirectServesLoopbackWhenNothingHasToReachIn is the host-sandboxing
// case: no advertised address, so the listener stays on loopback and the
// URL names the address it actually bound, port and all.
func TestDirectServesLoopbackWhenNothingHasToReachIn(t *testing.T) {
	fabric := daemon.NewDirect("")
	endpoint, stop, err := fabric.Serve("test service", http.NotFoundHandler())
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	defer stop(context.Background())

	if !strings.HasPrefix(endpoint.URL, "http://127.0.0.1:") {
		t.Errorf("URL = %q, want it on loopback", endpoint.URL)
	}
	if endpoint.URL != "http://"+endpoint.Bound {
		t.Errorf("URL = %q and Bound = %q; with no advertised address the "+
			"reachable address is the bound one", endpoint.URL, endpoint.Bound)
	}
	if fabric.Host() != "" {
		t.Errorf("Host = %q, want empty: there is no address to name", fabric.Host())
	}
}

// TestDirectAdvertisesTheAddressSandboxesCanRouteTo is the isolated-backend
// case: a granule guest or a pod cannot reach this process's own loopback,
// so the listener takes every interface and the URL names the address the
// deployment supplied -- carrying over the port and nothing else, since
// the wildcard it bound is not something a sandbox can be told to use.
func TestDirectAdvertisesTheAddressSandboxesCanRouteTo(t *testing.T) {
	fabric := daemon.NewDirect("172.17.0.1")
	endpoint, stop, err := fabric.Serve("test service", http.NotFoundHandler())
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	defer stop(context.Background())

	if !strings.HasPrefix(endpoint.URL, "http://172.17.0.1:") {
		t.Errorf("URL = %q, want it on the advertised address", endpoint.URL)
	}
	_, advertisedPort, err := net.SplitHostPort(strings.TrimPrefix(endpoint.URL, "http://"))
	if err != nil {
		t.Fatalf("parsing %q: %v", endpoint.URL, err)
	}
	_, boundPort, err := net.SplitHostPort(endpoint.Bound)
	if err != nil {
		t.Fatalf("parsing %q: %v", endpoint.Bound, err)
	}
	if advertisedPort != boundPort {
		t.Errorf("advertised port %s but bound %s -- the port is the one thing "+
			"that has to survive from the real listener", advertisedPort, boundPort)
	}
	// A wildcard bind, in whichever spelling this machine's stack reports
	// it -- Go answers "[::]" for a dual-stack 0.0.0.0 listener, which is
	// what the daemon's own startup line has always shown. What matters is
	// that it is not loopback: a sandbox outside this process's namespace
	// cannot reach that.
	boundHost, _, err := net.SplitHostPort(endpoint.Bound)
	if err != nil {
		t.Fatalf("parsing %q: %v", endpoint.Bound, err)
	}
	if ip := net.ParseIP(boundHost); ip == nil || !ip.IsUnspecified() {
		t.Errorf("Bound = %q, want every interface", endpoint.Bound)
	}
	if fabric.Host() != "172.17.0.1" {
		t.Errorf("Host = %q, want the advertised address", fabric.Host())
	}
}

// TestDirectServesWhatItWasGiven checks the listener actually carries the
// handler, rather than the endpoint being a string this computed.
func TestDirectServesWhatItWasGiven(t *testing.T) {
	fabric := daemon.NewDirect("")
	endpoint, stop, err := fabric.Serve("test service", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("served")) }))
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	defer stop(context.Background())

	resp, err := http.Get(endpoint.URL)
	if err != nil {
		t.Fatalf("GET %s: %v", endpoint.URL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if string(body) != "served" {
		t.Errorf("body = %q, want the handler's own answer", body)
	}
}

// TestEndpointsCarriesEveryPublishedService is the property the whole
// refactor exists for: a fabric that has to make these reachable from
// another machine can enumerate them instead of being told about each new
// one. A service added later shows up here without this file changing.
func TestEndpointsCarriesEveryPublishedService(t *testing.T) {
	fabric := daemon.NewDirect("")
	var stops []func(context.Context) error
	for _, name := range []string{"git proxy", "claude proxy", "package cache"} {
		_, stop, err := fabric.Serve(name, http.NotFoundHandler())
		if err != nil {
			t.Fatalf("Serve(%q): %v", name, err)
		}
		stops = append(stops, stop)
	}
	defer func() {
		for _, stop := range stops {
			stop(context.Background())
		}
	}()

	published := fabric.Endpoints()
	if len(published) != 3 {
		t.Fatalf("Endpoints has %d entries, want 3", len(published))
	}
	for i, want := range []string{"git proxy", "claude proxy", "package cache"} {
		if published[i].Name != want {
			t.Errorf("Endpoints()[%d].Name = %q, want %q (publication order)", i, published[i].Name, want)
		}
		if published[i].URL == "" {
			t.Errorf("Endpoints()[%d] (%s) has no URL", i, published[i].Name)
		}
	}
	// A copy, so a caller ranging over it while another service starts
	// does not have the slice grow underneath it.
	published[0].Name = "mutated"
	if fabric.Endpoints()[0].Name != "git proxy" {
		t.Error("Endpoints handed back its own backing array")
	}
}

// TestEverySandboxFacingServiceAgreesOnWhereSandboxesAre is the
// regression test for the bug that prompted sandboxnet.go.
//
// startModelProxy used to read cfg.granuleGitProxyHost directly while the
// git proxy and both caches asked sandboxAdvertiseHost. Nothing copies
// -container-git-proxy-host or -k8s-git-proxy-host into that field, so on
// either of those backends the model proxies advertised loopback -- which
// a sandbox container or a pod cannot reach -- while everything else was
// correctly on the routable address. An agent could clone and not reach a
// model.
//
// The check is on the config end rather than by starting four listeners:
// what went wrong was a service asking a different question, and this
// asserts there is only one question to ask. A new service that reaches
// for a backend's own flag instead of the fabric fails this.
func TestEverySandboxFacingServiceAgreesOnWhereSandboxesAre(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  daemon.Config
		want string
	}{
		{
			name: "container sandboxes",
			cfg:  daemon.Config{ContainerSandboxes: true, ContainerGitProxyHost: "172.17.0.1"},
			want: "172.17.0.1",
		},
		{
			name: "kubernetes sandboxes",
			cfg:  daemon.Config{K8sSandboxes: true, K8sGitProxyHost: "10.1.2.3"},
			want: "10.1.2.3",
		},
		{
			name: "granule sandboxes",
			cfg:  daemon.Config{GranuleSandboxes: true, GranuleGitProxyHost: "172.17.0.1"},
			want: "172.17.0.1",
		},
		{
			name: "host sandboxes share this process's loopback",
			cfg:  daemon.Config{SandboxDir: "/sandbox"},
			want: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Built exactly as run() builds it.
			fabric := daemon.NewDirect(daemon.SandboxAdvertiseHost(tc.cfg))
			if got := fabric.Host(); got != tc.want {
				t.Fatalf("fabric Host = %q, want %q", got, tc.want)
			}

			// Every service published through it lands on that same
			// address -- the model proxies included, which is the half
			// that used to differ.
			endpoint, stop, err := fabric.Serve("claude proxy", http.NotFoundHandler())
			if err != nil {
				t.Fatalf("Serve: %v", err)
			}
			defer stop(context.Background())

			host, _, err := net.SplitHostPort(strings.TrimPrefix(endpoint.URL, "http://"))
			if err != nil {
				t.Fatalf("parsing %q: %v", endpoint.URL, err)
			}
			if tc.want == "" {
				if host != "127.0.0.1" {
					t.Errorf("host sandboxing advertised %q, want loopback", host)
				}
				return
			}
			if host != tc.want {
				t.Errorf("a model proxy advertised %q while this deployment's sandboxes "+
					"reach it at %q -- the services have drifted apart again", host, tc.want)
			}
		})
	}
}

// TestModelProxiesArePublishedOnTheFabric drives the real
// startModelProxies against a fabric with an advertised address and
// checks the URL each vendor's endpoint was given names it.
//
// This is the end the bug actually showed at: the endpoint URL is what a
// sandbox is handed, so a proxy advertising loopback is a capability that
// cannot be used.
//
// The advertised address is deliberately NOT loopback. With a loopback
// one this test would pass against the bug as well: the broken code fell
// back to naming the address it bound, which for an empty
// granuleGitProxyHost was 127.0.0.1 -- the same string the fix produces.
// Only an address the two paths disagree about can tell them apart.
// Nothing connects to it; what is under test is the URL a sandbox is
// handed.
func TestModelProxiesArePublishedOnTheFabric(t *testing.T) {
	cfg := daemon.Config{ContainerSandboxes: true, ContainerGitProxyHost: "172.17.0.1"}
	fabric := daemon.NewDirect(daemon.SandboxAdvertiseHost(cfg))
	proxies := daemon.NewModelProxies()

	stop, err := daemon.StartModelProxies(cfg, fabric, proxies, nil, nil)
	if err != nil {
		t.Fatalf("startModelProxies: %v", err)
	}
	defer stop(context.Background())

	for name, endpoint := range proxies {
		url := endpoint.URL()
		if url == "" {
			t.Errorf("%s proxy has no URL", name)
			continue
		}
		if !strings.HasPrefix(url, "http://172.17.0.1:") {
			t.Errorf("%s proxy URL = %q, want it on the address this deployment's "+
				"sandboxes reach it at -- a proxy back on loopback is the bug "+
				"sandboxnet.go was written for", name, url)
		}
	}

	// And the fabric knows about all of them, which is what lets a
	// non-Direct fabric publish them somewhere else without being told
	// each vendor's name.
	if got := len(fabric.Endpoints()); got != len(proxies) {
		t.Errorf("fabric published %d endpoints for %d proxies", got, len(proxies))
	}
}

// TestThePortBlockIsOneAlignedRange is the property the block exists for:
// everything a sandbox reaches is inside one range, so what has to allow
// it is one rule.
func TestThePortBlockIsOneAlignedRange(t *testing.T) {
	if DefaultSandboxPortBase%daemon.SandboxPortBlockSize != 0 {
		t.Errorf("DefaultSandboxPortBase %d is not aligned to the block size %d, so the block "+
			"is not maskable", DefaultSandboxPortBase, daemon.SandboxPortBlockSize)
	}
	// Below the ephemeral range Linux allocates from by default. A block
	// inside it collides with the daemon's own outbound connections.
	if last := DefaultSandboxPortBase + daemon.SandboxPortBlockSize - 1; last >= 32768 {
		t.Errorf("the default block ends at %d, inside the usual ephemeral range (32768-60999)", last)
	}
	// Clear of the ports grain already has an opinion about.
	for _, taken := range []struct {
		port int
		what string
	}{
		{80, "the UI's default port"},
		{443, "the package cache's interception HTTPS listener"},
		{53, "the sandbox resolver"},
		{8420, "the UI's former default port, which older deployments still bind"},
		{4242, "the UI's default port"},
		{8422, "the executor listener"},
		{12000, "-granule-base-port's default"},
		{12255, "the top of -granule-base-port's own 256"},
	} {
		if taken.port >= DefaultSandboxPortBase && taken.port < DefaultSandboxPortBase+daemon.SandboxPortBlockSize {
			t.Errorf("the default block covers %d, which is %s", taken.port, taken.what)
		}
	}
	// Nor may it overlap the 42xx range the convention keeps for grain's
	// operator-facing ports (docs/ports.md).
	if DefaultSandboxPortBase <= ports.ConventionMax && DefaultSandboxPortBase+daemon.SandboxPortBlockSize > ports.ConventionMin {
		t.Errorf("the default block overlaps %d-%d, the range grain's fixed ports live in", ports.ConventionMin, ports.ConventionMax)
	}
}

// TestEveryServicePublishedTodayHasAReservedOffset: a service with no
// offset cannot be published at all, so this is what stops one being
// added without a port.
func TestEveryServicePublishedTodayHasAReservedOffset(t *testing.T) {
	offsets, err := daemon.SandboxPortOffsets()
	if err != nil {
		t.Fatalf("sandboxPortOffsets: %v", err)
	}
	want := []string{"git proxy", "package cache", "Actions cache", daemon.CapabilityPortsService}
	for _, vendor := range modelproxy.Vendors() {
		want = append(want, vendor.Name+" proxy")
	}
	for _, name := range want {
		if _, ok := offsets[name]; !ok {
			t.Errorf("no reserved offset for %q, which is published today", name)
		}
	}
	if len(offsets) != len(want) {
		t.Errorf("the table has %d entries for %d published services", len(offsets), len(want))
	}

	// Distinct, and inside the block.
	seen := map[int]string{}
	for name, offset := range offsets {
		if offset < 0 || offset >= daemon.SandboxPortBlockSize {
			t.Errorf("%q has offset %d, outside the block", name, offset)
		}
		if other, clash := seen[offset]; clash {
			t.Errorf("%q and %q share offset %d", name, other, offset)
		}
		seen[offset] = name
	}
}

// TestModelProxyOffsetsDoNotMoveWhenASingletonIsAdded is why the table
// has bands: adding a service among the singletons must not renumber the
// vendors, because a sandbox that outlived the change is still holding
// the old port.
func TestModelProxyOffsetsDoNotMoveWhenASingletonIsAdded(t *testing.T) {
	offsets, err := daemon.SandboxPortOffsets()
	if err != nil {
		t.Fatalf("sandboxPortOffsets: %v", err)
	}
	for _, vendor := range modelproxy.Vendors() {
		offset := offsets[vendor.Name+" proxy"]
		if offset < daemon.OffsetModelProxyBase || offset >= daemon.OffsetModelProxyEnd {
			t.Errorf("%s proxy is at offset %d, outside the %d-%d band reserved for vendors -- "+
				"a singleton added later would collide with it",
				vendor.Name, offset, daemon.OffsetModelProxyBase, daemon.OffsetModelProxyEnd-1)
		}
	}
	// And the singletons stay clear of that band.
	for _, offset := range []int{daemon.OffsetGitProxy, daemon.OffsetPackageCache, daemon.OffsetActionsCache} {
		if offset >= daemon.OffsetModelProxyBase {
			t.Errorf("a singleton is at offset %d, inside the vendor band", offset)
		}
	}
}

// TestServeTakesTheReservedPort checks the offset actually reaches the
// listener, and that the same service gets the same port on a restart --
// which is what a sandbox holding a URL across one depends on.
func TestServeTakesTheReservedPort(t *testing.T) {
	base := freeAlignedBlock(t)
	fabric, err := daemon.NewDirectWithPortBlock("", base)
	if err != nil {
		t.Fatalf("NewDirectWithPortBlock: %v", err)
	}
	endpoint, stop, err := fabric.Serve("git proxy", http.NotFoundHandler())
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	wantPort := base + daemon.OffsetGitProxy
	if _, port, _ := net.SplitHostPort(endpoint.Bound); port != strconv.Itoa(wantPort) {
		t.Errorf("bound %s, want port %d", endpoint.Bound, wantPort)
	}
	defer stop(context.Background())

	// A second fabric over the same block -- what a restart is -- resolves
	// the same service to the same port. Asserted on the number rather
	// than by rebinding: the first listener's port can still be held by
	// the kernel for a moment after Shutdown, and what matters here is
	// that the table is deterministic, which the bind above already showed
	// Serve honours.
	again, err := daemon.NewDirectWithPortBlock("", base)
	if err != nil {
		t.Fatalf("NewDirectWithPortBlock: %v", err)
	}
	afterRestart, err := again.Port("git proxy")
	if err != nil {
		t.Fatalf("port after restart: %v", err)
	}
	if afterRestart != wantPort {
		t.Errorf("after a restart the git proxy resolves to %d, was %d -- a sandbox holding the "+
			"first address would have been left pointing at nothing", afterRestart, wantPort)
	}
}

// TestAnUnreservedNameIsRefusedRatherThanGivenAnEphemeralPort: falling
// back would put the service outside the block, where no firewall rule
// covers it -- unreachable anyway, with nothing said about why.
func TestAnUnreservedNameIsRefusedRatherThanGivenAnEphemeralPort(t *testing.T) {
	fabric, err := daemon.NewDirectWithPortBlock("", freeAlignedBlock(t))
	if err != nil {
		t.Fatalf("NewDirectWithPortBlock: %v", err)
	}
	if _, _, err := fabric.Serve("a service nobody reserved a port for", http.NotFoundHandler()); err == nil {
		t.Error("Serve accepted a name with no reserved offset")
	} else if !strings.Contains(err.Error(), "sandboxPortOffsets") {
		t.Errorf("error %q does not say where to add the offset", err)
	}
}

// TestPortBaseMustBeAligned: an unaligned base is not one maskable range,
// which is the whole point.
func TestPortBaseMustBeAligned(t *testing.T) {
	for _, base := range []int{9000, 9729, 100, 65535} {
		if _, err := daemon.NewDirectWithPortBlock("", base); err == nil {
			t.Errorf("base %d was accepted", base)
		}
	}
	// Zero is not an unaligned base, it is the opt-out.
	fabric, err := daemon.NewDirectWithPortBlock("", 0)
	if err != nil {
		t.Fatalf("base 0 should be the ephemeral opt-out, got %v", err)
	}
	if first, last := fabric.PortBlock(); first != 0 || last != 0 {
		t.Errorf("PortBlock = %d-%d with no block", first, last)
	}
	endpoint, stop, err := fabric.Serve("anything at all", http.NotFoundHandler())
	if err != nil {
		t.Fatalf("with no block every name should be servable: %v", err)
	}
	defer stop(context.Background())
	if endpoint.URL == "" {
		t.Error("no URL")
	}
}

// freeAlignedBlock finds an aligned block this machine is not already
// using, so these tests do not fight the real default (or each other) for
// ports. It probes the offsets the table actually hands out, starting from
// a random block in [20480, 32768) so parallel `go test ./pkg/cli` runs do
// not all contend for 20480, and checks both loopback and wildcard since
// NewDirectWithPortBlock binds 0.0.0.0 when given an advertised host.
func freeAlignedBlock(t *testing.T) int {
	t.Helper()
	offsets, err := daemon.SandboxPortOffsets()
	if err != nil {
		t.Fatalf("sandboxPortOffsets: %v", err)
	}
	const lo, hi = 20480, 32768
	blocks := (hi - lo) / daemon.SandboxPortBlockSize
	start := rand.IntN(blocks)
	for i := range blocks {
		base := lo + ((start+i)%blocks)*daemon.SandboxPortBlockSize
		free := true
		var held []net.Listener
		for _, offset := range offsets {
			port := strconv.Itoa(base + offset)
			ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", port))
			if err != nil {
				free = false
				break
			}
			ln.Close()
			ln, err = net.Listen("tcp", net.JoinHostPort("0.0.0.0", port))
			if err != nil {
				free = false
				break
			}
			held = append(held, ln)
		}
		for _, ln := range held {
			ln.Close()
		}
		if free {
			return base
		}
	}
	t.Skip("no free aligned port block on this machine")
	return 0
}

// What a NetworkNone guest may reach at the gateway is what this process
// actually serves there: every port the fabric published, and the package
// cache's resolver and interception pair only while they are serving --
// with them down, 80 and 443 on the gateway are somebody else's.
func TestSandboxServicePortsAreWhatIsListening(t *testing.T) {
	ports := &daemon.SandboxServicePorts{}
	if got := ports.Ports(); len(got) != 0 {
		t.Errorf("ports before anything is served = %v, want none", got)
	}

	fabric := daemon.NewDirect("")
	_, stopGit, err := fabric.Serve("git proxy", http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	defer stopGit(context.Background())
	_, stopModel, err := fabric.Serve("claude proxy", http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	defer stopModel(context.Background())
	ports.Serve(fabric)

	var want []string
	for _, endpoint := range fabric.Endpoints() {
		_, port, _ := net.SplitHostPort(endpoint.Bound)
		want = append(want, "tcp/"+port)
	}
	render := func() string {
		var got []string
		for _, p := range ports.Ports() {
			got = append(got, p.Proto+"/"+strconv.Itoa(p.Port))
		}
		return strings.Join(got, " ")
	}
	if got := render(); got != strings.Join(want, " ") {
		t.Errorf("ports = %s, want the fabric's own %s", got, strings.Join(want, " "))
	}

	ports.WithPackageCache(&daemon.PackageCache{DnsServing: false})
	if got := render(); got != strings.Join(want, " ") {
		t.Errorf("ports with interception down = %s, want only the fabric's %s", got, strings.Join(want, " "))
	}
	ports.WithPackageCache(&daemon.PackageCache{DnsServing: true})
	want = append(want, "udp/53", "tcp/53", "tcp/80", "tcp/443")
	if got := render(); got != strings.Join(want, " ") {
		t.Errorf("ports with interception up = %s, want %s", got, strings.Join(want, " "))
	}
}

// TestAServiceFrontsTheGitProxyAndModelProxiesOnly is -k8s-sandbox-service
// at the fabric: the git proxy and every model proxy are handed out at the
// Service's address, on their own reserved ports, and the caches keep this
// pod's address -- the package cache tells a sandbox with no network apart
// by where it called from, which a Service in between can rewrite.
func TestAServiceFrontsTheGitProxyAndModelProxiesOnly(t *testing.T) {
	base := freeAlignedBlock(t)
	fabric, err := daemon.NewDirectWithPortBlock("10.1.2.3", base)
	if err != nil {
		t.Fatalf("NewDirectWithPortBlock: %v", err)
	}
	fronted := daemon.K8sServiceFrontedNames()
	if err := fabric.AdvertiseVia("10.96.0.42", fronted); err != nil {
		t.Fatalf("AdvertiseVia: %v", err)
	}
	if !slices.Contains(fronted, "git proxy") {
		t.Fatalf("K8sServiceFrontedNames = %v leaves out the git proxy, which is what an adopted run pushes through", fronted)
	}
	for _, vendor := range modelproxy.Vendors() {
		if !slices.Contains(fronted, vendor.Name+" proxy") {
			t.Errorf("K8sServiceFrontedNames = %v leaves out the %s proxy", fronted, vendor.Name)
		}
	}

	for _, name := range append(slices.Clone(fronted), "package cache", "Actions cache") {
		endpoint, stop, err := fabric.Serve(name, http.NotFoundHandler())
		if err != nil {
			t.Fatalf("Serve(%s): %v", name, err)
		}
		defer stop(context.Background())
		port, err := fabric.Port(name)
		if err != nil {
			t.Fatalf("Port(%s): %v", name, err)
		}
		wantHost := "10.1.2.3"
		if slices.Contains(fronted, name) {
			wantHost = "10.96.0.42"
		}
		if want := "http://" + net.JoinHostPort(wantHost, strconv.Itoa(port)); endpoint.URL != want {
			t.Errorf("%s is handed out at %s, want %s", name, endpoint.URL, want)
		}
	}
	if fabric.Host() != "10.1.2.3" {
		t.Errorf("Host = %q; the package cache's interception binds here, so it has to stay this pod's own "+
			"address, not one the pod cannot bind", fabric.Host())
	}
}

// TestAServiceNeedsThePortBlock: a Service forwards ports it was told,
// and an ephemeral one is none of them.
func TestAServiceNeedsThePortBlock(t *testing.T) {
	if err := daemon.NewDirect("10.1.2.3").AdvertiseVia("10.96.0.42", daemon.K8sServiceFrontedNames()); err == nil {
		t.Error("AdvertiseVia accepted a fabric with no port block")
	}
	cfg := kubeConfig()
	cfg.K8sSandboxService = "grain-sandbox"
	lookup := func(context.Context, string) ([]string, error) { return []string{"10.96.0.42"}, nil }
	if err := daemon.AdvertiseViaK8sService(context.Background(), cfg, daemon.NewDirect(cfg.K8sGitProxyHost), lookup); err == nil {
		t.Error("the daemon would start with -k8s-sandbox-service and -sandbox-port-base=0")
	}
}

// TestTheSandboxServiceIsResolvedToOneClusterIP: what a sandbox is
// handed is an IP and not the Service's name, so a sandbox with no
// network -- which can reach the daemon pod and not kube-dns -- can use
// it. A bare name is looked up in the deployment's namespace, and
// anything that is not one cluster IP is refused.
func TestTheSandboxServiceIsResolvedToOneClusterIP(t *testing.T) {
	answers := map[string][]string{
		"grain-sandbox.grain.svc":    {"10.96.0.42"},
		"elsewhere.other.svc":        {"10.96.7.7"},
		"headless.grain.svc":         {"10.1.2.3", "10.1.2.4"},
		"one-pod-headless.grain.svc": {"10.1.2.3"},
	}
	lookup := func(_ context.Context, name string) ([]string, error) {
		if addrs, ok := answers[name]; ok {
			return addrs, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	for service, want := range map[string]string{
		"grain-sandbox":       "10.96.0.42",
		"elsewhere.other.svc": "10.96.7.7",
	} {
		got, err := daemon.K8sSandboxServiceHost(context.Background(), lookup, service, "grain")
		if err != nil || got != want {
			t.Errorf("K8sSandboxServiceHost(%s) = %q, %v; want %s", service, got, err, want)
		}
	}
	// A name that never resolves is retried until the context ends, so
	// this one ends soon.
	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	for _, service := range []string{"missing", "headless"} {
		if got, err := daemon.K8sSandboxServiceHost(short, lookup, service, "grain"); err == nil {
			t.Errorf("K8sSandboxServiceHost(%s) = %q, want a refusal", service, got)
		}
	}

	// At startup: a headless Service with one pod resolves to that pod's
	// own address, which is the address this exists not to hand out.
	cfg := kubeConfig()
	cfg.SandboxPortBase = freeAlignedBlock(t)
	cfg.K8sSandboxService = "one-pod-headless"
	fabric, err := daemon.NewDirectWithPortBlock(cfg.K8sGitProxyHost, cfg.SandboxPortBase)
	if err != nil {
		t.Fatalf("NewDirectWithPortBlock: %v", err)
	}
	if err := daemon.AdvertiseViaK8sService(context.Background(), cfg, fabric, lookup); err == nil {
		t.Error("AdvertiseViaK8sService accepted a Service that resolves to this pod's own address")
	}
	cfg.K8sSandboxService = "missing"
	if err := daemon.AdvertiseViaK8sService(short, cfg, fabric, lookup); err == nil {
		t.Error("AdvertiseViaK8sService started with a Service it could not resolve, rather than falling " +
			"back to an address that will be gone after the next rollout")
	}
	cfg.K8sSandboxService = "grain-sandbox"
	if err := daemon.AdvertiseViaK8sService(context.Background(), cfg, fabric, lookup); err != nil {
		t.Fatalf("AdvertiseViaK8sService: %v", err)
	}
	endpoint, stop, err := fabric.Serve("git proxy", http.NotFoundHandler())
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	defer stop(context.Background())
	if want := "http://" + net.JoinHostPort("10.96.0.42", strconv.Itoa(cfg.SandboxPortBase+daemon.OffsetGitProxy)); endpoint.URL != want {
		t.Errorf("the git proxy is handed out at %s, want the Service's %s", endpoint.URL, want)
	}
}
