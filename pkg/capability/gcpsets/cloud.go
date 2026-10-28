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

package gcpsets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"cloud.google.com/go/compute/metadata"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iam/v1"
	"google.golang.org/api/option"
	"google.golang.org/api/storage/v1"

	"github.com/ai-on-gke/grain/pkg/capability/gcpkey"
)

// ServiceAccount is one service account as Cloud lists it.
type ServiceAccount struct {
	Email       string
	Description string
}

// Binding is one role's members in an IAM policy.
type Binding struct {
	Role    string
	Members []string
}

// Policy is an IAM policy: its bindings and the etag Set must echo.
type Policy struct {
	Bindings []Binding
	Etag     string
}

// ErrConflict is SetPolicy losing a race with another writer; the caller
// reads the policy again and retries.
var ErrConflict = errors.New("the IAM policy changed underneath this write")

// Cloud is everything the server does on GCP, as one admin. Resource is
// "projects/<id>" or "buckets/<name>". Tests substitute a fake.
type Cloud interface {
	gcpkey.Minter
	ListServiceAccounts(ctx context.Context, project string) ([]ServiceAccount, error)
	CreateServiceAccount(ctx context.Context, project, accountID, displayName, description string) (ServiceAccount, error)
	DeleteServiceAccount(ctx context.Context, project, email string) error
	GetPolicy(ctx context.Context, resource string) (*Policy, error)
	SetPolicy(ctx context.Context, resource string, p *Policy) error
	// TestPermissions returns the subset of perms the admin holds on
	// the project.
	TestPermissions(ctx context.Context, project string, perms []string) ([]string, error)
}

// Connector builds an admin's Cloud and says who it is.
type Connector interface {
	// Connect is a Cloud acting as a: with a's configured credential, or
	// with the server's own ambient identity.
	Connect(ctx context.Context, a Admin) (Cloud, error)
	// Identity is the email a resolves to -- for an ambient admin, the
	// identity the server process runs as where it runs.
	Identity(ctx context.Context, a Admin) (string, error)
}

// ADC finds the server's ambient credentials; google.FindDefaultCredentials
// unless a test substitutes a fake.
type ADC func(ctx context.Context, scopes ...string) (*google.Credentials, error)

// GoogleConnector is Connector against the real IAM, Resource Manager
// and Storage APIs.
type GoogleConnector struct {
	ADC ADC
	// HTTP is the client identity lookups go through; nil is
	// http.DefaultClient.
	HTTP *http.Client
	// MetadataEmail is the metadata server's default identity, asked when
	// ambient credentials come from it; nil is metadata.EmailWithContext.
	MetadataEmail func(ctx context.Context) (string, error)
	// TokenTransport is what token fetches go through; nil is
	// http.DefaultTransport.
	TokenTransport http.RoundTripper
	// Options are extra client options for the Google API clients
	// Connect builds; tests point them at a fake.
	Options []option.ClientOption
}

const cloudPlatform = "https://www.googleapis.com/auth/cloud-platform"

// credentials are a's credentials, built for a Cloud that outlives ctx:
// their token source fetches with tokenContext, never with ctx, and a
// fetch that fails comes back as a CredentialError.
func (g GoogleConnector) credentials(ctx context.Context, a Admin) (*google.Credentials, error) {
	tctx := tokenContext(ctx, g.TokenTransport)
	var creds *google.Credentials
	switch a.CredentialSource {
	case SourceConfigured:
		c, err := google.CredentialsFromJSON(tctx, []byte(a.Credential), cloudPlatform)
		if err != nil {
			return nil, &CredentialError{Admin: a.Name, Source: a.CredentialSource, Kind: CredentialMissing, Err: fmt.Errorf("%s credential", kindOrUnknown(a.Credential))}
		}
		creds = c
	case SourceAmbient:
		adc := g.ADC
		if adc == nil {
			adc = google.FindDefaultCredentials
		}
		c, err := adc(tctx, cloudPlatform)
		if err != nil {
			return nil, &CredentialError{Admin: a.Name, Source: a.CredentialSource, Kind: CredentialMissing, Err: err}
		}
		creds = c
	default:
		return nil, fmt.Errorf("admin %s: unknown credential_source %q", a.Name, a.CredentialSource)
	}
	if creds.TokenSource != nil {
		creds = &google.Credentials{
			ProjectID:              creds.ProjectID,
			TokenSource:            &adminTokenSource{admin: a, src: creds.TokenSource},
			JSON:                   creds.JSON,
			UniverseDomainProvider: creds.GetUniverseDomain,
		}
	}
	return creds, nil
}

func kindOrUnknown(raw string) string {
	k, err := credentialKind(raw)
	if err != nil {
		return "unrecognised"
	}
	return k
}

