package e2e

import (
	"context"
	"testing"

	"github.com/bwsalmon/grain/pkg/daemon"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/secrets"
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
