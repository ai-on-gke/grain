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

package intree

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/gcpkey"
	"github.com/ai-on-gke/grain/pkg/capability/gcpsets/gcpfake"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
)

type mapCredResolver map[string]string

func (m mapCredResolver) Resolve(_ context.Context, name string) (string, error) {
	v, ok := m[name]
	if !ok {
		return "", fmt.Errorf("missing secret %q", name)
	}
	return v, nil
}

func extractClientEmail(t *testing.T, keyJSON string) string {
	t.Helper()
	var parsed struct {
		ClientEmail string `json:"client_email"`
	}
	if err := json.Unmarshal([]byte(keyJSON), &parsed); err != nil {
		t.Fatalf("unmarshalling minted key JSON %q: %v", keyJSON, err)
	}
	if parsed.ClientEmail == "" {
		t.Fatalf("minted key JSON missing client_email: %q", keyJSON)
	}
	return parsed.ClientEmail
}

// TestGCPKeyMigrationFidelity proves that a task holding bare gcp-key and
// named gcp-key:<name> before schema migration 97 receives the exact same
// service accounts, IAM roles, max lease bounds, and sandbox credential file
// placements after migration 97 seeds permission sets onto
// https://grain.dev/capabilities/gcp and rewrites the stored grants -- and
// that the one-release alias spellings normalize to the same grants.
func TestGCPKeyMigrationFidelity(t *testing.T) {
	ctx := context.Background()
	store, db := sqlitetest.Open(t)

	cloud := gcpfake.New()
	cloud.DefaultIdentity = "minter@acme-admin.iam.gserviceaccount.com"

	poolAccounts := []string{
		"sandbox-agent-0@acme-pool.iam.gserviceaccount.com",
		"sandbox-agent-1@acme-pool.iam.gserviceaccount.com",
	}
	for _, sa := range poolAccounts {
		cloud.AddAccount("acme-pool", sa, "Operator-managed sandbox agent pool")
		cloud.Bind("projects/acme-pool", "roles/storage.objectViewer", "serviceAccount:"+sa)
	}
	deployAccounts := []string{
		"deployer-0@acme-deploy.iam.gserviceaccount.com",
		"deployer-1@acme-deploy.iam.gserviceaccount.com",
	}
	for _, sa := range deployAccounts {
		cloud.AddAccount("acme-deploy", sa, "Operator-managed deploy pool")
		cloud.Bind("projects/acme-deploy", "roles/run.admin", "serviceAccount:"+sa)
	}

	secretsMap := map[string]string{
		"gcp-key-minter": `{"type":"service_account","client_email":"pool-minter@acme-admin.iam.gserviceaccount.com"}`,
		"deploy-minter":  `{"type":"service_account","client_email":"deploy-minter@acme-admin.iam.gserviceaccount.com"}`,
	}

	cfg := model.DefaultConfig()
	cfg.GCPProject = "acme-pool"
	cfg.GCPServiceAccountEmail = "sandbox-agent@acme-pool.iam.gserviceaccount.com"
	cfg.GCPServiceAccountCount = 2
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.PutGCPKey(ctx, model.GCPKey{
		Name:                "deploy",
		ProjectID:           "acme-deploy",
		ServiceAccountEmail: "deployer@acme-deploy.iam.gserviceaccount.com",
		ServiceAccountCount: 2,
		MinterCredential:    "deploy-minter",
		MaxKeyAge:           2 * time.Hour,
		Description:         "Deploy pool",
	}); err != nil {
		t.Fatal(err)
	}

	// The gcp server checks deadlines against the real clock, so the test
	// clock starts from it rather than a fixed date that later passes.
	now := time.Now().UTC().Truncate(time.Second)
	cloud.Now = func() time.Time { return now }
	if err := store.PutTask(ctx, model.Task{
		ID:     "42",
		Title:  "Deploy service",
		Target: &model.RepoRef{Owner: "acme", Name: "widgets"},
		Grants: []model.Grant{
			{Capability: "gcp-key", Via: model.GrantByLabel},
			{Capability: "gcp-key:deploy", Via: model.GrantByLabel},
		},
		CreatedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Pre-migration materialization via in-process gcpkey providers.
	legacyPool := gcpkey.NewProvider(gcpkey.Config{
		ProjectID:           cfg.GCPProject,
		ServiceAccountEmail: cfg.GCPServiceAccountEmail,
		ServiceAccountCount: cfg.GCPServiceAccountCount,
	})
	legacyPool.NewMinter = func(_ context.Context, _ string) (gcpkey.Minter, error) { return cloud, nil }

	legacyDeploy := gcpkey.NewKeyProvider("deploy", store)
	legacyDeploy.NewMinter = func(_ context.Context, _ string) (gcpkey.Minter, error) { return cloud, nil }

	cc := model.CapabilityContext{
		Credentials: mapCredResolver(secretsMap),
		Now:         now,
	}

	var beforePoolEmails, beforeDeployEmails []string
	var beforePoolPath, beforeDeployPath string
	var beforePoolLease, beforeDeployLease time.Duration
	var legacyPoolLeases, legacyDeployLeases []model.Lease

	for round := 0; round < 2; round++ {
		matPool, err := legacyPool.Materialize(ctx, cc)
		if err != nil {
			t.Fatalf("legacy pool Materialize round %d: %v", round, err)
		}
		if len(matPool.Placements) != 1 {
			t.Fatalf("legacy pool placements = %+v, want 1", matPool.Placements)
		}
		beforePoolPath = matPool.Placements[0].Path
		beforePoolLease = matPool.Lease.ExpiresAt.Sub(matPool.Lease.IssuedAt)
		beforePoolEmails = append(beforePoolEmails, extractClientEmail(t, matPool.Placements[0].Content))
		legacyPoolLeases = append(legacyPoolLeases, *matPool.Lease)

		matDeploy, err := legacyDeploy.Materialize(ctx, cc)
		if err != nil {
			t.Fatalf("legacy deploy Materialize round %d: %v", round, err)
		}
		if len(matDeploy.Placements) != 1 {
			t.Fatalf("legacy deploy placements = %+v, want 1", matDeploy.Placements)
		}
		beforeDeployPath = matDeploy.Placements[0].Path
		beforeDeployLease = matDeploy.Lease.ExpiresAt.Sub(matDeploy.Lease.IssuedAt)
		beforeDeployEmails = append(beforeDeployEmails, extractClientEmail(t, matDeploy.Placements[0].Content))
		legacyDeployLeases = append(legacyDeployLeases, *matDeploy.Lease)
	}
	for _, l := range legacyPoolLeases {
		if err := legacyPool.Revoke(ctx, cc, l); err != nil {
			t.Fatalf("legacy pool Revoke: %v", err)
		}
	}
	for _, l := range legacyDeployLeases {
		if err := legacyDeploy.Revoke(ctx, cc, l); err != nil {
			t.Fatalf("legacy deploy Revoke: %v", err)
		}
	}
	slices.Sort(beforePoolEmails)
	slices.Sort(beforeDeployEmails)
	if cloud.LiveKeys() != 0 {
		t.Fatalf("live keys after legacy revoke = %d, want 0", cloud.LiveKeys())
	}
	callsBeforeMigration := cloud.Calls["CreateServiceAccount"] + cloud.Calls["SetPolicy"]

	// 2. Run real schema migration 97 on the store.
	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 96 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating store 96 -> 97: %v", err)
	}

	gotCfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	serverCfg, ok := gotCfg.MCPServers[model.GCPCapabilityServerID]
	if !ok {
		t.Fatalf("migrated config missing %s", model.GCPCapabilityServerID)
	}
	gotTask, err := store.GetTask(ctx, "42")
	if err != nil || gotTask == nil {
		t.Fatalf("GetTask(42): %v, %+v", err, gotTask)
	}

	// Resolve serverCfg.Env $secret: references just as servers.Registry does.
	resolvedEnv := map[string]string{}
	for k, v := range serverCfg.Env {
		secretName := strings.TrimPrefix(v, "$secret:")
		val, ok := secretsMap[secretName]
		if !ok {
			t.Fatalf("unknown secret %q in migrated server env %q", secretName, v)
		}
		resolvedEnv[k] = val
	}

	srv, err := newConfigurableGCP(GCPConfig{
		Connector: cloud,
		Env:       func(k string) string { return resolvedEnv[k] },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close(ctx) })
	srv.CapabilityServer.Token = controlToken
	ts := httptest.NewServer(srv.CapabilityServer)
	t.Cleanup(ts.Close)

	ps, err := protocol.Attach(ctx, model.GCPCapabilityServerID,
		protocol.ServerConfig{
			ServerURL: ts.URL + "/mcp",
			Headers:   map[string]string{"Authorization": "Bearer $secret:control"},
			Config:    serverCfg.Config,
		},
		func(name string) (string, bool) {
			if name == "control" {
				return controlToken, true
			}
			v, ok := secretsMap[name]
			return v, ok
		},
		protocol.DialOptions{Identity: testIdentity},
	)
	if err != nil {
		t.Fatalf("attaching migrated gcp capability server: %v", err)
	}
	t.Cleanup(func() { ps.Close() })

	// IAM roles and accounts in GCP must be completely untouched by migrating fixed-account pools.
	if got := cloud.Calls["CreateServiceAccount"] + cloud.Calls["SetPolicy"]; got != callsBeforeMigration {
		t.Errorf("migrated server mutated service accounts or IAM policies (calls=%v)", cloud.Calls)
	}
	if got := cloud.Members("projects/acme-pool", "roles/storage.objectViewer"); len(got) != 2 {
		t.Errorf("acme-pool roles/storage.objectViewer members = %v, want 2", got)
	}
	if got := cloud.Members("projects/acme-deploy", "roles/run.admin"); len(got) != 2 {
		t.Errorf("acme-deploy roles/run.admin members = %v, want 2", got)
	}

	lookup := func(capabilityID string) (*protocol.Server, error) {
		if capabilityID != model.GCPCapabilityServerID {
			t.Fatalf("unexpected capability ID %q", capabilityID)
		}
		return ps, nil
	}

	// Rewritten task grants and what an import rewrites the retired names to normalize identically.
	migratedGrants, err := model.CapabilityGrants(gotTask.Grants, lookup)
	if err != nil {
		t.Fatalf("normalizing migrated task grants %v: %v", gotTask.Grants, err)
	}
	aliasGrants, err := model.CapabilityGrants([]model.Grant{
		{Capability: model.RetiredGrantsBecame("gcp-key")[0], Via: model.GrantByLabel},
		{Capability: model.RetiredGrantsBecame("gcp-key:deploy")[0], Via: model.GrantByLabel},
	}, lookup)
	if err != nil {
		t.Fatalf("normalizing alias grants: %v", err)
	}
	if len(migratedGrants) != 2 || len(aliasGrants) != 2 {
		t.Fatalf("grants len: migrated=%d alias=%d, want 2", len(migratedGrants), len(aliasGrants))
	}
	var migratedRecorded, aliasRecorded []string
	for _, g := range migratedGrants {
		migratedRecorded = append(migratedRecorded, model.RecordedCapabilityGrant(g))
	}
	for _, g := range aliasGrants {
		aliasRecorded = append(aliasRecorded, model.RecordedCapabilityGrant(g))
	}
	slices.Sort(migratedRecorded)
	slices.Sort(aliasRecorded)
	if !slices.Equal(migratedRecorded, aliasRecorded) {
		t.Errorf("migrated grants %v != alias grants %v", migratedRecorded, aliasRecorded)
	}

	// 3. Mint both rewritten grants together across 2 concurrent passes to cover the full pools.
	minterFor := func(id string) (protocol.Minter, error) {
		return ps, nil
	}
	var afterPoolEmails, afterDeployEmails []string
	var passes []*protocol.Pass

	for round := 0; round < 2; round++ {
		pass, err := protocol.MintPass(ctx, migratedGrants, minterFor, protocol.PassOptions{
			Deployment: "049f1e09",
			Task:       "42",
			Attempt:    round + 1,
			Deadline:   now.Add(beforeDeployLease),
			Now:        func() time.Time { return now },
		})
		if err != nil {
			t.Fatalf("MintPass round %d: %v", round, err)
		}
		passes = append(passes, pass)

		placementsByPath := map[string]protocol.Placement{}
		for _, pl := range pass.Placements() {
			placementsByPath[pl.Path] = pl
		}

		poolPlacement, ok := placementsByPath[beforePoolPath]
		if !ok {
			t.Fatalf("round %d: missing placement at pre-migration pool path %q; got %v", round, beforePoolPath, pass.Placements())
		}
		if poolPlacement.Mode != "" && poolPlacement.Mode != "0600" {
			t.Errorf("pool placement mode = %q, want default/0600", poolPlacement.Mode)
		}
		afterPoolEmails = append(afterPoolEmails, extractClientEmail(t, poolPlacement.Content))

		deployPlacement, ok := placementsByPath[beforeDeployPath]
		if !ok {
			t.Fatalf("round %d: missing placement at pre-migration deploy path %q; got %v", round, beforeDeployPath, pass.Placements())
		}
		if deployPlacement.Mode != "" && deployPlacement.Mode != "0600" {
			t.Errorf("deploy placement mode = %q, want default/0600", deployPlacement.Mode)
		}
		afterDeployEmails = append(afterDeployEmails, extractClientEmail(t, deployPlacement.Content))

		// Only the primary sandbox-agent key (/home/debian/.gcp-service-account.json) runs gcloud activation;
		// deploy's /home/debian/.gcp-service-account-deploy.json does not clobber gcloud's active account.
		cmds := pass.SetupCommands()
		if len(cmds) != 1 || !strings.Contains(cmds[0].Path, ".gcp-key/") {
			t.Errorf("setup commands = %+v, want single gcloud activation for sandbox-agent", cmds)
		}
	}
	for _, pass := range passes {
		pass.Release(ctx)
	}

	slices.Sort(afterPoolEmails)
	slices.Sort(afterDeployEmails)
	if !slices.Equal(afterPoolEmails, beforePoolEmails) {
		t.Errorf("pool accounts minted after migration = %v, want pre-migration %v", afterPoolEmails, beforePoolEmails)
	}
	if !slices.Equal(afterDeployEmails, beforeDeployEmails) {
		t.Errorf("deploy accounts minted after migration = %v, want pre-migration %v", afterDeployEmails, beforeDeployEmails)
	}
	if cloud.LiveKeys() != 0 {
		t.Errorf("live keys after pass release = %d, want 0", cloud.LiveKeys())
	}

	// 4. Max lease fidelity: deploy's 2h max_lease refuses a 3h deadline, while sandbox-agent accepts up to 24h.
	realNow := time.Now().UTC()
	overDeploy, err := ps.Mint(ctx, "gcp-key", "049f1e09-42-9-0", realNow.Add(3*time.Hour), map[string]any{"permission_set": "deploy", "primary": true})
	if err != nil || overDeploy.Status != protocol.StatusRefused {
		t.Errorf("deploy 3h deadline (> %v) = %+v, %v; want refused", beforeDeployLease, overDeploy, err)
	}
	withinPool, err := ps.Mint(ctx, "gcp-key", "049f1e09-42-9-1", realNow.Add(beforePoolLease-time.Minute), map[string]any{"permission_set": model.GCPKeyDefaultPermissionSet, "primary": true})
	if err != nil || withinPool.Status != protocol.StatusOK {
		t.Fatalf("sandbox-agent %v deadline = %+v, %v; want ok", beforePoolLease, withinPool, err)
	}
	if _, err := ps.Release(ctx, "gcp-key", "049f1e09-42-9-1"); err != nil {
		t.Fatal(err)
	}
}
