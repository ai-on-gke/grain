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

package metrics

import (
	"math"
	"slices"
	"sort"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func cleanFloat(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func filterFinite(xs []float64) []float64 {
	out := make([]float64, 0, len(xs))
	for _, x := range xs {
		if isFinite(x) {
			out = append(out, x)
		}
	}
	return out
}

// WilsonCI95 returns the two-sided 95% Wilson score confidence interval for k events in n trials.
func WilsonCI95(k, n int) (low, high float64) {
	if n <= 0 {
		return 0, 0
	}
	if k < 0 {
		k = 0
	}
	if k > n {
		k = n
	}
	const z = 1.959963984540054 // 97.5th percentile of N(0,1)
	z2 := z * z
	nf := float64(n)
	p := float64(k) / nf
	denom := 1.0 + z2/nf
	center := (p + z2/(2.0*nf)) / denom
	half := (z / denom) * math.Sqrt((p*(1.0-p)/nf)+(z2/(4.0*nf*nf)))
	lo := math.Max(0, math.Min(1, center-half))
	hi := math.Max(0, math.Min(1, center+half))
	return cleanFloat(lo), cleanFloat(hi)
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

// FisherExactTwoSided computes the exact two-sided hypergeometric p-value for a 2x2 contingency table:
// Treatment: kT events in nT trials; Control: kC events in nC trials.
func FisherExactTwoSided(kT, nT, kC, nC int) float64 {
	if nT <= 0 || nC <= 0 {
		return 1
	}
	kT = max(0, min(nT, kT))
	kC = max(0, min(nC, kC))
	totalN := nT + nC
	totalK := kT + kC
	if totalK == 0 || totalK == totalN {
		return 1
	}

	logDenom := logChoose(totalN, totalK)
	logProb := func(x int) float64 {
		return logChoose(nT, x) + logChoose(nC, totalK-x) - logDenom
	}

	obsLP := logProb(kT)
	minX := max(0, totalK-nC)
	maxX := min(nT, totalK)

	var pSum float64
	const eps = 1e-12
	for x := minX; x <= maxX; x++ {
		lp := logProb(x)
		if lp <= obsLP+eps {
			pSum += math.Exp(lp)
		}
	}
	if pSum > 1 {
		pSum = 1
	}
	if pSum < 0 {
		pSum = 0
	}
	return cleanFloat(pSum)
}

func waldPooledScoreAbs(x, n1, y, n2 int) float64 {
	totalN := float64(n1 + n2)
	if totalN <= 0 || n1 <= 0 || n2 <= 0 {
		return 0
	}
	totalK := float64(x + y)
	if totalK <= 0 || totalK >= totalN {
		return 0
	}
	pPool := totalK / totalN
	se := math.Sqrt(pPool * (1.0 - pPool) * (1.0/float64(n1) + 1.0/float64(n2)))
	if se <= 0 {
		return 0
	}
	diff := math.Abs(float64(x)/float64(n1) - float64(y)/float64(n2))
	return diff / se
}

// BarnardExactTwoSided computes Barnard's unconditional two-sided exact p-value (using pooled
// Z-score ordering maximized over the nuisance parameter pi in (0,1)) for small/moderate N
// (nT+nC <= 240), and delegates to FisherExactTwoSided above 240 to keep worst-case CPU < 5 ms.
func BarnardExactTwoSided(kT, nT, kC, nC int) float64 {
	if nT <= 0 || nC <= 0 {
		return 1
	}
	kT = max(0, min(nT, kT))
	kC = max(0, min(nC, kC))
	if kT == 0 && kC == 0 || (kT == nT && kC == nC) {
		return 1
	}
	if nT+nC > 240 {
		return FisherExactTwoSided(kT, nT, kC, nC)
	}

	obsStat := waldPooledScoreAbs(kT, nT, kC, nC)
	if obsStat == 0 {
		return 1
	}

	type cell struct {
		x, y   int
		logCmb float64
	}
	var critical []cell
	const eps = 1e-12
	for x := 0; x <= nT; x++ {
		lc1 := logChoose(nT, x)
		for y := 0; y <= nC; y++ {
			if waldPooledScoreAbs(x, nT, y, nC)+eps >= obsStat {
				critical = append(critical, cell{
					x:      x,
					y:      y,
					logCmb: lc1 + logChoose(nC, y),
				})
			}
		}
	}
	if len(critical) == 0 {
		return 1
	}

	evalAt := func(pi float64) float64 {
		if pi <= 0 || pi >= 1 {
			return 0
		}
		lpi := math.Log(pi)
		l1p := math.Log(1.0 - pi)
		var sum float64
		for _, c := range critical {
			k := c.x + c.y
			rem := (nT + nC) - k
			sum += math.Exp(c.logCmb + float64(k)*lpi + float64(rem)*l1p)
		}
		return sum
	}

	const gridSteps = 100
	bestP := 0.0
	bestPi := 0.5
	for i := 1; i < gridSteps; i++ {
		pi := float64(i) / float64(gridSteps)
		if v := evalAt(pi); v > bestP {
			bestP = v
			bestPi = pi
		}
	}
	// Fine local search around the coarse argmax.
	step := 1.0 / float64(gridSteps)
	lo := math.Max(1e-4, bestPi-step)
	hi := math.Min(1.0-1e-4, bestPi+step)
	for i := 0; i <= 20; i++ {
		pi := lo + (hi-lo)*float64(i)/20.0
		if v := evalAt(pi); v > bestP {
			bestP = v
		}
	}
	if bestP > 1 {
		bestP = 1
	}
	return cleanFloat(bestP)
}

// TwoByTwoStratum represents one rollout regime stratum (regime_id) for Cochran-Mantel-Haenszel aggregation.
type TwoByTwoStratum struct {
	RegimeID    int `json:"regimeId"`
	RolloutPct  int `json:"rolloutPct"`
	TreatEvents int `json:"treatEvents"`
	TreatN      int `json:"treatN"`
	CtrlEvents  int `json:"ctrlEvents"`
	CtrlN       int `json:"ctrlN"`
}

// CMHResult summarizes Cochran-Mantel-Haenszel stratified effect size and significance across regimes.
type CMHResult struct {
	Strata         int     `json:"strata"`
	RiskDiff       float64 `json:"riskDiff"`
	RiskDiffCILow  float64 `json:"riskDiffCiLow"`
	RiskDiffCIHigh float64 `json:"riskDiffCiHigh"`
	OddsRatio      float64 `json:"oddsRatio"`
	PValue         float64 `json:"pValue"`
}

// CMHStratified aggregates binary outcomes across rollout-percentage regimes (e.g. 30% -> 50%)
// without Simpson's paradox distortion.
func CMHStratified(strata []TwoByTwoStratum) CMHResult {
	var valid []TwoByTwoStratum
	for _, s := range strata {
		if s.TreatN > 0 && s.CtrlN > 0 {
			valid = append(valid, s)
		}
	}
	if len(valid) == 0 {
		return CMHResult{OddsRatio: 1, PValue: 1}
	}

	var mhWeightSum, mhDiffNum, mhVarNum float64
	var orNum, orDen float64
	var cmhNum, cmhVar float64
	hasZeroCell := false

	for _, s := range valid {
		a := float64(max(0, min(s.TreatN, s.TreatEvents)))
		b := float64(s.TreatN) - a
		c := float64(max(0, min(s.CtrlN, s.CtrlEvents)))
		d := float64(s.CtrlN) - c
		n1 := float64(s.TreatN)
		n2 := float64(s.CtrlN)
		n := n1 + n2

		w := (n1 * n2) / n
		pT := a / n1
		pC := c / n2
		mhWeightSum += w
		mhDiffNum += w * (pT - pC)

		// Sato / Greenland-Robinson variance under stratified binomial sampling.
		vT := 0.0
		if n1 > 1 {
			vT = pT * (1 - pT) / n1
		} else {
			vT = pT * (1 - pT)
		}
		vC := 0.0
		if n2 > 1 {
			vC = pC * (1 - pC) / n2
		} else {
			vC = pC * (1 - pC)
		}
		mhVarNum += w * w * (vT + vC)

		if a == 0 || b == 0 || c == 0 || d == 0 {
			hasZeroCell = true
		}
		orNum += (a * d) / n
		orDen += (b * c) / n

		m1 := a + c
		m0 := b + d
		expectedA := n1 * m1 / n
		cmhNum += (a - expectedA)
		if n > 1 {
			cmhVar += (n1 * n2 * m1 * m0) / (n * n * (n - 1))
		}
	}

	if hasZeroCell || orDen == 0 || orNum == 0 {
		orNum, orDen = 0, 0
		for _, s := range valid {
			a := float64(max(0, min(s.TreatN, s.TreatEvents))) + 0.5
			b := float64(s.TreatN-max(0, min(s.TreatN, s.TreatEvents))) + 0.5
			c := float64(max(0, min(s.CtrlN, s.CtrlEvents))) + 0.5
			d := float64(s.CtrlN-max(0, min(s.CtrlN, s.CtrlEvents))) + 0.5
			n := a + b + c + d
			orNum += (a * d) / n
			orDen += (b * c) / n
		}
	}

	or := 1.0
	if orDen > 0 {
		or = orNum / orDen
	}

	rd := 0.0
	rdLo, rdHi := 0.0, 0.0
	if mhWeightSum > 0 {
		rd = mhDiffNum / mhWeightSum
		se := math.Sqrt(mhVarNum) / mhWeightSum
		const z = 1.959963984540054
		rdLo = math.Max(-1, math.Min(1, rd-z*se))
		rdHi = math.Max(-1, math.Min(1, rd+z*se))
	}

	var pVal float64
	if len(valid) == 1 {
		pVal = BarnardExactTwoSided(valid[0].TreatEvents, valid[0].TreatN, valid[0].CtrlEvents, valid[0].CtrlN)
	} else if cmhVar <= 0 {
		pVal = 1
	} else {
		adj := math.Max(0, math.Abs(cmhNum)-0.5)
		chi2 := (adj * adj) / cmhVar
		pVal = math.Erfc(math.Sqrt(chi2 / 2.0))
		if pVal > 1 {
			pVal = 1
		}
	}

	return CMHResult{
		Strata:         len(valid),
		RiskDiff:       cleanFloat(rd),
		RiskDiffCILow:  cleanFloat(rdLo),
		RiskDiffCIHigh: cleanFloat(rdHi),
		OddsRatio:      cleanFloat(or),
		PValue:         cleanFloat(pVal),
	}
}

// CUPEDSample is one chronologically ordered WorkOrigin observation with a pre-dispatch covariate X
// and observed outcome Y (cost USD or billable tokens).
type CUPEDSample struct {
	Arm string
	X   float64
	Y   float64
	At  time.Time
}

// OnlineCUPEDResult summarizes pre-filtrated Online-CUPED adjusted means and variance reduction.
type OnlineCUPEDResult struct {
	TreatN            int     `json:"treatN"`
	CtrlN             int     `json:"ctrlN"`
	RawTreatMean      float64 `json:"rawTreatMean"`
	RawCtrlMean       float64 `json:"rawCtrlMean"`
	AdjTreatMean      float64 `json:"adjTreatMean"`
	AdjCtrlMean       float64 `json:"adjCtrlMean"`
	AdjDelta          float64 `json:"adjDelta"`
	AdjRelativeDelta  float64 `json:"adjRelativeDelta"`
	CILow             float64 `json:"ciLow"`
	CIHigh            float64 `json:"ciHigh"`
	VarianceReduction float64 `json:"varianceReduction"`
}

// CUPEDScoredSample pairs a chronologically ordered sample with its F_{t-1}-measurable adjusted score YStar.
type CUPEDScoredSample struct {
	CUPEDSample
	Theta float64
	YStar float64
}

// OnlineCUPEDScores computes strictly pre-filtrated (F_{t-1}-measurable) covariate-adjusted scores
// Y*_t = Y_t - theta_{t-1} * (X_t - Xbar_{t-1}) so appending future samples never alters past scores.
func OnlineCUPEDScores(samples []CUPEDSample) []CUPEDScoredSample {
	ordered := slices.Clone(samples)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].At.Before(ordered[j].At)
	})

	out := make([]CUPEDScoredSample, 0, len(ordered))
	// Running Welford accumulators over prior observations 0..t-1.
	priorN := 0
	var meanX, meanY, m2X, cXY float64

	for _, s := range ordered {
		if s.Arm != model.QAArmTreatment && s.Arm != model.QAArmControl {
			continue
		}
		if !isFinite(s.X) || !isFinite(s.Y) {
			continue
		}
		theta := 0.0
		xCenter := s.X
		if priorN >= 2 && m2X > 1e-12 {
			shrink := float64(priorN-1) / float64(priorN+1)
			theta = shrink * (cXY / m2X)
			xCenter = meanX
		}
		yStar := s.Y - theta*(s.X-xCenter)
		out = append(out, CUPEDScoredSample{
			CUPEDSample: s,
			Theta:       cleanFloat(theta),
			YStar:       cleanFloat(yStar),
		})

		// Update F_t history after scoring observation t.
		priorN++
		dx := s.X - meanX
		meanX += dx / float64(priorN)
		dy := s.Y - meanY
		meanY += dy / float64(priorN)
		m2X += dx * (s.X - meanX)
		cXY += dx * (s.Y - meanY)
	}
	return out
}

