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
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"gopkg.in/yaml.v3"
)

func (c *sandboxCLI) status(p sandboxArgs) error {
	note, err := c.textArg(p, "one-line-note")
	if err != nil {
		return err
	}
	return c.post("/cli/status", map[string]any{"status": strings.TrimSpace(note)}, c.out)
}

func (c *sandboxCLI) handoverWrite(p sandboxArgs) error {
	if p.get("file") == "" && len(p.positional) == 1 && p.positional[0] == "-" {
		p.flags["file"] = "-"
		p.positional = nil
	}
	body, err := c.textArg(p, "handover")
	if err != nil {
		return err
	}
	return c.post("/cli/handover", map[string]any{"handover": body}, c.out)
}

func (c *sandboxCLI) notify(p sandboxArgs) error {
	subject := strings.TrimSpace(p.get("subject"))
	if subject == "" {
		return fmt.Errorf("--subject is required: a short key for what this notification is about")
	}
	title := strings.TrimSpace(p.get("title"))
	if title == "" {
		return fmt.Errorf("--title is required: one line the operator reads first")
	}
	body := p.get("body")
	if body == "" && (p.get("file") != "" || len(p.positional) > 0) {
		var err error
		body, err = c.textArg(p, "body")
		if err != nil {
			return err
		}
	}
	req := gitproxy.CLINotifyRequest{
		Subject:  subject,
		Title:    title,
		Body:     body,
		Severity: strings.TrimSpace(p.get("severity")),
		Task:     strings.TrimSpace(p.get("task")),
	}
	if rawClear := strings.TrimSpace(p.get("clears-when")); rawClear != "" {
		var clear model.NotificationClear
		if err := json.Unmarshal([]byte(rawClear), &clear); err != nil {
			return fmt.Errorf("--clears-when must be valid JSON: %w", err)
		}
		req.ClearsWhen = &clear
	}
	var buf bytes.Buffer
	if err := c.post("/cli/notify", req, &buf); err != nil {
		return err
	}
	if p.has("json") {
		_, err := c.out.Write(buf.Bytes())
		return err
	}
	var res gitproxy.CLINotifyResult
	if err := json.Unmarshal(buf.Bytes(), &res); err != nil {
		_, _ = c.out.Write(buf.Bytes())
		return nil
	}
	verb := "Updated"
	if res.New {
		verb = "Raised"
	}
	fmt.Fprintf(c.out, "%s %s notification %q (#%d, count %d).\n", verb, res.Severity, res.Subject, res.ID, res.Count)
	return nil
}

func (c *sandboxCLI) taskMessage(p sandboxArgs) error {
	id, err := p.arg(0, "id")
	if err != nil {
		return err
	}
	body := p.get("body")
	if body == "" {
		rest := p
		rest.positional = p.positional[1:]
		body, err = c.textArg(rest, "body")
		if err != nil {
			return err
		}
	}
	return c.post("/cli/tasks/"+url.PathEscape(id)+"/notes", map[string]any{"body": body}, c.out)
}

func (c *sandboxCLI) taskWait(p sandboxArgs) error {
	if p.has("park") || strings.TrimSpace(p.get("resume-at")) != "" {
		return fmt.Errorf("parking a task ends the run and is a model-only control operation: call the wait_for_task (park: true) or resume_later MCP tool instead")
	}
	if len(p.positional) == 0 {
		return fmt.Errorf("missing <id>...: name at least one task to wait on")
	}
	timeout, err := parseWaitTimeout(p.get("timeout"), c.getenv)
	if err != nil {
		return err
	}
	req := gitproxy.CLITaskWaitRequest{
		IDs:            p.positional,
		Until:          strings.TrimSpace(p.get("until")),
		TimeoutSeconds: int(timeout / time.Second),
	}
	var buf bytes.Buffer
	if err := c.post("/cli/tasks/wait", req, &buf); err != nil {
		return err
	}
	if p.has("json") {
		_, err := c.out.Write(buf.Bytes())
		return err
	}
	var ans gitproxy.CLITaskWaitResponse
	if err := json.Unmarshal(buf.Bytes(), &ans); err != nil {
		return fmt.Errorf("reading the proxy's answer: %w", err)
	}
	waited := (time.Duration(ans.WaitedMS) * time.Millisecond).Round(time.Second)
	credit := ""
	if ans.CreditLive {
		credit = fmt.Sprintf("; wait credit used: %s / %s",
			(time.Duration(ans.CreditMS) * time.Millisecond).Round(time.Second),
			time.Duration(ans.CreditCapMS)*time.Millisecond)
	}
	fmt.Fprintf(c.out, "Task wait (%s, waited %s%s):\n%s", ans.Status, waited, credit, ans.Summary)
	if !strings.HasSuffix(ans.Summary, "\n") {
		fmt.Fprintln(c.out)
	}
	if ans.Status != "settled" {
		fmt.Fprintf(c.out, "To keep waiting in this run:  grain task wait %s --until %s\n", strings.Join(p.positional, " "), ans.Until)
		fmt.Fprintf(c.out, "To park and free the VM:      call wait_for_task with park: true (or resume_later)\n")
	}
	return nil
}

