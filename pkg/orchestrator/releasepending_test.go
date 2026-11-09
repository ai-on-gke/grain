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

package orchestrator

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/releaseapproval"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// fixtureForge is one GitHub repository's tags, release notes and
// workflow dispatches, served at the REST level so the real
// github.RESTClient reads it.
type fixtureForge struct {
	mu         sync.Mutex
	repo       string                // owner/name
	tags       map[string]fixtureTag // name -> tag
	files      map[string]string     // path on main -> contents
	dispatches []map[string]any      // each dispatch's body
	runs       []map[string]any      // workflow_runs served for promote-release.yml
	jobsByRun  map[string][]map[string]any
	logsByJob  map[string]string
	reads      int // matching-refs reads
}

type fixtureTag struct {
	commit  string
	message string // "" for a lightweight tag
}

var (
	matchingRefsPath = regexp.MustCompile(`^/repos/([^/]+/[^/]+)/git/matching-refs/tags/([^?]*)`)
	tagObjectPath    = regexp.MustCompile(`^/repos/([^/]+/[^/]+)/git/tags/(.+)$`)
	contentsPath     = regexp.MustCompile(`^/repos/([^/]+/[^/]+)/contents/([^?]+)\?ref=main$`)
	dispatchPath     = regexp.MustCompile(`^/repos/([^/]+/[^/]+)/actions/workflows/([^/]+)/dispatches$`)
	workflowRunsPath = regexp.MustCompile(`^/repos/([^/]+/[^/]+)/actions/workflows/([^/?]+)/runs\b`)
	runJobsPath      = regexp.MustCompile(`^/repos/([^/]+/[^/]+)/actions/runs/([0-9]+)/jobs\b`)
	jobLogsPath      = regexp.MustCompile(`^/repos/([^/]+/[^/]+)/actions/jobs/([0-9]+)/logs$`)
)

func (f *fixtureForge) Request(method, path string, _ map[string]string, body []byte) (github.ApiResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	jsonResp := func(v any) (github.ApiResponse, error) {
		b, _ := json.Marshal(v)
		return github.ApiResponse{Status: http.StatusOK, Body: b}, nil
	}
	notFound := github.ApiResponse{Status: http.StatusNotFound, Body: []byte(`{"message":"Not Found"}`)}
	switch {
	case method == "GET" && matchingRefsPath.MatchString(path):
		m := matchingRefsPath.FindStringSubmatch(path)
		if m[1] != f.repo {
			return notFound, nil
		}
		f.reads++
		var out []map[string]any
		for name, t := range f.tags {
			if !strings.HasPrefix(name, m[2]) {
				continue
			}
			obj := map[string]string{"sha": t.commit, "type": "commit"}
			if t.message != "" {
				obj = map[string]string{"sha": "tagobj-" + name, "type": "tag"}
			}
			out = append(out, map[string]any{"ref": "refs/tags/" + name, "object": obj})
		}
		return jsonResp(out)
	case method == "GET" && tagObjectPath.MatchString(path):
		m := tagObjectPath.FindStringSubmatch(path)
		name, ok := strings.CutPrefix(m[2], "tagobj-")
		t, found := f.tags[name]
		if m[1] != f.repo || !ok || !found {
			return notFound, nil
		}
		return jsonResp(map[string]any{"message": t.message, "object": map[string]string{"sha": t.commit, "type": "commit"}})
	case method == "GET" && contentsPath.MatchString(path):
		m := contentsPath.FindStringSubmatch(path)
		text, ok := f.files[m[2]]
		if m[1] != f.repo || !ok {
			return notFound, nil
		}
		return github.ApiResponse{Status: http.StatusOK, Body: []byte(text)}, nil
	case method == "POST" && dispatchPath.MatchString(path):
		m := dispatchPath.FindStringSubmatch(path)
		if m[1] != f.repo || m[2] != "promote-release.yml" {
			return notFound, nil
		}
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			return github.ApiResponse{Status: http.StatusUnprocessableEntity}, nil
		}
		f.dispatches = append(f.dispatches, req)
		return github.ApiResponse{Status: http.StatusNoContent}, nil
	case method == "GET" && workflowRunsPath.MatchString(path):
		m := workflowRunsPath.FindStringSubmatch(path)
		if m[1] != f.repo || m[2] != "promote-release.yml" || len(f.runs) == 0 {
			return notFound, nil
		}
		return jsonResp(map[string]any{"total_count": len(f.runs), "workflow_runs": f.runs})
	case method == "GET" && runJobsPath.MatchString(path):
		m := runJobsPath.FindStringSubmatch(path)
		if m[1] != f.repo {
			return notFound, nil
		}
		jobs := f.jobsByRun[m[2]]
		return jsonResp(map[string]any{"total_count": len(jobs), "jobs": jobs})
	case method == "GET" && jobLogsPath.MatchString(path):
		m := jobLogsPath.FindStringSubmatch(path)
		text, ok := f.logsByJob[m[2]]
		if m[1] != f.repo || !ok {
			return notFound, nil
		}
		return github.ApiResponse{Status: http.StatusOK, Body: []byte(text)}, nil
	}
	return notFound, nil
}

func (f *fixtureForge) setTag(name, commit, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tags[name] = fixtureTag{commit: commit, message: message}
}

func (f *fixtureForge) deleteTag(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.tags, name)
}

func (f *fixtureForge) dispatched() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.dispatches...)
}

func resetReleaseWatch(t *testing.T) {
	t.Helper()
	// The watcher logs what it raises; claim the log first so those lines
	// arrive under this test rather than as "no test to attach it to".
	logToTest(t)
	clear := func() {
		releasePendingPolled.Range(func(k, _ any) bool { releasePendingPolled.Delete(k); return true })
	}
	clear()
	t.Cleanup(clear)
}

