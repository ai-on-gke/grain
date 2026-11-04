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

package localscripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

const showScript = "#!/bin/sh\n# grain: Show it.\necho hi\n"

func TestConvertToHostScriptsRefusesApproval(t *testing.T) {
	root := stateRepo(t, "ops", `scripts:
  list:
    - path: scripts/show
      approve: true
`, map[string]string{"show": showScript})
	_, err := ConvertToHostScripts(Config{StateDir: root}, "ops")
	if err == nil || !strings.Contains(err.Error(), "approve") {
		t.Fatalf("err %v: want a declaration with an approved script refused", err)
	}
}

func TestConvertToHostScriptsEntry(t *testing.T) {
	root := stateRepo(t, "ops", `scripts:
  list:
    - path: scripts/show
      timeout: 1500ms
      env: {TOKEN: ops.token}
output_cap: 4096
`, map[string]string{"show": showScript})
	h, err := ConvertToHostScripts(Config{StateDir: root}, "ops")
	if err != nil {
		t.Fatal(err)
	}
	if got := h.Names(); !reflect.DeepEqual(got, []string{"ops-show"}) {
		t.Fatalf("names %v, want [ops-show]", got)
	}
	s := h.Scripts[0]
	if s["timeout"] != 2 || s["max_output"] != 4096 || s["description"] != "Show it." {
		t.Errorf("entry %v: want timeout rounded up to 2s, the output cap, and the # grain: description", s)
	}
	if body := s["script"].(string); !strings.Contains(body, "\nexec '"+root) || !strings.Contains(body, "SSH_AUTH_SOCK") {
		t.Errorf("script %q: want it to withhold the ambient identity and exec the declaration's own file", body)
	}
	if got := s["env"].(map[string]any)["TOKEN"]; got != "$env:GRAIN_LOCAL_SCRIPTS_OPS_TOKEN" {
		t.Errorf("env TOKEN = %v", got)
	}
	if h.Env["GRAIN_LOCAL_SCRIPTS_OPS_TOKEN"] != "$secret:ops.token" {
		t.Errorf("bindings %v", h.Env)
	}
}

func TestHostScriptNameTooLong(t *testing.T) {
	if _, err := HostScriptName(strings.Repeat("a", 60), "deploy"); err == nil {
		t.Fatal("want a name over host-script's 63 characters refused")
	}
}

func TestAddToHostScriptServer(t *testing.T) {
	h := HostScripts{
		Declaration: "ops",
		Scripts:     []map[string]any{{"name": "ops-show"}, {"name": "ops-deploy"}},
		Env:         map[string]string{"GRAIN_LOCAL_SCRIPTS_T": "$secret:t"},
	}

	// None configured: the canonical one is created.
	servers := map[string]protocol.ServerConfig{}
	id, err := AddToHostScriptServer(servers, []HostScripts{h})
	if err != nil || id != HostScriptServerID {
		t.Fatalf("id %q, err %v", id, err)
	}
	if !IsHostScriptServer(servers[id]) || len(servers[id].Config["scripts"].([]any)) != 2 {
		t.Fatalf("server %+v", servers[id])
	}
	// Again: nothing is added twice.
	if _, err := AddToHostScriptServer(servers, []HostScripts{h}); err != nil {
		t.Fatal(err)
	}
	if n := len(servers[id].Config["scripts"].([]any)); n != 2 {
		t.Fatalf("%d scripts after adding twice, want 2", n)
	}

	// The operator's own, in the daemon's environment, is used, and an
	// entry of the same name is left as they have it; one on the host is
	// passed over.
	mine := "https://example.com/host-script"
	servers = map[string]protocol.ServerConfig{
		"https://example.com/on-host": {Command: "host-script", Run: protocol.RunHost, Builtin: "host-script"},
		mine: {Command: "grain", Args: []string{"capability", "host-script"},
			Config: map[string]any{"scripts": []any{map[string]any{"name": "ops-show", "script": "mine"}}}},
	}
	id, err = AddToHostScriptServer(servers, []HostScripts{h})
	if err != nil || id != mine {
		t.Fatalf("id %q, err %v: want the operator's in-daemon server", id, err)
	}
	scripts := servers[mine].Config["scripts"].([]any)
	if len(scripts) != 2 || scripts[0].(map[string]any)["script"] != "mine" {
		t.Fatalf("scripts %v", scripts)
	}

	// A binding the server already makes differently is refused.
	servers[mine] = protocol.ServerConfig{Command: "grain", Args: []string{"capability", "host-script"},
		Env: map[string]string{"GRAIN_LOCAL_SCRIPTS_T": "$secret:other"}}
	if _, err := AddToHostScriptServer(servers, []HostScripts{h}); err == nil {
		t.Fatal("want a conflicting secret binding refused")
	}
}

