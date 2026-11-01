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

package sandboxcli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeActionProxy answers the acting /cli/ routes with canned replies and
// keeps the bodies it was sent.
type fakeActionProxy struct {
	mu      sync.Mutex
	bodies  map[string]map[string]any
	headers map[string]http.Header
	answers map[string]string
}

func (f *fakeActionProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	body := map[string]any{}
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	f.bodies[r.Method+" "+r.URL.Path] = body
	if f.headers != nil {
		f.headers[r.Method+" "+r.URL.Path] = r.Header.Clone()
	}
	answer, ok := f.answers[r.URL.Path]
	f.mu.Unlock()
	if !ok {
		http.Error(w, "no such route", http.StatusNotFound)
		return
	}
	_, _ = io.WriteString(w, answer)
}

func runActionCLI(t *testing.T, srv *httptest.Server, vars map[string]string, args ...string) (string, string, int) {
	t.Helper()
	env := map[string]string{"GRAIN_SANDBOX_PROXY": srv.URL, "GRAIN_SANDBOX_TOKEN": "tok"}
	for k, v := range vars {
		env[k] = v
	}
	var out, errOut bytes.Buffer
	code, handled := maybeRunSandboxCLI(args, fakeSandboxEnv(env, ""), &out, &errOut)
	if !handled {
		t.Fatalf("%q not handled in sandbox mode", args)
	}
	return out.String(), errOut.String(), code
}

func TestSandboxCIWaitReportsEachVerdict(t *testing.T) {
	proxy := &fakeActionProxy{bodies: map[string]map[string]any{}, answers: map[string]string{}}
	srv := httptest.NewServer(proxy)
	defer srv.Close()
	set := func(path, answer string) {
		proxy.mu.Lock()
		proxy.answers[path] = answer
		proxy.mu.Unlock()
	}

	set("/cli/ci/wait", `{"status":"running","run_id":"ci-1942","resume_run_id":"ci-1942","waited_ms":540000,
		"credit_used_ms":540000,"credit_cap_ms":2700000,"credit_live":true,
		"jobs":[{"name":"go","conclusion":"success"},{"name":"ui-e2e","conclusion":"running"}]}`)
	out, _, code := runActionCLI(t, srv, map[string]string{sandboxCommandTimeoutEnv: "300000"},
		"ci", "wait", "--jobs", "go,ui-e2e", "--timeout", "9m")
	if code != 0 || !strings.Contains(out, "CI run ci-1942 is still running after 9m0s (jobs running: ui-e2e; passed: go; wait credit used: 9m0s / 45m0s)") ||
		!strings.Contains(out, "grain ci wait --resume ci-1942") || !strings.Contains(out, `when: {"ci": "ci-1942"}`) {
		t.Errorf("running: %d\n%s", code, out)
	}
	body := proxy.bodies["POST /cli/ci/wait"]
	if body["timeout_seconds"] != float64(270) {
		t.Errorf("timeout_seconds = %v, want 270 (run_command's 300s less the margin)", body["timeout_seconds"])
	}
	if jobs, _ := body["jobs"].([]any); len(jobs) != 2 {
		t.Errorf("jobs = %v", body["jobs"])
	}

	set("/cli/ci/wait", `{"status":"failed","run_id":"ci-2","commit":"0123456789abcdef","conclusion":"failure",
		"jobs":[{"name":"go","conclusion":"failure"},{"name":"lint","conclusion":"success"}]}`)
	out, _, code = runActionCLI(t, srv, nil, "ci", "wait", "--resume", "ci-2")
	if code != 1 || !strings.Contains(out, "Failing: go (failure)") || !strings.Contains(out, "grain ci log ci-2 --job <job>") {
		t.Errorf("failed: %d\n%s", code, out)
	}
	if proxy.bodies["POST /cli/ci/wait"]["resume_run_id"] != "ci-2" {
		t.Errorf("resume not sent: %v", proxy.bodies["POST /cli/ci/wait"])
	}

	set("/cli/ci/run-job", `{"status":"passed","run_id":"ci-3","selective":true,"jobs":[{"name":"go","conclusion":"success"}]}`)
	out, _, code = runActionCLI(t, srv, nil, "ci", "run-job", "go")
	if code != 0 || !strings.Contains(out, "grain CI passed") || !strings.Contains(out, "never satisfies the full-suite gate") {
		t.Errorf("run-job: %d\n%s", code, out)
	}
	if proxy.bodies["POST /cli/ci/run-job"]["job"] != "go" {
		t.Errorf("run-job body = %v", proxy.bodies["POST /cli/ci/run-job"])
	}

	set("/cli/ci/retrigger", "Retriggered\n")
	if out, _, code := runActionCLI(t, srv, nil, "ci", "retrigger", "--all"); code != 0 || out != "Retriggered\n" ||
		proxy.bodies["POST /cli/ci/retrigger"]["all"] != true {
		t.Errorf("retrigger: %d %q %v", code, out, proxy.bodies["POST /cli/ci/retrigger"])
	}

	set("/cli/pr/reply", "Replied.\n")
	if _, _, code := runActionCLI(t, srv, nil, "pr", "reply", "thanks,", "fixed", "--comment-id", "5"); code != 0 ||
		proxy.bodies["POST /cli/pr/reply"]["body"] != "thanks, fixed" || proxy.bodies["POST /cli/pr/reply"]["comment_id"] != float64(5) {
		t.Errorf("reply: %d %v", code, proxy.bodies["POST /cli/pr/reply"])
	}
	if _, errOut, code := runActionCLI(t, srv, nil, "pr", "review-comment", "x", "--path", "a.go"); code != 1 || !strings.Contains(errOut, "--line") {
		t.Errorf("review-comment without --line: %d %s", code, errOut)
	}
	if _, errOut, code := runActionCLI(t, srv, nil, "pr", "resolve"); code != 1 || !strings.Contains(errOut, "--thread-id") {
		t.Errorf("resolve naming nothing: %d %s", code, errOut)
	}
}