// The whole of gap 4 on a fixture repository that releases from GitHub
// Actions: a -pending tag raises the notification with the release
// notes, a person's Approve dispatches promote-release.yml once with a
// record the deployment signed, an agent and a schedule are refused, and
// the notification clears when the release is tagged.
func TestAPendingTagOnAGitHubActionsRepoIsApprovedFromTheInbox(t *testing.T) {
	resetReleaseWatch(t)
	ctx := context.Background()
	store := openTestStoreForOrchestrator(t)
	repo := model.RepoRef{Owner: "example", Name: "widgets"}
	cfg := model.DefaultConfig()
	cfg.TargetRepos = []string{repo.String()}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGitHub}); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("c", 40)
	forge := &fixtureForge{repo: repo.String(), tags: map[string]fixtureTag{
		"v1.1.0":         {commit: strings.Repeat("b", 40)},
		"v1.2.0-rc.3":    {commit: commit, message: "rc"},
		"v1.2.0-pending": {commit: commit, message: "Release v1.2.0 pending approval\n\nQualification: PASS"},
	}, files: map[string]string{"docs/releases/v1.2.0.md": "# v1.2.0\n\n- Widgets now spin."}}
	client := github.NewClient(forge, nil)
	deps := Deps{Store: store, Client: client}
	now := time.Now().UTC()
	subject := model.NotificationSubject(model.FamilyReleasePending, "v1.2.0")

	// 1. The -pending tag raises release.pending, with the notes.
	if err := reconcileReleasePending(ctx, deps, now); err != nil {
		t.Fatal(err)
	}
	n, err := store.OpenNotification(ctx, subject)
	if err != nil || n == nil {
		t.Fatalf("no open %s after the watcher saw v1.2.0-pending (err %v)", subject, err)
	}
	if n.Repo != repo.String() || n.Title != "v1.2.0 is ready to promote" || strings.Join(n.Actions, ",") != "approve-release,reject-release" {
		t.Errorf("notification = %+v, want example/widgets' v1.2.0 with Approve and Reject", n)
	}
	for _, want := range []string{"Widgets now spin.", "Qualification: PASS", "docs/releases/v1.2.0.md"} {
		if !strings.Contains(n.Body, want) {
			t.Errorf("body does not say %q:\n%s", want, n.Body)
		}
	}
	// Within the poll interval nothing is read; after it, nothing is
	// raised again over the open one.
	reads := forge.reads
	_ = reconcileReleasePending(ctx, deps, now.Add(time.Minute))
	if forge.reads != reads {
		t.Errorf("the tags were read again within the poll interval")
	}
	_ = reconcileReleasePending(ctx, deps, now.Add(releasePendingPollInterval))
	if again, _ := store.OpenNotification(ctx, subject); again == nil || again.RaiseCount != 1 {
		t.Errorf("a second look raised it again: %+v", again)
	}

	person := model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com"}
	uic := ui.NewClient(ui.Config{Actor: person, DataDir: t.TempDir(), GitHubPullRequests: client}, store)

	// 2. Neither an agent nor a schedule's automation may answer it.
	for _, p := range []model.Principal{
		{Kind: model.PrincipalAgent, ID: "run-7"},
		{Kind: model.PrincipalAutomation, ID: "schedule:release-train"},
		{Kind: model.PrincipalService, ID: "steward"},
	} {
		if _, err := uic.DecideRelease(ui.WithPrincipal(ctx, p), repo.String(), "v1.2.0", true, ui.ReleaseApproval{}); err == nil ||
			!strings.Contains(err.Error(), "only a person may approve") {
			t.Errorf("%s approving: %v, want refused", model.PrincipalString(p), err)
		}
	}
	if d := forge.dispatched(); len(d) != 0 {
		t.Fatalf("a non-person's approval dispatched: %v", d)
	}

	// 3. A person's Approve dispatches promote-release.yml on main, once.
	decision, err := uic.DecideRelease(ui.WithPrincipal(ctx, person), repo.String(), "v1.2.0", true, ui.ReleaseApproval{})
	if err != nil {
		t.Fatalf("a person's approval: %v", err)
	}
	if decision.Dispatched == nil || decision.Dispatched.Commit != commit || decision.Dispatched.By != person.ID {
		t.Errorf("decision = %+v, want a dispatch for %s by %s", decision, commit, person.ID)
	}
	if _, err := uic.DecideRelease(ui.WithPrincipal(ctx, person), repo.String(), "v1.2.0", true, ui.ReleaseApproval{}); err == nil {
		t.Error("a second press was accepted; want it refused while the first dispatch's record is good")
	}
	d := forge.dispatched()
	if len(d) != 1 {
		t.Fatalf("dispatched %d times, want once", len(d))
	}
	inputs, _ := d[0]["inputs"].(map[string]any)
	if d[0]["ref"] != "main" || inputs["version"] != "v1.2.0" || inputs["decision"] != "approve" || inputs["short_soak_ok"] != "false" ||
		inputs["allow_breaking"] != "false" {
		t.Errorf("dispatch = %v, want v1.2.0 approve on main", d[0])
	}
	record := verifyDispatchedRecord(t, uic, inputs)
	for _, want := range []string{"repo: example/widgets\n", "version: v1.2.0\n", "decision: approve\n",
		"commit: " + commit + "\n", "approver: alice@example.com\n", "short_soak_ok: false\n", "allow_breaking: false\n"} {
		if !strings.Contains(record, want) {
			t.Errorf("the signed record does not say %q:\n%s", strings.TrimSpace(want), record)
		}
	}

	// The answer cleared the notification, and the watcher does not raise
	// it again while the promotion run has the -pending tag still there.
	if open, _ := store.OpenNotification(ctx, subject); open != nil {
		t.Errorf("%s still open after the approval", subject)
	}
	_ = reconcileReleasePending(ctx, deps, now.Add(2*releasePendingPollInterval))
	if open, _ := store.OpenNotification(ctx, subject); open != nil {
		t.Errorf("the watcher raised %s again right after a person answered it", subject)
	}

	// 4. The next release: raised, then cleared when vX.Y.0 appears.
	forge.setTag("v1.3.0-pending", strings.Repeat("d", 40), "Release v1.3.0 pending approval")
	_ = reconcileReleasePending(ctx, deps, now.Add(3*releasePendingPollInterval))
	next := model.NotificationSubject(model.FamilyReleasePending, "v1.3.0")
	if open, _ := store.OpenNotification(ctx, next); open == nil || !strings.Contains(open.Body, "has not merged yet") {
		t.Fatalf("v1.3.0-pending raised %+v, want an open notification saying its notes have not merged", open)
	}
	forge.setTag("v1.3.0", strings.Repeat("d", 40), "Release v1.3.0")
	forge.deleteTag("v1.3.0-pending")
	_ = reconcileReleasePending(ctx, deps, now.Add(4*releasePendingPollInterval))
	if open, _ := store.OpenNotification(ctx, next); open != nil {
		t.Errorf("%s still open after v1.3.0 was tagged", next)
	}

	// 5. Rolling back a promoted release on a GitHub Actions repo is
	// person-only too, and dispatches promote-release.yml with
	// decision=rollback and a signed record bound to v1.3.0's commit.
	for _, p := range []model.Principal{
		{Kind: model.PrincipalAgent, ID: "run-7"},
		{Kind: model.PrincipalAutomation, ID: "schedule:release-train"},
		{Kind: model.PrincipalService, ID: "steward"},
	} {
		if _, err := uic.RollbackRelease(ui.WithPrincipal(ctx, p), repo.String(), "v1.3.0", ui.ReleaseRollback{To: "v1.2.0"}); err == nil ||
			!strings.Contains(err.Error(), "only a person may roll back") {
			t.Errorf("%s rolling back: %v, want refused", model.PrincipalString(p), err)
		}
	}
	rb, err := uic.RollbackRelease(ui.WithPrincipal(ctx, person), repo.String(), "v1.3.0",
		ui.ReleaseRollback{To: "v1.2.0", Reason: "bad migration"})
	if err != nil {
		t.Fatalf("a person's rollback: %v", err)
	}
	if rb.Dispatched == nil || rb.Dispatched.Commit != strings.Repeat("d", 40) || rb.Dispatched.By != person.ID {
		t.Errorf("rollback decision = %+v, want a dispatch for v1.3.0 by %s", rb, person.ID)
	}
	d = forge.dispatched()
	if len(d) != 2 {
		t.Fatalf("dispatched %d times, want 2 (approve + rollback)", len(d))
	}
	rbInputs, _ := d[1]["inputs"].(map[string]any)
	if d[1]["ref"] != "main" || rbInputs["version"] != "v1.3.0" || rbInputs["decision"] != "rollback" ||
		rbInputs["rollback_to"] != "v1.2.0" || rbInputs["reason"] != "bad migration" {
		t.Errorf("rollback dispatch = %v, want v1.3.0 rollback on main", d[1])
	}
	rbRecord := verifyDispatchedRecord(t, uic, rbInputs)
	for _, want := range []string{"repo: example/widgets\n", "version: v1.3.0\n", "decision: rollback\n",
		"commit: " + strings.Repeat("d", 40) + "\n", "approver: alice@example.com\n", "rollback_to: v1.2.0\n"} {
		if !strings.Contains(rbRecord, want) {
			t.Errorf("the signed rollback record does not say %q:\n%s", strings.TrimSpace(want), rbRecord)
		}
	}
}

