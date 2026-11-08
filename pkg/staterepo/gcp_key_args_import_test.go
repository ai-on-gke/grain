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

package staterepo_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

func TestStateRepoImportsOldFormGCPKeyGrantsAndRewritesOnExport(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	src, srcDB := openDB(t)

	cfg := model.DefaultConfig()
	cfg.MCPServers = map[string]protocol.ServerConfig{
		model.GCPCapabilityServerID: {
			Command: "grain",
			Args:    []string{"capability", "gcp"},
			Builtin: "gcp",
			Config: map[string]any{
				"admin_credential_source": "ambient",
				"permission_sets": []any{
					map[string]any{"name": "sandbox-agent", "project": "acme-ai", "roles": []any{"roles/viewer"}},
					map[string]any{"name": "iap-test", "project": "acme-ai", "roles": []any{"roles/iap.admin"}},
				},
			},
		},
	}
	if err := src.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := src.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                widgets,
		DefaultCapabilities: []string{`https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"iap-test","primary":true}`},
		CICapabilities:      []string{"gcp-key", "package-publish"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := src.PutTemplate(ctx, model.Template{
		ID:    "tpl-deploy",
		Name:  "Deploy",
		Title: "Deploy",
		Body:  "Deploy.",
		Grants: []model.Grant{{
			Capability: `https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"sandbox-agent","primary":true}`,
			Via:        "label",
		}},
	}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := staterepo.Export(ctx, srcDB, dir); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.WriteSchemaVersion(dir, model.SchemaVersion); err != nil {
		t.Fatal(err)
	}

	// Simulate an operator's current-schema grain/config checkout whose files
	// still spell gcp#gcp-key grants with the removed account and activate args.
	capFile := filepath.Join(dir, staterepo.CapabilitiesPath)
	oldCapabilitiesYAML := `defaults:
  task:
    - https://grain.dev/capabilities/gcp#gcp-key:
        with:
          account: default
          activate: true
  ci: []
  repos:
    acme/widgets:
      task:
        - https://grain.dev/capabilities/gcp#gcp-key:
            with:
              account: iap-test
      ci:
        - gcp-key
        - package-publish
servers:
  - id: https://grain.dev/capabilities/gcp
    command: grain
    args: ["capability", "gcp"]
    builtin: gcp
    enabled: true
    config:
      admin_credential_source: ambient
      permission_sets:
        - name: sandbox-agent
          project: acme-ai
          roles: ["roles/viewer"]
        - name: iap-test
          project: acme-ai
          roles: ["roles/iap.admin"]
    methods:
      - name: gcp-key
        args:
          account:
            allowed: ["default", "iap-test"]
          activate:
            allowed: ["true"]
        restrictions:
          requires_person_when:
            account: ["iap-test"]
`
	if err := os.WriteFile(capFile, []byte(oldCapabilitiesYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	tplFile := filepath.Join(dir, "settings", "templates.yml")
	b, err := os.ReadFile(tplFile)
	if err != nil {
		t.Fatal(err)
	}
	replaced := strings.ReplaceAll(string(b),
		`"permission_set":"sandbox-agent","primary":true`,
		`"account":"iap-test","activate":true`)
	if replaced == string(b) {
		t.Fatalf("expected template grant in %s, got:\n%s", tplFile, b)
	}
	if err := os.WriteFile(tplFile, []byte(replaced), 0o644); err != nil {
		t.Fatal(err)
	}

	_, checkDB := openDB(t)
	if _, err := staterepo.Check(ctx, checkDB, dir, model.SchemaVersion); err != nil {
		t.Fatalf("staterepo.Check rejected one-release old-form gcp#gcp-key grants: %v", err)
	}

	dst, dstDB := openDB(t)
	if err := staterepo.Import(ctx, dstDB, dir); err != nil {
		t.Fatalf("staterepo.Import failed on old-form gcp#gcp-key grants: %v", err)
	}

	wantSandbox := `https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"sandbox-agent","primary":true}`
	wantIAP := `https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"iap-test","primary":true}`

	gotCfg, err := dst.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cat := gotCfg.CapabilityPolicy.Catalogue
	if cat == nil || !slices.Equal(cat.Defaults.Task, []string{wantSandbox}) {
		t.Fatalf("imported catalogue task defaults = %+v, want [%s]", cat, wantSandbox)
	}
	if rd := cat.Defaults.Repos["acme/widgets"]; !slices.Equal(rd.Task, []string{wantIAP}) {
		t.Errorf("imported catalogue repo defaults = %+v, want [%s]", rd, wantIAP)
	}
	m := cat.Servers[0].Methods[0]
	if _, has := m.Args["account"]; has {
		t.Errorf("imported catalogue still has account arg: %+v", m.Args)
	}
	if got := m.Args["permission_set"].Allowed; !slices.Equal(got, []string{"sandbox-agent", "iap-test"}) {
		t.Errorf("imported catalogue permission_set.allowed = %v", got)
	}

	gotTpl, err := dst.GetTemplate(ctx, "tpl-deploy")
	if err != nil || gotTpl == nil {
		t.Fatalf("GetTemplate: %v, %+v", err, gotTpl)
	}
	if len(gotTpl.Grants) != 1 || gotTpl.Grants[0].Capability != wantIAP {
		t.Errorf("imported template grants = %+v, want [%s]", gotTpl.Grants, wantIAP)
	}

	// Re-exporting rewrites the state repo to permission_set and drops activate.
	outDir := t.TempDir()
	if err := staterepo.Export(ctx, dstDB, outDir); err != nil {
		t.Fatal(err)
	}
	exportedCapBytes, err := os.ReadFile(filepath.Join(outDir, staterepo.CapabilitiesPath))
	if err != nil {
		t.Fatal(err)
	}
	exportedCap := string(exportedCapBytes)
	if strings.Contains(exportedCap, "account:") || strings.Contains(exportedCap, "activate:") {
		t.Errorf("exported capabilities.yml still contains removed args:\n%s", exportedCap)
	}
	if !strings.Contains(exportedCap, "permission_set: sandbox-agent") || !strings.Contains(exportedCap, "permission_set: iap-test") {
		t.Errorf("exported capabilities.yml missing rewritten permission_set:\n%s", exportedCap)
	}
}

// An "account" naming a permission set that is not configured -- the grant
// migration 109 used to keep as written -- is rewritten to permission_set
// like any other, so the deployment's own state repository passes
// `grain state check` (docs/test-runs/2026-11-07-capabilities-fencing-admission.md,
// F1) on every surface that validates it: templates, schedules, and the
// catalogue's task and per-repository CI defaults.
func TestStateRepoLoadsGCPKeyAccountGrantsNamingAnUnconfiguredSet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	src, srcDB := openDB(t)
	cfg := model.DefaultConfig()
	cfg.MCPServers = map[string]protocol.ServerConfig{
		model.GCPCapabilityServerID: {
			Command: "grain",
			Args:    []string{"capability", "gcp"},
			Builtin: "gcp",
			Config: map[string]any{
				"admin_credential_source": "ambient",
				"permission_sets": []any{
					map[string]any{"name": "sandbox-agent", "project": "acme-ai", "roles": []any{"roles/viewer"}},
				},
			},
		},
	}
	if err := src.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := src.PutRepoConfig(ctx, model.RepoConfig{Repo: widgets}); err != nil {
		t.Fatal(err)
	}
	canonical := `https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"sandbox-agent","primary":true}`
	if err := src.PutTemplate(ctx, model.Template{
		ID: "tpl-ghost", Name: "Ghost", Title: "Ghost", Body: "Ghost.",
		Grants: []model.Grant{{Capability: canonical, Via: "label"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := src.PutSchedule(ctx, model.Schedule{
		ID: "sched-ghost", Target: widgets, Title: "Ghost", Body: "Ghost.",
		Grants: []model.Grant{{Capability: canonical, Via: "label"}},
	}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := staterepo.Export(ctx, srcDB, dir); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.WriteSchemaVersion(dir, model.SchemaVersion); err != nil {
		t.Fatal(err)
	}

	ghost := `"account":"ghost-set","activate":false`
	for _, f := range []string{"templates.yml", "schedules.yml"} {
		path := filepath.Join(dir, "settings", f)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		replaced := strings.ReplaceAll(string(b), `"permission_set":"sandbox-agent","primary":true`, ghost)
		if replaced == string(b) {
			t.Fatalf("expected a gcp-key grant in %s, got:\n%s", path, b)
		}
		if err := os.WriteFile(path, []byte(replaced), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	capabilitiesYAML := `defaults:
  task:
    - https://grain.dev/capabilities/gcp#gcp-key:
        with:
          account: ghost-set
          activate: false
  repos:
    acme/widgets:
      ci:
        - https://grain.dev/capabilities/gcp#gcp-key:
            with:
              account: ghost-set
servers:
  - id: https://grain.dev/capabilities/gcp
    command: grain
    args: ["capability", "gcp"]
    builtin: gcp
    enabled: true
    config:
      admin_credential_source: ambient
      permission_sets:
        - name: sandbox-agent
          project: acme-ai
          roles: ["roles/viewer"]
`
	if err := os.WriteFile(filepath.Join(dir, staterepo.CapabilitiesPath), []byte(capabilitiesYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	_, checkDB := openDB(t)
	if _, err := staterepo.Check(ctx, checkDB, dir, model.SchemaVersion); err != nil {
		t.Fatalf("staterepo.Check refused gcp#gcp-key account grants naming an unconfigured set: %v", err)
	}
	dst, dstDB := openDB(t)
	if err := staterepo.Import(ctx, dstDB, dir); err != nil {
		t.Fatalf("staterepo.Import: %v", err)
	}

	want := `https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"ghost-set","primary":true}`
	gotTpl, err := dst.GetTemplate(ctx, "tpl-ghost")
	if err != nil || gotTpl == nil || len(gotTpl.Grants) != 1 || gotTpl.Grants[0].Capability != want {
		t.Errorf("imported template = %+v (err=%v), want grant %s", gotTpl, err, want)
	}
	if gotTpl != nil && !strings.Contains(gotTpl.Body, `permission set "ghost-set" is not configured`) {
		t.Errorf("imported template body = %q, want the migration note", gotTpl.Body)
	}
	gotSched, err := dst.GetSchedule(ctx, "sched-ghost")
	if err != nil || gotSched == nil || len(gotSched.Grants) != 1 || gotSched.Grants[0].Capability != want {
		t.Errorf("imported schedule = %+v (err=%v), want grant %s", gotSched, err, want)
	}
	gotCfg, err := dst.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cat := gotCfg.CapabilityPolicy.Catalogue
	if cat == nil || !slices.Equal(cat.Defaults.Task, []string{want}) ||
		!slices.Equal(cat.Defaults.Repos["acme/widgets"].CI, []string{want}) {
		t.Fatalf("imported catalogue defaults = %+v, want task and acme/widgets CI [%s]", cat, want)
	}

	// The re-export is canonical, and a fresh check of it passes.
	outDir := t.TempDir()
	if err := staterepo.Export(ctx, dstDB, outDir); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.WriteSchemaVersion(outDir, model.SchemaVersion); err != nil {
		t.Fatal(err)
	}
	_, recheckDB := openDB(t)
	if _, err := staterepo.Check(ctx, recheckDB, outDir, model.SchemaVersion); err != nil {
		t.Fatalf("staterepo.Check of the re-export: %v", err)
	}
}
