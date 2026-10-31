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
	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"net/http"
	"sort"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// The deployment's capability policy (grain_config.capability_policy,
// model.CapabilityPolicy, docs/capabilities-spec.md §4.2.2) and who holds
// each capability server's references today. Settings edits the policy
// here, previews which existing grants a change would newly refuse before
// it is saved, and warns with a server's holders before it is removed or
// renamed.

// CapabilityPolicyBody is GET and PUT /api/capability-policy, and the
// body of POST /api/capability-policy/preview.
type CapabilityPolicyBody struct {
	Policy model.CapabilityPolicy `json:"policy"`
}

// CapabilityGrantHolder is one place a capability reference is granted:
// an open task, a template, a schedule, or a job of a workflow this
// deployment declares for a repo.
type CapabilityGrantHolder struct {
	// Kind is "task", "template", "schedule" or "workflow".
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
	// Repo is the repository the holder's runs are against, if any.
	Repo string `json:"repo,omitempty"`
	// Grant is the recorded grant string, arguments and all.
	Grant string `json:"grant"`

	subjects []model.CapabilitySubject
}

// CapabilityPolicyDenial is one existing grant a proposed policy would
// refuse where the stored one admits it.
type CapabilityPolicyDenial struct {
	CapabilityGrantHolder
	Reason string `json:"reason"`
}

// CapabilityPolicyPreview is POST /api/capability-policy/preview's answer.
type CapabilityPolicyPreview struct {
	Denied []CapabilityPolicyDenial `json:"denied"`
}

// CapabilityServerUsage is GET /api/capability-servers/usage: each
// configured server's capability_id with every holder of a reference to
// it. A server nothing grants is listed with no holders.
type CapabilityServerUsage struct {
	ID      string                  `json:"id"`
	Holders []CapabilityGrantHolder `json:"holders"`
}

// CapabilityPolicy reads the stored policy.
func (c *Client) CapabilityPolicy(ctx context.Context) (model.CapabilityPolicy, error) {
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil || cfg == nil {
		return model.CapabilityPolicy{}, err
	}
	return cfg.CapabilityPolicy, nil
}

// SetCapabilityPolicy replaces the stored policy's rules with p's. A p
// with no catalogue keeps the stored one: the catalogue is
// settings/capabilities.yml's (model.CapabilityPolicy.Catalogue), and
// Settings → Capabilities' rule editor saves its rules alone, so
// replacing the whole policy would have erased every catalogue
// restriction -- and the file with them -- on a save about something else.
func (c *Client) SetCapabilityPolicy(ctx context.Context, p model.CapabilityPolicy) error {
	if err := p.Validate(); err != nil {
		return validationErrorf("%s", err)
	}
	if err := c.checkSettingsWritable(ctx, "grain_config"); err != nil {
		return err
	}
	return c.Store.UpdateConfig(ctx, func(cfg *model.Config) error {
		if p.Catalogue == nil {
			p.Catalogue = cfg.CapabilityPolicy.Catalogue
		}
		cfg.CapabilityPolicy = p
		return nil
	})
}

// PreviewCapabilityPolicy lists every existing grant next would refuse
// for a run the stored policy admits it to.
func (c *Client) PreviewCapabilityPolicy(ctx context.Context, next model.CapabilityPolicy) (CapabilityPolicyPreview, error) {
	if err := next.Validate(); err != nil {
		return CapabilityPolicyPreview{}, validationErrorf("%s", err)
	}
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		return CapabilityPolicyPreview{}, err
	}
	var current model.CapabilityPolicy
	var configured map[string]protocol.ServerConfig
	if cfg != nil {
		current = cfg.CapabilityPolicy
		configured = cfg.MCPServers
	}
	configured, _ = intree.EnsureBuiltins(configured)
	holders, err := c.grantHolders(ctx, model.IsCapabilityRef)
	if err != nil {
		return CapabilityPolicyPreview{}, err
	}
	out := CapabilityPolicyPreview{Denied: []CapabilityPolicyDenial{}}
	for _, h := range holders {
		for _, s := range h.subjects {
			if current.Permits(h.Grant, s, configured) != nil {
				continue
			}
			if err := next.Permits(h.Grant, s, configured); err != nil {
				out.Denied = append(out.Denied, CapabilityPolicyDenial{CapabilityGrantHolder: h, Reason: err.Error()})
				break
			}
		}
	}
	return out, nil
}

