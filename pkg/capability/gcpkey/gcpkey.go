// Package gcpkey is the "gcp-key" capability: a MINT provider
// (docs/data-model.md, model/capability.go) that mints a short-lived
// service-account key for a narrow "agent" service account and pushes it
// into the sandbox, mirroring grain/automation/gcp_keys.py's shape --
// bwsalmon/agents#126, ported to v2's declarative provider contract rather
// than v1's Runner-shelling-to-gcloud one.
//
// **A real key, not an impersonated token**, for the same reason
// gcp_keys.py gives: bwsalmon/agents#126 asks for exactly this tradeoff,
// mirroring gemini_keys.py's own "mint one per task, push the raw secret
// into the sandbox, revoke at end of session" shape. See that module's
// docstring for the fuller argument; nothing about the tradeoff changes
// here, only the mechanism.
//
// **Authentication: a minter credential, resolved fresh for every call,
// never the agent account's own.** Minting a *new* agent-account key has to
// be done by some identity other than the agent account itself -- a
// sandbox holding a leaked agent key must not be able to mint itself a
// fresh one and defeat the whole "expires" premise this package exists
// for. Provider.Config.MinterCredential names a credential
// (CapabilityContext.Credentials resolves it) that GCP's IAM grants
// roles/iam.serviceAccountKeyAdmin on the agent account and nothing wider
// -- gcp_keys.py's separate gcp-key-minter.json, never
// gcp-service-account.json, for the same reason. A fresh Minter is built
// from that material on every Materialize/Revoke/Reap rather than cached,
// the same reason gcp_keys.py's _activate runs before every gcloud
// invocation: two credentials authenticating as two different accounts
// must never leave which one "wins" to ambient global state.
//
// **Expiry is enforced by grain, not GCP**, for the same reason
// gcp_keys.py's own docstring gives: user-managed IAM service-account keys
// carry no native TTL. Two independent mechanisms enforce it here.
// Materialize sets Lease.ExpiresAt and CapabilitySpec.MaxLease from
// Config.MaxKeyAge for recorded leases. Reap is the orphan safety net:
// it uses the daemon's shared live-work trailing edge, independent of
// whether any record of a particular key survived.
//
// **Directly against the IAM API, not gcloud.** README.md already notes
// "the GCP Go SDK would retire the gcloud exception" as one of the two
// package-list wins Go buys over the Python controller; this package is
// that retirement for one capability. iam.go builds an
// *iam.Service straight from resolved credential material
// (google.golang.org/api/iam/v1), with no subprocess, no temp file on a
// controller's own disk, and no dependence on a particular gcloud version's
// stdout shape -- gcp_keys.py's create_key carries a long comment on
// exactly that last failure mode (bwsalmon/agents#140).
package gcpkey

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// SandboxKeyPath is where the minted key lands in the sandbox --
// the path v1's system diagram fixed for the sandbox GCP key, restated
// here rather than imported since there is no sandbox-facing config
// package yet for either side to share.
//
// It is the deployment `gcp-key`'s path only. A named key (`gcp-key:<name>`)
// lands at KeyPath(name) instead, so that a task or CI job granted more
// than one key gets every one of them: they all used to land
// here, and only whichever was placed last survived, with nothing telling
// the agent which account that was.
const SandboxKeyPath = "/home/debian/.gcp-service-account.json"

// KeyPath is where the key for the GCP key capability named name lands in
// the sandbox: SandboxKeyPath for the unnamed (or "default") key, and
// /home/debian/.gcp-service-account-<name>.json for any other, so no two
// keys a task holds at once share a file.
func KeyPath(name string) string {
	if name == "" || name == "default" {
		return SandboxKeyPath
	}
	return "/home/debian/.gcp-service-account-" + name + ".json"
}

// DefaultMaxKeyAge is Config.MaxKeyAge's default, and
// grain/automation/gcp_keys.DEFAULT_MAX_KEY_AGE_HOURS restated in Go --
// bwsalmon/agents#126's own "these keys should have an expiry of 24 hours".
const DefaultMaxKeyAge = 24 * time.Hour

// DefaultMinterCredential is Config.MinterCredential's default: the name a
// CapabilityContext.Credentials resolver is expected to answer with the
// host service-account key gcp_keys.py calls the minter -- deliberately not
// the same name a deployment's general host identity might use elsewhere,
// for the separation this package's doc comment explains.
const DefaultMinterCredential = "gcp-key-minter"

