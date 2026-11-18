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

package qa

import (
	"testing"
	"time"
)

type dummyProfiler struct{}

func (p dummyProfiler) Measure(meta PerfSample, fn func() (verdict string, err error)) (PerfSample, error) {
	_, _ = fn()
	return meta, nil
}

func TestAssessTaskComplexity(t *testing.T) {
	tests := []struct {
		name     string
		in       ComplexityInput
		wantTier ComplexityTier
	}{
		{
			name: "high from design item and writes",
			in: ComplexityInput{
				Body:        "Design-Item: true",
				WritesCount: 2,
				ReadsCount:  2,
				PathHints:   []string{"pkg/orchestrator"},
			},
			wantTier: ComplexityHigh,
		},
		{
			name: "low mechanical",
			in: ComplexityInput{
				Title: "rename stuff",
			},
			wantTier: ComplexityLow,
		},
		{
			name: "ui icon tweak with dispatch envelope stays below high",
			in: ComplexityInput{
				Title: "[ACME-10] Add little eye visibility icon glyph at bottom of card",
				Body: "Dispatched from **Issue Tracker** (`ACME-10` in project **Widgets**, owner `alice@example.com`).\n\n" +
					"> **Gate Override Note:** already checked\n\n" +
					"## Pinned Deliverables (Materialized in `./attachments/`)\n" +
					"- **SPEC** (`abc@v1`, file `./attachments/1-spec.md`): visibility auth note\n",
			},
			wantTier: ComplexityLow,
		},
		{
			name: "multi-deliverable cross-surface milestone feature scores high",
			in: ComplexityInput{
				Title: "Build attachment storage API and Angular inspector upload UI",
				Body: "## Pinned Deliverables\n" +
					"- **CONCEPT** (`./attachments/1-concept.md`)\n" +
					"- **SPEC** (`./attachments/2-spec.md`)\n\n" +
					"## Milestones\n- Wave 1: pkg/model/schema.go + internal/server\n- Wave 2: ui-ng/src/app/card.component.ts\n",
			},
			wantTier: ComplexityHigh,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AssessTaskComplexity(tt.in, dummyProfiler{})
			if got.Tier != tt.wantTier {
				t.Errorf("got tier %s, want %s (score %d, signals %v)", got.Tier, tt.wantTier, got.Score, got.Signals)
			}
		})
	}
}

func BenchmarkAssessTaskComplexity(b *testing.B) {
	in := ComplexityInput{
		Title:       "Add auth to orchestrator",
		Body:        "Design-Item: 123\n- a\n- b\n- c\n- d\n- e\n- f",
		WritesCount: 5,
		ReadsCount:  10,
		PathHints:   []string{"pkg/orchestrator", "ui/app.ts"},
	}
	prof := dummyProfiler{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		AssessTaskComplexity(in, prof)
	}
}

func TestAssessTaskComplexity_TimeLimit(t *testing.T) {
	in := ComplexityInput{
		Title:       "Add auth to orchestrator",
		Body:        "Design-Item: 123\n- a\n- b\n- c\n- d\n- e\n- f",
		WritesCount: 5,
		ReadsCount:  10,
		PathHints:   []string{"pkg/orchestrator", "ui/app.ts"},
	}
	prof := dummyProfiler{}

	start := time.Now()
	for i := 0; i < 100; i++ {
		AssessTaskComplexity(in, prof)
	}
	elapsed := time.Since(start)
	if avg := elapsed / 100; avg > 5*time.Millisecond {
		t.Errorf("execution took too long: avg %v (limit 5ms)", avg)
	}
}
