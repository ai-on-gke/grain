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
	"context"
	"encoding/json"
	"testing"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// `grain mcp` is an MCP surface a model is handed too, so it is held to
// the same function-declaration rules as the controller's
// (pkg/ui's TestMCPToolSurfacesConformToModelFunctionDeclarationRules):
// a provider that refuses one declaration refuses the whole list.
func TestExternalMCPToolSurfaceConformsToModelFunctionDeclarationRules(t *testing.T) {
	t.Parallel()
	reg := newExternalMCPRegistry(ui.NewHTTPClient("http://127.0.0.1:1"))
	raw := reg.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	var resp struct {
		Result struct {
			Tools []struct {
				Name        string         `json:"name"`
				InputSchema map[string]any `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Result.Tools) == 0 {
		t.Fatal("tools/list returned no tools")
	}
	for _, tool := range resp.Result.Tools {
		for _, p := range mcp.FunctionDeclarationProblems(tool.Name, tool.InputSchema) {
			t.Errorf("%s: %s", tool.Name, p)
		}
	}
}
