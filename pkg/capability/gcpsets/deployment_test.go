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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/gcpkey"
	"github.com/ai-on-gke/grain/pkg/capability/gcpsets"
	"github.com/ai-on-gke/grain/pkg/capability/gcpsets/gcpfake"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

func TestAccountIDIsPrefixDeploymentSetAndNumber(t *testing.T) {
	if got := gcpsets.AccountID("grain", "049f1e09", "p", "ci", 1); got != "grain-049f1e09-ci-1" {
		t.Errorf("AccountID = %s", got)
	}
	c := config(t, nil)
	if got := c.AccountID(c.Set("reader"), 2); got != "grain-049f1e09-reader-2" {
		t.Errorf("Config.AccountID = %s", got)
	}
	c = config(t, map[string]any{"account_prefix": "ci7"})
	if got := c.AccountID(c.Set("reader"), 1); got != "ci7-049f1e09-reader-1" {
		t.Errorf("with account_prefix = %s", got)
	}
	for _, bad := range []string{"Grain", "7up", "gr-ain", "abcdefghijk", "g_x"} {
		if _, err := gcpsets.Parse(map[string]any{
			"account_prefix":            bad,
			protocol.DeploymentIDConfig: dep,
			"admins":                    []any{map[string]any{"name": "a", "credential_source": "ambient", "projects": []any{proj}, "role_ceiling": []any{"roles/viewer"}}},
			"permission_sets":           []any{map[string]any{"name": "s", "admin": "a", "project": proj, "roles": []any{"roles/viewer"}}},
		}); err == nil || !strings.Contains(err.Error(), "account_prefix") {
			t.Errorf("account_prefix %q: %v", bad, err)
		}
	}
}

// Every combination of prefix, deployment id, set name, project and n
// grain allows gives a valid, deterministic id; ids differing in any
// input never coincide.
func TestAccountIDsAreValidDeterministicAndDistinct(t *testing.T) {
	prefixes := []string{"grain", "g", "abcdefghij", "x1"}
	deployments := []string{
		"049f1e09", "a", "prod", "0", "9-9", "a-b-c-d", "bsalmon-c-googlers-com",
		strings.Repeat("d", 32), strings.Repeat("e", 31) + "f", "a" + strings.Repeat("-", 30) + "b",
		"0123456789abcdef0123456789abcdef",
	}
	sets := []string{
		"ci", "s", "sandbox-agent", "reader", "a-very-long-permission-set-name",
		"a-very-long-permission-set-nam2", strings.Repeat("z", 31), "x-" + strings.Repeat("y", 29),
		"a" + strings.Repeat("-", 29) + "b", "abc-", "q1-2-3",
	}
	projects := []string{proj, "other-proj-123"}
	seen := map[string]string{}
	for _, pre := range prefixes {
		for _, d := range deployments {
			for _, s := range sets {
				for _, p := range projects {
					for _, n := range []int{1, 2, 9, 10, 99, 100, 999} {
						id := gcpsets.AccountID(pre, d, p, s, n)
						if !gcpsets.ValidAccountID(id) {
							t.Fatalf("AccountID(%s, %s, %s, %s, %d) = %q, not a GCP account id", pre, d, p, s, n, id)
						}
						if again := gcpsets.AccountID(pre, d, p, s, n); again != id {
							t.Fatalf("AccountID is not deterministic: %q then %q", id, again)
						}
						if !strings.HasPrefix(id, pre+"-") || !strings.HasSuffix(id, fmt.Sprintf("-%d", n)) {
							t.Fatalf("AccountID(%s, %s, %s, %s, %d) = %q lost its prefix or number", pre, d, p, s, n, id)
						}
						key := fmt.Sprintf("%s|%s|%s|%s|%d", pre, d, p, s, n)
						if prev, ok := seen[id]; ok && !sameName(prev, key) {
							t.Fatalf("AccountID collides: %s and %s are both %q", prev, key, id)
						}
						seen[id] = key
					}
				}
			}
		}
	}
}

// sameName is two inputs that are meant to give the same id: a name
// short enough to be used whole does not depend on its project.
func sameName(a, b string) bool {
	pa, pb := strings.Split(a, "|"), strings.Split(b, "|")
	return pa[0] == pb[0] && pa[1] == pb[1] && pa[3] == pb[3] && pa[4] == pb[4]
}