func TestParseWaitTimeout(t *testing.T) {
	for _, tc := range []struct {
		flag, env string
		want      time.Duration
	}{
		{"", "", 540 * time.Second},
		{"20m", "", 540 * time.Second},
		{"120", "", 2 * time.Minute},
		{"5m", "300000", 270 * time.Second},
		{"", "600000", 540 * time.Second},
	} {
		got, err := parseWaitTimeout(tc.flag, func(string) string { return tc.env })
		if err != nil || got != tc.want {
			t.Errorf("parseWaitTimeout(%q, env %q) = %s, %v; want %s", tc.flag, tc.env, got, err, tc.want)
		}
	}
	if _, err := parseWaitTimeout("soon", nil); err == nil {
		t.Error("a nonsense --timeout was accepted")
	}
}

func revertGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// grain git revert finds a task's merge by the branch grain recorded for
// it and reverts it in the sandbox's own tree, with nothing on the host.
func TestSandboxGitRevertRevertsATaskMergeLocally(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	revertGit(t, dir, "init", "-q", "-b", "main")
	writeFile := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("a.txt", "one\n")
	revertGit(t, dir, "add", ".")
	revertGit(t, dir, "commit", "-q", "-m", "base")
	revertGit(t, dir, "checkout", "-q", "-b", "grain/abc/task-42")
	writeFile("b.txt", "two\n")
	revertGit(t, dir, "add", ".")
	revertGit(t, dir, "commit", "-q", "-m", "feat: add b")
	revertGit(t, dir, "checkout", "-q", "main")
	// A later commit that only mentions the task by number must not be
	// taken for its merge.
	revertGit(t, dir, "merge", "-q", "--no-ff", "-m", "Merge pull request #9 from o/grain/abc/task-42", "grain/abc/task-42")
	merge := revertGit(t, dir, "rev-parse", "HEAD")
	writeFile("c.txt", "three\n")
	revertGit(t, dir, "add", ".")
	revertGit(t, dir, "commit", "-q", "-m", "chore: follow-up to task 42")

	proxy := &fakeActionProxy{bodies: map[string]map[string]any{}, answers: map[string]string{
		"/cli/tasks/42/revert-info": `{"task":"42","branch":"grain/abc/task-42"}`,
	}}
	srv := httptest.NewServer(proxy)
	defer srv.Close()

	if _, errOut, code := runActionCLI(t, srv, nil, "git", "revert", "--task", "42", "-C", dir); code != 1 || !strings.Contains(errOut, "--reason") {
		t.Errorf("revert without --reason: %d %s", code, errOut)
	}
	out, errOut, code := runActionCLI(t, srv, nil, "git", "revert", "--task", "42", "--reason", "broke main", "-C", dir)
	if code != 0 {
		t.Fatalf("revert: %d %s %s", code, out, errOut)
	}
	if !strings.Contains(out, merge) {
		t.Errorf("reverted the wrong commit: %s (want %s)", out, merge)
	}
	msg := revertGit(t, dir, "log", "-n", "1", "--format=%B")
	if !strings.Contains(msg, "revert: task 42") || !strings.Contains(msg, "Reverts-Commit: "+merge) || !strings.Contains(msg, "broke main") {
		t.Errorf("revert commit message = %q", msg)
	}
	if files := revertGit(t, dir, "ls-files"); strings.Contains(files, "b.txt") || !strings.Contains(files, "c.txt") {
		t.Errorf("tree after revert = %q, want b.txt gone and c.txt kept", files)
	}

	if _, errOut, code := runActionCLI(t, srv, nil, "git", "revert", "--task", "43", "--reason", "x", "-C", dir); code != 1 || !strings.Contains(errOut, "no such route") {
		t.Errorf("revert of a task the proxy does not know: %d %s", code, errOut)
	}
}

