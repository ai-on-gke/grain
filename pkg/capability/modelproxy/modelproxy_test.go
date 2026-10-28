package modelproxy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/modelproxy"
)

// testEndpoint is a proxy whose deployment has one connection of its
// vendor, "deploy-conn", holding credential -- unless the run's own
// agent spends another connection, which is then the one.
func testEndpoint(url, credential string) *modelproxy.Endpoint {
	endpoint := modelproxy.NewEndpoint()
	endpoint.SetURL(url)
	endpoint.SetConnections(func(_ context.Context, own string) (modelproxy.Connection, error) {
		if own != "" {
			return modelproxy.Connection{ID: own}, nil
		}
		return modelproxy.Connection{ID: "deploy-conn"}, nil
	})
	endpoint.SetCredential(func(context.Context, modelproxy.Connection) (string, error) { return credential, nil })
	return endpoint
}

func testContext() model.CapabilityContext {
	return model.CapabilityContext{
		Task: model.Task{},
		Run:  model.Run{ID: "run-7"},
		Now:  time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC),
	}
}

// Every test below runs for all three vendors, because there is one
// provider written once and built three times: a claim that only held
// for Claude would be one this package's whole shape is wrong about.
func eachVendor(t *testing.T, f func(t *testing.T, vendor modelproxy.Vendor)) {
	t.Helper()
	for _, vendor := range modelproxy.Vendors() {
		t.Run(vendor.Name, func(t *testing.T) { f(t, vendor) })
	}
}

func TestSpecNamesTheCapabilityAndRequiresNoDeploymentSecret(t *testing.T) {
	eachVendor(t, func(t *testing.T, vendor modelproxy.Vendor) {
		spec := New(vendor, nil).Spec()
		if spec.Name != CapabilityName(vendor) || spec.Name != vendor.Name+"-proxy" {
			t.Errorf("Name = %q, want %q", spec.Name, vendor.Name+"-proxy")
		}
		if spec.Provision != model.ProvisionMint {
			t.Errorf("Provision = %v, want MINT: a token is minted per run and has to be revoked", spec.Provision)
		}
		if spec.MaxLease == 0 {
			t.Error("MaxLease = 0: a minted token with no backstop is one nothing bounds")
		}
		// A request is forwarded with the run's team connection's key, so
		// the deployment-wide secret is nothing this capability needs.
		if len(spec.Requires) != 0 {
			t.Errorf("Requires = %v, want none", spec.Requires)
		}
	})
}

// Three grants, three secrets, three sets of files in a sandbox: a task
// granted one vendor's proxy gets that vendor's and nothing else.
func TestEachVendorIsItsOwnCapability(t *testing.T) {
	seen := map[string]string{}
	for _, vendor := range modelproxy.Vendors() {
		for _, value := range []string{CapabilityName(vendor), URLPath(vendor), TokenPath(vendor), vendor.Secret} {
			if other, dup := seen[value]; dup {
				t.Errorf("%q is shared by %s and %s: one vendor's grant would answer for another's",
					value, other, vendor.Name)
			}
			seen[value] = vendor.Name
		}
	}
	// And the name the grant that predates the other two has is
	// unchanged, since a deployment may already have it in its default
	// set.
	if got := CapabilityName(modelproxy.Claude); got != "claude-proxy" {
		t.Errorf("the Claude grant is now named %q, which no existing deployment has granted", got)
	}
}

func TestResolveRefusesWithoutAProxyOrACredential(t *testing.T) {
	eachVendor(t, func(t *testing.T, vendor modelproxy.Vendor) {
		for _, tc := range []struct {
			name     string
			provider *Provider
			wants    string
		}{
			{"no proxy at all", New(vendor, nil), "not running one"},
			{"a proxy that never published an address", New(vendor, testEndpoint("", "a-real-credential")), "not running one"},
			{"a connection with no credential to forward", New(vendor, testEndpoint("http://gateway:1234", "")), "connection deploy-conn holds no"},
			{"a deployment with no connection of this vendor", New(vendor, noConnectionEndpoint()), "this deployment has no " + vendor.Display + " connection"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				resolution, err := tc.provider.Resolve(t.Context(), testContext())
				if err != nil {
					t.Fatalf("Resolve: %v", err)
				}
				if !resolution.Refused {
					t.Fatal("Resolve honoured a capability there is nothing behind")
				}
				if !strings.Contains(resolution.Reason, tc.wants) {
					t.Errorf("reason = %q, want it to mention %q", resolution.Reason, tc.wants)
				}
				if strings.Contains(resolution.Reason, "Settings") &&
					!strings.Contains(resolution.Reason, "Settings -> Models") {
					t.Errorf("reason = %q, want its remedy to name Settings -> Models", resolution.Reason)
				}
			})
		}
	})
}

