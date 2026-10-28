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

// Package geminikey is a MINT model.CapabilityProvider: it mints a
// short-lived Gemini API key for a task labelled grain-gemini-key, places
// it in the sandbox, and revokes it when the task's slot frees --
// bwsalmon/agents#239, porting grain/automation/gemini_keys.py's
// "gemini-key" capability (docs/data-model.md's worked MINT example)
// into v2.
//
// README.md notes that a real MINT provider needs "standing
// credentials and a controller v2 has neither of" -- that gap is about
// wiring, not about whether this package's logic can be written and
// tested: nothing here assumes a controller process exists.
// model.CapabilityContext already carries a CredentialResolver, which is
// all minting needs to reach a standing credential by name; what v2 is
// still missing is something that constructs a real *store.Store-backed
// CapabilityContext and calls Materialize/Revoke against it during an
// actual dispatch. That wiring, and a gcp-key capability to go with it,
// are follow-on work.
package geminikey

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/apikeys/v2"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/ai-on-gke/grain/pkg/model"
)

// KeyPath is where a minted key lands in the sandbox -- fixed, not
// configurable, because the prompt this capability writes names it
// verbatim (see PromptSection) and nothing here has a reason to move it.
const KeyPath = "/home/debian/.gemini-api-key"

// DefaultAPITargetService scopes every minted key to the Generative
// Language API alone, so a leaked key is useless against anything else
// enabled in the project -- the same restriction
// grain/automation/gemini_keys.py applies via --api-target=service=...
const DefaultAPITargetService = "generativelanguage.googleapis.com"

// displayNamePrefix marks a key as this capability's own. An API key is
// not scoped to a service account the way a gcp-key capability's keys
// are -- ListKeys returns every key in the project -- so this prefix is
// the only thing separating grain's keys from anyone else's, and
// DeleteExpired must never delete a key without it.
//
// It is a *deployment*-blind mark, though: two grain deployments minting
// into one GCP project both write it, and neither's reap can tell the
// other's leaked key from its own. See Reap.
const displayNamePrefix = "grain-"

// OperatingKeyDisplayName names the daemon's own long-lived operating
// key -- the one pkg/agent/antigravity runs as, seeded at deploy time by
// `grain secrets mint-gemini-key` (pkg/cli/secrets.go) rather than
// supplied by hand.
//
// It deliberately keeps displayNamePrefix, because that prefix is what
// marks a key in this project as grain's at all. That alone would make
// it reapable, though, so deleteBefore exempts it by exact name.
// Renaming this constant out from under that check would let the shared
// trailing-edge sweep delete the running daemon's own key.
//
// The name is fixed rather than derived so the exemption is a literal
// comparison with no run-shaped input in it: Materialize's own keys are
// grain-<run id>, and no run id is this string.
const OperatingKeyDisplayName = displayNamePrefix + "daemon-operating-key"

// maxLease is the unconditional backstop past which a recorded lease is
// revoked regardless of whether its task released cleanly. A GCP API key
// has no native TTL. Orphans with no usable lease record are handled
// separately by Reap using the daemon's shared live-work trailing edge.
const maxLease = 24 * time.Hour

// minter is the narrow surface Materialize, Revoke and Reap
// need against the API Keys API -- narrow enough that Capability's own
// tests fake it and need no network, the same "test needs no sandbox and
// no cloud" bar docs/data-model.md sets for a MINT provider.
type minter interface {
	CreateKey(ctx context.Context, displayName, apiTargetService string) (name, keyString string, err error)
	DeleteKey(ctx context.Context, name string) error
	ListKeys(ctx context.Context) ([]mintedKey, error)
}

// mintedKey is one entry from ListKeys -- just enough for DeleteExpired
// to decide age and ownership without carrying the key string.
type mintedKey struct {
	Name        string
	DisplayName string
	CreateTime  time.Time
}

// minterFactory builds a minter from resolved credential material (a
// standing service-account key's raw JSON) and the project it mints
// into. A field on Capability rather than a package-level default, so
// tests substitute a fake with no network involved.
type minterFactory func(ctx context.Context, credentialJSON, projectID string) (minter, error)

