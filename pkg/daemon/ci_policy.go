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
	"fmt"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/policy"
)

// CheckCIJobPolicy is the local policy (-policy, pkg/policy) asked of one
// CI job before anything is built or materialized for it: nil when the
// job's claims fit what this machine's owner will run, and otherwise the
// error naming the first one the policy refuses (GSA1-07, silo INV-12).
//
// A job is judged the way a task is (policy.Policy.CheckClaims), as the
// task it would be: the repository under test as its target, every
// capability it carries as a grant -- including the `repo-read:` and
// `repo-write:` a workflow on a pushed branch declares for itself, whose
// repositories answer to repos: as well as to capabilities: -- and the
// network every CI sandbox is given, which is open (CiSandboxes.Acquire).
// Approval is not asked: a CI run is grain's own work, started by a push,
// a pull request, a schedule or a person here, never a request to accept.
//
// Every capability the job carries is judged, including one its event
// will not hold (a `repo-write:` on a pull request): what is refused is
// the workflow's declaration, whichever event happens to run it. The
// grain-ci capability ResolveCICapabilities adds to every job on its own
// is not the job's claim and is not judged, whether or not a repository's
// list names it too.
//
// A nil policy -- a daemon with no -policy -- allows every job, as it
// allows every task.
func CheckCIJobPolicy(p *policy.Policy, req ci.SandboxRequest) error {
	if p == nil {
		return nil
	}
	repo, err := model.ParseRepo(req.Repo)
	if err != nil {
		return fmt.Errorf("%q is not a repository local policy can judge: %w", req.Repo, err)
	}
	task := model.Task{Target: &repo, NetworkAccess: model.NetworkOpen}
	for _, id := range req.Capabilities {
		if id == grainci.CapabilityName {
			continue
		}
		task.Grants = append(task.Grants, model.Grant{Capability: id, Via: model.GrantByDefault})
	}
	return p.CheckClaims(task, model.NetworkOpen)
}

// ciPolicyRefusal is CheckCIJobPolicy for the two places a job is built
// (CiGrantReservations.Acquire, CiSandboxes.Acquire): the refusal said
// once in the daemon log, where an operator whose CI jobs all stop on
// their policy can see why, and returned as the job's own error.
func ciPolicyRefusal(p *policy.Policy, req ci.SandboxRequest, logf func(string, ...any)) error {
	err := CheckCIJobPolicy(p, req)
	if err == nil {
		return nil
	}
	if logf != nil {
		logf("grain ci: %s: %s job %s refused by local policy (-policy): %v", req.Repo, req.Event, req.Name, err)
	}
	return fmt.Errorf("local policy refuses this CI job: %w", err)
}
