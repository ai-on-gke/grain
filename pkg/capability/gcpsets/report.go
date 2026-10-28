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

// AdminPermissions is what an admin needs on each of its projects
// (docs/capabilities.md, "GCP"): the custom role grainGcpKeyAdmin holds
// exactly these.
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

// ScopePermissions are the extra permissions an admin needs when one of
// its sets binds on a bucket.
var ScopePermissions = []string{
	"storage.buckets.getIamPolicy",
	"storage.buckets.setIamPolicy",
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
			want := append([]string(nil), AdminPermissions...)
			for _, s := range m.Config.Sets {
				if s.Admin == a.Name && s.Project == p && s.ResourceScope != "" {
					want = append(want, ScopePermissions...)
					break
				}
			}
			has, err := c.TestPermissions(ctx, p, want)
			if err != nil {
				chk.Missing[p] = []string{"(could not test: " + err.Error() + ")"}
				continue
			}
			held := map[string]bool{}
			for _, h := range has {
				held[h] = true
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
		where := s.Project
		if s.ResourceScope != "" {
			where += " on " + s.ResourceScope
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