func TestResolveHonoursARunningProxy(t *testing.T) {
	eachVendor(t, func(t *testing.T, vendor modelproxy.Vendor) {
		resolution, err := New(vendor, testEndpoint("http://gateway:1234", "a-real-credential")).
			Resolve(t.Context(), testContext())
		if err != nil {
			t.Fatal(err)
		}
		if resolution.Refused {
			t.Fatalf("refused a deployment with a proxy and a credential: %s", resolution.Reason)
		}
	})
}

// vertexEndpoint is a proxy whose every team's connection authenticates
// through Vertex AI: its key resolver refuses, as the daemon's does for
// such a connection (modelProxyCredential), and its Vertex resolver
// answers target and err.
func vertexEndpoint(target *modelproxy.VertexTarget, err error) *modelproxy.Endpoint {
	endpoint := testEndpoint("http://gateway:1234", "")
	endpoint.SetCredential(func(_ context.Context, connection modelproxy.Connection) (string, error) {
		return "", &modelproxy.CredentialRefusal{Reason: "connection " + connection.ID + " authenticates through Vertex AI, which gives this grain proxy no key"}
	})
	endpoint.SetVertex(func(context.Context, modelproxy.Connection) (*modelproxy.VertexTarget, error) {
		return target, err
	})
	return endpoint
}

// A team whose Claude connection is on Vertex AI holds no key, and the
// proxy forwards on it by translating (modelproxy.Proxy.serveVertex): the
// capability is honoured for it rather than refused for having no key,
// which left the Vertex forwarding unreachable from any dispatched run.
func TestResolveHonoursAConnectionOnVertexAI(t *testing.T) {
	provider := New(modelproxy.Claude, vertexEndpoint(&modelproxy.VertexTarget{Project: "p", Region: "us-east5", Token: "ya29.x"}, nil))
	resolution, err := provider.Resolve(t.Context(), testContext())
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Refused {
		t.Fatalf("refused a Vertex AI connection the proxy forwards on: %s", resolution.Reason)
	}
}

func TestResolveRefusesAVertexAIConnectionItCannotMintFor(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		wants string
	}{
		{"a refusal of its own", &modelproxy.CredentialRefusal{Reason: "connection alpha-conn of team alpha authenticates through Vertex AI, and holds no service account key"}, "holds no service account key"},
		{"any other failure", errors.New("oauth2: cannot fetch token"), "could not mint a Google Cloud access token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolution, err := New(modelproxy.Claude, vertexEndpoint(nil, tc.err)).Resolve(t.Context(), testContext())
			if err != nil {
				t.Fatal(err)
			}
			if !resolution.Refused || !strings.Contains(resolution.Reason, tc.wants) {
				t.Fatalf("resolution = %+v, want a refusal mentioning %q", resolution, tc.wants)
			}
			if strings.Contains(resolution.Reason, "cannot fetch token") {
				t.Errorf("reason %q repeats the token source's own error", resolution.Reason)
			}
		})
	}
}

