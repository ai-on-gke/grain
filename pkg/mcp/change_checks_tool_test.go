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

package mcp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/change"
)

// fakeChangeChecks is a ChangeChecksReader answering from canned
// reports, recording every wait it was asked for.
type fakeChangeChecks struct {
	status  change.ChecksReport
	waits   []change.ChecksReport
	asked   []change.ChecksWait
	block   time.Duration
	refused []string
	err     error
}

func (f *fakeChangeChecks) ChangeChecksStatus(context.Context) (change.ChecksReport, error) {
	return f.status, f.err
}

func (f *fakeChangeChecks) WaitChangeChecks(_ context.Context, req change.ChecksWait) (change.ChecksReport, error) {
	f.asked = append(f.asked, req)
	time.Sleep(f.block)
	if f.err != nil {
		return change.ChecksReport{}, f.err
	}
	r := f.waits[0]
	if len(f.waits) > 1 {
		f.waits = f.waits[1:]
	}
	return r, nil
}

func (f *fakeChangeChecks) ChangeCIRefusal(_ context.Context, tool string) (string, error) {
	f.refused = append(f.refused, tool)
	return tool + " is grain CI's; this task's CL is checked by TAP.", nil
}

func changeTool(t *testing.T, tools []Tool, name string) Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("no %s among the changelist tools", name)
	return Tool{}
}

func TestChangeChecksToolsAreTheChecksRoster(t *testing.T) {
	var names []string
	for _, tool := range NewChangeChecksTools(&fakeChangeChecks{}) {
		names = append(names, tool.Name)
	}
	want := []string{"pull_request_status", "wait_for_checks", "retrigger_ci", "run_ci_job", "get_ci_log"}
	if !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
}

func TestChangePullRequestStatusIsTheProvidersTextWithHealth(t *testing.T) {
	fake := &fakeChangeChecks{status: change.ChecksReport{
		Text: "cl/123 snapshot 4: presubmit failed\n- //foo:bar_test failed", Health: change.HealthFailing,
	}}
	res := changeTool(t, NewChangeChecksTools(fake), "pull_request_status").Handler(context.Background(), nil)
	if res.IsError {
		t.Fatalf("status errored: %s", res.Text)
	}
	for _, want := range []string{"cl/123 snapshot 4: presubmit failed", "//foo:bar_test failed", "Health: failing."} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("status = %q, want it to contain %q", res.Text, want)
		}
	}

	fake.err = errors.New("credentials_expired: renew them")
	res = changeTool(t, NewChangeChecksTools(fake), "pull_request_status").Handler(context.Background(), nil)
	if !res.IsError || !strings.Contains(res.Text, "credentials_expired") {
		t.Errorf("status with a failing provider = %+v, want its error", res)
	}
}

func TestChangeWaitForChecksReportsAVerdictTelemetryReads(t *testing.T) {
	fake := &fakeChangeChecks{waits: []change.ChecksReport{{
		Text: "Presubmit on snapshot 4: failed.", Presubmit: change.PresubmitFailed, Snapshot: 4,
		Failing: []string{"//foo:bar_test"}, Provider: "tap", SatisfiesGate: false,
		FaultNote: "//flaky:test is failing on other CLs too: grain files no repair task for it.",
	}}}
	res := changeTool(t, NewChangeChecksTools(fake), "wait_for_checks").Handler(context.Background(), nil)
	if res.IsError {
		t.Fatalf("wait errored: %s", res.Text)
	}
	for _, want := range []string{"Verdict from tap: failed.", "Presubmit on snapshot 4: failed.", "failing on other CLs too"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("wait = %q, want it to contain %q", res.Text, want)
		}
	}
	wait, ok := ReadCheckWait(res.Text)
	if !ok || wait.Provider != "tap" || wait.Verdict != WaitVerdictFailed || !wait.WaitedKnown {
		t.Errorf("ReadCheckWait = %+v, %v; want a known tap failed wait", wait, ok)
	}
	if len(fake.asked) != 1 || len(fake.asked[0].Jobs) != 0 {
		t.Errorf("asked = %+v, want one unnarrowed wait", fake.asked)
	}
}

