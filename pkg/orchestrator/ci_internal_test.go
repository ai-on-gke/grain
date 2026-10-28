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

package orchestrator

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// What grain says about a run that could not get a job the environment it
// runs in -- a cold content-addressed cache, a registry that would not
// answer, a rebuild that ran out of time (ci.Step.Infrastructure).
//
// The shape of the sentence is the point. It has to open by saying the
// commit is not what went wrong, because that is what stops whoever reads
// it on the pull request from going looking for a bug in their branch --
// and it has to quote the step anyway, because the registry's own refusal
// is the whole of what the person who *does* have to act on it has to go
// on. A run that errored before any job got a machine has no step to
// quote and is unchanged by this: ci.JobRun.Error is its account.
func TestTheCommentForARunThatCouldNotGetAnEnvironmentBlamesNoBranch(t *testing.T) {
	t.Parallel()
	run := ci.Run{
		Repo: "acme/widgets", Ref: "grain/task-1", Commit: "0123456789abcdef",
		Conclusion: ci.ConclusionErrored,
		Jobs: []ci.JobRun{{
			Workflow: "tests", JobID: "kvm", Name: "real KVM sandboxes",
			Conclusion: ci.ConclusionErrored,
			Error: `"Resolve the sandbox guest" could not get this job the environment its steps run in: ` +
				"it did not succeed. Nothing here tested the commit, so this is not a verdict on it",
			Steps: []ci.StepRun{{
				Name: "Resolve the sandbox guest", Conclusion: ci.ConclusionErrored, ExitCode: 1,
				Output: "manifest for ghcr.io/acme/widgets/guest:inputs-deadbeef not found",
			}, {
				Name: "Twenty consecutive two-VM boots", Conclusion: ci.ConclusionSkipped, ExitCode: -1,
			}},
		}},
	}

	body := ciCommentBody(run, nil, nil)
	if !strings.HasPrefix(body, "**grain CI could not run**") {
		opening, _, _ := strings.Cut(body, "\n")
		t.Fatalf("comment opens %q, want it to open by saying grain could not run", opening)
	}
	for _, want := range []string{
		"says nothing",              // whose problem this is
		"not this branch",           // and whose it is not
		"real KVM sandboxes",        // which job
		"Resolve the sandbox guest", // which step
		"inputs-deadbeef not found", // and what that step said
		"could not get its job an environment",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("comment does not mention %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "The first failing step") {
		t.Error("the step is described as a failing step, which is exactly the reading this verdict exists to prevent")
	}

	if detail := ciDetail(run); !strings.Contains(detail, "could not run") ||
		!strings.Contains(detail, "tests/real KVM sandboxes") {
		t.Errorf("stored detail = %q, want it to say grain could not run and name the job", detail)
	}
}
