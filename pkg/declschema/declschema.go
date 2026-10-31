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

// Package declschema is the one definition of what a template, a suite
// and a schedule are made of (grain/task-339).
//
// The same three things are written down in two places today. Grain's
// own settings hold them as rows, exported to the state repository as a
// database dump -- flat, id-keyed, every relation in its own join table
// (pkg/staterepo, tables/template.json). A repository holds them as
// hand-written YAML under `.grain` -- nested, name-keyed, hyphenated,
// and deliberately smaller (pkg/repodecl). Both are maintained by hand,
// which is how a field lands in one and not the other: `template_field`
// has been a column, a table and a row in the dump since grain/task-58,
// and nothing in `.grain` could ask for one until this package made
// somebody answer for it.
//
// So this is a list of fields, and for each of them the keys it is
// written under, which contexts may write it, and where the export
// carries it. It is not a decoder and it is not a struct to unmarshal
// into: pkg/repodecl's typed structs stay exactly where they were, for
// the reason its own file gives -- "the file is shaped for whoever
// writes it, hyphens and all, and the type is shaped for its readers".
// What this package does is make those two shapes answerable to one
// list, so that adding a field to model.Template is a change that does
// not compile until it says which contexts accept the field and what
// the ones that refuse it are to tell somebody who writes it anyway.
//
// # Two contexts, one definition
//
// The two places are not two dialects of this format, they are two
// contexts of it:
//
//   - Settings is grain's own: the store, the UI, and the settings
//     export a state repository round-trips. It carries every field
//     there is.
//   - Repo is a repository's own `.grain` directory. It carries the
//     subset a repository is allowed to say about itself.
//
// The difference between them is the whole of the security argument in
// model/repo_declaration.go, and it is why the two are contexts of one
// definition rather than two formats that resemble each other. A field
// the repo context does not carry is not a field somebody forgot: it is
// a refusal, with a reason (Why), which pkg/repodecl says out loud to
// whoever writes the key. A design where the repo layer quietly ignored
// what it does not honour would turn `grants: [gcp-key]` from an error
// into a silent drop, and the fields in question are capabilities and
// target repositories.
//
// # What is enforced, and how
//
// Two halves, both tested rather than generated:
//
//   - Against the model. Every exported field of model.Template,
//     model.Suite and model.Schedule is named by exactly one Field here,
//     and every Field names a real one. A field added to the model with
//     no entry here fails TestEverySchemaFieldIsAModelFieldAndBack, and
//     the entry it has to add is what forces the decision.
//   - Against pkg/repodecl. The keys the repo context carries are
//     exactly the yaml tags on that package's raw structs, and the keys
//     it refuses are read straight out of this list at parse time --
//     there is no second copy of the refused set to fall out of step
//     with, only the accepted set, which a test in that package pins.
//
// # The fourth thing: what a repository may say about *itself*
//
// Templates, suites and schedules are work -- what to do in a repository
// and when. A repository's own settings are a different kind of thing:
// they are the row grain already keeps about it (model.RepoConfig), the
// per-repository layer of the deployment-wide model.Config, and until
// grain/task-340 a `.grain/settings.yml` could write every field of it.
// It is the fourth schema here (RepoSettingsSchema) for the same reason
// the other three are here at all -- one list, answerable to the model,
// so that a field added to RepoConfig cannot land with nobody having
// decided whether a repository may write it.
//
// The rule that decides is short, and it is deliberately not the obvious
// one. The obvious rule is "a repository may declare anything that only
// affects itself", and it is wrong: by that test a repository could hand
// its own tasks `gcp-key`, which affects nothing but its own tasks and
// is precisely the thing that must never happen. The line is
// **authority, not behaviour**.
//
// A repository already decides what *code* runs. Its `.grain/workflows`
// are in it, its tests are in it, its setup script runs its own tree,
// and every agent grain dispatches there can rewrite all three. Nobody
// is protected from a repository's code by keeping a setting out of that
// repository, so a setting that only decides what the repository's own
// work *does* may as well be written where the work is. What a
// repository must never decide is the authority that code runs with:
// which capabilities are granted and which secrets resolve (Authority),
// whether its own verdicts count and what this deployment spends
// reaching one (Verdict), and whether any of its word is read at all
// (Bootstrap).
//
// So, in one sentence, and this sentence is the deliverable rather than
// the list below it:
//
//	A setting is declarable when it changes what a repository's own
//	work does, and never when it changes what that work is permitted
//	to do, who judges it, or whether the repository is believed.
//
// Two fields pass that test today -- `prompt-extension` and
// `setup-command`, both of them instructions to an agent standing in a
// tree it can already rewrite -- and every other field of RepoConfig
// fails it, each saying in RepoSettingsSchema which of the three reasons
// it fails for. Somebody adding a field to RepoConfig answers the
// question in the same place, or the build stops
// (TestEverySchemaFieldIsAModelFieldAndBack).
//
// # A declarable field is added to the operator's, never substituted
//
// The second half of the rule is Compose. A repository's layer unions or
// appends onto the deployment's and can never reduce it, which is
// already how the three settings with a repo dimension compose
// (Config.DefaultCapabilities, Config.PromptExtension,
// Config.IgnoreGitHubCI each say so in their own doc comments). A layer
// that could subtract would be a repository re-enabling something a
// deployment has switched off, or quietly discarding what was set
// deployment-wide -- and for the prompt extension in particular it would
// be an agent-writable file overwriting a settings box a person
// maintains, which model.Memory's doc comment is careful is exactly the
// thing that must not happen. So the repository's value lands in a
// column of its own (Field.Declared), written only by the declaration
// sync and never by the settings UI, and the two are composed where they
// are read (RepoConfig.RepoPromptExtension, RepoConfig.SetupScript).
//
// The export's own half is pinned too: Columns names the columns of the
// kind's own table, and a test checks them against pkg/model's DDL both
// ways, so a column added to `schedule` has to be a field here as well.
// The companion tables a template's relations live in are named by
// Table rather than flattened, which is what tells the exporter which
// lists to nest -- pkg/staterepo/decl.go reads this list to write
// settings/templates.yml and to read one back, and refuses to start on a
// field whose shape in the file nobody has decided.
//
// docs/declaration-schema.md is the argument in full: which direction
// the unification goes (the authoring format wins -- the JSON is a dump,
// the YAML is a format people write), which identity a unified format
// uses (name-keyed in a repository, id-keyed in settings, and never a
// value anybody types), what a rename does on each side, and what
// protects the `system:` rows.
package declschema

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Context is one of the two places one of these things is written.
//
// A set rather than an enum: a key carried in both is the ordinary case,
// and the interesting entries are the ones carried in one.
type Context uint8