// KeyInfo is one key as GCP itself reports it -- what Reap needs and
// nothing more.
type KeyInfo struct {
	ID        string
	CreatedAt time.Time
}

// Minter mints, deletes, and lists user-managed keys for one GCP service
// account, authenticated as some other identity permitted to administer
// them. account is always a fully-qualified resource name,
// "projects/{project}/serviceAccounts/{email}" -- callers never assemble a
// per-key name themselves, since Delete's shape
// ("{account}/keys/{id}") is Minter's own implementation detail.
//
// A provider is handed no cached Minter, only a constructor
// (Provider.NewMinter) called fresh with resolved credential material for
// each Materialize/Revoke/Reap -- see this package's doc comment for why.
type Minter interface {
	CreateKey(ctx context.Context, account string) (id, keyJSON string, err error)
	DeleteKey(ctx context.Context, account, keyID string) error
	ListKeys(ctx context.Context, account string) ([]KeyInfo, error)
}

// Config is this deployment's gcp-key settings -- the same
// operator-set-tunables role grain/automation/gcp_keys.GcpKeyConfig plays
// in v1, held on the Provider itself rather than threaded through
// CapabilityContext, since v2's CapabilityContext (model/capability.go)
// carries nothing capability-specific.
type Config struct {
	// Name is the custom identifier for this GCP key capability (e.g. "deploy").
	// Empty means the default "gcp-key" capability.
	Name string
	// ServiceAccountEmail is the narrow "agent" account keys are minted
	// for -- terraform/gcp/iam.tf's google_service_account.agent.
	// When ServiceAccountCount > 1, this acts as the base email template
	// formatted as <prefix>-<index>@<domain> (e.g. grain-agent-0@proj.iam.gserviceaccount.com).
	ServiceAccountEmail string
	// ServiceAccountCount is the number of agent service accounts to use.
	// 0 or 1 uses a single service account (ServiceAccountEmail).
	// A value > 1 generates accounts with a fixed 0-indexed numbering scheme:
	// <prefix>-0@<domain>, <prefix>-1@<domain>, ..., <prefix>-<N-1>@<domain>.
	ServiceAccountCount int
	// ProjectID is the GCP project ServiceAccountEmail lives in.
	ProjectID string
	// MaxKeyAge is the recorded lease's expiry backstop. Reap instead
	// receives the daemon's shared live-work trailing edge. Zero means
	// DefaultMaxKeyAge, never "no expiry".
	MaxKeyAge time.Duration
	// MinterCredential is resolved through CapabilityContext.Credentials
	// (Materialize, PromptSection is uninvolved) to the host
	// service-account key permitted to mint/delete ServiceAccountEmail's
	// own keys. Empty means DefaultMinterCredential (or "gcp-key-minter-" + Name if Name != "").
	MinterCredential string
}

// Provider is the gcp-key capability. The zero value is not usable --
// construct one with NewProvider, which fills in NewMinter.
type Provider struct {
	model.BaseCapability
	Config Config
	// NewMinter builds a Minter authenticated with credentialJSON, the
	// material CapabilityContext.Credentials.Resolve returned for
	// Config.MinterCredential (or a Lease's own MintedBy, for Revoke).
	// NewProvider sets this to NewIAMMinter; tests substitute a fake with
	// no network and no real GCP project involved, the same "no sandbox
	// and no cloud" bar model/capability_test.go's mintCapability holds
	// to.
	NewMinter func(ctx context.Context, credentialJSON string) (Minter, error)

	nextAccount uint32
}

func (*Provider) ScarceResourceProvider() {}

// NewProvider builds a Provider wired to the real IAM API.
func NewProvider(cfg Config) *Provider {
	return &Provider{Config: cfg, NewMinter: NewIAMMinter}
}

func (p *Provider) maxKeyAge() time.Duration {
	if p.Config.MaxKeyAge > 0 {
		return p.Config.MaxKeyAge
	}
	return DefaultMaxKeyAge
}

func (p *Provider) minterCredential() string {
	if p.Config.MinterCredential != "" {
		return p.Config.MinterCredential
	}
	if p.Config.Name != "" && p.Config.Name != "default" {
		return "gcp-key-minter-" + p.Config.Name
	}
	return DefaultMinterCredential
}

func (p *Provider) capabilityName() string {
	return model.GCPKeyCapability(p.Config.Name)
}

func (p *Provider) label() string {
	if p.Config.Name == "" || p.Config.Name == "default" {
		return "grain-gcp-key"
	}
	return "grain-gcp-key-" + p.Config.Name
}

