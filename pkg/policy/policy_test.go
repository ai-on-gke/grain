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
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

const sample = `
repos:
  - acme/widgets
  - tools/*
  - git.example.com/corp/app
local_requesters: [owner@example.com]
approvers: [lead@example.com, other@example.com]
capabilities:
  deny: [gcp-key]
network: [none]
`

func mustParse(t *testing.T, src string) *Policy {
	t.Helper()
	p, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return p
}

func human(id string) *model.Attribution {
	return &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: id}}
}

func TestCheck(t *testing.T) {
	p := mustParse(t, sample)
	widgets := &model.RepoRef{Owner: "acme", Name: "widgets"}
	ok := model.Task{ID: "1", Target: widgets, NetworkAccess: model.NetworkNone}

	cases := []struct {
		name    string
		task    func(model.Task) model.Task
		wantErr string
	}{
		{"local requester on an allowed repo", func(t model.Task) model.Task { t.Creator = "owner@example.com"; return t }, ""},
		{"grain's own task", func(t model.Task) model.Task { return t }, ""},
		{"case-insensitive repo", func(t model.Task) model.Task { t.Target = &model.RepoRef{Owner: "ACME", Name: "Widgets"}; return t }, ""},
		{"wildcard repo", func(t model.Task) model.Task { t.Target = &model.RepoRef{Owner: "tools", Name: "x"}; return t }, ""},
		{"other-host repo", func(t model.Task) model.Task {
			t.Target = &model.RepoRef{Owner: "corp", Name: "app", Host: "git.example.com"}
			return t
		}, ""},
		{"same name on the default host", func(t model.Task) model.Task { t.Target = &model.RepoRef{Owner: "corp", Name: "app"}; return t }, "repository corp/app"},
		{"unlisted target", func(t model.Task) model.Task { t.Target = &model.RepoRef{Owner: "acme", Name: "gadgets"}; return t }, "repository acme/gadgets"},
		{"unlisted write", func(t model.Task) model.Task {
			t.Writes = []model.WriteTarget{{Repo: model.RepoRef{Owner: "evil", Name: "x"}}}
			return t
		}, "repository evil/x"},
		{"unlisted read", func(t model.Task) model.Task { t.Reads = []model.RepoRef{{Owner: "evil", Name: "y"}}; return t }, "repository evil/y"},
		{"external, unapproved", func(t model.Task) model.Task { t.Creator = "stranger@example.com"; return t }, "no human approval"},
		{"external, approved by automation", func(t model.Task) model.Task {
			t.Creator = "stranger@example.com"
			t.Approval = &model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}}
			return t
		}, "no human approval"},
		{"external, self-approved", func(t model.Task) model.Task {
			t.Creator = "lead@example.com"
			t.Approval = human("lead@example.com")
			return t
		}, "its own requester"},
		{"external, approved by a non-approver", func(t model.Task) model.Task {
			t.Creator = "stranger@example.com"
			t.Approval = human("rando@example.com")
			return t
		}, "not one of this machine's approvers"},
		{"external, approved", func(t model.Task) model.Task {
			t.Creator = "stranger@example.com"
			t.Approval = human("Lead@example.com")
			return t
		}, ""},
		{"denied capability", func(t model.Task) model.Task { t.Grants = []model.Grant{{Capability: "gcp-key"}}; return t }, `capability "gcp-key"`},
		{"other capability", func(t model.Task) model.Task { t.Grants = []model.Grant{{Capability: "github-sandbox"}}; return t }, ""},
		{"self-debug reads no repository", func(t model.Task) model.Task { t.Grants = []model.Grant{{Capability: "self-debug"}}; return t }, ""},
		{"capability reading an unlisted repo", func(t model.Task) model.Task {
			t.Grants = []model.Grant{{Capability: "repo-read:ai-on-gke/grain"}}
			return t
		}, "repository ai-on-gke/grain"},
		{"capability writing an unlisted repo", func(t model.Task) model.Task {
			t.Grants = []model.Grant{{Capability: "repo-write:evil/z"}}
			return t
		}, "repository evil/z"},
		{"open network", func(t model.Task) model.Task { t.NetworkAccess = model.NetworkOpen; return t }, `network access "open"`},
		{"network from the open default", func(t model.Task) model.Task { t.NetworkAccess = ""; return t }, `network access "open"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := p.Check(c.task(ok), "")
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("Check = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("Check = %v, want an error containing %q", err, c.wantErr)
			}
		})
	}
}

func TestCheckAllowList(t *testing.T) {
	p := mustParse(t, "repos: [acme/widgets, ai-on-gke/grain]\ncapabilities:\n  allow: [self-debug]\n")
	task := model.Task{Target: &model.RepoRef{Owner: "acme", Name: "widgets"}, Grants: []model.Grant{{Capability: "gcp-key"}}}
	if err := p.Check(task, ""); err == nil || !strings.Contains(err.Error(), "does not allow capability") {
		t.Fatalf("Check = %v, want the allow list to refuse gcp-key", err)
	}
	task.Grants = []model.Grant{{Capability: "self-debug"}}
	if err := p.Check(task, model.NetworkNone); err != nil {
		t.Fatalf("Check = %v, want nil", err)
	}
}

// A policy file on this machine is not rewritten by schema migration 115,
// which rewrote a self-debug grant to api:self-debug and grain#source: an
// allow list naming self-debug admits both halves, and a deny naming it
// refuses each.
func TestRetiredSelfDebugInAPolicyBindsWhatItBecame(t *testing.T) {
	allow := mustParse(t, "repos: [acme/widgets]\ncapabilities:\n  allow: [self-debug]\n")
	task := model.Task{Target: &model.RepoRef{Owner: "acme", Name: "widgets"}}
	for _, g := range model.SelfDebugGrants() {
		task.Grants = []model.Grant{{Capability: g}}
		if err := allow.Check(task, ""); err != nil {
			t.Errorf("allow: [self-debug] refused %s: %v", g, err)
		}
	}
	task.Grants = []model.Grant{{Capability: model.GrainHealthGrant}}
	if err := allow.Check(task, ""); err == nil {
		t.Errorf("allow: [self-debug] admitted %s, which self-debug never became", model.GrainHealthGrant)
	}
	deny := mustParse(t, "repos: [acme/widgets]\ncapabilities:\n  deny: [self-debug]\n")
	for _, g := range model.SelfDebugGrants() {
		task.Grants = []model.Grant{{Capability: g}}
		if err := deny.Check(task, ""); err == nil || !strings.Contains(err.Error(), "forbids capability") {
			t.Errorf("deny: [self-debug] = %v for %s, want it refused", err, g)
		}
	}
}

// The other direction: a policy naming what a retired name became, in
// canonical spelling, binds a task still granted the retired name. A
// grant named self-debug is both api#self-debug and grain#source, so it
// is admitted when both are and refused when either is.
func TestCanonicalPolicyBindsARetiredGrantThatBecameSeveral(t *testing.T) {
	task := model.Task{Target: &model.RepoRef{Owner: "acme", Name: "widgets"}}
	check := func(rules, grant string) error {
		t.Helper()
		task.Grants = []model.Grant{{Capability: grant}}
		return mustParse(t, "repos: [acme/widgets]\ncapabilities:\n"+rules+"\n").Check(task, "")
	}
	both := "  allow: [" + strconvQuote(model.APIRef("self-debug")+"={}") + ", " + strconvQuote(model.GrainSourceGrant) + "]"
	for _, g := range []string{"self-debug", "Self-Debug"} {
		if err := check(both, g); err != nil {
			t.Errorf("allowing both grants self-debug became refused %s: %v", g, err)
		}
	}
	half := "  allow: [" + strconvQuote(model.APIRef("self-debug")+"={}") + "]"
	if err := check(half, "self-debug"); err == nil {
		t.Error("allowing only api#self-debug admitted self-debug, which is grain#source too")
	}
	for _, d := range []string{model.APIRef("self-debug"), model.GrainSourceRef, "api:self-debug"} {
		if err := check("  deny: ["+strconvQuote(d)+"]", "self-debug"); err == nil || !strings.Contains(err.Error(), "forbids capability") {
			t.Errorf("deny: [%s] = %v for self-debug, want it refused", d, err)
		}
	}
	if err := check("  deny: ["+strconvQuote(model.GrainHealthGrant)+"]", "self-debug"); err != nil {
		t.Errorf("deny: [grain#health] refused self-debug, which never became it: %v", err)
	}
	if err := check("  deny: ["+strconvQuote(model.GrainHealthGrant)+"]", "deployment-health"); err == nil {
		t.Error("deny: [grain#health] admitted deployment-health, which became it")
	}
	// 1-to-1 retired API bare names answer to legacy api: deny/allow rules too.
	for _, d := range []string{"api:self-upgrade", "api:*", model.APIRef("self-upgrade")} {
		if err := check("  deny: ["+strconvQuote(d)+"]", "self-upgrade"); err == nil || !strings.Contains(err.Error(), "forbids capability") {
			t.Errorf("deny: [%s] = %v for self-upgrade, want it refused", d, err)
		}
	}
	if err := check("  allow: [memory]", "api:memory"); err != nil {
		t.Errorf("allow: [memory] refused api:memory: %v", err)
	}
	if err := check("  allow: [memory]", "api:memory@acme/widgets"); err == nil {
		t.Error("allow: [memory] admitted repo-scoped api:memory@acme/widgets")
	}
}

func TestRuntimeGrantExpansionsBindSingleAndMultiScriptRetirements(t *testing.T) {
	const hostScriptRef = "https://grain.dev/capabilities/host-script#host-script"
	opsDeploy := hostScriptRef + `={"script":"ops-deploy"}`
	opsStatus := hostScriptRef + `={"script":"ops-status"}`
	guardedWipe := hostScriptRef + `={"script":"guarded-wipe"}`
	t.Cleanup(model.SetRuntimeGrantExpansions(map[string][]string{
		"local-scripts:ops":     {opsDeploy, opsStatus},
		"local-scripts:guarded": {guardedWipe},
	}))

	task := model.Task{Target: &model.RepoRef{Owner: "acme", Name: "widgets"}}
	check := func(rules, grant string) error {
		t.Helper()
		task.Grants = []model.Grant{{Capability: grant}}
		return mustParse(t, "repos: [acme/widgets]\ncapabilities:\n"+rules+"\n").Check(task, "")
	}

	// Single-script and multi-script runtime expansions are both refused when
	// any canonical script grant (or the method itself) is denied.
	for _, d := range []string{hostScriptRef, guardedWipe} {
		if err := check("  deny: ["+strconvQuote(d)+"]", "local-scripts:guarded"); err == nil || !strings.Contains(err.Error(), "forbids capability") {
			t.Errorf("deny: [%s] = %v for 1-script local-scripts:guarded, want it refused", d, err)
		}
	}
	if err := check("  deny: ["+strconvQuote(opsStatus)+"]", "local-scripts:ops"); err == nil || !strings.Contains(err.Error(), "forbids capability") {
		t.Errorf("deny: [%s] = %v for local-scripts:ops, want it refused", opsStatus, err)
	}
	if err := check("  deny: ["+strconvQuote(opsStatus)+"]", "local-scripts:guarded"); err != nil {
		t.Errorf("deny of unrelated script refused local-scripts:guarded: %v", err)
	}

	// Canonical allow lists admit retired runtime names only when all of their
	// scripts are allowed.
	if err := check("  allow: ["+strconvQuote(guardedWipe)+"]", "local-scripts:guarded"); err != nil {
		t.Errorf("allow: [%s] refused 1-script local-scripts:guarded: %v", guardedWipe, err)
	}
	if err := check("  allow: ["+strconvQuote(opsDeploy)+"]", "local-scripts:ops"); err == nil {
		t.Error("allowing only one script admitted 2-script local-scripts:ops")
	}
	if err := check("  allow: ["+strconvQuote(opsDeploy)+", "+strconvQuote(opsStatus)+"]", "local-scripts:ops"); err != nil {
		t.Errorf("allowing both scripts refused local-scripts:ops: %v", err)
	}

	// Allowing a retired runtime name admits its exact script grants and keeps
	// JSON argument values case-sensitive.
	if err := check("  allow: [local-scripts:guarded]", guardedWipe); err != nil {
		t.Errorf("allow: [local-scripts:guarded] refused %s: %v", guardedWipe, err)
	}
	if err := check("  allow: [local-scripts:guarded]", hostScriptRef+`={"script":"GUARDED-WIPE"}`); err == nil {
		t.Error("allow: [local-scripts:guarded] folded JSON script value case")
	}
	if err := check("  allow: [local-scripts:ops]", opsDeploy); err != nil {
		t.Errorf("allow: [local-scripts:ops] refused %s: %v", opsDeploy, err)
	}
	if err := check("  allow: [local-scripts:ops]", hostScriptRef+`={"script":"OPS-DEPLOY"}`); err == nil {
		t.Error("allow: [local-scripts:ops] folded JSON script value case")
	}
}

// The starter policy `grain init workstation` writes moved off the
// retired names it used to write (grain-ci, self-debug,
// api:deployment:read, api:ci-read) onto the grants they became
// (model.DirectorPolicyCapabilities). Each replacement admits every grant
// the retired name did, in every spelling a task may hold it in. The only
// thing it admits beyond that is the same api grant narrowed to one
// repository: grain files tasks api#ci-read={"repo":...} itself, so a
// policy that refused it would hold them. Non-api grants (grain#ci={},
// grain#source={}, ...) keep ={} so non-empty arguments stay refused.
func TestStarterPolicyCanonicalNamesAdmitWhatTheRetiredNamesDid(t *testing.T) {
	repoScoped := func(ref string) string { return ref + `={"repo":"acme/widgets"}` }
	grants := []string{
		"grain-ci", "GRAIN-CI", model.GrainCIGrant, model.GrainCIRef, repoScoped(model.GrainCIRef),
		"self-debug", "api:self-debug", "api:self-debug@acme/widgets",
		model.APIRef("self-debug"), model.APIRef("self-debug") + "={}", repoScoped(model.APIRef("self-debug")),
		model.GrainSourceGrant, model.GrainSourceRef, repoScoped(model.GrainSourceRef),
		model.GrainHealthGrant, repoScoped(model.GrainHealthRef),
		model.GrainJournalGrant, repoScoped(model.GrainJournalRef),
		"deployment-health", "api:deployment:read", "api:deployment:read@acme/widgets", "api:deployment:write",
		model.APIRef("deployment:read"), repoScoped(model.APIRef("deployment:read")),
		"api:ci-read", "api:ci-read@acme/widgets", model.APIRef("ci-read"), repoScoped(model.APIRef("ci-read")),
		"api:tasks:admin", "gcp-key", model.ConfigMapRef + "={}",
	}
	narrowed := []string{
		"api:self-debug@acme/widgets", repoScoped(model.APIRef("self-debug")),
		"api:deployment:read@acme/widgets", repoScoped(model.APIRef("deployment:read")),
		"api:ci-read@acme/widgets", repoScoped(model.APIRef("ci-read")),
	}
	for _, tc := range []struct {
		retired   string
		canonical []string
	}{
		{"grain-ci", []string{model.GrainCIGrant}},
		{"self-debug", []string{model.APIRef("self-debug"), model.GrainSourceGrant}},
		{"deployment-health", []string{model.APIRef("deployment:read"), model.GrainHealthGrant}},
		{"api:deployment:read", []string{model.APIRef("deployment:read")}},
		{"api:ci-read", []string{model.APIRef("ci-read")}},
	} {
		retired := &Policy{Capabilities: CapabilityRules{Allow: []string{tc.retired}}}
		canonical := &Policy{Capabilities: CapabilityRules{Allow: tc.canonical}}
		for _, g := range grants {
			task := model.Task{Grants: []model.Grant{{Capability: g}}}
			was, is := retired.checkCapabilities(task) == nil, canonical.checkCapabilities(task) == nil
			switch {
			case was && !is:
				t.Errorf("allow: [%s] admits %s, and allow: %v does not", tc.retired, g, tc.canonical)
			case is && !was && !slices.Contains(narrowed, g):
				t.Errorf("allow: %v admits %s, which allow: [%s] did not", tc.canonical, g, tc.retired)
			}
		}
	}
}

func strconvQuote(s string) string { return strconv.Quote(s) }

func TestCheckCapabilityPatterns(t *testing.T) {
	cases := []struct {
		rules, grant string
		wantErr      string
	}{
		{"deny: [repo-write:*]", "repo-write:acme/widgets", "forbids capability"},
		{"deny: [repo-write:*]", "repo-write", ""},
		{"deny: [repo-write:*]", "repo-read:acme/widgets", ""},
		{"deny: [Repo-Write:acme/*]", "repo-write:acme/widgets", "forbids capability"},
		{"deny: [repo-write:acme/*]", "repo-write:other/widgets", ""},
		{"deny: [gcp-key]", "gcp-key", "forbids capability"},
		{"deny: [gcp-key]", "gcp-key:team-a", "forbids capability"},
		{"deny: [gcp-key]", "gcp-keys", ""},
		{"deny: [gcp-key:team-a]", "gcp-key:team-b", ""},
		{"allow: [gcp-key]", "gcp-key:team-a", ""},
		{"allow: [gcp-key:team-*]", "gcp-key:team-a", ""},
		{"allow: [gcp-key:team-*]", "gcp-key:other", "does not allow capability"},
		{"allow: [gcp-key:team-*]", "gcp-key", "does not allow capability"},
		{"allow: [repo-write:*]", "repo-write:acme/widgets", ""},
		{"allow: [repo-write:*]", "self-debug", "does not allow capability"},
		{"allow: [\"*\"]\n  deny: [gcp-key]", "self-debug", ""},
		{"allow: [\"*\"]\n  deny: [gcp-key]", "gcp-key:x", "forbids capability"},
		// gcp-key retired onto gcp#gcp-key {permission_set}: the old names
		// in a policy file still reach the sets they became.
		{"deny: [gcp-key:team-a]", model.GCPKeyServerGrant("team-a"), "forbids capability"},
		{"deny: [gcp-key:team-a]", model.GCPKeyServerGrant("team-b"), ""},
		{"deny: [gcp-key]", model.GCPKeyServerGrant("team-b"), "forbids capability"},
		{"allow: [gcp-key:team-*]", model.GCPKeyServerGrant("team-a"), ""},
		{"allow: [gcp-key:team-*]", model.GCPKeyServerGrant("other"), "does not allow capability"},
		{"allow: [gcp-key:team-*]", model.GCPKeySandboxAgentGrant, "does not allow capability"},
		// A grant naming no set gets whichever set the server lists
		// first, so any gcp-key:<set> deny stops it and only an allow
		// covering every set admits it.
		{"deny: [gcp-key:team-a]", model.GCPKeyRef + "={}", "forbids capability"},
		{"deny: [gcp-key:team-a]", model.GCPKeyRef + `={"account":"default"}`, "permission_set"},
		{`allow: ["*"]`, model.GCPKeyRef + `={"activate":true}`, "permission_set"},
		{"allow: [gcp-key:team-*]", model.GCPKeyRef + "={}", "does not allow capability"},
		{"allow: [gcp-key]", model.GCPKeyRef + "={}", ""},
		// A retired bare name in a policy file still reaches the grant
		// it became: migration 85 rewrote git-push-any-ref on the task,
		// not here.
		{"deny: [git-push-any-ref]", model.GitPushAnyRefGrant, "forbids capability"},
		{"deny: [git-push-any-ref]", model.RepoWriteRef + `={"refs":["refs/tags/*","refs/heads/*"],"force":true}`, "forbids capability"},
		{"deny: [Git-Push-Any-Ref]", model.GitPushAnyRefGrant, "forbids capability"},
		{"deny: [\"" + model.RepoWriteRef + "\"]", model.GitPushAnyRefCapability, "forbids capability"},
		{"deny: [git-*]", model.GitPushAnyRefGrant, "forbids capability"},
		{"deny: [\"*-any-ref\"]", model.RepoWriteRef + `={"refs":["refs/tags/*","refs/heads/*"],"force":true}`, "forbids capability"},
		{"deny: [git-*]", model.GrainSQLWriteGrant, ""},
		{"allow: [git-push-any-ref]", model.GitPushAnyRefGrant, ""},
		{"allow: [git-push-any-ref]", model.GrainSQLWriteGrant, "does not allow capability"},
		{"allow: [db-read]", model.GrainSQLWriteGrant, "does not allow capability"},
		// Repository access matches in either spelling: migration 88
		// rewrote repo-write: on the store, not here, and a repository's
		// own .grain/workflows keeps the bare name.
		{"deny: [repo-write:*]", model.RepoWriteGrant(model.RepoRef{Owner: "acme", Name: "widgets"}), "forbids capability"},
		{"deny: [repo-write:acme/*]", model.RepoWriteGrant(model.RepoRef{Owner: "other", Name: "widgets"}), ""},
		{"deny: [repo-write:*]", model.RepoReadGrant(model.RepoRef{Owner: "acme", Name: "widgets"}), ""},
		{"deny: [repo-write:*]", model.GitPushAnyRefGrant, ""},
		{"deny: [\"" + model.RepoReadRef + "\"]", "repo-read:acme/widgets", "forbids capability"},
		{"allow: [repo-read:acme/*]", model.RepoReadGrant(model.RepoRef{Owner: "acme", Name: "widgets"}), ""},
		{"allow: [repo-read:acme/*]", model.RepoWriteGrant(model.RepoRef{Owner: "acme", Name: "widgets"}), "does not allow capability"},
		// Exact capability-reference grants (ref=<json>) bind only those
		// arguments, while bootstrap-playbooks denies every config-map and
		// skill alias selecting the bootstrap playbooks.
		{`allow: [` + strconvQuote(model.BootstrapPlaybooksGrant) + `]`, model.BootstrapPlaybooksGrant, ""},
		{`allow: [` + strconvQuote(model.BootstrapPlaybooksGrant) + `]`, model.ConfigMapRef + `={"config_maps": ["bootstrap"]}`, ""},
		{`allow: [` + strconvQuote(model.BootstrapPlaybooksGrant) + `]`, "bootstrap-playbooks", ""},
		{`allow: [` + strconvQuote(model.BootstrapPlaybooksGrant) + `]`, model.ConfigMapRef, "does not allow capability"},
		{`allow: [` + strconvQuote(model.BootstrapPlaybooksGrant) + `]`, model.ConfigMapRef + "={}", "does not allow capability"},
		{`allow: [` + strconvQuote(model.BootstrapPlaybooksGrant) + `]`, model.ConfigMapRef + `={"secrets":["prod"]}`, "does not allow capability"},
		{`allow: [` + strconvQuote(model.BootstrapPlaybooksGrant) + `]`, model.ConfigMapRef + `={"config_maps":["BOOTSTRAP"]}`, "does not allow capability"},
		{`allow: [bootstrap-playbooks]`, model.ConfigMapRef + `={"config_maps": ["bootstrap"]}`, ""},
		{`allow: [bootstrap-playbooks]`, model.ConfigMapRef + `={"secrets":["prod"]}`, "does not allow capability"},
		{`allow: [` + strconvQuote(model.GrainSourceGrant) + `]`, model.GrainSourceGrant, ""},
		{`allow: [` + strconvQuote(model.GrainSourceGrant) + `]`, model.GrainSourceRef, ""},
		{`deny: [bootstrap-playbooks]`, model.BootstrapPlaybooksGrant, "forbids capability"},
		{`deny: [bootstrap-playbooks]`, model.SkillCapabilityServerID + `#config-map={"config_maps":["bootstrap"]}`, "forbids capability"},
		{`deny: [bootstrap-playbooks]`, model.SkillCapabilityServerID + `#skill={"skills":["bootstrap"]}`, "forbids capability"},
		{`deny: [bootstrap-playbooks]`, model.ConfigMapCapabilityServerID + `#skill={"skills":["bootstrap"]}`, "forbids capability"},
		{`deny: [boot*]`, model.SkillCapabilityServerID + `#skill={"skills":["bootstrap"]}`, "forbids capability"},
		{`deny: [` + strconvQuote(model.BootstrapPlaybooksGrant) + `]`, model.SkillCapabilityServerID + `#skill={"skills":["bootstrap"]}`, "forbids capability"},
		{`deny: [` + strconvQuote(model.ConfigMapRef+`={"secrets":["prod"]}`) + `]`, model.BootstrapPlaybooksGrant, ""},
		// Case-insensitive https:// scheme/method on ref=<json>, while preserving JSON value case.
		{`deny: [` + strconvQuote(`HTTPS://GRAIN.DEV/CAPABILITIES/CONFIG-MAP#CONFIG-MAP={"secrets":["prod"]}`) + `]`, model.ConfigMapRef + `={"secrets":["prod"]}`, "forbids capability"},
		{`deny: [` + strconvQuote(`HTTPS://GRAIN.DEV/CAPABILITIES/CONFIG-MAP#CONFIG-MAP={"secrets":["prod"]}`) + `]`, model.ConfigMapRef + `={"secrets":["PROD"]}`, ""},
		{`allow: [` + strconvQuote(`HTTPS://GRAIN.DEV/CAPABILITIES/CONFIG-MAP#CONFIG-MAP={"config_maps":["bootstrap"]}`) + `]`, model.BootstrapPlaybooksGrant, ""},
		{`allow: [` + strconvQuote(`HTTPS://GRAIN.DEV/CAPABILITIES/CONFIG-MAP#CONFIG-MAP={"config_maps":["bootstrap"]}`) + `]`, model.ConfigMapRef + `={"config_maps":["BOOTSTRAP"]}`, "does not allow capability"},
		// Unnormalized methodless shorthand, unnamed #skill, and skill/config-map server aliases cannot bypass deny rules.
		{`deny: [` + strconvQuote(model.ConfigMapRef) + `]`, model.ConfigMapCapabilityServerID + `={"secrets":["prod"]}`, "forbids capability"},
		{`deny: [` + strconvQuote(model.ConfigMapRef) + `]`, model.SkillCapabilityServerID + `#config-map={"secrets":["prod"]}`, "forbids capability"},
		{`deny: [` + strconvQuote(model.ConfigMapRef+`={"secrets":["prod"]}`) + `]`, model.ConfigMapCapabilityServerID + `={"secrets":["prod"]}`, "forbids capability"},
		{`deny: [` + strconvQuote(model.ConfigMapRef+`={"secrets":["prod"]}`) + `]`, model.SkillCapabilityServerID + `#config-map={"secrets":["prod"]}`, "forbids capability"},
		{`deny: [` + strconvQuote(model.ConfigMapRef+`={"secrets":["prod"]}`) + `]`, model.ConfigMapCapabilityServerID + `={"config_maps":["bootstrap"]}`, ""},
		{`deny: [bootstrap-playbooks]`, model.SkillCapabilityServerID + `#config-map={"secrets":["prod"]}`, "forbids capability"},
		{`deny: [bootstrap-playbooks]`, model.ConfigMapCapabilityServerID + `={"config_maps":["bootstrap"]}`, "forbids capability"},
		{`deny: [bootstrap-playbooks]`, model.SkillCapabilityServerID + `#skill`, "forbids capability"},
		{`deny: [bootstrap-playbooks]`, model.ConfigMapCapabilityServerID + `#skill`, "forbids capability"},
		{`deny: [bootstrap-playbooks]`, model.SkillCapabilityServerID, "forbids capability"},
		{`deny: [bootstrap-playbooks]`, model.SkillCapabilityServerID + `#skill={"skills":["ops"]}`, ""},
		{`deny: [` + strconvQuote(model.BootstrapPlaybooksGrant) + `]`, model.SkillCapabilityServerID + `#skill`, "forbids capability"},
		{`deny: [` + strconvQuote(model.BootstrapPlaybooksGrant) + `]`, model.SkillCapabilityServerID + `#skill={"skills":["ops"]}`, ""},
		{`deny: [` + strconvQuote(model.SkillCapabilityServerID+"#*") + `]`, model.ConfigMapRef + `={"secrets":["prod"]}`, "forbids capability"},
		// Agent API grants match across legacy api:<what>[@repo] and canonical
		// https://grain.dev/capabilities/api#<what>[={"repo":...}] spellings.
		{"deny: [api:*]", model.APIRef("secrets:write"), "forbids capability"},
		{"deny: [api:*]", model.APIGrantString("ci:write", "acme/widgets"), "forbids capability"},
		{"deny: [api:secrets:write]", model.APIRef("secrets:write"), "forbids capability"},
		{"deny: [api:ci:write@acme/*]", model.APIGrantString("ci:write", "acme/widgets"), "forbids capability"},
		{"deny: [api:ci:write@acme/*]", model.APIGrantString("ci:write", "other/widgets"), ""},
		{`deny: [` + strconvQuote(model.APIRef("secrets:write")) + `]`, "api:secrets:write", "forbids capability"},
		{`deny: [` + strconvQuote(model.APICapabilityServerID+"#*") + `]`, "api:ci:read@acme/widgets", "forbids capability"},
		{"deny: [self-*]", "api:self-debug", "forbids capability"},
		{"deny: [self-*]", model.APIRef("self-debug"), "forbids capability"},
		{"allow: [api:ci-read]", model.APIRef("ci-read"), ""},
		{"allow: [self-debug]", "api:self-debug", ""},
		{"allow: [self-debug]", model.APIRef("self-debug"), ""},
		{`allow: ["*"]`, model.APICapabilityServerID, "must name a #<scope> method"},
		{`allow: ["*"]`, model.APIRef("ci:write") + `={"bogus":1}`, `only a valid "repo"`},
	}
	for _, c := range cases {
		p := mustParse(t, "repos: [acme/widgets, other/widgets, ai-on-gke/grain]\ncapabilities:\n  "+c.rules+"\n")
		task := model.Task{Target: &model.RepoRef{Owner: "acme", Name: "widgets"}, Grants: []model.Grant{{Capability: c.grant}}}
		err := p.Check(task, "")
		if c.wantErr == "" {
			if err != nil {
				t.Errorf("%q, grant %q: Check = %v, want nil", c.rules, c.grant, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%q, grant %q: Check = %v, want an error containing %q", c.rules, c.grant, err, c.wantErr)
		}
	}
}

// TestCheckCapabilityRepos is GSA1-04: a capability that reaches a
// repository of its own -- a repo-read: or repo-write: grant -- answers
// to repos like the task's own Target and Reads. self-debug reaches
// none: its grain-src/ is the deployment's baked-in copy, not a clone of
// ai-on-gke/grain, so a policy that does not list grain admits it.
func TestCheckCapabilityRepos(t *testing.T) {
	widgets := &model.RepoRef{Owner: "acme", Name: "widgets"}
	task := model.Task{Target: widgets, Grants: []model.Grant{{Capability: model.SelfDebugCapability}}}

	confined := mustParse(t, "repos: [acme/*]\ncapabilities:\n  allow: [\"*\"]\n")
	if err := confined.Check(task, ""); err != nil {
		t.Fatalf("Check = %v, want repos: [acme/*] to admit a self-debug task: it reads no repository", err)
	}
	if err := confined.CheckClaims(task, ""); err != nil {
		t.Fatalf("CheckClaims = %v, want repos: [acme/*] to admit a self-debug offer", err)
	}
	for _, grant := range []string{
		model.RepoReadCapability(model.GrainSourceRepo()),
		model.RepoReadCapability(model.RepoRef{Owner: "evil", Name: "y"}),
		model.RepoWriteCapability(model.GrainSourceRepo()),
		"repo-read:evil/y",
		"repo-write:ai-on-gke/grain",
	} {
		task.Grants = []model.Grant{{Capability: model.SelfDebugCapability}, {Capability: grant}}
		repo := model.CapabilityRepos(grant)[0].String()
		if err := confined.Check(task, ""); err == nil || !strings.Contains(err.Error(), "repository "+repo) {
			t.Errorf("Check = %v, want repos: [acme/*] to refuse %s", err, grant)
		}
		if err := confined.CheckClaims(task, ""); err == nil {
			t.Errorf("CheckClaims = nil, want repos: [acme/*] to refuse %s", grant)
		}
	}

	task.Grants = []model.Grant{{Capability: model.RepoReadCapability(model.GrainSourceRepo())}}
	listed := mustParse(t, "repos: [acme/*, ai-on-gke/grain]\ncapabilities:\n  allow: [\"*\"]\n")
	if err := listed.Check(task, ""); err != nil {
		t.Fatalf("Check = %v, want nil once %s is listed", err, model.GrainSourceRepo())
	}
}

func TestCheckClaimsLeavesApprovalToDispatch(t *testing.T) {
	p := mustParse(t, sample)
	external := model.Task{Creator: "stranger@example.com", Target: &model.RepoRef{Owner: "acme", Name: "widgets"}}
	if err := p.CheckClaims(external, model.NetworkNone); err != nil {
		t.Fatalf("CheckClaims = %v, want an unapproved external request let through", err)
	}
	if err := p.Check(external, model.NetworkNone); err == nil {
		t.Fatal("Check let an unapproved external request through")
	}
	for task, want := range map[*model.Task]string{
		{Creator: external.Creator, Target: &model.RepoRef{Owner: "acme", Name: "gadgets"}}:                  "repository acme/gadgets",
		{Creator: external.Creator, Target: external.Target, Grants: []model.Grant{{Capability: "gcp-key"}}}: `capability "gcp-key"`,
	} {
		if err := p.CheckClaims(*task, model.NetworkNone); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("CheckClaims = %v, want an error containing %q", err, want)
		}
	}
	if err := p.CheckClaims(external, ""); err == nil || !strings.Contains(err.Error(), `network access "open"`) {
		t.Errorf("CheckClaims = %v, want the open default refused", err)
	}
	var none *Policy
	if err := none.CheckClaims(external, ""); err != nil {
		t.Fatalf("no policy at all: %v", err)
	}
}

