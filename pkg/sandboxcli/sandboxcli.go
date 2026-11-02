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

// Package sandboxcli is the grain CLI as an agent's sandbox sees it
// (design/design/sandbox-first-agent-surface.design.md, §4): read commands that
// reach this deployment over the sandbox's own scoped proxy
// (http://127.0.0.1:9728 or the guest's bridge gateway on :9728, the same
// address and credential git uses), with
// no size cap, so a run can pipe a CI log to grep or redirect a transcript
// to a file instead of paging through an MCP tool's 16 KB results.
//
// Every command is a GET on /fs/ (the grain namespace) or /cli/ (queries
// over grain's records, pkg/gitproxy/cli.go). Scope is the proxy's to
// enforce, not this file's: a run without the grant a command needs gets
// the proxy's refusal, which says which capability is missing in the
// words of pkg/mcp's unknownToolMessage, printed as it came.
package sandboxcli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// sandboxProxyEnv names the sandbox proxy explicitly; sandboxCLIEnv=0
// turns sandbox mode off (for a host CLI run inside a sandbox).
const (
	sandboxProxyEnv     = "GRAIN_SANDBOX_PROXY"
	sandboxTokenEnv     = "GRAIN_SANDBOX_TOKEN"
	sandboxCLIEnv       = "GRAIN_SANDBOX_CLI"
	sandboxProxyPort    = "9728"
	sandboxGitCredsPath = "/home/debian/.git-credentials"
	// serverEnvVar is pkg/cli's own: a CLI pointed at a daemon is not in
	// sandbox mode, wherever it runs.
	serverEnvVar = "GRAIN_SERVER"
)

var cgnatPrefix = netip.MustParsePrefix("100.64.0.0/10")

func isSandboxProxyCredential(u *url.URL) bool {
	if (u.Scheme != "http" && u.Scheme != "https") || u.User == nil {
		return false
	}
	host, port := u.Hostname(), u.Port()
	if host == "" || port == "" {
		return false
	}
	if port == sandboxProxyPort {
		return true
	}
	return u.User.Username() == "sandbox" && isSandboxProxyHost(host)
}

func isSandboxProxyHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || cgnatPrefix.Contains(ip)
	}
	lower := strings.ToLower(host)
	return !strings.Contains(lower, ".") ||
		strings.HasSuffix(lower, ".internal") ||
		strings.HasSuffix(lower, ".local") ||
		strings.HasSuffix(lower, ".svc")
}

// sandboxEnv is what sandbox mode is detected from, injectable for tests.
type sandboxEnv struct {
	Getenv    func(string) string
	ReadFile  func(string) ([]byte, error)
	CredsPath string
}

func osSandboxEnv() sandboxEnv {
	creds := sandboxGitCredsPath
	if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
		p := filepath.Join(home, ".git-credentials")
		if _, err := os.Stat(p); err == nil {
			creds = p
		}
	}
	return sandboxEnv{Getenv: os.Getenv, ReadFile: os.ReadFile, CredsPath: creds}
}

