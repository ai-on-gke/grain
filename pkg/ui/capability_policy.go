package ui

import (
	"context"
	"github.com/bwsalmon/grain/pkg/capability/intree"
	"net/http"
	"sort"

	"github.com/bwsalmon/grain/pkg/capability/protocol"
	"github.com/bwsalmon/grain/pkg/ci"
	"github.com/bwsalmon/grain/pkg/model"
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

// SetCapabilityPolicy replaces the stored policy with p.
func (c *Client) SetCapabilityPolicy(ctx context.Context, p model.CapabilityPolicy) error {
	if err := p.Validate(); err != nil {
		return validationErrorf("%s", err)
	}
	if err := c.checkSettingsWritable(ctx, "grain_config"); err != nil {
		return err
	}
	return c.Store.UpdateConfig(ctx, func(cfg *model.Config) error {
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
	holders, err := c.capabilityGrantHolders(ctx)
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
	holders, err := c.capabilityGrantHolders(ctx)
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

// capabilityGrantHolders gathers every capability reference grant on an
// open task, a template, a schedule, and a job of a workflow declared in
// Settings, each with the runs it is held by (model.CapabilitySubject),
// sorted by kind and id. A repository's own .grain/workflows files are
// not read: they live in the repository, not in this deployment.
func (c *Client) capabilityGrantHolders(ctx context.Context) ([]CapabilityGrantHolder, error) {
	var out []CapabilityGrantHolder
	addGrants := func(kind, id, name string, repo *model.RepoRef, grants []model.Grant, subjects []model.CapabilitySubject) {
		for _, g := range grants {
			if !model.IsCapabilityRef(g.Capability) {
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
