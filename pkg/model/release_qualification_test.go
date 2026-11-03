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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

const (
	qualTag = "v1.4.0-rc.2"
	qualSHA = "0123456789abcdef0123456789abcdef01234567"
)

func footer(verdict, tag, sha string) string {
	return "Everything checked out.\n\nQualification-Verdict: " + verdict +
		"\nQualified-Tag: " + tag + "\nQualified-Commit: " + sha + "\n"
}

func passingAttempt() model.QualificationAttempt {
	return model.QualificationAttempt{
		TaskID: "t1", TemplateID: "qualification-mergequeue",
		State: model.StateAwaitingReply, Note: footer("PASS", qualTag, qualSHA),
	}
}

func TestParseQualificationFooterReadsTheLastValueOfEachKey(t *testing.T) {
	note := "The format is `Qualification-Verdict: PASS|FAIL|INFRA`.\n\n" + footer("FAIL", qualTag, qualSHA)
	f, ok := model.ParseQualificationFooter(note)
	if !ok || f.Verdict != model.QualificationFail || f.Tag != qualTag || f.Commit != qualSHA {
		t.Fatalf("got (%+v, %v), want the FAIL footer the note ends with", f, ok)
	}
}

func TestParseQualificationFooterRefusesAnIncompleteFooter(t *testing.T) {
	for name, note := range map[string]string{
		"none":          "looks fine",
		"short sha":     footer("PASS", qualTag, "0123456"),
		"no tag":        "Qualification-Verdict: PASS\nQualified-Commit: " + qualSHA,
		"other verdict": footer("MAYBE", qualTag, qualSHA),
	} {
		if f, ok := model.ParseQualificationFooter(note); ok {
			t.Errorf("%s: parsed %+v, want no footer", name, f)
		}
	}
}

func TestJudgeQualificationAttempt(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*model.QualificationAttempt)
		want   model.ItemResult
		reason string
	}{
		{"clean comment with a PASS footer", func(*model.QualificationAttempt) {}, model.ItemPass, ""},
		{"nothing_needed counts too", func(a *model.QualificationAttempt) { a.State = model.StateCompleted }, model.ItemPass, ""},
		{"still running", func(a *model.QualificationAttempt) { a.State = model.StateRunning }, model.ItemPending, ""},
		{"opened a pull request", func(a *model.QualificationAttempt) { a.OpenedPullRequest = true }, model.ItemFail, "pull request"},
		{"proposed a follow-up", func(a *model.QualificationAttempt) { a.Proposed = true }, model.ItemFail, "follow-up"},
		{"no footer", func(a *model.QualificationAttempt) { a.Note = "all good" }, model.ItemFail, "footer"},
		{"another commit", func(a *model.QualificationAttempt) {
			a.Note = footer("PASS", qualTag, strings.Repeat("f", 40))
		}, model.ItemFail, "qualified commit"},
		{"another tag", func(a *model.QualificationAttempt) { a.Note = footer("PASS", "v1.4.0-rc.1", qualSHA) }, model.ItemFail, "qualified tag"},
		{"FAIL footer", func(a *model.QualificationAttempt) { a.Note = footer("FAIL", qualTag, qualSHA) }, model.ItemFail, "FAIL"},
		{"INFRA footer", func(a *model.QualificationAttempt) { a.Note = footer("INFRA", qualTag, qualSHA) }, model.ItemInfra, "INFRA"},
		{"leaked scratch branch", func(a *model.QualificationAttempt) { a.LeakedScratchRef = true }, model.ItemFail, "qualification/t1"},
		{"open scratch fixture", func(a *model.QualificationAttempt) { a.OpenChildren = []string{"t9"} }, model.ItemFail, "t9"},
		{"closed", func(a *model.QualificationAttempt) { a.State = model.StateClosed }, model.ItemFail, "closed"},
		{"failed on its own", func(a *model.QualificationAttempt) { a.State, a.LastOutcome = model.StateFailed, "error" }, model.ItemFail, "failed"},
		{"failed on capacity", func(a *model.QualificationAttempt) {
			a.State, a.LastOutcome = model.StateFailed, model.CapacityOutcome
		}, model.ItemInfra, "infrastructure"},
	}
	for _, c := range cases {
		a := passingAttempt()
		c.mutate(&a)
		v := model.JudgeQualificationAttempt(a, qualTag, qualSHA)
		if v.Result != c.want || !strings.Contains(v.Reason, c.reason) {
			t.Errorf("%s: got %s (%q), want %s mentioning %q", c.name, v.Result, v.Reason, c.want, c.reason)
		}
	}
}

var qualItems = []string{"mergequeue", "ci-diagnostics", "base-triage", "system-smoke"}

func verdicts(results ...model.ItemResult) []model.ItemVerdict {
	out := make([]model.ItemVerdict, len(results))
	for i, r := range results {
		out[i] = model.ItemVerdict{TaskID: "t", Result: r}
	}
	return out
}

func decide(attempts map[string][]model.ItemVerdict) model.QualificationDecision {
	return model.DecideReleaseQualification(qualItems, attempts, model.DefaultQualificationPolicy)
}

