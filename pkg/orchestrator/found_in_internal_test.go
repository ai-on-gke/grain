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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// TestAStage1RepairOnACandidateIsFoundInIt: the repair a red Stage 1 on
// vX.Y.Z-rc.N files records that candidate as the release it is found in
// (model.LinkFoundIn), and keeps it when a later red run is appended; a
// repair filed for a red default branch is against no release.
func TestAStage1RepairOnACandidateIsFoundInIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 18, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ ref, want string }{
		{"v3.5.0-rc.2", "v3.5.0-rc.2"},
		{"main", ""},
	} {
		store := openInternalStore(t)
		repo := model.RepoRef{Owner: "acme", Name: "widgets"}
		optInStage1(t, ctx, store, repo)
		run, jobs := stage1Run(t, ctx, store, repo, 41, tc.ref, "push", "failure", now,
			map[string]string{"go-long": "failure"})
		if err := handleStage1Verdict(ctx, Deps{Store: store}, run, jobs, now); err != nil {
			t.Fatal(err)
		}
		again, jobs := stage1Run(t, ctx, store, repo, 42, "main", "schedule", "failure", now.Add(time.Hour),
			map[string]string{"go-long": "failure"})
		if err := handleStage1Verdict(ctx, Deps{Store: store}, again, jobs, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		ids := stage1Repairs(t, ctx, store, repo)
		if len(ids) != 1 {
			t.Fatalf("%s: %d repairs, want 1", tc.ref, len(ids))
		}
		repair, err := store.GetTask(ctx, ids[0])
		if err != nil || repair == nil {
			t.Fatal(err)
		}
		if got := model.FoundIn(*repair); got != tc.want {
			t.Errorf("repair of a red %s found in %q, want %q", tc.ref, got, tc.want)
		}
	}
}

// TestABackportIsFoundInTheReleaseItsBranchCarries: grain's own port of a
// merged change onto release/vX.Y.Z is a defect vX.Y.Z shipped, whatever
// its source was filed against; a port onto any other branch keeps the
// source's.
func TestABackportIsFoundInTheReleaseItsBranchCarries(t *testing.T) {
	store, ctx := openTreeStore(t)
	now := time.Date(2026, 10, 16, 6, 0, 0, 0, time.UTC)
	source := externalSource("7")
	model.SetFoundIn(&source, "v2.6.0-rc.1")
	ref := model.PullRequestRef{Repo: widgets, Number: 30}
	for target, want := range map[string]string{
		"release/v2.5.0": "v2.5.0",
		"candidate":      "v2.6.0-rc.1",
	} {
		port, err := newPortTask(ctx, store, source, ref, github.PullRequestDetail{Title: "x", BaseRef: "main"}, target, now)
		if err != nil {
			t.Fatal(err)
		}
		if got := model.FoundIn(port); got != want {
			t.Errorf("port onto %s found in %q, want %q", target, got, want)
		}
	}
}
