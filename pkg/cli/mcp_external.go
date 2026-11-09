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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// mcp_external.go implements `grain mcp [--context <name>]` (#XC-31): the
// workstation-facing local stdio JSON-RPC 2.0 MCP server exposing the 13-tool
// external coordination roster over the CLI's own HTTPClient, validation, and
// context credentials.

var (
	// defaultMCPWaitSlice and maxMCPWaitSlice bound a single wait_for_task
	// tool call well below standard MCP client tool-call timeouts (2–5m),
	// returning {"status":"waiting","resume":true,...} when the wake condition
	// has not fired yet. Tests shorten these variables.
	defaultMCPWaitSlice = 90 * time.Second
	maxMCPWaitSlice     = 90 * time.Second
)

func cmdMCP(ctx context.Context, c *ui.HTTPClient, parentFS *flag.FlagSet, sel selectedContext, args []string) error {
	fs := flag.NewFlagSet("grain mcp", flag.ContinueOnError)
	ctxFlag := fs.String("context", sel.name, "the named context to use ($"+contextEnvVar+"; see `grain context list`)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if err := noArgs(fs, pos); err != nil {
		return err
	}
	c, err = resolveMCPClient(c, parentFS, sel, *ctxFlag)
	if err != nil {
		return err
	}
	return serveExternalMCP(ctx, c, os.Stdin, os.Stdout)
}

func resolveMCPClient(c *ui.HTTPClient, parentFS *flag.FlagSet, sel selectedContext, ctxFlag string) (*ui.HTTPClient, error) {
	if name := strings.TrimSpace(ctxFlag); name != "" && name != sel.name {
		targetFS := parentFS
		if targetFS == nil {
			targetFS = flag.NewFlagSet("grain", flag.ContinueOnError)
		}
		server := c.BaseURL
		nextSel, _, err := resolveTarget(targetFS, &server, name)
		if err != nil {
			return nil, err
		}
		nextClient := ui.NewHTTPClient(server)
		nextClient.HTTP = c.HTTP
		nextClient.RetryPolicy = c.RetryPolicy
		nextClient.Token = c.Token
		if flagWasSet(targetFS, "audience") {
			nextClient.Audience = c.Audience
		} else {
			nextClient.Audience = audienceDefault()
		}
		nextClient.User = c.User
		useContextCredential(nextClient, nextSel, tokenSource(c.Token))
		return nextClient, nil
	}
	return c, nil
}

