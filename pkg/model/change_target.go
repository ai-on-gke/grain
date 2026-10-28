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

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// ChangeTarget names where a non-git task's changes live: a registered target
// kind (such as "google3" for a CitC workspace) and its target-specific JSON
// specification. A task with a ChangeTarget has no git repository (Target ==
// nil) and runs on the workspace-only (NoRepo) checkout path.
type ChangeTarget struct {
	Kind string          `json:"kind" yaml:"kind"`
	Spec json.RawMessage `json:"spec,omitempty" yaml:"spec,omitempty"`
}

// Clone returns a deep copy of ct.
func (ct *ChangeTarget) Clone() *ChangeTarget {
	if ct == nil {
		return nil
	}
	out := &ChangeTarget{Kind: ct.Kind}
	if len(ct.Spec) > 0 {
		out.Spec = append(json.RawMessage(nil), ct.Spec...)
	}
	return out
}

// Equal reports whether ct and other have the same Kind and canonical Spec.
func (ct *ChangeTarget) Equal(other *ChangeTarget) bool {
	if ct == nil || other == nil {
		return ct == nil && other == nil
	}
	if ct.Kind != other.Kind {
		return false
	}
	return canonicalSpecString(ct.Spec) == canonicalSpecString(other.Spec)
}

// SpecString returns Spec as a canonical JSON string (or "{}" when empty), for
// SQLite storage.
func (ct *ChangeTarget) SpecString() string {
	if ct == nil {
		return ""
	}
	return canonicalSpecString(ct.Spec)
}

func canonicalSpecString(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("{}")) {
		return ""
	}
	var v any
	if err := json.Unmarshal(trimmed, &v); err != nil {
		return string(trimmed)
	}
	if m, ok := v.(map[string]any); ok && len(m) == 0 {
		return ""
	}
	norm, err := json.Marshal(v)
	if err != nil {
		return string(trimmed)
	}
	return string(norm)
}

// MarshalYAML writes ChangeTarget with Spec decoded as a YAML mapping when
// non-empty, so state-repository YAML files stay human-readable and round-trip
// cleanly.
func (ct ChangeTarget) MarshalYAML() (any, error) {
	type yamlTarget struct {
		Kind string         `yaml:"kind"`
		Spec map[string]any `yaml:"spec,omitempty"`
	}
	out := yamlTarget{Kind: ct.Kind}
	if s := canonicalSpecString(ct.Spec); s != "" {
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			return nil, err
		}
		if len(m) > 0 {
			out.Spec = m
		}
	}
	return out, nil
}

// UnmarshalYAML reads a ChangeTarget from either a {kind, spec} mapping or a
// bare kind string (such as "google3").
func (ct *ChangeTarget) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		kind := strings.TrimSpace(node.Value)
		if kind == "" {
			return fmt.Errorf("line %d: change_target kind cannot be empty", node.Line)
		}
		ct.Kind = kind
		ct.Spec = nil
		return nil
	}
	var raw struct {
		Kind string         `yaml:"kind"`
		Spec map[string]any `yaml:"spec"`
	}
	if err := node.Decode(&raw); err != nil {
		return err
	}
	ct.Kind = strings.TrimSpace(raw.Kind)
	if len(raw.Spec) > 0 {
		data, err := json.Marshal(yamlToJSON(raw.Spec))
		if err != nil {
			return fmt.Errorf("line %d: encoding change_target spec: %w", node.Line, err)
		}
		ct.Spec = data
	} else {
		ct.Spec = nil
	}
	return nil
}

// changeTargetKind is ct's Kind, "" for a git or no-repo task.
func changeTargetKind(ct *ChangeTarget) string {
	if ct == nil {
		return ""
	}
	return ct.Kind
}

// ChangeTargetFromStored reconstructs a *ChangeTarget from task/template row
// columns, returning nil when kind is empty.
func ChangeTargetFromStored(kind, spec string) *ChangeTarget {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return nil
	}
	ct := &ChangeTarget{Kind: kind}
	if s := strings.TrimSpace(spec); s != "" && s != "{}" && s != "null" {
		ct.Spec = json.RawMessage(s)
	}
	return ct
}

