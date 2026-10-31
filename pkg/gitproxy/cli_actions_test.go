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

package gitproxy_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/namespace"
)

// fakeCLIActions is a daemon with one task's CI and pull request.
type fakeCLIActions struct {
	mu       sync.Mutex
	runs     map[string]ci.Run
	finishAt int // CIRun reads before the started run concludes; <0 never
	reads    int
	started  []gitproxy.CIStartRequest
	replies  []string
	refuse   bool
	// downStarts and downReads are how many StartTaskCI and CIRun calls
	// are answered as a daemon whose CI is not up yet; lastDown is
	// whether the latest CIRun was, which is what Ready reports.
	downStarts, downReads int
	lastDown              bool
	githubChecks          bool
	githubWaitResult      mcp.GitHubCheckWaitResult
	githubWaits           int
}

func (f *fakeCLIActions) UsesGitHubChecks(_ context.Context, _ string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.githubChecks, nil
}

func (f *fakeCLIActions) WaitForGitHubChecks(_ context.Context, _ string, _, _ time.Duration, onPoll func(waited time.Duration)) (mcp.GitHubCheckWaitResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.githubWaits++
	if onPoll != nil {
		onPoll(10 * time.Millisecond)
	}
	return f.githubWaitResult, nil
}

func (f *fakeCLIActions) Ready() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.lastDown
}

func (f *fakeCLIActions) StartTaskCI(_ context.Context, taskID string, req gitproxy.CIStartRequest) (ci.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.downStarts > 0 {
		f.downStarts--
		return ci.Run{}, ci.ErrNotUp
	}
	f.started = append(f.started, req)
	run := ci.Run{ID: "run-new", Repo: "bwsalmon/grain", Ref: "grain/task-7", Commit: "abcdef0123456789",
		Jobs: []ci.JobRun{{Name: "go"}, {Name: "lint", Conclusion: ci.ConclusionSuccess}}}
	f.runs[run.ID] = run
	return run, nil
}

func (f *fakeCLIActions) CIRun(_ context.Context, id string) (ci.Run, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastDown = f.downReads > 0
	if f.lastDown {
		f.downReads--
		return ci.Run{}, false
	}
	run, ok := f.runs[id]
	if !ok {
		return ci.Run{}, false
	}
	if id == "run-new" {
		f.reads++
		if f.finishAt >= 0 && f.reads >= f.finishAt {
			run.Conclusion = ci.ConclusionSuccess
			run.Jobs = []ci.JobRun{{Name: "go", Conclusion: ci.ConclusionSuccess}, {Name: "lint", Conclusion: ci.ConclusionSuccess}}
		}
	}
	return run, true
}

func (f *fakeCLIActions) OpenPullRequest(context.Context, string) (gitproxy.CLIActionResult, error) {
	return gitproxy.CLIActionResult{Text: "Pull request #12 in bwsalmon/grain"}, nil
}

func (f *fakeCLIActions) ReplyToPullRequestComment(_ context.Context, taskID string, commentID int, body string) (gitproxy.CLIActionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies = append(f.replies, taskID+":"+body)
	if f.refuse {
		return gitproxy.CLIActionResult{Text: "Comment 5 is not an inline review comment", Refused: true}, nil
	}
	return gitproxy.CLIActionResult{Text: "Replied."}, nil
}

func (f *fakeCLIActions) ResolveReviewThread(context.Context, string, string, int) (gitproxy.CLIActionResult, error) {
	return gitproxy.CLIActionResult{Text: "Resolved."}, nil
}

func (f *fakeCLIActions) RevertInfo(_ context.Context, taskID, target string, anyTask bool) (gitproxy.RevertInfo, error) {
	if target != "42" {
		return gitproxy.RevertInfo{}, namespace.ErrNotFound
	}
	return gitproxy.RevertInfo{Task: "42", Branch: "grain/task-42"}, nil
}

// fakeCreditor records each cumulative report.
type fakeCreditor struct {
	mu      sync.Mutex
	reports []time.Duration
}

func (f *fakeCreditor) CreditCLIWait(_ context.Context, taskID string, blocked time.Duration) (gitproxy.CLIWaitCredit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, blocked)
	return gitproxy.CLIWaitCredit{Blocked: blocked, Credited: blocked, Cap: 45 * time.Minute, Live: true}, nil
}

// fullActivity is fakeActivity that also keeps check waits and call
// arguments, as *model.Store does.
type fullActivity struct {
	fakeActivity
	waits    []string
	calls    []map[string]any
	apiCalls []string
}

func (f *fullActivity) RecordProxyCheckWait(_ context.Context, runID, verdict string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waits = append(f.waits, runID+":"+verdict)
	return nil
}

func (f *fullActivity) RecordProxyToolCallArgs(_ context.Context, runID, tool string, args map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, args)
	return nil
}

