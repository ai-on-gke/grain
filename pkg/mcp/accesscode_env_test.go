package mcp

import (
	"path/filepath"
	"strings"
	"testing"
)

// The daemon names its access code's file (the local API token) in its
// own environment for the `grain mcpserver` it starts; nothing a sandbox
// runs inherits it (grain/task-4343). A container sandbox's `docker
// exec` forwards no variable, and a host sandbox's commands get the
// sandboxPassthrough allowlist only.
func TestSandboxCommandsDoNotInheritTheAccessCodeFile(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("GRAIN_LOCAL_API_TOKEN_FILE", filepath.Join(dataDir, "local-api-token"))
	t.Setenv("GRAIN_DATA_DIR", dataDir)
	for _, kv := range sandboxEnv(t.TempDir()) {
		if strings.HasPrefix(kv, "GRAIN_LOCAL_API_TOKEN_FILE=") || strings.HasPrefix(kv, "GRAIN_DATA_DIR=") {
			t.Errorf("a host sandbox command inherits %s", kv)
		}
	}
	r := &ContainerExecRunner{Container: "grain-sandbox-x"}
	for _, a := range r.execArgs([]string{"sh", "-c", "env"}, true) {
		if a == "-e" || a == "--env" || a == "--env-file" || strings.Contains(a, "GRAIN_LOCAL_API_TOKEN_FILE") {
			t.Errorf("a container sandbox exec passes %q", a)
		}
	}
}
