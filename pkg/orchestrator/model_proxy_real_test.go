package orchestrator_test

// The one claim pkg/modelproxy rests on that nothing on the controller
// can check: that a process *inside a granule guest* can reach the address
// a proxy advertises itself at.
//
// Everything else about those proxies is covered from the host, and
// covered well -- the handler and its refusals
// (pkg/modelproxy/proxy_test.go), the token registry (tokens_test.go),
// the capability that mints and places a token
// (pkg/capability/modelproxy), and startModelProxies binding loopback or
// every interface and advertising the right one of them
// (pkg/cli/modelproxy_test.go, reached over real HTTP from the test
// process). None of that touches the sentence the whole design stands
// on: "it needs no routing -- a granule guest can already reach the
// docker bridge gateway, so another listener on the same address is
// visible from inside the guest" (README, pkg/modelproxy's own doc
// comment). That sentence is about netshim's NAT, about the address
// -granule-git-proxy-host names really being the gateway the guest routes
// through, and about nothing in the guest image blocking outbound TCP to
// another port on it. A test process on the host can reach its own
// listener whatever any of those do.
//
// It is worth its own VM because the same claim, made for the git proxy,
// was wrong for months: the daemon advertised the proxy at its own
// 127.0.0.1, every guest's clone failed against a loopback that was the
// guest's and not the daemon's, and nothing noticed until
// bwsalmon/agents#567 -- because every test that could have caught it ran
// on the host, where 127.0.0.1 is the right answer. This is the same
// mistake made impossible for these proxies, in the only place it is
// visible: from inside a guest.
//
// All three vendors, in one VM. The routing claim is vendor-blind and
// would be proven by any one of them, but the *rest* of what a run is
// handed is not: the address placed in the sandbox is the one that
// vendor's base-URL variable wants (OPENAI_BASE_URL carries a /v1 the
// others do not), and the credential is presented the way that vendor
// takes one (Gemini's in the query string). Those are the lines the
// capability's own prompt tells a run to paste, and this is the only
// place they are executed rather than asserted. The VM is what costs
// something here; a second and third listener on it cost seconds.
//
// What it deliberately does not do is call any vendor. The upstream is a
// stand-in httptest server on the host, so this suite spends nothing, is
// not a live vendor test, and fails only for reasons this repository owns
// -- the proxies are real, the VM is real, the netshim NAT between them
// is real, and the requests really cross it.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	modelproxycap "github.com/ai-on-gke/grain/pkg/capability/modelproxy"
	"github.com/ai-on-gke/grain/pkg/granule"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/modelproxy"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// liveUpstream stands in for a vendor's own API the way
// pkg/gitproxy/live_test.go's `git http-backend` stands in for GitHub:
// the far end of the proxy, run locally, so the thing under test is the
// path to it rather than a vendor's availability or a credential this
// environment does not hold.
//
// It records what it was asked with rather than what it was asked, since
// the assertions here are about credentials: that the deployment's own
// reached it, and that the run's token -- which is worth nothing upstream
// -- never did. The query string is recorded beside the headers because
// for one of these vendors the credential is in it.
type liveUpstream struct {
	server *httptest.Server
	answer string

	mu       sync.Mutex
	requests []liveRequest
}

// liveRequest is one call the stand-in was sent, in the two places a
// credential can be.
type liveRequest struct {
	header http.Header
	query  url.Values
}