// CapabilityServerUsage lists, for each configured server, where its
// references are granted.
func (c *Client) CapabilityServerUsage(ctx context.Context) ([]CapabilityServerUsage, error) {
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string]*CapabilityServerUsage{}
	var configured map[string]protocol.ServerConfig
	if cfg != nil {
		configured = cfg.MCPServers
	}
	configured, _ = intree.EnsureBuiltins(configured)
	for id := range configured {
		byID[id] = &CapabilityServerUsage{ID: id, Holders: []CapabilityGrantHolder{}}
	}
	holders, err := c.grantHolders(ctx, model.IsCapabilityRef)
	if err != nil {
		return nil, err
	}
	for _, h := range holders {
		id := capabilityGrantServer(h.Grant)
		// A grant naming an id a built-in adopted is that entry's.
		if entry, ok := intree.ResolveAlias(configured, id); ok {
			id = entry
		}
		u, ok := byID[id]
		if !ok {
			// A grant naming a server nobody has configured is still
			// usage: Settings shows it so a re-added server's holders
			// are not a surprise.
			u = &CapabilityServerUsage{ID: id, Holders: []CapabilityGrantHolder{}}
			byID[id] = u
		}
		u.Holders = append(u.Holders, h)
	}
	out := make([]CapabilityServerUsage, 0, len(byID))
	for _, u := range byID {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// capabilityGrantServer is the capability_id a reference grant names.
func capabilityGrantServer(g string) string {
	ref := model.CapabilityGrantRef(g)
	if parsed, err := protocol.ParseRef(ref); err == nil {
		return parsed.CapabilityID
	}
	return ref
}

// grantHolders gathers every grant keep admits -- model.IsCapabilityRef
// for capability references -- on an open task, a template, a schedule,
// and a job of a workflow declared in Settings, each with the runs it is
// held by (model.CapabilitySubject), sorted by kind and id. A
// repository's own .grain/workflows files are not read: they live in the
// repository, not in this deployment.
func (c *Client) grantHolders(ctx context.Context, keep func(string) bool) ([]CapabilityGrantHolder, error) {
	var out []CapabilityGrantHolder
	addGrants := func(kind, id, name string, repo *model.RepoRef, grants []model.Grant, subjects []model.CapabilitySubject) {
		for _, g := range grants {
			if !keep(g.Capability) {
				continue
			}
			h := CapabilityGrantHolder{Kind: kind, ID: id, Name: name, Grant: g.Capability, subjects: subjects}
			if repo != nil {
				h.Repo = repo.String()
			}
			out = append(out, h)
		}
	}
	taskSubject := func(repo *model.RepoRef) []model.CapabilitySubject {
		return []model.CapabilitySubject{{Repo: repo, Kind: model.CapabilityRunTask}}
	}

	tasks, err := c.Store.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	states, err := c.ReadStates(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		if states[t.ID].Settled() {
			continue
		}
		addGrants("task", t.ID, t.Title, t.Target, t.Grants, taskSubject(t.Target))
	}
	templates, err := c.Store.ListTemplates(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range templates {
		name := t.Name
		if name == "" {
			name = t.Title
		}
		addGrants("template", t.ID, name, t.Target, t.Grants, taskSubject(t.Target))
	}
	schedules, err := c.Store.ListSchedules(ctx)
	if err != nil {
		return nil, err
	}
	for _, s := range schedules {
		// A schedule naming a template files that template's grants,
		// which are already listed under the template.
		if s.TemplateID != nil {
			continue
		}
		repo := s.Target
		addGrants("schedule", s.ID, s.Title, &repo, s.Grants, taskSubject(&repo))
	}
	workflows, err := c.Store.ListRepoWorkflows(ctx)
	if err != nil {
		return nil, err
	}
	for _, w := range workflows {
		parsed, err := ci.Parse(w.Path, []byte(w.Text))
		if err != nil {
			continue
		}
		repo := w.Repo
		subjects := workflowSubjects(&repo, parsed.On)
		for _, job := range parsed.Jobs {
			var grants []model.Grant
			for _, g := range append(append([]string{}, job.Capabilities...), job.OptionalCapabilities...) {
				grants = append(grants, model.Grant{Capability: g})
			}
			addGrants("workflow", repo.String()+":"+w.Path+"#"+job.ID, w.Path+" · "+job.ID, &repo, grants, subjects)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// workflowSubjects are the CI runs a workflow's triggers start: one per
// event it answers to, with a pull request counted both from a trusted
// author and from an untrusted one (a fork, or no write access).
func workflowSubjects(repo *model.RepoRef, on ci.Triggers) []model.CapabilitySubject {
	var out []model.CapabilitySubject
	add := func(event ci.Event, untrusted bool) {
		out = append(out, model.CapabilitySubject{Repo: repo, Kind: model.CapabilityRunCI, Event: string(event), Untrusted: untrusted})
	}
	if on.Push != nil {
		add(ci.EventPush, false)
	}
	if on.PullRequest != nil {
		add(ci.EventPullRequest, false)
		add(ci.EventPullRequest, true)
	}
	if on.Release != nil {
		add(ci.EventRelease, false)
	}
	if on.Manual {
		add(ci.EventManual, false)
	}
	if len(on.Schedule) > 0 {
		add(ci.EventSchedule, false)
	}
	return out
}

func (s *Server) handleGetCapabilityPolicy(w http.ResponseWriter, r *http.Request) {
	p, err := s.tasks.CapabilityPolicy(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, CapabilityPolicyBody{Policy: p})
}

func (s *Server) handleSetCapabilityPolicy(w http.ResponseWriter, r *http.Request) {
	var req CapabilityPolicyBody
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.tasks.SetCapabilityPolicy(r.Context(), req.Policy); err != nil {
		writeClientError(w, err)
		return
	}
	s.handleGetCapabilityPolicy(w, r)
}

func (s *Server) handlePreviewCapabilityPolicy(w http.ResponseWriter, r *http.Request) {
	var req CapabilityPolicyBody
	if !readJSON(w, r, &req) {
		return
	}
	out, err := s.tasks.PreviewCapabilityPolicy(r.Context(), req.Policy)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCapabilityServerUsage(w http.ResponseWriter, r *http.Request) {
	out, err := s.tasks.CapabilityServerUsage(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// CapabilityPolicy is GET /api/capability-policy.
func (c *HTTPClient) CapabilityPolicy(ctx context.Context) (CapabilityPolicyBody, error) {
	var out CapabilityPolicyBody
	err := c.do(ctx, http.MethodGet, "/api/capability-policy", nil, &out)
	return out, err
}

// SetCapabilityPolicy is PUT /api/capability-policy.
func (c *HTTPClient) SetCapabilityPolicy(ctx context.Context, p model.CapabilityPolicy) (CapabilityPolicyBody, error) {
	var out CapabilityPolicyBody
	err := c.do(ctx, http.MethodPut, "/api/capability-policy", CapabilityPolicyBody{Policy: p}, &out)
	return out, err
}

// PreviewCapabilityPolicy is POST /api/capability-policy/preview.
func (c *HTTPClient) PreviewCapabilityPolicy(ctx context.Context, p model.CapabilityPolicy) (CapabilityPolicyPreview, error) {
	var out CapabilityPolicyPreview
	err := c.do(ctx, http.MethodPost, "/api/capability-policy/preview", CapabilityPolicyBody{Policy: p}, &out)
	return out, err
}

// CapabilityServerUsage is GET /api/capability-servers/usage.
func (c *HTTPClient) CapabilityServerUsage(ctx context.Context) ([]CapabilityServerUsage, error) {
	var out []CapabilityServerUsage
	err := c.do(ctx, http.MethodGet, "/api/capability-servers/usage", nil, &out)
	return out, err
}

// capabilityPolicyFor is the stored policy and the configured servers
// whose aliases it is judged with, the way admission
// (orchestrator.mintCapabilityPass) reads them.
func (c *Client) capabilityPolicyFor(ctx context.Context) (model.CapabilityPolicy, map[string]protocol.ServerConfig, error) {
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		return model.CapabilityPolicy{}, nil, err
	}
	var policy model.CapabilityPolicy
	var configured map[string]protocol.ServerConfig
	if cfg != nil {
		policy = cfg.CapabilityPolicy
		configured = cfg.MCPServers
	}
	configured, _ = intree.EnsureBuiltins(configured)
	return policy, configured, nil
}

// policyFilingGrants judges a new agent task's capability references
// against the capability policy (§4.2.2) before it is written, so a
// grant admission would only refuse later is refused with the task
// (400) instead. A default the policy denies -- a repository's default
// the policy keeps from this run kind, say -- is dropped, since nobody
// asked for it, and dropped names it with the policy's reason, for the
// task's thread: a filer who expected the default is told it is not
// there rather than left to find it missing. Admission asks the policy
// again when it mints, so a policy saved after a task was filed still
// has the last word.
func (c *Client) policyFilingGrants(ctx context.Context, grants []model.Grant, target *model.RepoRef) (kept []model.Grant, dropped []string, err error) {
	policy, configured, err := c.capabilityPolicyFor(ctx)
	if err != nil || len(policy.Rules) == 0 {
		return grants, nil, err
	}
	subject := model.CapabilitySubject{Repo: target, Kind: model.CapabilityRunTask}
	kept = make([]model.Grant, 0, len(grants))
	for _, g := range grants {
		if err := policy.Permits(g.Capability, subject, configured); err != nil {
			if g.Via == model.GrantByDefault {
				dropped = append(dropped, fmt.Sprintf("Default grant %s was not attached to this task: %v.", model.CapabilityGrantRef(g.Capability), err))
				continue
			}
			return nil, nil, validationErrorf("%v", err)
		}
		kept = append(kept, g)
	}
	return kept, dropped, nil
}

// policyEditRefusalFor is the capability policy's answer to grant being
// attached to the task stored as id: 400 for one it denies the task. A
// task that is not there is the edit's own error to report.
func (c *Client) policyEditRefusalFor(ctx context.Context, id, grant string) error {
	if !model.IsCapabilityRef(grant) {
		return nil
	}
	task, err := c.Store.GetTask(ctx, model.NormalizeTaskID(id))
	if err != nil || task == nil {
		return nil
	}
	policy, configured, err := c.capabilityPolicyFor(ctx)
	if err != nil {
		return err
	}
	subject := model.CapabilitySubject{Repo: task.Target, Kind: model.CapabilityRunTask}
	if err := policy.Permits(grant, subject, configured); err != nil {
		return validationErrorf("%v", err)
	}
	return nil
}

// markPolicyRefusals sets PolicyRefusal on each method of methods the
// capability policy denies to subject, so a picker can show it refused
// rather than offer a grant filing would turn away.
func (c *Client) markPolicyRefusals(ctx context.Context, methods []CapabilityMethod, subject model.CapabilitySubject) ([]CapabilityMethod, error) {
	policy, configured, err := c.capabilityPolicyFor(ctx)
	if err != nil || len(policy.Rules) == 0 {
		return methods, err
	}
	for i := range methods {
		if err := policy.Permits(methods[i].Ref, subject, configured); err != nil {
			methods[i].PolicyRefusal = err.Error()
		}
	}
	return methods, nil
}
