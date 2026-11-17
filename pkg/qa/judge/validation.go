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

package judge

func ComputeICC1K(scores [][]float64) float64 {
	n := len(scores)
	if n == 0 {
		return 0
	}
	k := len(scores[0])
	if k == 0 || k == 1 {
		return 0
	}

	var grandTotal float64
	for _, target := range scores {
		for _, score := range target {
			grandTotal += score
		}
	}
	grandMean := grandTotal / float64(n*k)

	var sst, ssb float64
	for _, target := range scores {
		var targetTotal float64
		for _, score := range target {
			targetTotal += score
			sst += (score - grandMean) * (score - grandMean)
		}
		targetMean := targetTotal / float64(k)
		ssb += float64(k) * (targetMean - grandMean) * (targetMean - grandMean)
	}
	ssw := sst - ssb

	if n-1 == 0 {
		return 1.0
	}
	bms := ssb / float64(n-1)
	if float64(n*(k-1)) == 0 {
		return 1.0
	}
	wms := ssw / float64(n*(k-1))

	if bms == 0 {
		return 1.0
	}

	icc := (bms - wms) / bms
	return icc
}

func ComputeArmProbeAUC(treatmentProbs []float64, isTreatment []bool) float64 {
	if len(treatmentProbs) != len(isTreatment) || len(treatmentProbs) == 0 {
		return 0
	}
	var tCount, cCount int
	for _, t := range isTreatment {
		if t {
			tCount++
		} else {
			cCount++
		}
	}
	if tCount == 0 || cCount == 0 {
		return 0
	}
	var u float64
	for i, pi := range treatmentProbs {
		if !isTreatment[i] {
			continue
		}
		for j, pj := range treatmentProbs {
			if isTreatment[j] {
				continue
			}
			if pi > pj {
				u += 1
			} else if pi == pj {
				u += 0.5
			}
		}
	}
	return u / float64(tCount*cCount)
}
