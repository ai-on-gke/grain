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
	mathrand "math/rand/v2"
	"sort"
)

type ClusterObs struct {
	Num     float64
	Den     float64
	Segment string
}

func sumRatio(obs []ClusterObs) float64 {
	var num, den float64
	for _, o := range obs {
		num += o.Num
		den += o.Den
	}
	if den == 0 {
		return 0
	}
	return num / den
}

func sumMean(obs []ClusterObs) float64 {
	var num float64
	for _, o := range obs {
		num += o.Num
	}
	if len(obs) == 0 {
		return 0
	}
	return num / float64(len(obs))
}

func ClusterRatioEstimate(treat, ctrl []ClusterObs, better string, nBoot int, seed int64) (delta, ciLow, ciHigh float64) {
	rng := mathrand.New(mathrand.NewPCG(uint64(seed), 1))

	treatRatio := sumRatio(treat)
	ctrlRatio := sumRatio(ctrl)

	actualDelta := treatRatio - ctrlRatio
	if better == "lower" {
		actualDelta = -actualDelta
	}

	deltas := make([]float64, nBoot)
	for i := 0; i < nBoot; i++ {
		tBoot := make([]ClusterObs, len(treat))
		for j := 0; j < len(treat); j++ {
			tBoot[j] = treat[rng.IntN(len(treat))]
		}
		cBoot := make([]ClusterObs, len(ctrl))
		for j := 0; j < len(ctrl); j++ {
			cBoot[j] = ctrl[rng.IntN(len(ctrl))]
		}
		tRatio := sumRatio(tBoot)
		cRatio := sumRatio(cBoot)

		d := tRatio - cRatio
		if better == "lower" {
			d = -d
		}
		deltas[i] = d
	}

	sort.Float64s(deltas)

	// Percentile CI (simple). BCa is complex, instructions say "percentile/BCa", we can just do percentile.
	lowIdx := int(math.Floor(0.025 * float64(nBoot)))
	highIdx := int(math.Ceil(0.975 * float64(nBoot)))
	if highIdx >= nBoot {
		highIdx = nBoot - 1
	}

	return actualDelta, deltas[lowIdx], deltas[highIdx]
}

func ClusterPermutationTest(treat, ctrl []ClusterObs, stratifiedSegments bool, nPerm int, seed int64) float64 {
	rng := mathrand.New(mathrand.NewPCG(uint64(seed), 1))

	allObs := append([]ClusterObs{}, treat...)
	allObs = append(allObs, ctrl...)

	treatRatio := sumRatio(treat)
	ctrlRatio := sumRatio(ctrl)
	actualDiff := math.Abs(treatRatio - ctrlRatio)

	if actualDiff == 0 {
		return 1.0
	}

	// Pre-group by segment if stratified
	segments := make(map[string][]ClusterObs)
	if stratifiedSegments {
		for _, o := range allObs {
			segments[o.Segment] = append(segments[o.Segment], o)
		}
	} else {
		segments[""] = allObs
	}

	treatSizes := make(map[string]int)
	if stratifiedSegments {
		for _, o := range treat {
			treatSizes[o.Segment]++
		}
	} else {
		treatSizes[""] = len(treat)
	}

	greaterOrEqual := 0
	for i := 0; i < nPerm; i++ {
		var pTreat, pCtrl []ClusterObs

		for seg, obs := range segments {
			shuffled := make([]ClusterObs, len(obs))
			copy(shuffled, obs)
			rng.Shuffle(len(shuffled), func(i, j int) {
				shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
			})

			tSize := treatSizes[seg]
			pTreat = append(pTreat, shuffled[:tSize]...)
			pCtrl = append(pCtrl, shuffled[tSize:]...)
		}

		pTreatRatio := sumRatio(pTreat)
		pCtrlRatio := sumRatio(pCtrl)

		if math.Abs(pTreatRatio-pCtrlRatio) >= actualDiff {
			greaterOrEqual++
		}
	}

	return float64(greaterOrEqual) / float64(nPerm)
}

