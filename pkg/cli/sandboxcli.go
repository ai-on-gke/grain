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

// sandboxcli.go is the grain CLI as an agent's sandbox sees it
// (docs/design/sandbox-first-agent-surface.md, §4): read commands that
// reach this deployment over the sandbox's own scoped proxy
// (http://127.0.0.1:9728, the same address and credential git uses), with
// no size cap, so a run can pipe a CI log to grep or redirect a transcript
// to a file instead of paging through an MCP tool's 16 KB results.
//
// Every command is a GET on /fs/ (the grain namespace) or /cli/ (queries
// over grain's records, pkg/gitproxy/cli.go). Scope is the proxy's to
// enforce, not this file's: a run without the grant a command needs gets
// the proxy's refusal, which says which capability is missing in the
// words of pkg/mcp's unknownToolMessage, printed as it came.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// sandboxProxyEnv names the sandbox proxy explicitly; sandboxCLIEnv=0
// turns sandbox mode off (for a host CLI run inside a sandbox).
const (
	sandboxProxyEnv     = "GRAIN_SANDBOX_PROXY"
	sandboxTokenEnv     = "GRAIN_SANDBOX_TOKEN"
	sandboxCLIEnv       = "GRAIN_SANDBOX_CLI"
	sandboxProxyAddr    = "127.0.0.1:9728"
	sandboxGitCredsPath = "/home/debian/.git-credentials"
)

// sandboxEnv is what sandbox mode is detected from, injectable for tests.
type sandboxEnv struct {
	Getenv    func(string) string
	ReadFile  func(string) ([]byte, error)
	CredsPath string
}

func osSandboxEnv() sandboxEnv {
	return sandboxEnv{Getenv: os.Getenv, ReadFile: os.ReadFile, CredsPath: sandboxGitCredsPath}
}

// detectSandboxProxy returns the proxy base URL and token when this process
// runs inside an agent sandbox: $GRAIN_SANDBOX_PROXY is set, or the
// sandbox's git credentials point at the proxy's address.
func detectSandboxProxy(env sandboxEnv) (base, token string, ok bool) {
	if v := strings.TrimSpace(env.Getenv(sandboxCLIEnv)); v == "0" || strings.EqualFold(v, "false") {
		return "", "", false
	}
	if strings.TrimSpace(env.Getenv(serverEnvVar)) != "" {
		// Somebody pointed this CLI at a daemon: that wins.
		return "", "", false
	}
	base = strings.TrimRight(strings.TrimSpace(env.Getenv(sandboxProxyEnv)), "/")
	token = strings.TrimSpace(env.Getenv(sandboxTokenEnv))
	if raw, err := env.ReadFile(env.CredsPath); err == nil {
		sc := bufio.NewScanner(strings.NewReader(string(raw)))
		for sc.Scan() {
			u, err := url.Parse(strings.TrimSpace(sc.Text()))
			if err != nil || u.User == nil {
				continue
			}
			if base == "" && u.Host != sandboxProxyAddr {
				continue
			}
			if base != "" && !strings.HasSuffix(base, "//"+u.Host) {
				continue
			}
			if base == "" {
				base = u.Scheme + "://" + u.Host
			}
			if token == "" {
				token, _ = u.User.Password()
			}
			break
		}
	}
	if base == "" {
		return "", "", false
	}
	return base, token, true
}

// sandboxCommands are the commands sandbox mode answers. A command not
// here -- grain mcpserver, grain capability serve, the agy hooks -- runs
// as it does anywhere else, even inside a sandbox.
var sandboxCommands = map[string][]string{
	"help": nil, "-h": nil, "--help": nil,
	"ls": nil, "read": nil, "grep": nil,
	"ci":          {"list", "log", "test-history", "artifact", "wait", "run-job", "retrigger"},
	"pr":          {"status", "base-status", "open", "reply", "resolve", "review-comment"},
	"git":         {"revert"},
	"merge-queue": nil,
	"task":        {"list", "read", "prompt", "transcript"},
	"metrics":     nil, "health": nil, "log": nil, "playbooks": nil,
	"issue": {"list", "read"},
	"db":    {"query"},
}