func parseGitCredentialURL(line string) (*url.URL, error) {
	line = strings.TrimSpace(line)
	if at := strings.LastIndex(line, "@"); at >= 0 {
		hostPart := line[at+1:]
		hostPart = strings.ReplaceAll(hostPart, "%3a", ":")
		hostPart = strings.ReplaceAll(hostPart, "%3A", ":")
		line = line[:at+1] + hostPart
	}
	return url.Parse(line)
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
			u, err := parseGitCredentialURL(sc.Text())
			if err != nil || u.User == nil {
				continue
			}
			if base == "" && !isSandboxProxyCredential(u) {
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
	"ci":            {"list", "log", "test-history", "artifact", "wait", "run-job", "retrigger"},
	"pr":            {"status", "base-status", "open", "reply", "resolve", "review-comment"},
	"git":           {"revert"},
	"merge-queue":   nil,
	"status":        nil,
	"handover":      {"write"},
	"notify":        nil,
	"task":          {"list", "read", "prompt", "transcript", "wait", "message", "propose"},
	"api":           nil,
	"memory":        {"list", "read", "write", "doc-write", "doc"},
	"release-notes": {"stats", "publish"},
	"quarantine":    nil,
	"metrics":       nil, "health": nil, "log": nil, "playbooks": nil,
	"issue": {"list", "read"},
	"db":    {"query"},
	"cap":   nil,
}

// sandboxCommandFlags are the flags each command reads; parseSandboxArgs
// takes any --name, so run refuses the rest rather than answer a question
// nobody asked (grain cap's flags are its tool's, checked there).
var sandboxCommandFlags = map[string][]string{
	"ls":                    {"glob"},
	"read":                  {"tail", "offset", "limit", "lines", "n", "cursor", "since", "page"},
	"grep":                  {"i", "C", "glob", "max"},
	"ci list":               {"ref", "conclusion", "job", "since", "limit", "repo", "task"},
	"ci test-history":       {"limit", "repo"},
	"ci log":                {"task", "repo", "job", "step", "attempt", "tail", "offset", "limit", "lines", "n"},
	"ci artifact":           {"o", "run", "task", "repo"},
	"ci wait":               {"jobs", "timeout", "resume"},
	"ci run-job":            {"timeout", "resume"},
	"ci retrigger":          {"jobs", "all", "commit", "workflow"},
	"pr status":             {"task", "ref", "repo"},
	"pr base-status":        nil,
	"pr open":               nil,
	"pr reply":              {"file", "comment-id"},
	"pr resolve":            {"thread-id", "comment-id"},
	"pr review-comment":     {"file", "path", "line"},
	"git revert":            {"task", "commit", "reason", "C"},
	"merge-queue":           nil,
	"status":                {"file"},
	"handover write":        {"file"},
	"notify":                {"subject", "title", "body", "file", "severity", "task", "clears-when", "json"},
	"task list":             {"state", "repo", "limit", "search", "changed-since", "stalled", "kind", "json"},
	"task read":             nil,
	"task prompt":           {"attempt", "tail", "offset", "limit", "lines", "n"},
	"task transcript":       {"attempt", "tail", "offset", "limit", "lines", "n"},
	"task wait":             {"until", "timeout", "json", "park", "resume-at"},
	"task message":          {"body", "file"},
	"task propose":          {"title", "body", "file", "id", "repo", "branch", "no-repo", "depends-on", "capability", "fixes-base-branch", "no-port", "keep-running", "origin", "read", "custom-field", "not-before", "priority", "json", "supersedes", "auto-merge", "hold-for-approval"},
	"api":                   {"scope", "json", "body", "file", "field", "reason"},
	"memory list":           nil,
	"memory read":           nil,
	"memory write":          {"repo", "file", "reason"},
	"memory doc-write":      {"repo", "file", "summary", "reason"},
	"release-notes stats":   {"period"},
	"release-notes publish": {"version", "period", "title", "file", "reason"},
	"quarantine":            {"reason", "ttl", "no-follow-up", "C"},
	"metrics":               {"window", "since", "until", "repo", "author"},
	"health":                nil,
	"log":                   {"source", "since", "until", "priority", "min-priority", "contains", "max-lines", "lines", "n", "tail"},
	"playbooks":             nil,
	"issue list":            {"repo", "label", "limit"},
	"issue read":            {"repo", "comments"},
	"db query":              nil,
}

// sandboxCountFlags take a non-negative integer, checked before the
// command runs rather than dropped from its query.
var sandboxCountFlags = []string{"tail", "offset", "limit", "lines", "n", "max", "max-lines", "attempt", "page"}

// pipelineChangesFlags are the flags only deploy://pipeline-changes reads:
// the cursor a page ended with (--since is its other name) and how many
// changes a page holds.
var pipelineChangesFlags = []string{"cursor", "since", "page"}

// usageError is a command line no command can answer: printed as it is,
// with exit status 2.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

// isSandboxCommand reports whether args name a command sandbox mode serves.
// A sandbox command's unknown subcommand is still one: run answers it with
// that command's own help, never the host CLI's.
func isSandboxCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	_, ok := sandboxCommands[args[0]]
	return ok
}

func isHelpFlag(a string) bool { return a == "-h" || a == "--help" || a == "help" }

// Main runs args as a sandbox command when this process is in a sandbox
// and args name one; handled is false otherwise, and nothing is printed.
// Both the full grain binary (pkg/cli) and the sandbox image's own
// /usr/local/bin/grain (cmd/grain-sandbox) answer through it.
func Main(args []string, stdout, stderr io.Writer) (code int, handled bool) {
	return maybeRunSandboxCLI(args, osSandboxEnv(), stdout, stderr)
}

