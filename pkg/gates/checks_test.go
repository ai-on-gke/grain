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

package gates

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func checkFixture(now time.Time) ChecksFacts {
	a := CheckAttempt{Identity: "trusted/go", Names: []string{"go"}, Head: "H1", Version: Digest("run1"), Order: 1, State: "completed", Conclusion: "success"}
	return ChecksFacts{TaskID: "66", Subject: Subject{Kind: "change", Authority: "github.com", Repository: "acme/widgets", ChangeKind: "pr", ID: "7"}, Head: "H1", PolicyVersion: Digest("policy1"), PolicyComplete: true, Mode: "github", ObservedAt: now, ValidUntil: now.Add(time.Minute), Sources: []CheckSource{{Kind: "github", Head: "H1", Version: Digest("collection1"), Complete: true, All: true, Attempts: []CheckAttempt{a}}}}
}
func TestChecksPredicatesAndSourceSemantics(t *testing.T) {
	now := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		edit func(*ChecksFacts)
		want string
	}{
		{"success", func(*ChecksFacts) {}, "met"},
		{"authorized-none", func(f *ChecksFacts) { f.Mode = "none"; f.Sources = nil; f.ExplicitNone = true }, "met"},
		{"unauthorized-none", func(f *ChecksFacts) { f.Mode = "none"; f.Sources = nil }, "unknown"},
		{"empty-jobs-positive-workflow", func(f *ChecksFacts) { f.Sources[0].Attempts[0].Workflow = true }, "unknown"},
		{"empty-complete", func(f *ChecksFacts) { f.Sources[0].Attempts = nil }, "unknown"},
		{"missing-source", func(f *ChecksFacts) { f.Sources = nil }, "unknown"},
		{"partial", func(f *ChecksFacts) { f.Sources[0].Complete = false }, "unknown"},
		{"denied", func(f *ChecksFacts) { f.Sources[0].Complete = false; f.Gap = "denied" }, "unknown"},
		{"source-error", func(f *ChecksFacts) { f.Sources[0].Error = true }, "error"},
		{"missing-policy", func(f *ChecksFacts) { f.PolicyComplete = false }, "unknown"},
		{"head-changed", func(f *ChecksFacts) { f.Head = "H2" }, "unknown"},
		{"mixed-head", func(f *ChecksFacts) { f.Sources[0].Attempts[0].Head = "H0" }, "unknown"},
		{"stale", func(f *ChecksFacts) { f.ValidUntil = now }, "unknown"},
		{"future", func(f *ChecksFacts) { f.ObservedAt = now.Add(time.Second) }, "unknown"},
		{"missing-required", func(f *ChecksFacts) { f.Sources[0].Required = []string{"test"} }, "pending"},
		{"wrong-app", func(f *ChecksFacts) { f.Sources[0].Required = []string{"other/go"} }, "pending"},
		{"active", func(f *ChecksFacts) { f.Sources[0].Attempts[0].State = "running" }, "pending"},
		{"new-attempt-replaces-pass", func(f *ChecksFacts) {
			a := f.Sources[0].Attempts[0]
			a.Order = 2
			a.Version = Digest("retry")
			a.Conclusion = "cancelled"
			f.Sources[0].Attempts = append(f.Sources[0].Attempts, a)
		}, "pending"},
		{"new-attempt-replaces-failure", func(f *ChecksFacts) {
			a := f.Sources[0].Attempts[0]
			a.Order = 0
			a.Version = Digest("old")
			a.Conclusion = "failure"
			f.Sources[0].Attempts = append(f.Sources[0].Attempts, a)
		}, "met"},
		{"ambiguous-order", func(f *ChecksFacts) {
			a := f.Sources[0].Attempts[0]
			a.Conclusion = "failure"
			f.Sources[0].Attempts = append(f.Sources[0].Attempts, a)
		}, "unknown"},
		{"both-missing-grain", func(f *ChecksFacts) { f.Mode = "both" }, "unknown"},
		{"both-grain-pending", func(f *ChecksFacts) {
			f.Mode = "both"
			s := f.Sources[0]
			s.Kind = "grain"
			s.Verdict = "cancelled"
			f.Sources = append(f.Sources, s)
		}, "pending"},
		{"both-success", func(f *ChecksFacts) {
			f.Mode = "both"
			s := f.Sources[0]
			s.Kind = "grain"
			s.Verdict = "success"
			f.Sources = append(f.Sources, s)
		}, "met"},
		{"conflicts-separate", func(f *ChecksFacts) {
			f.Preconditions = []string{"merge conflict", "base red", "stale overlap recheck"}
		}, "met"},
	}
	for _, conclusion := range []string{"success", "neutral", "skipped", "failure", "timed_out", "startup_failure", "cancelled", "action_required", "stale", "", "mystery", "errored"} {
		want := map[string]string{"success": "met", "neutral": "met", "skipped": "met", "failure": "unmet", "timed_out": "unmet", "startup_failure": "unmet", "cancelled": "pending", "action_required": "pending", "stale": "pending", "": "pending", "mystery": "unknown", "errored": "error"}[conclusion]
		tests = append(tests, struct {
			name string
			edit func(*ChecksFacts)
			want string
		}{"github-" + conclusion, func(f *ChecksFacts) { f.Sources[0].Attempts[0].Conclusion = conclusion }, want})
	}
	var outputs []Result
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := checkFixture(now)
			tc.edit(&f)
			r := EvaluateChecks(ChecksBinding(f, false), f, now, 1)
			if r.Status != tc.want {
				t.Fatalf("got %s: %s; want %s", r.Status, r.Summary, tc.want)
			}
			if r.Status == "met" && f.Subject.ChangeKind == "pr" && r.AgentText != nil && !strings.Contains(*r.AgentText, "No merge attempt is predicted") {
				t.Fatal("CI success implied an automatic merge proposal without complete guard evidence")
			}
			if len(r.Actions) != 0 || len(r.Work) != 0 {
				t.Fatal("effectful output")
			}
			if r.Status == "error" && r.Error == nil {
				t.Fatal("missing typed error")
			}
			outputs = append(outputs, r)
		})
	}
	for _, conclusion := range []string{"success", "skipped", "neutral", "cancelled", "errored", "failure", ""} {
		f := checkFixture(now)
		f.Mode = "grain"
		f.Sources[0].Kind = "grain"
		f.Sources[0].Verdict = conclusion
		want := map[string]string{"success": "met", "skipped": "pending", "neutral": "pending", "cancelled": "pending", "errored": "error", "failure": "unmet", "": "pending"}[conclusion]
		r := EvaluateChecks(ChecksBinding(f, false), f, now, 2)
		if r.Status != want {
			t.Errorf("Grain %q: %s want %s", conclusion, r.Status, want)
		}
		outputs = append(outputs, r)
	}
	for _, conclusion := range []string{"passed", "failed", "running", "not_run", "unknown", ""} {
		f := checkFixture(now)
		f.Mode = "provider"
		f.Subject.ChangeKind = "cl"
		f.Sources[0].Kind = "provider"
		f.Sources[0].Verdict = conclusion
		f.Sources[0].Attempts = nil
		want := map[string]string{"passed": "met", "failed": "unmet", "running": "pending", "not_run": "pending", "unknown": "unknown", "": "unknown"}[conclusion]
		r := EvaluateChecks(ChecksBinding(f, false), f, now, 3)
		if r.Status != want {
			t.Errorf("provider %q: %s want %s", conclusion, r.Status, want)
		}
		outputs = append(outputs, r)
	}
	if path := os.Getenv("GRAIN_CHECKS_RESULTS"); path != "" {
		b, _ := json.Marshal(outputs)
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestChecksPolicyUnionImportsAndVersions(t *testing.T) {
	now := time.Now().UTC()
	f := checkFixture(now)
	b := ChecksBinding(f, false)
	c := ChecksConfig{Mode: "effective", ExtraJobs: []string{"extra"}, Empty: "unknown", Semantics: "current_v1"}
	b.Config, _ = json.Marshal(c)
	if r := EvaluateChecks(b, f, now, 1); r.Status != "pending" {
		t.Fatal("extra job did not union inherited policy", r)
	}
	c.ExtraJobs = []string{}
	c.Mode = "grain"
	b.Config, _ = json.Marshal(c)
	if r := EvaluateChecks(b, f, now, 1); r.Status != "unknown" {
		t.Fatal("source replacement admitted")
	}
	b = ChecksBinding(f, true)
	f.LegacyEmptyKnown = true
	f.Sources[0].Attempts = nil
	if r := EvaluateChecks(b, f, now, 1); r.Status != "pending" {
		t.Fatal(r)
	}
	f.LegacyEmptySettled = true
	if r := EvaluateChecks(b, f, now, 1); r.Status != "met" {
		t.Fatal("legacy timer not preserved", r)
	}
	f.Sources[0].Required = []string{"inherited"}
	if r := EvaluateChecks(b, f, now, 1); r.Status == "met" {
		t.Fatal("empty timer waived required job")
	}
	b.Origin.Imported = false
	if r := EvaluateChecks(b, f, now, 1); r.Status != "unknown" {
		t.Fatal("unproven legacy import")
	}
	f = checkFixture(now)
	f.Mode = "none"
	f.Sources = nil
	f.ExplicitNone = true
	for _, legacy := range []bool{false, true} {
		r := EvaluateChecks(ChecksBinding(f, legacy), f, now, 1)
		if r.Status != "met" {
			t.Fatal(r)
		}
	}
	f.ExplicitNone = false
	if r := EvaluateChecks(ChecksBinding(f, false), f, now, 1); r.Status == "met" {
		t.Fatal("unauthorized no-CI passed")
	}
	f = checkFixture(now)
	a := EvaluateChecks(ChecksBinding(f, false), f, now, 1)
	f.PolicyVersion = Digest("new base definitions")
	r := EvaluateChecks(ChecksBinding(f, false), f, now, 1)
	if a.ContextDigest == r.ContextDigest {
		t.Fatal("policy change did not invalidate context")
	}
	f = checkFixture(now)
	f.Mode = "grain"
	f.Sources[0].Kind = "grain"
	f.Sources[0].Verdict = "failure"
	f.LegacySources = slices.Clone(f.Sources)
	f.LegacySources[0].Verdict = "success"
	if EvaluateChecks(ChecksBinding(f, false), f, now, 1).Status != "unmet" || EvaluateChecks(ChecksBinding(f, true), f, now, 1).Status != "met" {
		t.Fatal("legacy narrowing not separated")
	}
}

func TestChecksClosedConfiguration(t *testing.T) {
	for _, raw := range []string{
		`{}`, `{"mode":"effective","extra_jobs":[],"empty":"legacy_settled","semantics":"current_v1"}`,
		`{"mode":"effective","extra_jobs":["a","a"],"empty":"unknown","semantics":"current_v1"}`,
		`{"mode":"effective","extra_jobs":null,"empty":"unknown","semantics":"current_v1"}`,
		`{"mode":"effective","extra_jobs":[],"empty":"unknown","semantics":"current_v1","command":"merge"}`,
		`{"mode":"effective","mode":"none","extra_jobs":[],"empty":"unknown","semantics":"current_v1"}`,
	} {
		if _, err := Validate("checks", 1, []byte(raw)); err == nil {
			t.Errorf("admitted %s", raw)
		}
	}
	for _, mode := range []string{"effective", "github", "grain", "both", "provider", "none"} {
		raw, _ := json.Marshal(ChecksConfig{Mode: mode, ExtraJobs: []string{}, Empty: "unknown", Semantics: "current_v1"})
		if _, err := Validate("checks", 1, raw); err != nil {
			t.Fatal(err)
		}
	}
}
