package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/bwsalmon/grain/pkg/agent"
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

// A custom backend's forked mcpserver is told the same declarations a
// built-in framework's is.
func TestCustomMCPServerArgsCarryLocalScripts(t *testing.T) {
	args, err := CustomMCPServerArgs(context.Background(), Config{}, agent.RunConfig{
		SandboxRoot:         t.TempDir(),
		LocalScripts:        []agent.LocalScripts{{Name: "ops", Path: "/state/local-scripts/ops/local-scripts.yaml"}},
		LocalScriptsEnvFile: "/tmp/grain-local-scripts-1.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	i := slices.Index(args, "-local-scripts")
	if i < 0 || args[i+1] != "ops=/state/local-scripts/ops/local-scripts.yaml" {
		t.Fatalf("args %v: want -local-scripts ops=...", args)
	}
	j := slices.Index(args, "-local-scripts-env-file")
	if j < 0 || args[j+1] != "/tmp/grain-local-scripts-1.json" {
		t.Fatalf("args %v: want -local-scripts-env-file", args)
	}
}
