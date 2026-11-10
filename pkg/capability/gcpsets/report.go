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
	"fmt"
	"sort"
	"strings"
)

// AdminPermissions is what an admin needs on each project where it manages
// key-mode permission sets (docs/capabilities.md, "GCP").
var AdminPermissions = []string{
	"iam.serviceAccounts.create",
	"iam.serviceAccounts.delete",
	"iam.serviceAccounts.get",
	"iam.serviceAccounts.list",
	"iam.serviceAccountKeys.create",
	"iam.serviceAccountKeys.delete",
	"iam.serviceAccountKeys.list",
	"resourcemanager.projects.get",
	"resourcemanager.projects.getIamPolicy",
	"resourcemanager.projects.setIamPolicy",
}

// TokenAdminServiceAccountPermissions are the service-account permissions an
// admin needs on a project for a managed token-mode set: creating, listing and
// deleting the set's service account, plus setting its IAM policy so grain can
// grant the admin roles/iam.serviceAccountTokenCreator on that account only
// (no serviceAccountKeys.* permissions are needed).
var TokenAdminServiceAccountPermissions = []string{
	"iam.serviceAccounts.create",
	"iam.serviceAccounts.delete",
	"iam.serviceAccounts.get",
	"iam.serviceAccounts.list",
	"iam.serviceAccounts.getIamPolicy",
	"iam.serviceAccounts.setIamPolicy",
}

// ProjectPolicyPermissions are the project IAM permissions needed when a
// managed set binds roles on the project rather than on a resource_scope.
var ProjectPolicyPermissions = []string{
	"resourcemanager.projects.get",
	"resourcemanager.projects.getIamPolicy",
	"resourcemanager.projects.setIamPolicy",
}

// TokenAdminPermissions are all project permissions an admin needs for an
// unscoped managed token-mode set.
var TokenAdminPermissions = append(append([]string(nil), TokenAdminServiceAccountPermissions...), ProjectPolicyPermissions...)

// ScopePermissions are the extra permissions an admin needs when one of
// its sets binds on a bucket.
var ScopePermissions = []string{
	"storage.buckets.getIamPolicy",
	"storage.buckets.setIamPolicy",
}

// ArtifactRegistryScopePermissions are the extra permissions an admin needs
// when one of its sets binds on an Artifact Registry repository
// (repositories/<location>/<repo>).
var ArtifactRegistryScopePermissions = []string{
	"artifactregistry.repositories.getIamPolicy",
	"artifactregistry.repositories.setIamPolicy",
}

// ResourcePermissionTester is an optional Cloud capability for checking
// permissions directly on a scoped bucket or Artifact Registry repository when
// the admin holds resource-level rather than project-wide IAM.
type ResourcePermissionTester interface {
	TestResourcePermissions(ctx context.Context, resource string, perms []string) ([]string, error)
}

// AdminCheck is Test's finding for one admin.
type AdminCheck struct {
	Admin    string
	Source   string
	Identity string
	Error    string
	// Missing is, by project, the permissions the admin lacks.
	Missing map[string][]string
}

// TokenChecker is a Cloud that can fetch its admin's access token on its
// own, as every call it makes does first.
type TokenChecker interface {
	CheckToken(ctx context.Context) error
}

