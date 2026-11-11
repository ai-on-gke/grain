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
	"slices"
	"sort"
	"strings"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

// The capability catalogue (settings/capabilities.yml,
// model.CapabilityCatalogue) at filing and at edit
// (design/design/config-repo-settings.design.md §3.4). Dispatch asks it again
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

// markCatalogueRefusals sets CatalogueRefusal on each checked grant the
// catalogue refuses a task filed against target (nil: no repository)
// with no template -- what the new-task form files.
func (c *Client) markCatalogueRefusals(ctx context.Context, checks []GrantCheck, target *model.RepoRef) ([]GrantCheck, error) {
	cat, err := c.capabilityCatalogue(ctx)
	if err != nil {
		return checks, err
	}
	for i, check := range checks {
		grant := check.Recorded
		if grant == "" {
			grant = check.Grant
		}
		if ciOnlyCapability(grant) {
			checks[i].CatalogueRefusal = model.CIOnlyRefusal(grant)
			continue
		}
		if cat == nil {
			continue
		}
		gc := c.catalogueGrantContext(ctx, target, "", []model.Grant{{Capability: grant}})
		if _, err := cat.ValidateGrants(gc); err != nil {
			checks[i].CatalogueRefusal = err.Error()
		}
	}
	return checks, nil
}

// taskGrantContext is the GrantContext an existing task's grant edit or
// access request is judged in, with its template's name resolved.
func (c *Client) taskGrantContext(ctx context.Context, task model.Task, grants []string) model.GrantContext {
	gc := model.TaskGrantContext(task)
	gc.Grants = grants
	if gc.TemplateID != "" {
		if tmpl, err := c.Store.GetTemplate(ctx, gc.TemplateID); err == nil && tmpl != nil {
			gc.TemplateName = tmpl.Name
		}
	}
	return gc
}

// catalogueEditRefusal judges one grant being attached to an existing
// task: 400 for one the catalogue refuses the task, 403 for an agent
// attaching one the catalogue says is a person's decision. It asks even
// when the deployment has no catalogue, since a nil catalogue still
// applies grain's built-in director floor (model.DirectorFloorRefusal):
// nobody -- person, agent or config import -- attaches api:merge:force
// and the rest of the Tier 3 scopes to the director session.
func (c *Client) catalogueEditRefusal(ctx context.Context, task model.Task, grant string) error {
	cat, err := c.capabilityCatalogue(ctx)
	if err != nil {
		return err
	}
	verdict, err := cat.ValidateGrants(c.taskGrantContext(ctx, task, []string{grant}))
	if err != nil {
		return validationErrorf("%v", err)
	}
	if isDelegatedRequest(ctx) && (len(verdict.RequiresPerson) > 0 || model.IsPersonOnlyGrant(grant) || model.IsAdminOnlyGrant(grant)) {
		return forbiddenCodeErrorf("requires_person", "capability %s requires a person acting directly (not via a delegating app)", grant)
	}
	if len(verdict.RequiresPerson) > 0 && c.requestActor(ctx).Actor.Kind != model.PrincipalHuman {
		return forbiddenErrorf("capability %s requires human approval (settings/capabilities.yml marks it requires_person)", grant)
	}
	return nil
}

