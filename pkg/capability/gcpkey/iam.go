package gcpkey

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"google.golang.org/api/googleapi"
	iam "google.golang.org/api/iam/v1"
	"google.golang.org/api/option"

	"github.com/bwsalmon/grain/pkg/model"
)

// maxUserManagedKeys is GCP's own cap on user-managed keys per service
// account. Not configurable and not ours: it is stated here only so
// explainCreateFailure can tell the one FAILED_PRECONDITION that means
// "you are at the cap" apart from the ones that do not.
const maxUserManagedKeys = 10

// iamMinter is Minter against the real IAM API, holding one *iam.Service
// authenticated as whatever credential built it -- see NewIAMMinter.
type iamMinter struct {
	svc *iam.Service
}

// NewIAMMinter builds a Minter authenticated with credentialJSON -- a
// Google service-account credentials-file document, the same shape
// CapabilityContext.Credentials.Resolve returns for Config.MinterCredential
// or a Lease's own MintedBy. This is Provider's default NewMinter.
func NewIAMMinter(ctx context.Context, credentialJSON string) (Minter, error) {
	svc, err := iam.NewService(ctx, option.WithCredentialsJSON([]byte(credentialJSON)))
	if err != nil {
		return nil, fmt.Errorf("gcpkey: building an IAM client: %w", err)
	}
	return &iamMinter{svc: svc}, nil
}

// NewIAMMinterFromService is a Minter on an IAM client someone else
// authenticated -- an ambient identity, or a workload-identity config --
// rather than on a service-account key document.
func NewIAMMinterFromService(svc *iam.Service) (Minter, error) {
	if svc == nil {
		return nil, fmt.Errorf("gcpkey: no IAM client")
	}
	return &iamMinter{svc: svc}, nil
}

// CreateKey mints a fresh key under account and decodes its private key
// material back out. PrivateKeyData arrives base64-encoded even when
// PrivateKeyType asks for TYPE_GOOGLE_CREDENTIALS_FILE -- IAM's own field
// doc says as much ("When base64 decoded, the private key data can be used
// to authenticate ... with gcloud auth activate-service-account") -- so the
// decoded bytes, not the raw field, are the credentials-file document
// Placement.Content should carry.
func (m *iamMinter) CreateKey(ctx context.Context, account string) (id, keyJSON string, err error) {
	key, err := m.svc.Projects.ServiceAccounts.Keys.Create(account, &iam.CreateServiceAccountKeyRequest{
		PrivateKeyType: "TYPE_GOOGLE_CREDENTIALS_FILE",
	}).Context(ctx).Do()
	if err != nil {
		return "", "", explainCreateFailure(ctx, m, account, err)
	}
	decoded, err := base64.StdEncoding.DecodeString(key.PrivateKeyData)
	if err != nil {
		return "", "", fmt.Errorf("gcpkey: the created key's private data did not decode: %w", err)
	}
	// The one failure here that would otherwise look like success: an
	// empty document placed at SandboxKeyPath is a file the agent is
	// told is a working key, and the first thing it authenticates with
	// fails somewhere far from the mint that produced it. Treated as a
	// failed mint, and the key deleted rather than left to count against
	// maxUserManagedKeys until the reaper's own cutoff -- the same
	// created-but-unusable cleanup geminikey's CreateKey does.
	if len(bytes.TrimSpace(decoded)) == 0 {
		id := keyID(key.Name)
		if delErr := m.DeleteKey(ctx, account, id); delErr != nil {
			return "", "", fmt.Errorf(
				"gcpkey: IAM created key %s for %s but returned no private key material, "+
					"and deleting it again failed: %w", id, account, delErr)
		}
		return "", "", fmt.Errorf(
			"gcpkey: IAM created key %s for %s but returned no private key material "+
				"(the key has been deleted again)", id, account)
	}
	return keyID(key.Name), string(decoded), nil
}

