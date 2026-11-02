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

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/capability/selfdebug"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// pull_request_status and wait_for_checks have to be registered whatever
// the flags said, so that a run whose task has no repo gets each tool's
// own explanation rather than "unknown tool pull_request_status" -- which
// reads like grain is broken rather than like a task with nothing to
// look at.
func TestPullRequestToolsAlwaysRegisterTheTool(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		dataDir string
		repo    string
		branch  string
	}{
		{name: "nothing configured"},
		{name: "repo but no branch", repo: "acme/widgets"},
		{name: "repo but no data directory", repo: "acme/widgets", branch: "grain/task-9"},
		{name: "unparseable repo", dataDir: t.TempDir(), repo: "not-a-repo", branch: "grain/task-9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools := daemon.PullRequestTools(tc.dataDir, "github.com", false, tc.repo, tc.branch, "main", "", false, false, "", nil)
			var names []string
			for _, tool := range tools {
				names = append(names, tool.Name)
				res := tool.Handler(context.Background(), map[string]any{})
				if !res.IsError || !strings.Contains(res.Text, "no GitHub repository configured") {
					t.Errorf("%s answered %q (isError=%v), want the "+
						"nothing-configured explanation", tool.Name, res.Text, res.IsError)
				}
			}
			if want := []string{"pull_request_status", "base_branch_status",
				"reply_to_pull_request_comment", "resolve_review_thread", "wait_for_checks"}; !slices.Equal(names, want) {
				t.Fatalf("pullRequestTools registered %v, want %v", names, want)
			}
		})
	}
}

// The happy path: a repo, a branch and a data directory produce a real
// reader scoped to exactly that repo and branch. A data directory with
// no secrets/github in it yields an empty credential ladder rather than
// an error (gitproxy.readStringMap) -- that is a deployment reading a
// public repo unauthenticated, a supported shape and not a failure.
func TestPullRequestReaderScopesToTheRunsOwnBranch(t *testing.T) {
	t.Parallel()
	client, scope, err := daemon.PullRequestReader(t.TempDir(), "github.com", false, "acme/widgets", "grain/task-9", "release-2", "", false, false, "")
	if err != nil {
		t.Fatalf("pullRequestReader: %v", err)
	}
	if client == nil {
		t.Fatal("client = nil, want a GitHub reader")
	}
	want := mcp.PullRequestScope{
		Owner: "acme", Repo: "widgets", Branch: "grain/task-9", Base: "release-2",
	}
	if scope != want {
		t.Errorf("scope = %+v, want %+v", scope, want)
	}
}

// An unset -pr-base is carried through unresolved rather than turned
// into a branch name here: it is what every ordinary task has, and
// base_branch_status resolves it against the repo when it is actually
// called, so a repo this credential cannot read costs that one tool an
// answer rather than costing the run pull_request_status too.
func TestPullRequestReaderLeavesAnUnsetBaseUnresolved(t *testing.T) {
	t.Parallel()
	_, scope, err := daemon.PullRequestReader(t.TempDir(), "github.com", false, "acme/widgets", "grain/task-9", "", "", false, false, "")
	if err != nil {
		t.Fatalf("pullRequestReader: %v", err)
	}
	if scope.Base != "" {
		t.Errorf("scope.Base = %q, want it left empty for base_branch_status to resolve", scope.Base)
	}
}

// An unset -pr-repo is a task with no repo, not a misconfiguration:
// there is nothing to warn an operator about, so it must not come back
// as an error.
func TestPullRequestReaderTreatsNoRepoAsNoError(t *testing.T) {
	t.Parallel()
	client, scope, err := daemon.PullRequestReader("/data", "github.com", false, "", "", "", "", false, false, "")
	if err != nil {
		t.Fatalf("pullRequestReader err = %v, want nil for a task with no repo", err)
	}
	if client != nil {
		t.Errorf("client = %+v, want nil", client)
	}
	if scope != (mcp.PullRequestScope{}) {
		t.Errorf("scope = %+v, want the zero scope", scope)
	}
}