// Capability mints Gemini API keys, scoped to one GCP project and
// authenticated as one standing credential.
//
// Credential deliberately names the same standing credential a gcp-key
// capability would mint service-account keys with (bwsalmon/agents#239:
// "This can share the same account from the gcp capability") -- there is
// no separate minter identity here the way
// grain/automation/gemini_keys.py's impersonation dance has, because
// nothing in this project yet grants that identity narrower permissions
// than the account it would otherwise share.
type Capability struct {
	// ProjectID is the GCP project keys are minted in and listed from.
	// Empty makes Resolve refuse -- see Resolve.
	ProjectID string
	// Credential names the standing credential Materialize and Revoke
	// resolve, through model.CapabilityContext.Credentials, to get
	// material to mint with. Empty makes Resolve refuse.
	Credential model.CredentialRef
	// APITargetService restricts every minted key to one API. Empty
	// means DefaultAPITargetService.
	APITargetService string

	factory minterFactory // nil means newAPIKeysMinter
}

func (*Capability) ScarceResourceProvider() {}

// New builds a Capability minting into project, authenticated as
// credential, restricted to DefaultAPITargetService.
func New(projectID string, credential model.CredentialRef) *Capability {
	return &Capability{ProjectID: projectID, Credential: credential}
}

func (c *Capability) apiTargetService() string {
	if c.APITargetService != "" {
		return c.APITargetService
	}
	return DefaultAPITargetService
}

func (c *Capability) Spec() model.CapabilitySpec {
	spec := model.CapabilitySpec{
		Name:        "gemini-key",
		Label:       "grain-gemini-key",
		Description: "Mint a short-lived Gemini API key for this task",
		Source:      model.GrantByLabel,
		Provision:   model.ProvisionMint,
		MaxLease:    maxLease,
	}
	// The standing credential Materialize/Revoke mint under (this
	// package's doc comment: "there is no separate minter identity
	// here") is resolved through CapabilityContext.Credentials, so it
	// belongs here the same way gcpkey.Provider.Spec() lists its own
	// minter credential. Omitted, not the empty string, when this
	// Capability was never configured with one -- Resolve already
	// refuses that case, and an unconfigured deployment has no name to
	// require.
	if c.Credential.Name != "" {
		spec.Requires = []string{c.Credential.Name}
	}
	return spec
}

// Resolve refuses when this Capability was never configured with a
// project and a standing credential to mint with -- the same
// "unhonourable means parked, never silently downgraded" treatment
// grain/automation/gemini_keys.py's GeminiKey.resolve gives a deployment
// missing gemini_key_config, translated to "this value was never wired
// up" since v2 has no deployment config to be absent from yet.
//
// It also refuses when that credential names nothing this deployment's
// secret store can resolve. Materialize would fail on the very same call
// a moment later, so nothing new is caught here -- what changes is which
// half of the contract reports it. A refusal is posted to the task
// verbatim (Resolution.Reason), so an operator reads a sentence naming
// the secret to set; a failed Materialize is wrapped into a run's error
// detail as "materializing capabilities: geminikey: resolving credential
// ...", which is grain describing its own internals. The distinction
// matters most for exactly this capability, whose configuration is
// spread across a deployment setting, a secret and a GCP IAM grant, and
// which therefore has three separate ways to be half-wired.
func (c *Capability) Resolve(ctx context.Context, cc model.CapabilityContext) (model.Resolution, error) {
	if c.ProjectID == "" || c.Credential.Name == "" {
		return model.RefusedBecause(
			"this task asks for a Gemini key this deployment isn't configured to " +
				"mint. An operator sets the GCP project the key is minted in, under " +
				"Settings -> Capabilities (`grain settings -gcp-project <project>`).",
		), nil
	}
	if cc.Credentials == nil {
		return model.RefusedBecause(
			"this task asks for a Gemini key, but nothing here can reach the " +
				"standing credential `" + c.Credential.Name + "` the key would be " +
				"minted under.",
		), nil
	}
	if _, err := cc.Credentials.Resolve(ctx, c.Credential.Name); err != nil {
		return model.RefusedBecause(
			"this task asks for a Gemini key, but the standing credential `" +
				c.Credential.Name + "` it is minted under is not set on this " +
				"deployment. An operator pastes the GCP minter service account's " +
				"key file into Settings -> Secrets, or runs `grain secrets set " +
				c.Credential.Name + " key.json -value-file <path>`.",
		), nil
	}
	return model.Honoured(), nil
}

