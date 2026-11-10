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
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/api/googleapi"
)

// KeysPerAccount is GCP's cap on user-managed keys per service account.
// The pool spreads keys across accounts so no one reaches it.
const KeysPerAccount = 10

// Grace is how long past a lease's deadline the reaper waits before it
// deletes a key nobody released.
const Grace = 5 * time.Minute

// Lease is one attempt's key.
type Lease struct {
	Set      string
	Account  string
	KeyID    string
	Deadline time.Time
}

// Refusal is a mint the configuration does not allow.
type Refusal struct{ Reason string }

func (r *Refusal) Error() string { return r.Reason }

// Exhausted is a mint the pool cannot serve now: every account it may
// have is at KeysPerAccount.
type Exhausted struct {
	Reason     string
	RetryAfter time.Time
}

func (e *Exhausted) Error() string { return e.Reason }

// Manager runs one configuration: it holds the admins' Clouds and every
// lease the server has minted.
type Manager struct {
	Config    *Config
	Connector Connector
	Now       func() time.Time

	mu              sync.Mutex
	clouds          map[string]Cloud
	leases          map[string]Lease
	setMu           map[string]*sync.Mutex
	managedAccounts map[string]string
}

// NewManager is a Manager for cfg.
func NewManager(cfg *Config, conn Connector) *Manager {
	return &Manager{
		Config:          cfg,
		Connector:       conn,
		Now:             time.Now,
		clouds:          map[string]Cloud{},
		leases:          map[string]Lease{},
		setMu:           map[string]*sync.Mutex{},
		managedAccounts: map[string]string{},
	}
}

func (m *Manager) cloud(ctx context.Context, admin string) (Cloud, error) {
	m.mu.Lock()
	c := m.clouds[admin]
	m.mu.Unlock()
	if c != nil {
		return c, nil
	}
	a := m.Config.Admin(admin)
	if a == nil {
		return nil, fmt.Errorf("no admin is called %q", admin)
	}
	c, err := m.Connector.Connect(ctx, *a)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.clouds[admin] = c
	m.mu.Unlock()
	return c, nil
}

