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
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/workflows"
)

// TestCIGrantSpellingsMatchTheRecordedSandboxRepoGrant: schema migration
// 124 rewrote repo_config.ci_capabilities' github-sandbox rows to
// github#sandbox-repo={}, while a workflow's declaration (or the list
// itself, edited by hand) may spell it bare or with the server ID in
// another case. Every such spelling is the recorded grant, so the
// declaration still matches the list and the job's pass still carries it.
// The retired bare github-sandbox is not one of them any longer.
func TestCIGrantSpellingsMatchTheRecordedSandboxRepoGrant(t *testing.T) {
	for _, spelling := range []string{
		model.GitHubSandboxRepoGrant,
		model.GitHubSandboxRepoRef,
		" " + model.GitHubSandboxRepoRef + " ",
		"https://grain.dev/capabilities/GitHub#sandbox-repo",
	} {
		if got := model.CanonicalCIGrant(spelling); got != model.GitHubSandboxRepoGrant {
			t.Errorf("CanonicalCIGrant(%q) = %q, want %q", spelling, got, model.GitHubSandboxRepoGrant)
		}
		if got := workflows.Capabilities.Canonical(spelling); got != workflows.Capabilities.Canonical(model.GitHubSandboxRepoGrant) {
			t.Errorf("workflow Canonical(%q) = %q, want the recorded grant's", spelling, got)
		}
		refs := ciCapabilityRefs([]string{spelling}, ci.EventPush, false, false)
		if len(refs) != 1 || refs[0].Capability != model.GitHubSandboxRepoGrant {
			t.Errorf("ciCapabilityRefs(%q) = %+v, want [%s]", spelling, refs, model.GitHubSandboxRepoGrant)
		}
	}
	if got := model.CanonicalCIGrant("github-sandbox"); got != "github-sandbox" {
		t.Errorf("CanonicalCIGrant(github-sandbox) = %q, want it unchanged: the retired name is no alias any longer", got)
	}
	if refs := ciCapabilityRefs([]string{"github-sandbox"}, ci.EventPush, false, false); len(refs) != 0 {
		t.Errorf("ciCapabilityRefs(github-sandbox) = %+v, want none", refs)
	}
	// Not event-gated: a withheld run (which withholds only event-gated
	// grants) still carries it.
	if refs := ciCapabilityRefs([]string{model.GitHubSandboxRepoRef}, ci.EventManual, false, true); len(refs) != 1 {
		t.Errorf("ciCapabilityRefs on a withheld run = %+v, want the sandbox-repo grant", refs)
	}
	// A different method on the same server is not the sandbox-repo grant.
	if got := model.CanonicalCIGrant(model.GitHubCapabilityServerID + "#read-issues"); got == model.GitHubSandboxRepoGrant {
		t.Errorf("CanonicalCIGrant(github#read-issues) = %q", got)
	}
}