const (
	// Settings is grain's own -- the store, the UI, and the settings
	// export. It carries everything.
	Settings Context = 1 << iota
	// Repo is a repository's own `.grain` declaration.
	Repo
)

// Everywhere is both of them, which is what most keys are.
const Everywhere = Settings | Repo

// Has reports whether c includes one.
func (c Context) Has(one Context) bool { return c&one != 0 }

func (c Context) String() string {
	switch c {
	case Settings:
		return "settings"
	case Repo:
		return "repo"
	case Everywhere:
		return "settings and repo"
	case 0:
		return "nowhere"
	default:
		return "unknown"
	}
}

// Why is the reason a context does not carry a key -- the reason
// pkg/repodecl gives back when a file sets one anyway.
//
// There is no free-text option, deliberately. A refusal that can say
// anything ends up saying something slightly different in each of the
// places that refuse, so there is one sentence per reason and every
// refusal in grain picks one of them.
//
// They fall into two groups, which is the two questions this package
// answers. Four are about the work a repository declares -- you may not
// say that here, the template says that, the file name says that, or
// grain keeps that. Three are about the settings a repository declares
// for itself, and they are the whole of grain/task-340's rule: a
// repository does not grant its own authority, does not judge itself,
// and does not switch on the reading of its own word.
type Why string

const (
	// Carried is the zero value: nothing refuses this key.
	Carried Why = ""
	// Reach is the one the whole feature rests on -- the key would let a
	// repository's own file say what the work it declares may touch.
	Reach Why = "reach"
	// Authority is Reach in the settings file: the key would let a
	// repository decide what authority its own work runs with --
	// capabilities, the secrets a job resolves, the tree grain clones for
	// it. It is the reason the line is drawn at authority rather than at
	// behaviour, and the single thing this whole design exists to refuse.
	Authority Why = "authority"
	// Verdict is the subtler half: the key would let a repository decide
	// whether its own checks are believed, or what this deployment spends
	// finding out. A repository that can say its failures do not judge it
	// has exempted itself from review as surely as one that can grant
	// itself a capability.
	Verdict Why = "verdict"
	// Bootstrap is the one that looks like an oversight if it is not said
	// out loud: the switch meaning "this repository's own word is good
	// enough to read" can never itself be something the repository says.
	Bootstrap Why = "bootstrap"
	// Ownership is the odd one out, and the only refusal here that is
	// not about trusting the repository at all: the key asks which
	// *deployment* runs this repository's standing work, and a file in
	// the repository cannot know. Several deployments may be looking at
	// one repository, and only one of them should be cutting its
	// releases (grain/task-486).
	Ownership Why = "ownership"
	// Inline is the schedule's own: the work belongs to the template it
	// fires, so a declared schedule carries no content of its own.
	Inline Why = "inline"
	// Identity is derived from the file, so no file says it.
	Identity Why = "identity"
	// Kept is grain's record of a thing already running -- written by the
	// daemon, overwritten on the next poll.
	Kept Why = "kept"
)

// Reason is what a repository that sets such a key is told.
//
// One sentence per Why, said in one place, because the alternative is
// six refusals that each explain the rule slightly differently.
func (w Why) Reason() string {
	switch w {
	case Reach:
		return "a repository's own declarations say what work to do in it and when, " +
			"never what that work may touch -- a task filed from this file targets this repository, " +
			"holds the capabilities this deployment already gives tasks here, and reaches another " +
			"repository only where a template says `capabilities: [\"repo-read:<owner>/<repo>\", " +
			"\"repo-write:<owner>/<repo>\"]`, which its team's own GitHub credential then has to allow. " +
			"A template that needs more than that belongs in grain's own settings"
	case Authority:
		return "a repository says what its own work does, never what that work is allowed to do: " +
			"capabilities, the secret names a job may resolve, the tree grain clones and the " +
			"repository a row is about are settled in grain's own settings, by somebody who can see " +
			"every repository they are settled for. " +
			"Keeping this key out of the repository is the whole of why the rest of the file can be " +
			"trusted, so a repository that needs more than it has been given asks for it there"
	case Verdict:
		return "whether this repository's checks are believed, and what this deployment spends " +
			"reaching a verdict on them, is not the repository's to declare -- a repository that " +
			"could say its own failures do not judge it, or that grain should stop paying to find " +
			"out, would be grading its own homework. This one belongs in grain's own settings"
	case Ownership:
		return "this says whether *this deployment* runs this repository's standing work -- its " +
			"schedules and its release -- and that is not a fact about the repository: another " +
			"deployment may already be running them, and no file here can know which. Everything " +
			"about what this repository is and what its work needs belongs in this file; which " +
			"deployment owns running it is settled in grain's own settings, per repository, with " +
			"a per-item override where one deployment runs some of the work and another the rest"
	case Bootstrap:
		return "this is the switch that says this repository's own word is good enough to read at " +
			"all (model.RepoConfig.Declarations), so it can never be something this repository says: " +
			"a file that could switch on the reading of itself would be no control at all. An " +
			"operator turns it on in grain's settings"
	case Inline:
		return "a declared schedule says when, and a template says what: this belongs in the template " +
			"it fires (" + model.TemplateDeclarationDir + "/<name>.yml), which it names with `template:`"
	case Identity:
		return "a declaration's identity is its file -- the id is derived from this repository and " +
			"this file's own name (model.RepoDeclaredID), so no file says it, and renaming the file " +
			"is what makes a new declaration"
	case Kept:
		return "grain keeps this, not the file: it is written as the declaration runs, and the next " +
			"poll of this repository would read the file again without it"
	default:
		return "this key is not part of the format"
	}
}

