// Package modelproxy is the "<vendor>-proxy" capabilities -- claude-proxy,
// openai-proxy and gemini-proxy -- one MINT model.CapabilityProvider per
// vendor, each minting a per-run token for this deployment's own proxy
// to that vendor (pkg/modelproxy) and placing it, with the address to
// use it against, in the run's sandbox.
//
// What one hands a run is the *use* of a model credential, never the
// credential: the token it places is worth nothing except against this
// deployment's proxy, and only until the run's lease is revoked as its
// slot frees. The credential every forwarded request is actually
// authenticated with stays on the controller: the key of the run's own
// team connection for that vendor -- the very connection the run's own
// agent spends when it is that vendor's framework
// (model.CapabilityContext.Connection), and the team's connection of
// that vendor otherwise -- which is the whole reason these capabilities
// exist rather than a placement of those keys. A team with no connection
// for the vendor is refused the capability; no deployment-wide secret
// stands in (pkg/modelproxy's "Whose credential").
//
// They are MINT providers by the same reading gemini-key is: something
// is created per run that has to be destroyed when the run ends, and a
// Lease is what records that obligation. The resource one names is the
// token's *id* rather than the token, since a lease is a row in grain's
// store and therefore a file in the state repository it exports itself
// to -- see pkg/modelproxy's own note on why a token is two halves.
//
// # Three capabilities rather than one with an argument
//
// A run that needs to call Claude is granted claude-proxy, and gets a
// token for the Claude proxy and nothing else. The alternative -- one
// "model-proxy" grant covering whichever vendors this deployment
// happens to have credentials for -- would make the picker vaguer (a
// task would be granted more than it asked for) and refusals vaguer
// still: readiness is per vendor connection, and a team holding an
// Anthropic connection and no OpenAI one has one capability it can use
// and one it is refused, which is two answers. Three grants cost one provider registration each
// and no duplicated code, since the provider itself is written once and
// built three times.
package modelproxy

import (
	"context"
	"errors"
	"fmt"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/modelproxy"
)

// CapabilityName is the grant id for one vendor's proxy -- the string
// ui.OfferedCapabilities offers, model.Config.DefaultCapabilities may
// name, and every Lease this package mints carries as Lease.Capability.
//
// Built from the vendor's own name rather than written down three times,
// so "claude-proxy", "openai-proxy" and "gemini-proxy" cannot drift from
// the vendors they belong to. claude-proxy is the one that predates the
// other two (grain/task-25), which is why the shape is <vendor>-proxy
// and not proxy-<vendor>: a grant an operator may already have in a
// deployment's default set keeps its name.
func CapabilityName(vendor modelproxy.Vendor) string { return vendor.Name + "-proxy" }

// URLPath and TokenPath are where Materialize places the two things a
// run needs. Derived from the vendor's name rather than configurable,
// for the same reason geminikey.KeyPath and githubsandbox.
// SandboxTokenPath are constants: PromptSection's own text names them
// verbatim.
func URLPath(vendor modelproxy.Vendor) string {
	return "/home/debian/." + vendor.Name + "-proxy-url"
}

// TokenPath is where the run's own token is placed -- see URLPath.
func TokenPath(vendor modelproxy.Vendor) string {
	return "/home/debian/." + vendor.Name + "-proxy-token"
}

// maxLease is model.CapabilitySpec.MaxLease and the backstop the
// registry itself enforces (modelproxy.DefaultLease): a token nobody
// revoked stops working a day later regardless. A run that states its
// own deadline (model.CapabilityContext.Deadline) is held to that
// instead, which is every run grain dispatches.
const maxLease = modelproxy.DefaultLease

// Provider is one vendor's proxy capability, minting into that vendor's
// running proxy.
type Provider struct {
	// vendor is which API this capability is a grant to call, and
	// everything that differs between one and another: the framework
	// whose connections it forwards with, the environment variables the
	// prompt names, the words the refusals are written with.
	vendor modelproxy.Vendor
	// endpoint is the running proxy for that vendor: its address, its
	// token registry and how a team connection and its credential are
	// resolved. It
	// may be nil -- pkg/ui's own catalog builds every provider grain
	// ships with zero-value configuration, purely to read Spec() back
	// out -- and every method here answers for a nil one the way it
	// answers for a deployment that never started a proxy: refused, with
	// a sentence.
	endpoint *modelproxy.Endpoint
}

// Config holds configuration for modelproxy Provider.
type Config struct{}

// New returns a provider for vendor, minting against endpoint.
func New(vendor modelproxy.Vendor, endpoint *modelproxy.Endpoint) *Provider {
	return &Provider{vendor: vendor, endpoint: endpoint}
}

// NewWithConfig returns a provider for vendor, minting against endpoint with configuration.
func NewWithConfig(vendor modelproxy.Vendor, endpoint *modelproxy.Endpoint, _ Config) *Provider {
	return &Provider{vendor: vendor, endpoint: endpoint}
}