// explainCreateFailure names which way minting actually failed, in a
// sentence naming the command or the pane that fixes that one -- the same
// job geminikey.advise does for the API Keys API, and for the same
// reason: everything below arrives from GCP as a bare status code whose
// own message reads like a bug in grain rather than like one unrun setup
// step, on a deployment where Settings has already said **Ready**
// because a project, an account and a secret are all set. Nothing on any
// configuration pane can see whether the *project* was ever finished.
//
// Four causes, each with a different remedy:
//
//   - 403 with SERVICE_DISABLED -- iam.googleapis.com was never enabled.
//   - 403 otherwise -- the minter holds no roles/iam.serviceAccountKeyAdmin
//     on the agent account. `grain setup gcp` is the one command that
//     fixes either, so both name it.
//   - 404 -- Settings names a service account that does not exist in this
//     project (a typo, or the wrong project), which is a configuration
//     answer rather than a GCP one.
//   - 400 FAILED_PRECONDITION -- two entirely different things, told
//     apart below.
//
// That last split is the bug this function had. bwsalmon/agents#140
// added it for the key-quota case, whose raw text ("FAILED_PRECONDITION:
// Precondition check failed") names nothing at all -- but it then
// answered *every* FAILED_PRECONDITION with the quota explanation. The
// far more common one on a modern project is the organization policy
// constraints/iam.disableServiceAccountKeyCreation, which forbids
// user-managed service-account keys outright and is enforced by default
// in organizations created since 2024. An operator whose org forbids
// keys was being told, with a count of 0 in the sentence, that grain was
// creating keys faster than it releases them -- sending them to look for
// a leak that is not there, in the one case where no amount of looking
// at grain can help. The count decides it now: the quota explanation is
// given only when the account is actually at the cap.
//
// Never worse than the original error, on every path: GCP's own message
// is wrapped, never replaced, and a listing that fails while counting
// leaves the two possibilities named rather than one of them asserted.
func explainCreateFailure(ctx context.Context, m *iamMinter, account string, err error) error {
	// GCP's own error, carried into every message below and never
	// replaced -- but no longer re-labelled "minting a key for
	// <account>" on the way, which Materialize's own wrap already says.
	// Both saying it is what made task 163's failure reach an operator
	// as "materializing capabilities: model: materializing capability
	// \"gcp-key\": gcpkey: minting a key for projects/.../serviceAccounts/...:
	// gcpkey: minting a key for projects/.../serviceAccounts/...: Post ..."
	// -- forty duplicated characters of resource name ahead of the one
	// clause that said what went wrong.
	wrapped := err
	project, email := splitAccount(account)

	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case http.StatusForbidden:
			if isServiceDisabled(apiErr) {
				return fmt.Errorf("the IAM API is not enabled in project %s -- run "+
					"`grain setup gcp -project %s`, which enables iam.googleapis.com and "+
					"grants the minter the role it needs on %s: %w", project, project, email, wrapped)
			}
			return fmt.Errorf("the minter credential is not permitted to create keys for "+
				"%s (it needs roles/iam.serviceAccountKeyAdmin on that account) -- run "+
				"`grain setup gcp -project %s`, which grants exactly that: %w", email, project, wrapped)
		case http.StatusNotFound:
			return fmt.Errorf("project %s has no service account %s -- check the GCP "+
				"project and service account email under Settings -> Capabilities, or run "+
				"`grain setup gcp -project %s` to create it: %w", project, email, project, wrapped)
		}
	}

	// Asked before isFailedPrecondition, not after: the org policy's own
	// refusal carries status FAILED_PRECONDITION in the response body but
	// a message of its own ("Key creation is not allowed on this service
	// account."), and googleapi.Error.Error() prints the message, never
	// the status -- so the conclusive signal is checked first rather than
	// gated behind a spelling it need not use.
	if isKeyCreationDisabled(detailOf(err, apiErr)) {
		return fmt.Errorf("GCP refuses to create keys for %s at all: the organization "+
			"policy constraints/iam.disableServiceAccountKeyCreation forbids user-managed "+
			"service-account keys in project %s. Either have that constraint lifted for "+
			"this project, or stop granting gcp-key here (Settings -> Capabilities, and "+
			"each task's own capability picker): %w", email, project, wrapped)
	}
	if !isFailedPrecondition(detailOf(err, apiErr)) {
		return wrapped
	}
	keys, listErr := m.ListKeys(ctx, account)
	if listErr != nil {
		return fmt.Errorf("%w\n\nGCP refused this as a failed precondition, which is "+
			"either the %d-key cap per service account or the organization policy "+
			"constraints/iam.disableServiceAccountKeyCreation forbidding user-managed keys "+
			"in project %s. Listing %s's keys to tell the two apart also failed, so this "+
			"names both.", wrapped, maxUserManagedKeys, project, email)
	}
	if len(keys) > 0 {
		// Asked again after a jittered half-minute or more, not the
		// model's default five seconds: every retry is one CreateKey
		// per account -- a write against the project's IAM quota --
		// for every job queued on it, and a dozen jobs waiting out a
		// full account at five seconds each spent that quota for
		// everything else (grain/task-281).
		return &model.ResourceExhaustedError{Resource: "GCP service-account keys",
			RetryAfter: rateLimitRetryMin + jitter(rateLimitRetrySpread), Err: fmt.Errorf("%w\n\nGCP allows at most %d user-managed keys per service "+
				"account, and %s currently has %d. grain revokes a key when its task finishes "+
				"and reaps anything past its own max age, so hitting this cap means keys are "+
				"being created faster than either is releasing them.",
				wrapped, maxUserManagedKeys, email, len(keys))}
	}
	return fmt.Errorf("%w\n\nThis is not the %d-key cap -- %s holds 0. The usual cause "+
		"is the organization policy constraints/iam.disableServiceAccountKeyCreation, "+
		"which forbids user-managed service-account keys in project %s; either have it "+
		"lifted for this project, or stop granting gcp-key here (Settings -> "+
		"Capabilities).", wrapped, maxUserManagedKeys, email, project)
}