// OnlineCUPED summarizes pre-filtrated Online-CUPED adjusted means and variance reduction.
func OnlineCUPED(samples []CUPEDSample) OnlineCUPEDResult {
	scored := OnlineCUPEDScores(samples)
	var rawT, rawC []float64
	var adjT, adjC []float64
	var allRaw, allAdj []float64

	for _, s := range scored {
		allRaw = append(allRaw, s.Y)
		allAdj = append(allAdj, s.YStar)
		if s.Arm == model.QAArmTreatment {
			rawT = append(rawT, s.Y)
			adjT = append(adjT, s.YStar)
		} else {
			rawC = append(rawC, s.Y)
			adjC = append(adjC, s.YStar)
		}
	}

	rawMeanT, _ := meanAndSampleVar(rawT)
	rawMeanC, _ := meanAndSampleVar(rawC)
	adjMeanT, adjVarT := meanAndSampleVar(adjT)
	adjMeanC, adjVarC := meanAndSampleVar(adjC)

	delta := 0.0
	relDelta := 0.0
	ciLo, ciHi := 0.0, 0.0
	if len(adjT) > 0 && len(adjC) > 0 {
		delta = adjMeanT - adjMeanC
		if math.Abs(adjMeanC) > 1e-9 {
			relDelta = delta / adjMeanC
		} else if math.Abs(rawMeanC) > 1e-9 {
			relDelta = delta / rawMeanC
		}
		se2 := 0.0
		if len(adjT) > 1 {
			se2 += adjVarT / float64(len(adjT))
		}
		if len(adjC) > 1 {
			se2 += adjVarC / float64(len(adjC))
		}
		se := math.Sqrt(se2)
		const z = 1.959963984540054
		ciLo = delta - z*se
		ciHi = delta + z*se
	}

	_, totalRawVar := meanAndSampleVar(allRaw)
	_, totalAdjVar := meanAndSampleVar(allAdj)
	varRed := 0.0
	if totalRawVar > 1e-12 && len(allAdj) >= 5 {
		varRed = math.Max(0, 1.0-(totalAdjVar/totalRawVar))
	}

	return OnlineCUPEDResult{
		TreatN:            len(adjT),
		CtrlN:             len(adjC),
		RawTreatMean:      cleanFloat(rawMeanT),
		RawCtrlMean:       cleanFloat(rawMeanC),
		AdjTreatMean:      cleanFloat(adjMeanT),
		AdjCtrlMean:       cleanFloat(adjMeanC),
		AdjDelta:          cleanFloat(delta),
		AdjRelativeDelta:  cleanFloat(relDelta),
		CILow:             cleanFloat(ciLo),
		CIHigh:            cleanFloat(ciHi),
		VarianceReduction: cleanFloat(varRed),
	}
}

