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
	"encoding/hex"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/hub"
)

// TestProvisionStoresGeneratedDatabasePasswordInSecretManager is the
// regression test for the 2026-11-08 second security review, finding 2: with
// `instance_sql.cloud_sql_admin: false`, Provision must write the generated
// password as a Secret Manager version under the instance's prefix and return
// only the secret's name, never the password, in the bundle the VM's
// grain-config metadata is built from.
func TestProvisionStoresGeneratedDatabasePasswordInSecretManager(t *testing.T) {
	for _, bindProjectIAM := range []bool{true, false} {
		name := "bind_project_iam"
		if !bindProjectIAM {
			name = "no_project_iam"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			srv, cp := newFakeGCPControlPlaneServer(t)
			cloudSQLAdmin := false
			bind := bindProjectIAM
			cfg := hub.Config{
				Compute: hub.ComputeConfig{
					Provider:       "gce",
					Project:        "test-proj",
					Region:         "us-central1",
					Zones:          []string{"us-central1-a"},
					BindProjectIAM: &bind,
					SelfPrincipal:  testSelfPrincipal,
				},
				Secrets: hub.SecretsConfig{Project: "test-proj", SecretPrefix: "grain-inst-"},
				InstanceSQL: hub.InstanceSQLConfig{
					CloudSQLAdmin: &cloudSQLAdmin,
					SQLInstances: []hub.SQLInstanceConfig{
						{Name: "sql-pool-01", ConnectionName: "test-proj:us-central1:sql-pool-01", PrivateIP: "10.8.0.5"},
					},
				},
			}
			res, err := NewGCPInstanceResources(ctx, cfg,
				WithResourcesHTTPClient(srv.Client()),
				WithIAMBaseURL(srv.URL),
				WithResourceManagerBaseURL(srv.URL),
				WithSQLAdminBaseURL(srv.URL),
				WithSecretManagerBaseURL(srv.URL),
				WithStorageBaseURL(srv.URL),
				WithResourcesComputeBaseURL(srv.URL+"/compute/v1"),
			)
			if err != nil {
				t.Fatalf("NewGCPInstanceResources: %v", err)
			}
			spec := hub.InstanceResourceSpec{EntryID: "entry-alice", Name: "alice-vm", OwnerEmail: "alice@example.com"}
			bundle, err := res.Provision(ctx, spec)
			if err != nil {
				t.Fatalf("Provision: %v", err)
			}

			const secretID = "grain-inst-alice-vm-db-password"
			if bundle.DatabasePassword != "" {
				t.Fatalf("bundle.DatabasePassword = %q; the password must not leave Provision", bundle.DatabasePassword)
			}
			if want := "projects/test-proj/secrets/" + secretID; bundle.DatabasePasswordSecret != want {
				t.Fatalf("bundle.DatabasePasswordSecret = %q, want %q", bundle.DatabasePasswordSecret, want)
			}
			if bundle.DatabaseUser == "" || bundle.SQLPrivateIP != "10.8.0.5" {
				t.Fatalf("bundle lacks the password user's connection details: %+v", bundle)
			}

			cp.mu.Lock()
			versions := cp.secrets[secretID]
			policy, hasSecretPolicy := cp.secretPolicies[secretID]
			projectPolicy := cp.projectPolicy
			cp.mu.Unlock()
			if len(versions) != 1 {
				t.Fatalf("Secret Manager %s has %d versions, want 1", secretID, len(versions))
			}
			if pw := string(versions[0]); len(pw) != 48 {
				t.Fatalf("stored password %q is not 24 random bytes in hex", pw)
			} else if _, err := hex.DecodeString(pw); err != nil {
				t.Fatalf("stored password %q is not hex: %v", pw, err)
			}

			member := "serviceAccount:" + bundle.ServiceAccountEmail
			if bindProjectIAM {
				// The project-level prefix binding already covers the secret.
				if hasSecretPolicy {
					t.Errorf("unexpected secret-level policy with bind_project_iam: %+v", policy)
				}
				var found bool
				for _, b := range projectPolicy.Bindings {
					if b.Role == "roles/secretmanager.secretAccessor" && b.Condition != nil &&
						strings.Contains(b.Condition.Expression, "projects/test-proj/secrets/grain-inst-alice-vm-") {
						for _, m := range b.Members {
							found = found || m == member
						}
					}
				}
				if !found {
					t.Errorf("no prefix-scoped secretAccessor binding for %s: %+v", member, projectPolicy)
				}
			} else {
				if len(policy.Bindings) != 1 || policy.Bindings[0].Role != "roles/secretmanager.secretAccessor" ||
					len(policy.Bindings[0].Members) != 1 || policy.Bindings[0].Members[0] != member {
					t.Errorf("secret-level policy = %+v, want secretAccessor for exactly %s", policy, member)
				}
			}

			// Re-provisioning (replace, restore) rotates the password: a new
			// version, which the replacement VM reads as `latest`.
			if _, err := res.Provision(ctx, spec); err != nil {
				t.Fatalf("Provision again: %v", err)
			}
			cp.mu.Lock()
			versions = cp.secrets[secretID]
			cp.mu.Unlock()
			if len(versions) != 2 || string(versions[0]) == string(versions[1]) {
				t.Fatalf("re-provisioning should add one fresh version, got %d versions", len(versions))
			}

			// Final purge deletes the password secret with the rest of the prefix.
			if err := res.Teardown(ctx, spec, *bundle, false); err != nil {
				t.Fatalf("Teardown(retainDB=false): %v", err)
			}
			cp.mu.Lock()
			_, stillThere := cp.secrets[secretID]
			cp.mu.Unlock()
			if stillThere {
				t.Fatalf("Teardown left %s behind", secretID)
			}
		})
	}
}
