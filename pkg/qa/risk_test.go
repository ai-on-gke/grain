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

func TestAssessDiffBlast(t *testing.T) {
	tests := []struct {
		name      string
		stats     DiffStats
		overrides []RiskOverride
		wantTier  BlastTier
		wantTrust bool
		wantScore int
	}{
		{
			name: "t0 docs",
			stats: DiffStats{
				Files: []string{"README.md"},
			},
			wantTier:  BlastT0,
			wantScore: 0,
		},
		{
			name: "t1 normal",
			stats: DiffStats{
				Files:        []string{"pkg/foo/bar.go"},
				NonTestAdded: 10,
			},
			wantTier:  BlastT1,
			wantScore: 15,
		},
		{
			name: "t3 trust boundary",
			stats: DiffStats{
				Files: []string{"pkg/qa/something.go"},
			},
			wantTier:  BlastT3,
			wantTrust: true,
			wantScore: 85,
		},
		{
			name: "t3 test tampered",
			stats: DiffStats{
				Files:        []string{"pkg/foo/bar.go"},
				TestTampered: true,
			},
			wantTier:  BlastT3,
			wantScore: 75,
		},
		{
			name: "t3 10 siblings",
			stats: DiffStats{
				Files:                 []string{"pkg/foo/bar.go"},
				SiblingCallerPackages: 10,
			},
			wantTier:  BlastT3,
			wantScore: 75,
		},
		{
			name: "t2 overrides",
			stats: DiffStats{
				Files:        []string{"pkg/foo/bar.go"},
				NonTestAdded: 10,
			},
			overrides: []RiskOverride{
				{Paths: []string{"pkg/foo/*"}, MinTier: BlastT2},
			},
			wantTier:  BlastT2,
			wantScore: 35,
		},
		{
			name: "monotonic overrides",
			stats: DiffStats{
				Files: []string{"pkg/qa/bar.go"}, // T3
			},
			overrides: []RiskOverride{
				{Paths: []string{"pkg/qa/*"}, MinTier: BlastT1}, // should not downgrade
			},
			wantTier:  BlastT3,
			wantTrust: true,
			wantScore: 85,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AssessDiffBlast(tt.stats, tt.overrides, dummyProfiler{})
			if got.Tier != tt.wantTier {
				t.Errorf("got tier %v want %v", got.Tier, tt.wantTier)
			}
			if got.TrustBoundaryHit != tt.wantTrust {
				t.Errorf("got trust boundary %v want %v", got.TrustBoundaryHit, tt.wantTrust)
			}
			if got.Score < tt.wantScore {
				t.Errorf("got score %d, expected at least %d", got.Score, tt.wantScore)
			}
		})
	}
}

func BenchmarkAssessDiffBlast(b *testing.B) {
	stats := DiffStats{
		Files:                 []string{"pkg/orchestrator/foo.go", "pkg/model/schema.go"},
		NonTestAdded:          100,
		NonTestDeleted:        50,
		SiblingCallerPackages: 5,
	}
	prof := dummyProfiler{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		AssessDiffBlast(stats, nil, prof)
	}
}

func TestAssessDiffBlast_TimeLimit(t *testing.T) {
	stats := DiffStats{
		Files:                 []string{"pkg/orchestrator/foo.go", "pkg/model/schema.go"},
		NonTestAdded:          100,
		NonTestDeleted:        50,
		SiblingCallerPackages: 5,
	}
	prof := dummyProfiler{}
	start := time.Now()
	for i := 0; i < 100; i++ {
		AssessDiffBlast(stats, nil, prof)
	}
	elapsed := time.Since(start)
	if avg := elapsed / 100; avg > 200*time.Millisecond {
		t.Errorf("execution took too long: avg %v (limit 200ms)", avg)
	}
}
