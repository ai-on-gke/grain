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

package model

// The New Task form's configuration (docs/design/config-repo-settings.md
// §6): which fields the form shows, in what order, under what labels,
// with what defaults, deployment-wide and per board, repository and
// template. settings/new-task-form.yml is where it is written
// (pkg/staterepo/new_task_form_file.go); grain_config.new_task_form is
// where it is stored, as the JSON of NewTaskForm, so the UI reads it off
// GET /api/config and POST /api/tasks applies the defaults of the fields
// it hides.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The three placements a field can have.
const (
	FormFieldShown  = "shown"
	FormFieldMore   = "more"
	FormFieldHidden = "hidden"
)

// NewTaskFormFieldIDs is every field the New Task form has, in the order
// it draws them when nothing is configured (§6.1). legacyCapabilities is
// accepted as an alias of grants.
var NewTaskFormFieldIDs = []string{
	"title", "description", "attachments", "customFields", "repo", "newBranch", "base",
	"writes", "reads", "autoMerge", "noPort", "fixesBaseBranch", "grants", "legacyCapabilities",
	"model", "dependsOn", "agentBackendId", "networkAccess", "nothingNeededAction",
	"reviewTemplateId", "promptExtension", "sandboxShape", "approved", "atFront",
}

// IsNewTaskFormField reports whether id names a field of the form.
func IsNewTaskFormField(id string) bool {
	for _, f := range NewTaskFormFieldIDs {
		if f == id {
			return true
		}
	}
	return false
}

// NewTaskForm is settings/new-task-form.yml: the deployment's layer and
// the layers that override it.
type NewTaskForm struct {
	Default   *NewTaskFormLayer           `json:"default,omitempty" yaml:"default,omitempty"`
	Boards    map[string]NewTaskFormLayer `json:"boards,omitempty" yaml:"boards,omitempty"`
	Repos     map[string]NewTaskFormLayer `json:"repos,omitempty" yaml:"repos,omitempty"`
	Templates map[string]NewTaskFormLayer `json:"templates,omitempty" yaml:"templates,omitempty"`
}

// NewTaskFormLayer is one layer: the fields it speaks for and the
// capability picker's configuration.
type NewTaskFormLayer struct {
	Fields       map[string]NewTaskFormField `json:"fields,omitempty" yaml:"fields,omitempty"`
	Capabilities *NewTaskFormCapabilities    `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
}

// NewTaskFormField is one field's configuration. Every property is
// optional: a layer overrides only what it names.
type NewTaskFormField struct {
	Visibility string `json:"visibility,omitempty" yaml:"visibility,omitempty"`
	Order      *int   `json:"order,omitempty" yaml:"order,omitempty"`
	Label      string `json:"label,omitempty" yaml:"label,omitempty"`
	HelpText   string `json:"help_text,omitempty" yaml:"help_text,omitempty"`
	// Default is the field's starting value: a string, a bool, a number
	// or a list, as the field takes.
	Default any `json:"default,omitempty" yaml:"default,omitempty"`
	// Offered narrows the choices of repo, model and networkAccess.
	Offered     []string `json:"offered,omitempty" yaml:"offered,omitempty"`
	AllowNoRepo *bool    `json:"allow_no_repo,omitempty" yaml:"allow_no_repo,omitempty"`
	// AllowChangeTargets false leaves out the repo picker's non-git
	// change targets (the kinds the binary registers, pkg/change).
	AllowChangeTargets *bool `json:"allow_change_targets,omitempty" yaml:"allow_change_targets,omitempty"`
	// The sandboxShape field's presets.
	Presets            []NewTaskFormSandboxPreset `json:"presets,omitempty" yaml:"presets,omitempty"`
	DefaultPreset      *string                    `json:"default_preset,omitempty" yaml:"default_preset,omitempty"`
	AllowCustomNumbers *bool                      `json:"allow_custom_numbers,omitempty" yaml:"allow_custom_numbers,omitempty"`
}

// NewTaskFormSandboxPreset is one named sandbox shape.
type NewTaskFormSandboxPreset struct {
	ID       string `json:"id" yaml:"id"`
	Label    string `json:"label,omitempty" yaml:"label,omitempty"`
	CPUs     int    `json:"cpus,omitempty" yaml:"cpus,omitempty"`
	MemoryMB int    `json:"memory_mb,omitempty" yaml:"memory_mb,omitempty"`
	DiskGB   int    `json:"disk_gb,omitempty" yaml:"disk_gb,omitempty"`
}

// NewTaskFormCapabilities is the capability picker's configuration.
type NewTaskFormCapabilities struct {
	// Defaults are pre-added when the form opens; nil inherits the
	// capability catalogue's defaults.
	Defaults *[]string `json:"defaults,omitempty" yaml:"defaults,omitempty"`
	// Offered is an allowlist of what the picker offers ("*" for all).
	Offered *[]string `json:"offered,omitempty" yaml:"offered,omitempty"`
	// Hidden are left out of the picker without being disabled.
	Hidden  []string                                `json:"hidden,omitempty" yaml:"hidden,omitempty"`
	Display map[string]NewTaskFormCapabilityDisplay `json:"display,omitempty" yaml:"display,omitempty"`
}

// NewTaskFormCapabilityDisplay is how the picker presents one capability.
type NewTaskFormCapabilityDisplay struct {
	Label       string `json:"label,omitempty" yaml:"label,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	Order       *int   `json:"order,omitempty" yaml:"order,omitempty"`
}

