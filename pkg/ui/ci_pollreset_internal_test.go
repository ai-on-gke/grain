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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A run starting grain CI for its branch puts its pull request back on
// every sync cycle, so the merge queue reads it fresh rather than on an
// idle backoff.
func TestStartingTaskCIResetsItsPullRequestsPollBackoff(t *testing.T) {
	t.Parallel()
	repo := model.RepoRef{Owner: "acme", Name: "poll-reset"}
	key := github.PollKey{Scope: t.Name(), Subject: github.PullRequestSubject(repo.Owner, repo.Name, 7),
		Aspect: "pull request"}
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	github.Polls().Observe(key, "idle", now)
	if github.Polls().Due(key, github.PollNormal, false, now.Add(time.Second)) {
		t.Fatal("setup: a pull request read a second ago is already due")
	}

	resetPullRequestPolls(model.Task{ID: "t1", Links: []model.Link{
		{Kind: model.LinkFixes, Target: model.PullRequestRef{Repo: repo, Number: 7}.String()},
	}})

	if !github.Polls().Due(key, github.PollNormal, false, now.Add(time.Second)) {
		t.Fatal("starting CI for a task left its pull request on its idle backoff")
	}
}
