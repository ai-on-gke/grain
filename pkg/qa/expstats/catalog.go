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

type MetricSpec struct {
	ID      string
	Label   string
	Tip     string
	Unit    string
	Level   string
	Better  string
	Roles   []string
	Source  string
	Test    string
	MinN    int
	Version int
}

func Catalog() []MetricSpec {
	return []MetricSpec{
		{ID: "review.findings_sev.per_pr", Label: "Findings (Sev/PR)", Better: "lower"},
		{ID: "review.findings_sev.per_kloc", Label: "Findings (Sev/kLOC)", Better: "lower"},
		{ID: "qa.escape_rate.blind_ex_holds", Label: "Escape Rate (Blind, ex Holds)", Better: "lower"},
		{ID: "qa.escape_class_mix", Label: "Escape Class Mix", Better: "lower"},
		{ID: "quality.panel_score", Label: "Panel Score", Better: "higher"},
		{ID: "quality.mutation_score.added_lines", Label: "Mutation Score (Added Lines)", Better: "higher"},
		{ID: "bench.hidden_acceptance_pass", Label: "Hidden Acceptance Pass", Better: "higher"},
		{ID: "quality.added_line_coverage", Label: "Added Line Coverage", Better: "higher"},
		{ID: "quality.scope_creep", Label: "Scope Creep", Better: "lower"},
		{ID: "flow.pr_shape", Label: "PR Shape", Better: "higher"},
		{ID: "flow.time_to_pr", Label: "Time to PR", Better: "lower"},
		{ID: "flow.time_to_merge.excl_hold", Label: "Time to Merge (excl Hold)", Better: "lower"},
		{ID: "flow.tokens.per_task", Label: "Tokens / Task", Better: "lower"},
		{ID: "flow.overhead", Label: "Overhead", Better: "lower"},
		{ID: "qa.held_rate", Label: "Held Rate", Better: "lower"},
		{ID: "qa.check.precision", Label: "Check Precision", Better: "higher"},
		{ID: "qa.check.matched_recall", Label: "Check Matched Recall", Better: "higher"},
		{ID: "qa.check.noop_remediation", Label: "Check Noop Remediation", Better: "lower"},
		{ID: "qa.check.applicability", Label: "Check Applicability", Better: "higher"},
		{ID: "qa.s4.coverage", Label: "S4 Coverage", Better: "higher"},
		{ID: "flow.blindness_leaks", Label: "Blindness Leaks", Better: "lower"},
		{ID: "legacy.escape.blind", Label: "Legacy Escape Blind", Better: "lower"},
	}
}

func LookupMetric(id string) (MetricSpec, bool) {
	for _, m := range Catalog() {
		if m.ID == id {
			return m, true
		}
	}
	return MetricSpec{}, false
}
