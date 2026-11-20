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
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func referenceDigest(v any) string {
	b, _ := json.Marshal(v)
	var obj any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	_ = dec.Decode(&obj)
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(obj)
	encoded := bytes.TrimSuffix(out.Bytes(), []byte("\n"))
	canonical := make([]byte, 0, len(encoded))
	for i := 0; i < len(encoded); i++ {
		if encoded[i] == '\\' && i+1 < len(encoded) {
			if i+5 < len(encoded) && (string(encoded[i+1:i+6]) == "u2028" || string(encoded[i+1:i+6]) == "u2029") {
				if encoded[i+5] == '8' {
					canonical = append(canonical, []byte("\u2028")...)
				} else {
					canonical = append(canonical, []byte("\u2029")...)
				}
				i += 5
				continue
			}
			canonical = append(canonical, encoded[i], encoded[i+1])
			i++
			continue
		}
		canonical = append(canonical, encoded[i])
	}
	sum := sha256.Sum256(canonical)
	return fmt.Sprintf("sha256:%x", sum)
}

func TestDigestMatchesReference(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 34, 56, 789, time.UTC)
	head := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cases := []struct {
		name string
		val  any
	}{
		{"nil", nil},
		{"empty-string", ""},
		{"plain-string", "hello world"},
		{"html-string", `<script>&"hello"</script>`},
		{"line-sep-string", "a\u2028b\u2029c\\u2028d"},
		{"string-slice", []string{"z", "a", "<tag>", "x\u2028y"}},
		{"nil-string-slice", []string(nil)},
		{"empty-string-slice", []string{}},
		{"struct-unsorted-keys", struct {
			Z int
			A string
			M []any
		}{Z: 42, A: "alpha<&>", M: []any{true, false, nil, 1.5, map[string]any{"b": 2, "a": 1}}}},
		{"raw-message-unsorted-and-escaped", struct {
			Config json.RawMessage `json:"config"`
		}{Config: json.RawMessage(`{ "z" : "\u0041\u003c\/b\u003e", "a": [1, 2], "z": "last" }`)}},
		{"raw-message-escaped-key", json.RawMessage(`{"\u0062": 1, "a": 2}`)},
		{"bindings", Bindings("42")},
		{"observation-view", ObservationView{
			APIVersion: "grain.gates/v1",
			Mode:       "observation_only",
			Authority:  "legacy",
			ComparedAt: now,
			Bindings:   Bindings("42"),
			Results: []EvaluationState{{
				EvaluationID: "eval-1",
				Revision:     1,
				Disposition:  "accepted",
				AcceptedAt:   &now,
				Result: Result{
					Type:         "delivery",
					Version:      1,
					Status:       "met",
					Summary:      "ok <1> & \u2028",
					Evidence:     []Evidence{{Kind: "report", ID: "1", Head: &head}},
					Dependencies: []Dependency{{Key: "k", Head: &head, ObservedAt: now, ValidUntil: now.Add(time.Minute)}},
				},
			}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Digest(tc.val)
			want := referenceDigest(tc.val)
			if got != want {
				t.Fatalf("Digest(%s) = %s, want %s", tc.name, got, want)
			}
		})
	}
}

func TestObservationViewCloneIsIndependent(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	head := "deadbeef"
	text := "original"
	action := "merge"
	orig := ObservationView{
		APIVersion: "grain.gates/v1",
		Mode:       "observation_only",
		Authority:  "legacy",
		ComparedAt: now,
		Bindings: []Binding{{
			ID:     "b1",
			Type:   "delivery",
			Action: &action,
			Config: json.RawMessage(`{"product":"report"}`),
		}},
		Results: []EvaluationState{{
			EvaluationID: "e1",
			AcceptedAt:   &now,
			Result: Result{
				Status:       "met",
				HumanText:    &text,
				AgentText:    &text,
				Evidence:     []Evidence{{Kind: "report", ID: "1", Head: &head}},
				Dependencies: []Dependency{{Key: "d1", Head: &head}},
				Error:        &EvaluationError{Code: "unavailable", Message: "err"},
			},
		}},
	}
	cloned := orig.Clone()
	if !reflect.DeepEqual(orig, cloned) {
		t.Fatalf("clone mismatch:\ngot  %+v\nwant %+v", cloned, orig)
	}
	// Mutate every mutable field in the clone and confirm orig is unchanged.
	*cloned.Bindings[0].Action = "mutated"
	cloned.Bindings[0].Config[0] = '['
	*cloned.Results[0].AcceptedAt = now.Add(time.Hour)
	*cloned.Results[0].Result.HumanText = "changed"
	*cloned.Results[0].Result.AgentText = "changed"
	*cloned.Results[0].Result.Evidence[0].Head = "changed"
	*cloned.Results[0].Result.Dependencies[0].Head = "changed"
	cloned.Results[0].Result.Error.Message = "changed"
	cloned.Results[0].Result.Status = "unknown"
	if *orig.Bindings[0].Action != "merge" || orig.Bindings[0].Config[0] != '{' ||
		!orig.Results[0].AcceptedAt.Equal(now) || *orig.Results[0].Result.HumanText != "original" ||
		*orig.Results[0].Result.AgentText != "original" || *orig.Results[0].Result.Evidence[0].Head != "deadbeef" ||
		*orig.Results[0].Result.Dependencies[0].Head != "deadbeef" || orig.Results[0].Result.Error.Message != "err" ||
		orig.Results[0].Result.Status != "met" {
		t.Fatalf("mutating clone affected original: %+v", orig)
	}
}
