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
	"context"
	"encoding/json"
	"log"
	"slices"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/servers"
	"github.com/ai-on-gke/grain/pkg/model"
)

// changeTargetCheckTimeout bounds one filing-time live check of a change
// target (model.ChangeTargetFilingCheck).
const changeTargetCheckTimeout = 30 * time.Second

// changeTargetFilingRefusal is why ct cannot be filed as it stands, asked
// of every attached capability server offering ct's filing-check tool --
// for a change target naming a starting change, the capability server on the
// operator's host, which reads that change. Nil when ct needs no live check,
// when no attached server offers the tool, or when the one that does
// cannot answer: the capability's mint checks again at dispatch, so a
// host grain cannot reach now refuses nothing it would not refuse then.
func (c *Client) changeTargetFilingRefusal(ctx context.Context, ct *model.ChangeTarget) error {
	tool, args := model.ChangeTargetFilingCheck(ct)
	if tool == "" || c.Config.CapabilityServers == nil {
		return nil
	}
	caller, _ := c.Config.CapabilityServers.(CapabilityServerToolCaller)
	if caller == nil {
		return nil
	}
	for _, st := range c.Config.CapabilityServers.Statuses() {
		if st.State != servers.Attached || !slices.Contains(st.Tools, tool) {
			continue
		}
		raw, err := caller.CallTool(ctx, st.ID, tool, args, changeTargetCheckTimeout)
		if err != nil {
			log.Printf("ui: checking a %s target with %s's %s: %v (left to dispatch)", ct.Kind, st.ID, tool, err)
			continue
		}
		var res struct {
			OK     bool   `json:"ok"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			log.Printf("ui: %s answered %s unreadably: %v (left to dispatch)", st.ID, tool, err)
			continue
		}
		if !res.OK {
			reason := res.Reason
			if reason == "" {
				reason = st.ID + " refused it"
			}
			return validationErrorf("%s target: %s", ct.Kind, reason)
		}
	}
	return nil
}