func TestStage3SandboxCLICommands(t *testing.T) {
	proxy := &fakeActionProxy{bodies: map[string]map[string]any{}, answers: map[string]string{
		"/cli/status":              "Updated status: checking tests\n",
		"/cli/handover":            "Recorded standing handover summary (12 bytes).\n",
		"/cli/notify":              `{"id":4,"subject":"flake","severity":"warning","count":1,"new":true}`,
		"/cli/tasks/9/notes":       "Left a note on task #9 (comment 12).\n",
		"/cli/tasks/wait":          `{"status":"settled","until":"closed","summary":"[reached] task #9 (completed)\n","waited_ms":2000,"credit_used_ms":2000,"credit_cap_ms":2700000,"credit_live":true}`,
		"/cli/proposals":           `{"id":"101","url":"/tasks/101","hold_for_approval":true,"auto_merge":false,"held":true}`,
		"/cli/api-routes":          `[{"method":"GET","path":"/api/tasks","scope":"tasks:read","tool":"api_list_tasks"}]`,
		"/api/tasks/9":             `{"id":"9","state":"completed"}`,
		"/api/memory":              `{"text":"remember this"}`,
		"/api/memory/documents":    `{"name":"arch","summary":"Arch","length":10}`,
		"/api/release-notes/stats": `{"period":"2026-W43","pullRequestsMerged":5}`,
		"/api/release-notes":       `{"period":"2026-W43","title":"v1.2","length":12}`,
		"/cli/quarantine":          `{"task_id":"7","follow_up_task_id":"102"}`,
	}}
	srv := httptest.NewServer(proxy)
	defer srv.Close()

	// 1. grain help propose & grain task propose --help
	for _, args := range [][]string{{"help", "propose"}, {"task", "propose", "--help"}} {
		out, errOut, code := runActionCLI(t, srv, nil, args...)
		if code != 0 || !strings.Contains(out, "SURFACE & APPROVAL") || !strings.Contains(out, "--supersedes") {
			t.Errorf("%v: code=%d out=%q err=%q", args, code, out, errOut)
		}
	}

	// 2. status, handover write, notify, task message
	if out, errOut, code := runActionCLI(t, srv, nil, "status", "checking", "tests"); code != 0 || !strings.Contains(out, "Updated status") {
		t.Errorf("status: %d %s %s", code, out, errOut)
	}
	if out, errOut, code := runActionCLI(t, srv, nil, "handover", "write", "settled summary"); code != 0 || !strings.Contains(out, "Recorded standing handover") {
		t.Errorf("handover write: %d %s %s", code, out, errOut)
	}
	if out, errOut, code := runActionCLI(t, srv, nil, "notify", "--subject", "flake", "--title", "Flake seen", "--body", "details"); code != 0 || !strings.Contains(out, `Raised warning notification "flake"`) {
		t.Errorf("notify: %d %s %s", code, out, errOut)
	}
	if out, errOut, code := runActionCLI(t, srv, nil, "task", "message", "9", "hello from parent"); code != 0 || !strings.Contains(out, "Left a note on task #9") {
		t.Errorf("task message: %d %s %s", code, out, errOut)
	}

	// 3. task wait (blocking allowed; --park refused)
	if out, errOut, code := runActionCLI(t, srv, nil, "task", "wait", "9", "--until", "closed"); code != 0 || !strings.Contains(out, "[reached] task #9") {
		t.Errorf("task wait: %d %s %s", code, out, errOut)
	}
	if _, errOut, code := runActionCLI(t, srv, nil, "task", "wait", "9", "--park"); code != 1 || !strings.Contains(errOut, "wait_for_task") {
		t.Errorf("task wait --park: %d %s", code, errOut)
	}

	// 4. task propose (draft allowed; --priority forwarded; --supersedes and --auto-merge refused)
	if out, errOut, code := runActionCLI(t, srv, nil, "task", "propose", "--title", "Follow-up", "--body", "Do x", "--depends-on", "7,8", "--priority", "8"); code != 0 ||
		!strings.Contains(out, "Proposed task #101") || !strings.Contains(out, "held for human approval") {
		t.Errorf("task propose: %d %s %s", code, out, errOut)
	}
	if gotPri, ok := proxy.bodies["POST /cli/proposals"]["priority"].(float64); !ok || gotPri != 8 {
		t.Errorf("task propose priority in body = %v, want 8", proxy.bodies["POST /cli/proposals"]["priority"])
	}
	if _, errOut, code := runActionCLI(t, srv, nil, "task", "propose", "--title", "t", "--body", "b", "--priority", "-1"); code != 1 ||
		!strings.Contains(errOut, "--priority") {
		t.Errorf("task propose --priority -1: %d %s", code, errOut)
	}
	if _, errOut, code := runActionCLI(t, srv, nil, "task", "propose", "--title", "t", "--body", "b", "--supersedes", "5"); code != 1 ||
		!strings.Contains(errOut, "propose_task") {
		t.Errorf("task propose --supersedes: %d %s", code, errOut)
	}
	if _, errOut, code := runActionCLI(t, srv, nil, "task", "propose", "--title", "t", "--body", "b", "--auto-merge"); code != 1 ||
		!strings.Contains(errOut, "propose_task") {
		t.Errorf("task propose --auto-merge: %d %s", code, errOut)
	}

	// 5. grain api routes, grain api GET, & grain api DELETE --reason
	proxy.headers = map[string]http.Header{}
	if out, errOut, code := runActionCLI(t, srv, nil, "api", "routes"); code != 0 || !strings.Contains(out, "/api/tasks") {
		t.Errorf("api routes: %d %s %s", code, out, errOut)
	}
	if out, errOut, code := runActionCLI(t, srv, nil, "api", "GET", "/api/tasks/9"); code != 0 || !strings.Contains(out, `"completed"`) {
		t.Errorf("api GET: %d %s %s", code, out, errOut)
	}
	if out, errOut, code := runActionCLI(t, srv, nil, "api", "DELETE", "/api/tasks/9", "--reason", "cleanup"); code != 0 || !strings.Contains(out, `"completed"`) {
		t.Errorf("api DELETE: %d %s %s", code, out, errOut)
	}
	if got := proxy.headers["DELETE /api/tasks/9"].Get("X-Grain-Acting-Reason"); got != "cleanup" {
		t.Errorf("DELETE /api/tasks/9 X-Grain-Acting-Reason = %q, want cleanup", got)
	}

	// 6. memory write, memory doc-write (and memory doc write), release-notes, quarantine
	if out, errOut, code := runActionCLI(t, srv, nil, "memory", "write", "remember this"); code != 0 || !strings.Contains(out, "remember this") {
		t.Errorf("memory write: %d %s %s", code, out, errOut)
	}
	// Memory has no keys: the form the old help advertised ("write <key>
	// <body>") must be refused rather than replace the deployment's whole
	// memory with "<key> <body>".
	delete(proxy.bodies, "PUT /api/memory")
	if _, errOut, code := runActionCLI(t, srv, nil, "memory", "write", "walk-key", "a body"); code == 0 || !strings.Contains(errOut, "takes no <key>") {
		t.Errorf("memory write <key> <body>: %d %s, want a refusal", code, errOut)
	}
	if body, sent := proxy.bodies["PUT /api/memory"]; sent {
		t.Errorf("memory write <key> <body> reached PUT /api/memory with %v", body)
	}
	if _, errOut, code := runActionCLI(t, srv, nil, "memory", "write", "acme/widgets", "repo memory"); code != 0 ||
		proxy.bodies["PUT /api/memory"]["repo"] != "acme/widgets" || proxy.bodies["PUT /api/memory"]["text"] != "repo memory" {
		t.Errorf("memory write <repo> <text>: %d %s %v", code, errOut, proxy.bodies["PUT /api/memory"])
	}
	if out, errOut, code := runActionCLI(t, srv, nil, "memory", "doc", "write", "arch", "doc body", "--summary", "Arch"); code != 0 || !strings.Contains(out, `"arch"`) {
		t.Errorf("memory doc write: %d %s %s", code, out, errOut)
	}
	if out, errOut, code := runActionCLI(t, srv, nil, "release-notes", "stats", "2026-W43"); code != 0 || !strings.Contains(out, "2026-W43") {
		t.Errorf("release-notes stats: %d %s %s", code, out, errOut)
	}
	if out, errOut, code := runActionCLI(t, srv, nil, "release-notes", "publish", "--version", "2026-W43", "--title", "v1.2", "shipped x"); code != 0 || !strings.Contains(out, "v1.2") {
		t.Errorf("release-notes publish: %d %s %s", code, out, errOut)
	}
	qDir := t.TempDir()
	if out, errOut, code := runActionCLI(t, srv, nil, "quarantine", "pkg/foo", "TestFlaky", "--reason", "timing flake", "-C", qDir); code != 0 ||
		!strings.Contains(out, `Quarantined test "pkg/foo.TestFlaky"`) || !strings.Contains(out, "#102") {
		t.Errorf("quarantine: %d %s %s", code, out, errOut)
	}
	qRaw, err := os.ReadFile(filepath.Join(qDir, ".grain", "quarantine.yml"))
	if err != nil || !strings.Contains(string(qRaw), "pkg/foo.TestFlaky") || !strings.Contains(string(qRaw), "102") {
		t.Errorf("quarantine.yml = %q, %v", qRaw, err)
	}
}