// isSandboxCommand reports whether args name a command sandbox mode serves.
func isSandboxCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	subs, ok := sandboxCommands[args[0]]
	if !ok {
		return false
	}
	if subs == nil || len(args) < 2 {
		return true
	}
	if isHelpFlag(args[1]) {
		return true
	}
	for _, s := range subs {
		if args[1] == s {
			return true
		}
	}
	return false
}

func isHelpFlag(a string) bool { return a == "-h" || a == "--help" || a == "help" }

// maybeRunSandboxCLI runs args as a sandbox command when this process is in
// a sandbox and args name one; handled is false otherwise.
func maybeRunSandboxCLI(args []string, env sandboxEnv, stdout, stderr io.Writer) (code int, handled bool) {
	if !isSandboxCommand(args) {
		return 0, false
	}
	base, token, ok := detectSandboxProxy(env)
	if !ok {
		return 0, false
	}
	c := &sandboxCLI{base: base, token: token, out: stdout, errOut: stderr,
		client: &http.Client{Timeout: 10 * time.Minute}, env: env.Getenv}
	if err := c.run(args); err != nil {
		if !errors.Is(err, errSandboxReported) {
			fmt.Fprintln(stderr, "grain: "+err.Error())
		}
		return 1, true
	}
	return 0, true
}

// errSandboxReported is an error already printed (the proxy's own words).
var errSandboxReported = errors.New("reported")

type sandboxCLI struct {
	base, token string
	out, errOut io.Writer
	client      *http.Client
	// env and in stand in for os.Getenv and os.Stdin in tests.
	env func(string) string
	in  io.Reader
}

const sandboxHelpText = `grain -- sandbox CLI for this grain deployment (proxy: %s)

NAMESPACE & FILES (no size cap; pipe to grep, jq, head, or redirect to files):
  grain ls <uri> [--glob <pat>]          List a directory in ci://, src://, task://, config://, memory://, deploy://
  grain read <uri> [--tail N] [--offset N --limit N]
                                         Read a file from the namespace to stdout
  grain grep <pattern> <uri> [-i] [-C N] [--glob <pat>]
                                         Search a file or subtree with an RE2 regex

CI & PULL REQUEST (scoped to this task's repository and branch):
  grain ci list [--ref <b>] [--conclusion <c>] [--job <j>] [--since <d>] [--limit N]
                                         List recent grain CI runs
  grain ci log [<run-id>] [--job <j>] [--step <s>] [--attempt K] [--tail N]
                                         List a run's jobs and steps, or print one step's log
  grain ci test-history <test-name> [--limit N]
                                         Pass/fail/flake history across recent CI runs
  grain ci artifact <job> <name> [-o <dest>] [--run <run-id>]
                                         Download a CI artifact
  grain ci wait [--jobs j1,j2] [--timeout 9m] [--resume <run-id>]
                                         Start or attach to grain CI for this branch and wait for its verdict
                                         (time blocked is credited back to this run's deadline)
  grain ci run-job <job> [--timeout 9m] [--resume <run-id>]
                                         Run a single named CI job against the current branch commit and wait
  grain ci retrigger [--jobs j1,j2 | --all] [--commit <sha>] [--workflow <file>]
                                         Rerun failed (or named) jobs fresh and uncached
  grain pr status                        Latest grain CI verdict on this task's branch
  grain pr base-status                   Latest grain CI verdict on the base branch
  grain pr open                          Open or update the pull request for this branch from its commit messages
  grain pr reply <body|--file f> [--comment-id <id>]
                                         Reply to an inline review comment, or post a top-level PR comment
  grain pr resolve (--thread-id <id> | --comment-id <id>)
                                         Mark a review conversation on this branch's pull request resolved
  grain pr review-comment <body|--file f> [--path <file> --line <N>]
                                         Record a review finding (relayed when the run finishes)
  grain merge-queue                      Merge queue batches, queued PRs and held gates

GIT (runs in this sandbox's working tree; pushes nothing):
  grain git revert (--task <id> | --commit <sha>) --reason <why> [-C <dir>]
                                         Commit a git revert of a task's merge or of one commit

TASKS & DEPLOYMENT (shown to every run; the proxy refuses what this run's grants do not cover):
  grain task list                        Tasks this run may read
  grain task read <id>                   A task's record
  grain task prompt <id> [--attempt N]   A task's prompt
  grain task transcript <id> [--attempt N]
                                         A task's transcript
  grain metrics [<section>]              grain's own measurements (self-debug)
  grain health                           This deployment's health (deployment-health)
  grain log [<source>] [--tail N]        grain's own log, redacted (grain-log)
  grain playbooks [<name>]               Bootstrap playbooks (bootstrap-playbooks)
  grain issue list | grain issue read <n>
                                         GitHub issues (read-issues)
  grain db query <sql>                   Read-only query on grain's database (db-read)

Run 'grain <command> --help' for flags and examples.
`

