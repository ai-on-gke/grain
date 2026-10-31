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

package security

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/bootstrap"
	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/capability/quarantine"
	"github.com/ai-on-gke/grain/pkg/capability/readissues"
	"github.com/ai-on-gke/grain/pkg/capability/selfdebug"
	"github.com/ai-on-gke/grain/pkg/capability/selfupgrade"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A sandbox's tools are its authority: whatever its MCP server publishes,
// the agent can call. Two boundaries hold that authority to the task:
//
//   - a tool that reads or acts beyond the sandbox's own workspace and
//     its own task's conversation is published only to a run holding the
//     grant for it, so a run with no grants gets the sandbox's own tools,
//     the escape hatches that hand a question to a person, and its own
//     branch's CI -- and nothing else;
//   - a tool that takes a repository refuses one outside the task's own
//     scope (its target and its Reads), rather than reading it.
//
// The first is driven through the real `grain mcpserver` -- which tools
// it registers for which -grant is decided in cmd/grain, where no test
// outside it can reach -- and the second through pkg/mcp's exported
// constructors, which is all the server does with a repository scope.

// everyRunTool is what a run holding no grant may be offered, and why.
var everyRunTool = map[string]string{
	// The sandbox's own workspace. revert_task/revert_merge commit a git
	// revert in it and push nothing -- what run_command could do there
	// anyway -- and refuse every call where the workspace is not a local
	// -sandbox-root (grain/task-1568).
	"run_command": "sandbox", "read_file": "sandbox", "write_file": "sandbox", "edit_file": "sandbox",
	"revert_task": "sandbox", "revert_merge": "sandbox",
	// Escape hatches: each is relayed to a person after the run, and
	// grants nothing itself. request_secret is one of them -- what a
	// person types goes to the secret store and never back to the agent.
	"ask_question": "escape hatch", "request_secret": "escape hatch", "comment_on_issue": "escape hatch",
	"nothing_needed": "escape hatch",
	"write_handover": "escape hatch", "propose_task": "escape hatch", "add_review_comment": "escape hatch",
	// The run's own branch's CI, which it cannot leave.
	"pull_request_status": "own branch", "base_branch_status": "own branch", "wait_for_checks": "own branch",
	// Answering and resolving review comments on the pull request open
	// for that same branch: the tools find the pull request from the
	// run's own scope and refuse any comment or thread not on it.
	"reply_to_pull_request_comment": "own branch", "resolve_review_thread": "own branch",
	// Read-only merge queue state.
	"query_merge_queue": "read-only",
}

// daemonRunTool is what a run holding no grant is offered on top of
// everyRunTool once its server is attached to the daemon that
// dispatched it (-server/-task) -- every production run -- and why. Each
// asks that daemon to act on the run's own task and nothing else.
var daemonRunTool = map[string]string{
	// Opens the pull request for the run's own branch, which the run
	// has already pushed; grain picks the repo, branch and base.
	"open_pull_request": "own branch",
	// Rebuilds the run's own sandbox, and no other.
	"recreate_sandbox": "sandbox",
	// One phrase on the run's own task row, which grain never reads back.
	"update_status": "own task",
	// Reads another task's title, state, outcome and closing note --
	// never its transcript -- and parks the run's own task on it as a
	// depends-on link the picker's own checks admit (ui.Client.WaitOn).
	"wait_for_task": "own task",
	// Stops the run and holds its own task -- depends-on links, a
	// deferral, or nothing -- until a condition (ui.Client.ResumeLater):
	// the general form of wait_for_task's park.
	"resume_later": "own task",
	// Raises an operator notification as the run's own task, namespaced
	// to it, bounded per run and per task, and never critical without
	// api:task-admin (ui.Client.AgentNotify). It parks nothing.
	"notify_operator": "own task",
	// A note on the run's parent's or a child's conversation, signed as
	// the run's own task; any other target needs api:task-admin
	// (ui.Client.PostTaskNote). It never parks or re-states the target.
	"message_task": "own task",
	// Thin MCP wrappers over the grant-scoped grain namespace: each call
	// checks the run's own namespace.Scope before resolving ci:// or src://.
	"grain_ls": "grant-scoped namespace", "grain_read": "grant-scoped namespace",
	"grain_grep": "grant-scoped namespace", "grain_copy": "grant-scoped namespace",
}

