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
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// suiteNotFoundError marks a suite ID with no suite behind it.
type suiteNotFoundError struct{ ID string }

func (e *suiteNotFoundError) Error() string { return "no suite " + e.ID }

// suiteRunNotFoundError is the same, for a run id.
type suiteRunNotFoundError struct{ ID int64 }

func (e *suiteRunNotFoundError) Error() string {
	return "no suite run " + strconv.FormatInt(e.ID, 10)
}

// SuiteItem is one template a suite runs, over the wire -- TemplateName
// is resolved by the caller (ListSuites/CreateSuite/UpdateSuite fetch it
// once per distinct id), Schedule.TemplateName's own reasoning.
type SuiteItem struct {
	TemplateID   string `json:"templateId"`
	TemplateName string `json:"templateName,omitempty"`
}

// Suite is a suite's JSON shape (bwsalmon/agents#642) -- a saved
// combination of templates plus how to run them, created once and run
// against any repo and branch any number of times.
type Suite struct {
	ID    string      `json:"id"`
	Name  string      `json:"name"`
	Items []SuiteItem `json:"items"`
	// Mode is "count" or "until_clean" -- model.SuiteMode's own
	// vocabulary, unchanged, since it is already the plain English a UI
	// wants to show and to send back.
	Mode string `json:"mode"`
	// Count is meaningful for Mode "count" only; MaxPasses for
	// "until_clean" only -- which is which is model.Suite.Validate's
	// own job, not this type's.
	Count                int       `json:"count,omitempty"`
	MaxPasses            int       `json:"maxPasses,omitempty"`
	RequireApproval      bool      `json:"requireApproval"`
	AutoMerge            bool      `json:"autoMerge"`
	AutoApproveProposals bool      `json:"autoApproveProposals"`
	CreatedAt            time.Time `json:"createdAt"`
	// DeclaredIn is where this suite came from when a repository
	// declares it rather than somebody here creating it --
	// ui.Template.DeclaredIn's own doc comment.
	DeclaredIn *Declaration `json:"declaredIn,omitempty"`
	// Revision identifies this suite's editable content as it stands
	// (revision.go): an editor sends it back on PATCH, and is refused
	// with a 409 if somebody else has saved the suite since.
	Revision string `json:"revision,omitempty"`
}

func suiteItemsFrom(items []model.SuiteItem, names map[string]string) []SuiteItem {
	out := make([]SuiteItem, 0, len(items))
	for _, it := range items {
		out = append(out, SuiteItem{TemplateID: it.TemplateID, TemplateName: names[it.TemplateID]})
	}
	return out
}

func suiteFrom(s model.Suite, names map[string]string) Suite {
	out := Suite{
		ID:                   s.ID,
		Name:                 s.Name,
		Items:                suiteItemsFrom(s.Items, names),
		Mode:                 string(s.Mode),
		Count:                s.Count,
		MaxPasses:            s.MaxPasses,
		RequireApproval:      s.RequireApproval,
		AutoMerge:            s.AutoMerge,
		AutoApproveProposals: s.AutoApproveProposals,
		CreatedAt:            s.CreatedAt,
	}
	out.Revision = suiteRevision(out)
	return out
}

// templateNamesFor resolves every id in ids to its template's own Name,
// once per distinct id -- ListSchedules' own caching, applied here since
// a suite's own items (and a list of many suites) can repeat a template.
func (c *Client) templateNamesFor(ctx context.Context, ids []string) (map[string]string, error) {
	names := map[string]string{}
	for _, id := range ids {
		if _, ok := names[id]; ok || id == "" {
			continue
		}
		if t, err := c.Store.GetTemplate(ctx, id); err == nil && t != nil {
			names[id] = t.Name
		}
	}
	return names, nil
}

func templateIDsOf(items []model.SuiteItem) []string {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.TemplateID
	}
	return ids
}

// ListSuites returns every suite, newest first.
func (c *Client) ListSuites(ctx context.Context) ([]Suite, error) {
	list, err := c.Store.ListSuites(ctx)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, s := range list {
		ids = append(ids, templateIDsOf(s.Items)...)
	}
	names, err := c.templateNamesFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	declared, err := c.declaredIn(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Suite, 0, len(list))
	for _, s := range list {
		suite := suiteFrom(s, names)
		suite.DeclaredIn = declarationOf(declared, s.ID)
		out = append(out, suite)
	}
	return out, nil
}

