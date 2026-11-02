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
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Named approvers (grain/task-1714): a change a sensitive `human:
// required` trigger, or grain's own guard over a repository's trigger
// declarations, has held carries one model.LinkApprovalRequired per rule,
// each naming who may release it (model's sensitive_path.go). Submit is
// the release, so Submit is where that is enforced.

// ApprovalView is one rule a held change waits on a named approver for.
type ApprovalView struct {
	// Rule is "trigger:<name>" (the repository's own
	// `.grain/triggers/<name>.yml`) or "builtin:<category>" (grain's).
	Rule string `json:"rule"`
	// Source says which of those two it is, for a reader.
	Source string `json:"source"`
	// Approvers is who may release it, as a reader would say it.
	Approvers  []string `json:"approvers"`
	ApprovedBy string   `json:"approvedBy,omitempty"`
}

func approvalViews(t model.Task) []ApprovalView {
	var out []ApprovalView
	for _, r := range model.ApprovalRequirements(t) {
		v := ApprovalView{Rule: r.Rule, ApprovedBy: r.ApprovedBy}
		if name, ok := strings.CutPrefix(r.Rule, model.ApprovalRuleTrigger); ok {
			v.Source = model.TriggerDeclarationDir + "/" + name + ".yml"
		} else {
			v.Source = "built in"
		}
		for _, a := range r.Approvers {
			v.Approvers = append(v.Approvers, model.DescribeApprover(a))
		}
		out = append(out, v)
	}
	return out
}

// submitWithApprovals is Submit for a change still owed approvals: the
// caller's Submit counts for every outstanding rule they are an approver
// of, and auto-merge comes back only when none is left. A caller who
// approves none of them is refused, and told who can.
func (c *Client) submitWithApprovals(ctx context.Context, task model.Task, pending []model.ApprovalRequirement) error {
	actor := c.requestActor(ctx).Actor
	user := ""
	if actor.Kind == model.PrincipalHuman {
		user = actor.ID
	}
	approves := map[string]bool{}
	for _, r := range pending {
		for _, token := range r.Approvers {
			ok, err := c.Store.IsApprover(ctx, user, token)
			if err != nil {
				return err
			}
			if ok {
				approves[r.Rule] = true
				break
			}
		}
	}
	if len(approves) == 0 {
		who := user
		if who == "" {
			who = "this caller"
		}
		return forbiddenErrorf("%s touches sensitive paths, and %s is not one of the people who may release it. "+
			"It waits for:\n%s", task.ID, who, describePending(pending))
	}
	var remaining []model.ApprovalRequirement
	now, head := c.now(), c.submittedHead(ctx, task)
	if err := c.Store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		for _, r := range model.PendingApprovals(*t) {
			if approves[r.Rule] {
				t.Links = append(t.Links, model.Link{Kind: model.LinkApprovedBy, Target: r.Rule + "=" + user})
			}
		}
		remaining = model.PendingApprovals(*t)
		if len(remaining) == 0 {
			t.AutoMerge = true
			humanSubmitted(t, now, head)
		}
		return nil
	}); err != nil {
		return err
	}
	if len(remaining) == 0 {
		if err := c.unblockSubmitted(ctx, task.ID, now); err != nil {
			return err
		}
		// Released: the same fresh CI run a plain Submit asks for.
		return c.clearUnansweredCI(ctx, task.ID)
	}
	_, err := c.Store.AddComment(ctx, model.Comment{
		TaskID: task.ID,
		Author: model.Attribution{Actor: actor},
		Body: fmt.Sprintf("Approved by %s. This change still waits for:\n%s", user,
			describePending(remaining)),
		CreatedAt: c.now(),
	})
	return err
}

func describePending(reqs []model.ApprovalRequirement) string {
	var b strings.Builder
	for _, r := range reqs {
		fmt.Fprintf(&b, "- `%s`: %s\n", r.Rule, model.DescribeApprovers(r.Approvers))
	}
	return b.String()
}
