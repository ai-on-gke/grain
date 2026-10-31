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
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// templateNotFoundError marks a template ID with no template behind it --
// scheduleNotFoundError's own reasoning, so the message a caller sees
// names a template, not a task or a schedule.
type templateNotFoundError struct{ ID string }

func (e *templateNotFoundError) Error() string { return "no template " + e.ID }

// Template is a template's JSON shape (bwsalmon/agents#516) --
// Schedule's own content fields (Title/Description/AutoMerge/Reads/
// Capabilities/CustomFields), the subset a schedule's
// ScheduleOverlay.jsx already collects, minus everything about firing on
// a cadence: a template is never itself something that fires, only
// something a schedule (or another caller) fires from.
//
// Repo and Base are the optional binding (grain/task-285): empty, which
// is the ordinary case, means whatever fires this template decides which
// repo and branch to target; set, they are what every firing targets
// instead (model.Template's own doc comment on both halves). Base is
// only ever set alongside Repo, and can be empty even then -- a repo
// bound with no branch pinned.
//
// Writes is the extra write targets every task filed from this template
// carries (model.Template.Writes, grain/task-86) -- {repo, base}
// objects, ui.WriteTarget's own shape, for the reason Task.Writes gives:
// a repo here without its branch is a declaration with a piece missing.
// Unrelated to Repo and Base above, which are the binding: those say
// where a firing is filed, these say which further repos its run may
// have work landed in by proposing a task.
//
// CustomFields is what every task filed from this template says about
// this deployment's own declared fields (model.Template.CustomFields,
// grain/task-105), keyed by field name -- the same {name: value} shape
// Task.CustomFields has on the wire, joined by the frontend to the
// declarations GET /api/config already carries.
type Template struct {
	// ChangeTarget is the non-git target every task filed from this
	// template carries (model.Template.ChangeTarget): `target: <kind>`.
	// Omitted for a git or unbound template.
	ChangeTarget *model.ChangeTarget `json:"changeTarget,omitempty"`
	// Reviewers is the person-set list of reviewer usernames every task filed
	// from this template starts with (model.Template.Reviewers).
	Reviewers   []string `json:"reviewers,omitempty"`
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	// Fields is the inputs this template asks for when somebody
	// instantiates it (model.TemplateField, grain/task-58) -- the named
	// holes its title and body leave as `{{name}}`, in the order a form
	// should ask for them. Omitted for the ordinary template, which asks
	// for nothing.
	Fields    []TemplateField `json:"fields,omitempty"`
	Repo      string          `json:"repo,omitempty"`
	Base      string          `json:"base,omitempty"`
	Priority  *int            `json:"priority,omitempty"`
	AutoMerge bool            `json:"autoMerge"`
	// NetworkAccess is how much of the internet the sandbox of every
	// task filed from this template may reach
	// (model.Template.NetworkAccess, grain/task-269) -- "" for a
	// template that fixes none, in which case a firing takes the
	// deployment's own default. Omitted when empty, like Repo above, so
	// "fixes nothing" is silence rather than a word.
	NetworkAccess string `json:"networkAccess,omitempty"`
	// NothingNeededAction is what a run ending on nothing_needed does to
	// every task filed from this template
	// (model.Template.NothingNeededAction): "ack", "close", or "" for
	// close by default. Omitted when empty, like NetworkAccess.
	NothingNeededAction string `json:"nothingNeededAction,omitempty"`
	// Model is the model every task filed from this template asks for
	// (model.Template.Model) -- "" for a template that chose none, whose
	// tasks run on their team's default model.
	Model string `json:"model,omitempty"`
	// Interactive is whether every task filed from this template is a
	// live chat with whoever is watching it (model.Template.Interactive).
	// Omitted when false, like Repo, so a template saved before the
	// switch existed keeps the revision it had.
	Interactive  bool                        `json:"interactive,omitempty"`
	Reads        []string                    `json:"reads,omitempty"`
	Writes       []WriteTarget               `json:"writes,omitempty"`
	CustomFields map[string]CustomFieldValue `json:"customFields,omitempty"`
	Capabilities []string                    `json:"capabilities"`
	CreatedAt    time.Time                   `json:"createdAt"`
	// DeclaredIn is set when this template is declared by a repository
	// in its own `.grain` directory rather than created here
	// (grain/task-60): the repository and the file. A UI shows it so
	// that a row belonging to a repository reads as one, and so that
	// somebody who wants it changed is sent to the file rather than to a
	// form that answers 400 (Client.refuseDeclaredEdit).
	DeclaredIn *Declaration `json:"declaredIn,omitempty"`
	// System marks one of grain's own built-in templates
	// (model.FileableSystemTemplates): listed so a person can file from
	// it, never edited or deleted here, and not something a schedule or
	// suite can name. Omitted for every template this deployment owns.
	System bool `json:"system,omitempty"`
	// Revision identifies this template's editable content as it stands
	// (revision.go): an editor sends it back on PATCH, and is refused
	// with a 409 if somebody else has saved the template since.
	Revision string `json:"revision,omitempty"`
}

// TemplateField is one input a template asks for, on the wire: the key
// `{{name}}` in its prose refers to, what to label the box with, and --
// the whole of the difference between the two kinds of field -- the
// options that make it a picker rather than a text box
// (model.TemplateField's own doc comment on each).
type TemplateField struct {
	Name     string   `json:"name"`
	Label    string   `json:"label,omitempty"`
	Options  []string `json:"options,omitempty"`
	Default  string   `json:"default,omitempty"`
	Required bool     `json:"required,omitempty"`
}

