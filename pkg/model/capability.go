package model

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/bwsalmon/grain/pkg/secrets"
)

// This file is docs/data-model.md's "Capabilities are an extension
// point, not a table" section, decided in code: a capability travels
// through four moments -- requested (a Grant already exists by the time
// any of this runs), resolved, materialized, revoked -- and a provider
// is exactly those four methods and nothing else.
//
// Unlike the Python contract that document describes, a provider here
// is handed no Runner. v2 has no host adapter yet (see README.md),
// so there is nothing for one to run commands against -- and Placement
// was already the declarative half of that design, the half a
// containerised provider is restricted to because it has no other
// route to move material. Starting v2's contract there means "what
// crosses" is structural from the first provider, rather than a
// convention an in-process Runner would let slip.

// Side is where a Placement lands.
type Side string

const (
	SideSandbox Side = "sandbox"
)

// Placement is one piece of material a capability wants written down,
// returned rather than performed so every mover of material stays
// enumerable and auditable, and so a provider is testable against a
// returned value with no sandbox and no cloud involved. Content is
// material: never logged, and never handed to PromptSection, which
// sees only a placement's Path.
type Placement struct {
	Side    Side
	Path    string
	Content string
	Mode    string // see EffectiveMode; empty means the safe default.
}

// EffectiveMode is Mode, defaulting to "600" -- the safe answer, so a
// provider that leaves Mode unset does not thereby get a wider one.
func (p Placement) EffectiveMode() string {
	if p.Mode == "" {
		return "600"
	}
	return p.Mode
}

// Resolution is Resolve's answer. The zero value is Honoured, because
// most capabilities honour every request -- see BaseCapability.
// Refused parks the task, with Reason posted verbatim as the comment
// explaining why: human-facing text, not a code.
type Resolution struct {
	Refused bool
	Reason  string
}

// Honoured is the resolution a provider returns when it can grant what
// was asked.
func Honoured() Resolution { return Resolution{} }

// RefusedBecause is the resolution a provider returns when it cannot --
// reason is posted to the task verbatim, so it should read as a
// sentence a human can act on.
func RefusedBecause(reason string) Resolution { return Resolution{Refused: true, Reason: reason} }

// Materialization is what Materialize produces. Three fields for three
// kinds of effect: a Lease to record and eventually revoke, Placements
// for the executor to apply, and -- for a SELECT capability, which
// mints nothing and writes nothing into a sandbox -- which standing
// credential to route through instead.
type Materialization struct {
	Lease              *Lease
	Placements         []Placement
	CredentialOverride *CredentialRef
}

// ResourceExhaustedError says a capability is configured correctly but
// cannot mint one more instance of a finite resource right now. It is not a
// failed grant: another run releasing its lease can make the exact same
// Materialize call succeed. Admission callers keep the task or CI job queued
// and retry without taking a sandbox permit.
//
// RetryAfter is provider advice when one is available. Zero uses
// DefaultResourceRetryInterval. Err remains reachable through errors.Is/As so
// the provider's original response is not lost.
type ResourceExhaustedError struct {
	Capability string
	Resource   string
	RetryAfter time.Duration
	Err        error
}

func (e *ResourceExhaustedError) Error() string {
	if e == nil {
		return "model: capability resource is temporarily exhausted"
	}
	prefix := "model: capability resource is temporarily exhausted"
	if e.Resource != "" {
		prefix = fmt.Sprintf("model: %s is temporarily exhausted", e.Resource)
	}
	if e.Err == nil {
		return prefix
	}
	return prefix + ": " + e.Err.Error()
}

func (e *ResourceExhaustedError) Unwrap() error { return e.Err }

// ResourceExhausted finds a provider's temporary finite-resource refusal in
// an error chain. Capability implementations use the concrete type; callers
// use this helper so the errors.As spelling and nil handling live in one
// place.
func ResourceExhausted(err error) (*ResourceExhaustedError, bool) {
	var exhausted *ResourceExhaustedError
	if errors.As(err, &exhausted) {
		return exhausted, true
	}
	return nil, false
}

const DefaultResourceRetryInterval = 5 * time.Second

