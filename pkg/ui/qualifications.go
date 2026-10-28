package ui

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// QualificationItem is one entry in a repo's qualification plan, over the
// wire -- model.QualificationItem's own fields, plus TemplateName filled
// in for display (a resolved lookup, never itself accepted on write --
// PutQualificationPlan re-resolves every TemplateID from the store
// instead of trusting whatever a caller sent back).
type QualificationItem struct {
	TemplateID   string   `json:"templateId"`
	TemplateName string   `json:"templateName,omitempty"`
	Repeat       int      `json:"repeat"`
	DependsOn    []string `json:"dependsOn,omitempty"`
}

func qualificationItemFrom(it model.QualificationItem, name string) QualificationItem {
	return QualificationItem{TemplateID: it.TemplateID, TemplateName: name, Repeat: it.Repeat, DependsOn: it.DependsOn}
}

// QualificationPlan is a repo's whole qualification setup, over the wire
// -- Configured false, with Items empty, means nothing has ever been
// saved for this repo.
type QualificationPlan struct {
	Configured      bool                `json:"configured"`
	Repo            string              `json:"repo"`
	RequireApproval bool                `json:"requireApproval"`
	AutoPromote     bool                `json:"autoPromote"`
	Items           []QualificationItem `json:"items"`
}

// qualificationPlanFrom resolves each item's template name for display --
// GetQualificationPlan's own caller, not PutQualificationPlan's, which
// never has a reason to render one back.
func (c *Client) qualificationPlanFrom(ctx context.Context, p model.QualificationPlan) QualificationPlan {
	out := QualificationPlan{
		Configured: p.Configured, Repo: p.Repo.String(),
		RequireApproval: p.RequireApproval, AutoPromote: p.AutoPromote,
		Items: []QualificationItem{},
	}
	for _, it := range p.Items {
		name := it.TemplateID
		if tmpl, err := c.Store.GetTemplate(ctx, it.TemplateID); err == nil && tmpl != nil {
			name = tmpl.Name
		}
		out.Items = append(out.Items, qualificationItemFrom(it, name))
	}
	return out
}

// GetQualificationPlan reads repo's qualification plan. A zero
// QualificationPlan with Configured false, and no error, means nothing
// has saved one yet.
func (c *Client) GetQualificationPlan(ctx context.Context, repo model.RepoRef) (QualificationPlan, error) {
	plan, err := c.Store.GetQualificationPlan(ctx, repo)
	if err != nil {
		return QualificationPlan{}, err
	}
	if plan == nil {
		return QualificationPlan{Repo: repo.String(), Items: []QualificationItem{}}, nil
	}
	return c.qualificationPlanFrom(ctx, *plan), nil
}

// PutQualificationPlanRequest is a repo's whole qualification plan, all
// required rather than a partial update: PutQualificationPlan always
// replaces the whole plan wholesale, so a partial-update shape would only
// invite a caller to believe a field it omitted was left alone when it
// was in fact cleared. TemplateName is accepted but ignored -- the
// response shape's own field, echoed back by a form that round-trips
// what it read -- since what a template is actually called always comes
// fresh from the store, never from a caller.
type PutQualificationPlanRequest struct {
	RequireApproval bool                `json:"requireApproval"`
	AutoPromote     bool                `json:"autoPromote"`
	Items           []QualificationItem `json:"items"`
}

