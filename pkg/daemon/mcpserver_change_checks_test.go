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

package daemon

// A changelist task's checks tools as a run's mcpserver serves them: the
// tools mcpserver registers for a CL task (NewChangeChecksTools over
// DaemonChangeChecks), against a real ui.Server and store over HTTP,
// with the provider the one boundary stubbed. This is gap 1 and gap 4
// of docs/test-plans/2026-10-29.md §1, which grain demo cannot reach
// because it never runs a run's mcpserver against its own CL tasks
// (docs/test-runs/2026-10-29-changelist-checks-review-mail.md).

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/change"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// walkTAP is a CL provider with presubmit checks: one passing and one
// failing check on snapshot 4, and credentials that can expire.
type walkTAP struct {
	expired bool
	waits   []change.ChecksWait
}

func (*walkTAP) Kind() model.LinkKind { return model.LinkChangelist }
func (*walkTAP) TargetKind() string   { return "walktap" }
func (p *walkTAP) Observe(context.Context, model.Task, string) (change.Observation, error) {
	return change.Observation{}, nil
}
func (*walkTAP) Threads(context.Context, model.Task, string) ([]change.Thread, error) {
	return nil, nil
}
func (*walkTAP) Submit(context.Context, model.Task, string, change.Person) (change.Result, error) {
	return change.Result{}, nil
}
func (*walkTAP) Sync(context.Context, model.Task, string) (change.SyncResult, error) {
	return change.SyncResult{}, nil
}
func (p *walkTAP) Check(context.Context) error {
	if p.expired {
		return &change.CredentialsExpiredError{Kind: "walktap", Detail: "still expired"}
	}
	return nil
}

func (p *walkTAP) report(target string, jobs []string) (change.ChecksReport, error) {
	if p.expired {
		return change.ChecksReport{}, &change.CredentialsExpiredError{Kind: "walktap", Detail: "LOAS2 certificate has expired"}
	}
	r := change.ChecksReport{Provider: "tap", Snapshot: 4, Health: change.HealthFailing, Presubmit: change.PresubmitFailed,
		Text: target + " snapshot 4: //a:test failed, //a:lint passed."}
	if len(jobs) == 1 && jobs[0] == "//a:lint" {
		r.Presubmit, r.Health, r.Text = change.PresubmitPassed, change.HealthOK, target+" snapshot 4: //a:lint passed."
	} else {
		r.Failing = []string{"//a:test"}
	}
	return r, nil
}

func (p *walkTAP) ChecksStatus(_ context.Context, _ model.Task, target string) (change.ChecksReport, error) {
	return p.report(target, nil)
}

func (p *walkTAP) WaitChecks(_ context.Context, _ model.Task, target string, req change.ChecksWait) (change.ChecksReport, error) {
	p.waits = append(p.waits, req)
	r, err := p.report(target, req.Jobs)
	r.SatisfiesGate = err == nil && len(req.Jobs) == 0 && r.Presubmit == change.PresubmitPassed
	return r, err
}

func (*walkTAP) GrainCIRefusal(tool string) string {
	return tool + " is grain CI's; this CL's presubmit is TAP's."
}