// explainRefusedCredential names the one failure that is about the
// standing minter credential itself rather than about the project being
// minted in: GCP refusing to issue a token for it at all, so that
// nothing this package does ever reaches the API Keys API
// (isCredentialRefused, and see it for the shape that error arrives in).
//
// It is the failure task 163 fixed for gcp-key, and it lands here for
// the same reason it landed there: gemini-key mints through the very
// same standing credential (pkg/cli/daemon.go hands geminikey.New
// gcpkey.DefaultMinterCredential), so a minter key deleted or rotated
// away in GCP breaks every Gemini mint exactly as it breaks a
// service-account one. What an operator got was Google's own
// `invalid_grant` / "Invalid JWT Signature" -- a sentence naming no
// credential, no secret, no project and nothing about grain, on a
// deployment whose Settings pane reads **Ready** because a project and a
// secret are both set. Nothing on any configuration pane can see that
// the key inside that secret has stopped working: only GCP knows, and
// only when something authenticates with it.
//
// Every cause comes down to "the key in that secret is not one GCP will
// accept any more", so the sentence names the ways that happens --
// including the one this deployment shape actually hits, a deployer that
// rotates the minter key underneath a host that had only ever seeded its
// own copy once -- and the two places a current key is pasted. Wrapped,
// never replaced: GCP's own words survive, the same rule advise holds
// to. The wording is deliberately all but identical to
// gcpkey.explainRefusedCredential's, past the name of the API nothing
// reached: an operator who has read one of these sentences in a task's
// error detail should recognise the other on sight rather than have to
// work out whether it is the same problem.
//
// The credential's *name* is the reason this sits out here rather than
// beside advise, down with the minter: a minter is handed material and
// never told which secret it came out of, while a Capability (and
// MintOperatingKey, which is handed the name directly) is the only thing
// that knows both.
func explainRefusedCredential(err error, credential string) error {
	if !isCredentialRefused(err) {
		return err
	}
	return fmt.Errorf(
		"GCP will not issue a token for the minter credential held in the `%s` secret, so "+
			"nothing here reached the API Keys API at all: the service-account key that secret "+
			"holds has been deleted or rotated away in GCP (terraform/gcp's push-secrets.sh "+
			"mints a fresh minter key and invalidates older ones on every run), or the "+
			"account it belongs to is gone or disabled, or this host's clock is too far out "+
			"for Google to accept a request signed by it. Paste a current key file into "+
			"Settings -> Capabilities, or run `grain secrets set %s key.json -value-file "+
			"<path>` on the host: %w",
		credential, credential, err)
}

// Materialize mints a fresh key named grain-<run id> -- the janitor's
// positive signal falling out of ordinary use, the same reasoning
// docs/data-model.md gives for gemini_keys.py's own display_name -- and
// returns it as a single sandbox Placement at KeyPath plus the Lease to
// revoke it by later.
func (c *Capability) Materialize(ctx context.Context, cc model.CapabilityContext) (model.Materialization, error) {
	m, err := c.minter(ctx, cc)
	if err != nil {
		return model.Materialization{}, err
	}
	displayName := displayNamePrefix + cc.Run.ID
	name, keyString, err := m.CreateKey(ctx, displayName, c.apiTargetService())
	if err != nil {
		err = waitableKeyLimit(err)
		return model.Materialization{}, fmt.Errorf("geminikey: minting a key for run %s: %w",
			cc.Run.ID, explainRefusedCredential(err, c.Credential.Name))
	}
	expires := cc.Now.Add(maxLease)
	return model.Materialization{
		Lease: &model.Lease{
			Capability: c.Spec().Name,
			Resource:   name,
			MintedBy:   c.Credential,
			IssuedAt:   cc.Now,
			ExpiresAt:  &expires,
		},
		Placements: []model.Placement{
			{Side: model.SideSandbox, Path: KeyPath, Content: keyString},
		},
	}, nil
}

