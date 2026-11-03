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

package ui

import (
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// applyHiddenFormDefaults is POST /api/tasks' half of
// settings/new-task-form.yml (design/design/config-repo-settings.design.md §6.3):
// a field the resolved form hides is filed with the default the form
// gives it, whatever the request says, so hiding a field is a decision
// the form's configuration makes rather than one only the browser keeps.
// A hidden field with no default of its own is left as the request has
// it -- which, for the form, is the built-in default it submitted.
func applyHiddenFormDefaults(req *CreateTaskRequest, form *model.NewTaskForm) error {
	if form == nil {
		return nil
	}
	ctx := model.NewTaskFormContext{Repo: requestRepo(req), TemplateID: req.TemplateID, BoardID: req.BoardID}
	resolved := form.Resolve(ctx)
	// The repository first: which repository's layer applies to every
	// other field depends on it.
	if def, ok := resolved.Hidden("repo"); ok && def != nil {
		repo := strings.TrimSpace(formString(def))
		if strings.EqualFold(repo, "none") {
			req.Repo, req.NoRepo = "", true
		} else if repo != "" {
			req.Repo, req.NoRepo = repo, false
		}
		ctx.Repo = requestRepo(req)
		resolved = form.Resolve(ctx)
	}
	var errs []string
	for id, f := range resolved.Fields {
		if f.Visibility != model.FormFieldHidden {
			continue
		}
		if id == "sandboxShape" {
			if err := applyHiddenSandboxShape(req, f); err != nil {
				errs = append(errs, err.Error())
			}
			continue
		}
		def := f.Default
		if def == nil {
			continue
		}
		switch id {
		case "description":
			req.Description = formString(def)
		case "base":
			req.Base = formString(def)
		case "newBranch":
			req.NewBranch = formBool(def)
		case "autoMerge":
			req.AutoMerge = formBool(def)
		case "noPort":
			req.NoPort = formBool(def)
		case "fixesBaseBranch":
			req.FixesBaseBranch = formBool(def)
		case "approved":
			req.Approved = formBool(def)
		case "atFront":
			v := formBool(def)
			req.AtFront = &v
		case "model":
			req.Model = formString(def)
		case "agentBackendId":
			req.AgentBackendID = formString(def)
		case "networkAccess":
			req.NetworkAccess = formString(def)
		case "nothingNeededAction":
			req.NothingNeededAction = formString(def)
		case "reviewTemplateId":
			req.ReviewTemplateID = formString(def)
		case "promptExtension":
			req.PromptExtension = formString(def)
		case "dependsOn":
			req.DependsOn = formStrings(def)
		case "reads":
			req.Reads = formStrings(def)
		case "writes":
			req.Writes = formWrites(def)
		case "grants":
			caps := formStrings(def)
			req.Capabilities = &caps
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("settings/new-task-form.yml: %s", strings.Join(errs, "; "))
	}
	return nil
}

func applyHiddenSandboxShape(req *CreateTaskRequest, f model.NewTaskFormField) error {
	if f.DefaultPreset != nil && *f.DefaultPreset != "" {
		for _, p := range f.Presets {
			if p.ID == *f.DefaultPreset {
				req.SandboxCPUs, req.SandboxMemoryMB, req.SandboxDiskGB = p.CPUs, p.MemoryMB, p.DiskGB
				return nil
			}
		}
		return fmt.Errorf("sandboxShape: default_preset %q names no preset", *f.DefaultPreset)
	}
	if m, ok := f.Default.(map[string]any); ok {
		req.SandboxCPUs = formInt(m["cpus"])
		req.SandboxMemoryMB = formInt(m["memory_mb"])
		req.SandboxDiskGB = formInt(m["disk_gb"])
	}
	return nil
}

// requestRepo is the repository a create request names, "" for none.
func requestRepo(req *CreateTaskRequest) string {
	if req.NoRepo {
		return ""
	}
	return strings.TrimSpace(req.Repo)
}

func formString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

func formBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(x, "true") || x == "front"
	}
	return false
}

func formInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	}
	return 0
}

func formStrings(v any) []string {
	out := []string{}
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			if s := strings.TrimSpace(formString(e)); s != "" {
				out = append(out, s)
			}
		}
	case []string:
		out = append(out, x...)
	case string:
		if s := strings.TrimSpace(x); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func formWrites(v any) []WriteTarget {
	out := []WriteTarget{}
	list, _ := v.([]any)
	for _, e := range list {
		switch x := e.(type) {
		case string:
			out = append(out, WriteTarget{Repo: x})
		case map[string]any:
			out = append(out, WriteTarget{Repo: formString(x["repo"]), Base: formString(x["base"])})
		}
	}
	return out
}