func (m *Manager) lockSet(name string) func() {
	m.mu.Lock()
	l := m.setMu[name]
	if l == nil {
		l = &sync.Mutex{}
		m.setMu[name] = l
	}
	m.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// Accounts lists a set's service accounts, by n: those this deployment
// created for it, then -- for a set with adopt_legacy -- the accounts it
// had under the old grain-<set>-<n> names; or, for a migrated set, the
// accounts it was given.
func (m *Manager) Accounts(ctx context.Context, set *PermissionSet) ([]string, error) {
	mine, _, err := m.listAccounts(ctx, set)
	return mine, err
}

// listAccounts is Accounts and every account in the set's project, which
// a new account's name must not collide with.
func (m *Manager) listAccounts(ctx context.Context, set *PermissionSet) ([]string, []ServiceAccount, error) {
	if set.Existing() {
		return []string{set.ServiceAccount}, nil, nil
	}
	if len(set.Accounts) > 0 {
		return append([]string(nil), set.Accounts...), nil, nil
	}
	c, err := m.cloud(ctx, set.Admin)
	if err != nil {
		return nil, nil, err
	}
	all, err := c.ListServiceAccounts(ctx, set.Project)
	if err != nil {
		return nil, nil, explainDenied(set.Admin, "iam.serviceAccounts.list", "project "+set.Project, "", err)
	}
	return m.Config.ownedAccounts(all, set), all, nil
}

// ownedAccounts are the accounts in all that are set's: carrying this
// deployment's marker for it (Ours), whatever deployment id or prefix
// they were named under, or -- with adopt_legacy -- the old marker and
// the old name.
func (c *Config) ownedAccounts(all []ServiceAccount, set *PermissionSet) []string {
	type acct struct {
		legacy bool
		n      int
		email  string
	}
	var mine []acct
	for _, sa := range all {
		o, ok := OwnedBy(sa.Description)
		if !ok || o.Set != set.Name {
			continue
		}
		n := accountNumber(sa.Email)
		id, _, _ := strings.Cut(sa.Email, "@")
		switch {
		case c.Ours(o):
			mine = append(mine, acct{false, n, sa.Email})
		case o.Legacy && set.AdoptLegacy && n > 0 && LegacyAccountID(set.Name, n) == id:
			mine = append(mine, acct{true, n, sa.Email})
		}
	}
	sort.Slice(mine, func(i, j int) bool {
		a, b := mine[i], mine[j]
		if a.legacy != b.legacy {
			return !a.legacy
		}
		if a.n != b.n {
			return a.n < b.n
		}
		return a.email < b.email
	})
	out := make([]string, 0, len(mine))
	for _, a := range mine {
		out = append(out, a.email)
	}
	return out
}

// Mint creates a key for one of set's service accounts for attemptID,
// creating the account first when the pool has room and needs one, and
// returns the key's JSON. The key is the caller's to place; it is a
// secret.
func (m *Manager) Mint(ctx context.Context, attemptID, setName string, deadline time.Time) (string, Lease, error) {
	keyJSON, l, err := m.mint(ctx, attemptID, setName, deadline)
	if _, ok := AsCredentialError(err); ok {
		// The next mint connects afresh: a person who logs in again on the
		// host, or replaces the credential, is not left with these.
		if set := m.Config.Set(setName); set != nil {
			m.forget(set.Admin)
		}
	}
	return keyJSON, l, err
}

// forget drops admin's Cloud, so the next call reads its credentials
// again.
func (m *Manager) forget(admin string) {
	m.mu.Lock()
	delete(m.clouds, admin)
	m.mu.Unlock()
}

func (m *Manager) mint(ctx context.Context, attemptID, setName string, deadline time.Time) (string, Lease, error) {
	set := m.Config.Set(setName)
	if set == nil {
		return "", Lease{}, &Refusal{"no permission set is called " + setName}
	}
	now := m.Now()
	if limit := now.Add(set.MaxLease); deadline.After(limit) {
		return "", Lease{}, &Refusal{fmt.Sprintf("deadline %s is past permission set %s's max_lease of %s", deadline.UTC().Format(time.RFC3339), set.Name, set.MaxLease)}
	}
	defer m.lockSet(set.Name)()
	c, err := m.cloud(ctx, set.Admin)
	if err != nil {
		return "", Lease{}, err
	}
	accounts, all, err := m.listAccounts(ctx, set)
	if err != nil {
		return "", Lease{}, err
	}
	if set.TokenMode() {
		account := set.ServiceAccount
		if set.ManagedToken() {
			account, err = m.ensureTokenAccount(ctx, c, set, accounts, all)
			if err != nil {
				return "", Lease{}, err
			}
		}
		return m.mintToken(ctx, c, attemptID, set, account, deadline)
	}
	if !set.fixed() && len(accounts) > 0 {
		if err := m.reconcileSetWithAll(ctx, c, set, accounts, all); err != nil {
			return "", Lease{}, err
		}
	}
	tried := map[string]bool{}
	for {
		account, err := m.pick(ctx, c, set, accounts, tried)
		if err != nil {
			return "", Lease{}, err
		}
		if account == "" {
			if set.fixed() || len(accounts) >= set.PoolMax {
				return "", Lease{}, m.exhausted(set, now)
			}
			account, err = m.createAccount(ctx, c, set, accounts, all)
			if err != nil {
				return "", Lease{}, err
			}
			accounts = append(accounts, account)
			all = append(all, ServiceAccount{Email: account, Description: OwnerDescriptionWithScope(m.Config.Deployment, m.Config.Instance, set.Name, set.ResourceScope)})
		}
		tried[account] = true
		id, keyJSON, err := c.CreateKey(ctx, "projects/"+set.Project+"/serviceAccounts/"+account)
		if err != nil {
			if IsKeyCreationDisabled(err) {
				return "", Lease{}, explainKeyCreationDisabled(set, account, err)
			}
			if isKeyQuota(err) {
				// Someone else's keys filled it between List and Create:
				// try the next account.
				continue
			}
			return "", Lease{}, explainDenied(set.Admin, "iam.serviceAccountKeys.create", "service account "+account, "", err)
		}
		l := Lease{Set: set.Name, Account: account, KeyID: id, Deadline: deadline}
		m.mu.Lock()
		m.leases[attemptID] = l
		m.mu.Unlock()
		return keyJSON, l, nil
	}
}

// IsKeyCreationDisabled reports whether err is GCP refusing user-managed key
// creation under constraints/iam.disableServiceAccountKeyCreation.
func IsKeyCreationDisabled(err error) bool {
	if err == nil {
		return false
	}
	detail := err.Error()
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		detail += "\n" + apiErr.Body
	}
	return strings.Contains(detail, "iam.disableServiceAccountKeyCreation") ||
		strings.Contains(detail, "Key creation is not allowed")
}

func explainKeyCreationDisabled(set *PermissionSet, account string, err error) error {
	return &Refusal{
		Reason: fmt.Sprintf(
			"permission set %s: GCP refuses to create service-account keys for %s in project %s because organization policy constraints/iam.disableServiceAccountKeyCreation forbids user-managed keys. "+
				"Switch permission set %s to token mode (no keys created; grain creates and binds the service account and grants the admin roles/iam.serviceAccountTokenCreator on it, or impersonates service_account if set) in Settings → Capabilities → gcp instead of pool/key mode:\n%s\n(cause: %v)",
			set.Name, account, set.Project, set.Name, set.TokenModeConfigJSON(), err),
	}
}

