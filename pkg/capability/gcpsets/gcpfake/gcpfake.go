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

// Package gcpfake is an in-memory IAM and Resource Manager for gcpsets'
// tests: service accounts, their keys (with GCP's ten-key cap), project
// and bucket IAM policies with etags, and testIamPermissions.
package gcpfake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/gcpkey"
	"github.com/ai-on-gke/grain/pkg/capability/gcpsets"
)

// Cloud is the fake GCP. It is also a gcpsets.Connector: every admin
// connects to the same Cloud, and Identities says who each one is.
type Cloud struct {
	mu       sync.Mutex
	Accounts map[string]map[string]gcpsets.ServiceAccount // project -> email -> account
	Keys     map[string][]gcpkey.KeyInfo                  // email -> keys
	Policies map[string]*gcpsets.Policy                   // resource -> policy
	// Granted is, by project, the permissions TestPermissions reports;
	// a project missing from it has all of them.
	Granted map[string][]string
	// Identities is, by admin name, what Identity reports.
	Identities map[string]string
	// DefaultIdentity is what Identity reports for an admin Identities
	// does not name; empty makes that an error.
	DefaultIdentity string
	// ConflictOnce makes the next SetPolicy lose a race.
	ConflictOnce bool
	Now          func() time.Time
	// ListErrs are what the next ListServiceAccounts calls fail with, one
	// each, as a token fetch failing underneath them would.
	ListErrs []error
	// Connects counts Connect calls.
	Connects int
	// Calls counts mutating calls, by name, and the IAM Credentials
	// calls (GenerateAccessToken, GenerateIDToken).
	Calls map[string]int
	// AccountGranted is, by service account email, the permissions
	// TestAccountPermissions reports and the impersonation calls check
	// (iam.serviceAccounts.getAccessToken, getOpenIdToken); an account
	// missing from it grants all of them unless RequireTokenCreator is set.
	AccountGranted map[string][]string
	// RequireTokenCreator makes GenerateAccessToken, GenerateIDToken and
	// TestAccountPermissions require roles/iam.serviceAccountTokenCreator
	// on projects/<p>/serviceAccounts/<email> in Policies (unless
	// AccountGranted explicitly grants the permission).
	RequireTokenCreator bool
	// Denied names permissions that operations and TestPermissions refuse
	// with 403 PERMISSION_DENIED.
	Denied map[string]bool
	// DisableKeyCreation marks projects that enforce
	// constraints/iam.disableServiceAccountKeyCreation on CreateKey.
	DisableKeyCreation map[string]bool
	// Lifetimes are the lifetimes GenerateAccessToken was asked for.
	Lifetimes []time.Duration
	n         int
}

// New is an empty fake.
func New() *Cloud {
	return &Cloud{
		Accounts:   map[string]map[string]gcpsets.ServiceAccount{},
		Keys:       map[string][]gcpkey.KeyInfo{},
		Policies:   map[string]*gcpsets.Policy{},
		Identities: map[string]string{},
		Calls:      map[string]int{},
		Now:        time.Now,
	}
}

// Connect implements gcpsets.Connector.
func (c *Cloud) Connect(_ context.Context, a gcpsets.Admin) (gcpsets.Cloud, error) {
	if a.CredentialSource == gcpsets.SourceConfigured && a.Credential == "" {
		return nil, errors.New("no credential")
	}
	c.mu.Lock()
	c.Connects++
	c.mu.Unlock()
	return c, nil
}

// Identity implements gcpsets.Connector.
func (c *Cloud) Identity(_ context.Context, a gcpsets.Admin) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id, ok := c.Identities[a.Name]; ok {
		return id, nil
	}
	if c.DefaultIdentity != "" {
		return c.DefaultIdentity, nil
	}
	if a.Credential != "" {
		var doc struct {
			ClientEmail string `json:"client_email"`
		}
		if json.Unmarshal([]byte(a.Credential), &doc) == nil && doc.ClientEmail != "" {
			return doc.ClientEmail, nil
		}
	}
	return "", fmt.Errorf("admin %s: no identity", a.Name)
}

