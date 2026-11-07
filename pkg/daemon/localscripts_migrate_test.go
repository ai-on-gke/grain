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
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/localscripts"
	"github.com/ai-on-gke/grain/pkg/model"
)

func writeDeclaration(t *testing.T, stateDir, name, decl string, scripts ...string) {
	t.Helper()
	dir := filepath.Join(stateDir, localscripts.StateRepoDir, name)
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, localscripts.DeclarationFile), []byte(decl), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, s := range scripts {
		writeExecutableStub(t, filepath.Join(dir, "scripts", s), "#!/bin/sh\n# grain: "+s+"\necho "+s+"\n")
	}
}

// TestMigrateLocalScriptsRewritesStoredGrants is the stored-grant half of
// the local-scripts -> host-script fidelity test (the run half is
// pkg/capability/intree's TestLocalScriptsDeclarationRunsTheSameOnHostScript):
// a task holding local-scripts:ops holds one host-script grant per
// script of ops afterwards, the host-script server is configured with
// them, a declaration host-script cannot serve is left as it was, and a
// second start changes nothing.
func TestMigrateLocalScriptsRewritesStoredGrants(t *testing.T) {
	ctx := context.Background()
	store, closeStore := openTestStore(t, t.TempDir())
	defer closeStore()
	stateDir := t.TempDir()
	writeDeclaration(t, stateDir, "ops", "scripts:\n  dir: scripts\n", "deploy", "status")
	writeDeclaration(t, stateDir, "guarded", "scripts:\n  list:\n    - path: scripts/wipe\n      approve: true\n", "wipe")
	local := localscripts.Config{StateDir: stateDir}

	if err := store.PutTask(ctx, model.Task{ID: "1", Intent: model.IntentImplement, Title: "t",
		Grants: []model.Grant{{Capability: "local-scripts:ops", Via: model.GrantByLabel},
			{Capability: "local-scripts:guarded", Via: model.GrantByLabel}}}); err != nil {
		t.Fatal(err)
	}

	restore, err := migrateLocalScripts(ctx, store, local)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)

	wantOps := []string{
		localscripts.HostScriptServerID + `#host-script={"script":"ops-deploy"}`,
		localscripts.HostScriptServerID + `#host-script={"script":"ops-status"}`,
	}
	wantGuarded := []string{
		localscripts.HostScriptServerID + `#host-script={"script":"guarded-wipe"}`,
	}
	wantAll := append(slices.Clone(wantGuarded), wantOps...)
	slices.Sort(wantAll)
	task, err := store.GetTask(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, g := range task.Grants {
		got = append(got, g.Capability)
	}
	slices.Sort(got)
	if !slices.Equal(got, wantAll) {
		t.Fatalf("task grants %v: want %v", got, wantAll)
	}
	if exp := model.CanonicalLegacyGrants("local-scripts:ops"); !slices.Equal(exp, wantOps) {
		t.Errorf("local-scripts:ops still written somewhere expands to %v, want %v", exp, wantOps)
	}
	if exp := model.CanonicalLegacyGrants("local-scripts:guarded"); !slices.Equal(exp, wantGuarded) {
		t.Errorf("local-scripts:guarded expands to %v, want %v", exp, wantGuarded)
	}

	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	server, ok := cfg.MCPServers[localscripts.HostScriptServerID]
	if !ok || !localscripts.IsHostScriptServer(server) {
		t.Fatalf("mcp_servers %v: want the host-script server added", cfg.MCPServers)
	}
	scripts := server.Config["scripts"].([]any)
	if len(scripts) != 3 {
		t.Fatalf("%d scripts configured, want 3", len(scripts))
	}
	foundApproved := false
	for _, raw := range scripts {
		m := raw.(map[string]any)
		if m["name"] == "guarded-wipe" && m["approve"] == true {
			foundApproved = true
		}
	}
	if !foundApproved {
		t.Fatalf("scripts %+v: want guarded-wipe configured with approve: true", scripts)
	}

	// The next start: nothing more.
	restore2, err := migrateLocalScripts(ctx, store, local)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore2)
	cfg, _ = store.GetConfig(ctx)
	if n := len(cfg.MCPServers[localscripts.HostScriptServerID].Config["scripts"].([]any)); n != 3 {
		t.Fatalf("%d scripts configured after a second start, want 3", n)
	}
	task, _ = store.GetTask(ctx, "1")
	if len(task.Grants) != 3 {
		t.Fatalf("grants after a second start: %+v", task.Grants)
	}
}