// Key is one spelling of one field, and the contexts that accept it.
//
// Most fields have exactly one key accepted Everywhere. The ones that do
// not are the interesting half of this package: a key accepted only in
// Settings is a refusal in a repository, and a key accepted nowhere at
// all ("capabilities", "repo") is a spelling somebody would reasonably
// reach for, declared here so that writing it is an error that explains
// itself rather than "field not found".
type Key struct {
	// Name is the key as it is written. Hyphens, not underscores: the
	// unified format is the authoring one, and `auto-merge` is what a
	// person types. Where the export's column disagrees -- it is
	// `auto_merge`, because it is a column -- Columns below records it,
	// and the exporter writes this spelling over that one.
	Name string
	// In is the contexts that accept this key.
	In Context
	// Why is the reason a repository is refused this key, and is set
	// exactly when In does not include Repo.
	//
	// There is no such reason for the other direction, because there is
	// no refusal in it: grain's own settings are the superset, and a key
	// the settings context does not accept -- `name` on a schedule,
	// whose row spells the same field `title` -- is a spelling the
	// exporter converges, not something anybody is told no about.
	Why Why
}

// Field is one field of one of the three things: where it lives on the
// model type, what it is called, and where the export carries it.
type Field struct {
	// Model is the name of the field on the model type. Empty for the
	// handful of keys that have nothing behind them at all -- a suite has
	// no target, and `target:` in a suite file is refused all the same,
	// because somebody who writes it has misunderstood something worth
	// correcting -- and for a template's `capabilities:`, which has two
	// fields behind it rather than one (templateSchema says which).
	Model string
	// Keys is every spelling of this field, in every context.
	Keys []Key
	// Columns is the columns of the kind's own table that carry this
	// field in the settings export. Several for the fields a row spreads
	// out -- a target is target_owner and target_name, a recurrence is
	// five columns of which four are NULL.
	Columns []string
	// Table is the companion table this field lives in instead, for the
	// relations that are not columns at all. A template is not one row:
	// it is `template` plus template_read, template_write,
	// template_grant, template_field, template_custom_field. Naming them
	// here is what tells the exporter which lists to nest
	// (pkg/staterepo/decl.go).
	Table string
	// Declared is the model field holding the *repository's* own value
	// for this setting, where the two are kept apart -- set on exactly
	// the settings fields a repository may write, and empty everywhere
	// else.
	//
	// Two fields rather than one because the row is written by two
	// parties at two different times. The operator's value is a settings
	// box a person maintains and the declaration sync must not rewrite it
	// (model.Memory's own doc comment on why), and a single column would
	// also mean a re-read of an unchanged file appending its text to
	// itself on every poll. Kept apart, the sync owns one column, the UI
	// owns the other, and Compose says how they are read together.
	Declared string
	// Compose is the direction the repository's layer folds into the
	// deployment's, and is set exactly when Declared is.
	Compose Direction
}

// Direction is how a repository's own value for a setting composes with
// the one grain's settings hold for it.
//
// There are two of them and there is deliberately no third, because the
// missing one is the point: nothing here subtracts. A repository's layer
// adds to what an operator wrote and can never reduce it, which is the
// rule RepoConfig.DefaultCapabilities has held to since it existed. The
// alternative -- a repo layer that replaces -- is a repository turning
// off something the deployment switched on, in a file every agent
// dispatched there can rewrite.
type Direction uint8

const (
	// Fixed is the zero value: this setting has no repository layer at
	// all, because a repository may not declare it.
	Fixed Direction = iota
	// Append is two pieces of text, the operator's first. It is the
	// prompt extension's rule already (model.PromptExtensionFor) and the
	// setup command's: both run, or both are said, in that order.
	Append
	// Union is two sets, folded so that neither side can drop an entry
	// the other made -- which for a boolean is OR, the way
	// Config.IgnoreGitHubCI and RepoConfig.IgnoreGitHubCI already
	// compose. Nothing declarable is set-valued yet; it is named here
	// because the next declarable field may be, and inventing the rule
	// at that moment is how a subtraction gets in.
	Union
)

func (d Direction) String() string {
	switch d {
	case Append:
		return "append"
	case Union:
		return "union"
	default:
		return "fixed"
	}
}

// KeyIn is the key this field is written under in one context, and
// whether it has one there at all.
func (f Field) KeyIn(ctx Context) (string, bool) {
	for _, k := range f.Keys {
		if k.In.Has(ctx) {
			return k.Name, true
		}
	}
	return "", false
}

// Schema is one of the three things, whole.
type Schema struct {
	Kind model.DeclarationKind
	// Type is the model type this describes, so that a test -- and, one
	// day, an exporter -- can walk the fields by name.
	Type reflect.Type
	// Table is the kind's own table in the store and in the export.
	Table string
	// Fields is every field of it, in the order a file reads best: what
	// it is called, what it does, then the bookkeeping.
	Fields []Field
}

// Keys is the keys ctx accepts, in this schema's own field order -- what
// a decoder for that context has to have a field for, and nothing more.
func (s Schema) Keys(ctx Context) []string {
	var out []string
	for _, f := range s.Fields {
		for _, k := range f.Keys {
			if k.In.Has(ctx) {
				out = append(out, k.Name)
			}
		}
	}
	return out
}

// RepoRefusals is the keys a repository's own file may not set, each
// with its reason -- exactly what pkg/repodecl checks a file's top-level
// keys against before it decodes one.
func (s Schema) RepoRefusals() map[string]Why {
	out := map[string]Why{}
	for _, f := range s.Fields {
		for _, k := range f.Keys {
			if !k.In.Has(Repo) {
				out[k.Name] = k.Why
			}
		}
	}
	return out
}

// Field is the entry for one field of the model type, by name.
func (s Schema) Field(model string) (Field, bool) {
	for _, f := range s.Fields {
		if f.Model == model {
			return f, true
		}
	}
	return Field{}, false
}

// Of is the schema for one kind.
func Of(kind model.DeclarationKind) (Schema, bool) {
	for _, s := range All() {
		if s.Kind == kind {
			return s, true
		}
	}
	return Schema{}, false
}

// RepoRefusals is Of(kind).RepoRefusals() for a kind that must exist --
// the declaration kinds are a closed set, so a caller reading one of
// them has nothing to recover from.
func RepoRefusals(kind model.DeclarationKind) map[string]Why {
	s, ok := Of(kind)
	if !ok {
		panic(fmt.Sprintf("declschema: no schema for kind %q", kind))
	}
	return s.RepoRefusals()
}

