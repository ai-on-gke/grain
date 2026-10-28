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

package orchestrator

import (
	"context"
	"reflect"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
)

func openTestStore(t *testing.T) (*model.Store, context.Context) {
	t.Helper()
	store, _ := sqlitetest.Open(t)
	return store, context.Background()
}

// A task is filed holding the deployment's defaults and its repository's,
// and grain-ci when the repository's CI mode is grain. The repository's
// row is the only layer since schema 23 folded the default team's
// team_repo_config into it, so the team a task names changes nothing.
func TestDefaultTaskGrantsMergesRepoCapabilities(t *testing.T) {
	t.Parallel()
	store, ctx := openTestStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "lib"}

	if err := store.PutConfig(ctx, model.Config{
		DefaultCapabilities: []string{"global-cap"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                repo,
		DefaultCapabilities: []string{"repo-cap"},
		CIMode:              model.CIModeGrain,
	}); err != nil {
		t.Fatal(err)
	}

	for _, team := range []string{model.DefaultTeamID, "team-1"} {
		grants, err := defaultTaskGrants(ctx, store, &repo, team)
		if err != nil {
			t.Fatal(err)
		}
		var caps []string
		for _, g := range grants {
			caps = append(caps, g.Capability)
		}
		want := []string{"global-cap", "repo-cap", "grain-ci"}
		if !reflect.DeepEqual(caps, want) {
			t.Errorf("team %s: caps = %v, want %v", team, caps, want)
		}
	}
}
