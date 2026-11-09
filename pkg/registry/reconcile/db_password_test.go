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

package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/registry"
	"github.com/ai-on-gke/grain/pkg/registry/api"
	"github.com/ai-on-gke/grain/pkg/registry/store"
)

const testDBPassword = "SECRET-PW-0123/@:"

// assertNoPassword fails when any grain-config value carries pw in any form a
// DSN or JSON encoding would give it.
func assertNoPassword(t *testing.T, env map[string]string, pw string) {
	t.Helper()
	forms := []string{pw, url.QueryEscape(pw), url.PathEscape(pw), url.UserPassword("u", pw).String()[2:]}
	for k, v := range env {
		for _, f := range forms {
			if f != "" && strings.Contains(v, f) {
				t.Errorf("grain-config key %q carries the database password: %q", k, v)
			}
		}
		if strings.Contains(v, "sslmode=disable") {
			t.Errorf("grain-config key %q carries a cleartext DSN: %q", k, v)
		}
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), pw) {
		t.Errorf("serialised grain-config carries the database password:\n%s", raw)
	}
}

// TestInstanceGrainConfigNeverCarriesDatabasePassword is the regression test
// for the 2026-11-08 second security review, finding 2: grain-config becomes
// GCE instance metadata (readable with compute.instances.get), so even a bundle
// that still holds a raw DatabasePassword must not put it there.
func TestInstanceGrainConfigNeverCarriesDatabasePassword(t *testing.T) {
	cfg := registry.Config{
		Host:           "registry.example.com",
		InstanceDomain: "instances.example.com",
		Compute:        registry.ComputeConfig{Project: "test-proj", Region: "us-central1"},
	}
	entry := &store.EntryRow{Name: "alpha", OwnerEmail: "alice@example.com"}
	bundle := &registry.InstanceResourceBundle{
		SQLInstance:            "grain-shared-pg",
		SQLPrivateIP:           "10.0.0.5",
		DatabaseName:           "grain_alpha",
		DatabaseUser:           "grain-sa-alpha@test-proj.iam",
		DatabasePassword:       testDBPassword,
		DatabasePasswordSecret: "projects/test-proj/secrets/grain-inst-alpha-db-password",
		SecretPrefix:           "grain-inst-alpha-",
	}
	env := InstanceGrainConfig(cfg, entry, bundle, "")
	assertNoPassword(t, env, testDBPassword)
	for _, k := range []string{"GRAIN_DB_PASSWORD", "db_dsn"} {
		if v, ok := env[k]; ok {
			t.Errorf("grain-config sets %s=%q; want it absent", k, v)
		}
	}
	for k, want := range map[string]string{
		"db_password_secret":       bundle.DatabasePasswordSecret,
		"GRAIN_DB_PASSWORD_SECRET": bundle.DatabasePasswordSecret,
		"db_user":                  bundle.DatabaseUser,
		"db_name":                  bundle.DatabaseName,
		"sql_private_ip":           bundle.SQLPrivateIP,
	} {
		if got := env[k]; got != want {
			t.Errorf("grain-config %s = %q, want %q", k, got, want)
		}
	}
}

// passwordResources is an InstanceResources provider that hands back a raw
// DatabasePassword, as a provider without its own secret store would.
// storingPasswordResources adds registry.SecretWriter.
type passwordResources struct {
	*fakeResources
	mu      sync.Mutex
	secrets map[string][]byte
}

func (p *passwordResources) Provision(ctx context.Context, spec registry.InstanceResourceSpec) (*registry.InstanceResourceBundle, error) {
	b, err := p.fakeResources.Provision(ctx, spec)
	if err != nil {
		return nil, err
	}
	b.DatabaseUser = "grain-sa-" + spec.Name + "@test-proj.iam"
	b.DatabasePassword = testDBPassword
	return b, nil
}

type storingPasswordResources struct{ *passwordResources }

func (p storingPasswordResources) PutSecret(_ context.Context, entryName, suffix string, payload []byte) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	name := fmt.Sprintf("projects/test-proj/secrets/grain-inst-%s-%s", entryName, suffix)
	p.secrets[name] = append([]byte(nil), payload...)
	return name, nil
}

