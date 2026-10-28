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

package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
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
