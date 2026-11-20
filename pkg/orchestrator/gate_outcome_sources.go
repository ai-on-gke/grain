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
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
)

// This wrapper observes only the successful legacy finish decision. It never
// derives a product from FinalText: the existing relay must have persisted it.
func processResultFrom(ctx context.Context, store *model.Store, reach *gitproxy.Reach, client github.Client,
	appBot AppBotIdentityFunc, task model.Task, result *agent.Result, runID, preRunBranchSHA string, now time.Time) error {
	err := processResultFromLegacy(ctx, store, reach, client, appBot, task, result, runID, preRunBranchSHA, now)
	if err != nil {
		return err
	}
	diagnosticCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	ctx = diagnosticCtx
	t, terr := store.GetTask(ctx, task.ID)
	o, oerr := store.GetObservation(ctx, task.ID)
	if terr != nil || oerr != nil || t == nil || o == nil || o.CompletedAt == nil || o.CompletedAt.Truncate(time.Microsecond).Before(now.Truncate(time.Microsecond)) || o.PendingQuestionCommentID != nil || o.PendingAccess != nil || o.PendingSecret != "" {
		return nil
	}
	product := "report"
	if hasSuccessfulCall(result, "nothing_needed") {
		product = "nothing_needed"
	}
	// An owning open PR/CL takes precedence over no-work or a report.
	for _, l := range t.Links {
		if l.Kind.IsChange() {
			product = "change"
		}
	}
	if (t.Filing.Reason == model.ReasonReview || t.Intent == model.IntentReview) && product == "report" {
		product = "review"
	}
	runs, rerr := store.LatestRunsForState(ctx, task.ID)
	if rerr != nil {
		return nil
	}
	for _, run := range runs {
		if run.ID == runID {
			if run.Outcome == "no_action" || run.Outcome == model.ParkedOutcome || run.Outcome == model.InterruptedOutcome {
				return nil
			}
			if run.Outcome == model.NothingNeededOutcome {
				product = "nothing_needed"
			}
		}
	}
	store.RecordGateOutcomeSource(ctx, task.ID, model.GateOutcomeSource{RunID: runID, Product: product, Resource: "finish", State: "settled", Policy: model.GateOutcomePolicy(*t), At: *o.CompletedAt})
	return nil
}
func hasSuccessfulCall(r *agent.Result, tool string) bool {
	if r == nil {
		return false
	}
	for _, c := range r.ToolCalls {
		if c.Name == tool && !c.IsError {
			return true
		}
	}
	return false
}

func recordGatePRSource(ctx context.Context, store *model.Store, taskID string, ref model.PullRequestRef, detail github.PullRequestDetail, now time.Time, attributionOverride ...string) {
	t, err := store.GetTask(ctx, taskID)
	if err != nil || t == nil || detail.Number <= 0 || detail.HeadSHA == "" {
		return
	}
	if ref.Number != detail.Number || !hasPRLink(*t, ref) {
		return
	}
	state := "open"
	if detail.State == "closed" {
		state = "closed"
	}
	if detail.Merged {
		state = "merged"
	}
	attribution := "forge-observed-merged-by:" + detail.MergedBy
	if len(attributionOverride) > 0 {
		attribution = attributionOverride[0]
	}
	store.RecordGateOutcomeSource(ctx, taskID, model.GateOutcomeSource{Resource: ref.String(), Head: detail.HeadSHA, State: state, Policy: model.GateOutcomePolicy(*t), Attribution: attribution, At: now})
}

func hasPRLink(t model.Task, ref model.PullRequestRef) bool {
	for _, l := range t.Links {
		if l.Kind == model.LinkFixes && l.Target == ref.String() {
			return true
		}
	}
	return false
}

func recordGateEquivalent(ctx context.Context, store *model.Store, task model.Task, key string, w landedWork, now time.Time) {
	if !w.all() || !w.Pinned || w.ObservedHead == "" || w.ObservedBase == "" {
		return
	}
	store.RecordGateOutcomeSource(ctx, task.ID, model.GateOutcomeSource{Resource: key, Head: w.ObservedHead, BaseHead: w.ObservedBase, State: "equivalent", Policy: model.GateOutcomePolicy(task), Proof: gates.Digest(w), Attribution: "verified-landed-equivalence", At: now})
}

func recordGatePRUnavailable(ctx context.Context, store *model.Store, task model.Task, ref model.PullRequestRef, now time.Time) {
	store.RecordGateOutcomeSource(ctx, task.ID, model.GateOutcomeSource{Resource: ref.String(), State: "unknown", At: now})
}