// ChangeTargetRegistration is what an edition (such as google/) registers for
// a ChangeTarget.Kind so core packages can validate specs, attach implied
// grants, and render prompt sections without importing the edition.
type ChangeTargetRegistration struct {
	Kind string
	// Validate checks and normalizes Spec JSON, returning canonical JSON (or
	// nil/empty for the empty spec) or a validation error.
	Validate func(spec json.RawMessage) (json.RawMessage, error)
	// EnsureGrants returns grants with the target's implied capability grant
	// attached (for example, citc#workspace on a google3 task) when none is
	// present, or merges target spec fields into an existing grant.
	EnsureGrants func(spec json.RawMessage, grants []Grant) ([]Grant, error)
	// NeedsPersonApproval reports whether this target spec requires a human's
	// approval at filing/dispatch (for example, naming a non-default workspace
	// or a start_cl), returning the reason if so.
	NeedsPersonApproval func(spec json.RawMessage) (bool, string)
	// FilingCheck names a control tool of a capability server, and the
	// arguments to call it with, that checks spec live when a task carrying
	// it is filed (for example, that a google3 start_cl is still pending);
	// "" when spec needs no live check. The tool answers {ok, reason}. A
	// filing whose daemon reaches no server with the tool is not refused:
	// the capability's own mint checks again at dispatch.
	FilingCheck func(spec json.RawMessage) (tool string, args map[string]any)
	// PromptSection builds the prompt section that replaces the git branch and
	// push instructions in orchestrator.BuildPrompt.
	PromptSection func(task Task, commitTrailers []string) string
	// SuspendedPromptLine builds the opening orientation line in
	// orchestrator.BuildSuspendedPrompt.
	SuspendedPromptLine func(task Task) string
}

var (
	changeTargetsMu sync.RWMutex
	changeTargets   = map[string]ChangeTargetRegistration{}
)

// RegisterChangeTarget registers a ChangeTargetRegistration by Kind.
func RegisterChangeTarget(reg ChangeTargetRegistration) {
	if strings.TrimSpace(reg.Kind) == "" {
		panic("model.RegisterChangeTarget: empty Kind")
	}
	changeTargetsMu.Lock()
	defer changeTargetsMu.Unlock()
	changeTargets[reg.Kind] = reg
}

// LookupChangeTarget returns the registration for kind, if any.
func LookupChangeTarget(kind string) (ChangeTargetRegistration, bool) {
	changeTargetsMu.RLock()
	defer changeTargetsMu.RUnlock()
	reg, ok := changeTargets[kind]
	return reg, ok
}