// parseSuiteMode validates mode and the count/maxPasses pair that goes
// with it -- CreateSuite's and UpdateSuite's shared check, so a bad
// suite is refused here rather than left for model.Suite.Validate to
// discover with a less specific message.
func parseSuiteMode(mode string, count, maxPasses int) (model.SuiteMode, error) {
	switch model.SuiteMode(mode) {
	case model.SuiteCount:
		if count < 1 {
			return "", validationErrorf("count must be at least 1")
		}
		return model.SuiteCount, nil
	case model.SuiteUntilClean:
		if maxPasses < 1 {
			return "", validationErrorf("maxPasses must be at least 1")
		}
		return model.SuiteUntilClean, nil
	default:
		return "", validationErrorf(`mode must be "count" or "until_clean", got %q`, mode)
	}
}

func parseSuiteItems(templateIDs []string) ([]model.SuiteItem, error) {
	if len(templateIDs) == 0 {
		return nil, validationErrorf("a suite needs at least one template")
	}
	items := make([]model.SuiteItem, 0, len(templateIDs))
	for _, id := range templateIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, validationErrorf("a suite item needs a template")
		}
		items = append(items, model.SuiteItem{TemplateID: id})
	}
	return items, nil
}

// checkSuiteTemplatesExist reports an error naming the first templateId
// in items that GetTemplate cannot find -- CreateSuite's and
// UpdateSuite's own early check, so a suite pointed at a typo'd or
// already-deleted template is refused at save time rather than left for
// CreateSuiteRun/FireNextPass to fail on days or a pass later.
//
// It also refuses a template a suite could not fill
// (model.ValidateUnattendedTemplate) -- except one already in kept, the
// items the suite carried before this edit: one of those that has since
// gained such a field is UpdateTemplate's to refuse, and a suite left
// over from before that rule can still be renamed or paused.
func (c *Client) checkSuiteTemplatesExist(ctx context.Context, items, kept []model.SuiteItem) error {
	already := make(map[string]bool, len(kept))
	for _, it := range kept {
		already[it.TemplateID] = true
	}
	for _, it := range items {
		t, err := c.Store.GetTemplate(ctx, it.TemplateID)
		if err != nil {
			return err
		}
		if t == nil {
			return validationErrorf("unknown template %s", it.TemplateID)
		}
		if already[it.TemplateID] {
			continue
		}
		if err := model.ValidateUnattendedTemplate(t.Fields, "a suite"); err != nil {
			return validationErrorf("template %q: %s", t.Name, err)
		}
	}
	return nil
}

// CreateSuiteRequest is a new suite's fields. AutoMerge, omitted,
// defaults to true and RequireApproval, omitted, defaults to false --
// bwsalmon/agents#642's own "by default they should auto queue and auto
// merge."
type CreateSuiteRequest struct {
	Name                 string   `json:"name"`
	TemplateIDs          []string `json:"templateIds"`
	Mode                 string   `json:"mode"`
	Count                int      `json:"count"`
	MaxPasses            int      `json:"maxPasses"`
	RequireApproval      bool     `json:"requireApproval"`
	AutoMerge            *bool    `json:"autoMerge"`
	AutoApproveProposals bool     `json:"autoApproveProposals"`
}

