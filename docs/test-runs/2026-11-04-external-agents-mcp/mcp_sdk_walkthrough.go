//go:build ignore

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

// mcp_sdk_walkthrough.go drives a live `grain daemon`'s POST /api/mcp endpoint
// using the official MCP Go SDK client (github.com/modelcontextprotocol/go-sdk/mcp)
// and records every HTTP / JSON-RPC exchange for docs/test-plans/2026-11-04.md §4.1.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type recordedExchange struct {
	Seq            int               `json:"seq"`
	Phase          string            `json:"phase"`
	Method         string            `json:"method"`
	URL            string            `json:"url"`
	RequestHeaders map[string]string `json:"requestHeaders"`
	RequestBody    json.RawMessage   `json:"requestBody,omitempty"`
	RequestRaw     string            `json:"requestRaw,omitempty"`
	StatusCode     int               `json:"statusCode"`
	RespHeaders    map[string]string `json:"responseHeaders"`
	ResponseBody   json.RawMessage   `json:"responseBody,omitempty"`
	ResponseRaw    string            `json:"responseRaw,omitempty"`
	DurationMs     int64             `json:"durationMs"`
	TransportError string            `json:"transportError,omitempty"`
}

type recordingTransport struct {
	base    http.RoundTripper
	headers map[string]string
	redacts map[string]string

	mu        sync.Mutex
	phase     string
	exchanges []recordedExchange
}

func (rt *recordingTransport) setPhase(p string) {
	rt.mu.Lock()
	rt.phase = p
	rt.mu.Unlock()
}

func (rt *recordingTransport) mask(s string) string {
	out := s
	for secret, placeholder := range rt.redacts {
		if secret != "" {
			out = strings.ReplaceAll(out, secret, placeholder)
		}
	}
	return out
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	for k, v := range rt.headers {
		if v == "" {
			clone.Header.Del(k)
		} else {
			clone.Header.Set(k, v)
		}
	}

	var reqBytes []byte
	if clone.Body != nil {
		b, err := io.ReadAll(clone.Body)
		if err != nil {
			return nil, err
		}
		reqBytes = b
		clone.Body = io.NopCloser(bytes.NewReader(b))
	}

	reqHdr := map[string]string{}
	for k, vals := range clone.Header {
		reqHdr[k] = rt.mask(strings.Join(vals, ", "))
	}

	rt.mu.Lock()
	phase := rt.phase
	rt.mu.Unlock()

	start := time.Now()
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(clone)
	dur := time.Since(start).Milliseconds()

	ex := recordedExchange{
		Phase:          phase,
		Method:         clone.Method,
		URL:            clone.URL.String(),
		RequestHeaders: reqHdr,
		DurationMs:     dur,
	}
	if len(bytes.TrimSpace(reqBytes)) > 0 {
		maskedReq := []byte(rt.mask(string(reqBytes)))
		if json.Valid(maskedReq) {
			ex.RequestBody = json.RawMessage(maskedReq)
		} else {
			ex.RequestRaw = string(maskedReq)
		}
	}

	if err != nil {
		ex.TransportError = rt.mask(err.Error())
		rt.appendExchange(ex)
		return nil, err
	}

	var respBytes []byte
	if resp.Body != nil && !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		b, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			ex.TransportError = rt.mask(readErr.Error())
			rt.appendExchange(ex)
			return nil, readErr
		}
		respBytes = b
		resp.Body = io.NopCloser(bytes.NewReader(b))
	}

	ex.StatusCode = resp.StatusCode
	respHdr := map[string]string{}
	for k, vals := range resp.Header {
		respHdr[k] = rt.mask(strings.Join(vals, ", "))
	}
	ex.RespHeaders = respHdr
	if len(bytes.TrimSpace(respBytes)) > 0 {
		maskedResp := []byte(rt.mask(string(respBytes)))
		if json.Valid(maskedResp) {
			ex.ResponseBody = json.RawMessage(maskedResp)
		} else {
			ex.ResponseRaw = string(maskedResp)
		}
	}
	rt.appendExchange(ex)
	return resp, nil
}

func (rt *recordingTransport) appendExchange(ex recordedExchange) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	ex.Seq = len(rt.exchanges) + 1
	rt.exchanges = append(rt.exchanges, ex)
}

