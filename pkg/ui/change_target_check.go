package ui

import (
	"context"
	"encoding/json"
	"log"
	"slices"
	"time"

	"github.com/bwsalmon/grain/pkg/capability/servers"
	"github.com/bwsalmon/grain/pkg/model"
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