func (p *Provider) description() string {
	if p.Config.Name == "" || p.Config.Name == "default" {
		return "Mint a short-lived GCP service-account key for this task"
	}
	return fmt.Sprintf("Mint a short-lived GCP service-account key (%s) for this task", p.Config.Name)
}

func (p *Provider) accounts() []string {
	if p.Config.ServiceAccountEmail == "" || p.Config.ProjectID == "" {
		return nil
	}
	if p.Config.ServiceAccountCount <= 1 {
		return []string{accountName(p.Config.ProjectID, p.Config.ServiceAccountEmail)}
	}
	emails := p.accountEmails()
	accounts := make([]string, len(emails))
	for i, email := range emails {
		accounts[i] = accountName(p.Config.ProjectID, email)
	}
	return accounts
}

func (p *Provider) accountEmails() []string {
	return PoolEmails(p.Config.ServiceAccountEmail, p.Config.ServiceAccountCount, p.Config.ProjectID)
}

// PoolEmails is the accounts a fixed pool of count agent accounts named
// after email holds (Config.ServiceAccountCount): email itself when count
// is 0 or 1, and <prefix>-0 .. <prefix>-<count-1> otherwise.
func PoolEmails(email string, count int, projectID string) []string {
	if email == "" {
		return nil
	}
	if count <= 1 {
		return []string{email}
	}
	prefix, domain, hasAt := strings.Cut(email, "@")
	if !hasAt && projectID != "" {
		domain = fmt.Sprintf("%s.iam.gserviceaccount.com", projectID)
	}
	prefix = strings.TrimSuffix(prefix, "-0")
	prefix = strings.TrimRight(prefix, "-")
	emails := make([]string, count)
	for i := 0; i < count; i++ {
		if domain != "" {
			emails[i] = fmt.Sprintf("%s-%d@%s", prefix, i, domain)
		} else {
			emails[i] = fmt.Sprintf("%s-%d", prefix, i)
		}
	}
	return emails
}

func accountName(projectID, email string) string {
	return fmt.Sprintf("projects/%s/serviceAccounts/%s", projectID, email)
}

func (p *Provider) newMinter(ctx context.Context, credentialJSON string) (Minter, error) {
	build := p.NewMinter
	if build == nil {
		build = NewIAMMinter
	}
	minter, err := build(ctx, credentialJSON)
	if err != nil {
		return nil, fmt.Errorf("gcpkey: authenticating as the minter: %w", err)
	}
	return minter, nil
}

func (p *Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:        p.capabilityName(),
		Label:       p.label(),
		Description: p.description(),
		Source:      model.GrantByLabel,
		Provision:   model.ProvisionMint,
		MaxLease:    p.maxKeyAge(),
		// The minter credential is resolved fresh on every
		// Materialize/Revoke/Reap (this package's doc comment) --
		// never ServiceAccountEmail/ProjectID, which are deployment
		// config, not secret material.
		Requires: []string{p.minterCredential()},
	}
}

// Resolve refuses unless this deployment is actually configured for
// gcp-key: ServiceAccountEmail and ProjectID both come from an operator,
// never a default, since a mistaken default here would mint against
// whichever project a stray empty string happened to resolve to -- and
// the minter credential those two are minted through has to actually
// resolve to something.
//
// Every reason is a sentence naming the pane and the command that fixes
// it, the "posted verbatim, so it should read as a sentence a human can
// act on" bar Resolution.Reason sets. The three it replaced each failed
// that bar in its own way: the configuration one named `grain controller
// configure --gcp-agent-service-account-email`, flags no build of grain
// has ever had (the real ones are `grain settings`', and the same pair
// of fields sits on Settings -> Capabilities), and opened by telling a
// human their *issue* carries a label, from a version of grain with
// neither issues nor labels left in it. The missing-credential one
// existed at all only as Materialize's own wrapped error a moment later
// -- "materializing capabilities: gcpkey: resolving minter credential
// ...", grain describing its own internals where a task's own comment
// should have said which secret to paste where. Nothing new is caught
// here: what changes is where an operator reads about it.
//
// Checking the credential means resolving it, which is why this asks for
// nothing back: Resolve wants to know the name answers, and a
// CredentialResolver that returns material to a caller with no use for it
// is a copy of a secret made for no reason.
func (p *Provider) Resolve(ctx context.Context, cc model.CapabilityContext) (model.Resolution, error) {
	if p.Config.ServiceAccountEmail == "" || p.Config.ProjectID == "" {
		return model.RefusedBecause(
			"this task asks for a GCP service-account key this deployment isn't " +
				"configured to mint. An operator sets the GCP project and the agent " +
				"service account keys are minted for, under Settings -> Capabilities " +
				"(`grain settings -gcp-project <project> -gcp-agent-service-account " +
				"<email>`).",
		), nil
	}
	credential := p.minterCredential()
	if cc.Credentials == nil {
		return model.RefusedBecause(
			"this task asks for a GCP service-account key, but nothing here can reach " +
				"the standing credential `" + credential + "` the key would be minted " +
				"under.",
		), nil
	}
	if _, err := cc.Credentials.Resolve(ctx, credential); err != nil {
		return model.RefusedBecause(
			"this task asks for a GCP service-account key, but the standing credential `" +
				credential + "` it is minted under is not set on this deployment. An " +
				"operator pastes the GCP minter service account's key file into Settings " +
				"-> Secrets, or runs `grain secrets set " + credential + " key.json " +
				"-value-file <path>`.",
		), nil
	}
	return model.Honoured(), nil
}

