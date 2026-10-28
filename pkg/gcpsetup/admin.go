package gcpsetup

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	crm "google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/googleapi"
	iam "google.golang.org/api/iam/v1"
	"google.golang.org/api/option"
	su "google.golang.org/api/serviceusage/v1"
)

// RealAdmin is Admin against the real GCP APIs: IAM (service accounts,
// keys, per-resource IAM policies), Service Usage (enabling APIs) and
// Cloud Resource Manager (project-level IAM policies -- IAM's own
// service-account endpoints have no project-level policy call). All
// three are built from the same credential, exactly like gcpkey's own
// iamMinter builds one *iam.Service per Materialize/Revoke/Reap call
// (gcpkey.go's doc comment on "a fresh Minter is built... rather than
// cached") -- here there is only ever one bootstrap run, so RealAdmin
// builds its three clients once, in NewRealAdmin, rather than per call.
type RealAdmin struct {
	projectID string
	iam       *iam.Service
	su        *su.Service
	crm       *crm.Service
}

// NewRealAdmin builds a RealAdmin for projectID. With no opts, the
// underlying clients authenticate with Application Default Credentials
// (`gcloud auth application-default login`, or
// GOOGLE_APPLICATION_CREDENTIALS) -- the same default every
// google.golang.org/api service uses when given no option.WithCredentials*
// of its own; pass option.WithCredentialsFile(path) explicitly for an
// operator running this as a specific service account.
func NewRealAdmin(ctx context.Context, projectID string, opts ...option.ClientOption) (*RealAdmin, error) {
	iamSvc, err := iam.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("gcpsetup: building an IAM client: %w", err)
	}
	suSvc, err := su.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("gcpsetup: building a Service Usage client: %w", err)
	}
	crmSvc, err := crm.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("gcpsetup: building a Cloud Resource Manager client: %w", err)
	}
	return &RealAdmin{projectID: projectID, iam: iamSvc, su: suSvc, crm: crmSvc}, nil
}

func (a *RealAdmin) accountResource(email string) string {
	return fmt.Sprintf("projects/%s/serviceAccounts/%s", a.projectID, email)
}

// EnableAPI enables name, polling the resulting operation until it
// finishes -- Enable itself only starts the work (serviceusage-gen.go's
// own Enable returns an Operation, not a synchronous result), and every
// step EnsureInfrastructure runs after this one needs the API to
// actually be usable, not merely requested.
func (a *RealAdmin) EnableAPI(ctx context.Context, name string) error {
	serviceName := fmt.Sprintf("projects/%s/services/%s", a.projectID, name)
	op, err := a.su.Services.Enable(serviceName, &su.EnableServiceRequest{}).Context(ctx).Do()
	if err != nil {
		return err
	}
	if op.Done {
		return nil
	}
	deadline := time.Now().Add(60 * time.Second)
	for !op.Done {
		if time.Now().After(deadline) {
			return fmt.Errorf("gcpsetup: enabling %s did not finish within 60s", name)
		}
		time.Sleep(2 * time.Second)
		op, err = a.su.Operations.Get(op.Name).Context(ctx).Do()
		if err != nil {
			return err
		}
	}
	if op.Error != nil {
		return fmt.Errorf("gcpsetup: enabling %s: %s", name, op.Error.Message)
	}
	return nil
}

// EnsureServiceAccount creates accountID, tolerating "already exists"
// (a 409 from Create) as success -- the get-or-create idempotency every
// Admin method promises, done here as create-or-tolerate since IAM's own
// Create is the only call that needs the display name/description and a
// plain Get first would be an extra round trip for the common "already
// exists" case this achieves in one.
func (a *RealAdmin) EnsureServiceAccount(ctx context.Context, accountID, displayName, description string) error {
	_, err := a.iam.Projects.ServiceAccounts.Create(
		fmt.Sprintf("projects/%s", a.projectID),
		&iam.CreateServiceAccountRequest{
			AccountId: accountID,
			ServiceAccount: &iam.ServiceAccount{
				DisplayName: displayName,
				Description: description,
			},
		},
	).Context(ctx).Do()
	if err != nil && !isAlreadyExists(err) {
		return err
	}
	return nil
}

