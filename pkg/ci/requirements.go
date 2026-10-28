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
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/workflows"
)

// What a job requires and what it is given moved to pkg/workflows
// (requirements.go there) with the rest of the planner. What stays is
// the half that reads grain's own capability grammar, which pkg/ci
// installs into workflows.Capabilities (workflows.go).

var (
	jobCapabilities        = workflows.JobCapabilities
	jobSecrets             = workflows.JobSecrets
	unmetRequirements      = workflows.UnmetRequirements
	unmetRequirementsError = workflows.UnmetRequirementsError
)

// JobSecrets is jobSecrets for a caller outside this package, for the
// same reason Engine.JobCapabilities is.
func JobSecrets(job Job, provided []string) []string { return jobSecrets(job, provided) }

// declaredOnly reports whether id is granted by a job declaring it,
// subject to an authority other than the repository's CI capabilities
// list: repository access, capability references (jobCapabilities), and
// api grants (`api:<scope>`, `api:<preset>`) with release-gate-read, the
// alias for the api:release-gate-read preset, whose provider admits only
// trusted events of the repositories it was configured for, and only
// the scopes a CI job may hold (pkg/capability/ciapi) -- the release
// workflow that declares it runs on grain's own repository whether or
// not an operator has listed anything for it.
func declaredOnly(id string) bool {
	return IsRepoAccessCapability(id) || model.IsCapabilityRef(id) || id == model.ReleaseGateReadCapability ||
		strings.HasPrefix(strings.TrimSpace(id), model.APIGrantPrefix)
}

// IsRepoAccessCapability reports whether id is a repository access
// capability -- `repo-read:<owner>/<repo>` or `repo-write:<owner>/<repo>`
// -- which a job's own declaration allows and its team's GitHub
// credential enforces, rather than one the deployment grants
// (grain/task-1654).
func IsRepoAccessCapability(id string) bool {
	if _, ok := model.RepoReadTarget(id); ok {
		return true
	}
	_, ok := model.RepoWriteTarget(id)
	return ok
}
