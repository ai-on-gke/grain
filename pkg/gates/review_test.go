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

func reviewFixture(category string) Facts {
	now := time.Date(2026, 11, 17, 1, 0, 0, 0, time.UTC)
	return Facts{TaskID: "1", SourceVersion: "source-v1", ObservedAt: now, ValidUntil: now.Add(time.Minute),
		Change: &ChangeFacts{Subject: Subject{Kind: "change", Authority: "github.com", Repository: "owner/repo", ChangeKind: "pr", ID: "42"},
			Head: "head-1", PolicyVersion: "policy-1", AuthorizationVersion: "roles-1", Complete: true,
			Reviews: map[string]ReviewFacts{"scope": {AssignedReviewID: "review-task", Category: category, AssignmentVersion: "assignment-1", AssignmentPresent: true, AssignmentReadable: true,
				CollectionsComplete: true, DiffComplete: true, BaseOwned: true, FixesComplete: true, FindingsDigest: "findings-1",
				Disposition: &ReviewDisposition{ReviewID: "review-task", Version: "review-1", AssignmentVersion: "assignment-1", Head: "head-1", PolicyVersion: "policy-1", AuthorizationVersion: "roles-1",
					Reviewer: "reviewer", Authenticated: true, Eligible: true, Clean: true},
				LegacyStatus: "met", LegacyDetail: "closed review task"}},
			Approvals: map[string]ApprovalFacts{"alice-scope": {EligibleIdentityKnown: true, Receipt: &ApprovalReceipt{
				Version: "receipt-1", By: "alice", RenderedHead: "head-1", Head: "head-1", PolicyVersion: "policy-1", AuthorizationVersion: "roles-1",
				RequirementID: "alice-scope", Human: true, Authenticated: true, Eligible: true}}}}}
}

func TestReviewApprovalRejectFutureCollections(t *testing.T) {
	f := reviewFixture("ordinary")
	now := f.ObservedAt.Add(-time.Second)
	for _, b := range ChangeBindings(f.TaskID, *f.Change) {
		r := Evaluate(b, f, now, 1)
		if r.Status != "unknown" || len(r.Actions) != 0 || len(r.Work) != 0 {
			t.Fatalf("future %s observation passed: %+v", b.Type, r)
		}
	}
}