// CheckAdmins resolves each admin's identity and tests its permissions on
// each of its projects with testIamPermissions.
func (m *Manager) CheckAdmins(ctx context.Context) []AdminCheck {
	var out []AdminCheck
	for _, a := range m.Config.Admins {
		chk := AdminCheck{Admin: a.Name, Source: a.CredentialSource, Missing: map[string][]string{}}
		idCtx, cancel := context.WithTimeout(ctx, identityTimeout)
		id, err := m.Connector.Identity(idCtx, a)
		cancel()
		if err != nil {
			chk.Error = err.Error()
			out = append(out, chk)
			continue
		}
		chk.Identity = id
		c, err := m.cloud(ctx, a.Name)
		if err != nil {
			chk.Error = err.Error()
			out = append(out, chk)
			continue
		}
		// The token fetch every mint makes, so credentials gone stale
		// show here before a task's dispatch is refused over them.
		if tc, ok := c.(TokenChecker); ok {
			if err := tc.CheckToken(ctx); err != nil {
				m.forget(a.Name)
				chk.Error = err.Error()
				out = append(out, chk)
				continue
			}
		}
		for _, p := range a.Projects {
			if !m.Config.managesAccountsIn(a.Name, p) {
				// Only sets over existing accounts act here, and they
				// create and bind nothing: what they need is checked on
				// each account (CheckExisting).
				continue
			}
			want, scopeResources := m.Config.wantedPermissionsIn(a.Name, p)
			has, err := c.TestPermissions(ctx, p, want)
			if err != nil {
				chk.Missing[p] = []string{"(could not test: " + err.Error() + ")"}
				continue
			}
			held := map[string]bool{}
			for _, h := range has {
				held[h] = true
			}
			if rpt, ok := c.(ResourcePermissionTester); ok && len(scopeResources) > 0 {
				for perm, resources := range scopeResources {
					if held[perm] {
						continue
					}
					allHeld := true
					for _, res := range resources {
						rhas, rerr := rpt.TestResourcePermissions(ctx, res, []string{perm})
						if rerr != nil || !contains(rhas, perm) {
							allHeld = false
							break
						}
					}
					if allHeld {
						held[perm] = true
					}
				}
			}
			for _, w := range want {
				if !held[w] {
					chk.Missing[p] = append(chk.Missing[p], w)
				}
			}
		}
		out = append(out, chk)
	}
	return out
}

// Report is the text Settings' Test shows for this configuration: who
// each admin resolved to and what it lacks, each set's pool and usage,
// and the accounts of removed sets. It never holds a credential.
func (m *Manager) Report(ctx context.Context, census Census, deleted []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "deployment %s (instance %s): new service accounts are named %s\n",
		m.Config.Deployment, InstanceTag(m.Config.Instance), m.Config.namePattern())
	for _, chk := range m.CheckAdmins(ctx) {
		switch {
		case chk.Error != "":
			fmt.Fprintf(&b, "admin %s (%s): %s\n", chk.Admin, chk.Source, chk.Error)
		default:
			fmt.Fprintf(&b, "admin %s (%s) is %s", chk.Admin, chk.Source, chk.Identity)
			if len(chk.Missing) == 0 {
				b.WriteString("; has every permission it needs\n")
				continue
			}
			b.WriteString("; MISSING:")
			projects := make([]string, 0, len(chk.Missing))
			for p := range chk.Missing {
				projects = append(projects, p)
			}
			sort.Strings(projects)
			for _, p := range projects {
				fmt.Fprintf(&b, " %s: %s;", p, strings.Join(chk.Missing[p], ", "))
			}
			b.WriteString("\n")
		}
	}
	held := m.Leases()
	for i := range m.Config.Sets {
		s := &m.Config.Sets[i]
		accounts, err := m.Accounts(ctx, s)
		if err != nil {
			fmt.Fprintf(&b, "set %s: %v\n", s.Name, err)
			continue
		}
		if s.Existing() {
			b.WriteString(m.CheckExisting(ctx, s, held[s.Name]).String() + "\n")
			continue
		}
		where := s.Project
		if s.ResourceScope != "" {
			where += " on " + s.ResourceScope
		}
		if s.TokenMode() {
			if len(accounts) > 0 {
				fmt.Fprintf(&b, "set %s (%s, %s): service account %s (credential token; short-lived tokens, no keys), %d attempt(s) holding tokens now\n",
					s.Name, where, strings.Join(s.Roles, ", "), accounts[0], held[s.Name])
			} else {
				fmt.Fprintf(&b, "set %s (%s, %s): 0 of 1 account(s) (credential token; short-lived tokens, no keys), %d attempt(s) holding tokens now\n",
					s.Name, where, strings.Join(s.Roles, ", "), held[s.Name])
			}
			continue
		}
		fmt.Fprintf(&b, "set %s (%s, %s): %d of %d account(s), %d key(s) held now\n",
			s.Name, where, strings.Join(s.Roles, ", "), len(accounts), s.PoolMax, held[s.Name])
	}
	for _, o := range census.Orphans {
		if contains(deleted, o.Email) {
			fmt.Fprintf(&b, "deleted %s (removed set %s)\n", o.Email, o.Set)
			continue
		}
		fmt.Fprintf(&b, "removed set %s still has %s: add %s to delete_orphans to delete it\n", o.Set, o.Email, o.Set)
	}
	for _, p := range sortedKeys(census.Others) {
		fmt.Fprintf(&b, "project %s: %d service account(s) of other grain deployments, left alone\n", p, census.Others[p])
	}
	for _, p := range sortedKeys(census.Legacy) {
		fmt.Fprintf(&b, "project %s: %d service account(s) with the old marker that names no deployment, which no set here adopts (adopt_legacy); left alone\n", p, census.Legacy[p])
	}
	return strings.TrimSpace(b.String())
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// managesAccountsIn reports whether admin needs AdminPermissions in
// project: some set of its there creates (or was migrated over) accounts,
// gemini_key creates keys there, or no set of its names the project at
// all.
func (c *Config) managesAccountsIn(admin, project string) bool {
	if g := c.GeminiKey; g != nil && g.Admin == admin && g.Project == project {
		return true
	}
	existing := false
	for i := range c.Sets {
		s := &c.Sets[i]
		if s.Admin != admin {
			continue
		}
		if s.Existing() {
			if home, _ := AccountProject(s.ServiceAccount); home == project || s.Project == project {
				existing = true
			}
			continue
		}
		if s.Project == project {
			return true
		}
	}
	return !existing
}

