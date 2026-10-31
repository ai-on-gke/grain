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

// sandboxcli_actions.go is the acting half of the sandbox CLI
// (docs/design/sandbox-first-agent-surface.md, Stage 2): grain ci wait,
// run-job and retrigger, grain pr open, reply, resolve and
// review-comment, which POST to the sandbox proxy's /cli/ routes
// (pkg/gitproxy's cli_actions.go), and grain git revert, which asks the
// proxy only what a task's work was called and then runs git revert here,
// in the sandbox's own working tree -- so it works wherever the sandbox
// is, a granule VM or a container as well as a host directory.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/mcp"
)

// sandboxCommandTimeoutEnv is the run_command timeout, in milliseconds,
// a framework may export to the commands it runs: a wait is sliced to
// finish inside it.
const sandboxCommandTimeoutEnv = "GRAIN_COMMAND_TIMEOUT_MS"

// defaultSandboxWaitSlice is grain ci wait's default --timeout, and its
// most: gitproxy.DefaultCLIWaitSlice.
const defaultSandboxWaitSlice = 540 * time.Second

// post sends body as JSON to path and streams the answer to w, or prints
// the proxy's refusal.
func (c *sandboxCLI) post(path string, body any, w io.Writer) error {
	resp, err := c.doPost(path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		fmt.Fprintln(c.errOut, "grain: "+strings.TrimSpace(string(msg)))
		return errSandboxReported
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

func (c *sandboxCLI) doPost(path string, body any) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, c.base+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.SetBasicAuth("x-access-token", c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reaching the sandbox proxy at %s: %w", c.base, err)
	}
	return resp, nil
}

// parseWaitTimeout reads --timeout as a duration ("9m", "300s") or a
// number of seconds, capped at the default slice and, when the framework
// says how long the command may run, at that less a margin.
func parseWaitTimeout(flag string, getenv func(string) string) (time.Duration, error) {
	slice := defaultSandboxWaitSlice
	if flag = strings.TrimSpace(flag); flag != "" {
		d, err := time.ParseDuration(flag)
		if err != nil {
			n, nerr := strconv.Atoi(flag)
			if nerr != nil {
				return 0, fmt.Errorf("--timeout %q is not a duration (9m, 300s) or a number of seconds", flag)
			}
			d = time.Duration(n) * time.Second
		}
		if d <= 0 {
			return 0, fmt.Errorf("--timeout must be positive")
		}
		slice = min(slice, d)
	}
	if getenv != nil {
		if ms, err := strconv.Atoi(strings.TrimSpace(getenv(sandboxCommandTimeoutEnv))); err == nil && ms > 0 {
			if limit := time.Duration(ms)*time.Millisecond - 30*time.Second; limit > 0 {
				slice = min(slice, limit)
			}
		}
	}
	return slice, nil
}

// sandboxWaitAnswer is gitproxy.CLIWaitResponse as the CLI reads it.
type sandboxWaitAnswer struct {
	Status      string `json:"status"`
	RunID       string `json:"run_id"`
	ResumeRunID string `json:"resume_run_id"`
	Commit      string `json:"commit"`
	Conclusion  string `json:"conclusion"`
	Error       string `json:"error"`
	Report      string `json:"report"`
	Selective   bool   `json:"selective"`
	Jobs        []struct {
		Name       string `json:"name"`
		Conclusion string `json:"conclusion"`
	} `json:"jobs"`
	WaitedMS    int64 `json:"waited_ms"`
	CreditMS    int64 `json:"credit_used_ms"`
	CreditCapMS int64 `json:"credit_cap_ms"`
	CreditLive  bool  `json:"credit_live"`
}

// ciWait runs grain ci wait (single == "") or grain ci run-job <single>.
func (c *sandboxCLI) ciWait(p sandboxArgs, single string) error {
	timeout, err := parseWaitTimeout(p.get("timeout"), c.getenv)
	if err != nil {
		return err
	}
	body := map[string]any{"timeout_seconds": int(timeout / time.Second)}
	path := "/cli/ci/wait"
	if single != "" {
		path = "/cli/ci/run-job"
		body["job"] = single
	} else if jobs := p.get("jobs"); jobs != "" {
		body["jobs"] = strings.Split(jobs, ",")
	}
	if resume := p.get("resume"); resume != "" {
		body["resume_run_id"] = resume
	}
	var buf bytes.Buffer
	if err := c.post(path, body, &buf); err != nil {
		return err
	}
	var ans sandboxWaitAnswer
	if err := json.Unmarshal(buf.Bytes(), &ans); err != nil {
		return fmt.Errorf("reading the proxy's answer: %w", err)
	}
	return c.reportWait(ans)
}

func (c *sandboxCLI) reportWait(ans sandboxWaitAnswer) error {
	if ans.Report != "" {
		fmt.Fprintln(c.out, strings.TrimRight(ans.Report, "\n"))
		if ans.Status == "failed" {
			return errSandboxReported
		}
		return nil
	}
	waited := (time.Duration(ans.WaitedMS) * time.Millisecond).Round(time.Second)
	credit := ""
	if ans.CreditLive {
		credit = fmt.Sprintf("; wait credit used: %s / %s",
			(time.Duration(ans.CreditMS) * time.Millisecond).Round(time.Second),
			time.Duration(ans.CreditCapMS)*time.Millisecond)
	}
	var running, passed, failed []string
	for _, j := range ans.Jobs {
		switch j.Conclusion {
		case "running", "queued", "":
			running = append(running, j.Name)
		case "success", "skipped":
			passed = append(passed, j.Name)
		default:
			failed = append(failed, j.Name+" ("+j.Conclusion+")")
		}
	}
	commit := ans.Commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	switch ans.Status {
	case "running":
		fmt.Fprintf(c.out, "CI run %s is still running after %s (jobs running: %s; passed: %s%s).\n",
			ans.RunID, waited, listOrNone(running), listOrNone(passed), credit)
		fmt.Fprintf(c.out, "To keep waiting in this run:  grain ci wait --resume %s\n", ans.ResumeRunID)
		fmt.Fprintf(c.out, "To suspend and free the VM:   call resume_later with when: {\"ci\": \"%s\"}\n", ans.ResumeRunID)
		return nil
	case "passed":
		fmt.Fprintf(c.out, "grain CI passed on %s (run %s, waited %s%s): %s\n",
			commit, ans.RunID, waited, credit, listOrNone(passed))
		if ans.Selective {
			fmt.Fprintln(c.out, "This was a selective run: it never satisfies the full-suite gate. Run grain ci wait with no --jobs for that.")
		}
		return nil
	}
	fmt.Fprintf(c.out, "grain CI failed on %s (run %s, %s, waited %s%s).\n",
		commit, ans.RunID, ans.Conclusion, waited, credit)
	if ans.Error != "" {
		fmt.Fprintf(c.out, "  %s\n", ans.Error)
	}
	if len(failed) > 0 {
		fmt.Fprintf(c.out, "Failing: %s\n", strings.Join(failed, ", "))
		fmt.Fprintf(c.out, "Read a failing job's steps: grain ci log %s --job <job>\n", ans.RunID)
	}
	if len(passed) > 0 {
		fmt.Fprintf(c.out, "Passed: %s\n", strings.Join(passed, ", "))
	}
	return errSandboxReported
}

func listOrNone(xs []string) string {
	if len(xs) == 0 {
		return "none"
	}
	return strings.Join(xs, ", ")
}

func (c *sandboxCLI) ciRetrigger(p sandboxArgs) error {
	body := map[string]any{}
	if jobs := p.get("jobs"); jobs != "" {
		body["jobs"] = strings.Split(jobs, ",")
	}
	if p.has("all") {
		body["all"] = true
	}
	if v := p.get("commit"); v != "" {
		body["commit"] = v
	}
	if v := p.get("workflow"); v != "" {
		body["workflow"] = v
	}
	return c.post("/cli/ci/retrigger", body, c.out)
}

// textArg is a command's text: its positionals joined, or --file's
// contents ("-" for stdin).
func (c *sandboxCLI) textArg(p sandboxArgs, what string) (string, error) {
	if f := p.get("file"); f != "" {
		var raw []byte
		var err error
		if f == "-" {
			raw, err = io.ReadAll(c.stdin())
		} else {
			raw, err = os.ReadFile(f)
		}
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}
	text := strings.Join(p.positional, " ")
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("missing <%s> (or --file): run with --help for usage", what)
	}
	return text, nil
}