func (f *fullActivity) RecordProxyAPICall(_ context.Context, runID, tool, targetIDs string, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apiCalls = append(f.apiCalls, runID+":"+tool+":"+targetIDs)
	return nil
}

func newActionProxy(t *testing.T, actions *fakeCLIActions) (http.Handler, *fullActivity, *fakeCreditor) {
	t.Helper()
	gitproxy.LogToTest(t)
	tokenFile := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	raw, _ := json.Marshal(map[string]string{"sb-ci": "tok-ci", "sb-plain": "tok-plain"})
	if err := os.WriteFile(tokenFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, err := gitproxy.LoadSandboxTokens(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	act := &fullActivity{}
	credit := &fakeCreditor{}
	proxy := &gitproxy.GitProxy{
		Tokens:    tokens,
		Namespace: &namespace.Server{CIStore: ci.NewRunStore(10)},
		NamespaceScopes: fakeNamespaceScopes{scopes: map[string]model.NamespaceScope{
			"sb-ci": {TaskID: "7", RunID: "run-a", TaskRepo: "bwsalmon/grain", TaskBranch: "grain/task-7", TaskBase: "main",
				MayReadCI: true, AllowedRepos: map[string]bool{"bwsalmon/grain": true}},
			"sb-plain": {TaskID: "8", RunID: "run-b", TaskRepo: "bwsalmon/grain", TaskBranch: "grain/task-8"},
		}},
		Activity:   act,
		CLIActions: actions,
		WaitCredit: credit,
	}
	return gitproxy.NewHandler(proxy), act, credit
}

func send(h http.Handler, method, token, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", basicAuthHeader(token))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCLIWaitBlocksUntilVerdictCreditsAndRecords(t *testing.T) {
	defer gitproxy.SetCLIWaitIntervals(5*time.Millisecond, 10*time.Millisecond)()
	actions := &fakeCLIActions{runs: map[string]ci.Run{}, finishAt: 6}
	h, act, credit := newActionProxy(t, actions)

	rec := send(h, http.MethodPost, "tok-ci", "/cli/ci/wait", `{"jobs":["go, lint"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("wait: %d %s", rec.Code, rec.Body)
	}
	var resp gitproxy.CLIWaitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "passed" || resp.RunID != "run-new" || !resp.Selective || resp.ResumeRunID != "" || !resp.CreditLive {
		t.Errorf("answer = %+v", resp)
	}
	if got := actions.started[0].Jobs; strings.Join(got, ",") != "go,lint" {
		t.Errorf("started jobs = %q", got)
	}

	credit.mu.Lock()
	reports := append([]time.Duration(nil), credit.reports...)
	credit.mu.Unlock()
	if len(reports) == 0 {
		t.Fatal("no wait credit reported")
	}
	for i := 1; i < len(reports); i++ {
		if reports[i] < reports[i-1] {
			t.Errorf("credit reports are not cumulative: %v", reports)
		}
	}

	act.mu.Lock()
	defer act.mu.Unlock()
	phrase := mcp.ToolActivityNote("wait_for_checks")
	if !mcp.IsDeclaredWait(phrase) {
		t.Fatalf("%q is not a declared wait", phrase)
	}
	stamped := 0
	for _, n := range act.notes {
		if n == "7:"+phrase {
			stamped++
		}
	}
	if stamped < 1 {
		t.Errorf("activity notes = %q, want the declared-wait phrase", act.notes)
	}
	if strings.Join(act.waits, " ") != "run-a:"+mcp.WaitVerdictPassed {
		t.Errorf("check waits = %q", act.waits)
	}
	if len(act.toolRows) != 1 || act.toolRows[0].tool != "wait_for_checks" {
		t.Errorf("tool rows = %+v", act.toolRows)
	}
}

// grain/task-1863: a grain ci wait started, or resumed, while the
// daemon's CI service is not up yet -- the first minute or so after a
// restart -- sits that out inside its slice, rather than being refused
// (start) or told its run has been forgotten (resume).
func TestCLIWaitSitsOutCINotBeingUpYet(t *testing.T) {
	defer gitproxy.SetCLIWaitIntervals(5*time.Millisecond, 10*time.Millisecond)()
	actions := &fakeCLIActions{runs: map[string]ci.Run{}, finishAt: 3, downStarts: 3}
	h, _, _ := newActionProxy(t, actions)

	rec := send(h, http.MethodPost, "tok-ci", "/cli/ci/wait", `{"timeout_seconds":30}`)
	var resp gitproxy.CLIWaitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("wait: %d %s", rec.Code, rec.Body)
	}
	if resp.Status != "passed" || len(actions.started) != 1 {
		t.Errorf("answer = %+v (started %d), want the run started once CI was up, passed", resp, len(actions.started))
	}

	actions.mu.Lock()
	actions.downReads, actions.reads = 3, 0
	actions.mu.Unlock()
	rec = send(h, http.MethodPost, "tok-ci", "/cli/ci/wait", `{"resume_run_id":"run-new","timeout_seconds":30}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("resume while CI is not up: %d %s", rec.Code, rec.Body)
	}
}

// One that is still not up when its slice ends is a 503 saying so --
// never the 404 "forgotten" that ended waits across a restart.
func TestCLIWaitIsUnavailableNotForgottenWhileCIStaysDown(t *testing.T) {
	defer gitproxy.SetCLIWaitIntervals(5*time.Millisecond, 10*time.Millisecond)()
	actions := &fakeCLIActions{runs: map[string]ci.Run{}, downStarts: 1 << 30, downReads: 1 << 30}
	h, _, _ := newActionProxy(t, actions)

	for _, body := range []string{`{"timeout_seconds":1}`, `{"resume_run_id":"run-new","timeout_seconds":1}`} {
		rec := send(h, http.MethodPost, "tok-ci", "/cli/ci/wait", body)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "not up yet") {
			t.Errorf("wait %s: %d %s, want 503 saying CI is not up yet", body, rec.Code, rec.Body)
		}
	}
}

func TestCLIWaitSliceEndsRunningAndResumesOnlyOwnBranch(t *testing.T) {
	defer gitproxy.SetCLIWaitIntervals(5*time.Millisecond, 10*time.Millisecond)()
	actions := &fakeCLIActions{runs: map[string]ci.Run{
		"run-other": {ID: "run-other", Repo: "bwsalmon/grain", Ref: "grain/task-99"},
	}, finishAt: -1}
	h, act, _ := newActionProxy(t, actions)

	rec := send(h, http.MethodPost, "tok-ci", "/cli/ci/run-job", `{"job":"go","timeout_seconds":1}`)
	var resp gitproxy.CLIWaitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("run-job: %d %s", rec.Code, rec.Body)
	}
	if resp.Status != "running" || resp.ResumeRunID != "run-new" {
		t.Errorf("answer = %+v, want running with a run to resume", resp)
	}
	if got := actions.started[0].Jobs; len(got) != 1 || got[0] != "go" {
		t.Errorf("run-job started %q", got)
	}

	// Resuming picks the same run up without starting another.
	rec = send(h, http.MethodPost, "tok-ci", "/cli/ci/wait", `{"resume_run_id":"run-new","timeout_seconds":1}`)
	if rec.Code != http.StatusOK || len(actions.started) != 1 {
		t.Errorf("resume: %d %s (started %d runs)", rec.Code, rec.Body, len(actions.started))
	}
	// A run on another task's branch is not this sandbox's to wait on.
	rec = send(h, http.MethodPost, "tok-ci", "/cli/ci/wait", `{"resume_run_id":"run-other"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("resume of another branch's run: %d %s", rec.Code, rec.Body)
	}

	act.mu.Lock()
	defer act.mu.Unlock()
	if len(act.waits) != 2 || act.waits[0] != "run-a:"+mcp.WaitVerdictTimedOut {
		t.Errorf("check waits = %q", act.waits)
	}
}

func TestCLIActionsRefuseAndRoute(t *testing.T) {
	actions := &fakeCLIActions{runs: map[string]ci.Run{}}
	h, act, _ := newActionProxy(t, actions)

	if rec := send(h, http.MethodPost, "tok-plain", "/cli/ci/wait", `{}`); rec.Code != http.StatusForbidden ||
		!strings.Contains(rec.Body.String(), `"grain-ci"`) {
		t.Errorf("wait without grain-ci: %d %s", rec.Code, rec.Body)
	}
	if rec := send(h, http.MethodGet, "tok-ci", "/cli/ci/wait", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /cli/ci/wait: %d", rec.Code)
	}
	if rec := send(h, http.MethodPost, "tok-ci", "/cli/ci/run-job", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("run-job without a job: %d", rec.Code)
	}
	rec := send(h, http.MethodPost, "tok-ci", "/cli/ci/retrigger", `{"jobs":["go"]}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "grain ci wait --resume run-new") {
		t.Errorf("retrigger: %d %s", rec.Code, rec.Body)
	}
	if got := actions.started[0]; !got.Recheck || got.FailedOnly {
		t.Errorf("retrigger of named jobs = %+v", got)
	}
	send(h, http.MethodPost, "tok-ci", "/cli/ci/retrigger", ``)
	if got := actions.started[1]; !got.Recheck || !got.FailedOnly {
		t.Errorf("bare retrigger = %+v, want failed jobs only", got)
	}
	if rec := send(h, http.MethodPost, "tok-ci", "/cli/ci/retrigger", `{"commit":"--upload-pack=x"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("retrigger of a non-SHA commit: %d", rec.Code)
	}

	if rec := send(h, http.MethodPost, "tok-ci", "/cli/pr/open", ``); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "#12") {
		t.Errorf("pr open: %d %s", rec.Code, rec.Body)
	}
	if rec := send(h, http.MethodPost, "tok-plain", "/cli/pr/reply", `{"body":"thanks","comment_id":5}`); rec.Code != http.StatusOK {
		t.Errorf("pr reply: %d %s", rec.Code, rec.Body)
	}
	actions.refuse = true
	if rec := send(h, http.MethodPost, "tok-ci", "/cli/pr/reply", `{"body":"thanks","comment_id":5}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("refused reply: %d %s", rec.Code, rec.Body)
	}
	if strings.Join(actions.replies, " ") != "8:thanks 7:thanks" {
		t.Errorf("replies went to %q, want each sandbox's own task", actions.replies)
	}
	if rec := send(h, http.MethodPost, "tok-ci", "/cli/pr/resolve", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("resolve naming nothing: %d", rec.Code)
	}

	if rec := send(h, http.MethodPost, "tok-ci", "/cli/pr/review-comment", `{"body":"x","path":"a.go"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("review comment with a path and no line: %d", rec.Code)
	}
	if rec := send(h, http.MethodPost, "tok-ci", "/cli/pr/review-comment", `{"body":"nil deref","path":"a.go","line":3}`); rec.Code != http.StatusOK {
		t.Errorf("review comment: %d %s", rec.Code, rec.Body)
	}

	rec = send(h, http.MethodGet, "tok-ci", "/cli/tasks/42/revert-info", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"branch":"grain/task-42"`) {
		t.Errorf("revert-info: %d %s", rec.Code, rec.Body)
	}
	if rec := send(h, http.MethodGet, "tok-ci", "/cli/tasks/43/revert-info", ""); rec.Code != http.StatusNotFound {
		t.Errorf("revert-info of an unknown task: %d", rec.Code)
	}

	act.mu.Lock()
	defer act.mu.Unlock()
	if len(act.calls) != 1 || act.calls[0]["path"] != "a.go" || act.calls[0]["line"] != 3 {
		t.Errorf("review calls = %+v", act.calls)
	}
	counted := map[string]int{}
	for _, row := range act.toolRows {
		counted[row.tool]++
	}
	for _, tool := range []string{"retrigger_ci", "open_pull_request", "reply_to_pull_request_comment",
		"resolve_review_thread", "add_review_comment", "revert_task"} {
		if counted[tool] == 0 {
			t.Errorf("no task_run_tool row for %s: %v", tool, counted)
		}
	}
}

