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

package githubsim

import (
	"os/exec"
	"strings"
	"testing"
)

// The calls grain's work trees make (orchestrator/worktree.go): an
// `origin/#N` label on a pull request, and a merge commit message
// carrying `Origin:`/`Filed-by:` trailers. Sim used to dereference a nil
// issue for a pull request's number, and dropped the merge message.

func TestSimLabelsAPullRequestByItsNumber(t *testing.T) {
	sim, client := newSim(t, "main")
	pushBranch(t, sim.BareRepo, "grain/task-1")
	pr, err := client.CreatePullRequest("acme", "widgets", "grain/task-1", "main", "grain: task 1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.AddLabel("acme", "widgets", pr.Number, "origin/#7"); err != nil {
		t.Fatalf("AddLabel on a pull request: %v", err)
	}
	if _, ok := sim.PullRequests[0].Labels["origin/#7"]; !ok {
		t.Fatalf("labels = %v, want origin/#7", sim.PullRequests[0].Labels)
	}
	if err := client.AddLabel("acme", "widgets", 4242, "origin/#7"); err == nil {
		t.Fatal("AddLabel on a number that is neither an issue nor a pull request succeeded")
	}
}

func TestSimRefuseLabelsAnswersWithTheConfiguredStatus(t *testing.T) {
	sim, client := newSim(t, "main")
	pushBranch(t, sim.BareRepo, "grain/task-1")
	pr, err := client.CreatePullRequest("acme", "widgets", "grain/task-1", "main", "grain: task 1", "")
	if err != nil {
		t.Fatal(err)
	}
	sim.RefuseLabels = 403
	err = client.AddLabel("acme", "widgets", pr.Number, "origin/#7")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("AddLabel with RefuseLabels=403: err = %v, want a 403", err)
	}
	if len(sim.PullRequests[0].Labels) != 0 {
		t.Fatalf("a refused label was applied: %v", sim.PullRequests[0].Labels)
	}
}

func TestSimMergesAPullRequestWithTheMessageItWasGiven(t *testing.T) {
	sim, client := newSim(t, "main")
	pushBranch(t, sim.BareRepo, "grain/task-1")
	pr, err := client.CreatePullRequest("acme", "widgets", "grain/task-1", "main", "grain: task 1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.MergePullRequestWithMessage("acme", "widgets", pr.Number, "", "", "grain: task 1\n\nOrigin: acme/widgets#7\nFiled-by: task 3"); err != nil {
		t.Fatalf("MergePullRequestWithMessage: %v", err)
	}
	out, err := exec.Command("git", "--git-dir", sim.BareRepo, "log", "-1", "--format=%B", "main").CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, out)
	}
	msg := string(out)
	for _, want := range []string{"Merge pull request #", "Origin: acme/widgets#7", "Filed-by: task 3"} {
		if !strings.Contains(msg, want) {
			t.Errorf("merge commit message %q lacks %q", msg, want)
		}
	}
	if !strings.Contains(sim.PullRequests[0].MergeMessage, "Origin: acme/widgets#7") {
		t.Errorf("MergeMessage = %q", sim.PullRequests[0].MergeMessage)
	}
}
