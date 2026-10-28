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

package ui_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/change"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// fakeTAP is a changelist provider that answers checks.
type fakeTAP struct {
	fakeCLs
	waits []change.ChecksWait
}

func (f *fakeTAP) Kind() model.LinkKind { return model.LinkChangelist }
func (f *fakeTAP) TargetKind() string   { return "faketap" }
func (f *fakeTAP) ChecksStatus(_ context.Context, _ model.Task, target string) (change.ChecksReport, error) {
	return change.ChecksReport{Text: target + ": presubmit failed", Health: change.HealthFailing,
		Presubmit: change.PresubmitFailed, Snapshot: 2, Failing: []string{"//a:test"}, Provider: "tap"}, nil
}
func (f *fakeTAP) WaitChecks(_ context.Context, _ model.Task, target string, req change.ChecksWait) (change.ChecksReport, error) {
	f.waits = append(f.waits, req)
	return change.ChecksReport{Text: target + ": passed", Presubmit: change.PresubmitPassed,
		Selective: len(req.Jobs) > 0, SatisfiesGate: len(req.Jobs) == 0, Provider: "tap"}, nil
}
func (f *fakeTAP) GrainCIRefusal(tool string) string { return tool + ": not on a CL" }

func TestChangeChecksAnswersThroughTheRegisteredChecks(t *testing.T) {
	client, store, ctx := testClient(t)
	fake := &fakeTAP{}
	change.Register(fake)
	t.Cleanup(func() { change.Unregister(model.LinkChangelist); change.Forget("cl/42") })

	tk := model.Task{ID: "81", Title: "t", Intent: model.IntentImplement, ChangeTarget: &model.ChangeTarget{Kind: "faketap"}}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ChangeChecks(ctx, "81", ui.ChangeChecksRequest{Op: ui.ChangeChecksStatus}); err == nil ||
		!strings.Contains(err.Error(), "no CL yet") {
		t.Fatalf("status before any CL = %v, want it to say there is no CL yet", err)
	}
	refusal, err := client.ChangeChecks(ctx, "81", ui.ChangeChecksRequest{Op: ui.ChangeChecksRefusal, Tool: "run_ci_job"})
	if err != nil || refusal.Refusal != "run_ci_job: not on a CL" {
		t.Fatalf("refusal = %+v, %v", refusal, err)
	}

	if err := store.UpdateTask(ctx, "81", func(t *model.Task) error {
		t.Links = append(t.Links, model.Link{Kind: model.LinkChangelist, Target: "cl/42"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	status, err := client.ChangeChecks(ctx, "81", ui.ChangeChecksRequest{Op: ui.ChangeChecksStatus})
	if err != nil || status.Report.Text != "cl/42: presubmit failed" {
		t.Fatalf("status = %+v, %v", status, err)
	}
	// What the agent read is what the badge shows.
	got, err := client.GetTask(ctx, "81")
	if err != nil {
		t.Fatal(err)
	}
	if got.Presubmit == nil || got.Presubmit.State != "failed" || len(got.Presubmit.Failing) != 1 || got.Presubmit.Failing[0] != "//a:test" {
		t.Fatalf("presubmit on the task = %+v", got.Presubmit)
	}

	wait, err := client.ChangeChecks(ctx, "81", ui.ChangeChecksRequest{Op: ui.ChangeChecksWait,
		Jobs: []string{"//a:test"}, TimeoutMS: 60_000, CreditedMS: 5_000})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.waits) != 1 || fake.waits[0].Timeout != time.Minute || fake.waits[0].Credited != 5*time.Second {
		t.Fatalf("waits = %+v", fake.waits)
	}
	if !wait.Report.Selective || wait.Report.SatisfiesGate {
		t.Fatalf("selective wait = %+v, want it never to satisfy the gate", wait.Report)
	}

	change.MarkBlocked("faketap", change.BlockedCredentials, "expired", time.Now())
	t.Cleanup(func() { change.ClearBlocked("faketap") })
	if _, err := client.ChangeChecks(ctx, "81", ui.ChangeChecksRequest{Op: ui.ChangeChecksStatus}); !change.IsCredentialsExpired(err) {
		t.Fatalf("status while blocked = %v, want credentials_expired", err)
	}
}
