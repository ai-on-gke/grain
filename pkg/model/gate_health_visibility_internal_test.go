// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package model

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gates"
)

func TestFilteredHealthHistoryPreservesComparisonFamilies(t *testing.T) {
	d := GateObservationDiagnostics{
		StartedAt: time.Now(), Evaluations: 100,
		Counts: map[string]int{"outcome_ok": 90, "checks_met": 90, "health_profiles_agree": 10},
		History: []GateObservationSample{
			{Comparison: "outcome_ok;checks_met", View: gates.ObservationView{Bindings: gates.Bindings("report")}},
			{Comparison: "health_profiles_agree", View: gates.ObservationView{Bindings: []gates.Binding{
				{Type: "base_health", Subject: gates.Subject{Kind: "repository", Repository: "acme/private"}},
			}}},
		},
	}
	filterHealthDiagnostics(&d, GateHealthAccess{ScopeLimited: true})
	if len(d.History) != 1 || !d.StartedAt.IsZero() || d.Evaluations != 0 {
		t.Fatalf("restricted history retained hidden samples or aggregate metadata: %+v", d)
	}
	if len(d.Counts) != 2 || d.Counts["outcome_ok"] != 1 || d.Counts["checks_met"] != 1 {
		t.Fatalf("visible samples must retain independent comparison-family counts: %v", d.Counts)
	}
}