// waitableKeyLimit marks only the API Keys service's finite-capacity answers
// as temporary. Permission, authentication, configuration and transport
// failures still fail immediately; retrying those would leave a job blocked
// on something only an operator can fix.
func waitableKeyLimit(err error) error {
	var apiErr *googleapi.Error
	if !errors.As(err, &apiErr) {
		return err
	}
	detail := strings.ToLower(apiErr.Message + "\n" + apiErr.Body)
	limited := apiErr.Code == http.StatusTooManyRequests ||
		strings.Contains(detail, "resource_exhausted") ||
		strings.Contains(detail, "quota exceeded") ||
		strings.Contains(detail, "maximum number of api keys") ||
		(strings.Contains(detail, "api key") && (strings.Contains(detail, "too many") ||
			strings.Contains(detail, "cannot have more than")))
	if !limited {
		return err
	}
	return &model.ResourceExhaustedError{Resource: "Gemini API keys", Err: err}
}

func (c *Capability) PromptSection(ctx context.Context, cc model.CapabilityContext, placements []model.Placement) (string, error) {
	return fmt.Sprintf("A Gemini API key is at %s, readable only by you:\n\n"+
		"    export GEMINI_API_KEY=\"$(cat %s)\"\n", KeyPath, KeyPath), nil
}

// Revoke deletes the key Materialize minted, by the resource name the
// Lease carries. Idempotent: a lease revoked twice, or one whose key is
// already gone, is not an error -- see (*apiKeysMinter).DeleteKey.
//
// A refused minter credential breaks releasing a key exactly as it
// breaks minting one, so this goes through explainRefusedCredential too.
// It names this Capability's own credential rather than the lease's
// MintedBy, because that is the one it authenticated as: unlike
// gcpkey.Provider.Revoke, which builds its minter from whatever the
// lease names, c.minter always resolves c.Credential -- and pointing an
// operator at a secret this call never touched would send them to
// replace the wrong one.
func (c *Capability) Revoke(ctx context.Context, cc model.CapabilityContext, lease model.Lease) error {
	m, err := c.minter(ctx, cc)
	if err != nil {
		return err
	}
	if err := m.DeleteKey(ctx, lease.Resource); err != nil {
		return fmt.Errorf("geminikey: revoking key %s: %w",
			lease.Resource, explainRefusedCredential(err, c.Credential.Name))
	}
	return nil
}

func (c *Capability) minter(ctx context.Context, cc model.CapabilityContext) (minter, error) {
	return c.minterFor(ctx, cc.Credentials)
}

// minterFor is the seam Materialize, Revoke and Reap share: everything
// minting needs is a resolver and this Capability's own two fields, and
// a reap has no CapabilityContext to carry the resolver in -- it is not
// scoped to a task (model.Reaper's own doc comment).
func (c *Capability) minterFor(ctx context.Context, credentials model.CredentialResolver) (minter, error) {
	if credentials == nil {
		return nil, fmt.Errorf("geminikey: no credential resolver to resolve %q with", c.Credential.Name)
	}
	material, err := credentials.Resolve(ctx, c.Credential.Name)
	if err != nil {
		return nil, fmt.Errorf("geminikey: resolving credential %q: %w", c.Credential.Name, err)
	}
	build := c.factory
	if build == nil {
		build = newAPIKeysMinter
	}
	return build(ctx, material, c.ProjectID)
}

// Reap implements model.Reaper: it deletes Grain-owned task keys created
// before the caller's shared trailing edge, independent of any Lease Grain
// may or may not still have recorded. The daemon computes that edge once
// from the oldest live agent or CI run and applies one grace period across
// every capability.
//
// The sweep is project-wide, unlike gcpkey.Provider.Reap: an API key hangs
// off no service account, so displayNamePrefix is the ownership boundary.
// Two Grain deployments sharing one GCP project can therefore reap each
// other's old task keys. Give each deployment its own project when that is
// not acceptable. The daemon's operating key is always exempt by exact name.
//
// Returns without an API call when this Capability has no project or
// credential configured.
func (c *Capability) Reap(ctx context.Context, creds model.CredentialResolver, before time.Time) ([]string, error) {
	if c.ProjectID == "" || c.Credential.Name == "" {
		return nil, nil
	}
	m, err := c.minterFor(ctx, creds)
	if err != nil {
		return nil, err
	}
	return deleteBefore(ctx, m, before, c.Credential.Name)
}

