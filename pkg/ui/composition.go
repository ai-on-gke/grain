package ui

// What a schedule or a suite will actually file, before it files it.
//
// A schedule fires a template; a suite runs several of them against a
// repo and branch; a schedule can fire a whole suite on a cadence. Each
// of those combines two or three declarations into a task, and until
// model/compose.go there was no single place that said how -- and still
// no place at all that would simply *show* the answer. A human editing a
// suite could see that it had three templates and that it auto-merges,
// and had to know from somewhere else that the suite's own auto-merge is
// what its tasks get whatever each template says.
//
// These two endpoints are that answer, rendered: every task the next
// firing (or the next run) files, with the value of each field and the
// layer that decided it. Nothing here recomputes the combination -- it
// calls model.Store.ScheduleComposition/SuiteComposition, which is what
// the firing itself calls -- so a preview that says "auto-merge: on,
// from the suite" is not a claim about the firing, it is the firing's
// own answer, made without writing anything.

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// ComposedField is one field of one composed task: its value, rendered
// for reading, and which declaration decided it.
//
// Values is a list for every field, scalars included (one entry), so a
// UI renders every row the same way. It is empty where the composition
// says nothing -- no reads, no branch, no network override -- and Source
// is what makes that emptiness readable: an empty base from the
// "template" is a bound template asking for its repo's default branch,
// and an empty network access from the "deployment" is the deployment's
// own default answering.
type ComposedField struct {
	// Name is the field, in the vocabulary a UI labels: "title",
	// "body", "repo", "base", "reads", "writes", "capabilities",
	// "customFields", "network", "autoMerge", "approval".
	Name   string   `json:"name"`
	Values []string `json:"values,omitempty"`
	// Source is a model.CompositionLayer: "template", "schedule",
	// "suite", "plan", "candidate", "repo" or "deployment".
	Source string `json:"source"`
	// Sources, when non-empty, gives the model.CompositionLayer for each
	// entry in Values (same length and order) when a list field combines
	// values from more than one layer -- e.g. a schedule's or template's
	// own capabilities alongside the target repo's default grain-ci grant.
	Sources []string `json:"sources,omitempty"`
}

// ComposedTask is one task a firing will file.
type ComposedTask struct {
	// TemplateID and TemplateName name the template this task's content
	// comes from, both empty for a schedule firing its own inline
	// content.
	TemplateID   string `json:"templateId,omitempty"`
	TemplateName string `json:"templateName,omitempty"`
	// Title is repeated out of Fields so a UI can head the task with it
	// without hunting through the list.
	Title  string          `json:"title"`
	Fields []ComposedField `json:"fields"`
}

// Composition is every task one firing files, with enough of the firing
// itself to say what it is: a schedule filing one task, a schedule
// running a suite, or a suite run somebody is about to start by hand.
type Composition struct {
	// Kind is "task" for a firing that files a single task and "suite"
	// for one that starts a suite run.
	Kind string `json:"kind"`
	// ScheduleID is set when this is a schedule's own composition,
	// empty for a suite previewed on its own.
	ScheduleID string `json:"scheduleId,omitempty"`
	SuiteID    string `json:"suiteId,omitempty"`
	SuiteName  string `json:"suiteName,omitempty"`
	// Repo and Base are what the firing runs against, before any
	// template binding moves an individual task off them -- empty for a
	// suite previewed with no repo chosen yet, which is exactly what a
	// "Run…" form has to show before somebody picks one.
	Repo string `json:"repo,omitempty"`
	Base string `json:"base,omitempty"`
	// Mode, Count and MaxPasses are the suite's own stopping rule
	// (model.SuiteMode), so a UI can say that these tasks are one pass
	// of up to several. Empty and zero for a task-backed schedule.
	Mode      string `json:"mode,omitempty"`
	Count     int    `json:"count,omitempty"`
	MaxPasses int    `json:"maxPasses,omitempty"`
	// Tasks is what one firing files, in the order it files them: one
	// task, or one per item of a suite's pass.
	Tasks []ComposedTask `json:"tasks"`
}