func TestSandboxActionsUseGitCredentialsWithoutProxyEnv(t *testing.T) {
	proxy := &fakeActionProxy{
		bodies:  map[string]map[string]any{},
		headers: map[string]http.Header{},
		answers: map[string]string{
			"/cli/ci/wait": `{"status":"passed","run_id":"ci-gw","commit":"abcdef0123456789","conclusion":"success",
				"jobs":[{"name":"go","conclusion":"success"}]}`,
			"/cli/pr/open": "Opened pull request #42: https://github.com/ai-on-gke/grain/pull/42\n",
		},
	}
	srv := httptest.NewServer(proxy)
	defer srv.Close()

	// Point .git-credentials at the listener without setting GRAIN_SANDBOX_PROXY
	// or GRAIN_SANDBOX_TOKEN, matching a Granule VM or host sandbox session.
	creds := "http://sandbox:tok-from-creds@" + strings.TrimPrefix(srv.URL, "http://") + "\n"
	env := fakeSandboxEnv(map[string]string{}, creds)

	var out, errOut bytes.Buffer
	code, handled := maybeRunSandboxCLI([]string{"ci", "wait"}, env, &out, &errOut)
	if !handled || code != 0 || !strings.Contains(out.String(), "ci-gw") {
		t.Fatalf("ci wait via .git-credentials: handled=%v code=%d out=%q err=%q", handled, code, out.String(), errOut.String())
	}

	out.Reset()
	errOut.Reset()
	code, handled = maybeRunSandboxCLI([]string{"pr", "open"}, env, &out, &errOut)
	if !handled || code != 0 || !strings.Contains(out.String(), "#42") {
		t.Fatalf("pr open via .git-credentials: handled=%v code=%d out=%q err=%q", handled, code, out.String(), errOut.String())
	}
}