// CheckCredential implements model.CredentialChecker: it authenticates
// as the standing minter credential and lists the project's API keys,
// the same call Reap already makes, and reports what came back.
//
// This capability shares gcp-key's minter credential
// (pkg/cli/daemon.go hands New the same gcpkey.DefaultMinterCredential),
// so it goes stale in exactly the same way and at exactly the same
// moment -- which is why it is checkable here rather than left as a
// gcp-key special case. What it adds over checking gcp-key alone is the
// second thing only this API can answer: whether apikeys.googleapis.com
// is enabled and the minter may administer keys in it, the pair of 403s
// advise exists to tell apart. A deployment can hold a perfectly live
// minter key and still fail every Gemini mint on that.
//
// The listing is project-wide, since an API key hangs off no account to
// scope it to (see Reap); nothing is created and nothing is deleted.
func (c *Capability) CheckCredential(ctx context.Context, creds model.CredentialResolver) (model.CredentialCheck, error) {
	check := model.CredentialCheck{Credentials: []string{c.Credential.Name}}
	if c.ProjectID == "" || c.Credential.Name == "" {
		return check, fmt.Errorf(
			"geminikey: this deployment has no GCP project and minter credential set, so " +
				"there is nothing to check (Settings -> Capabilities, or `grain settings " +
				"-gcp-project <project>`)")
	}
	m, err := c.minterFor(ctx, creds)
	if err != nil {
		return check, err
	}
	keys, err := m.ListKeys(ctx)
	if err != nil {
		return check, fmt.Errorf("geminikey: listing keys: %w",
			advise(c.ProjectID, explainRefusedCredential(err, c.Credential.Name)))
	}
	var mine int
	for _, k := range keys {
		if strings.HasPrefix(k.DisplayName, displayNamePrefix) {
			mine++
		}
	}
	check.Detail = fmt.Sprintf(
		"GCP accepted the key held in `%s` and listed %d API key(s) in project %s, %d of them grain's.",
		c.Credential.Name, len(keys), c.ProjectID, mine)
	return check, nil
}

// MintOperatingKey mints the daemon's own long-lived Gemini API key --
// the credential pkg/agent/antigravity runs as, distinct from the per-task
// keys Materialize mints -- authenticating with the credential named
// credentialName, the same standing minter credential the capability
// itself uses.
//
// This exists so a deployment that already grants its minter
// roles/serviceusage.apiKeysAdmin (terraform/gcp's enable_gemini_key)
// does not also need a Gemini key supplied by hand before its daemon
// will start. It returns the key's resource name alongside the secret so
// a caller can report what it created; the key is restricted to
// DefaultAPITargetService exactly like every other key minted here.
//
// It is deliberately not a Capability method: nothing about it is
// per-task, it holds no Lease, and DeleteExpired exempts what it mints
// -- see OperatingKeyDisplayName.
func MintOperatingKey(ctx context.Context, credentials model.CredentialResolver, credentialName, projectID string) (name, keyString string, err error) {
	material, err := credentials.Resolve(ctx, credentialName)
	if err != nil {
		return "", "", fmt.Errorf("geminikey: resolving credential %q: %w", credentialName, err)
	}
	m, err := newAPIKeysMinter(ctx, material, projectID)
	if err != nil {
		return "", "", err
	}
	return mintOperatingKey(ctx, m, credentialName)
}

// mintOperatingKey names the credential in a refused-credential failure
// for a reader this package's other paths do not have: scripts/setup.sh's
// mint_gemini_operating_key runs this during a deploy, so a minter key
// the deploy has itself just rotated away turns up as one line in a
// deploy log -- where "invalid_grant" alone reads like the deploy broke,
// rather than like the secret on the host is a key GCP no longer holds
// the other half of.
func mintOperatingKey(ctx context.Context, m minter, credential string) (name, keyString string, err error) {
	name, keyString, err = m.CreateKey(ctx, OperatingKeyDisplayName, DefaultAPITargetService)
	if err != nil {
		return "", "", fmt.Errorf("geminikey: minting the daemon's operating key: %w",
			explainRefusedCredential(err, credential))
	}
	return name, keyString, nil
}