func TestMaterializePlacesATokenAndTheAddressToUseItAgainst(t *testing.T) {
	eachVendor(t, func(t *testing.T, vendor modelproxy.Vendor) {
		endpoint := testEndpoint("http://172.17.0.1:41234", "a-real-credential")
		provider := New(vendor, endpoint)
		cc := testContext()
		materialization, err := provider.Materialize(t.Context(), cc)
		if err != nil {
			t.Fatalf("Materialize: %v", err)
		}

		placements := map[string]string{}
		for _, p := range materialization.Placements {
			if p.Side != model.SideSandbox {
				t.Errorf("placement %s lands on %s, want the sandbox", p.Path, p.Side)
			}
			if p.EffectiveMode() != "600" {
				t.Errorf("placement %s has mode %s, want 600", p.Path, p.EffectiveMode())
			}
			placements[p.Path] = strings.TrimSpace(p.Content)
		}
		// The address is written as this vendor's own base-URL variable
		// wants it: bare for Anthropic and Google, with the /v1
		// OPENAI_BASE_URL carries for OpenAI.
		want := "http://172.17.0.1:41234" + vendor.BaseURLSuffix
		if got := placements[URLPath(vendor)]; got != want {
			t.Errorf("%s holds %q, want %q", URLPath(vendor), got, want)
		}
		token := placements[TokenPath(vendor)]
		if token == "" {
			t.Fatalf("%s holds nothing", TokenPath(vendor))
		}
		if owner, ok := endpoint.Tokens.Authenticate(token); !ok || owner != "run-7" {
			t.Fatalf("the placed token authenticates as %q, %v, want run-7", owner, ok)
		}

		lease := materialization.Lease
		if lease == nil {
			t.Fatal("no Lease: nothing would ever revoke the token")
		}
		if lease.Capability != CapabilityName(vendor) {
			t.Errorf("Lease.Capability = %q", lease.Capability)
		}
		// The lease is a row grain exports to its state repository, so the
		// one thing it must not carry is the token itself.
		if lease.Resource == "" || strings.Contains(token, lease.Resource+".") == false {
			t.Errorf("Lease.Resource = %q, want the token's id", lease.Resource)
		}
		if lease.Resource == token || strings.Contains(lease.Resource, ".") {
			t.Errorf("Lease.Resource = %q -- the token itself would be published to the state repository", lease.Resource)
		}
		if lease.ExpiresAt == nil || !lease.ExpiresAt.After(cc.Now) {
			t.Errorf("Lease.ExpiresAt = %v, want a bound past now", lease.ExpiresAt)
		}

		// And revoking that lease is what makes the token in the sandbox
		// worthless once the run is over.
		if err := provider.Revoke(t.Context(), cc, *lease); err != nil {
			t.Fatalf("Revoke: %v", err)
		}
		if _, ok := endpoint.Tokens.Authenticate(token); ok {
			t.Fatal("the token still authenticates after its lease was revoked")
		}
		// Twice, and for a lease naming nothing, is what a retried cleanup
		// does.
		if err := provider.Revoke(t.Context(), cc, *lease); err != nil {
			t.Fatalf("revoking twice: %v", err)
		}
	})
}

// A run that states its deadline gets a token, and a Lease, that end
// there rather than a flat modelproxy.DefaultLease later
// (bwsalmon/grain#2418).
func TestMaterializeBoundsTheTokenByTheRunsDeadline(t *testing.T) {
	eachVendor(t, func(t *testing.T, vendor modelproxy.Vendor) {
		endpoint := testEndpoint("http://172.17.0.1:41234", "a-real-credential")
		cc := testContext()
		cc.Deadline = cc.Now.Add(3 * time.Hour)
		now := cc.Now
		endpoint.Tokens.Now = func() time.Time { return now }
		materialization, err := New(vendor, endpoint).Materialize(t.Context(), cc)
		if err != nil {
			t.Fatalf("Materialize: %v", err)
		}
		if lease := materialization.Lease; lease == nil || lease.ExpiresAt == nil || !lease.ExpiresAt.Equal(cc.Deadline) {
			t.Errorf("Lease = %+v, want it to expire at the run's deadline %v", lease, cc.Deadline)
		}
		var token string
		for _, p := range materialization.Placements {
			if p.Path == TokenPath(vendor) {
				token = strings.TrimSpace(p.Content)
			}
		}
		now = cc.Deadline
		if _, ok := endpoint.Tokens.Authenticate(token); !ok {
			t.Fatal("the token was refused while its run could still be alive")
		}
		now = cc.Deadline.Add(time.Second)
		if _, ok := endpoint.Tokens.Authenticate(token); ok {
			t.Fatal("the token outlives its run's deadline")
		}
	})
}