// interactiveRunTool is what -interactive adds on top of that, for a
// task somebody is sitting with, and why. (-interactive also swaps
// propose_task for a form that files its proposal there and then; the
// name, and what a proposal is allowed to be, stay the escape hatch's.)
var interactiveRunTool = map[string]string{
	// Posts into the task's own conversation and waits for a person
	// there to answer -- ask_question without ending the turn.
	"chat": "escape hatch",
}

// daemonToolGrants is toolGrants plus the grants whose tools exist only
// with a daemon to ask.
var daemonToolGrants = append(slices.Clone(toolGrants),
	quarantine.CapabilityName,
)

// knownUngatedTools are tools every run is offered today that act beyond
// its own task with no grant. Remove each from here when it is gated;
// the test then fails by name if it comes back. Empty since
// grain/task-1568 put broadcast_team_message behind team-broadcast and
// listed the revert tools as the sandbox tools they are.
var knownUngatedTools = map[string]bool{}

// toolGrants is every grant `grain mcpserver` serves tools for without a
// daemon -- tests/e2e's everyToolGrant.
var toolGrants = []string{
	selfdebug.CapabilityName,
	"https://grain.dev/capabilities/grain#journal",
	"https://grain.dev/capabilities/grain#health",
	bootstrap.CapabilityName,
	selfupgrade.CapabilityName,
	grainci.CapabilityName,
	readissues.CapabilityName,
	`https://grain.dev/capabilities/grain#sql={"write":false}`,
	`https://grain.dev/capabilities/grain#sql={"write":true}`,
}

// grantImplies is the one grant that carries another's tools: write
// access to grain's database includes reading it.
var grantImplies = map[string]string{
	`https://grain.dev/capabilities/grain#sql={"write":true}`: `https://grain.dev/capabilities/grain#sql={"write":false}`,
}

var (
	grainBinaryOnce sync.Once
	grainBinaryPath string
	grainBinaryErr  error
)

func grainBinary(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not installed")
	}
	grainBinaryOnce.Do(func() {
		dir, err := os.MkdirTemp("", "grain-security-")
		if err != nil {
			grainBinaryErr = err
			return
		}
		grainBinaryPath = filepath.Join(dir, "grain")
		// The main the Makefile ships (MAIN_grain), so the tools and
		// grants checked here are those of the binary deployments run.
		out, err := exec.Command("go", "build", "-o", grainBinaryPath, "github.com/ai-on-gke/grain/cmd/grain").CombinedOutput()
		if err != nil {
			grainBinaryErr = fmt.Errorf("building grain: %v\n%s", err, out)
		}
	})
	if grainBinaryErr != nil {
		t.Fatal(grainBinaryErr)
	}
	return grainBinaryPath
}

// publishedTools starts `grain mcpserver` over a fresh workspace with
// grants and returns what tools/list advertises.
func publishedTools(t *testing.T, grants ...string) map[string]bool {
	t.Helper()
	return listTools(t, mcpserverArgs(t, grants), grants)
}