// Which repositories a run may read grain CI for is -pr-repo's answer
// for an ordinary task, and "every one of them" for a task holding the
// wide grant (grain/task-172, pkg/mcp's ci_repo_scope.go).
func TestCIRepoScopeFollowsTheRunsOwnRepositoryAndTheWideGrant(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		repo   string
		grants daemon.GrantNames
		wide   []string
		want   mcp.CIRepoScope
	}{
		{"an ordinary task", "acme/widgets", daemon.GrantNames{"grain-ci"}, nil,
			mcp.CIRepoScope{Repo: "acme/widgets"}},
		{"a task debugging grain itself", "acme/widgets", daemon.GrantNames{"grain-ci", selfdebug.CapabilityName},
			[]string{selfdebug.CapabilityName},
			mcp.CIRepoScope{Repo: "acme/widgets", AllRepos: true}},
		// grain/task-2555: a team whose self-debug is scoped to its own
		// work reads its own repository's CI and nobody else's.
		{"a team-scoped self-debug grant", "acme/widgets", daemon.GrantNames{"grain-ci", selfdebug.CapabilityName},
			nil, mcp.CIRepoScope{Repo: "acme/widgets"}},
		{"a task with no repository", "", daemon.GrantNames{"grain-ci"}, nil, mcp.CIRepoScope{}},
		{"no repository, but the wide grant", "", daemon.GrantNames{selfdebug.CapabilityName},
			[]string{selfdebug.CapabilityName}, mcp.CIRepoScope{AllRepos: true}},
		{"grain-ci plus repo-scoped api:ci-read", "acme/widgets",
			daemon.GrantNames{"grain-ci", "api:ci-read@acme/other"}, nil,
			mcp.CIRepoScope{Repo: "acme/widgets", Reads: []string{"acme/other"}}},
		{"grain-ci plus unscoped api:ci-read", "acme/widgets",
			daemon.GrantNames{"grain-ci", "api:ci-read"}, nil,
			mcp.CIRepoScope{Repo: "acme/widgets", AllRepos: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := daemon.CiRepoScope(tc.repo, tc.grants, tc.wide); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ciRepoScope(%q, %v) = %+v, want %+v", tc.repo, tc.grants, got, tc.want)
			}
		})
	}
}

// pkg/mcp restates the wide grant's name because pkg/capability imports
// it rather than the other way round. This is the seam that keeps the
// two spellings one grant: rename the capability and this fails here,
// where both are in scope, rather than silently leaving every run
// scoped to its own repository.
func TestCIWideReadGrantIsSelfDebug(t *testing.T) {
	t.Parallel()
	if mcp.CIWideReadGrant != selfdebug.CapabilityName {
		t.Errorf("mcp.CIWideReadGrant = %q, want %q", mcp.CIWideReadGrant, selfdebug.CapabilityName)
	}
}

// -propose-repo is how the daemon's own answer to "which repositories
// may a proposal name?" reaches the process that describes the argument
// (model.Store.WorkRepos, agent.ProposeRepoArgs). The order is the
// daemon's -- most recently worked in first -- and has to survive, since
// a run reading a capped list reads the front of it.
//
// An entry that names no repository is dropped rather than being fatal,
// for the reason parsedRunDeadline's own malformed value is: every other
// tool this process serves is how a run touches its sandbox at all, and
// these values come off grain's store rather than off a human, so a bad
// one is grain's bug to fix and not a run's to be killed by.
//
// Not parallel: whether a URL's host is dropped depends on the
// process-wide default git host (model.SetDefaultGitHost), which every
// in-process daemon test in this package sets to its own fake GitHub.
// A sequential test runs with none of them going, so it pins the
// default itself (pinDefaultGitHost).
// pinDefaultGitHost puts the process-wide default git host back to
// github.com for the rest of t, and restores whatever it was afterwards.
// Every in-process daemon test in this package sets it to its own fake
// GitHub (daemon.Run, model.SetDefaultGitHost) and nothing sets it back,
// so a sequential test that parses or prints a github.com repository
// reads whichever daemon ran last unless it pins the default itself.
// Only for tests that are not parallel: the value is shared by the whole
// process.
func pinDefaultGitHost(t *testing.T) {
	t.Helper()
	prev := model.DefaultRepoHost()
	model.SetDefaultGitHost("")
	t.Cleanup(func() { model.SetDefaultGitHost(prev) })
}