func TestChangeChecksToolsOverALiveServer(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(t.TempDir(), "data")))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTask(ctx, model.Task{
		ID: "7", Intent: model.IntentImplement, Title: "port it",
		ChangeTarget: &model.ChangeTarget{Kind: "walktap"},
		Links:        []model.Link{{Kind: model.LinkChangelist, Target: "cl/700"}},
	}); err != nil {
		t.Fatal(err)
	}
	tap := &walkTAP{}
	change.Register(tap)
	t.Cleanup(func() {
		change.Unregister(model.LinkChangelist)
		change.ClearBlocked("walktap")
		change.Forget("cl/700")
	})
	srv := httptest.NewServer(ui.NewServer(ui.Config{
		Actor:         ui.DefaultActor("operator"),
		Capabilities:  ui.OfferedCapabilities(),
		LocalAPIToken: testAccessCode,
	}, store))
	t.Cleanup(srv.Close)
	tools := map[string]mcp.Tool{}
	for _, tool := range mcp.NewChangeChecksTools(DaemonChangeChecks{Client: accessCodeClient(srv.URL), TaskID: "7"}) {
		tools[tool.Name] = tool
	}
	call := func(name string, args map[string]any) mcp.Result {
		t.Helper()
		tool, ok := tools[name]
		if !ok {
			t.Fatalf("a CL task's run has no %s tool", name)
		}
		return tool.Handler(ctx, args)
	}

	// pull_request_status is the provider's answer, and the badge reads it.
	status := call("pull_request_status", map[string]any{})
	if status.IsError || !strings.Contains(status.Text, "cl/700 snapshot 4: //a:test failed") {
		t.Fatalf("pull_request_status = %+v, want TAP's text", status)
	}
	if obs, _ := change.LastObserved("cl/700"); obs.Presubmit != change.PresubmitFailed || obs.Snapshot != 4 {
		t.Fatalf("observation after status = %+v, want failed snapshot 4 for the badge", obs)
	}

	// A full wait: the verdict line telemetry stores, provider and all.
	full := call("wait_for_checks", map[string]any{"timeout_seconds": 5})
	wait, ok := mcp.ReadCheckWait(full.Text)
	if full.IsError || !ok || wait.Provider != "tap" || wait.Verdict != mcp.WaitVerdictFailed {
		t.Fatalf("wait_for_checks = %q read back as %+v, want tap: failed", full.Text, wait)
	}

	// A jobs-narrowed wait that passes still never satisfies the gate.
	narrow := call("wait_for_checks", map[string]any{"timeout_seconds": 5, "jobs": []any{"//a:lint"}})
	wait, ok = mcp.ReadCheckWait(narrow.Text)
	if narrow.IsError || !ok || wait.Provider != "tap" || wait.Verdict != mcp.WaitVerdictPassed {
		t.Fatalf("narrowed wait_for_checks = %q read back as %+v, want tap: passed", narrow.Text, wait)
	}
	if !strings.Contains(strings.ToLower(narrow.Text), "gate") {
		t.Fatalf("narrowed wait_for_checks = %q, want it to say it does not satisfy the gate", narrow.Text)
	}
	if len(tap.waits) != 2 || len(tap.waits[1].Jobs) != 1 || tap.waits[0].Timeout <= 0 {
		t.Fatalf("provider saw waits %+v, want a full then a narrowed one, each with a timeout", tap.waits)
	}

	// grain CI's own tools refuse in the provider's words.
	for _, name := range []string{"retrigger_ci", "run_ci_job", "get_ci_log"} {
		res := call(name, map[string]any{"job": "x"})
		if !strings.Contains(res.Text, name+" is grain CI's; this CL's presubmit is TAP's.") {
			t.Fatalf("%s = %+v, want the provider's refusal", name, res)
		}
	}

	// Expired credentials: the call answers with the block, the kind is
	// blocked, and the operator notice is raised once.
	tap.expired = true
	expired := call("pull_request_status", map[string]any{})
	if !expired.IsError || !strings.Contains(expired.Text, "credentials_expired") {
		t.Fatalf("pull_request_status with expired credentials = %+v, want credentials_expired", expired)
	}
	again := call("wait_for_checks", map[string]any{"timeout_seconds": 5})
	if !again.IsError || !strings.Contains(again.Text, "blocked since") || len(tap.waits) != 2 {
		t.Fatalf("wait_for_checks while blocked = %+v (provider waits %d), want the block without asking TAP", again, len(tap.waits))
	}
	open := func() []model.Notification {
		t.Helper()
		ns, err := store.ListNotifications(ctx, model.NotificationFilter{Open: true, Family: "credentials-expired.changelist"})
		if err != nil {
			t.Fatal(err)
		}
		return ns
	}
	if ns := open(); len(ns) != 1 || ns[0].RaiseCount != 1 {
		t.Fatalf("open credential notices = %+v, want one raised once", ns)
	}

	// The recheck clears nothing while the check still fails, then
	// clears the block and the notice once it passes.
	change.Recheck(ctx, store)
	if len(open()) != 1 {
		t.Fatal("a failing recheck cleared the notice")
	}
	tap.expired = false
	change.Recheck(ctx, store)
	if ns := open(); len(ns) != 0 {
		t.Fatalf("open credential notices after recovery = %+v, want none", ns)
	}
	if res := call("pull_request_status", map[string]any{}); res.IsError {
		t.Fatalf("pull_request_status after recovery = %+v", res)
	}
}