// CredentialResolver resolves a credential by name to material. A
// provider is handed one of these, never a store to browse, so what it
// can reach is exactly what it names.
type CredentialResolver interface {
	Resolve(ctx context.Context, name string) (string, error)
}

// CapabilityContext bounds what a provider can do: this task, this run,
// a resolver for named credentials, per-run scratch, and the clock --
// not arbitrary shell, not a credential store to browse.
type CapabilityContext struct {
	Task        Task
	Run         Run
	Now         time.Time
	Workdir     string
	Credentials CredentialResolver
	// CI says this context was built for a CI job rather than for an
	// agent run on a task -- resolveCICapabilities (pkg/cli/ci.go) is
	// the one thing that sets it, and it is what lets ResolveGrants tell
	// the two paths apart without either of them owning the rule.
	//
	// It defaults to false on purpose: a zero CapabilityContext is a
	// task's, so a new caller that resolves grants for something other
	// than CI gets the safe answer (no CI-only capability) without
	// having to know this field exists.
	CI bool
	// Deadline is the latest the run this context was built for can
	// still be alive, whatever happens to it: past it, grain has
	// cancelled the run even with every minute of wait credit it could
	// earn. A provider minting something the run's end should revoke may
	// bound it by this too, so that a Revoke that never comes -- a crash
	// between the run ending and its slot freeing -- leaves it live only
	// as long as the run could have been, rather than for the whole of
	// the provider's own MaxLease.
	//
	// Zero is a caller with no deadline to state, and a provider falls
	// back to its own bound. RunDispatch is what sets it.
	Deadline time.Time
	// Connection is the team model connection whose credential the run's
	// own agent spends -- the connection that owns the key
	// (AgentBackend.CredentialOwnerID), so one borrowing another's key by
	// CredentialBackendID names the source -- or "" for a run on none. A
	// provider that spends a model credential on the run's behalf (the
	// model proxies) spends this one when it is of its vendor, and
	// otherwise the team's own connection of its vendor, never a
	// deployment-wide key. RunDispatch is what sets it.
	Connection string
}

// CapabilitySpec is a capability's fixed identity: what a grant of it
// means, before any particular task holds one.
type CapabilitySpec struct {
	Name        string
	Label       string
	Description string
	Source      GrantSource
	Provision   Provision
	// MaxLease is the unconditional backstop past which a lease is
	// revoked regardless of its own ExpiresAt -- see Lease.Expired. Zero
	// for a GRANT or SELECT capability, which mint nothing to expire.
	MaxLease time.Duration
	// Requires names every secret this capability resolves through
	// CapabilityContext.Credentials -- docs/data-model.md's `requires`
	// field, widened from one name to every name a provider actually
	// calls Resolve with (gcp-key already needs a minter credential
	// distinct from the key it mints). A name here is never material:
	// it is exactly what a deployment's secret store (see secrets)
	// must have configured for this capability to materialize, so this
	// field is what a checked-in listing of capability requirements can
	// show without showing a single value.
	Requires []string
	// CIOnly marks a capability that may be granted to a CI job and
	// never to a task -- SupportsCI's converse, and the one thing a
	// spec could not say before grain/task-382.
	//
	// The capabilities that need it are the publishing credentials
	// (registry-push, package-publish). Their material is a token that
	// can write to a repository's packages, so an operator enables them
	// per repository and CI hands them out only for a trusted post-merge
	// release or an operator's own workflow_dispatch -- the
	// CIEventCapability gate below, applied in resolveCICapabilities
	// (pkg/cli/ci.go). A *task* is not an event: it has no release to
	// be the commit of and nobody asked for this particular run by hand,
	// so there is no task-path equivalent of that gate to apply, and
	// "the task is working on the default branch" is not the assurance
	// "this commit is already merged" is. The honest answer is therefore
	// that such a capability is not a task grant at all, and this field
	// is how the model says so once, for every layer that has to act on
	// it: ResolveGrants refuses one held by a task, ui.OfferedCapabilities
	// does not offer it, and ui's settings and per-repo default pickers
	// refuse to seed it.
	//
	// The rule for the next event-gated capability, since a precedent is
	// harder to read than a rule: a provider that implements
	// CIEventCapability must set this, because every event it gates on is
	// a CI event and a task produces none of them.
	// ui.TestEveryEventGatedCapabilityIsCIOnly holds the tree to it.
	CIOnly bool
}