// catalogueApprovalRefusal refuses a non-person approving a task that holds a
// grant the catalogue marks as a person's decision. Filing leaves such a
// task proposed for a person to approve (catalogueFilingGrants), and
// nothing at dispatch asks again whether a person did: without this, an
// agent or service account holding tasks:admin could file the task and approve it
// itself, and the run would hold the grant with no person deciding.
// A grant the catalogue refuses outright is not this check's business:
// dispatch holds that task whoever approves it (orchestrator.catalogueHold).
func (c *Client) catalogueApprovalRefusal(ctx context.Context, task model.Task) error {
	if isDelegatedRequest(ctx) {
		var personOnly []string
		for _, g := range task.Grants {
			if (model.IsPersonOnlyGrant(g.Capability) || model.IsAdminOnlyGrant(g.Capability)) && !slices.Contains(personOnly, g.Capability) {
				personOnly = append(personOnly, g.Capability)
			}
		}
		// Admission above standard replaced the person-only
		// priority-dispatch and durable grants, so it asks for a
		// person acting directly the way holding either did.
		if adm := model.NormalizeAdmission(task.Admission); adm.AtLeast(model.AdmissionBypass) {
			personOnly = append(personOnly, "admission "+string(adm))
		}
		if push, err := c.templateDeclaredPush(ctx, task.TemplateID); err != nil {
			return err
		} else if push != nil {
			personOnly = append(personOnly, "declared push set from "+push.Path)
		}
		if len(personOnly) > 0 {
			return forbiddenCodeErrorf("requires_person", "task %s holds %s, which only a person acting directly may approve",
				task.ID, strings.Join(personOnly, ", "))
		}
		needsPerson, err := c.catalogueRequiresPerson(ctx, task)
		if err != nil || len(needsPerson) == 0 {
			return err
		}
		return forbiddenCodeErrorf("requires_person", "task %s holds %s, which settings/capabilities.yml marks as a person's decision (requires_person): only a person acting directly may approve it",
			task.ID, strings.Join(needsPerson, ", "))
	}
	if c.requestActor(ctx).Actor.Kind == model.PrincipalHuman {
		return nil
	}
	needsPerson, err := c.catalogueRequiresPerson(ctx, task)
	if err != nil || len(needsPerson) == 0 {
		return err
	}
	return forbiddenErrorf("task %s holds %s, which settings/capabilities.yml marks as a person's decision (requires_person): only a person may approve it",
		task.ID, strings.Join(needsPerson, ", "))
}

