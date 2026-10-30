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
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The capability catalogue (settings/capabilities.yml,
// model.CapabilityCatalogue) at filing and at edit
// (docs/design/config-repo-settings.md §3.4). Dispatch asks it again
// (orchestrator.catalogueHold), so a config change merged after a task
// was filed still has the last word.

// capabilityCatalogue is the deployment's catalogue, nil when its
// grain/config has none.
func (c *Client) capabilityCatalogue(ctx context.Context) (*model.CapabilityCatalogue, error) {
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil || cfg == nil {
		return nil, err
	}
	return cfg.CapabilityPolicy.Catalogue, nil
}

// catalogueGrantContext is the GrantContext a task filed with these
// fields is judged in.
func (c *Client) catalogueGrantContext(ctx context.Context, target *model.RepoRef, templateID string, grants []model.Grant) model.GrantContext {
	gc := model.GrantContext{Repo: target, TemplateID: strings.TrimSpace(templateID), Kind: model.CatalogueKindTask}
	if gc.TemplateID != "" {
		if tmpl, err := c.Store.GetTemplate(ctx, gc.TemplateID); err == nil && tmpl != nil {
			gc.TemplateName = tmpl.Name
		}
	}
	for _, g := range grants {
		gc.Grants = append(gc.Grants, g.Capability)
	}
	return gc
}

// catalogueFilingGrants judges a new task's grants. A grant somebody
// asked for that the catalogue refuses is refused with the task (400); a
// default the catalogue refuses for this task -- a repository's default
// that does not reach the template it was filed from, say -- is dropped,
// since nobody asked for it. needsPerson is every grant the catalogue
// marks as a person's decision.
func (c *Client) catalogueFilingGrants(ctx context.Context, grants []model.Grant, target *model.RepoRef, templateID string) (kept []model.Grant, needsPerson []string, err error) {
	cat, err := c.capabilityCatalogue(ctx)
	if err != nil || cat == nil {
		return grants, nil, err
	}
	for _, g := range grants {
		gc := c.catalogueGrantContext(ctx, target, templateID, []model.Grant{g})
		verdict, err := cat.ValidateGrants(gc)
		if err != nil {
			if g.Via == model.GrantByDefault {
				continue
			}
			return nil, nil, validationErrorf("%v", err)
		}
		kept = append(kept, g)
		needsPerson = append(needsPerson, verdict.RequiresPerson...)
	}
	return kept, needsPerson, nil
}

// catalogueEditRefusal judges one grant being attached to an existing
// task: 400 for one the catalogue refuses the task, 403 for an agent
// attaching one the catalogue says is a person's decision.
func (c *Client) catalogueEditRefusal(ctx context.Context, task model.Task, grant string) error {
	cat, err := c.capabilityCatalogue(ctx)
	if err != nil || cat == nil {
		return err
	}
	gc := model.TaskGrantContext(task)
	gc.Grants = []string{grant}
	if gc.TemplateID != "" {
		if tmpl, err := c.Store.GetTemplate(ctx, gc.TemplateID); err == nil && tmpl != nil {
			gc.TemplateName = tmpl.Name
		}
	}
	verdict, err := cat.ValidateGrants(gc)
	if err != nil {
		return validationErrorf("%v", err)
	}
	if len(verdict.RequiresPerson) > 0 && c.actor(ctx).Kind == model.PrincipalAgent {
		return forbiddenErrorf("capability %s requires human approval (settings/capabilities.yml marks it requires_person)", grant)
	}
	return nil
}

// catalogueApprovalRefusal refuses an agent approving a task that holds a
// grant the catalogue marks as a person's decision. Filing leaves such a
// task proposed for a person to approve (catalogueFilingGrants), and
// nothing at dispatch asks again whether a person did: without this, an
// agent holding api:tasks:admin could file the task and approve it
// itself, and the run would hold the grant with no person deciding.
// A grant the catalogue refuses outright is not this check's business:
// dispatch holds that task whoever approves it (orchestrator.catalogueHold).
func (c *Client) catalogueApprovalRefusal(ctx context.Context, task model.Task) error {
	if c.actor(ctx).Kind != model.PrincipalAgent {
		return nil
	}
	cat, err := c.capabilityCatalogue(ctx)
	if err != nil || cat == nil {
		return err
	}
	gc := model.TaskGrantContext(task)
	if gc.TemplateID != "" {
		if tmpl, err := c.Store.GetTemplate(ctx, gc.TemplateID); err == nil && tmpl != nil {
			gc.TemplateName = tmpl.Name
		}
	}
	var needsPerson []string
	for _, g := range gc.Grants {
		one := gc
		one.Grants = []string{g}
		if verdict, err := cat.ValidateGrants(one); err == nil {
			needsPerson = append(needsPerson, verdict.RequiresPerson...)
		}
	}
	if len(needsPerson) == 0 {
		return nil
	}
	return forbiddenErrorf("task %s holds %s, which settings/capabilities.yml marks as a person's decision (requires_person): only a person may approve it",
		task.ID, strings.Join(needsPerson, ", "))
}

// catalogueEditRefusalFor is catalogueEditRefusal for the task stored as
// id; a task that is not there is the edit's own error to report.
func (c *Client) catalogueEditRefusalFor(ctx context.Context, id, grant string) error {
	task, err := c.Store.GetTask(ctx, model.NormalizeTaskID(id))
	if err != nil || task == nil {
		return nil
	}
	return c.catalogueEditRefusal(ctx, *task, grant)
}
