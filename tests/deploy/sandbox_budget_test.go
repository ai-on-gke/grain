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

package deploy

import (
	"strings"
	"testing"
)

// admitsNothing is `grain sandbox-budget` on the 10-28 walkthrough's
// 2 vCPU / 7.8 GiB e2-standard-2 (pkg/cli's
// TestSandboxBudgetReportsAHostThatAdmitsNothing pins the same output).
const admitsNothing = "admits-nothing: no run can ever be admitted, so every approved task would stay queued: " +
	"3994 MiB of sandbox memory (7987 MiB host less 3993 MiB reserved, at 1.00x) cannot hold one 8192 MiB sandbox; " +
	"0.0 vCPU of sandbox cpu (2 host vCPU less the controller's reserve, at 2.00x) cannot hold one 2 vCPU sandbox. " +
	"Lower the reserve or the sandbox shape in Settings → Sandbox\n" +
	"fix: grain settings -sandbox-cpu-overcommit 0 -sandbox-memory-mb 6144 -sandbox-memory-reserve-mb 1792\n"

// The walkthrough's bug: setup.sh's readiness report said nothing on a
// host whose sandbox budget can never admit a run, where grain init
// reports it as a manual step. It has to ask the daemon the same
// question, print a !! block with the fix, and count it against
// "all runtime prerequisites are in place".
func TestTheReadinessReportSaysWhenNoRunCanBeAdmitted(t *testing.T) {
	problem := runBash(t, shellFunction(t, "report_sandbox_budget_problem")+`report_sandbox_budget_problem "$BUDGET"`,
		"BUDGET="+admitsNothing)
	if !strings.HasPrefix(problem, "    !! no run can ever be admitted") {
		t.Errorf("problem block = %q, want it to open with a !! line", problem)
	}
	for _, line := range strings.Split(strings.TrimRight(problem, "\n"), "\n")[1:] {
		if !strings.HasPrefix(line, "       ") {
			t.Errorf("continuation line %q is not indented under the !!", line)
		}
	}
	contains(t, problem, "7987 MiB host less 3993 MiB reserved")
	contains(t, problem, "To fit one, run: grain settings -sandbox-cpu-overcommit 0 -sandbox-memory-mb 6144 -sandbox-memory-reserve-mb 1792\n")

	// A host no setting can fix gets the finding and no command.
	tooSmall := runBash(t, shellFunction(t, "report_sandbox_budget_problem")+`report_sandbox_budget_problem "$BUDGET"`,
		"BUDGET=admits-nothing: no run can ever be admitted -- though this host has too little memory left for even a 1 GiB sandbox\n")
	contains(t, tooSmall, "too little memory")
	absent(t, tooSmall, "To fit one")

	summary := shellFunction(t, "sandbox_budget_summary")
	contains(t, runBash(t, summary+`sandbox_budget_summary "$BUDGET"`, "BUDGET="+admitsNothing), "NONE -- ")
	if got := runBash(t, summary+`sandbox_budget_summary "$BUDGET"`,
		"BUDGET=fits: one 8192 MiB sandbox fits in 57344 MiB of sandbox memory\n"); got != "one 8192 MiB sandbox fits in 57344 MiB of sandbox memory\n" {
		t.Errorf("fits summary = %q", got)
	}
	contains(t, runBash(t, summary+`sandbox_budget_summary ""`), "not read")

	readiness := body(t, setupCode(t), "report_readiness() {")
	contains(t, readiness, "grain_cli -connect-timeout 30s -timeout 30s sandbox-budget")
	contains(t, readiness, `if [[ "$budget" == admits-nothing:* ]]; then`+"\n    ready=0\n    report_sandbox_budget_problem \"$budget\"")
	contains(t, readiness, `$(sandbox_budget_summary "$budget")`)
	// Single-tenant: there are no teams with counts of their own.
	absent(t, readiness, "each team's own")
	absent(t, readiness, "Team settings")
}
