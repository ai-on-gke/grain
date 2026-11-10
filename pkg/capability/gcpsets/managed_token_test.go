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
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/gcpsets"
	"github.com/ai-on-gke/grain/pkg/capability/gcpsets/gcpfake"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

func managedTokenConfig(t *testing.T, set map[string]any, extra map[string]any) *gcpsets.Config {
	t.Helper()
	s := map[string]any{
		"name":       "ar-writer",
		"admin":      "minter",
		"project":    proj,
		"credential": "token",
		"roles":      []any{"roles/artifactregistry.writer"},
	}
	for k, v := range set {
		if v == nil {
			delete(s, k)
		} else {
			s[k] = v
		}
	}
	cfg := map[string]any{
		protocol.DeploymentIDConfig: dep,
		protocol.InstanceIDConfig:   inst,
		"admins": []any{
			map[string]any{
				"name":              "minter",
				"credential_source": "configured",
				"projects":          []any{proj},
				"role_ceiling":      []any{"roles/artifactregistry.writer", "roles/storage.objectViewer"},
			},
		},
		"admin_credentials": `{"minter": ` + adminKey + `}`,
		"permission_sets":   []any{s},
	}
	for k, v := range extra {
		cfg[k] = v
	}
	c, err := gcpsets.Parse(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestManagedTokenCreateBindTokenCreatorOnConfigureAndIdempotentReconfigure(t *testing.T) {
	ctx := context.Background()
	cloud := gcpfake.New()
	cloud.RequireTokenCreator = true
	now := time.Date(2026, 11, 10, 1, 0, 0, 0, time.UTC)

	c := managedTokenConfig(t, nil, nil)
	m := manager(t, c, cloud, &now)

	// First configure (Reconcile): creates grain-<deploy>-<set>, binds roles on
	// the project, and grants the admin roles/iam.serviceAccountTokenCreator on
	// that service account only.
	if err := m.Reconcile(ctx); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}

	wantEmail := "grain-" + dep + "-ar-writer@" + proj + ".iam.gserviceaccount.com"
	sa, ok := cloud.Accounts[proj][wantEmail]
	if !ok {
		t.Fatalf("expected managed service account %s, got %v", wantEmail, cloud.Accounts[proj])
	}
	if o, ok := gcpsets.OwnedBy(sa.Description); !ok || !c.Ours(o) || o.Set != "ar-writer" {
		t.Fatalf("account description %q is not marked as owned by ar-writer", sa.Description)
	}
	if got := cloud.Members("projects/"+proj, "roles/artifactregistry.writer"); len(got) != 1 || got[0] != "serviceAccount:"+wantEmail {
		t.Fatalf("project bindings = %v, want [serviceAccount:%s]", got, wantEmail)
	}
	// tokenCreator is granted on the service account ONLY, never on the project.
	if got := cloud.Members("projects/"+proj, "roles/iam.serviceAccountTokenCreator"); len(got) != 0 {
		t.Fatalf("tokenCreator was bound at project scope: %v", got)
	}
	saRes := "projects/" + proj + "/serviceAccounts/" + wantEmail
	wantAdminMember := "serviceAccount:minter@" + proj + ".iam.gserviceaccount.com"
	if got := cloud.Members(saRes, "roles/iam.serviceAccountTokenCreator"); len(got) != 1 || got[0] != wantAdminMember {
		t.Fatalf("SA tokenCreator bindings = %v, want [%s]", got, wantAdminMember)
	}
	if cloud.LiveKeys() != 0 || cloud.Calls["CreateKey"] != 0 {
		t.Fatal("managed token configure created a key")
	}

	// Idempotent reconfigure & restart: a new Manager with the same config makes
	// zero CreateServiceAccount calls and zero SetPolicy calls.
	createsBefore := cloud.Calls["CreateServiceAccount"]
	setsBefore := cloud.Calls["SetPolicy"]
	m2 := manager(t, managedTokenConfig(t, nil, nil), cloud, &now)
	if err := m2.Reconcile(ctx); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if cloud.Calls["CreateServiceAccount"] != createsBefore {
		t.Errorf("CreateServiceAccount calls = %d, want %d", cloud.Calls["CreateServiceAccount"], createsBefore)
	}
	if cloud.Calls["SetPolicy"] != setsBefore {
		t.Errorf("SetPolicy calls = %d, want %d (idempotent)", cloud.Calls["SetPolicy"], setsBefore)
	}

	// Minting and exchanging tokens uses the managed account with no keys.
	deadline := now.Add(2 * time.Hour)
	keyJSON, lease, err := m2.Mint(ctx, "att-1", "ar-writer", deadline)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if keyJSON != "" || lease.KeyID != "" || lease.Account != wantEmail {
		t.Fatalf("Mint = (%q, %+v), want keyless lease for %s", keyJSON, lease, wantEmail)
	}
	tok, exp, err := m2.AccessToken(ctx, "ar-writer", wantEmail, deadline)
	if err != nil || !strings.HasSuffix(tok, wantEmail) || !exp.Equal(now.Add(gcpsets.MaxTokenLifetime)) {
		t.Fatalf("AccessToken = (%q, %v, %v)", tok, exp, err)
	}
	idTok, err := m2.IDToken(ctx, "ar-writer", wantEmail, "https://ar.example", deadline)
	if err != nil || idTok != "id-https://ar.example-"+wantEmail {
		t.Fatalf("IDToken = (%q, %v)", idTok, err)
	}

	// Across a server restart before Reconcile has cached managedAccounts,
	// AccessToken still resolves the owned account from GCP and refuses others.
	m3 := manager(t, managedTokenConfig(t, nil, nil), cloud, &now)
	if tok2, _, err := m3.AccessToken(ctx, "ar-writer", wantEmail, deadline); err != nil || !strings.HasSuffix(tok2, wantEmail) {
		t.Fatalf("post-restart AccessToken = (%q, %v)", tok2, err)
	}
	var refusal *gcpsets.Refusal
	if _, _, err := m3.AccessToken(ctx, "ar-writer", "other@"+proj+".iam.gserviceaccount.com", deadline); !errors.As(err, &refusal) {
		t.Fatalf("foreign account AccessToken = %v, want Refusal", err)
	}

	// Reap skips token-mode sets without touching ListKeys or DeleteKey.
	if deleted, err := m2.Reap(ctx); err != nil || len(deleted) != 0 {
		t.Fatalf("Reap = (%v, %v)", deleted, err)
	}
	if err := m2.Release(ctx, "att-1"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if cloud.LiveKeys() != 0 || cloud.Calls["CreateKey"] != 0 || cloud.Calls["DeleteKey"] != 0 {
		t.Fatalf("unexpected key operations: calls=%v", cloud.Calls)
	}
}

func TestManagedTokenArtifactRegistryScopeAndCleanup(t *testing.T) {
	ctx := context.Background()
	cloud := gcpfake.New()
	cloud.RequireTokenCreator = true
	now := time.Date(2026, 11, 10, 1, 0, 0, 0, time.UTC)

	const scope = "repositories/us-central1/images"
	arResource := "projects/" + proj + "/locations/us-central1/repositories/images"

	// Simulate least-privilege admin: no project-wide setIamPolicy/getIamPolicy,
	// only service-account admin permissions on the project and repoAdmin on the
	// single Artifact Registry repository.
	cloud.Denied = map[string]bool{
		"resourcemanager.projects.getIamPolicy": true,
		"resourcemanager.projects.setIamPolicy": true,
	}
	cloud.Granted = map[string][]string{
		proj:       gcpsets.TokenAdminServiceAccountPermissions,
		arResource: gcpsets.ArtifactRegistryScopePermissions,
	}

	c := managedTokenConfig(t, map[string]any{"resource_scope": scope}, nil)
	m := manager(t, c, cloud, &now)
	if err := m.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile AR-scoped set: %v", err)
	}

	wantEmail := "grain-" + dep + "-ar-writer@" + proj + ".iam.gserviceaccount.com"
	if got := cloud.Members(arResource, "roles/artifactregistry.writer"); len(got) != 1 || got[0] != "serviceAccount:"+wantEmail {
		t.Fatalf("AR repo bindings = %v, want [serviceAccount:%s]", got, wantEmail)
	}
	if got := cloud.Members("projects/"+proj, "roles/artifactregistry.writer"); len(got) != 0 {
		t.Fatalf("project bindings = %v, want empty", got)
	}

	// Report sees repo-level permissions via TestResourcePermissions and marks
	// the admin green.
	report := m.Report(ctx, gcpsets.Census{}, nil)
	for _, want := range []string{
		"has every permission it needs",
		"set ar-writer (" + proj + " on " + scope + ", roles/artifactregistry.writer): service account " + wantEmail + " (credential token; short-lived tokens, no keys)",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}

	// Removing the set without delete_orphans retains the account and reports it.
	keepCfg := managedTokenConfig(t, map[string]any{
		"name":  "other-reader",
		"roles": []any{"roles/storage.objectViewer"},
	}, nil)
	delete(cloud.Denied, "resourcemanager.projects.getIamPolicy")
	delete(cloud.Denied, "resourcemanager.projects.setIamPolicy")
	mKeep := manager(t, keepCfg, cloud, &now)
	census, err := mKeep.Census(ctx)
	if err != nil || len(census.Orphans) != 1 || census.Orphans[0].Email != wantEmail {
		t.Fatalf("Census orphans = %+v, err = %v", census.Orphans, err)
	}
	deleted, err := mKeep.DeleteOrphans(ctx, census.Orphans)
	if err != nil || len(deleted) != 0 {
		t.Fatalf("DeleteOrphans without opt-in = (%v, %v)", deleted, err)
	}
	if _, stillThere := cloud.Accounts[proj][wantEmail]; !stillThere {
		t.Fatal("removed set's account was deleted without delete_orphans")
	}

	// Naming the removed set in delete_orphans unbinds the AR repository first,
	// then deletes the service account, even without project IAM permissions.
	cloud.Denied["resourcemanager.projects.getIamPolicy"] = true
	cloud.Denied["resourcemanager.projects.setIamPolicy"] = true
	delCfg := managedTokenConfig(t, map[string]any{
		"name":           "other-reader",
		"roles":          []any{"roles/artifactregistry.writer"},
		"resource_scope": "repositories/us-central1/other",
	}, map[string]any{"delete_orphans": []any{"ar-writer"}})
	mDel := manager(t, delCfg, cloud, &now)
	census, err = mDel.Census(ctx)
	if err != nil {
		t.Fatalf("Census: %v", err)
	}
	deleted, err = mDel.DeleteOrphans(ctx, census.Orphans)
	if err != nil || len(deleted) != 1 || deleted[0] != wantEmail {
		t.Fatalf("DeleteOrphans = (%v, %v), want [%s]", deleted, err, wantEmail)
	}
	if got := cloud.Members(arResource, "roles/artifactregistry.writer"); len(got) != 0 {
		t.Errorf("AR repo binding survived orphan deletion: %v", got)
	}
	if _, stillThere := cloud.Accounts[proj][wantEmail]; stillThere {
		t.Errorf("account %s survived DeleteOrphans", wantEmail)
	}
}

func TestManagedTokenRoleCeilingRefusal(t *testing.T) {
	cfg := map[string]any{
		protocol.DeploymentIDConfig: dep,
		protocol.InstanceIDConfig:   inst,
		"admins": []any{
			map[string]any{
				"name":              "minter",
				"credential_source": "configured",
				"projects":          []any{proj},
				"role_ceiling":      []any{"roles/artifactregistry.reader"},
			},
		},
		"admin_credentials": `{"minter": ` + adminKey + `}`,
		"permission_sets": []any{
			map[string]any{
				"name":       "ar-writer",
				"admin":      "minter",
				"project":    proj,
				"credential": "token",
				"roles":      []any{"roles/artifactregistry.writer"},
			},
		},
	}
	_, err := gcpsets.Parse(cfg)
	if err == nil || !strings.Contains(err.Error(), "roles/artifactregistry.writer exceeds admin minter's role ceiling") {
		t.Fatalf("Parse = %v, want role ceiling refusal naming roles/artifactregistry.writer", err)
	}
}

func TestManagedTokenMissingAdminPermissionsAreNamed(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 1, 0, 0, 0, time.UTC)

	for _, perm := range []string{
		"iam.serviceAccounts.create",
		"iam.serviceAccounts.setIamPolicy",
		"resourcemanager.projects.setIamPolicy",
		"artifactregistry.repositories.setIamPolicy",
	} {
		t.Run(perm, func(t *testing.T) {
			cloud := gcpfake.New()
			cloud.Denied = map[string]bool{perm: true}
			setOverrides := map[string]any{}
			if strings.HasPrefix(perm, "artifactregistry.") {
				setOverrides["resource_scope"] = "repositories/us-central1/images"
			}
			c := managedTokenConfig(t, setOverrides, nil)
			m := manager(t, c, cloud, &now)

			err := m.Reconcile(ctx)
			if err == nil || !gcpsets.IsDenied(err) || !strings.Contains(err.Error(), perm) {
				t.Fatalf("Reconcile err = %v, want denial naming %s", err, perm)
			}
			_, _, mintErr := m.Mint(ctx, "att-1", "ar-writer", now.Add(time.Hour))
			if mintErr == nil || !gcpsets.IsDenied(mintErr) || !strings.Contains(mintErr.Error(), perm) {
				t.Fatalf("Mint err = %v, want denial naming %s", mintErr, perm)
			}
			report := m.Report(ctx, gcpsets.Census{}, nil)
			if !strings.Contains(report, "MISSING:") || !strings.Contains(report, perm) {
				t.Fatalf("Report did not list missing %s:\n%s", perm, report)
			}
		})
	}
}