// DeleteExpired preserves the operator-facing fixed-age sweep: it deletes
// Grain-owned task keys older than maxAge. The daemon does not call this
// helper; it reaches deleteBefore through Reap with the shared live-work
// boundary. Per-key failures are returned alongside successful deletions.
func DeleteExpired(ctx context.Context, credentials model.CredentialResolver, credentialName, projectID string, now time.Time, maxAge time.Duration) ([]string, error) {
	c := New(projectID, model.CredentialRef{Name: credentialName})
	m, err := c.minterFor(ctx, credentials)
	if err != nil {
		return nil, err
	}
	return deleteExpired(ctx, m, now, maxAge, credentialName)
}

// deleteExpired is given the credential's name only to say it in the one
// error it can fail with outright: a listing refused because GCP will
// not issue a token for the minter at all leaves every leaked key in the
// project uncollected, hourly, and says so in a log line naming no
// secret unless this does -- see explainRefusedCredential.
func deleteExpired(ctx context.Context, m minter, now time.Time, maxAge time.Duration, credential string) ([]string, error) {
	return deleteBefore(ctx, m, now.Add(-maxAge), credential)
}

// deleteBefore removes Grain-owned task keys created before the caller's
// shared trailing edge. It reports per-key failures without stopping later
// deletions, so an incomplete sweep is observable.
func deleteBefore(ctx context.Context, m minter, before time.Time, credential string) ([]string, error) {
	keys, err := m.ListKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("geminikey: listing keys: %w", explainRefusedCredential(err, credential))
	}
	var deleted []string
	var deleteErrs []error
	for _, k := range keys {
		if !strings.HasPrefix(k.DisplayName, displayNamePrefix) {
			continue
		}
		// The daemon's own operating key is grain's, and old by
		// design -- it is the credential this process runs as, not a
		// per-task lease that leaked. Reaping it on age would stop the
		// daemon a day after every deploy.
		if k.DisplayName == OperatingKeyDisplayName {
			continue
		}
		if !k.CreateTime.Before(before) {
			continue
		}
		if err := m.DeleteKey(ctx, k.Name); err != nil {
			deleteErrs = append(deleteErrs, fmt.Errorf("deleting key %s: %w", k.Name, err))
			continue
		}
		deleted = append(deleted, k.Name)
	}
	return deleted, errors.Join(deleteErrs...)
}

// --- the real minter, against the live API Keys API ------------------

// pollInterval paces polling a Create/Delete operation to completion.
// Not configurable by an operator: it trades a handful of extra round
// trips for one fewer knob, and every caller already governs the overall
// wait through ctx. A var rather than a const only so this package's own
// tests can drive await's polling in milliseconds instead of seconds.
var pollInterval = 2 * time.Second

type apiKeysMinter struct {
	svc       *apikeys.Service
	projectID string
}

// newAPIKeysMinter is the real minterFactory: credentialJSON is a
// standing service account's raw key file, exactly what
// model.CredentialResolver.Resolve returns for a GCP credential name.
//
// Unlike grain/automation/gemini_keys.py, which shells out to gcloud
// because this project is otherwise stdlib-only Python, this calls the
// API Keys API directly through its Go client library -- the "the GCP Go
// SDK would retire the gcloud exception" README.md already names as
// one of the things a Go port corrects. That also sidesteps the CLI
// quirks gemini_keys.py's docstring documents at length (create prints
// an operation id, not a key id, on some gcloud versions): the client
// library's Create returns a proper long-running Operation to poll, with
// a typed Response to unmarshal once it is done, rather than text this
// project would otherwise have to parse defensively.
func newAPIKeysMinter(ctx context.Context, credentialJSON, projectID string) (minter, error) {
	creds, err := google.CredentialsFromJSON(ctx, []byte(credentialJSON), apikeys.CloudPlatformScope)
	if err != nil {
		return nil, fmt.Errorf("geminikey: parsing credential material: %w", err)
	}
	svc, err := apikeys.NewService(ctx, option.WithCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("geminikey: building an API Keys client: %w", err)
	}
	return &apiKeysMinter{svc: svc, projectID: projectID}, nil
}

func (a *apiKeysMinter) parent() string {
	return "projects/" + a.projectID + "/locations/global"
}

