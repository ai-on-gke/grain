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

	mu     sync.Mutex
	clouds map[string]Cloud
	leases map[string]Lease
	setMu  map[string]*sync.Mutex
}

// NewManager is a Manager for cfg.
func NewManager(cfg *Config, conn Connector) *Manager {
	return &Manager{Config: cfg, Connector: conn, Now: time.Now, clouds: map[string]Cloud{}, leases: map[string]Lease{}, setMu: map[string]*sync.Mutex{}}
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
	if len(set.Accounts) > 0 {
		return append([]string(nil), set.Accounts...), nil, nil
	}
	c, err := m.cloud(ctx, set.Admin)
	if err != nil {
		return nil, nil, err
	}
	all, err := c.ListServiceAccounts(ctx, set.Project)
	if err != nil {
		return nil, nil, err
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
		if n == 0 {
			continue
		}
		id, _, _ := strings.Cut(sa.Email, "@")
		switch {
		case c.Ours(o):
			mine = append(mine, acct{false, n, sa.Email})
		case o.Legacy && set.AdoptLegacy && LegacyAccountID(set.Name, n) == id:
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
	if len(set.Accounts) == 0 && len(accounts) > 0 {
		if err := m.reconcileSet(ctx, c, set, accounts); err != nil {
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
			if len(set.Accounts) > 0 || len(accounts) >= set.PoolMax {
				return "", Lease{}, m.exhausted(set, now)
			}
			account, err = m.createAccount(ctx, c, set, accounts, all)
			if err != nil {
				return "", Lease{}, err
			}
			accounts = append(accounts, account)
			all = append(all, ServiceAccount{Email: account})
		}
		tried[account] = true
		id, keyJSON, err := c.CreateKey(ctx, "projects/"+set.Project+"/serviceAccounts/"+account)
		if err != nil {
			if isKeyQuota(err) {
				// Someone else's keys filled it between List and Create:
				// try the next account.
				continue
			}
			return "", Lease{}, err
		}
		l := Lease{Set: set.Name, Account: account, KeyID: id, Deadline: deadline}
		m.mu.Lock()
		m.leases[attemptID] = l
		m.mu.Unlock()
		return keyJSON, l, nil
	}
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
		sa, err := c.CreateServiceAccount(ctx, set.Project, id, "grain "+m.Config.Deployment+" "+set.Name+" "+strconv.Itoa(n), OwnerDescription(m.Config.Deployment, m.Config.Instance, set.Name))
		if err != nil {
			if isAlreadyExists(err) && try < maxNameTries {
				taken[Email(id, set.Project)] = true
				continue
			}
			return "", err
		}
		if err := m.reconcileSet(ctx, c, set, append(append([]string(nil), accounts...), sa.Email)); err != nil {
			return "", err
		}
		return sa.Email, nil
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
	c, err := m.cloud(ctx, set.Admin)
	if err != nil {
		return err
	}
	if err := c.DeleteKey(ctx, "projects/"+set.Project+"/serviceAccounts/"+l.Account, l.KeyID); err != nil && !isNotFound(err) {
		return err
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
		if len(set.Accounts) > 0 {
			// Migrated accounts hold keys this server never minted.
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
	if len(set.Accounts) > 0 || len(accounts) == 0 {
		return nil
	}
	members := make([]string, 0, len(accounts))
	for _, a := range accounts {
		members = append(members, "serviceAccount:"+a)
	}
	projectRoles, scopeRoles := set.Roles, []string(nil)
	if set.ResourceScope != "" {
		projectRoles, scopeRoles = nil, set.Roles
		if err := reconcileResource(ctx, c, set.ResourceScope, members, scopeRoles); err != nil {
			return err
		}
	}
	return reconcileResource(ctx, c, "projects/"+set.Project, members, projectRoles)
}

// reconcileResource makes members hold exactly roles on resource, retrying
// a lost race a few times. It reports whether it changed anything through
// the error alone.
func reconcileResource(ctx context.Context, c Cloud, resource string, members, roles []string) error {
	for try := 0; ; try++ {
		p, err := c.GetPolicy(ctx, resource)
		if err != nil {
			return err
		}
		if !applyBindings(p, members, roles) {
			return nil
		}
		err = c.SetPolicy(ctx, resource, p)
		if errors.Is(err, ErrConflict) && try < 4 {
			continue
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
	Admin, Project, Set, Email string
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
					out.Orphans = append(out.Orphans, Orphan{Admin: a.Name, Project: p, Set: o.Set, Email: sa.Email})
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
	if s == nil || !s.AdoptLegacy || s.Project != project || len(s.Accounts) > 0 {
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
// named in delete_orphans, first taking their project bindings away. It
// returns the accounts it deleted.
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
		if err := reconcileResource(ctx, c, "projects/"+o.Project, []string{"serviceAccount:" + o.Email}, nil); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := c.DeleteServiceAccount(ctx, o.Project, o.Email); err != nil && !isNotFound(err) {
			errs = append(errs, err)
			continue
		}
		deleted = append(deleted, o.Email)
	}
	return deleted, errors.Join(errs...)
}

// Reconcile corrects the bindings of every set's existing accounts: the
// drift check configure makes.
func (m *Manager) Reconcile(ctx context.Context) error {
	var errs []error
	for i := range m.Config.Sets {
		set := &m.Config.Sets[i]
		if len(set.Accounts) > 0 {
			continue
		}
		c, err := m.cloud(ctx, set.Admin)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		accounts, err := m.Accounts(ctx, set)
		if err != nil {
			errs = append(errs, fmt.Errorf("permission set %s: %w", set.Name, err))
			continue
		}
		if err := m.reconcileSet(ctx, c, set, accounts); err != nil {
			errs = append(errs, fmt.Errorf("permission set %s: %w", set.Name, err))
		}
	}
	return errors.Join(errs...)
}
