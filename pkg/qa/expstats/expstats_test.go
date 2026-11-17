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
	"fmt"
	"math"
	"testing"
)

func TestHashBucket(t *testing.T) {
	counts := make([]int, 100)
	for i := 0; i < 10000; i++ {
		b := HashBucket("salt", fmt.Sprintf("origin-%d", i))
		counts[b%100]++
	}
	var chiSq float64
	for _, c := range counts {
		chiSq += math.Pow(float64(c)-100, 2) / 100
	}
	if chiSq > 150 {
		t.Errorf("HashBucket not uniform enough, chiSq=%f", chiSq)
	}
}

func TestExplicitListAndAudit(t *testing.T) {
	arms := []string{"treat", "ctrl"}
	entries, _, err := GenerateExplicitList("paired", arms, 10, nil, 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 20 {
		t.Fatalf("expected 20 entries, got %d", len(entries))
	}

	enrollments := []Enrollment{
		{RootOrigin: "r1", Key: entries[0].Key, ArmID: entries[0].ArmID},
		{RootOrigin: "r2", Key: entries[1].Key, ArmID: entries[1].ArmID},
	}
	rep := AuditAssignments("explicit", "", nil, entries, enrollments)
	if !rep.Passed {
		t.Errorf("expected audit to pass")
	}

	enrollments[0].ArmID = "tampered"
	rep2 := AuditAssignments("explicit", "", nil, entries, enrollments)
	if rep2.Passed {
		t.Errorf("expected audit to fail on tampered arm")
	}
	if len(rep2.Mismatches) != 1 {
		t.Errorf("expected 1 mismatch, got %d", len(rep2.Mismatches))
	}

	_, _, err = GenerateExplicitList("blocked", arms, 20, []string{"stratumA"}, 42)
	if err != nil {
		t.Fatal(err)
	}
}

func TestAdjustments(t *testing.T) {
	pvals := []float64{0.01, 0.04, 0.03, 0.005}
	holm := HolmAdjust(pvals)
	if holm[3] != 0.02 || holm[0] != 0.03 || holm[2] != 0.06 || holm[1] != 0.06 {
		t.Errorf("Holm adjustment incorrect: %v", holm)
	}
	bh := BHAdjust(pvals)
	if bh[3] != 0.02 || bh[0] != 0.02 {
		t.Errorf("BH adjustment incorrect: %v", bh)
	}
}

func TestTests(t *testing.T) {
	diffs := []float64{10, 15, -2, 12, 8, -5, 20, 18, 5, 2}
	_, _, _, pVal, _ := PairedSignFlipTest(diffs, 1000, 42)
	if pVal >= 0.05 {
		t.Errorf("expected strong signal to have p<0.05, got %f", pVal)
	}

	zeroDiffs := []float64{1, -1, 2, -2, 3, -3, 4, -4}
	_, _, _, pVal0, _ := PairedSignFlipTest(zeroDiffs, 1000, 42)
	if pVal0 < 0.05 {
		t.Errorf("expected null signal to have high p, got %f", pVal0)
	}

	// Constant float slices whose sumX2*n - sumX*sumX can underflow below 0 in IEEE-754 must return 0, never NaN.
	constVals := []float64{0.1 + 0.2, 0.1 + 0.2, 0.1 + 0.2, 0.1 + 0.2, 0.1 + 0.2, 0.1 + 0.2, 0.1 + 0.2}
	rho := PairedCorrelation(constVals, []float64{1, 2, 3, 4, 5, 6, 7})
	if math.IsNaN(rho) || rho != 0 {
		t.Errorf("expected PairedCorrelation on constant vector to be 0 (non-NaN), got %v", rho)
	}
}

func TestClusterPermutationTest(t *testing.T) {
	treat := []ClusterObs{{10, 1, ""}, {15, 1, ""}, {12, 1, ""}, {20, 1, ""}, {18, 1, ""}}
	ctrl := []ClusterObs{{2, 1, ""}, {5, 1, ""}, {-2, 1, ""}, {-5, 1, ""}, {8, 1, ""}}
	pVal := ClusterPermutationTest(treat, ctrl, false, 1000, 42)
	if pVal >= 0.05 {
		t.Errorf("expected strong signal to have p<0.05, got %f", pVal)
	}
}

func TestExactMultinomialSRM(t *testing.T) {
	counts := map[string]int{"t": 500, "c": 500}
	weights := map[string]float64{"t": 1.0, "c": 1.0}
	p := ExactMultinomialSRM(counts, weights)
	if p < 0.9 {
		t.Errorf("expected ~1.0, got %f", p)
	}

	counts2 := map[string]int{"t": 800, "c": 200}
	p2 := ExactMultinomialSRM(counts2, weights)
	if p2 > 0.001 {
		t.Errorf("expected <0.001, got %f", p2)
	}
}

func TestNormalMixtureSPRTOneSided(t *testing.T) {
	treat := []float64{1, 2, 1, 2}
	ctrl := []float64{1, 2, 1, 2}
	_, _, harmed, _ := NormalMixtureSPRTOneSided(treat, ctrl, 1.0, 0.01)
	if harmed {
		t.Errorf("should not trigger for N < 15")
	}

	treat2 := make([]float64, 200)
	ctrl2 := make([]float64, 200)
	for i := 0; i < 200; i++ {
		treat2[i] = 16.0
		ctrl2[i] = 10.0
	}
	_, _, harmed2, _ := NormalMixtureSPRTOneSided(treat2, ctrl2, 1.0, 0.01)
	if !harmed2 {
		t.Errorf("expected to trigger on +60%% harm")
	}

	for i := 0; i < 200; i++ {
		treat2[i] = 7.0
		ctrl2[i] = 10.0
	}
	_, _, harmed3, _ := NormalMixtureSPRTOneSided(treat2, ctrl2, 1.0, 0.01)
	if harmed3 {
		t.Errorf("expected to NOT trigger on -30%% improvement")
	}
}

func TestSimulatePowerPreview(t *testing.T) {
	p1 := SimulatePowerPreview(PowerConfig{PlannedOriginsPerArm: 100, Design: "unpaired"})
	p2 := SimulatePowerPreview(PowerConfig{PlannedOriginsPerArm: 1000, Design: "unpaired"})
	if p1.PowerObserved >= p2.PowerObserved {
		t.Errorf("expected power to be monotonic in N")
	}
}
