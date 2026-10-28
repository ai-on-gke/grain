package cli

// The model proxies as a daemon actually starts them: one more listener
// per vendor, reachable from where a sandbox is, holding the credential
// and handing out tokens through the capability registry this same
// process builds.

import (
	"context"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/daemon"

	modelproxycap "github.com/bwsalmon/grain/pkg/capability/modelproxy"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/modelproxy"
	"github.com/bwsalmon/grain/pkg/secrets"
)

// startTestModelProxies starts one vendor proxy per vendor for this test
// alone and hands back the endpoints they were published into -- a set
// made here rather than the package-level modelProxies the daemon
// serves.
//
// That is the whole of what these tests do differently from runDaemon,
// and it is deliberate: modelProxies is one set of mutable endpoints
// shared by everything in this binary, so two tests that both started
// the proxies wrote the same endpoint's credential and URL, which the
// race detector reported as a data race and which failed whichever four
// tests happened to be running at the time ("race detected during
// execution of test", on tests that had nothing to do with the proxies).
// Serialising the tests that write them was the first answer and only
// half of one: nothing outside this file would have honoured that lock,
// so a test that started a daemon -- or a second copy of one of these
// under -count=2 -- was racing again. A test that owns its endpoints
// races with nobody, and every test here stays parallel.
//
// startModelProxies and capabilityProviders both take the endpoints they
// are wiring for exactly this reason; nothing in a test reaches for
// modelProxies itself.
func startTestModelProxies(t *testing.T, cfg daemon.Config, store *secrets.Store) map[string]*modelproxy.Endpoint {
	t.Helper()
	proxies := daemon.NewModelProxies()
	// The fabric is built from cfg exactly as run() builds it, so a test
	// naming an advertise host gets proxies advertised there rather than
	// on loopback.
	stop, err := daemon.StartModelProxies(cfg, daemon.NewDirect(daemon.SandboxAdvertiseHost(cfg)), proxies, store, nil)
	if err != nil {
		t.Fatalf("startModelProxies: %v", err)
	}
	t.Cleanup(func() { _ = stop(context.Background()) })
	return proxies
}

// teamConnections is a deployment whose default team has one connection
// per vendor, "<vendor>-conn", each holding "a-real-<vendor>-credential"
// as its own key -- and whose deployment-wide vendor secrets hold
// something else, which no proxy may ever forward with.
func teamConnections(t *testing.T, secretStore *secrets.Store) daemon.Config {
	t.Helper()
	var cfg daemon.Config
	for _, vendor := range modelproxy.Vendors() {
		id := vendor.Name + "-conn"
		cfg.AgentBackends = append(cfg.AgentBackends, model.AgentBackend{ID: id, Framework: vendor.Framework, Team: model.DefaultTeamID})
		if err := secretStore.Set(secrets.AgentBackendCredentialSecret(id), secrets.AgentCredentialKey, []byte("a-real-"+vendor.Name+"-credential")); err != nil {
			t.Fatal(err)
		}
		if err := secretStore.Set(vendor.Secret, secrets.AgentCredentialKey, []byte("the-deployment-wide-"+vendor.Name+"-key")); err != nil {
			t.Fatal(err)
		}
	}
	return cfg
}

// A deployment whose sandboxes are directories on this host reaches each
// proxy on loopback, exactly as it reaches the git proxy.
func TestTheModelProxiesServeOnLoopbackByDefault(t *testing.T) {
	t.Parallel()
	secretStore := testSecrets(t)
	proxies := startTestModelProxies(t, teamConnections(t, secretStore), secretStore)

	for _, vendor := range modelproxy.Vendors() {
		endpoint := proxies[vendor.Name]
		url := endpoint.URL()
		if !strings.HasPrefix(url, "http://127.0.0.1:") {
			t.Fatalf("the %s proxy advertises %q, want a loopback address", vendor.Name, url)
		}
		// Reachable, and refusing anything holding no token it minted --
		// which is the whole of what it does before a run is dispatched.
		resp, err := http.Post(url+"/v1/models", "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("calling the %s proxy: %v", vendor.Name, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("an untokened call to the %s proxy got %d: %s", vendor.Name, resp.StatusCode, body)
		}

		// And the credential it would forward with is the team's own
		// connection's for that vendor -- never the deployment-wide
		// secret -- resolved live rather than read at startup.
		connection, err := endpoint.ConnectionFor(t.Context(), "")
		if err != nil || connection.ID != vendor.Name+"-conn" {
			t.Fatalf("%s ConnectionFor = %+v, %v, want the team's %s-conn", vendor.Name, connection, err, vendor.Name)
		}
		credential, err := endpoint.ResolveCredential(t.Context(), connection)
		if err != nil || credential != "a-real-"+vendor.Name+"-credential" {
			t.Fatalf("%s ResolveCredential = %q, %v, want the key the team's connection holds",
				vendor.Name, credential, err)
		}
		if err := secretStore.Set(secrets.AgentBackendCredentialSecret(connection.ID), secrets.AgentCredentialKey, []byte("a-rotated-"+vendor.Name+"-credential")); err != nil {
			t.Fatal(err)
		}
		if credential, _ := endpoint.ResolveCredential(t.Context(), connection); credential != "a-rotated-"+vendor.Name+"-credential" {
			t.Fatalf("after the secret was replaced the %s proxy still forwards %q: a rotated credential "+
				"should reach the next call", vendor.Name, credential)
		}
	}
}

