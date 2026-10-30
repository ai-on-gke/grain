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

package cli

import (
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// taskattributes.go is what `grain list` knows how to ask about: every
// attribute a -filter condition or a -sort key can name, and the type
// that decides which comparisons apply to it. It is the Go half of
// ui/src/taskAttributes.js, deliberately the same vocabulary under the
// same ids -- so "repo:any:acme/widgets" means one thing whether it is
// read off a link or typed after -filter.
//
// # Why there are two tables and not one
//
// The obvious alternative is one table, in Go, served on GET /api/config
// for the frontend to build its menus from. It is not what this does,
// for two reasons. Most of what a descriptor holds is a *function* --
// how to read the attribute off a task, what to call one of its values,
// which glyph its menu row wears -- and none of that crosses an API;
// what could be sent is the ids, the types and the labels, which is the
// small half. And the two halves are asked in different places: the
// frontend narrows a list it already holds, on every keystroke, with no
// round trip in it, and would have to hold the table anyway to do that.
// Serving it would leave the same functions written on both sides plus a
// wire format between them.
//
// So the table is written twice and *checked* once: TestTaskAttributes
// MatchTheFrontend (taskattributes_test.go) reads taskAttributes.js and
// taskFilters.js off disk and fails when either side grows an attribute,
// a type or a comparison the other does not have. That is the property
// worth having -- neither surface quietly drifting into asking a
// question the other cannot -- and it costs a test rather than an
// endpoint.
//
// What is deliberately not mirrored: labels and glyphs. Nothing here
// draws a menu, and a CLI's values are the ids a person types, not the
// words a menu shows. The one place the frontend compares against a
// label rather than a value is "contains" over a text attribute, and no
// text attribute (Title, Pull request, Task) has a label distinct from
// its value, so the two agree anyway.
//
// # The attributes a deployment declares
//
// The table below is the built-ins. What a deployment adds to them is a
// custom field (model.CustomField): a name, a label, a type and, for an
// enum, its options, written down in its state repository and served on
// GET /api/config. A task's own values for them ride on the task, so a
// declaration is exactly the two halves an attribute needs -- a
// vocabulary, and a way to read one task -- and attributesFor builds one
// per declared field, under custom.<name>, the same seam
// attributesFor(config) is on the other side (grain/task-99).
//
// That makes the vocabulary a *value* rather than a package-level table:
// what `grain list` can be asked depends on the deployment it is asked
// of, so a condition is parsed against an attributeSet, and cmdList
// fetches the declarations before parsing when -- and only when -- the
// command line names something the built-ins do not have.

// attrType is what an attribute's values *are*, which is the whole of
// what decides the comparisons it offers (taskfilters.go's operators).
// The five are taskAttributes.js's own TYPE_* constants, by their own
// spellings, since those are what the sync test compares.
type attrType string

const (
	typeEnum   attrType = "enum"   // one value out of a vocabulary
	typeTags   attrType = "tags"   // any number of values at once
	typeText   attrType = "text"   // free text nobody picks from a menu
	typeDate   attrType = "date"   // a moment
	typeNumber attrType = "number" // a count
)

// attribute is one thing a task can be narrowed or ordered by.
//
//	id        what a -filter condition, a -sort key and the UI's own URL
//	          all name it by. Stable, and the same string on both sides.
//	typ       which comparisons it takes.
//	values    every value one task has for it -- none for a task that
//	          has none, more than one for the multi-valued ones. The
//	          only thing here that reads a task.
//	order     the vocabulary, for the attributes whose values are a
//	          fixed set rather than whatever the deployment's tasks
//	          happen to hold. It is what a value is checked against, so
//	          `-filter origin:any:robot` is answered with the words that
//	          work; the open-ended ones (a repo, an author) are checked
//	          against nothing, since the CLI cannot know a deployment's
//	          repos without asking and a filter that matches nothing is
//	          a real answer rather than a typo.
//	noneLabel what "has no value for this" is called -- non-empty for
//	          the attributes where having none is a real answer, which
//	          is also what decides whether "empty"/"filled" are offered
//	          and whether "none" is an acceptable value.
//	key       what to sort by, when it is not the first value: a state
//	          sorts by rank and an id as a number, neither of which is
//	          what comparing their text would do.
//	custom    whether this is a field the deployment declared rather than
//	          one every grain has. Two things turn on it: a date field
//	          holds a calendar day rather than a timestamp, so reading
//	          one needs the deployment's own zone (taskQuery.needsZone),
//	          and the help says which attributes this deployment added.
type attribute struct {
	id        string
	typ       attrType
	values    func(ui.Task) []string
	order     []string
	noneLabel string
	key       func(ui.Task) sortKey
	// aliases are values a condition may name beyond order, each
	// standing for several of them: the state attribute's "asked", the
	// one "Asked" the UI shows awaiting_reply and awaiting_submit as
	// (taskAttributes.js's expand), so a link carrying it parses here too.
	aliases map[string][]string
	custom  bool
	// legacy is whether one task is carrying a value for this attribute
	// that its declaration has moved on from
	// (model.CustomField.LegacyValues) -- nil for an attribute where
	// the question does not arise, which is every built-in, since their
	// vocabularies are this build's own rather than a deployment's.
	// Carrying one is what offers the "legacy" comparison.
	legacy func(ui.Task) bool
	// label is what the deployment calls a custom field on screen
	// (model.CustomField.DisplayName) -- carried only so `grain list -h`
	// can say which of its own fields a deployment declared under which
	// id, since nothing else here would tell somebody that custom.due is
	// the one called "Due date". Empty for the built-ins, whose labels
	// are the frontend's business.
	label string
	// people is whether every value is a person's name
	// (taskAttributes.js's own `people`): those can be matched on part of
	// a name as well as picked whole, "user:has:sam".
	people bool
}

// attributeSet is the vocabulary of one invocation: the built-ins, plus
// one attribute per field the deployment declares. Every parse goes
// through one, since "is custom.priority an attribute" is a question
// only a deployment can answer.
type attributeSet []attribute

// canBeEmpty is whether "has no value at all" is a real answer for this
// attribute, and so whether "empty"/"filled" are worth offering and
// "none" is worth accepting as a value. taskFilters.js's own canBeEmpty,
// to the letter: an attribute that says what having none is called is
// saying exactly that, and a date or a number is absent often enough to
// be worth asking about either way.
func (a *attribute) canBeEmpty() bool {
	return a.noneLabel != "" || a.typ == typeDate || a.typ == typeNumber
}

// taskStates is every state a task can be in, in the order the UI's own
// sidebar and `-sort state` put them (ui/src/state.js's STATE_ORDER):
// roughly a task's life from proposal to close. It is also the state
// attribute's vocabulary, so a typo is answered with the list rather
// than with an empty listing that looks like a deployment with no such
// tasks.
var taskStates = []model.State{
	model.StateProposed,
	model.StateQueued,
	model.StateRunning,
	model.StateAwaitingReply,
	model.StateFailed,
	model.StateAwaitingSubmit,
	model.StateCompleted,
	model.StateDeferred,
	model.StateClosed,
}

// taskOrigins is every way a task can come to exist, and what the origin
// attribute calls each -- the same set the UI's own Origin menu offers,
// read off the same fields. A task carrying more than one marker is
// reported as the most specific of them, which is why the order below is
// the order taskOrigin tests them in: a merge fix is always generated
// from another task, so calling it "proposed" would say the less
// specific of two true things.
var taskOrigins = []string{"hand", "external", "proposed", "schedule", "suite", "fix", "review"}

func taskOrigin(t ui.Task) string {
	switch {
	// Review before fix: both are stacked on another task's own branch,
	// and only one of them is a repair of a red build (ui.Task.Review).
	case t.Review:
		return "review"
	case t.Stacked:
		return "fix"
	case t.Scheduled:
		return "schedule"
	case t.SuiteRun:
		return "suite"
	case t.GeneratedFrom != "":
		return "proposed"
	// An external agent's credential files as the deployment's person
	// (Creator) but is the author (model.ExternalAgentPrincipalPrefix).
	case strings.HasPrefix(t.Author, model.ExternalAgentPrincipalPrefix):
		return "external"
	}
	return "hand"
}

// one is the shape almost every `values` below takes: a task has this
// attribute or it does not.
func one(pick func(ui.Task) string) func(ui.Task) []string {
	return func(t ui.Task) []string {
		if v := pick(t); v != "" {
			return []string{v}
		}
		return nil
	}
}

// builtinAttributes is the vocabulary every deployment has, in
// taskAttributes.js's own order -- which is the order the editor's menu
// offers them in and the order `grain list -h` prints them in. What a
// deployment adds to them is attributesFor's business.
var builtinAttributes = attributeSet{
	{
		id:     "state",
		typ:    typeEnum,
		values: one(func(t ui.Task) string { return string(t.State) }),
		order:  stateNames(),
		key:    func(t ui.Task) sortKey { return numberKey(float64(stateRank(t.State))) },
		aliases: map[string][]string{
			"asked": {string(model.StateAwaitingReply), string(model.StateAwaitingSubmit)},
		},
	},
	{
		// The repo this task's own runs push to, which is the same repo
		// a repo's own page scopes its copy of the task list by: a task
		// that only reads a repo is not one of that repo's tasks. The
		// repos it reads and the other repos its work may land in are
		// "reads" and "writes", below.
		id:        "repo",
		typ:       typeEnum,
		values:    one(func(t ui.Task) string { return t.Repo }),
		noneLabel: "No repo",
	},
	{
		id:     "title",
		typ:    typeText,
		values: one(func(t ui.Task) string { return t.Title }),
	},
	{
		id:        "capability",
		typ:       typeTags,
		values:    func(t ui.Task) []string { return t.Capabilities },
		noneLabel: "No capabilities",
	},
	{
		id:        "base",
		typ:       typeEnum,
		values:    one(func(t ui.Task) string { return t.Base }),
		noneLabel: "Repo default",
	},
	{
		id:        "reads",
		typ:       typeTags,
		values:    func(t ui.Task) []string { return t.Reads },
		noneLabel: "No read-only repos",
	},
	{
		// The repo names alone, though a write target is a repo *and* a
		// branch (ui.Task.Writes): "which tasks may land work in
		// acme/widgets" is the question asked of this attribute, and
		// "acme/widgets@deploy" would be a value nobody could type
		// without being shown it first. The branch stays on the task's
		// own page, where it is chosen.
		//
		// Its own attribute rather than folded into "repo", which is
		// still the single repo a task's own runs push to -- everything
		// that may land work in one repo is the two conditions
		// together.
		id:  "writes",
		typ: typeTags,
		values: func(t ui.Task) []string {
			var repos []string
			for _, w := range t.Writes {
				if w.Repo != "" {
					repos = append(repos, w.Repo)
				}
			}
			return repos
		},
		noneLabel: "No other writeable repos",
	},
	{
		id:     "author",
		typ:    typeEnum,
		values: one(func(t ui.Task) string { return t.Author }),
		people: true,
	},
	{
		id:     "creator",
		typ:    typeEnum,
		values: one(func(t ui.Task) string { return t.Creator }),
		people: true,
	},
	{
		// Either of the two above: the name the task was filed under,
		// and its author when that is a person rather than an agent's run
		// or the deployment itself -- once, when they are the same.
		id:     "user",
		typ:    typeEnum,
		values: taskUsers,
		people: true,
	},
	{
		id:     "origin",
		typ:    typeEnum,
		values: func(t ui.Task) []string { return []string{taskOrigin(t)} },
		order:  taskOrigins,
	},
	{
		id:  "kind",
		typ: typeEnum,
		values: func(t ui.Task) []string {
			if t.Interactive {
				return []string{"interactive"}
			}
			return []string{"background"}
		},
		order: []string{"background", "interactive"},
	},
	{
		id:  "autoMerge",
		typ: typeEnum,
		values: func(t ui.Task) []string {
			if t.AutoMerge {
				return []string{"on"}
			}
			return []string{"off"}
		},
		order: []string{"on", "off"},
	},
	{
		id:  "blocked",
		typ: typeEnum,
		values: func(t ui.Task) []string {
			if t.Blocked {
				return []string{"yes"}
			}
			return []string{"no"}
		},
		order: []string{"yes", "no"},
	},
	{
		// Carried as the RFC3339 text the API sent, and parsed back by
		// whatever compares it -- the same bargain the frontend makes
		// with an ISO string, and what keeps `values` one shape for
		// every attribute.
		id:  "created",
		typ: typeDate,
		values: one(func(t ui.Task) string {
			if t.CreatedAt == nil {
				return ""
			}
			return t.CreatedAt.Format(time.RFC3339Nano)
		}),
		key: func(t ui.Task) sortKey {
			if t.CreatedAt == nil {
				return missingKey
			}
			return numberKey(float64(t.CreatedAt.UnixNano()))
		},
	},
	{
		id:  "started",
		typ: typeDate,
		values: one(func(t ui.Task) string {
			if t.StartedAt == nil {
				return ""
			}
			return t.StartedAt.Format(time.RFC3339Nano)
		}),
		key: func(t ui.Task) sortKey {
			if t.StartedAt == nil {
				return missingKey
			}
			return numberKey(float64(t.StartedAt.UnixNano()))
		},
	},
	{
		id:  "completed",
		typ: typeDate,
		values: one(func(t ui.Task) string {
			if t.EndedAt == nil {
				return ""
			}
			return t.EndedAt.Format(time.RFC3339Nano)
		}),
		key: func(t ui.Task) sortKey {
			if t.EndedAt == nil {
				return missingKey
			}
			return numberKey(float64(t.EndedAt.UnixNano()))
		},
	},
	// When the task's pull request merged -- the Merged panel's own
	// order (ui/src/taskFilters.js's defaultSortFor). The list shape
	// carries the merge as PrMergedAt alone; the frontend's fallbacks
	// read fields only TaskDetail has.
	{
		id:  "merged",
		typ: typeDate,
		values: one(func(t ui.Task) string {
			if t.PrMergedAt == nil {
				return ""
			}
			return t.PrMergedAt.Format(time.RFC3339Nano)
		}),
		key: func(t ui.Task) sortKey {
			if t.PrMergedAt == nil {
				return missingKey
			}
			return numberKey(float64(t.PrMergedAt.UnixNano()))
		},
	},
	{
		id:        "pullRequest",
		typ:       typeText,
		values:    one(func(t ui.Task) string { return t.PullRequest }),
		noneLabel: "No pull request",
	},
	{
		// The review attached to a task, by the name of the template it
		// names -- what the deployment calls it and what the task's own
		// page shows. A task whose template has since been deleted
		// reports none, the same as a task that never had one.
		id:        "review",
		typ:       typeEnum,
		values:    one(func(t ui.Task) string { return t.ReviewTemplateName }),
		noneLabel: "No review",
	},
	{
		id:        "framework",
		typ:       typeEnum,
		values:    one(func(t ui.Task) string { return t.AgentFramework }),
		noneLabel: "Deployment default",
	},
	{
		id:        "dependsOn",
		typ:       typeTags,
		values:    func(t ui.Task) []string { return t.DependsOn },
		noneLabel: "Nothing",
	},
	{
		id:     "id",
		typ:    typeText,
		values: one(func(t ui.Task) string { return t.ID }),
		key:    func(t ui.Task) sortKey { return idKey(t.ID) },
	},
}

// customPrefix namespaces every deployment-defined attribute, so
// declaring a field called "repo" adds custom.repo rather than quietly
// replacing the built-in of that name. taskAttributes.js's own
// CUSTOM_PREFIX, since the two have to write the same ids into the same
// links.
const customPrefix = "custom."

// customNoneLabel is what "nobody filled this in" is called for a
// declared field -- the frontend's NONE_LABEL_FALLBACK. Nothing here
// prints it; carrying one is how an attribute says that having no value
// is a real answer, which is what offers "empty"/"filled" and accepts
// "none" as a value. Every custom field says so, since no field is
// required (model.CustomField.NormalizeValue).
const customNoneLabel = "Not set"

// customFieldTypes maps what a deployment may declare
// (model.CustomFieldTypes) onto what this command can compare. Four of
// the five are the same idea under the same name; a boolean is an enum
// of two values, since "is any of true" is the question somebody asks
// about one. taskAttributes.js's FIELD_TYPES, which has to agree: a
// field compared as a date in the browser and as text here would answer
// the same link two ways.
//
// A type this build has never heard of -- a newer daemon offering one --
// reads as text, the type that assumes least, rather than dropping the
// field out of the vocabulary entirely.
var customFieldTypes = map[model.CustomFieldType]attrType{
	model.CustomFieldText:   typeText,
	model.CustomFieldNumber: typeNumber,
	model.CustomFieldEnum:   typeEnum,
	model.CustomFieldDate:   typeDate,
	model.CustomFieldBool:   typeEnum,
}

// booleanValues is the two values of a boolean field, as the store holds
// them (model.CustomField.NormalizeValue) and as this command's own
// -filter takes them. The frontend's BOOLEAN_ORDER: it writes them "Yes"
// and "No" in a menu, and a menu is the half a CLI does not have.
var booleanValues = []string{"true", "false"}

// customAttribute is one declared field as an attribute.
//
// A field declared `multiple` (model.CustomField.Multiple) is a tags
// attribute whatever its own type is, since "several values at once" is
// what that means here -- so it takes "is any of", "is none of" and "has
// all of", the questions worth asking of a set. Its declared type still
// decides how its values are *read*: a multi-valued enum keeps its
// options as its vocabulary, which is what `order` below says.
//
// An enum's options are its vocabulary, so `-filter
// custom.priority:any:urgent` is refused with the options that exist,
// the same way `-state in_progress` is -- and, since options are ordered
// deliberately ("low, medium, high" is not alphabetical), they are also
// what `-sort custom.priority:asc` ranks by.
// The comparison no built-in offers is "is a legacy value": whether
// this task is still carrying something the declaration has moved on
// from -- a retired enum option, a set left behind by a field that has
// stopped holding one. A value like that stays on the task until
// somebody edits it off (model.NormalizeCustomFieldValuesFrom), so
// `grain list -filter custom.priority:legacy` is how the tasks a merged
// declaration stranded are found without already knowing which value to
// ask about.
func customAttribute(f ui.CustomFieldDeclaration) attribute {
	declared := model.CustomFieldType(f.Type)
	valueType, known := customFieldTypes[declared]
	if !known {
		valueType = typeText
	}
	typ := valueType
	if f.Multiple {
		typ = typeTags
	}
	// The declaration as the model states one, which is what answers
	// "would this still be produced today" -- asked of model.CustomField
	// rather than of a second copy of each type's rules here, so this
	// command and the deployment cannot disagree about one value.
	field := model.CustomField{
		Name:     f.Name,
		Label:    f.Label,
		Type:     declared,
		Options:  f.Options,
		Multiple: f.Multiple,
	}
	a := attribute{
		id:        customPrefix + f.Name,
		typ:       typ,
		custom:    true,
		label:     f.Label,
		noneLabel: customNoneLabel,
		values: func(t ui.Task) []string {
			var out []string
			for _, v := range t.CustomFields[f.Name] {
				// A value that is empty text is no value: nothing stores
				// one (model.NormalizeCustomFieldValues drops it), and
				// the frontend reads one the same way.
				if v != "" {
					out = append(out, v)
				}
			}
			return out
		},
		legacy: func(t ui.Task) bool {
			return len(field.LegacyValues(t.CustomFields[f.Name])) > 0
		},
	}
	switch {
	case declared == model.CustomFieldBool:
		a.order = booleanValues
	case valueType == typeEnum:
		a.order = f.Options
	}
	if a.label == "" {
		a.label = f.Name
	}
	return a
}

// attributesFor is the vocabulary as of these declarations: the
// built-ins, then one attribute per field the deployment declares, in
// the order it declares them (model.SortCustomFields -- position, then
// name), which is the order they are printed and offered in on the other
// side too.
//
// A declaration with no name is skipped rather than added as "custom.":
// nothing can store one, and an id nobody could type is worse than an
// attribute that is simply absent.
func attributesFor(fields []ui.CustomFieldDeclaration) attributeSet {
	if len(fields) == 0 {
		return builtinAttributes
	}
	attrs := make(attributeSet, 0, len(builtinAttributes)+len(fields))
	attrs = append(attrs, builtinAttributes...)
	for _, f := range fields {
		if f.Name == "" {
			continue
		}
		attrs = append(attrs, customAttribute(f))
	}
	return attrs
}

// byID is the descriptor one condition or sort key names, or nil for a
// name this deployment has never heard of. Unlike the frontend -- which
// drops an unknown clause so that a stale link still opens the list it
// names -- the CLI answers with an error and the ids that work: a flag
// is typed rather than followed, and a typo silently narrowing nothing
// is the failure this command already refuses for -state.
func (s attributeSet) byID(id string) *attribute {
	for i := range s {
		if s[i].id == id {
			return &s[i]
		}
	}
	return nil
}

func (s attributeSet) ids() []string {
	ids := make([]string, 0, len(s))
	for i := range s {
		ids = append(ids, s[i].id)
	}
	return ids
}

// custom is the attributes this deployment declared, which is what the
// help prints under a heading of their own -- the built-ins are the same
// everywhere and these are not.
func (s attributeSet) custom() attributeSet {
	var out attributeSet
	for _, a := range s {
		if a.custom {
			out = append(out, a)
		}
	}
	return out
}

func stateNames() []string {
	names := make([]string, 0, len(taskStates))
	for _, s := range taskStates {
		names = append(names, string(s))
	}
	return names
}

// stateRank is where taskStates says a state goes. A state this build
// has never heard of sorts after all of them rather than before, so a
// newer daemon's unknown state does not quietly take over the top of the
// listing.
func stateRank(s model.State) int {
	for i, known := range taskStates {
		if s == known {
			return i
		}
	}
	return len(taskStates)
}

// idKey sorts task ids as the numbers they are ("9" before "10"), with
// anything that is not a number sorting as text after them -- the
// frontend's own idKey.
func idKey(id string) sortKey {
	if n, err := strconv.ParseFloat(strings.TrimSpace(id), 64); err == nil && strings.TrimSpace(id) != "" {
		return numberKey(n)
	}
	if strings.TrimSpace(id) == "" {
		return missingKey
	}
	return textKey(id)
}

// taskUsers is the people a task belongs to, for the "user" attribute:
// its creator, and its author when the author is human.
func taskUsers(t ui.Task) []string {
	var out []string
	if t.Creator != "" {
		out = append(out, t.Creator)
	}
	human := t.AuthorKind == "" || t.AuthorKind == string(model.PrincipalHuman)
	if t.Author != "" && human && t.Author != t.Creator {
		out = append(out, t.Author)
	}
	return out
}