// A shortened id keeps the readable start of the set first, cutting the
// deployment id only when the set's first characters and hash do not fit
// beside it.
func TestALongNameCutsTheSetBeforeTheDeployment(t *testing.T) {
	id := gcpsets.AccountID("grain", "049f1e09", proj, "a-very-long-permission-set-name", 1)
	if !strings.HasPrefix(id, "grain-049f1e09-a-very") || len(id) != 30 {
		t.Errorf("long set = %q", id)
	}
	long := strings.Repeat("d", 32)
	id = gcpsets.AccountID("grain", long, proj, "ci", 1)
	if !strings.HasPrefix(id, "grain-ddd") || !strings.HasSuffix(id, "-ci-1") || len(id) > 30 {
		t.Errorf("long deployment = %q", id)
	}
	id = gcpsets.AccountID("grain", long, proj, "sandbox-agent", 1)
	if !strings.Contains(id, "-san-") || !strings.HasPrefix(id, "grain-d") {
		t.Errorf("long deployment and set = %q, want both readable starts kept", id)
	}
	// The set's hash covers its project.
	if gcpsets.AccountID("grain", "049f1e09", "proj-one", "a-very-long-permission-set-name", 1) ==
		gcpsets.AccountID("grain", "049f1e09", "proj-two", "a-very-long-permission-set-name", 1) {
		t.Error("a long set name gives the same id in two projects")
	}
}

func TestLegacyAccountIDIsTheOldScheme(t *testing.T) {
	if got := gcpsets.LegacyAccountID("reader", 1); got != "grain-reader-1" {
		t.Errorf("LegacyAccountID = %s", got)
	}
	long := "a-very-long-permission-set-name"
	for n := 1; n <= 99; n++ {
		id := gcpsets.LegacyAccountID(long, n)
		if !gcpsets.ValidAccountID(id) || id == gcpsets.LegacyAccountID("a-very-long-permission-set-nam2", n) {
			t.Fatalf("LegacyAccountID(%s, %d) = %s", long, n, id)
		}
	}
}

func TestOwnerMarkersRoundTripAndFitGCPsDescription(t *testing.T) {
	d := gcpsets.OwnerDescription(strings.Repeat("d", 32), strings.Repeat("f", 32), strings.Repeat("s", 31))
	if len(d) > 256 {
		t.Errorf("the longest description is %d characters, past GCP's 256", len(d))
	}
	o, ok := gcpsets.OwnedBy(ours("ci"))
	if !ok || o != (gcpsets.Owner{Deployment: dep, Instance: "049f1e09c0ffee01", Set: "ci"}) {
		t.Errorf("OwnedBy(new) = %+v %v", o, ok)
	}
	o, ok = gcpsets.OwnedBy(gcpsets.LegacyOwnerDescription("ci"))
	if !ok || o != (gcpsets.Owner{Set: "ci", Legacy: true}) {
		t.Errorf("OwnedBy(legacy) = %+v %v", o, ok)
	}
	if _, ok := gcpsets.OwnedBy("made by hand"); ok {
		t.Error("an unmarked description is owned")
	}
}