// AddAccount puts an account in project, as someone other than the
// capability under test might have.
func (c *Cloud) AddAccount(project, email, description string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Accounts[project] == nil {
		c.Accounts[project] = map[string]gcpsets.ServiceAccount{}
	}
	c.Accounts[project][email] = gcpsets.ServiceAccount{Email: email, Description: description}
}

// AddKey gives email a key created at t.
func (c *Cloud) AddKey(email, id string, t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Keys[email] = append(c.Keys[email], gcpkey.KeyInfo{ID: id, CreatedAt: t})
}

// KeyCount is how many keys email has.
func (c *Cloud) KeyCount(email string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.Keys[email])
}

// LiveKeys is how many keys exist across every account.
func (c *Cloud) LiveKeys() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, keys := range c.Keys {
		n += len(keys)
	}
	return n
}

// Members is the members bound to role on resource.
func (c *Cloud) Members(resource, role string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.Policies[resource]
	if p == nil {
		return nil
	}
	for _, b := range p.Bindings {
		if b.Role == role {
			return append([]string(nil), b.Members...)
		}
	}
	return nil
}

// Bind adds member to role on resource directly.
func (c *Cloud) Bind(resource, role, member string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.policy(resource)
	for i, b := range p.Bindings {
		if b.Role == role {
			p.Bindings[i].Members = append(b.Members, member)
			p.Etag = c.etag()
			return
		}
	}
	p.Bindings = append(p.Bindings, gcpsets.Binding{Role: role, Members: []string{member}})
	p.Etag = c.etag()
}

func (c *Cloud) etag() string { c.n++; return fmt.Sprintf("etag-%d", c.n) }

func (c *Cloud) policy(resource string) *gcpsets.Policy {
	p := c.Policies[resource]
	if p == nil {
		p = &gcpsets.Policy{Etag: c.etag()}
		c.Policies[resource] = p
	}
	return p
}

func project(account string) (string, string) {
	rest := strings.TrimPrefix(account, "projects/")
	p, email, _ := strings.Cut(rest, "/serviceAccounts/")
	return p, email
}

func (c *Cloud) exists(account string) (string, bool) {
	p, email := project(account)
	_, ok := c.Accounts[p][email]
	return email, ok
}

func (c *Cloud) denyErr(perm, resource string) error {
	if c.Denied[perm] {
		return fmt.Errorf("googleapi: Error 403: Permission '%s' denied on resource '%s' (or it may not exist), PERMISSION_DENIED", perm, resource)
	}
	return nil
}

func policyPerms(resource string) (getPerm, setPerm string) {
	if strings.HasPrefix(resource, "buckets/") {
		return "storage.buckets.getIamPolicy", "storage.buckets.setIamPolicy"
	}
	if strings.HasPrefix(resource, "projects/") && strings.Contains(resource, "/locations/") && strings.Contains(resource, "/repositories/") {
		return "artifactregistry.repositories.getIamPolicy", "artifactregistry.repositories.setIamPolicy"
	}
	if strings.HasPrefix(resource, "projects/") && strings.Contains(resource, "/serviceAccounts/") {
		return "iam.serviceAccounts.getIamPolicy", "iam.serviceAccounts.setIamPolicy"
	}
	return "resourcemanager.projects.getIamPolicy", "resourcemanager.projects.setIamPolicy"
}

func (c *Cloud) CreateKey(_ context.Context, account string) (string, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.denyErr("iam.serviceAccountKeys.create", account); err != nil {
		return "", "", err
	}
	p, _ := project(account)
	email, ok := c.exists(account)
	if !ok {
		return "", "", fmt.Errorf("404 NOT_FOUND: %s", account)
	}
	if c.DisableKeyCreation[p] {
		return "", "", fmt.Errorf("googleapi: Error 400: Key creation is not allowed on this service account. [constraints/iam.disableServiceAccountKeyCreation], FAILED_PRECONDITION")
	}
	if len(c.Keys[email]) >= gcpsets.KeysPerAccount {
		return "", "", fmt.Errorf("FAILED_PRECONDITION: %s has the maximum number of keys", email)
	}
	c.n++
	id := fmt.Sprintf("key%d", c.n)
	c.Keys[email] = append(c.Keys[email], gcpkey.KeyInfo{ID: id, CreatedAt: c.Now()})
	c.Calls["CreateKey"]++
	return id, `{"type":"service_account","client_email":"` + email + `","private_key_id":"` + id + `"}`, nil
}

