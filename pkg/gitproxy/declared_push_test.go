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
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
)

// stubDeclaredPush is a BranchLookup whose run was filed from a template
// declaring push.
type stubDeclaredPush struct {
	stubBranchLookup
	declared *model.DeclaredPush
}

func (s stubDeclaredPush) GitDeclaredPush(context.Context, string) (*model.DeclaredPush, error) {
	return s.declared, nil
}

func releaseDeclaration(push ...model.RefPush) *model.DeclaredPush {
	return &model.DeclaredPush{
		TaskID:     "2112",
		TemplateID: "decl-template-acme-widgets-release",
		Declaration: model.RepoDeclaration{
			Kind: model.DeclaredTemplate, ID: "decl-template-acme-widgets-release",
			Repo: model.RepoRef{Owner: "acme", Name: "widgets"},
			Path: ".grain/templates/release.yml", Commit: strings.Repeat("c", 40),
		},
		Push: push,
	}
}

func TestADeclaredPushSetIsWhatARunMayPushBeyondItsBranch(t *testing.T) {
	ctx := context.Background()
	own := stubBranchLookup{branch: "grain/task-2112", live: true}
	authorize := func(d *model.DeclaredPush, owner, repo string, updates ...RefUpdate) string {
		t.Helper()
		a := &ModelAuthorizer{Store: stubScopeLookup{}, Branches: stubDeclaredPush{stubBranchLookup: own, declared: d}}
		reason, err := a.AuthorizeRefUpdates(ctx, "sandbox-0", owner, repo, updates)
		if err != nil {
			t.Fatal(err)
		}
		return reason
	}
	decl := releaseDeclaration(
		model.RefPush{Pattern: "refs/heads/candidate", Force: true},
		model.RefPush{Pattern: "refs/heads/main"},
		model.RefPush{Pattern: "refs/heads/scratch", Delete: true},
		model.RefPush{Pattern: "refs/tags/v*"},
		model.RefPush{Pattern: "refs/tags/latest", Force: true},
	)

	for name, u := range map[string]RefUpdate{
		"a declared branch created":         {Old: zero, New: oidB, Ref: "refs/heads/main"},
		"a force-declared branch moved":     {Old: oidA, New: oidB, Ref: "refs/heads/candidate"},
		"a declared tag created by a glob":  {Old: zero, New: oidB, Ref: "refs/tags/v1.12.0-rc.1"},
		"a force-declared tag moved":        {Old: oidA, New: oidB, Ref: "refs/tags/latest"},
		"a delete-declared branch deleted":  {Old: oidA, New: zero, Ref: "refs/heads/scratch"},
		"the run's own branch, as ever":     {Old: oidA, New: oidB, Ref: "refs/heads/grain/task-2112"},
		"in the declaring repo, any casing": {Old: zero, New: oidB, Ref: "refs/heads/main"},
	} {
		owner, repo := "acme", "widgets"
		if strings.Contains(name, "casing") {
			owner, repo = "Acme", "Widgets"
		}
		if reason := authorize(decl, owner, repo, u); reason != "" {
			t.Errorf("%s: refused: %s", name, reason)
		}
	}

	for name, tc := range map[string]struct {
		u    RefUpdate
		want string
	}{
		"an undeclared branch": {RefUpdate{Old: oidA, New: oidB, Ref: "refs/heads/release"},
			"refs/heads/release is not declared"},
		"an undeclared tag": {RefUpdate{Old: zero, New: oidB, Ref: "refs/tags/nightly"},
			"refs/tags/nightly is not declared"},
		"a glob does not cross a slash": {RefUpdate{Old: zero, New: oidB, Ref: "refs/heads/main/x"},
			"refs/heads/main/x is not declared"},
		"a declared branch deleted without delete": {RefUpdate{Old: oidA, New: zero, Ref: "refs/heads/main"},
			"not deleted"},
		"a declared tag moved without force": {RefUpdate{Old: oidA, New: oidB, Ref: "refs/tags/v1.11.0"},
			"moving a tag is a force-push"},
		// No pack in hand, so no fast-forward shown: refused unless the
		// entry allows force (the end-to-end test below pushes a real one).
		"a declared branch moved without force": {RefUpdate{Old: oidA, New: oidB, Ref: "refs/heads/main"},
			"could not show"},
	} {
		reason := authorize(decl, "acme", "widgets", tc.u)
		if !strings.Contains(reason, tc.want) || !strings.Contains(reason, ".grain/templates/release.yml") {
			t.Errorf("%s: reason %q, want it to say %q and name the declaration file", name, reason, tc.want)
		}
	}

	// One undeclared ref refuses the whole push, as it always has.
	if reason := authorize(decl, "acme", "widgets",
		RefUpdate{Old: zero, New: oidB, Ref: "refs/tags/v2"},
		RefUpdate{Old: zero, New: oidB, Ref: "refs/heads/other"}); !strings.Contains(reason, "refs/heads/other") {
		t.Errorf("a push mixing declared and undeclared refs: %q", reason)
	}
	// The push set is the declaring repository's and nobody else's.
	if reason := authorize(decl, "acme", "gadgets", RefUpdate{Old: zero, New: oidB, Ref: "refs/heads/main"}); !strings.Contains(reason, "for acme/widgets only") {
		t.Errorf("a declaration for acme/widgets let a push into acme/gadgets: %q", reason)
	}
	// A task filed from no declared template is held to its branch, and
	// told where a push set would come from.
	if reason := authorize(nil, "acme", "widgets", RefUpdate{Old: zero, New: oidB, Ref: "refs/heads/main"}); !strings.Contains(reason, "push:") {
		t.Errorf("an undeclared run's refusal does not point at push: -- %q", reason)
	}
}

