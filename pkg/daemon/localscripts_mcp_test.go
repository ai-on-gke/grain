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

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func localScriptsDeclaration(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ops")
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	decl := filepath.Join(dir, "local-scripts.yaml")
	if err := os.WriteFile(decl, []byte("scripts: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "hello"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return decl
}

// The mcpserver serves a declaration only to a task that still holds its
// capability, and closes what it opened.
func TestLocalScriptToolsFollowTheTasksGrants(t *testing.T) {
	decl := localScriptsDeclaration(t)
	list := LocalScriptsFlags{{Name: "ops", Path: decl}}

	tools, closeTools := localScriptTools(list, "", GrantNames{"local-scripts:ops"}, true, nil, "7")
	defer closeTools()
	if len(tools) != 1 || tools[0].Name != "local_hello" {
		t.Fatalf("tools %+v: want local_hello for a task holding local-scripts:ops", tools)
	}
	res := tools[0].Handler(context.Background(), map[string]any{"args": []any{}})
	if res.IsError {
		t.Fatalf("local_hello: %s", res.Text)
	}

	none, closeNone := localScriptTools(list, "", GrantNames{"self-debug"}, true, nil, "7")
	defer closeNone()
	if len(none) != 0 {
		t.Fatalf("tools %+v: want none for a task that no longer holds local-scripts:ops", none)
	}
}
