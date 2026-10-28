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

package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// bindChangeTargetFlags registers --<kind>, --<kind>-workspace,
// --<kind>-start-cl and --<kind>-sync-to on fs for every non-git change
// target kind registered with pkg/model, and returns a resolver that
// builds the chosen *model.ChangeTarget after fs.Parse.
func bindChangeTargetFlags(fs *flag.FlagSet) func() (*model.ChangeTarget, error) {
	type targetFlags struct {
		kind      string
		on        *bool
		workspace *string
		startCL   *int64
		syncTo    *string
	}
	var bound []targetFlags
	for _, kind := range model.ChangeTargetKinds() {
		bound = append(bound, targetFlags{
			kind:      kind,
			on:        fs.Bool(kind, false, fmt.Sprintf("file this task against %s: its change is a CL in a %s workspace, with no repo or branch", kind, kind)),
			workspace: fs.String(kind+"-workspace", "", fmt.Sprintf("with --%s: an existing %s workspace of the operator's, rather than the task's own", kind, kind)),
			startCL:   fs.Int64(kind+"-start-cl", 0, fmt.Sprintf("with --%s: a pending CL of the operator's to continue", kind)),
			syncTo:    fs.String(kind+"-sync-to", "", fmt.Sprintf(`with --%s: "head" or a CL number to sync the workspace to`, kind)),
		})
	}
	return func() (*model.ChangeTarget, error) {
		var chosen *model.ChangeTarget
		for _, b := range bound {
			ct, err := changeTargetForKind(b.kind, *b.on, *b.workspace, *b.startCL, *b.syncTo)
			if err != nil {
				return nil, err
			}
			if ct != nil {
				if chosen != nil {
					return nil, fmt.Errorf("cannot combine --%s and --%s", chosen.Kind, ct.Kind)
				}
				chosen = ct
			}
		}
		return chosen, nil
	}
}

// changeTargetForKind is the change target `grain create --<kind>` files,
// nil without the flag. Its spec flags mean nothing on their own, so
// naming one without --<kind> is an error rather than ignored.
func changeTargetForKind(kind string, on bool, workspace string, startCL int64, syncTo string) (*model.ChangeTarget, error) {
	workspace, syncTo = strings.TrimSpace(workspace), strings.TrimSpace(syncTo)
	if !on {
		if workspace != "" || startCL != 0 || syncTo != "" {
			return nil, fmt.Errorf("--%s-workspace, --%s-start-cl and --%s-sync-to need --%s", kind, kind, kind, kind)
		}
		return nil, nil
	}
	spec := map[string]any{}
	if workspace != "" {
		spec["workspace"] = workspace
	}
	if startCL != 0 {
		spec["start_cl"] = startCL
	}
	if syncTo != "" {
		spec["sync_to"] = syncTo
	}
	ct := &model.ChangeTarget{Kind: kind}
	if len(spec) > 0 {
		data, err := json.Marshal(spec)
		if err != nil {
			return nil, err
		}
		ct.Spec = data
	}
	return ct, nil
}