// Declarations is the three kinds of standing work, in
// model.DeclarationKinds' own order.
//
// Separate from All because the settings export treats these three and
// only these three as declaration files with rows of their own
// (pkg/staterepo/decl.go): a repository's settings are a layer on a
// repo_config row rather than a template, a suite or a schedule, and
// exporting them as one would be a file shape nobody decided.
func Declarations() []Schema { return []Schema{templateSchema(), suiteSchema(), scheduleSchema()} }

// All is every schema this package defines: the three declaration kinds
// and a repository's own settings. It is what the guards iterate, so
// that a field added to any of the four model types has to be
// classified.
func All() []Schema { return append(Declarations(), RepoSettingsSchema()) }

// carried is the ordinary key: one spelling, accepted in both contexts.
func carried(name string) []Key { return []Key{{Name: name, In: Everywhere}} }

// settingsOnly is a key grain's settings carry and a repository may not,
// with the reason it is told.
func settingsOnly(name string, why Why) Key { return Key{Name: name, In: Settings, Why: why} }

// nowhere is a spelling that is not the format's own in either context,
// kept so that writing it is answered rather than merely rejected.
func nowhere(name string, why Why) Key { return Key{Name: name, In: 0, Why: why} }

// retiredTeam is `team:`, which a repository's file may still say and
// grain keeps nowhere: schema 25 dropped the team columns, so there is
// no field behind it and nothing for the settings export to write. A
// file naming the default team (or none) parses as it always did, and
// one naming any other is refused (repodecl.checkTeam), so it is no
// reach either.
func retiredTeam() []Key { return []Key{{Name: "team", In: Repo}} }

func templateSchema() Schema {
	return Schema{
		Kind:  model.DeclaredTemplate,
		Type:  reflect.TypeOf(model.Template{}),
		Table: "template",
		Fields: []Field{
			{Model: "Name", Keys: carried("name"), Columns: []string{"name"}},
			{Model: "Title", Keys: carried("title"), Columns: []string{"title"}},
			{
				Model: "Body",
				Keys: []Key{
					{Name: "body", In: Everywhere},
					{Name: "prompt", In: Repo},
				},
				Columns: []string{"body"},
			},
			// The inputs a template asks for when somebody instantiates
			// it. Carried in both: a field is a named hole in this
			// template's own prose and a default to fill it with, which
			// says nothing about what the work may touch -- and an
			// unattended firing fills every one of them with its default
			// (model.Template.FieldValues), so a declared template with
			// fields still fires on its cadence rather than waiting for
			// somebody to type into a form.
			{Model: "Fields", Keys: carried("fields"), Table: "template_field"},
			{Model: "Base", Keys: carried("base"), Columns: []string{"base"}},
			// Read and checked, but kept nowhere: a deployment has no team
			// besides the default one, so a file may name it or leave the
			// key out, and one naming any other team is refused
			// (repodecl.checkTeam). Schema 25 dropped the column.
			{Keys: retiredTeam()},
			// Carried for the reason a suite's is (suiteSchema has it).
			{Model: "AutoMerge", Keys: carried("auto-merge"), Columns: []string{"auto_merge"}},
			// The one isolation setting a declaration may carry, because
			// it can only ever take something away (repodecl.parseNetwork
			// has the argument).
			{Model: "NetworkAccess", Keys: carried("network"), Columns: []string{"network_access"}},
			// What a nothing_needed ending does to what this template
			// files -- close or ack. It decides only who reads the run's
			// report, so a repository saying it reaches nothing.
			{
				Model:   "NothingNeededAction",
				Keys:    carried("nothing-needed"),
				Columns: []string{"nothing_needed_action"},
			},
			// Which model the work runs on (docs/connections-and-models.md).
			// Carried: it changes what runs the work and nothing it may
			// touch, and it is only ever served by the connections the
			// filing team already has.
			{Model: "Model", Keys: carried("model"), Columns: []string{"agent_model"}},
			// The goal every task it files is declared with
			// (model.Template.Goal, docs/design/durable-tasks.md#DT-4): JSON
			// model.GoalDecl. Settings only: a goal decides when grain
			// closes the work, which is the deployment's to say.
			{Model: "Goal", Keys: []Key{settingsOnly("goal", Inline)}, Columns: []string{"goal"}},
			// Whether what it files is a live chat with whoever is
			// watching the task (model.Template.Interactive). Carried: it
			// decides who the agent talks to while it works, not what the
			// work may touch.
			{Model: "Interactive", Keys: carried("interactive"), Columns: []string{"interactive"}},
			{Model: "CustomFields", Keys: carried("custom-fields"), Table: "template_custom_field"},

			// The refusals. Each of these would be perfectly natural to
			// write, and each is exactly what a repository does not get to
			// say about itself.
			{
				Model:   "Target",
				Keys:    []Key{settingsOnly("target", Reach), nowhere("repo", Reach)},
				Columns: []string{"target_owner", "target_name"},
			},
			// A non-git change target in place of a repo binding
			// (model.Template.ChangeTarget, google/docs/design/
			// ChangeTarget): its kind and JSON spec, written as the
			// bare kind (`change-target: <kind>`) or {kind, spec}.
			// Settings only, like target: where a template's work lands is
			// the deployment's to say.
			{
				Model:   "ChangeTarget",
				Keys:    []Key{settingsOnly("change-target", Reach)},
				Columns: []string{"change_target_kind", "change_target_spec"},
			},
			{
				Model:   "Reviewers",
				Keys:    []Key{settingsOnly("reviewers", Reach)},
				Columns: []string{"reviewers"},
			},
			{Model: "Reads", Keys: []Key{settingsOnly("reads", Reach)}, Table: "template_read"},
			{Model: "Writes", Keys: []Key{settingsOnly("writes", Reach)}, Table: "template_write"},
			{Model: "Grants", Keys: []Key{settingsOnly("grants", Reach)}, Table: "template_grant"},
			// The one exception to the refusals above (grain/task-1780),
			// and the same rule a `.grain/workflows` job's `capabilities:`
			// already follows (grain/task-1654): a template may declare
			// `repo-read:<owner>/<repo>` and `repo-write:<owner>/<repo>`,
			// and nothing else. The declaration is the allowance and the
			// filing team's GitHub credential is the authority -- the git
			// proxy clones and pushes a task's work with that credential
			// and nothing else (model.Store.TaskGitCredential), so GitHub
			// itself refuses whatever the team's token cannot do. Every
			// other capability is still refused, with Reach's reason, by
			// pkg/repodecl's parseRepoAccess.
			//
			// No model field of its own: the key spreads over two, Reads
			// for each repo-read: and Writes for each repo-write:, and a
			// settings row writes those as `reads:` and `writes:` as it
			// always has. Which is also why it is accepted in a repository
			// only -- in settings it would be a synonym for both.
			{Keys: []Key{{Name: "capabilities", In: Repo}}},
			// The refs, in the declaring repository only, that the runs of
			// tasks filed from this template may push beyond their own
			// task branch (model.RefPush, grain/task-2536) -- what a
			// release job's whole point is. capabilities:' rule again, one
			// level down: the declaration is the allowance, read only from
			// the default branch, and the filing team's credential is the
			// authority. Explicit ref patterns only, force and delete only
			// where an entry says so (repodecl.parsePush).
			//
			// No model field: the push set is the declaration's, kept
			// beside its provenance (model.RepoDeclaration.Push) rather
			// than on the template row, so the settings export never
			// carries it and no settings edit can write one. Which is also
			// why it is accepted in a repository only.
			{Keys: []Key{{Name: "push", In: Repo}}},

			// The bookkeeping.
			{Model: "ID", Keys: []Key{settingsOnly("id", Identity)}, Columns: []string{"id"}},
			{Model: "CreatedAt", Keys: []Key{settingsOnly("created-at", Kept)}, Columns: []string{"created_at"}},
		},
	}
}

