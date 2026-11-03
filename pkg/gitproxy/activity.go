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

package gitproxy

// activity.go narrates and counts the in-sandbox grain CLI's reads.
//
// A grain CLI call inside run_command reaches the daemon here, over
// /fs/, rather than through grain mcpserver's tool registry, so neither
// of the registry's two observers sees it: the run's activity row would
// only ever show run_command's description, and task_run_tool would count
// every such read as one more run_command. The proxy authenticates the
// sandbox, and so knows the live run, on every request -- which is all
// either observer needs. Each request is named after the MCP tool it
// replaces (so the read_grain_metrics "tools" census stays continuous
// across the move), its phrase is that tool's own (mcp.ToolActivityNote),
// and its response size and status are recorded against the live attempt.

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// ProxyActivityRecorder is where the proxy reports what a sandbox's live
// run is doing and what it called. Implemented by *model.Store.
type ProxyActivityRecorder interface {
	SetTaskActivity(ctx context.Context, taskID, activity string, at time.Time) (live bool, err error)
	RecordProxyToolCall(ctx context.Context, runID, tool string, resultBytes int64, errored bool) error
}

// ProxyAPICallRecorder is an optional extension of ProxyActivityRecorder that
// records each served proxy call in task_run_api_call. Implemented by *model.Store.
type ProxyAPICallRecorder interface {
	RecordProxyAPICall(ctx context.Context, runID, tool, targetIDs string, resultBytes int64) error
}

var (
	_ ProxyActivityRecorder = (*model.Store)(nil)
	_ ProxyAPICallRecorder  = (*model.Store)(nil)
)

// proxyActivityRepeatWindow is how long a phrase just written is trusted
// to still be on the row, matching pkg/mcp's toolActivityRepeatWindow: a
// shell loop reading fifty files writes the row once, not fifty times.
const proxyActivityRepeatWindow = 30 * time.Second

// RouteActivity is what one proxy request is, in the terms of the MCP tool
// it replaces.
type RouteActivity struct {
	// Tool is the canonical tool name recorded in task_run_tool.
	Tool string
	// Phrase is the run's activity row while the request is served.
	Phrase string
	// Targets are the task or CI run IDs touched by the request, recorded in
	// task_run_api_call.
	Targets []string
}

// NamespaceRouteActivity names a /fs/<scheme>/<path> request after the MCP
// tool it stands in for.
func NamespaceRouteActivity(path string, query url.Values) RouteActivity {
	tool := namespaceRouteTool(path, query)
	return RouteActivity{
		Tool:    tool,
		Phrase:  mcp.ToolActivityNote(tool),
		Targets: namespaceRouteTargets(path),
	}
}

func namespaceRouteTargets(path string) []string {
	rest := strings.Trim(strings.TrimPrefix(path, "/fs"), "/")
	scheme, rel, _ := strings.Cut(rest, "/")
	rel = strings.Trim(rel, "/")
	switch scheme {
	case "task":
		if id, _, _ := strings.Cut(rel, "/"); id != "" {
			return []string{id}
		}
	case "ci":
		parts := strings.Split(rel, "/")
		runIdx := 2
		if len(parts) > 0 && strings.ContainsAny(parts[0], ".:") {
			runIdx = 3
		}
		if len(parts) > runIdx && parts[runIdx] != "" {
			return []string{parts[runIdx]}
		}
	}
	return nil
}