func TestSandboxAPIInputModesAndCIArtifactDownload(t *testing.T) {
	proxy := &fakeActionProxy{
		bodies:  map[string]map[string]any{},
		headers: map[string]http.Header{},
		answers: map[string]string{
			"/cli/api-routes": `[{"method":"DELETE","path":"/api/tasks/{id}","scope":"tasks:write","needs_reason":true}]`,
			"/api/tasks/9":    `{"ok":true}`,
			"/cli/context":    `{"repo":"ai-on-gke/grain","branch":"grain/task-7"}`,
			"/cli/ci/runs":    "ID\nrun-42 grain/task-7\n",
			"/fs/ci/ai-on-gke/grain/run-42/artifacts/go/cover.out": "mode: atomic\n",
		},
	}
	srv := httptest.NewServer(proxy)
	defer srv.Close()

	// 1. grain api routes --scope & --json
	out, errOut, code := runActionCLI(t, srv, nil, "api", "routes", "--scope", "tasks:write")
	if code != 0 || !strings.Contains(out, "[--reason required]") {
		t.Errorf("api routes --scope: code=%d out=%q err=%q", code, out, errOut)
	}
	out, errOut, code = runActionCLI(t, srv, nil, "api", "routes", "--json")
	if code != 0 || !strings.Contains(out, `"needs_reason":true`) {
		t.Errorf("api routes --json: code=%d out=%q err=%q", code, out, errOut)
	}

	// 2. grain api --field (JSON boolean/array/object coercion + plain string)
	out, errOut, code = runActionCLI(t, srv, nil, "api", "POST", "/api/tasks/9",
		"--field", "enabled=true",
		"--field", `tags=["ci","fast"]`,
		"--field", `meta={"tier":1}`,
		"--field", "title=plain string")
	if code != 0 || !strings.Contains(out, `"ok":true`) {
		t.Fatalf("api POST --field: code=%d out=%q err=%q", code, out, errOut)
	}
	gotBody := proxy.bodies["POST /api/tasks/9"]
	if gotBody["enabled"] != true || gotBody["title"] != "plain string" {
		t.Errorf("POST --field coerced body = %+v", gotBody)
	}
	if tags, ok := gotBody["tags"].([]any); !ok || len(tags) != 2 || tags[0] != "ci" {
		t.Errorf("POST --field tags = %+v", gotBody["tags"])
	}

	// 3. grain api --file <path> and --file - (stdin)
	bodyFile := filepath.Join(t.TempDir(), "payload.json")
	if err := os.WriteFile(bodyFile, []byte(`{"from":"file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := runActionCLI(t, srv, nil, "api", "PUT", "/api/tasks/9", "--file", bodyFile); code != 0 {
		t.Fatalf("api PUT --file: code=%d err=%q", code, errOut)
	}
	if proxy.bodies["PUT /api/tasks/9"]["from"] != "file" {
		t.Errorf("PUT --file body = %+v", proxy.bodies["PUT /api/tasks/9"])
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cliStdin := &sandboxCLI{
		base:   srv.URL,
		token:  "tok",
		out:    &stdoutBuf,
		errOut: &stderrBuf,
		client: http.DefaultClient,
		in:     strings.NewReader(`{"from":"stdin"}`),
	}
	if err := cliStdin.run([]string{"api", "PATCH", "/api/tasks/9", "--file", "-"}); err != nil {
		t.Fatalf("api PATCH --file -: %v (%s)", err, stderrBuf.String())
	}
	if proxy.bodies["PATCH /api/tasks/9"]["from"] != "stdin" {
		t.Errorf("PATCH --file - body = %+v", proxy.bodies["PATCH /api/tasks/9"])
	}

	// 4. grain ci artifact to stdout, to -o <file>, and cleanup on missing artifact
	out, errOut, code = runActionCLI(t, srv, nil, "ci", "artifact", "go", "cover.out")
	if code != 0 || out != "mode: atomic\n" {
		t.Errorf("ci artifact stdout: code=%d out=%q err=%q", code, out, errOut)
	}
	destFile := filepath.Join(t.TempDir(), "cover.out")
	if _, errOut, code := runActionCLI(t, srv, nil, "ci", "artifact", "go", "cover.out", "-o", destFile); code != 0 {
		t.Fatalf("ci artifact -o: code=%d err=%q", code, errOut)
	}
	if data, err := os.ReadFile(destFile); err != nil || string(data) != "mode: atomic\n" {
		t.Errorf("ci artifact file = %q, %v", data, err)
	}
	missingDest := filepath.Join(t.TempDir(), "missing.out")
	if _, _, code := runActionCLI(t, srv, nil, "ci", "artifact", "go", "missing.out", "-o", missingDest); code == 0 {
		t.Errorf("ci artifact for missing file succeeded unexpectedly")
	}
	if _, err := os.Stat(missingDest); !os.IsNotExist(err) {
		t.Errorf("failed ci artifact left partial file behind at %s: %v", missingDest, err)
	}
}