func TestCLIActionsUnwiredAnswer501(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	raw, _ := json.Marshal(map[string]string{"sb-ci": "tok-ci"})
	if err := os.WriteFile(tokenFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, _ := gitproxy.LoadSandboxTokens(tokenFile)
	proxy := &gitproxy.GitProxy{Tokens: tokens, NamespaceScopes: fakeNamespaceScopes{scopes: map[string]model.NamespaceScope{
		"sb-ci": {TaskID: "7", RunID: "run-a", TaskRepo: "bwsalmon/grain", TaskBranch: "grain/task-7", MayReadCI: true},
	}}}
	if rec := send(gitproxy.NewHandler(proxy), http.MethodPost, "tok-ci", "/cli/pr/open", ""); rec.Code != http.StatusNotImplemented {
		t.Errorf("unwired pr open: %d %s", rec.Code, rec.Body)
	}
}

func (f *fakeCLIActions) NotifyOperator(_ context.Context, taskID string, req gitproxy.CLINotifyRequest) (gitproxy.CLINotifyResult, error) {
	return gitproxy.CLINotifyResult{ID: 1, Subject: req.Subject, Severity: "warning", Count: 1, New: true}, nil
}

func (f *fakeCLIActions) MessageTask(_ context.Context, taskID, targetTaskID, body string) (int64, error) {
	return 99, nil
}

func (f *fakeCLIActions) CheckWaitTargets(_ context.Context, taskID string, targetIDs []string) ([]model.WaitTarget, error) {
	return []model.WaitTarget{{ID: targetIDs[0], Closed: true, Merged: true}}, nil
}

func (f *fakeCLIActions) ProposeTask(_ context.Context, taskID, runID string, req gitproxy.CLIProposeRequest) (gitproxy.CLIProposeResult, error) {
	return gitproxy.CLIProposeResult{
		ID:              "100",
		URL:             "/tasks/100",
		HoldForApproval: true,
		AutoMerge:       false,
		Held:            true,
	}, nil
}

func (f *fakeCLIActions) ListAPIRoutes(_ context.Context, taskID string, grants []string, scopeFilter string) ([]gitproxy.CLIAPIRoute, error) {
	return []gitproxy.CLIAPIRoute{{Method: "GET", Path: "/api/tasks", Scope: "tasks:read", Tool: "api_list_tasks"}}, nil
}

func TestPerAttemptTokenBindingAndStage3CLIActions(t *testing.T) {
	gitproxy.LogToTest(t)
	tokenFile := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	store := gitproxy.NewSandboxTokenStore(tokenFile)
	tok1, err := store.EnsureGrant("sb-1", "7", 1)
	if err != nil || tok1 == "" {
		t.Fatalf("EnsureGrant(sb-1, 7, 1): %q %v", tok1, err)
	}
	// Same task and attempt reuses token.
	if same, err := store.EnsureGrant("sb-1", "7", 1); err != nil || same != tok1 {
		t.Fatalf("EnsureGrant same attempt = %q, %v; want %q", same, err, tok1)
	}
	// New attempt rotates token.
	tok2, err := store.EnsureGrant("sb-1", "7", 2)
	if err != nil || tok2 == "" || tok2 == tok1 {
		t.Fatalf("EnsureGrant new attempt = %q, %v; want rotated token != %q", tok2, err, tok1)
	}

	tokens, err := gitproxy.LoadSandboxTokens(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	act := &fullActivity{}
	credit := &fakeCreditor{}
	actions := &fakeCLIActions{}
	scopes := fakeNamespaceScopes{scopes: map[string]model.NamespaceScope{
		"sb-1": {
			TaskID: "7", RunID: "run-2", Attempt: 2,
			TaskRepo: "bwsalmon/grain", TaskBranch: "grain/task-7", TaskBase: "main",
			Grants: []string{"api:test-quarantine", "api:tasks:read"},
		},
	}}
	var apiHeaderChannel, apiHeaderTask, apiHeaderRun, apiHeaderReason, apiHeaderAuth, apiHeaderTunnel string
	proxy := &gitproxy.GitProxy{
		Tokens:          tokens,
		Namespace:       &namespace.Server{CIStore: ci.NewRunStore(10)},
		NamespaceScopes: scopes,
		CLIActions:      actions,
		Activity:        act,
		WaitCredit:      credit,
		API: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiHeaderChannel = r.Header.Get("X-Grain-Channel")
			apiHeaderTask = r.Header.Get("X-Grain-Acting-Task")
			apiHeaderRun = r.Header.Get("X-Grain-Acting-Run")
			apiHeaderReason = r.Header.Get("X-Grain-Acting-Reason")
			apiHeaderAuth = r.Header.Get("Authorization")
			apiHeaderTunnel = r.Header.Get("X-Grain-Tunnel-Token")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}),
	}
	h := gitproxy.NewHandler(proxy)

	// Old attempt token is rejected.
	if rec := send(h, http.MethodPost, tok1, "/cli/status", `{"status":"working"}`); rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Errorf("revoked attempt 1 token: got %d, want 401/403", rec.Code)
	}

	// If the sandbox's active attempt advances to 3 while token is bound to attempt 2, proxy rejects with 403.
	scopes.scopes["sb-1"] = model.NamespaceScope{TaskID: "7", RunID: "run-3", Attempt: 3}
	if rec := send(h, http.MethodPost, tok2, "/cli/status", `{"status":"working"}`); rec.Code != http.StatusForbidden ||
		!strings.Contains(rec.Body.String(), "belongs to attempt 2") {
		t.Errorf("mismatched attempt token: got %d %s, want 403 belongs to attempt 2", rec.Code, rec.Body.String())
	}
	scopes.scopes["sb-1"] = model.NamespaceScope{
		TaskID: "7", RunID: "run-2", Attempt: 2,
		TaskRepo: "bwsalmon/grain", TaskBranch: "grain/task-7", TaskBase: "main",
		Grants: []string{"api:test-quarantine", "api:tasks:read"},
	}

	// Stage 3 collaboration & blocking-wait endpoints:
	if rec := send(h, http.MethodPost, tok2, "/cli/status", `{"status":"investigating"}`); rec.Code != http.StatusOK {
		t.Errorf("POST /cli/status: %d %s", rec.Code, rec.Body)
	}
	if rec := send(h, http.MethodPost, tok2, "/cli/handover", `{"handover":"settled decisions"}`); rec.Code != http.StatusOK {
		t.Errorf("POST /cli/handover: %d %s", rec.Code, rec.Body)
	}
	if rec := send(h, http.MethodPost, tok2, "/cli/notify", `{"subject":"flake","title":"Flaky test"}`); rec.Code != http.StatusOK {
		t.Errorf("POST /cli/notify: %d %s", rec.Code, rec.Body)
	}
	if rec := send(h, http.MethodPost, tok2, "/cli/tasks/8/notes", `{"body":"hello child task"}`); rec.Code != http.StatusOK {
		t.Errorf("POST /cli/tasks/8/notes: %d %s", rec.Code, rec.Body)
	}
	if rec := send(h, http.MethodPost, tok2, "/cli/tasks/wait", `{"ids":["8"],"until":"closed"}`); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"settled"`) {
		t.Errorf("POST /cli/tasks/wait: %d %s", rec.Code, rec.Body)
	}
	// Parking via /cli/tasks/wait is refused.
	if rec := send(h, http.MethodPost, tok2, "/cli/tasks/wait", `{"ids":["8"],"park":true}`); rec.Code != http.StatusForbidden {
		t.Errorf("POST /cli/tasks/wait park=true: %d %s, want 403", rec.Code, rec.Body)
	}

	// Proposals enforce hold_for_approval=true, auto_merge=false, no supersedes, and cap at 10.
	if rec := send(h, http.MethodPost, tok2, "/cli/proposals", `{"title":"t","body":"b","supersedes":"5"}`); rec.Code != http.StatusForbidden {
		t.Errorf("POST /cli/proposals with supersedes: %d %s, want 403", rec.Code, rec.Body)
	}
	if rec := send(h, http.MethodPost, tok2, "/cli/proposals", `{"title":"t","body":"b","auto_merge":true}`); rec.Code != http.StatusForbidden {
		t.Errorf("POST /cli/proposals with auto_merge=true: %d %s, want 403", rec.Code, rec.Body)
	}
	for i := 0; i < gitproxy.MaxCLIProposalsPerAttempt; i++ {
		if rec := send(h, http.MethodPost, tok2, "/cli/proposals", `{"title":"t","body":"b"}`); rec.Code != http.StatusOK {
			t.Fatalf("proposal %d: %d %s", i+1, rec.Code, rec.Body)
		}
	}
	if rec := send(h, http.MethodPost, tok2, "/cli/proposals", `{"title":"t","body":"b"}`); rec.Code != http.StatusTooManyRequests {
		t.Errorf("proposal over cap: %d %s, want 429", rec.Code, rec.Body)
	}
	// Quarantine follow-up proposals share the same per-attempt proposal cap.
	if rec := send(h, http.MethodPost, tok2, "/cli/quarantine", `{"test_name":"pkg/foo.TestBar","reason":"flake"}`); rec.Code != http.StatusTooManyRequests {
		t.Errorf("quarantine follow-up proposal over cap: %d %s, want 429", rec.Code, rec.Body)
	}
	// Without tests:write (or on a mismatched repo-scoped grant), /cli/quarantine is refused with 403.
	scopes.scopes["sb-1"] = model.NamespaceScope{
		TaskID: "7", RunID: "run-2", Attempt: 2,
		TaskRepo: "bwsalmon/grain", TaskBranch: "grain/task-7", TaskBase: "main",
		Grants: []string{"api:tests:write@other/repo"},
	}
	if rec := send(h, http.MethodPost, tok2, "/cli/quarantine", `{"test_name":"pkg/foo.TestBar","reason":"flake"}`); rec.Code != http.StatusForbidden ||
		!strings.Contains(rec.Body.String(), "api:test-quarantine") {
		t.Errorf("quarantine without grant on repo: got %d %s, want 403 naming api:test-quarantine", rec.Code, rec.Body)
	}
	scopes.scopes["sb-1"] = model.NamespaceScope{
		TaskID: "7", RunID: "run-2", Attempt: 2,
		TaskRepo: "bwsalmon/grain", TaskBranch: "grain/task-7", TaskBase: "main",
		Grants: []string{"api:test-quarantine", "api:tasks:read"},
	}

	// /api/ forwarding attaches X-Grain-Channel: sandbox-cli, X-Grain-Acting-Task, and X-Grain-Acting-Run,
	// and forwards X-Grain-Reason as X-Grain-Acting-Reason.
	req := httptest.NewRequest(http.MethodDelete, "/api/tasks/9/comments/1", nil)
	req.Header.Set("Authorization", basicAuthHeader(tok2))
	req.Header.Set("X-Grain-Reason", "obsolete note")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK ||
		apiHeaderChannel != "sandbox-cli" || apiHeaderTask != "7" || apiHeaderRun != "run-2" || apiHeaderReason != "obsolete note" {
		t.Errorf("DELETE /api/tasks/9/comments/1: code=%d channel=%q task=%q run=%q reason=%q",
			rec.Code, apiHeaderChannel, apiHeaderTask, apiHeaderRun, apiHeaderReason)
	}

	// The acting task, run and channel are the proxy's to say, never the
	// sandbox's: a sandbox naming another task, dropping the channel or
	// passing its own credential on is overwritten, not believed.
	req = httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	req.Header.Set("Authorization", basicAuthHeader(tok2))
	req.Header.Set("X-Grain-Acting-Task", "1")
	req.Header.Set("X-Grain-Acting-Run", "run-of-task-1")
	req.Header.Set("X-Grain-Channel", "model")
	req.Header.Set("X-Grain-Tunnel-Token", "stolen")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK ||
		apiHeaderChannel != "sandbox-cli" || apiHeaderTask != "7" || apiHeaderRun != "run-2" ||
		apiHeaderAuth != "" || apiHeaderTunnel != "" || apiHeaderReason != "" {
		t.Errorf("GET /api/tasks with spoofed headers: code=%d channel=%q task=%q run=%q auth=%q tunnel=%q reason=%q, "+
			"want the proxy's own task 7, run-2, sandbox-cli and no credential",
			rec.Code, apiHeaderChannel, apiHeaderTask, apiHeaderRun, apiHeaderAuth, apiHeaderTunnel, apiHeaderReason)
	}

	// An external capability server is never handed the sandbox's proxy
	// token or a sandbox-supplied X-Grain-* header.
	var capAuth, capActing string
	capReached := false
	caps := gitproxy.NewCapRegistry()
	proxy.CapServers = caps
	unregister := caps.Register(gitproxy.CapRunKey("run-2"), []gitproxy.CapServer{{
		Name: "widgets", Grant: "https://example.com/widgets#w",
		Tools: []gitproxy.CapTool{{Name: "ping"}, {Name: "secret", ModelOnly: true}},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capReached = true
			capAuth = r.Header.Get("Authorization")
			capActing = r.Header.Get("X-Grain-Acting-Task")
			w.WriteHeader(http.StatusOK)
		}),
	}})
	req = httptest.NewRequest(http.MethodPost, "/cap/widgets/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ping"}}`))
	req.Header.Set("Authorization", basicAuthHeader(tok2))
	req.Header.Set("X-Grain-Acting-Task", "1")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !capReached || capAuth != "" || capActing != "" {
		t.Errorf("POST /cap/widgets/mcp: reached=%v auth=%q acting=%q; want the capability server reached with neither",
			capReached, capAuth, capActing)
	}
	// A model-only tool -- alone or inside a batch -- and a tool the
	// server never listed are refused before the server sees them.
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"secret"}}`,
		`[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ping"}},{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"secret"}}]`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"unlisted"}}`,
	} {
		capReached = false
		req = httptest.NewRequest(http.MethodPost, "/cap/widgets/mcp", strings.NewReader(body))
		req.Header.Set("Authorization", basicAuthHeader(tok2))
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || capReached {
			t.Errorf("POST /cap/widgets/mcp %s: code=%d reached=%v; want 403 and the server untouched", body, rec.Code, capReached)
		}
	}
	req = httptest.NewRequest(http.MethodGet, "/cap/", nil)
	req.Header.Set("Authorization", basicAuthHeader(tok2))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"ping"`) ||
		!strings.Contains(rec.Body.String(), `"modelOnlyTools":["secret"]`) || strings.Contains(rec.Body.String(), `"name":"secret"`) {
		t.Errorf("GET /cap/: %d %s; want widgets listing ping as a tool and secret as model-only", rec.Code, rec.Body)
	}
	req = httptest.NewRequest(http.MethodPost, "/cap/gadgets/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", basicAuthHeader(tok2))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("POST /cap/gadgets/mcp, never granted: %d, want 403", rec.Code)
	}
	unregister()
	proxy.CapServers = nil

	// A live run with no task to act for is never forwarded: without
	// X-Grain-Acting-Task the API would answer as the deployment itself.
	scopes.scopes["sb-1"] = model.NamespaceScope{RunID: "run-2", Attempt: 2}
	apiHeaderTask = "unset"
	for _, path := range []string{"/api/tasks", "/cap/memory"} {
		req = httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", basicAuthHeader(tok2))
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || apiHeaderTask != "unset" {
			t.Errorf("GET %s with no live task: code=%d, forwarded=%v; want 403 and nothing forwarded",
				path, rec.Code, apiHeaderTask != "unset")
		}
	}

	act.mu.Lock()
	apiCallCount := len(act.apiCalls)
	act.mu.Unlock()
	if apiCallCount == 0 {
		t.Error("expected RecordProxyAPICall to record served proxy calls")
	}
}

func TestCLIWaitEndsRunningWhenDaemonShutdownStarts(t *testing.T) {
	defer gitproxy.SetCLIWaitIntervals(5*time.Millisecond, 10*time.Millisecond)()
	tokenFile := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	raw, _ := json.Marshal(map[string]string{"sb-ci": "tok-ci"})
	if err := os.WriteFile(tokenFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, err := gitproxy.LoadSandboxTokens(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	shutdown := make(chan struct{})
	actions := &fakeCLIActions{runs: map[string]ci.Run{}, finishAt: -1}
	proxy := &gitproxy.GitProxy{
		Tokens:    tokens,
		Namespace: &namespace.Server{CIStore: ci.NewRunStore(10)},
		NamespaceScopes: fakeNamespaceScopes{scopes: map[string]model.NamespaceScope{
			"sb-ci": {TaskID: "7", RunID: "run-a", TaskRepo: "bwsalmon/grain", TaskBranch: "grain/task-7", TaskBase: "main",
				MayReadCI: true, AllowedRepos: map[string]bool{"bwsalmon/grain": true}},
		}},
		CLIActions:      actions,
		ShutdownStarted: shutdown,
	}
	h := gitproxy.NewHandler(proxy)

	go func() {
		time.Sleep(25 * time.Millisecond)
		close(shutdown)
	}()

	start := time.Now()
	rec := send(h, http.MethodPost, "tok-ci", "/cli/ci/wait", `{"timeout_seconds":60}`)
	elapsed := time.Since(start)
	if rec.Code != http.StatusOK {
		t.Fatalf("wait: %d %s", rec.Code, rec.Body)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("wait took %s after ShutdownStarted closed, want immediate return", elapsed)
	}
	var resp gitproxy.CLIWaitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "running" || resp.ResumeRunID != "run-new" {
		t.Errorf("answer = %+v, want status=running with resume_run_id=run-new", resp)
	}
}

// TestAPIForwardKeepsAnEscapedSlash is a host-qualified repository's
// route reached through the sandbox proxy: /api/repos/<host>%2F<owner>/
// <name>/... reaches the agent API with the %2F still one {owner}
// segment, not three segments no route matches.
func TestAPIForwardKeepsAnEscapedSlash(t *testing.T) {
	gitproxy.LogToTest(t)
	tokenFile := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	raw, _ := json.Marshal(map[string]string{"sb-1": "tok-1"})
	if err := os.WriteFile(tokenFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, err := gitproxy.LoadSandboxTokens(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	var owner, name string
	api := http.NewServeMux()
	api.HandleFunc("GET /api/repos/{owner}/{name}/x", func(w http.ResponseWriter, r *http.Request) {
		owner, name = r.PathValue("owner"), r.PathValue("name")
	})
	h := gitproxy.NewHandler(&gitproxy.GitProxy{
		Tokens:    tokens,
		Namespace: &namespace.Server{CIStore: ci.NewRunStore(10)},
		NamespaceScopes: fakeNamespaceScopes{scopes: map[string]model.NamespaceScope{
			"sb-1": {TaskID: "7", RunID: "run-a", TaskRepo: "h.example/o/n", TaskBranch: "grain/task-7"},
		}},
		API: api,
	})
	rec := send(h, http.MethodGet, "tok-1", "/api/repos/h.example%2Fo/n/x", "")
	if rec.Code != http.StatusOK || owner != "h.example/o" || name != "n" {
		t.Errorf("GET /api/repos/h.example%%2Fo/n/x: %d %s, owner=%q name=%q; want 200, owner h.example/o, name n",
			rec.Code, rec.Body, owner, name)
	}
}

func TestCIWaitPollsGitHubChecksWhenRepoUsesGitHubCIMode(t *testing.T) {
	gitproxy.LogToTest(t)
	tokenFile := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	raw, _ := json.Marshal(map[string]string{"sb-gh": "tok-gh"})
	if err := os.WriteFile(tokenFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, err := gitproxy.LoadSandboxTokens(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	actions := &fakeCLIActions{
		runs:         map[string]ci.Run{},
		githubChecks: true,
		githubWaitResult: mcp.GitHubCheckWaitResult{
			Status:     "passed",
			Verdict:    mcp.WaitVerdictPassed,
			Commit:     "abcdef0123456789",
			Conclusion: "success",
			Report:     "Waited 1s for CI on grain/task-7 at abcdef012345.\n\nEvery check against abcdef012345 finished and none of them failed.",
		},
	}
	proxy := &gitproxy.GitProxy{
		Tokens:    tokens,
		Namespace: &namespace.Server{CIStore: ci.NewRunStore(10)},
		NamespaceScopes: fakeNamespaceScopes{scopes: map[string]model.NamespaceScope{
			"sb-gh": {TaskID: "7", RunID: "run-a", TaskRepo: "bwsalmon/grain", TaskBranch: "grain/task-7", TaskBase: "main",
				MayReadCI: true, AllowedRepos: map[string]bool{"bwsalmon/grain": true}},
		}},
		CLIActions: actions,
	}
	h := gitproxy.NewHandler(proxy)

	rec := send(h, http.MethodPost, "tok-gh", "/cli/ci/wait", `{"timeout_seconds":30}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /cli/ci/wait: %d %s", rec.Code, rec.Body)
	}
	if len(actions.started) != 0 {
		t.Fatalf("StartTaskCI called %d times on CIModeGitHub repo, want 0", len(actions.started))
	}
	if actions.githubWaits != 1 {
		t.Fatalf("WaitForGitHubChecks called %d times, want 1", actions.githubWaits)
	}
	var resp gitproxy.CLIWaitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "passed" || resp.RunID != "github" || resp.Commit != "abcdef0123456789" {
		t.Fatalf("response = %+v, want status=passed run_id=github commit=abcdef0123456789", resp)
	}
}