// verifyDispatchedRecord checks the dispatch's record against the
// deployment's public key, as scripts/verify-release-approval.sh does in
// the workflow, and returns its text.
func verifyDispatchedRecord(t *testing.T, uic *ui.Client, inputs map[string]any) string {
	t.Helper()
	keyPEM, err := uic.ReleaseApprovalKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		t.Fatal("the approval key is not PEM")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	record, err := base64.StdEncoding.DecodeString(fmt.Sprint(inputs["approval"]))
	if err != nil {
		t.Fatal(err)
	}
	sig, err := base64.StdEncoding.DecodeString(fmt.Sprint(inputs["approval_signature"]))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(record)
	if !ecdsa.VerifyASN1(pub.(*ecdsa.PublicKey), digest[:], sig) {
		t.Fatal("the dispatched record's signature does not verify against the deployment's key")
	}
	return string(record)
}

// A repository on grain CI is the train's own run to notify about; the
// watcher reads no tags there.
func TestTheReleaseWatchReadsOnlyGitHubActionsRepos(t *testing.T) {
	resetReleaseWatch(t)
	ctx := context.Background()
	store := openTestStoreForOrchestrator(t)
	repo := model.RepoRef{Owner: "example", Name: "gadgets"}
	cfg := model.DefaultConfig()
	cfg.TargetRepos = []string{repo.String()}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGrain}); err != nil {
		t.Fatal(err)
	}
	forge := &fixtureForge{repo: repo.String(), tags: map[string]fixtureTag{"v2.0.0-pending": {commit: "e"}}}
	if err := reconcileReleasePending(ctx, Deps{Store: store, Client: github.NewClient(forge, nil)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if forge.reads != 0 {
		t.Errorf("read a grain CI repository's tags %d times", forge.reads)
	}
}

// A repository that never set a CI Mode is an Actions repository -- what
// its repo page shows and what the merge queue gates it on -- so a
// person's Approve of its -pending tag dispatches promote-release.yml.
// Reading EffectiveRepoConfig's raw, empty CIMode sent it down the grain
// CI path, which refused it as "grain CI is not enabled"
// (docs/test-runs/2026-11-04-release-approval.md, F1). One that set none
// but declares grain CI workflows is still grain CI's.
func TestAnActionsRepoThatNeverSetACIModeIsApprovedThroughActions(t *testing.T) {
	ctx := context.Background()
	store := openTestStoreForOrchestrator(t)
	repo := model.RepoRef{Owner: "example", Name: "unset"}
	declaring := model.RepoRef{Owner: "example", Name: "declares"}
	cfg := model.DefaultConfig()
	cfg.TargetRepos = []string{repo.String(), declaring.String()}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoWorkflows(ctx, declaring, []model.RepoWorkflow{{Repo: declaring, Path: ".grain/workflows/tests.yml",
		Text: "jobs:\n  t:\n    steps:\n      - run: true\n"}}); err != nil {
		t.Fatal(err)
	}
	for r, want := range map[model.RepoRef]bool{repo: true, declaring: false} {
		if got, err := store.ReleasesFromGitHubActions(ctx, r); err != nil || got != want {
			t.Errorf("ReleasesFromGitHubActions(%s) = %v, %v; want %v", r, got, err, want)
		}
	}
	commit := strings.Repeat("d", 40)
	forge := &fixtureForge{repo: repo.String(), tags: map[string]fixtureTag{
		"v1.0.1-pending": {commit: commit, message: "patch"},
	}}
	client := github.NewClient(forge, nil)
	person := model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com"}
	uic := ui.NewClient(ui.Config{Actor: person, DataDir: t.TempDir(), GitHubPullRequests: client}, store)
	decision, err := uic.DecideRelease(ui.WithPrincipal(ctx, person), repo.String(), "v1.0.1", true, ui.ReleaseApproval{})
	if err != nil {
		t.Fatalf("a person's approval of a patch release on a repository with no CI Mode set: %v", err)
	}
	if decision.Dispatched == nil || decision.Dispatched.Commit != commit {
		t.Errorf("decision = %+v, want a promote-release.yml dispatch for %s", decision, commit)
	}
	if d := forge.dispatched(); len(d) != 1 {
		t.Fatalf("dispatched %d times, want once", len(d))
	}
}

type multiFixtureForge map[string]*fixtureForge

func (m multiFixtureForge) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	if rest, ok := strings.CutPrefix(path, "/repos/"); ok {
		if idx := strings.IndexByte(rest, '?'); idx >= 0 {
			rest = rest[:idx]
		}
		parts := strings.SplitN(rest, "/", 3)
		if len(parts) >= 2 {
			if f, ok := m[parts[0]+"/"+parts[1]]; ok {
				return f.Request(method, path, headers, body)
			}
		}
	}
	return github.ApiResponse{Status: http.StatusNotFound, Body: []byte(`{"message":"Not Found"}`)}, nil
}