// Providers is one provider per vendor grain ships a proxy for, built
// against the endpoint each vendor's proxy is served on. endpoints is
// keyed by modelproxy.Vendor.Name; a vendor missing from it gets a nil
// endpoint, which is a capability that refuses cleanly rather than one
// that is not offered at all -- the same answer a deployment whose proxy
// failed to bind gives.
func Providers(endpoints map[string]*modelproxy.Endpoint) []*Provider {
	return ProvidersWithConfig(endpoints, Config{})
}

// ProvidersWithConfig is one provider per vendor grain ships a proxy for, built
// with cfg.
func ProvidersWithConfig(endpoints map[string]*modelproxy.Endpoint, cfg Config) []*Provider {
	vendors := modelproxy.Vendors()
	out := make([]*Provider, 0, len(vendors))
	for _, vendor := range vendors {
		out = append(out, NewWithConfig(vendor, endpoints[vendor.Name], cfg))
	}
	return out
}

func (p *Provider) Spec() model.CapabilitySpec {
	name := CapabilityName(p.vendor)
	return model.CapabilitySpec{
		Name:  name,
		Label: "grain-" + name,
		Description: "Let this task call the " + p.vendor.Display + " API through grain's own proxy -- a token " +
			"in its sandbox, and its team's " + p.vendor.Company + " connection's credential never leaving " +
			"the controller",
		Source:    model.GrantByLabel,
		Provision: model.ProvisionMint,
		MaxLease:  maxLease,
		// No Requires: what a request is forwarded with is the key of the
		// run's own team connection, not a deployment secret, and whether
		// a team has one is Resolve's to answer per run.
	}
}

// Resolve refuses where there is nothing to hand the run: no proxy
// running on this deployment for this vendor, no connection of this
// vendor in the run's team, or one with no credential the proxy can
// forward with.
//
// The connection and its credential are asked of the proxy's own
// endpoint, because the endpoint is what will actually resolve them on
// every request.
func (p *Provider) Resolve(ctx context.Context, cc model.CapabilityContext) (model.Resolution, error) {
	if p.endpoint == nil || p.endpoint.URL() == "" {
		return model.RefusedBecause(
			"this task asks to call the " + p.vendor.Display + " API through grain's own proxy, but " +
				"this deployment is not running one. It starts with the daemon, so a deployment where this " +
				"is missing is one whose daemon failed to bind it -- an operator finds the reason in " +
				"grain's own log.",
		), nil
	}
	connection, err := p.endpoint.ConnectionFor(ctx, cc.Connection)
	if err != nil {
		return model.Resolution{}, fmt.Errorf("modelproxy: choosing the %s connection: %w", p.vendor.Display, err)
	}
	if connection.ID == "" {
		return model.RefusedBecause(
			"this task asks to call the " + p.vendor.Display + " API through grain's own proxy, but this " +
				"deployment has no " + p.vendor.Display + " connection for the proxy to forward with. An admin " +
				"adds one in Settings -> Models.",
		), nil
	}
	// A connection on Vertex AI holds no key, and the proxy forwards on
	// it by translating instead (modelproxy.Proxy.serveVertex), so it is
	// asked about first, in the order the proxy itself asks.
	vertex, err := p.endpoint.ResolveVertex(ctx, connection)
	if err != nil {
		reason := "connection " + connection.ID + " authenticates through Vertex AI, " +
			"and the proxy could not mint a Google Cloud access token for it. An admin checks its service " +
			"account key in Settings -> Models."
		var refusal *modelproxy.CredentialRefusal
		if errors.As(err, &refusal) {
			reason = refusal.Reason
		}
		return p.refused(reason), nil
	}
	if vertex != nil {
		return model.Honoured(), nil
	}
	if credential, err := p.endpoint.ResolveCredential(ctx, connection); err != nil || credential == "" {
		reason := "connection " + connection.ID + " holds no " + p.vendor.Company +
			" credential for the proxy to forward with. An admin sets one on it in Settings -> Models."
		var refusal *modelproxy.CredentialRefusal
		if errors.As(err, &refusal) {
			reason = refusal.Reason
		}
		return p.refused(reason), nil
	}
	return model.Honoured(), nil
}

// refused is Resolve's refusal for a connection the proxy cannot forward
// on, for reason.
func (p *Provider) refused(reason string) model.Resolution {
	return model.RefusedBecause("this task asks to call the " + p.vendor.Display + " API through grain's own proxy, but " + reason)
}