// IsCommand reports whether args name a command sandbox mode serves,
// whether or not this process is in a sandbox to serve it.
func IsCommand(args []string) bool { return isSandboxCommand(args) }

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
		var usage usageError
		if errors.As(err, &usage) {
			fmt.Fprint(stderr, usage.msg)
			return 2, true
		}
		if !errors.Is(err, errSandboxReported) {
			fmt.Fprintln(stderr, "grain: "+err.Error())
		}
		return 1, true
	}
	return 0, true
}

// Unknown answers args, which no command of the host CLI's took, when this
// process is in a sandbox: with the sandbox CLI's help on stderr and exit
// status 2, rather than the host CLI's usage, whose daemon and runhost
// commands a sandbox has no use for. handled is false outside a sandbox,
// and for a leading global flag (-server, -json), which is the host CLI's.
func Unknown(args []string, stderr io.Writer) (code int, handled bool) {
	return unknownCommand(args, osSandboxEnv(), stderr)
}

func unknownCommand(args []string, env sandboxEnv, stderr io.Writer) (code int, handled bool) {
	if len(args) > 0 && strings.HasPrefix(args[0], "-") {
		return 0, false
	}
	base, _, ok := detectSandboxProxy(env)
	if !ok {
		return 0, false
	}
	if len(args) > 0 {
		fmt.Fprintf(stderr, "grain: unknown command %q\n\n", args[0])
	}
	fmt.Fprintf(stderr, sandboxHelpText, redactProxy(base))
	return 2, true
}