// explainDenied wraps a 403 IAM denial with the exact permission and resource
// the admin lacked so failures are actionable even when GCP's own 403 message
// says only "The caller does not have permission".
func explainDenied(admin, perm, target, hint string, err error) error {
	if err == nil || !IsDenied(err) {
		return err
	}
	if strings.Contains(err.Error(), perm) && strings.Contains(err.Error(), "admin "+admin) {
		return err
	}
	extra := ""
	if hint != "" {
		extra = " (" + hint + ")"
	}
	return fmt.Errorf("admin %s lacks permission %s on %s%s: %w", admin, perm, target, extra, err)
}

// IsDenied reports whether err is IAM refusing the admin permission: a
// real denial, which only a grant on the project clears.
func IsDenied(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		return apiErr.Code == http.StatusForbidden
	}
	s := err.Error()
	return strings.Contains(s, "PERMISSION_DENIED") || strings.Contains(s, "Error 403") || strings.Contains(s, "is not permitted to")
}

func isKeyQuota(err error) bool {
	s := err.Error()
	return strings.Contains(s, "FAILED_PRECONDITION") || strings.Contains(s, "maximum number of keys") || strings.Contains(s, "10 user-managed keys")
}

// pick is the account in accounts with the fewest user-managed keys,
// skipping full ones and those already tried; "" if none has room.
func (m *Manager) pick(ctx context.Context, c Cloud, set *PermissionSet, accounts []string, tried map[string]bool) (string, error) {
	best, bestN := "", KeysPerAccount
	for _, a := range accounts {
		if tried[a] {
			continue
		}
		keys, err := c.ListKeys(ctx, "projects/"+set.Project+"/serviceAccounts/"+a)
		if err != nil {
			return "", err
		}
		if len(keys) < bestN {
			best, bestN = a, len(keys)
		}
	}
	return best, nil
}

func (m *Manager) exhausted(set *PermissionSet, now time.Time) error {
	next := time.Time{}
	m.mu.Lock()
	for _, l := range m.leases {
		if l.Set == set.Name && (next.IsZero() || l.Deadline.Before(next)) {
			next = l.Deadline
		}
	}
	m.mu.Unlock()
	if next.IsZero() || next.Before(now) {
		next = now.Add(time.Minute)
	}
	return &Exhausted{
		Reason:     fmt.Sprintf("permission set %s: all %d service account(s) its pool_max allows hold %d keys, GCP's limit per account; raise pool_max or wait for a key to be released", set.Name, set.PoolMax, KeysPerAccount),
		RetryAfter: next,
	}
}

// maxNameTries bounds how many names createAccount tries past ones
// another deployment -- or an account nobody marked -- already holds.
const maxNameTries = 20

// createAccount creates set's next account and binds its roles: named
// under the deployment's current id and prefix (AccountID), with the
// lowest n whose name no account in the project already has. A name
// taken meanwhile is skipped too, never shared.
func (m *Manager) createAccount(ctx context.Context, c Cloud, set *PermissionSet, accounts []string, all []ServiceAccount) (string, error) {
	taken := map[string]bool{}
	for _, sa := range all {
		taken[sa.Email] = true
	}
	n := 0
	for try := 0; ; try++ {
		n++
		id := m.Config.AccountID(set, n)
		if taken[Email(id, set.Project)] {
			continue
		}
		sa, err := c.CreateServiceAccount(ctx, set.Project, id, "grain "+m.Config.Deployment+" "+set.Name+" "+strconv.Itoa(n), OwnerDescriptionWithScope(m.Config.Deployment, m.Config.Instance, set.Name, set.ResourceScope))
		if err != nil {
			if isAlreadyExists(err) && try < maxNameTries {
				taken[Email(id, set.Project)] = true
				continue
			}
			return "", explainDenied(set.Admin, "iam.serviceAccounts.create", "project "+set.Project, "grant roles/iam.serviceAccountAdmin or custom grainGcpKeyAdmin", err)
		}
		if err := m.reconcileSetWithAll(ctx, c, set, append(append([]string(nil), accounts...), sa.Email), all); err != nil {
			return "", err
		}
		return sa.Email, nil
	}
}

// iamMember formats an identity email as a GCP IAM policy member string.
func iamMember(identity string) string {
	if strings.Contains(identity, ":") {
		return identity
	}
	if strings.HasSuffix(identity, ".gserviceaccount.com") {
		return "serviceAccount:" + identity
	}
	return "user:" + identity
}

func (m *Manager) adminIAMMember(ctx context.Context, adminName string) (string, error) {
	a := m.Config.Admin(adminName)
	if a == nil {
		return "", fmt.Errorf("no admin is called %q", adminName)
	}
	idCtx, cancel := context.WithTimeout(ctx, identityTimeout)
	defer cancel()
	id, err := m.Connector.Identity(idCtx, *a)
	if err != nil {
		return "", err
	}
	return iamMember(id), nil
}