func TestMaterializeRefusesWithNoProxy(t *testing.T) {
	eachVendor(t, func(t *testing.T, vendor modelproxy.Vendor) {
		if _, err := New(vendor, nil).Materialize(t.Context(), testContext()); !errors.Is(err, modelproxy.ErrNoProxy) {
			t.Fatalf("Materialize with no proxy = %v, want ErrNoProxy", err)
		}
		if _, err := New(vendor, testEndpoint("", "a-real-credential")).Materialize(t.Context(), testContext()); !errors.Is(err, modelproxy.ErrNoProxy) {
			t.Fatalf("Materialize with no address = %v, want ErrNoProxy", err)
		}
	})
}

// The prompt is a promise to the agent, and the one thing it must never
// carry is the material -- model.CapabilityProvider hands PromptSection
// the placements without their Content for exactly this reason.
func TestPromptSectionNamesThePathsAndNoMaterial(t *testing.T) {
	eachVendor(t, func(t *testing.T, vendor modelproxy.Vendor) {
		endpoint := testEndpoint("http://172.17.0.1:41234", "a-real-credential")
		provider := New(vendor, endpoint)
		materialization, err := provider.Materialize(t.Context(), testContext())
		if err != nil {
			t.Fatal(err)
		}
		var token string
		for _, p := range materialization.Placements {
			if p.Path == TokenPath(vendor) {
				token = strings.TrimSpace(p.Content)
			}
		}
		section, err := provider.PromptSection(t.Context(), testContext(), stripContent(materialization.Placements))
		if err != nil {
			t.Fatal(err)
		}
		// The two paths, and the two variables this vendor's own clients
		// read -- which are the whole of the setup the prompt promises.
		for _, want := range []string{URLPath(vendor), TokenPath(vendor), vendor.BaseURLEnv, vendor.TokenEnv} {
			if !strings.Contains(section, want) {
				t.Errorf("prompt section does not mention %q:\n%s", want, section)
			}
		}
		if strings.Contains(section, token) || strings.Contains(section, "a-real-credential") {
			t.Error("the prompt section carries material")
		}
	})
}

// The OpenAI prompt once listed the codex CLI among the clients that work
// unchanged from OPENAI_BASE_URL and OPENAI_API_KEY, and codex 0.153.0
// reads neither: it went straight to api.openai.com, and with a base URL
// configured still sent no credential until logged in (docs/test-runs/
// 2026-10-01-credential-proxy-hardening.md, O1). So the prompt must not
// make that promise, and must give the recipe that was measured to work.
func TestOpenAIPromptGivesTheCodexRecipe(t *testing.T) {
	provider := New(modelproxy.OpenAI, testEndpoint("http://172.17.0.1:41234", "a-real-credential"))
	materialization, err := provider.Materialize(t.Context(), testContext())
	if err != nil {
		t.Fatal(err)
	}
	section, err := provider.PromptSection(t.Context(), testContext(), stripContent(materialization.Placements))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(modelproxy.OpenAI.Clients, "codex") {
		t.Errorf("OpenAI.Clients = %q, which promises codex works from the two variables alone", modelproxy.OpenAI.Clients)
	}
	for _, want := range []string{
		"its built-in OpenAI provider ignores OPENAI_BASE_URL and sends no credential until it has been logged in",
		"    export CODEX_HOME=/home/debian/.codex-openai-proxy\n",
		"    mkdir -p \"$CODEX_HOME\"\n",
		`    printf 'openai_base_url = "%s"\n' "$(cat /home/debian/.openai-proxy-url)" > "$CODEX_HOME/config.toml"` + "\n",
		"    codex login --with-api-key < /home/debian/.openai-proxy-token\n",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("OpenAI prompt section does not carry %q:\n%s", want, section)
		}
	}
}