// PutQualificationPlan validates req -- every item naming a template
// that actually exists, a positive repeat count, and a dependency graph
// with no cycle (model.QualificationPlan.Validate) -- before replacing
// repo's plan wholesale. An unbound template may schedule against any
// repo's plan, which is the ordinary case; a template bound to a repo
// of its own (model.Template.Target) may only schedule against that
// repo's, since CreateQualificationRun always targets repo and the
// candidate's own branch and so could not honour a binding pointing
// somewhere else. Rejected here, when a human is saving the plan,
// rather than left to fail the run days later when a candidate is
// finally cut.
func (c *Client) PutQualificationPlan(ctx context.Context, repo model.RepoRef, req PutQualificationPlanRequest) (QualificationPlan, error) {
	if err := c.checkRepoPerson(ctx, repo); err != nil {
		return QualificationPlan{}, err
	}
	if err := c.checkSettingsWritable(ctx, "qualification_config", "qualification_item"); err != nil {
		return QualificationPlan{}, err
	}

	// A plan is the standing instruction to file tasks against this repo
	// -- model.Store.CreateQualificationRun targets it for every
	// candidate, straight into the store and already approved -- so a
	// repo off Config.TargetRepos or the owning team's target_repos is refused
	// here, where somebody is saving the plan, rather than at the run nothing
	// could have landed (refuseOffRepoAllowlist). A plan holding no items at all is the
	// exception: emptying one is how a plan for a repo the list has since
	// stopped naming is taken out of service, and refusing that would
	// leave it firing with no way to stop it but deleting the repo.
	if len(req.Items) > 0 {
		if err := c.refuseOffRepoAllowlist(ctx, repo, "candidates cannot be qualified in it"); err != nil {
			return QualificationPlan{}, err
		}
	}
	items := make([]model.QualificationItem, 0, len(req.Items))
	for _, it := range req.Items {
		if it.TemplateID == "" {
			return QualificationPlan{}, validationErrorf("every qualification item needs a template")
		}
		tmpl, err := c.Store.GetTemplate(ctx, it.TemplateID)
		if err != nil {
			return QualificationPlan{}, err
		}
		if tmpl == nil {
			return QualificationPlan{}, validationErrorf("unknown template %s", it.TemplateID)
		}
		if tmpl.Target != nil && *tmpl.Target != repo {
			return QualificationPlan{}, validationErrorf(
				"template %s is bound to %s, so it cannot qualify %s", tmpl.Name, tmpl.Target, repo)
		}
		repeat := it.Repeat
		if repeat < 1 {
			repeat = 1
		}
		items = append(items, model.QualificationItem{TemplateID: it.TemplateID, Repeat: repeat, DependsOn: it.DependsOn})
	}
	plan := model.QualificationPlan{
		Repo: repo, Configured: true,
		RequireApproval: req.RequireApproval, AutoPromote: req.AutoPromote,
		Items: items,
	}
	if err := plan.Validate(); err != nil {
		return QualificationPlan{}, validationErrorf("%v", err)
	}
	if err := c.Store.PutQualificationPlan(ctx, plan); err != nil {
		return QualificationPlan{}, err
	}
	return c.qualificationPlanFrom(ctx, plan), nil
}

// QualificationTaskSummary is one task instance's own row in a run's
// summary -- everything a UI needs to render it without a second call
// per task.
type QualificationTaskSummary struct {
	TaskID        string      `json:"taskId"`
	TemplateID    string      `json:"templateId"`
	TemplateName  string      `json:"templateName"`
	InstanceIndex int         `json:"instanceIndex"`
	Repeat        int         `json:"repeat"`
	Approved      bool        `json:"approved"`
	State         model.State `json:"state"`
}

// QualificationRun is one candidate's own qualification summary. Status
// is model.QualificationStatus's own vocabulary, unchanged, and Tasks is
// ordered failures first -- the issue's own "show...any failures up
// front" -- so the frontend needs no sort of its own.
type QualificationRun struct {
	ID          int64                      `json:"id"`
	CandidateID int64                      `json:"candidateId"`
	CreatedAt   time.Time                  `json:"createdAt"`
	Status      string                     `json:"status"`
	Tasks       []QualificationTaskSummary `json:"tasks"`
}

// qualificationTaskRank sorts a run's tasks failures-first, then
// everything else in the order they were created.
func qualificationTaskRank(s model.State) int {
	switch s {
	case model.StateFailed, model.StateClosed:
		return 0
	default:
		return 1
	}
}