// GrantServiceAccountRole adds a role/member binding to the IAM policy
// of the service account named by email -- a read-modify-write against
// GetIamPolicy/SetIamPolicy, the only way GCP exposes a per-resource IAM
// policy; skipped entirely (no SetIamPolicy call at all) if member
// already holds role, so a second run makes no write where the first one
// already succeeded.
func (a *RealAdmin) GrantServiceAccountRole(ctx context.Context, email, role, member string) error {
	resource := a.accountResource(email)
	policy, err := a.iam.Projects.ServiceAccounts.GetIamPolicy(resource).Context(ctx).Do()
	if err != nil {
		return err
	}
	if addBinding(policy, role, member) {
		if _, err := a.iam.Projects.ServiceAccounts.SetIamPolicy(resource, &iam.SetIamPolicyRequest{Policy: policy}).Context(ctx).Do(); err != nil {
			return err
		}
	}
	return nil
}

// GrantProjectRole is GrantServiceAccountRole's project-level twin --
// Cloud Resource Manager's Projects.GetIamPolicy/SetIamPolicy, since IAM
// itself has no project-scoped policy endpoint.
func (a *RealAdmin) GrantProjectRole(ctx context.Context, role, member string) error {
	policy, err := a.crm.Projects.GetIamPolicy(a.projectID, &crm.GetIamPolicyRequest{}).Context(ctx).Do()
	if err != nil {
		return err
	}
	if addProjectBinding(policy, role, member) {
		if _, err := a.crm.Projects.SetIamPolicy(a.projectID, &crm.SetIamPolicyRequest{Policy: policy}).Context(ctx).Do(); err != nil {
			return err
		}
	}
	return nil
}

// CreateServiceAccountKey mints a fresh key for email and decodes its
// private key material -- the same TYPE_GOOGLE_CREDENTIALS_FILE /
// base64-decode shape as gcpkey's own iamMinter.CreateKey
// (pkg/capability/gcpkey/iam.go), duplicated rather than shared because
// that type is unexported and this call happens at most once per
// bootstrap, not once per dispatch.
func (a *RealAdmin) CreateServiceAccountKey(ctx context.Context, email string) (string, error) {
	key, err := a.iam.Projects.ServiceAccounts.Keys.Create(a.accountResource(email), &iam.CreateServiceAccountKeyRequest{
		PrivateKeyType: "TYPE_GOOGLE_CREDENTIALS_FILE",
	}).Context(ctx).Do()
	if err != nil {
		return "", err
	}
	decoded, err := base64.StdEncoding.DecodeString(key.PrivateKeyData)
	if err != nil {
		return "", fmt.Errorf("gcpsetup: the created key's private data did not decode: %w", err)
	}
	return string(decoded), nil
}

// addBinding adds member to role's binding in policy, creating the
// binding if role has none yet, and reports whether it changed anything
// -- false means member already held role, the signal callers use to
// skip an unneeded SetIamPolicy. A conditional binding is never reused or
// counted: holding a role only under a condition is not holding it, and a
// member appended to one would be granted the role only when the
// condition holds.
func addBinding(policy *iam.Policy, role, member string) bool {
	for _, b := range policy.Bindings {
		if b.Role != role || b.Condition != nil {
			continue
		}
		for _, m := range b.Members {
			if m == member {
				return false
			}
		}
		b.Members = append(b.Members, member)
		return true
	}
	policy.Bindings = append(policy.Bindings, &iam.Binding{Role: role, Members: []string{member}})
	return true
}

// addProjectBinding is addBinding against Cloud Resource Manager's own
// Policy/Binding types -- structurally identical to iam.Policy/Binding,
// but the two packages define distinct Go types for them, so there is no
// generic version of this without reflection.
func addProjectBinding(policy *crm.Policy, role, member string) bool {
	for _, b := range policy.Bindings {
		if b.Role != role || b.Condition != nil {
			continue
		}
		for _, m := range b.Members {
			if m == member {
				return false
			}
		}
		b.Members = append(b.Members, member)
		return true
	}
	policy.Bindings = append(policy.Bindings, &crm.Binding{Role: role, Members: []string{member}})
	return true
}

// isAlreadyExists reports whether err is the 409 IAM's own Create
// returns for an account_id already in use in this project (including
// one soft-deleted within the last 30 days -- IAM's own documented
// behavior, not something this package works around).
func isAlreadyExists(err error) bool {
	var gerr *googleapi.Error
	return errors.As(err, &gerr) && gerr.Code == 409
}
