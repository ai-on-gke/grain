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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// TestCIGrantRuleEventGateIsAlwaysCIOnly holds the invariants of the
// TaskGrantable field (added for github#sandbox-repo, schema migration
// 124): a rule is CI-only unless it opts in with TaskGrantable -- the
// contract every rule was written to before the field existed, so a rule
// that predates it never silently becomes a task grant -- and a rule that
// gates on CI trigger events or grain's own schedule is CI-only even when
// it opts in, because a task has no event for the gate to admit.
func TestCIGrantRuleEventGateIsAlwaysCIOnly(t *testing.T) {
	cases := []struct {
		name       string
		rule       model.CIGrantRule
		ciOnly     bool
		eventGated bool
	}{
		{"zero rule", model.CIGrantRule{}, true, false},
		{"repo-granted alone", model.CIGrantRule{RepoGranted: true}, true, false},
		{"events", model.CIGrantRule{Events: []string{"release"}, RepoGranted: true}, true, true},
		{"trusted schedule", model.CIGrantRule{TrustedSchedule: true}, true, true},
		{"events with TaskGrantable", model.CIGrantRule{TaskGrantable: true, Events: []string{"release"}}, true, true},
		{"trusted schedule with TaskGrantable", model.CIGrantRule{TaskGrantable: true, TrustedSchedule: true}, true, true},
		{"task-grantable repo-granted", model.CIGrantRule{TaskGrantable: true, RepoGranted: true}, false, false},
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
	t.Cleanup(model.RegisterCIGrantRule(openRef, model.CIGrantRule{RepoGranted: true, TaskGrantable: true}))

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

// TestCIGrantRuleWithoutTaskGrantableRefusedToTasks: a method declaring a
// rule with no event gate and no TaskGrantable -- the shape a rule written
// before TaskGrantable existed would have, when declaring any rule made a
// method CI-only -- is still refused to task and process runs in every
// spelling, by CIGrantRuleRefusal and by the capability policy alike.
func TestCIGrantRuleWithoutTaskGrantableRefusedToTasks(t *testing.T) {
	const (
		server = "https://example.test/capabilities/ci-grant-rule-ci-only"
		ref    = server + "#mint"
	)
	t.Cleanup(model.RegisterCIGrantRule(ref, model.CIGrantRule{RepoGranted: true}))
	var policy model.CapabilityPolicy
	for _, kind := range []string{model.CapabilityRunTask, model.CapabilityRunProcess} {
		s := model.CapabilitySubject{Kind: kind}
		for _, g := range []string{ref + "={}", ref, server} {
			if model.CIGrantRuleRefusal(g, s) == "" {
				t.Errorf("CIGrantRuleRefusal(%q, %s) admitted a CI-only method", g, kind)
			}
			if err := policy.Permits(g, s); err == nil {
				t.Errorf("Permits(%q, %s) admitted a CI-only method", g, kind)
			}
		}
	}
	ci := model.CapabilitySubject{Kind: model.CapabilityRunCI, Event: "push"}
	if reason := model.CIGrantRuleRefusal(ref+"={}", ci); reason != "" {
		t.Errorf("CIGrantRuleRefusal(ci push) = %q, want a CI grant", reason)
	}
}

// TestCIGrantRuleNotJudgedForAnyEvent: a CI subject whose trigger event
// is not known yet (the repository's CI capabilities picker) is not
// refused an event-gated method, since some job of the repository's is
// admitted it; a job with no event, or the wrong one, still is, and so
// is a task, and a capability policy deny rule still applies
// (2026-11-09 repository walkthrough).
func TestCIGrantRuleNotJudgedForAnyEvent(t *testing.T) {
	const gated = "https://example.test/capabilities/ci-grant-rule-any-event#sign"
	t.Cleanup(model.RegisterCIGrantRule(gated, model.CIGrantRule{Events: []string{"release"}, RepoGranted: true}))
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	anyEvent := model.CapabilitySubject{Kind: model.CapabilityRunCI, Repo: &repo, AnyEvent: true}
	if reason := model.CIGrantRuleRefusal(gated, anyEvent); reason != "" {
		t.Errorf("CIGrantRuleRefusal(any event) = %q, want admitted", reason)
	}
	var policy model.CapabilityPolicy
	if err := policy.Permits(gated, anyEvent); err != nil {
		t.Errorf("Permits(any event) = %v, want admitted", err)
	}
	noEvent := model.CapabilitySubject{Kind: model.CapabilityRunCI, Repo: &repo}
	if reason := model.CIGrantRuleRefusal(gated, noEvent); !strings.Contains(reason, "this is a run with no trigger event") {
		t.Errorf("CIGrantRuleRefusal(no event) = %q, want the rule's refusal of an event-less run", reason)
	}
	push := model.CapabilitySubject{Kind: model.CapabilityRunCI, Repo: &repo, Event: "push"}
	if reason := model.CIGrantRuleRefusal(gated, push); !strings.Contains(reason, "this is a push run") {
		t.Errorf("CIGrantRuleRefusal(push) = %q, want refused", reason)
	}
	task := model.CapabilitySubject{Kind: model.CapabilityRunTask, Repo: &repo, AnyEvent: true}
	if model.CIGrantRuleRefusal(gated, task) == "" {
		t.Error("CIGrantRuleRefusal admitted an event-gated method to a task")
	}
	deny := model.CapabilityPolicy{Rules: []model.CapabilityPolicyRule{{Capability: gated, Deny: true}}}
	if err := deny.Permits(gated, anyEvent); err == nil {
		t.Error("a deny rule did not apply to a CI subject with any event")
	}
}