// SupportsCI reports whether this capability can deliver material to a CI
// shell. Agent tool grants and agent Git credential selectors require an agent
// run and are not offered for CI.
func (s CapabilitySpec) SupportsCI() bool {
	return s.Provision == ProvisionMint || s.Provision == ProvisionPlace || s.Name == "grain-ci"
}

// SupportsTask is SupportsCI's converse: whether a task may hold this
// capability at all. False for a CI-only one, whose material is released
// on a CI event a task cannot produce -- see CapabilitySpec.CIOnly.
func (s CapabilitySpec) SupportsTask() bool { return !s.CIOnly }

// CIEventCapability optionally narrows a CI-capable provider to events on
// which its material may enter a sandbox. Release credentials use this to be
// structurally absent from pull-request and ordinary push jobs even when an
// operator has enabled the capability for the repository.
type CIEventCapability interface {
	SupportsCIEvent(event string) bool
}

// TrustedScheduleCapability optionally widens a CIEventCapability that
// refuses the "schedule" event to the one schedule run a caller cannot
// ask for: the run grain's own scheduled-workflow starter fires from a
// workflow's `on: schedule` cron (ci.Request.TrustedSchedule). It is
// opt-in per provider, so a scheduled job is handed only the trusted
// material whose provider has said it wants a cron to hold it.
type TrustedScheduleCapability interface {
	SupportsTrustedSchedule() bool
}

// CapabilityProvider is the contract a capability implements.
//
// PromptSection is only ever called, by ResolveGrants/MaterializeGrants/
// PromptSections below, for a grant whose Materialize already succeeded
// -- the prompt is a promise to the agent, and a capability that
// half-applied must not be described as present. It receives the
// placements Materialize returned, not the request that produced them,
// and never their Content -- so leaking material into a prompt is
// structurally impossible rather than carefully avoided.
type CapabilityProvider interface {
	Spec() CapabilitySpec
	Resolve(ctx context.Context, cc CapabilityContext) (Resolution, error)
	Materialize(ctx context.Context, cc CapabilityContext) (Materialization, error)
	PromptSection(ctx context.Context, cc CapabilityContext, placements []Placement) (string, error)
	Revoke(ctx context.Context, cc CapabilityContext, lease Lease) error
}

// ScarceResourceProvider marks a provider whose Materialize call reserves a
// finite external resource that another live run may currently hold. Grain
// materializes these providers during admission, before it records an agent
// run or takes a CI sandbox permit, so a ResourceExhaustedError leaves the
// work queued rather than consuming the capacity needed to make progress.
type ScarceResourceProvider interface {
	CapabilityProvider
	ScarceResourceProvider()
}

// IsScarceResourceProvider reports whether p participates in the admission
// gate above. It is exported because dispatch and CI are deliberately outside
// the provider packages and need the answer without depending on either one.
func IsScarceResourceProvider(p CapabilityProvider) bool {
	_, ok := p.(ScarceResourceProvider)
	return ok
}

// BaseCapability is embeddable by a provider that wants the contract's
// default behaviour: resolve always Honoured, mint nothing, no prompt
// text, revoke a no-op. A GRANT capability -- one that needs no
// credential at all -- is most of a file shorter for embedding this and
// writing only Spec and, if it has one, PromptSection.
type BaseCapability struct{}

func (BaseCapability) Resolve(context.Context, CapabilityContext) (Resolution, error) {
	return Honoured(), nil
}

func (BaseCapability) Materialize(context.Context, CapabilityContext) (Materialization, error) {
	return Materialization{}, nil
}

func (BaseCapability) PromptSection(context.Context, CapabilityContext, []Placement) (string, error) {
	return "", nil
}

func (BaseCapability) Revoke(context.Context, CapabilityContext, Lease) error {
	return nil
}

