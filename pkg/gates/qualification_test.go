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

func stageFixture(now time.Time) StageFacts {
	yes := true
	f := StageFacts{TaskID: "1", Head: strings.Repeat("a", 40), FlowRevision: Digest("flow"), PolicyVersion: Digest("policy"), AssignmentVersion: Digest("assignment"),
		Arm: "treatment", Engine: 7, Complete: true, Supported: true, ObservedAt: now, ValidUntil: now.Add(time.Minute), S4Required: &yes, S5Enabled: true,
		Checks: []StageCheck{{Stage: "S3", ID: "integrity", Severity: "blocking"}, {Stage: "S3.5", ID: "strict", Severity: "blocking"}}}
	for i, check := range append(append([]StageCheck{}, f.Checks...), StageCheck{Stage: "S4", ID: "golden:s4-orthogonal-verifier"}, StageCheck{Stage: "S5", ID: "qa:merge-triage"}) {
		f.Runs = append(f.Runs, StageRun{ID: string(rune('1' + i)), Stage: check.Stage, CheckID: check.ID, Head: f.Head, FlowRevision: f.FlowRevision, Arm: f.Arm, Engine: f.Engine, Verdict: "pass", TriageDecision: "auto_merge", Version: Digest(check)})
	}
	return f
}
func TestQAStageAuthenticPredicatesAndInertness(t *testing.T) {
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		change func(*StageFacts)
		want   []string
	}{
		{"complete", func(f *StageFacts) {}, []string{"met", "met", "met"}},
		{"head-changed", func(f *StageFacts) { f.Head = strings.Repeat("b", 40) }, []string{"unknown", "unknown", "unknown"}},
		{"flow-changed", func(f *StageFacts) { f.FlowRevision = Digest("new") }, []string{"unknown", "unknown", "unknown"}},
		{"engine-changed", func(f *StageFacts) { f.Engine++ }, []string{"unknown", "unknown", "unknown"}},
		{"arm-changed", func(f *StageFacts) { f.Arm = "other-treatment" }, []string{"unknown", "unknown", "unknown"}},
		{"experiment-changed", func(f *StageFacts) { f.Experiment = "new" }, []string{"unknown", "unknown", "unknown"}},
		{"missing-head", func(f *StageFacts) { f.Head = "" }, []string{"unknown", "unknown", "unknown"}},
		{"missing-assignment", func(f *StageFacts) { f.AssignmentVersion = "" }, []string{"unknown", "unknown", "unknown"}},
		{"unsupported-flow", func(f *StageFacts) { f.Supported = false }, []string{"unknown", "unknown", "unknown"}},
		{"incomplete", func(f *StageFacts) { f.Complete = false }, []string{"unknown", "unknown", "unknown"}},
		{"stale", func(f *StageFacts) { f.ValidUntil = now }, []string{"unknown", "unknown", "unknown"}},
		{"missing-check", func(f *StageFacts) { f.Runs = f.Runs[1:] }, []string{"unknown", "met", "met"}},
		{"s3-failed", func(f *StageFacts) { f.Runs[0].Verdict = "fail" }, []string{"unmet", "met", "met"}},
		{"s4-canceled", func(f *StageFacts) { f.Runs[2].Verdict = "canceled" }, []string{"met", "unmet", "met"}},
		{"s4-error", func(f *StageFacts) { f.Runs[2].Verdict = "error" }, []string{"met", "error", "met"}},
		{"s4-running", func(f *StageFacts) { f.Runs[2].Verdict = "running" }, []string{"met", "pending", "met"}},
		{"no-s4-tier", func(f *StageFacts) { f.S4Required = nil }, []string{"met", "unknown", "met"}},
		{"person-triage", func(f *StageFacts) { f.Runs[3].TriageDecision = "require_human" }, []string{"met", "met", "unmet"}},
		{"missing-triage", func(f *StageFacts) { f.Runs[3].TriageDecision = "" }, []string{"met", "met", "unknown"}},
		{"human-floor", func(f *StageFacts) { f.HumanFloors = []string{"trust boundary requires a person"} }, []string{"met", "met", "unmet"}},
		{"conditional-missing", func(f *StageFacts) {
			f.Checks = append(f.Checks, StageCheck{Stage: "custom", ID: "custom", Conditional: true})
		}, []string{"unknown", "met", "met"}},
		{"missing-record-version", func(f *StageFacts) { f.Runs[0].Version = "" }, []string{"unknown", "met", "met"}},
		{"shadow-cannot-reuse-treatment", func(f *StageFacts) { f.Shadow = true }, []string{"unknown", "unknown", "unknown"}},
		{"triage-shadow", func(f *StageFacts) {
			f.S5Shadow = true
			f.Runs[3].Shadow = true
			f.Runs[3].TriageDecision = "require_human"
		}, []string{"met", "met", "unmet"}},
		{"warn", func(f *StageFacts) { f.Runs[0].Verdict = "warn" }, []string{"met", "met", "met"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := stageFixture(now)
			tc.change(&f)
			before := Digest(f)
			c := QualificationCollection{QA: &f}
			for i, b := range c.Bindings("1")[2:] {
				r := EvaluateStage(b, f, now, 1)
				if r.Status != tc.want[i] {
					t.Errorf("%s=%s want %s (%+v)", b.ID, r.Status, tc.want[i], r.Blockers)
				}
				if len(r.Work) > 0 || len(r.Actions) > 0 || len(r.Waits) > 0 || !strings.HasPrefix(r.EvaluationID, "shadow-") {
					t.Fatal("operational output")
				}
				if !reflect.DeepEqual(r, EvaluateStage(b, f, now, 1)) {
					t.Fatal("not deterministic")
				}
			}
			if before != Digest(f) {
				t.Fatal("mutated source")
			}
		})
	}
	f := stageFixture(now)
	// Legacy loadRecordedS3Outcome retains any earlier failure on the same
	// exact head. A later pass on that head is not a repair on another SHA.
	old := f.Runs[0]
	old.ID = "old"
	old.Verdict = "fail"
	f.Runs = append([]StageRun{old}, f.Runs...)
	b := QualificationCollection{QA: &f}.Bindings("1")[2]
	if r := EvaluateStage(b, f, now, 1); r.Status != "unmet" || len(r.Evidence) < 4 {
		t.Fatalf("lost prior failure %+v", r)
	}
	b.Required = true
	b.Purpose = "guard"
	action := "change.merge"
	b.Action = &action
	if r := EvaluateStage(b, f, now, 1); r.Status != "unknown" {
		t.Fatal("enforcement activated")
	}
}
func releaseFixture(now time.Time) ReleaseFacts {
	yes := true
	seconds := int64(22 * 3600)
	f := ReleaseFacts{TaskID: "1", Repository: "acme/r", Candidate: "v3.1.0-rc.1", Version: "v3.1.0", Head: strings.Repeat("a", 40), PolicyVersion: Digest("policy"), SourceVersion: Digest("source"), Complete: true, ObservedAt: now, ValidUntil: now.Add(time.Minute),
		Provenance: &yes, RecordedSoakSeconds: &seconds, RequiredSoakSeconds: 22 * 3600, BreakingAllowed: &yes, PublicationReady: &yes}
	f.Suite = &ReleaseSuite{ID: "99", Head: f.Head, App: "grain", ExpectedApp: "grain", ExternalID: "candidate#2", ExpectedExternalID: "candidate#2", Status: "completed", Conclusion: "success", Version: Digest("verdict"), Complete: true}
	f.Person = &ReleasePerson{Identity: "alice", Kind: "human", Decision: "approve", Commit: f.Head, Version: f.Version, Verified: true, IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(15 * time.Minute)}
	f.Artifact = &ReleaseArtifact{Commit: f.Head, Version: f.Version, Digest: Digest("artifact"), SignatureVerified: &yes}
	return f
}
func TestReleaseQualificationTypedPredicates(t *testing.T) {
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, predicate, want string
		change                func(*ReleaseFacts)
	}{
		{"policy-veto", "promotion_policy", "unmet", func(f *ReleaseFacts) {
			f.PromotionPolicy = &ReleasePromotionPolicy{Veto: true, Complete: true, Version: Digest("policy")}
		}},
		{"policy-incomplete-veto", "promotion_policy", "unknown", func(f *ReleaseFacts) {
			f.PromotionPolicy = &ReleasePromotionPolicy{Veto: true, Version: Digest("policy")}
		}},
		{"occupied-version", "promotion_policy", "unmet", func(f *ReleaseFacts) { f.PromotionPolicy = &ReleasePromotionPolicy{VersionOccupied: true} }},
		{"policy-complete", "promotion_policy", "met", func(f *ReleaseFacts) {
			f.PromotionPolicy = &ReleasePromotionPolicy{Complete: true, Version: Digest("policy")}
		}},
		{"policy-unversioned", "promotion_policy", "unknown", func(f *ReleaseFacts) { f.PromotionPolicy = &ReleasePromotionPolicy{Complete: true} }},
		{"soak-expired", "soak", "unknown", func(f *ReleaseFacts) { f.ValidUntil = now }},
		{"suite", "suite", "met", func(f *ReleaseFacts) {}},
		{"wrong-RC", "suite", "unknown", func(f *ReleaseFacts) { f.Head = strings.Repeat("b", 40) }},
		{"foreign-issuer", "suite", "unknown", func(f *ReleaseFacts) { f.Suite.App = "foreign" }},
		{"wrong-attempt", "suite", "unknown", func(f *ReleaseFacts) { f.Suite.ExternalID = "other" }},
		{"incomplete-suite", "suite", "unknown", func(f *ReleaseFacts) { f.Suite.Complete = false }},
		{"incomplete-collection", "suite", "unknown", func(f *ReleaseFacts) { f.Complete = false }},
		{"failed-suite", "suite", "unmet", func(f *ReleaseFacts) { f.Suite.Conclusion = "failure" }},
		{"canceled-suite", "suite", "pending", func(f *ReleaseFacts) { f.Suite.Conclusion = "cancelled" }},
		{"infra-suite", "suite", "pending", func(f *ReleaseFacts) { f.Suite.Conclusion = "neutral" }},
		{"skipped-suite", "suite", "unknown", func(f *ReleaseFacts) { f.Suite.Conclusion = "skipped" }},
		{"pending-suite", "suite", "pending", func(f *ReleaseFacts) { f.Suite.Status = "in_progress" }},
		{"missing-suite", "suite", "unknown", func(f *ReleaseFacts) { f.Suite = nil }},
		{"provenance", "provenance", "met", func(f *ReleaseFacts) {}},
		{"wrong-provenance", "provenance", "unmet", func(f *ReleaseFacts) { v := false; f.Provenance = &v }},
		{"soak", "soak", "met", func(f *ReleaseFacts) {}},
		{"short-soak", "soak", "unmet", func(f *ReleaseFacts) { *f.RecordedSoakSeconds-- }},
		{"missing-soak", "soak", "unknown", func(f *ReleaseFacts) { f.RecordedSoakSeconds = nil }},
		{"missing-effective-soak-policy", "soak", "unknown", func(f *ReleaseFacts) { f.RequiredSoakSeconds = 0 }},
		{"qualification-era", "soak", "met", func(f *ReleaseFacts) { f.RecordedSoakSeconds = nil; f.QualificationBased = true }},
		{"person", "person", "met", func(f *ReleaseFacts) {}},
		{"agent", "person", "unmet", func(f *ReleaseFacts) { f.Person.Kind = "agent" }},
		{"unverified-person", "person", "unmet", func(f *ReleaseFacts) { f.Person.Verified = false }},
		{"anonymous-person", "person", "unmet", func(f *ReleaseFacts) { f.Person.Identity = "" }},
		{"expired-person", "person", "unmet", func(f *ReleaseFacts) { f.Person.ExpiresAt = now }},
		{"rejected-person", "person", "unmet", func(f *ReleaseFacts) { f.Person.Decision = "reject" }},
		{"wrong-person-head", "person", "unknown", func(f *ReleaseFacts) { f.Person.Commit = strings.Repeat("b", 40) }},
		{"breaking", "breaking_change", "met", func(f *ReleaseFacts) {}},
		{"breaking-denied", "breaking_change", "unmet", func(f *ReleaseFacts) { v := false; f.BreakingAllowed = &v }},
		{"signed", "signing_version", "met", func(f *ReleaseFacts) {}},
		{"unsigned", "signing_version", "unmet", func(f *ReleaseFacts) { v := false; f.Artifact.SignatureVerified = &v }},
		{"wrong-artifact", "signing_version", "unmet", func(f *ReleaseFacts) { f.Artifact.Commit = strings.Repeat("b", 40) }},
		{"wrong-version", "signing_version", "unmet", func(f *ReleaseFacts) { f.Artifact.Version = "v3.2.0" }},
		{"no-signature-proof", "signing_version", "unknown", func(f *ReleaseFacts) { f.Artifact.SignatureVerified = nil }},
		{"readiness", "publication_readiness", "met", func(f *ReleaseFacts) {}},
		{"missing-asset", "publication_readiness", "unmet", func(f *ReleaseFacts) { v := false; f.PublicationReady = &v }},
		{"unknown-assets", "publication_readiness", "unknown", func(f *ReleaseFacts) { f.PublicationReady = nil }},
		{"stale", "suite", "unknown", func(f *ReleaseFacts) { f.ValidUntil = now }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := releaseFixture(now)
			tc.change(&f)
			before := Digest(f)
			for _, b := range (QualificationCollection{Release: &f}).Bindings("1")[2:] {
				var cfg ReleaseQualificationConfig
				_ = json.Unmarshal(b.Config, &cfg)
				if cfg.Predicate != tc.predicate {
					continue
				}
				r := EvaluateRelease(b, f, now, 1)
				if r.Status != tc.want {
					t.Fatalf("%s want %s: %s", r.Status, tc.want, r.Summary)
				}
				if len(r.Actions) > 0 || len(r.Work) > 0 || len(r.Waits) > 0 {
					t.Fatal("operational output")
				}
				if tc.want != "met" && len(r.Blockers) == 0 {
					t.Fatal("hidden blocker")
				}
			}
			if before != Digest(f) {
				t.Fatal("mutated source")
			}
		})
	}
}
func TestQualificationClosedConfigAndHistoryPayloadBound(t *testing.T) {
	for _, raw := range []string{
		`{"stage":"s3","flow_revision":"f","assignment":"treatment","Stage":"s4"}`,
		`{"stage":"s3","stage":"s4","flow_revision":"f","assignment":"treatment"}`,
		`{"stage":"s3","flow_revision":null,"assignment":"treatment"}`,
		`{"stage":"s3","flow_revision":"f","assignment":"treatment","shell":"echo yes"}`,
	} {
		if _, err := Validate("qa_stage", 1, []byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	now := time.Now().UTC()
	f := stageFixture(now)
	c := QualificationCollection{QA: &f}
	bindings := c.Bindings("1")
	v := ObservationView{Bindings: bindings}
	for _, b := range bindings {
		r := EvaluateStage(b, f, now, 1)
		r.Evidence = make([]Evidence, 1024)
		v.Results = append(v.Results, EvaluationState{Revision: 1, Result: r})
	}
	BoundQualificationView(&v, now)
	if v.Results[2].Result.Status != "unknown" || len(v.Results[2].Result.Evidence) != 0 || v.Results[2].Result.Blockers[0].Code != "resource_limit" {
		t.Fatal("unbounded qualification envelope")
	}
}

func TestQualificationSubjectsCannotBorrowForeignEvidence(t *testing.T) {
	now := time.Now().UTC()
	qa := stageFixture(now)
	c := QualificationCollection{QA: &qa}
	b := c.Bindings("1")[2]
	b.Subject.TaskID = "foreign"
	if EvaluateStage(b, qa, now, 1).Status != "unknown" {
		t.Fatal("foreign QA evidence was accepted")
	}
	release := releaseFixture(now)
	c = QualificationCollection{Release: &release}
	b = c.Bindings("1")[2]
	b.Subject.Repository = "private/foreign"
	if EvaluateRelease(b, release, now, 1).Status != "unknown" {
		t.Fatal("foreign release evidence was accepted")
	}
}