// CreateSuite files a new suite template straight into the store.
func (c *Client) CreateSuite(ctx context.Context, req CreateSuiteRequest) (Suite, error) {
	if err := c.checkSettingsWritable(ctx, "suite"); err != nil {
		return Suite{}, err
	}
	if strings.TrimSpace(req.Name) == "" {
		return Suite{}, validationErrorf("name is required")
	}
	items, err := parseSuiteItems(req.TemplateIDs)
	if err != nil {
		return Suite{}, err
	}
	mode, err := parseSuiteMode(req.Mode, req.Count, req.MaxPasses)
	if err != nil {
		return Suite{}, err
	}
	if err := c.checkSuiteTemplatesExist(ctx, items, nil); err != nil {
		return Suite{}, err
	}
	autoMerge := true
	if req.AutoMerge != nil {
		autoMerge = *req.AutoMerge
	}

	id, err := c.Store.NewSuiteID(ctx)
	if err != nil {
		return Suite{}, err
	}
	suite := model.Suite{
		ID:                   id,
		Name:                 req.Name,
		Items:                items,
		Mode:                 mode,
		Count:                req.Count,
		MaxPasses:            req.MaxPasses,
		RequireApproval:      req.RequireApproval,
		AutoMerge:            autoMerge,
		AutoApproveProposals: req.AutoApproveProposals,
		CreatedAt:            c.now(),
	}
	if err := suite.Validate(); err != nil {
		return Suite{}, &ValidationError{err: err}
	}
	if err := c.checkSettingsWritable(ctx, "suite"); err != nil {
		return Suite{}, err
	}
	if err := c.Store.PutSuite(ctx, suite); err != nil {
		return Suite{}, err
	}
	names, err := c.templateNamesFor(ctx, templateIDsOf(suite.Items))
	if err != nil {
		return Suite{}, err
	}
	return suiteFrom(suite, names), nil
}

// draftSuite is the suite req describes, for composing rather than for
// saving: the same fields CreateSuite reads, with none of the things it
// insists on. A suite somebody is still typing may have no name, no
// template picked yet and an empty count box, and none of that is an
// error in a preview of it -- the items chosen so far compose into
// exactly the tasks those items file, which is the whole point of
// showing one mid-edit (DraftSuiteComposition).
//
// A mode that is neither of the two known ones is left empty for the
// same reason, and reads as "no stopping rule stated yet": mode decides
// how many times a pass is filed and nothing about what a pass files, so
// an unanswered one costs the preview a sentence rather than the answer.
// It cannot invent anything either way -- a template id that names no
// template still fails, in the composition itself, because "this does
// not resolve" is the answer a preview exists to give.
func draftSuite(req CreateSuiteRequest) model.Suite {
	items := make([]model.SuiteItem, 0, len(req.TemplateIDs))
	for _, id := range req.TemplateIDs {
		if id = strings.TrimSpace(id); id != "" {
			items = append(items, model.SuiteItem{TemplateID: id})
		}
	}
	autoMerge := true
	if req.AutoMerge != nil {
		autoMerge = *req.AutoMerge
	}
	suite := model.Suite{
		Name:            req.Name,
		Items:           items,
		Count:           req.Count,
		MaxPasses:       req.MaxPasses,
		RequireApproval: req.RequireApproval,
		AutoMerge:       autoMerge,
	}
	switch mode := model.SuiteMode(req.Mode); mode {
	case model.SuiteCount, model.SuiteUntilClean:
		suite.Mode = mode
	}
	return suite
}

// UpdateSuiteRequest is a suite's editable fields -- nil means "leave
// this one alone", UpdateScheduleRequest's own convention. Editing a
// suite never changes a run already in flight (model.SuiteRun's own doc
// comment: everything a run needs is snapshotted at creation).
type UpdateSuiteRequest struct {
	Name                 *string   `json:"name,omitempty"`
	TemplateIDs          *[]string `json:"templateIds,omitempty"`
	Mode                 *string   `json:"mode,omitempty"`
	Count                *int      `json:"count,omitempty"`
	MaxPasses            *int      `json:"maxPasses,omitempty"`
	RequireApproval      *bool     `json:"requireApproval,omitempty"`
	AutoMerge            *bool     `json:"autoMerge,omitempty"`
	AutoApproveProposals *bool     `json:"autoApproveProposals,omitempty"`
	// Revision, given, is the suite's Revision as the caller last read
	// it, and the edit is refused (staleEditError) if the suite has been
	// saved since. Absent applies the edit regardless.
	Revision *string `json:"revision,omitempty"`
}