// explainRefusedCredential names the one failure that is about the
// minter credential itself rather than about the account being minted
// for: GCP refusing to issue a token for it at all, so that nothing
// this package does ever reaches the IAM API (isCredentialRefused, and
// see it for the shape that error arrives in).
//
// Task 163 is that failure, and what an operator got was Google's own
// `invalid_grant` / "Invalid JWT Signature" -- a sentence naming no
// credential, no secret, no service account and nothing about grain, on
// a deployment whose Settings pane reads **Ready** because a project,
// an account and a secret are all set. Nothing on any configuration
// pane can see that the key inside that secret has stopped working:
// only GCP knows, and only when something authenticates with it.
//
// Every cause comes down to "the key in that secret is not one GCP will
// accept any more", so the sentence names the ways that happens --
// including the one this deployment shape actually hits, a deployer
// that rotates the minter key underneath a host that had only ever
// seeded its own copy once -- and the two places a current key is
// pasted. Wrapped, never replaced: GCP's own words survive, the same
// rule explainCreateFailure holds to.
//
// The credential's *name* is the reason this lives here rather than
// beside iam.go's other explanations: a Minter is handed material and
// never told which secret it came out of, while Provider is the only
// thing that knows both (and, for Revoke, that the lease may name a
// different one than Config does).
func explainRefusedCredential(err error, credential string) error {
	if !isCredentialRefused(err) {
		return err
	}
	return fmt.Errorf(
		"GCP will not issue a token for the minter credential held in the `%s` secret, so "+
			"nothing here reached the IAM API at all: the service-account key that secret "+
			"holds has been deleted or rotated away in GCP (terraform/gcp's push-secrets.sh "+
			"mints a fresh minter key and invalidates older ones on every run), or the "+
			"account it belongs to is gone or disabled, or this host's clock is too far out "+
			"for Google to accept a request signed by it. Paste a current key file into "+
			"Settings -> Capabilities, or run `grain secrets set %s key.json -value-file "+
			"<path>` on the host: %w",
		credential, credential, err)
}