// detailOf is everything GCP said about a failure, in one string to
// match against: the error's own text plus, for an API error, the raw
// response body googleapi.Error keeps -- which is where a `status` the
// printed message leaves out (FAILED_PRECONDITION) and a reason no
// message mentions (SERVICE_DISABLED) actually live.
func detailOf(err error, apiErr *googleapi.Error) string {
	if apiErr == nil {
		return err.Error()
	}
	return err.Error() + "\n" + apiErr.Body
}

// isFailedPrecondition matches GCP's own two spellings of it -- the
// status name in a structured error body, and the "Precondition check
// failed" message the IAM API answers a key-quota refusal with.
func isFailedPrecondition(detail string) bool {
	return strings.Contains(detail, "FAILED_PRECONDITION") ||
		strings.Contains(detail, "Precondition check failed")
}

// isKeyCreationDisabled reports whether GCP named the org-policy
// constraint itself. The machine-readable form is a PreconditionFailure
// detail whose type is the constraint's own name; the message form is
// "Key creation is not allowed on this service account." Either is
// conclusive -- without one, the caller falls back to counting keys
// rather than guessing.
func isKeyCreationDisabled(detail string) bool {
	return strings.Contains(detail, "iam.disableServiceAccountKeyCreation") ||
		strings.Contains(detail, "Key creation is not allowed")
}