func newPasswordTestReconciler(t *testing.T, res registry.InstanceResources) (*Reconciler, *store.Store, *fakeCompute) {
	t.Helper()
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.SeedShapes(ctx, []store.ShapeRow{{Name: "medium", MachineType: "e2-standard-8", BootDiskGB: 100, QuotaUnits: 1}}); err != nil {
		t.Fatalf("SeedShapes: %v", err)
	}
	if err := st.SeedSQLInstances(ctx, []store.SQLInstanceRow{
		{Name: "sql-pool-01", ConnectionName: "test-proj:us-central1:sql-pool-01", PrivateIP: "10.8.0.5", MaxDatabases: 50},
	}); err != nil {
		t.Fatalf("SeedSQLInstances: %v", err)
	}
	comp := newFakeCompute("http://127.0.0.1:1")
	if pr, ok := res.(*passwordResources); ok {
		pr.fakeResources.st = st
	} else if sr, ok := res.(storingPasswordResources); ok {
		sr.fakeResources.st = st
	}
	rec, err := New(Options{
		Config: registry.Config{
			Host:           "grain.example.com",
			InstanceDomain: "instances.grain.example.com",
			Compute: registry.ComputeConfig{
				Project:       "test-proj",
				Region:        "us-central1",
				Zones:         []string{"us-central1-a"},
				HealthTimeout: 10 * time.Minute,
				RegistryID:    "grain.example.com",
			},
		},
		Store:         st,
		Compute:       comp,
		Resources:     res,
		HolderID:      "leader-1",
		HealthTimeout: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("New Reconciler: %v", err)
	}
	if _, err := st.CreateEntry(ctx, store.CreateEntryInput{
		Name:       "alice-vm",
		Kind:       api.KindInstance,
		OwnerEmail: "alice@example.com",
		Shape:      "medium",
		Zone:       "us-central1-a",
	}); err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}
	return rec, st, comp
}

// TestReconcilerMovesProviderDatabasePasswordIntoSecretManager checks that a
// raw password a provider returns is stored through SecretWriter and only the
// secret's name reaches the VM's StartupEnv (its grain-config metadata).
func TestReconcilerMovesProviderDatabasePasswordIntoSecretManager(t *testing.T) {
	pr := &passwordResources{fakeResources: newFakeResources(nil), secrets: map[string][]byte{}}
	rec, _, comp := newPasswordTestReconciler(t, storingPasswordResources{pr})
	_ = rec.RunOnce(context.Background())

	comp.mu.Lock()
	spec, ok := comp.specs["grain-vm-alice-vm"]
	comp.mu.Unlock()
	if !ok {
		t.Fatal("expected the reconciler to create grain-vm-alice-vm")
	}
	assertNoPassword(t, spec.StartupEnv, testDBPassword)
	const wantSecret = "projects/test-proj/secrets/grain-inst-alice-vm-db-password"
	if got := spec.StartupEnv["db_password_secret"]; got != wantSecret {
		t.Errorf("db_password_secret = %q, want %q", got, wantSecret)
	}
	pr.mu.Lock()
	stored := string(pr.secrets[wantSecret])
	pr.mu.Unlock()
	if stored != testDBPassword {
		t.Errorf("Secret Manager %s holds %q, want the generated password", wantSecret, stored)
	}
}

// TestReconcilerRefusesDatabasePasswordWithoutSecretStore checks that a
// provider returning a raw password it cannot store fails provisioning rather
// than falling back to VM metadata.
func TestReconcilerRefusesDatabasePasswordWithoutSecretStore(t *testing.T) {
	pr := &passwordResources{fakeResources: newFakeResources(nil), secrets: map[string][]byte{}}
	rec, st, comp := newPasswordTestReconciler(t, pr)
	if err := rec.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce: want an error for a password with nowhere safe to go")
	}
	comp.mu.Lock()
	n := len(comp.specs)
	comp.mu.Unlock()
	if n != 0 {
		t.Fatalf("created %d VMs; want none", n)
	}
	e, err := st.GetEntryByName(context.Background(), "alice-vm")
	if err != nil {
		t.Fatalf("GetEntryByName: %v", err)
	}
	if e.LifecycleState != api.StateFailed {
		t.Fatalf("lifecycle_state = %s, want failed", e.LifecycleState)
	}
}