// wantedPermissionsIn lists the permissions admin needs in project across its
// sets there, plus -- for resource-scoped permissions -- the scoped resources
// that may satisfy them individually.
func (c *Config) wantedPermissionsIn(admin, project string) ([]string, map[string][]string) {
	var want []string
	seen := map[string]bool{}
	add := func(perms ...string) {
		for _, p := range perms {
			if !seen[p] {
				seen[p] = true
				want = append(want, p)
			}
		}
	}
	scopeResources := map[string][]string{}
	addScope := func(res string, perms []string) {
		add(perms...)
		for _, p := range perms {
			if !contains(scopeResources[p], res) {
				scopeResources[p] = append(scopeResources[p], res)
			}
		}
	}
	hasPoolOrMigrated := false
	hasManagedToken := false
	hasUnscopedManagedToken := false
	hasAnyManagedSet := false
	if g := c.GeminiKey; g != nil && g.Admin == admin && g.Project == project {
		hasPoolOrMigrated = true
	}
	for i := range c.Sets {
		s := &c.Sets[i]
		if s.Admin != admin || s.Existing() || s.Project != project {
			continue
		}
		hasAnyManagedSet = true
		if s.ManagedToken() {
			hasManagedToken = true
			if s.ResourceScope == "" {
				hasUnscopedManagedToken = true
			}
		} else {
			hasPoolOrMigrated = true
		}
	}
	if !hasAnyManagedSet {
		hasPoolOrMigrated = true
	}
	if hasPoolOrMigrated {
		add(AdminPermissions...)
	}
	if hasManagedToken {
		add(TokenAdminServiceAccountPermissions...)
		if hasUnscopedManagedToken {
			add(ProjectPolicyPermissions...)
		}
	}
	for i := range c.Sets {
		s := &c.Sets[i]
		if s.Admin != admin || s.Existing() || s.Project != project || s.ResourceScope == "" {
			continue
		}
		res, ok := scopeResource(s.Project, s.ResourceScope)
		if !ok {
			continue
		}
		if _, isBucket := bucketOf(s.ResourceScope); isBucket {
			addScope(res, ScopePermissions)
		} else if _, _, isAR := artifactRepoOf(s.ResourceScope); isAR {
			addScope(res, ArtifactRegistryScopePermissions)
		}
	}
	return want, scopeResources
}

