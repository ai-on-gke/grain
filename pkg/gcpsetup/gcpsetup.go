// Package gcpsetup bootstraps the external GCP infrastructure the
// gcp-key and gemini-key capabilities (pkg/capability/gcpkey,
// pkg/capability/geminikey) need before either can mint anything --
// bwsalmon/agents#358.
//
// terraform/gcp/iam.tf builds the equivalent for v1's controller/sandbox
// cluster; this package is the same set of resources (two service
// accounts, one IAM grant scoped to the agent account, one project-level
// grant for API-key administration, plus the APIs both capabilities call)
// but reached directly through the GCP Go SDK rather than Terraform, so
// a fresh v2 install (scripts/setup.sh's own GRAIN_GCP_* variables) has
// something to run before it has anything to seed them with.
// README.md's own "Accepted limits" list still says "GCP token minting
// has never run against a real project"; this is what closes that gap
// well enough for a first real run.
//
// Every step is get-or-create / add-binding-if-missing, so running this
// twice against the same project is a no-op the second time -- the same
// "safe to re-run" bar scripts/setup.sh holds itself to, since this is
// meant to be called from both `grain setup gcp` (a new installation)
// and `grain sync` (reconciling an existing one after a config change).
//
// Not every step can be automated: enabling an API or writing an IAM
// policy needs a permission the credential this runs as may not hold
// (most commonly, an operator running it as a project editor rather than
// an owner). Rather than aborting the whole run, EnsureInfrastructure
// records those as manual steps in Result.Steps with the exact command an
// operator with the right access can run instead, and continues with
// whatever else it can still do -- see StepStatus's doc comment.
package gcpsetup

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/api/googleapi"
)

// DefaultAgentAccountID and DefaultMinterAccountID name the two service
// accounts this package creates, absent an operator override --
// terraform/gcp/iam.tf's google_service_account.agent restated as a
// literal default rather than a Terraform variable with one.
const (
	DefaultAgentAccountID  = "grain-agent"
	DefaultMinterAccountID = "grain-gcp-key-minter"
)

// requiredAPIs are always enabled, regardless of Options.EnableGeminiKey:
// iam.googleapis.com backs every ServiceAccounts/Keys call this package
// and gcpkey.NewIAMMinter both make, and iamcredentials.googleapis.com is
// what a future impersonation-based credential (v1's own
// --impersonate-service-account path, terraform/gcp/iam.tf's
// host_impersonates_agent) would need already enabled.
var requiredAPIs = []string{
	"iam.googleapis.com",
	"iamcredentials.googleapis.com",
}

// geminiAPIs are only enabled when Options.EnableGeminiKey is set --
// mirroring terraform/gcp/iam.tf's own enable_gemini_key gate on
// generativelanguage.googleapis.com, plus apikeys.googleapis.com, which
// the API Keys API calls geminikey.Capability makes need enabled the
// same way.
var geminiAPIs = []string{
	"generativelanguage.googleapis.com",
	"apikeys.googleapis.com",
}

// Options is one bootstrap run's configuration -- an operator's answers
// to "which project, which account names, which capabilities."
type Options struct {
	// ProjectID is the GCP project every resource below is created in or
	// granted against. Required.
	ProjectID string
	// AgentAccountID is the account_id (not email) of the narrow service
	// account gcp-key mints per-task keys for -- gcpkey.Provider's own
	// Config.ServiceAccountEmail. Empty means DefaultAgentAccountID.
	AgentAccountID string
	// AgentAccountCount is the number of agent service accounts to create
	// with a fixed numbering scheme (e.g. grain-agent-0, grain-agent-1, ...).
	// 0 or 1 means a single account.
	AgentAccountCount int
	// MinterAccountID is the account_id of the standing service account
	// that mints and revokes the agent account's keys, and (if
	// EnableGeminiKey) administers API keys project-wide -- what
	// scripts/setup.sh's GRAIN_GCP_SERVICE_ACCOUNT_KEY_FILE ends up
	// holding a key for. Empty means DefaultMinterAccountID.
	MinterAccountID string
	// EnableGeminiKey grants the minter account roles/serviceusage.apiKeysAdmin
	// at the project level and enables the two APIs geminiAPIs names --
	// the geminikey capability's own requirements. Off by default: unlike
	// gcp-key, a deployment may not want the project-wide grant this needs.
	EnableGeminiKey bool
	// MintMinterKey, if true, mints a fresh key for the minter account and
	// returns its raw JSON in Result.MinterKeyJSON. A caller mints at most
	// once per bootstrap -- see EnsureInfrastructure's own doc comment on
	// why this is opt-in, not automatic, on every call.
	MintMinterKey bool
}

