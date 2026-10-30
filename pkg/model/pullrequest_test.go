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

package model_test

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestPullRequestRefRoundTrips(t *testing.T) {
	ref := model.PullRequestRef{Repo: model.RepoRef{Owner: "bwsalmon", Name: "grain"}, Number: 42}
	s := ref.String()
	if s != "bwsalmon/grain#42" {
		t.Fatalf("String() = %q, want bwsalmon/grain#42", s)
	}
	got, err := model.ParsePullRequestRef(s)
	if err != nil {
		t.Fatalf("ParsePullRequestRef(%q): %v", s, err)
	}
	if got != ref {
		t.Fatalf("ParsePullRequestRef(%q) = %+v, want %+v", s, got, ref)
	}
}

func TestParsePullRequestRefRejectsMissingNumber(t *testing.T) {
	if _, err := model.ParsePullRequestRef("bwsalmon/grain"); err == nil {
		t.Fatal("expected an error for a ref with no #number")
	}
}

func TestParsePullRequestRefRejectsBadNumber(t *testing.T) {
	if _, err := model.ParsePullRequestRef("bwsalmon/grain#abc"); err == nil {
		t.Fatal("expected an error for a non-numeric #suffix")
	}
}

// A task's links come back ordered by their target's text, where
// "o/r#100" sorts before "o/r#99". The task's pull request is the newest
// by number, in its own target ahead of a write repository's.
func TestTaskPullRequestComparesNumbersAsNumbers(t *testing.T) {
	repo := model.RepoRef{Owner: "ai-on-gke", Name: "grain"}
	other := model.RepoRef{Owner: "ai-on-gke", Name: "zz-other"}
	task := model.Task{ID: "t1", Target: &repo, Writes: []model.WriteTarget{{Repo: other}}}
	for _, target := range []string{"ai-on-gke/grain#100", "ai-on-gke/grain#99", "ai-on-gke/zz-other#7"} {
		task.Links = append(task.Links, model.Link{Kind: model.LinkFixes, Target: target})
	}
	got := model.TaskPullRequest(task)
	want := model.PullRequestRef{Repo: repo, Number: 100}
	if got == nil || !model.SamePullRequest(*got, want) {
		t.Fatalf("TaskPullRequest = %v, want %v", got, want)
	}

	if !model.PullRequestRefLess(model.PullRequestRef{Repo: repo, Number: 99}, want) {
		t.Fatal("#99 does not order before #100")
	}
	if model.PullRequestRefLess(want, model.PullRequestRef{Repo: repo, Number: 99}) {
		t.Fatal("#100 orders before #99")
	}
}
