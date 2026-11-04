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

package intree

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/localscripts"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/localexec"
)

// TestLocalScriptsDeclarationRunsTheSameOnHostScript is the fidelity
// test of the local-scripts -> host-script conversion: a declaration's
// scripts, served by local-scripts as MCP tools and by a host-script
// server configured with ConvertToHostScripts' entries, see the same
// argv, working directory and secret, and answer with the same stdout,
// stderr and exit status.
func TestLocalScriptsDeclarationRunsTheSameOnHostScript(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, localscripts.StateRepoDir, "ops")
	for _, d := range []string{filepath.Join(dir, "scripts"), filepath.Join(dir, "tools"), filepath.Join(dir, "work")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel, content string, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(localscripts.DeclarationFile, `scripts:
  dir: scripts
  list:
    - path: tools/deploy.sh
      description: Deploy one thing.
      timeout: 90s
      env: {TOKEN: ops-token}
workdir: work
timeout: 2m
`, 0o644)
	body := "#!/bin/sh\n# grain: Show what it was called with.\n" +
		"for a in \"$@\"; do printf '<%s>\\n' \"$a\"; done\n" +
		"printf 'cwd=%s token=%s agent=%s\\n' \"$(basename \"$PWD\")\" \"${TOKEN-unset}\" \"${SSH_AUTH_SOCK-unset}\"\n" +
		"echo to-stderr >&2\nexit 3\n"
	write("scripts/show", body, 0o755)
	write("tools/deploy.sh", body, 0o755)

	cfg := localscripts.Config{StateDir: root}
	conv, err := localscripts.ConvertToHostScripts(cfg, "ops")
	if err != nil {
		t.Fatal(err)
	}
	// Every ambient variable host-script passes a script and
	// local-scripts did not is withheld by the converted body.
	for _, script := range conv.Scripts {
		body := script["script"].(string)
		var unset string
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, "unset ") {
				unset = line
				break
			}
		}
		for _, v := range AmbientIdentityEnv {
			if len(localexec.BaseEnviron([]string{v + "=x"})) == 0 && !slices.Contains(strings.Fields(unset), v) {
				t.Errorf("%s: body %q passes %s on, which local-scripts withheld", script["name"], body, v)
			}
		}
	}
	servers := map[string]protocol.ServerConfig{}
	id, err := localscripts.AddToHostScriptServer(servers, []localscripts.HostScripts{conv})
	if err != nil {
		t.Fatal(err)
	}
	entry := servers[id]
	if entry.Env["GRAIN_LOCAL_SCRIPTS_OPS_TOKEN"] != "$secret:ops-token" {
		t.Fatalf("server env %v: want the script's secret bound", entry.Env)
	}
	// The configuration as the store hands it back: JSON numbers and all.
	raw, err := json.Marshal(entry.Config)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if err := CheckBuiltinConfig("host-script", config); err != nil {
		t.Fatalf("converted configuration is refused: %v", err)
	}

	// local-scripts, as the run's mcpserver serves it.
	path, _ := cfg.Path("ops")
	execs, err := localscripts.Open([]string{path}, localscripts.Credentials{"ops-token": "s3cret"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, e := range execs {
			e.Close()
		}
	})
	// host-script, with the server's own environment binding the secret
	// as its mcpServers env would.
	// SSH_AUTH_SOCK is ambient identity host-script would pass on, and
	// local-scripts never did.
	environ := append([]string{"GRAIN_LOCAL_SCRIPTS_OPS_TOKEN=s3cret", "SSH_AUTH_SOCK=/run/agent.sock"}, hostScriptEnviron...)
	s, err := NewHostScript(HostScriptConfig{Listen: "127.0.0.1:0", DataDir: t.TempDir(), Environ: func() []string { return environ }})
	if err != nil {
		t.Fatal(err)
	}
	ps, err := attachConfigured(t, s, config, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := &hostScriptFixture{t: t, s: s, ps: ps}

	argv := []string{"two words", "$(touch /tmp/pwned)", ""}
	for tool, script := range map[string]string{"local_show": "ops-show", "local_deploy_sh": "ops-deploy_sh"} {
		args := make([]any, len(argv))
		for i, a := range argv {
			args[i] = a
		}
		res := execs[0].Call(context.Background(), tool, map[string]any{"args": args})
		var local localexec.Result
		if err := json.Unmarshal([]byte(res.Text), &local); err != nil {
			t.Fatalf("%s answered %q: %v", tool, res.Text, err)
		}
		host := f.testScript(script, argv...)
		if host.Refused != "" {
			t.Fatalf("host-script refused %s: %s", script, host.Refused)
		}
		if host.Stdout != local.Stdout || host.Stderr != local.Stderr || host.ExitCode != local.ExitCode {
			t.Errorf("%s on host-script = (%q, %q, %d); local-scripts' %s = (%q, %q, %d)",
				script, host.Stdout, host.Stderr, host.ExitCode, tool, local.Stdout, local.Stderr, local.ExitCode)
		}
	}
}