type stepSummary struct {
	Step         string `json:"step"`
	IsError      bool   `json:"isError,omitempty"`
	PayloadBytes int    `json:"payloadBytes,omitempty"`
	Detail       any    `json:"detail,omitempty"`
	Error        string `json:"error,omitempty"`
}

func firstText(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	raw, _ := json.Marshal(res.Content)
	return string(raw)
}

func runSequence(ctx context.Context, endpoint, label string, headers, redacts map[string]string, rt *recordingTransport, taskTitle string) ([]stepSummary, error) {
	var steps []stepSummary
	rt.headers = headers
	rt.redacts = redacts

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "grain-walkthrough-mcp-sdk",
		Version: "1.0.0",
	}, nil)

	rt.setPhase(label + ": connect (initialize + notifications/initialized)")
	transport := &mcp.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: &http.Client{Transport: rt, Timeout: 30 * time.Second},
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		steps = append(steps, stepSummary{Step: label + "/connect", Error: rt.mask(err.Error())})
		return steps, err
	}
	defer session.Close()

	initRes := session.InitializeResult()
	steps = append(steps, stepSummary{
		Step: label + "/initialize",
		Detail: map[string]any{
			"protocolVersion": initRes.ProtocolVersion,
			"serverInfo":      initRes.ServerInfo,
			"instructionsLen": len(initRes.Instructions),
		},
	})

	rt.setPhase(label + ": tools/list")
	toolsRes, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return steps, fmt.Errorf("ListTools: %w", err)
	}
	var toolInfo []map[string]any
	for _, t := range toolsRes.Tools {
		toolInfo = append(toolInfo, map[string]any{
			"name":       t.Name,
			"alwaysLoad": t.Meta["antigravity/alwaysLoad"],
		})
	}
	steps = append(steps, stepSummary{
		Step: label + "/tools/list",
		Detail: map[string]any{
			"count": len(toolsRes.Tools),
			"tools": toolInfo,
		},
	})

	call := func(phase, tool string, args map[string]any) (*mcp.CallToolResult, string, error) {
		rt.setPhase(label + ": " + phase)
		res, callErr := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      tool,
			Arguments: args,
		})
		if callErr != nil {
			steps = append(steps, stepSummary{Step: label + "/" + phase, Error: rt.mask(callErr.Error())})
			return nil, "", callErr
		}
		txt := firstText(res)
		var parsed any = txt
		var js any
		if json.Unmarshal([]byte(txt), &js) == nil {
			parsed = js
		}
		steps = append(steps, stepSummary{
			Step:         label + "/" + phase,
			IsError:      res.IsError,
			PayloadBytes: len(txt),
			Detail:       parsed,
		})
		return res, txt, nil
	}

	if _, _, err := call("grain_explore_scope(overview)", "grain_explore_scope", map[string]any{}); err != nil {
		return steps, err
	}
	if _, _, err := call("grain_explore_scope(tasks,comment)", "grain_explore_scope", map[string]any{
		"scope": "tasks",
		"tool":  "comment",
	}); err != nil {
		return steps, err
	}
	if _, _, err := call("grain_explore_scope(settings-unauthorized)", "grain_explore_scope", map[string]any{
		"scope": "settings",
	}); err != nil {
		return steps, err
	}
	if _, _, err := call("grain_filing_context", "grain_filing_context", map[string]any{}); err != nil {
		return steps, err
	}

	longDesc := strings.Repeat("Detailed acceptance criteria and context for unattended execution so full view exceeds compact summary. ", 25)
	_, submitTxt, err := call("grain_submit_task", "grain_submit_task", map[string]any{
		"title":       taskTitle,
		"description": longDesc,
		"repo":        "acme/app",
	})
	if err != nil {
		return steps, err
	}
	var created struct {
		ID       string `json:"id"`
		State    string `json:"state"`
		Approved bool   `json:"approved"`
	}
	_ = json.Unmarshal([]byte(submitTxt), &created)

	if created.ID != "" {
		if _, _, err := call("grain_get_task(summary)", "grain_get_task", map[string]any{
			"id": created.ID,
		}); err != nil {
			return steps, err
		}
		if _, _, err := call("grain_get_task(full)", "grain_get_task", map[string]any{
			"id":   created.ID,
			"view": "full",
		}); err != nil {
			return steps, err
		}
		if _, _, err := call("grain_invoke(allowed:api_add_comment)", "grain_invoke", map[string]any{
			"tool": "api_add_comment",
			"arguments": map[string]any{
				"id":   created.ID,
				"body": map[string]any{"body": "Comment posted via grain_invoke (" + label + ")"},
			},
		}); err != nil {
			return steps, err
		}
	}

	if _, _, err := call("grain_invoke(refused-scope:api_get_settings)", "grain_invoke", map[string]any{
		"tool":      "api_get_settings",
		"arguments": map[string]any{},
	}); err != nil {
		return steps, err
	}
	if _, _, err := call("grain_invoke(refused-repo:acme/other)", "grain_invoke", map[string]any{
		"tool": "api_create_task",
		"arguments": map[string]any{
			"body": map[string]any{
				"title": "Off-allowlist repo attempt",
				"repo":  "acme/other",
			},
		},
	}); err != nil {
		return steps, err
	}
	if _, _, err := call("grain_list_tasks", "grain_list_tasks", map[string]any{
		"repo": "acme/app",
	}); err != nil {
		return steps, err
	}

	rt.setPhase(label + ": close")
	return steps, nil
}