// composedTaskFrom renders one model.Composition. The field order is the
// order a reader wants it in -- what the task is, where it runs, what it
// may reach, how it lands -- and every field is always present, because
// "reads: nothing" is an answer a preview exists to give.
//
// When the composition has no model of its own, modelFallbackSource and
// modelFallbackValues specify which layer the empty value falls to:
// "team" with the team's default model name, or "deployment" with no values.
func composedTaskFrom(c model.Composition, modelFallbackSource string, modelFallbackValues []string) ComposedTask {
	repo := ""
	if c.Target != nil {
		repo = c.Target.String()
	}
	reads := make([]string, 0, len(c.Reads))
	for _, r := range c.Reads {
		reads = append(reads, r.String())
	}
	writes := make([]string, 0, len(c.Writes))
	for _, w := range c.Writes {
		if w.Base == "" {
			writes = append(writes, w.Repo.String())
			continue
		}
		writes = append(writes, fmt.Sprintf("%s (%s)", w.Repo, w.Base))
	}
	capabilities := make([]string, 0, len(c.Grants))
	capabilitySources := make([]string, 0, len(c.Grants))
	hasDistinctCapabilitySources := false
	for i, g := range c.Grants {
		capabilities = append(capabilities, g.Capability)
		src := c.Sources.Grants
		if i < len(c.GrantSources) && c.GrantSources[i] != "" {
			src = c.GrantSources[i]
		} else if g.Via == model.GrantByDefault {
			src = model.LayerRepo
		}
		capabilitySources = append(capabilitySources, string(src))
		if i > 0 && capabilitySources[i] != capabilitySources[0] {
			hasDistinctCapabilitySources = true
		}
	}
	capabilitiesSource := string(c.Sources.Grants)
	if len(capabilitySources) > 0 && !hasDistinctCapabilitySources {
		capabilitiesSource = capabilitySources[0]
		capabilitySources = nil
	}
	modelSource := string(c.Sources.Model)
	modelValues := nonEmpty(c.Model)
	if c.Model == "" {
		if modelFallbackSource != "" {
			modelSource = modelFallbackSource
			modelValues = modelFallbackValues
		}
	}
	fields := []ComposedField{
		{Name: "title", Values: nonEmpty(c.Title), Source: string(c.Sources.Title)},
		{Name: "body", Values: nonEmpty(c.Body), Source: string(c.Sources.Body)},
		{Name: "repo", Values: nonEmpty(repo), Source: string(c.Sources.Target)},
		{Name: "base", Values: nonEmpty(c.Base), Source: string(c.Sources.Base)},
		{Name: "reads", Values: reads, Source: string(c.Sources.Reads)},
		{Name: "writes", Values: writes, Source: string(c.Sources.Writes)},
		{Name: "capabilities", Values: capabilities, Source: capabilitiesSource, Sources: capabilitySources},
		{Name: "customFields", Values: composedCustomFields(c.CustomFields), Source: string(c.Sources.CustomFields)},
		{Name: "network", Values: nonEmpty(string(c.NetworkAccess)), Source: string(c.Sources.NetworkAccess)},
		{Name: "nothingNeeded", Values: nonEmpty(string(c.NothingNeededAction)), Source: string(c.Sources.NothingNeededAction)},
		{Name: "model", Values: modelValues, Source: modelSource},
		{Name: "autoMerge", Values: []string{onOff(c.AutoMerge)}, Source: string(c.Sources.AutoMerge)},
		{Name: "approval", Values: []string{approvalWord(c.Approved)}, Source: string(c.Sources.Approved)},
	}
	return ComposedTask{
		TemplateID:   c.TemplateID,
		TemplateName: c.TemplateName,
		Title:        c.Title,
		Fields:       fields,
	}
}

func nonEmpty(v string) []string {
	if v == "" {
		return nil
	}
	return []string{v}
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

// approvalWord says what the state actually means for the filed task,
// rather than "true"/"false": an unapproved task sits in proposed until
// somebody approves it, which is the thing a reader of a preview needs
// to know before starting a run of thirty of them.
func approvalWord(approved bool) string {
	if approved {
		return "approved when filed"
	}
	return "waits for approval"
}

// composedCustomFields renders the declared-field values one task
// carries, sorted by field name so two previews of the same composition
// read identically (a map's own order would not).
func composedCustomFields(values map[string][]string) []string {
	if len(values) == 0 {
		return nil
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, name+": "+strings.Join(values[name], ", "))
	}
	return out
}

// ScheduleComposition is every task sched's next firing files, composed
// but not filed -- the same resolution model.Store.FireSchedule makes,
// so what this shows and what the cadence does cannot disagree.
//
// A schedule that runs a suite composes that suite's next pass: every
// pass of the run composes identically, so one pass is the answer for
// all of them, and Mode/Count/MaxPasses say how many there may be.
func (c *Client) ScheduleComposition(ctx context.Context, id string) (Composition, error) {
	sched, err := c.Store.GetSchedule(ctx, id)
	if err != nil {
		return Composition{}, err
	}
	if sched == nil {
		return Composition{}, &scheduleNotFoundError{ID: id}
	}
	return c.compositionOfSchedule(ctx, *sched, id)
}

