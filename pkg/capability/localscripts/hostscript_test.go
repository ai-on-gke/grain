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
	if !strings.HasPrefix(s["script"].(string), "exec '"+root) {
		t.Errorf("script %q: want it to exec the declaration's own file", s["script"])
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