// CheckCredential implements model.CredentialChecker: it authenticates
// as the minter credential and lists the agent account's own keys, which
// is the cheapest call this package makes that proves GCP still accepts
// that credential.
//
// ListKeys rather than anything else because Reap already makes exactly
// this call hourly, it needs no permission a mint does not already need
// (roles/iam.serviceAccountKeyAdmin covers both), and it changes
// nothing: a check an operator is expected to press should not leave a
// key behind that something else then has to reap.
//
// This is the answer to the failure "Debugging `gcp-key` again"
// (README.md) took a whole debugging session to reach: a deployment
// whose Settings pane read **Ready** the entire time -- a project, an
// agent account and a `gcp-key-minter` secret are all set -- while every
// mint failed with `invalid_grant`, because the key inside that secret
// had been rotated away in GCP. Presence is all a configuration pane can
// see; this is the one thing that can see validity, and it says so in
// explainRefusedCredential's own words, naming the secret to replace.
func (p *Provider) CheckCredential(ctx context.Context, creds model.CredentialResolver) (model.CredentialCheck, error) {
	credential := p.minterCredential()
	check := model.CredentialCheck{Credentials: []string{credential}}
	if p.Config.ProjectID == "" || p.Config.ServiceAccountEmail == "" {
		return check, fmt.Errorf(
			"gcpkey: this deployment has no GCP project and agent service account set, so " +
				"there is no account to check the minter credential against (Settings -> " +
				"Capabilities, or `grain settings -gcp-project <project> " +
				"-gcp-agent-service-account <email>`)")
	}
	if creds == nil {
		return check, fmt.Errorf("gcpkey: no credential resolver to resolve %q with", credential)
	}
	minterKey, err := creds.Resolve(ctx, credential)
	if err != nil {
		return check, fmt.Errorf("gcpkey: resolving minter credential %q: %w", credential, err)
	}
	minter, err := p.newMinter(ctx, minterKey)
	if err != nil {
		return check, err
	}
	accounts := p.accounts()
	totalKeys := 0
	for _, account := range accounts {
		keys, err := minter.ListKeys(ctx, account)
		if err != nil {
			return check, explainRefusedCredential(err, credential)
		}
		totalKeys += len(keys)
	}
	if len(accounts) <= 1 {
		check.Detail = fmt.Sprintf(
			"GCP accepted the key held in `%s` and listed %d user-managed key(s) on %s.",
			credential, totalKeys, p.Config.ServiceAccountEmail)
	} else {
		check.Detail = fmt.Sprintf(
			"GCP accepted the key held in `%s` and listed %d user-managed key(s) across %d service accounts (%s).",
			credential, totalKeys, len(accounts), p.Config.ServiceAccountEmail)
	}
	return check, nil
}

func (p *Provider) Materialize(ctx context.Context, cc model.CapabilityContext) (model.Materialization, error) {
	credential := p.minterCredential()
	minterKey, err := cc.Credentials.Resolve(ctx, credential)
	if err != nil {
		return model.Materialization{}, fmt.Errorf("gcpkey: resolving minter credential %q: %w", credential, err)
	}
	minter, err := p.newMinter(ctx, minterKey)
	if err != nil {
		return model.Materialization{}, err
	}
	accounts := p.accounts()
	if len(accounts) == 0 {
		return model.Materialization{}, fmt.Errorf("gcpkey: no agent service accounts configured")
	}
	// The name becomes part of a file name. model.GCPKey.Validate
	// already refuses separators; this is the backstop for a key placed
	// anywhere but its own file in the sandbox's home.
	if strings.ContainsAny(p.Config.Name, "/\\ \t\n\r") {
		return model.Materialization{}, fmt.Errorf("gcpkey: key name %q cannot name a file", p.Config.Name)
	}

	start := int(atomic.AddUint32(&p.nextAccount, 1)-1) % len(accounts)
	var lastExhausted *model.ResourceExhaustedError

	for i := 0; i < len(accounts); i++ {
		account := accounts[(start+i)%len(accounts)]
		id, keyJSON, err := minter.CreateKey(ctx, account)
		if err == nil {
			expires := cc.Now.Add(p.maxKeyAge())
			return model.Materialization{
				Lease: &model.Lease{
					Capability: p.capabilityName(),
					Resource:   id,
					MintedBy:   model.CredentialRef{Name: credential},
					IssuedAt:   cc.Now,
					ExpiresAt:  &expires,
				},
				Placements: []model.Placement{
					{Side: model.SideSandbox, Path: KeyPath(p.Config.Name), Content: keyJSON},
				},
			}, nil
		}
		// Rate limited: the quota is the project's, not this
		// account's, so the next account would only be refused too.
		// Waited out, not failed (ratelimit.go).
		if isRateLimited(err) {
			return model.Materialization{}, rateLimitedMint(account, err)
		}
		if exh, ok := model.ResourceExhausted(err); ok {
			lastExhausted = exh
			continue
		}
		return model.Materialization{}, fmt.Errorf("gcpkey: minting a key for %s: %w",
			account, explainRefusedCredential(err, credential))
	}

	if lastExhausted != nil {
		return model.Materialization{}, lastExhausted
	}
	return model.Materialization{}, fmt.Errorf("gcpkey: all service accounts exhausted")
}