// serveExternalMCP answers JSON-RPC on in/out until the client closes in.
// A closed stdin is how every stdio MCP client (Claude Code, Codex, Gemini
// CLI, Jetski) ends a session, so it is a clean exit rather than an error:
// reported as one, `grain mcp` printed "grain: EOF" and exited 1 at the end
// of every session, which a client shows as its server having crashed.
func serveExternalMCP(ctx context.Context, c *ui.HTTPClient, in io.Reader, out io.Writer) error {
	reg := newExternalMCPRegistry(c)
	if err := mcp.Serve(ctx, reg, bufio.NewReader(in), bufio.NewWriter(out)); !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func newExternalMCPRegistry(c *ui.HTTPClient) *mcp.Registry {
	r := mcp.NewRegistry()
	r.SetServerName("grain")
	r.Register(
		externalProposeTaskTool(c),
		externalReadTaskTool(c),
		externalListTasksTool(c),
		externalWaitForTaskTool(c),
		externalReplyToTaskTool(c),
		externalCommentOnTaskTool(c),
		externalInboxTool(c),
		externalCIRunsTool(c),
		externalCILogTool(c),
		externalMergeQueueTool(c),
		externalFSReadTool(c),
		externalFSGrepTool(c),
		externalAdoptPRTool(c),
	)
	return r
}

func mcpObjectSchema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func decodeMCPArgs(raw map[string]any, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

func mcpJSONResult(v any) mcp.Result {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.Result{Text: err.Error(), IsError: true}
	}
	return mcp.Result{Text: string(b)}
}

func mcpToolErr(err error) mcp.Result {
	if err == nil {
		return mcp.Result{Text: "ok"}
	}
	return mcp.Result{Text: err.Error(), IsError: true}
}

func cleanMCPTaskID(raw string) (string, error) {
	return taskID(strings.TrimPrefix(strings.TrimSpace(raw), "#"))
}

// 1. propose_task
func externalProposeTaskTool(c *ui.HTTPClient) mcp.Tool {
	type args struct {
		Title            string           `json:"title"`
		Body             string           `json:"body"`
		Description      string           `json:"description"`
		Repo             string           `json:"repo"`
		NoRepo           bool             `json:"no_repo"`
		Base             string           `json:"base"`
		Branch           string           `json:"branch"`
		ExternalRef      string           `json:"external_ref"`
		DependsOn        []string         `json:"depends_on"`
		Designs          []string         `json:"designs"`
		Design           string           `json:"design"`
		Priority         *int             `json:"priority"`
		CustomFields     map[string]any   `json:"custom_fields"`
		Capabilities     []string         `json:"capabilities"`
		Reads            []string         `json:"reads"`
		Writes           []ui.WriteTarget `json:"writes"`
		AutoMerge        bool             `json:"auto_merge"`
		Approved         bool             `json:"approved"`
		AtFront          *bool            `json:"at_front"`
		ReviewTemplateID string           `json:"review_template_id"`
		NetworkAccess    string           `json:"network_access"`
		Owner            string           `json:"owner"`
	}
	return mcp.Tool{
		Name:        "propose_task",
		Description: "File a new task on Grain (defaults to unapproved proposal unless the caller holds tasks:approve and sets approved: true).",
		InputSchema: mcpObjectSchema(map[string]any{
			"title":              map[string]any{"type": "string", "description": "Task title (required)."},
			"body":               map[string]any{"type": "string", "description": "Task description in Markdown."},
			"repo":               map[string]any{"type": "string", "description": "Target repository as owner/name."},
			"no_repo":            map[string]any{"type": "boolean", "description": "File without a target repository."},
			"base":               map[string]any{"type": "string", "description": "Target base branch if not default."},
			"depends_on":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"designs":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Committed design/spec paths."},
			"design":             map[string]any{"type": "string", "description": "Single committed design/spec path."},
			"priority":           map[string]any{"type": "integer"},
			"custom_fields":      map[string]any{"type": "object"},
			"capabilities":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"reads":              map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"writes":             map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"external_ref":       map[string]any{"type": "string"},
			"auto_merge":         map[string]any{"type": "boolean"},
			"approved":           map[string]any{"type": "boolean"},
			"at_front":           map[string]any{"type": "boolean"},
			"review_template_id": map[string]any{"type": "string"},
			"network_access":     map[string]any{"type": "string"},
		}, "title"),
		Handler: func(ctx context.Context, raw map[string]any) mcp.Result {
			var a args
			if err := decodeMCPArgs(raw, &a); err != nil {
				return mcpToolErr(fmt.Errorf("propose_task: %w", err))
			}
			title := strings.TrimSpace(a.Title)
			if title == "" {
				return mcpToolErr(errors.New("propose_task: title is required"))
			}
			desc := a.Body
			if desc == "" {
				desc = a.Description
			}
			base := strings.TrimSpace(a.Base)
			if base == "" {
				base = strings.TrimSpace(a.Branch)
			}
			designs := slices.Clone(a.Designs)
			if d := strings.TrimSpace(a.Design); d != "" && !slices.Contains(designs, d) {
				designs = append(designs, d)
			}

			var cfPairs []string
			for k, v := range a.CustomFields {
				switch val := v.(type) {
				case []any:
					for _, item := range val {
						cfPairs = append(cfPairs, fmt.Sprintf("%s=%v", k, item))
					}
				case []string:
					for _, item := range val {
						cfPairs = append(cfPairs, fmt.Sprintf("%s=%s", k, item))
					}
				default:
					cfPairs = append(cfPairs, fmt.Sprintf("%s=%v", k, val))
				}
			}
			customFields, _, err := customFieldValues(ctx, c, cfPairs)
			if err != nil {
				return mcpToolErr(err)
			}

			req := ui.CreateTaskRequest{
				Title:            title,
				Description:      desc,
				Repo:             strings.TrimSpace(a.Repo),
				NoRepo:           a.NoRepo,
				Base:             base,
				ExternalRef:      strings.TrimSpace(a.ExternalRef),
				AutoMerge:        a.AutoMerge,
				Reads:            a.Reads,
				Writes:           a.Writes,
				Approved:         a.Approved,
				AtFront:          a.AtFront,
				Priority:         a.Priority,
				ReviewTemplateID: strings.TrimSpace(a.ReviewTemplateID),
				CustomFields:     customFields,
				NetworkAccess:    strings.TrimSpace(a.NetworkAccess),
				DependsOn:        a.DependsOn,
				Owner:            strings.TrimSpace(a.Owner),
				Designs:          designs,
			}
			if len(a.Capabilities) > 0 {
				caps := slices.Clone(a.Capabilities)
				for _, id := range caps {
					if model.IsCapabilityRef(id) {
						if err := model.CheckCapabilityGrantSyntax(id); err != nil {
							return mcpToolErr(fmt.Errorf("propose_task capability %q: %w", id, err))
						}
					}
				}
				req.Capabilities = &caps
			}
			task, err := c.CreateTask(ctx, req)
			if err != nil {
				return mcpToolErr(err)
			}
			return mcpJSONResult(task)
		},
	}
}

// 2. read_grain_task
func externalReadTaskTool(c *ui.HTTPClient) mcp.Tool {
	type args struct {
		ID            string `json:"id"`
		TaskID        string `json:"task_id"`
		CommentsSince string `json:"comments_since"`
	}
	return mcp.Tool{
		Name:        "read_grain_task",
		Description: "Read one Grain task and its conversation, optionally incremental after comments_since (RFC 3339).",
		InputSchema: mcpObjectSchema(map[string]any{
			"id":             map[string]any{"type": "string", "description": "Task ID (e.g. '42' or '#42')."},
			"task_id":        map[string]any{"type": "string", "description": "Alias for id."},
			"comments_since": map[string]any{"type": "string", "description": "Only return conversation entries strictly after this RFC 3339 timestamp."},
		}),
		Handler: func(ctx context.Context, raw map[string]any) mcp.Result {
			var a args
			if err := decodeMCPArgs(raw, &a); err != nil {
				return mcpToolErr(fmt.Errorf("read_grain_task: %w", err))
			}
			rawID := a.ID
			if strings.TrimSpace(rawID) == "" {
				rawID = a.TaskID
			}
			id, err := cleanMCPTaskID(rawID)
			if err != nil {
				return mcpToolErr(err)
			}
			if sinceStr := strings.TrimSpace(a.CommentsSince); sinceStr != "" {
				since, err := time.Parse(time.RFC3339Nano, sinceStr)
				if err != nil {
					return mcpToolErr(fmt.Errorf("comments_since %q is not an RFC 3339 timestamp: %w", sinceStr, err))
				}
				detail, err := c.GetTaskCommentsSince(ctx, id, since)
				if err != nil {
					return mcpToolErr(err)
				}
				return mcpJSONResult(detail)
			}
			detail, err := c.GetTask(ctx, id)
			if err != nil {
				return mcpToolErr(err)
			}
			return mcpJSONResult(detail)
		},
	}
}

// 3. list_grain_tasks
func externalListTasksTool(c *ui.HTTPClient) mcp.Tool {
	type args struct {
		Repo            string   `json:"repo"`
		State           string   `json:"state"`
		Base            string   `json:"base"`
		Owner           string   `json:"owner"`
		Author          string   `json:"author"`
		Origin          string   `json:"origin"`
		ExternalRef     string   `json:"external_ref"`
		Search          string   `json:"search"`
		Filters         []string `json:"filters"`
		Filter          string   `json:"filter"`
		Sort            string   `json:"sort"`
		IncludeClosed   bool     `json:"include_closed"`
		IncludeDeferred bool     `json:"include_deferred"`
		Limit           int      `json:"limit"`
	}
	return mcp.Tool{
		Name:        "list_grain_tasks",
		Description: "List Grain tasks with the CLI's attribute and provenance filters (including derived_from:eq: and filed_via:has:).",
		InputSchema: mcpObjectSchema(map[string]any{
			"repo":             map[string]any{"type": "string", "description": "Filter by repository (owner/name)."},
			"state":            map[string]any{"type": "string", "description": "Filter by task state."},
			"owner":            map[string]any{"type": "string"},
			"author":           map[string]any{"type": "string"},
			"origin":           map[string]any{"type": "string"},
			"external_ref":     map[string]any{"type": "string"},
			"search":           map[string]any{"type": "string"},
			"filters":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Attribute or provenance filter clauses (attribute:comparison:value)."},
			"filter":           map[string]any{"type": "string", "description": "Single filter clause."},
			"sort":             map[string]any{"type": "string"},
			"include_closed":   map[string]any{"type": "boolean"},
			"include_deferred": map[string]any{"type": "boolean"},
			"limit":            map[string]any{"type": "integer"},
		}),
		Handler: func(ctx context.Context, raw map[string]any) mcp.Result {
			var a args
			if err := decodeMCPArgs(raw, &a); err != nil {
				return mcpToolErr(fmt.Errorf("list_grain_tasks: %w", err))
			}
			f := listFlags{
				state:       strings.TrimSpace(a.State),
				repo:        strings.TrimSpace(a.Repo),
				base:        strings.TrimSpace(a.Base),
				owner:       strings.TrimSpace(a.Owner),
				author:      strings.TrimSpace(a.Author),
				origin:      strings.TrimSpace(a.Origin),
				externalRef: strings.TrimSpace(a.ExternalRef),
				search:      strings.TrimSpace(a.Search),
				sortBy:      strings.TrimSpace(a.Sort),
			}
			for _, flt := range a.Filters {
				if strings.TrimSpace(flt) != "" {
					f.filters = append(f.filters, strings.TrimSpace(flt))
				}
			}
			if single := strings.TrimSpace(a.Filter); single != "" {
				f.filters = append(f.filters, single)
			}

			listConfig, configErr := c.TaskListConfig(ctx)
			var down *ui.DaemonUnavailableError
			if errors.As(configErr, &down) {
				return mcpToolErr(configErr)
			}
			attrs := builtinAttributes
			if configErr == nil {
				attrs = attributesFor(listConfig.CustomFields)
			} else if f.namesAnUndeclaredAttribute() {
				return mcpToolErr(fmt.Errorf("asking which fields this deployment declares: %w", configErr))
			}
			q, err := f.query(attrs)
			if err != nil {
				return mcpToolErr(err)
			}

			show := map[string]*bool{}
			if a.IncludeClosed {
				t := true
				show["closed"] = &t
			}
			if a.IncludeDeferred {
				t := true
				show["deferred"] = &t
			}
			explicitFilters := len(q.filters) > 0
			explicitVisibility := len(show) > 0
			usingDeploymentDefault := !explicitFilters && !explicitVisibility &&
				configErr == nil && listConfig.DefaultFilters != nil
			if usingDeploymentDefault {
				q.filters = append(q.filters, deploymentDefaultFilters(listConfig, attrs)...)
				q.hidden = hiddenFromDefaultFilters(q.filters)
			} else if !explicitFilters {
				q, err = withDefaultTaskKind(q, attrs)
				if err != nil {
					return mcpToolErr(err)
				}
			}
			needsHidden := !usingDeploymentDefault && !explicitFilters
			var settings ui.Settings
			if needsHidden && q.needsSettings(show) {
				settings = deploymentSettings(ctx, c)
			}
			if q.needsZone() {
				q.zone = time.UTC
			}
			if needsHidden {
				q.hidden = hiddenStates(q, show, settings)
			}
			normalizeOwnerConditions(ctx, c, &q, listConfig.ViewerOwner)
			tasks, err := c.ListTasks(ctx)
			if err != nil {
				return mcpToolErr(err)
			}
			matched := q.apply(tasks)
			if a.Limit > 0 && len(matched) > a.Limit {
				matched = matched[:a.Limit]
			}
			return mcpJSONResult(matched)
		},
	}
}

// 4. wait_for_task
func externalWaitForTaskTool(c *ui.HTTPClient) mcp.Tool {
	type args struct {
		TaskID         string   `json:"task_id"`
		TaskIDs        []string `json:"task_ids"`
		IDs            []string `json:"ids"`
		Until          string   `json:"until"`
		TimeoutSeconds float64  `json:"timeout_seconds"`
		Since          string   `json:"since"`
	}
	type waitToolResponse struct {
		Status     string `json:"status"`
		Met        bool   `json:"met"`
		Condition  string `json:"condition,omitempty"`
		Failed     bool   `json:"failed,omitempty"`
		Tasks      any    `json:"tasks"`
		Revision   string `json:"revision,omitempty"`
		Resume     bool   `json:"resume"`
		ResumeHint string `json:"resumeHint,omitempty"`
	}
	return mcp.Tool{
		Name:        "wait_for_task",
		Description: "Wait for one or more Grain tasks to reach a wake condition (closed, merged, awaiting-reply, ci-done, any), sliced below MCP client tool timeouts so callers can re-invoke when resume is true.",
		InputSchema: mcpObjectSchema(map[string]any{
			"task_id":         map[string]any{"type": "string", "description": "Task ID to wait on."},
			"task_ids":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Additional task IDs to wait on."},
			"until":           map[string]any{"type": "string", "description": "Comma-separated wake conditions: closed, merged, awaiting-reply, ci-done, any (default closed)."},
			"timeout_seconds": map[string]any{"type": "number", "description": "Maximum seconds to hold in this tool call (clamped to slice budget; re-call when resume is true)."},
			"since":           map[string]any{"type": "string", "description": "Revision cursor from a prior wait_for_task response."},
		}),
		Handler: func(ctx context.Context, raw map[string]any) mcp.Result {
			var a args
			if err := decodeMCPArgs(raw, &a); err != nil {
				return mcpToolErr(fmt.Errorf("wait_for_task: %w", err))
			}
			var ids []string
			addID := func(s string) {
				for _, part := range strings.Split(s, ",") {
					id := strings.TrimPrefix(strings.TrimSpace(part), "#")
					if id != "" && !slices.Contains(ids, id) {
						ids = append(ids, id)
					}
				}
			}
			addID(a.TaskID)
			for _, s := range a.TaskIDs {
				addID(s)
			}
			for _, s := range a.IDs {
				addID(s)
			}
			if len(ids) == 0 {
				return mcpToolErr(errors.New("wait_for_task: task_id or task_ids is required"))
			}

			rawUntil := strings.TrimSpace(a.Until)
			if rawUntil == "" {
				rawUntil = "closed"
			}
			var until []string
			for _, u := range strings.Split(rawUntil, ",") {
				u = strings.TrimSpace(u)
				if u == "" {
					continue
				}
				if !slices.Contains(ui.WaitUntilConditions, u) {
					return mcpToolErr(fmt.Errorf("wait_for_task: until %q is not one of %s", u, strings.Join(ui.WaitUntilConditions, ", ")))
				}
				until = append(until, u)
			}
			if len(until) == 0 {
				return mcpToolErr(errors.New("wait_for_task: until names no condition"))
			}

			budget := defaultMCPWaitSlice
			if a.TimeoutSeconds > 0 {
				budget = time.Duration(a.TimeoutSeconds * float64(time.Second))
			}
			if budget > maxMCPWaitSlice {
				budget = maxMCPWaitSlice
			}
			if budget <= 0 {
				budget = 50 * time.Millisecond
			}

			deadline := time.Now().Add(budget)
			since := strings.TrimSpace(a.Since)
			var last ui.TasksWaitResponse
			for {
				left := time.Until(deadline)
				if left <= 0 {
					break
				}
				slice := min(left, waitSlice)
				callCtx, cancel := context.WithTimeout(ctx, slice+30*time.Second)
				resp, err := c.WaitTasks(callCtx, ids, until, slice, since)
				cancel()
				if err != nil {
					return mcpToolErr(err)
				}
				last = resp
				if since == "" {
					since = resp.Revision
				}
				if resp.Met {
					status := "met"
					isErr := false
					if resp.Failed && failureCounts(until, resp.Condition) {
						status = "failed"
						isErr = true
					}
					res := mcpJSONResult(waitToolResponse{
						Status:    status,
						Met:       true,
						Condition: resp.Condition,
						Failed:    resp.Failed,
						Tasks:     resp.Tasks,
						Revision:  resp.Revision,
						Resume:    false,
					})
					res.IsError = isErr
					return res
				}
			}

			return mcpJSONResult(waitToolResponse{
				Status:     "waiting",
				Met:        false,
				Tasks:      last.Tasks,
				Revision:   last.Revision,
				Resume:     true,
				ResumeHint: fmt.Sprintf("call wait_for_task again with task_ids=%v, until=%q, since=%q to continue waiting", ids, strings.Join(until, ","), since),
			})
		},
	}
}

// 5. reply_to_task and 6. comment_on_task
func externalReplyToTaskTool(c *ui.HTTPClient) mcp.Tool {
	return externalTaskMessageTool(c, "reply_to_task",
		"Answer a task parked in awaiting-reply on a clarifying question and re-queue it (refuses secret requests with 409; never pass secrets here).",
		true)
}

func externalCommentOnTaskTool(c *ui.HTTPClient) mcp.Tool {
	return externalTaskMessageTool(c, "comment_on_task",
		"Post a general comment on a Grain task (use reply_to_task instead when answering a parked question).",
		false)
}

func externalTaskMessageTool(c *ui.HTTPClient, name, description string, isReply bool) mcp.Tool {
	type args struct {
		ID          string   `json:"id"`
		TaskID      string   `json:"task_id"`
		Body        string   `json:"body"`
		Attachments []string `json:"attachments"`
	}
	return mcp.Tool{
		Name:        name,
		Description: description,
		InputSchema: mcpObjectSchema(map[string]any{
			"id":          map[string]any{"type": "string", "description": "Task ID."},
			"task_id":     map[string]any{"type": "string", "description": "Alias for id."},
			"body":        map[string]any{"type": "string", "description": "Message body in Markdown."},
			"attachments": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional local file paths to attach."},
		}, "body"),
		Handler: func(ctx context.Context, raw map[string]any) mcp.Result {
			var a args
			if err := decodeMCPArgs(raw, &a); err != nil {
				return mcpToolErr(fmt.Errorf("%s: %w", name, err))
			}
			rawID := a.ID
			if strings.TrimSpace(rawID) == "" {
				rawID = a.TaskID
			}
			id, err := cleanMCPTaskID(rawID)
			if err != nil {
				return mcpToolErr(err)
			}
			attachments, err := loadMCPAttachments(a.Attachments)
			if err != nil {
				return mcpToolErr(err)
			}
			body := strings.TrimSpace(a.Body)
			if body == "" && len(attachments) == 0 {
				return mcpToolErr(fmt.Errorf("%s: body cannot be empty", name))
			}
			if isReply {
				err = c.Reply(ctx, id, body, attachments)
			} else {
				err = c.AddComment(ctx, id, body, attachments)
			}
			if err != nil {
				return mcpToolErr(err)
			}
			task, err := c.Task(ctx, id)
			if err != nil {
				return mcpToolErr(err)
			}
			return mcpJSONResult(task)
		},
	}
}
