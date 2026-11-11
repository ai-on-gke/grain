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
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/namespace"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestGrainMCPStdioToolsCallSharedClientAndSliceWaitForTask verifies
// external-cli.spec.md#XC-31.1 and #XC-31.2: `grain mcp --context <name>`
// serves the exact 13-tool external coordination roster over local stdio
// JSON-RPC 2.0 using the CLI's shared HTTPClient, context credentials, and
// validation paths, and slices `wait_for_task` below MCP client tool timeouts
// returning `{"status": "waiting", "tasks": [...], "resume": true}` until the
// wake condition fires.
func TestGrainMCPStdioToolsCallSharedClientAndSliceWaitForTask(t *testing.T) {
	origLogOut := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(origLogOut) })

	ts, store := loginServer(t)
	ctx := context.Background()

	// Attach namespace handler for /api/fs/ (reusing mqTestDeploy) while verifying
	// that every request from `grain mcp --context mcpctx` carries its gma_ token.
	ns := &namespace.Server{Deploy: mqTestDeploy{}}
	baseHandler := ts.Config.Handler
	ts.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/fs/") {
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer gma_") {
				http.Error(w, "missing context bearer token", http.StatusUnauthorized)
				return
			}
			in := r.Clone(r.Context())
			in.URL.Path = "/fs/" + strings.TrimPrefix(r.URL.Path, "/api/fs/")
			ns.ServeHTTP(w, in, namespace.Scope{
				MayReadCI:      true,
				AllowedRepos:   map[string]bool{"acme/widgets": true},
				MergeQueueByCI: true,
			})
			return
		}
		baseHandler.ServeHTTP(w, r)
	})

	if err := store.SetTargetRepos(ctx, []string{"acme/widgets"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCustomField(ctx, model.CustomField{
		Name:    "component",
		Type:    model.CustomFieldEnum,
		Options: []string{"api", "cli", "ui"},
	}); err != nil {
		t.Fatal(err)
	}

	// Mint a named context ("mcpctx") and switch current away from it so we
	// verify `grain mcp --context mcpctx` resolves the explicit context flag.
	_ = captureStdout(t, func() {
		if err := runCLI([]string{
			"login", ts.URL,
			"--name", "mcpctx",
			"--auth", "test-sso",
			"--issue-token",
			"--expires", "7d",
		}); err != nil {
			t.Fatalf("grain login --issue-token: %v", err)
		}
	})
	cfg, err := loadContexts()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Contexts["broken-default"] = &cliContext{
		Server:   "http://127.0.0.1:1",
		Auth:     "none",
		Audience: "https://wrong-default-audience.example",
	}
	cfg.Current = "broken-default"
	if err := saveContexts(cfg); err != nil {
		t.Fatal(err)
	}

	workspaceDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspaceDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "note.txt"), []byte("workspace log excerpt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, ".env"), []byte("SECRET_KEY=topsecret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(t.TempDir(), "host-secret.txt")
	if err := os.WriteFile(outsideFile, []byte("HOST_SECRET_CONTENT\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(workspaceDir, "escape-link.txt")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workspaceDir)

	origDefaultSlice := defaultMCPWaitSlice
	origMaxSlice := maxMCPWaitSlice
	origWaitSlice := waitSlice
	defaultMCPWaitSlice = 120 * time.Millisecond
	maxMCPWaitSlice = 120 * time.Millisecond
	waitSlice = 80 * time.Millisecond
	t.Cleanup(func() {
		defaultMCPWaitSlice = origDefaultSlice
		maxMCPWaitSlice = origMaxSlice
		waitSlice = origWaitSlice
	})

	// Drive resolveMCPClient + serveExternalMCP over real stdio pipes (testing
	// `--context mcpctx` resolution, audience isolation from broken-default, and
	// JSON-RPC 2.0 stdio framing).
	badClient := ui.NewHTTPClient("http://127.0.0.1:1")
	badClient.Audience = "https://wrong-default-audience.example"
	realClient, err := resolveMCPClient(badClient, nilFlagSet(), selectedContext{
		name: "broken-default",
		ctx:  cfg.Contexts["broken-default"],
	}, "mcpctx")
	if err != nil {
		t.Fatalf("resolveMCPClient: %v", err)
	}
	if realClient.Audience != "" {
		t.Fatalf("resolveMCPClient leaked default context audience %q onto mcpctx", realClient.Audience)
	}

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srvCtx, cancelSrv := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		err := serveExternalMCP(srvCtx, realClient, inR, outW)
		_ = outW.Close()
		done <- err
	}()
	t.Cleanup(func() {
		cancelSrv()
		_ = inW.Close()
		_ = outR.Close()
		<-done
	})

	client := mcp.NewClient(inW, outR, inW)
	t.Cleanup(func() { _ = client.Close() })

	// 1. Verify exact 13-tool roster (and absence of force-merge or generated api_* tools).
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	gotNames := make([]string, 0, len(tools))
	for _, tl := range tools {
		gotNames = append(gotNames, tl.Name)
		if strings.HasPrefix(tl.Name, "api_") || strings.Contains(tl.Name, "force") {
			t.Fatalf("forbidden tool advertised on external grain mcp: %q", tl.Name)
		}
	}
	slices.Sort(gotNames)
	wantNames := []string{
		"adopt_pull_request",
		"ci_log",
		"ci_runs",
		"comment_on_task",
		"fs_grep",
		"fs_read",
		"grain_inbox",
		"list_grain_tasks",
		"merge_queue",
		"propose_task",
		"read_grain_task",
		"reply_to_task",
		"wait_for_task",
	}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("tools/list = %v, want %v", gotNames, wantNames)
	}

	// 2. Shared custom-field validation rejects undeclared enum option before filing.
	badCreate, err := client.CallTool(ctx, "propose_task", map[string]any{
		"title":         "Bad custom field task",
		"repo":          "acme/widgets",
		"custom_fields": map[string]any{"component": "unknown-option"},
	})
	if err != nil {
		t.Fatalf("CallTool propose_task (invalid): %v", err)
	}
	if !badCreate.IsError {
		t.Fatalf("expected validation error for invalid custom_fields option, got: %s", badCreate.Text())
	}

	// 3. propose_task creates a proposed task using the context's external-dev credential.
	createdRes, err := client.CallTool(ctx, "propose_task", map[string]any{
		"title":         "Implement MCP stdio test coverage",
		"body":          "Verify local stdio grain mcp tools against live store.",
		"repo":          "acme/widgets",
		"custom_fields": map[string]any{"component": "cli"},
	})
	if err != nil {
		t.Fatalf("CallTool propose_task: %v", err)
	}
	if createdRes.IsError {
		t.Fatalf("propose_task returned error: %s", createdRes.Text())
	}
	var createdTask ui.Task
	if err := json.Unmarshal([]byte(createdRes.Text()), &createdTask); err != nil {
		t.Fatalf("unmarshal propose_task output (%q): %v", createdRes.Text(), err)
	}
	if createdTask.ID == "" || createdTask.State != model.StateProposed || createdTask.Owner != "jane@example.com" {
		t.Fatalf("unexpected proposed task: %+v", createdTask)
	}

	// 4. comment_on_task (including workspace attachment confinement) and read_grain_task.
	commentRes, err := client.CallTool(ctx, "comment_on_task", map[string]any{
		"id":          createdTask.ID,
		"body":        "Tracking note from workstation MCP client.",
		"attachments": []string{"note.txt"},
	})
	if err != nil || commentRes.IsError {
		t.Fatalf("comment_on_task failed: err=%v res=%v", err, commentRes)
	}

	cfgDir, err := grainConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	credFile := filepath.Join(cfgDir, "credentials", "mcpctx")
	for _, badAttach := range []struct {
		name string
		path string
	}{
		{"outside-workspace", outsideFile},
		{"symlink-escaping-workspace", "escape-link.txt"},
		{"dotenv-secret", ".env"},
		{"grain-credential-store", credFile},
	} {
		badAttachRes, err := client.CallTool(ctx, "comment_on_task", map[string]any{
			"id":          createdTask.ID,
			"body":        "Should fail attachment confinement",
			"attachments": []string{badAttach.path},
		})
		if err != nil || !badAttachRes.IsError {
			t.Fatalf("comment_on_task(%s) should have refused attachment %q, got err=%v res=%+v", badAttach.name, badAttach.path, err, badAttachRes)
		}
	}

	readRes, err := client.CallTool(ctx, "read_grain_task", map[string]any{
		"id": "#" + createdTask.ID,
	})
	if err != nil || readRes.IsError {
		t.Fatalf("read_grain_task failed: err=%v text=%s", err, readRes.Text())
	}
	if !strings.Contains(readRes.Text(), "Tracking note from workstation MCP client.") {
		t.Errorf("read_grain_task missing posted comment:\n%s", readRes.Text())
	}

	// 5. list_grain_tasks and grain_inbox.
	listRes, err := client.CallTool(ctx, "list_grain_tasks", map[string]any{
		"repo": "acme/widgets",
	})
	if err != nil || listRes.IsError {
		t.Fatalf("list_grain_tasks failed: err=%v text=%s", err, listRes.Text())
	}
	var listed struct {
		Tasks     []ui.Task `json:"tasks"`
		Total     int       `json:"total"`
		Truncated bool      `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(listRes.Text()), &listed); err != nil {
		t.Fatalf("unmarshal list_grain_tasks: %v", err)
	}
	if len(listed.Tasks) != 1 || listed.Tasks[0].ID != createdTask.ID || listed.Total != 1 || listed.Truncated {
		t.Fatalf("list_grain_tasks = %+v, want [%s]", listed, createdTask.ID)
	}

	inboxRes, err := client.CallTool(ctx, "grain_inbox", map[string]any{
		"repo": "acme/widgets",
		"kind": "proposal",
	})
	if err != nil || inboxRes.IsError {
		t.Fatalf("grain_inbox failed: err=%v text=%s", err, inboxRes.Text())
	}
	if !strings.Contains(inboxRes.Text(), createdTask.Title) {
		t.Errorf("grain_inbox missing proposal %q:\n%s", createdTask.Title, inboxRes.Text())
	}

	// 6. Namespace (fs_read, fs_grep), merge_queue, CI, and adopt_pull_request validation.
	mqRes, err := client.CallTool(ctx, "merge_queue", map[string]any{"repo": "acme/widgets"})
	if err != nil || mqRes.IsError {
		t.Fatalf("merge_queue failed: err=%v text=%s", err, mqRes.Text())
	}
	if !strings.Contains(mqRes.Text(), "Fix widgets") || strings.Contains(mqRes.Text(), "acme/secret") {
		t.Errorf("merge_queue unexpected output:\n%s", mqRes.Text())
	}
	fsListRes, err := client.CallTool(ctx, "fs_read", map[string]any{"uri": "deploy://", "list": true})
	if err != nil || fsListRes.IsError {
		t.Fatalf("fs_read (list deploy://) failed: err=%v text=%s", err, fsListRes.Text())
	}
	fsGrepRes, err := client.CallTool(ctx, "fs_grep", map[string]any{
		"pattern": "Fix widgets",
		"uri":     "deploy://merge-queue",
	})
	if err != nil || fsGrepRes.IsError {
		t.Fatalf("fs_grep failed: err=%v text=%s", err, fsGrepRes.Text())
	}
	if !strings.Contains(fsGrepRes.Text(), "Fix widgets") {
		t.Errorf("fs_grep missing match:\n%s", fsGrepRes.Text())
	}
	badCIRuns, err := client.CallTool(ctx, "ci_runs", map[string]any{"conclusion": "not-a-valid-conclusion"})
	if err != nil || !badCIRuns.IsError {
		t.Fatalf("expected ci_runs to reject invalid conclusion via shared CLI filter, got err=%v res=%+v", err, badCIRuns)
	}
	badCILog, err := client.CallTool(ctx, "ci_log", map[string]any{})
	if err != nil || !badCILog.IsError {
		t.Fatalf("expected ci_log to require run_id, got err=%v res=%+v", err, badCILog)
	}
	for _, badCIArgs := range []map[string]any{
		{"repo": "acme/widgets", "run_id": "../other/1"},
		{"repo": "acme/widgets", "run_id": "run-1", "job": "build?raw=1"},
		{"repo": "not-a-valid-repo-slug", "run_id": "run-1"},
	} {
		res, err := client.CallTool(ctx, "ci_log", badCIArgs)
		if err != nil || !res.IsError {
			t.Fatalf("expected ci_log(%v) to reject unsafe URI segment, got err=%v res=%+v", badCIArgs, err, res)
		}
	}
	badAdopt, err := client.CallTool(ctx, "adopt_pull_request", map[string]any{"pr": "not-a-valid-pr"})
	if err != nil || !badAdopt.IsError {
		t.Fatalf("expected adopt_pull_request to reject malformed PR ref, got err=%v res=%+v", err, badAdopt)
	}

	// 7. reply_to_task answers a parked question and re-queues the task.
	humanHTTP := ui.NewHTTPClient(ts.URL)
	humanHTTP.Token = "sso:jane@example.com"
	if err := humanHTTP.Approve(ctx, createdTask.ID); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	qid, err := store.AddComment(ctx, model.Comment{
		TaskID: createdTask.ID,
		Body:   "Should config output default to JSON or YAML?",
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "run"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: createdTask.ID, PendingQuestionCommentID: &qid}); err != nil {
		t.Fatal(err)
	}

	replyRes, err := client.CallTool(ctx, "reply_to_task", map[string]any{
		"task_id": createdTask.ID,
		"body":    "Use JSON.",
	})
	if err != nil || replyRes.IsError {
		t.Fatalf("reply_to_task failed: err=%v text=%s", err, replyRes.Text())
	}
	var afterReply ui.Task
	if err := json.Unmarshal([]byte(replyRes.Text()), &afterReply); err != nil {
		t.Fatal(err)
	}
	if afterReply.State != model.StateQueued {
		t.Fatalf("after reply_to_task, state = %s, want queued", afterReply.State)
	}

	// 8. wait_for_task slices below standard MCP client tool timeouts (#XC-31.2):
	// while the task is still queued, even a 300s requested timeout returns
	// promptly with status="waiting" and resume=true.
	start := time.Now()
	waitSliceRes, err := client.CallTool(ctx, "wait_for_task", map[string]any{
		"task_id":         createdTask.ID,
		"until":           "closed",
		"timeout_seconds": 300,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("wait_for_task (unfinished): %v", err)
	}
	if waitSliceRes.IsError {
		t.Fatalf("wait_for_task slice should not set IsError on timeout slice: %s", waitSliceRes.Text())
	}
	if elapsed > 3*time.Second {
		t.Fatalf("wait_for_task did not slice below client timeout; took %v", elapsed)
	}
	var slicePayload struct {
		Status   string `json:"status"`
		Met      bool   `json:"met"`
		Resume   bool   `json:"resume"`
		Revision string `json:"revision"`
		Tasks    []any  `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(waitSliceRes.Text()), &slicePayload); err != nil {
		t.Fatalf("unmarshal wait_for_task slice (%q): %v", waitSliceRes.Text(), err)
	}
	if slicePayload.Status != "waiting" || slicePayload.Met || !slicePayload.Resume || len(slicePayload.Tasks) != 1 || slicePayload.Revision == "" {
		t.Fatalf("unexpected sliced wait_for_task payload: %+v", slicePayload)
	}

	// Close the task and resume waiting; wait_for_task now reports
	// status="met", met=true, resume=false.
	if err := humanHTTP.Close(ctx, createdTask.ID, ui.CloseOptions{}); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitMetRes, err := client.CallTool(ctx, "wait_for_task", map[string]any{
		"task_id": createdTask.ID,
		"until":   "closed",
		"since":   slicePayload.Revision,
	})
	if err != nil || waitMetRes.IsError {
		t.Fatalf("wait_for_task (closed): err=%v text=%s", err, waitMetRes.Text())
	}
	var metPayload struct {
		Status    string `json:"status"`
		Met       bool   `json:"met"`
		Condition string `json:"condition"`
		Resume    bool   `json:"resume"`
	}
	if err := json.Unmarshal([]byte(waitMetRes.Text()), &metPayload); err != nil {
		t.Fatalf("unmarshal met wait_for_task (%q): %v", waitMetRes.Text(), err)
	}
	if metPayload.Status != "met" || !metPayload.Met || metPayload.Condition != "closed" || metPayload.Resume {
		t.Fatalf("unexpected completed wait_for_task payload: %+v", metPayload)
	}
}

func nilFlagSet() *flag.FlagSet {
	return flag.NewFlagSet("grain", flag.ContinueOnError)
}

// A stdio MCP client ends a session by closing the server's stdin; that is
// a clean exit for `grain mcp`, not an error it prints and exits 1 on
// (docs/test-runs/2026-11-08-cli-install-skill-mcp.md).
func TestServeExternalMCPTreatsClosedStdinAsCleanExit(t *testing.T) {
	c := ui.NewHTTPClient("http://127.0.0.1:1")
	for name, in := range map[string]string{
		"empty session":          "",
		"after a request":        `{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n",
		"unterminated last line": `{"jsonrpc":"2.0","id":1,"method":"ping"}`,
	} {
		t.Run(name, func(t *testing.T) {
			var out strings.Builder
			if err := serveExternalMCP(context.Background(), c, strings.NewReader(in), &out); err != nil {
				t.Fatalf("serveExternalMCP at end of input = %v, want nil", err)
			}
			if in != "" && !strings.Contains(out.String(), `"id":1`) {
				t.Fatalf("ping was not answered before EOF: %q", out.String())
			}
		})
	}
}

// ci_log builds ci://<repo>/<run>/<job>/log from its arguments; a segment
// that could step out of that path, even percent-encoded, is refused
// before anything is sent.
func TestIsSafeCIURISegmentRefusesTraversal(t *testing.T) {
	for _, ok := range []string{"91190", "gha:acme:widgets:91190", "test", "go-unit_1.2"} {
		if !isSafeCIURISegment(ok) {
			t.Errorf("isSafeCIURISegment(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", ".", "..", "../x", "a/b", `a\b`, "a?b", "a#b", "%2e%2e%2fsecret", "%2e%2e", "x%2fy", "a\nb", "a\x00b"} {
		if isSafeCIURISegment(bad) {
			t.Errorf("isSafeCIURISegment(%q) = true, want false", bad)
		}
	}
}
