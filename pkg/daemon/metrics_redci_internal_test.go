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

package daemon

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// read_grain_metrics' checks section names the red-CI procedure's two
// shares, so a deployment can watch them move after the prompt changed.
func TestPrintChecksReportsTheRedCIProcedure(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	printChecks(&out, ui.MetricsChecks{RedCI: ui.MetricsRedCI{
		FixingRuns: 4, Narrowed: 1, NarrowedShare: 0.25, Selective: 3, SelectiveShare: 0.75,
	}})
	text := out.String()
	for _, want := range []string{
		"red-CI procedure (4 CI-fixing run(s)",
		"1 (25%) ran a narrowed local test before the fixing push",
		"3 (75%) used run_ci_job, retrigger_ci or wait_for_checks with jobs",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("checks section is missing %q:\n%s", want, text)
		}
	}

	out.Reset()
	printChecks(&out, ui.MetricsChecks{})
	if out.Len() != 0 {
		t.Errorf("a window with nothing to report printed:\n%s", out.String())
	}
}

func TestPrintChecksReportsPushCoverageAndProviders(t *testing.T) {
	for _, tc := range []struct {
		name  string
		count ui.MetricsCount
		want  string
	}{
		{name: "unknown", want: "unavailable (0 of 3 green run(s) with known push counts)"},
		{name: "known zero", count: ui.MetricsCount{N: 1}, want: "0.0 on average, 0 at worst (1 of 3 green run(s) with known push counts)"},
		{name: "known positive", count: ui.MetricsCount{N: 2, Mean: 2, Max: 4}, want: "2.0 on average, 4 at worst (2 of 3 green run(s) with known push counts)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			printChecks(&out, ui.MetricsChecks{Waits: 4, Runs: 3, GreenRuns: 3, PushesToGreen: tc.count,
				ByProvider: []ui.MetricsProviderChecks{{Provider: "github", Waits: 3, Runs: 2, Verdicts: map[string]int{"passed": 3}}, {Provider: "grain", Waits: 1, Runs: 1, Verdicts: map[string]int{"passed": 1}}},
			})
			got := out.String()
			if !strings.Contains(got, tc.want) || !strings.Contains(got, "github") || !strings.Contains(got, "grain") || !strings.Contains(got, "3 wait(s) across 2 run(s)") {
				t.Fatalf("report = %s, want coverage %q and both providers", got, tc.want)
			}
			if tc.count.N == 0 && strings.Contains(got, "0.0 on average") {
				t.Fatal("unknown pushes reported as zero")
			}
		})
	}
}