// DraftScheduleComposition is every task the schedule req describes
// would file -- the same answer ScheduleComposition gives for a saved
// row, given the request body that would have created one instead of an
// id.
//
// This is what a schedule form previews itself with while it is being
// edited: a brand new schedule has no id to ask about, and an edited one
// has an id whose stored fields are precisely the ones somebody is in
// the middle of changing. Nothing is written, and nothing is
// snapshotted: the template or suite the draft names is resolved out of
// the store here and now (scheduleFromRequest, then
// model.Store.ScheduleComposition), so a preview of an unsaved schedule
// shows today's template rather than a copy of one.
//
// A half-filled draft composes as far as it goes rather than failing --
// scheduleFromRequest's own doc comment on which requirements belong to
// creating a schedule and which to describing one.
func (c *Client) DraftScheduleComposition(ctx context.Context, req CreateScheduleRequest) (Composition, error) {
	req.Base = strings.TrimSpace(req.Base) // as CreateSchedule will
	sched, _, _, err := c.scheduleFromRequest(ctx, req)
	if err != nil {
		return Composition{}, err
	}
	return c.compositionOfSchedule(ctx, sched, "this schedule")
}

// compositionOfSchedule composes sched, saved or drafted. what names it
// in the one error message that has to say which schedule could not be
// composed -- an id for a stored one, a phrase for a draft that has no
// id yet.
func (c *Client) compositionOfSchedule(ctx context.Context, sched model.Schedule, what string) (Composition, error) {
	composed, err := c.Store.ScheduleComposition(ctx, sched, c.stateRepo())
	if err != nil {
		// A template or suite that has gone missing from under a
		// schedule is the deployment's own state, not a bad request, but
		// it is also exactly what somebody opening this wants told --
		// so it is reported as the message it is rather than as a bare
		// 500 (the firing itself fails the same way, once a cycle,
		// where only the log hears it).
		return Composition{}, validationErrorf("%s cannot be composed: %v", what, err)
	}
	out := Composition{
		Kind:       "task",
		ScheduleID: sched.ID,
		Base:       sched.Base,
	}
	if sched.Target != (model.RepoRef{}) {
		out.Repo = sched.Target.String()
	}
	if sched.SuiteID != nil {
		out.Kind, out.SuiteID = "suite", *sched.SuiteID
		suite, err := c.Store.GetSuite(ctx, *sched.SuiteID)
		if err != nil {
			return Composition{}, err
		}
		if suite != nil {
			describeSuiteRun(&out, *suite)
		}
	}
	out.Tasks = c.composedTasksFrom(ctx, composed)
	return out, nil
}

// SuiteComposition is every task one pass of a suite files when it is
// run against repo and base -- what the "Run…" form is about to start,
// shown before it starts it.
//
// repo may be empty: a suite previewed before anybody has chosen one
// still has an answer worth showing, since each item's own binding (and
// everything that is not placement) is already decided. Those tasks come
// back with an empty repo whose source is the suite, which reads as
// "whatever this run targets".
func (c *Client) SuiteComposition(ctx context.Context, id, repo, base string) (Composition, error) {
	suite, err := c.Store.GetSuite(ctx, id)
	if err != nil {
		return Composition{}, err
	}
	if suite == nil {
		return Composition{}, &suiteNotFoundError{ID: id}
	}
	return c.compositionOfSuite(ctx, *suite, repo, base, id)
}

// DraftSuiteComposition is every task one pass of the suite req
// describes would file against repo and base -- SuiteComposition's own
// answer, given the request body that would have created the suite
// instead of an id.
//
// It is what the suite form previews itself with as it is edited, for
// DraftScheduleComposition's reasons: a suite being created has no id,
// and an edited one's stored items are the thing being changed. The
// templates the draft names are resolved out of the store on every call,
// so what this shows is today's templates and not a snapshot.
func (c *Client) DraftSuiteComposition(ctx context.Context, req CreateSuiteRequest, repo, base string) (Composition, error) {
	// The items a draft names are asked the question a saved suite's
	// are, so a preview cannot read out another team's template either.
	if err := c.checkSuiteTemplatesExist(ctx, draftSuite(req).Items, nil); err != nil {
		return Composition{}, err
	}
	return c.compositionOfSuite(ctx, draftSuite(req), repo, base, "this suite")
}