func TestDecideStartsTheFinalItemOnlyOnceEveryGateHasPassed(t *testing.T) {
	d := decide(map[string][]model.ItemVerdict{
		"mergequeue": verdicts(model.ItemPass), "ci-diagnostics": verdicts(model.ItemPass),
		"base-triage": verdicts(model.ItemPending),
	})
	if d.StartFinal || d.Conclusion != "" {
		t.Fatalf("got %+v with a gate still running, want to wait", d)
	}
	d = decide(map[string][]model.ItemVerdict{
		"mergequeue": verdicts(model.ItemPass), "ci-diagnostics": verdicts(model.ItemPass),
		"base-triage": verdicts(model.ItemPass),
	})
	if !d.StartFinal || d.Conclusion != "" {
		t.Fatalf("got %+v with every gate passed, want system-smoke started", d)
	}
}

func TestDecideSucceedsWhenEveryItemPasses(t *testing.T) {
	d := decide(map[string][]model.ItemVerdict{
		"mergequeue": verdicts(model.ItemPass), "ci-diagnostics": verdicts(model.ItemPass),
		"base-triage": verdicts(model.ItemPass), "system-smoke": verdicts(model.ItemPass),
	})
	if d.Conclusion != model.ConclusionSuccess {
		t.Fatalf("got %+v, want success", d)
	}
}

func TestDecideRetriesAFirstFailureOnceAndThenFails(t *testing.T) {
	d := decide(map[string][]model.ItemVerdict{
		"mergequeue": verdicts(model.ItemFail), "ci-diagnostics": verdicts(model.ItemPass),
		"base-triage": verdicts(model.ItemPass),
	})
	if d.Conclusion != "" || len(d.Rerun) != 1 || d.Rerun[0] != "mergequeue" || d.StartFinal {
		t.Fatalf("got %+v, want mergequeue retried once and nothing else", d)
	}
	d = decide(map[string][]model.ItemVerdict{
		"mergequeue": verdicts(model.ItemFail, model.ItemPass), "ci-diagnostics": verdicts(model.ItemPass),
		"base-triage": verdicts(model.ItemPass),
	})
	if !d.StartFinal {
		t.Fatalf("got %+v after the retry passed, want the flake forgiven", d)
	}
	d = decide(map[string][]model.ItemVerdict{
		"mergequeue": verdicts(model.ItemFail, model.ItemFail), "ci-diagnostics": verdicts(model.ItemPending),
	})
	if d.Conclusion != model.ConclusionFailure || len(d.Rerun) != 0 {
		t.Fatalf("got %+v after the retry failed too, want failure at once", d)
	}
}

func TestDecideFailsThreeFailingItemsWithoutRetrying(t *testing.T) {
	d := decide(map[string][]model.ItemVerdict{
		"mergequeue": verdicts(model.ItemFail), "ci-diagnostics": verdicts(model.ItemFail),
		"base-triage": verdicts(model.ItemFail),
	})
	if d.Conclusion != model.ConclusionFailure || len(d.Rerun) != 0 {
		t.Fatalf("got %+v, want three red items to read as systemic", d)
	}
}

func TestDecideRerunsInfraWithoutSpendingTheFlakeRetry(t *testing.T) {
	d := decide(map[string][]model.ItemVerdict{
		"mergequeue":     verdicts(model.ItemInfra, model.ItemInfra, model.ItemFail),
		"ci-diagnostics": verdicts(model.ItemPass), "base-triage": verdicts(model.ItemPass),
	})
	if d.Conclusion != "" || len(d.Rerun) != 1 {
		t.Fatalf("got %+v, want infra re-runs not to count as the flake retry", d)
	}
}

func TestDecideLeavesACandidateStalledOnInfraNeutral(t *testing.T) {
	stalled := verdicts(model.ItemInfra, model.ItemInfra, model.ItemInfra, model.ItemInfra)
	d := decide(map[string][]model.ItemVerdict{
		"mergequeue": stalled, "ci-diagnostics": verdicts(model.ItemPending), "base-triage": verdicts(model.ItemPass),
	})
	if d.Conclusion != "" {
		t.Fatalf("got %+v, want to wait for the running item before going neutral", d)
	}
	d = decide(map[string][]model.ItemVerdict{
		"mergequeue": stalled, "ci-diagnostics": verdicts(model.ItemPass), "base-triage": verdicts(model.ItemPass),
	})
	if d.Conclusion != model.ConclusionNeutral {
		t.Fatalf("got %+v, want neutral after three infra re-runs", d)
	}
	d = decide(map[string][]model.ItemVerdict{
		"mergequeue":     verdicts(model.ItemInfra, model.ItemInfra, model.ItemInfra),
		"ci-diagnostics": verdicts(model.ItemPass), "base-triage": verdicts(model.ItemPass),
	})
	if d.Conclusion != "" || len(d.Rerun) != 1 {
		t.Fatalf("got %+v, want a third infra re-run", d)
	}
}

func TestParseRCTag(t *testing.T) {
	rc, ok := model.ParseRCTag("v1.12.0-rc.3")
	if !ok || rc.String() != "v1.12.0-rc.3" || rc.Version() != "v1.12.0" {
		t.Fatalf("got (%+v, %v)", rc, ok)
	}
	for _, name := range []string{"v1.12.0", "v1.12.0-rc.3-discarded", "v1.12.0-pending", "candidate"} {
		if _, ok := model.ParseRCTag(name); ok {
			t.Errorf("%s parsed as a candidate", name)
		}
	}
	older, _ := model.ParseRCTag("v1.9.0-rc.4")
	if !older.Less(rc) || rc.Less(older) {
		t.Fatal("v1.9.0-rc.4 should order before v1.12.0-rc.3")
	}
}
