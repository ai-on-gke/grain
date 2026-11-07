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

package model_test

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// TestCIGrantRuleEventGateIsAlwaysCIOnly holds the invariant the CIOnly
// field was added under (github#sandbox-repo, schema migration 121): a
// rule that gates on CI trigger events or grain's own schedule is CI-only
// whether or not CIOnly is set, because a task has no event for the gate
// to admit. Only a rule with no event gate and CIOnly unset (RepoGranted
// alone, as github#sandbox-repo declares) is also a task grant.
func TestCIGrantRuleEventGateIsAlwaysCIOnly(t *testing.T) {
	cases := []struct {
		name       string
		rule       model.CIGrantRule
		ciOnly     bool
		eventGated bool
	}{
		{"events without CIOnly", model.CIGrantRule{Events: []string{"release"}, RepoGranted: true}, true, true},
		{"trusted schedule without CIOnly", model.CIGrantRule{TrustedSchedule: true}, true, true},
		{"CIOnly alone", model.CIGrantRule{CIOnly: true}, true, false},
		{"repo-granted alone", model.CIGrantRule{RepoGranted: true}, false, false},
	}
	for _, tc := range cases {
		if got := tc.rule.IsCIOnly(); got != tc.ciOnly {
			t.Errorf("%s: IsCIOnly = %v, want %v", tc.name, got, tc.ciOnly)
		}
		if got := tc.rule.IsEventGated(); got != tc.eventGated {
			t.Errorf("%s: IsEventGated = %v, want %v", tc.name, got, tc.eventGated)
		}
	}
	gated := model.CIGrantRule{Events: []string{"release"}}
	if gated.Admits("push", false) || gated.Admits("pull_request", false) || !gated.Admits("release", false) {
		t.Error("an event-gated rule admits an event it does not name, or refuses one it does")
	}
	open := model.CIGrantRule{RepoGranted: true}
	for _, ev := range []string{"push", "pull_request", "release", ""} {
		if !open.Admits(ev, false) {
			t.Errorf("a rule with no event gate refuses event %q", ev)
		}
	}
}

// TestCIGrantRuleBindsEverySpelling: the rule a server declared is found
// for the recorded grant, the bare reference, the server id spelled in
// another case and -- on a server with one rule-bearing method -- the
// methodless shorthand, so no spelling of a CI-only method reaches a task
// and no spelling of a repo-granted one is granted by a job's declaration
// alone.
func TestCIGrantRuleBindsEverySpelling(t *testing.T) {
	const (
		gatedServer = "https://example.test/capabilities/ci-grant-rule-gated"
		gatedRef    = gatedServer + "#sign"
		openServer  = "https://example.test/capabilities/ci-grant-rule-open"
		openRef     = openServer + "#scratch"
	)
	t.Cleanup(model.RegisterCIGrantRule(gatedRef, model.CIGrantRule{Events: []string{"release"}, RepoGranted: true}))
	t.Cleanup(model.RegisterCIGrantRule(openRef, model.CIGrantRule{RepoGranted: true}))

	task := model.CapabilitySubject{Kind: model.CapabilityRunTask}
	var policy model.CapabilityPolicy
	for _, g := range []string{
		gatedRef + "={}",
		gatedRef,
		"https://example.test/Capabilities/CI-Grant-Rule-Gated#sign={}",
		gatedServer,
	} {
		rule, ok := model.CIGrantRuleFor(g)
		if !ok || !rule.IsCIOnly() {
			t.Errorf("CIGrantRuleFor(%q) = %+v, %v, want the event-gated rule", g, rule, ok)
			continue
		}
		if model.CIGrantRuleRefusal(g, task) == "" {
			t.Errorf("CIGrantRuleRefusal(%q, task) admitted an event-gated method to a task", g)
		}
		if err := policy.Permits(g, task); err == nil {
			t.Errorf("Permits(%q, task) admitted an event-gated method to a task", g)
		}
		if !model.IsRepoGrantedCIGrant(g) {
			t.Errorf("IsRepoGrantedCIGrant(%q) = false, want the repository's list to be what grants it", g)
		}
	}
	for _, g := range []string{openRef + "={}", openRef, openServer} {
		rule, ok := model.CIGrantRuleFor(g)
		if !ok || rule.IsCIOnly() {
			t.Errorf("CIGrantRuleFor(%q) = %+v, %v, want the repo-granted, task-grantable rule", g, rule, ok)
			continue
		}
		if reason := model.CIGrantRuleRefusal(g, task); reason != "" {
			t.Errorf("CIGrantRuleRefusal(%q, task) = %q, want a task grant", g, reason)
		}
		if !model.IsRepoGrantedCIGrant(g) {
			t.Errorf("IsRepoGrantedCIGrant(%q) = false, want a job's declaration alone not to grant it", g)
		}
		if got := model.CanonicalCIGrant(g); got != openRef+"={}" {
			t.Errorf("CanonicalCIGrant(%q) = %q, want %q", g, got, openRef+"={}")
		}
	}
	// Arguments are not collapsed: a grant carrying any is not the
	// empty-argument grant.
	if got := model.CanonicalCIGrant(openRef + `={"x":1}`); got == openRef+"={}" {
		t.Errorf("CanonicalCIGrant collapsed a grant with arguments to %q", got)
	}
	// A different method on the same server is not the registered one.
	if _, ok := model.CIGrantRuleFor(openServer + "#other"); ok {
		t.Errorf("CIGrantRuleFor(%s#other) found a rule declared for %s", openServer, openRef)
	}
}