func (c *Cloud) DeleteKey(_ context.Context, account, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, email := project(account)
	keys := c.Keys[email]
	for i, k := range keys {
		if k.ID == id {
			c.Keys[email] = append(keys[:i:i], keys[i+1:]...)
			c.Calls["DeleteKey"]++
			return nil
		}
	}
	return fmt.Errorf("404 NOT_FOUND: key %s", id)
}

func (c *Cloud) ListKeys(_ context.Context, account string) ([]gcpkey.KeyInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, email := project(account)
	return append([]gcpkey.KeyInfo(nil), c.Keys[email]...), nil
}

func (c *Cloud) ListServiceAccounts(_ context.Context, p string) ([]gcpsets.ServiceAccount, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.denyErr("iam.serviceAccounts.list", "projects/"+p); err != nil {
		return nil, err
	}
	if len(c.ListErrs) > 0 {
		err := c.ListErrs[0]
		c.ListErrs = c.ListErrs[1:]
		return nil, err
	}
	var out []gcpsets.ServiceAccount
	for _, sa := range c.Accounts[p] {
		out = append(out, sa)
	}
	return out, nil
}

func (c *Cloud) CreateServiceAccount(_ context.Context, p, id, _, description string) (gcpsets.ServiceAccount, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.denyErr("iam.serviceAccounts.create", "projects/"+p); err != nil {
		return gcpsets.ServiceAccount{}, err
	}
	if len(id) < 6 || len(id) > 30 {
		return gcpsets.ServiceAccount{}, fmt.Errorf("400: account id %q is not 6-30 characters", id)
	}
	email := id + "@" + p + ".iam.gserviceaccount.com"
	if c.Accounts[p] == nil {
		c.Accounts[p] = map[string]gcpsets.ServiceAccount{}
	}
	if _, ok := c.Accounts[p][email]; ok {
		return gcpsets.ServiceAccount{}, fmt.Errorf("409: %s exists", email)
	}
	sa := gcpsets.ServiceAccount{Email: email, Description: description}
	c.Accounts[p][email] = sa
	c.Calls["CreateServiceAccount"]++
	return sa, nil
}

func (c *Cloud) DeleteServiceAccount(_ context.Context, p, email string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.denyErr("iam.serviceAccounts.delete", "projects/"+p+"/serviceAccounts/"+email); err != nil {
		return err
	}
	if _, ok := c.Accounts[p][email]; !ok {
		return fmt.Errorf("404 NOT_FOUND: %s", email)
	}
	delete(c.Accounts[p], email)
	delete(c.Policies, "projects/"+p+"/serviceAccounts/"+email)
	c.Calls["DeleteServiceAccount"]++
	return nil
}

func (c *Cloud) GetPolicy(_ context.Context, resource string) (*gcpsets.Policy, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	getPerm, _ := policyPerms(resource)
	if err := c.denyErr(getPerm, resource); err != nil {
		return nil, err
	}
	p := c.policy(resource)
	out := &gcpsets.Policy{Etag: p.Etag}
	for _, b := range p.Bindings {
		out.Bindings = append(out.Bindings, gcpsets.Binding{Role: b.Role, Members: append([]string(nil), b.Members...)})
	}
	return out, nil
}

