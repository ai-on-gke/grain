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
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/nsclient"
	"github.com/ai-on-gke/grain/pkg/safepath"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// mcp_external_ops.go holds the inbox, CI, merge-queue, namespace, and pull
// request adoption tools for `grain mcp` (#XC-31).

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
			repo, job, workflow, since, until, sortBy := a.Repo, a.Job, a.Workflow, a.Since, a.Until, a.Sort
			fv := &ciListFlagValues{
				repo: &repo, ref: &ref, job: &job, workflow: &workflow,
				since: &since, until: &until, sort: &sortBy, order: &order,
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
					parsedRepo, rerr := model.ParseRepo(repo)
					j := strings.TrimSpace(a.Job)
					if rerr != nil {
						return mcpToolErr(fmt.Errorf("ci_log repo: %w", rerr))
					}
					if !isSafeCIURISegment(runID) || (j != "" && !isSafeCIURISegment(j)) {
						return mcpToolErr(errors.New("ci_log: invalid run_id or job segment"))
					}
					uri := "ci://" + parsedRepo.String() + "/" + runID
					if j != "" {
						uri += "/" + j + "/log"
					} else {
						uri += "/log"
					}
					var outBuf, errBuf bytes.Buffer
					if rerr := fsClient(c, &outBuf, &errBuf).Read(uri, url.Values{}, nsclient.ReadOptions{LastLines: a.Tail}); rerr == nil {
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
				jo := jobOut{ID: j.JobID, Name: j.Name, Status: j.Status, Conclusion: string(j.Conclusion)}
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
						Name: s.Name, Conclusion: string(s.Conclusion), ExitCode: s.ExitCode, Output: outText,
					})
				}
				jobs = append(jobs, jo)
			}
			return mcpJSONResult(map[string]any{
				"id": run.ID, "repo": run.Repo, "ref": run.Ref, "commit": run.Commit,
				"conclusion": run.Conclusion, "jobs": jobs,
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
			opts := nsclient.GrepOptions{IgnoreCase: a.IgnoreCase, Glob: strings.TrimSpace(a.Glob)}
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
				PR: prRef.String(), Mode: mode, Spec: strings.TrimSpace(a.Spec), Design: strings.TrimSpace(a.Design),
			})
			if err != nil {
				return mcpToolErr(err)
			}
			return mcpJSONResult(got)
		},
	}
}

func isSafeCIURISegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	// '%' too: "%2e%2e%2f" is "../" once the daemon decodes the path. The
	// daemon refuses that traversal itself, but this guard is the client's
	// own line and should not lean on it; no run or job id holds a '%'.
	return !strings.ContainsAny(s, "/\\?#%\r\n\x00")
}

// loadMCPAttachments confines attachment file reads in `grain mcp` to regular
// files inside the current project/working directory root, preventing a
// prompt-injected coding agent from using reply_to_task or comment_on_task to
// exfiltrate host credentials or arbitrary files outside the workspace.
func loadMCPAttachments(paths []string) ([]ui.AttachmentUpload, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	root, err := resolveProjectRoot()
	if err != nil {
		return nil, err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolving workspace root %s: %w", root, err)
	}
	cfgDir, _ := grainConfigDir()

	resolved := make([]string, 0, len(paths))
	for _, p := range paths {
		absP := p
		if !filepath.IsAbs(absP) {
			absP = filepath.Join(root, p)
		}
		if cfgDir != "" {
			if _, cerr := safepath.Within(cfgDir, absP); cerr == nil {
				return nil, fmt.Errorf("refusing attachment %s inside grain config directory", p)
			}
		}
		realP, err := safepath.Within(realRoot, absP)
		if err != nil {
			return nil, fmt.Errorf("refusing attachment %s outside workspace %s: %w", p, root, err)
		}
		if cfgDir != "" {
			if _, cerr := safepath.Within(cfgDir, realP); cerr == nil {
				return nil, fmt.Errorf("refusing attachment %s inside grain config directory", p)
			}
		}
		rel, err := filepath.Rel(realRoot, realP)
		if err != nil || rel == "." {
			return nil, fmt.Errorf("refusing attachment %s outside workspace %s", p, root)
		}
		for _, seg := range strings.Split(rel, string(filepath.Separator)) {
			lower := strings.ToLower(seg)
			if lower == ".git" || lower == ".ssh" || lower == ".gnupg" || lower == ".aws" ||
				lower == ".env" || strings.HasPrefix(lower, ".env.") {
				return nil, fmt.Errorf("refusing sensitive attachment path %s", p)
			}
		}
		info, err := os.Stat(realP)
		if err != nil {
			return nil, fmt.Errorf("reading attachment %s: %w", p, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("refusing non-regular attachment %s", p)
		}
		resolved = append(resolved, realP)
	}
	uploads, err := loadAttachments(resolved)
	if err != nil {
		return nil, err
	}
	for i := range uploads {
		uploads[i].Filename = filepath.Base(paths[i])
	}
	return uploads, nil
}