// Connect implements Connector.
func (g GoogleConnector) Connect(ctx context.Context, a Admin) (Cloud, error) {
	creds, err := g.credentials(ctx, a)
	if err != nil {
		return nil, err
	}
	// The clients outlive ctx as the credentials do.
	ctx = context.WithoutCancel(ctx)
	opt := append([]option.ClientOption{option.WithCredentials(creds)}, g.Options...)
	iamSvc, err := iam.NewService(ctx, opt...)
	if err != nil {
		return nil, fmt.Errorf("admin %s: building an IAM client: %w", a.Name, err)
	}
	crm, err := cloudresourcemanager.NewService(ctx, opt...)
	if err != nil {
		return nil, fmt.Errorf("admin %s: building a Resource Manager client: %w", a.Name, err)
	}
	st, err := storage.NewService(ctx, opt...)
	if err != nil {
		return nil, fmt.Errorf("admin %s: building a Storage client: %w", a.Name, err)
	}
	minter, err := gcpkey.NewIAMMinterFromService(iamSvc)
	if err != nil {
		return nil, err
	}
	return &googleCloud{Minter: minter, iam: iamSvc, crm: crm, st: st, tokens: creds.TokenSource}, nil
}

var impersonationEmail = regexp.MustCompile(`serviceAccounts/([^/:]+@[^/:]+):generateAccessToken`)

// Identity implements Connector: the email in a service-account key, the
// account a workload-identity config impersonates, the metadata server's
// default account, or -- for a user's own gcloud credentials on a host --
// the email Google's tokeninfo reports for its access token.
func (g GoogleConnector) Identity(ctx context.Context, a Admin) (string, error) {
	creds, err := g.credentials(ctx, a)
	if err != nil {
		return "", err
	}
	if email, kind, ok := identityFromJSON(creds.JSON); ok {
		return email, nil
	} else if kind == "external_account" {
		return "", fmt.Errorf("admin %s: its workload-identity config impersonates no service account, so its identity is the federated principal itself", a.Name)
	}
	if len(creds.JSON) == 0 {
		// No credentials file: the metadata server's identity.
		lookup := g.MetadataEmail
		if lookup == nil {
			lookup = func(ctx context.Context) (string, error) { return metadata.EmailWithContext(ctx, "default") }
		}
		email, err := lookup(ctx)
		if err != nil {
			return "", fmt.Errorf("admin %s: asking the metadata server who this is: %v", a.Name, err)
		}
		return email, nil
	}
	tok, err := creds.TokenSource.Token()
	if err != nil {
		return "", err
	}
	client := g.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://oauth2.googleapis.com/tokeninfo?access_token="+tok.AccessToken, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("admin %s: asking tokeninfo who this is failed", a.Name)
	}
	defer resp.Body.Close()
	var info struct {
		Email string `json:"email"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&info) != nil || info.Email == "" {
		return "", fmt.Errorf("admin %s: tokeninfo did not say who its ambient credentials are (status %d)", a.Name, resp.StatusCode)
	}
	return info.Email, nil
}

// identityFromJSON reads the identity a credentials document names
// itself, without quoting anything else in it.
func identityFromJSON(raw []byte) (email, kind string, ok bool) {
	var doc struct {
		Type                           string `json:"type"`
		ClientEmail                    string `json:"client_email"`
		ServiceAccountImpersonationURL string `json:"service_account_impersonation_url"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &doc) != nil {
		return "", "", false
	}
	switch doc.Type {
	case "service_account":
		return doc.ClientEmail, doc.Type, doc.ClientEmail != ""
	case "external_account", "impersonated_service_account":
		if m := impersonationEmail.FindStringSubmatch(doc.ServiceAccountImpersonationURL); m != nil {
			return m[1], doc.Type, true
		}
	}
	return "", doc.Type, false
}

type googleCloud struct {
	gcpkey.Minter
	iam *iam.Service
	crm *cloudresourcemanager.Service
	st  *storage.Service
	// tokens is the token source the clients authenticate with.
	tokens oauth2.TokenSource
}

// CheckToken fetches an access token the way every call does, so Test
// finds expired or missing credentials before a mint does.
func (c *googleCloud) CheckToken(context.Context) error {
	if c.tokens == nil {
		return nil
	}
	_, err := c.tokens.Token()
	return err
}

