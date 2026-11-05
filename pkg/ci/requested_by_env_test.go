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

package ci

import (
	"encoding/json"
	"testing"
)

// A person-only workflow (.grain/workflows/promote-release.yml) is told
// who asked only by grain's own callers: the variables appear when
// Request.RequestedByKind is set, never otherwise, and no JSON caller can
// set it -- or every other job's key would move with it.
func TestRequestedByKindReachesStepsOnlyFromGrainsOwnCallers(t *testing.T) {
	plain := runEnv(Request{Repo: "a/b", Ref: "main", RequestedBy: "alice"}, "w", "j")
	for _, k := range []string{RequestedByKindEnv, RequestedByEnv, ReleaseVersionEnv,
		ReleaseShortSoakOKEnv, ReleaseAllowBreakingEnv, ReleaseRollbackToEnv, ReleaseRollbackReasonEnv} {
		if _, ok := plain[k]; ok {
			t.Errorf("%s is set on a run nobody vouched for: %v", k, plain)
		}
	}
	vouched := runEnv(Request{Repo: "a/b", Ref: "main", RequestedBy: "alice@example.com",
		RequestedByKind: "human", ReleaseVersion: "v2.4.0", ReleaseShortSoakOK: true, ReleaseAllowBreaking: true,
		ReleaseRollbackTo: "v2.3.0", ReleaseRollbackReason: "bad schema"}, "w", "j")
	if vouched[RequestedByKindEnv] != "human" || vouched[RequestedByEnv] != "alice@example.com" ||
		vouched[ReleaseVersionEnv] != "v2.4.0" || vouched[ReleaseShortSoakOKEnv] != "true" ||
		vouched[ReleaseAllowBreakingEnv] != "true" || vouched[ReleaseRollbackToEnv] != "v2.3.0" ||
		vouched[ReleaseRollbackReasonEnv] != "bad schema" {
		t.Errorf("a vouched run's env = %v, want the person and the release", vouched)
	}

	var req Request
	if err := json.Unmarshal([]byte(`{"Repo":"a/b","Ref":"main","RequestedByKind":"human","ReleaseVersion":"v2.4.0","ReleaseShortSoakOK":true,"ReleaseAllowBreaking":true,"ReleaseRollbackTo":"v2.3.0","ReleaseRollbackReason":"forged"}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.RequestedByKind != "" || req.ReleaseVersion != "" || req.ReleaseShortSoakOK || req.ReleaseAllowBreaking ||
		req.ReleaseRollbackTo != "" || req.ReleaseRollbackReason != "" {
		t.Errorf("JSON set RequestedByKind=%q ReleaseVersion=%q ReleaseShortSoakOK=%v ReleaseAllowBreaking=%v ReleaseRollbackTo=%q ReleaseRollbackReason=%q, want none settable by a caller",
			req.RequestedByKind, req.ReleaseVersion, req.ReleaseShortSoakOK, req.ReleaseAllowBreaking, req.ReleaseRollbackTo, req.ReleaseRollbackReason)
	}

	// A workflow, job or step env: block may override ordinary variables
	// like GRAIN_CI_REF, but may neither forge nor replace the caller
	// identity variables that person-only release workflows read.
	forgedLayers := PlannedJob{
		Workflow: "w",
		WorkflowEnv: map[string]string{
			RequestedByKindEnv:       "human",
			RequestedByEnv:           "forged@example.com",
			ReleaseVersionEnv:        "v9.9.0",
			ReleaseShortSoakOKEnv:    "true",
			ReleaseAllowBreakingEnv:  "true",
			ReleaseRollbackToEnv:     "v1.0.0",
			ReleaseRollbackReasonEnv: "forged-workflow",
			"GRAIN_CI_REF":           "overridden-ref",
		},
		Job: Job{
			ID: "j",
			Env: map[string]string{
				RequestedByKindEnv:       "human",
				RequestedByEnv:           "forged-job@example.com",
				ReleaseVersionEnv:        "v9.9.1",
				ReleaseShortSoakOKEnv:    "true",
				ReleaseAllowBreakingEnv:  "true",
				ReleaseRollbackToEnv:     "v1.0.1",
				ReleaseRollbackReasonEnv: "forged-job",
			},
		},
	}
	forgedStep := Step{
		Env: map[string]string{
			RequestedByKindEnv:       "human",
			RequestedByEnv:           "forged-step@example.com",
			ReleaseVersionEnv:        "v9.9.2",
			ReleaseShortSoakOKEnv:    "true",
			ReleaseAllowBreakingEnv:  "true",
			ReleaseRollbackToEnv:     "v1.0.2",
			ReleaseRollbackReasonEnv: "forged-step",
		},
	}
	unvouchedStepEnv := stepEnv(Plan{Request: Request{Repo: "a/b", Ref: "main", RequestedBy: "agent-1"}}, forgedLayers, forgedStep)
	for _, k := range []string{RequestedByKindEnv, RequestedByEnv, ReleaseVersionEnv,
		ReleaseShortSoakOKEnv, ReleaseAllowBreakingEnv, ReleaseRollbackToEnv, ReleaseRollbackReasonEnv} {
		if got, ok := unvouchedStepEnv[k]; ok {
			t.Errorf("stepEnv let workflow/job/step env set %s=%q on an unvouched run", k, got)
		}
	}
	if got := unvouchedStepEnv["GRAIN_CI_REF"]; got != "overridden-ref" {
		t.Errorf("stepEnv[GRAIN_CI_REF] = %q, want overridden-ref", got)
	}

	vouchedStepEnv := stepEnv(Plan{Request: Request{
		Repo: "a/b", Ref: "main", RequestedBy: "alice@example.com",
		RequestedByKind: "human", ReleaseVersion: "v2.4.0",
	}}, forgedLayers, forgedStep)
	if vouchedStepEnv[RequestedByKindEnv] != "human" ||
		vouchedStepEnv[RequestedByEnv] != "alice@example.com" ||
		vouchedStepEnv[ReleaseVersionEnv] != "v2.4.0" {
		t.Errorf("stepEnv let workflow/job/step env override vouched identity: %v", vouchedStepEnv)
	}
	if _, ok := vouchedStepEnv[ReleaseShortSoakOKEnv]; ok {
		t.Errorf("stepEnv let workflow/job/step env accept a short soak no person accepted: %v", vouchedStepEnv)
	}
	if _, ok := vouchedStepEnv[ReleaseAllowBreakingEnv]; ok {
		t.Errorf("stepEnv let workflow/job/step env accept breaking changes no person accepted: %v", vouchedStepEnv)
	}
	if _, ok := vouchedStepEnv[ReleaseRollbackToEnv]; ok {
		t.Errorf("stepEnv let workflow/job/step env set a rollback target no person asked for: %v", vouchedStepEnv)
	}
	if _, ok := vouchedStepEnv[ReleaseRollbackReasonEnv]; ok {
		t.Errorf("stepEnv let workflow/job/step env set a rollback reason no person wrote: %v", vouchedStepEnv)
	}
}