func meanAndSampleVar(xs []float64) (mean, variance float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	for _, x := range xs {
		mean += x
	}
	mean /= float64(len(xs))
	if len(xs) < 2 {
		return mean, 0
	}
	for _, x := range xs {
		d := x - mean
		variance += d * d
	}
	variance /= float64(len(xs) - 1)
	return mean, variance
}

func sampleMedianSorted(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return 0.5 * (sorted[n/2-1] + sorted[n/2])
}

// HLResult holds Hodges-Lehmann non-parametric location shift and Mann-Whitney U significance.
type HLResult struct {
	TreatN        int     `json:"treatN"`
	CtrlN         int     `json:"ctrlN"`
	TreatMedian   float64 `json:"treatMedian"`
	CtrlMedian    float64 `json:"ctrlMedian"`
	Shift         float64 `json:"shift"`
	ShiftCILow    float64 `json:"shiftCiLow"`
	ShiftCIHigh   float64 `json:"shiftCiHigh"`
	RelativeShift float64 `json:"relativeShift"`
	MannWhitneyP  float64 `json:"mannWhitneyP"`
}

// HodgesLehmannShift computes the non-parametric median pairwise difference (treat_i - ctrl_j),
// Moses 95% rank-order confidence bounds, and two-sided Mann-Whitney U p-value.
func HodgesLehmannShift(treat, ctrl []float64) HLResult {
	tSorted := filterFinite(treat)
	cSorted := filterFinite(ctrl)
	sort.Float64s(tSorted)
	sort.Float64s(cSorted)

	res := HLResult{
		TreatN:       len(tSorted),
		CtrlN:        len(cSorted),
		TreatMedian:  cleanFloat(sampleMedianSorted(tSorted)),
		CtrlMedian:   cleanFloat(sampleMedianSorted(cSorted)),
		MannWhitneyP: 1,
	}
	if len(tSorted) == 0 || len(cSorted) == 0 {
		return res
	}

	n1 := len(tSorted)
	n2 := len(cSorted)
	if float64(n1)*float64(n2) > 250_000 {
		// Subsample deterministically to bound worst-case pairwise allocation.
		tSorted = strideSample(tSorted, 500)
		cSorted = strideSample(cSorted, 500)
		n1 = len(tSorted)
		n2 = len(cSorted)
	}

	diffs := make([]float64, 0, n1*n2)
	var uStat float64
	for _, y := range tSorted {
		for _, x := range cSorted {
			d := y - x
			diffs = append(diffs, d)
			if d > 0 {
				uStat += 1.0
			} else if d == 0 {
				uStat += 0.5
			}
		}
	}
	sort.Float64s(diffs)
	shift := sampleMedianSorted(diffs)
	res.Shift = cleanFloat(shift)
	if res.CtrlMedian > 1e-9 {
		res.RelativeShift = cleanFloat(shift / res.CtrlMedian)
	}

	m := len(diffs)
	fn1, fn2 := float64(n1), float64(n2)
	meanU := (fn1 * fn2) / 2.0
	varU := (fn1 * fn2 * (fn1 + fn2 + 1.0)) / 12.0
	seU := math.Sqrt(varU)
	if seU > 0 {
		const z = 1.959963984540054
		kRank := int(math.Floor(meanU - z*seU))
		if kRank < 0 {
			kRank = 0
		}
		hiIdx := m - 1 - kRank
		if hiIdx >= m {
			hiIdx = m - 1
		}
		if hiIdx < kRank {
			hiIdx = kRank
		}
		res.ShiftCILow = cleanFloat(diffs[kRank])
		res.ShiftCIHigh = cleanFloat(diffs[hiIdx])

		adj := math.Max(0, math.Abs(uStat-meanU)-0.5)
		zScore := adj / seU
		res.MannWhitneyP = cleanFloat(math.Min(1, math.Erfc(zScore/math.Sqrt2)))
	} else {
		res.ShiftCILow = res.Shift
		res.ShiftCIHigh = res.Shift
	}
	return res
}