// ensureTokenAccount creates (or adopts) a managed token-mode set's service
// account, reconciles its role bindings on its project or resource_scope, and
// grants the admin roles/iam.serviceAccountTokenCreator on that account only.
func (m *Manager) ensureTokenAccount(ctx context.Context, c Cloud, set *PermissionSet, accounts []string, all []ServiceAccount) (string, error) {
	adminMember, err := m.adminIAMMember(ctx, set.Admin)
	if err != nil {
		return "", err
	}
	var account string
	if len(accounts) == 0 {
		account, err = m.createTokenAccount(ctx, c, set, all)
		if err != nil {
			return "", err
		}
	} else {
		account = accounts[0]
	}
	if err := m.reconcileSetWithAll(ctx, c, set, []string{account}, all); err != nil {
		return "", err
	}
	if err := reconcileAccountTokenCreator(ctx, c, set.Admin, set.Project, account, adminMember); err != nil {
		return "", err
	}
	m.mu.Lock()
	m.managedAccounts[set.Name] = account
	m.mu.Unlock()
	return account, nil
}

func (m *Manager) createTokenAccount(ctx context.Context, c Cloud, set *PermissionSet, all []ServiceAccount) (string, error) {
	taken := map[string]bool{}
	for _, sa := range all {
		taken[sa.Email] = true
	}
	desc := OwnerDescriptionWithScope(m.Config.Deployment, m.Config.Instance, set.Name, set.ResourceScope)
	for try := 0; ; try++ {
		var id, display string
		if try == 0 {
			id = m.Config.SingleAccountID(set)
			display = "grain " + m.Config.Deployment + " " + set.Name
		} else {
			id = m.Config.AccountID(set, try)
			display = "grain " + m.Config.Deployment + " " + set.Name + " " + strconv.Itoa(try)
		}
		if taken[Email(id, set.Project)] {
			if try >= maxNameTries {
				return "", fmt.Errorf("permission set %s: all %d candidate service account names in %s are taken", set.Name, maxNameTries, set.Project)
			}
			continue
		}
		sa, err := c.CreateServiceAccount(ctx, set.Project, id, display, desc)
		if err != nil {
			if isAlreadyExists(err) && try < maxNameTries {
				taken[Email(id, set.Project)] = true
				continue
			}
			return "", explainDenied(set.Admin, "iam.serviceAccounts.create", "project "+set.Project, "grant roles/iam.serviceAccountAdmin or custom grainGcpKeyAdmin", err)
		}
		return sa.Email, nil
	}
}

// reconcileAccountTokenCreator ensures adminMember holds
// roles/iam.serviceAccountTokenCreator on service account email (and nobody
// else holds that role on this grain-managed account), writing nothing when
// already converged.
func reconcileAccountTokenCreator(ctx context.Context, c Cloud, admin, project, email, adminMember string) error {
	const role = "roles/iam.serviceAccountTokenCreator"
	resource := "projects/" + project + "/serviceAccounts/" + email
	target := "service account " + email
	hint := "grant iam.serviceAccounts.getIamPolicy and iam.serviceAccounts.setIamPolicy, e.g. roles/iam.serviceAccountAdmin or custom grainGcpKeyAdmin"
	for try := 0; ; try++ {
		p, err := c.GetPolicy(ctx, resource)
		if err != nil {
			return explainDenied(admin, "iam.serviceAccounts.getIamPolicy", target, hint, err)
		}
		changed := false
		found := false
		var out []Binding
		for _, b := range p.Bindings {
			if b.Role != role {
				out = append(out, b)
				continue
			}
			found = true
			if len(b.Members) == 1 && b.Members[0] == adminMember {
				out = append(out, b)
				continue
			}
			out = append(out, Binding{Role: role, Members: []string{adminMember}})
			changed = true
		}
		if !found {
			out = append(out, Binding{Role: role, Members: []string{adminMember}})
			changed = true
		}
		if !changed {
			return nil
		}
		p.Bindings = out
		err = c.SetPolicy(ctx, resource, p)
		if errors.Is(err, ErrConflict) && try < 4 {
			continue
		}
		return explainDenied(admin, "iam.serviceAccounts.setIamPolicy", target, hint, err)
	}
}

func isAlreadyExists(err error) bool {
	s := err.Error()
	return strings.Contains(s, "409") || strings.Contains(s, "ALREADY_EXISTS") || strings.Contains(s, "already exists")
}