func TestProposeReposKeepsWhatNamesARepository(t *testing.T) {
	pinDefaultGitHost(t)
	repos := daemon.ProposeRepos{"acme/widgets", "https://github.com/acme/docs.git", "not a repo", ""}
	want := []string{"acme/widgets", "acme/docs"}
	if got := repos.Nameable(); !slices.Equal(got, want) {
		t.Errorf("nameable() = %v, want %v", got, want)
	}
	if got := daemon.ProposeRepos(nil).Nameable(); got != nil {
		t.Errorf("nameable() = %v with no flag at all, want nothing to describe", got)
	}
}

// The flag really does reach the description a run reads, which is the
// whole point of carrying the list this far: propose_task's own `repo`
// argument is where an agent decides what to write there.
func TestProposeReposReachProposeTasksOwnDescription(t *testing.T) {
	t.Parallel()
	tools := mcp.NewMockTools(&mcp.MockSink{}, daemon.ProposeRepos{"acme/widgets", "acme/docs"}.Nameable())
	var described string
	for _, tool := range tools {
		if tool.Name != "propose_task" {
			continue
		}
		properties, _ := tool.InputSchema["properties"].(map[string]any)
		spec, _ := properties["repo"].(map[string]any)
		described, _ = spec["description"].(string)
	}
	for _, want := range []string{"acme/widgets", "acme/docs"} {
		if !strings.Contains(described, want) {
			t.Errorf("propose_task's repo argument = %q, want it to name %q", described, want)
		}
	}
}

// -run-deadline is how a run's wall-clock deadline reaches the tools it
// calls (agent.RunDeadlineArgs on the way in,
// mcp.Registry.AnnounceDeadline on the way out), so it has to survive
// the round trip through the command line as the same instant.
func TestParsedRunDeadlineReadsTheFlagBack(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	if got := daemon.ParsedRunDeadline(at.Format(time.RFC3339)); !got.Equal(at) {
		t.Errorf("parsedRunDeadline = %s, want %s", got, at)
	}
}

// Unset is every caller with no deadline to give -- pkg/mcp's own tests,
// tests/e2e, a `grain mcpserver` run by hand -- and a malformed value is
// an operator's problem, not the run's: neither may cost a run the tools
// that are its only way to touch its sandbox, so both come back as "say
// nothing about time" rather than as a failure.
func TestParsedRunDeadlineIsZeroWithoutAUsableValue(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "not-a-time", "2026-03-04 05:06:07"} {
		if got := daemon.ParsedRunDeadline(value); !got.IsZero() {
			t.Errorf("parsedRunDeadline(%q) = %s, want the zero time", value, got)
		}
	}
}

func TestServeMCPServerReturnsErrorsOnMalformedArgs(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	testCases := []struct {
		name string
		args []string
	}{
		{
			name: "unknown flag",
			args: []string{"-sandbox-root", tmpDir, "-invalid-flag-xyz"},
		},
		{
			name: "missing all sandbox targets",
			args: []string{"-pr-repo", "owner/repo"},
		},
		{
			name: "multiple sandbox targets",
			args: []string{"-sandbox-root", tmpDir, "-container", "my-container"},
		},
		{
			name: "non-existent sandbox root",
			args: []string{"-sandbox-root", filepath.Join(tmpDir, "does-not-exist")},
		},
		{
			name: "task without server",
			args: []string{"-sandbox-root", tmpDir, "-task", "123"},
		},
		{
			name: "server without task",
			args: []string{"-sandbox-root", tmpDir, "-server", "http://127.0.0.1:8080"},
		},
		{
			name: "granule vm without ssh user",
			args: []string{"-granule-vm", "vm-1", "-workspace", "/work"},
		},
		{
			name: "granule vm without workspace",
			args: []string{"-granule-vm", "vm-1", "-ssh-user", "debian"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			in := bytes.NewReader(nil)
			var out bytes.Buffer
			err := serveMCPServer(in, &out, tc.args)
			if err == nil {
				t.Fatalf("expected error for args %v, got nil", tc.args)
			}
		})
	}
}

