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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRegistryClosedProfilesAndSchemaPins(t *testing.T) {
	for _, tc := range []struct {
		typ   string
		v     uint32
		raw   string
		valid bool
	}{
		{"delivery", 1, `{"product":"report","semantics":"current_v1"}`, true},
		{"acknowledgment", 1, `{"mode":"explicit_receipt"}`, true},
		{"remote", 1, `{}`, false}, {"delivery", 2, `{}`, false}, {"delivery", 1, `{"product":"any","semantics":"current_v1"}`, true},
		{"acknowledgment", 1, `{"mode":"explicit_receipt","mode":"explicit_receipt"}`, false},
		{"acknowledgment", 1, `{"mode":"explicit_receipt","callback":"secret"}`, false}, {"acknowledgment", 1, `{"mode":"explicit_receipt"} {}`, false},
		{"delivery", 1, `null`, false}, {"acknowledgment", 1, `{"mode":null}`, false},
		{"acknowledgment", 1, `{"Mode":"explicit_receipt"}`, false},
		{"acknowledgment", 1, `{"mode":"legacy_report","MODE":"explicit_receipt"}`, false},
		{"delivery", 1, `{"Product":"report","semantics":"current_v1"}`, false},
		{"delivery", 1, `{"product":"change","PRODUCT":"report","semantics":"current_v1"}`, false},
	} {
		_, err := Validate(tc.typ, tc.v, []byte(tc.raw))
		if (err == nil) != tc.valid {
			t.Errorf("%+v: %v", tc, err)
		}
	}
	var canonical struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	data, err := os.ReadFile("../../design/spec/gates/contracts.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &canonical); err != nil {
		t.Fatal(err)
	}
	var attachment struct {
		OneOf []struct {
			Properties struct {
				Type struct{ Const string } `json:"type"`
			} `json:"properties"`
		} `json:"oneOf"`
	}
	if err := json.Unmarshal(canonical.Defs["attachment"], &attachment); err != nil {
		t.Fatal(err)
	}
	registered := map[string]bool{}
	for _, d := range Discover() {
		if registered[d.Type] {
			t.Fatalf("duplicate gate type %s", d.Type)
		}
		registered[d.Type] = true
		if d.SchemaDigest != Digest(canonical.Defs[d.Type+"_config"]) {
			t.Errorf("schema drift: %s", d.Type)
		}
	}
	if len(registered) != len(attachment.OneOf) {
		t.Fatalf("registry/catalogue count mismatch: %d/%d", len(registered), len(attachment.OneOf))
	}
	for _, branch := range attachment.OneOf {
		if !registered[branch.Properties.Type.Const] {
			t.Fatalf("missing canonical gate %s", branch.Properties.Type.Const)
		}
	}
	descriptors := Discover()
	descriptors[0].ConfigSchema[0] = '!'
	if Discover()[0].ConfigSchema[0] == '!' {
		t.Fatal("mutable registry")
	}
}
func TestPurePredicatesAndInertOutputs(t *testing.T) {
	now := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	evidence := &Evidence{Kind: "report", ID: "1", Version: "v1", Subject: Subject{Kind: "task", TaskID: "1"}, Digest: Digest("report")}
	for _, tc := range []struct {
		name          string
		change        func(*Facts)
		delivery, ack string
	}{
		{"complete-empty", func(f *Facts) {}, "unmet", "unknown"},
		{"report", func(f *Facts) { f.Settled = true; f.Report = evidence }, "met", "pending"},
		{"receipt", func(f *Facts) { f.Settled = true; f.Report = evidence; f.Receipt = evidence }, "met", "met"},
		{"incomplete", func(f *Facts) { f.Complete = false; f.Settled = true; f.Report = evidence }, "unknown", "unknown"},
		{"stale", func(f *Facts) { f.ValidUntil = now; f.Settled = true; f.Report = evidence }, "unknown", "unknown"},
		{"running", func(f *Facts) { f.Running = true }, "pending", "unknown"},
		{"code/standing", func(f *Facts) { f.Applicable = false; f.UnsupportedReason = "unsupported task shape" }, "unknown", "unknown"},
		{"old-close", func(f *Facts) { f.Settled = true; f.Report = evidence; f.ReceiptUnversioned = true }, "met", "unknown"},
		{"implement-report", func(f *Facts) {
			f.Settled = true
			f.Product = "report"
			f.Report = evidence
			f.ProductEvidence = evidence
		}, "met", "pending"},
		{"implement-report-receipt", func(f *Facts) {
			f.Settled = true
			f.Product = "report"
			f.Report = evidence
			f.ProductEvidence = evidence
			f.Receipt = evidence
		}, "met", "met"},
		{"change-with-comment", func(f *Facts) {
			f.Settled = true
			f.Product = "change"
			f.ProductEvidence = evidence
			f.Report = evidence
			f.Receipt = evidence
		}, "unmet", "unknown"},
		{"no-work-with-comment", func(f *Facts) {
			f.Settled = true
			f.Product = "nothing_needed"
			f.ProductEvidence = evidence
			f.Report = evidence
			f.Receipt = evidence
		}, "unmet", "unknown"},
		{"review-with-comment", func(f *Facts) {
			f.Settled = true
			f.Product = "review"
			f.ProductEvidence = evidence
			f.Report = evidence
			f.Receipt = evidence
		}, "unmet", "unknown"},
		{"blocked-with-comment", func(f *Facts) { f.Settled = true; f.Blocked = true; f.Report = evidence }, "pending", "unknown"},
		{"unknown-product-with-comment", func(f *Facts) {
			f.Settled = true
			f.ProductUnknown = "Settled change task lacks current authenticated finish provenance."
			f.Report = evidence
			f.Receipt = evidence
		}, "unknown", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := Facts{TaskID: "1", Applicable: true, Complete: true, SourceVersion: "v1", ObservedAt: now, ValidUntil: now.Add(time.Minute)}
			tc.change(&f)
			before := f
			for i, b := range Bindings("1") {
				r := Evaluate(b, f, now, 1)
				want := []string{tc.delivery, tc.ack}[i]
				if r.Status != want {
					t.Errorf("%s=%s want %s", b.Type, r.Status, want)
				}
				if b.Type == "acknowledgment" && want == "unknown" && !f.ReceiptUnversioned && (len(r.Evidence) != 0 || len(r.Waits) != 0) {
					t.Fatalf("non-report acknowledgment retained evidence or waits: %+v", r)
				}
				if len(r.Actions) != 0 || len(r.Work) != 0 {
					t.Fatal("operational outputs")
				}
				if !reflect.DeepEqual(r, Evaluate(b, f, now, 1)) {
					t.Fatal("not pure")
				}
				if strings.Contains(r.EvaluationID, "http") {
					t.Fatal("remote evaluator")
				}
			}
			if !reflect.DeepEqual(before, f) {
				t.Fatal("mutated facts")
			}
		})
	}
	if Implementation != "builtin-gate-observer-v10" {
		t.Fatalf("implementation pin = %q, want builtin-gate-observer-v10", Implementation)
	}
}

func TestDigestCanonicalUnicodeValuesAndEscapes(t *testing.T) {
	for _, value := range []struct{ value, canonical string }{
		{"\u2028\u2029", "\"\u2028\u2029\""},
		{`\u2028`, `"\\u2028"`},
		{"<>&", `"<>&"`},
	} {
		want := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(value.canonical)))
		if got := Digest(value.value); got != want {
			t.Errorf("%q: %s want %s", value.value, got, want)
		}
	}
}
