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

// google3Kind is the change target kind propose_task's `google3`
// argument files (google/docs/design/google3-cls.md#G3-1). Core knows
// only the name; the google edition registers what it means.
const google3Kind = "google3"

// ProposedChangeTarget reads propose_task's `google3` argument as a
// change target: nil when the call has none, an error when it is not an
// object. `true` and an empty object both mean the task's own workspace.
// The spec's own keys are checked by the kind's validator where the
// task is filed (model.ValidateTaskChangeTarget).
func ProposedChangeTarget(args map[string]any) (*model.ChangeTarget, error) {
	raw, ok := args["google3"]
	if !ok || raw == nil {
		return nil, nil
	}
	switch v := raw.(type) {
	case bool:
		if !v {
			return nil, nil
		}
		return &model.ChangeTarget{Kind: google3Kind}, nil
	case map[string]any:
		ct := &model.ChangeTarget{Kind: google3Kind}
		if len(v) > 0 {
			data, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf("google3: %w", err)
			}
			ct.Spec = data
		}
		return model.ValidateChangeTarget(ct)
	}
	return nil, fmt.Errorf("google3 must be an object {workspace?, start_cl?, sync_to?}, not %T", raw)
}

// google3Refusal is why a propose_task call cannot carry `google3`
// alongside the git-shaped arguments it names, "" when it can. These
// arguments have no meaning for a task with no repository
// (google3-cls.md §1.4), and filing one anyway would drop them without
// saying so.
func google3Refusal(args map[string]any) string {
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
		if kind, _ := g["kind"].(string); kind == string(model.GoalBaseGreen) {
			named = append(named, "goal: base-green")
		}
	}
	if len(named) == 0 {
		return ""
	}
	return fmt.Sprintf("google3 cannot be combined with %s: a google3 task's change is a CL in a CitC workspace, "+
		"with no git repository, branch, pull request or base branch", strings.Join(named, ", "))
}
