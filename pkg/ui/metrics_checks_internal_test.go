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

package ui

import (
	"encoding/json"
	"github.com/ai-on-gke/grain/pkg/metrics"
	"github.com/ai-on-gke/grain/pkg/model"
	"testing"
	"time"
)

func TestMetricsChecksWirePreservesProvidersAndPushCoverage(t *testing.T) {
	now := time.Now()
	rep := metrics.Compute(metrics.Input{
		Window: metrics.Window{Since: now.Add(-time.Hour), Until: now.Add(time.Hour)},
		Runs:   []model.RunTiming{{RunID: "cli", FinishedAt: &now}, {RunID: "census", FinishedAt: &now}},
		CheckWaits: []model.RunCheckWait{
			{RunID: "cli", Verdict: "passed", Provider: model.CIProviderGitHub, Waited: time.Minute},
			{RunID: "census", Verdict: "passed", Provider: model.CIProviderGrain, Waited: 2 * time.Minute, PushesBeforeKnown: true},
		},
	})
	data, err := json.Marshal(metricsReportFrom(rep, false))
	if err != nil {
		t.Fatal(err)
	}
	var wire MetricsReport
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	c := wire.Checks
	if c.GreenRuns != 2 || c.PushesToGreen.N != 1 || c.PushesToGreen.Mean != 0 || c.Waits != 2 || c.Blocked.N != 2 {
		t.Fatalf("wire coverage = %+v, want one known zero over two green runs", c)
	}
	if len(c.ByProvider) != 2 || c.ByProvider[0].Provider != "grain" || c.ByProvider[0].Waits != 1 || c.ByProvider[1].Provider != "github" || c.ByProvider[1].Waits != 1 || c.ByProvider[1].Verdicts["passed"] != 1 || c.ByProvider[1].Blocked.MeanSeconds != 60 {
		t.Fatalf("wire providers = %+v", c.ByProvider)
	}
}