func (c *Cloud) SetPolicy(_ context.Context, resource string, in *gcpsets.Policy) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, setPerm := policyPerms(resource)
	if err := c.denyErr(setPerm, resource); err != nil {
		return err
	}
	p := c.policy(resource)
	if c.ConflictOnce {
		c.ConflictOnce = false
		p.Etag = c.etag()
		return gcpsets.ErrConflict
	}
	if in.Etag != p.Etag {
		return gcpsets.ErrConflict
	}
	p.Bindings = in.Bindings
	p.Etag = c.etag()
	c.Calls["SetPolicy"]++
	return nil
}

func (c *Cloud) filterDenied(perms []string) []string {
	if len(c.Denied) == 0 {
		return perms
	}
	var out []string
	for _, p := range perms {
		if !c.Denied[p] {
			out = append(out, p)
		}
	}
	return out
}

func (c *Cloud) TestPermissions(_ context.Context, p string, perms []string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	granted, ok := c.Granted[p]
	if !ok {
		return c.filterDenied(perms), nil
	}
	var out []string
	for _, w := range perms {
		if c.Denied[w] {
			continue
		}
		for _, g := range granted {
			if g == w {
				out = append(out, w)
			}
		}
	}
	return out, nil
}

func (c *Cloud) TestResourcePermissions(_ context.Context, resource string, perms []string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	granted, ok := c.Granted[resource]
	if !ok {
		return nil, nil
	}
	var out []string
	for _, w := range perms {
		if c.Denied[w] {
			continue
		}
		for _, g := range granted {
			if g == w {
				out = append(out, w)
			}
		}
	}
	return out, nil
}

func (c *Cloud) accountAllows(email, perm string) bool {
	if c.Denied[perm] {
		return false
	}
	if granted, ok := c.AccountGranted[email]; ok {
		for _, g := range granted {
			if g == perm {
				return true
			}
		}
		return false
	}
	if !c.RequireTokenCreator {
		return true
	}
	if perm != "iam.serviceAccounts.getAccessToken" && perm != "iam.serviceAccounts.getOpenIdToken" {
		return false
	}
	if p, ok := gcpsets.AccountProject(email); ok {
		if pol := c.Policies["projects/"+p+"/serviceAccounts/"+email]; pol != nil {
			for _, b := range pol.Bindings {
				if b.Role == "roles/iam.serviceAccountTokenCreator" && len(b.Members) > 0 {
					return true
				}
			}
		}
	}
	return false
}

// GenerateAccessToken implements gcpsets.Impersonator: the token is
// "tok-<n>-<email>".
func (c *Cloud) GenerateAccessToken(_ context.Context, email string, _ []string, lifetime time.Duration) (string, time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.accountAllows(email, "iam.serviceAccounts.getAccessToken") {
		return "", time.Time{}, fmt.Errorf("googleapi: Error 403: Permission 'iam.serviceAccounts.getAccessToken' denied on resource %s, PERMISSION_DENIED", email)
	}
	if lifetime > gcpsets.MaxTokenLifetime {
		return "", time.Time{}, fmt.Errorf("googleapi: Error 400: The lifetime %s is past the maximum of %s, INVALID_ARGUMENT", lifetime, gcpsets.MaxTokenLifetime)
	}
	c.n++
	c.Calls["GenerateAccessToken"]++
	c.Lifetimes = append(c.Lifetimes, lifetime)
	return fmt.Sprintf("tok-%d-%s", c.n, email), c.Now().Add(lifetime), nil
}

// GenerateIDToken implements gcpsets.Impersonator: the token is
// "id-<audience>-<email>".
func (c *Cloud) GenerateIDToken(_ context.Context, email, audience string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.accountAllows(email, "iam.serviceAccounts.getOpenIdToken") {
		return "", fmt.Errorf("googleapi: Error 403: Permission 'iam.serviceAccounts.getOpenIdToken' denied on resource %s, PERMISSION_DENIED", email)
	}
	c.Calls["GenerateIDToken"]++
	return "id-" + audience + "-" + email, nil
}

// TestAccountPermissions implements gcpsets.Impersonator.
func (c *Cloud) TestAccountPermissions(_ context.Context, email string, perms []string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, p := range perms {
		if c.accountAllows(email, p) {
			out = append(out, p)
		}
	}
	return out, nil
}
