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

// Package repowrite implements the parameterized CI capability that grants
// a CI sandbox write (push) git proxy access to a specific repository
// (repo-write:<owner>/<repo>).
package repowrite

import (
	"context"
	"fmt"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Provider is the capability provider for one repository's write access in CI.
type Provider struct {
	model.BaseCapability
	repo model.RepoRef
}

// New returns a provider for repo.
func New(repo model.RepoRef) *Provider {
	return &Provider{repo: repo}
}

// Repo returns the repository this provider grants write access to.
func (p *Provider) Repo() model.RepoRef {
	return p.repo
}

// Spec describes this provider's capability.
func (p *Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:        model.RepoWriteCapability(p.repo),
		Label:       "grain-repo-write-" + p.repo.String(),
		Description: fmt.Sprintf("Grant CI push access to %s via the git proxy", p.repo),
		Source:      model.GrantByLabel,
		Provision:   model.ProvisionPlace,
		CIOnly:      true,
	}
}

// SupportsCIEvent restricts write access to post-merge releases and manual workflow_dispatch runs.
func (*Provider) SupportsCIEvent(event string) bool {
	return event == "release" || event == "workflow_dispatch"
}

// SupportsTrustedSchedule lets a cron grain itself fired hold write access
// too: that cron was written into a workflow on the default branch, whose
// job declared this capability, and it runs as the schedule's team, whose
// GitHub credential is what the push goes out with (grain/task-1654). That
// is the same assurance a workflow_dispatch somebody typed carries. A
// caller's own `-event schedule` run is still refused
// (ci.Request.TrustedSchedule).
func (*Provider) SupportsTrustedSchedule() bool {
	return true
}

// DynamicProvider resolves repo-write:<owner>/<repo> capability IDs dynamically.
func DynamicProvider(name string) (model.CapabilityProvider, bool) {
	repo, ok := model.RepoWriteTarget(name)
	if !ok {
		return nil, false
	}
	return New(repo), true
}

func (p *Provider) Resolve(ctx context.Context, cc model.CapabilityContext) (model.Resolution, error) {
	return model.Honoured(), nil
}
