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

package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFunctionDeclarationProblems(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, tool, schema string
		want               string // a substring of the one problem, or "" for none
	}{
		{"accepted", "grain_submit_task", `{"type":"object","properties":{"grants":{"type":"array","items":{}},"n":{"type":"integer","format":"int64"},"when":{"type":"string","format":"date-time"},"kind":{"type":"string","enum":["a","b"]},"opt":{"type":["string","null"]}},"required":["grants"],"additionalProperties":false}`, ""},
		{"array without items", "t", `{"type":"object","properties":{"grants":{"type":"array"}}}`, `inputSchema.properties.grants: array schema must define an object "items"`},
		{"nested array without items", "t", `{"type":"object","properties":{"a":{"type":"array","items":{"type":"object","properties":{"b":{"type":"array"}}}}}}`, `properties.a.items.properties.b: array schema`},
		{"non-object root", "t", `{"type":"string"}`, `root must be "type": "object"`},
		{"unknown type", "t", `{"type":"object","properties":{"a":{"type":"file"}}}`, `type "file"`},
		{"two types", "t", `{"type":"object","properties":{"a":{"type":["string","integer"]}}}`, `single type`},
		{"unsupported format", "t", `{"type":"object","properties":{"a":{"type":"string","format":"uri"}}}`, `format uri`},
		{"unsupported keyword", "t", `{"type":"object","properties":{"a":{"oneOf":[{"type":"string"}]}}}`, `keyword "oneOf"`},
		{"ref", "t", `{"type":"object","properties":{"a":{"$ref":"#/$defs/x"}}}`, `keyword "$ref"`},
		{"numeric enum", "t", `{"type":"object","properties":{"a":{"enum":[1,2]}}}`, `enum value 1`},
		{"required undeclared", "t", `{"type":"object","properties":{},"required":["a"]}`, `required a`},
		{"dotted name", "grain.submit", `{"type":"object"}`, `name "grain.submit"`},
		{"leading digit", "1tool", `{"type":"object"}`, `name "1tool"`},
		{"long name", strings.Repeat("a", 65), `{"type":"object"}`, `must match`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var schema map[string]any
			if err := json.Unmarshal([]byte(tc.schema), &schema); err != nil {
				t.Fatal(err)
			}
			got := FunctionDeclarationProblems(tc.tool, schema)
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("problems = %q, want none", got)
				}
				return
			}
			if len(got) != 1 || !strings.Contains(got[0], tc.want) {
				t.Fatalf("problems = %q, want exactly one containing %q", got, tc.want)
			}
		})
	}
}

func TestArrayItemsProblems(t *testing.T) {
	t.Parallel()
	var schema any
	if err := json.Unmarshal([]byte(`{"oneOf":[{"type":"array"},{"type":"array","items":{"type":"string"}}]}`), &schema); err != nil {
		t.Fatal(err)
	}
	got := ArrayItemsProblems("args", schema)
	if len(got) != 1 || !strings.HasPrefix(got[0], "args.oneOf[0]:") {
		t.Fatalf("problems = %q, want one at args.oneOf[0]", got)
	}
}