func TestHostScriptGrants(t *testing.T) {
	got, err := HostScriptGrants(HostScriptServerID, HostScripts{Scripts: []map[string]any{{"name": "ops-show"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("grants %v", got)
	}
	spec, err := model.CapabilityGrantSpec(got[0])
	if err != nil {
		t.Fatal(err)
	}
	if spec.Capability != HostScriptServerID+"#host-script" || spec.With["script"] != "ops-show" {
		t.Fatalf("grant %q decodes to %+v", got[0], spec)
	}
}

// TestHostScriptBodyRechecksTheFile: the per-run snapshot refused a
// script anyone but grain's own user could write, and host-script makes
// no snapshot, so the body makes the same refusal on every call --
// the directories between it and the declaration's folder included -- and
// withholds the ambient identity local-scripts never passed a script.
func TestHostScriptBodyRechecksTheFile(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "scripts")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(sub, "show")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$1 ${SSH_AUTH_SOCK-unset} ${KUBECONFIG-unset}\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := filepath.Join(dir, "body")
	if err := os.WriteFile(body, []byte(hostScriptBody("ops", script, []string{sub, dir}, map[string]string{"KUBECONFIG": "$env:X"})), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func() (string, int) {
		cmd := exec.Command("bash", body, "arg")
		cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "SSH_AUTH_SOCK=/run/agent", "KUBECONFIG=/declared"}
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return string(out), code
	}
	if out, code := run(); code != 0 || out != "arg unset /declared\n" {
		t.Fatalf("(%q, %d): want the script run, the ssh agent withheld and the declared variable kept", out, code)
	}
	if err := os.Chmod(script, 0o757); err != nil {
		t.Fatal(err)
	}
	if out, code := run(); code != 126 || !strings.Contains(out, "not running it") {
		t.Fatalf("(%q, %d): want a world-writable script refused", out, code)
	}
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{sub, dir} {
		if err := os.Chmod(d, 0o775); err != nil {
			t.Fatal(err)
		}
		if out, code := run(); code != 126 || !strings.Contains(out, "not running it") {
			t.Fatalf("(%q, %d): want a script under a group-writable %s refused", out, code, d)
		}
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, code := run(); code != 0 {
		t.Fatalf("exit %d: want the script run again once its directories are grain's alone", code)
	}
	target := script + ".real"
	if err := os.Rename(script, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, script); err != nil {
		t.Fatal(err)
	}
	if _, code := run(); code != 126 {
		t.Fatalf("exit %d: want a script replaced by a symlink refused", code)
	}
}

// TestMigrateToHostScriptLeavesAClashOnLocalScripts: a script the
// operator configured under the name a declaration's script would take,
// running something else, is not what local-scripts:<name> becomes.
func TestMigrateToHostScriptLeavesAClashOnLocalScripts(t *testing.T) {
	root := stateRepo(t, "ops", "scripts:\n  dir: scripts\n", map[string]string{"show": showScript})
	cfg := Config{StateDir: root}
	servers := map[string]protocol.ServerConfig{
		HostScriptServerID: {Command: "grain", Args: []string{"capability", "host-script"},
			Config: map[string]any{"scripts": []any{map[string]any{"name": "ops-show", "script": "rm -rf \"$1\""}}}},
	}
	renames, skipped, err := MigrateToHostScript(cfg, servers)
	if err != nil {
		t.Fatal(err)
	}
	if len(renames) != 0 || len(skipped) != 1 || !strings.Contains(skipped[0].Error(), "ops-show") {
		t.Fatalf("renames %v, skipped %v: want ops left on local-scripts", renames, skipped)
	}

	// Its own body, from an earlier start, is ops's.
	renames, skipped, err = MigrateToHostScript(cfg, map[string]protocol.ServerConfig{})
	if err != nil || len(skipped) != 0 || len(renames) != 1 {
		t.Fatalf("renames %v, skipped %v, err %v", renames, skipped, err)
	}
	servers = map[string]protocol.ServerConfig{}
	if _, _, err := MigrateToHostScript(cfg, servers); err != nil {
		t.Fatal(err)
	}
	if renames, skipped, err = MigrateToHostScript(cfg, servers); err != nil || len(skipped) != 0 || len(renames) != 1 {
		t.Fatalf("second start: renames %v, skipped %v, err %v", renames, skipped, err)
	}
}

// TestMigrateToHostScriptFollowsTheDeclaration: what an earlier start
// converted is not kept once the declaration changes. A script given
// approve: true no longer runs unapproved through host-script, a removed
// script is gone, the operator's own script and binding
// stay, and a secret no script binds any more is unbound.
func TestMigrateToHostScriptFollowsTheDeclaration(t *testing.T) {
	root := stateRepo(t, "ops", `scripts:
  list:
    - path: scripts/show
      env: {TOKEN: ops.token}
    - path: scripts/wipe
`, map[string]string{"show": showScript, "wipe": showScript})
	cfg := Config{StateDir: root}
	servers := map[string]protocol.ServerConfig{
		HostScriptServerID: {Command: "grain", Args: []string{"capability", "host-script"},
			Env: map[string]string{"MINE": "$secret:mine"},
			Config: map[string]any{"scripts": []any{map[string]any{"name": "mine", "script": "echo mine",
				"env": map[string]any{"M": "$env:MINE"}}}}},
	}
	names := func() []string {
		var out []string
		for _, item := range servers[HostScriptServerID].Config["scripts"].([]any) {
			out = append(out, item.(map[string]any)["name"].(string))
		}
		return out
	}
	if _, _, err := MigrateToHostScript(cfg, servers); err != nil {
		t.Fatal(err)
	}
	if got := names(); !reflect.DeepEqual(got, []string{"mine", "ops-show", "ops-wipe"}) {
		t.Fatalf("scripts %v", got)
	}
	before, _ := model.EncodeMCPServers(servers)
	if _, _, err := MigrateToHostScript(cfg, servers); err != nil {
		t.Fatal(err)
	}
	if after, _ := model.EncodeMCPServers(servers); after != before {
		t.Fatalf("a second start changed the server:\n%s\n%s", before, after)
	}

	// wipe is removed, and show no longer names a secret.
	decl := filepath.Join(root, StateRepoDir, "ops", DeclarationFile)
	if err := os.Remove(filepath.Join(root, StateRepoDir, "ops", "scripts", "wipe")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(decl, []byte("scripts:\n  list:\n    - path: scripts/show\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	renames, _, err := MigrateToHostScript(cfg, servers)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(); !reflect.DeepEqual(got, []string{"mine", "ops-show"}) {
		t.Fatalf("scripts %v: want ops-wipe gone", got)
	}
	if len(renames["local-scripts:ops"]) != 1 {
		t.Fatalf("renames %v", renames)
	}
	if env := servers[HostScriptServerID].Env; !reflect.DeepEqual(env, map[string]string{"MINE": "$secret:mine"}) {
		t.Fatalf("env %v: want ops.token unbound and the operator's own binding kept", env)
	}

	// show now waits for approval: none of ops runs through host-script.
	if err := os.WriteFile(decl, []byte("scripts:\n  list:\n    - path: scripts/show\n      approve: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	renames, skipped, err := MigrateToHostScript(cfg, servers)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(); !reflect.DeepEqual(got, []string{"mine"}) || len(renames) != 0 || len(skipped) != 1 {
		t.Fatalf("scripts %v, renames %v, skipped %v: want ops off host-script", got, renames, skipped)
	}
}