// sandboxCommandHelp is each command's --help.
var sandboxCommandHelp = map[string]string{
	"ls":              "grain ls <uri> [--glob <pat>]\n\nList a namespace directory.\n\n  grain ls ci://bwsalmon/grain\n  grain ls task://\n  grain ls src://pkg --glob '*_test.go'\n",
	"read":            "grain read <uri> [--tail N] [--offset N --limit N]\n\nPrint a namespace file to stdout, whole: there is no size cap.\n\n  grain read task://1436/record.md\n  grain read ci://bwsalmon/grain/<run>/go/gotest.log --tail 4000\n",
	"grep":            "grain grep <pattern> <uri> [-i] [-C N] [--glob <pat>]\n\nSearch a namespace file or subtree with an RE2 regular expression.\n\n  grain grep -- '--- FAIL' ci://bwsalmon/grain/<run>/go/gotest.log\n  grain grep -i -C 2 TODO src://pkg/ci --glob '*.go'\n",
	"ci list":         "grain ci list [--ref <branch>] [--conclusion failure,errored] [--job <j>] [--since 24h] [--limit N] [--repo owner/name]\n\nList recent grain CI runs, newest first. --ref @branch is this task's own branch.\n",
	"ci log":          "grain ci log [<run-id>] [--job <j>] [--step <s>] [--attempt K] [--tail N]\n\nWith no run, the latest run on this task's branch. With no job, list the run's jobs;\nwith a job and no step, list its step logs; with both, print that step's log.\n\n  grain ci log --job go\n  grain ci log --job go --step Test --tail 8000\n",
	"ci test-history": "grain ci test-history <test-name> [--limit N]\n\nPass, fail and flake history of one test across recent CI runs.\n",
	"ci artifact":     "grain ci artifact <job> <name> [-o <dest>] [--run <run-id>]\n\nDownload a stored CI artifact, to <dest> or stdout. With no run, the latest run on this task's branch.\n",
	"pr status":       "grain pr status\n\nThe latest grain CI run on this task's branch and each job's verdict.\n",
	"pr base-status":  "grain pr base-status\n\nThe latest grain CI run on this task's base branch and each job's verdict.\n",
	"ci wait": "grain ci wait [--jobs j1,j2] [--timeout 9m] [--resume <run-id>]\n\n" +
		"Start grain CI on this task's branch (or attach to the identical run in flight) and wait for\n" +
		"its verdict: exit 0 green, 1 red. One call waits at most 9m (and less when\n" +
		"$GRAIN_COMMAND_TIMEOUT_MS says the command has less), so give run_command timeout 600000.\n" +
		"If CI is still running when the slice ends it says so, exits 0, and names the run to resume:\n\n" +
		"  grain ci wait --resume <run-id>\n\n" +
		"or, to free the sandbox while CI finishes, call resume_later with when: {\"ci\": \"<run-id>\"}.\n" +
		"Time blocked here is credited back to this run's deadline (up to 45m over the run).\n" +
		"--jobs runs only those jobs (and their dependencies): fast, but never the full-suite gate.\n\n" +
		"  git push origin HEAD && grain ci wait\n  grain ci wait --jobs go,lint\n",
	"ci run-job": "grain ci run-job <job> [--timeout 9m] [--resume <run-id>]\n\n" +
		"Run one named CI job (and its dependencies) against this branch's commit and wait for it,\n" +
		"with the same 9m slice, resume and wait credit as grain ci wait.\n",
	"ci retrigger": "grain ci retrigger [--jobs j1,j2 | --all] [--commit <sha>] [--workflow <file>]\n\n" +
		"Rerun this branch's failed jobs fresh and uncached (or the named ones, or --all of them).\n" +
		"Then wait on it with grain ci wait --resume <run-id>.\n",
	"pr open": "grain pr open\n\nOpen the pull request for this task's branch (or find the one already open) and\n" +
		"report its checks. Its title and description come from the branch's commit messages.\n",
	"pr reply": "grain pr reply <body|--file f> [--comment-id <id>]\n\n" +
		"Post on GitHub on this branch's own pull request: in an inline review comment's thread\n" +
		"with --comment-id (grain pr status lists them), or as a top-level comment without.\n",
	"pr resolve": "grain pr resolve (--thread-id <id> | --comment-id <id>)\n\n" +
		"Mark a review conversation on this branch's own pull request resolved.\n",
	"pr review-comment": "grain pr review-comment <body|--file f> [--path <file> --line <N>]\n\n" +
		"Record one review finding, as add_review_comment does: on a line with --path and --line,\n" +
		"or a general remark without. They are relayed together when this run finishes.\n",
	"git revert": "grain git revert (--task <id> | --commit <sha>) --reason <why> [-C <dir>]\n\n" +
		"Commit a git revert in this sandbox's working tree (the current directory, or -C <dir>) of\n" +
		"a task's merge -- found by the branch grain recorded for it -- or of one commit. A merge is\n" +
		"reverted against its first parent. Nothing is pushed.\n\n" +
		"  grain git revert --task 1234 --reason 'broke the go job on main'\n",
	"git":             "grain git revert (--task <id> | --commit <sha>) --reason <why>\n",
	"merge-queue":     "grain merge-queue\n\nThe merge queue: in-flight batches, queued pull requests and held gates (deploy://merge-queue).\n",
	"task list":       "grain task list\n\nThe tasks this run may read: its own, or every task with self-debug.\n",
	"task read":       "grain task read <id>\n\nA task's record (task://<id>/record.md). grain ls task://<id> shows its other files.\n",
	"task prompt":     "grain task prompt <id> [--attempt N]\n\nA task's latest prompt, or attempt N's.\n",
	"task transcript": "grain task transcript <id> [--attempt N]\n\nA task's latest transcript, or attempt N's.\n",
	"metrics":         "grain metrics [<section>]\n\ngrain's own measurements (deploy://metrics). Needs self-debug.\n",
	"health":          "grain health\n\nThis deployment's health (deploy://health). Needs deployment-health.\n",
	"log":             "grain log [<source>] [--tail N]\n\ngrain's own log, redacted (deploy://grain.log). Needs grain-log.\n",
	"playbooks":       "grain playbooks [<name>]\n\nList the bootstrap playbooks, or print one (src://playbooks). Needs bootstrap-playbooks.\n",
	"issue list":      "grain issue list [--repo owner/name]\n\nGitHub issues. Needs read-issues.\n",
	"issue read":      "grain issue read <number> [--repo owner/name]\n\nOne GitHub issue. Needs read-issues.\n",
	"db query":        "grain db query <sql>\n\nA read-only SQL query on grain's database. Needs db-read.\n",
	"ci":              "grain ci (list | log | test-history | artifact | wait | run-job | retrigger) ...\n\nRun 'grain ci <command> --help' for each.\n",
	"pr":              "grain pr (status | base-status | open | reply | resolve | review-comment)\n",
	"task":            "grain task (list | read | prompt | transcript) ...\n",
	"issue":           "grain issue (list | read) ...\n",
	"db":              "grain db query <sql>\n",
}