func namespaceRouteTool(path string, query url.Values) string {
	if query.Has("grep") {
		return "grain_grep"
	}
	_, wantList := query["list"]
	rest := strings.Trim(strings.TrimPrefix(path, "/fs"), "/")
	scheme, rel, _ := strings.Cut(rest, "/")
	rel = strings.Trim(rel, "/")
	switch scheme {
	case "ci":
		if wantList {
			return "grain_ls"
		}
		if strings.Contains("/"+rel+"/", "/artifacts/") {
			return "get_ci_artifact"
		}
		return "get_ci_log"
	case "src":
		if rel == "playbooks" || strings.HasPrefix(rel, "playbooks/") {
			if wantList || rel == "playbooks" {
				return "list_bootstrap_playbooks"
			}
			return "read_bootstrap_playbook"
		}
		if wantList {
			return "grain_ls"
		}
		return "grain_read"
	case "task":
		if rel == "" {
			return "list_grain_tasks"
		}
		switch base := rel[strings.LastIndex(rel, "/")+1:]; {
		case strings.HasPrefix(base, "prompt"):
			return "read_grain_task_prompt"
		case strings.HasPrefix(base, "transcript"):
			return "read_grain_task_transcript"
		}
		return "read_grain_task"
	case "memory":
		if wantList || rel == "" {
			return "list_memory_documents"
		}
		return "read_memory_document"
	case "deploy":
		first, _, _ := strings.Cut(rel, "/")
		switch first {
		case "grain.log":
			return "read_grain_log"
		case "health":
			return "read_deployment_health"
		case "state":
			return "read_deployment_state"
		case "metrics":
			return "read_grain_metrics"
		case "merge-queue":
			return "query_merge_queue"
		case "pipeline-changes":
			return "read_pipeline_changes"
		}
	}
	if wantList {
		return "grain_ls"
	}
	return "grain_read"
}

// proxyActivity remembers the phrase last written per task, so a repeat
// within proxyActivityRepeatWindow skips the write.
type proxyActivity struct {
	mu   sync.Mutex
	last map[string]proxyActivityNote
}

type proxyActivityNote struct {
	phrase string
	at     time.Time
}

func (a *proxyActivity) shouldWrite(taskID, phrase string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.last == nil {
		a.last = make(map[string]proxyActivityNote)
	}
	if prev, ok := a.last[taskID]; ok && prev.phrase == phrase && now.Sub(prev.at) < proxyActivityRepeatWindow {
		return false
	}
	a.last[taskID] = proxyActivityNote{phrase: phrase, at: now}
	return true
}

// noteActivity puts phrase on taskID's row. Best-effort: the row is a
// courtesy and never holds up or fails the read itself.
func (p *GitProxy) noteActivity(ctx context.Context, taskID, phrase string) {
	if p.Activity == nil || taskID == "" || phrase == "" {
		return
	}
	now := time.Now()
	if !p.activity.shouldWrite(taskID, phrase, now) {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, _ = p.Activity.SetTaskActivity(ctx, taskID, phrase, now)
}

// recordToolCall counts one served request against the live attempt.
func (p *GitProxy) recordToolCall(ctx context.Context, runID, tool string, w *countingResponseWriter) {
	p.recordRouteCall(ctx, runID, RouteActivity{Tool: tool}, w)
}

// recordRouteCall counts one served request against the live attempt in
// task_run_tool, task_run_api_call, and systemlog.
func (p *GitProxy) recordRouteCall(ctx context.Context, runID string, act RouteActivity, w *countingResponseWriter) {
	if p.Activity == nil || runID == "" || act.Tool == "" {
		return
	}
	errored := w.statusCode() >= 400
	log.Printf("grain: sandbox proxy call run %s -> %s (status %d, %d bytes)", runID, act.Tool, w.statusCode(), w.bytes)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = p.Activity.RecordProxyToolCall(ctx, runID, act.Tool, w.bytes, errored)
	if !errored {
		if apiRec, ok := p.Activity.(ProxyAPICallRecorder); ok {
			_ = apiRec.RecordProxyAPICall(ctx, runID, act.Tool, strings.Join(act.Targets, ","), w.bytes)
		}
	}
}

// countingResponseWriter counts the body bytes and captures the status of
// a response while passing both through.
type countingResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *countingResponseWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *countingResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Flush keeps ?follow= streaming working through the wrapper.
func (w *countingResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *countingResponseWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}