// NewTaskFormContext is what the form is being filled in for.
type NewTaskFormContext struct {
	Repo       string
	TemplateID string
	BoardID    string
}

// ResolvedNewTaskForm is the layers merged for one context: fields only
// as the layers spoke for them (built-in placements are the UI's), and
// the picker's configuration.
type ResolvedNewTaskForm struct {
	Fields       map[string]NewTaskFormField
	Capabilities NewTaskFormCapabilities
}

// Layers returns the layers that apply to ctx, lowest precedence first:
// default, board, repo, template.
func (f *NewTaskForm) Layers(ctx NewTaskFormContext) []NewTaskFormLayer {
	if f == nil {
		return nil
	}
	var out []NewTaskFormLayer
	if f.Default != nil {
		out = append(out, *f.Default)
	}
	if l, ok := f.Boards[ctx.BoardID]; ok && ctx.BoardID != "" {
		out = append(out, l)
	}
	if l, ok := lookupRepoLayer(f.Repos, ctx.Repo); ok {
		out = append(out, l)
	}
	if l, ok := f.Templates[ctx.TemplateID]; ok && ctx.TemplateID != "" {
		out = append(out, l)
	}
	return out
}

func lookupRepoLayer(m map[string]NewTaskFormLayer, repo string) (NewTaskFormLayer, bool) {
	if repo == "" {
		return NewTaskFormLayer{}, false
	}
	if l, ok := m[repo]; ok {
		return l, true
	}
	for k, l := range m {
		if strings.EqualFold(k, repo) {
			return l, true
		}
	}
	return NewTaskFormLayer{}, false
}

// Resolve merges the layers that apply to ctx.
func (f *NewTaskForm) Resolve(ctx NewTaskFormContext) ResolvedNewTaskForm {
	out := ResolvedNewTaskForm{Fields: map[string]NewTaskFormField{}}
	for _, l := range f.Layers(ctx) {
		for id, fc := range l.Fields {
			if id == "legacyCapabilities" {
				id = "grants"
			}
			out.Fields[id] = mergeFormField(out.Fields[id], fc)
		}
		if c := l.Capabilities; c != nil {
			if c.Defaults != nil {
				out.Capabilities.Defaults = c.Defaults
			}
			if c.Offered != nil {
				out.Capabilities.Offered = c.Offered
			}
			if c.Hidden != nil {
				out.Capabilities.Hidden = c.Hidden
			}
			for k, d := range c.Display {
				if out.Capabilities.Display == nil {
					out.Capabilities.Display = map[string]NewTaskFormCapabilityDisplay{}
				}
				prev := out.Capabilities.Display[k]
				if d.Label != "" {
					prev.Label = d.Label
				}
				if d.Description != "" {
					prev.Description = d.Description
				}
				if d.Order != nil {
					prev.Order = d.Order
				}
				out.Capabilities.Display[k] = prev
			}
		}
	}
	return out
}

func mergeFormField(base, over NewTaskFormField) NewTaskFormField {
	if over.Visibility != "" {
		base.Visibility = over.Visibility
	}
	if over.Order != nil {
		base.Order = over.Order
	}
	if over.Label != "" {
		base.Label = over.Label
	}
	if over.HelpText != "" {
		base.HelpText = over.HelpText
	}
	if over.Default != nil {
		base.Default = over.Default
	}
	if over.Offered != nil {
		base.Offered = over.Offered
	}
	if over.AllowNoRepo != nil {
		base.AllowNoRepo = over.AllowNoRepo
	}
	if over.AllowChangeTargets != nil {
		base.AllowChangeTargets = over.AllowChangeTargets
	}
	if over.Presets != nil {
		base.Presets = over.Presets
	}
	if over.DefaultPreset != nil {
		base.DefaultPreset = over.DefaultPreset
	}
	if over.AllowCustomNumbers != nil {
		base.AllowCustomNumbers = over.AllowCustomNumbers
	}
	return base
}

// Hidden reports whether the resolved form hides id, and its default.
func (r ResolvedNewTaskForm) Hidden(id string) (any, bool) {
	f, ok := r.Fields[id]
	if !ok || f.Visibility != FormFieldHidden {
		return nil, false
	}
	return f.Default, true
}