// isCredentialRefused reports whether err is Google's *token* endpoint
// refusing the credential this client was built with, rather than the
// IAM API refusing a call it made. It is the failure task 163 arrived
// as, and it is unlike every other one in this file: it happens before
// any request reaches iam.googleapis.com, so it carries no
// googleapi.Error, no status code errors.As can read, and no resource
// name at all -- only the token endpoint's own JSON body, quoted inside
// an oauth2 transport error:
//
//	Post "https://iam.googleapis.com/v1/projects/.../keys?alt=json":
//	auth: cannot fetch token: 400 Response:
//	{"error":"invalid_grant","error_description":"Invalid JWT Signature."}
//
// So the only thing to match on is that body. `invalid_grant` is
// conclusive on its own -- it is the OAuth error for "this grant is not
// one I will exchange", which for a service-account JWT means the key
// signing it is not one Google holds the public half of any more --
// and the signature description is matched too because it is the
// spelling a deleted or rotated key produces, which is the common case
// by a wide margin.
//
// geminikey has its own copy, restated rather than shared for the same
// reason isServiceDisabled below is: gemini-key mints through this very
// credential (pkg/cli/daemon.go passes DefaultMinterCredential to
// geminikey.New), so the same dead key breaks it in the same way, and
// the two packages depend on each other for nothing.
func isCredentialRefused(err error) bool {
	if err == nil {
		return false
	}
	detail := err.Error()
	return strings.Contains(detail, "invalid_grant") ||
		strings.Contains(detail, "Invalid JWT Signature")
}

// isServiceDisabled reports whether a 403 is Google's "this API has never
// been enabled in this project" rather than an IAM refusal -- the same
// SERVICE_DISABLED reason geminikey.isServiceDisabled reads, restated
// here rather than shared because the two packages have no dependency on
// each other and one three-line predicate is a smaller thing to repeat
// than a package to introduce for it.
func isServiceDisabled(apiErr *googleapi.Error) bool {
	return strings.Contains(apiErr.Body, "SERVICE_DISABLED") ||
		strings.Contains(apiErr.Message, "has not been used in project")
}

// splitAccount takes a resource name back apart into the project and the
// email accountName put together, so a message can name the two things an
// operator actually typed into Settings rather than the API's own
// "projects/p/serviceAccounts/e" spelling of them. A name in any other
// shape is handed back whole, in both halves: a diagnosis is never worth
// a panic.
func splitAccount(account string) (project, email string) {
	parts := strings.Split(account, "/")
	if len(parts) != 4 || parts[0] != "projects" || parts[2] != "serviceAccounts" {
		return account, account
	}
	return parts[1], parts[3]
}

// DeleteKey revokes keyID under account. keyID is always the bare id --
// the last path segment of a key's own resource name -- never the full
// name, matching what Lease.Resource and KeyInfo.ID both carry.
func (m *iamMinter) DeleteKey(ctx context.Context, account, keyID string) error {
	_, err := m.svc.Projects.ServiceAccounts.Keys.Delete(account + "/keys/" + keyID).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("gcpkey: deleting key %s: %w", keyID, err)
	}
	return nil
}

// ListKeys lists every user-managed key under account -- KeyTypes(
// "USER_MANAGED") excludes the Google-managed keys every service account
// also has, which this package never touches (they cannot be listed,
// downloaded, or deleted by this API at all -- the same exclusion
// gcp_keys.py's own _list_keys applies with --managed-by=user).
func (m *iamMinter) ListKeys(ctx context.Context, account string) ([]KeyInfo, error) {
	resp, err := m.svc.Projects.ServiceAccounts.Keys.List(account).KeyTypes("USER_MANAGED").Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("gcpkey: listing keys for %s: %w", account, err)
	}
	out := make([]KeyInfo, 0, len(resp.Keys))
	for _, key := range resp.Keys {
		info := KeyInfo{ID: keyID(key.Name)}
		if key.ValidAfterTime != "" {
			if t, err := time.Parse(time.RFC3339, key.ValidAfterTime); err == nil {
				info.CreatedAt = t
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// keyID is the bare id off the end of a key's resource name --
// "projects/{p}/serviceAccounts/{e}/keys/{id}".
func keyID(name string) string {
	idx := strings.LastIndex(name, "/")
	if idx == -1 {
		return name
	}
	return name[idx+1:]
}
