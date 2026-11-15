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
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func gatesByID(t model.Task, states map[string]model.State) map[string]model.MergeGate {
	out := map[string]model.MergeGate{}
	for _, g := range model.ComputeMergeGates(t, nil, states) {
		out[g.ID] = g
	}
	return out
}

// A targeted review -- a sensitive trigger's, with no template of its own
// -- is its own merge gate beside the code review: its task, its findings
// and its state are its own, and an override of `review` still covers it.
func TestTargetedReviewsAreAGateApartFromTheCodeReview(t *testing.T) {
	t.Parallel()
	task := model.Task{ID: "t1", Links: []model.Link{
		{Kind: model.LinkFixes, Target: "acme/widgets#7"},
		{Kind: model.LinkPathTriggerHold, Target: string(model.TriggerHumanOnFindings)},
		{Kind: model.LinkPathTriggerTask, Target: "every-pr=r1"},
		{Kind: model.LinkPathTriggerTask, Target: "release-machinery=r2"},
		{Kind: model.LinkPathTriggerTask, Target: "grain-declarations=r2"},
		{Kind: model.LinkTargetedReviewTrigger, Target: "release-machinery"},
		{Kind: model.LinkTargetedReviewTrigger, Target: "grain-declarations"},
	}}
	states := map[string]model.State{"r1": model.StateCompleted, "r2": model.StateRunning}

	gates := gatesByID(task, states)
	if g := gates[model.GateReview]; g.Label != "Code review" || g.State != model.GateStatePassed || g.ReviewTask != "r1" {
		t.Errorf("code review gate = %+v, want r1's, passed", g)
	}
	if g := gates[model.GateTargetedReview]; g.Label != "Targeted review" || g.State != model.GateStateRunning || g.ReviewTask != "r2" {
		t.Errorf("targeted review gate = %+v, want r2's, running", g)
	}

	model.AddReviewFindings(&task, model.ReviewFindingsRecord{Gate: "trigger:release-machinery", ReviewTask: "r2",
		Summary: "review r2 left findings", At: time.Unix(0, 0)})
	gates = gatesByID(task, map[string]model.State{"r1": model.StateCompleted, "r2": model.StateCompleted})
	if g := gates[model.GateTargetedReview]; g.State != model.GateStateFindings {
		t.Errorf("targeted review gate = %+v, want its trigger's findings", g)
	}
	if g := gates[model.GateReview]; g.State != model.GateStatePassed {
		t.Errorf("code review gate = %+v, want it passed: the findings are the targeted review's", g)
	}

	if !model.GateOverriddenBy(model.GateTargetedReview, []string{model.GateReview}) {
		t.Error("--override-gates=review no longer covers a targeted review")
	}
	if model.GateOverriddenBy(model.GateReview, []string{model.GateTargetedReview}) {
		t.Error("overriding the targeted review overrode the code review")
	}
}

// A task with no targeted trigger recorded -- every review its triggers
// filed was from a template -- shows one Code review gate, as before.
func TestTriggerReviewsFromTemplatesStayTheCodeReview(t *testing.T) {
	t.Parallel()
	task := model.Task{ID: "t1", Links: []model.Link{
		{Kind: model.LinkFixes, Target: "acme/widgets#7"},
		{Kind: model.LinkPathTriggerHold, Target: string(model.TriggerHumanOnFindings)},
		{Kind: model.LinkPathTriggerTask, Target: "every-pr=r1"},
		{Kind: model.LinkPathTriggerTask, Target: "docs=r2"},
	}}
	gates := gatesByID(task, map[string]model.State{"r1": model.StateCompleted, "r2": model.StateRunning})
	if _, ok := gates[model.GateTargetedReview]; ok {
		t.Errorf("a targeted review gate appeared with no targeted trigger: %+v", gates)
	}
	if g := gates[model.GateReview]; g.Label != "Code review" || g.State != model.GateStateRunning {
		t.Errorf("code review gate = %+v, want running on r2", g)
	}
}