func (a *apiKeysMinter) CreateKey(ctx context.Context, displayName, apiTargetService string) (name, keyString string, err error) {
	key := &apikeys.V2Key{DisplayName: displayName}
	if apiTargetService != "" {
		key.Restrictions = &apikeys.V2Restrictions{
			ApiTargets: []*apikeys.V2ApiTarget{{Service: apiTargetService}},
		}
	}
	op, err := a.svc.Projects.Locations.Keys.Create(a.parent(), key).Context(ctx).Do()
	if err != nil {
		return "", "", fmt.Errorf("creating key: %w", advise(a.projectID, err))
	}
	op, err = a.await(ctx, op)
	if err != nil {
		return "", "", advise(a.projectID, err)
	}

	var created apikeys.V2Key
	if err := json.Unmarshal(op.Response, &created); err != nil {
		return "", "", fmt.Errorf("parsing the created key out of the operation response: %w", err)
	}
	if created.Name == "" {
		return "", "", fmt.Errorf("created key carries no resource name: %s", op.Response)
	}

	// Past this point a key exists in the project whether or not the
	// rest of this call succeeds -- a caller only gets a name to revoke
	// later if CreateKey returns, so a failure reading the key string
	// back must clean up here rather than strand it, the same
	// bwsalmon/agents#104 lesson gemini_keys.py's own create_key learned.
	got, err := a.svc.Projects.Locations.Keys.GetKeyString(created.Name).Context(ctx).Do()
	if err != nil {
		_ = a.DeleteKey(ctx, created.Name)
		return "", "", fmt.Errorf("reading back the key string: %w", advise(a.projectID, err))
	}
	// An empty key string is the one failure here that looks like
	// success: it would be placed at KeyPath, described to the agent as a
	// working key by PromptSection, and only fail much later as an
	// authentication error from Gemini with nothing in it naming grain.
	// Treated as a failed mint, and cleaned up the same way an unreadable
	// key is, for the same bwsalmon/agents#104 reason.
	if got.KeyString == "" {
		_ = a.DeleteKey(ctx, created.Name)
		return "", "", fmt.Errorf("key %s was created but its key string came back empty", created.Name)
	}
	return created.Name, got.KeyString, nil
}

func (a *apiKeysMinter) await(ctx context.Context, op *apikeys.Operation) (*apikeys.Operation, error) {
	for !op.Done {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
		next, err := a.svc.Operations.Get(op.Name).Context(ctx).Do()
		if err != nil {
			return nil, fmt.Errorf("polling operation %s: %w", op.Name, err)
		}
		op = next
	}
	if op.Error != nil {
		return nil, fmt.Errorf("operation %s failed: %s", op.Name, op.Error.Message)
	}
	return op, nil
}

// DeleteKey waits for the deletion to actually finish, not just be
// accepted -- this is what makes a key gone in a caller's own next
// ListKeys, not merely queued to become so. Idempotent: a key already
// gone (deleted by a previous call, or by DeleteExpired's reap racing
// this one) reports success rather than an error, the same tolerance
// docs/data-model.md asks of every provider's Revoke.
func (a *apiKeysMinter) DeleteKey(ctx context.Context, name string) error {
	op, err := a.svc.Projects.Locations.Keys.Delete(name).Context(ctx).Do()
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("deleting key %s: %w", name, err)
	}
	if _, err := a.await(ctx, op); err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("deleting key %s: %w", name, err)
	}
	return nil
}