// catalogueRequiresPerson is the grants task holds that the capability
// catalogue marks as a person's decision (requires_person), whoever is
// asking: catalogueApprovalRefusal's question without its exemption for
// a person, so the agent API's approval rail (Server.agentApprovalRail)
// can ask it of the task an agent is about to approve. nil for none, or
// for a deployment with no catalogue.
func (c *Client) catalogueRequiresPerson(ctx context.Context, task model.Task) ([]string, error) {
	cat, err := c.capabilityCatalogue(ctx)
	if err != nil || cat == nil {
		return nil, err
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
	return needsPerson, nil
}

// catalogueEditRefusalFor is catalogueEditRefusal for the task stored as
// id, after the capability policy's own refusal (policyEditRefusalFor);
// a task that is not there is the edit's own error to report.
func (c *Client) catalogueEditRefusalFor(ctx context.Context, id, grant string) error {
	if err := c.policyEditRefusalFor(ctx, id, grant); err != nil {
		return err
	}
	task, err := c.Store.GetTask(ctx, model.NormalizeTaskID(id))
	if err != nil || task == nil {
		return nil
	}
	return c.catalogueEditRefusal(ctx, *task, grant)
}

func fieldValidationError(field string, err error) error {
	msg := err.Error()
	return &ValidationError{
		err:    fmt.Errorf("%s: %s", field, msg),
		fields: map[string]string{field: msg},
	}
}

// mergedCapabilityCatalogue builds the model.CapabilityCatalogue that
// staterepo.ExportConfig writes into settings/capabilities.yml
// (staterepo.capabilitiesDocumentFor): the policy's catalogue combined with
// MCPServers' enabled/alias state, deployment task defaults, catalogue CI
// defaults, and per-repo task and CI defaults. Validating it before a save
// refuses contradictions grain state check would refuse after export.
func mergedCapabilityCatalogue(
	cat *model.CapabilityCatalogue,
	mcpServers map[string]protocol.ServerConfig,
	taskDefaults []string,
	ciDefaults []string,
	repoDefaults map[string]model.CatalogueRepoDefaults,
) *model.CapabilityCatalogue {
	if cat == nil {
		return nil
	}
	servers := make([]model.CatalogueServer, 0, len(cat.Servers)+len(mcpServers))
	seen := make(map[string]bool, len(cat.Servers))
	for _, s := range cat.Servers {
		id := strings.TrimSpace(s.ID)
		if srvCfg, ok := mcpServers[id]; ok && id != "" {
			enabled := !srvCfg.Disabled && (s.Enabled == nil || *s.Enabled)
			s.Enabled = &enabled
			if len(srvCfg.Aliases) > 0 {
				s.Aliases = srvCfg.Aliases
			}
		}
		if id != "" {
			seen[id] = true
		}
		servers = append(servers, s)
	}
	extraIDs := make([]string, 0, len(mcpServers))
	for id := range mcpServers {
		if !seen[id] {
			extraIDs = append(extraIDs, id)
		}
	}
	sort.Strings(extraIDs)
	for _, id := range extraIDs {
		srvCfg := mcpServers[id]
		enabled := !srvCfg.Disabled
		servers = append(servers, model.CatalogueServer{
			ID:      id,
			Enabled: &enabled,
			Aliases: srvCfg.Aliases,
		})
	}
	var inProcess map[string]model.CatalogueInProcess
	if len(cat.InProcess) > 0 {
		inProcess = make(map[string]model.CatalogueInProcess, len(cat.InProcess))
		for k, v := range cat.InProcess {
			if k == model.PriorityDispatchCapability || k == model.DurableCapability {
				continue
			}
			inProcess[k] = v
		}
	}
	keptTask, _, _ := model.ExtractLegacyAdmissionStrings(taskDefaults)
	keptCI, _, _ := model.ExtractLegacyAdmissionStrings(ciDefaults)
	var keptRepos map[string]model.CatalogueRepoDefaults
	if len(repoDefaults) > 0 {
		keptRepos = make(map[string]model.CatalogueRepoDefaults, len(repoDefaults))
		for repo, rd := range repoDefaults {
			rt, _, _ := model.ExtractLegacyAdmissionStrings(rd.Task)
			rc, _, _ := model.ExtractLegacyAdmissionStrings(rd.CI)
			keptRepos[repo] = model.CatalogueRepoDefaults{Task: rt, CI: rc}
		}
	}
	return &model.CapabilityCatalogue{
		Defaults: model.CatalogueDefaults{
			Task:  keptTask,
			CI:    keptCI,
			Repos: keptRepos,
		},
		Servers:   servers,
		InProcess: inProcess,
		APIScopes: cat.APIScopes,
	}
}

func repoDefaultsMap(repoConfigs []model.RepoConfig) map[string]model.CatalogueRepoDefaults {
	if len(repoConfigs) == 0 {
		return nil
	}
	out := make(map[string]model.CatalogueRepoDefaults, len(repoConfigs))
	for _, rc := range repoConfigs {
		if len(rc.DefaultCapabilities) == 0 && len(rc.CICapabilities) == 0 {
			continue
		}
		out[rc.Repo.String()] = model.CatalogueRepoDefaults{
			Task: rc.DefaultCapabilities,
			CI:   rc.CICapabilities,
		}
	}
	return out
}

func validateCataloguePolicySave(
	cat *model.CapabilityCatalogue,
	mcpServers map[string]protocol.ServerConfig,
	taskDefaults []string,
	repoConfigs []model.RepoConfig,
) error {
	if cat == nil {
		return nil
	}
	merged := mergedCapabilityCatalogue(cat, mcpServers, taskDefaults, cat.Defaults.CI, repoDefaultsMap(repoConfigs))
	if err := merged.Validate(); err != nil {
		return fieldValidationError("catalogue", err)
	}
	return nil
}

func validateCatalogueTaskDefaults(cfg *model.Config, ids []string) error {
	if cfg == nil || cfg.CapabilityPolicy.Catalogue == nil || len(ids) == 0 {
		return nil
	}
	merged := mergedCapabilityCatalogue(cfg.CapabilityPolicy.Catalogue, cfg.MCPServers, ids, nil, nil)
	if err := merged.Validate(); err != nil {
		return fieldValidationError("defaultCapabilities", err)
	}
	return nil
}

func (c *Client) validateCatalogueRepoDefaults(ctx context.Context, repo model.RepoRef, taskGrants, ciGrants []string) error {
	if c.Store == nil || (len(taskGrants) == 0 && len(ciGrants) == 0) {
		return nil
	}
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil || cfg == nil || cfg.CapabilityPolicy.Catalogue == nil {
		return err
	}
	if len(taskGrants) > 0 {
		merged := mergedCapabilityCatalogue(cfg.CapabilityPolicy.Catalogue, cfg.MCPServers, nil, nil, map[string]model.CatalogueRepoDefaults{
			repo.String(): {Task: taskGrants},
		})
		if err := merged.Validate(); err != nil {
			return fieldValidationError("defaultCapabilities", err)
		}
	}
	if len(ciGrants) > 0 {
		merged := mergedCapabilityCatalogue(cfg.CapabilityPolicy.Catalogue, cfg.MCPServers, nil, nil, map[string]model.CatalogueRepoDefaults{
			repo.String(): {CI: ciGrants},
		})
		if err := merged.Validate(); err != nil {
			return fieldValidationError("ciCapabilities", err)
		}
	}
	return nil
}