func main() {
	endpoint := flag.String("endpoint", "http://127.0.0.1:14300/api/mcp", "MCP HTTP endpoint")
	gmaToken := flag.String("gma-token", "", "gma_ external agent token")
	accessCode := flag.String("access-code", "", "daemon access code")
	narrowTaskID := flag.String("narrow-task", "", "optional narrow task ID for X-Grain-Acting-Task with access code")
	outPath := flag.String("out", "", "path to write JSON report")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	redacts := map[string]string{
		*gmaToken:   "<REDACTED_GMA_TOKEN>",
		*accessCode: "<REDACTED_ACCESS_CODE>",
	}
	rt := &recordingTransport{redacts: redacts}

	report := map[string]any{}

	// 1. Authorization: Bearer <gma_token>
	bearerSteps, bearerErr := runSequence(ctx, *endpoint, "bearer-gma", map[string]string{
		"Authorization":  "Bearer " + *gmaToken,
		"X-Grain-Client": "mcp-go-sdk/1.8.0 session=item1-bearer",
	}, redacts, rt, "MCP SDK task via Authorization: Bearer gma_")
	report["bearerGma"] = map[string]any{"steps": bearerSteps, "error": errStr(bearerErr)}

	// 2a. Probe: X-Grain-Access-Code carrying a gma_ token (with upstream OIDC JWT in Authorization)
	gmaInAccessCodeSteps, gmaInAccessCodeErr := runSequence(ctx, *endpoint, "x-grain-access-code-with-gma", map[string]string{
		"Authorization":       "Bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.upstream-proxy.sig",
		"X-Grain-Access-Code": *gmaToken,
		"X-Grain-Client":      "mcp-go-sdk/1.8.0 session=item1-xgac-gma",
	}, redacts, rt, "MCP SDK task via X-Grain-Access-Code gma_")
	report["xGrainAccessCodeWithGma"] = map[string]any{"steps": gmaInAccessCodeSteps, "error": errStr(gmaInAccessCodeErr)}

	// 2b. X-Grain-Access-Code carrying the daemon access code alongside an upstream OIDC JWT in Authorization
	acHeaders := map[string]string{
		"Authorization":       "Bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.upstream-proxy.sig",
		"X-Grain-Access-Code": *accessCode,
		"X-Grain-Client":      "mcp-go-sdk/1.8.0 session=item1-xgac-code",
	}
	if *narrowTaskID != "" {
		acHeaders["X-Grain-Acting-Task"] = *narrowTaskID
	}
	acSteps, acErr := runSequence(ctx, *endpoint, "x-grain-access-code", acHeaders, redacts, rt, "MCP SDK task via X-Grain-Access-Code")
	report["xGrainAccessCode"] = map[string]any{"steps": acSteps, "error": errStr(acErr)}

	report["exchanges"] = rt.exchanges

	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal report: %v\n", err)
		os.Exit(1)
	}
	if *outPath != "" {
		if err := os.WriteFile(*outPath, append(encoded, '\n'), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write %s: %v\n", *outPath, err)
			os.Exit(1)
		}
	}
	fmt.Printf("Recorded %d HTTP exchanges.\n", len(rt.exchanges))
	fmt.Printf("  bearer-gma error: %q\n", errStr(bearerErr))
	fmt.Printf("  x-grain-access-code-with-gma error: %q\n", errStr(gmaInAccessCodeErr))
	fmt.Printf("  x-grain-access-code error: %q\n", errStr(acErr))
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