// Release deletes attemptID's key. An attempt with no key is released.
func (m *Manager) Release(ctx context.Context, attemptID string) error {
	m.mu.Lock()
	l, ok := m.leases[attemptID]
	m.mu.Unlock()
	if !ok {
		return nil
	}
	set := m.Config.Set(l.Set)
	if set == nil {
		return fmt.Errorf("lease %s names permission set %s, which is no longer configured", attemptID, l.Set)
	}
	if l.KeyID != "" {
		c, err := m.cloud(ctx, set.Admin)
		if err != nil {
			return err
		}
		if err := c.DeleteKey(ctx, "projects/"+set.Project+"/serviceAccounts/"+l.Account, l.KeyID); err != nil && !isNotFound(err) {
			return err
		}
	}
	m.mu.Lock()
	delete(m.leases, attemptID)
	m.mu.Unlock()
	return nil
}

func isNotFound(err error) bool {
	s := err.Error()
	return strings.Contains(s, "404") || strings.Contains(s, "NOT_FOUND") || strings.Contains(s, "not found")
}

// Leases is how many keys are held, by set.
func (m *Manager) Leases() map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int{}
	for _, l := range m.leases {
		out[l.Set]++
	}
	return out
}

// Reap deletes, on every account the capability created, each key no
// lease holds that is older than its set's max_lease plus Grace: the keys
// of attempts whose release never came, across a restart of the server.
// It returns the keys it deleted, as account/key.
func (m *Manager) Reap(ctx context.Context) ([]string, error) {
	held := map[string]bool{}
	m.mu.Lock()
	for _, l := range m.leases {
		held[l.Account+"/"+l.KeyID] = true
	}
	m.mu.Unlock()
	now := m.Now()
	var deleted []string
	var errs []error
	for i := range m.Config.Sets {
		set := &m.Config.Sets[i]
		if set.fixed() || set.TokenMode() {
			// Migrated and existing accounts hold keys this server never
			// minted, and token-mode sets never create keys.
			continue
		}
		c, err := m.cloud(ctx, set.Admin)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		accounts, err := m.Accounts(ctx, set)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, a := range accounts {
			name := "projects/" + set.Project + "/serviceAccounts/" + a
			keys, err := c.ListKeys(ctx, name)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			for _, k := range keys {
				if held[a+"/"+k.ID] || !k.CreatedAt.Add(set.MaxLease+Grace).Before(now) {
					continue
				}
				if err := c.DeleteKey(ctx, name, k.ID); err != nil && !isNotFound(err) {
					errs = append(errs, err)
					continue
				}
				deleted = append(deleted, a+"/"+k.ID)
			}
		}
	}
	return deleted, errors.Join(errs...)
}

// reconcileSet makes the set's bindings what its configuration says: each
// of its accounts holds exactly its roles, on its resource_scope or its
// project, and nothing on the other. Members that are not the set's own
// accounts are never touched.
func (m *Manager) reconcileSet(ctx context.Context, c Cloud, set *PermissionSet, accounts []string) error {
	return m.reconcileSetWithAll(ctx, c, set, accounts, nil)
}

func (m *Manager) reconcileSetWithAll(ctx context.Context, c Cloud, set *PermissionSet, accounts []string, all []ServiceAccount) error {
	if set.fixed() || len(accounts) == 0 {
		return nil
	}
	members := make([]string, 0, len(accounts))
	acctSet := map[string]bool{}
	for _, a := range accounts {
		members = append(members, "serviceAccount:"+a)
		acctSet[a] = true
	}
	// If an owned account previously bound on a different resource_scope,
	// clear that old scope's bindings first.
	for _, sa := range all {
		if !acctSet[sa.Email] {
			continue
		}
		if o, ok := OwnedBy(sa.Description); ok && o.Scope != "" && o.Scope != set.ResourceScope {
			if oldRes, ok := scopeResource(set.Project, o.Scope); ok {
				if err := reconcileResourceForAdmin(ctx, c, set.Admin, oldRes, members, nil); err != nil && !IsDenied(err) {
					return err
				}
			}
		}
	}
	projectRoles := set.Roles
	if set.ResourceScope != "" {
		res, ok := scopeResource(set.Project, set.ResourceScope)
		if !ok {
			return fmt.Errorf("permission set %s: invalid resource_scope %q", set.Name, set.ResourceScope)
		}
		if err := reconcileResourceForAdmin(ctx, c, set.Admin, res, members, set.Roles); err != nil {
			return err
		}
		// Best-effort clear on the project in case the set was previously
		// unscoped; an admin granted IAM only on the scoped resource may
		// lack project-level getIamPolicy.
		if err := reconcileResourceForAdmin(ctx, c, set.Admin, "projects/"+set.Project, members, nil); err != nil && !IsDenied(err) {
			return err
		}
		return nil
	}
	return reconcileResourceForAdmin(ctx, c, set.Admin, "projects/"+set.Project, members, projectRoles)
}