// deployment is a configuration for one grain deployment over a shared
// project: set "ci", created on demand.
func deployment(t *testing.T, id, instance string, extra map[string]any) *gcpsets.Config {
	t.Helper()
	set := map[string]any{"name": "ci", "admin": "a", "project": proj, "roles": []any{"roles/viewer"}, "pool_max": float64(3)}
	for k, v := range extra {
		set[k] = v
	}
	cfg := map[string]any{
		protocol.DeploymentIDConfig: id,
		"admins":                    []any{map[string]any{"name": "a", "credential_source": "ambient", "projects": []any{proj}, "role_ceiling": []any{"roles/viewer"}}},
		"permission_sets":           []any{set},
	}
	if instance != "" {
		cfg[protocol.InstanceIDConfig] = instance
	}
	c, err := gcpsets.Parse(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Two deployments in one project with the same set name create disjoint
// accounts, never lease on each other's, and never see each other's as
// orphans -- even when a second one is given the first one's id.
func TestTwoDeploymentsSharingAProjectAndSetNameAreDisjoint(t *testing.T) {
	cloud := gcpfake.New()
	now := time.Now()
	ctx := context.Background()
	a := manager(t, deployment(t, "049f1e09", "aaaa0000aaaa0000aaaa0000aaaa0000", nil), cloud, &now)
	b := manager(t, deployment(t, "prod", "bbbb0000bbbb0000bbbb0000bbbb0000", nil), cloud, &now)
	// Fill a's first account so the reuse path is exercised too.
	for i := 0; i < gcpsets.KeysPerAccount+1; i++ {
		if _, _, err := a.Mint(ctx, fmt.Sprintf("a-%d", i), "ci", now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	_, lb, err := b.Mint(ctx, "b-0", "ci", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if lb.Account != gcpsets.Email("grain-prod-ci-1", proj) {
		t.Errorf("b leased %s", lb.Account)
	}
	accA, _ := a.Accounts(ctx, a.Config.Set("ci"))
	accB, _ := b.Accounts(ctx, b.Config.Set("ci"))
	if strings.Join(accA, ",") != gcpsets.Email("grain-049f1e09-ci-1", proj)+","+gcpsets.Email("grain-049f1e09-ci-2", proj) {
		t.Errorf("a's accounts = %v", accA)
	}
	if strings.Join(accB, ",") != lb.Account {
		t.Errorf("b's accounts = %v", accB)
	}
	for _, m := range []*gcpsets.Manager{a, b} {
		census, err := m.Census(ctx)
		if err != nil || len(census.Orphans) != 0 {
			t.Errorf("%s sees orphans %+v (%v)", m.Config.Deployment, census.Orphans, err)
		}
	}
	// Each set's bindings hold only its own accounts beside the other's,
	// which reconciling never takes away.
	if err := a.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := cloud.Members("projects/"+proj, "roles/viewer"); len(got) != 3 {
		t.Errorf("viewer = %v, want both deployments' accounts", got)
	}

	// The other deployment removes its set and asks for its orphans to go:
	// only its own account is deleted.
	gone := manager(t, func() *gcpsets.Config {
		c := deployment(t, "prod", "bbbb0000bbbb0000bbbb0000bbbb0000", nil)
		c.Sets[0].Name = "other"
		c.DeleteOrphans = []string{"ci"}
		return c
	}(), cloud, &now)
	census, err := gone.Census(ctx)
	if err != nil || len(census.Orphans) != 1 || census.Orphans[0].Email != lb.Account || census.Others[proj] != 2 {
		t.Fatalf("census = %+v %v", census, err)
	}
	deleted, err := gone.DeleteOrphans(ctx, census.Orphans)
	if err != nil || strings.Join(deleted, ",") != lb.Account {
		t.Fatalf("deleted %v %v", deleted, err)
	}
	if r := gone.Report(ctx, census, deleted); !strings.Contains(r, "2 service account(s) of other grain deployments") || strings.Contains(r, "grain-049f1e09") {
		t.Errorf("report:\n%s", r)
	}
	if after, _ := a.Accounts(ctx, a.Config.Set("ci")); len(after) != 2 {
		t.Errorf("a's accounts after b's cleanup = %v", after)
	}

	// The reaper of one deployment never touches the other's keys.
	now = now.Add(48 * time.Hour)
	c := manager(t, deployment(t, "third", "cccc0000cccc0000cccc0000cccc0000", nil), cloud, &now)
	if deleted, err := c.Reap(ctx); err != nil || len(deleted) != 0 {
		t.Errorf("a third deployment reaped %v %v", deleted, err)
	}
}

// Two deployments an admin gave the same id: the installation still
// tells them apart, and the second skips the name the first holds.
func TestTheSameDeploymentIDOnTwoInstallationsDoesNotShare(t *testing.T) {
	cloud := gcpfake.New()
	now := time.Now()
	ctx := context.Background()
	a := manager(t, deployment(t, "prod", "aaaa0000aaaa0000aaaa0000aaaa0000", nil), cloud, &now)
	b := manager(t, deployment(t, "prod", "bbbb0000bbbb0000bbbb0000bbbb0000", nil), cloud, &now)
	_, la, err := a.Mint(ctx, "a-0", "ci", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	_, lb, err := b.Mint(ctx, "b-0", "ci", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if la.Account == lb.Account || lb.Account != gcpsets.Email("grain-prod-ci-2", proj) {
		t.Errorf("a %s, b %s", la.Account, lb.Account)
	}
	if acc, _ := b.Accounts(ctx, b.Config.Set("ci")); strings.Join(acc, ",") != lb.Account {
		t.Errorf("b's accounts = %v", acc)
	}
}

// A deployment id renamed in Settings keeps its accounts -- the
// installation in their marker says they are still its own -- and names
// the accounts it creates afterwards under the new id.
func TestARenamedDeploymentKeepsItsAccountsAndNamesNewOnesAfresh(t *testing.T) {
	cloud := gcpfake.New()
	now := time.Now()
	ctx := context.Background()
	const install = "aaaa0000aaaa0000aaaa0000aaaa0000"
	before := manager(t, deployment(t, "049f1e09", install, nil), cloud, &now)
	for i := 0; i < gcpsets.KeysPerAccount; i++ {
		if _, _, err := before.Mint(ctx, fmt.Sprintf("old-%d", i), "ci", now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	old := gcpsets.Email("grain-049f1e09-ci-1", proj)

	after := manager(t, deployment(t, "bsalmon", install, nil), cloud, &now)
	acc, err := after.Accounts(ctx, after.Config.Set("ci"))
	if err != nil || strings.Join(acc, ",") != old {
		t.Fatalf("accounts after the rename = %v %v", acc, err)
	}
	// The old account is full: the next one is named under the new id.
	_, l, err := after.Mint(ctx, "new-0", "ci", now.Add(time.Hour))
	if err != nil || l.Account != gcpsets.Email("grain-bsalmon-ci-1", proj) {
		t.Fatalf("mint after the rename = %+v %v", l, err)
	}
	// Leases of the old name are still reaped and nothing is an orphan.
	census, err := after.Census(ctx)
	if err != nil || len(census.Orphans) != 0 || census.Others[proj] != 0 {
		t.Errorf("census = %+v %v", census, err)
	}
	now = now.Add(48 * time.Hour)
	if deleted, err := after.Reap(ctx); err != nil || len(deleted) != gcpsets.KeysPerAccount {
		t.Errorf("reaped %d (%v), want the old name's unheld keys", len(deleted), err)
	}
	if cloud.Calls["DeleteServiceAccount"] != 0 {
		t.Error("a rename deleted an account")
	}
}

// Without an instance id -- a configuration grain did not attach -- the
// deployment id alone decides ownership.
func TestWithoutAnInstanceTheDeploymentIDDecides(t *testing.T) {
	c := deployment(t, "049f1e09", "", nil)
	if !c.Ours(gcpsets.Owner{Deployment: "049f1e09", Instance: "ffff", Set: "ci"}) || c.Ours(gcpsets.Owner{Deployment: "prod", Set: "ci"}) {
		t.Error("ownership without an instance")
	}
	if c.Ours(gcpsets.Owner{Set: "ci", Legacy: true}) {
		t.Error("a legacy marker is ours")
	}
}

func TestLegacyAccountsAreAdoptedOnlyWhenTheSetSaysSo(t *testing.T) {
	cloud := gcpfake.New()
	now := time.Now()
	ctx := context.Background()
	legacy := gcpsets.Email("grain-ci-1", proj)
	cloud.AddAccount(proj, legacy, gcpsets.LegacyOwnerDescription("ci"))
	// Named like a legacy account but not by the old scheme: never adopted.
	cloud.AddAccount(proj, gcpsets.Email("grain-ci-x-1", proj), gcpsets.LegacyOwnerDescription("ci"))

	off := manager(t, deployment(t, "049f1e09", inst, nil), cloud, &now)
	if acc, _ := off.Accounts(ctx, off.Config.Set("ci")); len(acc) != 0 {
		t.Errorf("without adopt_legacy: %v", acc)
	}
	census, _ := off.Census(ctx)
	if census.Legacy[proj] != 2 || len(census.Orphans) != 0 {
		t.Errorf("census without adopt_legacy = %+v", census)
	}
	if r := off.Report(ctx, census, nil); !strings.Contains(r, "2 service account(s) with the old marker") {
		t.Errorf("report:\n%s", r)
	}
	_, l, err := off.Mint(ctx, "e0", "ci", now.Add(time.Hour))
	if err != nil || l.Account != gcpsets.Email("grain-049f1e09-ci-1", proj) {
		t.Fatalf("mint without adopt_legacy = %+v %v", l, err)
	}

	on := manager(t, deployment(t, "049f1e09", inst, map[string]any{"adopt_legacy": true}), cloud, &now)
	acc, _ := on.Accounts(ctx, on.Config.Set("ci"))
	if strings.Join(acc, ",") != gcpsets.Email("grain-049f1e09-ci-1", proj)+","+legacy {
		t.Errorf("with adopt_legacy: %v", acc)
	}
	census, _ = on.Census(ctx)
	if census.Legacy[proj] != 1 {
		t.Errorf("census with adopt_legacy = %+v", census)
	}
	// The adopted account is bound like the set's own and is never an
	// orphan, deleted or renamed -- even once the set is removed.
	if err := on.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := cloud.Members("projects/"+proj, "roles/viewer"); len(got) != 2 {
		t.Errorf("viewer = %v", got)
	}
	removed := deployment(t, "049f1e09", inst, nil)
	removed.Sets[0].Name = "other"
	removed.DeleteOrphans = []string{"ci"}
	rm := manager(t, removed, cloud, &now)
	census, _ = rm.Census(ctx)
	if deleted, _ := rm.DeleteOrphans(ctx, census.Orphans); strings.Join(deleted, ",") != gcpsets.Email("grain-049f1e09-ci-1", proj) {
		t.Errorf("delete_orphans deleted %v, want only this deployment's own account", deleted)
	}
	if _, ok := cloud.Accounts[proj][legacy]; !ok {
		t.Error("the legacy account was deleted")
	}
}

// The migrated fixed pool still mints from grain-sb-agent-0..9, whatever
// the deployment id, and is never renamed or reaped.
func TestTheMigratedSandboxAgentSetStillMintsFromItsPool(t *testing.T) {
	cloud := gcpfake.New()
	now := time.Now()
	ctx := context.Background()
	pool := gcpkey.PoolEmails("grain-sb-agent-0@"+proj+".iam.gserviceaccount.com", 10, proj)
	for _, e := range pool {
		cloud.AddAccount(proj, e, "")
	}
	c, err := gcpsets.Migrate(proj, []gcpsets.LegacyAccount{{Name: "default", Email: pool[0], Count: 10}}, adminKey)
	if err != nil {
		t.Fatal(err)
	}
	c.Deployment, c.Instance = dep, inst
	set := c.Set(gcpsets.DefaultSet)
	if set == nil || !set.AdoptLegacy {
		t.Fatalf("migrated set = %+v", set)
	}
	m := manager(t, c, cloud, &now)
	_, l, err := m.Mint(ctx, "e0", gcpsets.DefaultSet, now.Add(time.Hour))
	if err != nil || !strings.HasPrefix(l.Account, "grain-sb-agent-") {
		t.Fatalf("mint = %+v %v", l, err)
	}
	if cloud.Calls["CreateServiceAccount"] != 0 {
		t.Error("the migrated set created an account")
	}
	census, _ := m.Census(ctx)
	if len(census.Orphans) != 0 {
		t.Errorf("orphans = %+v", census.Orphans)
	}
}

func TestASetThatCreatesAccountsNeedsTheDeploymentID(t *testing.T) {
	_, err := gcpsets.Parse(map[string]any{
		"admins":          []any{map[string]any{"name": "a", "credential_source": "ambient", "projects": []any{proj}, "role_ceiling": []any{"roles/viewer"}}},
		"permission_sets": []any{map[string]any{"name": "s", "admin": "a", "project": proj, "roles": []any{"roles/viewer"}}},
	})
	if err == nil || !strings.Contains(err.Error(), protocol.DeploymentIDConfig) {
		t.Errorf("no deployment id: %v", err)
	}
	for _, bad := range []string{"Prod", "-x", "x-", strings.Repeat("a", 33), "a_b"} {
		if _, err := gcpsets.Parse(map[string]any{
			protocol.DeploymentIDConfig: bad,
			"admins":                    []any{map[string]any{"name": "a", "credential_source": "ambient", "projects": []any{proj}, "role_ceiling": []any{"roles/viewer"}}},
			"permission_sets":           []any{map[string]any{"name": "s", "admin": "a", "project": proj, "roles": []any{"roles/viewer"}}},
		}); err == nil {
			t.Errorf("deployment id %q accepted", bad)
		}
	}
}

func TestTestReportsTheDeploymentAndHowAccountsAreNamed(t *testing.T) {
	now := time.Now()
	m := manager(t, config(t, nil), gcpfake.New(), &now)
	r := m.Report(context.Background(), gcpsets.Census{}, nil)
	if !strings.Contains(r, "deployment 049f1e09 (instance 049f1e09c0ffee01): new service accounts are named grain-049f1e09-<set>-<n>") {
		t.Errorf("report:\n%s", r)
	}
}

// A configuration without an instance id still owns the accounts it
// creates: its marker, written with an empty instance, reads back as its
// own, so its pool stays bounded and its accounts are not lost.
func TestWithoutAnInstanceCreatedAccountsAreOwned(t *testing.T) {
	cloud := gcpfake.New()
	now := time.Now()
	ctx := context.Background()
	m := manager(t, deployment(t, "049f1e09", "", nil), cloud, &now)
	for i := 0; i < 3*gcpsets.KeysPerAccount; i++ {
		if _, _, err := m.Mint(ctx, fmt.Sprintf("a-%d", i), "ci", now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := m.Mint(ctx, "over", "ci", now.Add(time.Hour)); err == nil {
		t.Error("minted past pool_max")
	}
	acc, err := m.Accounts(ctx, m.Config.Set("ci"))
	if err != nil || len(acc) != 3 {
		t.Errorf("accounts = %v %v", acc, err)
	}
	if n := cloud.Calls["CreateServiceAccount"]; n != 3 {
		t.Errorf("created %d accounts, want 3", n)
	}
}