func qualificationRunFrom(r model.QualificationRun) QualificationRun {
	tasks := make([]QualificationTaskSummary, len(r.Tasks))
	for i, t := range r.Tasks {
		tasks[i] = QualificationTaskSummary{
			TaskID: t.TaskID, TemplateID: t.TemplateID, TemplateName: t.TemplateName,
			InstanceIndex: t.InstanceIndex, Repeat: t.Repeat, Approved: t.Approved, State: t.State,
		}
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		return qualificationTaskRank(tasks[i].State) < qualificationTaskRank(tasks[j].State)
	})
	return QualificationRun{
		ID: r.ID, CandidateID: r.CandidateID, CreatedAt: r.CreatedAt,
		Status: string(model.QualificationStatus(r.Tasks)),
		Tasks:  tasks,
	}
}

// qualificationRunNotFoundError marks a candidate id with no qualification
// run behind it, or a run named by a candidate id that does not belong to
// the repo a caller asked through -- writeClientError maps it to a 404,
// scheduleNotFoundError's own reasoning for why this is its own type
// rather than NotFoundError itself.
type qualificationRunNotFoundError struct{ CandidateID int64 }

func (e *qualificationRunNotFoundError) Error() string {
	return fmt.Sprintf("release candidate %d has no qualification run", e.CandidateID)
}

// GetCandidateQualification returns candidateID's own qualification run
// summary, or nil (with no error) if none has been created for it yet --
// the normal state for an active candidate a plan has not been scheduled
// against by the next reconciler cycle, or one whose repo has no plan
// configured at all, so a UI polling this is not treated as asking a
// question with a wrong answer.
func (c *Client) GetCandidateQualification(ctx context.Context, repo model.RepoRef, candidateID int64) (*QualificationRun, error) {
	run, err := c.Store.CandidateQualificationRun(ctx, candidateID)
	if err != nil {
		return nil, err
	}
	if run == nil || run.Repo != repo {
		return nil, nil
	}
	out := qualificationRunFrom(*run)
	return &out, nil
}

// ApproveQualificationRun approves every task instance in candidateID's
// own qualification run as one action -- the issue's own "require the
// user to approve submitting all of them."
func (c *Client) ApproveQualificationRun(ctx context.Context, repo model.RepoRef, candidateID int64) (QualificationRun, error) {
	if err := c.checkRepoPerson(ctx, repo); err != nil {
		return QualificationRun{}, err
	}
	run, err := c.Store.CandidateQualificationRun(ctx, candidateID)
	if err != nil {
		return QualificationRun{}, err
	}
	if run == nil || run.Repo != repo {
		return QualificationRun{}, &qualificationRunNotFoundError{CandidateID: candidateID}
	}
	if err := c.Store.ApproveQualificationRun(ctx, run.ID, model.Attribution{Actor: c.actor(ctx)}, c.now()); err != nil {
		return QualificationRun{}, err
	}
	updated, err := c.Store.CandidateQualificationRun(ctx, candidateID)
	if err != nil {
		return QualificationRun{}, err
	}
	return qualificationRunFrom(*updated), nil
}

// --- handlers ------------------------------------------------------------

func (s *Server) handleGetQualificationPlan(w http.ResponseWriter, r *http.Request) {
	repo, err := s.parseRepoPath(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	plan, err := s.tasks.GetQualificationPlan(r.Context(), repo)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) handlePutQualificationPlan(w http.ResponseWriter, r *http.Request) {
	repo, err := s.parseRepoPath(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	var req PutQualificationPlanRequest
	if !readJSON(w, r, &req) {
		return
	}
	plan, err := s.tasks.PutQualificationPlan(r.Context(), repo, req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func parseCandidateID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, validationErrorf("invalid candidate id %q", r.PathValue("id"))
	}
	return id, nil
}

func (s *Server) handleGetCandidateQualification(w http.ResponseWriter, r *http.Request) {
	repo, err := s.parseRepoPath(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	id, err := parseCandidateID(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	run, err := s.tasks.GetCandidateQualification(r.Context(), repo, id)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleApproveQualificationRun(w http.ResponseWriter, r *http.Request) {
	repo, err := s.parseRepoPath(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	id, err := parseCandidateID(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	run, err := s.tasks.ApproveQualificationRun(r.Context(), repo, id)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}
