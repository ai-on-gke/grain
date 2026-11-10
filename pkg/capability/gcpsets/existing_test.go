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

package gcpsets_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/gcpsets"
	"github.com/ai-on-gke/grain/pkg/capability/gcpsets/gcpfake"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// orgSA is an existing account with grants grain did not make.
const orgSA = "org-runner@" + proj + ".iam.gserviceaccount.com"

// existingConfig is a configuration whose one set is over orgSA, with
// set's fields added.
func existingConfig(set map[string]any) map[string]any {
	s := map[string]any{"name": "org", "admin": "minter", "service_account": orgSA}
	for k, v := range set {
		s[k] = v
	}
	return map[string]any{
		protocol.DeploymentIDConfig: dep,
		protocol.InstanceIDConfig:   inst,
		"admins": []any{
			map[string]any{"name": "minter", "credential_source": "configured", "projects": []any{proj}, "role_ceiling": []any{"roles/logging.viewer"}},
		},
		"admin_credentials": `{"minter": ` + adminKey + `}`,
		"permission_sets":   []any{s},
	}
}

func existingManager(t *testing.T, set map[string]any, cloud *gcpfake.Cloud, now *time.Time) *gcpsets.Manager {
	t.Helper()
	c, err := gcpsets.Parse(existingConfig(set))
	if err != nil {
		t.Fatal(err)
	}
	cloud.AddAccount(proj, orgSA, "made by hand, with org-level grants")
	return manager(t, c, cloud, now)
}

// noAccountChanges fails t if grain created, bound or deleted anything.
func noAccountChanges(t *testing.T, cloud *gcpfake.Cloud) {
	t.Helper()
	for _, call := range []string{"CreateServiceAccount", "SetPolicy", "DeleteServiceAccount"} {
		if n := cloud.Calls[call]; n != 0 {
			t.Errorf("%s called %d time(s) for a set over an existing account", call, n)
		}
	}
}