// ExistingCheck is Test's finding for a set over an existing service
// account.
type ExistingCheck struct {
	Set, Account, Admin, Credential string
	// CanAct is whether the admin holds the permission the set's
	// credential mode needs on the account; Needs names it.
	CanAct bool
	Needs  string
	// Error is why that could not be told.
	Error string
	// Lacks are the set's listed roles the account does not hold on the
	// set's project; RolesError is why that could not be told.
	Lacks        []string
	RolesError   string
	RolesChecked bool
	// Held is how many attempts hold a token or a key of the set now.
	Held int
}

// TokenPermission and KeyPermission are what an admin needs on an
// existing service account to impersonate it, or to mint its keys.
const (
	TokenPermission = "iam.serviceAccounts.getAccessToken"
	KeyPermission   = "iam.serviceAccountKeys.create"
)

// CheckExisting tests what a set over an existing service account relies
// on: the admin's right to impersonate the account (or mint its keys),
// and -- when the set lists roles -- whether the account holds each on
// the set's project. It changes nothing.
func (m *Manager) CheckExisting(ctx context.Context, s *PermissionSet, held int) ExistingCheck {
	chk := ExistingCheck{Set: s.Name, Account: s.ServiceAccount, Admin: s.Admin, Credential: s.Credential, Needs: TokenPermission, Held: held}
	if !s.TokenMode() {
		chk.Needs = KeyPermission
	}
	imp, err := m.impersonator(ctx, s)
	if err != nil {
		chk.Error = err.Error()
		return chk
	}
	has, err := imp.TestAccountPermissions(ctx, s.ServiceAccount, []string{chk.Needs})
	if err != nil {
		chk.Error = err.Error()
	} else {
		chk.CanAct = contains(has, chk.Needs)
	}
	if len(s.Roles) == 0 {
		return chk
	}
	c, err := m.cloud(ctx, s.Admin)
	if err != nil {
		chk.RolesError = err.Error()
		return chk
	}
	p, err := c.GetPolicy(ctx, "projects/"+s.Project)
	if err != nil {
		chk.RolesError = err.Error()
		return chk
	}
	member := "serviceAccount:" + s.ServiceAccount
	holds := map[string]bool{}
	for _, b := range p.Bindings {
		if contains(b.Members, member) {
			holds[b.Role] = true
		}
	}
	for _, r := range s.Roles {
		if !holds[r] {
			chk.Lacks = append(chk.Lacks, r)
		}
	}
	chk.RolesChecked = true
	return chk
}

// String is the check as Test's report line.
func (chk ExistingCheck) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "set %s: existing service account %s (credential %s; grain creates, binds and deletes nothing)", chk.Set, chk.Account, chk.Credential)
	verb := "impersonate it"
	if chk.Needs == KeyPermission {
		verb = "mint keys for it"
	}
	switch {
	case chk.Error != "":
		fmt.Fprintf(&b, "; could not tell whether admin %s can %s: %s", chk.Admin, verb, chk.Error)
	case chk.CanAct:
		fmt.Fprintf(&b, "; admin %s can %s", chk.Admin, verb)
	default:
		fmt.Fprintf(&b, "; admin %s CANNOT %s: it lacks %s on the account", chk.Admin, verb, chk.Needs)
		if chk.Needs == TokenPermission {
			b.WriteString(" (grant it roles/iam.serviceAccountTokenCreator there)")
		}
	}
	if chk.Needs == TokenPermission {
		fmt.Fprintf(&b, "; short-lived tokens, no key cap, %d attempt(s) holding tokens now", chk.Held)
	} else {
		fmt.Fprintf(&b, "; at most %d concurrent attempts (GCP's key limit), %d key(s) held now", KeysPerAccount, chk.Held)
	}
	switch {
	case chk.RolesError != "":
		fmt.Fprintf(&b, "; could not check its roles: %s", chk.RolesError)
	case len(chk.Lacks) > 0:
		fmt.Fprintf(&b, "; it LACKS listed role(s) on its project: %s (grain grants nothing; roles inherited from a folder or the organization are not seen here)", strings.Join(chk.Lacks, ", "))
	case chk.RolesChecked:
		b.WriteString("; it holds every listed role on its project")
	}
	return b.String()
}