func (o Options) agentAccountID() string {
	if o.AgentAccountID != "" {
		return o.AgentAccountID
	}
	return DefaultAgentAccountID
}

func (o Options) minterAccountID() string {
	if o.MinterAccountID != "" {
		return o.MinterAccountID
	}
	return DefaultMinterAccountID
}

// StepStatus is what EnsureInfrastructure could do about one piece of
// the bootstrap.
type StepStatus string

const (
	// StepDone means this step made the change (or found it already
	// made) using the credential EnsureInfrastructure ran as.
	StepDone StepStatus = "done"
	// StepManual means the credential this ran as lacks permission to
	// make this change -- Step.Command is the equivalent gcloud
	// invocation an operator with the right access can run by hand, and
	// re-running EnsureInfrastructure afterward will find it already
	// done and move on.
	StepManual StepStatus = "manual"
)

// Step is one unit of the bootstrap, ordered the way EnsureInfrastructure
// performs it -- a caller renders Result.Steps in order to walk an
// operator through exactly what happened and what is left.
type Step struct {
	Name    string
	Status  StepStatus
	Detail  string
	Command string // only set when Status == StepManual
}

// Result is everything EnsureInfrastructure produced: the two account
// emails (needed either way, to feed scripts/setup.sh's
// GRAIN_GCP_SERVICE_ACCOUNT_EMAIL or `grain settings
// -gcp-agent-service-account`), the ordered Steps, and -- only when
// Options.MintMinterKey was set -- the raw key JSON a caller writes to
// disk itself (this package never touches the filesystem; see
// EnsureInfrastructure's doc comment).
type Result struct {
	AgentEmail  string
	AgentEmails []string
	MinterEmail string
	Steps       []Step
	// MinterKeyJSON is the minted key's credentials-file document, set
	// only when Options.MintMinterKey was true and minting succeeded.
	MinterKeyJSON string
}

// AllManual reports whether every step in r needed a human -- the signal
// a caller uses to decide whether to keep going (e.g. write
// scripts/setup.sh's GRAIN_GCP_* variables) or stop and print the manual
// steps for an operator to run first.
func (r Result) AllManual() bool {
	for _, s := range r.Steps {
		if s.Status != StepManual {
			return false
		}
	}
	return len(r.Steps) > 0
}

// Admin is the narrow GCP surface EnsureInfrastructure needs -- narrow
// enough that gcpsetup_test.go fakes it with no network involved, the
// same "no sandbox and no cloud" bar model/capability_test.go's own
// fakes hold to. RealAdmin (admin.go) is the implementation
// EnsureInfrastructure is actually called with; every method is
// idempotent, since EnsureInfrastructure may retry any of them on a
// re-run.
type Admin interface {
	// EnableAPI enables the named API (e.g. "iam.googleapis.com") in the
	// project, succeeding immediately if it is already enabled.
	EnableAPI(ctx context.Context, name string) error
	// EnsureServiceAccount creates accountID if it does not already
	// exist. It does not return the account's email: that is a pure
	// function of accountID and the project (ServiceAccountEmail,
	// below), so there is nothing for a real API response to tell a
	// caller that computing it does not already.
	EnsureServiceAccount(ctx context.Context, accountID, displayName, description string) error
	// GrantServiceAccountRole grants role to member on the service
	// account named by email, succeeding immediately if member already
	// holds it.
	GrantServiceAccountRole(ctx context.Context, email, role, member string) error
	// GrantProjectRole grants role to member at the project level,
	// succeeding immediately if member already holds it.
	GrantProjectRole(ctx context.Context, role, member string) error
	// CreateServiceAccountKey mints a fresh key for email and returns its
	// credentials-file JSON document.
	CreateServiceAccountKey(ctx context.Context, email string) (keyJSON string, err error)
}