func optInt(p sandboxArgs, name string) (int, error) {
	v := p.get(name)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("--%s must be a positive integer", name)
	}
	return n, nil
}

func (c *sandboxCLI) prReply(p sandboxArgs) error {
	body, err := c.textArg(p, "body")
	if err != nil {
		return err
	}
	id, err := optInt(p, "comment-id")
	if err != nil {
		return err
	}
	req := map[string]any{"body": body}
	if id > 0 {
		req["comment_id"] = id
	}
	return c.post("/cli/pr/reply", req, c.out)
}

func (c *sandboxCLI) prResolve(p sandboxArgs) error {
	id, err := optInt(p, "comment-id")
	if err != nil {
		return err
	}
	thread := p.get("thread-id")
	if thread == "" && id == 0 {
		return fmt.Errorf("name the thread: --thread-id <id> or --comment-id <id>")
	}
	req := map[string]any{}
	if thread != "" {
		req["thread_id"] = thread
	}
	if id > 0 {
		req["comment_id"] = id
	}
	return c.post("/cli/pr/resolve", req, c.out)
}

func (c *sandboxCLI) prReviewComment(p sandboxArgs) error {
	body, err := c.textArg(p, "body")
	if err != nil {
		return err
	}
	line, err := optInt(p, "line")
	if err != nil {
		return err
	}
	path := p.get("path")
	if (path == "") != (line == 0) {
		return fmt.Errorf("give both --path and --line to attach a comment to a line, or neither for a general remark")
	}
	req := map[string]any{"body": body}
	if path != "" {
		req["path"], req["line"] = path, line
	}
	return c.post("/cli/pr/review-comment", req, c.out)
}

