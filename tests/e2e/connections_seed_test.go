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

package e2e

import (
	"context"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

// seedConnections records dataDir's deployment connections before its
// daemon starts, the way `grain connections import` does for an
// install: git connections on the deployment, and each model connection
// with its own key (keys, by connection id) when it has one. Model
// connections live on the settings row, which is first seeded from cfg
// the way cfg's daemon seeds it on its first start.
func seedConnections(t *testing.T, cfg daemon.Config, git []model.TeamGitConnection, models []model.AgentBackend, keys map[string]string) {
	t.Helper()
	ctx := context.Background()
	dataDir := cfg.DataDir
	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("opening the store to seed connections: %v", err)
	}
	defer db.Close()
	if len(models) > 0 {
		stored, err := store.GetConfig(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if stored == nil {
			seed := cfg.ToModelConfig()
			stored = &seed
		}
		stored.AgentBackends = append(stored.AgentBackends, models...)
		if err := store.PutConfig(ctx, *stored); err != nil {
			t.Fatalf("seeding model connections: %v", err)
		}
	}
	for id, key := range keys {
		if err := daemon.OpenSecrets(dataDir).Set(secrets.AgentBackendCredentialSecret(id), secrets.AgentCredentialKey, []byte(key)); err != nil {
			t.Fatalf("seeding %s's key: %v", id, err)
		}
	}
	if len(git) > 0 {
		if err := store.UpdateConfig(ctx, func(team *model.Config) error {
			team.GitConnections = append(team.GitConnections, git...)
			return nil
		}); err != nil {
			t.Fatalf("seeding git connections: %v", err)
		}
	}
}
