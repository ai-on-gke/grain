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
	"fmt"
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// Gemini rejects eagerly registered MCP tool declarations when any "array"
// schema omits "items". Verify every tool served by `grain mcp` defines an
// object "items" schema on every array node.
func TestExternalMCPToolSchemasSpecifyArrayItems(t *testing.T) {
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
		assertExternalArraySchemasHaveItems(t, tool.Name+".inputSchema", tool.InputSchema)
	}
}

func assertExternalArraySchemasHaveItems(t *testing.T, path string, node any) {
	t.Helper()
	switch v := node.(type) {
	case map[string]any:
		if externalSchemaHasArrayType(v["type"]) {
			items, ok := v["items"].(map[string]any)
			if !ok || items == nil {
				t.Errorf("%s: array schema must define an object \"items\" field: %+v", path, v)
			}
		}
		for k, child := range v {
			assertExternalArraySchemasHaveItems(t, path+"."+k, child)
		}
	case []any:
		for i, child := range v {
			assertExternalArraySchemasHaveItems(t, fmt.Sprintf("%s[%d]", path, i), child)
		}
	}
}

func externalSchemaHasArrayType(raw any) bool {
	switch typ := raw.(type) {
	case string:
		return typ == "array"
	case []any:
		for _, elem := range typ {
			if s, _ := elem.(string); s == "array" {
				return true
			}
		}
	case []string:
		return slices.Contains(typ, "array")
	}
	return false
}
