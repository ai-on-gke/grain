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

package workflows

import "sort"

// A required verdict is judged against the workflows of the branch a pull
// request is going *into*, not only those of the branch it comes from.
//
// Every run reads its workflows off the ref it tests, which is what lets a
// pull request that changes CI be tested by the CI it changes. It is also
// what let a branch switch its own gate off (bwsalmon/grain#2390): a head
// that deletes every file under `.grain/workflows` and `.github/workflows`
// plans zero jobs, a repository declaring no workflow at all is a pass
// (PlanRun), and the merge queue read that pass exactly as it reads a
// suite that ran and passed. Deleting one workflow, or one job out of one,
// was the same thing on a smaller scale.
//
// So a run whose Request names a base (GateOnBase) also reads the base's
// workflows, works out which jobs *they* would run for this same branch
// and event, and fails the run before anything is scheduled if the head
// would not run every one of them. A head may add jobs, change what a job
// does and change which files it is keyed on -- the run still tests that
// with the head's own definitions -- but it cannot make the required
// verdict ask less of it than its base does. A change that really means
// to retire or rename a job fails here, which is the point: it is a
// change to the merge gate, and it needs a person to merge it.
//
// What this does not catch is a job kept under its old name with its
// steps hollowed out. Closing that needs the base's step definitions to
// be what runs, which changes what every workflow-editing pull request is
// tested by; it is left for a decision of its own.

// RemovedGateJobs returns, sorted, the plan ids ("<workflow path>:<job
// id>") of every job base's workflows would run for req that head's
// workflows would not. Empty means head runs at least everything its base
// requires.
//
// It reads which workflows answer req's event and branch the same way
// PlanRun does, and ignores a workflow's own planning error: a file that
// does not parse on the head fails its run anyway, and one that does not
// parse on the base still names the jobs it declared.
func RemovedGateJobs(req Request, base, head []Workflow) []string {
	req = req.Normalize()
	headJobs := gateJobs(req, head)
	var removed []string
	for id := range gateJobs(req, base) {
		if !headJobs[id] {
			removed = append(removed, id)
		}
	}
	sort.Strings(removed)
	return removed
}

func gateJobs(req Request, workflows []Workflow) map[string]bool {
	jobs := map[string]bool{}
	for _, w := range workflows {
		if _, matches := WorkflowEvent(w, req); !matches {
			continue
		}
		for _, job := range w.Jobs {
			jobs[PlanID(w.Path, job.ID)] = true
		}
	}
	return jobs
}
