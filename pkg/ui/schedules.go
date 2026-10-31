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
	"github.com/ai-on-gke/grain/pkg/schedulefilter"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// scheduleNotFoundError marks a schedule ID with no schedule behind it --
// writeClientError maps it to a 404, the same as NotFoundError does for
// a task id. It is its own type rather than NotFoundError itself so the
// message a caller sees names a schedule, not a task.
type scheduleNotFoundError struct{ ID string }

func (e *scheduleNotFoundError) Error() string { return "no schedule " + e.ID }

// Recurrence is a schedule's cadence, over the wire -- model.Recurrence's
// fields, with Kind matching model.RecurrenceKind's own string values
// exactly (no second vocabulary to translate) and TimeOfDay/Weekday
// rendered the way a human reads them ("14:30", "monday") rather than as
// minutes-since-midnight or time.Weekday's own int. Which of
// EveryNHours/EveryNMinutes/TimeOfDay/Weekday/DayOfMonth is meaningful
// depends on Kind,
// the same sum-type-as-struct shape model.Recurrence itself uses.
type Recurrence struct {
	Kind              string                          `json:"kind"`
	EveryNHours       int                             `json:"everyNHours,omitempty"`
	EveryNMinutes     int                             `json:"everyNMinutes,omitempty"`
	TimeOfDay         string                          `json:"timeOfDay,omitempty"`
	Weekday           string                          `json:"weekday,omitempty"`
	DayOfMonth        int                             `json:"dayOfMonth,omitempty"`
	TimeZone          string                          `json:"timeZone,omitempty"`
	GitHubWorkflowRun *model.GitHubWorkflowRunTrigger `json:"githubWorkflowRun,omitempty"`
}

var weekdayNames = [...]string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}

func formatTimeOfDay(minutes int) string {
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
}

func parseTimeOfDay(text string) (int, error) {
	hh, mm, ok := strings.Cut(text, ":")
	if !ok {
		return 0, validationErrorf("timeOfDay: want HH:MM, got %q", text)
	}
	h, err1 := strconv.Atoi(hh)
	m, err2 := strconv.Atoi(mm)
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, validationErrorf("timeOfDay: want HH:MM, got %q", text)
	}
	return h*60 + m, nil
}

func parseWeekday(name string) (time.Weekday, error) {
	for i, n := range weekdayNames {
		if n == strings.ToLower(strings.TrimSpace(name)) {
			return time.Weekday(i), nil
		}
	}
	return 0, validationErrorf("weekday: unknown day %q", name)
}

func recurrenceFrom(r model.Recurrence) Recurrence {
	out := Recurrence{Kind: string(r.Kind), TimeZone: r.TimeZone}
	switch r.Kind {
	case model.RecurrenceEveryNHours:
		out.EveryNHours = r.EveryNHours
	case model.RecurrenceEveryNMinutes:
		out.EveryNMinutes = r.EveryNMinutes
	case model.RecurrenceDaily:
		out.TimeOfDay = formatTimeOfDay(r.TimeOfDay)
	case model.RecurrenceWeekly:
		out.TimeOfDay = formatTimeOfDay(r.TimeOfDay)
		out.Weekday = weekdayNames[r.Weekday]
	case model.RecurrenceMonthly:
		out.TimeOfDay = formatTimeOfDay(r.TimeOfDay)
		out.DayOfMonth = r.DayOfMonth
	case model.RecurrenceGitHubWorkflowRun:
		out.GitHubWorkflowRun = r.GitHubWorkflowRun
	}
	return out
}

// parseRecurrence turns the wire shape back into model.Recurrence,
// rejecting anything Validate would -- caught here, before the store is
// touched, the same as parseInterval used to for the plain-duration
// version this replaces. That includes everyNMinutes' floor of
// model.MinEveryNMinutes: a schedule cannot ask for a sandbox every
// minute through the API any more than through the editor.
func parseRecurrence(req Recurrence) (model.Recurrence, error) {
	r := model.Recurrence{
		Kind:              model.RecurrenceKind(req.Kind),
		EveryNHours:       req.EveryNHours,
		EveryNMinutes:     req.EveryNMinutes,
		DayOfMonth:        req.DayOfMonth,
		TimeZone:          strings.TrimSpace(req.TimeZone),
		GitHubWorkflowRun: req.GitHubWorkflowRun,
	}
	if req.TimeOfDay != "" {
		minutes, err := parseTimeOfDay(req.TimeOfDay)
		if err != nil {
			return model.Recurrence{}, err
		}
		r.TimeOfDay = minutes
	}
	if req.Weekday != "" {
		weekday, err := parseWeekday(req.Weekday)
		if err != nil {
			return model.Recurrence{}, err
		}
		r.Weekday = weekday
	}
	if err := r.Validate(); err != nil {
		return model.Recurrence{}, validationErrorf("recurrence: %v", err)
	}
	return r, nil
}

// overlapOr is p unless it is the zero value, in which case fallback --
// the "" written down before this field existed reads as
// model.OverlapSkip everywhere it is shown or sent back, so no caller
// has to know that a blank ever meant anything.
func overlapOr(p, fallback model.OverlapPolicy) model.OverlapPolicy {
	if p == "" {
		return fallback
	}
	return p
}

// parseOverlap turns the wire value into a model.OverlapPolicy,
// rejecting anything Validate would -- caught here, before the store is
// touched, the same as parseRecurrence does for a cadence. An empty
// string is "not given", which CreateSchedule reads as the default
// (skip) and UpdateSchedule never sees, since a nil pointer is what
// means "leave this one alone" there.
func parseOverlap(text string) (model.OverlapPolicy, error) {
	p := model.OverlapPolicy(strings.TrimSpace(text))
	if err := p.Validate(); err != nil {
		return "", validationErrorf("overlap: %v", err)
	}
	return overlapOr(p, model.OverlapSkip), nil
}

