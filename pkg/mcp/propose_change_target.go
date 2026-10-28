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

package mcp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// withProposeChangeTargetProperties adds a property to propose_task's
// InputSchema for each registered change target kind (model.ChangeTargetKinds).
func withProposeChangeTargetProperties(props map[string]any) map[string]any {
	for _, kind := range model.ChangeTargetKinds() {
		props[kind] = proposeChangeTargetSchema(kind)
	}
	return props
}

func proposeChangeTargetSchema(kind string) map[string]any {
	return map[string]any{
		"type": "object",
		"description": fmt.Sprintf("Files this task against %s rather than a git repository: its change is a CL "+
			"made in a %s workspace, with no clone and no branch. workspace names an existing workspace "+
			"of the operator's (left out, the task's own is created for it); start_cl a pending CL of the "+
			"operator's to continue; sync_to \"head\" or a CL number. Naming a workspace or a start_cl "+
			"files the task for a person to approve. Cannot be combined with repo, branch, writes, no_repo, "+
			"fixes_base_branch, auto_merge or a base-green goal; reads are still allowed.", kind, kind),
		"properties": map[string]any{
			"workspace": map[string]any{"type": "string"},
			"start_cl":  map[string]any{"type": "integer"},
			"sync_to":   map[string]any{"type": "string"},
		},
		"additionalProperties": false,
	}
}

// ProposedChangeTarget reads a registered change-target argument from
// propose_task's args as a change target: nil when the call has none, an error
// when it is not an object. `true` and an empty object both mean the task's own
// workspace. The spec's own keys are checked by the kind's validator where the
// task is filed (model.ValidateTaskChangeTarget).
func ProposedChangeTarget(args map[string]any) (*model.ChangeTarget, error) {
	for _, kind := range model.ChangeTargetKinds() {
		raw, ok := args[kind]
		if !ok || raw == nil {
			continue
		}
		switch v := raw.(type) {
		case bool:
			if !v {
				continue
			}
			return model.ValidateChangeTarget(&model.ChangeTarget{Kind: kind})
		case map[string]any:
			ct := &model.ChangeTarget{Kind: kind}
			if len(v) > 0 {
				data, err := json.Marshal(v)
				if err != nil {
					return ct, fmt.Errorf("%s: %w", kind, err)
				}
				ct.Spec = data
			}
			norm, err := model.ValidateChangeTarget(ct)
			if err != nil {
				return ct, err
			}
			return norm, nil
		default:
			return &model.ChangeTarget{Kind: kind}, fmt.Errorf("%s must be an object {workspace?, start_cl?, sync_to?}, not %T", kind, raw)
		}
	}
	return nil, nil
}

// changeTargetRefusal is why a propose_task call cannot carry a non-git change
// target alongside the git-shaped arguments it names, "" when it can. These
// arguments have no meaning for a task with no repository, and filing one
// anyway would drop them without saying so.
func changeTargetRefusal(kind string, args map[string]any) string {
	var named []string
	for _, key := range []string{"repo", "branch", "no_repo"} {
		switch v := args[key].(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				named = append(named, key)
			}
		case bool:
			if v {
				named = append(named, key)
			}
		}
	}
	if w, ok := args["writes"].([]any); ok && len(w) > 0 {
		named = append(named, "writes")
	}
	for _, key := range []string{"fixes_base_branch", "auto_merge"} {
		if v, _ := args[key].(bool); v {
			named = append(named, key)
		}
	}
	if g, ok := args["goal"].(map[string]any); ok {
		if gk, _ := g["kind"].(string); gk == string(model.GoalBaseGreen) {
			named = append(named, "goal: base-green")
		}
	}
	if len(named) == 0 {
		return ""
	}
	return fmt.Sprintf("%s cannot be combined with %s: a %s task's change is a CL in a %s workspace, "+
		"with no git repository, branch, pull request or base branch", kind, strings.Join(named, ", "), kind, kind)
}