func policyPermsForResource(resource string) (getPerm, setPerm, target, hint string) {
	if b, ok := bucketOf(resource); ok {
		return "storage.buckets.getIamPolicy", "storage.buckets.setIamPolicy", "bucket " + b, "grant roles/storage.admin on bucket " + b
	}
	if isArtifactRegistryResource(resource) {
		return "artifactregistry.repositories.getIamPolicy", "artifactregistry.repositories.setIamPolicy", "Artifact Registry repository " + resource, "grant roles/artifactregistry.repoAdmin on the repository"
	}
	if isServiceAccountResource(resource) {
		return "iam.serviceAccounts.getIamPolicy", "iam.serviceAccounts.setIamPolicy", "service account " + resource, "grant roles/iam.serviceAccountAdmin on the project or account"
	}
	proj := strings.TrimPrefix(resource, "projects/")
	return "resourcemanager.projects.getIamPolicy", "resourcemanager.projects.setIamPolicy", "project " + proj, "grant custom grainGcpKeyAdmin on project " + proj
}

// reconcileResource makes members hold exactly roles on resource, retrying
// a lost race a few times. It reports whether it changed anything through
// the error alone.
func reconcileResource(ctx context.Context, c Cloud, resource string, members, roles []string) error {
	return reconcileResourceForAdmin(ctx, c, "", resource, members, roles)
}

func reconcileResourceForAdmin(ctx context.Context, c Cloud, admin, resource string, members, roles []string) error {
	getPerm, setPerm, target, hint := policyPermsForResource(resource)
	for try := 0; ; try++ {
		p, err := c.GetPolicy(ctx, resource)
		if err != nil {
			if admin != "" {
				return explainDenied(admin, getPerm, target, hint, err)
			}
			return err
		}
		if !applyBindings(p, members, roles) {
			return nil
		}
		err = c.SetPolicy(ctx, resource, p)
		if errors.Is(err, ErrConflict) && try < 4 {
			continue
		}
		if admin != "" {
			return explainDenied(admin, setPerm, target, hint, err)
		}
		return err
	}
}

// applyBindings edits p so members hold exactly roles, and reports
// whether anything changed.
func applyBindings(p *Policy, members, roles []string) bool {
	ours := map[string]bool{}
	for _, mb := range members {
		ours[mb] = true
	}
	want := map[string]bool{}
	for _, r := range roles {
		want[r] = true
	}
	changed := false
	seen := map[string]bool{}
	var out []Binding
	for _, b := range p.Bindings {
		seen[b.Role] = true
		var keep []string
		have := map[string]bool{}
		for _, mb := range b.Members {
			if ours[mb] && !want[b.Role] {
				changed = true
				continue
			}
			have[mb] = true
			keep = append(keep, mb)
		}
		if want[b.Role] {
			for _, mb := range members {
				if !have[mb] {
					keep = append(keep, mb)
					changed = true
				}
			}
		}
		if len(keep) > 0 {
			out = append(out, Binding{Role: b.Role, Members: keep})
		}
	}
	for _, r := range roles {
		if !seen[r] {
			out = append(out, Binding{Role: r, Members: append([]string(nil), members...)})
			changed = true
		}
	}
	p.Bindings = out
	return changed
}

// Orphan is a service account this deployment created for a permission
// set that is no longer configured.
type Orphan struct {
	Admin, Project, Set, Email, Scope string
}

// Census is what an admin's projects hold beside the configured sets'
// own accounts.
type Census struct {
	// Orphans are this deployment's accounts of removed sets: the only
	// accounts delete_orphans can delete.
	Orphans []Orphan
	// Others counts, by project, accounts another grain deployment
	// created. They are never orphans and never touched.
	Others map[string]int
	// Legacy counts, by project, accounts with the old marker that names
	// no deployment ([grain-gcp-key set=<set>]) which no set here adopts.
	// Whose they are cannot be told, so they are never touched either.
	Legacy map[string]int
}