// ChangeTargetKinds is every registered kind, sorted.
func ChangeTargetKinds() []string {
	changeTargetsMu.RLock()
	defer changeTargetsMu.RUnlock()
	kinds := make([]string, 0, len(changeTargets))
	for kind := range changeTargets {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}

// ValidateChangeTarget checks ct's Kind and normalizes its Spec against the
// registered validator when one is present.
func ValidateChangeTarget(ct *ChangeTarget) (*ChangeTarget, error) {
	if ct == nil {
		return nil, nil
	}
	kind := strings.TrimSpace(ct.Kind)
	if kind == "" {
		return nil, fmt.Errorf("change_target: kind is required")
	}
	reg, ok := LookupChangeTarget(kind)
	if !ok {
		if kind != "google3" {
			return nil, fmt.Errorf("unknown change target kind %q", kind)
		}
		// When core tests run without linking google/, still validate that
		// Spec is a JSON object if provided.
		norm, err := validateGenericSpecObject(ct.Spec)
		if err != nil {
			return nil, fmt.Errorf("%s target: %w", kind, err)
		}
		return &ChangeTarget{Kind: kind, Spec: norm}, nil
	}
	var norm json.RawMessage
	if reg.Validate != nil {
		var err error
		norm, err = reg.Validate(ct.Spec)
		if err != nil {
			return nil, err
		}
	} else {
		var err error
		norm, err = validateGenericSpecObject(ct.Spec)
		if err != nil {
			return nil, err
		}
	}
	if s := canonicalSpecString(norm); s != "" {
		norm = json.RawMessage(s)
	} else {
		norm = nil
	}
	return &ChangeTarget{Kind: kind, Spec: norm}, nil
}

func validateGenericSpecObject(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("{}")) {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal(trimmed, &m); err != nil {
		return nil, fmt.Errorf("spec must be a JSON object: %w", err)
	}
	if len(m) == 0 {
		return nil, nil
	}
	out, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ValidateTaskChangeTarget validates t.ChangeTarget, refuses git-only task
// fields that do not apply to a workspace-only change target, and attaches the
// target's implied capability grant when absent.
func ValidateTaskChangeTarget(t *Task) error {
	if t == nil || t.ChangeTarget == nil {
		return nil
	}
	norm, err := ValidateChangeTarget(t.ChangeTarget)
	if err != nil {
		return err
	}
	t.ChangeTarget = norm
	kind := norm.Kind

	if t.Target != nil {
		return fmt.Errorf("a %s task has no git repository or base branch: repo cannot be set", kind)
	}
	if t.Base != "" {
		return fmt.Errorf("a %s task has no git repository or base branch: branch/base cannot be set", kind)
	}
	if len(t.Writes) > 0 {
		return fmt.Errorf("a %s task works in a workspace and cannot declare git write targets (writes)", kind)
	}
	if FixesBaseBranch(*t) {
		return fmt.Errorf("a %s task has no git base branch to repair: fixes_base_branch cannot be set", kind)
	}
	if t.AutoMerge {
		return fmt.Errorf("a %s task has no git pull request: auto_merge cannot be set", kind)
	}
	if t.GoalKind == GoalBaseGreen {
		return fmt.Errorf("a %s task has no git base branch: goal base-green cannot be set", kind)
	}
	for _, l := range t.Links {
		if l.Kind == LinkMergeWith {
			return fmt.Errorf("a %s task has no git pull request: merge_with cannot be set", kind)
		}
	}
	if reg, ok := LookupChangeTarget(kind); ok && reg.EnsureGrants != nil {
		grants, err := reg.EnsureGrants(norm.Spec, t.Grants)
		if err != nil {
			return err
		}
		t.Grants = grants
	}
	return nil
}

// ValidateTemplateChangeTarget validates tmpl.ChangeTarget and refuses
// git-only template fields that do not apply to a workspace-only change target.
func ValidateTemplateChangeTarget(tmpl *Template) error {
	if tmpl == nil || tmpl.ChangeTarget == nil {
		return nil
	}
	norm, err := ValidateChangeTarget(tmpl.ChangeTarget)
	if err != nil {
		return err
	}
	tmpl.ChangeTarget = norm
	kind := norm.Kind

	if tmpl.Target != nil {
		return fmt.Errorf("a %s template has no git repository or base branch: repo cannot be set", kind)
	}
	if tmpl.Base != "" {
		return fmt.Errorf("a %s template has no git repository or base branch: branch/base cannot be set", kind)
	}
	if len(tmpl.Writes) > 0 {
		return fmt.Errorf("a %s template works in a workspace and cannot declare git write targets (writes)", kind)
	}
	if tmpl.AutoMerge {
		return fmt.Errorf("a %s template has no git pull request: auto_merge cannot be set", kind)
	}
	if tmpl.Goal != nil && tmpl.Goal.Kind == GoalBaseGreen {
		return fmt.Errorf("a %s template has no git base branch: goal base-green cannot be set", kind)
	}
	if reg, ok := LookupChangeTarget(kind); ok && reg.EnsureGrants != nil {
		grants, err := reg.EnsureGrants(norm.Spec, tmpl.Grants)
		if err != nil {
			return err
		}
		tmpl.Grants = grants
	}
	return nil
}

// ChangeTargetNeedsPersonApproval reports whether ct requires a person's
// approval (for example, naming another workspace or a start_cl).
func ChangeTargetNeedsPersonApproval(ct *ChangeTarget) (bool, string) {
	if ct == nil {
		return false, ""
	}
	if reg, ok := LookupChangeTarget(ct.Kind); ok && reg.NeedsPersonApproval != nil {
		return reg.NeedsPersonApproval(ct.Spec)
	}
	return false, ""
}

// ChangeTargetFilingCheck is the control tool, and its arguments, that
// checks ct live at filing (ChangeTargetRegistration.FilingCheck), or ""
// when ct needs none.
func ChangeTargetFilingCheck(ct *ChangeTarget) (string, map[string]any) {
	if ct == nil {
		return "", nil
	}
	if reg, ok := LookupChangeTarget(ct.Kind); ok && reg.FilingCheck != nil {
		return reg.FilingCheck(ct.Spec)
	}
	return "", nil
}

// ChangeTargetPromptSection renders the registered prompt section for
// task.ChangeTarget, or returns "" if none is registered.
func ChangeTargetPromptSection(task Task, commitTrailers []string) string {
	if task.ChangeTarget == nil {
		return ""
	}
	if reg, ok := LookupChangeTarget(task.ChangeTarget.Kind); ok && reg.PromptSection != nil {
		return reg.PromptSection(task, commitTrailers)
	}
	return ""
}

// ChangeTargetSuspendedPromptLine renders the registered suspended-run opening
// line for task.ChangeTarget, or returns "" if none is registered.
func ChangeTargetSuspendedPromptLine(task Task) string {
	if task.ChangeTarget == nil {
		return ""
	}
	if reg, ok := LookupChangeTarget(task.ChangeTarget.Kind); ok && reg.SuspendedPromptLine != nil {
		return reg.SuspendedPromptLine(task)
	}
	return ""
}
