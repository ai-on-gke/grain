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
		// A retired bare name in a policy file still reaches the grant
		// it became: migration 84 rewrote git-push-any-ref on the task,
		// not here.
		{"deny: [git-push-any-ref]", model.GitPushAnyRefGrant, "forbids capability"},
		{"deny: [git-push-any-ref]", model.RepoWriteRef + `={"refs":["refs/tags/*","refs/heads/*"],"force":true}`, "forbids capability"},
		{"deny: [Git-Push-Any-Ref]", model.GitPushAnyRefGrant, "forbids capability"},
		{"deny: [\"" + model.RepoWriteRef + "\"]", model.GitPushAnyRefCapability, "forbids capability"},
		{"allow: [git-push-any-ref]", model.GitPushAnyRefGrant, ""},
		{"allow: [git-push-any-ref]", model.GrainSQLWriteGrant, "does not allow capability"},
		{"allow: [db-read]", model.GrainSQLWriteGrant, "does not allow capability"},
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
	} {
		task.Grants = []model.Grant{{Capability: model.SelfDebugCapability}, {Capability: grant}}
		repo := strings.SplitN(grant, ":", 2)[1]
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
		"repo: [acme/widgets]\n",                     // misspelt key
		"repos: [widgets]\n",                         // not owner/name
		"repos: [a/b/c/d]\n",                         // too many segments
		"repos: [\"acme/[\"]\n",                      // bad pattern
		"repos: [acme/w]\nnetwork: [x]\n",            // unknown access
		"repos: acme/widgets\n",                      // not a list
		"capabilities:\n  deny: [\"gcp-key:[\"]\n",   // bad parameter pattern
		"capabilities:\n  allow: [\"[\"]\n",          // bad family pattern
		"capabilities:\n  deny: [\"repo-write:\"]\n", // empty parameter
		"capabilities:\n  deny: [\":x\"]\n",          // empty family
		"repos: [acme/w]\n---\nrepos: [evil/x]\n",    // a second document (GSA1-14)
		"---\nrepos: [acme/w]\n---\n{}\n",            // likewise, however small
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