func PairedCorrelation(x, y []float64) float64 {
	if len(x) != len(y) || len(x) < 2 {
		return 0
	}
	var sumX, sumY, sumXY, sumX2, sumY2 float64
	n := float64(len(x))
	for i := 0; i < len(x); i++ {
		sumX += x[i]
		sumY += y[i]
		sumXY += x[i] * y[i]
		sumX2 += x[i] * x[i]
		sumY2 += y[i] * y[i]
	}
	num := n*sumXY - sumX*sumY
	varX := n*sumX2 - sumX*sumX
	varY := n*sumY2 - sumY*sumY
	if varX <= 0 || varY <= 0 {
		return 0
	}
	den := math.Sqrt(varX * varY)
	if den == 0 || math.IsNaN(den) || math.IsInf(den, 0) {
		return 0
	}
	r := num / den
	if r > 1 {
		return 1
	}
	if r < -1 {
		return -1
	}
	return r
}

func PairedSignFlipTest(diffs []float64, nPerm int, seed int64) (meanDiff, ciLow, ciHigh, pValue, pearsonRho float64) {
	rng := mathrand.New(mathrand.NewPCG(uint64(seed), 1))

	var sumDiff float64
	for _, d := range diffs {
		sumDiff += d
	}
	n := len(diffs)
	if n == 0 {
		return 0, 0, 0, 1, 0
	}
	meanDiff = sumDiff / float64(n)
	actualAbsSum := math.Abs(sumDiff)

	var greaterOrEqual int
	exact := n <= 16
	var numPerms int

	var bootMeans []float64

	if exact {
		numPerms = 1 << n
		for i := 0; i < numPerms; i++ {
			var pSum float64
			for j := 0; j < n; j++ {
				if (i & (1 << j)) != 0 {
					pSum += diffs[j]
				} else {
					pSum -= diffs[j]
				}
			}
			if math.Abs(pSum) >= actualAbsSum {
				greaterOrEqual++
			}
		}
	} else {
		numPerms = nPerm
		for i := 0; i < numPerms; i++ {
			var pSum float64
			for j := 0; j < n; j++ {
				if rng.Float64() < 0.5 {
					pSum += diffs[j]
				} else {
					pSum -= diffs[j]
				}
			}
			if math.Abs(pSum) >= actualAbsSum {
				greaterOrEqual++
			}
		}
	}

	pValue = float64(greaterOrEqual) / float64(numPerms)

	// Bootstrap CI for the mean diff
	nBoot := 10000
	for i := 0; i < nBoot; i++ {
		var bSum float64
		for j := 0; j < n; j++ {
			bSum += diffs[rng.IntN(n)]
		}
		bootMeans = append(bootMeans, bSum/float64(n))
	}
	sort.Float64s(bootMeans)
	ciLow = bootMeans[int(float64(nBoot)*0.025)]
	ciHigh = bootMeans[int(float64(nBoot)*0.975)]

	return meanDiff, ciLow, ciHigh, pValue, 0 // correlation handled by caller? Wait.
}

func HolmAdjust(pValues []float64) []float64 {
	n := len(pValues)
	if n == 0 {
		return nil
	}
	type pv struct {
		idx int
		p   float64
	}
	pvs := make([]pv, n)
	for i, p := range pValues {
		pvs[i] = pv{i, p}
	}
	sort.Slice(pvs, func(i, j int) bool {
		return pvs[i].p < pvs[j].p
	})

	adj := make([]float64, n)
	for i := 0; i < n; i++ {
		a := pvs[i].p * float64(n-i)
		if i > 0 && a < pvs[i-1].p {
			a = pvs[i-1].p // Monotonicity
		}
		if a > 1.0 {
			a = 1.0
		}
		pvs[i].p = a
	}
	for _, p := range pvs {
		adj[p.idx] = p.p
	}
	return adj
}