// One listener per vendor, and no two of them on the same port: a proxy
// sharing an address with another vendor's would forward this
// deployment's Anthropic credential to OpenAI.
func TestEachVendorGetsItsOwnListener(t *testing.T) {
	t.Parallel()
	proxies := startTestModelProxies(t, daemon.Config{}, testSecrets(t))
	seen := map[string]string{}
	for _, vendor := range modelproxy.Vendors() {
		url := proxies[vendor.Name].URL()
		if url == "" {
			t.Fatalf("the %s proxy is not serving anywhere", vendor.Name)
		}
		if other, dup := seen[url]; dup {
			t.Errorf("%s and %s are both advertised at %s", other, vendor.Name, url)
		}
		seen[url] = vendor.Name
	}
}

// A granule guest has its own 127.0.0.1 that never reaches this process's
// (see -granule-git-proxy-host), so each proxy binds every interface and
// advertises the address a guest actually routes to -- the same one the
// git proxy is advertised at, which is why there is no second flag.
func TestTheModelProxiesAreAdvertisedWhereAGranuleGuestCanReachThem(t *testing.T) {
	t.Parallel()
	host := nonLoopbackHost(t)
	proxies := startTestModelProxies(t, daemon.Config{GranuleGitProxyHost: host}, testSecrets(t))

	for _, vendor := range modelproxy.Vendors() {
		url := proxies[vendor.Name].URL()
		if !strings.HasPrefix(url, "http://"+host+":") {
			t.Fatalf("the %s proxy advertises %q, want the advertised host %q", vendor.Name, url, host)
		}
		// Bound on that interface for real, not merely named in a string:
		// a URL naming an address nothing is listening on is exactly the
		// failure bwsalmon/agents#567 was.
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Post(url+"/v1/models", "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("calling the %s proxy at its advertised address: %v", vendor.Name, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("the %s proxy answered %d, want 401 from a call holding no token", vendor.Name, resp.StatusCode)
		}
	}
}

// The capability a task is granted and the proxy this process serves are
// two ends of one thing: the provider the registry builds has to mint a
// token the running proxy actually honours, and stop honouring it when
// the run's lease is revoked -- for each vendor, against that vendor's
// own proxy.
func TestTheGrantedCapabilitiesMintTokensTheseProxiesHonour(t *testing.T) {
	t.Parallel()
	secretStore := testSecrets(t)
	proxies := startTestModelProxies(t, teamConnections(t, secretStore), secretStore)

	// Built over this test's own endpoints, the way runDaemon builds
	// them over the ones it just started: what is under test is that the
	// provider capabilityProviders registers mints into the very proxy
	// this process is serving, whichever set of endpoints that is.
	registered := map[string]model.CapabilityProvider{}
	for _, p := range daemon.CapabilityProviders(daemon.Config{}, nil, proxies, nil, nil) {
		registered[p.Spec().Name] = p
	}

	for _, vendor := range modelproxy.Vendors() {
		name := modelproxycap.CapabilityName(vendor)
		provider := registered[name]
		if provider == nil {
			t.Fatalf("no %q provider is registered", name)
		}

		cc := model.CapabilityContext{Run: model.Run{ID: "run-7"}, Now: time.Now().UTC()}
		resolution, err := provider.Resolve(t.Context(), cc)
		if err != nil {
			t.Fatal(err)
		}
		if resolution.Refused {
			t.Fatalf("a deployment serving a proxy and holding a credential refused %q: %s", name, resolution.Reason)
		}
		materialization, err := provider.Materialize(t.Context(), cc)
		if err != nil {
			t.Fatalf("Materialize %q: %v", name, err)
		}

		var url, token string
		for _, p := range materialization.Placements {
			switch p.Path {
			case modelproxycap.URLPath(vendor):
				url = strings.TrimSpace(p.Content)
			case modelproxycap.TokenPath(vendor):
				token = strings.TrimSpace(p.Content)
			}
		}
		endpoint := proxies[vendor.Name]
		if want := endpoint.URL() + vendor.BaseURLSuffix; url != want {
			t.Fatalf("the sandbox is told the %s proxy is at %q, this process serves it at %q",
				vendor.Name, url, want)
		}
		if owner, connection, ok := endpoint.Tokens.Lookup(token); !ok || owner != "run-7" || connection.ID != vendor.Name+"-conn" {
			t.Fatalf("the placed %s token authenticates as %q on %+v, %v, want run-7 on %s-conn", vendor.Name, owner, connection, ok, vendor.Name)
		}
		if err := provider.Revoke(t.Context(), cc, *materialization.Lease); err != nil {
			t.Fatal(err)
		}
		if _, ok := endpoint.Tokens.Authenticate(token); ok {
			t.Fatalf("the %s token still works after the run's lease was revoked", vendor.Name)
		}
	}
}