// UpdateSuite edits a suite's fields in place.
func (c *Client) UpdateSuite(ctx context.Context, id string, req UpdateSuiteRequest) (Suite, error) {
	if err := c.checkSettingsWritable(ctx, "suite"); err != nil {
		return Suite{}, err
	}
	if err := c.refuseDeclaredEdit(ctx, id, "edit"); err != nil {
		return Suite{}, err
	}
	existing, err := c.Store.GetSuite(ctx, id)
	if err != nil {
		return Suite{}, err
	}
	if existing == nil {
		return Suite{}, &suiteNotFoundError{ID: id}
	}
	if err := checkRevision("suite", id, req.Revision, suiteRevisionOf(*existing)); err != nil {
		return Suite{}, err
	}

	var items []model.SuiteItem
	if req.TemplateIDs != nil {
		items, err = parseSuiteItems(*req.TemplateIDs)
		if err != nil {
			return Suite{}, err
		}
		if err := c.checkSuiteTemplatesExist(ctx, items, existing.Items); err != nil {
			return Suite{}, err
		}
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		return Suite{}, validationErrorf("name cannot be empty")
	}

	// Validated against the merged copy before anything is written, not
	// inside the store's own read-modify-write closure: mutate may run
	// more than once against a freshly re-read row (Store.UpdateSuite's
	// own retry shape), and model.Suite.Validate's error is a plain
	// error, not this package's ValidationError -- easier to check
	// once, here, where the type writeClientError needs to see it as is
	// still in scope, than to have the store's own s.write wrap it in
	// transit.
	merged := *existing
	if req.Name != nil {
		merged.Name = *req.Name
	}
	if items != nil {
		merged.Items = items
	}
	if req.Mode != nil {
		merged.Mode = model.SuiteMode(*req.Mode)
	}
	if req.Count != nil {
		merged.Count = *req.Count
	}
	if req.MaxPasses != nil {
		merged.MaxPasses = *req.MaxPasses
	}
	if req.RequireApproval != nil {
		merged.RequireApproval = *req.RequireApproval
	}
	if req.AutoMerge != nil {
		merged.AutoMerge = *req.AutoMerge
	}
	if req.AutoApproveProposals != nil {
		merged.AutoApproveProposals = *req.AutoApproveProposals
	}
	if err := merged.Validate(); err != nil {
		return Suite{}, &ValidationError{err: err}
	}

	if err := c.checkSettingsWritable(ctx, "suite"); err != nil {
		return Suite{}, err
	}
	if err := c.Store.UpdateSuite(ctx, id, func(s *model.Suite) error {
		// merged was built from the row as read above; one saved since
		// would otherwise be overwritten whole by it.
		if err := checkRevision("suite", id, req.Revision, suiteRevisionOf(*s)); err != nil {
			return err
		}
		*s = merged
		return nil
	}); err != nil {
		return Suite{}, err
	}

	updated, err := c.Store.GetSuite(ctx, id)
	if err != nil {
		return Suite{}, err
	}
	names, err := c.templateNamesFor(ctx, templateIDsOf(updated.Items))
	if err != nil {
		return Suite{}, err
	}
	return suiteFrom(*updated, names), nil
}

// DeleteSuite removes a suite outright -- DeleteSuite's own doc comment
// gives the reasoning: a run already started from it keeps its own
// snapshot and is untouched.
//
// A schedule that still runs this suite on a cadence is the one thing
// that refuses the delete, DeleteTemplate's own guard for the same
// situation one level down: deleting the suite anyway would leave that
// schedule with nothing to fire, surfacing only as a failed firing
// whenever it next came due.
func (c *Client) DeleteSuite(ctx context.Context, id string) error {
	if err := c.checkSettingsWritable(ctx, "suite"); err != nil {
		return err
	}
	if err := c.refuseDeclaredEdit(ctx, id, "delete"); err != nil {
		return err
	}
	existing, err := c.Store.GetSuite(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return &suiteNotFoundError{ID: id}
	}
	inUse, err := c.Store.SchedulesUsingSuite(ctx, id)
	if err != nil {
		return err
	}
	if len(inUse) > 0 {
		named := make([]dependent, len(inUse))
		for i, sched := range inUse {
			named[i] = dependent{name: firstNonEmpty(sched.Title, sched.ID)}
		}
		which, err := c.nameDependents(ctx, named)
		if err != nil {
			return err
		}
		return validationErrorf(
			"suite is used by %d schedule(s)%s; repoint or delete those first", len(inUse), which)
	}
	if err := c.checkSettingsWritable(ctx, "suite"); err != nil {
		return err
	}
	return c.Store.DeleteSuite(ctx, id)
}

// --- runs --------------------------------------------------------------