// The task's acceptance walkthrough, end to end and with no admin grant
// anywhere: a repository declares a release template with a push set and
// a schedule firing it; the declaration sync stores both; the schedule
// fires; its task's run pushes `candidate` (force), `main` (as a
// fast-forward) and a release tag through the proxy with a real git
// client -- and everything else, including what it declares for itself
// by editing `.grain` on its own branch, is refused.
func TestADeclaredSchedulesRunPushesItsDeclaredRefs(t *testing.T) {
	store, _ := sqlitetest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 18, 0, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	tmplID := model.RepoDeclaredID(model.DeclaredTemplate, repo, "release")
	schedID := model.RepoDeclaredID(model.DeclaredSchedule, repo, "release")
	target := repo
	if _, err := store.SyncRepoDeclarations(ctx, repo, strings.Repeat("d", 40), model.RepoDeclarations{
		Templates: []model.DeclaredTemplateFile{{
			Path: ".grain/templates/release.yml",
			Template: model.Template{
				ID: tmplID, Name: "Release", Title: "Cut a release", Body: "Cut it.", Target: &target,
			},
			Push: model.RefPushSet{
				{Pattern: "refs/heads/candidate", Force: true},
				{Pattern: "refs/heads/main"},
				{Pattern: "refs/tags/v*"},
			},
		}},
		Schedules: []model.DeclaredScheduleFile{{
			Path: ".grain/schedules/release.yml",
			Schedule: model.Schedule{
				ID: schedID, Title: "release", Target: repo, TemplateID: &tmplID,
				Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 24}, Enabled: true,
			},
		}},
	}, now, time.UTC); err != nil {
		t.Fatalf("syncing the declarations: %v", err)
	}
	sched, err := store.GetSchedule(ctx, schedID)
	if err != nil || sched == nil {
		t.Fatalf("reading the declared schedule: %v, %v", sched, err)
	}
	firing, err := store.FireSchedule(ctx, *sched, now, time.UTC, model.RepoRef{})
	if err != nil || firing.TaskID == "" {
		t.Fatalf("firing the schedule: %+v, %v", firing, err)
	}
	filed, err := store.GetTask(ctx, firing.TaskID)
	if err != nil || filed == nil {
		t.Fatalf("reading the filed task: %v", err)
	}
	if filed.TemplateID != tmplID {
		t.Fatalf("the filed task's TemplateID = %q, want %q", filed.TemplateID, tmplID)
	}
	if len(filed.Grants) != 0 {
		t.Fatalf("the filed task holds grants %v; this walkthrough is the push with none", filed.Grants)
	}
	if err := store.StartRun(ctx, model.Run{ID: "r1", TaskID: filed.ID, Attempt: 1, StartedAt: now}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunSandbox(ctx, "r1", "sandbox-0", filed.Target); err != nil {
		t.Fatal(err)
	}
	branch := filed.BranchName()

	// The repository, served locally by git http-backend: main, and a
	// candidate that main does not descend from, as after a promotion.
	root := t.TempDir()
	state := filepath.Join(root, "state")
	runLocalGit(t, "", "init", "-q", "-b", "main", state)
	gitAs := []string{"-c", "user.name=grain", "-c", "user.email=grain@example.invalid"}
	runLocalGit(t, state, append(gitAs, "commit", "-q", "--allow-empty", "-m", "Initial")...)
	runLocalGit(t, state, "checkout", "-q", "-b", "candidate")
	runLocalGit(t, state, append(gitAs, "commit", "-q", "--allow-empty", "-m", "Candidate fix")...)
	runLocalGit(t, state, "checkout", "-q", "--detach", "main")

	tokens, err := LoadSandboxTokens(writeTokensFile(t, t.TempDir(), map[string]string{"sandbox-0": "tok0"}))
	if err != nil {
		t.Fatal(err)
	}
	audit := &RecordingAuditLog{}
	proxy := &GitProxy{
		Authorizer: &ReadOnlyScopeAuthorizer{
			Scopes: NewReadScopes(), WriteScopes: NewWriteScopes(),
			Next: &ModelAuthorizer{Store: store},
		},
		Tokens:    tokens,
		Forwarder: &FakeForwarder{},
		Audit:     audit,
		Local:     &LocalRepository{Owner: "acme", Name: "widgets", Dir: state},
	}
	srv := httptest.NewServer(NewHandler(proxy))
	t.Cleanup(srv.Close)
	url := strings.Replace(srv.URL, "http://", "http://x:tok0@", 1) + "/acme/widgets.git"

	client := filepath.Join(root, "client")
	runLocalGit(t, "", "clone", "-q", url, client)
	runLocalGit(t, client, "config", "user.name", "sandbox")
	runLocalGit(t, client, "config", "user.email", "sandbox@example.invalid")
	tip := func(ref string) string {
		out, _ := localGitOutput(t, state, "", "rev-parse", "--verify", "-q", ref)
		return strings.TrimSpace(out)
	}
	head := func() string {
		out, _ := localGitOutput(t, client, "", "rev-parse", "HEAD")
		return strings.TrimSpace(out)
	}
	push := func(args ...string) (string, error) {
		return localGitOutput(t, client, "", append([]string{"push", "origin"}, args...)...)
	}

	// main, as a fast-forward with a commit made here: allowed, shown from
	// the commits the push carries.
	runLocalGit(t, client, "commit", "-q", "--allow-empty", "-m", "Release notes")
	if out, err := push("HEAD:refs/heads/main"); err != nil {
		t.Fatalf("a fast-forward of declared main was refused: %v\n%s", err, out)
	}
	if got := tip("refs/heads/main"); got != head() {
		t.Errorf("main = %s, want %s", got, head())
	}

	// The run widens its own push set on its branch. Its own branch takes
	// the commit -- and the widening does nothing at all.
	if err := os.MkdirAll(filepath.Join(client, ".grain", "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	widened := "title: Cut a release\nbody: Cut it.\npush:\n  - refs/heads/candidate\n  - refs/heads/main\n  - refs/tags/v*\n  - refs/heads/sneaky\n"
	if err := os.WriteFile(filepath.Join(client, ".grain", "templates", "release.yml"), []byte(widened), 0o644); err != nil {
		t.Fatal(err)
	}
	runLocalGit(t, client, "add", ".grain")
	runLocalGit(t, client, "commit", "-q", "-m", "A wider push set")
	if out, err := push("HEAD:refs/heads/" + branch); err != nil {
		t.Fatalf("pushing the run's own branch: %v\n%s", err, out)
	}
	if out, err := push("HEAD:refs/heads/sneaky"); err == nil || !strings.Contains(out, "refs/heads/sneaky is not declared") {
		t.Errorf("a push the run declared for itself on its own branch: err %v\n%s", err, out)
	}

	// main, rewritten: main's entry does not allow force.
	runLocalGit(t, client, "reset", "-q", "--hard", "HEAD~2")
	runLocalGit(t, client, "commit", "-q", "--allow-empty", "-m", "Release notes, rewritten")
	if out, err := push("--force", "HEAD:refs/heads/main"); err == nil || !strings.Contains(out, "could not show") {
		t.Errorf("a force-push of main, declared without force: err %v\n%s", err, out)
	}
	runLocalGit(t, client, "fetch", "-q", "origin")
	runLocalGit(t, client, "reset", "-q", "--hard", "origin/main")

	// candidate, force-moved to main's tip: declared force.
	if out, err := push("--force", "HEAD:refs/heads/candidate"); err != nil {
		t.Fatalf("force-moving declared candidate was refused: %v\n%s", err, out)
	}
	if got := tip("refs/heads/candidate"); got != head() {
		t.Errorf("candidate = %s, want main's tip %s", got, head())
	}
	// A release tag, by the v* glob.
	runLocalGit(t, client, "tag", "v1.12.0-rc.1")
	if out, err := push("refs/tags/v1.12.0-rc.1"); err != nil {
		t.Fatalf("pushing a declared release tag was refused: %v\n%s", err, out)
	}
	if tip("refs/tags/v1.12.0-rc.1") == "" {
		t.Error("the release tag did not reach the repository")
	}

	// main, fast-forwarded to a commit the remote already has: the release
	// cut's back-merge, pushed to its own branch and verified there before
	// main moves. The push to main carries no commits at all, so the proof
	// comes from the repository's own (grain/task-3546).
	runLocalGit(t, client, "commit", "-q", "--allow-empty", "-m", "Back-merge")
	if out, err := push("--force", "HEAD:refs/heads/"+branch); err != nil {
		t.Fatalf("pushing the back-merge to the run's own branch: %v\n%s", err, out)
	}
	if out, err := push("HEAD:refs/heads/main"); err != nil {
		t.Fatalf("a fast-forward of declared main to a commit the remote has was refused: %v\n%s", err, out)
	}
	if got := tip("refs/heads/main"); got != head() {
		t.Errorf("main = %s, want %s", got, head())
	}
	// A commit made here on top of one the remote has: the walk leaves the
	// pack at its parent and carries on in the repository.
	runLocalGit(t, client, "commit", "-q", "--allow-empty", "-m", "Verified")
	if out, err := push("HEAD:refs/heads/" + branch); err != nil {
		t.Fatalf("pushing the run's own branch: %v\n%s", err, out)
	}
	runLocalGit(t, client, "commit", "-q", "--allow-empty", "-m", "On top")
	if out, err := push("HEAD:refs/heads/main"); err != nil {
		t.Fatalf("a fast-forward of declared main through a commit the remote has was refused: %v\n%s", err, out)
	}
	if got := tip("refs/heads/main"); got != head() {
		t.Errorf("main = %s, want %s", got, head())
	}
	// A commit the remote has that is not a fast-forward of main stays
	// refused: having the commit is not descending from main.
	runLocalGit(t, client, "reset", "-q", "--hard", "HEAD~1")
	runLocalGit(t, client, "commit", "-q", "--allow-empty", "-m", "Sibling")
	if out, err := push("--force", "HEAD:refs/heads/"+branch); err != nil {
		t.Fatalf("pushing the run's own branch: %v\n%s", err, out)
	}
	if out, err := push("--force", "HEAD:refs/heads/main"); err == nil || !strings.Contains(out, "could not show") {
		t.Errorf("a force-push of main to a commit the remote has, declared without force: err %v\n%s", err, out)
	}
	asked := 0
	for _, e := range audit.Entries {
		if strings.HasPrefix(e.Outcome, "fast-forward check: ") {
			asked++
		}
	}
	if asked == 0 {
		t.Error("no fast-forward check asked the repository, according to the audit log")
	}

	// Everything not declared stays refused.
	runLocalGit(t, client, "tag", "latest")
	for name, args := range map[string][]string{
		"an undeclared tag":           {"refs/tags/latest"},
		"deleting declared candidate": {":refs/heads/candidate"},
	} {
		if out, err := push(args...); err == nil || !strings.Contains(out, ".grain/templates/release.yml") {
			t.Errorf("%s: err %v\n%s", name, err, out)
		}
	}

	// And each ref the run moved besides its own branch is in the audit
	// log, naming the task, the template and the declaration file.
	allowed := map[string]bool{}
	for _, e := range audit.Entries {
		if e.Ref == "" {
			continue
		}
		if e.Task != filed.ID || e.Template != tmplID || e.Declaration != ".grain/templates/release.yml" {
			t.Errorf("audit entry %+v does not name the task, template and declaration", e)
		}
		allowed[e.Ref] = true
	}
	for _, ref := range []string{"refs/heads/main", "refs/heads/candidate", "refs/tags/v1.12.0-rc.1"} {
		if !allowed[ref] {
			entries, _ := json.MarshalIndent(audit.Entries, "", "  ")
			t.Errorf("no audit entry for %s:\n%s", ref, entries)
		}
	}
}
