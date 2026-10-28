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

package deploymenthealth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeReader struct {
	value any
	err   error
}

func (f fakeReader) DeploymentHealth(context.Context) (any, error) {
	return f.value, f.err
}

func TestProviderSpecAndNew(t *testing.T) {
	p := New()
	spec := p.Spec()
	if spec.Name != CapabilityName {
		t.Fatalf("spec.Name = %q, want %q", spec.Name, CapabilityName)
	}
	if spec.Label == "" || spec.Description == "" {
		t.Fatalf("empty spec fields: %+v", spec)
	}
}

func TestToolsList(t *testing.T) {
	cfg := Config{}
	tools := Tools(cfg)
	if len(tools) != 2 {
		t.Fatalf("Tools() returned %d tools, want 2", len(tools))
	}
	if tools[0].Name != "read_deployment_health" || tools[1].Name != "read_deployment_state" {
		t.Fatalf("tool names mismatch: %q, %q", tools[0].Name, tools[1].Name)
	}
}

func TestHealthToolErrorsAndEdgeCases(t *testing.T) {
	ctx := context.Background()

	// 1. Missing Redact func
	toolNoRedact := healthTool(Config{Reader: fakeReader{value: "ok"}})
	res := toolNoRedact.Handler(ctx, nil)
	if !res.IsError || !strings.Contains(res.Text, "Deployment health read refused") {
		t.Fatalf("health without Redact: got %#v", res)
	}

	// 2. Missing Reader
	toolNoReader := healthTool(Config{Redact: func(s string) string { return s }})
	res = toolNoReader.Handler(ctx, nil)
	if !res.IsError || !strings.Contains(res.Text, "Deployment health is unavailable") {
		t.Fatalf("health without Reader: got %#v", res)
	}

	// 3. Reader returns error
	var logged []string
	toolErrReader := healthTool(Config{
		TaskID: "task-123",
		Reader: fakeReader{err: errors.New("connection reset")},
		Redact: func(s string) string { return s },
		Log: func(format string, args ...any) {
			logged = append(logged, fmt.Sprintf(format, args...))
		},
	})
	res = toolErrReader.Handler(ctx, nil)
	if !res.IsError || !strings.Contains(res.Text, "connection reset") {
		t.Fatalf("health with failing Reader: got %#v", res)
	}
	if len(logged) == 0 || !strings.Contains(logged[0], "task=\"task-123\"") {
		t.Fatalf("audit log missing task id: %v", logged)
	}
}

func TestStateReadIsScopedAndRedacted(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "tables"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tables", "task.json"), []byte(`{"token":"secret-value"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{StateDir: dir, Redact: func(s string) string { return strings.ReplaceAll(s, "secret-value", "[REDACTED]") }}
	got := readState(cfg, map[string]any{"path": "tables/task.json"})
	if got.IsError || strings.Contains(got.Text, "secret-value") || !strings.Contains(got.Text, "[REDACTED]") {
		t.Fatalf("read = %#v, want redacted table", got)
	}
	for _, path := range []string{"../secrets/key", ".git/config", "tables/nested/file.json", "README.md"} {
		got := readState(cfg, map[string]any{"path": path})
		if !got.IsError || !strings.Contains(got.Text, "not authorized") {
			t.Errorf("%q = %#v, want authorization refusal", path, got)
		}
	}
}

func TestStateReadListAndMissingDir(t *testing.T) {
	// Missing redact
	got := readState(Config{}, map[string]any{"path": "tables/task.json"})
	if !got.IsError || !strings.Contains(got.Text, "Deployment state read refused") {
		t.Fatalf("read without Redact: got %#v", got)
	}

	// Missing stateDir
	got = readState(Config{Redact: func(s string) string { return s }}, map[string]any{"path": "tables/task.json"})
	if !got.IsError || !strings.Contains(got.Text, "Deployment state is unavailable") {
		t.Fatalf("read without StateDir: got %#v", got)
	}

	// List tables when path is omitted
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "tables"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tables", "a.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tables", "b.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := Config{StateDir: dir, Redact: func(s string) string { return s }}
	got = readState(cfg, map[string]any{})
	if got.IsError || !strings.Contains(got.Text, "tables/a.json") || !strings.Contains(got.Text, "tables/b.json") {
		t.Fatalf("list tables: got %#v", got)
	}

	// Missing file in tables
	got = readState(cfg, map[string]any{"path": "tables/nonexistent.json"})
	if !got.IsError || !strings.Contains(got.Text, "Deployment state repository availability error") {
		t.Fatalf("read nonexistent table: got %#v", got)
	}
}

func TestStateReadRefusesOversizeFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "tables"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tables", "task.json"), make([]byte, maxStateBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	got := readState(Config{StateDir: dir, Redact: func(s string) string { return s }}, map[string]any{"path": "tables/task.json"})
	if !got.IsError || !strings.Contains(got.Text, "over the per-call limit") {
		t.Fatalf("read = %#v", got)
	}
}

func TestHealthOutputIsRedacted(t *testing.T) {
	tool := healthTool(Config{Reader: fakeReader{value: map[string]string{"detail": "ghp_secret"}}, Redact: func(s string) string { return strings.ReplaceAll(s, "ghp_secret", "[REDACTED]") }})
	got := tool.Handler(context.Background(), nil)
	if got.IsError || strings.Contains(got.Text, "ghp_secret") || !strings.Contains(got.Text, "[REDACTED]") {
		t.Fatalf("health = %#v", got)
	}
}