func (c *sandboxCLI) taskPropose(p sandboxArgs) error {
	if strings.TrimSpace(p.get("supersedes")) != "" {
		return fmt.Errorf("superseding an existing task is not permitted on the sandbox credential: use the propose_task MCP control tool instead")
	}
	if v := strings.TrimSpace(p.get("auto-merge")); v != "" && v != "false" && v != "0" {
		return fmt.Errorf("auto_merge is not permitted on the sandbox credential: grain task propose always files unapproved with auto_merge=false; use the propose_task MCP control tool to inherit auto-merge")
	}
	if v := strings.TrimSpace(p.get("hold-for-approval")); v == "false" || v == "0" {
		return fmt.Errorf("hold_for_approval=false is not permitted on the sandbox credential: grain task propose always holds proposals for human approval; use the propose_task MCP control tool to inherit auto-approval")
	}
	title := strings.TrimSpace(p.get("title"))
	if title == "" {
		return fmt.Errorf("--title is required: run 'grain help propose' for usage")
	}
	body := strings.TrimSpace(p.get("body"))
	if body == "" && (p.get("file") != "" || len(p.positional) > 0) {
		var err error
		body, err = c.textArg(p, "body")
		if err != nil {
			return err
		}
	}
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("--body (or --file) is required: run 'grain help propose' for usage")
	}
	hold := true
	auto := false
	req := gitproxy.CLIProposeRequest{
		ID:              strings.TrimSpace(p.get("id")),
		Title:           title,
		Body:            body,
		Repo:            strings.TrimSpace(p.get("repo")),
		Branch:          strings.TrimSpace(p.get("branch")),
		NoRepo:          p.has("no-repo"),
		DependsOn:       splitMultiFlag(p, "depends-on"),
		Capabilities:    splitMultiFlag(p, "capability"),
		FixesBaseBranch: p.has("fixes-base-branch"),
		NoPort:          p.has("no-port"),
		KeepRunning:     p.has("keep-running"),
		Origin:          strings.TrimSpace(p.get("origin")),
		Reads:           splitMultiFlag(p, "read"),
		HoldForApproval: &hold,
		AutoMerge:       &auto,
	}
	if cfs := p.getAll("custom-field"); len(cfs) > 0 {
		req.CustomFields = map[string][]string{}
		for _, cf := range cfs {
			k, v, ok := strings.Cut(cf, "=")
			if !ok || strings.TrimSpace(k) == "" {
				return fmt.Errorf("--custom-field %q must be key=value", cf)
			}
			req.CustomFields[strings.TrimSpace(k)] = append(req.CustomFields[strings.TrimSpace(k)], v)
		}
	}
	if nb := strings.TrimSpace(p.get("not-before")); nb != "" {
		t, err := parseNotBeforeFlag(nb)
		if err != nil {
			return err
		}
		req.NotBefore = &t
	}
	if rawPri := strings.TrimSpace(p.get("priority")); rawPri != "" {
		pri, err := strconv.Atoi(rawPri)
		if err != nil || pri < 0 {
			return fmt.Errorf("--priority %q must be a non-negative integer", rawPri)
		}
		req.Priority = &pri
	}
	var buf bytes.Buffer
	if err := c.post("/cli/proposals", req, &buf); err != nil {
		return err
	}
	if p.has("json") {
		_, err := c.out.Write(buf.Bytes())
		return err
	}
	var res gitproxy.CLIProposeResult
	if err := json.Unmarshal(buf.Bytes(), &res); err != nil {
		_, _ = c.out.Write(buf.Bytes())
		return nil
	}
	fmt.Fprintf(c.out, "Proposed task #%s (%s) — held for human approval (auto_merge=false).\n", res.ID, res.URL)
	if res.ApprovalNote != "" {
		fmt.Fprintln(c.out, res.ApprovalNote)
	}
	return nil
}