func suiteSchema() Schema {
	return Schema{
		Kind:  model.DeclaredSuite,
		Type:  reflect.TypeOf(model.Suite{}),
		Table: "suite",
		Fields: []Field{
			{Model: "Name", Keys: carried("name"), Columns: []string{"name"}},
			// Named by file stem in a repository and by template id in
			// settings -- the same field, keyed the way each context keys
			// everything else (docs/declarations.md, "Identity").
			{Model: "Items", Keys: carried("templates"), Table: "suite_item"},
			{Model: "Mode", Keys: carried("mode"), Columns: []string{"mode"}},
			{Model: "Count", Keys: carried("count"), Columns: []string{"count"}},
			{Model: "MaxPasses", Keys: carried("max-passes"), Columns: []string{"max_passes"}},
			// Who judges the work, and carried on purpose (grain/task-1486).
			// Each of the three only ever decides what happens to work in
			// this same repository, landing on a branch of it: auto-merge
			// lands a run's own commits here, and an auto-approved
			// proposal is held to the branch and the grants its parent
			// had (model.AutoSubmits). And a declaration takes effect only
			// from the default branch, so the authority behind writing
			// one of these is the authority to merge into that branch
			// already -- which is the authority they hand out. Nothing
			// here reaches another repository, a capability, or a team.
			{Model: "RequireApproval", Keys: carried("require-approval"), Columns: []string{"require_approval"}},
			{Model: "AutoMerge", Keys: carried("auto-merge"), Columns: []string{"auto_merge"}},
			{Model: "AutoApproveProposals", Keys: carried("auto-approve-proposals"), Columns: []string{"auto_approve_proposals"}},
			// Checked and kept nowhere, as on a template.
			{Keys: retiredTeam()},

			// A suite has no target of its own -- a suite run takes the
			// repo and branch it is started against -- so these two keys
			// have no field behind them in either context. They are
			// refused rather than unknown because somebody who writes
			// `repo:` in a suite file has understood the format and
			// misunderstood the rule, and "field not found" would not
			// tell them which.
			{Keys: []Key{nowhere("target", Reach), nowhere("repo", Reach)}},

			{Model: "ID", Keys: []Key{settingsOnly("id", Identity)}, Columns: []string{"id"}},
			{Model: "CreatedAt", Keys: []Key{settingsOnly("created-at", Kept)}, Columns: []string{"created_at"}},
		},
	}
}

