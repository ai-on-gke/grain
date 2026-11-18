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
)

type PowerConfig struct {
	PlannedOriginsPerArm int
	Design               string
	PairedRho            float64
}

type PowerPreview struct {
	PowerObserved   float64
	PowerMeaningful float64
	MDE80           float64
}

func SimulatePowerPreview(cfg PowerConfig) PowerPreview {
	rng := mathrand.New(mathrand.NewPCG(42, 1))

	n := cfg.PlannedOriginsPerArm
	if n <= 0 {
		return PowerPreview{}
	}

	replicates := 2000
	sigCountObs := 0
	sigCountMean := 0

	// We assume base standard deviation of the metric is 1.0.
	// For unpaired design, variance of difference of means is (1/n + 1/n) = 2/n.
	// For paired design, variance is 2*(1-rho)/n.
	sdDiff := math.Sqrt(2.0)
	if cfg.Design == "paired" {
		sdDiff = math.Sqrt(2.0 * (1.0 - cfg.PairedRho))
	}
	se := sdDiff / math.Sqrt(float64(n))

	// Holm threshold for the first of 2 tests if there were 2 treatments would be 0.05/2 = 0.025.
	// We'll simulate 1 treatment vs control, so alpha=0.05.
	threshold := 1.95996398454 // z for 0.05 two-sided

	for i := 0; i < replicates; i++ {
		// simulate observed effect delta = 0.2
		zObs := rng.NormFloat64() + 0.2/se
		if math.Abs(zObs) > threshold {
			sigCountObs++
		}
		// simulate meaningful effect delta = 0.5
		zMean := rng.NormFloat64() + 0.5/se
		if math.Abs(zMean) > threshold {
			sigCountMean++
		}
	}

	// MDE for 80% power = (z_alpha/2 + z_beta) * se = (1.96 + 0.8416) * se
	mde80 := 2.80156 * se

	return PowerPreview{
		PowerObserved:   float64(sigCountObs) / float64(replicates),
		PowerMeaningful: float64(sigCountMean) / float64(replicates),
		MDE80:           mde80,
	}
}