// Reaper is implemented by a capability provider whose minted resource can
// outlive the Lease that recorded it -- a controller crash between mint and
// store write, or an operator-invalidated record. Unlike Revoke, which acts
// on one Lease a task held, Reap consults the resource's own source of truth
// (a cloud API's own listing, never grain's store) and deletes Grain-owned
// resources created before that timestamp.
//
// The caller computes before once from every live agent and CI run, with a
// grace period, and passes the same trailing edge to every provider. A
// provider must not apply a second age window: centralizing the boundary is
// what keeps different kinds of external infrastructure under one safety
// rule.
//
// creds resolves whatever standing credential the provider needs to do
// that: the same kind of resolver a Materialize/Revoke call is handed
// through CapabilityContext, but usable with no live task in hand. Reap
// returns both the identifiers it deleted and any per-resource errors, so
// one failed delete does not hide successful cleanup or stop later resources.
//
// Optional: a provider that mints nothing external to a Lease has nothing
// to check outside its own leases and need not implement this.
type Reaper interface {
	Reap(ctx context.Context, creds CredentialResolver, before time.Time) ([]string, error)
}

// CredentialCheck is what CheckCredential below reports back: which
// standing credentials grain authenticated as, and one sentence naming
// what the far end said when it did.
//
// Detail is human-facing text, the same bar Resolution.Reason holds to:
// it is shown verbatim on Settings' Capabilities tab and printed by
// `grain settings`, so it should read as a sentence somebody can act on
// rather than as a status code or a dump of an API response.
type CredentialCheck struct {
	// Credentials names every standing credential the check
	// authenticated as -- CapabilitySpec.Requires' own names, so a
	// caller reporting a refusal can name the secret to replace without
	// knowing anything about the capability. Usually one; github-sandbox
	// signs as a GitHub App, which is two.
	Credentials []string
	// Detail is what came back, on the way through: how many keys the
	// account has, which installation answered. It is the evidence that
	// the credential is live rather than merely present -- an empty
	// Detail with a nil error would say "checked" and show nothing for
	// it, which is the shape of answer this whole check exists to
	// replace.
	Detail string
}

// CredentialChecker is implemented by a capability provider that holds a
// *standing* credential -- one an operator pasted once, which every
// later mint authenticates as. Such a credential can stop working with
// no setting on this deployment changing: the key inside the secret is
// deleted or rotated away at the far end, and nothing grain stores can
// see it. A configuration pane reads that deployment as configured, and
// says so, until a task fails.
//
// CheckCredential is the one call that can tell the difference. It
// authenticates as the credential and makes the cheapest, most harmless
// request that proves the far end still accepts it -- a listing of what
// the provider already administers (gcp-key lists the agent account's
// own keys, exactly as Reap does), never a mint, never a write, and
// never anything needing permission beyond what an ordinary
// Materialize already needs.
//
// It is called on demand, by a human who asked for it, not on a timer:
// every call is a real round trip to somebody else's API, and the answer
// is true only at the moment it is given.
//
// An error means the check ran and the answer is no -- the credential
// was refused, or the API refused the call it was used for -- and it
// should be the provider's own explained error, the one naming the
// secret to replace, rather than the raw error the API returned. A
// provider that cannot even attempt the check (this deployment never
// configured it) says so with an error too; there is no third return.
//
// Optional, exactly as Reaper is: a capability holding no standing
// credential -- self-debug, self-repair, bootstrap-playbooks -- has
// nothing that could go stale behind grain's back and need not implement
// this.
type CredentialChecker interface {
	CheckCredential(ctx context.Context, creds CredentialResolver) (CredentialCheck, error)
}

// DynamicProvider resolves a capability on demand if no static provider is
// registered for its name.
type DynamicProvider func(name string) (CapabilityProvider, bool)

// CapabilityRegistry holds providers in registration order --
// deterministic, so two providers that both act on the same task
// compose predictably rather than by map iteration order -- and looks
// them up by name for the grants a particular task actually holds.
type CapabilityRegistry struct {
	order     []string
	providers map[string]CapabilityProvider
	dynamic   []DynamicProvider
	ciDynamic []DynamicProvider
}

// NewCapabilityRegistry builds a registry from providers, in the order
// given.
func NewCapabilityRegistry(providers ...CapabilityProvider) *CapabilityRegistry {
	r := &CapabilityRegistry{providers: make(map[string]CapabilityProvider)}
	r.Register(providers...)
	return r
}