// EnsureInfrastructure is the whole bootstrap: enable the APIs both
// capabilities need, create the agent and minter service accounts, grant
// the minter account roles/iam.serviceAccountKeyAdmin on the agent
// account (so it can mint and revoke that account's per-task keys --
// terraform/gcp/iam.tf's host_manages_agent_keys, restated for a
// deployment with no separate host/controller account), and -- if
// Options.EnableGeminiKey -- roles/serviceusage.apiKeysAdmin on the
// project itself (host_gemini_keys). It never mints a minter key unless
// asked to: a caller runs this once with MintMinterKey set, on a genuinely
// new installation, and every later call (a `grain sync` reconciling
// drift, or a re-run after fixing a manual step) leaves any existing key
// alone -- GCP has no way to hand back a key already minted, so minting
// again on every run would only accumulate them toward the 10-key cap
// gcpkey's own explainCreateFailure already warns about.
//
// A step whose error is a 403 (googleapi.Error with Code 403) is recorded
// as StepManual and does not stop the run -- see StepStatus's doc
// comment. Any other error aborts immediately: a network failure or a
// malformed project ID is a real failure this bootstrap cannot route
// around, unlike a missing grant.
func EnsureInfrastructure(ctx context.Context, admin Admin, opts Options) (Result, error) {
	if opts.ProjectID == "" {
		return Result{}, errors.New("gcpsetup: ProjectID is required")
	}

	var result Result
	apis := append([]string{}, requiredAPIs...)
	if opts.EnableGeminiKey {
		apis = append(apis, geminiAPIs...)
	}
	for _, api := range apis {
		step := Step{Name: fmt.Sprintf("enable %s", api)}
		if err := admin.EnableAPI(ctx, api); err != nil {
			manual, mErr := manualStep(err, fmt.Sprintf("gcloud services enable %s --project=%s", api, opts.ProjectID))
			if mErr != nil {
				return result, fmt.Errorf("gcpsetup: enabling %s: %w", api, mErr)
			}
			step.Status, step.Command, step.Detail = StepManual, manual, "insufficient permission to enable this API"
		} else {
			step.Status, step.Detail = StepDone, "enabled (or already was)"
		}
		result.Steps = append(result.Steps, step)
	}

	agentIDs := AgentAccountIDs(opts.agentAccountID(), opts.AgentAccountCount)
	agentEmails := make([]string, len(agentIDs))
	for i, id := range agentIDs {
		email := ServiceAccountEmail(id, opts.ProjectID)
		agentEmails[i] = email
		if err := ensureAccount(ctx, admin, &result, opts.ProjectID, id,
			"grain sandboxed agents", "The controller mints a fresh, short-lived key for this account on every dispatch and pushes it into the sandbox."); err != nil {
			return result, err
		}
	}
	result.AgentEmail = agentEmails[0]
	result.AgentEmails = agentEmails

	minterEmail := ServiceAccountEmail(opts.minterAccountID(), opts.ProjectID)
	if err := ensureAccount(ctx, admin, &result, opts.ProjectID, opts.minterAccountID(),
		"grain GCP key minter", "Mints and revokes the agent account's per-task keys; never the agent account's own credential."); err != nil {
		return result, err
	}
	result.MinterEmail = minterEmail

	minterMember := "serviceAccount:" + minterEmail
	for _, email := range agentEmails {
		if err := grantStep(ctx, admin, &result,
			fmt.Sprintf("grant %s roles/iam.serviceAccountKeyAdmin on %s", minterEmail, email),
			func() error {
				return admin.GrantServiceAccountRole(ctx, email, "roles/iam.serviceAccountKeyAdmin", minterMember)
			},
			fmt.Sprintf("gcloud iam service-accounts add-iam-policy-binding %s --member=%s --role=roles/iam.serviceAccountKeyAdmin --project=%s",
				email, minterMember, opts.ProjectID),
		); err != nil {
			return result, err
		}
	}

	if opts.EnableGeminiKey {
		if err := grantStep(ctx, admin, &result,
			fmt.Sprintf("grant %s roles/serviceusage.apiKeysAdmin on project %s", minterEmail, opts.ProjectID),
			func() error { return admin.GrantProjectRole(ctx, "roles/serviceusage.apiKeysAdmin", minterMember) },
			fmt.Sprintf("gcloud projects add-iam-policy-binding %s --member=%s --role=roles/serviceusage.apiKeysAdmin",
				opts.ProjectID, minterMember),
		); err != nil {
			return result, err
		}
	}

	if opts.MintMinterKey {
		step := Step{Name: fmt.Sprintf("mint a key for %s", minterEmail)}
		keyJSON, err := admin.CreateServiceAccountKey(ctx, minterEmail)
		if err != nil {
			manual, mErr := manualStep(err, fmt.Sprintf(
				"gcloud iam service-accounts keys create <output-file> --iam-account=%s --project=%s",
				minterEmail, opts.ProjectID))
			if mErr != nil {
				return result, fmt.Errorf("gcpsetup: minting a key for %s: %w", minterEmail, mErr)
			}
			step.Status, step.Command, step.Detail = StepManual, manual, "insufficient permission to mint a key for the minter account"
		} else {
			step.Status, step.Detail = StepDone, "minted"
			result.MinterKeyJSON = keyJSON
		}
		result.Steps = append(result.Steps, step)
	}

	return result, nil
}

