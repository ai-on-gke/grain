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

package hookpolicy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndParse(t *testing.T) {
	if f, err := Load(""); f != nil || err != nil {
		t.Fatalf("Load empty = %+v, %v; want nil, nil", f, err)
	}
	if f, err := Load(filepath.Join(t.TempDir(), "missing.json")); f != nil || err != nil {
		t.Fatalf("Load missing = %+v, %v; want nil, nil", f, err)
	}

	raw, err := Marshal(File{
		Tool: &ToolPolicy{
			AllowedTools: []string{"mcp_grain-sandbox_run_command"},
		},
		Stop: &StopPolicy{
			ClosingTools: []string{"comment_on_issue"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), Filename)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || got == nil || got.Version != Version {
		t.Fatalf("Load(%s) = %+v, %v", path, got, err)
	}
	if len(got.Tool.AllowedTools) != 1 || got.Tool.AllowedTools[0] != "mcp_grain-sandbox_run_command" {
		t.Errorf("Tool = %+v", got.Tool)
	}
	if len(got.Stop.ClosingTools) != 1 || got.Stop.ClosingTools[0] != "comment_on_issue" {
		t.Errorf("Stop = %+v", got.Stop)
	}

	// Unknown additive fields in version 1 must be ignored so an installed
	// host binary keeps working with newer daemons.
	withUnknown := `{"version":1,"future_top":true,"tool":{"allowed_tools":["t1"],"future_nested":123}}`
	parsed, err := Parse([]byte(withUnknown))
	if err != nil || parsed.Tool == nil || len(parsed.Tool.AllowedTools) != 1 {
		t.Fatalf("Parse with unknown fields = %+v, %v", parsed, err)
	}

	// Unsupported future major version is rejected rather than misread.
	if _, err := Parse([]byte(`{"version":2}`)); err == nil {
		t.Error("Parse accepted version 2")
	}
	if _, err := Parse([]byte(`not json`)); err == nil {
		t.Error("Parse accepted malformed JSON")
	}
}

func TestMatchWildcard(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"exact", "exact", true},
		{"exact", "exact2", false},
		{"mcp_grain-sandbox_*", "mcp_grain-sandbox_run_command", true},
		{"mcp_grain-sandbox_*", "mcp_grain-sandbox_", true},
		{"mcp_grain-sandbox_*", "mcp_other_run_command", false},
		{"mcp_*_lookup", "mcp_acme_lookup", true},
		{"mcp_*_lookup", "mcp_acme_write", false},
		{"*", "anything/at[all]", true},
		{"", "", true},
		{"", "x", false},
	}
	for _, c := range cases {
		if got := MatchWildcard(c.pattern, c.s); got != c.want {
			t.Errorf("MatchWildcard(%q, %q) = %v, want %v", c.pattern, c.s, got, c.want)
		}
	}
}
