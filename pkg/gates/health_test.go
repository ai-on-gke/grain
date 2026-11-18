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
	"reflect"
	"strings"
	"testing"
	"time"
)

var healthNow = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func healthResult(t *testing.T, typ string, config any, h HealthFacts) Result {
	t.Helper()
	raw, _ := json.Marshal(config)
	if _, err := Validate(typ, 1, raw); err != nil {
		t.Fatal(err)
	}
	h.Version = Digest(h)
	h.Complete = true
	h.ObservedAt = healthNow
	h.ValidUntil = healthNow.Add(time.Minute)
	b := Binding{ID: "health", Type: typ, Version: 1, Purpose: "monitor", Subject: Subject{Kind: "repository", Repository: "acme/widgets", Ref: "main"}, Config: raw}
	f := Facts{TaskID: "repair", Applicable: true, Complete: true, ObservedAt: healthNow, ValidUntil: h.ValidUntil, Health: map[string]HealthFacts{"health": h}}
	before := Digest(f)
	r := Evaluate(b, f, healthNow, 1)
	if len(r.Work) != 0 || len(r.Actions) != 0 || before != Digest(f) || !reflect.DeepEqual(r, Evaluate(b, f, healthNow, 1)) {
		t.Fatal("impure or operational evaluator")
	}
	return r
}
func TestHealthClosedConfigurations(t *testing.T) {
	for _, tc := range []struct {
		typ, raw string
		valid    bool
	}{
		{"base_health", `{"semantics":"current_tip_v1","allow_nonreproduction":false}`, true},
		{"base_health", `{"semantics":"legacy_recorded_v1","allow_nonreproduction":true}`, true},
		{"base_health", `{"semantics":"current_tip_v1"}`, false},
		{"base_health", `{"semantics":"current_tip_v1","allow_nonreproduction":null}`, false},
		{"base_health", `{"semantics":"current_tip_v1","allow_nonreproduction":true,"allow_nonreproduction":false}`, false},
		{"base_health", `{"Semantics":"current_tip_v1","allow_nonreproduction":true}`, false},
		{"nightly_health", `{"workflow":"nightly","since":"2026-11-17T00:00:00Z","window_seconds":60,"semantics":"window_v1"}`, true},
		{"nightly_health", `{"workflow":"nightly","since":"yesterday","window_seconds":86400,"semantics":"window_v1"}`, false},
		{"nightly_health", `{"workflow":"nightly","since":"2026-11-17T00:00:00Z","window_seconds":59,"semantics":"window_v1"}`, false},
		{"pipeline_health", `{"repos":["acme/widgets","acme/widgets"],"semantics":"scoped_v1"}`, false},
		{"pipeline_health", `{"repos":[],"semantics":"legacy_quiet_v1"}`, false},
		{"publishing_health", `{"semantics":"positive_current_v1","expected_commit":"current_tip","callback":"mcp://evaluate"}`, false},
	} {
		_, err := Validate(tc.typ, 1, []byte(tc.raw))
		if (err == nil) != tc.valid {
			t.Errorf("%s %s: %v", tc.typ, tc.raw, err)
		}
	}
}
func TestBaseHealthProfiles(t *testing.T) {
	for _, tc := range []struct {
		name            string
		mutate          func(*BaseHealthFacts)
		legacy, current string
	}{
		{"green", func(*BaseHealthFacts) {}, "met", "met"},
		{"missing", func(b *BaseHealthFacts) { b.Present = false }, "unknown", "unknown"},
		{"old-tip", func(b *BaseHealthFacts) { b.Tip = "new" }, "met", "unknown"},
		{"absent-tip", func(b *BaseHealthFacts) { b.Tip = "" }, "met", "unknown"},
		{"incomplete-ci", func(b *BaseHealthFacts) { b.CurrentProof = false }, "met", "unknown"},
		{"inflight", func(b *BaseHealthFacts) { b.InFlight = true; b.Conclusion = "" }, "pending", "pending"},
		{"broken", func(b *BaseHealthFacts) { b.Broken = true; b.Conclusion = "failure" }, "unmet", "unmet"},
		{"cancelled", func(b *BaseHealthFacts) { b.Conclusion = "cancelled" }, "unknown", "unknown"},
		{"cleared-unproven", func(b *BaseHealthFacts) { b.Conclusion = "failure"; b.Confirmation = "cleared" }, "met", "unknown"},
		{"cleared-authentic", func(b *BaseHealthFacts) {
			b.Conclusion = "failure"
			b.Confirmation = "cleared"
			b.NonreproductionProof = true
		}, "met", "met"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := BaseHealthFacts{Present: true, Commit: "head", Tip: "head", RunID: "run", Conclusion: "success", CurrentProof: true}
			tc.mutate(&b)
			for i, sem := range []string{"legacy_recorded_v1", "current_tip_v1"} {
				r := healthResult(t, "base_health", BaseHealthConfig{Semantics: sem, AllowNonreproduction: true}, HealthFacts{Base: &b})
				if r.Status != []string{tc.legacy, tc.current}[i] {
					t.Fatalf("%s %s", sem, r.Status)
				}
			}
		})
	}
	b := BaseHealthFacts{Present: true, Commit: "head", Tip: "head", Conclusion: "failure", Confirmation: "cleared", CurrentProof: true, NonreproductionProof: true}
	if healthResult(t, "base_health", BaseHealthConfig{Semantics: "current_tip_v1", AllowNonreproduction: false}, HealthFacts{Base: &b}).Status == "met" {
		t.Fatal("non-reproduction disabled")
	}
}
func healthRun() WorkflowHealthRun {
	return WorkflowHealthRun{ID: 1, Attempt: 1, Repository: "acme/widgets", HeadRepository: "acme/widgets", Ref: "main", Commit: "head", Workflow: "nightly", Path: ".github/workflows/nightly.yml", Event: "schedule", Source: "github", Conclusion: "success", StartedAt: healthNow.Add(-2 * time.Hour), CompletedAt: healthNow.Add(-time.Hour), Complete: true, Jobs: []HealthJob{{ID: "11", Name: "test", RunID: 1, Attempt: 1, Workflow: "nightly", Ref: "main", Commit: "head", Conclusion: "success", Source: "github", App: "github-actions"}}}
}
func TestCurrentNightlySourceWindowAndJobPredicates(t *testing.T) {
	c := NightlyHealthConfig{Workflow: "nightly", Since: healthNow.Add(-48 * time.Hour).Format(time.RFC3339), WindowSeconds: 86400, Semantics: "window_v1"}
	for _, tc := range []struct {
		name   string
		mutate func(*WorkflowHealthRun)
		want   string
	}{
		{"green", func(*WorkflowHealthRun) {}, "met"},
		{"run-failed-with-green-jobs", func(r *WorkflowHealthRun) { r.Conclusion = "failure" }, "unmet"},
		{"run-cancelled-with-green-jobs", func(r *WorkflowHealthRun) { r.Conclusion = "cancelled" }, "pending"},
		{"run-skipped-with-green-jobs", func(r *WorkflowHealthRun) { r.Conclusion = "skipped" }, "pending"},
		{"cancelled-with-failed-job", func(r *WorkflowHealthRun) {
			r.Conclusion = "cancelled"
			r.Jobs[0].Conclusion = "failure"
		}, "unmet"},
		{"run-stale-with-green-jobs", func(r *WorkflowHealthRun) { r.Conclusion = "stale" }, "unknown"},
		{"run-conclusion-missing", func(r *WorkflowHealthRun) { r.Conclusion = "" }, "unknown"},
		{"old", func(r *WorkflowHealthRun) { r.CompletedAt = healthNow.Add(-25 * time.Hour) }, "unknown"},
		{"future", func(r *WorkflowHealthRun) { r.CompletedAt = healthNow.Add(time.Hour) }, "unknown"},
		{"wrong-workflow-case", func(r *WorkflowHealthRun) { r.Workflow = "Nightly" }, "unknown"},
		{"fork-main", func(r *WorkflowHealthRun) { r.HeadRepository = "fork/widgets" }, "unknown"},
		{"pr-main", func(r *WorkflowHealthRun) { r.Event = "pull_request" }, "unknown"},
		{"partial", func(r *WorkflowHealthRun) { r.Complete = false }, "unknown"},
		{"empty", func(r *WorkflowHealthRun) { r.Jobs = nil }, "unknown"},
		{"path-missing", func(r *WorkflowHealthRun) { r.Path = "" }, "unknown"},
		{"attempt-mismatch", func(r *WorkflowHealthRun) { r.Jobs[0].Attempt = 2 }, "unknown"},
		{"commit-mismatch", func(r *WorkflowHealthRun) { r.Jobs[0].Commit = "old" }, "unknown"},
		{"wrong-issuer", func(r *WorkflowHealthRun) { r.Jobs[0].App = "other" }, "unknown"},
		{"failed", func(r *WorkflowHealthRun) { r.Jobs[0].Conclusion = "timed_out" }, "unmet"},
		{"cancelled", func(r *WorkflowHealthRun) { r.Jobs[0].Conclusion = "cancelled" }, "pending"},
		{"no-verdict", func(r *WorkflowHealthRun) { r.Jobs[0].Conclusion = "" }, "pending"},
		{"all-skipped", func(r *WorkflowHealthRun) { r.Jobs[0].Conclusion = "skipped" }, "pending"},
		{"idle-only", func(r *WorkflowHealthRun) { r.Jobs[0].Name = "idle-gate" }, "pending"},
		{"neutral", func(r *WorkflowHealthRun) { r.Jobs[0].Conclusion = "neutral" }, "met"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := healthRun()
			tc.mutate(&run)
			r := healthResult(t, "nightly_health", c, HealthFacts{Runs: []WorkflowHealthRun{run}})
			if r.Status != tc.want {
				t.Fatalf("%s: %s", r.Status, r.Summary)
			}
		})
	}
	run := healthRun()
	r := healthResult(t, "nightly_health", c, HealthFacts{Runs: []WorkflowHealthRun{run}})
	if !r.ValidUntil.Equal(healthNow.Add(time.Minute)) {
		t.Fatal("window enlarged collection lease")
	}
	run.CompletedAt = healthNow.Add(-24*time.Hour + time.Second)
	run.StartedAt = run.CompletedAt.Add(-time.Hour)
	if !healthResult(t, "nightly_health", c, HealthFacts{Runs: []WorkflowHealthRun{run}}).ValidUntil.Equal(healthNow.Add(time.Second)) {
		t.Fatal("window expiry timer missing")
	}
	run.CompletedAt = healthNow.Add(-24 * time.Hour)
	if healthResult(t, "nightly_health", c, HealthFacts{Runs: []WorkflowHealthRun{run}}).Status == "met" {
		t.Fatal("exact expiry passed")
	}
	c.Semantics = "legacy_verified_push_v1"
	run = healthRun()
	if healthResult(t, "nightly_health", c, HealthFacts{Runs: []WorkflowHealthRun{run}}).Status == "met" {
		t.Fatal("unverified push")
	}
	run.VerifiedDefaultPush = true
	run.Event = "push"
	if healthResult(t, "nightly_health", c, HealthFacts{Runs: []WorkflowHealthRun{run}}).Status != "met" {
		t.Fatal("verified push failed")
	}
}
func TestLegacyNightlySelection(t *testing.T) {
	c := NightlyHealthConfig{Workflow: "NIGHTLY", Since: healthNow.Add(-48 * time.Hour).Format(time.RFC3339), WindowSeconds: 86400, Semantics: "legacy_500_v1"}
	job := func(id int64, name, conclusion string) HealthJob {
		return HealthJob{RunID: id, Workflow: "nightly", Name: name, Conclusion: conclusion, Ref: "refs/tags/v2.1.0-rc.1", At: healthNow.Add(-30 * time.Hour)}
	}
	h := HealthFacts{LegacyJobs: []HealthJob{job(3, "test", "cancelled"), job(2, "idle-gate", "success"), job(2, "test", "skipped"), job(1, "test", "neutral")}}
	r := healthResult(t, "nightly_health", c, h)
	if r.Status != "met" || !strings.Contains(r.Summary, "500") {
		t.Fatalf("%+v", r)
	}
	h.LegacyJobs[0].Conclusion = "failure"
	if healthResult(t, "nightly_health", c, h).Status != "unmet" {
		t.Fatal("newer failed verdict ignored")
	}
}
func TestPublicationRequiresPositiveTargetCommitArtifactReceipts(t *testing.T) {
	c := PublishingHealthConfig{Semantics: "positive_current_v1", ExpectedCommit: "current_tip"}
	for _, tc := range []struct {
		name string
		p    *PublishingHealthFacts
		want string
	}{
		{"no-row", nil, "unknown"},
		{"superseded", &PublishingHealthFacts{Present: true, Commit: "head", Tip: "head", Conclusion: "cancelled"}, "unknown"},
		{"build-success", &PublishingHealthFacts{Present: true, Commit: "head", Tip: "head", Conclusion: "success"}, "unknown"},
		{"outstanding", &PublishingHealthFacts{Present: true, Tip: "head", TargetsComplete: true, Targets: []string{"registry"}}, "pending"},
		{"failed", &PublishingHealthFacts{Present: true, Commit: "head", Tip: "head", TargetsComplete: true, Targets: []string{"registry"}, Conclusion: "failure"}, "unmet"},
		{"positive", &PublishingHealthFacts{Present: true, Tip: "head", TargetsComplete: true, Targets: []string{"registry"}, Receipts: []PublicationReceipt{{Target: "registry", Commit: "head", Artifact: "sha256:artifact", Version: "v1", Issuer: "publisher", Successful: true}}}, "met"},
		{"old-commit", &PublishingHealthFacts{Present: true, Tip: "head", TargetsComplete: true, Targets: []string{"registry"}, Receipts: []PublicationReceipt{{Target: "registry", Commit: "old", Artifact: "sha256:artifact", Version: "v1", Issuer: "publisher", Successful: true}}}, "pending"},
		{"missing-target", &PublishingHealthFacts{Present: true, Tip: "head", TargetsComplete: true, Targets: []string{"registry", "archive"}, Receipts: []PublicationReceipt{{Target: "registry", Commit: "head", Artifact: "sha256:artifact", Version: "v1", Issuer: "publisher", Successful: true}}}, "pending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if r := healthResult(t, "publishing_health", c, HealthFacts{Publishing: tc.p}); r.Status != tc.want {
				t.Fatalf("%s %s", r.Status, r.Summary)
			}
		})
	}
	c.Semantics = "legacy_no_finding_v1"
	if healthResult(t, "publishing_health", c, HealthFacts{}).Status != "met" {
		t.Fatal("legacy absent row changed")
	}
	if healthResult(t, "publishing_health", c, HealthFacts{Publishing: &PublishingHealthFacts{LegacyFinding: true}}).Status != "unmet" {
		t.Fatal("legacy threshold ignored")
	}
}
func TestPipelineScopeCompletenessAndLegacyDifferences(t *testing.T) {
	for _, tc := range []struct {
		name            string
		p               PipelineHealthFacts
		legacy, current string
	}{
		{"empty", PipelineHealthFacts{DeploymentRead: true, ScanComplete: true}, "met", "unknown"},
		{"partial", PipelineHealthFacts{DeploymentRead: true}, "unknown", "unknown"},
		{"denied-deployment", PipelineHealthFacts{ScanComplete: true}, "unknown", "unknown"},
		{"notifications", PipelineHealthFacts{DeploymentRead: true, OpenNotifications: 1}, "unmet", "unmet"},
		{"stalled", PipelineHealthFacts{DeploymentRead: true, StalledTasks: 1}, "unmet", "unmet"},
		{"queue", PipelineHealthFacts{DeploymentRead: true, BlockedQueue: 1}, "unmet", "unmet"},
		{"quiet", PipelineHealthFacts{DeploymentRead: true, ScanComplete: true, ExpectedBasesKnown: true, Bases: []BaseHealthFacts{{Present: true, Commit: "head", Tip: "head", Conclusion: "success", CurrentProof: true}}}, "met", "met"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for i, sem := range []string{"legacy_quiet_v1", "scoped_v1"} {
				r := healthResult(t, "pipeline_health", PipelineHealthConfig{Repos: []string{"acme/widgets"}, Semantics: sem}, HealthFacts{Pipeline: &tc.p})
				if r.Status != []string{tc.legacy, tc.current}[i] {
					t.Fatalf("%s %s: %s", sem, r.Status, r.Summary)
				}
			}
		})
	}
}