// Both kinds of default-mode GitHub Actions repository -- a concrete target
// repository with no repo_config row at all, and a repository whose row left
// CIMode unset -- have their tags watched while their release work item runs
// and TargetRepos admits them; repositories whose release item is off, that
// declare grain CI workflows, set ci_mode: none, or sit outside TargetRepos
// are not polled (docs/test-runs/2026-11-04-release-approval.md, F1b).
func TestTheReleaseWatchRaisesPendingForDefaultModeActionsRepos(t *testing.T) {
	resetReleaseWatch(t)
	ctx := context.Background()
	store := openTestStoreForOrchestrator(t)

	noRow := model.RepoRef{Owner: "example", Name: "norow"}
	unsetRow := model.RepoRef{Owner: "other", Name: "unsetrow"}
	releaseOff := model.RepoRef{Owner: "example", Name: "releaseoff"}
	declaring := model.RepoRef{Owner: "example", Name: "declares"}
	ciNone := model.RepoRef{Owner: "example", Name: "cinone"}
	offAllowlist := model.RepoRef{Owner: "stranger", Name: "repo"}

	cfg := model.DefaultConfig()
	cfg.TargetRepos = []string{
		noRow.String(),
		"other/*",
		releaseOff.String(),
		declaring.String(),
		ciNone.String(),
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	for _, rc := range []model.RepoConfig{
		{Repo: unsetRow, SetupCommand: "make deps"},
		{Repo: releaseOff, Work: model.ItemOwnership(model.ReleaseItem, model.OwnershipOff)},
		{Repo: ciNone, CIMode: model.CIModeNone},
		{Repo: offAllowlist, SetupCommand: "true"},
	} {
		if err := store.PutRepoConfig(ctx, rc); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutRepoWorkflows(ctx, declaring, []model.RepoWorkflow{{
		Repo: declaring, Path: ".grain/workflows/tests.yml",
		Text: "jobs:\n  t:\n    steps:\n      - run: true\n",
	}}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetRepoConfig(ctx, noRow); err != nil || got != nil {
		t.Fatalf("GetRepoConfig(%s) = %+v, %v; want nil row for a repository left on every default", noRow, got, err)
	}

	forges := multiFixtureForge{}
	seed := func(r model.RepoRef, version string) *fixtureForge {
		f := &fixtureForge{
			repo: r.String(),
			tags: map[string]fixtureTag{version + "-pending": {commit: strings.Repeat("a", 40), message: "staged"}},
		}
		forges[r.String()] = f
		return f
	}
	noRowForge := seed(noRow, "v3.0.0")
	unsetRowForge := seed(unsetRow, "v3.1.0")
	releaseOffForge := seed(releaseOff, "v3.2.0")
	declaringForge := seed(declaring, "v3.3.0")
	ciNoneForge := seed(ciNone, "v3.4.0")
	offAllowlistForge := seed(offAllowlist, "v3.5.0")

	now := time.Now().UTC()
	deps := Deps{Store: store, Client: github.NewClient(forges, nil)}
	if err := reconcileReleasePending(ctx, deps, now); err != nil {
		t.Fatalf("reconcileReleasePending: %v", err)
	}

	for repo, version := range map[model.RepoRef]string{noRow: "v3.0.0", unsetRow: "v3.1.0"} {
		subject := model.NotificationSubject(model.FamilyReleasePending, version)
		n, err := store.OpenNotification(ctx, subject)
		if err != nil || n == nil || n.Repo != repo.String() {
			t.Errorf("OpenNotification(%s) = %+v, %v; want open for %s", subject, n, err, repo)
		}
	}
	if noRowForge.reads != 1 {
		t.Errorf("reads on %s = %d, want 1", noRow, noRowForge.reads)
	}
	if unsetRowForge.reads != 1 {
		t.Errorf("reads on %s = %d, want 1", unsetRow, unsetRowForge.reads)
	}
	for name, f := range map[string]*fixtureForge{
		"release work item off":      releaseOffForge,
		"declaring .grain/workflows": declaringForge,
		"ci_mode: none":              ciNoneForge,
		"outside Config.TargetRepos": offAllowlistForge,
	} {
		if f.reads != 0 {
			t.Errorf("read %s (%s) %d times, want 0", f.repo, name, f.reads)
		}
	}
}

// A repository that never set a CI Mode -- either because it has no
// repo_config row at all (only TargetRepos names it, as in `grain demo
// -github-sim`) or because its repo_config row sets other defaults and
// leaves ci_mode empty -- still releases from GitHub Actions, so its
// -pending tag raises release.pending without an explicit ci_mode: github
// (docs/test-runs/2026-11-04-release-approval.md, F1b). Unset repositories
// where grain CI is in effect (.grain/workflows, IgnoreGitHubCI), ones
// with ci_mode: none, ones with release work turned off, git-only/plain-git
// repositories behind a wrapped Client, retired grain/config-<team>
// aliases, and ones off TargetRepos are not polled.
func TestAnActionsRepoThatNeverSetACIModeRaisesReleasePending(t *testing.T) {
	resetReleaseWatch(t)
	ctx := context.Background()
	store := openTestStoreForOrchestrator(t)

	noRow := model.RepoRef{Owner: "example", Name: "norow"}
	unsetRow := model.RepoRef{Owner: "example", Name: "unsetrow"}
	declaring := model.RepoRef{Owner: "example", Name: "declares"}
	legacyGrain := model.RepoRef{Owner: "example", Name: "legacygrain"}
	noneMode := model.RepoRef{Owner: "example", Name: "nonemode"}
	releaseOff := model.RepoRef{Owner: "example", Name: "releaseoff"}
	sshOnly := model.RepoRef{Owner: "example", Name: "sshonly"}
	plainGit := model.RepoRef{Host: "git.example.com", Owner: "example", Name: "plaingit"}
	retiredCfg := model.RepoRef{Owner: "grain", Name: "config-legacy"}
	offList := model.RepoRef{Owner: "example", Name: "offlist"}

	cfg := model.DefaultConfig()
	cfg.TargetRepos = []string{
		noRow.String(),
		unsetRow.String(),
		declaring.String(),
		legacyGrain.String(),
		noneMode.String(),
		releaseOff.String(),
		sshOnly.String(),
		plainGit.String(),
		retiredCfg.String(),
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: unsetRow, SetupCommand: "make deps"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoWorkflows(ctx, declaring, []model.RepoWorkflow{{
		Repo: declaring, Path: ".grain/workflows/tests.yml",
		Text: "jobs:\n  t:\n    steps:\n      - run: true\n",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: legacyGrain, IgnoreGitHubCI: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: noneMode, CIMode: model.CIModeNone}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: releaseOff, Work: model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: retiredCfg, SetupCommand: "make deps"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: offList, SetupCommand: "make deps"}); err != nil {
		t.Fatal(err)
	}

	forges := map[model.RepoRef]*fixtureForge{
		noRow: {
			repo: noRow.String(),
			tags: map[string]fixtureTag{
				"v2.5.0-pending": {commit: strings.Repeat("a", 40), message: "v2.5.0 staged"},
			},
			files: map[string]string{"docs/releases/v2.5.0.md": "# v2.5.0\n\n- Unset mode watched."},
		},
		unsetRow: {
			repo: unsetRow.String(),
			tags: map[string]fixtureTag{
				"v1.4.0-pending": {commit: strings.Repeat("b", 40), message: "v1.4.0 staged"},
			},
		},
	}
	skippedRepos := []model.RepoRef{declaring, legacyGrain, noneMode, releaseOff, sshOnly, plainGit, retiredCfg, offList}
	for _, skipped := range skippedRepos {
		forges[skipped] = &fixtureForge{
			repo: skipped.String(),
			tags: map[string]fixtureTag{"v9.0.0-pending": {commit: "deadbeef"}},
		}
	}
	router := roundTripByRepo(forges)
	tokens := mapGitOnlyTokens{
		sshOnly.StoredOwner() + "/" + sshOnly.Name:   "SSH credential covers git only",
		plainGit.StoredOwner() + "/" + plainGit.Name: "plain git host with no forge API",
	}
	// Wrap the RESTClient the way daemon's StateRepoPullRequestClient + WithCaller do.
	wrappedClient := github.WithCaller(wrappedReleaseClient{Client: github.NewClient(router, tokens)}, "release-pending")
	deps := Deps{Store: store, Client: wrappedClient}
	now := time.Now().UTC()

	if err := reconcileReleasePending(ctx, deps, now); err != nil {
		t.Fatal(err)
	}

	subNoRow := model.NotificationSubject(model.FamilyReleasePending, "v2.5.0")
	nNoRow, err := store.OpenNotification(ctx, subNoRow)
	if err != nil || nNoRow == nil {
		t.Fatalf("no open %s for %s (no repo_config row): %+v (err %v)", subNoRow, noRow, nNoRow, err)
	}
	if nNoRow.Repo != noRow.String() || !strings.Contains(nNoRow.Body, "Unset mode watched.") {
		t.Errorf("notification for %s = %+v", noRow, nNoRow)
	}

	subUnset := model.NotificationSubject(model.FamilyReleasePending, "v1.4.0")
	nUnset, err := store.OpenNotification(ctx, subUnset)
	if err != nil || nUnset == nil || nUnset.Repo != unsetRow.String() {
		t.Fatalf("no open %s for %s (unset ci_mode row): %+v (err %v)", subUnset, unsetRow, nUnset, err)
	}

	for _, skipped := range skippedRepos {
		if forges[skipped].reads != 0 {
			t.Errorf("read %s's tags %d times, want 0", skipped, forges[skipped].reads)
		}
	}
	if open, _ := store.OpenNotification(ctx, model.NotificationSubject(model.FamilyReleasePending, "v9.0.0")); open != nil {
		t.Errorf("unexpected v9.0.0 notification from skipped repo: %+v", open)
	}

	// Tagging v2.5.0 clears the notification on the unset repository's next poll.
	forges[noRow].setTag("v2.5.0", strings.Repeat("a", 40), "Release v2.5.0")
	forges[noRow].deleteTag("v2.5.0-pending")
	if err := reconcileReleasePending(ctx, deps, now.Add(releasePendingPollInterval)); err != nil {
		t.Fatal(err)
	}
	if open, _ := store.OpenNotification(ctx, subNoRow); open != nil {
		t.Errorf("%s still open after v2.5.0 was tagged: %+v", subNoRow, open)
	}
}

type roundTripByRepo map[model.RepoRef]*fixtureForge

func (r roundTripByRepo) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	for repo, f := range r {
		if strings.HasPrefix(path, "/repos/"+repo.String()+"/") {
			if repo.Host != "" {
				f.mu.Lock()
				f.reads++
				f.mu.Unlock()
				return github.ApiResponse{Status: http.StatusOK, Body: []byte(`[]`)}, nil
			}
			return f.Request(method, path, headers, body)
		}
	}
	return github.ApiResponse{Status: http.StatusNotFound, Body: []byte(`{"message":"Not Found"}`)}, nil
}

type mapGitOnlyTokens map[string]string

func (m mapGitOnlyTokens) TokenFor(owner, repo string) *string { return nil }
func (m mapGitOnlyTokens) GitOnly(owner, repo string) string   { return m[owner+"/"+repo] }

// wrappedReleaseClient stands in for daemon.StateRepoPullRequestClient,
// wrapping a github.Client without exposing *github.RESTClient directly.
type wrappedReleaseClient struct {
	github.Client
}

func (w wrappedReleaseClient) WithCaller(caller string) github.Client {
	return wrappedReleaseClient{Client: github.WithCaller(w.Client, caller)}
}

func (w wrappedReleaseClient) ListMatchingTags(owner, repo, prefix string) ([]github.TagRef, error) {
	return w.Client.(github.ReleaseTagReader).ListMatchingTags(owner, repo, prefix)
}

func (w wrappedReleaseClient) GetAnnotatedTag(owner, repo, sha string) (*github.AnnotatedTag, error) {
	return w.Client.(github.ReleaseTagReader).GetAnnotatedTag(owner, repo, sha)
}

func (w wrappedReleaseClient) ReadFile(owner, repo, ref, path string) ([]byte, bool, error) {
	return w.Client.(github.FileReader).ReadFile(owner, repo, ref, path)
}

func (w wrappedReleaseClient) GitOnly(owner, repo string) string {
	return w.Client.(github.GitOnlySource).GitOnly(owner, repo)
}

// Approving a staged release from the CLI or API before the watcher has
// raised release.pending records the decision in the store, keyed by
// (repo, version, commit): the next watcher poll does not raise
// release.pending for that commit, and a second Approve past the signed
// record's 15-minute expiry (or after a restart) is still refused while the
// dispatch is outstanding (docs/test-runs/2026-11-04-release-approval.md, F5).
// Re-staging the same version at a new commit raises and dispatches at once;
// a commit still pending past ReleaseApprovalHold (its promote run failed)
// raises and can be dispatched again.
func TestACLIApprovalBeforeTheWatcherPollsNeitherRaisesNorDispatchesAgain(t *testing.T) {
	resetReleaseWatch(t)
	ctx := context.Background()
	store := openTestStoreForOrchestrator(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg := model.DefaultConfig()
	cfg.TargetRepos = []string{repo.String()}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGitHub}); err != nil {
		t.Fatal(err)
	}

	commit1 := strings.Repeat("1", 40)
	forge := &fixtureForge{repo: repo.String(), tags: map[string]fixtureTag{
		"v2.6.0-pending": {commit: commit1, message: "Release v2.6.0 pending approval\n\nQualification: PASS"},
	}}
	client := github.NewClient(forge, nil)
	deps := Deps{Store: store, Client: client}

	now := time.Now().UTC().Truncate(time.Second).Add(-50 * time.Hour)
	dataDir := t.TempDir()
	person := model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com"}
	uic := ui.NewClient(ui.Config{Actor: person, DataDir: dataDir, GitHubPullRequests: client}, store)
	uic.Now = func() time.Time { return now }

	subject := model.NotificationSubject(model.FamilyReleasePending, "v2.6.0")

	// 1. Approve-then-poll: approve before reconcileReleasePending has ever
	// seen v2.6.0-pending, then run the watcher poll.
	decision, err := uic.DecideRelease(ui.WithPrincipal(ctx, person), repo.String(), "v2.6.0", true,
		ui.ReleaseApproval{AllowBreaking: true})
	if err != nil {
		t.Fatalf("CLI/API approval before watcher poll: %v", err)
	}
	if decision.Dispatched == nil || decision.Dispatched.Commit != commit1 || !decision.AllowBreaking {
		t.Fatalf("decision = %+v, want a dispatch for %s with allow_breaking", decision, commit1)
	}
	if d := forge.dispatched(); len(d) != 1 {
		t.Fatalf("dispatched %d times, want 1", len(d))
	}

	now = now.Add(releasePendingPollInterval)
	if err := reconcileReleasePending(ctx, deps, now); err != nil {
		t.Fatalf("reconcileReleasePending after CLI approval: %v", err)
	}
	if forge.reads == 0 {
		t.Fatal("watcher did not poll repository tags")
	}
	if open, err := store.OpenNotification(ctx, subject); err != nil || open != nil {
		t.Fatalf("watcher raised %s for an already-approved commit: %+v (err %v)", subject, open, err)
	}

	// 2. Approve-after-15-minutes (and across a fresh Client / restart):
	// past releaseapproval.Lifetime while promote-release.yml still has
	// v2.6.0-pending at commit1, neither the watcher raises nor Approve
	// dispatches a second time.
	now = now.Add(releaseapproval.Lifetime) // 25m after the approval (> 15m)
	if err := reconcileReleasePending(ctx, deps, now); err != nil {
		t.Fatalf("reconcileReleasePending after 25m: %v", err)
	}
	if open, err := store.OpenNotification(ctx, subject); err != nil || open != nil {
		t.Fatalf("watcher raised %s 25m after approval: %+v (err %v)", subject, open, err)
	}

	restarted := ui.NewClient(ui.Config{Actor: person, DataDir: dataDir, GitHubPullRequests: client}, store)
	restarted.Now = func() time.Time { return now }
	for name, c := range map[string]*ui.Client{"same client": uic, "restarted client": restarted} {
		_, err := c.DecideRelease(ui.WithPrincipal(ctx, person), repo.String(), "v2.6.0", true,
			ui.ReleaseApproval{AllowBreaking: true})
		var valErr *ui.ValidationError
		if !errors.As(err, &valErr) || !strings.Contains(err.Error(), "v2.6.0 was already answered") {
			t.Errorf("%s after 25m: err = %v, want ValidationError saying v2.6.0 was already answered", name, err)
		}
	}
	if d := forge.dispatched(); len(d) != 1 {
		t.Fatalf("dispatched %d times after >15m retries, want 1", len(d))
	}

	// 3. Re-staging v2.6.0-pending at a new commit raises and allows
	// approving immediately without waiting out ReleaseApprovalHold.
	commit2 := strings.Repeat("2", 40)
	forge.setTag("v2.6.0-pending", commit2, "Release v2.6.0 re-staged\n\nQualification: PASS")
	now = now.Add(releasePendingPollInterval)
	if err := reconcileReleasePending(ctx, deps, now); err != nil {
		t.Fatalf("reconcileReleasePending for re-staged commit: %v", err)
	}
	if open, err := store.OpenNotification(ctx, subject); err != nil || open == nil {
		t.Fatalf("re-staged commit did not raise %s: %+v (err %v)", subject, open, err)
	}
	if _, err := restarted.DecideRelease(ui.WithPrincipal(ctx, person), repo.String(), "v2.6.0", true,
		ui.ReleaseApproval{AllowBreaking: true}); err != nil {
		t.Fatalf("approving re-staged commit: %v", err)
	}
	if open, _ := store.OpenNotification(ctx, subject); open != nil {
		t.Errorf("%s still open after approving re-staged commit", subject)
	}
	if d := forge.dispatched(); len(d) != 2 {
		t.Fatalf("dispatched %d times after re-staged commit, want 2", len(d))
	}

	// 4. If the promote run failed and v2.6.0-pending still points at
	// commit2 past ReleaseApprovalHold, the watcher re-raises and a person
	// may dispatch again.
	now = now.Add(model.ReleaseApprovalHold)
	if err := reconcileReleasePending(ctx, deps, now); err != nil {
		t.Fatalf("reconcileReleasePending after ReleaseApprovalHold: %v", err)
	}
	if open, err := store.OpenNotification(ctx, subject); err != nil || open == nil {
		t.Fatalf("expired hold did not re-raise %s: %+v (err %v)", subject, open, err)
	}
	if _, err := restarted.DecideRelease(ui.WithPrincipal(ctx, person), repo.String(), "v2.6.0", true,
		ui.ReleaseApproval{AllowBreaking: true}); err != nil {
		t.Fatalf("re-approving after ReleaseApprovalHold: %v", err)
	}
	if d := forge.dispatched(); len(d) != 3 {
		t.Fatalf("dispatched %d times after ReleaseApprovalHold retry, want 3", len(d))
	}

	// 5. If the hold expires again and a person dismisses the re-raised
	// notification directly without dispatching, the watcher honours that
	// dismissal rather than re-raising on the next 10-minute poll.
	now = now.Add(model.ReleaseApprovalHold)
	if err := reconcileReleasePending(ctx, deps, now); err != nil {
		t.Fatalf("reconcileReleasePending after second ReleaseApprovalHold: %v", err)
	}
	if open, err := store.OpenNotification(ctx, subject); err != nil || open == nil {
		t.Fatalf("second expired hold did not re-raise %s: %+v (err %v)", subject, open, err)
	}
	if _, err := store.ClearNotification(ctx, subject, model.PrincipalString(person)); err != nil {
		t.Fatalf("ClearNotification (human dismiss): %v", err)
	}
	now = time.Now().UTC().Add(releasePendingPollInterval)
	if err := reconcileReleasePending(ctx, deps, now); err != nil {
		t.Fatalf("reconcileReleasePending after human dismiss: %v", err)
	}
	if open, err := store.OpenNotification(ctx, subject); err != nil || open != nil {
		t.Fatalf("watcher re-raised %s after human dismiss: %+v (err %v)", subject, open, err)
	}
}

// If a person's Approve dispatches promote-release.yml and that workflow run
// fails (for example, GRAIN_RELEASE_APPROVAL_KEY unset on the repository), the
// watcher polls at releasePendingActivePollInterval, re-raises release.pending
// with the failure and GET /api/release-approval-key remediation, and unblocks
// a fresh Approve without waiting out ReleaseApprovalHold.
func TestAFailedPromoteReleaseDispatchReRaisesWithTheFailureAndUnblocksRetry(t *testing.T) {
	resetReleaseWatch(t)
	ctx := context.Background()
	store := openTestStoreForOrchestrator(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg := model.DefaultConfig()
	cfg.TargetRepos = []string{repo.String()}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGitHub}); err != nil {
		t.Fatal(err)
	}

	commit := strings.Repeat("d", 40)
	forge := &fixtureForge{
		repo: repo.String(),
		tags: map[string]fixtureTag{
			"v3.2.0-pending": {commit: commit, message: "Release v3.2.0 pending approval\n\nQualification: PASS"},
		},
		jobsByRun: map[string][]map[string]any{},
		logsByJob: map[string]string{},
	}
	client := github.NewClient(forge, nil)
	deps := Deps{Store: store, Client: client}

	now := time.Now().UTC().Truncate(time.Second)
	dataDir := t.TempDir()
	person := model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com"}
	uic := ui.NewClient(ui.Config{Actor: person, DataDir: dataDir, GitHubPullRequests: client}, store)
	uic.Now = func() time.Time { return now }

	subject := model.NotificationSubject(model.FamilyReleasePending, "v3.2.0")

	// 1. Initial poll raises release.pending:v3.2.0.
	if err := reconcileReleasePending(ctx, deps, now); err != nil {
		t.Fatalf("initial reconcileReleasePending: %v", err)
	}
	if open, _ := store.OpenNotification(ctx, subject); open == nil {
		t.Fatalf("expected %s open before approval", subject)
	}

	// 2. Person approves v3.2.0: dispatches promote-release.yml and clears the open item.
	now = now.Add(2 * time.Second)
	if _, err := uic.DecideRelease(ui.WithPrincipal(ctx, person), repo.String(), "v3.2.0", true,
		ui.ReleaseApproval{AllowBreaking: true}); err != nil {
		t.Fatalf("DecideRelease: %v", err)
	}
	if open, _ := store.OpenNotification(ctx, subject); open != nil {
		t.Fatalf("%s still open immediately after dispatch: %+v", subject, open)
	}

	// 3. promote-release.yml run 42577215514 fails on missing GRAIN_RELEASE_APPROVAL_KEY.
	startedAt := now.Add(time.Second).Format(time.RFC3339)
	updatedAt := now.Add(4 * time.Second).Format(time.RFC3339)
	forge.mu.Lock()
	forge.runs = []map[string]any{{
		"id":             int64(42577215514),
		"name":           "promote-release",
		"path":           ".github/workflows/promote-release.yml",
		"head_branch":    "main",
		"event":          "workflow_dispatch",
		"status":         "completed",
		"conclusion":     "failure",
		"html_url":       "https://github.com/acme/widgets/actions/runs/42577215514",
		"run_started_at": startedAt,
		"updated_at":     updatedAt,
	}}
	forge.jobsByRun["42577215514"] = []map[string]any{{
		"id":         int64(9001),
		"run_id":     int64(42577215514),
		"name":       "approve the pending release",
		"status":     "completed",
		"conclusion": "failure",
	}}
	forge.logsByJob["9001"] = "2026-11-09T07:00:03.0000000Z ##[group]Run Refuse anybody but a person\n" +
		"2026-11-09T07:00:04.0000000Z verify-release-approval: refused: the repository variable GRAIN_RELEASE_APPROVAL_KEY is not set, so no approval from grain's Inbox can be verified: set it to the deployment's key (`grain release approval-key`)\n" +
		"2026-11-09T07:00:04.1000000Z ##[error]Process completed with exit code 1.\n"
	forge.mu.Unlock()

	// 4. Within releasePendingActivePollInterval (not 10m / 24h), the item is
	// re-raised with the failed run id, refusal message, and GET /api/release-approval-key.
	now = now.Add(releasePendingActivePollInterval)
	if err := reconcileReleasePending(ctx, deps, now); err != nil {
		t.Fatalf("reconcileReleasePending after failed dispatch: %v", err)
	}
	open, err := store.OpenNotification(ctx, subject)
	if err != nil || open == nil {
		t.Fatalf("failed dispatch did not re-raise %s: %+v (err %v)", subject, open, err)
	}
	if open.Severity != model.NotificationCritical {
		t.Errorf("severity = %q, want %q", open.Severity, model.NotificationCritical)
	}
	for _, want := range []string{
		"DISPATCH FAILED (gha:acme:widgets:42577215514):",
		"verify-release-approval: refused: the repository variable GRAIN_RELEASE_APPROVAL_KEY is not set",
		"GET /api/release-approval-key",
		"https://github.com/acme/widgets/actions/runs/42577215514",
	} {
		if !strings.Contains(open.Body, want) {
			t.Errorf("re-raised body missing %q:\n%s", want, open.Body)
		}
	}

	// 5. After setting the variable, the person can immediately approve again
	// without waiting out ReleaseApprovalHold.
	now = now.Add(10 * time.Second)
	if _, err := uic.DecideRelease(ui.WithPrincipal(ctx, person), repo.String(), "v3.2.0", true,
		ui.ReleaseApproval{AllowBreaking: true}); err != nil {
		t.Fatalf("re-approving after failed dispatch: %v", err)
	}
	if d := forge.dispatched(); len(d) != 2 {
		t.Fatalf("dispatched %d times after retry, want 2", len(d))
	}
}