func TestExistingAccountConfig(t *testing.T) {
	c, err := gcpsets.Parse(existingConfig(map[string]any{"roles": []any{"roles/owner"}}))
	if err != nil {
		t.Fatalf("a set over an existing account, listing a role past the ceiling: %v", err)
	}
	s := c.Set("org")
	if s.Credential != gcpsets.CredentialToken || !s.TokenMode() || s.Project != proj || !s.PersonGranted || s.MaxLease != gcpsets.DefaultMaxLease {
		t.Fatalf("defaults: %+v", s)
	}
	if got := c.PersonGrantedSets(); len(got) != 1 || got[0] != "org" {
		t.Fatalf("PersonGrantedSets = %v", got)
	}
	c, err = gcpsets.Parse(existingConfig(map[string]any{"credential": "key", "person_granted": false}))
	if err != nil {
		t.Fatal(err)
	}
	if s := c.Set("org"); s.TokenMode() || s.PersonGranted {
		t.Fatalf("credential: key, person_granted: false: %+v", s)
	}

	for name, tc := range map[string]struct {
		set  map[string]any
		want string
	}{
		"pool_max":       {map[string]any{"pool_max": float64(2)}, "service_account and pool_max are exclusive"},
		"adopt_legacy":   {map[string]any{"adopt_legacy": true}, "service_account and adopt_legacy are exclusive"},
		"accounts":       {map[string]any{"accounts": []any{"other@" + proj + ".iam.gserviceaccount.com"}}, "service_account and accounts are exclusive"},
		"resource_scope": {map[string]any{"resource_scope": "buckets/b"}, "resource_scope binds roles"},
		"credential":     {map[string]any{"credential": "password"}, "credential must be token or key"},
		"other project":  {map[string]any{"service_account": "x-runner@another-proj.iam.gserviceaccount.com"}, `is in project "another-proj", which is not one of admin "minter"'s projects`},
		"no project":     {map[string]any{"service_account": "123-compute@developer.gserviceaccount.com"}, "cannot tell which project"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := gcpsets.Parse(existingConfig(tc.set))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	cfg := existingConfig(nil)
	cfg["permission_sets"] = []any{map[string]any{"name": "managed-tok", "admin": "minter", "project": proj, "roles": []any{"roles/logging.viewer"}, "credential": "token"}}
	parsed, err := gcpsets.Parse(cfg)
	if err != nil {
		t.Fatalf("managed token set without service_account: %v", err)
	}
	if ms := parsed.Set("managed-tok"); ms.Existing() || !ms.TokenMode() || !ms.ManagedToken() || ms.PersonGranted {
		t.Fatalf("managed token set defaults: %+v", ms)
	}
	cfg["permission_sets"] = []any{map[string]any{"name": "bad-pool", "admin": "minter", "project": proj, "roles": []any{"roles/logging.viewer"}, "credential": "token", "pool_max": float64(2)}}
	if _, err := gcpsets.Parse(cfg); err == nil || !strings.Contains(err.Error(), "credential: token and pool_max are exclusive") {
		t.Fatalf("credential: token with pool_max: %v", err)
	}
	// With no projects of its own, the admin acts in the account's.
	cfg = existingConfig(nil)
	cfg["admins"] = []any{map[string]any{"name": "minter", "credential_source": "configured"}}
	c, err = gcpsets.Parse(cfg)
	if err != nil || len(c.Admins[0].Projects) != 1 || c.Admins[0].Projects[0] != proj {
		t.Fatalf("implicit projects: %v %+v", err, c)
	}
}

func TestExistingAccountKeyModeMintsAndReleasesOnlyKeys(t *testing.T) {
	cloud := gcpfake.New()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	m := existingManager(t, map[string]any{"credential": "key", "roles": []any{"roles/viewer"}}, cloud, &now)
	ctx := context.Background()
	keyJSON, l, err := m.Mint(ctx, "a1", "org", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if l.Account != orgSA || l.KeyID == "" || !strings.Contains(keyJSON, orgSA) {
		t.Fatalf("lease %+v key %s", l, keyJSON)
	}
	for i := 2; i <= gcpsets.KeysPerAccount; i++ {
		if _, _, err := m.Mint(ctx, "a"+string(rune('0'+i)), "org", now.Add(time.Hour)); err != nil {
			t.Fatalf("mint %d: %v", i, err)
		}
	}
	var ex *gcpsets.Exhausted
	if _, _, err := m.Mint(ctx, "a11", "org", now.Add(time.Hour)); !errors.As(err, &ex) {
		t.Fatalf("an eleventh concurrent key: %v, want Exhausted", err)
	}
	if err := m.Release(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if n := cloud.KeyCount(orgSA); n != gcpsets.KeysPerAccount-1 {
		t.Fatalf("after release %d keys, want %d", n, gcpsets.KeysPerAccount-1)
	}
	// Neither the drift check nor the reaper touches the account.
	now = now.Add(48 * time.Hour)
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if deleted, err := m.Reap(ctx); err != nil || len(deleted) != 0 {
		t.Fatalf("Reap = %v, %v", deleted, err)
	}
	if census, err := m.Census(ctx); err != nil || len(census.Orphans) != 0 {
		t.Fatalf("Census = %+v, %v", census, err)
	}
	noAccountChanges(t, cloud)
}

func TestExistingAccountTokenModeMintsNoKey(t *testing.T) {
	cloud := gcpfake.New()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	m := existingManager(t, map[string]any{"max_lease": "3h"}, cloud, &now)
	ctx := context.Background()
	if _, _, err := m.Mint(ctx, "a1", "org", now.Add(4*time.Hour)); err == nil {
		t.Fatal("a deadline past max_lease was minted")
	}
	keyJSON, l, err := m.Mint(ctx, "a1", "org", now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if keyJSON != "" || l.KeyID != "" || l.Account != orgSA {
		t.Fatalf("token mint returned key %q lease %+v", keyJSON, l)
	}
	if cloud.LiveKeys() != 0 || cloud.Calls["CreateKey"] != 0 {
		t.Fatal("a token-mode mint created a key")
	}
	if m.Leases()["org"] != 1 {
		t.Fatalf("leases %v", m.Leases())
	}

	deadline := now.Add(2 * time.Hour)
	tok, exp, err := m.AccessToken(ctx, "org", orgSA, deadline)
	if err != nil || !strings.HasSuffix(tok, orgSA) {
		t.Fatalf("AccessToken = %q, %v", tok, err)
	}
	if want := now.Add(gcpsets.MaxTokenLifetime); !exp.Equal(want) {
		t.Fatalf("first token expires %s, want an hour (%s)", exp, want)
	}
	// Near the deadline a refresh lives only until it.
	now = deadline.Add(-10 * time.Minute)
	if _, exp, err = m.AccessToken(ctx, "org", orgSA, deadline); err != nil || exp.After(deadline.Add(time.Second)) {
		t.Fatalf("a refresh near the deadline expires %s (%v), past %s", exp, err, deadline)
	}
	now = deadline.Add(time.Second)
	var refusal *gcpsets.Refusal
	if _, _, err := m.AccessToken(ctx, "org", orgSA, deadline); !errors.As(err, &refusal) {
		t.Fatalf("a refresh past the deadline: %v, want a refusal", err)
	}
	if _, _, err := m.AccessToken(ctx, "org", "someone-else@grain-test-proj.iam.gserviceaccount.com", deadline.Add(time.Hour)); !errors.As(err, &refusal) {
		t.Fatalf("a token for an account the grant was not given for: %v, want a refusal", err)
	}
	if _, err := m.IDToken(ctx, "org", "someone-else@grain-test-proj.iam.gserviceaccount.com", "https://svc.example.com", deadline.Add(time.Hour)); !errors.As(err, &refusal) {
		t.Fatalf("an ID token for an account the grant was not given for: %v, want a refusal", err)
	}
	if id, err := m.IDToken(ctx, "org", orgSA, "https://svc.example.com", deadline.Add(time.Hour)); err != nil || id != "id-https://svc.example.com-"+orgSA {
		t.Fatalf("IDToken = %q, %v", id, err)
	}
	if err := m.Release(ctx, "a1"); err != nil || len(m.Leases()) != 0 {
		t.Fatalf("Release: %v, leases %v", err, m.Leases())
	}
	if cloud.Calls["DeleteKey"] != 0 {
		t.Fatal("releasing a token grant deleted a key")
	}
	noAccountChanges(t, cloud)

	// An admin that may not impersonate the account is denied at mint.
	cloud.AccountGranted = map[string][]string{orgSA: nil}
	if _, _, err := m.Mint(ctx, "a2", "org", now.Add(time.Hour)); !gcpsets.IsDenied(err) {
		t.Fatalf("mint without getAccessToken: %v, want a denial", err)
	}
}

func TestReportForAnExistingAccount(t *testing.T) {
	cloud := gcpfake.New()
	cloud.DefaultIdentity = "minter@" + proj + ".iam.gserviceaccount.com"
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	m := existingManager(t, map[string]any{"roles": []any{"roles/viewer", "roles/storage.admin"}}, cloud, &now)
	cloud.Bind("projects/"+proj, "roles/viewer", "serviceAccount:"+orgSA)
	// The admin lacks the pool permissions, which a set over an existing
	// account does not need: they are not reported.
	cloud.Granted = map[string][]string{proj: nil}
	ctx := context.Background()
	report := m.Report(ctx, gcpsets.Census{}, nil)
	for _, want := range []string{
		"has every permission it needs",
		"set org: existing service account " + orgSA + " (credential token",
		"admin minter can impersonate it",
		"no key cap",
		"LACKS listed role(s) on its project: roles/storage.admin",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "MISSING") {
		t.Errorf("report asks for pool permissions:\n%s", report)
	}

	cloud.AccountGranted = map[string][]string{orgSA: {"iam.serviceAccounts.get"}}
	report = m.Report(ctx, gcpsets.Census{}, nil)
	if !strings.Contains(report, "admin minter CANNOT impersonate it: it lacks iam.serviceAccounts.getAccessToken on the account (grant it roles/iam.serviceAccountTokenCreator there)") {
		t.Errorf("report:\n%s", report)
	}

	km := existingManager(t, map[string]any{"credential": "key"}, cloud, &now)
	report = km.Report(ctx, gcpsets.Census{}, nil)
	if !strings.Contains(report, "admin minter CANNOT mint keys for it: it lacks iam.serviceAccountKeys.create") ||
		!strings.Contains(report, "at most 10 concurrent attempts (GCP's key limit), 0 key(s) held now") {
		t.Errorf("key-mode report:\n%s", report)
	}
	noAccountChanges(t, cloud)
}