// Census surveys every project an admin acts in.
func (m *Manager) Census(ctx context.Context) (Census, error) {
	out := Census{Others: map[string]int{}, Legacy: map[string]int{}}
	var errs []error
	seen := map[string]bool{}
	for _, a := range m.Config.Admins {
		c, err := m.cloud(ctx, a.Name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, p := range a.Projects {
			if seen[p] {
				continue
			}
			seen[p] = true
			all, err := c.ListServiceAccounts(ctx, p)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			for _, sa := range all {
				o, ok := OwnedBy(sa.Description)
				switch {
				case !ok:
				case o.Legacy:
					if !m.Config.adopts(p, sa.Email, o) {
						out.Legacy[p]++
					}
				case !m.Config.Ours(o):
					out.Others[p]++
				case m.Config.Set(o.Set) == nil:
					out.Orphans = append(out.Orphans, Orphan{Admin: a.Name, Project: p, Set: o.Set, Email: sa.Email, Scope: o.Scope})
				}
			}
		}
	}
	return out, errors.Join(errs...)
}

// adopts reports whether a configured set in project takes the legacy
// account email as its own.
func (c *Config) adopts(project, email string, o Owner) bool {
	s := c.Set(o.Set)
	if s == nil || !s.AdoptLegacy || s.Project != project || s.fixed() {
		return false
	}
	id, _, _ := strings.Cut(email, "@")
	return LegacyAccountID(s.Name, accountNumber(email)) == id
}

// Orphans lists this deployment's accounts of removed sets, in every
// project an admin acts in.
func (m *Manager) Orphans(ctx context.Context) ([]Orphan, error) {
	c, err := m.Census(ctx)
	return c.Orphans, err
}

// DeleteOrphans deletes the accounts of every removed set the operator
// named in delete_orphans, first taking their resource_scope and project
// bindings away. It returns the accounts it deleted.
func (m *Manager) DeleteOrphans(ctx context.Context, orphans []Orphan) ([]string, error) {
	var deleted []string
	var errs []error
	for _, o := range orphans {
		if !contains(m.Config.DeleteOrphans, o.Set) {
			continue
		}
		c, err := m.cloud(ctx, o.Admin)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		member := []string{"serviceAccount:" + o.Email}
		if o.Scope != "" {
			if res, ok := scopeResource(o.Project, o.Scope); ok {
				if err := reconcileResourceForAdmin(ctx, c, o.Admin, res, member, nil); err != nil {
					errs = append(errs, err)
					continue
				}
			}
			if err := reconcileResourceForAdmin(ctx, c, o.Admin, "projects/"+o.Project, member, nil); err != nil && !IsDenied(err) {
				errs = append(errs, err)
				continue
			}
		} else {
			if err := reconcileResourceForAdmin(ctx, c, o.Admin, "projects/"+o.Project, member, nil); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if err := c.DeleteServiceAccount(ctx, o.Project, o.Email); err != nil && !isNotFound(err) {
			errs = append(errs, explainDenied(o.Admin, "iam.serviceAccounts.delete", "service account "+o.Email, "", err))
			continue
		}
		deleted = append(deleted, o.Email)
	}
	return deleted, errors.Join(errs...)
}

// Reconcile ensures managed token-mode sets have their service account,
// role bindings and tokenCreator binding, and corrects the bindings of
// every pool set's existing accounts: the setup and drift check configure
// makes.
func (m *Manager) Reconcile(ctx context.Context) error {
	var errs []error
	for i := range m.Config.Sets {
		set := &m.Config.Sets[i]
		if set.fixed() {
			continue
		}
		unlock := m.lockSet(set.Name)
		c, err := m.cloud(ctx, set.Admin)
		if err != nil {
			unlock()
			errs = append(errs, err)
			continue
		}
		accounts, all, err := m.listAccounts(ctx, set)
		if err != nil {
			unlock()
			errs = append(errs, fmt.Errorf("permission set %s: %w", set.Name, err))
			continue
		}
		if set.ManagedToken() {
			if _, err := m.ensureTokenAccount(ctx, c, set, accounts, all); err != nil {
				unlock()
				errs = append(errs, fmt.Errorf("permission set %s: %w", set.Name, err))
				continue
			}
			unlock()
			continue
		}
		if err := m.reconcileSetWithAll(ctx, c, set, accounts, all); err != nil {
			unlock()
			errs = append(errs, fmt.Errorf("permission set %s: %w", set.Name, err))
			continue
		}
		unlock()
	}
	return errors.Join(errs...)
}

// MaxTokenLifetime is the longest an impersonated access token lives:
// generateAccessToken's own default and limit without the org policy
// that extends it. A token-mode grant is refreshed well inside it.
const MaxTokenLifetime = time.Hour

// Impersonator is a Cloud that can act as another service account
// through the IAM Credentials API: the admin needs
// iam.serviceAccounts.getAccessToken (roles/iam.serviceAccountTokenCreator)
// on it. Nothing it does creates a key or changes the account.
type Impersonator interface {
	// GenerateAccessToken is an OAuth access token for email with scopes,
	// living lifetime.
	GenerateAccessToken(ctx context.Context, email string, scopes []string, lifetime time.Duration) (token string, expiry time.Time, err error)
	// GenerateIDToken is an OpenID Connect ID token for email whose aud
	// is audience.
	GenerateIDToken(ctx context.Context, email, audience string) (string, error)
	// TestAccountPermissions is the permissions in perms the admin holds
	// on the service account email (testIamPermissions on it).
	TestAccountPermissions(ctx context.Context, email string, perms []string) ([]string, error)
}

func (m *Manager) impersonator(ctx context.Context, set *PermissionSet) (Impersonator, error) {
	c, err := m.cloud(ctx, set.Admin)
	if err != nil {
		return nil, err
	}
	imp, ok := c.(Impersonator)
	if !ok {
		return nil, fmt.Errorf("admin %s's connection cannot impersonate service accounts", set.Admin)
	}
	return imp, nil
}

// mintToken is a token-mode grant's mint: it proves the admin can act as
// account by minting one access token, which it throws away, and records
// the lease. No key is created; the sandbox fetches its tokens through
// AccessToken as it needs them.
func (m *Manager) mintToken(ctx context.Context, c Cloud, attemptID string, set *PermissionSet, account string, deadline time.Time) (string, Lease, error) {
	imp, ok := c.(Impersonator)
	if !ok {
		return "", Lease{}, fmt.Errorf("admin %s's connection cannot impersonate service accounts", set.Admin)
	}
	if _, _, err := imp.GenerateAccessToken(ctx, account, []string{cloudPlatform}, time.Minute); err != nil {
		return "", Lease{}, explainDenied(set.Admin, "iam.serviceAccounts.getAccessToken", "service account "+account, "grant roles/iam.serviceAccountTokenCreator on "+account, err)
	}
	l := Lease{Set: set.Name, Account: account, Deadline: deadline}
	m.mu.Lock()
	m.leases[attemptID] = l
	m.mu.Unlock()
	return "", l, nil
}

// remaining is how long a token minted now for a grant ending at
// deadline may live: until the deadline, at most MaxTokenLifetime.
func (m *Manager) remaining(set *PermissionSet, deadline time.Time) (time.Duration, error) {
	left := deadline.Sub(m.Now())
	if left <= 0 {
		return 0, &Refusal{fmt.Sprintf("permission set %s: the grant's deadline %s has passed", set.Name, deadline.UTC().Format(time.RFC3339))}
	}
	if left > set.MaxLease {
		left = set.MaxLease
	}
	left = left.Round(time.Second) + time.Second
	if left > MaxTokenLifetime {
		// generateAccessToken refuses a lifetime past its limit.
		left = MaxTokenLifetime
	}
	return left, nil
}

// tokenSet is token-mode set setName, refused unless it still acts as
// account, the account its grant was given for: a set reconfigured to
// another account does not hand a running grant that account's tokens.
func (m *Manager) tokenSet(ctx context.Context, setName, account string) (*PermissionSet, error) {
	set := m.Config.Set(setName)
	if set == nil {
		return nil, &Refusal{"no permission set is called " + setName}
	}
	if !set.TokenMode() {
		return nil, &Refusal{"permission set " + setName + " does not mint access tokens"}
	}
	if set.Existing() {
		if set.ServiceAccount != account {
			return nil, &Refusal{"permission set " + setName + " no longer acts as " + account + ", the account this grant was given for"}
		}
		return set, nil
	}
	m.mu.Lock()
	cached := m.managedAccounts[setName]
	m.mu.Unlock()
	if cached == "" {
		accounts, err := m.Accounts(ctx, set)
		if err != nil {
			return nil, err
		}
		if len(accounts) > 0 {
			cached = accounts[0]
			m.mu.Lock()
			m.managedAccounts[setName] = cached
			m.mu.Unlock()
		}
	}
	if cached != account {
		return nil, &Refusal{"permission set " + setName + " no longer acts as " + account + ", the account this grant was given for"}
	}
	return set, nil
}

// AccessToken is a fresh access token for token-mode set setName's
// account, for a grant of account whose deadline is deadline: it lives
// until then, at most MaxTokenLifetime, so a refresh is always another
// call here.
func (m *Manager) AccessToken(ctx context.Context, setName, account string, deadline time.Time) (string, time.Time, error) {
	set, err := m.tokenSet(ctx, setName, account)
	if err != nil {
		return "", time.Time{}, err
	}
	lifetime, err := m.remaining(set, deadline)
	if err != nil {
		return "", time.Time{}, err
	}
	imp, err := m.impersonator(ctx, set)
	if err != nil {
		return "", time.Time{}, err
	}
	tok, exp, err := imp.GenerateAccessToken(ctx, account, []string{cloudPlatform}, lifetime)
	if _, ok := AsCredentialError(err); ok {
		m.forget(set.Admin)
	}
	return tok, exp, err
}

// IDToken is an ID token for token-mode set setName's account with
// audience, for a grant of account whose deadline has not passed.
func (m *Manager) IDToken(ctx context.Context, setName, account, audience string, deadline time.Time) (string, error) {
	set, err := m.tokenSet(ctx, setName, account)
	if err != nil {
		return "", err
	}
	if _, err := m.remaining(set, deadline); err != nil {
		return "", err
	}
	imp, err := m.impersonator(ctx, set)
	if err != nil {
		return "", err
	}
	return imp.GenerateIDToken(ctx, account, audience)
}
