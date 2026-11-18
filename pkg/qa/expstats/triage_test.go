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

package expstats

import (
	"testing"
)

func TestEvaluateTriageHarmBreaker(t *testing.T) {
	tests := []struct {
		name        string
		refObs      []TriageObservation
		candObs     []TriageObservation
		wantTripped bool
	}{
		{
			name:        "empty",
			refObs:      []TriageObservation{},
			candObs:     []TriageObservation{},
			wantTripped: false,
		},
		{
			name: "benign parity",
			refObs: []TriageObservation{
				{Decision: "auto_merge", Escaped: false},
				{Decision: "auto_merge", Escaped: true},
			},
			candObs: []TriageObservation{
				{Decision: "auto_merge", Escaped: false}, {Decision: "auto_merge", Escaped: true},
				{Decision: "auto_merge", Escaped: false}, {Decision: "auto_merge", Escaped: true},
				{Decision: "auto_merge", Escaped: false}, {Decision: "auto_merge", Escaped: true},
				{Decision: "auto_merge", Escaped: false}, {Decision: "auto_merge", Escaped: true},
				{Decision: "auto_merge", Escaped: false}, {Decision: "auto_merge", Escaped: true},
				{Decision: "auto_merge", Escaped: false}, {Decision: "auto_merge", Escaped: true},
				{Decision: "auto_merge", Escaped: false}, {Decision: "auto_merge", Escaped: true},
				{Decision: "auto_merge", Escaped: false}, {Decision: "auto_merge", Escaped: true},
			},
			wantTripped: false,
		},
		{
			name: "synthetic escape spike",
			refObs: []TriageObservation{
				{Decision: "auto_merge", Escaped: false},
				{Decision: "auto_merge", Escaped: false},
				{Decision: "auto_merge", Escaped: false},
				{Decision: "auto_merge", Escaped: false},
				{Decision: "auto_merge", Escaped: false},
			},
			candObs: []TriageObservation{
				{Decision: "auto_merge", Escaped: true},
				{Decision: "auto_merge", Escaped: true},
				{Decision: "auto_merge", Escaped: true},
				{Decision: "auto_merge", Escaped: true},
				{Decision: "auto_merge", Escaped: true},
				{Decision: "auto_merge", Escaped: true},
				{Decision: "auto_merge", Escaped: true},
				{Decision: "auto_merge", Escaped: true},
				{Decision: "auto_merge", Escaped: true},
				{Decision: "auto_merge", Escaped: true},
				{Decision: "auto_merge", Escaped: true},
				{Decision: "auto_merge", Escaped: true},
				{Decision: "auto_merge", Escaped: true},
				{Decision: "auto_merge", Escaped: true},
				{Decision: "auto_merge", Escaped: true},
				{Decision: "auto_merge", Escaped: true},
			},
			wantTripped: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateTriageHarmBreaker(tt.refObs, tt.candObs, 0.01, 15)
			if got.Tripped != tt.wantTripped {
				t.Errorf("got tripped %v want %v (pvalue=%v delta=%v)", got.Tripped, tt.wantTripped, got.PValue, got.DeltaEscapeRate)
			}
		})
	}
}
