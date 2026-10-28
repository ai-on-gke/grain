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
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryRecordsToolCallsWithFullArguments(t *testing.T) {
	tmpDir := t.TempDir()
	recordPath := filepath.Join(tmpDir, "recorded-tools.jsonl")

	reg := NewRegistry()
	reg.AnnounceRecordedTools(recordPath)

	longString := strings.Repeat("a", 2000)
	reg.Register(Tool{
		Name:        "test_tool",
		InputSchema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, args map[string]any) Result {
			return Result{Text: "ok"}
		},
	})

	client := NewInProcess(context.Background(), reg)
	t.Cleanup(func() { client.Close() })

	_, err := client.CallTool(context.Background(), "test_tool", map[string]any{
		"content": longString,
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	data, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("ReadFile recorded tools: %v", err)
	}

	var rec ToolCallRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("Unmarshal recorded tool call: %v", err)
	}

	if rec.Name != "test_tool" {
		t.Errorf("rec.Name = %q, want test_tool", rec.Name)
	}
	content, ok := rec.Arguments["content"].(string)
	if !ok || len(content) != 2000 {
		t.Errorf("rec.Arguments[content] length = %d, want 2000", len(content))
	}
}
