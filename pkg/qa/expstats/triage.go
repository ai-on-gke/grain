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
	"math"
)

type TriageObservation struct {
	RootOrigin                string
	Decision                  string
	Escaped                   bool
	HadHumanFindingOrBoundary bool
}

type TriageSummary struct {
	TotalEvaluated              int
	AutoMergeCount              int
	HumanRoutedCount            int
	AutoMergeEligiblePct        float64
	HumanRoutedPct              float64
	AutoMergedEscapes           int
	AutoMergedEscapeRate        float64
	HumanRoutedTruePositives    int
	HumanRoutedTruePositiveRate float64
}

func ComputeTriageSummary(obs []TriageObservation) TriageSummary {
	var s TriageSummary
	s.TotalEvaluated = len(obs)
	if s.TotalEvaluated == 0 {
		return s
	}

	for _, o := range obs {
		if o.Decision == "auto_merge" {
			s.AutoMergeCount++
			if o.Escaped {
				s.AutoMergedEscapes++
			}
		} else if o.Decision == "require_human" {
			s.HumanRoutedCount++
			if o.HadHumanFindingOrBoundary {
				s.HumanRoutedTruePositives++
			}
		}
	}

	s.AutoMergeEligiblePct = float64(s.AutoMergeCount) / float64(s.TotalEvaluated)
	s.HumanRoutedPct = float64(s.HumanRoutedCount) / float64(s.TotalEvaluated)

	if s.AutoMergeCount > 0 {
		s.AutoMergedEscapeRate = float64(s.AutoMergedEscapes) / float64(s.AutoMergeCount)
	}
	if s.HumanRoutedCount > 0 {
		s.HumanRoutedTruePositiveRate = float64(s.HumanRoutedTruePositives) / float64(s.HumanRoutedCount)
	}

	return s
}

type TriageHarmVerdict struct {
	Tripped         bool
	PValue          float64
	DeltaEscapeRate float64
	Reason          string
}

func EvaluateTriageHarmBreaker(refObs, candObs []TriageObservation, alpha float64, minTrees int) TriageHarmVerdict {
	if alpha == 0 {
		alpha = 0.01
	}
	if minTrees == 0 {
		minTrees = 15
	}

	candSummary := ComputeTriageSummary(candObs)
	refSummary := ComputeTriageSummary(refObs)

	if len(candObs) < minTrees {
		return TriageHarmVerdict{PValue: 1.0, Reason: "insufficient candidate observations"}
	}
	if candSummary.AutoMergeCount == 0 {
		return TriageHarmVerdict{PValue: 1.0, Reason: "no auto_merge decisions in candidate"}
	}

	refRate := refSummary.AutoMergedEscapeRate
	n2 := float64(refSummary.AutoMergeCount)

	if refSummary.AutoMergeCount == 0 {
		escapes := 0
		for _, o := range refObs {
			if o.Escaped {
				escapes++
			}
		}
		if len(refObs) > 0 {
			refRate = float64(escapes) / float64(len(refObs))
			n2 = float64(len(refObs))
		}
	}

	if n2 == 0 {
		return TriageHarmVerdict{PValue: 1.0, Reason: "no reference observations"}
	}

	candRate := candSummary.AutoMergedEscapeRate
	delta := candRate - refRate

	if delta <= 0 {
		return TriageHarmVerdict{PValue: 1.0, DeltaEscapeRate: delta, Reason: "no harm (delta <= 0)"}
	}

	n1 := float64(candSummary.AutoMergeCount)
	p1 := candRate
	p2 := refRate

	x1 := p1 * n1
	x2 := p2 * n2
	pPool := (x1 + x2) / (n1 + n2)

	if pPool == 0 || pPool == 1 {
		return TriageHarmVerdict{PValue: 1.0, DeltaEscapeRate: delta, Reason: "zero variance"}
	}

	seSq := pPool * (1 - pPool) * (1/n1 + 1/n2)
	if seSq == 0 {
		return TriageHarmVerdict{PValue: 1.0, DeltaEscapeRate: delta, Reason: "zero variance"}
	}

	V := 1.0 / seSq
	tauSq := 0.1

	zSq := delta * delta * V
	lambda := math.Sqrt(1.0/(1.0+V*tauSq)) * math.Exp((V*tauSq*zSq)/(2.0*(1.0+V*tauSq)))
	pValue := 1.0 / lambda

	if pValue > 1.0 || math.IsNaN(pValue) {
		pValue = 1.0
	}

	tripped := pValue <= alpha && delta > 0
	reason := "safe"
	if tripped {
		reason = "mSPRT tripped"
	}

	return TriageHarmVerdict{
		Tripped:         tripped,
		PValue:          pValue,
		DeltaEscapeRate: delta,
		Reason:          reason,
	}
}