// compositionOfSuite composes one pass of suite, saved or drafted,
// against repo and base. what names it in the one error message that has
// to say which suite could not be composed (compositionOfSchedule's own
// note on why it is a parameter).
func (c *Client) compositionOfSuite(ctx context.Context, suite model.Suite, repo, base, what string) (Composition, error) {
	base = strings.TrimSpace(base) // as CreateSuiteRun will
	var target model.RepoRef
	if strings.TrimSpace(repo) != "" {
		parsed, err := c.parseRepo(repo)
		if err != nil {
			return Composition{}, err
		}
		target = parsed
	}
	composed, err := c.Store.SuiteComposition(ctx, suite, target, base)
	if err != nil {
		return Composition{}, validationErrorf("%s cannot be composed: %v", what, err)
	}
	out := Composition{
		Kind:      "suite",
		SuiteID:   suite.ID,
		SuiteName: suite.Name,
		Base:      base,
		Tasks:     c.composedTasksFrom(ctx, composed),
	}
	if target != (model.RepoRef{}) {
		out.Repo = target.String()
	}
	describeSuiteRun(&out, suite)
	return out, nil
}

// describeSuiteRun copies the stopping rule a run of suite would use
// onto out -- the same snapshot CreateSuiteRun takes, so a preview says
// how many passes of the tasks below it there may be.
func describeSuiteRun(out *Composition, suite model.Suite) {
	out.SuiteName = suite.Name
	out.Mode = string(suite.Mode)
	switch suite.Mode {
	case model.SuiteCount:
		out.Count = suite.Count
	case model.SuiteUntilClean:
		out.MaxPasses = suite.MaxPasses
	}
}

func (c *Client) resolveModelFallback(ctx context.Context) (string, []string) {
	// The deployment's default model (Config.DefaultModel)
	// (docs/single-tenant-reset.md, stage 1 and 6-ii-b2).
	if c != nil && c.Store != nil {
		if cfg, err := c.Store.GetConfig(ctx); err == nil && cfg != nil {
			if models := cfg.DefaultModels(); len(models) > 0 {
				return string(model.LayerDeployment), models
			}
		}
	}
	return string(model.LayerDeployment), nil
}

func (c *Client) composedTasksFrom(ctx context.Context, composed []model.Composition) []ComposedTask {
	fallbackSource, fallbackValues := c.resolveModelFallback(ctx)
	out := make([]ComposedTask, 0, len(composed))
	for _, comp := range composed {
		out = append(out, composedTaskFrom(comp, fallbackSource, fallbackValues))
	}
	return out
}

// --- handlers ------------------------------------------------------------

func (s *Server) handleScheduleComposition(w http.ResponseWriter, r *http.Request) {
	composition, err := s.tasks.ScheduleComposition(r.Context(), r.PathValue("id"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, composition)
}

// handleSuiteComposition takes the repo and branch a run would be
// started against as query parameters rather than a body: it is a
// question, not a change, and a form that re-asks it on every keystroke
// of the branch field wants a URL it can cache and cancel.
func (s *Server) handleSuiteComposition(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	composition, err := s.tasks.SuiteComposition(r.Context(), r.PathValue("id"), q.Get("repo"), q.Get("base"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, composition)
}

// The two draft endpoints below are POSTs that write nothing: the thing
// being composed is a body somebody is still typing, which is too big to
// be a query string and does not exist anywhere to be named by an id.
// They take exactly the body their POST /api/schedules and POST
// /api/suites neighbours take, so a form previews itself by sending the
// payload it would save -- there is no second shape to keep in step, and
// a preview of a request that would be refused says why it would be.

func (s *Server) handleDraftScheduleComposition(w http.ResponseWriter, r *http.Request) {
	var req CreateScheduleRequest
	if !readJSON(w, r, &req) {
		return
	}
	composition, err := s.tasks.DraftScheduleComposition(r.Context(), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, composition)
}

// handleDraftSuiteComposition keeps repo and base in the query, where
// the saved suite's own endpoint has them: they are the run's, not the
// suite's, and the body here is the suite.
func (s *Server) handleDraftSuiteComposition(w http.ResponseWriter, r *http.Request) {
	var req CreateSuiteRequest
	if !readJSON(w, r, &req) {
		return
	}
	q := r.URL.Query()
	composition, err := s.tasks.DraftSuiteComposition(r.Context(), req, q.Get("repo"), q.Get("base"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, composition)
}