func (a *apiKeysMinter) ListKeys(ctx context.Context) ([]mintedKey, error) {
	var out []mintedKey
	err := a.svc.Projects.Locations.Keys.List(a.parent()).Pages(ctx, func(resp *apikeys.V2ListKeysResponse) error {
		for _, k := range resp.Keys {
			if k.DeleteTime != "" {
				continue
			}
			// A key with no parseable createTime is left out of the
			// listing entirely, rather than defaulting to a zero time
			// deleteExpired would then treat as ancient -- "absent data
			// loses, doesn't crash".
			created, err := time.Parse(time.RFC3339, k.CreateTime)
			if err != nil {
				continue
			}
			out = append(out, mintedKey{Name: k.Name, DisplayName: k.DisplayName, CreateTime: created})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func isNotFound(err error) bool {
	var apiErr *googleapi.Error
	return errors.As(err, &apiErr) && apiErr.Code == 404
}

// advise annotates the two ways a mint fails on a project nobody
// finished setting up, with the sentence that says what to do about it.
//
// Both arrive as an indistinguishable HTTP 403 from the API Keys API,
// and both are easy to reach: `grain setup gcp` defaults
// -enable-gemini-key to *off*, while terraform/gcp's own
// enable_gemini_key defaults to *on*, so a deployment installed by
// script rather than by that module has a GCP project, a minter
// credential, a "Ready" badge on Settings' Capabilities tab -- and a
// minter holding no roles/serviceusage.apiKeysAdmin and a project with
// apikeys.googleapis.com never enabled. The raw error for that is
// "googleapi: Error 403: Permission 'apikeys.keys.create' denied on
// resource ... (or it may not exist)", which reads like a bug in grain
// rather than like one unrun setup flag.
//
// The distinction between the two is drawn from the error's own
// SERVICE_DISABLED reason (an ErrorInfo detail Google returns for every
// call against an API a project has not enabled), falling back to the
// message text, since that reason is what separates "enable the API"
// from "grant the role" -- and the remedy names both anyway, because the
// one command that fixes either fixes both.
//
// Anything that is not a 403 is returned unchanged: a 404, a quota
// error, a transport failure all already say what they are, and wrapping
// every failure in advice about IAM would make the one case this is for
// harder to spot rather than easier.
func advise(projectID string, err error) error {
	var apiErr *googleapi.Error
	if !errors.As(err, &apiErr) || apiErr.Code != http.StatusForbidden {
		return err
	}
	remedy := fmt.Sprintf("run `grain setup gcp -project %s -enable-gemini-key`, "+
		"which grants the minter roles/serviceusage.apiKeysAdmin and enables both "+
		"apikeys.googleapis.com and %s", projectID, DefaultAPITargetService)
	if isServiceDisabled(apiErr) {
		return fmt.Errorf("the API Keys API is not enabled in project %s -- %s: %w", projectID, remedy, err)
	}
	return fmt.Errorf("the minter credential is not permitted to administer API keys in "+
		"project %s (it needs roles/serviceusage.apiKeysAdmin) -- %s: %w", projectID, remedy, err)
}

// isCredentialRefused reports whether err is Google's *token* endpoint
// refusing the standing credential this client was built with, rather
// than the API Keys API refusing a call it made. It is unlike every
// other failure this package classifies: it happens before any request
// reaches apikeys.googleapis.com, so it carries no googleapi.Error, no
// status code errors.As can read and no resource name at all -- only the
// token endpoint's own JSON body, quoted inside an oauth2 transport
// error:
//
//	Post "https://apikeys.googleapis.com/v2/projects/.../keys?alt=json":
//	auth: cannot fetch token: 400 Response:
//	{"error":"invalid_grant","error_description":"Invalid JWT Signature."}
//
// So the only thing to match on is that body. `invalid_grant` is
// conclusive on its own -- it is the OAuth error for "this grant is not
// one I will exchange", which for a service-account JWT means the key
// signing it is not one Google holds the public half of any more -- and
// the signature description is matched too because it is the spelling a
// deleted or rotated key produces, which is the common case by a wide
// margin.
//
// Restated from gcpkey.isCredentialRefused rather than shared, for the
// same reason isServiceDisabled below is: the two packages have no
// dependency on each other, and one small predicate is a smaller thing
// to repeat than a package to introduce for it. It is deliberately not
// folded into advise, which classifies a googleapi.Error a call came
// back with and would have to grow a second, unrelated shape of error to
// take this one.
func isCredentialRefused(err error) bool {
	if err == nil {
		return false
	}
	detail := err.Error()
	return strings.Contains(detail, "invalid_grant") ||
		strings.Contains(detail, "Invalid JWT Signature")
}

// isServiceDisabled reports whether a 403 is Google's "this API has
// never been enabled in this project" rather than an IAM refusal. The
// machine-readable form is an ErrorInfo detail with reason
// SERVICE_DISABLED; googleapi.Error keeps the raw body, so this looks
// there rather than reaching for a typed detail the generated client
// does not expose.
func isServiceDisabled(apiErr *googleapi.Error) bool {
	return strings.Contains(apiErr.Body, "SERVICE_DISABLED") ||
		strings.Contains(apiErr.Message, "has not been used in project")
}