func TestReviewDispositionPredicates(t *testing.T) {
	for _, category := range []string{"ordinary", "security", "path", "takeover"} {
		for _, tc := range []struct {
			name, want string
			change     func(*ReviewFacts, *ChangeFacts, *Facts)
		}{
			{"exact-authenticated", "met", func(*ReviewFacts, *ChangeFacts, *Facts) {}},
			{"active-reviewer", "pending", func(r *ReviewFacts, _ *ChangeFacts, _ *Facts) { r.Running = true }},
			{"failed-review", "unmet", func(r *ReviewFacts, _ *ChangeFacts, _ *Facts) { r.Failed = true }},
			{"closed-empty-handed-is-not-clean", "unknown", func(r *ReviewFacts, _ *ChangeFacts, _ *Facts) { r.Disposition = nil }},
			{"missing-assignment", "unknown", func(r *ReviewFacts, _ *ChangeFacts, _ *Facts) { r.AssignmentPresent = false }},
			{"denied-assignment", "unknown", func(r *ReviewFacts, _ *ChangeFacts, _ *Facts) { r.AssignmentReadable = false }},
			{"unattributed-comment", "unknown", func(r *ReviewFacts, _ *ChangeFacts, _ *Facts) { r.Disposition.Authenticated = false }},
			{"forged-reviewer", "unknown", func(r *ReviewFacts, _ *ChangeFacts, _ *Facts) { r.Disposition.Reviewer = "" }},
			{"role-revoked", "unknown", func(r *ReviewFacts, _ *ChangeFacts, _ *Facts) { r.Disposition.Eligible = false }},
			{"roles-changed", "unknown", func(_ *ReviewFacts, c *ChangeFacts, _ *Facts) { c.AuthorizationVersion = "roles-2" }},
			{"wrong-assigned-review", "unknown", func(r *ReviewFacts, _ *ChangeFacts, _ *Facts) { r.Disposition.ReviewID = "another-review" }},
			{"new-assignment", "unknown", func(r *ReviewFacts, _ *ChangeFacts, _ *Facts) { r.AssignmentVersion = "assignment-2" }},
			{"new-head", "unknown", func(_ *ReviewFacts, c *ChangeFacts, _ *Facts) { c.Head = "head-2" }},
			{"path-policy-changed", "unknown", func(_ *ReviewFacts, c *ChangeFacts, _ *Facts) { c.PolicyVersion = "policy-2" }},
			{"incomplete-findings", "unknown", func(r *ReviewFacts, _ *ChangeFacts, _ *Facts) { r.CollectionsComplete = false }},
			{"truncated-diff", "unknown", func(r *ReviewFacts, _ *ChangeFacts, _ *Facts) { r.DiffComplete = false }},
			{"head-owned-rules", "unknown", func(r *ReviewFacts, _ *ChangeFacts, _ *Facts) { r.BaseOwned = false }},
			{"incomplete-fixes", "unknown", func(r *ReviewFacts, _ *ChangeFacts, _ *Facts) { r.FixesComplete = false }},
			{"deferred-fix", "unmet", func(r *ReviewFacts, _ *ChangeFacts, _ *Facts) { r.FixesPending = 1 }},
			{"closed-unmerged-fix", "unmet", func(r *ReviewFacts, _ *ChangeFacts, _ *Facts) { r.FixesFailed = 1 }},
			{"findings", "unmet", func(r *ReviewFacts, _ *ChangeFacts, _ *Facts) { r.Findings = 1 }},
			{"questions", "unmet", func(r *ReviewFacts, _ *ChangeFacts, _ *Facts) { r.Questions = 1 }},
			{"delivered-without-disposition", "unmet", func(r *ReviewFacts, _ *ChangeFacts, _ *Facts) { r.Disposition.Clean = false }},
			{"stale", "unknown", func(_ *ReviewFacts, _ *ChangeFacts, f *Facts) { f.ValidUntil = f.ObservedAt }},
			{"incomplete-policy-union", "unknown", func(_ *ReviewFacts, c *ChangeFacts, _ *Facts) { c.Complete = false }},
			{"carry-forward", "met", func(r *ReviewFacts, c *ChangeFacts, _ *Facts) {
				c.Head = "head-2"
				r.Disposition.CarryForwardHead = c.Head
				r.Disposition.CarryForwardPolicy = c.PolicyVersion
				r.Disposition.CarryForwardComplete = true
				r.Disposition.CarryForwardBaseOwned = true
			}},
			{"unproved-carried-stamp", "unknown", func(r *ReviewFacts, c *ChangeFacts, _ *Facts) {
				c.Head = "head-2"
				r.Disposition.CarryForwardHead = c.Head
			}},
		} {
			t.Run(category+"/"+tc.name, func(t *testing.T) {
				f := reviewFixture(category)
				scope := f.Change.Reviews["scope"]
				tc.change(&scope, f.Change, &f)
				f.Change.Reviews["scope"] = scope
				b := ChangeBindings("1", *f.Change)[0]
				before := Digest(f)
				r := Evaluate(b, f, f.ObservedAt, 1)
				if r.Status != tc.want {
					t.Fatalf("got %s: %s; want %s", r.Status, r.Summary, tc.want)
				}
				if len(r.Work) != 0 || len(r.Actions) != 0 || len(r.Waits) != 0 {
					t.Fatal("shadow proposed operational effects")
				}
				if before != Digest(f) || !reflect.DeepEqual(r, Evaluate(b, f, f.ObservedAt, 1)) {
					t.Fatal("evaluation mutates facts or is nondeterministic")
				}
			})
		}
	}
}