// Validate checks the form's shape on its own: known field IDs,
// placements, and the fields the form cannot do without. Whether its
// capabilities exist is the catalogue's question (staterepo.Check).
func (f *NewTaskForm) Validate() error {
	if f == nil {
		return nil
	}
	var errs []string
	check := func(where string, l NewTaskFormLayer) {
		ids := make([]string, 0, len(l.Fields))
		for id := range l.Fields {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			fc := l.Fields[id]
			at := where + ".fields." + id
			if !IsNewTaskFormField(id) {
				errs = append(errs, fmt.Sprintf("%s: unknown field (the form's fields are %s)", at, strings.Join(NewTaskFormFieldIDs, ", ")))
				continue
			}
			switch fc.Visibility {
			case "", FormFieldShown, FormFieldMore, FormFieldHidden:
			default:
				errs = append(errs, fmt.Sprintf("%s: visibility %q is not one of shown, more, hidden", at, fc.Visibility))
			}
			if fc.Visibility != FormFieldHidden {
				continue
			}
			switch id {
			case "title":
				errs = append(errs, at+": the title is required and cannot be hidden")
			case "repo":
				def, _ := fc.Default.(string)
				def = strings.TrimSpace(def)
				switch {
				case def == "":
					errs = append(errs, at+": a hidden repo needs a default")
				case strings.EqualFold(def, "none"):
					if fc.AllowNoRepo == nil || !*fc.AllowNoRepo {
						errs = append(errs, at+`: a hidden repo defaulting to "none" needs allow_no_repo: true`)
					}
				default:
					if _, err := ParseRepo(def); err != nil {
						errs = append(errs, fmt.Sprintf("%s: default %q is not owner/name", at, def))
					}
				}
			}
		}
	}
	if f.Default != nil {
		check("default", *f.Default)
	}
	for _, group := range []struct {
		name   string
		layers map[string]NewTaskFormLayer
	}{{"boards", f.Boards}, {"repos", f.Repos}, {"templates", f.Templates}} {
		keys := make([]string, 0, len(group.layers))
		for k := range group.layers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if group.name == "repos" {
				if _, err := ParseRepo(k); err != nil {
					errs = append(errs, fmt.Sprintf("repos.%s: not owner/name", k))
				}
			}
			check(group.name+"."+k, group.layers[k])
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// EncodeNewTaskForm and DecodeNewTaskForm are grain_config.new_task_form:
// "" for a deployment with no form configuration.
func EncodeNewTaskForm(f *NewTaskForm) (string, error) {
	if f == nil {
		return "", nil
	}
	b, err := json.Marshal(f)
	if err != nil {
		return "", fmt.Errorf("model: encoding new_task_form: %w", err)
	}
	return string(b), nil
}

func DecodeNewTaskForm(s string) (*NewTaskForm, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var f NewTaskForm
	if err := json.Unmarshal([]byte(s), &f); err != nil {
		return nil, fmt.Errorf("model: decoding new_task_form: %w", err)
	}
	return &f, nil
}

// Defines reports whether the catalogue names grant g at all: a server
// it lists, an agent API preset it or grain declares (or a raw scope),
// or an in-process capability (or its family) it lists. A nil catalogue
// defines nothing.
func (c *CapabilityCatalogue) Defines(g string) bool {
	if c == nil {
		return false
	}
	g = strings.TrimSpace(g)
	switch {
	case IsCapabilityRef(g):
		ref := CapabilityGrantRef(g)
		if spec, err := CapabilityGrantSpec(g); err == nil {
			ref = spec.Capability
		}
		id, _, _ := strings.Cut(ref, "#")
		_, ok := c.server(id)
		return ok
	case strings.HasPrefix(g, APIGrantPrefix):
		what, _, _ := strings.Cut(strings.TrimPrefix(g, APIGrantPrefix), "@")
		if _, ok := c.APIScopes.Presets[what]; ok || strings.Contains(what, ":") {
			return true
		}
		for _, s := range BuiltinAPIScopeSets() {
			if s.Name == what {
				return true
			}
		}
		return false
	}
	if _, ok := c.InProcess[g]; ok {
		return true
	}
	if family, _, cut := strings.Cut(g, ":"); cut {
		_, ok := c.InProcess[family]
		return ok
	}
	return false
}

// FormGrantRefusal is why the New Task form may not offer or pre-add g
// for a layer of settings/new-task-form.yml: the catalogue does not
// define it, or would refuse it to a task on repo filed from templateID.
// A layer with no repository (the deployment's own, or a board's) is
// judged without the catalogue's repository rules, the way a
// deployment-wide default is (defaultRefusal).
func (c *CapabilityCatalogue) FormGrantRefusal(g string, repo *RepoRef, templateID string) error {
	if !c.Defines(g) {
		return fmt.Errorf("capability %s is not defined in settings/capabilities.yml", g)
	}
	probe := c
	if repo == nil {
		cp := *c
		cp.stripRepos()
		probe = &cp
	}
	_, err := probe.ValidateGrants(GrantContext{Repo: repo, TemplateID: templateID, Kind: CatalogueKindTask, Grants: []string{g}})
	return err
}