// Only OpenAI has a client needing more than the two variables; the
// others' prompts carry no recipe, and in particular no codex one.
func TestOnlyOpenAIsPromptHasASetupRecipe(t *testing.T) {
	eachVendor(t, func(t *testing.T, vendor modelproxy.Vendor) {
		if vendor.Name == modelproxy.OpenAI.Name {
			return
		}
		section, err := New(vendor, nil).PromptSection(t.Context(), testContext(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(section, "codex") || strings.Contains(section, "CODEX_HOME") {
			t.Errorf("%s prompt section mentions codex:\n%s", vendor.Name, section)
		}
	})
}

func stripContent(placements []model.Placement) []model.Placement {
	out := make([]model.Placement, 0, len(placements))
	for _, p := range placements {
		p.Content = ""
		out = append(out, p)
	}
	return out
}

// noConnectionEndpoint is a running proxy for a team with no connection
// of its vendor: its credential resolver answers for anybody asked, and
// must never be.
func noConnectionEndpoint() *modelproxy.Endpoint {
	endpoint := modelproxy.NewEndpoint()
	endpoint.SetURL("http://gateway:1234")
	endpoint.SetCredential(func(context.Context, modelproxy.Connection) (string, error) {
		return "a-deployment-wide-credential", nil
	})
	return endpoint
}

// The token records the run's connection: the one its own agent spends
// where there is one -- a borrowing connection's source, as RunDispatch
// names it -- and the deployment's connection of the vendor otherwise.
func TestMaterializeRecordsTheRunsConnection(t *testing.T) {
	eachVendor(t, func(t *testing.T, vendor modelproxy.Vendor) {
		for _, tc := range []struct {
			name, own string
			want      modelproxy.Connection
		}{
			{"the run's own connection", "source-conn", modelproxy.Connection{ID: "source-conn"}},
			{"the deployment's connection of this vendor", "", modelproxy.Connection{ID: "deploy-conn"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				endpoint := testEndpoint("http://gateway:1234", "a-real-credential")
				cc := testContext()
				cc.Connection = tc.own
				materialization, err := New(vendor, endpoint).Materialize(t.Context(), cc)
				if err != nil {
					t.Fatal(err)
				}
				for _, p := range materialization.Placements {
					if p.Path != TokenPath(vendor) {
						continue
					}
					_, connection, ok := endpoint.Tokens.Lookup(strings.TrimSpace(p.Content))
					if !ok || connection != tc.want {
						t.Errorf("the token forwards with %+v (%v), want %+v", connection, ok, tc.want)
					}
				}
			})
		}
	})
}

// Providers is what cmd/grain and pkg/ui both build their providers
// with: one per vendor, each holding the endpoint its own proxy serves
// and nobody else's.
func TestProvidersPairEachVendorWithItsOwnEndpoint(t *testing.T) {
	endpoints := map[string]*modelproxy.Endpoint{
		modelproxy.Claude.Name: testEndpoint("http://gateway:1", "a-real-credential"),
		modelproxy.OpenAI.Name: testEndpoint("http://gateway:2", "a-real-credential"),
	}
	providers := Providers(endpoints)
	if len(providers) != len(modelproxy.Vendors()) {
		t.Fatalf("Providers built %d providers, want one per vendor", len(providers))
	}
	for _, p := range providers {
		materialization, err := p.Materialize(t.Context(), testContext())
		if p.vendor.Name == modelproxy.Gemini.Name {
			// Missing from the map: a capability that refuses cleanly,
			// which is what a deployment whose proxy failed to bind
			// gives.
			if !errors.Is(err, modelproxy.ErrNoProxy) {
				t.Errorf("the vendor with no endpoint materialized %v, want ErrNoProxy", err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("Materialize for %s: %v", p.vendor.Name, err)
		}
		for _, placement := range materialization.Placements {
			if placement.Path != TokenPath(p.vendor) {
				continue
			}
			token := strings.TrimSpace(placement.Content)
			if _, ok := endpoints[p.vendor.Name].Tokens.Authenticate(token); !ok {
				t.Errorf("%s's token does not authenticate against %s's own proxy", p.vendor.Name, p.vendor.Name)
			}
			for name, endpoint := range endpoints {
				if name == p.vendor.Name {
					continue
				}
				if _, ok := endpoint.Tokens.Authenticate(token); ok {
					t.Errorf("%s's token also authenticates against the %s proxy", p.vendor.Name, name)
				}
			}
		}
	}
}
