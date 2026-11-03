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

package staterepo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// designNewTaskForm is design/design/config-repo-settings.design.md §6.2's
// example, its capabilities trimmed to designCapabilities'.
const designNewTaskForm = `
default:
  fields:
    title: {visibility: shown, order: 10, label: Title}
    description:
      visibility: shown
      order: 20
      help_text: "Describe the goal, constraints, and acceptance criteria."
    repo:
      visibility: shown
      order: 40
      default: ai-on-gke/grain
      allow_no_repo: true
      offered: [ai-on-gke/grain, gke-labs/granule]
    newBranch: {visibility: more, order: 45}
    autoMerge: {visibility: shown, order: 70, default: true}
    model: {visibility: shown, order: 100, default: "", offered: [claude-opus-4-7]}
    agentBackendId: {visibility: hidden, default: ""}
    networkAccess: {visibility: more, order: 210, default: "", offered: ["", open, none]}
    promptExtension: {visibility: hidden, default: ""}
    sandboxShape:
      visibility: more
      order: 240
      allow_custom_numbers: true
      default_preset: ""
      presets:
        - {id: standard, label: Standard, cpus: 4, memory_mb: 8192, disk_gb: 30}
  capabilities:
    defaults: [corp-research, api:ops/triage]
    offered: ["*"]
    hidden: [k8s-kubeconfig]
    display:
      corp-research: {label: "Internal Code & Doc Search", order: 10}
repos:
  ai-on-gke/grain:
    fields:
      reviewTemplateId: {visibility: shown, order: 105, default: go-ui-review}
    capabilities:
      defaults: [corp-research, self-debug]
templates:
  release-cut:
    fields:
      autoMerge: {visibility: shown, default: false}
      networkAccess: {visibility: hidden, default: none}
boards:
  triage:
    fields:
      approved: {visibility: hidden, default: false}
`

func writeNewTaskForm(t *testing.T, dir, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, SettingsDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, SettingsDir, NewTaskFormFile), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDesignNewTaskFormParsesAndResolves(t *testing.T) {
	form, err := ParseNewTaskFormFile([]byte(designNewTaskForm))
	if err != nil {
		t.Fatalf("ParseNewTaskFormFile: %v", err)
	}
	r := form.Resolve(model.NewTaskFormContext{Repo: "ai-on-gke/grain", TemplateID: "release-cut", BoardID: "triage"})
	if def, ok := r.Hidden("networkAccess"); !ok || def != "none" {
		t.Errorf("template layer: networkAccess hidden=%v default=%v", ok, def)
	}
	if def, ok := r.Hidden("approved"); !ok || def != false {
		t.Errorf("board layer: approved hidden=%v default=%v", ok, def)
	}
	if f := r.Fields["autoMerge"]; f.Default != false || f.Order == nil || *f.Order != 70 {
		t.Errorf("autoMerge = %+v: template default over deployment order", f)
	}
	if got := r.Fields["reviewTemplateId"].Default; got != "go-ui-review" {
		t.Errorf("repo layer: reviewTemplateId default = %v", got)
	}
	if d := r.Capabilities.Defaults; d == nil || strings.Join(*d, ",") != "corp-research,self-debug" {
		t.Errorf("repo layer capability defaults = %v", d)
	}
	if got := r.Capabilities.Display["corp-research"].Label; got != "Internal Code & Doc Search" {
		t.Errorf("display label = %q", got)
	}
	plain := form.Resolve(model.NewTaskFormContext{Repo: "gke-labs/granule"})
	if _, ok := plain.Hidden("approved"); ok {
		t.Error("the triage board's layer applied without the board")
	}
	if d := plain.Capabilities.Defaults; d == nil || strings.Join(*d, ",") != "corp-research,api:ops/triage" {
		t.Errorf("deployment capability defaults = %v", d)
	}
}

func TestNewTaskFormRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ text, want string }{
		"unknown key":         {"default:\n  fields:\n    title: {visible: shown}\n", "visible"},
		"unknown field":       {"default:\n  fields:\n    colour: {visibility: shown}\n", "unknown field"},
		"bad visibility":      {"default:\n  fields:\n    model: {visibility: maybe}\n", `"maybe"`},
		"hidden title":        {"repos:\n  a/b:\n    fields:\n      title: {visibility: hidden}\n", "title is required"},
		"hidden repo":         {"default:\n  fields:\n    repo: {visibility: hidden}\n", "needs a default"},
		"hidden none":         {"default:\n  fields:\n    repo: {visibility: hidden, default: none}\n", "allow_no_repo"},
		"hidden bad repo":     {"default:\n  fields:\n    repo: {visibility: hidden, default: nope}\n", "owner/name"},
		"bad repo layer name": {"repos:\n  nope:\n    fields: {}\n", "repos.nope"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseNewTaskFormFile([]byte(tc.text))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ParseNewTaskFormFile = %v, want an error mentioning %q", err, tc.want)
			}
		})
	}
	for _, ok := range []string{
		"",
		"default:\n  fields:\n    repo: {visibility: hidden, default: none, allow_no_repo: true}\n",
		"default:\n  fields:\n    repo: {visibility: hidden, default: a/b}\n    legacyCapabilities: {visibility: hidden}\n",
	} {
		if _, err := ParseNewTaskFormFile([]byte(ok)); err != nil {
			t.Errorf("ParseNewTaskFormFile(%q) = %v", ok, err)
		}
	}
}

// Check judges every layer's capabilities against the catalogue: one it
// does not define, one only CI may hold, and one the layer's repository
// may not hold are each refused, while a repository-restricted one is
// fine in the deployment's own layer.
func TestCheckNewTaskFormJudgesCapabilities(t *testing.T) {
	dir := t.TempDir()
	writeCapabilities(t, dir, designCapabilities)
	writeNewTaskForm(t, dir, designNewTaskForm)
	if err := checkNewTaskForm(dir); err != nil {
		t.Fatalf("checkNewTaskForm(design) = %v", err)
	}
	for name, tc := range map[string]struct{ text, want string }{
		"undefined": {"default:\n  capabilities:\n    defaults: [no-such-thing]\n", "default.capabilities.defaults: capability no-such-thing is not defined"},
		"ci only":   {"default:\n  capabilities:\n    offered: [gcp-key]\n", "CI-only"},
		"disabled":  {"default:\n  capabilities:\n    offered: [k8s-kubeconfig]\n", "disabled"},
		"repo":      {"repos:\n  other/repo:\n    capabilities:\n      defaults: [self-debug]\n", "repos.other/repo.capabilities.defaults"},
	} {
		t.Run(name, func(t *testing.T) {
			writeNewTaskForm(t, dir, tc.text)
			err := checkNewTaskForm(dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("checkNewTaskForm = %v, want an error mentioning %q", err, tc.want)
			}
		})
	}
	writeNewTaskForm(t, dir, "default:\n  capabilities:\n    defaults: [self-debug]\n")
	if err := checkNewTaskForm(dir); err != nil {
		t.Errorf("a repository-restricted default in the deployment's layer: %v", err)
	}
}

// The file is grain_config.new_task_form: importing a dump that carries
// it stores it, and exporting writes it back (and no file for a
// deployment with none).
func TestNewTaskFormFileImportsAndExports(t *testing.T) {
	ctx := context.Background()
	db := openSchema(t)
	if err := model.New(db).PutConfig(ctx, model.Config{}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := ExportConfig(ctx, db, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, SettingsDir, NewTaskFormFile)); !os.IsNotExist(err) {
		t.Fatalf("a deployment with no form configuration exported %s (stat: %v)", NewTaskFormPath, err)
	}
	writeNewTaskForm(t, dir, designNewTaskForm)

	fresh := openSchema(t)
	if err := Import(ctx, fresh, dir); err != nil {
		t.Fatalf("Import: %v", err)
	}
	cfg, err := model.New(fresh).GetConfig(ctx)
	if err != nil || cfg == nil || cfg.NewTaskForm == nil {
		t.Fatalf("GetConfig = %+v, %v", cfg, err)
	}
	if _, ok := cfg.NewTaskForm.Boards["triage"]; !ok {
		t.Errorf("stored form = %+v", cfg.NewTaskForm)
	}
	out := t.TempDir()
	if err := ExportConfig(ctx, fresh, out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(out, SettingsDir, NewTaskFormFile))
	if err != nil {
		t.Fatalf("export wrote no %s: %v", NewTaskFormPath, err)
	}
	again, err := ParseNewTaskFormFile(data)
	if err != nil {
		t.Fatalf("exported file does not parse: %v\n%s", err, data)
	}
	r := again.Resolve(model.NewTaskFormContext{TemplateID: "release-cut"})
	if def, ok := r.Hidden("networkAccess"); !ok || def != "none" {
		t.Errorf("exported template layer: networkAccess hidden=%v default=%v\n%s", ok, def, data)
	}
}