func TestEmptyPolicyFailsClosed(t *testing.T) {
	p := mustParse(t, "")
	if err := p.Check(model.Task{Target: &model.RepoRef{Owner: "acme", Name: "widgets"}}, ""); err == nil {
		t.Fatal("an empty policy allowed a repository")
	}
	if err := p.Check(model.Task{}, ""); err != nil {
		t.Fatalf("a task naming no repository: %v", err)
	}
	var none *Policy
	if err := none.Check(model.Task{Creator: "anyone"}, ""); err != nil {
		t.Fatalf("no policy at all: %v", err)
	}
}

func TestParseRejects(t *testing.T) {
	for _, src := range []string{
		"repo: [acme/widgets]\n",                                              // misspelt key
		"repos: [widgets]\n",                                                  // not owner/name
		"repos: [a/b/c/d]\n",                                                  // too many segments
		"repos: [\"acme/[\"]\n",                                               // bad pattern
		"repos: [acme/w]\nnetwork: [x]\n",                                     // unknown access
		"repos: acme/widgets\n",                                               // not a list
		"capabilities:\n  deny: [\"gcp-key:[\"]\n",                            // bad parameter pattern
		"capabilities:\n  allow: [\"[\"]\n",                                   // bad family pattern
		"capabilities:\n  deny: [\"repo-write:\"]\n",                          // empty parameter
		"capabilities:\n  deny: [\":x\"]\n",                                   // empty family
		"capabilities:\n  allow: [\"" + model.ConfigMapRef + "=not-json\"]\n", // bad inline JSON args
		"capabilities:\n  deny: [\"HTTPS://grain.dev/capabilities/config-map#config-map=not-json\"]\n", // bad inline JSON args with uppercase scheme
		"repos: [acme/w]\n---\nrepos: [evil/x]\n",                                                      // a second document (GSA1-14)
		"---\nrepos: [acme/w]\n---\n{}\n",                                                              // likewise, however small
	} {
		if _, err := Parse([]byte(src)); err == nil {
			t.Errorf("Parse(%q) succeeded, want an error", src)
		}
	}
}