// sandboxRevertInfo is gitproxy.RevertInfo as the CLI reads it.
type sandboxRevertInfo struct {
	Task        string `json:"task"`
	Title       string `json:"title"`
	Branch      string `json:"branch"`
	PullRequest string `json:"pull_request"`
}

// gitRevert runs grain git revert in the working tree the command was run
// in: the task's merge is found in that tree's own history, by the branch
// grain recorded for it when it can be, and reverted there with the same
// code revert_task runs (mcp.GitTaskReverter). Nothing is pushed.
func (c *sandboxCLI) gitRevert(p sandboxArgs) error {
	reason := strings.TrimSpace(p.get("reason"))
	if reason == "" {
		return fmt.Errorf("--reason is required: say why this is being reverted")
	}
	task, commit := strings.TrimSpace(p.get("task")), strings.TrimSpace(p.get("commit"))
	if (task == "") == (commit == "") {
		return fmt.Errorf("name exactly one of --task <id> or --commit <sha>")
	}
	dir := p.get("C")
	if dir == "" {
		dir = "."
	}
	ctx := context.Background()
	req := mcp.RevertTaskRequest{TaskID: task, CommitSHA: commit, Reason: reason}
	if task != "" {
		var buf bytes.Buffer
		if err := c.get("/cli/tasks/"+url.PathEscape(task)+"/revert-info", nil, &buf); err != nil {
			return err
		}
		var info sandboxRevertInfo
		if err := json.Unmarshal(buf.Bytes(), &info); err != nil {
			return fmt.Errorf("reading the proxy's answer: %w", err)
		}
		req.TaskID = info.Task
		if sha := findBranchMerge(ctx, dir, info.Branch); sha != "" {
			req.CommitSHA = sha
		}
	}
	res, err := mcp.NewGitTaskReverter(dir).RevertTask(ctx, req)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "Reverted %s (%s) as %s.\n", res.Summary, res.CommitSHA, res.NewCommitSHA)
	fmt.Fprintln(c.out, "Nothing was pushed: check the tree, then push the branch as usual.")
	return nil
}

// findBranchMerge is the newest commit in dir's history that names branch
// -- the merge of the pull request opened from it -- or "" when there is
// none or no branch to look for.
func findBranchMerge(ctx context.Context, dir, branch string) string {
	branch = strings.TrimSpace(branch)
	if branch == "" || strings.HasPrefix(branch, "-") {
		return ""
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "log", "-n", "1", "--fixed-strings",
		"--grep="+branch, "--format=%H")
	// The sandbox's own environment, deliberately: this runs inside the
	// agent's sandbox, on its own working tree, as the agent's shell would.
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (c *sandboxCLI) getenv(name string) string {
	if c.env != nil {
		return c.env(name)
	}
	return os.Getenv(name)
}

func (c *sandboxCLI) stdin() io.Reader {
	if c.in != nil {
		return c.in
	}
	return os.Stdin
}