func scheduleSchema() Schema {
	return Schema{
		Kind:  model.DeclaredSchedule,
		Type:  reflect.TypeOf(model.Schedule{}),
		Table: "schedule",
		Fields: []Field{
			// A schedule's Title is two things at once in the store: the
			// name it is listed under, and -- for a schedule carrying its
			// own inline task content -- the title of the task it files.
			// A declared schedule is never the second, so it writes the
			// first as `name:`, alongside every other declaration, and
			// `title:` is refused: somebody writing it means inline
			// content, and inline content is what a declared schedule
			// does not have.
			{
				Model:   "Title",
				Keys:    []Key{{Name: "name", In: Repo}, settingsOnly("title", Inline)},
				Columns: []string{"title"},
			},
			{Model: "Body", Keys: []Key{settingsOnly("body", Inline)}, Columns: []string{"body"}},
			// `workflow:` is the same field written another way
			// (grain/task-3159): one of the repository's own
			// `.grain/workflows` to run on the schedule's `base:`, stored
			// as grain's review template for that workflow
			// (model.WorkflowReviewTemplateID) -- exactly what the
			// schedule a workflow's own `on: schedule` cron is translated
			// into stores, so both fire through the same path
			// (repodecl.parseSchedule refuses a file that sets both). A
			// settings row says the same thing by naming that template id,
			// which is why the key is accepted in a repository only.
			{
				Model:   "TemplateID",
				Keys:    []Key{{Name: "template", In: Everywhere}, {Name: "workflow", In: Repo}},
				Columns: []string{"template_id"},
			},
			{Model: "SuiteID", Keys: carried("suite"), Columns: []string{"suite_id"}},
			// Four nested cadence blocks here, six columns there. The
			// same field either way, and the clearest single case for
			// which direction the convergence goes: `daily: {at: "09:00"}`
			// against recurrence_kind/every_n_hours/every_n_minutes/
			// time_of_day_minutes/weekday/day_of_month, five of them NULL.
			{
				Model: "Recurrence",
				Keys: []Key{
					{Name: "every", In: Everywhere},
					{Name: "daily", In: Everywhere},
					{Name: "weekly", In: Everywhere},
					{Name: "monthly", In: Everywhere},
					{Name: "trigger", In: Everywhere},
				},
				Columns: []string{
					"recurrence_kind", "every_n_hours", "every_n_minutes", "time_of_day_minutes", "weekday", "day_of_month",
				},
			},
			{Model: "Overlap", Keys: carried("overlap"), Columns: []string{"overlap"}},
			{Model: "TimeZone", Keys: carried("timezone"), Columns: []string{"time_zone"}},
			{Model: "Enabled", Keys: carried("enabled"), Columns: []string{"enabled"}},
			{Model: "Base", Keys: carried("base"), Columns: []string{"base"}},
			// When a schedule that runs a workflow files a review of the
			// run (model.WorkflowReviewPolicy, grain/task-3160): "always"
			// or "on-failure". A cadence setting rather than content, so
			// a declared schedule may say it.
			{Model: "WorkflowReview", Keys: carried("review"), Columns: []string{"workflow_review"}},
			// Carried for the reason a suite's is (suiteSchema has it);
			// and the team checked and kept nowhere, as on a template.
			{Model: "AutoApproveProposals", Keys: carried("auto-approve-proposals"), Columns: []string{"auto_approve_proposals"}},
			{Keys: retiredTeam()},
			// Carried for the reason a template's model is, and not
			// content: a schedule's model beats the template's
			// (model.Schedule.Model), so a declared schedule may say it.
			{Model: "Model", Keys: carried("model"), Columns: []string{"agent_model"}},

			// Content, which a declared schedule takes from the template
			// or the suite it names rather than carrying twice.
			{Model: "AutoMerge", Keys: []Key{settingsOnly("auto-merge", Inline)}, Columns: []string{"auto_merge"}},
			{
				Model:   "NetworkAccess",
				Keys:    []Key{settingsOnly("network", Inline)},
				Columns: []string{"network_access"},
			},
			{
				Model:   "NothingNeededAction",
				Keys:    []Key{settingsOnly("nothing-needed", Inline)},
				Columns: []string{"nothing_needed_action"},
			},
			{
				Model: "CustomFields",
				Keys:  []Key{settingsOnly("custom-fields", Inline)},
				Table: "schedule_custom_field",
			},

			// Reach, as on a template, plus the target every declared
			// schedule has and no file gets to set.
			{
				Model:   "Target",
				Keys:    []Key{settingsOnly("target", Reach), nowhere("repo", Reach)},
				Columns: []string{"target_owner", "target_name"},
			},
			{Model: "Reads", Keys: []Key{settingsOnly("reads", Reach)}, Table: "schedule_read"},
			{Model: "Writes", Keys: []Key{settingsOnly("writes", Reach)}, Table: "schedule_write"},
			// Capabilities a declared schedule grants directly, beyond what
			// its template or suite declares.
			{
				Model: "Grants",
				Keys:  []Key{settingsOnly("grants", Reach), {Name: "capabilities", In: Repo}},
				Table: "schedule_grant",
			},

			// Bookkeeping, and the one piece of genuine runtime state in
			// this format: a schedule's next and last firing times are on
			// the settings row and are written by the firing loop
			// (staterepo.SettingsTables' own comment on what an import of
			// them costs -- at most one early or late firing). A declared
			// schedule's survive a re-read of the file precisely because
			// the file does not carry them.
			{Model: "ID", Keys: []Key{settingsOnly("id", Identity)}, Columns: []string{"id"}},
			{Model: "NextRunAt", Keys: []Key{settingsOnly("next-run-at", Kept)}, Columns: []string{"next_run_at"}},
			{Model: "LastRunAt", Keys: []Key{settingsOnly("last-run-at", Kept)}, Columns: []string{"last_run_at"}},
			{Model: "CreatedAt", Keys: []Key{settingsOnly("created-at", Kept)}, Columns: []string{"created_at"}},
		},
	}
}

