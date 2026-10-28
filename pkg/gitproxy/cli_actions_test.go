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
}

func (f *fakeCLIActions) StartTaskCI(_ context.Context, taskID string, req gitproxy.CIStartRequest) (ci.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, req)
	run := ci.Run{ID: "run-new", Repo: "bwsalmon/grain", Ref: "grain/task-7", Commit: "abcdef0123456789",
		Jobs: []ci.JobRun{{Name: "go"}, {Name: "lint", Conclusion: ci.ConclusionSuccess}}}
	f.runs[run.ID] = run
	return run, nil
}

func (f *fakeCLIActions) CIRun(_ context.Context, id string) (ci.Run, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	waits []string
	calls []map[string]any
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

func newActionProxy(t *testing.T, actions *fakeCLIActions) (http.Handler, *fullActivity, *fakeCreditor) {
	t.Helper()
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