// A narrowed wait never satisfies the gate, whatever the provider said,
// and says so.
func TestChangeWaitForChecksWithJobsNeverSatisfiesTheGate(t *testing.T) {
	fake := &fakeChangeChecks{waits: []change.ChecksReport{{
		Text: "//foo:bar_test passed.", Presubmit: change.PresubmitPassed, Provider: "tap", SatisfiesGate: true,
	}}}
	res := changeTool(t, NewChangeChecksTools(fake), "wait_for_checks").Handler(context.Background(),
		map[string]any{"jobs": []any{"//foo:bar_test"}})
	if !slices.Equal(fake.asked[0].Jobs, []string{"//foo:bar_test"}) {
		t.Errorf("jobs = %v, want the TAP target passed through", fake.asked[0].Jobs)
	}
	if !strings.Contains(res.Text, "does not satisfy this task's gate") {
		t.Errorf("selective wait = %q, want it to say it does not satisfy the gate", res.Text)
	}
	report, _, err := waitChangeChecks(context.Background(), &fakeChangeChecks{waits: fake.waits},
		[]string{"//foo:bar_test"}, time.Minute)
	if err != nil || report.SatisfiesGate || !report.Selective {
		t.Errorf("selective report = %+v, %v; want Selective and not SatisfiesGate", report, err)
	}
}

// The time a changelist wait blocks is credited to the run's deadline
// through the same ledger wait_for_checks' GitHub and grain CI waits use,
// under the same cap, and a long wait is several slices each credited.
func TestChangeWaitForChecksCreditsBlockedTime(t *testing.T) {
	defer func(old time.Duration) { maxChangeWaitSlice = old }(maxChangeWaitSlice)
	maxChangeWaitSlice = 20 * time.Millisecond

	granter := &fakeGranter{cap: 45 * time.Minute}
	ctx, _ := runWithCreditLeft(time.Hour, 45*time.Minute, granter)
	fake := &fakeChangeChecks{block: 20 * time.Millisecond, waits: []change.ChecksReport{
		{Text: "running", Presubmit: change.PresubmitRunning, TimedOut: true, Waiting: []string{"//a:test"}},
		{Text: "passed", Presubmit: change.PresubmitPassed, SatisfiesGate: true},
	}}
	res := changeTool(t, NewChangeChecksTools(fake), "wait_for_checks").Handler(ctx, nil)
	if res.IsError {
		t.Fatalf("wait errored: %s", res.Text)
	}
	if len(fake.asked) != 2 {
		t.Fatalf("asked %d slices, want 2 (one timed out, one with a verdict)", len(fake.asked))
	}
	for _, a := range fake.asked {
		if a.Timeout > maxChangeWaitSlice {
			t.Errorf("slice timeout = %s, want at most %s", a.Timeout, maxChangeWaitSlice)
		}
	}
	if fake.asked[1].Credited <= fake.asked[0].Credited {
		t.Errorf("credited = %s then %s, want it to grow with the wait", fake.asked[0].Credited, fake.asked[1].Credited)
	}
	if len(granter.reports) != 2 || granter.reports[1] <= granter.reports[0] || granter.reports[0] <= 0 {
		t.Errorf("credit reports = %v, want two growing cumulative totals", granter.reports)
	}
	if wait, ok := ReadCheckWait(res.Text); !ok || wait.Verdict != WaitVerdictPassed {
		t.Errorf("ReadCheckWait = %+v, %v; want passed", wait, ok)
	}
}

func TestChangeGrainCIToolsRefuseInTheProvidersWords(t *testing.T) {
	fake := &fakeChangeChecks{}
	tools := NewChangeChecksTools(fake)
	for _, name := range []string{"retrigger_ci", "run_ci_job", "get_ci_log"} {
		res := changeTool(t, tools, name).Handler(context.Background(), map[string]any{"job": "go"})
		if !res.IsError || !strings.Contains(res.Text, name+" is grain CI's; this task's CL is checked by TAP.") {
			t.Errorf("%s = %+v, want the provider's refusal as an error", name, res)
		}
	}
	if !slices.Equal(fake.refused, []string{"retrigger_ci", "run_ci_job", "get_ci_log"}) {
		t.Errorf("refusals asked for %v", fake.refused)
	}
}

func TestChangeWaitVerdicts(t *testing.T) {
	for _, tc := range []struct {
		r    change.ChecksReport
		want string
	}{
		{change.ChecksReport{Failing: []string{"x"}}, WaitVerdictFailed},
		{change.ChecksReport{TimedOut: true}, WaitVerdictTimedOut},
		{change.ChecksReport{Presubmit: change.PresubmitNotRun}, WaitVerdictNoChecks},
		{change.ChecksReport{Presubmit: change.PresubmitPassed}, WaitVerdictPassed},
	} {
		if got := changeWaitVerdict(tc.r); got != tc.want {
			t.Errorf("verdict(%+v) = %s, want %s", tc.r, got, tc.want)
		}
	}
}