// SuiteRunTask is one task instance a run's pass instantiated, over the
// wire.
type SuiteRunTask struct {
	TaskID            string `json:"taskId"`
	TemplateID        string `json:"templateId"`
	TemplateName      string `json:"templateName"`
	Pass              int    `json:"pass"`
	State             string `json:"state"`
	Approved          bool   `json:"approved"`
	OpenedPullRequest bool   `json:"openedPullRequest"`
	Proposed          bool   `json:"proposed"`
}

// SuiteRun is one run of a suite against a repo and branch, over the
// wire -- what a "see the status of outstanding suite runs" view needs
// (bwsalmon/agents#642).
type SuiteRun struct {
	ID        int64  `json:"id"`
	SuiteID   string `json:"suiteId"`
	SuiteName string `json:"suiteName"`
	// ScheduleID is the schedule whose firing started this run, empty for
	// one a human started by hand -- what lets a run say where it came
	// from, the same way a task a schedule filed carries its firing tag.
	ScheduleID           string `json:"scheduleId,omitempty"`
	Repo                 string `json:"repo"`
	Base                 string `json:"base"`
	Mode                 string `json:"mode"`
	Count                int    `json:"count,omitempty"`
	MaxPasses            int    `json:"maxPasses,omitempty"`
	RequireApproval      bool   `json:"requireApproval"`
	AutoMerge            bool   `json:"autoMerge"`
	AutoApproveProposals bool   `json:"autoApproveProposals"`
	// Status is one of "active", "succeeded", "failed" --
	// model.SuiteRunStatus's own vocabulary.
	Status      string         `json:"status"`
	Error       string         `json:"error,omitempty"`
	Pass        int            `json:"pass"`
	CreatedAt   time.Time      `json:"createdAt"`
	CompletedAt *time.Time     `json:"completedAt,omitempty"`
	Tasks       []SuiteRunTask `json:"tasks"`
}

func suiteRunFrom(r model.SuiteRun) SuiteRun {
	out := SuiteRun{
		ID:                   r.ID,
		SuiteID:              r.SuiteID,
		SuiteName:            r.SuiteName,
		ScheduleID:           r.ScheduleID,
		Repo:                 r.Target.String(),
		Base:                 r.Base,
		Mode:                 string(r.Mode),
		Count:                r.Count,
		MaxPasses:            r.MaxPasses,
		RequireApproval:      r.RequireApproval,
		AutoMerge:            r.AutoMerge,
		AutoApproveProposals: r.AutoApproveProposals,
		Status:               string(r.Status),
		Error:                r.LastError,
		Pass:                 r.CurrentPass(),
		CreatedAt:            r.CreatedAt,
		CompletedAt:          r.CompletedAt,
	}
	for _, t := range r.Tasks {
		out.Tasks = append(out.Tasks, SuiteRunTask{
			TaskID: t.TaskID, TemplateID: t.TemplateID, TemplateName: t.TemplateName,
			Pass: t.PassNumber, State: string(t.State), Approved: t.Approved,
			OpenedPullRequest: t.OpenedPullRequest, Proposed: t.Proposed,
		})
	}
	return out
}

// ListSuiteRuns returns every suite run, newest first.
func (c *Client) ListSuiteRuns(ctx context.Context) ([]SuiteRun, error) {
	list, err := c.Store.ListSuiteRuns(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SuiteRun, 0, len(list))
	for _, r := range list {
		out = append(out, suiteRunFrom(r))
	}
	return out, nil
}

// GetSuiteRun returns one run, or an error if there is none with that id.
func (c *Client) GetSuiteRun(ctx context.Context, id int64) (SuiteRun, error) {
	run, err := c.Store.GetSuiteRun(ctx, id)
	if err != nil {
		return SuiteRun{}, err
	}
	if run == nil {
		return SuiteRun{}, &suiteRunNotFoundError{ID: id}
	}
	return suiteRunFrom(*run), nil
}

// CreateSuiteRunRequest starts suiteId running against repo and base --
// bwsalmon/agents#642's own "run the template against a repo and
// branch."
type CreateSuiteRunRequest struct {
	SuiteID string `json:"suiteId"`
	Repo    string `json:"repo"`
	Base    string `json:"base"`
}

