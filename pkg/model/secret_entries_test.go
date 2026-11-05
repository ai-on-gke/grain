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

package model_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/secrets"
)

func TestSecretEntriesCatalogueCRUD(t *testing.T) {
	store, ctx := open(t)
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)

	if entries, err := store.ListSecretEntries(ctx); err != nil || len(entries) != 0 {
		t.Fatalf("initial ListSecretEntries = %+v, %v; want empty", entries, err)
	}
	if _, ok, err := store.GetSecretEntry(ctx, "db", "password"); err != nil || ok {
		t.Fatalf("GetSecretEntry missing = ok=%v, err=%v", ok, err)
	}

	if err := store.PutSecretEntry(ctx, secrets.CatalogueEntry{
		SecretName: "db",
		Key:        "password",
		SMSecret:   "grain-inst-alice-db-password_abcdef012345",
		Version:    "1",
		UpdatedAt:  now,
	}); err != nil {
		t.Fatalf("PutSecretEntry 1: %v", err)
	}
	if err := store.PutSecretEntry(ctx, secrets.CatalogueEntry{
		SecretName: "db",
		Key:        "username",
		SMSecret:   "grain-inst-alice-db-username_123456abcdef",
		Version:    "1",
		UpdatedAt:  now,
	}); err != nil {
		t.Fatalf("PutSecretEntry 2: %v", err)
	}
	if err := store.PutSecretEntry(ctx, secrets.CatalogueEntry{
		SecretName: secrets.GeminiAPIKeySecret,
		Key:        secrets.AgentCredentialKey,
		SMSecret:   "grain-inst-alice-gemini-api-key",
		Version:    "1",
		UpdatedAt:  now,
	}); err != nil {
		t.Fatalf("PutSecretEntry 3: %v", err)
	}

	// Upsert updates version and timestamp in place.
	later := now.Add(5 * time.Minute)
	if err := store.PutSecretEntry(ctx, secrets.CatalogueEntry{
		SecretName: secrets.GeminiAPIKeySecret,
		Key:        secrets.AgentCredentialKey,
		SMSecret:   "grain-inst-alice-gemini-api-key",
		Version:    "2",
		UpdatedAt:  later,
	}); err != nil {
		t.Fatalf("PutSecretEntry update: %v", err)
	}

	got, ok, err := store.GetSecretEntry(ctx, secrets.GeminiAPIKeySecret, secrets.AgentCredentialKey)
	if err != nil || !ok {
		t.Fatalf("GetSecretEntry = %+v, %v, %v", got, ok, err)
	}
	if got.Version != "2" || got.SMSecret != "grain-inst-alice-gemini-api-key" || !got.UpdatedAt.Equal(later) {
		t.Fatalf("unexpected updated entry: %+v", got)
	}

	entries, err := store.ListSecretEntries(ctx)
	if err != nil {
		t.Fatalf("ListSecretEntries: %v", err)
	}
	wantNames := []string{"db/password", "db/username", "gemini-api-key/value"}
	var gotNames []string
	for _, e := range entries {
		gotNames = append(gotNames, e.SecretName+"/"+e.Key)
	}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("ListSecretEntries order = %v, want %v", gotNames, wantNames)
	}

	if err := store.DeleteSecretEntry(ctx, "db", "password"); err != nil {
		t.Fatalf("DeleteSecretEntry: %v", err)
	}
	if _, ok, err := store.GetSecretEntry(ctx, "db", "password"); err != nil || ok {
		t.Fatalf("after DeleteSecretEntry, GetSecretEntry(db, password) = %v, %v", ok, err)
	}
	if _, ok, err := store.GetSecretEntry(ctx, "db", "username"); err != nil || !ok {
		t.Fatalf("sibling key db/username should remain: ok=%v, err=%v", ok, err)
	}

	if err := store.DeleteSecretEntries(ctx, "db"); err != nil {
		t.Fatalf("DeleteSecretEntries(db): %v", err)
	}
	entries, err = store.ListSecretEntries(ctx)
	if err != nil || len(entries) != 1 || entries[0].SecretName != secrets.GeminiAPIKeySecret {
		t.Fatalf("remaining entries = %+v, %v", entries, err)
	}
}
