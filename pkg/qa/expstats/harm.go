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
	"sort"
)

func NormalMixtureSPRTOneSided(treatTimes, ctrlTimes []float64, tau, alpha float64) (stat, threshold float64, harmed bool, hlShiftPct float64) {
	if len(treatTimes) < 15 || len(ctrlTimes) < 15 {
		return 0, 0, false, 0
	}

	if alpha <= 0 {
		alpha = 0.01
	}
	if tau <= 0 {
		tau = 1.0
	}

	mT, vT := meanAndSampleVar(treatTimes)
	mC, vC := meanAndSampleVar(ctrlTimes)

	diff := mT - mC
	vN := vT/float64(len(treatTimes)) + vC/float64(len(ctrlTimes))

	if vN <= 1e-12 {
		if diff > 0 {
			stat = math.Inf(1)
		} else {
			stat = 0
		}
	} else {
		tau2 := tau * tau
		ratio := vN / (vN + tau2)
		exponent := (tau2 * diff * diff) / (2.0 * vN * (vN + tau2))
		if exponent > 50 {
			exponent = 50
		}
		if diff > 0 {
			stat = math.Sqrt(ratio) * math.Exp(exponent)
		} else {
			stat = 1.0
		}
	}

	threshold = 1.0 / alpha
	hlShiftPct = hodgesLehmannRelativeShift(treatTimes, ctrlTimes)

	if stat >= threshold && hlShiftPct > 0.25 {
		harmed = true
	}

	return stat, threshold, harmed, hlShiftPct
}

func meanAndSampleVar(xs []float64) (mean, variance float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	mean = sum / float64(len(xs))
	if len(xs) > 1 {
		var sqSum float64
		for _, x := range xs {
			sqSum += (x - mean) * (x - mean)
		}
		variance = sqSum / float64(len(xs)-1)
	}
	return mean, variance
}

func hodgesLehmannRelativeShift(treat, ctrl []float64) float64 {
	tSorted := make([]float64, len(treat))
	copy(tSorted, treat)
	cSorted := make([]float64, len(ctrl))
	copy(cSorted, ctrl)
	sort.Float64s(tSorted)
	sort.Float64s(cSorted)

	var cMed float64
	n := len(cSorted)
	if n > 0 {
		if n%2 == 0 {
			cMed = (cSorted[n/2-1] + cSorted[n/2]) / 2.0
		} else {
			cMed = cSorted[n/2]
		}
	}

	diffs := make([]float64, 0, len(treat)*len(ctrl))
	for _, y := range tSorted {
		for _, x := range cSorted {
			diffs = append(diffs, y-x)
		}
	}
	sort.Float64s(diffs)
	var shift float64
	nd := len(diffs)
	if nd > 0 {
		if nd%2 == 0 {
			shift = (diffs[nd/2-1] + diffs[nd/2]) / 2.0
		} else {
			shift = diffs[nd/2]
		}
	}

	if cMed > 1e-9 {
		return shift / cMed
	}
	return 0
}
