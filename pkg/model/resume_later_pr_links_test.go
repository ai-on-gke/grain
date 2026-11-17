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
	"reflect"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// ResumeLaterPullRequestLinks is the pull requests of tasks resting on a
// resume_later of their own, which OpenPullRequestLinks leaves out: the
// one SyncReviewThreads and SyncReviewQuestions read beside it
// (docs/test-runs/2026-11-15-resume-later-release.md). A task a person
// deferred, and a closed one, are not in it.
func TestResumeLaterPullRequestLinksAreTheTasksRestingOnTheirOwnWait(t *testing.T) {
	t.Parallel()
	store, ctx := open(t)
	pr := func(id string) model.Task {
		tk := model.Task{
			ID: id, Intent: model.IntentImplement, Title: "t " + id,
			Filing:   model.Filing{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
			Approval: &model.Attribution{Actor: human}, Binding: model.BindingDirective,
			Target: &model.RepoRef{Owner: "acme", Name: "widgets"},
			Links:  []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#" + id}},
		}
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
		return tk
	}
	at := now.Add(time.Hour)
	waiting, person, closed := pr("901"), pr("902"), pr("903")
	for _, tk := range []model.Task{waiting, closed} {
		if err := store.StartRun(ctx, model.Run{ID: tk.ID + "-1", TaskID: tk.ID, Sandbox: "s", Attempt: 1,
			StartedAt: at.Add(-time.Minute)}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.SetDeferred(ctx, tk.ID, &at, nil, at); err != nil {
			t.Fatal(err)
		}
		if err := store.HoldResumeOn(ctx, tk.ID, model.ResumeOn{CI: model.ResumeOnMyBranch}, at); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, tk.ID+"-1", at.Add(time.Second), "succeeded", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetDeferred(ctx, person.ID, &at, nil, at); err != nil {
		t.Fatal(err)
	}
	closedAt := at.Add(time.Minute)
	if err := store.ObserveField(ctx, closed.ID, closedAt, func(o *model.Observation) { o.ClosedAt = &closedAt }); err != nil {
		t.Fatal(err)
	}

	got, err := store.ResumeLaterPullRequestLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []model.TaskPullRequestLink{{TaskID: "901", PullRequest: "acme/widgets#901"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ResumeLaterPullRequestLinks = %+v, want %+v", got, want)
	}
	if ok, err := store.RestsOnResumeLater(ctx, person.ID); err != nil || ok {
		t.Fatalf("RestsOnResumeLater on a person's deferral = %v, %v; want false", ok, err)
	}
}
