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
	"testing"
	"time"
)

func outcomeBinding(typ string, config any) Binding {
	raw, _ := json.Marshal(config)
	return Binding{ID: "selected", Type: typ, Version: 1, Purpose: "monitor", Subject: Subject{Kind: "task", TaskID: "parent"}, Config: raw}
}
func outcomeFacts(now time.Time) Facts {
	return Facts{TaskID: "parent", Applicable: true, Complete: true, Settled: true, SourceVersion: "revision", ObservedAt: now, ValidUntil: now.Add(time.Minute)}
}
func TestOutcomeProfilesAreClosed(t *testing.T) {
	cases := []struct {
		typ, raw string
		valid    bool
	}{
		{"integration", `{"kind":"pr_merged","semantics":"positive_v1"}`, true},
		{"integration", `{"kind":"cl_submitted","semantics":"positive_v1"}`, true},
		{"integration", `{"kind":"pr_merged","semantics":"legacy"}`, false},
		{"children", `{"tasks":["1"],"predicate":"success"}`, true},
		{"children", `{"tasks":[],"predicate":"closed"}`, false},
		{"children", `{"tasks":["1","1"],"predicate":"merged"}`, false},
		{"children", `{"tasks":["1"],"predicate":"closed","callback":"url"}`, false},
		{"children", `{"tasks":["1"],"tasks":["2"],"predicate":"success"}`, false},
	}
	for _, c := range cases {
		_, err := Validate(c.typ, 1, []byte(c.raw))
		if (err == nil) != c.valid {
			t.Errorf("%s: %v", c.raw, err)
		}
	}
	for _, product := range []string{"report", "change", "nothing_needed", "review", "any"} {
		for _, semantics := range []string{"current_v1", "legacy_finish_v1"} {
			raw, _ := json.Marshal(DeliveryConfig{Product: product, Semantics: semantics})
			if _, err := Validate("delivery", 1, raw); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, mode := range []string{"explicit_receipt", "legacy_report", "legacy_child_answer", "legacy_nothing_needed_ack"} {
		raw, _ := json.Marshal(AcknowledgmentConfig{Mode: mode})
		if _, err := Validate("acknowledgment", 1, raw); err != nil {
			t.Fatal(err)
		}
	}
}
func TestDeliveryVariantsAndCompatibilityAreInert(t *testing.T) {
	now := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	e := &Evidence{Kind: "integration", ID: "pr/1", Version: "v1"}
	for _, product := range []string{"change", "review", "nothing_needed", "report"} {
		for _, selected := range []string{"change", "review", "nothing_needed", "report", "any"} {
			f := outcomeFacts(now)
			f.Product = product
			f.ProductEvidence = e
			want := "unmet"
			if selected == product || selected == "any" {
				want = "met"
			}
			r := Evaluate(outcomeBinding("delivery", DeliveryConfig{Product: selected, Semantics: "current_v1"}), f, now, 1)
			if r.Status != want {
				t.Errorf("product=%s selected=%s: %s", product, selected, r.Status)
			}
			if len(r.Actions) != 0 || len(r.Work) != 0 || len(r.Waits) != 0 {
				t.Fatal("delivery produced an effect")
			}
			if product == "change" && selected != "report" {
				f.Complete = false
				if got := Evaluate(outcomeBinding("delivery", DeliveryConfig{Product: selected, Semantics: "current_v1"}), f, now, 1).Status; got != want {
					t.Fatalf("unrelated report gap changed delivered artifact: %s", got)
				}
				f.Complete = true
			}
			f.Blocked = true
			if got := Evaluate(outcomeBinding("delivery", DeliveryConfig{Product: selected, Semantics: "legacy_finish_v1"}), f, now, 1).Status; got != "pending" {
				t.Fatalf("blocked precedence=%s", got)
			}
		}
	}
	f := outcomeFacts(now)
	f.Product = "report"
	f.Report = e
	f.ProductEvidence = e
	child := outcomeBinding("acknowledgment", AcknowledgmentConfig{Mode: "legacy_child_answer"})
	if Evaluate(child, f, now, 1).Status != "unknown" {
		t.Fatal("child answer accepted without filing provenance")
	}
	legacyReport := outcomeBinding("acknowledgment", AcknowledgmentConfig{Mode: "legacy_report"})
	if Evaluate(legacyReport, f, now, 1).Status != "pending" {
		t.Fatal("report delivery was mistaken for acknowledgment")
	}
	f.Receipt = e
	if Evaluate(legacyReport, f, now, 1).Status != "met" {
		t.Fatal("legacy report receipt missing")
	}
	f.Receipt = nil
	f.ChildAnswer = true
	if Evaluate(child, f, now, 1).Status != "met" {
		t.Fatal("provenance-backed child answer missing")
	}
	f.Product = "review"
	if Evaluate(child, f, now, 1).Status != "unknown" {
		t.Fatal("ordinary review became a second acknowledgment")
	}
	f.Product = "nothing_needed"
	f.NothingAck = true
	ack := outcomeBinding("acknowledgment", AcknowledgmentConfig{Mode: "legacy_nothing_needed_ack"})
	if r := Evaluate(ack, f, now, 1); r.Status != "pending" || len(r.Evidence) != 0 {
		t.Fatalf("no-work acknowledgment policy lost or carried report evidence: %+v", r)
	}
	f.Receipt = e
	if r := Evaluate(ack, f, now, 1); r.Status != "pending" || len(r.Evidence) != 0 {
		t.Fatal("report receipt was reused for the no-work product")
	}
	explicit := outcomeBinding("acknowledgment", AcknowledgmentConfig{Mode: "explicit_receipt"})
	if r := Evaluate(explicit, f, now, 1); r.Status != "unknown" || len(r.Evidence) != 0 || len(r.Waits) != 0 {
		t.Fatalf("no-work product satisfied explicit report receipt: %+v", r)
	}
	if r := Evaluate(legacyReport, f, now, 1); r.Status != "unknown" || len(r.Evidence) != 0 {
		t.Fatalf("no-work product satisfied legacy report receipt: %+v", r)
	}
	f.NoWorkReceipt = e
	if r := Evaluate(ack, f, now, 1); r.Status != "met" || len(r.Evidence) != 1 {
		t.Fatalf("eligible no-work receipt missing or polluted: %+v", r)
	}
	f.Product = "report"
	f.ProductUnknown = "No-work settlement lacks current authenticated finish provenance."
	for _, binding := range []Binding{explicit, legacyReport, child, ack} {
		if r := Evaluate(binding, f, now, 1); r.Status != "unknown" || len(r.Evidence) != 0 || len(r.Waits) != 0 {
			t.Fatalf("unknown product provenance bypassed acknowledgment %s: %+v", binding.Config, r)
		}
	}
}
func TestIntegrationRequiresEveryFreshExactResource(t *testing.T) {
	now := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	head := "exact-snapshot/3"
	resource := ResourceFact{Evidence: Evidence{ID: "cl/1=>cl/2 (external)", Version: "pinned-policy-and-receipt", Head: &head}, State: "submitted", Complete: true, ObservedAt: now, ValidUntil: now.Add(time.Minute)}
	b := outcomeBinding("integration", IntegrationConfig{Kind: "cl_submitted", Semantics: "positive_v1"})
	for _, tc := range []struct {
		name   string
		change func(*Facts)
		want   string
	}{
		{"all", func(f *Facts) {}, "met"},
		{"second-open", func(f *Facts) { f.Resources["selected"][1].State = "open" }, "unmet"},
		{"second-submitting", func(f *Facts) { f.Resources["selected"][1].State = "submitting" }, "pending"},
		{"second-missing", func(f *Facts) { f.Resources["selected"][1].Complete = false }, "unknown"},
		{"unversioned", func(f *Facts) { f.Resources["selected"][1].Evidence.Head = nil }, "unknown"},
		{"empty-head", func(f *Facts) { empty := ""; f.Resources["selected"][1].Evidence.Head = &empty }, "unknown"},
		{"expired", func(f *Facts) { f.Resources["selected"][1].ValidUntil = now }, "unknown"},
		{"empty", func(f *Facts) { f.Resources["selected"] = nil }, "unknown"},
		{"report-incomplete-independent", func(f *Facts) { f.Complete = false }, "met"},
		{"standing-independent", func(f *Facts) { f.Applicable = false }, "met"},
		{"future", func(f *Facts) { f.ObservedAt = now.Add(time.Second) }, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := outcomeFacts(now)
			f.Resources = map[string][]ResourceFact{"selected": {resource, resource}}
			tc.change(&f)
			before := Digest(f)
			r := Evaluate(b, f, now, 3)
			if r.Status != tc.want {
				t.Fatalf("%s: %s", r.Status, r.Summary)
			}
			if before != Digest(f) || len(r.Actions) > 0 || len(r.Work) > 0 || len(r.Waits) > 0 {
				t.Fatal("integration was not pure and inert")
			}
			if !reflect.DeepEqual(r, Evaluate(b, f, now, 3)) {
				t.Fatal("replay changed prediction")
			}
		})
	}
}
func TestChildSuccessClosedAndMergedRemainDistinct(t *testing.T) {
	now := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name                    string
		child                   ChildFact
		success, closed, merged string
	}{
		{"open", ChildFact{Known: true}, "pending", "pending", "pending"},
		{"failed-close", ChildFact{Known: true, Closed: true, Failed: true, SuccessKnown: true, MergedKnown: true}, "unmet", "met", "unmet"},
		{"closed-unmerged", ChildFact{Known: true, Closed: true, SuccessKnown: true, MergedKnown: true}, "unmet", "met", "unmet"},
		{"unknown-legacy-close", ChildFact{Known: true, Closed: true}, "unknown", "met", "unknown"},
		{"merged", ChildFact{Known: true, Closed: true, Success: true, Merged: true, SuccessKnown: true, MergedKnown: true}, "met", "met", "met"},
		{"report-only-answer", ChildFact{Known: true, Closed: true, Success: true, SuccessKnown: true, MergedKnown: true}, "met", "met", "unmet"},
		{"inaccessible", ChildFact{}, "unknown", "unknown", "unknown"},
	} {
		for i, predicate := range []string{"success", "closed", "merged"} {
			f := outcomeFacts(now)
			f.Children = map[string]ChildFact{"chosen": tc.child}
			b := outcomeBinding("children", ChildrenConfig{Tasks: []string{"chosen"}, Predicate: predicate})
			r := Evaluate(b, f, now, 1)
			if r.Status != []string{tc.success, tc.closed, tc.merged}[i] {
				t.Errorf("%s/%s=%s", tc.name, predicate, r.Status)
			}
			f.Cycle = true
			if Evaluate(b, f, now, 1).Status != "unknown" {
				t.Fatal("cycle accepted")
			}
		}
	}
}