func strideSample(sorted []float64, capN int) []float64 {
	if len(sorted) <= capN || capN <= 0 {
		return sorted
	}
	out := make([]float64, 0, capN)
	for i := 0; i < capN; i++ {
		idx := (i * (len(sorted) - 1)) / (capN - 1)
		out = append(out, sorted[idx])
	}
	return out
}

// MSPRTResult holds an anytime-valid Mixture Sequential Probability Ratio Test summary.
type MSPRTResult struct {
	N               int     `json:"n"`
	MeanDiff        float64 `json:"meanDiff"`
	LikelihoodRatio float64 `json:"likelihoodRatio"`
	PSeq            float64 `json:"pSeq"`
	HalfWidth95     float64 `json:"halfWidth95"`
	Significant     bool    `json:"significant"`
}

// NormalMixtureSPRT computes Johari et al.'s normal-mixture sequential likelihood ratio Lambda_n
// and anytime-valid sequential p-value p_seq = min(1, 1/Lambda_n) at alpha = 0.05.
func NormalMixtureSPRT(treat, ctrl []float64, tau float64, alpha float64) MSPRTResult {
	tFinite := filterFinite(treat)
	cFinite := filterFinite(ctrl)
	n1, n2 := len(tFinite), len(cFinite)
	if n1 < 2 || n2 < 2 {
		return MSPRTResult{N: n1 + n2, LikelihoodRatio: 1, PSeq: 1}
	}
	mT, vT := meanAndSampleVar(tFinite)
	mC, vC := meanAndSampleVar(cFinite)
	diff := mT - mC
	vN := vT/float64(n1) + vC/float64(n2)
	if vN <= 1e-12 {
		if math.Abs(diff) < 1e-12 {
			return MSPRTResult{N: n1 + n2, MeanDiff: 0, LikelihoodRatio: 1, PSeq: 1}
		}
		return MSPRTResult{N: n1 + n2, MeanDiff: cleanFloat(diff), LikelihoodRatio: 100, PSeq: 0.01, Significant: true}
	}
	if tau <= 0 || !isFinite(tau) {
		// Scale default mixing SD to unit-level pooled standard deviation (independent of sample count n)
		// so vN / (vN + tau^2) shrinks as O(1/n) and preserves anytime-valid Type-I error control.
		unitSD := math.Sqrt(0.5 * (vT + vC))
		tau = math.Max(0.05, 0.5*unitSD)
	}
	tau2 := tau * tau
	ratio := vN / (vN + tau2)
	exponent := (tau2 * diff * diff) / (2.0 * vN * (vN + tau2))
	// Cap exponent to avoid float64 overflow on extreme separations.
	if exponent > 50 {
		exponent = 50
	}
	lambda := math.Sqrt(ratio) * math.Exp(exponent)
	if lambda < 1e-12 {
		lambda = 1e-12
	}
	pSeq := math.Min(1.0, 1.0/lambda)
	if alpha <= 0 {
		alpha = 0.05
	}
	logTerm := math.Max(0, -2.0*math.Log(alpha)-math.Log(ratio))
	hw := math.Sqrt(((vN * (vN + tau2)) / tau2) * logTerm)

	return MSPRTResult{
		N:               n1 + n2,
		MeanDiff:        cleanFloat(diff),
		LikelihoodRatio: cleanFloat(lambda),
		PSeq:            cleanFloat(pSeq),
		HalfWidth95:     cleanFloat(hw),
		Significant:     pSeq < alpha,
	}
}
