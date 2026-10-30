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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A propose_task call whose branch is a GitHub fork head label is filed
// against the pull request that label heads, as ui.CreateTask files the
// live call: based on what it merges into and linked to it. One no open
// pull request heads is filed for a person on the default branch, saying
// so, rather than with a base no run could check out.
func TestEndOfRunProposalOnAForkHeadResolvesToItsPullRequest(t *testing.T) {
	store, ctx := openTreeStore(t)
	now := time.Date(2026, 10, 29, 6, 0, 0, 0, time.UTC)
	source := externalSource("7")
	if err := store.PutTask(ctx, source); err != nil {
		t.Fatal(err)
	}
	sim := githubsim.New("acme", "widgets", "", "main")
	sim.PullRequests = []githubsim.PullRequest{{Number: 139, Title: "chore: bump agy", Head: "chore/bump-agy-1.4.9",
		Base: "main", State: "open", HeadRepo: "andrzejfudro/widgets", Author: "andrzejfudro"}}
	client := github.NewClient(sim, nil)

	result := &agent.Result{ToolCalls: []agent.ToolCall{
		{Name: "propose_task", Arguments: map[string]any{"title": "Finish the bump", "body": "b",
			"branch": "andrzejfudro:chore/bump-agy-1.4.9"}},
		{Name: "propose_task", Arguments: map[string]any{"title": "Nobody's head", "body": "b",
			"branch": "someone:nothing-here"}},
	}}
	if err := relayProposedTasks(ctx, store, nil, client, source, result, now); err != nil {
		t.Fatal(err)
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]model.Task{}
	for _, task := range tasks {
		byTitle[task.Title] = task
	}

	resolved, ok := byTitle["Finish the bump"]
	if !ok {
		t.Fatal("the proposal on the fork head was not filed")
	}
	if resolved.Base != "main" || linkTarget(resolved, model.LinkExternalPR) != "acme/widgets#139" ||
		!strings.Contains(resolved.Body, "refs/pull/139/head") {
		t.Fatalf("resolved proposal = base %q, links %+v, body %q", resolved.Base, resolved.Links, resolved.Body)
	}

	unresolved, ok := byTitle["Nobody's head"]
	if !ok {
		t.Fatal("the proposal on an unknown fork head was not filed")
	}
	if unresolved.Base != "" || unresolved.Approval != nil ||
		!strings.Contains(unresolved.Body, "no open pull request on acme/widgets has it as its head") {
		t.Fatalf("unresolved proposal = base %q, approval %+v, body %q", unresolved.Base, unresolved.Approval, unresolved.Body)
	}
}
