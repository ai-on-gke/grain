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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

func TestConfigRepoMirrorModeKeepsInternalConfigAndPushesToOrigin(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()

	remote := filepath.Join(t.TempDir(), "instance-config.git")
	if out, err := exec.Command("git", "init", "--bare", "--initial-branch=main", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}

	// Instance 1: boots with state-repo.json in mirror mode, seeds and pushes to remote.
	dataDir1 := t.TempDir()
	store1, db1, err := OpenStore("sqlite", "", dataDir1)
	if err != nil {
		t.Fatalf("OpenStore(dataDir1): %v", err)
	}
	defer db1.Close()

	if err := staterepo.SaveSettings(dataDir1, staterepo.Settings{
		Remote:     remote,
		Branch:     "main",
		Mode:       staterepo.ModeMirror,
		SSHKeyFile: "/run/grain-secrets/config-repo-key",
	}); err != nil {
		t.Fatalf("SaveSettings(dataDir1): %v", err)
	}

	repo1, err := OpenStateRepo(ctx, dataDir1)
	if err != nil {
		t.Fatalf("OpenStateRepo(dataDir1): %v", err)
	}

	manager1 := NewStateManager(dataDir1, db1, repo1, OpenSecrets(dataDir1), nil, nil)
	if !manager1.isInternalConfigRepo() {
		t.Fatal("isInternalConfigRepo() = false in mirror mode, want true so grain/config stays controller-local")
	}
	wantInternal := model.RepoRef{Owner: "grain", Name: "config"}
	if got := manager1.settingsRepo(); got != wantInternal {
		t.Fatalf("settingsRepo() in mirror mode = %v, want %v", got, wantInternal)
	}

	if err := store1.PutTemplate(ctx, model.Template{
		ID:    "tmpl-mirror-1",
		Name:  "mirrored-template",
		Title: "Mirrored Template",
		Body:  "Backed up to per-instance config repo",
	}); err != nil {
		t.Fatalf("PutTemplate: %v", err)
	}

	if _, err := manager1.cycle(ctx, false, "test"); err != nil {
		t.Fatalf("manager1.cycle: %v", err)
	}

	st := manager1.status(ctx)
	if st.Mode != "internal" || st.Repository != "grain/config" || st.Remote != remote {
		t.Fatalf("status = %+v, want Mode=internal Repository=grain/config Remote=%s", st, remote)
	}

	// Verify the bare remote received the mirrored commit on main.
	if out, err := exec.Command("git", "-C", remote, "rev-parse", "--verify", "refs/heads/main").CombinedOutput(); err != nil {
		t.Fatalf("remote refs/heads/main missing after mirror cycle: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	// Instance 2 (replacement VM): starts with an empty database and a fresh
	// clone/settings in mirror mode, and recovers the state from remote via loadStateRepo.
	dataDir2 := t.TempDir()
	store2, db2, err := OpenStore("sqlite", "", dataDir2)
	if err != nil {
		t.Fatalf("OpenStore(dataDir2): %v", err)
	}
	defer db2.Close()

	if err := staterepo.SaveSettings(dataDir2, staterepo.Settings{
		Remote: remote,
		Branch: "main",
		Mode:   staterepo.ModeMirror,
	}); err != nil {
		t.Fatalf("SaveSettings(dataDir2): %v", err)
	}
	repo2, err := OpenStateRepo(ctx, dataDir2)
	if err != nil {
		t.Fatalf("OpenStateRepo(dataDir2): %v", err)
	}
	if err := loadStateRepo(ctx, dataDir2, repo2, db2, storeMigration{}); err != nil {
		t.Fatalf("loadStateRepo(dataDir2): %v", err)
	}

	tmpls, err := store2.ListTemplates(ctx)
	if err != nil {
		t.Fatalf("store2.ListTemplates: %v", err)
	}
	found := false
	for _, tmpl := range tmpls {
		if tmpl.ID == "tmpl-mirror-1" && tmpl.Title == "Mirrored Template" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("replacement VM store2 did not import tmpl-mirror-1 from mirrored config repo; got %+v", tmpls)
	}
}

func TestStateSyncFlushesFinalExportAfterParentContextCanceled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	finalCtxErr := make(chan error, 1)
	sync := func(context.Context) (bool, error) { return false, nil }
	syncAll := func(c context.Context) (bool, error) {
		finalCtxErr <- c.Err()
		return true, nil
	}
	stop := startStateSync(ctx, sync, syncAll, make(chan struct{}))

	// Cancel parent ctx first, as happens when SIGTERM triggers shutdown drain
	// before defer stopStateSync() runs.
	cancel()
	stop()

	select {
	case err := <-finalCtxErr:
		if err != nil {
			t.Fatalf("final syncAll received cancelled context (%v); want non-cancelled drain context", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("final syncAll did not run on stop()")
	}
}