func TestExactScopeFindingsAcceptance(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*ReviewFacts, *ChangeFacts)
	}{
		{"human-acceptance", "met", func(*ReviewFacts, *ChangeFacts) {}},
		{"new-findings", "unmet", func(r *ReviewFacts, _ *ChangeFacts) { r.FindingsDigest = "new-findings" }},
		{"wrong-category", "unmet", func(r *ReviewFacts, _ *ChangeFacts) { r.Acceptance.Category = "ordinary" }},
		{"wrong-requirement", "unmet", func(r *ReviewFacts, _ *ChangeFacts) { r.Acceptance.RequirementID = "other-scope" }},
		{"reused-counter-other-review", "unmet", func(r *ReviewFacts, _ *ChangeFacts) { r.Acceptance.ReviewID = "other-review" }},
		{"new-assignment-same-review", "unmet", func(r *ReviewFacts, _ *ChangeFacts) { r.Acceptance.AssignmentVersion = "old-assignment" }},
		{"new-review", "unmet", func(r *ReviewFacts, _ *ChangeFacts) { r.Disposition.Version = "review-2" }},
		{"wrong-head", "unmet", func(r *ReviewFacts, _ *ChangeFacts) { r.Acceptance.Head = "old-head" }},
		{"old-policy", "unmet", func(r *ReviewFacts, _ *ChangeFacts) { r.Acceptance.PolicyVersion = "old-policy" }},
		{"revoked-role", "unmet", func(r *ReviewFacts, _ *ChangeFacts) { r.Acceptance.AuthorizationVersion = "old-roles" }},
		{"ineligible", "unmet", func(r *ReviewFacts, _ *ChangeFacts) { r.Acceptance.Eligible = false }},
		{"agent", "unmet", func(r *ReviewFacts, _ *ChangeFacts) { r.Acceptance.Human = false }},
		{"unattributed-external-comment", "unmet", func(r *ReviewFacts, _ *ChangeFacts) { r.Acceptance.Authenticated = false }},
		{"self-clear", "unmet", func(r *ReviewFacts, _ *ChangeFacts) { r.Acceptance.By = "reviewer" }},
		{"missing-reason", "unmet", func(r *ReviewFacts, _ *ChangeFacts) { r.Acceptance.Reason = "" }},
		{"acceptance-is-not-fix-integration", "unmet", func(r *ReviewFacts, _ *ChangeFacts) { r.FixesPending = 1 }},
		{"clean-subsequent-review", "met", func(r *ReviewFacts, _ *ChangeFacts) {
			r.Findings = 0
			r.Questions = 0
			r.Disposition.Version = "review-2"
			r.Acceptance = nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := reviewFixture("security")
			r := f.Change.Reviews["scope"]
			r.Findings = 1
			r.Questions = 1
			r.Acceptance = &FindingsDisposition{RequirementID: "scope", Category: "security", ReviewID: "review-task", AssignmentVersion: "assignment-1", Version: "acceptance-1", ReviewVersion: "review-1", Head: "head-1", PolicyVersion: "policy-1", AuthorizationVersion: "roles-1",
				FindingsDigest: "findings-1", By: "alice", Reason: "Accept this scoped risk", Human: true, Authenticated: true, Eligible: true}
			tc.change(&r, f.Change)
			f.Change.Reviews["scope"] = r
			got := Evaluate(ChangeBindings("1", *f.Change)[0], f, f.ObservedAt, 1)
			if got.Status != tc.want {
				t.Fatalf("%s: %s; want %s", got.Status, got.Summary, tc.want)
			}
		})
	}
}

