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

package model

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

func TestGCPKeyValidationAndMethods(t *testing.T) {
	t.Parallel()

	valid := GCPKey{
		Name:                "deployer",
		ProjectID:           "my-gcp-project",
		ServiceAccountEmail: "deployer@my-gcp-project.iam.gserviceaccount.com",
		ServiceAccountCount: 3,
		MinterCredential:    "custom-minter-key",
		MaxKeyAge:           12 * time.Hour,
	}

	if err := valid.Validate(); err != nil {
		t.Fatalf("valid key failed Validate: %v", err)
	}

	if valid.CapabilityName() != "gcp-key:deployer" {
		t.Errorf("CapabilityName = %q, want %q", valid.CapabilityName(), "gcp-key:deployer")
	}

	if valid.Label() != "grain-gcp-key-deployer" {
		t.Errorf("Label = %q, want %q", valid.Label(), "grain-gcp-key-deployer")
	}

	if got := (GCPKey{Name: "deployer"}).EffectiveMinterCredential(); got != "gcp-key-minter-deployer" {
		t.Errorf("EffectiveMinterCredential with no minter = %q, want gcp-key-minter-deployer", got)
	}

	defaultKey := GCPKey{
		Name:                "default",
		ProjectID:           "proj",
		ServiceAccountEmail: "agent@proj.iam.gserviceaccount.com",
	}
	if defaultKey.CapabilityName() != "gcp-key" {
		t.Errorf("default key CapabilityName = %q, want %q", defaultKey.CapabilityName(), "gcp-key")
	}
	if defaultKey.Label() != "grain-gcp-key" {
		t.Errorf("default key Label = %q, want %q", defaultKey.Label(), "grain-gcp-key")
	}

	// Invalid cases
	tests := []struct {
		name string
		key  GCPKey
	}{
		{"missing name", GCPKey{ProjectID: "p", ServiceAccountEmail: "e"}},
		{"invalid name with colon", GCPKey{Name: "a:b", ProjectID: "p", ServiceAccountEmail: "e"}},
		{"invalid name with space", GCPKey{Name: "a b", ProjectID: "p", ServiceAccountEmail: "e"}},
		{"invalid name with slash", GCPKey{Name: "a/b", ProjectID: "p", ServiceAccountEmail: "e"}},
		{"reserved name default", GCPKey{Name: "default", ProjectID: "p", ServiceAccountEmail: "e"}},
		{"reserved name Default", GCPKey{Name: "Default", ProjectID: "p", ServiceAccountEmail: "e"}},
		{"missing project", GCPKey{Name: "deploy", ServiceAccountEmail: "e"}},
		{"missing email", GCPKey{Name: "deploy", ProjectID: "p"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.key.Validate(); err == nil {
				t.Errorf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

func TestGCPKeyStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store := New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("init store: %v", err)
	}

	// List on empty
	keys, err := store.ListGCPKeys(ctx)
	if err != nil {
		t.Fatalf("ListGCPKeys on empty: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("expected 0 keys, got %d", len(keys))
	}

	k1 := GCPKey{
		Name:                "data-pipeline",
		ProjectID:           "proj-alpha",
		ServiceAccountEmail: "pipeline@proj-alpha.iam.gserviceaccount.com",
		ServiceAccountCount: 2,
		MinterCredential:    "alpha-minter",
		MaxKeyAge:           8 * time.Hour,
		Description:         "Pipeline key for data tasks",
	}
	if err := store.PutGCPKey(ctx, k1); err != nil {
		t.Fatalf("PutGCPKey: %v", err)
	}

	k2 := GCPKey{
		Name:                "analytics",
		ProjectID:           "proj-alpha-2",
		ServiceAccountEmail: "analytics@proj-alpha-2.iam.gserviceaccount.com",
		ServiceAccountCount: 1,
	}
	if err := store.PutGCPKey(ctx, k2); err != nil {
		t.Fatalf("PutGCPKey: %v", err)
	}

	// Get specific
	got, err := store.GetGCPKey(ctx, "data-pipeline")
	if err != nil {
		t.Fatalf("GetGCPKey: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil key")
	}
	if got.ProjectID != "proj-alpha" || got.ServiceAccountCount != 2 || got.MaxKeyAge != 8*time.Hour {
		t.Errorf("got %+v, unexpected fields", got)
	}

	// Every key, sorted by name: analytics, data-pipeline.
	all, err := store.ListGCPKeys(ctx)
	if err != nil {
		t.Fatalf("ListGCPKeys: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 keys, got %d: %+v", len(all), all)
	}
	if all[0].Name != "analytics" || all[1].Name != "data-pipeline" {
		t.Errorf("unexpected order: %v", all)
	}

	if err := store.DeleteGCPKey(ctx, "analytics"); err != nil {
		t.Fatalf("DeleteGCPKey: %v", err)
	}
	after, _ := store.ListGCPKeys(ctx)
	if len(after) != 1 || after[0].Name != "data-pipeline" {
		t.Fatalf("expected 1 key after delete, got %v", after)
	}
}