// Materialize mints this run's token and places it, with the proxy's
// address, in the sandbox.
//
// The Lease's Resource is the token's id, which is what Revoke below
// needs and all it needs: the token itself is in the sandbox and in the
// proxy's own memory, and nowhere else.
func (p *Provider) Materialize(ctx context.Context, cc model.CapabilityContext) (model.Materialization, error) {
	if p.endpoint == nil {
		return model.Materialization{}, modelproxy.ErrNoProxy
	}
	url := p.endpoint.URL()
	if url == "" {
		return model.Materialization{}, modelproxy.ErrNoProxy
	}
	// Bounded by the run's own deadline where it has one, so the token
	// and the Lease recording it end together, and a token whose Revoke
	// never came is worthless once its run cannot be alive -- not for
	// the rest of a day (bwsalmon/grain#2418). A zero Deadline leaves
	// the registry's own lease in charge.
	//
	// The token records the run's connection (Endpoint.ConnectionFor),
	// so every request on it spends that connection's key and nobody
	// else's for as long as the run lasts.
	connection, err := p.endpoint.ConnectionFor(ctx, cc.Connection)
	if err != nil {
		return model.Materialization{}, fmt.Errorf("modelproxy: choosing the %s connection for run %s: %w",
			p.vendor.Display, cc.Run.ID, err)
	}
	id, token, err := p.endpoint.MintUntil(cc.Run.ID, connection, cc.Deadline)
	if err != nil {
		return model.Materialization{}, fmt.Errorf("modelproxy: minting a %s token for run %s: %w",
			p.vendor.Name, cc.Run.ID, err)
	}
	expires := cc.Now.Add(maxLease)
	if !cc.Deadline.IsZero() && cc.Deadline.Before(expires) {
		expires = cc.Deadline
	}
	return model.Materialization{
		Lease: &model.Lease{
			Capability: CapabilityName(p.vendor),
			Resource:   id,
			IssuedAt:   cc.Now,
			ExpiresAt:  &expires,
		},
		Placements: []model.Placement{
			// The address is written as this vendor's own clients expect
			// to be given one -- OPENAI_BASE_URL carries the /v1 an
			// ANTHROPIC_BASE_URL does not (modelproxy.Vendor.
			// BaseURLSuffix) -- so that the file can be pasted straight
			// into the variable rather than adjusted per vendor by
			// whoever is reading the prompt.
			{Side: model.SideSandbox, Path: URLPath(p.vendor), Content: url + p.vendor.BaseURLSuffix + "\n"},
			{Side: model.SideSandbox, Path: TokenPath(p.vendor), Content: token + "\n"},
		},
	}, nil
}

// PromptSection tells the run what it has and how to use it -- two
// exports, which is the whole of the setup most of these vendors'
// clients need, and the vendor's Setup recipe for any that need more
// (the codex CLI).
//
// It names the paths rather than the material, which is the contract
// model.CapabilityProvider draws: this is handed the placements without
// their Content precisely so a token cannot end up in a prompt.
func (p *Provider) PromptSection(ctx context.Context, cc model.CapabilityContext, placements []model.Placement) (string, error) {
	var setup string
	if p.vendor.Setup != nil {
		setup = "\n" + p.vendor.Setup(URLPath(p.vendor), TokenPath(p.vendor))
	}
	return fmt.Sprintf(
		"You can call the %s API from inside this sandbox, through grain's own proxy -- it holds your "+
			"team's %s connection's credential, so you never see one:\n\n"+
			"    export %s=\"$(cat %s)\"\n"+
			"    export %s=\"$(cat %s)\"\n\n"+
			"Past the proxy is %s's own API, limited to its model endpoints: %s, streaming included -- "+
			"its files, batch, fine-tuning and account APIs are refused -- and any client that "+
			"reads those two variables (%s) works unchanged. The token is this run's alone and stops "+
			"working when this run ends, so there is nothing worth writing down or committing.\n%s",
		p.vendor.Display, p.vendor.Company,
		p.vendor.BaseURLEnv, URLPath(p.vendor),
		p.vendor.TokenEnv, TokenPath(p.vendor),
		p.vendor.Company, p.vendor.Endpoints, p.vendor.Clients, setup), nil
}

// Revoke drops the token the Lease names, so a copy of it that outlived
// the sandbox authorizes nothing. Idempotent: an id the registry has
// already forgotten -- revoked twice, or expired -- is not an error.
func (p *Provider) Revoke(ctx context.Context, cc model.CapabilityContext, lease model.Lease) error {
	p.endpoint.Revoke(lease.Resource)
	return nil
}

// There is deliberately no CheckCredential (model.CredentialChecker): it
// would check one deployment-wide credential, and what a request through
// this proxy spends is each team's own connection's key, which Settings'
// connections pane already checks per connection.

// Reap is deliberately not implemented -- see pkg/modelproxy's Tokens,
// which is the only place a minted token exists: one that leaks past its
// lease stops being honoured after modelproxy.DefaultLease, and dies
// with the process in any case. There is no far end holding a resource
// grain could lose track of, which is the situation model.Reaper exists
// for.