func TestPartialApprovalAndRevocation(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*ChangeFacts)
	}{
		{"exact-rendered-head", "met", func(*ChangeFacts) {}},
		{"partial-eligibility", "pending", func(c *ChangeFacts) { c.Approvals["bob-scope"] = ApprovalFacts{EligibleIdentityKnown: true} }},
		{"missing-assignment", "unknown", func(c *ChangeFacts) { delete(c.Approvals, "alice-scope") }},
		{"missing-identity-read", "unknown", func(c *ChangeFacts) {
			a := c.Approvals["alice-scope"]
			a.EligibleIdentityKnown = false
			c.Approvals["alice-scope"] = a
		}},
		{"stale-rendered-head", "pending", func(c *ChangeFacts) { c.Approvals["alice-scope"].Receipt.RenderedHead = "old-head" }},
		{"new-head", "pending", func(c *ChangeFacts) { c.Head = "head-2" }},
		{"new-policy", "pending", func(c *ChangeFacts) { c.PolicyVersion = "policy-2" }},
		{"role-revocation", "pending", func(c *ChangeFacts) { c.Approvals["alice-scope"].Receipt.Revoked = true }},
		{"role-version-changed", "pending", func(c *ChangeFacts) { c.AuthorizationVersion = "roles-2" }},
		{"self-approved-agent", "unknown", func(c *ChangeFacts) { c.Approvals["alice-scope"].Receipt.Human = false }},
		{"forged-comment", "unknown", func(c *ChangeFacts) { c.Approvals["alice-scope"].Receipt.Authenticated = false }},
		{"wrong-requirement", "unknown", func(c *ChangeFacts) { c.Approvals["alice-scope"].Receipt.RequirementID = "other-scope" }},
		{"auto-merge-intent-is-not-receipt", "pending", func(c *ChangeFacts) { a := c.Approvals["alice-scope"]; a.Receipt = nil; c.Approvals["alice-scope"] = a }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := reviewFixture("ordinary")
			tc.change(f.Change)
			ids := []string{"alice-scope"}
			if tc.name == "partial-eligibility" {
				ids = append(ids, "bob-scope")
			}
			raw, _ := json.Marshal(ApprovalConfig{ids, "exact_head_v1"})
			b := Binding{ID: "approval", Type: "approval", Version: 1, Subject: f.Change.Subject, Config: raw}
			got := Evaluate(b, f, f.ObservedAt, 1)
			if got.Status != tc.want {
				t.Fatalf("%s: %s; want %s", got.Status, got.Summary, tc.want)
			}
			if tc.name == "partial-eligibility" && !strings.Contains(got.Summary, "passed [alice-scope]; pending [bob-scope]") {
				t.Fatal(got.Summary)
			}
			// Submit is independent of every review/acceptance/receipt predicate.
			if f.Change.Reviews["scope"].Acceptance != nil {
				t.Fatal("approval accepted findings")
			}
		})
	}
}

func TestReviewApprovalConfigDenials(t *testing.T) {
	for _, tc := range []struct{ typ, raw string }{
		{"review", `{"category":"qa","requirement_id":"r","semantics":"current_head_v1"}`},
		{"review", `{"category":"ordinary","requirement_id":"","semantics":"current_head_v1"}`},
		{"review", `{"category":"ordinary","requirement_id":"r","semantics":"current_head_v1","Category":"security"}`},
		{"review", `{"category":"ordinary","category":"path","requirement_id":"r","semantics":"current_head_v1"}`},
		{"approval", `{"requirement_ids":[],"semantics":"exact_head_v1"}`},
		{"approval", `{"requirement_ids":["a","a"],"semantics":"exact_head_v1"}`},
		{"approval", `{"requirement_ids":["a"],"semantics":"legacy_submit"}`},
		{"approval", `{"requirement_ids":["a"],"semantics":"exact_head_v1","callback":"mcp"}`},
	} {
		if _, err := Validate(tc.typ, 1, []byte(tc.raw)); err == nil {
			t.Errorf("admitted %s", tc.raw)
		}
	}
}

func TestLegacyReviewIsOnlyAnImportedMonitor(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*Binding, *Facts)
		want  string
	}{
		{"imported-monitor", func(*Binding, *Facts) {}, "met"},
		{"not-imported", func(b *Binding, _ *Facts) { b.Origin.Imported = false }, "unknown"},
		{"required", func(b *Binding, _ *Facts) { b.Required = true }, "unknown"},
		{"guard", func(b *Binding, _ *Facts) { b.Purpose = "guard"; action := "change.merge"; b.Action = &action }, "unknown"},
		{"unreadable", func(_ *Binding, f *Facts) {
			r := f.Change.Reviews["scope"]
			r.AssignmentReadable = false
			f.Change.Reviews["scope"] = r
		}, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := reviewFixture("ordinary")
			b := ChangeBindings("1", *f.Change)[1]
			tc.alter(&b, &f)
			r := Evaluate(b, f, f.ObservedAt, 1)
			if r.Status != tc.want {
				t.Fatalf("%s: %s", r.Status, r.Summary)
			}
			if r.Status == "met" {
				for _, d := range r.Dependencies {
					if !d.Complete {
						t.Fatal("legacy comparison declared an irrelevant incomplete current-head dependency")
					}
				}
			}
			if len(r.Work) > 0 || len(r.Actions) > 0 || len(r.Waits) > 0 {
				t.Fatal("legacy comparison produced operational work")
			}
		})
	}
}