// stubDaemon answers the one call `grain mcpserver -server -task` makes
// before it serves -- the task itself, whose capabilities are the grants
// the server registers tools for, overriding any -grant flag -- and
// fails the test on any other: listing tools must not reach the daemon.
func stubDaemon(t *testing.T, grants []string) string {
	t.Helper()
	caps := append([]string{}, grants...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/tasks/1" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "1", "team": "default", "branch": "grain/task-1", "capabilities": caps,
			})
			return
		}
		t.Errorf("listing tools reached the daemon: %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// publishedDaemonTools is publishedTools for a server attached to a
// daemon, the way every production run's is: -server/-task against a
// stub daemon whose task holds grants, and -interactive when asked.
func publishedDaemonTools(t *testing.T, interactive bool, grants ...string) map[string]bool {
	t.Helper()
	args := append(mcpserverArgs(t, grants), "-server", stubDaemon(t, grants), "-task", "1",
		"-pr-repo", "acme/widgets", "-pr-branch", "grain/task-1")
	if interactive {
		args = append(args, "-interactive")
	}
	return listTools(t, args, grants)
}

// mcpserverArgs serves the full roster: what these tests check is which
// grant turns on which tool, which the control roster hides by serving
// most of them as `grain` commands instead (TestControlRoster checks that).
func mcpserverArgs(t *testing.T, grants []string) []string {
	args := []string{"mcpserver", "-sandbox-root", t.TempDir(), "-mcp-surface", "full"}
	for _, g := range grants {
		args = append(args, "-grant", g)
	}
	return args
}

func listTools(t *testing.T, args, grants []string) map[string]bool {
	t.Helper()
	cmd := exec.Command(grainBinary(t), args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(stdin, stdout, stdin)
	defer func() {
		client.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			_ = cmd.Process.Kill()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	infos, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("tools/list against grain mcpserver %v: %v\nstderr:\n%s", grants, err, stderr.String())
	}
	names := map[string]bool{}
	for _, info := range infos {
		names[info.Name] = true
	}
	return names
}

func TestGrantGatedToolsAreAbsentWithoutTheGrant(t *testing.T) {
	bare := publishedTools(t)
	if !bare["run_command"] {
		t.Fatalf("a run with no grants was not offered run_command; got %v", bare)
	}
	for name := range daemonRunTool {
		if bare[name] {
			t.Errorf("a run with no daemon to ask is offered %q, which only a daemon can answer", name)
		}
	}
	checkGrantPartition(t, "a run holding no grant", bare, everyRunTool, toolGrants, func(grant string) map[string]bool {
		return publishedTools(t, grant)
	})
}

// The roster above is a bare `grain mcpserver -sandbox-root`. Every
// production run's server is also attached to the daemon that
// dispatched it, which registers more tools -- some for every run, some
// only under a grant -- and the same partition has to hold there.
func TestDaemonAttachedGrantGatedToolsAreAbsentWithoutTheGrant(t *testing.T) {
	allowed := map[string]string{}
	for name, why := range everyRunTool {
		allowed[name] = why
	}
	for name, why := range daemonRunTool {
		allowed[name] = why
	}
	bare := publishedDaemonTools(t, false)
	for name := range daemonRunTool {
		if !bare[name] {
			t.Errorf("a run attached to its daemon was not offered %q; got %v", name, bare)
		}
	}
	for name := range interactiveRunTool {
		if bare[name] {
			t.Errorf("a run nobody is sitting with is offered %q, which only an -interactive run should be", name)
		}
	}
	checkGrantPartition(t, "a daemon-attached run holding no grant", bare, allowed, daemonToolGrants, func(grant string) map[string]bool {
		return publishedDaemonTools(t, false, grant)
	})

	for name, why := range interactiveRunTool {
		allowed[name] = why
	}
	live := publishedDaemonTools(t, true)
	for name := range interactiveRunTool {
		if !live[name] {
			t.Errorf("an -interactive run was not offered %q; got %v", name, live)
		}
	}
	checkGrantPartition(t, "an interactive run holding no grant", live, allowed, daemonToolGrants, func(grant string) map[string]bool {
		return publishedDaemonTools(t, true, grant)
	})
}

// checkGrantPartition holds a roster to its partition: bare, the tools
// a run holding no grant is offered, is only what allowed lists (or
// knownUngatedTools excuses), and each of grants turns on tools of its
// own that no other grant does and allowed does not list.
func checkGrantPartition(t *testing.T, who string, bare map[string]bool, allowed map[string]string, grants []string, withGrant func(string) map[string]bool) {
	t.Helper()
	for name := range bare {
		if _, ok := allowed[name]; ok {
			continue
		}
		if knownUngatedTools[name] {
			continue
		}
		t.Errorf("%s is offered %q, which is not a sandbox tool, an escape hatch "+
			"or its own branch's CI. Register it under the grant it belongs to in pkg/daemon/mcpserver.go, "+
			"or -- if every run really should have it -- add it to everyRunTool (or daemonRunTool, "+
			"interactiveRunTool) with the reason", who, name)
	}
	for name := range knownUngatedTools {
		if !bare[name] {
			t.Errorf("%q is no longer offered to %s: remove it from knownUngatedTools "+
				"so this test holds it there", name, who)
		}
	}

	// Each grant's own tools appear with that grant and with no other:
	// holding one grant is not a way to another's tools.
	own := map[string][]string{}
	for _, grant := range grants {
		for name := range withGrant(grant) {
			if !bare[name] {
				own[grant] = append(own[grant], name)
			}
		}
	}
	gated := 0
	for grant, names := range own {
		gated += len(names)
		for other, otherNames := range own {
			if other == grant || grantImplies[other] == grant || grantImplies[grant] == other {
				continue
			}
			for _, name := range names {
				if strings.HasPrefix(name, "api_") && model.APIAliasPreset(grant) != "" && model.APIAliasPreset(other) != "" {
					continue
				}
				if slices.Contains(otherNames, name) {
					t.Errorf("%q is offered both to a run holding %s and to one holding %s", name, grant, other)
				}
			}
		}
	}
	// Not vacuous: grants do turn tools on, so their absence above is
	// a decision rather than a server that registers nothing.
	if gated == 0 || !slices.Contains(own[selfdebug.CapabilityName], "read_grain_task") {
		t.Fatalf("no grant turned on any tool (self-debug's: %v); the absences above prove nothing", own[selfdebug.CapabilityName])
	}
	// And none of what a grant turns on is allowed to a bare run.
	for grant, names := range own {
		for _, name := range names {
			if _, ok := allowed[name]; ok {
				t.Errorf("%q is allowed to %s but is offered only with the %s grant", name, who, grant)
			}
		}
	}
}

// scopedCI is grain CI as the daemon answers it: one run of the task's
// own repository, one of a repository it may read, one of another
// task's repository.
type scopedCI struct {
	mu    sync.Mutex
	finds []ci.RunFilter
}

var scopedCIRuns = map[string]ci.Run{
	"own":     {ID: "own", Repo: "acme/widgets", Ref: "grain/task-1", Jobs: []ci.JobRun{{JobID: "go", Name: "go", Steps: []ci.StepRun{{Name: "test", Output: "own log"}}, Artifacts: []ci.Artifact{{Run: "own", Job: "go", Name: "trace", Stored: true, Bytes: int64(len("own trace bytes"))}}}}},
	"read":    {ID: "read", Repo: "acme/shared-lib", Ref: "main", Jobs: []ci.JobRun{{JobID: "go", Name: "go", Steps: []ci.StepRun{{Name: "test", Output: "shared log"}}}}},
	"foreign": {ID: "foreign", Repo: "other/secret-app", Ref: "main", Jobs: []ci.JobRun{{JobID: "go", Name: "go", Steps: []ci.StepRun{{Name: "test", Output: "SECRET-LOG"}}, Artifacts: []ci.Artifact{{Run: "foreign", Job: "go", Name: "trace", Stored: true, Bytes: int64(len("SECRET-ARTIFACT-BYTES"))}}}}},
}

func (s *scopedCI) StartGrainCI(context.Context, []string) (ci.Run, error) {
	return scopedCIRuns["own"], nil
}
func (s *scopedCI) RetriggerGrainCI(context.Context, string, string, []string, bool) (ci.Run, error) {
	return scopedCIRuns["own"], nil
}
func (s *scopedCI) GrainCIRun(_ context.Context, id string) (ci.Run, error) {
	if r, ok := scopedCIRuns[id]; ok {
		return r, nil
	}
	return ci.Run{}, fmt.Errorf("no run %s", id)
}
func (s *scopedCI) FindGrainCIRuns(_ context.Context, f ci.RunFilter, _ int) (ci.RunPage, error) {
	s.mu.Lock()
	s.finds = append(s.finds, f)
	s.mu.Unlock()
	var page ci.RunPage
	ids := make([]string, 0, len(scopedCIRuns))
	for id := range scopedCIRuns {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if r := scopedCIRuns[id]; f.Repo == "" || strings.EqualFold(f.Repo, r.Repo) {
			page.Runs = append(page.Runs, r)
		}
	}
	return page, nil
}
func (s *scopedCI) GrainCISharedFaults(context.Context) ([]mcp.CIFault, error) { return nil, nil }

var scopedCIArtifactBodies = map[string]string{
	"own":     "own trace bytes",
	"foreign": "SECRET-ARTIFACT-BYTES",
}

func (s *scopedCI) DownloadGrainCIArtifact(_ context.Context, ref ci.ArtifactRef, w io.Writer) (int64, error) {
	body := scopedCIArtifactBodies[ref.Run]
	n, err := w.Write([]byte(body))
	return int64(n), err
}

func TestRepoScopedToolsRefuseARepositoryOutsideTheTask(t *testing.T) {
	runner := &scopedCI{}
	scope := mcp.CIRepoScope{Repo: "acme/widgets", Reads: []string{"acme/shared-lib"}}
	tools := map[string]mcp.Tool{}
	for _, tool := range mcp.NewGrainCIWaitTools(runner, scope) {
		tools[tool.Name] = tool
	}
	for _, tool := range mcp.NewRunCIJobTools(runner, scope) {
		tools[tool.Name] = tool
	}
	ctx := context.Background()
	call := func(name string, args map[string]any) mcp.Result {
		t.Helper()
		tool, ok := tools[name]
		if !ok {
			t.Fatalf("no %s tool", name)
		}
		return tool.Handler(ctx, args)
	}

	// Its own repository and its Reads: answered.
	if res := call("get_ci_log", map[string]any{"run_id": "own", "job": "go"}); res.IsError || !strings.Contains(res.Text, "own log") {
		t.Fatalf("get_ci_log of the task's own run: %s", res.Text)
	}
	if res := call("get_ci_log", map[string]any{"run_id": "read", "job": "go"}); res.IsError || !strings.Contains(res.Text, "shared log") {
		t.Fatalf("get_ci_log of a run in the task's reads: %s", res.Text)
	}

	// Anything else: refused, in a sentence, with nothing of it shown.
	if res := call("get_ci_log", map[string]any{"run_id": "foreign", "job": "go"}); !res.IsError || strings.Contains(res.Text, "SECRET-LOG") {
		t.Errorf("get_ci_log of another repository's run answered: %s", res.Text)
	}
	for _, repo := range []string{"other/secret-app", "OTHER/Secret-App", "acme/widgets-fork"} {
		if res := call("list_ci_runs", map[string]any{"repo": repo}); !res.IsError || strings.Contains(res.Text, "foreign") {
			t.Errorf("list_ci_runs for %s answered: %s", repo, res.Text)
		}
		if res := call("query_test_history", map[string]any{"repo": repo, "test_name": "TestX"}); !res.IsError {
			t.Errorf("query_test_history for %s answered: %s", repo, res.Text)
		}
	}
	// And a list that names no repository is the task's own, never
	// every repository's.
	if res := call("list_ci_runs", map[string]any{}); res.IsError || strings.Contains(res.Text, "secret-app") {
		t.Errorf("list_ci_runs with no repository showed another repository's runs: %s", res.Text)
	}
	runner.mu.Lock()
	for _, f := range runner.finds {
		if strings.EqualFold(f.Repo, "other/secret-app") || f.Repo == "" {
			t.Errorf("the daemon was asked for runs filtered to %q", f.Repo)
		}
	}
	runner.mu.Unlock()
}

// get_ci_artifact writes what it downloads onto disk rather than into
// its own result text, so the boundary that matters here is not just
// "nothing of another repository's artifact appears in the answer" --
// it is also that the tool never asks the daemon to download it at all.
func TestGetCIArtifactRefusesARepositoryOutsideTheTask(t *testing.T) {
	runner := &scopedCI{}
	scope := mcp.CIRepoScope{Repo: "acme/widgets", Reads: []string{"acme/shared-lib"}}
	root := t.TempDir()
	tools := mcp.NewGrainCIArtifactTools(runner, scope, root)
	if len(tools) != 1 || tools[0].Name != "get_ci_artifact" {
		t.Fatalf("NewGrainCIArtifactTools = %v, want exactly get_ci_artifact", tools)
	}
	tool := tools[0]
	ctx := context.Background()

	// Its own repository: answered, and the bytes land in the sandbox.
	if res := tool.Handler(ctx, map[string]any{"run_id": "own", "job": "go", "name": "trace"}); res.IsError {
		t.Fatalf("get_ci_artifact of the task's own run: %s", res.Text)
	}
	if data, err := os.ReadFile(filepath.Join(root, "own-go-trace.tar.gz")); err != nil || string(data) != "own trace bytes" {
		t.Fatalf("own artifact not written as expected: data=%q err=%v", data, err)
	}

	// Another repository's run: refused, before any download, and
	// nothing of its bytes reaches the sandbox under any name.
	if res := tool.Handler(ctx, map[string]any{"run_id": "foreign", "job": "go", "name": "trace"}); !res.IsError || strings.Contains(res.Text, "SECRET-ARTIFACT-BYTES") {
		t.Errorf("get_ci_artifact of another repository's run answered: %s", res.Text)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if data, err := os.ReadFile(filepath.Join(root, entry.Name())); err == nil && strings.Contains(string(data), "SECRET-ARTIFACT-BYTES") {
			t.Errorf("another repository's artifact bytes reached the sandbox as %s", entry.Name())
		}
	}
}

// TestControlRoster is the roster every production run is served
// (docs/design/sandbox-first-agent-surface.md §6): with a sandbox and no
// -mcp-surface, the sandbox tools and the run-ending, parking and
// escalation tools, plus the tools of grants no sandbox process may
// reach -- and nothing the `grain` CLI serves instead.
func TestControlRoster(t *testing.T) {
	control := func(interactive bool, grants ...string) map[string]bool {
		t.Helper()
		args := []string{"mcpserver", "-sandbox-root", t.TempDir()}
		for _, g := range grants {
			args = append(args, "-grant", g)
		}
		args = append(args, "-server", stubDaemon(t, grants), "-task", "1",
			"-pr-repo", "acme/widgets", "-pr-branch", "grain/task-1")
		if interactive {
			args = append(args, "-interactive")
		}
		return listTools(t, args, grants)
	}
	want := []string{"run_command", "read_file", "write_file", "edit_file",
		"ask_question", "request_secret", "comment_on_issue", "nothing_needed",
		"propose_task", "wait_for_task", "resume_later", "recreate_sandbox"}
	got := control(false, "grain-ci", "self-debug", "memory")
	for _, name := range want {
		if !got[name] {
			t.Errorf("the control roster has no %q; got %v", name, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("the control roster is %d tools, want exactly %v; got %v", len(got), want, got)
	}
	for _, name := range []string{"wait_for_checks", "get_ci_log", "pull_request_status", "grain_read",
		"update_status", "write_handover", "update_memory", "read_grain_metrics", "open_pull_request"} {
		if got[name] {
			t.Errorf("the control roster serves %q, which is a `grain` CLI command", name)
		}
	}
	if github := control(false); !github["wait_for_checks"] || !github["pull_request_status"] || !github["base_branch_status"] {
		t.Errorf("a run without grain-ci reads GitHub's checks only through its MCP tools; got %v", github)
	}
	if !control(true, "grain-ci")["chat"] {
		t.Errorf("an interactive run's control roster has no chat")
	}
	if !control(false, "grain-ci", "self-upgrade")["upgrade_deployment"] {
		t.Errorf("self-upgrade's tools are model-only, and missing from the control roster")
	}
}
