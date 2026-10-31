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
	"fmt"
	"slices"
)

// TemplateWithinTask reports why tmpl hands out more than acting holds, or
// nil when it does not: every one of the template's grants must be one
// acting holds itself and none may be one only a person may give
// (IsPersonOnlyGrant: a dangerous api grant, or priority-dispatch); every repository
// it may push to must be one acting may push to (WriteTargetFor); every
// repository it reads must be one acting may already fetch.
//
// It is the rule for an agent naming a template on work it files -- the
// template a task is filed from (ui's agentFilingEscalation, "templateId")
// and the one its review is filed from once it is done
// ("reviewTemplateId", propose_task's review_template_id). The second is
// the sharper of the two: a review is filed approved and auto-merge, on
// the branch the agent wrote, so a template naming more than the agent
// holds is that much handed to a task nobody looked at. What else a
// template filed directly may not be (auto-merge, a target the agent's
// scope does not reach) is the caller's, since a review takes neither
// from its template.
func TemplateWithinTask(acting Task, tmpl Template) error {
	held := make([]string, len(acting.Grants))
	for i, g := range acting.Grants {
		held[i] = g.Capability
	}
	for _, g := range tmpl.Grants {
		if IsPersonOnlyGrant(g.Capability) {
			return fmt.Errorf("template %s grants %s, which only a person may give", tmpl.ID, g.Capability)
		}
		if !slices.Contains(held, g.Capability) {
			return fmt.Errorf("template %s grants %s, which the agent's own task does not hold", tmpl.ID, g.Capability)
		}
	}
	for _, w := range tmpl.Writes {
		if _, ok := WriteTargetFor(acting, w.Repo); !ok {
			return fmt.Errorf("template %s grants push access to %s, which the agent's own task cannot push to", tmpl.ID, w.Repo)
		}
	}
	for _, r := range tmpl.Reads {
		if !mayRead(acting, r) {
			return fmt.Errorf("template %s grants read access to %s, which the agent's own task cannot read", tmpl.ID, r)
		}
	}
	if len(tmpl.Reviewers) > 0 {
		return fmt.Errorf("template %s sets reviewers, which only a person may set", tmpl.ID)
	}
	return nil
}

// ReviewTemplateWithinTask is TemplateWithinTask for the review template
// a task filed by acting names (Task.ReviewTemplateID), filed against
// target: the repository's own declared default review
// (DefaultReviewTemplateID) is always allowed, since it is the review
// that task gets when it names none, and any other must pass
// TemplateWithinTask.
func ReviewTemplateWithinTask(acting Task, tmpl Template, target *RepoRef) error {
	if target != nil && tmpl.ID == DefaultReviewTemplateID(*target) {
		return nil
	}
	return TemplateWithinTask(acting, tmpl)
}

// ReviewTemplateRefusedNote is the line added to a proposal whose
// review_template_id was refused (ReviewTemplateWithinTask) and replaced
// by fallback ("" for none).
func ReviewTemplateRefusedNote(proposer, asked, fallback string, why error) string {
	instead := "no review"
	if fallback != "" {
		instead = fmt.Sprintf("the repository's default review (`%s`)", fallback)
	}
	return fmt.Sprintf("\n\n---\n\ngrain: task %s asked for this work to be reviewed from template `%s`, "+
		"which was refused (%v): a review is filed approved and auto-merge, so its template may not "+
		"hand out more than the task asking for it holds. It was filed with %s instead.",
		proposer, asked, why, instead)
}

// IsDangerousGrant is IsDangerousAPIGrant for any capability name,
// including the bare aliases (self-upgrade, dispatch-control, ...) that stand
// for an api preset.
func IsDangerousGrant(name string) bool {
	if preset, ok := apiAliasCapabilities[name]; ok {
		name = APIGrantPrefix + preset
	}
	return IsDangerousAPIGrant(name)
}

// ReviewGrants is grants, the grants a review of reviewed is about to be
// filed with, less every one only a person may give (IsPersonOnlyGrant)
// reviewed does not hold itself. A review is filed approved and auto-merge on the
// reviewed task's own branch, so a dangerous grant reaching it is one a
// person never gave that work; checked when the review is filed rather
// than only when its template was chosen, because the template may have
// been edited since. Dropped names are returned beside the rest.
func ReviewGrants(grants []Grant, reviewed Task) (kept []Grant, dropped []string) {
	for _, g := range grants {
		if IsPersonOnlyGrant(g.Capability) && !slices.ContainsFunc(reviewed.Grants, func(h Grant) bool { return h.Capability == g.Capability }) {
			dropped = append(dropped, g.Capability)
			continue
		}
		kept = append(kept, g)
	}
	return kept, dropped
}
