package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bwsalmon/grain/pkg/daemon"

	"github.com/bwsalmon/grain/pkg/gitproxy"
)

func bareRemote(t *testing.T) string {
	t.Helper()
	return initBareRepo(t, filepath.Join(t.TempDir(), "state.git"))
}

func stateManagerFor(t *testing.T, dataDir string, forbidden *gitproxy.ForbiddenSet) *daemon.StateManager {
	t.Helper()
	ctx := context.Background()
	_, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	repo, err := daemon.OpenStateRepo(ctx, dataDir)
	if err != nil {
		t.Fatalf("opening the config repository: %v", err)
	}
	return daemon.NewStateManager(dataDir, db, repo, daemon.OpenSecrets(dataDir), nil, forbidden)
}
