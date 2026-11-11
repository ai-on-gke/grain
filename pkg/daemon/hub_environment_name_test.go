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

package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/version"
)

func environmentNames(t *testing.T, store *model.Store) (name, fromHub string) {
	t.Helper()
	cfg, err := store.GetConfig(context.Background())
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if cfg == nil {
		return "", ""
	}
	return cfg.EnvironmentName, cfg.HubEnvironmentName
}

// A hosted instance's -default-environment-name names an unnamed
// deployment, and a later one from the hub (its entry renamed) renames
// it -- but a name its operator set, in Settings or its state repo's
// grain_config, is never overridden.
func TestDefaultEnvironmentNameNeverOverridesAnExplicitOne(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("explicit name kept", func(t *testing.T) {
		store, closeStore := openTestStore(t, t.TempDir())
		defer closeStore()
		if err := store.UpdateConfig(ctx, func(c *model.Config) error {
			c.EnvironmentName = "staging"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		adoptDefaultEnvironmentName(ctx, store, "Alpha team")
		if name, fromHub := environmentNames(t, store); name != "staging" || fromHub != "" {
			t.Errorf("after a hub default over an explicit name: name=%q hub=%q, want staging and no hub record", name, fromHub)
		}
	})

	t.Run("unnamed takes the default and follows renames until overridden", func(t *testing.T) {
		store, closeStore := openTestStore(t, t.TempDir())
		defer closeStore()
		adoptDefaultEnvironmentName(ctx, store, "Alpha team")
		if name, fromHub := environmentNames(t, store); name != "Alpha team" || fromHub != "Alpha team" {
			t.Fatalf("unnamed deployment: name=%q hub=%q, want Alpha team for both", name, fromHub)
		}
		adoptDefaultEnvironmentName(ctx, store, "Alpha\nrenamed")
		if name, _ := environmentNames(t, store); name != "Alpha renamed" {
			t.Fatalf("after the hub entry was renamed: name=%q, want Alpha renamed", name)
		}
		// The operator names it in Settings; the hub's next name is ignored.
		if err := store.UpdateConfig(ctx, func(c *model.Config) error {
			c.EnvironmentName = "prod"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		adoptDefaultEnvironmentName(ctx, store, "Alpha again")
		if name, _ := environmentNames(t, store); name != "prod" {
			t.Errorf("after the operator renamed it: name=%q, want prod", name)
		}
	})
}

// A connected entry gets the same default through its heartbeat reply,
// so renaming it on the hub renames the daemon on its next heartbeat --
// and, as for a hosted instance, never over the operator's own name.
func TestHubHeartbeatAppliesTheEntrysEnvironmentNameAsADefault(t *testing.T) {
	t.Parallel()
	fake, srv := newFakeHubServer(t)
	store, db, err := OpenStore("sqlite", "", t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	cfg := model.DefaultConfig()
	cfg.Hub = model.HubConfig{
		URL:      srv.URL,
		Entry:    "dev-workstation",
		AuthMode: model.HubAuthOIDC,
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	mgr := NewHubManager(HubManagerOptions{
		Store:      store,
		HTTPClient: srv.Client(),
		LiveConfig: func() Config { return Config{GranuleSandboxes: true} },
		Version:    func() version.Info { return version.Info{Release: "v2.4.0"} },
		Health:     func() (api.HealthState, string) { return api.HealthOK, "" },
		OIDCTokenSource: func(context.Context, string) (string, time.Time, error) {
			return "oidc-jwt-token-1", time.Now().Add(30 * time.Minute), nil
		},
		BaseInterval:   40 * time.Millisecond,
		JitterFraction: -1,
	})
	heartbeat := func(envName string) string {
		t.Helper()
		fake.mu.Lock()
		fake.nextEnvironmentName = envName
		fake.mu.Unlock()
		if _, err := mgr.TriggerHeartbeat(ctx); err != nil {
			t.Fatalf("TriggerHeartbeat: %v", err)
		}
		name, _ := environmentNames(t, store)
		return name
	}

	if got := heartbeat("Dev Workstation"); got != "Dev Workstation" {
		t.Fatalf("after the first heartbeat: environment name %q, want Dev Workstation", got)
	}
	if got := heartbeat("Dev box"); got != "Dev box" {
		t.Fatalf("after the entry was renamed on the hub: environment name %q, want Dev box", got)
	}
	if got := heartbeat(""); got != "Dev box" {
		t.Fatalf("a hub that sends no name changed it to %q", got)
	}
	if err := store.UpdateConfig(ctx, func(c *model.Config) error {
		c.EnvironmentName = "my laptop"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := heartbeat("Dev box 2"); got != "my laptop" {
		t.Errorf("the hub's name overrode the operator's: environment name %q, want my laptop", got)
	}
}
