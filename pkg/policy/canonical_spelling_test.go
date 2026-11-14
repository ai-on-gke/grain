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

package policy

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// canonicalSpellingCorpus is every grant spelling the retired patterns
// below could plausibly be asked to judge: each one's legacy, reference
// and grant forms, with and without arguments, and neighbours they must
// not reach.
var canonicalSpellingCorpus = []string{
	model.GrainCICapability, model.GrainCIRef, model.GrainCIGrant,
	model.GrainCIRef + `={"repo":"acme/widgets"}`,
	"self-debug", "api:self-debug", model.APIRef("self-debug"), model.APIRef("self-debug") + "={}",
	model.APIGrantString("self-debug", "acme/widgets"), "api:self-debug@acme/widgets",
	model.GrainSourceRef, model.GrainSourceGrant,
	"deployment-health", "api:deployment:read", model.APIRef("deployment:read"), model.APIRef("deployment:read") + "={}",
	model.APIRef("deployment:write"), "api:deployment:write",
	model.GrainHealthRef, model.GrainHealthGrant,
	"api:ci-read", model.APIRef("ci-read"), model.APIRef("ci-read") + "={}",
	"api:ci-read@acme/widgets", model.APIGrantString("ci-read", "acme/widgets"),
	model.APIRef("tasks:read"), "api:tasks:read", model.GrainJournalGrant,
	"durable", "gcp-key", model.ConfigMapRef + `={"config_maps":["bootstrap"]}`,
}

// TestRetiredAndCanonicalPatternsAdmitTheSameGrants holds each retired
// spelling an older `grain init workstation` wrote into grain_policy.yml
// against the canonical patterns it writes now: every spelling of every
// grant in the corpus gets the same answer from both, so a policy a
// re-run keeps behaves exactly like one it writes afresh.
//
// An allow entry is written as the exact grant, =<json> and all
// (model.DirectorPolicyCapabilities), since a retired name allowed only
// the grant it became. A deny entry replacing a retired bare name is the
// bare reference, since such a name denied its method whatever the
// arguments; one replacing an api:<scope> is the exact grant, since
// api:<scope> denied only the unscoped grant.
func TestRetiredAndCanonicalPatternsAdmitTheSameGrants(t *testing.T) {
	for _, tc := range []struct {
		name             string
		old, allow, deny []string
	}{
		{"grain-ci", []string{"grain-ci"}, []string{model.GrainCIGrant}, []string{model.GrainCIRef}},
		// A retired name that became several grants is one pattern per grant.
		{"self-debug", []string{"self-debug"},
			[]string{model.APIRef("self-debug") + "={}", model.GrainSourceGrant},
			[]string{model.APIRef("self-debug"), model.GrainSourceRef}},
		{"deployment-health", []string{"deployment-health"},
			[]string{model.APIRef("deployment:read") + "={}", model.GrainHealthGrant},
			[]string{model.APIRef("deployment:read"), model.GrainHealthRef}},
		{"api:deployment:read", []string{"api:deployment:read"},
			[]string{model.APIRef("deployment:read") + "={}"}, []string{model.APIRef("deployment:read") + "={}"}},
		{"api:ci-read", []string{"api:ci-read"}, []string{model.APIRef("ci-read") + "={}"}, []string{model.APIRef("ci-read") + "={}"}},
		{"api:self-debug", []string{"api:self-debug"}, []string{model.APIRef("self-debug") + "={}"}, []string{model.APIRef("self-debug") + "={}"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			admitted := 0
			for _, g := range canonicalSpellingCorpus {
				oldAllow, newAllow := allowsCapability(tc.old, g), allowsCapability(tc.allow, g)
				if oldAllow != newAllow {
					t.Errorf("allow %q admits %s: %v, but allow %q: %v", tc.old, g, oldAllow, tc.allow, newAllow)
				}
				if oldAllow {
					admitted++
				}
				if tc.deny == nil {
					continue
				}
				if oldDeny, newDeny := deniesCapability(tc.old, g), deniesCapability(tc.deny, g); oldDeny != newDeny {
					t.Errorf("deny %q refuses %s: %v, but deny %q: %v", tc.old, g, oldDeny, tc.deny, newDeny)
				}
			}
			if admitted == 0 {
				t.Errorf("allow %q admits nothing in the corpus: the comparison proves nothing", tc.old)
			}
		})
	}
}

// TestTheStarterPolicyAdmitsWhatItsOlderSpellingDid holds the allow list an
// older `grain init workstation` wrote, retired spellings and all, against
// the one it writes now (grain#ci={} and the Director's references): the
// new list admits everything the old one did, and the only grants it
// admits beyond that are repo-scoped api grants, the
// https://grain.dev/capabilities/api#ci-read {repo} grain gives every task
// on a repository grain CI does not gate among them.
func TestTheStarterPolicyAdmitsWhatItsOlderSpellingDid(t *testing.T) {
	bootstrap := model.ConfigMapRef + `={"config_maps":["bootstrap"]}`
	old := []string{"grain-ci", "self-debug", model.GrainJournalRef, "api:deployment:read", model.GrainHealthRef, bootstrap, "api:ci-read"}
	current := []string{model.GrainCIGrant, model.APIRef("self-debug"), model.GrainSourceRef, model.GrainJournalRef,
		model.APIRef("deployment:read"), model.GrainHealthRef, bootstrap, model.APIRef("ci-read")}
	for _, g := range canonicalSpellingCorpus {
		was, is := allowsCapability(old, g), allowsCapability(current, g)
		switch {
		case was && !is:
			t.Errorf("the older starter policy admits %s and the current one does not", g)
		case is && !was && !strings.Contains(g, "acme/widgets"):
			t.Errorf("the current starter policy admits %s, which the older one refused and which is no repo-scoped grant", g)
		}
	}
	if scoped := model.APIGrantString("ci-read", "acme/widgets"); !allowsCapability(current, scoped) {
		t.Errorf("the current starter policy refuses %s, which a task on a GitHub-CI repository holds by default", scoped)
	}
}