// AgentAccountIDs returns the list of agent service account IDs for baseID.
// If count <= 1, it returns []string{baseID}.
// If count > 1, it returns []string{baseID-0, baseID-1, ..., baseID-(count-1)}.
func AgentAccountIDs(baseID string, count int) []string {
	if count <= 1 {
		return []string{baseID}
	}
	ids := make([]string, count)
	for i := 0; i < count; i++ {
		ids[i] = fmt.Sprintf("%s-%d", baseID, i)
	}
	return ids
}

// ServiceAccountEmail is the email GCP always assigns a service account
// created with the given accountID in projectID -- deterministic, so
// EnsureInfrastructure can name an account in every step after the one
// that (maybe only manually) creates it, without depending on a create
// call's own response.
func ServiceAccountEmail(accountID, projectID string) string {
	return fmt.Sprintf("%s@%s.iam.gserviceaccount.com", accountID, projectID)
}

// ensureAccount wraps Admin.EnsureServiceAccount with the same
// StepManual-on-403 handling every other step in EnsureInfrastructure
// gets, appending one Step either way -- a manual step here still lets
// every later step proceed, naming the same ServiceAccountEmail an
// operator's own `gcloud iam service-accounts create` will produce.
func ensureAccount(ctx context.Context, admin Admin, result *Result, projectID, accountID, displayName, description string) error {
	step := Step{Name: fmt.Sprintf("create service account %s", accountID)}
	if err := admin.EnsureServiceAccount(ctx, accountID, displayName, description); err != nil {
		manual, mErr := manualStep(err, fmt.Sprintf(
			"gcloud iam service-accounts create %s --display-name=%q --project=%s",
			accountID, displayName, projectID))
		if mErr != nil {
			return fmt.Errorf("gcpsetup: creating service account %s: %w", accountID, mErr)
		}
		step.Status, step.Command, step.Detail = StepManual, manual, "insufficient permission to create this service account"
		result.Steps = append(result.Steps, step)
		return nil
	}
	step.Status, step.Detail = StepDone, ServiceAccountEmail(accountID, projectID)
	result.Steps = append(result.Steps, step)
	return nil
}

// grantStep runs grant, appending one manual-or-done Step named name --
// the common shape both IAM-grant calls in EnsureInfrastructure share. It
// runs unconditionally, even after a manual account-creation step above
// it: the grant targets ServiceAccountEmail's deterministic address
// either way, and an operator working through the manual steps in order
// will have created that account by the time they reach this one.
func grantStep(ctx context.Context, admin Admin, result *Result, name string, grant func() error, manualCommand string) error {
	step := Step{Name: name}
	if err := grant(); err != nil {
		manual, mErr := manualStep(err, manualCommand)
		if mErr != nil {
			return fmt.Errorf("gcpsetup: %s: %w", name, mErr)
		}
		step.Status, step.Command, step.Detail = StepManual, manual, "insufficient permission to grant this role"
	} else {
		step.Status, step.Detail = StepDone, "granted (or already was)"
	}
	result.Steps = append(result.Steps, step)
	return nil
}

// manualStep classifies err: a 403 becomes (command, nil) -- proceed,
// recording command as the manual fallback -- and everything else
// becomes ("", err) -- abort. Kept as one function so every call site
// above applies the exact same rule.
func manualStep(err error, command string) (string, error) {
	var gerr *googleapi.Error
	if errors.As(err, &gerr) && gerr.Code == 403 {
		return command, nil
	}
	return "", err
}