// And the other half of that: the endpoints a test starts its proxies
// into are its own objects, not the process-wide ones the daemon serves.
// This is the invariant that keeps every test in this package parallel,
// so it is worth failing on rather than rediscovering as "race detected
// during execution of test" in some unrelated test months later -- if
// this fails, startTestModelProxies has gone back to handing out the
// daemon's endpoints and every test above is writing the same three
// again.
//
// It is identity that is checked, rather than the daemon's endpoints
// being unpublished: this binary does start a real daemon in places
// (daemon_schema_migration_test.go), which publishes into modelProxies
// exactly as a running deployment does, so "modelProxies has no URL" is
// true only until one of those tests has run. Whether two endpoints are
// the same object is true whatever else the binary has done.
func TestATestsProxiesAreNotTheDaemonsProxies(t *testing.T) {
	t.Parallel()
	proxies := startTestModelProxies(t, daemon.Config{}, testSecrets(t))
	for _, vendor := range modelproxy.Vendors() {
		if proxies[vendor.Name].URL() == "" {
			t.Fatalf("the %s proxy this test started is not serving anywhere", vendor.Name)
		}
		if proxies[vendor.Name] == daemon.ModelProxies[vendor.Name] {
			t.Errorf("the %s proxy this test started went into the process-wide endpoint: a test "+
				"is writing the endpoints the daemon serves, which every other test in this package shares",
				vendor.Name)
		}
		if url := proxies[vendor.Name].URL(); url == daemon.ModelProxies[vendor.Name].URL() {
			t.Errorf("this test's %s proxy and the process-wide one are both advertised at %q", vendor.Name, url)
		}
	}
}

// nonLoopbackHost is an address on this machine that is not 127.0.0.1 --
// standing in for the docker bridge gateway a granule guest routes to.
func nonLoopbackHost(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Skipf("no interfaces to test an advertised address against: %v", err)
	}
	for _, addr := range addrs {
		ipnet, ok := addr.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.To4() == nil {
			continue
		}
		return ipnet.IP.String()
	}
	t.Skip("this machine has no non-loopback IPv4 address to advertise")
	return ""
}

// The picker and the registry have to agree about these capabilities the
// way they do about every other one -- cmd/grain's own
// TestEveryRegisteredCapabilityIsGrantable covers the pair in general;
// this is the one line of it worth naming, since a vendor's proxy
// registered but not offered would be a proxy no task could ever be
// granted.
func TestEveryModelProxyIsBothRegisteredAndGrantable(t *testing.T) {
	t.Parallel()
	var registered []string
	for _, p := range daemon.CapabilityProviders(daemon.Config{}, nil, daemon.NewModelProxies(), nil, nil) {
		registered = append(registered, p.Spec().Name)
	}
	for _, vendor := range modelproxy.Vendors() {
		if !slices.Contains(registered, modelproxycap.CapabilityName(vendor)) {
			t.Errorf("capabilityProviders registers %v, without %q", registered, modelproxycap.CapabilityName(vendor))
		}
	}
}