func TestPoolModeDisableServiceAccountKeyCreationSuggestsTokenModeConfig(t *testing.T) {
	ctx := context.Background()
	cloud := gcpfake.New()
	cloud.DisableKeyCreation = map[string]bool{proj: true}
	now := time.Date(2026, 11, 10, 1, 0, 0, 0, time.UTC)

	c := managedTokenConfig(t, map[string]any{
		"credential":     nil, // omit credential -> legacy pool/key mode
		"resource_scope": "repositories/us-central1/images",
	}, nil)
	m := manager(t, c, cloud, &now)

	_, _, err := m.Mint(ctx, "att-key", "ar-writer", now.Add(time.Hour))
	var refusal *gcpsets.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Mint err = %v, want *gcpsets.Refusal", err)
	}
	// Must NOT switch to token mode automatically.
	if cloud.Calls["GenerateAccessToken"] != 0 {
		t.Fatalf("pool-mode failure switched to token mode automatically (GenerateAccessToken=%d)", cloud.Calls["GenerateAccessToken"])
	}
	for _, want := range []string{
		"constraints/iam.disableServiceAccountKeyCreation",
		`"credential":"token"`,
		`"name":"ar-writer"`,
		`"project":"` + proj + `"`,
		`"resource_scope":"repositories/us-central1/images"`,
	} {
		if !strings.Contains(refusal.Reason, want) {
			t.Errorf("refusal lacks %q:\n%s", want, refusal.Reason)
		}
	}

	// Verify the suggested JSON snippet parses cleanly as a managed token set.
	snippet := c.Set("ar-writer").TokenModeConfigJSON()
	var parsedSet map[string]any
	if err := json.Unmarshal([]byte(snippet), &parsedSet); err != nil {
		t.Fatalf("TokenModeConfigJSON invalid JSON %q: %v", snippet, err)
	}
	switched, err := gcpsets.Parse(map[string]any{
		protocol.DeploymentIDConfig: dep,
		protocol.InstanceIDConfig:   inst,
		"admins": []any{
			map[string]any{
				"name":              "minter",
				"credential_source": "configured",
				"projects":          []any{proj},
				"role_ceiling":      []any{"roles/artifactregistry.writer"},
			},
		},
		"admin_credentials": `{"minter": ` + adminKey + `}`,
		"permission_sets":   []any{parsedSet},
	})
	if err != nil || !switched.Set("ar-writer").ManagedToken() {
		t.Fatalf("suggested config did not parse as ManagedToken: %v", err)
	}
}