// Register adds providers, in the order given, appended after whatever
// is already registered. Registering a name a second time replaces its
// provider in place without changing registration order -- the same
// rule mcp.Registry.Register follows, for the same reason.
func (r *CapabilityRegistry) Register(providers ...CapabilityProvider) {
	for _, p := range providers {
		name := p.Spec().Name
		if _, exists := r.providers[name]; !exists {
			r.order = append(r.order, name)
		}
		r.providers[name] = p
	}
}

// RegisterDynamic registers dynamic capability provider resolvers.
func (r *CapabilityRegistry) RegisterDynamic(resolvers ...DynamicProvider) {
	r.dynamic = append(r.dynamic, resolvers...)
}

// RegisterCIDynamic registers dynamic resolvers consulted, ahead of
// every other provider, only for a CI job's grants (LookupFor): a name
// that means one thing to a task and another to a CI job -- an api
// grant is routes an agent may call on a task, and a token a job is
// minted in CI (pkg/capability/ciapi) -- is resolved here for CI.
func (r *CapabilityRegistry) RegisterCIDynamic(resolvers ...DynamicProvider) {
	r.ciDynamic = append(r.ciDynamic, resolvers...)
}

// LookupFor is Lookup for a CI job's grant when ci is true, and Lookup
// otherwise.
func (r *CapabilityRegistry) LookupFor(name string, ci bool) (CapabilityProvider, bool) {
	if r != nil && ci {
		for _, resolve := range r.ciDynamic {
			if p, ok := resolve(name); ok {
				return p, true
			}
		}
	}
	return r.Lookup(name)
}

// Lookup finds a provider by capability name.
func (r *CapabilityRegistry) Lookup(name string) (CapabilityProvider, bool) {
	if r == nil {
		return nil, false
	}
	if p, ok := r.providers[name]; ok {
		return p, true
	}
	for _, resolve := range r.dynamic {
		if p, ok := resolve(name); ok {
			return p, true
		}
	}
	return nil, false
}

// Providers returns every registered provider, in registration order.
func (r *CapabilityRegistry) Providers() []CapabilityProvider {
	out := make([]CapabilityProvider, len(r.order))
	for i, name := range r.order {
		out[i] = r.providers[name]
	}
	return out
}

// GrantResolution pairs one of a task's Grants with what Resolve
// decided for it.
type GrantResolution struct {
	Grant      Grant
	Resolution Resolution
}

// secretsContext tags ctx with the task and run cc is for, so a secret a
// provider resolves for it is recorded in the audit trail against the
// task and run that used it (secrets.Store.Resolve). A task belongs to no
// team, so the secret is resolved as the default team's.
func secretsContext(ctx context.Context, cc CapabilityContext) context.Context {
	ctx = secrets.WithTask(ctx, cc.Task.ID)
	return secrets.WithRun(ctx, cc.Run.ID)
}

