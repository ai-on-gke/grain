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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/nsclient"
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

func cmdMCP(ctx context.Context, c *ui.HTTPClient, sel selectedContext, args []string) error {
	fs := flag.NewFlagSet("grain mcp", flag.ContinueOnError)
	ctxFlag := fs.String("context", sel.name, "the named context to use ($"+contextEnvVar+"; see `grain context list`)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if err := noArgs(fs, pos); err != nil {
		return err
	}
	// If cmdMCP was invoked directly with -context rather than through runCLI's
	// liftGlobalFlags, resolve that context's URL and credential.
	if name := strings.TrimSpace(*ctxFlag); name != "" && name != sel.name {
		dummyFS := flag.NewFlagSet("grain", flag.ContinueOnError)
		server := c.BaseURL
		nextSel, _, err := resolveTarget(dummyFS, &server, name)
		if err != nil {
			return err
		}
		nextClient := ui.NewHTTPClient(server)
		nextClient.HTTP = c.HTTP
		nextClient.RetryPolicy = c.RetryPolicy
		useContextCredential(nextClient, nextSel, "")
		c = nextClient
	}
	return serveExternalMCP(ctx, c, os.Stdin, os.Stdout)
}

func serveExternalMCP(ctx context.Context, c *ui.HTTPClient, in io.Reader, out io.Writer) error {
	reg := newExternalMCPRegistry(c)
	return mcp.Serve(ctx, reg, bufio.NewReader(in), bufio.NewWriter(out))
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

// 5. reply_to_task
func externalReplyToTaskTool(c *ui.HTTPClient) mcp.Tool {
	type args struct {
		ID          string   `json:"id"`
		TaskID      string   `json:"task_id"`
		Body        string   `json:"body"`
		Attachments []string `json:"attachments"`
	}
	return mcp.Tool{
		Name:        "reply_to_task",
		Description: "Answer a task parked in awaiting-reply on a clarifying question and re-queue it (refuses secret requests with 409; never pass secrets here).",
		InputSchema: mcpObjectSchema(map[string]any{
			"id":          map[string]any{"type": "string", "description": "Task ID."},
			"task_id":     map[string]any{"type": "string", "description": "Alias for id."},
			"body":        map[string]any{"type": "string", "description": "Answer text."},
			"attachments": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional local file paths to attach."},
		}, "body"),
		Handler: func(ctx context.Context, raw map[string]any) mcp.Result {
			var a args
			if err := decodeMCPArgs(raw, &a); err != nil {
				return mcpToolErr(fmt.Errorf("reply_to_task: %w", err))
			}
			rawID := a.ID
			if strings.TrimSpace(rawID) == "" {
				rawID = a.TaskID
			}
			id, err := cleanMCPTaskID(rawID)
			if err != nil {
				return mcpToolErr(err)
			}
			attachments, err := loadAttachments(a.Attachments)
			if err != nil {
				return mcpToolErr(err)
			}
			body := strings.TrimSpace(a.Body)
			if body == "" && len(attachments) == 0 {
				return mcpToolErr(errors.New("reply_to_task: body cannot be empty"))
			}
			if err := c.Reply(ctx, id, body, attachments); err != nil {
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

// 6. comment_on_task
func externalCommentOnTaskTool(c *ui.HTTPClient) mcp.Tool {
	type args struct {
		ID          string   `json:"id"`
		TaskID      string   `json:"task_id"`
		Body        string   `json:"body"`
		Attachments []string `json:"attachments"`
	}
	return mcp.Tool{
		Name:        "comment_on_task",
		Description: "Post a general comment on a Grain task (use reply_to_task instead when answering a parked question).",
		InputSchema: mcpObjectSchema(map[string]any{
			"id":          map[string]any{"type": "string", "description": "Task ID."},
			"task_id":     map[string]any{"type": "string", "description": "Alias for id."},
			"body":        map[string]any{"type": "string", "description": "Comment body in Markdown."},
			"attachments": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional local file paths to attach."},
		}, "body"),
		Handler: func(ctx context.Context, raw map[string]any) mcp.Result {
			var a args
			if err := decodeMCPArgs(raw, &a); err != nil {
				return mcpToolErr(fmt.Errorf("comment_on_task: %w", err))
			}
			rawID := a.ID
			if strings.TrimSpace(rawID) == "" {
				rawID = a.TaskID
			}
			id, err := cleanMCPTaskID(rawID)
			if err != nil {
				return mcpToolErr(err)
			}
			attachments, err := loadAttachments(a.Attachments)
			if err != nil {
				return mcpToolErr(err)
			}
			body := strings.TrimSpace(a.Body)
			if body == "" && len(attachments) == 0 {
				return mcpToolErr(errors.New("comment_on_task: body cannot be empty"))
			}
			if err := c.AddComment(ctx, id, body, attachments); err != nil {
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

// 7. grain_inbox
func externalInboxTool(c *ui.HTTPClient) mcp.Tool {
	type args struct {
		Repo string `json:"repo"`
		Kind string `json:"kind"`
	}
	return mcp.Tool{
		Name:        "grain_inbox",
		Description: "List actionable items waiting for a person in Grain's inbox (questions, secret requests, notifications, held merges, proposals).",
		InputSchema: mcpObjectSchema(map[string]any{
			"repo": map[string]any{"type": "string", "description": "Filter by repository (owner/name)."},
			"kind": map[string]any{"type": "string", "description": "Filter by item kind: " + strings.Join(ui.InboxKinds, ", ") + "."},
		}),
		Handler: func(ctx context.Context, raw map[string]any) mcp.Result {
			var a args
			if err := decodeMCPArgs(raw, &a); err != nil {
				return mcpToolErr(fmt.Errorf("grain_inbox: %w", err))
			}
			kind := strings.TrimSpace(a.Kind)
			if kind != "" && !slices.Contains(ui.InboxKinds, kind) {
				return mcpToolErr(fmt.Errorf("grain_inbox: kind %q is not one of %s", kind, strings.Join(ui.InboxKinds, ", ")))
			}
			resp, err := c.Inbox(ctx, strings.TrimSpace(a.Repo), kind)
			if err != nil {
				return mcpToolErr(err)
			}
			return mcpJSONResult(resp)
		},
	}
}

// 8. ci_runs
func externalCIRunsTool(c *ui.HTTPClient) mcp.Tool {
	type args struct {
		Repo       string   `json:"repo"`
		Ref        string   `json:"ref"`
		Branch     string   `json:"branch"`
		Conclusion string   `json:"conclusion"`
		States     []string `json:"states"`
		Job        string   `json:"job"`
		Workflow   string   `json:"workflow"`
		Since      string   `json:"since"`
		Until      string   `json:"until"`
		Sort       string   `json:"sort"`
		Order      string   `json:"order"`
		Limit      int      `json:"limit"`
		Compact    bool     `json:"compact"`
	}
	return mcp.Tool{
		Name:        "ci_runs",
		Description: "List CI runs recorded by Grain with repository, branch/ref, conclusion, workflow, and time window filters.",
		InputSchema: mcpObjectSchema(map[string]any{
			"repo":       map[string]any{"type": "string", "description": "Repository as owner/name."},
			"ref":        map[string]any{"type": "string", "description": "Branch/ref substring filter."},
			"branch":     map[string]any{"type": "string", "description": "Alias for ref."},
			"conclusion": map[string]any{"type": "string", "description": "Comma-separated run states (" + strings.Join(ci.RunStates, ", ") + ")."},
			"job":        map[string]any{"type": "string"},
			"workflow":   map[string]any{"type": "string"},
			"since":      map[string]any{"type": "string", "description": "Duration (24h, 7d) or RFC 3339 / YYYY-MM-DD."},
			"until":      map[string]any{"type": "string"},
			"sort":       map[string]any{"type": "string"},
			"order":      map[string]any{"type": "string", "description": "desc or asc."},
			"limit":      map[string]any{"type": "integer", "description": "Maximum runs to return (default 20)."},
			"compact":    map[string]any{"type": "boolean"},
		}),
		Handler: func(ctx context.Context, raw map[string]any) mcp.Result {
			var a args
			if err := decodeMCPArgs(raw, &a); err != nil {
				return mcpToolErr(fmt.Errorf("ci_runs: %w", err))
			}
			ref := strings.TrimSpace(a.Ref)
			if ref == "" {
				ref = strings.TrimSpace(a.Branch)
			}
			order := strings.TrimSpace(a.Order)
			if order == "" {
				order = "desc"
			}
			repo := a.Repo
			job := a.Job
			workflow := a.Workflow
			since := a.Since
			until := a.Until
			sortBy := a.Sort
			fv := &ciListFlagValues{
				repo:     &repo,
				ref:      &ref,
				job:      &job,
				workflow: &workflow,
				since:    &since,
				until:    &until,
				sort:     &sortBy,
				order:    &order,
			}
			if a.Conclusion != "" {
				fv.conclusions = append(fv.conclusions, a.Conclusion)
			}
			fv.conclusions = append(fv.conclusions, a.States...)
			filter, err := fv.filter(time.Now())
			if err != nil {
				return mcpToolErr(err)
			}
			limit := a.Limit
			if limit <= 0 {
				limit = 20
			}
			if a.Compact {
				page, err := c.FindCIRunsCompact(ctx, filter, limit)
				if err != nil {
					return mcpToolErr(err)
				}
				return mcpJSONResult(page)
			}
			page, err := c.FindCIRuns(ctx, filter, limit)
			if err != nil {
				return mcpToolErr(err)
			}
			return mcpJSONResult(page)
		},
	}
}

// 9. ci_log
func externalCILogTool(c *ui.HTTPClient) mcp.Tool {
	type args struct {
		RunID string `json:"run_id"`
		ID    string `json:"id"`
		Repo  string `json:"repo"`
		Job   string `json:"job"`
		Step  string `json:"step"`
		Tail  int    `json:"tail"`
	}
	return mcp.Tool{
		Name:        "ci_log",
		Description: "Read a CI run's job and step log output by run_id (supports optional job/step filtering and line tailing).",
		InputSchema: mcpObjectSchema(map[string]any{
			"run_id": map[string]any{"type": "string", "description": "CI run ID."},
			"id":     map[string]any{"type": "string", "description": "Alias for run_id."},
			"repo":   map[string]any{"type": "string", "description": "Optional repository (owner/name) when reading via ci:// namespace."},
			"job":    map[string]any{"type": "string", "description": "Optional job ID or name."},
			"step":   map[string]any{"type": "string", "description": "Optional step name substring."},
			"tail":   map[string]any{"type": "integer", "description": "Return at most the last N lines per step/log."},
		}),
		Handler: func(ctx context.Context, raw map[string]any) mcp.Result {
			var a args
			if err := decodeMCPArgs(raw, &a); err != nil {
				return mcpToolErr(fmt.Errorf("ci_log: %w", err))
			}
			runID := strings.TrimSpace(a.RunID)
			if runID == "" {
				runID = strings.TrimSpace(a.ID)
			}
			if runID == "" {
				return mcpToolErr(errors.New("ci_log: run_id is required"))
			}
			run, err := c.CIRun(ctx, runID)
			if err != nil {
				if repo := strings.TrimSpace(a.Repo); repo != "" {
					uri := "ci://" + strings.Trim(repo, "/") + "/" + runID
					if j := strings.TrimSpace(a.Job); j != "" {
						uri += "/" + j + "/log"
					} else {
						uri += "/log"
					}
					var outBuf, errBuf bytes.Buffer
					rerr := fsClient(c, &outBuf, &errBuf).Read(uri, url.Values{}, nsclient.ReadOptions{LastLines: a.Tail})
					if rerr == nil {
						return mcp.Result{Text: outBuf.String()}
					}
				}
				return mcpToolErr(err)
			}

			jobFilter := strings.ToLower(strings.TrimSpace(a.Job))
			stepFilter := strings.ToLower(strings.TrimSpace(a.Step))
			type stepOut struct {
				Name       string `json:"name"`
				Conclusion string `json:"conclusion,omitempty"`
				ExitCode   int    `json:"exitCode"`
				Output     string `json:"output,omitempty"`
			}
			type jobOut struct {
				ID         string    `json:"id,omitempty"`
				Name       string    `json:"name"`
				Status     string    `json:"status,omitempty"`
				Conclusion string    `json:"conclusion,omitempty"`
				Steps      []stepOut `json:"steps,omitempty"`
			}
			var jobs []jobOut
			for _, j := range run.Jobs {
				if jobFilter != "" && !strings.EqualFold(j.JobID, jobFilter) && !strings.EqualFold(j.Name, jobFilter) && !strings.Contains(strings.ToLower(j.Name), jobFilter) {
					continue
				}
				jo := jobOut{
					ID:         j.JobID,
					Name:       j.Name,
					Status:     j.Status,
					Conclusion: string(j.Conclusion),
				}
				for _, s := range j.Steps {
					if stepFilter != "" && !strings.Contains(strings.ToLower(s.Name), stepFilter) {
						continue
					}
					outText := s.Output
					if a.Tail > 0 && outText != "" {
						lines := strings.Split(strings.TrimRight(outText, "\n"), "\n")
						if len(lines) > a.Tail {
							lines = lines[len(lines)-a.Tail:]
						}
						outText = strings.Join(lines, "\n")
					}
					jo.Steps = append(jo.Steps, stepOut{
						Name:       s.Name,
						Conclusion: string(s.Conclusion),
						ExitCode:   s.ExitCode,
						Output:     outText,
					})
				}
				jobs = append(jobs, jo)
			}
			return mcpJSONResult(map[string]any{
				"id":         run.ID,
				"repo":       run.Repo,
				"ref":        run.Ref,
				"commit":     run.Commit,
				"conclusion": run.Conclusion,
				"jobs":       jobs,
			})
		},
	}
}

// 10. merge_queue
func externalMergeQueueTool(c *ui.HTTPClient) mcp.Tool {
	type args struct {
		Repo string `json:"repo"`
	}
	return mcp.Tool{
		Name:        "merge_queue",
		Description: "Read Grain's merge queue (queued pull requests, batches, CI status, and held gates).",
		InputSchema: mcpObjectSchema(map[string]any{
			"repo": map[string]any{"type": "string", "description": "Optional repository filter (owner/name)."},
		}),
		Handler: func(ctx context.Context, raw map[string]any) mcp.Result {
			var a args
			if err := decodeMCPArgs(raw, &a); err != nil {
				return mcpToolErr(fmt.Errorf("merge_queue: %w", err))
			}
			q := url.Values{"format": []string{"json"}}
			if r := strings.TrimSpace(a.Repo); r != "" {
				if _, err := model.ParseRepo(r); err != nil {
					return mcpToolErr(fmt.Errorf("merge_queue repo: %w", err))
				}
				q.Set("repo", r)
			}
			var outBuf, errBuf bytes.Buffer
			if err := fsClient(c, &outBuf, &errBuf).Read(mergeQueueURI, q, nsclient.ReadOptions{}); err != nil {
				return mcpToolErr(err)
			}
			return mcp.Result{Text: strings.TrimSpace(outBuf.String())}
		},
	}
}

// 11. fs_read
func externalFSReadTool(c *ui.HTTPClient) mcp.Tool {
	type args struct {
		URI    string `json:"uri"`
		List   bool   `json:"list"`
		Glob   string `json:"glob"`
		Tail   int    `json:"tail"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
		Lines  int    `json:"lines"`
	}
	return mcp.Tool{
		Name:        "fs_read",
		Description: "Read or list a URI in Grain's namespace (ci://, task://, deploy://, config://, memory://, src://).",
		InputSchema: mcpObjectSchema(map[string]any{
			"uri":    map[string]any{"type": "string", "description": "Namespace URI to read or list (required)."},
			"list":   map[string]any{"type": "boolean", "description": "List directory entries as JSON instead of reading file bytes."},
			"glob":   map[string]any{"type": "string", "description": "With list: true, filter entries matching glob."},
			"tail":   map[string]any{"type": "integer", "description": "Read only the last N bytes."},
			"offset": map[string]any{"type": "integer", "description": "Start reading N bytes in."},
			"limit":  map[string]any{"type": "integer", "description": "Read at most N bytes."},
			"lines":  map[string]any{"type": "integer", "description": "Return only the last N lines."},
		}, "uri"),
		Handler: func(ctx context.Context, raw map[string]any) mcp.Result {
			var a args
			if err := decodeMCPArgs(raw, &a); err != nil {
				return mcpToolErr(fmt.Errorf("fs_read: %w", err))
			}
			uri := strings.TrimSpace(a.URI)
			if uri == "" {
				return mcpToolErr(errors.New("fs_read: uri is required"))
			}
			var outBuf, errBuf bytes.Buffer
			ns := fsClient(c, &outBuf, &errBuf)
			if a.List {
				q := nsclient.ListQuery(strings.TrimSpace(a.Glob))
				q.Set("format", "json")
				if err := ns.Get(uri, q); err != nil {
					return mcpToolErr(err)
				}
				return mcp.Result{Text: strings.TrimSpace(outBuf.String())}
			}
			q := url.Values{}
			if a.Tail > 0 {
				q.Set("tail", strconv.Itoa(a.Tail))
			}
			if a.Offset > 0 {
				q.Set("offset", strconv.Itoa(a.Offset))
			}
			if a.Limit > 0 {
				q.Set("limit", strconv.Itoa(a.Limit))
			}
			if err := ns.Read(uri, q, nsclient.ReadOptions{LastLines: a.Lines}); err != nil {
				return mcpToolErr(err)
			}
			return mcp.Result{Text: outBuf.String()}
		},
	}
}

// 12. fs_grep
func externalFSGrepTool(c *ui.HTTPClient) mcp.Tool {
	type args struct {
		Pattern      string `json:"pattern"`
		Regex        string `json:"regex"`
		URI          string `json:"uri"`
		IgnoreCase   bool   `json:"ignore_case"`
		ContextLines int    `json:"context_lines"`
		Glob         string `json:"glob"`
		MaxMatches   int    `json:"max_matches"`
	}
	return mcp.Tool{
		Name:        "fs_grep",
		Description: "Search Grain's namespace (ci://, task://, deploy://, config://, memory://, src://) by regular expression.",
		InputSchema: mcpObjectSchema(map[string]any{
			"pattern":       map[string]any{"type": "string", "description": "Regular expression to search for."},
			"regex":         map[string]any{"type": "string", "description": "Alias for pattern."},
			"uri":           map[string]any{"type": "string", "description": "Namespace URI to search under."},
			"ignore_case":   map[string]any{"type": "boolean"},
			"context_lines": map[string]any{"type": "integer"},
			"glob":          map[string]any{"type": "string"},
			"max_matches":   map[string]any{"type": "integer"},
		}, "uri"),
		Handler: func(ctx context.Context, raw map[string]any) mcp.Result {
			var a args
			if err := decodeMCPArgs(raw, &a); err != nil {
				return mcpToolErr(fmt.Errorf("fs_grep: %w", err))
			}
			pattern := a.Pattern
			if pattern == "" {
				pattern = a.Regex
			}
			if pattern == "" {
				return mcpToolErr(errors.New("fs_grep: pattern is required"))
			}
			uri := strings.TrimSpace(a.URI)
			if uri == "" {
				return mcpToolErr(errors.New("fs_grep: uri is required"))
			}
			opts := nsclient.GrepOptions{
				IgnoreCase: a.IgnoreCase,
				Glob:       strings.TrimSpace(a.Glob),
			}
			if a.ContextLines > 0 {
				opts.Context = strconv.Itoa(a.ContextLines)
			}
			if a.MaxMatches > 0 {
				opts.Max = strconv.Itoa(a.MaxMatches)
			}
			q := nsclient.GrepQuery(pattern, opts)
			var outBuf, errBuf bytes.Buffer
			ns := fsClient(c, &outBuf, &errBuf)
			var captured nsclient.GrepResult
			err := ns.GrepTo(uri, q, func(res nsclient.GrepResult) error {
				captured = res
				return nil
			})
			var notSearched *nsclient.NotSearchedError
			if err != nil && !errors.As(err, &notSearched) {
				return mcpToolErr(err)
			}
			res := mcpJSONResult(captured)
			if errors.As(err, &notSearched) {
				res.IsError = true
			}
			return res
		},
	}
}

// 13. adopt_pull_request
func externalAdoptPRTool(c *ui.HTTPClient) mcp.Tool {
	type args struct {
		PR     string `json:"pr"`
		Mode   string `json:"mode"`
		Spec   string `json:"spec"`
		Design string `json:"design"`
	}
	return mcp.Tool{
		Name:        "adopt_pull_request",
		Description: "Adopt an external pull request (owner/name#N or PR URL) into Grain for review-only or finish coordination.",
		InputSchema: mcpObjectSchema(map[string]any{
			"pr":     map[string]any{"type": "string", "description": "Pull request reference (owner/name#N or GitHub PR URL)."},
			"mode":   map[string]any{"type": "string", "description": "Takeover mode: review-only (default) or finish."},
			"spec":   map[string]any{"type": "string", "description": "Optional spec path under design/spec/."},
			"design": map[string]any{"type": "string", "description": "Optional design path under design/design/."},
		}, "pr"),
		Handler: func(ctx context.Context, raw map[string]any) mcp.Result {
			var a args
			if err := decodeMCPArgs(raw, &a); err != nil {
				return mcpToolErr(fmt.Errorf("adopt_pull_request: %w", err))
			}
			prRef, err := model.ParsePullRequestArg(strings.TrimSpace(a.PR))
			if err != nil {
				return mcpToolErr(fmt.Errorf("adopt_pull_request: %w", err))
			}
			mode := strings.TrimSpace(a.Mode)
			if mode == "" {
				mode = string(model.TakeoverReviewOnly)
			}
			if _, err := model.ParseTakeoverMode(mode); err != nil {
				return mcpToolErr(fmt.Errorf("adopt_pull_request mode: %w", err))
			}
			got, err := c.AdoptPullRequest(ctx, ui.AdoptPullRequestRequest{
				PR:     prRef.String(),
				Mode:   mode,
				Spec:   strings.TrimSpace(a.Spec),
				Design: strings.TrimSpace(a.Design),
			})
			if err != nil {
				return mcpToolErr(err)
			}
			return mcpJSONResult(got)
		},
	}
}