func (c *sandboxCLI) run(args []string) error {
	cmd := args[0]
	if cmd == "help" || cmd == "-h" || cmd == "--help" {
		if len(args) > 1 {
			return c.help(strings.Join(args[1:], " "))
		}
		fmt.Fprintf(c.out, sandboxHelpText, c.base)
		return nil
	}
	rest := args[1:]
	if sandboxCommands[cmd] != nil {
		if len(rest) == 0 || isHelpFlag(rest[0]) {
			return c.help(cmd)
		}
		cmd += " " + rest[0]
		rest = rest[1:]
	}
	for _, a := range rest {
		if a == "-h" || a == "--help" {
			return c.help(cmd)
		}
	}
	p, err := parseSandboxArgs(rest)
	if err != nil {
		return err
	}
	switch cmd {
	case "ls":
		uri, err := p.arg(0, "uri")
		if err != nil {
			return err
		}
		return c.fs(uri, p.query("list", "", "glob"))
	case "read":
		uri, err := p.arg(0, "uri")
		if err != nil {
			return err
		}
		return c.fs(uri, p.query("tail", "offset", "limit"))
	case "grep":
		pattern, err := p.arg(0, "pattern")
		if err != nil {
			return err
		}
		uri, err := p.arg(1, "uri")
		if err != nil {
			return err
		}
		q := p.query("glob")
		q.Set("grep", pattern)
		if p.has("i") {
			q.Set("i", "1")
		}
		if v := p.get("C"); v != "" {
			q.Set("context", v)
		}
		q.Set("n", "1")
		return c.fs(uri, q)
	case "ci list":
		return c.get("/cli/ci/runs", p.query("ref", "conclusion", "job", "since", "limit", "repo"), c.out)
	case "ci test-history":
		test, err := p.arg(0, "test-name")
		if err != nil {
			return err
		}
		q := p.query("limit", "repo")
		q.Set("test", test)
		return c.get("/cli/ci/test-history", q, c.out)
	case "ci log":
		return c.ciLog(p)
	case "ci artifact":
		return c.ciArtifact(p)
	case "pr status":
		return c.get("/cli/pr/status", nil, c.out)
	case "pr base-status":
		return c.get("/cli/pr/base-status", nil, c.out)
	case "ci wait":
		return c.ciWait(p, "")
	case "ci run-job":
		job, err := p.arg(0, "job")
		if err != nil {
			return err
		}
		return c.ciWait(p, job)
	case "ci retrigger":
		return c.ciRetrigger(p)
	case "pr open":
		return c.post("/cli/pr/open", map[string]any{}, c.out)
	case "pr reply":
		return c.prReply(p)
	case "pr resolve":
		return c.prResolve(p)
	case "pr review-comment":
		return c.prReviewComment(p)
	case "git revert":
		return c.gitRevert(p)
	case "merge-queue":
		return c.fs("deploy://merge-queue", nil)
	case "task list":
		return c.fs("task://", url.Values{"list": {""}})
	case "task read":
		id, err := p.arg(0, "id")
		if err != nil {
			return err
		}
		return c.fs("task://"+id+"/record.md", nil)
	case "task prompt", "task transcript":
		id, err := p.arg(0, "id")
		if err != nil {
			return err
		}
		file := strings.TrimPrefix(cmd, "task ") + ".md"
		if n := p.get("attempt"); n != "" {
			return c.fs("task://"+id+"/attempts/"+n+"/"+file, nil)
		}
		return c.fs("task://"+id+"/"+file, nil)
	case "metrics":
		if section := p.optArg(0); section != "" {
			return c.fs("deploy://metrics/"+section, nil)
		}
		return c.fs("deploy://metrics", nil)
	case "health":
		return c.fs("deploy://health", nil)
	case "log":
		uri := "deploy://grain.log"
		if source := p.optArg(0); source != "" {
			uri += "/" + source
		}
		return c.fs(uri, p.query("tail"))
	case "playbooks":
		if name := p.optArg(0); name != "" {
			return c.fs("src://playbooks/"+name, nil)
		}
		return c.fs("src://playbooks", url.Values{"list": {""}})
	case "issue list":
		return c.get("/cli/issues", p.query("repo"), c.out)
	case "issue read":
		n, err := p.arg(0, "number")
		if err != nil {
			return err
		}
		return c.get("/cli/issues/"+url.PathEscape(n), p.query("repo"), c.out)
	case "db query":
		sql := strings.Join(p.positional, " ")
		if strings.TrimSpace(sql) == "" {
			return fmt.Errorf("db query: a SQL statement is required")
		}
		return c.get("/cli/db/query", url.Values{"sql": {sql}}, c.out)
	}
	return fmt.Errorf("unknown command %q: run 'grain help'", cmd)
}