func newLiveUpstream(t *testing.T, answer string) *liveUpstream {
	t.Helper()
	up := &liveUpstream{answer: answer}
	up.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up.mu.Lock()
		up.requests = append(up.requests, liveRequest{header: r.Header.Clone(), query: r.URL.Query()})
		up.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"type":"message","role":"assistant","content":[{"type":"text","text":%q}]}`, up.answer)
	}))
	t.Cleanup(up.server.Close)
	return up
}

// seen is every request this stand-in has been sent.
func (up *liveUpstream) seen() []liveRequest {
	up.mu.Lock()
	defer up.mu.Unlock()
	return append([]liveRequest(nil), up.requests...)
}

// guestCall is the whole of what a run holding one of these capabilities
// is told to do (the capability's own PromptSection): read the two placed
// files into the two environment variables that vendor's clients read,
// and call the API. curl rather than an SDK because curl is in the guest
// image (the repository's Granulefile) and an SDK is not, and because
// what is being proven is that a TCP connection from the guest to the
// advertised address is answered -- for which the plainest client
// available says the most.
//
// The status code is printed separately from the body (-w/-o) so a
// refusal, which is a perfectly ordinary HTTP answer, is legible as one
// rather than arriving as a non-zero exit from curl.
func guestCall(vendor modelproxy.Vendor) string {
	// Where this vendor's request goes and how it carries a credential:
	// a path under the base URL the placed file holds, and either a
	// bearer or the `key=` parameter Google's API takes.
	path, auth := "/v1/messages", `-H "Authorization: Bearer $TOKEN"`
	switch vendor.Name {
	case modelproxy.OpenAI.Name:
		// The placed base URL already ends in /v1 for this vendor, which
		// is what OPENAI_BASE_URL means.
		path = "/chat/completions"
	case modelproxy.Gemini.Name:
		path, auth = "/v1beta/models/gemini-live-test:generateContent?key=$TOKEN", ""
	}
	return fmt.Sprintf(`set -eu
export BASE="$(cat %s)"
export TOKEN="$(cat %s)"
echo "base=$BASE"
curl -sS -m 30 -o /tmp/model-proxy-answer -w 'status=%%{http_code}\n' \
  %s \
  -H 'content-type: application/json' \
  -H 'anthropic-version: 2023-06-01' \
  -d '{"model":"a-model-this-test-never-runs","max_tokens":16,"messages":[{"role":"user","content":"ping"}]}' \
  "$BASE%s"