// ResolveGrants resolves every capability cc.Task was granted, calling
// Resolve on each one's registered provider. Order follows the
// registry's own registration order rather than the order Grants
// happen to be stored in, so results are deterministic regardless of
// how the grants were recorded -- the same "providers run in
// registration order" rule docs/data-model.md gives providers that act
// on a sandbox.
//
// A grant naming a capability no provider is registered for is refused
// with a reason that says so, rather than skipped or left to panic
// later in Materialize: the registry not recognising a capability by
// that name is exactly the unhonourable case Resolve exists to report.
// This is also the backstop docs/data-model.md asks for when a
// capability is retired while a lease is still outstanding -- Revoke
// stays reachable through Lookup even once nothing grants the name
// again.
//
// A grant of a CI-only capability (CapabilitySpec.CIOnly) held by a task
// -- a context with CI false -- is refused here, before that provider's
// own Resolve is called and so long before anything could Materialize:
// the material never leaves the controller, because the one call that
// would mint or place it is never reached. This is the task-path
// counterpart of the event gate resolveCICapabilities applies
// (CIEventCapability), and it is a refusal rather than a narrowing
// because a task is not an event and there is nothing narrower to
// apply. See CapabilitySpec.CIOnly for the whole of that reasoning.
func ResolveGrants(ctx context.Context, reg *CapabilityRegistry, cc CapabilityContext) ([]GrantResolution, error) {
	ctx = secretsContext(ctx, cc)
	granted := make(map[string]Grant, len(cc.Task.Grants))
	for _, g := range cc.Task.Grants {
		// A capability reference is a capability server's to mint, at
		// admission (protocol.MintPass), not an in-process provider's.
		if IsCapabilityRef(g.Capability) {
			continue
		}
		granted[g.Capability] = g
	}

	var out []GrantResolution
	seen := make(map[string]bool, len(granted))
	for _, p := range reg.Providers() {
		g, ok := granted[p.Spec().Name]
		if !ok {
			continue
		}
		seen[g.Capability] = true
		if !cc.CI && !p.Spec().SupportsTask() {
			out = append(out, GrantResolution{Grant: g, Resolution: RefusedBecause(CIOnlyRefusal(g.Capability))})
			continue
		}
		res, err := p.Resolve(ctx, cc)
		if err != nil {
			return nil, fmt.Errorf("model: resolving capability %q: %w", g.Capability, err)
		}
		out = append(out, GrantResolution{Grant: g, Resolution: res})
	}

	var unregistered []string
	for name := range granted {
		if !seen[name] {
			g := granted[name]
			if p, ok := reg.LookupFor(name, cc.CI); ok {
				seen[name] = true
				if !cc.CI && !p.Spec().SupportsTask() {
					out = append(out, GrantResolution{Grant: g, Resolution: RefusedBecause(CIOnlyRefusal(g.Capability))})
					continue
				}
				res, err := p.Resolve(ctx, cc)
				if err != nil {
					return nil, fmt.Errorf("model: resolving capability %q: %w", g.Capability, err)
				}
				out = append(out, GrantResolution{Grant: g, Resolution: res})
				continue
			}
			unregistered = append(unregistered, name)
		}
	}
	sort.Strings(unregistered)
	for _, name := range unregistered {
		out = append(out, GrantResolution{
			Grant:      granted[name],
			Resolution: RefusedBecause(fmt.Sprintf("no provider is registered for capability %q", name)),
		})
	}
	return out, nil
}

// MissingGCPKeyRefusal is what a task is told when it asks for a GCP
// key, gcp-key:<name>, that the deployment has no key of that name for.
func MissingGCPKeyRefusal(capability string) string {
	return fmt.Sprintf("capability %q names a GCP key this deployment has not configured: a system admin "+
		"configures one with `grain gcp-key set` or /api/gcp-keys, and the task grants %s",
		capability, GCPKeyCapabilityPrefix+"<name>")
}

// CIOnlyRefusal is what a task holding a CI-only capability is told,
// kept here beside the rule it is the refusal of so that the one
// sentence serves every reader of it: ResolveGrants posts it as a
// Resolution.Reason (which a failed dispatch repeats verbatim, and
// which grain explains in the task's own conversation), and ui's
// pickers refuse an id with the same words rather than with a second
// account of the same rule.
//
// It names three things on purpose -- the capability, what kind of
// grant it is, and where the setting that does work actually lives --
// because the operator this was written for had ticked the box on a
// task and had no way to find out why the task then failed.
func CIOnlyRefusal(capability string) string {
	return fmt.Sprintf("%s is a CI capability, not a task grant: its material is a credential that "+
		"writes to the repository itself, so it is released only to a trusted post-merge release or "+
		"to a run an operator asked for by hand, and a task run is neither. Take it off this task, and "+
		"enable it for the repository's CI jobs instead -- the repos page, under CI capabilities.",
		capability)
}

// Materialized is one grant's Materialize result, with the provider
// that produced it -- carried alongside so PromptSections below can
// call PromptSection without a second registry lookup.
type Materialized struct {
	Grant           Grant
	Provider        CapabilityProvider
	Materialization Materialization
}

// MaterializeGrants materializes every honoured resolution, in the order
// ResolveGrants returned them, skipping every refused one.
//
// A failed Materialize stops the pass immediately and returns both the
// error and what succeeded before it -- "a failed materialize means no
// dispatch": this package does not decide what a partial
// materialization means for a run (nothing dispatches yet -- see
// README.md), it just refuses to paper over the failure by
// continuing past it or by calling PromptSection for a capability that
// never finished applying. ResourceExhaustedError is returned unchanged in the
// error chain: the admission layers for workers and CI decide how to wait
// without holding their sandbox capacity.
func MaterializeGrants(ctx context.Context, reg *CapabilityRegistry, cc CapabilityContext, resolved []GrantResolution) ([]Materialized, error) {
	return CompleteMaterializeGrants(ctx, reg, cc, resolved, nil)
}