// checkNetwork judges network access and nothing else.
func TestCheckNetworkJudgesOnlyNetwork(t *testing.T) {
	p := &Policy{Network: []model.NetworkAccess{model.NetworkNone}}
	if err := p.checkNetwork(model.Task{NetworkAccess: model.NetworkNone}, ""); err != nil {
		t.Fatalf("checkNetwork of an allowed access: %v", err)
	}
	if err := p.checkNetwork(model.Task{NetworkAccess: model.NetworkOpen}, ""); err == nil || !strings.Contains(err.Error(), "network access") {
		t.Fatalf("checkNetwork of a refused access = %v, want a network refusal", err)
	}
}

func TestLoad(t *testing.T) {
	file := filepath.Join(t.TempDir(), "grain_policy.yml")
	if _, err := Load(file); err == nil {
		t.Fatal("Load of a missing file succeeded")
	}
	if err := os.WriteFile(file, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(p.Repos) != 3 || !p.IsLocalRequester("Owner@example.com") || p.IsLocalRequester("x@example.com") {
		t.Fatalf("Load = %+v", p)
	}
}

// Host sandboxes run every task as the machine's owner, so by default
// only the owner's own tasks run there, unless allow_external_on_host_sandboxes says so (GSA1-05).
func TestCheckUnisolated(t *testing.T) {
	p, err := Parse([]byte("repos: [acme/*]\nlocal_requesters: [owner]\n"))
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		task  model.Task
		local bool
	}{
		"grain's own":     {model.Task{}, true},
		"the owner's":     {model.Task{Creator: "Owner"}, true},
		"somebody else's": {model.Task{Creator: "mallory"}, false},
	} {
		if got := p.IsOwn(RequesterOf(c.task)); got != c.local {
			t.Errorf("IsOwn(%s) = %v, want %v", name, got, c.local)
		}
		if err := p.CheckUnisolated(c.local); (err == nil) != c.local {
			t.Errorf("CheckUnisolated(%s) = %v", name, err)
		}
	}

	var none *Policy
	if !none.IsOwn(RequesterOf(model.Task{Creator: "anyone"})) ||
		none.IsOwn(Requester{Unresolved: true}) {
		t.Error("with no policy, every task but an unresolved one should be local")
	}
	if err := none.CheckUnisolated(false); err == nil {
		t.Error("with no policy, a task that is not local ran on host sandboxes")
	}

	lax, err := Parse([]byte("allow_external_on_host_sandboxes: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := lax.CheckUnisolated(false); err != nil {
		t.Errorf("allow_external_on_host_sandboxes: %v", err)
	}
}

func TestAllowNamedSandboxAgentPatternRefusesUnnamedGCPKey(t *testing.T) {
	p := mustParse(t, "capabilities:\n  allow: [gcp-key:sandbox-*]\n")
	if err := p.CheckClaims(model.Task{Grants: []model.Grant{{Capability: model.GCPKeySandboxAgentGrant}}}, ""); err != nil {
		t.Fatalf("named sandbox-agent refused by allow [gcp-key:sandbox-*]: %v", err)
	}
	for _, unnamed := range []string{model.GCPKeyRef, model.GCPKeyRef + "={}"} {
		if err := p.CheckClaims(model.Task{Grants: []model.Grant{{Capability: unnamed}}}, ""); err == nil {
			t.Errorf("unnamed %q admitted by allow [gcp-key:sandbox-*]", unnamed)
		}
	}
	if err := p.CheckClaims(model.Task{Grants: []model.Grant{{Capability: model.GCPKeyRef + `={"account":"default"}`}}}, ""); err == nil || !strings.Contains(err.Error(), "permission_set") {
		t.Errorf("account=default CheckClaims = %v, want refusal naming permission_set", err)
	}
}

func TestGitHubCredentialLegacyRulesMatchRepoCredential(t *testing.T) {
	releaseGrant := model.RepoCredentialGrant("release-bot")
	docsGrant := model.RepoCredentialGrant("docs-bot")

	denyOne := mustParse(t, "capabilities:\n  deny: [github-credential:release-bot]\n")
	if err := denyOne.CheckClaims(model.Task{Grants: []model.Grant{{Capability: releaseGrant}}}, ""); err == nil {
		t.Errorf("deny [github-credential:release-bot] admitted %s", releaseGrant)
	}
	if err := denyOne.CheckClaims(model.Task{Grants: []model.Grant{{Capability: docsGrant}}}, ""); err != nil {
		t.Errorf("deny [github-credential:release-bot] refused unrelated %s: %v", docsGrant, err)
	}
	if err := denyOne.CheckClaims(model.Task{Grants: []model.Grant{{Capability: model.RepoCredentialRef}}}, ""); err == nil {
		t.Errorf("deny [github-credential:release-bot] admitted unnamed %s", model.RepoCredentialRef)
	}

	denyWild := mustParse(t, "capabilities:\n  deny: ['github-credential:*']\n")
	if err := denyWild.CheckClaims(model.Task{Grants: []model.Grant{{Capability: docsGrant}}}, ""); err == nil {
		t.Errorf("deny [github-credential:*] admitted %s", docsGrant)
	}

	allowOne := mustParse(t, "capabilities:\n  allow: [github-credential:release-bot]\n")
	if err := allowOne.CheckClaims(model.Task{Grants: []model.Grant{{Capability: releaseGrant}}}, ""); err != nil {
		t.Errorf("allow [github-credential:release-bot] refused %s: %v", releaseGrant, err)
	}
	if err := allowOne.CheckClaims(model.Task{Grants: []model.Grant{{Capability: docsGrant}}}, ""); err == nil {
		t.Errorf("allow [github-credential:release-bot] admitted unrelated %s", docsGrant)
	}
	for _, unnamed := range []string{model.RepoCredentialRef, model.RepoCredentialRef + "={}"} {
		if err := allowOne.CheckClaims(model.Task{Grants: []model.Grant{{Capability: unnamed}}}, ""); err == nil {
			t.Errorf("allow [github-credential:release-bot] admitted unnamed %s", unnamed)
		}
	}
}

func TestGitHubSandboxLegacyRulesMatchSandboxRepo(t *testing.T) {
	sandboxGrant := model.GitHubSandboxRepoGrant
	readIssuesGrant := model.GitHubReadIssuesGrant

	denySandbox := mustParse(t, "capabilities:\n  deny: [github-sandbox]\n")
	if err := denySandbox.CheckClaims(model.Task{Grants: []model.Grant{{Capability: sandboxGrant}}}, ""); err == nil {
		t.Errorf("deny [github-sandbox] admitted %s", sandboxGrant)
	}
	if err := denySandbox.CheckClaims(model.Task{Grants: []model.Grant{{Capability: readIssuesGrant}}}, ""); err != nil {
		t.Errorf("deny [github-sandbox] refused unrelated %s: %v", readIssuesGrant, err)
	}

	allowSandbox := mustParse(t, "capabilities:\n  allow: [github-sandbox]\n")
	if err := allowSandbox.CheckClaims(model.Task{Grants: []model.Grant{{Capability: sandboxGrant}}}, ""); err != nil {
		t.Errorf("allow [github-sandbox] refused %s: %v", sandboxGrant, err)
	}
	if err := allowSandbox.CheckClaims(model.Task{Grants: []model.Grant{{Capability: readIssuesGrant}}}, ""); err == nil {
		t.Errorf("allow [github-sandbox] admitted unrelated %s", readIssuesGrant)
	}
}