func splitMultiFlag(p sandboxArgs, name string) []string {
	var out []string
	for _, raw := range p.getAll(name) {
		for _, item := range strings.Split(raw, ",") {
			if s := strings.TrimSpace(item); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func parseNotBeforeFlag(raw string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("--not-before %q must be YYYY-MM-DD or RFC 3339 (e.g. 2026-10-14T09:00:00Z)", raw)
}

func (c *sandboxCLI) apiCommand(p sandboxArgs) error {
	first, err := p.arg(0, "routes | METHOD")
	if err != nil {
		return err
	}
	if first == "routes" {
		var buf bytes.Buffer
		if err := c.get("/cli/api-routes", p.query("scope"), &buf); err != nil {
			return err
		}
		if p.has("json") {
			_, err := c.out.Write(buf.Bytes())
			return err
		}
		var routes []gitproxy.CLIAPIRoute
		if err := json.Unmarshal(buf.Bytes(), &routes); err != nil {
			_, _ = c.out.Write(buf.Bytes())
			return nil
		}
		if len(routes) == 0 {
			fmt.Fprintln(c.out, "No agent API routes are granted to this attempt.")
			return nil
		}
		for _, rt := range routes {
			reason := ""
			if rt.NeedsReason {
				reason = " [--reason required]"
			}
			fmt.Fprintf(c.out, "%-6s %-42s (%s)%s\n", rt.Method, rt.Path, rt.Scope, reason)
		}
		return nil
	}
	method := strings.ToUpper(strings.TrimSpace(first))
	apiPath, err := p.arg(1, "path")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(apiPath, "/api/") && apiPath != "/api" {
		return fmt.Errorf("agent API path must start with /api/ (got %q)", apiPath)
	}
	var rawBody []byte
	if b := p.get("body"); b != "" {
		rawBody = []byte(b)
	} else if f := p.get("file"); f != "" {
		if f == "-" {
			rawBody, err = io.ReadAll(c.stdin())
		} else {
			rawBody, err = os.ReadFile(f)
		}
		if err != nil {
			return err
		}
	} else if fields := p.getAll("field"); len(fields) > 0 {
		obj := map[string]any{}
		for _, kv := range fields {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || strings.TrimSpace(k) == "" {
				return fmt.Errorf("--field %q must be key=value", kv)
			}
			var parsed any
			if json.Unmarshal([]byte(v), &parsed) == nil && (strings.HasPrefix(v, "{") || strings.HasPrefix(v, "[") || v == "true" || v == "false" || v == "null") {
				obj[strings.TrimSpace(k)] = parsed
			} else {
				obj[strings.TrimSpace(k)] = v
			}
		}
		rawBody, err = json.Marshal(obj)
		if err != nil {
			return err
		}
	}
	return c.doAPI(method, apiPath, rawBody, p.get("reason"), c.out)
}

func (c *sandboxCLI) doAPI(method, path string, rawBody []byte, reason string, w io.Writer) error {
	var bodyReader io.Reader
	if len(rawBody) > 0 {
		bodyReader = bytes.NewReader(rawBody)
	}
	req, err := http.NewRequest(method, c.base+path, bodyReader)
	if err != nil {
		return err
	}
	if len(rawBody) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(reason) != "" {
		req.Header.Set("X-Grain-Acting-Reason", strings.TrimSpace(reason))
		req.Header.Set("X-Grain-Reason", strings.TrimSpace(reason))
	}
	if c.token != "" {
		req.SetBasicAuth("x-access-token", c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("reaching the sandbox proxy at %s: %w", c.base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		fmt.Fprintln(c.errOut, "grain: "+strings.TrimSpace(string(msg)))
		return errSandboxReported
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		_, _ = fmt.Fprintln(w)
	}
	return nil
}

func (c *sandboxCLI) memoryWrite(p sandboxArgs) error {
	repo := strings.TrimSpace(p.get("repo"))
	rest := p
	if repo == "" && len(p.positional) >= 2 && strings.Contains(p.positional[0], "/") && !strings.Contains(p.positional[0], " ") {
		repo = p.positional[0]
		rest.positional = p.positional[1:]
	} else if repo == "" && p.get("file") != "" && len(p.positional) == 1 && strings.Contains(p.positional[0], "/") {
		repo = p.positional[0]
		rest.positional = nil
	}
	text, err := c.textArg(rest, "text")
	if err != nil {
		return err
	}
	raw, err := json.Marshal(map[string]any{"repo": repo, "text": text})
	if err != nil {
		return err
	}
	return c.doAPI(http.MethodPut, "/api/memory", raw, p.get("reason"), c.out)
}

func (c *sandboxCLI) memoryDocWrite(p sandboxArgs) error {
	name, err := p.arg(0, "name")
	if err != nil {
		return err
	}
	rest := p
	rest.positional = p.positional[1:]
	text, err := c.textArg(rest, "text")
	if err != nil {
		return err
	}
	summary := strings.TrimSpace(p.get("summary"))
	if summary == "" {
		firstLine, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
		summary = strings.TrimLeft(firstLine, "# ")
		if len(summary) > 120 {
			summary = summary[:120]
		}
	}
	raw, err := json.Marshal(map[string]any{
		"repo":    strings.TrimSpace(p.get("repo")),
		"name":    name,
		"summary": summary,
		"text":    text,
	})
	if err != nil {
		return err
	}
	return c.doAPI(http.MethodPut, "/api/memory/documents", raw, p.get("reason"), c.out)
}

func (c *sandboxCLI) releaseNotesStats(p sandboxArgs) error {
	period := strings.TrimSpace(p.get("period"))
	if period == "" {
		period = strings.TrimSpace(p.optArg(0))
	}
	q := url.Values{}
	if period != "" {
		q.Set("period", period)
	}
	path := "/api/release-notes/stats"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	return c.doAPI(http.MethodGet, path, nil, "", c.out)
}

func (c *sandboxCLI) releaseNotesPublish(p sandboxArgs) error {
	period := strings.TrimSpace(p.get("period"))
	if period == "" {
		period = strings.TrimSpace(p.get("version"))
	}
	title := strings.TrimSpace(p.get("title"))
	if title == "" {
		title = strings.TrimSpace(p.get("version"))
	}
	text, err := c.textArg(p, "markdown")
	if err != nil {
		return err
	}
	if title == "" {
		firstLine, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
		title = strings.TrimLeft(firstLine, "# ")
	}
	raw, err := json.Marshal(map[string]any{
		"period": period,
		"title":  title,
		"text":   text,
	})
	if err != nil {
		return err
	}
	return c.doAPI(http.MethodPut, "/api/release-notes", raw, p.get("reason"), c.out)
}

func (c *sandboxCLI) quarantine(p sandboxArgs) error {
	var testName string
	switch len(p.positional) {
	case 0:
		return fmt.Errorf("missing <test-name>: run 'grain quarantine --help' for usage")
	case 1:
		testName = strings.TrimSpace(p.positional[0])
	default:
		pkg := strings.TrimSpace(p.positional[0])
		tname := strings.TrimSpace(p.positional[1])
		if pkg != "" && !strings.Contains(tname, ".") {
			testName = pkg + "." + tname
		} else {
			testName = tname
		}
	}
	reason := strings.TrimSpace(p.get("reason"))
	if reason == "" {
		return fmt.Errorf("--reason is required: explain why the test is being quarantined")
	}
	ttl := strings.TrimSpace(p.get("ttl"))
	if ttl == "" {
		ttl = "30d"
	}
	fileFollowUp := !p.has("no-follow-up")
	req := gitproxy.CLIQuarantineRequest{
		TestName:     testName,
		Reason:       reason,
		TTL:          ttl,
		FileFollowUp: &fileFollowUp,
	}
	var buf bytes.Buffer
	if err := c.post("/cli/quarantine", req, &buf); err != nil {
		return err
	}
	var ans gitproxy.CLIQuarantineResult
	if err := json.Unmarshal(buf.Bytes(), &ans); err != nil {
		return fmt.Errorf("reading the proxy's answer: %w", err)
	}

	dir := p.get("C")
	if dir == "" {
		dir = "."
	}
	qPath := filepath.Join(dir, ".grain", "quarantine.yml")
	var qCfg ci.QuarantineConfig
	raw, _ := os.ReadFile(qPath)
	if len(raw) == 0 {
		raw, _ = os.ReadFile(filepath.Join(dir, ".grain", "quarantine.yaml"))
	}
	if len(raw) > 0 {
		if parsed, err := ci.LoadQuarantine(raw); err == nil && parsed != nil {
			qCfg = *parsed
		}
	}
	now := time.Now().UTC()
	var expiresAt *time.Time
	if dur, err := parseCLIQuarantineTTL(ttl); err == nil && dur > 0 {
		exp := now.Add(dur)
		expiresAt = &exp
	}
	entry := ci.QuarantineEntry{
		TestName:       testName,
		Reason:         reason,
		TaskID:         ans.TaskID,
		FollowUpTaskID: ans.FollowUpTaskID,
		CreatedAt:      &now,
		ExpiresAt:      expiresAt,
		TTL:            ttl,
	}
	updated := false
	for i, e := range qCfg.Tests {
		if e.TestName == testName {
			qCfg.Tests[i] = entry
			updated = true
			break
		}
	}
	if !updated {
		qCfg.Tests = append(qCfg.Tests, entry)
	}
	outBytes, err := yaml.Marshal(qCfg)
	if err != nil {
		return fmt.Errorf("marshaling quarantine yaml: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(qPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(qPath, outBytes, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "Quarantined test %q in %s.\n", testName, qPath)
	if ans.FollowUpTaskID != "" {
		fmt.Fprintf(c.out, "Follow-up repair task filed: #%s\n", ans.FollowUpTaskID)
	}
	return nil
}

func parseCLIQuarantineTTL(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		var days int
		if _, err := fmt.Sscanf(strings.TrimSuffix(s, "d"), "%d", &days); err == nil {
			return time.Duration(days) * 24 * time.Hour, nil
		}
	}
	return time.ParseDuration(s)
}

// taskList is grain task list: /cli/tasks, list_grain_tasks one line per
// task. The count and elision come back in a header and go to stderr, so
// what reaches stdout is the task lines alone.
func (c *sandboxCLI) taskList(p sandboxArgs) error {
	if len(p.positional) > 0 {
		return fmt.Errorf("task list: unexpected argument %q: run 'grain task list --help' for usage", p.positional[0])
	}
	q := url.Values{}
	for _, v := range p.getAll("state") {
		q.Add("state", v)
	}
	for _, name := range []string{"repo", "search", "kind"} {
		if v := strings.TrimSpace(p.get(name)); v != "" {
			q.Set(name, v)
		}
	}
	if v := strings.TrimSpace(p.get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n <= 0 {
			return fmt.Errorf("task list: --limit %q is not a positive number", v)
		}
		q.Set("limit", v)
	}
	if v := strings.TrimSpace(p.get("changed-since")); v != "" {
		q.Set("changed_since", v)
	}
	if p.has("stalled") {
		q.Set("stalled", "1")
	}
	if p.has("json") {
		q.Set("format", "json")
	}
	resp, err := c.do("/cli/tasks", q)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		fmt.Fprintln(c.errOut, "grain: "+strings.TrimSpace(string(body)))
		return errSandboxReported
	}
	if _, err := io.Copy(c.out, resp.Body); err != nil {
		return err
	}
	if note := resp.Header.Get(gitproxy.TaskListNoteHeader); note != "" && !p.has("json") {
		fmt.Fprintln(c.errOut, note)
	}
	return nil
}
