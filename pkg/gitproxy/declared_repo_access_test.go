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

package gitproxy

import (
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/repodecl"
)

// A template a repository declares with `capabilities: [repo-write:X]`
// files tasks the proxy lets push X -- with the filing team's own
// credential, the authority behind the declaration -- and one declared
// without it files tasks that cannot reach X at all (grain/task-1780).
// End to end from the file: parsed by pkg/repodecl, synced into the
// store, fired by a declared schedule, and asked of the proxy by the
// sandbox the filed task runs in.
func TestADeclaredTemplatesRepoWriteIsPushedWithTheDeploymentsCredential(t *testing.T) {
	t.Parallel()
	store, ctx := openStoreForTest(t)
	now := time.Now().UTC()

	// The deployment's connection, which its work pushes with.
	if err := store.UpdateConfig(ctx, func(tm *model.Config) error {
		tm.GitCredential = "infra-cred"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	tools := model.RepoRef{Owner: "acme", Name: "tools"}

	files := fstest.MapFS{}
	for name, body := range map[string]string{
		".grain/templates/sync.yml":  "title: Sync tools\nteam: default\ncapabilities: [\"repo-write:acme/tools\"]\n",
		".grain/templates/plain.yml": "title: Tidy widgets\nteam: default\n",
		".grain/schedules/sync.yml":  "template: sync\nteam: default\ndaily:\n  at: \"09:00\"\n",
		".grain/schedules/plain.yml": "template: plain\nteam: default\ndaily:\n  at: \"09:00\"\n",
	} {
		files[name] = &fstest.MapFile{Data: []byte(body)}
	}
	declared, err := repodecl.Load(files, widgets)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := store.SyncRepoDeclarations(ctx, widgets, "c1", declared, now, nil); err != nil {
		t.Fatalf("SyncRepoDeclarations: %v", err)
	}

	sandboxes := map[string]string{}
	for i, stem := range []string{"sync", "plain"} {
		sched, err := store.GetSchedule(ctx, model.RepoDeclaredID(model.DeclaredSchedule, widgets, stem))
		if err != nil || sched == nil {
			t.Fatalf("schedule %s: %v %v", stem, sched, err)
		}
		fired, err := store.FireScheduleNow(ctx, *sched, now, model.RepoRef{})
		if err != nil || fired.TaskID == "" {
			t.Fatalf("firing %s: %+v %v", stem, fired, err)
		}
		task, err := store.GetTask(ctx, fired.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		sandbox := "sandbox-" + stem
		if err := store.StartRun(ctx, model.Run{
			ID: "run-" + stem, TaskID: task.ID, Sandbox: sandbox, Attempt: 1, StartedAt: now.Add(time.Duration(i) * time.Second),
		}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		sandboxes[stem] = task.BranchName()
	}

	credsDir := writeCredentialSet(t, map[string]string{"*": "deployment-cred"}, map[string]string{
		"infra-cred":      "token-infra",
		"deployment-cred": "token-deployment",
	})
	credentials, err := LoadCredentialSet(credsDir)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := LoadSandboxTokens(writeTokensFile(t, t.TempDir(), map[string]string{
		"sandbox-sync": "tok-sync", "sandbox-plain": "tok-plain",
	}))
	if err != nil {
		t.Fatal(err)
	}
	forwarder := &FakeForwarder{Response: UpstreamResponse{Status: 200, Headers: map[string]string{"Content-Type": "x"}, Body: []byte("ok")}}
	proxy := &GitProxy{
		Authorizer:          &ModelAuthorizer{Store: store},
		Credentials:         credentials,
		TeamCredentials:     ladderConnections{creds: credentials},
		Tokens:              tokens,
		Forwarder:           forwarder,
		Audit:               &RecordingAuditLog{},
		CredentialOverrides: store,
	}
	push := func(token, branch string) ProxyResponse {
		headers := merge(gitHeaders, map[string]string{
			"Authorization": basicAuthHeader(token),
			"Accept":        "application/x-git-receive-pack-result",
			"Content-Type":  "application/x-git-receive-pack-request",
		})
		return proxy.Handle(ctx, "POST", "/"+tools.String()+".git/git-receive-pack", "",
			headers, pushBody(oidA+" "+oidB+" refs/heads/"+branch))
	}

	// Declared: the push to acme/tools goes out, on the team's token.
	if resp := push("tok-sync", sandboxes["sync"]); resp.Status != 200 {
		t.Fatalf("declared push status = %d, want 200: %s", resp.Status, resp.Body)
	}
	if len(forwarder.Calls) != 1 {
		t.Fatalf("forwarded %d requests, want 1", len(forwarder.Calls))
	}
	if got := forwarder.Calls[0].Token; got == nil || *got != "token-infra" {
		t.Errorf("declared push went out with %v, want the infra team's token", got)
	}

	// Undeclared: acme/tools is not in scope at all, for push or fetch.
	if resp := push("tok-plain", sandboxes["plain"]); resp.Status != 403 {
		t.Errorf("undeclared push status = %d, want 403: %s", resp.Status, resp.Body)
	}
	fetch := proxy.Handle(ctx, "GET", "/"+tools.String()+".git/info/refs", "service=git-upload-pack",
		merge(gitHeaders, map[string]string{"Authorization": basicAuthHeader("tok-plain")}), nil)
	if fetch.Status != 403 {
		t.Errorf("undeclared fetch status = %d, want 403: %s", fetch.Status, fetch.Body)
	}
	if len(forwarder.Calls) != 1 {
		t.Errorf("forwarded %d requests, want only the declared one", len(forwarder.Calls))
	}
}

// The team's TargetRepos bounds what a declaration may reach: a template
// declaring access to a repository its team may not work in refuses the
// whole sync, the way naming that team for a repository it may not work
// in already does.
func TestADeclaredTemplateCannotReachPastTheDeploymentsTargetRepos(t *testing.T) {
	t.Parallel()
	store, ctx := openStoreForTest(t)
	// The deployment's connection and target repos (Config.TargetRepos,
	// the default team's until schema 24).
	if err := store.UpdateConfig(ctx, func(tm *model.Config) error {
		tm.GitCredential = "infra-cred"
		tm.TargetRepos = []string{"acme/widgets"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	declared, err := repodecl.Load(fstest.MapFS{
		".grain/templates/sync.yml": &fstest.MapFile{Data: []byte(
			"title: Sync tools\nteam: default\ncapabilities: [\"repo-write:acme/tools\"]\n")},
	}, widgets)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err = store.SyncRepoDeclarations(ctx, widgets, "c1", declared, time.Now(), nil)
	if err == nil {
		t.Fatal("a template reaching a repository outside its team's was synced")
	}
	for _, want := range []string{".grain/templates/sync.yml", "this deployment may not work in", "acme/tools"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}