func templateFieldsFrom(fields []model.TemplateField) []TemplateField {
	if len(fields) == 0 {
		return nil
	}
	out := make([]TemplateField, 0, len(fields))
	for _, f := range fields {
		out = append(out, TemplateField{
			Name:     f.Name,
			Label:    f.Label,
			Options:  f.Options,
			Default:  f.Default,
			Required: f.Required,
		})
	}
	return out
}

// templateFields is the way back, with the declaration checked as it is
// converted: a hole nothing could ever fill is refused to the person
// writing the template rather than discovered by whoever reaches for it
// months later (model.ValidateTemplateFields).
func templateFields(fields []TemplateField) ([]model.TemplateField, error) {
	out := make([]model.TemplateField, 0, len(fields))
	for _, f := range fields {
		out = append(out, model.TemplateField{
			Name:     strings.TrimSpace(f.Name),
			Label:    strings.TrimSpace(f.Label),
			Options:  f.Options,
			Default:  f.Default,
			Required: f.Required,
		})
	}
	if err := model.ValidateTemplateFields(out); err != nil {
		return nil, validationErrorf("%s", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func templateFrom(t model.Template) Template {
	out := Template{
		ID:            t.ID,
		Name:          t.Name,
		Title:         t.Title,
		Description:   t.Body,
		Fields:        templateFieldsFrom(t.Fields),
		AutoMerge:     t.AutoMerge,
		Interactive:   t.Interactive,
		NetworkAccess: string(t.NetworkAccess),
		Model:         t.Model,
		CustomFields:  customFieldValues(t.CustomFields),
		Capabilities:  []string{},
		CreatedAt:     t.CreatedAt,

		NothingNeededAction: string(t.NothingNeededAction),
	}
	if t.Target != nil {
		out.Repo, out.Base = t.Target.String(), t.Base
	}
	if t.Priority != nil && *t.Priority != 0 {
		p := *t.Priority
		out.Priority = &p
	}
	out.ChangeTarget = t.ChangeTarget.Clone()
	out.Reviewers = slices.Clone(t.Reviewers)
	for _, r := range t.Reads {
		out.Reads = append(out.Reads, r.String())
	}
	for _, w := range t.Writes {
		out.Writes = append(out.Writes, WriteTarget{Repo: w.Repo.String(), Base: w.Base})
	}
	for _, g := range t.Grants {
		out.Capabilities = append(out.Capabilities, g.Capability)
	}
	out.Revision = templateRevision(out)
	return out
}

// ListTemplates returns every template, newest first.
func (c *Client) ListTemplates(ctx context.Context) ([]Template, error) {
	list, err := c.Store.ListTemplates(ctx)
	if err != nil {
		return nil, err
	}
	declared, err := c.declaredIn(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Template, 0, len(list))
	for _, t := range list {
		tmpl := templateFrom(t)
		tmpl.DeclaredIn = declarationOf(declared, t.ID)
		out = append(out, tmpl)
	}
	// grain's own fileable templates after the deployment's, so a
	// picker that preselects its first entry still leads with one of
	// this deployment's.
	for _, t := range model.FileableSystemTemplates() {
		tmpl := templateFrom(t)
		tmpl.System = true
		out = append(out, tmpl)
	}
	return out, nil
}

// fileableTemplate is the template a task is filed (or previewed) from:
// one of grain's own built-in templates for a "system:" id this build
// offers for filing (model.FileableSystemTemplate), and this
// deployment's own row otherwise. A "system:" id that backs a schedule
// rather than a picker entry resolves to nothing here, so a person
// cannot file the memory agent, with its grants, by naming its id.
func (c *Client) fileableTemplate(ctx context.Context, id string) (*model.Template, error) {
	if model.IsSystemTemplateID(id) {
		return model.FileableSystemTemplate(id), nil
	}
	return c.Store.GetTemplate(ctx, id)
}

// refuseSystemTemplateEdit is the answer to editing or deleting one of
// grain's own templates: they are code, not rows
// (model.FileableSystemTemplates' own doc comment on why), so the
// refusal says what to do instead rather than answering "no template".
func refuseSystemTemplateEdit(id, verb string) error {
	if !model.IsSystemTemplateID(id) {
		return nil
	}
	return validationErrorf(
		"%s is one of grain's own built-in templates, so it is not this deployment's to %s -- "+
			"file a task from it as it is, or create a template of your own", id, verb)
}

// CreateTemplateRequest is a new template's fields --
// CreateScheduleRequest's own content subset, minus Recurrence/Enabled
// for the same reason Template itself leaves them out.
//
// Repo is optional, and binds the new template to that repo when given;
// Base, only meaningful alongside it, pins a branch within that repo and
// is optional in turn. Blank Repo -- the ordinary case -- leaves the
// template unbound, deciding nothing about where its firings go.
//
// Writes is the extra write targets a firing carries, and is checked
// against this deployment's target repo list here (parseWrites) rather
// than when a schedule built on this template eventually fires: a repo
// nothing here could open a pull request against is worth refusing to
// the human saving the template, who can fix it, and not to a firing at
// three in the morning that nobody is watching.
//
// CustomFields is the values every firing carries for this deployment's
// declared fields, checked here against those declarations
// (normalizeCustomFields, model.NormalizeCustomFieldValues) exactly as a
// task filed by hand is: an agent, a schedule and a human all get the
// same canonical value out of "TRUE", and all three are told the same
// thing about a name nothing declares. It is refused to whoever is
// saving the template rather than to the firing that would have carried
// it, for parseWrites' own reason one field up.
type CreateTemplateRequest struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// Fields is the inputs this template asks for when it is
	// instantiated (Template.Fields), checked here against
	// model.ValidateTemplateFields so a field nobody could fill in --
	// a name that is not a key, a default outside its own options -- is
	// refused to whoever is writing the template.
	Fields []TemplateField `json:"fields"`
	// ChangeTarget binds the template to a non-git target (a registered change target kind), in
	// place of Repo and Base (model.ValidateTemplateChangeTarget).
	ChangeTarget *model.ChangeTarget `json:"changeTarget,omitempty"`
	// Reviewers is the person-set list of reviewer usernames every task filed
	// from this template starts with (model.Template.Reviewers).
	Reviewers []string `json:"reviewers,omitempty"`
	Repo      string   `json:"repo"`
	Base      string   `json:"base"`
	Priority  *int     `json:"priority,omitempty"`
	AutoMerge bool     `json:"autoMerge"`
	// NetworkAccess fixes the network of every task this template files
	// (model.Template.NetworkAccess): one of model.NetworkAccesses(), or
	// "" for a template that fixes none and leaves each firing on the
	// deployment's own default.
	//
	// Not refused on a deployment whose backend cannot honour "none",
	// the way a task filed with it is (Client.refuseIgnoredNetworkAccess):
	// a template is a declaration that outlives this deployment's
	// current backend, and the firing it eventually produces is where
	// that question is asked and answered.
	NetworkAccess string `json:"networkAccess"`
	// NothingNeededAction sets model.Template.NothingNeededAction: "close"
	// or "ack", or "" for close.
	NothingNeededAction string `json:"nothingNeededAction"`
	// Model is the model every task this template files asks for
	// (model.Template.Model), or "" to leave it to the team's default.
	Model string `json:"model"`
	// Interactive sets model.Template.Interactive.
	Interactive  bool                        `json:"interactive"`
	Capabilities []string                    `json:"capabilities"`
	Reads        []string                    `json:"reads"`
	Writes       []WriteTarget               `json:"writes"`
	CustomFields map[string]CustomFieldValue `json:"customFields"`
}

// parseBinding turns a request's repo/base pair into the binding
// model.Template carries: nil and "" for a blank repo (an unbound
// template, whatever base came with it -- a branch with no repo to name
// it in binds nothing, so it is dropped rather than half-applied), and
// the parsed repo with its base otherwise. Shared by CreateTemplate and
// UpdateTemplate so both read a blank repo the same way: as "not bound",
// which is also how UpdateTemplate unbinds one.
func (c *Client) parseBinding(repo, base string) (*model.RepoRef, string, error) {
	if strings.TrimSpace(repo) == "" {
		return nil, "", nil
	}
	target, err := c.parseRepo(repo)
	if err != nil {
		return nil, "", err
	}
	return &target, strings.TrimSpace(base), nil
}

// CreateTemplate files a new template straight into the store.
func (c *Client) CreateTemplate(ctx context.Context, req CreateTemplateRequest) (Template, error) {
	if err := c.checkSettingsWritable(ctx, "template"); err != nil {
		return Template{}, err
	}
	if strings.TrimSpace(req.Name) == "" {
		return Template{}, validationErrorf("name is required")
	}
	if strings.TrimSpace(req.Title) == "" {
		return Template{}, validationErrorf("title is required")
	}
	var priority *int
	if req.Priority != nil {
		if *req.Priority < 0 {
			return Template{}, validationErrorf("priority must be a positive integer")
		}
		if *req.Priority > 0 {
			p := *req.Priority
			priority = &p
		}
	}
	if err := validateNetworkAccess(req.NetworkAccess); err != nil {
		return Template{}, err
	}
	nothingNeeded, err := validateNothingNeededAction(req.NothingNeededAction)
	if err != nil {
		return Template{}, err
	}
	chosenModel, err := c.declaredModelChoice(ctx, req.Model)
	if err != nil {
		return Template{}, err
	}
	grants, err := c.grantsFor(req.Capabilities, model.GrantByLabel)
	if err != nil {
		return Template{}, err
	}
	reads, err := c.parseReads(req.Reads)
	if err != nil {
		return Template{}, err
	}
	writes, err := c.parseWrites(ctx, model.DefaultTeamID, req.Writes)
	if err != nil {
		return Template{}, err
	}
	target, base, err := c.parseBinding(req.Repo, req.Base)
	if err != nil {
		return Template{}, err
	}
	if err := validateBase("base", base); err != nil {
		return Template{}, err
	}
	// A binding is where a firing's target comes from whenever it is set
	// (model.Template.FiringTarget wins over the schedule's own repo), so
	// it is asked the same question a schedule's repo and a task's target
	// are: a template bound off Config.TargetRepos would file, on every
	// firing, a task nothing here may work in (refuseOffAllowlist).
	if target != nil {
		if err := c.refuseOffAllowlistForTeam(ctx, model.DefaultTeamID, *target, "a template cannot be bound to it"); err != nil {
			return Template{}, err
		}
	}
	customFields, err := c.normalizeCustomFields(ctx, req.CustomFields)
	if err != nil {
		return Template{}, err
	}
	fields, err := templateFields(req.Fields)
	if err != nil {
		return Template{}, err
	}

	id, err := c.Store.NewTemplateID(ctx)
	if err != nil {
		return Template{}, err
	}
	tmpl := model.Template{
		ID:            id,
		Name:          req.Name,
		Title:         req.Title,
		Body:          req.Description,
		Fields:        fields,
		Target:        target,
		Base:          base,
		Priority:      priority,
		AutoMerge:     req.AutoMerge,
		Interactive:   req.Interactive,
		NetworkAccess: model.NetworkAccess(req.NetworkAccess),
		Model:         chosenModel,
		Reads:         reads,
		Writes:        writes,
		Grants:        grants,
		CustomFields:  customFields,
		Reviewers:     model.NormalizeReviewers(req.Reviewers),
		CreatedAt:     c.now(),

		NothingNeededAction: nothingNeeded,
	}
	if req.ChangeTarget != nil {
		tmpl.ChangeTarget = req.ChangeTarget
		if err := model.ValidateTemplateChangeTarget(&tmpl); err != nil {
			return Template{}, validationErrorf("%v", err)
		}
	}
	if err := c.checkSettingsWritable(ctx, "template"); err != nil {
		return Template{}, err
	}
	if err := c.Store.PutTemplate(ctx, tmpl); err != nil {
		return Template{}, err
	}
	return templateFrom(tmpl), nil
}

// fromTemplate is what CreateTaskRequest.TemplateID means: the request,
// with every blank it left filled in from the template it names, and its
// input fields substituted with the values it supplied (grain/task-58).
//
// It runs before CreateTask's own validation rather than instead of it,
// so a task filed from a template goes through exactly the checks a
// hand-written one does -- the repo allowlist, the sandbox shape, the
// declared fields -- against the values the template supplied. What it
// composes is model.ComposeTemplateTask, which is also what the preview
// beside the form shows (Client.TemplateComposition): the two cannot
// drift, because there is one of them.
//
// "Blank" is per field, deliberately. A form that files from a template
// sends nothing but the template and its values, and gets the whole
// declaration; a caller that says "this template, but against my branch"
// keeps its branch. The exceptions are the two a template is entitled to
// decide outright: a bound template's repo and branch, which win here as
// they win at every other firing point, and which are therefore not
// applied at all to a task explicitly filed against no repo (NoRepo) --
// that request is not asking where, it is saying there is no such place.
func (c *Client) fromTemplate(ctx context.Context, req CreateTaskRequest) (CreateTaskRequest, error) {
	id := strings.TrimSpace(req.TemplateID)
	if id == "" {
		if len(req.Fields) > 0 {
			return req, validationErrorf(
				"fields were given with no templateId to read them against")
		}
		return req, nil
	}
	tmpl, err := c.fileableTemplate(ctx, id)
	if err != nil {
		return req, err
	}
	if tmpl == nil {
		return req, &templateNotFoundError{ID: id}
	}
	if err := model.ValidateTemplateFieldValues(tmpl.Fields, req.Fields); err != nil {
		return req, validationErrorf("%s", err)
	}

	// The repo the filing itself chose, for the composition to place the
	// task against unless the template is bound. Parsed here rather than
	// left to CreateTask so that a repo this deployment cannot read is
	// refused before anything is composed from it.
	var target model.RepoRef
	if !req.NoRepo && strings.TrimSpace(req.Repo) != "" {
		parsed, err := c.parseRepo(req.Repo)
		if err != nil {
			return req, err
		}
		target = parsed
	}
	comp := model.ComposeTemplateTask(*tmpl, req.Fields, target, req.Base, req.Approved)

	if strings.TrimSpace(req.Title) == "" {
		req.Title = comp.Title
	}
	if strings.TrimSpace(req.Description) == "" {
		req.Description = comp.Body
	}
	if !req.NoRepo && comp.Target != nil {
		req.Repo, req.Base = comp.Target.String(), comp.Base
	}
	// A template bound to a change target (`target: <kind>`) files its
	// tasks against it, with no repo, unless the request chose its own.
	if !req.NoRepo && req.ChangeTarget == nil && comp.ChangeTarget != nil {
		req.ChangeTarget = comp.ChangeTarget
	}
	if len(req.Reviewers) == 0 && len(comp.Reviewers) > 0 {
		req.Reviewers = slices.Clone(comp.Reviewers)
	}
	// One of grain's own templates is about this deployment rather than
	// any repository (model.FileableSystemTemplates), so filed with no
	// repo named it is filed with none, rather than falling back to the
	// default target and handing the run a checkout it has no use for.
	if model.IsSystemTemplateID(id) && comp.Target == nil && strings.TrimSpace(req.Repo) == "" {
		req.NoRepo = true
	}
	// A request cannot say "no" to an auto-merging template by leaving
	// the box unticked -- the false it sends is the same false a caller
	// that said nothing sends. Saying no is an edit on the filed task,
	// as it is for everything else a template decides, and the preview
	// beside the form is where that is visible before it is filed.
	if !req.AutoMerge {
		req.AutoMerge = comp.AutoMerge
	}
	if !req.Interactive {
		req.Interactive = comp.Interactive
	}
	if req.Priority == nil && comp.Priority != 0 {
		p := comp.Priority
		req.Priority = &p
	}
	if strings.TrimSpace(req.NetworkAccess) == "" {
		req.NetworkAccess = string(comp.NetworkAccess)
	}
	if strings.TrimSpace(req.NothingNeededAction) == "" {
		req.NothingNeededAction = string(comp.NothingNeededAction)
	}
	// The template's model (model.Template.Model) fills a request that
	// chose none. A request that pins a connection has chosen what runs
	// it, so the template's model is not pressed onto a connection that
	// may not serve it; the request's own model, when it states one,
	// wins outright. And a template that chose none leaves the team's
	// default to decide, as for any task.
	if strings.TrimSpace(req.Model) == "" && strings.TrimSpace(req.AgentBackendID) == "" {
		req.Model = comp.Model
		req.modelFromTemplate = comp.Model
	}
	if req.Capabilities == nil {
		capabilities := make([]string, 0, len(comp.Grants))
		for _, g := range comp.Grants {
			capabilities = append(capabilities, g.Capability)
		}
		req.Capabilities = &capabilities
		req.grantsFromTemplate = append([]model.Grant(nil), comp.Grants...)
	}
	if len(req.Reads) == 0 {
		for _, r := range comp.Reads {
			req.Reads = append(req.Reads, r.String())
		}
	}
	if len(req.Writes) == 0 {
		for _, w := range comp.Writes {
			req.Writes = append(req.Writes, WriteTarget{Repo: w.Repo.String(), Base: w.Base})
		}
	}
	if len(req.CustomFields) == 0 {
		req.CustomFields = customFieldValues(comp.CustomFields)
	}
	return req, nil
}

// TemplateComposition is the task filing this template would produce,
// with these values in its input fields, against this repo and branch --
// what the form that collects them shows beside itself, before anything
// is filed (composition.go's own preamble on why a preview is the
// firing's own answer rather than a second one).
//
// repo may be empty, exactly as SuiteComposition's may: a template
// picked before anybody has chosen a repo still has an answer worth
// showing, since its content -- which is the part the input fields
// change, and the part somebody is checking -- is already decided.
func (c *Client) TemplateComposition(ctx context.Context, id, repo, base string,
	values map[string]string, approved bool) (Composition, error) {
	tmpl, err := c.fileableTemplate(ctx, id)
	if err != nil {
		return Composition{}, err
	}
	if tmpl == nil {
		return Composition{}, &templateNotFoundError{ID: id}
	}
	if err := model.ValidateTemplateFieldValues(tmpl.Fields, values); err != nil {
		return Composition{}, validationErrorf("%s", err)
	}
	var target model.RepoRef
	if strings.TrimSpace(repo) != "" {
		parsed, err := c.parseRepo(repo)
		if err != nil {
			return Composition{}, err
		}
		target = parsed
	}
	// approved is the form's own switch rather than an assumption: a
	// deployment that files proposals by default would otherwise be
	// shown "approved when filed" over a task that will sit waiting for
	// somebody (CreateTaskRequest.Approved is the same choice, made on
	// the same form). An interactive template is the exception: a person
	// instantiating it always files an approved live chat
	// (Client.CreateTask's `req.Interactive && proposedBy == nil`), and the
	// form hides the Approved checkbox for that reason, so the template
	// itself decides approval here.
	if tmpl.Interactive {
		approved = true
	}
	comp, err := c.Store.TemplateComposition(ctx, *tmpl, values, target, base, approved)
	if err != nil {
		return Composition{}, err
	}
	if tmpl.Interactive {
		comp.Sources.Approved = model.LayerTemplate
	}
	tasks := c.composedTasksFrom(ctx, []model.Composition{comp})
	out := Composition{
		Kind:  "task",
		Base:  base,
		Tasks: tasks,
	}
	if target != (model.RepoRef{}) {
		out.Repo = target.String()
	}
	return out, nil
}

// UpdateTemplateRequest is a template's editable fields -- nil means
// "leave this one alone", UpdateScheduleRequest's own convention.
// Changing a template that a schedule already points at takes effect the
// next time that schedule fires (model's own fireTaskSchedule resolves
// TemplateID fresh each time), with no separate step to "push" the change
// out to every schedule using it.
//
// Repo is how a template is bound and unbound: a repo binds it (and
// rebinding one already bound just moves it), an empty string unbinds
// it, dropping any pinned branch with it, and omitting the field leaves
// the binding exactly as it is. Base pins or clears the branch within
// whatever repo the template is bound to by the end of this request --
// naming one for a template that will not be bound at all is refused
// rather than quietly kept, since a branch with no repo to read it
// against decides nothing.
//
// CustomFields follows UpdateTaskRequest.CustomFields exactly: given, it
// replaces this template's values for every declared field (the form
// renders them all, so what it sends is the whole answer), and a value
// for a field since deleted from the state repository is left alone
// rather than dropped by an unrelated edit -- mergeCustomFields, and the
// same reasoning about a value nothing can render and nobody can
// re-state.
type UpdateTemplateRequest struct {
	Name        *string `json:"name,omitempty"`
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	// Fields replaces this template's whole declaration of the inputs it
	// asks for -- the form renders them all, so what it sends is the
	// whole answer, and an empty list is how the last one is removed.
	// Absent leaves them exactly as they are.
	Fields *[]TemplateField `json:"fields,omitempty"`
	// ChangeTarget replaces the template's non-git target; one with an
	// empty kind clears it. Absent leaves it as it is.
	ChangeTarget *model.ChangeTarget `json:"changeTarget,omitempty"`
	// Reviewers replaces the template's reviewer list; a non-nil empty slice
	// clears it. Absent leaves it as it is.
	Reviewers *[]string `json:"reviewers,omitempty"`
	Repo      *string   `json:"repo,omitempty"`
	Base      *string   `json:"base,omitempty"`
	Priority  *int      `json:"priority,omitempty"`
	AutoMerge *bool     `json:"autoMerge,omitempty"`
	// NetworkAccess edits what CreateTemplateRequest.NetworkAccess sets,
	// with an empty string meaningful rather than ignored: it clears the
	// template's own answer, so its firings go back to taking the
	// deployment's. Only the field being absent leaves it alone.
	NetworkAccess *string `json:"networkAccess,omitempty"`
	// NothingNeededAction edits CreateTemplateRequest.NothingNeededAction's
	// setting; an empty string sets it back to close.
	NothingNeededAction *string `json:"nothingNeededAction,omitempty"`
	// Model edits what CreateTemplateRequest.Model sets; an empty string
	// clears it, so the template's tasks go back to the team's default.
	Model *string `json:"model,omitempty"`
	// Interactive edits CreateTemplateRequest.Interactive's own field.
	Interactive  *bool                        `json:"interactive,omitempty"`
	Capabilities *[]string                    `json:"capabilities,omitempty"`
	Reads        *[]string                    `json:"reads,omitempty"`
	Writes       *[]WriteTarget               `json:"writes,omitempty"`
	CustomFields *map[string]CustomFieldValue `json:"customFields,omitempty"`
	// Revision, given, is the template's Revision as the caller last read
	// it, and the edit is refused (staleEditError) if the template has been
	// saved since. Absent applies the edit regardless.
	Revision *string `json:"revision,omitempty"`
}

// UpdateTemplate edits a template's fields in place.
func (c *Client) UpdateTemplate(ctx context.Context, id string, req UpdateTemplateRequest) (Template, error) {
	if err := c.checkSettingsWritable(ctx, "template"); err != nil {
		return Template{}, err
	}
	// Before anything is parsed: a request that may not change this
	// template at all should hear that, not a complaint about a field
	// it also sent (refuseSystemScheduleContentEdit's own ordering).
	if err := refuseSystemTemplateEdit(id, "edit"); err != nil {
		return Template{}, err
	}
	if err := c.refuseDeclaredEdit(ctx, id, "edit"); err != nil {
		return Template{}, err
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		return Template{}, validationErrorf("name cannot be empty")
	}
	if req.Title != nil && strings.TrimSpace(*req.Title) == "" {
		return Template{}, validationErrorf("title cannot be empty")
	}
	if req.Priority != nil && *req.Priority < 0 {
		return Template{}, validationErrorf("priority must be a positive integer")
	}
	if req.NetworkAccess != nil {
		if err := validateNetworkAccess(*req.NetworkAccess); err != nil {
			return Template{}, err
		}
	}
	var nothingNeeded model.NothingNeededAction
	if req.NothingNeededAction != nil {
		var err error
		if nothingNeeded, err = validateNothingNeededAction(*req.NothingNeededAction); err != nil {
			return Template{}, err
		}
	}
	var chosenModel *string
	if req.Model != nil {
		m, err := c.declaredModelChoice(ctx, *req.Model)
		if err != nil {
			return Template{}, err
		}
		chosenModel = &m
	}
	var grants []model.Grant
	if req.Capabilities != nil {
		var err error
		grants, err = c.grantsFor(*req.Capabilities, model.GrantByLabel)
		if err != nil {
			return Template{}, err
		}
	}
	existing, err := c.Store.GetTemplate(ctx, id)
	if err != nil {
		return Template{}, err
	}
	if existing == nil {
		return Template{}, &templateNotFoundError{ID: id}
	}
	if err := checkRevision("template", id, req.Revision, templateRevisionOf(*existing)); err != nil {
		return Template{}, err
	}

	var reads []model.RepoRef
	if req.Reads != nil {
		var err error
		reads, err = c.parseReads(*req.Reads)
		if err != nil {
			return Template{}, err
		}
	}
	var writes []model.WriteTarget
	if req.Writes != nil {
		var err error
		writes, err = c.parseWrites(ctx, model.DefaultTeamID, *req.Writes)
		if err != nil {
			return Template{}, err
		}
	}
	var fields []model.TemplateField
	if req.Fields != nil {
		var err error
		fields, err = templateFields(*req.Fields)
		if err != nil {
			return Template{}, err
		}
		if err := c.refuseUnattendedUnfillable(ctx, id, fields); err != nil {
			return Template{}, err
		}
	}

	// The binding this request leaves behind, worked out against what
	// the template already carries so that Repo and Base can each be
	// given on their own.
	target, base := existing.Target, existing.Base
	if req.Repo != nil {
		target, base, err = c.parseBinding(*req.Repo, base)
		if err != nil {
			return Template{}, err
		}
	}
	if req.Base != nil {
		base = strings.TrimSpace(*req.Base)
		if err := validateBase("base", base); err != nil {
			return Template{}, err
		}
	}
	if target == nil && base != "" {
		return Template{}, validationErrorf(
			"base needs a repo to name a branch of: bind this template to a repo, or leave base empty")
	}
	// Rebinding onto a repo off Config.TargetRepos is refused, as
	// binding one there in the first place is (CreateTemplate) -- and
	// only when the binding really moves, matching UpdateTask: a form
	// that saves the whole template resends the repo it is already bound
	// to, and a list narrowed since must not freeze the template's title,
	// body or capabilities along with it.
	if target != nil && (existing.Target == nil || !existing.Target.Same(*target)) {
		if err := c.refuseOffRepoAllowlist(ctx, *target, "a template cannot be bound to it"); err != nil {
			return Template{}, err
		}
	}
	// Binding a template a qualification plan already schedules is
	// refused rather than left to fail that repo's next run, the same
	// "do not strand a caller that points at this" care DeleteTemplate
	// takes: a qualification run always targets its own candidate's repo
	// (model.Store.CreateQualificationRun), so a template bound
	// elsewhere is one such a plan can no longer use at all.
	if target != nil && (req.Repo != nil || req.Base != nil) {
		plans, err := c.Store.QualificationPlansUsingTemplate(ctx, id)
		if err != nil {
			return Template{}, err
		}
		for _, repo := range plans {
			if repo != *target {
				return Template{}, validationErrorf(
					"template is used by %s's qualification plan, so it cannot be bound to %s; remove it from that plan first",
					repo, target)
			}
		}
	}

	if err := c.checkSettingsWritable(ctx, "template"); err != nil {
		return Template{}, err
	}
	if err := c.Store.UpdateTemplate(ctx, id, func(t *model.Template) error {
		if err := checkRevision("template", id, req.Revision, templateRevisionOf(*t)); err != nil {
			return err
		}
		if req.Name != nil {
			t.Name = *req.Name
		}
		if req.Title != nil {
			t.Title = *req.Title
		}
		if req.Description != nil {
			t.Body = *req.Description
		}
		if req.Fields != nil {
			t.Fields = fields
		}
		if req.Repo != nil || req.Base != nil {
			t.Target, t.Base = target, base
		}
		if req.Priority != nil {
			if *req.Priority == 0 {
				t.Priority = nil
			} else {
				p := *req.Priority
				t.Priority = &p
			}
		}
		if req.AutoMerge != nil {
			t.AutoMerge = *req.AutoMerge
		}
		if req.Interactive != nil {
			t.Interactive = *req.Interactive
		}
		if req.NetworkAccess != nil {
			t.NetworkAccess = model.NetworkAccess(*req.NetworkAccess)
		}
		if req.NothingNeededAction != nil {
			t.NothingNeededAction = nothingNeeded
		}
		if chosenModel != nil {
			t.Model = *chosenModel
		}
		if req.Capabilities != nil {
			t.Grants = grants
		}
		if req.Reads != nil {
			t.Reads = reads
		}
		if req.Writes != nil {
			t.Writes = writes
		}
		if req.Reviewers != nil {
			t.Reviewers = model.NormalizeReviewers(*req.Reviewers)
		}
		if req.ChangeTarget != nil {
			t.ChangeTarget = nil
			if strings.TrimSpace(req.ChangeTarget.Kind) != "" {
				t.ChangeTarget = req.ChangeTarget
			}
		}
		if t.ChangeTarget != nil {
			if err := model.ValidateTemplateChangeTarget(t); err != nil {
				return validationErrorf("%v", err)
			}
		}
		if req.CustomFields != nil {
			declaredFields, err := c.Store.ListCustomFields(ctx)
			if err != nil {
				return err
			}
			customFields, err := model.NormalizeCustomFieldValuesFrom(declaredFields,
				customFieldModelValues(*req.CustomFields), t.CustomFields)
			if err != nil {
				return validationErrorf("%s", err)
			}
			t.CustomFields = mergeCustomFields(t.CustomFields, *req.CustomFields, customFields, declaredFields)
		}
		return nil
	}); err != nil {
		return Template{}, err
	}

	updated, err := c.Store.GetTemplate(ctx, id)
	if err != nil {
		return Template{}, err
	}
	// Schedules firing from it carry a display copy of its content;
	// refresh it now rather than at each one's next firing.
	if err := c.Store.ResyncTemplateSchedules(ctx, *updated); err != nil {
		return Template{}, err
	}
	return templateFrom(*updated), nil
}

// DeleteTemplate removes a template outright -- DeleteSchedule's own
// "no history worth keeping" reasoning applies again here, except a
// template additionally refuses to delete out from under a schedule
// that still fires from it, a qualification plan (bwsalmon/agents#518)
// that still schedules from it, a suite (bwsalmon/agents#642) that
// still runs it, or an open task (grain/task-284) that still names it as
// its review: unlike editing a template (which every schedule, plan,
// suite or task pointing at it is meant to pick up), deleting one out from
// under any of them would silently strand its next firing -- a
// schedule's with no content to file, a plan's with no template for
// CreateQualificationRun to resolve, a suite's with no template for
// CreateSuiteRun/FireNextPass to resolve, a task's with no template for
// orchestrator.fileReviewTask to resolve (which leaves its own pull
// request waiting on a review that can never be filed) -- worse than the
// plain, retried error each would otherwise have to surface days or
// weeks later. A human wanting to delete it anyway repoints or deletes
// those schedules, plans and suites first, and detaches it from those
// tasks.
func (c *Client) DeleteTemplate(ctx context.Context, id string) error {
	if err := c.checkSettingsWritable(ctx, "template"); err != nil {
		return err
	}
	if err := refuseSystemTemplateEdit(id, "delete"); err != nil {
		return err
	}
	if err := c.refuseDeclaredEdit(ctx, id, "delete"); err != nil {
		return err
	}
	existing, err := c.Store.GetTemplate(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return &templateNotFoundError{ID: id}
	}
	// Every kind of dependent is gathered before refusing, so one refusal
	// says everything standing in the way: refusing on the schedules
	// alone sent a human off to repoint them, only to be refused again on
	// the suite nobody had mentioned (grain/task-2415).
	var uses, remedies []string
	inUse, err := c.Store.SchedulesUsingTemplate(ctx, id)
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
		uses = append(uses, fmt.Sprintf("%d schedule(s)%s", len(inUse), which))
		remedies = append(remedies, "repoint or delete the schedules")
	}
	usedBy, err := c.Store.QualificationPlansUsingTemplate(ctx, id)
	if err != nil {
		return err
	}
	if len(usedBy) > 0 {
		uses = append(uses, fmt.Sprintf("%d repo's qualification plan", len(usedBy)))
		remedies = append(remedies, "remove it from the qualification plans")
	}
	suites, err := c.Store.SuitesUsingTemplate(ctx, id)
	if err != nil {
		return err
	}
	if len(suites) > 0 {
		named := make([]dependent, len(suites))
		for i, suite := range suites {
			named[i] = dependent{name: firstNonEmpty(suite.Name, suite.ID)}
		}
		which, err := c.nameDependents(ctx, named)
		if err != nil {
			return err
		}
		uses = append(uses, fmt.Sprintf("%d suite(s)%s", len(suites), which))
		remedies = append(remedies, "remove it from the suites")
	}
	switch {
	case len(uses) > 1:
		return validationErrorf("template is used by %s; %s first",
			joinAnd(uses), joinAnd(remedies))
	case len(inUse) > 0:
		return validationErrorf("template is used by %s; repoint or delete those first", uses[0])
	case len(usedBy) > 0:
		return validationErrorf("template is used by %s; remove it from those first", uses[0])
	case len(suites) > 0:
		return validationErrorf("template is used by %s; remove it from those first", uses[0])
	}
	reviewed, err := c.Store.TasksReviewedByTemplate(ctx, id)
	if err != nil {
		return err
	}
	if len(reviewed) > 0 {
		return validationErrorf(
			"template is the review attached to %d open task(s); detach it from those first",
			len(reviewed))
	}
	return c.Store.DeleteTemplate(ctx, id)
}

// refuseUnattendedUnfillable refuses fields for template id when a
// schedule or suite already fires it and could not fill one of them
// (model.ValidateUnattendedTemplate) -- the same rule CreateSchedule and
// CreateSuite apply at attach time, asked again here because a template
// can gain a required, default-less field long after it was attached.
func (c *Client) refuseUnattendedUnfillable(ctx context.Context, id string, fields []model.TemplateField) error {
	if _, ok := model.UnfillableRequiredField(fields); !ok {
		return nil
	}
	schedules, err := c.Store.SchedulesUsingTemplate(ctx, id)
	if err != nil {
		return err
	}
	if len(schedules) > 0 {
		named := make([]dependent, len(schedules))
		for i, sched := range schedules {
			named[i] = dependent{name: firstNonEmpty(sched.Title, sched.ID)}
		}
		which, err := c.nameDependents(ctx, named)
		if err != nil {
			return err
		}
		return validationErrorf("%s", model.ValidateUnattendedTemplate(fields,
			fmt.Sprintf("the %d schedule(s)%s firing this template", len(schedules), which)))
	}
	suites, err := c.Store.SuitesUsingTemplate(ctx, id)
	if err != nil {
		return err
	}
	if len(suites) > 0 {
		named := make([]dependent, len(suites))
		for i, suite := range suites {
			named[i] = dependent{name: firstNonEmpty(suite.Name, suite.ID)}
		}
		which, err := c.nameDependents(ctx, named)
		if err != nil {
			return err
		}
		return validationErrorf("%s", model.ValidateUnattendedTemplate(fields,
			fmt.Sprintf("the %d suite(s)%s running this template", len(suites), which)))
	}
	return nil
}

// dependent is one schedule or suite that refuses a delete, as
// nameDependents names it.
type dependent struct{ name string }

// nameDependents is the part of a refused delete that says which
// schedules or suites are in the way -- ` ("Nightly", "Weekly")` -- so
// whoever is told to repoint or delete them first is not left to search
// every list for them.
func (c *Client) nameDependents(ctx context.Context, deps []dependent) (string, error) {
	var names []string
	for _, d := range deps {
		names = append(names, strconv.Quote(d.name))
	}
	return " (" + strings.Join(names, ", ") + ")", nil
}

// --- handlers ------------------------------------------------------------

func (s *Server) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	list, err := s.tasks.ListTemplates(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleTemplateComposition takes the repo, the branch and the values
// for the template's input fields as query parameters rather than a
// body, for handleSuiteComposition's own reason: it is a question, not a
// change, and a form re-asking it as somebody types wants a URL it can
// cache and cancel.
//
// One field per `field.<name>` parameter -- the spelling a task's own
// filter links already use for a declared field (ui/src/paths.js) --
// rather than one packed parameter, so a value containing a comma, an
// equals sign or a newline needs nothing but ordinary escaping.
func (s *Server) handleTemplateComposition(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	values := map[string]string{}
	for key := range q {
		if name, ok := strings.CutPrefix(key, "field."); ok && name != "" {
			values[name] = q.Get(key)
		}
	}
	composition, err := s.tasks.TemplateComposition(
		r.Context(), r.PathValue("id"), q.Get("repo"), q.Get("base"), values, q.Get("approved") != "false")
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, composition)
}

func (s *Server) handleCreateTemplate(w http.ResponseWriter, r *http.Request) {
	var req CreateTemplateRequest
	if !readJSONRefusing(w, r, &req, retiredTeamField) {
		return
	}
	tmpl, err := s.tasks.CreateTemplate(r.Context(), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, tmpl)
}

func (s *Server) handleUpdateTemplate(w http.ResponseWriter, r *http.Request) {
	if err := s.tasks.authorizeTemplateEdit(r.Context(), r.PathValue("id")); err != nil {
		writeClientError(w, err)
		return
	}
	var req UpdateTemplateRequest
	if !readJSONRefusing(w, r, &req, retiredTeamField) {
		return
	}
	tmpl, err := s.tasks.UpdateTemplate(r.Context(), r.PathValue("id"), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tmpl)
}

func (s *Server) handleDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	if err := s.tasks.DeleteTemplate(r.Context(), r.PathValue("id")); err != nil {
		writeClientError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