// Schedule is a schedule's JSON shape -- everything a UI needs to list,
// create, edit and pause one. There is no separate "run now" or history
// list here: NextRunAt/LastRunAt are the whole of what a UI can show
// about a schedule's own timing, and every task it has ever filed already
// shows up in the ordinary task list (Origin.Reason == schedule renders
// there as Task.Scheduled).
type Schedule struct {
	ID                   string `json:"id"`
	Title                string `json:"title"`
	Description          string `json:"description"`
	Repo                 string `json:"repo"`
	Base                 string `json:"base,omitempty"`
	AutoMerge            bool   `json:"autoMerge"`
	AutoApproveProposals bool   `json:"autoApproveProposals"`
	// Reads and Capabilities mirror Task's own fields (bwsalmon/agents#464):
	// every task this schedule files carries them, the same as one filed
	// by hand through CreateTaskRequest.
	Reads []string `json:"reads,omitempty"`
	// Writes is the extra write targets every task this schedule files
	// carries -- model.Schedule.Writes, in ui.WriteTarget's {repo, base}
	// shape, exactly as a task's own Writes are rendered. Repo and Base
	// above are still the one repo and branch a firing is filed
	// against; these are the further ones its run may have work landed
	// in by proposing a task (model.WriteTarget).
	Writes []WriteTarget `json:"writes,omitempty"`
	// CustomFields is what every task this schedule files says about
	// this deployment's declared fields (model.Schedule.CustomFields,
	// grain/task-105) -- the template's while TemplateID is set, kept in
	// sync exactly as the content fields around it are.
	CustomFields map[string]CustomFieldValue `json:"customFields,omitempty"`
	// NetworkAccess fixes the sandbox network for every firing; empty
	// defers to the deployment default.
	NetworkAccess string `json:"networkAccess,omitempty"`
	// NothingNeededAction is what a run ending on nothing_needed does to
	// every task this schedule files (model.Schedule.NothingNeededAction):
	// "ack", "close", or "" for close -- the template's while TemplateID
	// is set, kept in sync like the content fields around it.
	NothingNeededAction string `json:"nothingNeededAction,omitempty"`
	KeepRunning         bool   `json:"keepRunning,omitempty"`
	// Model is the model every firing asks for (model.Schedule.Model):
	// this schedule's own choice, which beats its template's. Empty for
	// a schedule that chose none.
	Model string `json:"model,omitempty"`
	// TemplateID and TemplateName are set when this schedule fires from
	// a Template (bwsalmon/agents#516) rather than its own inline
	// content -- Title/Description/AutoMerge/Reads/Writes/Capabilities/
	// CustomFields above
	// still reflect that template's content (kept in sync each time the
	// schedule fires, or is itself saved), so a UI that only wants to
	// render a summary needs no separate template lookup; TemplateName
	// is only for a UI that wants to say which template, e.g. to link
	// to it. Repo and Base are this schedule's own unless that template
	// is bound to a repo of its own, in which case they hold the
	// binding, which is what every firing will target
	// (model.Template.Target, docs/composition.md).
	TemplateID   string `json:"templateId,omitempty"`
	TemplateName string `json:"templateName,omitempty"`
	// System marks a schedule that fires one of grain's own standing
	// jobs (model.IsSystemTemplateID) -- the row grain seeds for itself
	// rather than one anybody here created
	// (orchestrator.reconcileSystemSchedules).
	//
	// It is on the wire so that a UI can *say so*, which is the half of
	// this that pkg/ui cannot do on its own: the content fields above
	// are a cache of a template that ships in grain's own code and is
	// re-resolved on every firing, and a pane that renders them as an
	// editable prompt invites the belief that editing them changes what
	// the job is told. It does not, here or in the state repository's
	// dump -- and refuseSystemScheduleContentEdit already answers 400 to
	// anybody who tries, which is a poor way to find out.
	System bool `json:"system,omitempty"`
	// SuiteID and SuiteName are set when this schedule runs a suite on
	// its cadence rather than filing a single task -- Title is the
	// suite's own name in that case (model.Schedule.SuiteID's own doc
	// comment on the display cache), and
	// Description/AutoMerge/Reads/Capabilities mean nothing, since a
	// suite run takes all of those from the suite and its templates.
	// Repo and Base still decide what the run targets, exactly as they
	// do for a task.
	SuiteID      string     `json:"suiteId,omitempty"`
	SuiteName    string     `json:"suiteName,omitempty"`
	Capabilities []string   `json:"capabilities"`
	Recurrence   Recurrence `json:"recurrence"`
	// TimeZone is this schedule's explicit IANA time zone (e.g. "America/New_York"),
	// or empty for universal default UTC.
	TimeZone string `json:"timeZone,omitempty"`
	// Overlap is what this schedule does when it comes due with its own
	// previous firing still unfinished -- "skip" (the default) or
	// "allow", model.OverlapPolicy's own two values, never empty on the
	// way out even for a schedule stored before the field existed, so a
	// UI can render the setting without knowing which of the two the
	// blank used to mean.
	Overlap string `json:"overlap"`
	// DeclaredIn is where this schedule came from when a repository
	// declares it rather than somebody here creating it --
	// ui.Template.DeclaredIn's own doc comment.
	DeclaredIn *Declaration `json:"declaredIn,omitempty"`
	// Source is where this schedule is declared, as the Processes page's
	// filter and GET /api/schedules?f=source:... name it
	// (pkg/schedulefilter): the declaring repository for one a
	// repository's .grain/schedules declares, "system" for one of
	// grain's own, and "deployment" for one somebody created here -- in
	// the UI, over the API, or in the state repository's tables, which
	// are the same rows.
	Source string `json:"source"`
	// Type is what a firing does: "template" (files a task from a
	// template), "task" (files the schedule's own inline task), "suite",
	// "workflow" (runs a grain CI workflow) or "system" (one of grain's
	// own standing jobs) -- schedulefilter.Types.
	Type      string     `json:"type"`
	Enabled   bool       `json:"enabled"`
	NextRunAt time.Time  `json:"nextRunAt"`
	LastRunAt *time.Time `json:"lastRunAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	// Suppressed is set on a schedule that has come due and is not
	// firing, because its previous firing has not finished and it is set
	// to skip rather than overlap -- nil on every schedule that is
	// firing normally, which is nearly all of them.
	//
	// It is what makes NextRunAt readable when it has gone by: a
	// suppressed firing leaves that field exactly where it was, so a
	// page that only drew it showed a next run in the past with nothing
	// saying why (model.ScheduleSuppression). Derived at read time
	// rather than stored, so it cannot outlive the thing it is about --
	// only ListSchedules fills it in (see its own doc comment).
	Suppressed *ScheduleSuppression `json:"suppressed,omitempty"`
	// LastWorkflowRun is the last firing of a schedule that runs a
	// workflow (model.ScheduledWorkflowRun): the grain CI run it started,
	// how that ended, and whether a review was filed. Nil on every other
	// schedule and on one that has never fired.
	//
	// It is how a green night is seen at all (grain/task-3160): a run
	// that passed files no task, so the schedule is where it shows.
	// Only ListSchedules fills it in, for Suppressed's reason.
	LastWorkflowRun *ScheduleWorkflowRun `json:"lastWorkflowRun,omitempty"`
	// WorkflowReview is when a schedule that runs a workflow files a
	// review of the run (model.WorkflowReviewPolicy): "on-failure" or
	// "always", and empty on every other schedule.
	WorkflowReview string `json:"workflowReview,omitempty"`
	// Skips is this schedule's skipped firings, newest first
	// (model.ScheduleSkip, grain/task-3560): every occurrence that came
	// due and filed nothing, why, and what became of it. Unlike
	// Suppressed it is a record rather than a reading of right now, so a
	// firing held for nine minutes overnight is still there in the
	// morning. Only ListSchedules fills it in, for Suppressed's reason.
	Skips []ScheduleSkip `json:"skips,omitempty"`
	// Revision identifies this schedule's editable content as it stands
	// (revision.go): an editor sends it back on PATCH, and is refused
	// with a 409 if somebody else has saved the schedule since.
	Revision string `json:"revision,omitempty"`
}

// ScheduleSkip is model.ScheduleSkip on the wire. Reason carries
// model.ScheduleSuppressionReason's values verbatim, as Suppressed's
// does; Summary is the server's own sentence for it ("skipped at
// <time>: #<task> from the previous firing is still open (<state>)").
type ScheduleSkip struct {
	Occurrence time.Time `json:"occurrence"`
	At         time.Time `json:"at"`
	Reason     string    `json:"reason"`
	Summary    string    `json:"summary"`
	TaskID     string    `json:"taskId,omitempty"`
	TaskState  string    `json:"taskState,omitempty"`
	// Outcome is "" while the occurrence is still held, "fired" once it
	// fired late (FiredTaskID, ResolvedAt) and "dropped" when the cadence
	// moved on without it.
	Outcome     string     `json:"outcome,omitempty"`
	ResolvedAt  *time.Time `json:"resolvedAt,omitempty"`
	FiredTaskID string     `json:"firedTaskId,omitempty"`
	// Folded is how many later occurrences the late firing absorbed.
	Folded int `json:"folded,omitempty"`
}

func scheduleSkipsFrom(skips []model.ScheduleSkip) []ScheduleSkip {
	var out []ScheduleSkip
	for _, k := range skips {
		out = append(out, ScheduleSkip{
			Occurrence: k.Occurrence, At: k.At, Reason: string(k.Reason), Summary: k.Summary(),
			TaskID: k.TaskID, TaskState: string(k.TaskState), Outcome: string(k.Outcome),
			ResolvedAt: k.ResolvedAt, FiredTaskID: k.FiredTaskID, Folded: k.Folded,
		})
	}
	return out
}

// ScheduleWorkflowRun is model.ScheduledWorkflowRun on the wire.
type ScheduleWorkflowRun struct {
	// RunID is the grain CI run, empty for a firing whose run could not
	// be started.
	RunID    string `json:"runId,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Workflow string `json:"workflow,omitempty"`
	// Conclusion is pkg/ci's word for how the run ended, empty while it
	// is still going; Detail is the same in words.
	Conclusion string     `json:"conclusion,omitempty"`
	Detail     string     `json:"detail,omitempty"`
	Unfinished bool       `json:"unfinished,omitempty"`
	FiredAt    time.Time  `json:"firedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	// ReviewTaskID is the review filed for this firing; ReviewNotNeeded
	// says none was, because the run passed.
	ReviewTaskID    string `json:"reviewTaskId,omitempty"`
	ReviewNotNeeded bool   `json:"reviewNotNeeded,omitempty"`
}

func scheduleWorkflowRunFrom(r model.ScheduledWorkflowRun) *ScheduleWorkflowRun {
	return &ScheduleWorkflowRun{
		RunID: r.RunID, Branch: r.Branch, Workflow: r.Workflow,
		Conclusion: r.Conclusion, Detail: r.Detail, Unfinished: r.Unfinished,
		FiredAt: r.FiredAt, FinishedAt: r.FinishedAt,
		ReviewTaskID: r.ReviewTaskID, ReviewNotNeeded: r.ReviewNotNeeded,
	}
}

// ScheduleSuppression is why a schedule that has come due is not firing
// -- model.ScheduleSuppression on the wire, with Reason carrying that
// package's own values verbatim ("task", "suiteRun", "workflowRun",
// "review") for the reason Recurrence.Kind does: one vocabulary for the
// fact, not a second one to translate.
type ScheduleSuppression struct {
	Reason string `json:"reason"`
	// TaskID is the open task holding the cadence, for the two reasons
	// that have one ("task" and "review") -- what somebody looking at a
	// schedule that has stopped firing has to go and close.
	TaskID string `json:"taskId,omitempty"`
	// Until is when grain stops waiting for that task and fires anyway,
	// which only the "review" reason is bounded by
	// (model.workflowReviewHold). Absent for the others: they wait on
	// something ending rather than on a clock.
	Until *time.Time `json:"until,omitempty"`
	// Detail is the server's own sentence for a reason that carries one
	// -- "standingWork"'s "skipped: this deployment doesn't run <repo>'s
	// standing work" (grain/task-3332).
	Detail string `json:"detail,omitempty"`
}

// suppressionFrom renders a derived suppression, or nil for a schedule
// that is firing normally.
func suppressionFrom(s *model.ScheduleSuppression) *ScheduleSuppression {
	if s == nil {
		return nil
	}
	out := &ScheduleSuppression{Reason: string(s.Reason), TaskID: s.TaskID, Detail: s.Detail}
	if !s.Until.IsZero() {
		until := s.Until
		out.Until = &until
	}
	return out
}

// scheduleFrom renders s -- templateName and suiteName are the
// schedule's own TemplateID/SuiteID resolved to a name by the caller
// (fetched once per list rather than once per row's own doc comment on
// why these are parameters rather than lookups this function does
// itself), empty when the id in question is nil or the caller has no
// name to offer.
func scheduleFrom(s model.Schedule, templateName, suiteName string) Schedule {
	// A schedule with no repo of its own reports none, rather than the
	// "/" the zero RepoRef spells. Only a schedule grain itself seeded
	// can be in that state -- CreateSchedule requires a repo of every
	// schedule a human writes -- and it is in it because what it fires
	// has no branch to push (model.SystemTemplate).
	repo := ""
	if s.Target != (model.RepoRef{}) {
		repo = s.Target.String()
	}
	out := Schedule{
		ID:                   s.ID,
		Title:                s.Title,
		Description:          s.Body,
		Repo:                 repo,
		Base:                 s.Base,
		AutoMerge:            s.AutoMerge,
		AutoApproveProposals: s.AutoApproveProposals,
		NetworkAccess:        string(s.NetworkAccess),
		NothingNeededAction:  string(s.NothingNeededAction),
		KeepRunning:          s.KeepRunning,
		Model:                s.Model,
		CustomFields:         customFieldValues(s.CustomFields),
		Capabilities:         []string{},
		Recurrence:           recurrenceFrom(s.Recurrence),
		TimeZone:             s.TimeZone,
		Overlap:              string(overlapOr(s.Overlap, model.OverlapSkip)),
		Enabled:              s.Enabled,
		NextRunAt:            s.NextRunAt,
		LastRunAt:            s.LastRunAt,
		CreatedAt:            s.CreatedAt,
	}
	if s.TemplateID != nil {
		out.TemplateID = *s.TemplateID
		out.TemplateName = templateName
		out.System = model.IsSystemTemplateID(*s.TemplateID)
	}
	if s.TemplateID != nil && strings.HasPrefix(*s.TemplateID, model.WorkflowReviewTemplatePrefix) {
		out.WorkflowReview = string(model.WorkflowReviewOnFailure)
		if s.WorkflowReview.Always() {
			out.WorkflowReview = string(model.WorkflowReviewAlways)
		}
	}
	if s.SuiteID != nil {
		out.SuiteID = *s.SuiteID
		out.SuiteName = suiteName
	}
	for _, r := range s.Reads {
		out.Reads = append(out.Reads, r.String())
	}
	for _, w := range s.Writes {
		out.Writes = append(out.Writes, WriteTarget{Repo: w.Repo.String(), Base: w.Base})
	}
	for _, g := range s.Grants {
		out.Capabilities = append(out.Capabilities, g.Capability)
	}
	out.Type = scheduleTypeOf(out)
	out.Source = scheduleSourceOf(out)
	out.Revision = scheduleRevision(out)
	return out
}

// scheduleTypeOf is Schedule.Type: what a firing of sc does.
func scheduleTypeOf(sc Schedule) string {
	switch {
	case sc.SuiteID != "":
		return schedulefilter.TypeSuite
	case sc.WorkflowReview != "":
		return schedulefilter.TypeWorkflow
	case sc.System:
		return schedulefilter.TypeSystem
	case sc.TemplateID != "":
		return schedulefilter.TypeTemplate
	default:
		return schedulefilter.TypeTask
	}
}

// scheduleSourceOf is Schedule.Source: where sc is declared. It reads
// DeclaredIn, so ListSchedules asks again once it has filled that in.
func scheduleSourceOf(sc Schedule) string {
	switch {
	case sc.DeclaredIn != nil && sc.DeclaredIn.Repo != "":
		return sc.DeclaredIn.Repo
	case sc.System:
		return schedulefilter.SourceSystem
	default:
		return schedulefilter.SourceDeployment
	}
}

// scheduleFacts is sc as pkg/schedulefilter reads it -- the one place the
// API's own shape is turned into the filter's words.
func scheduleFacts(sc Schedule) schedulefilter.Facts {
	f := schedulefilter.Facts{
		ID:          sc.ID,
		Name:        sc.Title,
		Kind:        schedulefilter.KindSchedule,
		Source:      sc.Source,
		Type:        sc.Type,
		Repo:        sc.Repo,
		Base:        sc.Base,
		Template:    firstNonEmpty(sc.TemplateName, sc.TemplateID),
		Suite:       firstNonEmpty(sc.SuiteName, sc.SuiteID),
		Enabled:     sc.Enabled,
		Cadence:     sc.Recurrence.Kind,
		TimeZone:    firstNonEmpty(sc.Recurrence.TimeZone, sc.TimeZone, "UTC"),
		AutoApprove: sc.AutoApproveProposals,
		AutoMerge:   sc.AutoMerge,
		Overlap:     sc.Overlap,
		Next:        sc.NextRunAt,
		Last:        sc.LastRunAt,
		Created:     sc.CreatedAt,
	}
	if f.Name == "" {
		f.Name = sc.ID
	}
	// processes.js's isWorkflowSchedule: the page's row kind for a
	// schedule is read off workflowReview, set only on workflow ones.
	if sc.WorkflowReview != "" {
		f.Kind = schedulefilter.KindWorkflow
	}
	if sc.DeclaredIn != nil {
		f.DeclaredPath = sc.DeclaredIn.Path
	}
	switch {
	case sc.LastWorkflowRun != nil && sc.LastWorkflowRun.Conclusion != "":
		f.LastOutcome = sc.LastWorkflowRun.Conclusion
	case sc.Suppressed != nil:
		f.LastOutcome = schedulefilter.OutcomeNotFiring
	case sc.LastRunAt != nil:
		f.LastOutcome = schedulefilter.OutcomeFired
	}
	return f
}

// ListSchedules returns every schedule, newest first.
//
// It is the one reader that fills in Schedule.Suppressed, because it is
// the one a schedules page is drawn from -- rows and the pane a row
// opens both come from this list. A create or update response describes
// the write that was just made, and a firing suppressed by the previous
// one is not about that write; the page re-reads this list after either,
// which is where the fact belongs anyway since it changes on its own
// (the task holding a schedule can be closed with nobody touching the
// schedule at all).
func (c *Client) ListSchedules(ctx context.Context) ([]Schedule, error) {
	list, err := c.Store.ListSchedules(ctx)
	if err != nil {
		return nil, err
	}
	// The wall clock the cadence is read against, once for the list:
	// only the review half of a workflow schedule's hold needs it, and
	// it is the one a firing would have used (scheduleLocation).
	loc, err := c.scheduleLocation(ctx)
	if err != nil {
		return nil, err
	}
	now := c.now()
	// Templates and suites are looked up once per distinct id rather than
	// once per schedule -- more than one schedule pointing at the same
	// template or suite (the whole point of TemplateID and SuiteID
	// existing) would otherwise repeat the same store call.
	templateNames, suiteNames := map[string]string{}, map[string]string{}
	declared, err := c.declaredIn(ctx)
	if err != nil {
		return nil, err
	}
	// Every workflow firing at once: one row per schedule that has ever
	// fired a workflow, which is a handful.
	firings, err := c.Store.ListScheduledWorkflowRuns(ctx)
	if err != nil {
		return nil, err
	}
	lastFiring := make(map[string]model.ScheduledWorkflowRun, len(firings))
	for _, f := range firings {
		lastFiring[f.ScheduleID] = f
	}
	skips, err := c.Store.AllScheduleSkips(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Schedule, 0, len(list))
	for _, s := range list {
		var templateName, suiteName string
		if s.TemplateID != nil {
			id := *s.TemplateID
			if cached, ok := templateNames[id]; ok {
				templateName = cached
			} else {
				templateName = c.templateNameFor(ctx, id)
				templateNames[id] = templateName
			}
		}
		if s.SuiteID != nil {
			id := *s.SuiteID
			if cached, ok := suiteNames[id]; ok {
				suiteName = cached
			} else if suite, err := c.Store.GetSuite(ctx, id); err == nil && suite != nil {
				suiteName = suite.Name
				suiteNames[id] = suiteName
			}
		}
		sched := scheduleFrom(s, templateName, suiteName)
		sched.DeclaredIn = declarationOf(declared, s.ID)
		sched.Source = scheduleSourceOf(sched)
		// Costs a store read for an enabled schedule with a repository
		// (whether that repository's standing work is off), and a read
		// or two more only for one that is already overdue and set to
		// skip (model.Store.SuppressedFiring's own conditions).
		suppressed, err := c.Store.SuppressedFiring(ctx, s, now, loc)
		if err != nil {
			return nil, err
		}
		sched.Suppressed = suppressionFrom(suppressed)
		if f, ok := lastFiring[s.ID]; ok {
			sched.LastWorkflowRun = scheduleWorkflowRunFrom(f)
		}
		sched.Skips = scheduleSkipsFrom(skips[s.ID])
		out = append(out, sched)
	}
	return out, nil
}

// CreateScheduleRequest is a new schedule's fields -- CreateTaskRequest's
// own shape, minus DependsOn and Approved: a schedule fires with no
// reviewer in the loop each time (see fireTaskSchedule's own doc comment
// on why it needs no approved flag), and a one-shot depends-on link makes
// no sense against a task this schedule refiles indefinitely, so those
// two task-only concerns have no home here. Recurrence is always
// required: a schedule with no cadence cannot ever fire.
//
// Repo and Base are this request's own fields, and required, for every
// schedule except one fired from a template bound to a repo of its own
// (model.Template.Target): a template is normally content with no
// target, so which repo and branch a schedule fires against is decided
// here, at the point of use, exactly as bwsalmon/agents#516 asks -- but
// a bound template has already answered that, and its answer wins over
// (and stands in for) anything given here.
//
// TemplateID, given, takes over
// Title/Description/AutoMerge/Capabilities/Reads/Writes/CustomFields
// entirely (bwsalmon/agents#516) -- CreateSchedule copies the named
// Template's own content in, rather than letting a caller mix a
// template with its own per-field overrides, a combination this API
// deliberately does not support (see CreateSchedule's own doc comment).
// Sending one of those fields anyway is *refused* rather than dropped
// on the floor (refuseIgnoredScheduleContent), unless it says exactly
// what the template already says, which asks for nothing. Left blank,
// Title is also required, exactly as it always was.
//
// SuiteID, given, makes this a schedule that runs a whole suite on its
// cadence rather than filing one task: the suite decides everything
// about what runs (its items, how many passes, approval, auto-merge),
// so Title/Description/AutoMerge/Capabilities/Reads/Writes/CustomFields
// are refused the same way a TemplateID makes them refused, and
// TemplateID itself may not be given alongside it. Base is required for
// a suite-backed schedule, matching CreateSuiteRunRequest's own rule: a
// suite run stacks its tasks against one named branch and has no
// default to fall back to.
type CreateScheduleRequest struct {
	Title                string   `json:"title"`
	Description          string   `json:"description"`
	Repo                 string   `json:"repo"`
	Base                 string   `json:"base"`
	AutoMerge            bool     `json:"autoMerge"`
	AutoApproveProposals bool     `json:"autoApproveProposals"`
	Capabilities         []string `json:"capabilities"`
	Reads                []string `json:"reads"`
	// Writes is the extra write targets every firing carries
	// (grain/task-86) -- content like Reads and Capabilities, refused
	// the same way they are when TemplateID or SuiteID is given, and
	// checked against this deployment's target repo list here rather
	// than at firing time (CreateTemplateRequest.Writes' own note on
	// why).
	Writes []WriteTarget `json:"writes"`
	// CustomFields is what every firing says about this deployment's
	// declared fields (grain/task-105) -- content like Reads and Writes,
	// checked against those declarations here rather than at firing time
	// (CreateTemplateRequest.CustomFields' own note on why), and refused
	// the same way the rest of the content is when TemplateID or SuiteID
	// is given.
	CustomFields map[string]CustomFieldValue `json:"customFields"`
	// NetworkAccess is one of model.NetworkAccesses, or empty to defer
	// to the deployment default.
	NetworkAccess string `json:"networkAccess"`
	// NothingNeededAction is "close" or "ack", or empty for close --
	// content, refused like NetworkAccess when TemplateID or SuiteID is
	// given.
	NothingNeededAction string `json:"nothingNeededAction"`
	KeepRunning         bool   `json:"keepRunning,omitempty"`
	KeepRunningSnake    *bool  `json:"keep_running,omitempty"`
	// Model is the model every firing asks for (model.Schedule.Model),
	// or empty to take the template's, and failing that the team's
	// default. Not content: it is this schedule's own, given alongside
	// a templateId as much as with inline content -- and refused with a
	// suiteId, whose runs file each template as it stands.
	Model      string     `json:"model"`
	TemplateID string     `json:"templateId"`
	SuiteID    string     `json:"suiteId"`
	Recurrence Recurrence `json:"recurrence"`
	// TimeZone is the IANA time zone (e.g. "America/New_York"), or empty for UTC.
	TimeZone string `json:"timeZone,omitempty"`
	// Overlap is "skip" or "allow" (model.OverlapPolicy). Omitted, it is
	// "skip": a schedule nobody has thought about overlapping does not
	// pile a second firing on top of one still running, which is what
	// every schedule did before this was a choice at all.
	Overlap string `json:"overlap"`
	// Enabled, omitted, defaults to true -- a schedule somebody just
	// created is normally meant to run, not to sit paused until a second
	// click.
	Enabled *bool `json:"enabled"`
}

// CreateSchedule files a new schedule straight into the store, due to
// fire for the first time at the first occurrence of its own cadence
// after now (scheduleLocation for the clock that is read against) --
// never immediately. Creating a schedule is a human saying when
// something should run, not a request to run it now: a "daily at 09:00"
// written at four in the afternoon that files a task there and then is
// a firing nobody asked for, and one that has to be closed by hand
// before the schedule's own 09:00 firing is even allowed to happen
// (fireTaskSchedule's unfinished-firing check). Somebody who does want
// it to run now can still file the task by hand, which is the one place
// "now" was ever meant to come from.
//
// Repo and Base always come from req, template-backed or not
// (CreateScheduleRequest's own doc comment on why); a template-backed
// schedule (req.TemplateID set) needs no Title of its own:
// scheduleContentFromTemplate below reads it off the template instead,
// the same field fireTaskSchedule itself re-reads from the template on
// every firing rather than trusting whatever this call snapshots.
//
// A suite-backed schedule (req.SuiteID set) needs no Title either, for
// the same reason one field further out: the suite's own name stands in
// as this schedule's display title, re-read by fireSuiteSchedule on every
// firing rather than trusted from here.
func (c *Client) CreateSchedule(ctx context.Context, req CreateScheduleRequest) (Schedule, error) {
	if err := c.checkSettingsWritable(ctx, "schedule"); err != nil {
		return Schedule{}, err
	}
	recurrence, err := parseRecurrence(req.Recurrence)
	if err != nil {
		return Schedule{}, err
	}
	overlap, err := parseOverlap(req.Overlap)
	if err != nil {
		return Schedule{}, err
	}
	// Trimmed the way a template's base is (UpdateTemplate) and a new
	// task's branch is (CreateTask): a branch pasted with a stray space
	// is the branch, and the composition preview beside the form
	// already showed it as one.
	req.Base = strings.TrimSpace(req.Base)
	if err := validateBase("base", req.Base); err != nil {
		return Schedule{}, err
	}
	// The requirements that belong to creating a schedule rather than to
	// describing one, asked before scheduleFromRequest resolves anything
	// -- see its own doc comment for why they are not in there.
	if suiteID := strings.TrimSpace(req.SuiteID); suiteID != "" {
		if strings.TrimSpace(req.Base) == "" {
			return Schedule{}, validationErrorf("base is required: a suite run has no default branch to fall back to")
		}
	} else if templateID := strings.TrimSpace(req.TemplateID); templateID != "" {
		// One of grain's own templates is not a template a human builds
		// a schedule out of: grain seeds the one schedule that fires
		// each (orchestrator.reconcileSystemSchedules), and a second
		// schedule firing the same standing job would run it twice.
		if model.IsSystemTemplateID(templateID) {
			return Schedule{}, validationErrorf(
				"%s is one of grain's own templates and cannot be scheduled by hand -- "+
					"grain seeds the schedule that fires it", templateID)
		}
	} else if strings.TrimSpace(req.Title) == "" {
		return Schedule{}, validationErrorf("title is required")
	}

	sched, templateName, suiteName, err := c.scheduleFromRequest(ctx, req)
	if err != nil {
		return Schedule{}, err
	}
	// Content sent alongside the template or the suite that decides all
	// of it, refused rather than dropped on the floor
	// (refuseIgnoredScheduleContent) -- checked against what
	// scheduleFromRequest resolved, which is exactly the content this
	// schedule will carry once it exists.
	//
	// Asked here rather than in scheduleFromRequest for the reason the
	// rest of CreateSchedule's own requirements are: a draft being
	// previewed answers the same combination with the composition the
	// template or suite decides, which is that content being ignored
	// made visible rather than a refusal somebody has to read.
	switch {
	case strings.TrimSpace(req.SuiteID) != "":
		// A title that is the suite's own name is the one content field
		// a caller can send here without asking for anything.
		if err := refuseIgnoredScheduleContent("suite", suiteName, sched, contentOfferedOnCreate(req)); err != nil {
			return Schedule{}, err
		}
	case strings.TrimSpace(req.TemplateID) != "":
		if err := refuseIgnoredScheduleContent("template", templateName, sched, contentOfferedOnCreate(req)); err != nil {
			return Schedule{}, err
		}
	}
	if sched.Target == (model.RepoRef{}) {
		return Schedule{}, validationErrorf("repo is required")
	}
	// Asked of the repo this schedule will actually fire into -- a bound
	// template's binding included, since that is what wins above -- for
	// the reason a task's own target is asked the same question: a
	// firing files its task straight into the store
	// (model.Store.fireTaskSchedule), approved by the schedule itself
	// and dispatchable on the spot, with no allowlist check of its own
	// anywhere on that path. A schedule is refused rather than parked
	// because there is no schedule yet to park (refuseOffAllowlist).
	if err := c.refuseOffRepoAllowlist(ctx, sched.Target, "a schedule cannot fire tasks into it"); err != nil {
		return Schedule{}, err
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	id, err := c.Store.NewScheduleID(ctx)
	if err != nil {
		return Schedule{}, err
	}
	tz := strings.TrimSpace(req.TimeZone)
	if tz == "" && req.Recurrence.TimeZone != "" {
		tz = strings.TrimSpace(req.Recurrence.TimeZone)
	}
	if tz != "" && !model.ValidTimeZone(tz) {
		return Schedule{}, validationErrorf("timeZone: invalid time zone %q", tz)
	}
	now := c.now()
	sched.ID = id
	sched.TimeZone = tz
	recurrence.TimeZone = tz
	sched.Recurrence = recurrence
	sched.Overlap = overlap
	sched.Enabled = enabled
	sched.NextRunAt = recurrence.Next(now, sched.Location())
	sched.CreatedAt = now
	if err := c.Store.PutSchedule(ctx, sched); err != nil {
		return Schedule{}, err
	}
	return scheduleFrom(sched, templateName, suiteName), nil
}

// scheduleFromRequest is the schedule req describes: the content a
// firing of it would carry -- its suite's, its template's, or the
// request's own inline fields -- and the repo and branch it would fire
// against, with a bound template's binding already applied. It writes
// nothing, and it takes no view on whether the schedule is one that may
// be created: no cadence, no id, no allowlist.
//
// That split is what lets a preview of an *unsaved* schedule be the same
// resolution the saved one gets (DraftScheduleComposition). The
// requirements that only make sense when something is actually being
// stored -- a cadence, a title, a repo, a branch for a suite run, not
// one of grain's own templates, no content alongside the template or
// suite that decides it (refuseIgnoredScheduleContent) -- stay in
// CreateSchedule, because a form halfway through being filled in fails
// every one of them and still has a composition worth showing. What is
// refused here is what cannot be resolved at all: a suite or template
// that is not there, a repo that does not parse, a custom field this
// deployment does not declare.
//
// The two names come back alongside because scheduleFrom needs them and
// only this function has looked the rows up.
func (c *Client) scheduleFromRequest(ctx context.Context, req CreateScheduleRequest) (model.Schedule, string, string, error) {
	suiteID, templateID := strings.TrimSpace(req.SuiteID), strings.TrimSpace(req.TemplateID)
	if suiteID != "" && templateID != "" {
		return model.Schedule{}, "", "", validationErrorf(
			"a schedule fires either a suite or a task, not both: give suiteId or templateId, not both")
	}
	// Not required outright the way it used to be: a bound template
	// (model.Template.Target) already names the repo every firing
	// targets, so a schedule fired from one needs no repo of its own.
	// Whether one is required at all is CreateSchedule's question, asked
	// of what this returns.
	var target model.RepoRef
	if strings.TrimSpace(req.Repo) != "" {
		parsed, err := c.parseRepo(req.Repo)
		if err != nil {
			return model.Schedule{}, "", "", err
		}
		target = parsed
	}

	var sched model.Schedule
	var firing *model.Template
	var templateName, suiteName string
	switch {
	case suiteID != "":
		suite, err := c.Store.GetSuite(ctx, suiteID)
		if err != nil {
			return model.Schedule{}, "", "", err
		}
		if suite == nil {
			return model.Schedule{}, "", "", validationErrorf("unknown suite %s", suiteID)
		}
		sched = model.Schedule{Title: suite.Name, SuiteID: &suite.ID}
		suiteName = suite.Name
	case templateID != "":
		tmpl, err := c.Store.GetTemplate(ctx, templateID)
		if err != nil {
			return model.Schedule{}, "", "", err
		}
		if tmpl == nil {
			return model.Schedule{}, "", "", validationErrorf("unknown template %s", templateID)
		}
		if err := model.ValidateUnattendedTemplate(tmpl.Fields, "a schedule"); err != nil {
			return model.Schedule{}, "", "", validationErrorf("%s", err)
		}
		sched = scheduleContentFromTemplate(*tmpl)
		firing, templateName = tmpl, tmpl.Name
	default:
		if err := validateNetworkAccess(req.NetworkAccess); err != nil {
			return model.Schedule{}, "", "", err
		}
		nothingNeeded, err := validateNothingNeededAction(req.NothingNeededAction)
		if err != nil {
			return model.Schedule{}, "", "", err
		}
		grants, err := c.grantsFor(req.Capabilities, model.GrantByLabel)
		if err != nil {
			return model.Schedule{}, "", "", err
		}
		reads, err := c.parseReads(req.Reads)
		if err != nil {
			return model.Schedule{}, "", "", err
		}
		writes, err := c.parseWrites(ctx, model.DefaultTeamID, req.Writes)
		if err != nil {
			return model.Schedule{}, "", "", err
		}
		customFields, err := c.normalizeCustomFields(ctx, req.CustomFields)
		if err != nil {
			return model.Schedule{}, "", "", err
		}
		sched = model.Schedule{
			Title:                req.Title,
			Body:                 req.Description,
			AutoMerge:            req.AutoMerge,
			AutoApproveProposals: req.AutoApproveProposals,
			NetworkAccess:        model.NetworkAccess(req.NetworkAccess),
			NothingNeededAction:  nothingNeeded,
			KeepRunning:          req.KeepRunning || (req.KeepRunningSnake != nil && *req.KeepRunningSnake),
			Reads:                reads,
			Writes:               writes,
			Grants:               grants,
			CustomFields:         customFields,
		}
	}
	chosenModel, err := c.scheduleModelChoice(ctx, req.Model, suiteID != "")
	if err != nil {
		return model.Schedule{}, "", "", err
	}
	sched.Model = chosenModel
	sched.Target = target
	sched.Base = req.Base
	if firing != nil {
		// A bound template's own repo and branch win over whatever this
		// request named, the same rule fireTaskSchedule applies on every
		// firing (model.Template.FiringTarget) -- applied here as well so
		// the schedule reads as what it will actually fire from the
		// moment it is created, not only after it first fires.
		sched.Target, sched.Base = firing.FiringTarget(target, req.Base)
	}
	return sched, templateName, suiteName, nil
}

// errSuiteScheduleModel refuses a model on a suite-backed schedule: a
// suite run files each of its templates as it stands, so there is no
// task of the schedule's own for the choice to apply to. A model belongs
// on the suite's templates instead.
var errSuiteScheduleModel = validationErrorf(
	"a schedule that runs a suite cannot choose a model: set it on the suite's templates instead")

// scheduleModelChoice is the model a schedule chose (model.Schedule.Model),
// checked the way a template's is (declaredModelChoice), and refused on a
// suite-backed one (errSuiteScheduleModel).
func (c *Client) scheduleModelChoice(ctx context.Context, chosen string, suiteBacked bool) (string, error) {
	m, err := c.declaredModelChoice(ctx, chosen)
	if err != nil {
		return "", err
	}
	if m != "" && suiteBacked {
		return "", errSuiteScheduleModel
	}
	return m, nil
}

// scheduleLocation is the fallback wall clock for schedules.
func (c *Client) scheduleLocation(ctx context.Context) (*time.Location, error) {
	return time.UTC, nil
}

// stateRepo is which repository this deployment keeps its own settings
// in (Config.StateRepoRef), or the zero RepoRef when this UI was not
// told -- read only to resolve one of grain's own templates
// (model.SystemTemplate), which is the one kind of template whose
// content depends on the deployment firing it.
func (c *Client) stateRepo() model.RepoRef {
	if c.Config.StateRepoRef == nil {
		return model.RepoRef{}
	}
	return c.Config.StateRepoRef()
}

// templateNameFor is the display name of the template id names --
// this build's own for a "system:" id, and the store's row for every
// other, exactly as model.Store.resolveTemplate resolves the same id at
// firing time. Empty when there is nothing behind the id: a template
// deleted out from under a schedule leaves the schedule readable rather
// than the whole list failing, which is what every caller here already
// did with a store error.
//
// The state repository is not passed: nothing but Reads depends on it,
// and this only ever wants a name.
func (c *Client) templateNameFor(ctx context.Context, id string) string {
	if model.IsSystemTemplateID(id) {
		if tmpl := model.SystemTemplate(id, model.RepoRef{}); tmpl != nil {
			return tmpl.Name
		}
		return ""
	}
	if t, err := c.Store.GetTemplate(ctx, id); err == nil && t != nil {
		return t.Name
	}
	return ""
}

// refuseSystemScheduleContentEdit refuses an edit to a schedule grain
// itself seeded (model.SystemTemplate, orchestrator's
// reconcileSystemSchedules) that would change *what* it fires, and
// allows the three things that are the deployment's to decide: its
// cadence, what it does when a firing overlaps the last one, and whether
// it is enabled at all.
//
// The split is the same one that puts a system template in code rather
// than in a row. What the memory agent is told decides what every later
// run of this deployment is told it has learned, so it is reviewed in a
// pull request against grain and shipped by upgrading grain -- not
// editable by whoever happens to be holding this deployment's UI, and
// not editable by an agent that has talked its way into it. When it
// runs, how often, and whether at all are questions only this deployment
// can answer, and it answers them here.
//
// An edit is refused rather than ignored. A form that silently drops
// half of what somebody typed is worse than one that says no: the row
// would go on saying what it said, and the person would go on believing
// they had changed it.
//
// What is refused is a field that would actually *change* something,
// not a field that was sent. Every save from the schedule overlay
// submits the whole schedule -- the template it fires, its repo and its
// base go up on every PATCH, cadence edit or not -- so refusing on
// presence alone would leave a deployment unable to reach the three
// things this very function exists to leave it. It is the same
// distinction UpdateSchedule already makes one field along, where a
// resent recurrence identical to the stored one is not a cadence edit
// and must not push the next firing out.
func refuseSystemScheduleContentEdit(sched model.Schedule, req UpdateScheduleRequest) error {
	if sched.TemplateID == nil || !model.IsSystemTemplateID(*sched.TemplateID) {
		return nil
	}
	// What this schedule's repo reads as on the wire, which is what a
	// form that rendered it sends back unchanged: empty for the zero
	// RepoRef a system schedule carries (scheduleFrom's own note).
	repo := ""
	if sched.Target != (model.RepoRef{}) {
		repo = sched.Target.String()
	}
	changes := func(given *string, current string) bool { return given != nil && *given != current }
	refused := ""
	switch {
	case changes(req.Title, sched.Title), changes(req.Description, sched.Body):
		refused = "what it says"
	case changes(req.NetworkAccess, string(sched.NetworkAccess)):
		refused = "what network access it grants"
	case changes(req.NothingNeededAction, string(sched.NothingNeededAction)):
		refused = "what a nothing_needed ending does"
	// The list-valued fields are refused on presence: the overlay never
	// sends one for a schedule that fires a template, so anything that
	// does is asking for a change rather than resending a form.
	case req.Capabilities != nil:
		refused = "which capabilities it holds"
	case req.Reads != nil, req.Writes != nil:
		refused = "which repositories it reads or writes"
	case req.CustomFields != nil:
		refused = "the custom fields it carries"
	case changes(req.Repo, repo), changes(req.Base, sched.Base):
		refused = "which repository it targets"
	case req.AutoMerge != nil && *req.AutoMerge != sched.AutoMerge:
		refused = "whether its work merges on its own"
	case changes(req.TemplateID, *sched.TemplateID), req.SuiteID != nil:
		refused = "what it fires"
	}
	if refused == "" {
		return nil
	}
	return validationErrorf(
		"%s is one of grain's own scheduled jobs, so %s is not this deployment's to change -- "+
			"it ships with grain and changes when grain is upgraded. "+
			"You can change how often it runs, what it does when a run overlaps the last one, "+
			"and whether it is enabled at all", sched.ID, refused)
}

// scheduleContentOffered is the content a create or an update request
// carries, in the one shape both can be checked in: a nil field is one
// the request did not ask anything about.
//
// CreateScheduleRequest's content fields are values rather than
// pointers, so "not given" there can only mean "left at its zero value"
// -- a caller who spells out `"autoMerge": false` alongside a templateId
// is indistinguishable from one who says nothing, and nothing turns on
// the difference, since neither is asking for a content of their own.
type scheduleContentOffered struct {
	Title         *string
	Description   *string
	AutoMerge     *bool
	Reads         *[]string
	Writes        *[]WriteTarget
	Capabilities  *[]string
	CustomFields  *map[string]CustomFieldValue
	NetworkAccess *string

	NothingNeededAction *string
}

// contentOfferedOnCreate is req's content fields, in the shape
// refuseIgnoredScheduleContent checks -- everything left at its zero
// value read as "not given" (scheduleContentOffered's own note).
func contentOfferedOnCreate(req CreateScheduleRequest) scheduleContentOffered {
	var out scheduleContentOffered
	if strings.TrimSpace(req.Title) != "" {
		out.Title = &req.Title
	}
	if strings.TrimSpace(req.Description) != "" {
		out.Description = &req.Description
	}
	if req.AutoMerge {
		out.AutoMerge = &req.AutoMerge
	}
	if len(req.Reads) > 0 {
		out.Reads = &req.Reads
	}
	if len(req.Writes) > 0 {
		out.Writes = &req.Writes
	}
	if len(req.Capabilities) > 0 {
		out.Capabilities = &req.Capabilities
	}
	if len(req.CustomFields) > 0 {
		out.CustomFields = &req.CustomFields
	}
	if strings.TrimSpace(req.NetworkAccess) != "" {
		out.NetworkAccess = &req.NetworkAccess
	}
	if strings.TrimSpace(req.NothingNeededAction) != "" {
		out.NothingNeededAction = &req.NothingNeededAction
	}
	return out
}

// contentOfferedOnUpdate is req's content fields, which already carry
// "not given" as a nil pointer of their own.
func contentOfferedOnUpdate(req UpdateScheduleRequest) scheduleContentOffered {
	return scheduleContentOffered{
		Title:         req.Title,
		Description:   req.Description,
		AutoMerge:     req.AutoMerge,
		Reads:         req.Reads,
		Writes:        req.Writes,
		Capabilities:  req.Capabilities,
		CustomFields:  req.CustomFields,
		NetworkAccess: req.NetworkAccess,

		NothingNeededAction: req.NothingNeededAction,
	}
}

// refuseIgnoredScheduleContent refuses a request that sends content
// alongside a templateId or a suiteId, which decide all of it: a
// template-backed schedule's title, body, auto-merge, reads, writes,
// capabilities and custom fields are the template's, re-read from it on
// every firing, and a suite-backed one's are the suite's and those of
// the templates it runs. There are no per-field overrides in this API
// (CreateScheduleRequest's own doc comment on why), so a content field
// on such a request is one nothing will ever read.
//
// It is refused rather than dropped for refuseSystemScheduleContentEdit's
// reason, one layer out: a form that silently swallows half of what
// somebody typed is worse than one that says no, because the row goes on
// saying what it said and the person goes on believing they changed it.
// docs/composition.md made the combination legible after the fact;
// this is the part that stops it happening.
//
// effective is the content that will actually be in force once this
// request lands -- the template being attached or already attached, or
// a suite-backed schedule's own display cache -- and what is refused is
// a field that differs from it, not a field that was sent. That is the
// same distinction refuseSystemScheduleContentEdit draws and for the
// same reason: a form that submits a whole schedule resends fields it
// only read, and a resend that asks for nothing must not be an error.
// ScheduleOverlay.jsx sends no content at all while a template or a
// suite is chosen, so nothing this deployment saves reaches here with
// anything to refuse.
//
// kind is "template" or "suite" and name is that template's or suite's
// own name, which is the whole of what the message needs to say where
// the fields belong instead.
func refuseIgnoredScheduleContent(kind, name string, effective model.Schedule, given scheduleContentOffered) error {
	// Rendered rather than compared field by field against the model:
	// what a caller sends is the wire shape, so the wire shape is what
	// "the same as what is already there" has to mean (a repo spelled
	// as "owner/name", a capability by its label).
	current := scheduleFrom(effective, "", "")
	var refused []string
	if given.Title != nil && strings.TrimSpace(*given.Title) != current.Title {
		refused = append(refused, "title")
	}
	if given.Description != nil && *given.Description != current.Description {
		refused = append(refused, "description")
	}
	if given.AutoMerge != nil && *given.AutoMerge != current.AutoMerge {
		refused = append(refused, "autoMerge")
	}
	if given.Reads != nil && !sameStrings(*given.Reads, current.Reads) {
		refused = append(refused, "reads")
	}
	if given.Writes != nil && !sameWrites(*given.Writes, current.Writes) {
		refused = append(refused, "writes")
	}
	if given.Capabilities != nil && !sameStrings(*given.Capabilities, current.Capabilities) {
		refused = append(refused, "capabilities")
	}
	if given.CustomFields != nil && !sameCustomFields(*given.CustomFields, current.CustomFields) {
		refused = append(refused, "customFields")
	}
	if given.NetworkAccess != nil && strings.TrimSpace(*given.NetworkAccess) != current.NetworkAccess {
		refused = append(refused, "networkAccess")
	}
	if given.NothingNeededAction != nil && strings.TrimSpace(*given.NothingNeededAction) != current.NothingNeededAction {
		refused = append(refused, "nothingNeededAction")
	}
	if len(refused) == 0 {
		return nil
	}
	verb, pronoun := "come", "them"
	if len(refused) == 1 {
		verb, pronoun = "comes", "it"
	}
	switch kind {
	case "suite":
		return validationErrorf(
			"%s %s from the suite this schedule runs (%q) and the templates in it, not from the schedule -- "+
				"set %s there; a schedule with no suiteId carries content of its own instead",
			joinAnd(refused), verb, name, pronoun)
	default:
		return validationErrorf(
			"%s %s from the template this schedule fires (%q), not from the schedule -- "+
				"set %s on the template; a schedule with no templateId carries content of its own instead",
			joinAnd(refused), verb, name, pronoun)
	}
}

// joinAnd is "a", "a and b", "a, b and c" -- these field names are read
// in a sentence rather than scanned as a list (model.joinClauses, which
// is the same helper one package over).
func joinAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// sameStrings compares a list a caller sent against the one this package
// would have rendered back, order and all: these are lists somebody
// picked in a form, and a form that read them back sends them in the
// order it read them. Empty and absent are the same thing, since a
// schedule with no reads renders as no list at all.
func sameStrings(given, current []string) bool {
	if len(given) != len(current) {
		return false
	}
	for i := range given {
		if strings.TrimSpace(given[i]) != current[i] {
			return false
		}
	}
	return true
}

func sameWrites(given, current []WriteTarget) bool {
	if len(given) != len(current) {
		return false
	}
	for i := range given {
		if strings.TrimSpace(given[i].Repo) != current[i].Repo ||
			strings.TrimSpace(given[i].Base) != current[i].Base {
			return false
		}
	}
	return true
}

func sameCustomFields(given, current map[string]CustomFieldValue) bool {
	if len(given) != len(current) {
		return false
	}
	for name, v := range given {
		held, ok := current[name]
		if !ok || !sameStrings(v, held) {
			return false
		}
	}
	return true
}

// scheduleContentFromTemplate is the content half of a Schedule that a
// template actually carries (everything but identity, recurrence,
// timing and Target/Base) copied from t -- CreateSchedule's and
// UpdateSchedule's shared way of attaching a schedule to a template,
// mirroring what fireTaskSchedule itself re-reads from the template
// each time a template-backed schedule fires, so a schedule's own
// inline copy never drifts from the template it names for longer than
// one firing. Callers still set Target and Base themselves, through
// Template.FiringTarget: a template usually has no target of its own,
// and a bound one is the one case where those two are content too.
func scheduleContentFromTemplate(t model.Template) model.Schedule {
	title, body := t.FilledTitleBody()
	return model.Schedule{
		Title:         title,
		Body:          body,
		AutoMerge:     t.AutoMerge,
		Reads:         t.Reads,
		Writes:        t.Writes,
		Grants:        t.Grants,
		CustomFields:  t.CustomFields,
		NetworkAccess: t.NetworkAccess,
		TemplateID:    &t.ID,

		NothingNeededAction: t.NothingNeededAction,
		KeepRunning:         t.KeepRunning,
	}
}

// UpdateScheduleRequest is a schedule's editable fields -- nil means
// "leave this one alone", UpdateTaskRequest's own convention. Enabled is
// the pause/resume toggle: setting it false stops this schedule from
// ever coming due without losing its NextRunAt, so resuming it later
// picks up rather than firing every occurrence that elapsed while paused.
//
// Repo and Base follow the plain nil-means-leave-alone rule, and either
// can still be edited on the same request that attaches or detaches a
// template -- attaching an ordinary, unbound template never touches this
// schedule's own Target/Base. Attaching one bound to a repo of its own
// (model.Template.Target) does: a binding is what that schedule fires
// against from then on, so it overwrites Repo and Base here rather than
// leaving the row saying one thing and every firing doing another.
//
// TemplateID follows the same nil-means-leave-alone rule, with two
// non-nil cases of its own (bwsalmon/agents#516): a non-empty id
// attaches this schedule to that template, overwriting Title/
// Description/AutoMerge/Reads/Writes/Capabilities/CustomFields from it
// and refusing those same seven fields on this request, exactly as
// CreateScheduleRequest's own doc comment describes; an empty string
// detaches it, leaving the schedule's current content (most recently
// synced from whatever template it pointed at) in place as a
// now-independent copy, editable through those same seven fields from
// then on -- which is why a detach is the one update that may name a
// templateId and carry content in the same breath.
//
// SuiteID repoints a suite-backed schedule at a different suite, and is
// the one field here that cannot change what *kind* of schedule this is
// (model.Schedule.SuiteID's own doc comment on why): given on a
// schedule that fires a task, or given as "" on one that runs a suite,
// it is refused rather than quietly converting a schedule into
// something with no content to fire. Everything a suite-backed schedule
// still decides for itself -- Repo, Base, Recurrence, Overlap, Enabled -- is
// editable here exactly as it is for any other schedule;
// Title/Description/AutoMerge/Reads/Writes/Capabilities/CustomFields
// are refused, the same as they are while a TemplateID is set
// (refuseIgnoredScheduleContent).
type UpdateScheduleRequest struct {
	Title                *string        `json:"title,omitempty"`
	Description          *string        `json:"description,omitempty"`
	Repo                 *string        `json:"repo,omitempty"`
	Base                 *string        `json:"base,omitempty"`
	AutoMerge            *bool          `json:"autoMerge,omitempty"`
	AutoApproveProposals *bool          `json:"autoApproveProposals,omitempty"`
	Capabilities         *[]string      `json:"capabilities,omitempty"`
	Reads                *[]string      `json:"reads,omitempty"`
	Writes               *[]WriteTarget `json:"writes,omitempty"`
	// CustomFields, given, replaces this schedule's values for every
	// declared field -- UpdateTaskRequest.CustomFields' own contract,
	// including its one exception: a value for a field since deleted
	// from the state repository is left alone rather than dropped by an
	// unrelated edit. Content, so it is refused while a template or a
	// suite decides what this schedule files, exactly as
	// Title/Description/Reads/Writes are.
	CustomFields  *map[string]CustomFieldValue `json:"customFields,omitempty"`
	NetworkAccess *string                      `json:"networkAccess,omitempty"`
	// NothingNeededAction edits CreateScheduleRequest.NothingNeededAction's
	// setting; an empty string sets it back to close. Content, refused
	// while a template or a suite decides it.
	NothingNeededAction *string `json:"nothingNeededAction,omitempty"`
	KeepRunning         *bool   `json:"keepRunning,omitempty"`
	KeepRunningSnake    *bool   `json:"keep_running,omitempty"`
	// Model edits CreateScheduleRequest.Model; an empty string clears
	// the schedule's own choice. Allowed while a template is attached --
	// it is not content -- and on one of grain's own schedules, whose
	// model is the deployment's to choose like its cadence.
	Model      *string     `json:"model,omitempty"`
	TemplateID *string     `json:"templateId,omitempty"`
	SuiteID    *string     `json:"suiteId,omitempty"`
	Recurrence *Recurrence `json:"recurrence,omitempty"`
	// TimeZone is the IANA time zone (e.g. "America/New_York"), or empty for UTC.
	TimeZone *string `json:"timeZone,omitempty"`
	// Overlap follows the same nil-means-leave-alone rule: "skip" or
	// "allow" (model.OverlapPolicy), and nothing else -- an empty string
	// is refused rather than read as "back to the default", since a
	// caller that meant the default can say "skip" and one that sent a
	// blank by accident should hear about it.
	Overlap *string `json:"overlap,omitempty"`
	Enabled *bool   `json:"enabled,omitempty"`
	// Revision, given, is the schedule's Revision as the caller last read
	// it, and the edit is refused (staleEditError) if the schedule has been
	// saved since. Absent applies the edit regardless.
	Revision *string `json:"revision,omitempty"`
}

// UpdateSchedule edits a schedule's fields in place -- LastRunAt is
// left exactly as it is, and so is NextRunAt for every edit but one: a
// recurrence actually changed retimes it to the first occurrence of the
// new cadence (CreateSchedule's own rule, applied again to a cadence
// written a second time). Nothing else would make a cadence edit
// visible before the firing that is already scheduled: NextRunAt is an
// instant the old cadence chose, and advanceSchedule only ever walks
// forward from it, so a schedule moved from monthly to daily would
// otherwise keep its next firing a month out and only then start
// running daily. Pausing and resuming still keep their firing time
// (Enabled is not a cadence), which is what makes a pause a pause
// rather than a reset.
func (c *Client) UpdateSchedule(ctx context.Context, id string, req UpdateScheduleRequest) (Schedule, error) {
	if err := c.checkSettingsWritable(ctx, "schedule"); err != nil {
		return Schedule{}, err
	}
	if req.Base != nil {
		trimmed := strings.TrimSpace(*req.Base) // CreateSchedule's reason
		req.Base = &trimmed
	}
	existing, err := c.Store.GetSchedule(ctx, id)
	if err != nil {
		return Schedule{}, err
	}
	if existing == nil {
		return Schedule{}, &scheduleNotFoundError{ID: id}
	}
	// Asked first, and again inside the write below where it is decided:
	// a stale form measured against content somebody has since changed
	// would otherwise hear about that content, not about being stale.
	if err := checkRevision("schedule", id, req.Revision, scheduleRevisionOf(*existing)); err != nil {
		return Schedule{}, err
	}
	// Checked before anything else is parsed: a request that may not
	// change what this schedule fires should hear that, not a complaint
	// about the repo it also named.
	if err := c.refuseDeclaredEdit(ctx, id, "edit"); err != nil {
		return Schedule{}, err
	}
	if err := refuseSystemScheduleContentEdit(*existing, req); err != nil {
		return Schedule{}, err
	}
	if req.Base != nil {
		if err := validateBase("base", *req.Base); err != nil {
			return Schedule{}, err
		}
	}
	// One of grain's own scheduled jobs. Everything below that is about
	// content has already been refused; what is left is the handful of
	// places that assume a schedule has a repo, which this one does not
	// (model.SystemTemplate -- what it produces is memory, not a branch).
	systemBacked := existing.TemplateID != nil && model.IsSystemTemplateID(*existing.TemplateID)
	suiteBacked := existing.SuiteID != nil

	// repointing is non-nil only when this suite-backed schedule is
	// being moved to a different suite -- the one thing req.SuiteID can
	// do, since what a schedule fires is fixed when it is created
	// (UpdateScheduleRequest's own doc comment on why).
	var repointing *model.Suite
	if req.SuiteID != nil {
		wanted := strings.TrimSpace(*req.SuiteID)
		switch {
		case !suiteBacked && wanted != "":
			return Schedule{}, validationErrorf(
				"this schedule files a task, not a suite: create a suite-backed schedule instead")
		case suiteBacked && wanted == "":
			return Schedule{}, validationErrorf(
				"a suite-backed schedule cannot be detached from its suite: delete it, or point it at another suite")
		case suiteBacked:
			suite, err := c.Store.GetSuite(ctx, wanted)
			if err != nil {
				return Schedule{}, err
			}
			if suite == nil {
				return Schedule{}, validationErrorf("unknown suite %s", wanted)
			}
			repointing = suite
		}
	}
	if suiteBacked {
		if req.TemplateID != nil && strings.TrimSpace(*req.TemplateID) != "" {
			return Schedule{}, validationErrorf(
				"this schedule runs a suite, so it fires no template of its own")
		}
		if req.Base != nil && strings.TrimSpace(*req.Base) == "" {
			return Schedule{}, validationErrorf("base is required: a suite run has no default branch to fall back to")
		}
	}

	// attaching is non-nil only when req.TemplateID names a template to
	// attach to (the non-empty case) -- content below is populated from
	// it, and any content field on req that disagrees with it is
	// refused, the same "template wins outright" rule CreateSchedule
	// applies.
	var attaching *model.Template
	wanted := ""
	if req.TemplateID != nil {
		wanted = strings.TrimSpace(*req.TemplateID)
	}
	if wanted != "" {
		switch {
		case model.IsSystemTemplateID(wanted) && existing.TemplateID != nil && *existing.TemplateID == wanted:
			// The overlay resending the template this schedule already
			// fires, which every save from it does. Not an edit, and
			// there is nothing to attach: a system template is resolved
			// from this build at each firing rather than copied onto the
			// row (model.Store.resolveTemplate).
		case model.IsSystemTemplateID(wanted):
			return Schedule{}, validationErrorf(
				"%s is one of grain's own templates and cannot be attached to a schedule by hand -- "+
					"grain seeds the schedule that fires it", wanted)
		default:
			tmpl, err := c.Store.GetTemplate(ctx, wanted)
			if err != nil {
				return Schedule{}, err
			}
			if tmpl == nil {
				return Schedule{}, validationErrorf("unknown template %s", wanted)
			}
			// Asked only of a template this request newly attaches: one
			// this schedule already fires and has since gained such a
			// field is UpdateTemplate's to refuse, and a schedule left
			// over from before that rule can still be paused or edited.
			if existing.TemplateID == nil || *existing.TemplateID != wanted {
				if err := model.ValidateUnattendedTemplate(tmpl.Fields, "a schedule"); err != nil {
					return Schedule{}, validationErrorf("%s", err)
				}
			}
			attaching = tmpl
		}
	}

	// firing is the template this schedule will fire from once this
	// request lands -- the one being attached, or the one it already
	// carries and is not detaching from. Only its binding is read below;
	// content still comes from attaching alone, since a schedule already
	// pointing at a template holds its own synced copy of that.
	firing := attaching
	if firing == nil && existing.TemplateID != nil && !systemBacked &&
		!(req.TemplateID != nil && strings.TrimSpace(*req.TemplateID) == "") {
		tmpl, err := c.Store.GetTemplate(ctx, *existing.TemplateID)
		if err != nil {
			return Schedule{}, err
		}
		firing = tmpl
	}

	// Content sent alongside a template or a suite that will decide it
	// anyway, refused rather than dropped (refuseIgnoredScheduleContent).
	// One of grain's own scheduled jobs is not checked twice:
	// refuseSystemScheduleContentEdit above has already refused every
	// content field on such a request, with a message about grain's own
	// upgrades that says more than this one could.
	if !systemBacked {
		switch {
		case suiteBacked:
			// The suite's name, from the row's own display cache
			// (model.Schedule.Title) unless this request is repointing
			// the schedule at another suite, in which case that one's.
			// Nothing but the title is cached for a suite-backed
			// schedule, which is exactly the content a suite decides.
			name, effective := existing.Title, model.Schedule{Title: existing.Title}
			if repointing != nil {
				name, effective = repointing.Name, model.Schedule{Title: repointing.Name}
			}
			if err := refuseIgnoredScheduleContent("suite", name, effective, contentOfferedOnUpdate(req)); err != nil {
				return Schedule{}, err
			}
		case attaching != nil:
			// Attaching: the content in force after this request is the
			// template's, not the inline content still on the row, so
			// that is what a content field is measured against.
			if err := refuseIgnoredScheduleContent(
				"template", attaching.Name, scheduleContentFromTemplate(*attaching),
				contentOfferedOnUpdate(req)); err != nil {
				return Schedule{}, err
			}
		case firing != nil:
			// Already template-backed and staying that way: the row is
			// its own synced copy of that template's content, so it
			// stands in for the template here (and still does when the
			// template row has since been deleted, where firing is nil
			// and this schedule is left editable rather than frozen).
			if err := refuseIgnoredScheduleContent(
				"template", firing.Name, *existing, contentOfferedOnUpdate(req)); err != nil {
				return Schedule{}, err
			}
		}
	}

	// A blank repo is refused, except where the template this schedule
	// fires is bound to a repo of its own: the binding names the target
	// (FiringTarget, below) and the schedule editor hides its repo box and
	// sends "" for it, which CreateSchedule already reads as "none of my
	// own". Refusing it here made every save of such a schedule fail --
	// a cadence change included.
	boundFiring := firing != nil && firing.Target != nil
	var target *model.RepoRef
	if req.Repo != nil && !systemBacked && !(boundFiring && strings.TrimSpace(*req.Repo) == "") {
		if strings.TrimSpace(*req.Repo) == "" {
			return Schedule{}, validationErrorf("repo cannot be empty: a schedule with no target cannot fire")
		}
		parsed, err := c.parseRepo(*req.Repo)
		if err != nil {
			return Schedule{}, err
		}
		target = &parsed
	}
	var grants []model.Grant
	var reads []model.RepoRef
	var writes []model.WriteTarget
	var nothingNeeded model.NothingNeededAction
	if attaching == nil && !suiteBacked {
		if req.Title != nil && strings.TrimSpace(*req.Title) == "" {
			return Schedule{}, validationErrorf("title cannot be empty")
		}
		if req.NetworkAccess != nil {
			if err := validateNetworkAccess(*req.NetworkAccess); err != nil {
				return Schedule{}, err
			}
		}
		if req.NothingNeededAction != nil {
			var err error
			if nothingNeeded, err = validateNothingNeededAction(*req.NothingNeededAction); err != nil {
				return Schedule{}, err
			}
		}
		if req.Capabilities != nil {
			var err error
			grants, err = c.grantsFor(*req.Capabilities, model.GrantByLabel)
			if err != nil {
				return Schedule{}, err
			}
		}
		if req.Reads != nil {
			var err error
			reads, err = c.parseReads(*req.Reads)
			if err != nil {
				return Schedule{}, err
			}
		}
		if req.Writes != nil {
			var err error
			writes, err = c.parseWrites(ctx, model.DefaultTeamID, *req.Writes)
			if err != nil {
				return Schedule{}, err
			}
		}
	}
	var recurrence *model.Recurrence
	if req.Recurrence != nil {
		r, err := parseRecurrence(*req.Recurrence)
		if err != nil {
			return Schedule{}, err
		}
		recurrence = &r
	}
	var timeZone *string
	if req.TimeZone != nil {
		tz := strings.TrimSpace(*req.TimeZone)
		if tz != "" && !model.ValidTimeZone(tz) {
			return Schedule{}, validationErrorf("timeZone: invalid time zone %q", tz)
		}
		timeZone = &tz
	}
	var overlap *model.OverlapPolicy
	if req.Overlap != nil {
		if strings.TrimSpace(*req.Overlap) == "" {
			return Schedule{}, validationErrorf("overlap cannot be empty: give %q or %q",
				model.OverlapSkip, model.OverlapAllow)
		}
		p, err := parseOverlap(*req.Overlap)
		if err != nil {
			return Schedule{}, err
		}
		overlap = &p
	}
	var chosenModel *string
	if req.Model != nil {
		m, err := c.declaredModelChoice(ctx, *req.Model)
		if err != nil {
			return Schedule{}, err
		}
		chosenModel = &m
	}
	now := c.now()

	if err := c.Store.UpdateSchedule(ctx, id, func(s *model.Schedule) error {
		if err := checkRevision("schedule", id, req.Revision, scheduleRevisionOf(*s)); err != nil {
			return err
		}
		if chosenModel != nil {
			if *chosenModel != "" && (s.SuiteID != nil || repointing != nil) {
				return errSuiteScheduleModel
			}
			s.Model = *chosenModel
		}
		if s.SuiteID != nil {
			// A suite-backed schedule carries no content of its own to
			// edit -- the suite does. Only which suite it runs can change
			// here, and only to another suite.
			if repointing != nil {
				s.SuiteID, s.Title = &repointing.ID, repointing.Name
			}
		} else if attaching != nil {
			content := scheduleContentFromTemplate(*attaching)
			s.Title, s.Body, s.AutoMerge, s.Reads, s.Writes, s.Grants, s.CustomFields,
				s.NetworkAccess, s.NothingNeededAction, s.KeepRunning, s.TemplateID =
				content.Title, content.Body, content.AutoMerge, content.Reads, content.Writes,
				content.Grants, content.CustomFields, content.NetworkAccess, content.NothingNeededAction, content.KeepRunning, content.TemplateID
		} else {
			// req.TemplateID != nil here means the empty-string case:
			// detach, keeping whatever content is already on the row and
			// still applying any of the individual field edits below in
			// the same request.
			if req.TemplateID != nil {
				s.TemplateID = nil
			}
			if req.Title != nil {
				s.Title = *req.Title
			}
			if req.Description != nil {
				s.Body = *req.Description
			}
			if req.AutoMerge != nil {
				s.AutoMerge = *req.AutoMerge
			}
			if req.AutoApproveProposals != nil {
				s.AutoApproveProposals = *req.AutoApproveProposals
			}
			if req.Capabilities != nil {
				s.Grants = grants
			}
			if req.Reads != nil {
				s.Reads = reads
			}
			if req.Writes != nil {
				s.Writes = writes
			}
			if req.CustomFields != nil {
				declaredFields, err := c.Store.ListCustomFields(ctx)
				if err != nil {
					return err
				}
				customFields, err := model.NormalizeCustomFieldValuesFrom(declaredFields,
					customFieldModelValues(*req.CustomFields), s.CustomFields)
				if err != nil {
					return validationErrorf("%s", err)
				}
				s.CustomFields = mergeCustomFields(s.CustomFields, *req.CustomFields, customFields, declaredFields)
			}
			if req.NetworkAccess != nil {
				s.NetworkAccess = model.NetworkAccess(*req.NetworkAccess)
			}
			if req.NothingNeededAction != nil {
				s.NothingNeededAction = nothingNeeded
			}
			if req.KeepRunning != nil {
				s.KeepRunning = *req.KeepRunning
			} else if req.KeepRunningSnake != nil {
				s.KeepRunning = *req.KeepRunningSnake
			}
		}
		// Target and Base are applied on their own, template attached or
		// not (UpdateScheduleRequest's own doc comment on why) -- unless
		// the template this schedule fires from is bound to a repo of
		// its own, whose binding then wins over both what is on the row
		// and what this request asked for, the same rule
		// fireTaskSchedule applies on every firing.
		beforeTarget := s.Target
		if target != nil {
			s.Target = *target
		}
		if req.Base != nil {
			s.Base = *req.Base
		}
		if firing != nil {
			s.Target, s.Base = firing.FiringTarget(s.Target, s.Base)
		}
		// Moving a schedule onto a repo off Config.TargetRepos is refused,
		// as filing one against it is (CreateSchedule) -- whether the move
		// came from this request's own repo field or from the binding of a
		// template it is attaching to, since either one decides what its
		// firings target.
		//
		// Only when the repo really changes, matching
		// ui.(*Client).UpdateTask: a form that saves the whole schedule
		// resends the repo it already has, and a list narrowed since the
		// schedule was written must not freeze every other field on it --
		// disabling such a schedule is exactly what an operator reaching
		// for this would want to do.
		if !beforeTarget.Same(s.Target) {
			if err := c.refuseOffRepoAllowlist(ctx, s.Target, "a schedule cannot fire tasks into it"); err != nil {
				return err
			}
		}
		tzChanged := false
		if timeZone != nil && *timeZone != s.TimeZone {
			s.TimeZone = *timeZone
			s.Recurrence.TimeZone = *timeZone
			tzChanged = true
		}
		if recurrence != nil {
			if timeZone != nil {
				recurrence.TimeZone = *timeZone
			} else if s.TimeZone != "" && recurrence.TimeZone == "" {
				recurrence.TimeZone = s.TimeZone
			}
			// Retimed only when the cadence really is different: a
			// request that resends the recurrence it already has
			// (which is every save from a form that submits the whole
			// schedule) is not a cadence edit, and must not push the
			// next firing out.
			if *recurrence != s.Recurrence || tzChanged {
				s.Recurrence = *recurrence
				s.NextRunAt = s.Recurrence.Next(now, s.Location())
			}
		} else if tzChanged {
			s.NextRunAt = s.Recurrence.Next(now, s.Location())
		}
		if overlap != nil {
			s.Overlap = *overlap
		}
		if req.Enabled != nil {
			s.Enabled = *req.Enabled
		}
		return nil
	}); err != nil {
		return Schedule{}, err
	}

	updated, err := c.Store.GetSchedule(ctx, id)
	if err != nil {
		return Schedule{}, err
	}
	// A retimed cadence has moved past any occurrence that was being
	// held, so that one is dropped rather than left saying it will fire.
	if !updated.NextRunAt.Equal(existing.NextRunAt) {
		if err := c.Store.DropStrandedScheduleSkips(ctx, id, updated.NextRunAt, now); err != nil {
			return Schedule{}, err
		}
	}
	var templateName, suiteName string
	if attaching != nil {
		templateName = attaching.Name
	} else if updated.TemplateID != nil {
		templateName = c.templateNameFor(ctx, *updated.TemplateID)
	}
	if repointing != nil {
		suiteName = repointing.Name
	} else if updated.SuiteID != nil {
		if suite, err := c.Store.GetSuite(ctx, *updated.SuiteID); err == nil && suite != nil {
			suiteName = suite.Name
		}
	}
	return scheduleFrom(*updated, templateName, suiteName), nil
}

// ScheduleRun is what one firing somebody asked for by hand actually
// did, as POST /api/schedules/{id}/run reports it: the task it filed, or
// the suite run it started, plus the schedule itself as it now stands so
// a caller can show "last ran" without reading the list again.
//
// At most one of TaskID and SuiteRunID is ever set -- a schedule fires
// either a task or a suite, decided when it was created
// (UpdateScheduleRequest's own doc comment on why that never changes).
// Neither is set for a schedule translated out of a workflow's own
// `on: schedule` cron: that firing records a CI run for grain to start
// on its next cycle, and the task reviewing it is filed later, when that
// run has a verdict (model.ScheduleFiring.WorkflowRun). WorkflowRun says
// so, since "nothing was filed" is not what happened.
type ScheduleRun struct {
	TaskID      string   `json:"taskId,omitempty"`
	SuiteRunID  int64    `json:"suiteRunId,omitempty"`
	WorkflowRun bool     `json:"workflowRun,omitempty"`
	Schedule    Schedule `json:"schedule"`
}

// scheduleBusyError marks a run-now the schedule's own overlap policy
// suppressed -- writeClientError maps it to a 409, since the request was
// well formed and the deployment is fine: the previous firing simply has
// not finished, and this schedule is set to skip rather than pile up
// (model.OverlapSkip). Its own type so the message says which schedule
// and what to do about it.
//
// Review names the open review task holding a workflow schedule's
// cadence, when that is what the previous firing amounts to
// (model.ScheduleFiring.OpenReview): "its previous run has not finished"
// is true of one of those and useless, because the thing to do about it
// is to close a task rather than to wait for a run.
//
// Workflow is set for a schedule that runs a workflow, which cannot be
// set to overlap at all (repodecl refuses `overlap: allow` on one), so
// the setting the message otherwise points at is no way out for it.
type scheduleBusyError struct {
	ID       string
	Review   string
	Workflow bool
}

func (e *scheduleBusyError) Error() string {
	msg := "schedule " + e.ID + ": its previous run has not finished yet, and this schedule is set to skip " +
		"a run that would overlap one -- wait for it, or set \"If the last run has not finished\" to start it anyway"
	if e.Workflow {
		msg = "schedule " + e.ID + ": its previous run has not finished yet, and a schedule that runs a " +
			"workflow never overlaps one -- grain reviews one run of it at a time"
		if e.Review == "" {
			msg += ", so wait for that run to finish"
		}
	}
	if e.Review != "" {
		msg += " (what has not finished is the review of the last run, task " + e.Review +
			": closing it lets this schedule fire again)"
	}
	return msg
}

// RunScheduleNow fires a schedule this instant, on top of its own
// cadence (grain/task-227): the "Run now" button on ScheduleOverlay, and
// the answer to the question CreateSchedule's own doc comment used to
// send somebody away with ("somebody who does want it to run now can
// still file the task by hand"). Filing it by hand meant copying a
// template's content into a new task and remembering which capabilities
// and fields the schedule carries; this fires the schedule itself, so
// what runs is exactly what the cadence would have run.
//
// It is the same firing, through the same code the reconciler fires
// through (model.Store.FireScheduleNow), with the two differences that
// doc comment sets out: the cadence is left alone -- NextRunAt does not
// move, so the firing already scheduled still happens when it was going
// to -- and a paused schedule still fires, because Enabled stops the
// cadence and this is not the cadence.
//
// A schedule whose previous firing has not finished is refused rather
// than fired, unless it is set to allow overlap: the gate exists so a
// chore cannot have two copies of itself running, and a button is no
// reason to walk round it. Refused loudly, though (scheduleBusyError) --
// the cadence's own suppressed firing is silent because nobody is
// waiting on it, and here somebody is.
func (c *Client) RunScheduleNow(ctx context.Context, id string) (ScheduleRun, error) {
	sched, err := c.Store.GetSchedule(ctx, id)
	if err != nil {
		return ScheduleRun{}, err
	}
	if sched == nil {
		return ScheduleRun{}, &scheduleNotFoundError{ID: id}
	}
	firing, err := c.Store.FireScheduleNow(ctx, *sched, c.now(), c.stateRepo())
	if err != nil {
		return ScheduleRun{}, err
	}
	if firing.Skipped {
		workflow := false
		if sched.TemplateID != nil {
			_, workflow = model.WorkflowReviewStem(*sched.TemplateID)
		}
		return ScheduleRun{}, &scheduleBusyError{ID: id, Review: firing.OpenReview, Workflow: workflow}
	}
	// The cadence answers this one by quietly not firing (model.Store's
	// own suppressOffTargetRepos), because nobody is waiting on it and
	// the operator was told what the narrowing stranded at the save that
	// did it. Here somebody pressed a button, so they are told the same
	// way every other write of an off-list repo tells them
	// (refuseOffAllowlist, and the sentence itself is the one beside the
	// rule): filing the task would have parked it before it reached a
	// sandbox, which is no answer to "run this now".
	if firing.OffTargetRepos != nil {
		return ScheduleRun{}, validationErrorf("%s", model.OffAllowlistRefusal(*firing.OffTargetRepos,
			c.teamTargetRepos(ctx, model.DefaultTeamID), "this schedule cannot be run now"))
	}
	// The same for a repository whose standing work this deployment does
	// not run (grain/task-3332): the switch is the operator's, on the
	// repository's page, and running the schedule anyway would be this
	// button quietly overruling it.
	if firing.StandingWorkOff != nil {
		return ScheduleRun{}, validationErrorf("this schedule cannot be run now: %s; turn the repository's "+
			"standing work, or this schedule's own item, back on to run it",
			model.StandingWorkOffReason(*firing.StandingWorkOff))
	}

	// What it filed is parked, as the cadence's own firing would be
	// (orchestrator.parkUnreached), when the schedule's team cannot reach
	// a repository it names: the task or run exists either way.
	unreached := []string{}
	if firing.TaskID != "" {
		unreached = append(unreached, firing.TaskID)
	}
	if firing.SuiteRunID != 0 {
		run, err := c.Store.GetSuiteRun(ctx, firing.SuiteRunID)
		if err != nil {
			return ScheduleRun{}, err
		}
		if run != nil {
			unreached = append(unreached, suiteRunTaskIDs(*run)...)
		}
	}
	if err := c.parkUnreached(ctx, unreached); err != nil {
		return ScheduleRun{}, err
	}

	// Re-read rather than reported from the copy above: the firing has
	// just written LastRunAt, and a template-backed or suite-backed
	// schedule may have had its display cache resynced with it.
	updated, err := c.Store.GetSchedule(ctx, id)
	if err != nil {
		return ScheduleRun{}, err
	}
	var templateName, suiteName string
	if updated.TemplateID != nil {
		templateName = c.templateNameFor(ctx, *updated.TemplateID)
	}
	if updated.SuiteID != nil {
		if suite, err := c.Store.GetSuite(ctx, *updated.SuiteID); err == nil && suite != nil {
			suiteName = suite.Name
		}
	}
	return ScheduleRun{
		TaskID:      firing.TaskID,
		SuiteRunID:  firing.SuiteRunID,
		WorkflowRun: firing.WorkflowRun,
		Schedule:    scheduleFrom(*updated, templateName, suiteName),
	}, nil
}

// DeleteSchedule removes a schedule outright -- Store.DeleteSchedule's
// own doc comment gives the reasoning: unlike a task, a schedule is only
// ever a standing declaration, so there is no history on the row itself
// worth keeping once a human no longer wants it.
//
// One kind of schedule cannot be deleted: one grain seeded for itself
// (model.SystemTemplate). Not because it is precious, but because the
// deletion would not stick -- orchestrator's reconcileSystemSchedules
// seeds a missing row on the next cycle, so a "Delete" that appeared to
// work would have the row back within a tick, with whatever cadence the
// deployment had chosen for it reset to the default. Disabling it is the
// switch that actually means it: the row stays, keeps its cadence, and
// never comes due. So this refuses and says which button to press,
// rather than letting the reconciler quietly undo somebody.
func (c *Client) DeleteSchedule(ctx context.Context, id string) error {
	if err := c.checkSettingsWritable(ctx, "schedule"); err != nil {
		return err
	}
	if err := c.refuseDeclaredEdit(ctx, id, "delete"); err != nil {
		return err
	}
	existing, err := c.Store.GetSchedule(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return &scheduleNotFoundError{ID: id}
	}
	if existing.TemplateID != nil && model.IsSystemTemplateID(*existing.TemplateID) {
		return validationErrorf(
			"%s is one of grain's own scheduled jobs and cannot be deleted -- grain would "+
				"seed it again on its next cycle, losing the cadence you had chosen for it. "+
				"Disable it instead: it then keeps its settings and never comes due", id)
	}
	return c.Store.DeleteSchedule(ctx, id)
}

// --- handlers ------------------------------------------------------------

// handleListSchedules answers GET /api/schedules: every schedule, or,
// given a filter in the Processes page's own encoding (`q`, `sort`, one
// `f` per condition -- pkg/schedulefilter), the ones it keeps in the
// order it asks for. A filter it cannot read is a 400 naming what is
// wrong with it rather than a list that looks like it matched.
func (s *Server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	filter, err := schedulefilter.ParseQuery(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	list, err := s.tasks.ListSchedules(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	if !filter.Empty() {
		// A day named in "is after 2026-10-24" starts at midnight in the
		// zone every timestamp on the page is printed in (GET
		// /api/config's timeZone), which is how the page reads it too.
		zone := ""
		if cfg, err := s.tasks.Store.GetConfig(r.Context()); err == nil && cfg != nil {
			zone = cfg.TimeZone
		}
		loc := model.LoadLocation(model.TimeZoneOrDefault(zone))
		list = schedulefilter.Apply(filter, list, scheduleFacts, s.tasks.now(), loc)
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	var req CreateScheduleRequest
	if !readJSONRefusing(w, r, &req, retiredTeamField) {
		return
	}
	sched, err := s.tasks.CreateSchedule(r.Context(), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, sched)
}

func (s *Server) handleUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	if err := s.tasks.authorizeScheduleEdit(r.Context(), r.PathValue("id")); err != nil {
		writeClientError(w, err)
		return
	}
	var req UpdateScheduleRequest
	if !readJSONRefusing(w, r, &req, retiredTeamField) {
		return
	}
	sched, err := s.tasks.UpdateSchedule(r.Context(), r.PathValue("id"), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sched)
}

func (s *Server) handleRunSchedule(w http.ResponseWriter, r *http.Request) {
	run, err := s.tasks.RunScheduleNow(r.Context(), r.PathValue("id"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, run)
}

func (s *Server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	if err := s.tasks.DeleteSchedule(r.Context(), r.PathValue("id")); err != nil {
		writeClientError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
