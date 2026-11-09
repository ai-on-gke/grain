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

package gcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// roleServiceAccountUser carries iam.serviceAccounts.actAs, which GCE checks on
// the service account named in an instances.insert request.
const roleServiceAccountUser = "roles/iam.serviceAccountUser"

var (
	// selfActAsNotFoundDelays bounds how long ensureSelfActAs waits for a
	// just-created account to become visible to its own IAM policy methods
	// (about a minute in total, GCP's documented propagation time).
	selfActAsNotFoundDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second}
	// selfActAsConflictDelays bounds read-modify-write retries after another
	// writer changed the account's policy between our get and set (HTTP 409).
	selfActAsConflictDelays = []time.Duration{200 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second}
)

// WithSelfPrincipalResolver overrides how the provider works out its own IAM
// principal when compute.self_principal is unset (for tests).
func WithSelfPrincipalResolver(fn func(ctx context.Context) (string, error)) ResourcesOption {
	return func(r *GCPInstanceResources) {
		if fn != nil {
			r.principalResolver = fn
		}
	}
}

// WithIAMRetrySleep overrides the wait between IAM policy retries (for tests).
func WithIAMRetrySleep(fn func(ctx context.Context, d time.Duration) error) ResourcesOption {
	return func(r *GCPInstanceResources) {
		if fn != nil {
			r.sleep = fn
		}
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ensureSelfActAs grants the registry's own principal roles/iam.serviceAccountUser
// on saEmail -- that one account, through the account's own IAM policy -- so the
// instances.insert that attaches it to the VM passes GCE's actAs check. It is a
// read-modify-write on the policy's etag: an existing binding is left alone, and
// a concurrent writer (HTTP 409) or an account not yet visible to IAM (HTTP 404)
// is retried with bounded backoff. A refusal (HTTP 403) is reported naming the
// permission the registry is missing.
func (r *GCPInstanceResources) ensureSelfActAs(ctx context.Context, saEmail string) error {
	if !strings.HasPrefix(strings.ToLower(saEmail), strings.ToLower(r.saPrefix)) {
		return fmt.Errorf("%w: refusing to change the IAM policy of service account %q outside sa_prefix %q", ErrOutOfScope, saEmail, r.saPrefix)
	}
	principal, err := r.resolveSelfPrincipal(ctx)
	if err != nil {
		return fmt.Errorf("gcp iam: compute.grant_self_act_as is on, but the registry could not work out its own IAM principal to grant %s on %s: %v; "+
			"set compute.self_principal (for example \"serviceAccount:<registry-gsa>@<project>.iam.gserviceaccount.com\", or for GKE Workload Identity without a Google service account "+
			"\"principal://iam.googleapis.com/projects/<project-number>/locations/global/workloadIdentityPools/<project>.svc.id.goog/subject/ns/<namespace>/sa/<ksa>\"), "+
			"or grant the registry iam.serviceAccounts.actAs on the instance accounts yourself and set compute.grant_self_act_as: false",
			roleServiceAccountUser, saEmail, err)
	}

	saURL := fmt.Sprintf("%s/v1/projects/%s/serviceAccounts/%s",
		r.iamBaseURL, url.PathEscape(r.project), url.PathEscape(saEmail))
	notFound, conflicts := 0, 0
	for {
		var policy iamPolicy
		err := r.doJSONRequest(ctx, http.MethodPost, saURL+":getIamPolicy",
			map[string]any{"options": map[string]any{"requestedPolicyVersion": 3}}, &policy)
		if err == nil {
			if policy.hasUnconditionalMember(roleServiceAccountUser, principal) {
				return nil
			}
			policy.upsertConditionalBinding(roleServiceAccountUser, principal, nil)
			err = r.doJSONRequest(ctx, http.MethodPost, saURL+":setIamPolicy",
				map[string]any{"policy": policy}, nil)
			if err == nil {
				return nil
			}
			if httpStatusOf(err) == http.StatusForbidden {
				return r.selfActAsRefused("iam.serviceAccounts.setIamPolicy", principal, saEmail, err)
			}
		} else if httpStatusOf(err) == http.StatusForbidden {
			return r.selfActAsRefused("iam.serviceAccounts.getIamPolicy", principal, saEmail, err)
		}

		var delay time.Duration
		switch httpStatusOf(err) {
		case http.StatusConflict:
			if conflicts >= len(selfActAsConflictDelays) {
				return fmt.Errorf("gcp iam: granting %s %s on %s: the account's IAM policy kept changing underneath us (%d etag conflicts): %w",
					principal, roleServiceAccountUser, saEmail, conflicts+1, err)
			}
			delay = selfActAsConflictDelays[conflicts]
			conflicts++
		case http.StatusNotFound:
			if notFound >= len(selfActAsNotFoundDelays) {
				return fmt.Errorf("gcp iam: granting %s %s on %s: the service account was still not visible to IAM after retrying: %w",
					principal, roleServiceAccountUser, saEmail, err)
			}
			delay = selfActAsNotFoundDelays[notFound]
			notFound++
		default:
			return fmt.Errorf("gcp iam: granting %s %s on %s: %w", principal, roleServiceAccountUser, saEmail, err)
		}
		if err := r.sleep(ctx, delay); err != nil {
			return err
		}
	}
}

func (r *GCPInstanceResources) selfActAsRefused(permission, principal, saEmail string, err error) error {
	return fmt.Errorf("gcp iam: the registry (%s) is missing %s on service account %s, so it cannot grant itself %s there and GCE would refuse to attach the account to the VM: "+
		"grant the registry iam.serviceAccounts.getIamPolicy and iam.serviceAccounts.setIamPolicy on the instance service accounts (%s*) -- see docs/hub.md, \"IAM the registry needs\" -- "+
		"or grant it iam.serviceAccounts.actAs on them yourself and set compute.grant_self_act_as: false: %w",
		principal, permission, saEmail, roleServiceAccountUser, r.saPrefix, err)
}

func (r *GCPInstanceResources) resolveSelfPrincipal(ctx context.Context) (string, error) {
	r.selfPrincipalMu.Lock()
	defer r.selfPrincipalMu.Unlock()
	if r.selfPrincipal != "" {
		return r.selfPrincipal, nil
	}
	if r.principalResolver == nil {
		return "", errors.New("no principal resolver is configured")
	}
	p, err := r.principalResolver(ctx)
	if err != nil {
		return "", err
	}
	p, err = normalizeSelfPrincipal(p)
	if err != nil {
		return "", err
	}
	if p == "" {
		return "", errors.New("detected an empty principal")
	}
	r.selfPrincipal = p
	return p, nil
}

func (p *iamPolicy) hasUnconditionalMember(role, member string) bool {
	for _, b := range p.Bindings {
		if b.Role != role || b.Condition != nil {
			continue
		}
		for _, m := range b.Members {
			if strings.EqualFold(m, member) {
				return true
			}
		}
	}
	return false
}

// normalizeSelfPrincipal validates an IAM principal identifier, accepting a bare
// service account email as shorthand for "serviceAccount:<email>".
func normalizeSelfPrincipal(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", nil
	}
	for _, prefix := range []string{"serviceAccount:", "user:", "group:", "principal://", "principalSet://"} {
		if strings.HasPrefix(p, prefix) {
			if strings.TrimSpace(strings.TrimPrefix(p, prefix)) == "" {
				return "", fmt.Errorf("principal %q names no identity", raw)
			}
			return p, nil
		}
	}
	if !strings.Contains(p, ":") && strings.HasSuffix(strings.ToLower(p), ".gserviceaccount.com") {
		return "serviceAccount:" + p, nil
	}
	return "", fmt.Errorf("principal %q is not an IAM principal identifier (want \"serviceAccount:<email>\", \"user:<email>\" or \"principal://...\")", raw)
}

// adcPrincipalDetector works out which IAM principal the registry's Application
// Default Credentials authenticate as:
//   - a service account key, or credentials impersonating a service account:
//     "serviceAccount:<email>" from the credentials file;
//   - the metadata server (GCE, or GKE Workload Identity with a Google service
//     account bound to the pod's Kubernetes service account): its default
//     account's email;
//   - GKE Workload Identity without a Google service account, where the
//     metadata server's email is the pool name "<project>.svc.id.goog": the
//     "principal://.../subject/ns/<namespace>/sa/<ksa>" form, built from the
//     project number and the pod's own Kubernetes service account token;
//   - anything else (for example gcloud user credentials): the email the
//     access token's tokeninfo reports.
type adcPrincipalDetector struct {
	metadataBaseURL string
	k8sSADir        string
	tokenInfoURL    string
	httpClient      *http.Client
	findCredentials func(ctx context.Context) (*google.Credentials, error)
}

func newADCPrincipalDetector() *adcPrincipalDetector {
	host := strings.TrimSpace(os.Getenv("GCE_METADATA_HOST"))
	if host == "" {
		host = "metadata.google.internal"
	}
	return &adcPrincipalDetector{
		metadataBaseURL: "http://" + host + "/computeMetadata/v1",
		k8sSADir:        "/var/run/secrets/kubernetes.io/serviceaccount",
		tokenInfoURL:    "https://oauth2.googleapis.com/tokeninfo",
		httpClient:      &http.Client{Timeout: 5 * time.Second},
		findCredentials: func(ctx context.Context) (*google.Credentials, error) {
			return google.FindDefaultCredentials(ctx, cloudPlatformScope)
		},
	}
}

func (d *adcPrincipalDetector) detect(ctx context.Context) (string, error) {
	creds, credErr := d.findCredentials(ctx)
	if credErr == nil && creds != nil && len(creds.JSON) > 0 {
		if p := principalFromCredentialsJSON(creds.JSON); p != "" {
			return p, nil
		}
		return d.fromTokenInfo(ctx, creds.TokenSource)
	}
	email, mdErr := d.metadata(ctx, "instance/service-accounts/default/email")
	if mdErr == nil {
		return d.principalForMetadataEmail(ctx, email)
	}
	if credErr == nil && creds != nil && creds.TokenSource != nil {
		if p, err := d.fromTokenInfo(ctx, creds.TokenSource); err == nil {
			return p, nil
		}
	}
	if credErr != nil {
		return "", fmt.Errorf("no Application Default Credentials (%v) and no metadata server (%v)", credErr, mdErr)
	}
	return "", fmt.Errorf("reading the default service account from the metadata server: %v", mdErr)
}

func principalFromCredentialsJSON(b []byte) string {
	var f struct {
		Type                           string `json:"type"`
		ClientEmail                    string `json:"client_email"`
		ServiceAccountImpersonationURL string `json:"service_account_impersonation_url"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return ""
	}
	if f.Type == "service_account" && strings.TrimSpace(f.ClientEmail) != "" {
		return "serviceAccount:" + strings.TrimSpace(f.ClientEmail)
	}
	// impersonated_service_account and external_account files name the account
	// they impersonate as .../serviceAccounts/<email>:generateAccessToken.
	if u := f.ServiceAccountImpersonationURL; u != "" {
		if i := strings.LastIndex(u, "/serviceAccounts/"); i >= 0 {
			email := strings.TrimSuffix(u[i+len("/serviceAccounts/"):], ":generateAccessToken")
			if email, err := url.PathUnescape(email); err == nil && strings.Contains(email, "@") {
				return "serviceAccount:" + email
			}
		}
	}
	return ""
}

func (d *adcPrincipalDetector) principalForMetadataEmail(ctx context.Context, email string) (string, error) {
	email = strings.TrimSpace(email)
	if pool := email; strings.HasSuffix(pool, ".svc.id.goog") {
		number, err := d.metadata(ctx, "project/numeric-project-id")
		if err != nil {
			return "", fmt.Errorf("running under Workload Identity pool %s but reading the project number from the metadata server failed: %v", pool, err)
		}
		ns, ksa, err := d.kubernetesServiceAccount()
		if err != nil {
			return "", fmt.Errorf("running under Workload Identity pool %s but could not tell which Kubernetes service account this pod runs as: %v", pool, err)
		}
		return fmt.Sprintf("principal://iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s/subject/ns/%s/sa/%s",
			strings.TrimSpace(number), pool, ns, ksa), nil
	}
	if !strings.Contains(email, "@") {
		return "", fmt.Errorf("metadata server reported default service account %q, which is not an email", email)
	}
	return "serviceAccount:" + email, nil
}

func (d *adcPrincipalDetector) metadata(ctx context.Context, path string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.metadataBaseURL+"/"+path, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", fmt.Errorf("GET %s: empty response", path)
	}
	return v, nil
}

// kubernetesServiceAccount reads the namespace and name of the pod's own
// Kubernetes service account from its mounted token's "sub" claim
// ("system:serviceaccount:<namespace>:<name>"). The token is only decoded, not
// verified: it is our own credential, read for its name.
func (d *adcPrincipalDetector) kubernetesServiceAccount() (string, string, error) {
	tok, err := os.ReadFile(filepath.Join(d.k8sSADir, "token"))
	if err != nil {
		return "", "", err
	}
	parts := strings.Split(strings.TrimSpace(string(tok)), ".")
	if len(parts) != 3 {
		return "", "", errors.New("mounted service account token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", "", fmt.Errorf("decoding service account token: %w", err)
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", "", fmt.Errorf("decoding service account token claims: %w", err)
	}
	f := strings.Split(claims.Sub, ":")
	if len(f) != 4 || f[0] != "system" || f[1] != "serviceaccount" || f[2] == "" || f[3] == "" {
		return "", "", fmt.Errorf("service account token subject %q is not system:serviceaccount:<namespace>:<name>", claims.Sub)
	}
	return f[2], f[3], nil
}

func (d *adcPrincipalDetector) fromTokenInfo(ctx context.Context, ts oauth2.TokenSource) (string, error) {
	if ts == nil {
		return "", errors.New("no token source to ask tokeninfo about")
	}
	tok, err := ts.Token()
	if err != nil {
		return "", fmt.Errorf("minting an access token: %w", err)
	}
	// The token goes in a POST body, never the URL: a transport error (*url.Error)
	// quotes the request URL, and this error ends up in an entry's status_reason.
	form := url.Values{"access_token": {tok.AccessToken}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.tokenInfoURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("tokeninfo: %w", err)
	}
	defer resp.Body.Close()
	var info struct {
		Email string `json:"email"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tokeninfo: HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(b, &info); err != nil || strings.TrimSpace(info.Email) == "" {
		return "", errors.New("tokeninfo reported no email for the registry's credentials")
	}
	email := strings.TrimSpace(info.Email)
	if strings.HasSuffix(strings.ToLower(email), ".gserviceaccount.com") {
		return "serviceAccount:" + email, nil
	}
	return "user:" + email, nil
}
