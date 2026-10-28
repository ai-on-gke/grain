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

package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// Synthetic model responses exercise the REAL CLI dispatcher and Grain's
// REAL MCP child. No model/API credential is used. In particular, inject
// native calls even though they were not advertised: prompt omission is
// not a security boundary.
func TestLiveCodexMCPOnlyAcrossModelProfiles(t *testing.T) {
	binary, err := exec.LookPath("codex")
	if err != nil {
		skipUnlessLiveRequired(t, "no codex binary on PATH")
	}
	grain := buildGrainBinary(t)
	for _, tc := range []struct {
		model string
		code  bool
	}{
		{"gpt-5.1-codex", false}, {"gpt-5.5", false}, {"gpt-5.6-sol", true}, {"gpt-6-astra", true},
	} {
		t.Run(tc.model, func(t *testing.T) {
			root := t.TempDir()
			canary := filepath.Join(t.TempDir(), "native-must-not-write")
			var mu sync.Mutex
			var requests []map[string]any
			needDiscovery := false
			stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				mu.Lock()
				requests = append(requests, request)
				round := len(requests)
				mu.Unlock()
				var items []map[string]any
				mcpCall := func() map[string]any {
					name, namespace := findProbeTool(request, "write_file", "")
					if name == "" {
						t.Error("MCP write_file not found after discovery")
					}
					return map[string]any{"type": "function_call", "name": name, "namespace": namespace, "call_id": "mcp_write", "arguments": `{"file_path":"mcp-probe.txt","content":"MCP_ONLY_OK"}`}
				}
				if round == 1 {
					for _, name := range []string{"exec_command", "shell", "shell_command", "apply_patch", "view_image", "write_stdin", "request_permissions"} {
						if found, _ := findProbeTool(request["tools"], name, ""); found != "" {
							t.Errorf("native tool advertised: %s", name)
						}
					}
					for _, name := range []string{"exec_command", "shell_command", "shell", "view_image", "write_stdin", "request_permissions"} {
						args := map[string]any{"cmd": "printf HOST_NATIVE_EXECUTED", "command": "printf HOST_NATIVE_EXECUTED", "path": canary, "session_id": 1}
						if name == "shell" {
							args["command"] = []string{"sh", "-c", "printf HOST_NATIVE_EXECUTED"}
						}
						encoded, _ := json.Marshal(args)
						items = append(items, map[string]any{"type": "function_call", "name": name, "namespace": "functions", "call_id": "native_" + name, "arguments": string(encoded)})
					}
					items = append(items, map[string]any{"type": "custom_tool_call", "name": "apply_patch", "namespace": "functions", "call_id": "native_patch", "input": "*** Begin Patch\n*** Add File: " + canary + "\n+bad\n*** End Patch"})
					if tc.code {
						items = append(items, map[string]any{"type": "custom_tool_call", "name": "exec", "namespace": "functions", "call_id": "mcp_code", "input": `const native = ["exec_command","shell","shell_command","apply_patch","view_image","write_stdin","request_permissions"]; if (native.some(n => typeof tools[n] !== "undefined")) throw new Error("NATIVE_TOOL_EXPOSED"); const t = ALL_TOOLS.find(t => t.name.endsWith("__write_file")); text(await tools[t.name]({file_path:"mcp-probe.txt",content:"MCP_ONLY_OK"}));`})
					} else {
						serialized, _ := json.Marshal(request["tools"])
						needDiscovery = strings.Contains(string(serialized), `"type":"tool_search"`)
						if needDiscovery {
							items = append(items, map[string]any{"type": "tool_search_call", "call_id": "find_mcp", "execution": "client", "arguments": map[string]any{"query": "grain write_file", "limit": 1}})
						} else {
							items = append(items, mcpCall())
						}
					}
				} else if round == 2 && needDiscovery {
					items = append(items, mcpCall())
				} else {
					items = append(items, map[string]any{"type": "message", "role": "assistant", "id": "msg_done", "content": []any{map[string]any{"type": "output_text", "text": "done"}}})
				}
				w.Header().Set("Content-Type", "text/event-stream")
				write := func(event map[string]any) {
					data, _ := json.Marshal(event)
					fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], data)
				}
				write(map[string]any{"type": "response.created", "response": map[string]any{"id": fmt.Sprint("response", round)}})
				for _, item := range items {
					write(map[string]any{"type": "response.output_item.done", "item": item})
				}
				write(map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprint("response", round)}})
			}))
			defer stub.Close()
			extra := stubProviderConfig(stub.URL, noInternalRetries...)
			if tc.model == "gpt-6-astra" {
				// The pinned CLI's bundled catalog predates Astra. Simulate
				// the Code Mode metadata a refreshed catalog supplies; this
				// is a dispatch-path test, not an account availability claim.
				path, err := filepath.Abs("testdata/astra-code-mode-profile.json")
				if err != nil {
					t.Fatal(err)
				}
				quoted, _ := tomlString(path)
				extra = "model_catalog_json = " + quoted + "\n" + extra
			}
			f := New(binary, grain, WithModel(tc.model), WithAPIKey(stubAPIKey), withExtraConfig(extra))
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			result, err := f.Run(ctx, agent.RunConfig{SandboxRoot: root, Prompt: "Exercise the MCP-only tools."})
			if err != nil {
				t.Fatalf("Run: %v\n%+v", err, result)
			}
			data, err := os.ReadFile(filepath.Join(root, "mcp-probe.txt"))
			if err != nil || string(data) != "MCP_ONLY_OK" {
				t.Fatalf("MCP did not write in sandbox: %s, %v\n%s", data, err, result.Transcript)
			}
			if _, err := os.Stat(canary); !os.IsNotExist(err) {
				t.Fatalf("native patch reached controller: %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			outputs := make(map[string]string)
			for _, req := range requests {
				input, _ := req["input"].([]any)
				for _, entry := range input {
					item, _ := entry.(map[string]any)
					id, _ := item["call_id"].(string)
					if strings.HasPrefix(id, "native_") {
						if output, ok := item["output"]; ok {
							encoded, _ := json.Marshal(output)
							outputs[id] = string(encoded)
						}
					}
				}
			}
			for _, name := range []string{"exec_command", "shell_command", "shell", "view_image", "write_stdin", "request_permissions", "patch"} {
				output := outputs["native_"+name]
				if !strings.Contains(output, "unsupported") {
					t.Errorf("native %s was not rejected: %s", name, output)
				}
			}
			foundCall := false
			for _, call := range result.ToolCalls {
				if call.Name == "write_file" {
					foundCall = true
				}
			}
			if !foundCall {
				t.Errorf("MCP call missing from Grain's parsed record: %+v", result.ToolCalls)
			}
		})
	}
}

func findProbeTool(value any, needle, namespace string) (string, string) {
	switch v := value.(type) {
	case []any:
		for _, child := range v {
			if name, ns := findProbeTool(child, needle, namespace); name != "" {
				return name, ns
			}
		}
	case map[string]any:
		name, _ := v["name"].(string)
		if v["type"] == "namespace" {
			namespace = name
		} else if name == needle || strings.HasSuffix(name, "__"+needle) {
			return name, namespace
		}
		for _, key := range []string{"tools", "input"} {
			if name, ns := findProbeTool(v[key], needle, namespace); name != "" {
				return name, ns
			}
		}
	}
	return "", ""
}

// Keep the published image and the dispatch-time upgrade gate in sync.
func TestReviewedRuntimeMatchesImage(t *testing.T) {
	data, err := os.ReadFile("../../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "ARG CODEX_CLI_VERSION=rust-v"+ReviewedCLIVersion+"\n") {
		t.Fatal("CLI pin changed without reviewing the MCP-only policy")
	}
}