// PromptSection names the key's path *and* Config.ProjectID, because
// neither line it prints selects a project on its own: activating a key
// (or pointing GOOGLE_APPLICATION_CREDENTIALS at one) authenticates the
// caller and nothing more. Without the project set, the very first
// gcloud command a task runs fails with "The required property [project]
// is not currently set" -- a message that names no credential at all and
// reads, to an agent that has just been handed a key, like the key is
// the thing that is broken. Config.ProjectID is deployment config rather
// than secret material (Spec's own comment says so), so stating it here
// costs nothing.
//
// A named key says which capability it is, since a task holding more than
// one gets a section for each and has to tell the files apart.
func (p *Provider) PromptSection(ctx context.Context, cc model.CapabilityContext, placements []model.Placement) (string, error) {
	if len(placements) != 1 {
		return "", fmt.Errorf("gcpkey: expected exactly one placement, got %d", len(placements))
	}
	path := placements[0].Path
	which := "A GCP service-account key"
	if p.Config.Name != "" && p.Config.Name != "default" {
		which = fmt.Sprintf("The GCP service-account key for `%s` (project %s)",
			p.capabilityName(), p.Config.ProjectID)
	}
	return fmt.Sprintf(
		"%s is at %s, readable only by you:\n\n"+
			"    export GOOGLE_APPLICATION_CREDENTIALS=%q\n\n"+
			"or, for the gcloud CLI:\n\n"+
			"    gcloud auth activate-service-account --key-file=%s\n"+
			"    gcloud config set project %s\n\n"+
			"That second line is not optional: activating a key authenticates "+
			"you but does not select a project, so gcloud without it fails with "+
			"\"The required property [project] is not currently set\" rather "+
			"than anything about the key. Export GOOGLE_CLOUD_PROJECT=%s for "+
			"SDKs reading the file directly.\n",
		which, path, path, path, p.Config.ProjectID, p.Config.ProjectID,
	), nil
}

// Revoke deletes the key Materialize minted, authenticating with whatever
// credential the Lease itself names (Lease.MintedBy) rather than
// Config.MinterCredential's current value -- Lease.MintedBy's own doc
// comment is exactly this: "releasing asks the lease what to call", so a
// credential rotated between mint and revoke does not strand the key this
// lease is naming.
func (p *Provider) Revoke(ctx context.Context, cc model.CapabilityContext, lease model.Lease) error {
	minterKey, err := cc.Credentials.Resolve(ctx, lease.MintedBy.Name)
	if err != nil {
		return fmt.Errorf("gcpkey: resolving minter credential %q: %w", lease.MintedBy.Name, err)
	}
	minter, err := p.newMinter(ctx, minterKey)
	if err != nil {
		return err
	}
	accounts := p.accounts()
	if len(accounts) == 0 {
		return nil
	}
	var lastErr error
	for _, account := range accounts {
		err := deleteKeyRetrying(ctx, minter, account, lease.Resource)
		if err == nil {
			return nil
		}
		lastErr = err
		// Still rate limited after the retries: the quota is the
		// project's, so asking the next account would be one more
		// refused write against it. Reap deletes the key later.
		if isRateLimited(err) {
			break
		}
	}
	return fmt.Errorf("gcpkey: revoking key %s: %w",
		lease.Resource, explainRefusedCredential(lastErr, lease.MintedBy.Name))
}

// Reap implements model.Reaper: it deletes user-managed keys created
// before the caller's shared trailing edge, independent of any Lease grain
// may or may not still have a record of. The daemon derives before from the
// oldest live agent or CI run and applies its grace period once for every
// capability.
//
// A key with a zero CreatedAt is left alone rather than guessed at. Deletion
// remains best-effort across keys, but failures are returned alongside the
// identifiers successfully deleted so the daemon cannot report a clean sweep
// that actually removed nothing.
func (p *Provider) Reap(ctx context.Context, creds model.CredentialResolver, before time.Time) ([]string, error) {
	credential := p.minterCredential()
	minterKey, err := creds.Resolve(ctx, credential)
	if err != nil {
		return nil, fmt.Errorf("gcpkey: resolving minter credential %q: %w", credential, err)
	}
	minter, err := p.newMinter(ctx, minterKey)
	if err != nil {
		return nil, err
	}
	accounts := p.accounts()
	var deleted []string
	var deleteErrs []error
	for _, account := range accounts {
		keys, err := minter.ListKeys(ctx, account)
		if err != nil {
			return nil, explainRefusedCredential(err, credential)
		}
		for _, key := range keys {
			if key.CreatedAt.IsZero() || !key.CreatedAt.Before(before) {
				continue
			}
			if err := minter.DeleteKey(ctx, account, key.ID); err != nil {
				deleteErrs = append(deleteErrs, fmt.Errorf("deleting key %s: %w", key.ID, err))
				continue
			}
			deleted = append(deleted, key.ID)
		}
	}
	return deleted, errors.Join(deleteErrs...)
}