// RepoSettingsSchema is what a repository may and may not say about
// itself in `.grain/settings.yml` -- every field of model.RepoConfig,
// with the reason each of the refused ones is refused (grain/task-340).
//
// The package doc has the rule this list is the consequence of. Read
// down it and the shape is plain: two fields a repository writes, twelve
// it does not, and the twelve are not "everything that would be
// inconvenient" -- they are, exactly, the grants of authority, the
// things that decide whether a verdict counts, and the switch that says
// this repository is read at all.
func RepoSettingsSchema() Schema {
	return Schema{
		Kind:  model.DeclaredSettings,
		Type:  reflect.TypeOf(model.RepoConfig{}),
		Table: "repo_config",
		Fields: []Field{
			// The two a repository writes. Both are instructions to an
			// agent standing in a checkout of this repository, which is a
			// tree that agent can already rewrite -- so neither of them
			// tells the run anything it could not have told itself by
			// editing a file, and keeping them in grain's settings
			// protects nobody.
			//
			// PromptExtension is the less obvious of the two, because it
			// is text injected into a prompt rather than a script. What
			// makes it acceptable is that a repository's own README,
			// CLAUDE.md and code already shape what an agent does there;
			// what it costs is that the composition has to be an append
			// onto the operator's box rather than a replacement of it,
			// which Direction and Declared are for.
			{
				Model: "PromptExtension", Declared: "DeclaredPromptExtension", Compose: Append,
				Keys:    carried("prompt-extension"),
				Columns: []string{"prompt_extension", "declared_prompt_extension"},
			},
			// SetupCommand is the clearest case in the list. Grain
			// already runs this repository's own script, out of this
			// repository's own tree, in a sandbox where the agent runs
			// arbitrary shell anyway (RepoConfig.SetupCommand: "That
			// grants nothing new"). Keeping the *pointer* in an
			// operator's settings while the *script* it points at is in
			// the repository buys nothing at all -- an agent that wants
			// different setup edits the script. What it costs today is
			// that a repository whose toolchain changes needs an operator
			// to type a new command into grain, which is the friction a
			// declaration exists to remove.
			{
				Model: "SetupCommand", Declared: "DeclaredSetupCommand", Compose: Append,
				Keys:    carried("setup-command"),
				Columns: []string{"setup_command", "declared_setup_command"},
			},
			// PortBranches files ports of the repository's own merges, and a
			// port is the same change somebody already approved, under the
			// same team and capabilities, landing through the same queue
			// and CI. It grants nothing a merge had not already been
			// granted, and the repository is the party that knows which of
			// its branches are meant to stay in step. Unioned with the
			// operator's list (RepoConfig.PairedBranches), never replacing
			// it.
			{
				Model: "PortBranches", Declared: "DeclaredPortBranches", Compose: Append,
				Keys:    carried("port-branches"),
				Columns: []string{"port_branches", "declared_port_branches"},
			},
			// PortTypes only ever files fewer ports than port-branches
			// alone would, and only of the repository's own merges: which
			// of its changes a release branch cut from another needs is the
			// repository's to say. A declared list narrows only a pair the
			// repository declared itself, and widens an operator's
			// (RepoConfig.PortTypeFilter).
			{
				Model: "PortTypes", Declared: "DeclaredPortTypes", Compose: Append,
				Keys:    carried("port-types"),
				Columns: []string{"port_types", "declared_port_types"},
			},

			// The grants of authority. Each of these would be perfectly
			// natural to write in a repository, and each is the thing the
			// capability model rests on being an operator's word.
			{
				Model: "DefaultCapabilities",
				Keys: []Key{
					settingsOnly("default-capabilities", Authority),
					// The spelling somebody reaches for first, and the
					// single key this whole design exists to refuse out
					// loud rather than drop.
					nowhere("capabilities", Authority),
				},
				Columns: []string{"default_capabilities"},
			},
			// CICapabilities is what this deployment is willing to *give*
			// this repository's CI jobs, and that is the whole of what
			// it is since grain/task-486 -- which is the distinction
			// worth stating, because it used to be both halves of the
			// question at once.
			//
			// A repository saying "this job needs `gcp-key`" is stating
			// a requirement, and a requirement is not a grant: writing
			// the name into a file does not produce a key. So the
			// requirement moved where every other fact about this
			// repository's work lives -- `capabilities:` on the job in
			// `.grain/workflows`, next to the steps that cannot run
			// without it (ci.Job.Capabilities) -- and what stays here is
			// the grant. A job requiring something this row does not
			// offer does not run, and says so in those words.
			{
				Model:   "CICapabilities",
				Keys:    []Key{settingsOnly("ci-capabilities", Authority)},
				Columns: []string{"ci_capabilities"},
			},
			// ReleaseSecrets is the same shape and got the same split: a
			// job declares the names it needs (ci.Job.Secrets), this row
			// says which names this deployment will resolve material
			// for, and the material itself never comes near either --
			// it lives in pkg/secrets and is read into the one job
			// sandbox that is entitled to it.
			{
				Model:   "ReleaseSecrets",
				Keys:    []Key{settingsOnly("release-secrets", Authority)},
				Columns: []string{"release_secrets"},
			},
			// Where grain clones this repository from. It reads like
			// plumbing and it is authority: it decides which tree every
			// task in this repository is run against and which
			// credentials reach it, so a repository able to redirect it
			// could point grain's agents, and their grants, at code that
			// was never reviewed here.
			{
				Model:   "CloneURL",
				Keys:    []Key{settingsOnly("clone-url", Authority)},
				Columns: []string{"clone_url"},
			},
			// The jobs grain dispatches on a base branch after a merge,
			// and on a branch whose guest inputs nothing has published
			// yet. It reads like a cache-warming setting and it is a
			// grant, for the reason its own doc comment states out loud:
			// grain runs the named job "with that dispatch's publishing
			// material", so naming a job here is choosing which of this
			// repository's jobs gets handed the deployment's credentials
			// to publish under a content-addressed key. A repository that
			// could name its own would be granting itself exactly the
			// authority `release-secrets` above withholds.
			{
				Model:   "PostMergeDispatchJobs",
				Keys:    []Key{settingsOnly("post-merge-dispatch-jobs", Authority)},
				Columns: []string{"post_merge_dispatch_jobs"},
			},

			// The bootstrap. Stated as its own reason rather than folded
			// into the ones above it, because a switch that could turn on
			// the reading of itself is the kind of hole that looks like
			// an oversight to whoever finds it later.
			{
				Model:   "Declarations",
				Keys:    []Key{settingsOnly("declarations", Bootstrap)},
				Columns: []string{"declarations"},
			},

			// The ones that decide whether a verdict counts, or what
			// grain spends reaching one. None of them grants anything,
			// which is why they need their own reason -- and all of them
			// would let a repository exempt itself from review.
			//
			// ignore-github-ci swaps which CI system judges the
			// repository; a repository declaring that its own failing
			// provider does not judge it has exempted itself.
			{
				Model:   "IgnoreGitHubCI",
				Keys:    []Key{settingsOnly("ignore-github-ci", Verdict)},
				Columns: []string{"ignore_github_ci"},
			},
			// unfixable-checks does not exempt a check from holding the
			// queue, and it does stop grain spending on repairing one --
			// a claim about which of its own checks can never pass is
			// still a repository grading its own homework.
			{
				Model:   "UnfixableChecks",
				Keys:    []Key{settingsOnly("unfixable-checks", Verdict)},
				Columns: []string{"unfixable_checks"},
			},
			// Whether this deployment runs this repository's standing
			// work at all, and the per-item exceptions to that
			// (RepoConfig.Work, grain/task-486). It is the one setting
			// on this row that is not a fact about the repository in any
			// sense: another deployment may already be cutting this
			// repository's releases and firing its schedules, and there
			// is nothing in the repository that could know it. A file on
			// a branch that could answer it would be a repository
			// deciding which of several deployments owns it -- which is
			// reach, and the same answer `repo:` gets in a declared
			// template.
			//
			// The keys it refuses are the ones somebody would reach for.
			// `release-workflows-disabled` is among them because that is
			// what this replaced, and a file still carrying it should be
			// told where the decision went rather than have it dropped.
			{
				Model: "Work",
				Keys: []Key{
					settingsOnly("work-ownership", Ownership),
					nowhere("release-workflows-disabled", Ownership),
				},
				Columns: []string{"work_ownership", "work_overrides", "release_workflows_disabled"},
			},
			// base-branch-ci-jobs names which jobs judge the base branch
			// when releases are off. A repository naming one trivially
			// green job would have narrowed its own gate to nothing.
			{
				Model:   "BaseBranchCIJobs",
				Keys:    []Key{settingsOnly("base-branch-ci-jobs", Verdict)},
				Columns: []string{"base_branch_ci_jobs"},
			},
			// require-branch-up-to-date is a merge-queue rule the
			// deployment pays for, in full suites per branch per merge.
			{
				Model:   "RequireBranchUpToDate",
				Keys:    []Key{settingsOnly("require-branch-up-to-date", Verdict)},
				Columns: []string{"require_branch_up_to_date"},
			},
			// watch-default-branch is the same decision in the other
			// direction: it only ever adds judging, and its own doc
			// comment is explicit that the repositories are named "one
			// tick at a time, by whoever is paying for the CI". Spending
			// somebody else's CI budget unattended is not something a
			// repository declares for itself, even when what it buys is
			// more scrutiny of that repository.
			{
				Model:   "WatchDefaultBranch",
				Keys:    []Key{settingsOnly("watch-default-branch", Verdict)},
				Columns: []string{"watch_default_branch"},
			},
			// merge-on-green is a merge-queue rule like
			// require-branch-up-to-date: it decides when the deployment
			// merges into this repository, so it is the operator's.
			{
				Model:   "MergeOnGreen",
				Keys:    []Key{settingsOnly("merge-on-green", Verdict)},
				Columns: []string{"merge_on_green"},
			},
			// base-repair-bypasses-human-gates lets a base-branch repair pull
			// request merge on green CI without waiting for a human on
			// sensitive path triggers or trust-boundary paths (grain/task-401):
			// Verdict, because a repository cannot exempt its own pull requests
			// from human review.
			{
				Model:   "BaseRepairBypassesHumanGates",
				Keys:    []Key{settingsOnly("base-repair-bypasses-human-gates", Verdict)},
				Columns: []string{"base_repair_bypasses_human_gates"},
			},
			// merge-recheck is how the merge queue's overlap re-check is
			// applied here (off, observe, enforce; grain/task-985): a
			// merge-queue rule like merge-on-green, and Verdict for the
			// same reason -- a repository does not get to decide for
			// itself that its changes need no re-check.
			{
				Model:   "MergeRecheck",
				Keys:    []Key{settingsOnly("merge-recheck", Verdict)},
				Columns: []string{"merge_recheck"},
			},

			// issue-import turns GitHub issues into tasks here, and with
			// issue_import_approved it queues them unread: whoever can
			// open an issue on the source then decides what an agent is
			// dispatched to do. Authority, because a repository cannot
			// grant its own issue authors that.
			{
				Model: "IssueImport",
				Keys:  []Key{settingsOnly("issue-import", Authority)},
				Columns: []string{"issue_import_enabled", "issue_import_repo", "issue_import_label",
					"issue_import_approved", "issue_import_interval_seconds"},
			},

			// ci-mode configures the unified CI mode (grain, github, none).
			// Verdict, deciding which CI system gates merges.
			{
				Model:   "CIMode",
				Keys:    []Key{settingsOnly("ci-mode", Verdict)},
				Columns: []string{"ci_mode"},
			},

			// RepairTemplateID selects the template used to file repair tasks for this repository.
			// Authority, so only operators and authorized admins can set it.
			{
				Model:   "RepairTemplateID",
				Keys:    []Key{settingsOnly("repair-template-id", Authority)},
				Columns: []string{"repair_template_id"},
			},

			// The credential grain clones and pushes this repository
			// with, ahead of the deployment's git connections (the
			// default team's team_repo_config.git_credential until schema
			// 23). Authority, for clone-url's reason: a repository able to
			// name its own would choose which of the deployment's
			// credentials its agents push with.
			{
				Model:   "GitCredential",
				Keys:    []Key{settingsOnly("git-credential", Authority)},
				Columns: []string{"git_credential"},
			},

			// Which repository these settings are about. Grain read the
			// file out of a repository it already had in hand, and a file
			// that could name another one would be a repository writing
			// another repository's settings -- which is reach, and so the
			// same answer `repo:` gets in a declared template.
			{
				Model:   "Repo",
				Keys:    []Key{settingsOnly("repo", Authority)},
				Columns: []string{"owner", "name"},
			},
		},
	}
}