func TestServeMCPServerValidatesGrantsAgainstTaskStore(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tasks/task-valid" {
			_ = json.NewEncoder(w).Encode(ui.TaskDetail{
				Task: ui.Task{
					ID:           "task-valid",
					Capabilities: []string{"memory"},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	// 1. Task not found -> error returned
	in := bytes.NewReader(nil)
	var out bytes.Buffer
	err := serveMCPServer(in, &out, []string{
		"-sandbox-root", tmpDir,
		"-server", ts.URL,
		"-task", "task-nonexistent",
	})
	if err == nil || !strings.Contains(err.Error(), "fetching task") {
		t.Fatalf("expected error fetching nonexistent task, got: %v", err)
	}

	// 2. Valid task with client-forged grants -> forged grants ignored, task capabilities used
	inValid := bytes.NewReader(nil)
	var outValid bytes.Buffer
	errValid := serveMCPServer(inValid, &outValid, []string{
		"-sandbox-root", tmpDir,
		"-server", ts.URL,
		"-task", "task-valid",
		"-grant", "self-debug",
		"-grant", "grain-log",
	})
	if errValid != nil && !errors.Is(errValid, io.EOF) {
		t.Fatalf("unexpected error for valid task: %v", errValid)
	}
}

func TestServeMCPServerRegistersReadOnlyCIToolsForRepoScopedCIReadGrant(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tasks/task-review":
			_ = json.NewEncoder(w).Encode(ui.TaskDetail{
				Task: ui.Task{
					ID:           "task-review",
					Repo:         "acme/widgets",
					Capabilities: []string{"api:ci-read@acme/widgets"},
				},
			})
		case "/api/ci/runs/1201":
			_ = json.NewEncoder(w).Encode(ci.Run{
				ID:         "1201",
				Repo:       "acme/widgets",
				Ref:        "main",
				Commit:     "deadbeef12345678",
				Conclusion: ci.ConclusionFailure,
				Jobs: []ci.JobRun{{
					JobID:      "publish-image",
					Name:       "publish-image",
					Conclusion: ci.ConclusionFailure,
					Steps: []ci.StepRun{{
						Name:       "docker push",
						Conclusion: ci.ConclusionFailure,
						ExitCode:   1,
						Output:     "pushing image to registry: 500 Internal Server Error\n",
					}},
				}},
			})
		case "/api/ci/runs/9999":
			_ = json.NewEncoder(w).Encode(ci.Run{
				ID:         "9999",
				Repo:       "acme/other",
				Conclusion: ci.ConclusionFailure,
			})
		case "/api/ci/runs":
			if got := r.URL.Query().Get("repo"); got != "acme/widgets" {
				http.Error(w, "unscoped or wrong repo query: "+got, http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(ui.CIRunsResponse{
				Runs: []ci.Run{{
					ID:         "1201",
					Repo:       "acme/widgets",
					Ref:        "main",
					Conclusion: ci.ConclusionFailure,
				}},
				Matched: 1,
				Stored:  2,
			})
		case "/api/ci/faults":
			_ = json.NewEncoder(w).Encode([]any{})
		default:
			if strings.HasSuffix(r.URL.Path, "/status") {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	requests := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_ci_log","arguments":{"run_id":"1201"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get_ci_log","arguments":{"run_id":"9999"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"list_ci_runs","arguments":{}}}`,
	}, "\n") + "\n"

	// 1. On the full surface, get_ci_log and list_ci_runs are registered
	// and scoped to acme/widgets (even when -pr-repo is acme/app), while
	// wait_for_checks remains GitHub's and run_ci_job / retrigger_ci stay absent.
	var out bytes.Buffer
	err := serveMCPServer(strings.NewReader(requests), &out, []string{
		"-mcp-surface", daemon.MCPSurfaceFull,
		"-sandbox-root", tmpDir,
		"-server", ts.URL,
		"-task", "task-review",
		"-pr-repo", "acme/app",
		"-pr-branch", "grain/task-review",
	})
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("serveMCPServer (full): %v", err)
	}

	type rpcResp struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	parseResponses := func(raw string) map[int]json.RawMessage {
		byID := map[int]json.RawMessage{}
		for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
			if line == "" {
				continue
			}
			var resp rpcResp
			if err := json.Unmarshal([]byte(line), &resp); err != nil {
				t.Fatalf("unmarshalling response %q: %v", line, err)
			}
			byID[resp.ID] = resp.Result
		}
		return byID
	}
	byID := parseResponses(out.String())

	var listed struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(byID[2], &listed); err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	toolDesc := map[string]string{}
	for _, tool := range listed.Tools {
		toolDesc[tool.Name] = tool.Description
	}
	for _, want := range []string{"get_ci_log", "list_ci_runs", "get_ci_artifact", "wait_for_checks"} {
		if _, ok := toolDesc[want]; !ok {
			t.Errorf("tools/list missing %q", want)
		}
	}
	for _, unwanted := range []string{"run_ci_job", "retrigger_ci"} {
		if _, ok := toolDesc[unwanted]; ok {
			t.Errorf("tools/list unexpectedly registered %q without grain-ci grant", unwanted)
		}
	}
	if strings.HasPrefix(toolDesc["wait_for_checks"], "Start grain CI") {
		t.Errorf("wait_for_checks was overwritten with grain CI's tool: %s", toolDesc["wait_for_checks"])
	}

	type callResult struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	var allowed callResult
	if err := json.Unmarshal(byID[3], &allowed); err != nil {
		t.Fatalf("get_ci_log 1201: %v", err)
	}
	if allowed.IsError || len(allowed.Content) == 0 || !strings.Contains(allowed.Content[0].Text, "500 Internal Server Error") {
		t.Errorf("get_ci_log 1201 = %+v, want log output", allowed)
	}

	var refused callResult
	if err := json.Unmarshal(byID[4], &refused); err != nil {
		t.Fatalf("get_ci_log 9999: %v", err)
	}
	if !refused.IsError || len(refused.Content) == 0 || !strings.Contains(refused.Content[0].Text, "acme/other") {
		t.Errorf("get_ci_log 9999 = %+v, want scope refusal naming acme/other", refused)
	}

	var listedRuns callResult
	if err := json.Unmarshal(byID[5], &listedRuns); err != nil {
		t.Fatalf("list_ci_runs: %v", err)
	}
	if listedRuns.IsError || len(listedRuns.Content) == 0 || !strings.Contains(listedRuns.Content[0].Text, "1201") {
		t.Errorf("list_ci_runs = %+v, want run 1201 from pinned acme/widgets query", listedRuns)
	}

	// 2. On the control surface, GitHub's pull_request_status,
	// wait_for_checks and base_branch_status stay available (a grain-ci
	// grant would have hidden them on a ci_mode: github repository).
	controlRequests := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	}, "\n") + "\n"
	var controlOut bytes.Buffer
	err = serveMCPServer(strings.NewReader(controlRequests), &controlOut, []string{
		"-mcp-surface", daemon.MCPSurfaceControl,
		"-sandbox-root", tmpDir,
		"-server", ts.URL,
		"-task", "task-review",
		"-pr-repo", "acme/widgets",
		"-pr-branch", "grain/task-review",
	})
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("serveMCPServer (control): %v", err)
	}
	controlByID := parseResponses(controlOut.String())
	if err := json.Unmarshal(controlByID[2], &listed); err != nil {
		t.Fatalf("control tools/list: %v", err)
	}
	controlNames := map[string]bool{}
	for _, tool := range listed.Tools {
		controlNames[tool.Name] = true
	}
	for _, want := range []string{"pull_request_status", "wait_for_checks", "base_branch_status"} {
		if !controlNames[want] {
			t.Errorf("control tools/list missing %q", want)
		}
	}
}