// MaterializeScarceGrants reserves only the finite-resource providers among
// resolved. The returned material is passed to CompleteMaterializeGrants once
// the work has been admitted, so a successful reservation is never minted a
// second time. A ResourceExhaustedError means no run or sandbox should start.
func MaterializeScarceGrants(ctx context.Context, reg *CapabilityRegistry, cc CapabilityContext, resolved []GrantResolution) ([]Materialized, error) {
	ctx = secretsContext(ctx, cc)
	var out []Materialized
	for _, r := range resolved {
		if r.Resolution.Refused {
			continue
		}
		p, ok := reg.LookupFor(r.Grant.Capability, cc.CI)
		if !ok {
			return out, fmt.Errorf("model: materializing capability %q: no provider registered", r.Grant.Capability)
		}
		if !IsScarceResourceProvider(p) {
			continue
		}
		m, err := p.Materialize(ctx, cc)
		if err != nil {
			if exhausted, ok := ResourceExhausted(err); ok && exhausted.Capability == "" {
				exhausted.Capability = r.Grant.Capability
			}
			return out, fmt.Errorf("model: materializing capability %q: %w", r.Grant.Capability, err)
		}
		out = append(out, Materialized{Grant: r.Grant, Provider: p, Materialization: m})
	}
	return out, nil
}

// CompleteMaterializeGrants materializes every honoured grant not already in
// prepared and returns the complete slice in resolution order. prepared is the
// scarce material reserved before admission; accepting it here is what keeps
// the later sandbox setup on the ordinary capability path without minting a
// second key.
func CompleteMaterializeGrants(ctx context.Context, reg *CapabilityRegistry, cc CapabilityContext, resolved []GrantResolution, prepared []Materialized) ([]Materialized, error) {
	ctx = secretsContext(ctx, cc)
	byCapability := make(map[string]Materialized, len(prepared))
	for _, m := range prepared {
		byCapability[m.Grant.Capability] = m
	}
	var out []Materialized
	for _, r := range resolved {
		if r.Resolution.Refused {
			continue
		}
		if m, ok := byCapability[r.Grant.Capability]; ok {
			out = append(out, m)
			delete(byCapability, r.Grant.Capability)
			continue
		}
		p, ok := reg.LookupFor(r.Grant.Capability, cc.CI)
		if !ok {
			return out, fmt.Errorf("model: materializing capability %q: no provider registered", r.Grant.Capability)
		}
		m, err := p.Materialize(ctx, cc)
		if err != nil {
			if exhausted, ok := ResourceExhausted(err); ok && exhausted.Capability == "" {
				exhausted.Capability = r.Grant.Capability
			}
			return out, fmt.Errorf("model: materializing capability %q: %w", r.Grant.Capability, err)
		}
		out = append(out, Materialized{Grant: r.Grant, Provider: p, Materialization: m})
	}
	if len(byCapability) != 0 {
		var names []string
		for name := range byCapability {
			names = append(names, name)
		}
		sort.Strings(names)
		return out, fmt.Errorf("model: prepared material names unresolved capabilities: %v", names)
	}
	return out, nil
}

// PromptSections collects prompt text for every successfully
// materialized grant, in order, dropping any that returned no text --
// a GRANT capability with nothing to say, or a SELECT capability that
// only changed which credential the git proxy routes through.
func PromptSections(ctx context.Context, cc CapabilityContext, materialized []Materialized) ([]string, error) {
	ctx = secretsContext(ctx, cc)
	var out []string
	for _, m := range materialized {
		text, err := m.Provider.PromptSection(ctx, cc, m.Materialization.Placements)
		if err != nil {
			return nil, fmt.Errorf("model: prompt section for capability %q: %w", m.Grant.Capability, err)
		}
		if text != "" {
			out = append(out, text)
		}
	}
	return out, nil
}
