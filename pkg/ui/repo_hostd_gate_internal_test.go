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

package ui

import (
	"context"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// An agent may not set a repository's default hostd to a hostd its own
// task does not run on -- that would route every task filed there onto
// it -- but a filing it makes against a repository that has a default is
// not refused for it: the default is whoever set the repository's
// settings' word, not the agent's.
func TestAgentHostdGateHoldsARepoDefaultHostdButNotItsUse(t *testing.T) {
	LogToTest(t)
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, DefaultHostd: "gpu-box"}); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(Config{Actor: DefaultActor("alice")}, store)
	acting := &model.Task{ID: "41"}

	const settings = "PUT /api/repos/{owner}/{name}/settings"
	for _, body := range []string{`{"defaultHostd":"gpu-box"}`, `{"DEFAULTHOSTD":"pool=gpu"}`} {
		if err := srv.agentFilingEscalation(ctx, acting, APIGrantSet{}, settings, "repos:write", repo.String(), []byte(body)); err == nil {
			t.Errorf("an agent set %s on a repository", body)
		}
	}
	if err := srv.agentFilingEscalation(ctx, acting, APIGrantSet{}, settings, "repos:write", repo.String(), []byte(`{"defaultHostd":""}`)); err != nil {
		t.Errorf("an agent clearing a repository's default hostd was refused: %v", err)
	}
	if err := srv.agentFilingEscalation(ctx, acting, APIGrantSet{}, "POST /api/tasks", "tasks:write", repo.String(),
		[]byte(`{"title":"t","description":"d","repo":"acme/widgets"}`)); err != nil {
		t.Errorf("an agent's filing against a repository with a default hostd was refused: %v", err)
	}
}