// Order is the order refusals are reported in, so that a file setting
// several gets the same reading every time: the rule that matters most
// first.
var Order = []Why{Reach, Authority, Bootstrap, Ownership, Verdict, Inline, Identity, Kept}

// Group sorts keys by the reason each is refused, in Order, dropping the
// ones that are not refused at all. It is what a parser needs to say one
// sentence per reason rather than one per key.
//
// Each group's own keys are sorted, so that one file is read back the
// same way every time rather than in whatever order somebody happened to
// type two keys in.
func Group(refusals map[string]Why, keys []string) []Grouped {
	byWhy := map[Why][]string{}
	for _, key := range keys {
		if why, refused := refusals[key]; refused {
			byWhy[why] = append(byWhy[why], key)
		}
	}
	var out []Grouped
	for _, why := range Order {
		if named := byWhy[why]; len(named) > 0 {
			sort.Strings(named)
			out = append(out, Grouped{Why: why, Keys: named})
		}
	}
	return out
}

// Grouped is one reason and the keys refused for it.
type Grouped struct {
	Why  Why
	Keys []string
}

// String is the keys, listed the way an error message lists them.
func (g Grouped) String() string { return strings.Join(g.Keys, ", ") }

// APIScopeSetSchema is the agent API's scope sets (model.APIScopeSet,
// docs/agent-api.md "Scope sets"): a settings table of their own,
// `api_scope_set`, carried by the state repository as
// tables/api_scope_set.json and never declared by a repository -- every
// key is Authority, since a set says what authority the work granted it
// runs with. It is outside All() because it is not one of the
// declaration kinds the repository parser and the settings exporter
// walk; its own drift test holds it to model.APIScopeSet and to the
// table's columns the way those kinds are held to theirs, and what a
// value of each field may be is model.ResolveAPIScopeSets, which the
// state check and the daemon both run.
func APIScopeSetSchema() Schema {
	field := func(model, key, column string) Field {
		return Field{Model: model, Keys: []Key{settingsOnly(key, Authority)}, Columns: []string{column}}
	}
	return Schema{
		Type:  reflect.TypeOf(model.APIScopeSet{}),
		Table: "api_scope_set",
		Fields: []Field{
			field("Name", "name", "name"),
			field("Description", "description", "description"),
			field("Scopes", "scopes", "scopes"),
			field("Includes", "includes", "includes"),
			field("Repos", "repos", "repos"),
			field("Deny", "deny", "deny"),
			field("RequiresPerson", "requires-person", "requires_person"),
			field("Version", "version", "version"),
			field("Deprecated", "deprecated", "deprecated"),
		},
	}
}