// CreateSuiteRun starts a new run of req.SuiteID against req.Repo and
// req.Base, filing its first pass immediately.
func (c *Client) CreateSuiteRun(ctx context.Context, req CreateSuiteRunRequest) (SuiteRun, error) {
	if strings.TrimSpace(req.SuiteID) == "" {
		return SuiteRun{}, validationErrorf("suiteId is required")
	}
	if strings.TrimSpace(req.Repo) == "" {
		return SuiteRun{}, validationErrorf("repo is required")
	}
	// Trimmed for CreateSchedule's reason: " main " pasted into the run
	// form names main, and the preview above it already said so.
	req.Base = strings.TrimSpace(req.Base)
	if req.Base == "" {
		return SuiteRun{}, validationErrorf("base is required: a suite run has no default branch to fall back to")
	}
	if err := validateBase("base", req.Base); err != nil {
		return SuiteRun{}, err
	}
	target, err := c.parseRepo(req.Repo)
	if err != nil {
		return SuiteRun{}, err
	}
	// The same boundary a task's own target passes (refuseOffAllowlist):
	// a suite run files its first pass immediately and every later pass
	// as the last one finishes (model.Store.CreateSuiteRun and
	// fireSuitePass), each an approved task targeting this repo, and
	// nothing on that path asks this question again.
	if err := c.refuseOffAllowlist(target, "a suite cannot be run against it"); err != nil {
		return SuiteRun{}, err
	}
	suite, err := c.Store.GetSuite(ctx, req.SuiteID)
	if err != nil {
		return SuiteRun{}, err
	}
	if suite == nil {
		return SuiteRun{}, &suiteNotFoundError{ID: req.SuiteID}
	}
	run, err := c.Store.CreateSuiteRun(ctx, *suite, target, req.Base, c.now())
	if err != nil {
		return SuiteRun{}, err
	}
	// Its first pass is filed already, so a pass the team cannot reach
	// is parked rather than refused: the run exists either way, and
	// every later pass is parked the same way by the orchestrator
	// (orchestrator.parkUnreached).
	if err := c.parkUnreached(ctx, suiteRunTaskIDs(run)); err != nil {
		return SuiteRun{}, err
	}
	return suiteRunFrom(run), nil
}

// suiteRunTaskIDs is every task run has filed so far.
func suiteRunTaskIDs(run model.SuiteRun) []string {
	ids := make([]string, 0, len(run.Tasks))
	for _, t := range run.Tasks {
		ids = append(ids, t.TaskID)
	}
	return ids
}

// --- handlers ------------------------------------------------------------

func (s *Server) handleListSuites(w http.ResponseWriter, r *http.Request) {
	list, err := s.tasks.ListSuites(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateSuite(w http.ResponseWriter, r *http.Request) {
	var req CreateSuiteRequest
	if !readJSONRefusing(w, r, &req, retiredTeamField) {
		return
	}
	suite, err := s.tasks.CreateSuite(r.Context(), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, suite)
}

func (s *Server) handleUpdateSuite(w http.ResponseWriter, r *http.Request) {
	if err := s.tasks.authorizeSuiteEdit(r.Context(), r.PathValue("id")); err != nil {
		writeClientError(w, err)
		return
	}
	var req UpdateSuiteRequest
	if !readJSONRefusing(w, r, &req, retiredTeamField) {
		return
	}
	suite, err := s.tasks.UpdateSuite(r.Context(), r.PathValue("id"), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, suite)
}

func (s *Server) handleDeleteSuite(w http.ResponseWriter, r *http.Request) {
	if err := s.tasks.DeleteSuite(r.Context(), r.PathValue("id")); err != nil {
		writeClientError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListSuiteRuns(w http.ResponseWriter, r *http.Request) {
	list, err := s.tasks.ListSuiteRuns(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateSuiteRun(w http.ResponseWriter, r *http.Request) {
	var req CreateSuiteRunRequest
	if !readJSONRefusing(w, r, &req, retiredTeamField) {
		return
	}
	run, err := s.tasks.CreateSuiteRun(r.Context(), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, run)
}

func (s *Server) handleGetSuiteRun(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeClientError(w, validationErrorf("invalid run id"))
		return
	}
	run, err := s.tasks.GetSuiteRun(r.Context(), id)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}