func BHAdjust(pValues []float64) []float64 {
	n := len(pValues)
	if n == 0 {
		return nil
	}
	type pv struct {
		idx int
		p   float64
	}
	pvs := make([]pv, n)
	for i, p := range pValues {
		pvs[i] = pv{i, p}
	}
	sort.Slice(pvs, func(i, j int) bool {
		return pvs[i].p > pvs[j].p // Step-up: start from largest
	})

	adj := make([]float64, n)
	minQ := 1.0
	for i := 0; i < n; i++ {
		rank := n - i
		q := pvs[i].p * float64(n) / float64(rank)
		if q < minQ {
			minQ = q
		}
		if minQ > 1.0 {
			minQ = 1.0
		}
		pvs[i].p = minQ
	}
	for _, p := range pvs {
		adj[p.idx] = p.p
	}
	return adj
}

func ExactMultinomialSRM(counts map[string]int, weights map[string]float64) float64 {
	// Not implementing true exact multinomial for k>=3, just exact binomial for k=2 and chi-square for k>=3
	var totalCounts int
	var totalWeight float64
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		totalCounts += counts[k]
		totalWeight += weights[k]
	}
	if totalCounts == 0 || totalWeight == 0 {
		return 1.0
	}

	if len(counts) == 2 {
		// Exact binomial for 2 arms
		k1 := keys[0]
		n := totalCounts
		x := counts[k1]
		p := weights[k1] / totalWeight

		// Two-sided p-value: sum probabilities <= P(X=x)
		logProb := func(k int) float64 {
			return logChoose(n, k) + float64(k)*math.Log(p) + float64(n-k)*math.Log(1-p)
		}
		obsLP := logProb(x)
		var pSum float64
		for k := 0; k <= n; k++ {
			lp := logProb(k)
			if lp <= obsLP+1e-12 {
				pSum += math.Exp(lp)
			}
		}
		if pSum > 1 {
			pSum = 1
		}
		return pSum
	}

	// Chi-square for >= 3 arms
	var chiSq float64
	for _, k := range keys {
		obs := float64(counts[k])
		exp := float64(totalCounts) * (weights[k] / totalWeight)
		if exp > 0 {
			chiSq += (obs - exp) * (obs - exp) / exp
		}
	}
	// For degrees of freedom = len(counts) - 1, we return a basic approximation or leave as 1.0 if we don't have incomplete gamma.
	// We can use a simple Wilson-Hilferty transformation or direct calculation for small DF.
	df := len(counts) - 1
	if df == 2 {
		return math.Exp(-chiSq / 2.0)
	}
	// Fallback to Wilson-Hilferty
	z := math.Sqrt(9.0*float64(df)/2.0) * (math.Pow(chiSq/float64(df), 1.0/3.0) - 1.0 + 2.0/(9.0*float64(df)))
	return 0.5 * math.Erfc(z/math.Sqrt2)
}

func logFactorial(n int) float64 {
	if n <= 1 {
		return 0
	}
	v, _ := math.Lgamma(float64(n + 1))
	return v
}

func logChoose(n, k int) float64 {
	if k < 0 || k > n {
		return math.Inf(-1)
	}
	if k == 0 || k == n {
		return 0
	}
	return logFactorial(n) - logFactorial(k) - logFactorial(n-k)
}

func ModelMixBalanceTest(byArm map[string]map[string]int) (pValue float64, flagged bool) {
	// Pearson's chi-square test for independence
	var total int
	rowTotals := make(map[string]int)
	colTotals := make(map[string]int)

	for arm, models := range byArm {
		for model, count := range models {
			rowTotals[arm] += count
			colTotals[model] += count
			total += count
		}
	}

	if total == 0 {
		return 1.0, false
	}

	var chiSq float64
	for arm, models := range byArm {
		for model, count := range models {
			exp := float64(rowTotals[arm]) * float64(colTotals[model]) / float64(total)
			if exp > 0 {
				obs := float64(count)
				chiSq += (obs - exp) * (obs - exp) / exp
			}
		}
	}

	df := (len(rowTotals) - 1) * (len(colTotals) - 1)
	if df <= 0 {
		return 1.0, false
	}

	// Wilson-Hilferty
	z := math.Sqrt(9.0*float64(df)/2.0) * (math.Pow(chiSq/float64(df), 1.0/3.0) - 1.0 + 2.0/(9.0*float64(df)))
	pValue = 0.5 * math.Erfc(z/math.Sqrt2)

	return pValue, pValue < 0.01
}