func (c *googleCloud) ListServiceAccounts(ctx context.Context, project string) ([]ServiceAccount, error) {
	var out []ServiceAccount
	err := c.iam.Projects.ServiceAccounts.List("projects/"+project).Pages(ctx, func(r *iam.ListServiceAccountsResponse) error {
		for _, sa := range r.Accounts {
			out = append(out, ServiceAccount{Email: sa.Email, Description: sa.Description})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing service accounts in %s: %w", project, err)
	}
	return out, nil
}

func (c *googleCloud) CreateServiceAccount(ctx context.Context, project, accountID, displayName, description string) (ServiceAccount, error) {
	sa, err := c.iam.Projects.ServiceAccounts.Create("projects/"+project, &iam.CreateServiceAccountRequest{
		AccountId:      accountID,
		ServiceAccount: &iam.ServiceAccount{DisplayName: displayName, Description: description},
	}).Context(ctx).Do()
	if err != nil {
		return ServiceAccount{}, fmt.Errorf("creating service account %s in %s: %w", accountID, project, err)
	}
	return ServiceAccount{Email: sa.Email, Description: sa.Description}, nil
}

func (c *googleCloud) DeleteServiceAccount(ctx context.Context, project, email string) error {
	_, err := c.iam.Projects.ServiceAccounts.Delete("projects/" + project + "/serviceAccounts/" + email).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("deleting service account %s: %w", email, err)
	}
	return nil
}

func (c *googleCloud) GetPolicy(ctx context.Context, resource string) (*Policy, error) {
	if b, ok := bucketOf(resource); ok {
		p, err := c.st.Buckets.GetIamPolicy(b).Context(ctx).Do()
		if err != nil {
			return nil, fmt.Errorf("reading the IAM policy of bucket %s: %w", b, err)
		}
		out := &Policy{Etag: p.Etag}
		for _, bd := range p.Bindings {
			if bd.Condition != nil {
				continue
			}
			out.Bindings = append(out.Bindings, Binding{Role: bd.Role, Members: bd.Members})
		}
		return out, nil
	}
	project := strings.TrimPrefix(resource, "projects/")
	p, err := c.crm.Projects.GetIamPolicy(project, &cloudresourcemanager.GetIamPolicyRequest{}).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("reading the IAM policy of %s: %w", project, err)
	}
	out := &Policy{Etag: p.Etag}
	for _, bd := range p.Bindings {
		if bd.Condition != nil {
			continue
		}
		out.Bindings = append(out.Bindings, Binding{Role: bd.Role, Members: bd.Members})
	}
	return out, nil
}

// SetPolicy writes p's unconditional bindings back, keeping whatever
// conditional bindings the resource has: those are never this server's.
func (c *googleCloud) SetPolicy(ctx context.Context, resource string, p *Policy) error {
	if b, ok := bucketOf(resource); ok {
		cur, err := c.st.Buckets.GetIamPolicy(b).Context(ctx).Do()
		if err != nil {
			return fmt.Errorf("reading the IAM policy of bucket %s: %w", b, err)
		}
		if cur.Etag != p.Etag {
			return ErrConflict
		}
		var bindings []*storage.PolicyBindings
		for _, bd := range cur.Bindings {
			if bd.Condition != nil {
				bindings = append(bindings, bd)
			}
		}
		for _, bd := range p.Bindings {
			bindings = append(bindings, &storage.PolicyBindings{Role: bd.Role, Members: bd.Members})
		}
		cur.Bindings = bindings
		_, err = c.st.Buckets.SetIamPolicy(b, cur).Context(ctx).Do()
		return conflictOr(err, "bucket "+b)
	}
	project := strings.TrimPrefix(resource, "projects/")
	cur, err := c.crm.Projects.GetIamPolicy(project, &cloudresourcemanager.GetIamPolicyRequest{}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("reading the IAM policy of %s: %w", project, err)
	}
	if cur.Etag != p.Etag {
		return ErrConflict
	}
	var bindings []*cloudresourcemanager.Binding
	for _, bd := range cur.Bindings {
		if bd.Condition != nil {
			bindings = append(bindings, bd)
		}
	}
	for _, bd := range p.Bindings {
		bindings = append(bindings, &cloudresourcemanager.Binding{Role: bd.Role, Members: bd.Members})
	}
	cur.Bindings = bindings
	_, err = c.crm.Projects.SetIamPolicy(project, &cloudresourcemanager.SetIamPolicyRequest{Policy: cur}).Context(ctx).Do()
	return conflictOr(err, project)
}

func conflictOr(err error, what string) error {
	if err == nil {
		return nil
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) && apiErr.Code == http.StatusConflict {
		return ErrConflict
	}
	return fmt.Errorf("writing the IAM policy of %s: %w", what, err)
}

func (c *googleCloud) TestPermissions(ctx context.Context, project string, perms []string) ([]string, error) {
	r, err := c.crm.Projects.TestIamPermissions(project, &cloudresourcemanager.TestIamPermissionsRequest{Permissions: perms}).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("testing permissions on %s: %w", project, err)
	}
	return r.Permissions, nil
}

// identityTimeout bounds one identity lookup in a report.
const identityTimeout = 20 * time.Second