func (c *sandboxCLI) help(cmd string) error {
	text, ok := sandboxCommandHelp[cmd]
	if !ok {
		return fmt.Errorf("no help for %q: run 'grain help'", cmd)
	}
	fmt.Fprint(c.out, text)
	return nil
}

// fsPath turns "ci://owner/repo/run" into "/fs/ci/owner/repo/run".
func fsPath(uri string) (string, error) {
	scheme, rel, ok := strings.Cut(uri, "://")
	if !ok || scheme == "" {
		return "", fmt.Errorf("%q is not a namespace URI (ci://, src://, task://, config://, memory://, deploy://)", uri)
	}
	segs := strings.Split(strings.Trim(rel, "/"), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	p := "/fs/" + scheme
	if joined := strings.Join(segs, "/"); joined != "" {
		p += "/" + joined
	}
	return p, nil
}

func (c *sandboxCLI) fs(uri string, q url.Values) error {
	p, err := fsPath(uri)
	if err != nil {
		return err
	}
	return c.get(p, q, c.out)
}

// get streams path's response body to w, or prints the proxy's refusal.
func (c *sandboxCLI) get(path string, q url.Values, w io.Writer) error {
	resp, err := c.do(path, q)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		fmt.Fprintln(c.errOut, "grain: "+strings.TrimSpace(string(body)))
		return errSandboxReported
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

func (c *sandboxCLI) do(path string, q url.Values) (*http.Response, error) {
	u := c.base + path
	if enc := encodeQuery(q); enc != "" {
		u += "?" + enc
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.SetBasicAuth("x-access-token", c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reaching the sandbox proxy at %s: %w", c.base, err)
	}
	return resp, nil
}

// encodeQuery is url.Values.Encode, except that a flag with no value
// ("list") is sent bare, as the namespace expects.
func encodeQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		for _, v := range q[k] {
			if v == "" {
				parts = append(parts, url.QueryEscape(k))
				continue
			}
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

type runContext struct {
	Task, Repo, Branch, Base string
}

func (c *sandboxCLI) context() (runContext, error) {
	var buf strings.Builder
	if err := c.get("/cli/context", nil, &buf); err != nil {
		return runContext{}, err
	}
	var rc runContext
	if err := json.Unmarshal([]byte(buf.String()), &rc); err != nil {
		return runContext{}, fmt.Errorf("reading this run's context: %w", err)
	}
	return rc, nil
}

// latestRun is the newest grain CI run on this task's branch.
func (c *sandboxCLI) latestRun() (string, error) {
	var buf strings.Builder
	if err := c.get("/cli/ci/runs", url.Values{"ref": {"@branch"}, "limit": {"1"}}, &buf); err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) < 2 {
		return "", fmt.Errorf("this task's branch has no grain CI run yet")
	}
	id, _, _ := strings.Cut(lines[1], " ")
	return id, nil
}

func (c *sandboxCLI) ciLog(p sandboxArgs) error {
	rc, err := c.context()
	if err != nil {
		return err
	}
	run := p.optArg(0)
	if run == "" {
		if run, err = c.latestRun(); err != nil {
			return err
		}
	}
	uri := "ci://" + rc.Repo + "/" + run
	job := p.get("job")
	if job == "" {
		return c.fs(uri, url.Values{"list": {""}})
	}
	uri += "/" + job
	if n := p.get("attempt"); n != "" {
		uri += "/attempts/" + n
	}
	step := p.get("step")
	if step == "" {
		return c.fs(uri, url.Values{"list": {""}})
	}
	if !strings.HasSuffix(step, ".log") {
		step += ".log"
	}
	return c.fs(uri+"/"+step, p.query("tail"))
}

func (c *sandboxCLI) ciArtifact(p sandboxArgs) error {
	job, err := p.arg(0, "job")
	if err != nil {
		return err
	}
	name, err := p.arg(1, "name")
	if err != nil {
		return err
	}
	rc, err := c.context()
	if err != nil {
		return err
	}
	run := p.get("run")
	if run == "" {
		if run, err = c.latestRun(); err != nil {
			return err
		}
	}
	path, err := fsPath("ci://" + rc.Repo + "/" + run + "/artifacts/" + job + "/" + name)
	if err != nil {
		return err
	}
	dest := p.get("o")
	if dest == "" || dest == "-" {
		return c.get(path, nil, c.out)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	if err := c.get(path, nil, f); err != nil {
		f.Close()
		os.Remove(dest)
		return err
	}
	return f.Close()
}

// sandboxArgs is a command's arguments: flags anywhere among positionals,
// as --name value, --name=value, -n value, or a bare boolean (-i).
type sandboxArgs struct {
	positional []string
	flags      map[string]string
}

// sandboxBoolFlags take no value.
var sandboxBoolFlags = map[string]bool{"i": true, "full": true, "json": true, "all": true}

func parseSandboxArgs(args []string) (sandboxArgs, error) {
	p := sandboxArgs{flags: map[string]string{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			p.positional = append(p.positional, args[i+1:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' {
			p.positional = append(p.positional, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		if k, v, ok := strings.Cut(name, "="); ok {
			p.flags[k] = v
			continue
		}
		if sandboxBoolFlags[name] {
			p.flags[name] = "1"
			continue
		}
		if i+1 >= len(args) {
			return p, fmt.Errorf("flag %s needs a value", a)
		}
		p.flags[name] = args[i+1]
		i++
	}
	return p, nil
}

func (p sandboxArgs) arg(i int, name string) (string, error) {
	if i >= len(p.positional) {
		return "", fmt.Errorf("missing <%s>: run with --help for usage", name)
	}
	return p.positional[i], nil
}

func (p sandboxArgs) optArg(i int) string {
	if i >= len(p.positional) {
		return ""
	}
	return p.positional[i]
}

func (p sandboxArgs) get(name string) string { return p.flags[name] }

func (p sandboxArgs) has(name string) bool { _, ok := p.flags[name]; return ok }

// query copies the named flags into a query; "list" is always set bare.
func (p sandboxArgs) query(names ...string) url.Values {
	q := url.Values{}
	for _, n := range names {
		if n == "list" {
			q.Set("list", "")
			continue
		}
		if v, ok := p.flags[n]; ok && n != "" {
			if _, err := strconv.Atoi(v); err != nil && (n == "tail" || n == "offset" || n == "limit") {
				continue
			}
			q.Set(n, v)
		}
	}
	return q
}