// redactProxy is base as help prints it: without the sandbox's token,
// which would otherwise land in the transcript of every run that asks for
// help or mistypes a command.
func redactProxy(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.User == nil {
		return base
	}
	u.User = nil
	return u.String()
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

func (c *sandboxCLI) run(args []string) error {
	cmd := args[0]
	if cmd == "help" || cmd == "-h" || cmd == "--help" {
		if len(args) > 1 {
			return c.help(strings.Join(args[1:], " "))
		}
		fmt.Fprintf(c.out, sandboxHelpText, redactProxy(c.base))
		return nil
	}
	rest := args[1:]
	if cmd == "cap" {
		// grain cap <server> <tool>'s flags are the tool's own, and its
		// --help the tool's.
		return c.capCommand(rest)
	}
	if subs := sandboxCommands[cmd]; subs != nil {
		if len(rest) == 0 || isHelpFlag(rest[0]) {
			return c.help(cmd)
		}
		if !slices.Contains(subs, rest[0]) {
			return usageError{fmt.Sprintf("grain %s: unknown command %q\n\n%s", cmd, rest[0], sandboxCommandHelp[cmd])}
		}
		cmd += " " + rest[0]
		rest = rest[1:]
		if cmd == "memory doc" && len(rest) > 0 && rest[0] == "write" {
			cmd = "memory doc-write"
			rest = rest[1:]
		}
	}
	for _, a := range rest {
		if a == "-h" || a == "--help" {
			return c.help(cmd)
		}
	}
	allowed, ok := sandboxCommandFlags[cmd]
	if !ok {
		return usageError{fmt.Sprintf("grain %s: unknown command\n\n%s", cmd, sandboxCommandHelp[strings.Fields(cmd)[0]])}
	}
	p, parseErr := parseSandboxArgs(rest)
	// A flag the command does not take is the error to report, even when
	// it is the last argument and so has no value either.
	if err := p.only(cmd, allowed...); err != nil {
		return err
	}
	if parseErr != nil {
		return usageError{fmt.Sprintf("grain %s: %v\n", cmd, parseErr)}
	}
	for _, name := range sandboxCountFlags {
		if v, ok := p.flags[name]; ok {
			if n, err := strconv.Atoi(v); err != nil || n < 0 {
				return usageError{fmt.Sprintf("grain %s: %s %q is not a count: it takes a non-negative integer\n", cmd, flagName(name), v)}
			}
		}
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
		if !isPipelineChangesURI(uri) {
			for _, name := range pipelineChangesFlags {
				if p.has(name) {
					return usageError{fmt.Sprintf("grain read: %s pages deploy://pipeline-changes, not %s\n\n%s",
						flagName(name), uri, sandboxCommandHelp["read"])}
				}
			}
		}
		return c.read(uri, p, false)
	case "grep":
		pattern, err := p.arg(0, "pattern")
		if err != nil {
			return err
		}
		uri, err := p.arg(1, "uri")
		if err != nil {
			return err
		}
		q := p.query("glob", "max")
		q.Set("grep", pattern)
		if p.has("i") {
			q.Set("i", "1")
		}
		if v := p.get("C"); v != "" {
			if n, err := strconv.Atoi(v); err != nil || n < 0 {
				return usageError{fmt.Sprintf("grain grep: -C %q is not a count: it takes a non-negative integer\n", v)}
			}
			q.Set("context", v)
		}
		q.Set("n", "1")
		return c.fs(uri, q)
	case "ci list":
		return c.get("/cli/ci/runs", p.query("ref", "conclusion", "job", "since", "limit", "repo", "task"), c.out)
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
		return c.get("/cli/pr/status", p.query("task", "ref", "repo"), c.out)
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
	case "status":
		return c.status(p)
	case "handover write":
		return c.handoverWrite(p)
	case "notify":
		return c.notify(p)
	case "task list":
		return c.taskList(p)
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
		uri := "task://" + id + "/" + file
		if n := p.get("attempt"); n != "" {
			uri = "task://" + id + "/attempts/" + n + "/" + file
		}
		return c.read(uri, p, cmd == "task transcript")
	case "task wait":
		return c.taskWait(p)
	case "task message":
		return c.taskMessage(p)
	case "task propose":
		return c.taskPropose(p)
	case "api":
		return c.apiCommand(p)
	case "memory list":
		return c.fs("memory://", url.Values{"list": {""}})
	case "memory read":
		key, err := p.arg(0, "key")
		if err != nil {
			return err
		}
		return c.fs("memory://"+strings.TrimLeft(key, "/"), nil)
	case "memory write":
		return c.memoryWrite(p)
	case "memory doc-write":
		return c.memoryDocWrite(p)
	case "release-notes stats":
		return c.releaseNotesStats(p)
	case "release-notes publish":
		return c.releaseNotesPublish(p)
	case "quarantine":
		return c.quarantine(p)
	case "metrics":
		if err := p.only("metrics", "window", "since", "until", "repo", "author"); err != nil {
			return err
		}
		if section := p.optArg(0); section != "" {
			return c.fs("deploy://metrics/"+section, metricsQuery(p))
		}
		return c.fs("deploy://metrics", metricsQuery(p))
	case "health":
		return c.fs("deploy://health", nil)
	case "log":
		if err := p.only("log", "source", "since", "until", "priority", "min-priority", "contains", "max-lines", "lines", "tail"); err != nil {
			return err
		}
		uri := "deploy://grain.log"
		if source := grainLogSource(p); source != "" {
			uri += "/" + source
		}
		return c.fs(uri, grainLogQuery(p, time.Now()))
	case "playbooks":
		if name := p.optArg(0); name != "" {
			return c.fs("src://playbooks/"+name, nil)
		}
		// One playbook name per line, as `grain playbooks <name>` takes it.
		var listing bytes.Buffer
		p, err := fsPath("src://playbooks")
		if err != nil {
			return err
		}
		if err := c.get(p, url.Values{"list": {""}}, &listing); err != nil {
			return err
		}
		for _, line := range strings.Split(strings.TrimSpace(listing.String()), "\n") {
			if name := strings.TrimSuffix(line, ".md"); name != "" && !strings.HasSuffix(name, "/") {
				fmt.Fprintln(c.out, name)
			}
		}
		return nil
	case "issue list":
		return c.get("/cli/issues", p.query("repo", "label", "limit"), c.out)
	case "issue read":
		n, err := p.arg(0, "number")
		if err != nil {
			return err
		}
		return c.get("/cli/issues/"+url.PathEscape(n), p.query("repo", "comments"), c.out)
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

// isPipelineChangesURI is whether uri names deploy://pipeline-changes, the
// one file --cursor and --page page through.
func isPipelineChangesURI(uri string) bool {
	scheme, rel, _ := strings.Cut(uri, "://")
	rel = strings.Trim(rel, "/")
	return scheme == "deploy" && (rel == "pipeline-changes" || rel == "pipeline-changes.md")
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

// latestRun is the newest grain CI run on this task's branch, or on task's
// when one is named.
func (c *sandboxCLI) latestRun(task string) (string, error) {
	q := url.Values{"ref": {"@branch"}, "limit": {"1"}}
	what := "this task's branch"
	if task != "" {
		q = url.Values{"task": {task}, "limit": {"1"}}
		what = "task " + strings.TrimPrefix(task, "#") + "'s branch"
	}
	var buf strings.Builder
	if err := c.get("/cli/ci/runs", q, &buf); err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) < 2 {
		return "", fmt.Errorf("%s has no grain CI run yet", what)
	}
	id, _, _ := strings.Cut(lines[1], " ")
	return id, nil
}

// ciRun is the run a ci log or ci artifact command reads and the
// repository it is in: the run named (or the latest on --task's branch,
// or on this task's), in --repo or wherever the proxy finds it.
func (c *sandboxCLI) ciRun(p sandboxArgs, run string) (repo, id string, err error) {
	rc, err := c.context()
	if err != nil {
		return "", "", err
	}
	if run == "" {
		if run, err = c.latestRun(p.get("task")); err != nil {
			return "", "", err
		}
	}
	if repo = p.get("repo"); repo != "" {
		return repo, run, nil
	}
	var buf strings.Builder
	if err := c.get("/cli/ci/run", url.Values{"id": {run}}, &buf); err != nil {
		if rc.Repo == "" {
			return "", "", err
		}
		// A proxy that cannot say is asked about this task's own
		// repository, as before it could.
		return rc.Repo, run, nil
	}
	var named struct {
		Repo string `json:"repo"`
	}
	if err := json.Unmarshal([]byte(buf.String()), &named); err != nil || named.Repo == "" {
		if rc.Repo == "" {
			return "", "", fmt.Errorf("finding the repository of CI run %s: %q", run, buf.String())
		}
		return rc.Repo, run, nil
	}
	return named.Repo, run, nil
}

func (c *sandboxCLI) ciLog(p sandboxArgs) error {
	repo, run, err := c.ciRun(p, p.optArg(0))
	if err != nil {
		return err
	}
	uri := "ci://" + repo + "/" + run
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
	return c.read(uri+"/"+step, p, false)
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
	repo, run, err := c.ciRun(p, p.get("run"))
	if err != nil {
		return err
	}
	path, err := fsPath("ci://" + repo + "/" + run + "/artifacts/" + job + "/" + name)
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
	multiFlags map[string][]string
}

// sandboxBoolFlags take no value.
var sandboxBoolFlags = map[string]bool{
	"i": true, "full": true, "json": true, "all": true, "comments": true,
	"no-repo": true, "fixes-base-branch": true, "no-port": true, "keep-running": true,
	"no-follow-up": true, "park": true, "auto-merge": true,
	"hold-for-approval": true, "at-front": true, "stalled": true,
}

func parseSandboxArgs(args []string) (sandboxArgs, error) {
	p := sandboxArgs{flags: map[string]string{}, multiFlags: map[string][]string{}}
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
			p.multiFlags[k] = append(p.multiFlags[k], v)
			continue
		}
		if sandboxBoolFlags[name] {
			p.flags[name] = "1"
			p.multiFlags[name] = append(p.multiFlags[name], "1")
			continue
		}
		if i+1 >= len(args) {
			p.flags[name] = ""
			return p, fmt.Errorf("flag %s needs a value", a)
		}
		p.flags[name] = args[i+1]
		p.multiFlags[name] = append(p.multiFlags[name], args[i+1])
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

func (p sandboxArgs) getAll(name string) []string { return p.multiFlags[name] }

func (p sandboxArgs) has(name string) bool { _, ok := p.flags[name]; return ok }

// only refuses any flag not among names, for a command whose every flag
// narrows its answer: one it ignored would answer a question nobody asked.
func (p sandboxArgs) only(cmd string, names ...string) error {
	given := make([]string, 0, len(p.flags))
	for name := range p.flags {
		given = append(given, name)
	}
	sort.Strings(given)
	for _, name := range given {
		if slices.Contains(names, name) {
			continue
		}
		takes := "it takes none"
		if len(names) > 0 {
			sorted := slices.Sorted(slices.Values(names))
			for i, n := range sorted {
				sorted[i] = flagName(n)
			}
			takes = "flags: " + strings.Join(sorted, ", ")
		}
		return usageError{fmt.Sprintf("grain %s: unknown flag %s (%s); run 'grain %s --help' for usage\n", cmd, flagName(name), takes, cmd)}
	}
	return nil
}

// flagName is name as it is written on a command line: -i, --tail.
func flagName(name string) string {
	if len(name) == 1 {
		return "-" + name
	}
	return "--" + name
}

// grainLogQuery is `grain log`'s flags as deploy://grain.log's query:
// the window, priority, substring and line cap read_grain_log takes, and
// --tail, which the namespace applies to whatever that answers.
//
// read_grain_log takes RFC 3339 instants and nothing else, but every
// other window flag this CLI has (ci list --since, task list
// --changed-since) also takes a span back from now, so `grain log
// --since 10m` was refused while `grain ci list --since 10m` was not.
// A --since or --until the CLI can read (ci.ParseSince) is sent as the
// instant it names, relative to now; one it cannot is sent as given, for
// the tool to refuse in its own words.
func grainLogQuery(p sandboxArgs, now time.Time) url.Values {
	q := p.query("tail", "contains")
	for _, name := range []string{"since", "until"} {
		v := p.get(name)
		if v == "" {
			continue
		}
		if at, err := ci.ParseSince(v, now); err == nil && !at.IsZero() {
			v = at.UTC().Format(time.RFC3339)
		}
		q.Set(name, v)
	}
	for _, name := range []string{"priority", "min-priority"} {
		if v := p.get(name); v != "" {
			q.Set("min_priority", v)
		}
	}
	for _, name := range []string{"max-lines", "lines", "n"} {
		if v := p.get(name); v != "" {
			q.Set("max_lines", v)
		}
	}
	return q
}

// grainLogSource is the journal `grain log` reads: --source when given,
// else its first argument, else "" for the daemon's own.
func grainLogSource(p sandboxArgs) string {
	if v := p.get("source"); v != "" {
		return v
	}
	return p.optArg(0)
}

// metricsQuery is `grain metrics`' flags as deploy://metrics' query;
// --repo and --author may each be repeated or comma-separated.
func metricsQuery(p sandboxArgs) url.Values {
	q := p.query("window", "since", "until")
	for _, name := range []string{"repo", "author"} {
		for _, v := range p.getAll(name) {
			q.Add(name, v)
		}
	}
	return q
}

// query copies the named flags into a query; "list" is always set bare.
func (p sandboxArgs) query(names ...string) url.Values {
	q := url.Values{}
	for _, n := range names {
		if n == "list" {
			q.Set("list", "")
			continue
		}
		if v, ok := p.flags[n]; ok && n != "" {
			q.Set(n, v)
		}
	}
	return q
}

// lastLines is --lines (or -n): how many of a file's last lines to print,
// or 0 for all of what the byte range answers.
func (p sandboxArgs) lastLines() int {
	for _, name := range []string{"lines", "n"} {
		if v := p.get(name); v != "" {
			n, _ := strconv.Atoi(v)
			return n
		}
	}
	return 0
}

// read prints a namespace file: the byte range --tail, --offset and --limit
// name (the namespace's own), then only its last --lines lines.
// With sayingSize it says on stderr how big the whole file is, too.
func (c *sandboxCLI) read(uri string, p sandboxArgs, sayingSize bool) error {
	path, err := fsPath(uri)
	if err != nil {
		return err
	}
	resp, err := c.do(path, p.query("tail", "offset", "limit", "cursor", "since", "page"))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		fmt.Fprintln(c.errOut, "grain: "+strings.TrimSpace(string(body)))
		return errSandboxReported
	}
	if size := resp.Header.Get("X-Grain-Total-Size"); sayingSize && size != "" {
		fmt.Fprintf(c.errOut, "grain: %s is %s bytes; --tail, --offset and --limit take bytes, --lines N the last N lines\n", uri, size)
	}
	if !p.has("lines") && !p.has("n") {
		_, err = io.Copy(c.out, resp.Body)
		return err
	}
	tw := &tailWriter{n: p.lastLines()}
	if _, err := io.Copy(tw, resp.Body); err != nil {
		return err
	}
	_, err = c.out.Write(tw.bytes())
	return err
}

// tailWriter keeps the last n lines written to it.
type tailWriter struct {
	n     int
	lines [][]byte
	// part is the line still being written.
	part []byte
}

func (t *tailWriter) Write(b []byte) (int, error) {
	written := len(b)
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			t.part = append(t.part, b...)
			break
		}
		t.part = append(t.part, b[:i+1]...)
		t.lines = append(t.lines, t.part)
		t.part = nil
		if len(t.lines) > t.n {
			t.lines = t.lines[len(t.lines)-t.n:]
		}
		b = b[i+1:]
	}
	return written, nil
}

func (t *tailWriter) bytes() []byte {
	lines := t.lines
	if len(t.part) > 0 {
		lines = append(lines, t.part)
	}
	if len(lines) > t.n {
		lines = lines[len(lines)-t.n:]
	}
	return bytes.Join(lines, nil)
}