cat /tmp/model-proxy-answer`,
		modelproxycap.URLPath(vendor), modelproxycap.TokenPath(vendor), auth, path)
}

// TestTheModelProxiesAreReachableFromInsideARealGuest boots a real granule
// VM and, for each vendor grain proxies to, materializes that vendor's
// capability into it exactly as a dispatch does and makes the pair of
// calls that are the whole point of a per-run token: one that works from
// where the token was placed, and one after the lease is revoked that
// does not.
//
// It skips under exactly the same conditions the rest of this file's
// real-VM suites do and shares their build helpers, so it costs a host
// without docker/KVM nothing.
func TestTheModelProxiesAreReachableFromInsideARealGuest(t *testing.T) {
	granuleDockerRealTestPrereqs(t)

	granuleDir := buildGranule(t)
	image := buildGranuleGuestImage(t)

	t.Setenv("PATH", granuleDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx := context.Background()

	// The answer the guest has to come back holding, and the credential
	// each proxy forwards with. The Claude one is spelled like a Claude
	// Code OAuth token ("sk-ant-oat", credcheck's own prefix) so that the
	// bearer-plus-beta half of AuthorizeAnthropic is what the stand-in
	// sees -- the shape a real deployment's secret has.
	const answer = "grain-model-proxy-live-answer"
	credentialFor := func(vendor modelproxy.Vendor) string {
		if vendor.Name == modelproxy.Claude.Name {
			return "sk-ant-oat-live-test-deployment-credential"
		}
		return "live-test-deployment-" + vendor.Name + "-credential"
	}
	upstream := newLiveUpstream(t, answer)

	// --- a real VM, created the way the docker-exec suite above creates
	// its own; see that test for what each of these arguments is for.
	stateDir := t.TempDir()
	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir: stateDir,
		CreateArgs: []string{
			"-kontur-image", image,
			"-guest-port", "22",
			"-guest-user", "debian",
			"-disk-mode=overlay",
		},
		SSHUser:           "debian",
		Workspace:         "/home/debian",
		ReadyTimeout:      3 * time.Minute,
		ReadyPollInterval: time.Second,
	})

	name, err := k.VMNameFor("cp1-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := granule.Delete(context.Background(), stateDir, name); err != nil {
			t.Logf("cleaning up real granule VM %q: %v", name, err)
		}
	})

	sb, err := k.Acquire(ctx, "cp1-1", orchestrator.Shape{})
	if err != nil {
		t.Fatalf("Acquire against a real granule/docker/cloud-hypervisor VM: %v", err)
	}
	t.Cleanup(func() { _ = sb.Release(context.Background()) })

	tools, err := sb.Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var runCommand *mcp.Tool
	for i := range tools {
		if tools[i].Name == "run_command" {
			runCommand = &tools[i]
		}
	}
	if runCommand == nil {
		t.Fatalf("the sandbox returned no run_command tool (got %d tools)", len(tools))
	}

	// --- the address every proxy below is advertised at, read off docker
	// rather than written down here, for the reason assertGuestHasEgress
	// reads it: this test says "the guest can reach the gateway its own
	// namespace routes through", which is the claim, rather than "the
	// runner happened to use 172.17.0.1". scripts/setup.sh's
	// ensure_granule_git_proxy_host defaults -granule-git-proxy-host to the
	// same address by the same question.
	gateway := dockerInspect(t, "granule-vm-"+name,
		`{{range .NetworkSettings.Networks}}{{.Gateway}}{{end}}`)
	if gateway == "" {
		t.Fatalf("docker reports no gateway for this VM's network namespace, so there is no address to advertise a proxy at")
	}

	placer, ok := sb.(orchestrator.SandboxPlacer)
	if !ok {
		t.Fatalf("a granule sandbox does not implement orchestrator.SandboxPlacer, so nothing can write a capability's files into this guest")
	}

	for _, vendor := range modelproxy.Vendors() {
		t.Run(vendor.Name, func(t *testing.T) {
			deploymentCredential := credentialFor(vendor)
			calls := len(upstream.seen())

			// --- the proxy, bound and advertised the way cmd/grain's
			// startModelProxies does it for a deployment whose sandboxes
			// are granule VMs: every interface, advertised at
			// -granule-git-proxy-host.
			// One team connection of this vendor, holding the credential
			// the upstream expects -- what every request on the run's
			// token is forwarded with.
			endpoint := modelproxy.NewEndpoint()
			endpoint.SetConnections(func(context.Context, string) (modelproxy.Connection, error) {
				return modelproxy.Connection{ID: "live-" + vendor.Name}, nil
			})
			endpoint.SetCredential(func(context.Context, modelproxy.Connection) (string, error) { return deploymentCredential, nil })
			ln, err := net.Listen("tcp", "0.0.0.0:0")
			if err != nil {
				t.Fatalf("binding the %s proxy on every interface: %v", vendor.Name, err)
			}
			srv := &http.Server{Handler: &modelproxy.Proxy{
				Vendor:   vendor,
				Endpoint: endpoint,
				Upstream: upstream.server.URL,
			}}
			go func() { _ = srv.Serve(ln) }()
			t.Cleanup(func() {
				shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(shutdown)
			})
			endpoint.SetURL(fmt.Sprintf("http://%s:%d", gateway, ln.Addr().(*net.TCPAddr).Port))

			// --- the capability, resolved and materialized for a run
			// whose sandbox is that VM, and placed over the sandbox's own
			// transport -- which is what orchestrator.applyPlacements
			// does for a real dispatch (SandboxPlacer), restated here
			// because this test drives the sandbox directly rather than
			// through a cycle.
			provider := modelproxycap.New(vendor, endpoint)
			capability := modelproxycap.CapabilityName(vendor)
			cc := model.CapabilityContext{
				Task: model.Task{ID: "model-proxy-live"},
				Run:  model.Run{ID: "model-proxy-live-1", TaskID: "model-proxy-live", Sandbox: "cp1-1"},
				Now:  time.Now(),
			}
			resolution, err := provider.Resolve(ctx, cc)
			if err != nil {
				t.Fatalf("resolving the %s capability: %v", capability, err)
			}
			if resolution.Refused {
				t.Fatalf("the %s capability refused a deployment with a running proxy and a credential: %s",
					capability, resolution.Reason)
			}
			materialization, err := provider.Materialize(ctx, cc)
			if err != nil {
				t.Fatalf("materializing the %s capability: %v", capability, err)
			}
			if materialization.Lease == nil {
				t.Fatalf("the %s capability materialized no lease, so there is nothing for the run's end to revoke", capability)
			}

			var placedToken string
			for _, p := range materialization.Placements {
				if p.Side != model.SideSandbox {
					continue
				}
				if p.Path == modelproxycap.TokenPath(vendor) {
					placedToken = strings.TrimSpace(p.Content)
				}
				if err := placer.PlaceFile(ctx, p.Path, p.Content, p.EffectiveMode()); err != nil {
					t.Fatalf("placing %s in the guest: %v", p.Path, err)
				}
			}
			if placedToken == "" {
				t.Fatalf("the %s capability placed no token at %s", capability, modelproxycap.TokenPath(vendor))
			}

			// --- the claim: a process in the guest, reading the two
			// placed files and calling the advertised address, is
			// answered.
			call := guestCall(vendor)
			result := runCommand.Handler(ctx, map[string]any{"command": call})
			if result.IsError {
				// A connection refused or timed out here is the failure
				// this test exists for, and it is one of the guest's
				// network rather than of the proxy -- so print what the
				// guest's own stack looks like, the way the egress
				// assertions in this package do.
				dumpGuestNetwork(t, name)
				t.Fatalf("calling grain's %s proxy from inside the guest failed: %s\nthe proxy is listening on every interface and advertised at %s, which is the gateway this guest routes through",
					vendor.Name, result.Text, endpoint.URL())
			}
			for _, want := range []string{"status=200", answer} {
				if !strings.Contains(result.Text, want) {
					t.Errorf("the guest's call to the %s proxy returned %q, want it to contain %q",
						vendor.Name, result.Text, want)
				}
			}

			// The credential that crossed to the stand-in is the
			// deployment's, presented where that vendor takes one -- and
			// the run's own token, which is worth nothing upstream, went
			// no further than the proxy.
			seen := upstream.seen()
			if len(seen) != calls+1 {
				t.Fatalf("the stand-in upstream saw %d requests, want exactly the one the guest made", len(seen)-calls)
			}
			forwarded := seen[len(seen)-1]
			presented := forwarded.header.Get("Authorization") + " " + forwarded.header.Get("x-api-key") +
				" " + forwarded.query.Encode()
			if !strings.Contains(presented, deploymentCredential) {
				t.Errorf("the upstream was called with %q, want this deployment's own credential in it", presented)
			}
			if strings.Contains(presented, placedToken) {
				t.Errorf("the run's own proxy token reached the upstream in %q -- it is worth nothing there and must never leave this host", presented)
			}

			// --- and the other half of a per-run token: revoking the
			// lease, which is what the end of a run does, stops the same
			// call working from the same sandbox with the same file still
			// sitting in it.
			if err := provider.Revoke(ctx, cc, *materialization.Lease); err != nil {
				t.Fatalf("revoking the %s lease: %v", capability, err)
			}
			after := runCommand.Handler(ctx, map[string]any{"command": call})
			if after.IsError {
				t.Fatalf("the guest's second call failed before the proxy could refuse it: %s", after.Text)
			}
			if !strings.Contains(after.Text, "status=401") {
				t.Errorf("after the lease was revoked the guest's call returned %q, want a 401 -- a token that outlives its run authorizes nothing", after.Text)
			}
			if !strings.Contains(after.Text, "does not recognise that token") {
				t.Errorf("the refusal the guest read was %q, want the proxy's own sentence naming a token it no longer knows", after.Text)
			}
			if seen := upstream.seen(); len(seen) != calls+1 {
				t.Errorf("the stand-in upstream saw %d requests in total, want the revoked call answered at the proxy without reaching the vendor at all",
					len(seen)-calls)
			}
		})
	}
}
