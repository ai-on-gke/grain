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

package declschema_test

// The tests that make this package worth having. Each of them fails when
// a template, a suite or a schedule exists in one place and not the
// other -- which is the drift the shared definition is for, and without
// them the definition is decoration.

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/declschema"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A field added to model.Template, model.Suite or model.Schedule has to
// say here which contexts accept it. That is the whole point: a field
// that lands in the model and the export while `.grain` silently cannot
// ask for it is exactly what happened to template_field, and this test
// is what stops it happening again.
// It covers model.RepoConfig too, which is what stops grain/task-340's
// decision rotting: the next field added to a repository's settings row
// does not compile a passing build until somebody has said whether a
// repository may declare it, and why not if not.
func TestEverySchemaFieldIsAModelFieldAndBack(t *testing.T) {
	for _, s := range declschema.All() {
		named := map[string]bool{}
		name := func(f declschema.Field, model string) {
			if named[model] {
				t.Errorf("%s: %s is named by two entries", s.Kind, model)
			}
			named[model] = true
			if _, ok := s.Type.FieldByName(model); !ok {
				t.Errorf("%s: the schema names %s.%s, which does not exist",
					s.Kind, s.Type.Name(), model)
			}
		}
		for _, f := range s.Fields {
			if f.Model != "" {
				name(f, f.Model)
			}
			if f.Declared != "" {
				name(f, f.Declared)
			}
		}
		for i := 0; i < s.Type.NumField(); i++ {
			field := s.Type.Field(i)
			if !field.IsExported() {
				continue
			}
			if !named[field.Name] {
				t.Errorf("%s.%s is not in the schema: say which contexts accept it "+
					"(declschema, the %s schema), and what a repository that writes it anyway is told",
					s.Type.Name(), field.Name, strings.ToLower(string(s.Kind)))
			}
		}
	}
}

// The composition half of the rule, pinned field by field: a setting a
// repository may write has somewhere of its own to write it and a stated
// direction, and a setting it may not write has neither.
//
// The direction itself is enforced by the type -- there is no
// declschema.Replace and no declschema.Subtract -- so what is left to
// check here is that nobody has half-declared a field: a Compose with no
// column to compose from, or a repo-writable key whose two layers nobody
// has said how to read together.
func TestEveryDeclarableSettingSaysHowItComposes(t *testing.T) {
	s := declschema.RepoSettingsSchema()
	for _, f := range s.Fields {
		_, writable := f.KeyIn(declschema.Repo)
		switch {
		case writable && f.Declared == "":
			t.Errorf("%s: a repository may write it and there is nowhere of its own to put the "+
				"value -- name the field that holds the repository's own layer", f.Model)
		case writable && f.Compose == declschema.Fixed:
			t.Errorf("%s: a repository may write it and nothing says how it folds into the "+
				"operator's value -- Append or Union, never a replacement", f.Model)
		case !writable && f.Declared != "":
			t.Errorf("%s: a repository may not write it and it still has a declared layer (%s)",
				f.Model, f.Declared)
		case !writable && f.Compose != declschema.Fixed:
			t.Errorf("%s: a repository may not write it and it still says it composes (%s)",
				f.Model, f.Compose)
		}
	}
}

// The classification, read back as a list. A reviewer of this package
// should be able to see the whole of what a repository may say about
// itself in one place, and a change to it should be a change to this
// test rather than something noticed six months later.
func TestExactlyTwoSettingsAreDeclarable(t *testing.T) {
	var declarable []string
	for _, f := range declschema.RepoSettingsSchema().Fields {
		if key, ok := f.KeyIn(declschema.Repo); ok {
			declarable = append(declarable, key)
		}
	}
	want := []string{"prompt-extension", "setup-command", "port-branches", "port-types"}
	if !reflect.DeepEqual(declarable, want) {
		t.Errorf("a repository may declare %v, want %v -- if that is deliberate, the rule in "+
			"declschema's doc comment is what has to justify it", declarable, want)
	}
}

// Every refusal in a settings file is one of the four reasons the rule
// names. A new Why here would be a fifth kind of thing a repository may
// not say, which is a change to the rule and not to a list.
//
// Ownership is the fourth, added by grain/task-486, and it is the only
// one that is not about trusting the repository: a key refused for
// Ownership asks which *deployment* runs this repository's standing
// work, and the repository is refused it because it cannot know the
// answer rather than because it must not be allowed one.
func TestSettingsRefusalsAreAuthorityVerdictBootstrapOrOwnership(t *testing.T) {
	allowed := map[declschema.Why]bool{
		declschema.Authority: true,
		declschema.Verdict:   true,
		declschema.Bootstrap: true,
		declschema.Ownership: true,
	}
	for key, why := range declschema.RepoSettingsSchema().RepoRefusals() {
		if !allowed[why] {
			t.Errorf("%q is refused for %q: a repository is refused a setting because it would "+
				"grant its own authority, judge itself, switch on its own reading, or decide "+
				"which deployment owns running it", key, why)
		}
	}
}

// Grain's settings are the superset, by construction: a repository's
// declaration is a subset of what an operator may write, never the other
// way round. A field carried only in the repo context would be a thing a
// repository can say and grain cannot store.
func TestSettingsCarriesEveryField(t *testing.T) {
	for _, s := range declschema.All() {
		for _, f := range s.Fields {
			if f.Model == "" {
				continue
			}
			if _, ok := f.KeyIn(declschema.Settings); !ok {
				t.Errorf("%s: %s is written in no settings key -- grain's own settings carry "+
					"every field there is", s.Kind, f.Model)
			}
		}
	}
}

// The refusals are the security half of this package, so every key a
// repository may not write has to carry the reason it is told, and every
// key it may write has to carry none: a "reason" on an accepted key is a
// refusal somebody has half-written.
func TestEveryKeyARepositoryMayNotWriteSaysWhy(t *testing.T) {
	for _, s := range declschema.All() {
		for _, f := range s.Fields {
			for _, k := range f.Keys {
				switch {
				case k.In.Has(declschema.Repo) && k.Why != declschema.Carried:
					t.Errorf("%s: %q is accepted in a repository and still gives a reason (%q)",
						s.Kind, k.Name, k.Why)
				case !k.In.Has(declschema.Repo) && k.Why == declschema.Carried:
					t.Errorf("%s: %q is refused in a repository with no reason -- a refusal that "+
						"cannot say why is a silent drop with extra steps", s.Kind, k.Name)
				case !k.In.Has(declschema.Repo) && strings.TrimSpace(k.Why.Reason()) == "":
					t.Errorf("%s: %q is refused for %q, which says nothing", s.Kind, k.Name, k.Why)
				}
			}
		}
	}
}

// One key per field per context: two would be a format with a synonym,
// and a file whose meaning depends on which of them the parser read
// first.
func TestAFieldHasOneKeyPerContext(t *testing.T) {
	for _, s := range declschema.All() {
		for _, ctx := range []declschema.Context{declschema.Settings, declschema.Repo} {
			for _, f := range s.Fields {
				var in []string
				for _, k := range f.Keys {
					if k.In.Has(ctx) {
						in = append(in, k.Name)
					}
				}
				// A schedule's cadence is the one field written as one of
				// four blocks, and repodecl refuses a file that sets more
				// than one of them. A template's body can be written as
				// "body" or "prompt", and repodecl refuses a file that sets both.
				// A schedule's template is named by "template" or, for a
				// workflow's review, "workflow" -- and repodecl refuses a
				// file that sets both.
				if f.Model == "Recurrence" || (s.Kind == model.DeclaredTemplate && f.Model == "Body") ||
					(s.Kind == model.DeclaredSchedule && f.Model == "TemplateID") {
					continue
				}
				if len(in) > 1 {
					t.Errorf("%s: %s is written as %v in %s -- one key per field per context",
						s.Kind, f.Model, in, ctx)
				}
			}
		}
	}
}

// No two fields may claim the same key in the same context, refusals
// included: a key that means two things is a key whose error message
// cannot be right.
func TestNoKeyIsClaimedTwice(t *testing.T) {
	for _, s := range declschema.All() {
		seen := map[string]string{}
		for _, f := range s.Fields {
			for _, k := range f.Keys {
				if first, dup := seen[k.Name]; dup {
					t.Errorf("%s: %q is claimed by both %s and %s", s.Kind, k.Name, first, f.Model)
				}
				seen[k.Name] = f.Model
			}
		}
	}
}

// The export's half, as far as it goes before the file shapes converge:
// the kind's own table and the schema say the same thing about which
// columns there are. A column added to `schedule` -- which is a column
// added to tables/schedule.json -- has to be a field here, which forces
// the same decision a new model field does.
func TestEveryColumnOfTheExportIsAField(t *testing.T) {
	ddl := tableColumns(t)
	for _, s := range declschema.All() {
		cols, ok := ddl[s.Table]
		if !ok {
			t.Fatalf("%s: no `%s` table in pkg/model's DDL", s.Kind, s.Table)
		}
		named := map[string]string{}
		for _, f := range s.Fields {
			for _, col := range f.Columns {
				if !cols[col] {
					t.Errorf("%s: %s says it is column %q of `%s`, which does not exist",
						s.Kind, f.Model, col, s.Table)
				}
				if first, dup := named[col]; dup {
					t.Errorf("%s: column %q is claimed by both %s and %s", s.Kind, col, first, f.Model)
				}
				named[col] = f.Model
			}
			if f.Table != "" {
				if _, ok := ddl[f.Table]; !ok {
					t.Errorf("%s: %s says it lives in table `%s`, which does not exist",
						s.Kind, f.Model, f.Table)
				}
			}
			if f.Model != "" && len(f.Columns) == 0 && f.Table == "" {
				t.Errorf("%s: %s says nowhere the settings export carries it", s.Kind, f.Model)
			}
		}
		for col := range cols {
			if named[col] == "" {
				t.Errorf("`%s`.%s is a column of the settings export and no field of the schema: "+
					"say which field carries it and which contexts accept it", s.Table, col)
			}
		}
	}
}

// Group is what a parser reports a file's refusals with: one sentence
// per reason, in a fixed order, and nothing said about the keys it does
// accept.
func TestGroupSaysOneThingPerReason(t *testing.T) {
	refusals := declschema.RepoRefusals(model.DeclaredSchedule)
	got := declschema.Group(refusals, []string{"template", "grants", "repo", "title", "every"})
	if len(got) != 2 {
		t.Fatalf("grouped = %+v, want reach and inline and nothing else", got)
	}
	if got[0].Why != declschema.Reach || got[0].String() != "grants, repo" {
		t.Errorf("first group = %+v, want the reach keys first", got[0])
	}
	if got[1].Why != declschema.Inline || got[1].String() != "title" {
		t.Errorf("second group = %+v", got[1])
	}
}

// The three kinds are a closed set and every one of them has a schema,
// which is what lets a caller with a model.DeclarationKind in hand read
// it without a fallback.
func TestEveryDeclarationKindHasASchema(t *testing.T) {
	for _, kind := range model.DeclarationKinds() {
		s, ok := declschema.Of(kind)
		if !ok {
			t.Fatalf("no schema for %q", kind)
		}
		if s.Type == nil || s.Type.Kind() != reflect.Struct {
			t.Errorf("%s: type = %v", kind, s.Type)
		}
		if len(s.Keys(declschema.Repo)) == 0 {
			t.Errorf("%s: a repository can write none of it", kind)
		}
	}
}

// The settings schema is one of All -- so every guard above covers it --
// and deliberately not one of Declarations, which is the list the
// settings export walks to write one file per declared thing
// (pkg/staterepo/decl.go). A repository's settings are a layer on a
// repo_config row, not a template with an id of its own, and exporting
// them as one would be a file shape nobody decided.
func TestTheSettingsSchemaIsNotADeclarationKind(t *testing.T) {
	settings := declschema.RepoSettingsSchema()
	for _, s := range declschema.Declarations() {
		if s.Kind == settings.Kind {
			t.Fatalf("%s is in Declarations(), which the settings exporter walks", s.Kind)
		}
	}
	var found bool
	for _, s := range declschema.All() {
		if s.Kind == settings.Kind {
			found = true
		}
	}
	if !found {
		t.Error("the settings schema is in no list the guards iterate, so nothing checks it")
	}
	// And a caller with the kind in hand can still read it, which is how
	// pkg/repodecl gets the refusals for a settings file.
	if _, ok := declschema.Of(model.DeclaredSettings); !ok {
		t.Error("Of(DeclaredSettings) found nothing")
	}
}

// tableColumns reads pkg/model's own DDL -- the same statements the
// store is created from and the same columns pkg/staterepo dumps -- so
// that this test compares the schema against what the export actually
// emits rather than against a second list of column names.
func tableColumns(t *testing.T) map[string]map[string]bool {
	t.Helper()
	create := regexp.MustCompile("(?i)^CREATE TABLE IF NOT EXISTS `([a-z_]+)` \\(")
	column := regexp.MustCompile("^`([a-z_]+)`")
	out := map[string]map[string]bool{}
	for _, stmt := range model.Tables {
		m := create.FindStringSubmatch(stmt)
		if m == nil {
			continue
		}
		cols := map[string]bool{}
		for _, line := range strings.Split(stmt, "\n")[1:] {
			c := column.FindStringSubmatch(strings.TrimSpace(line))
			if c == nil {
				continue // PRIMARY KEY, UNIQUE, CHECK and the closing paren
			}
			cols[c[1]] = true
		}
		out[m[1]] = cols
	}
	if len(out) == 0 {
		t.Fatal("read no tables at all out of model.Tables -- the DDL's shape has changed")
	}
	return out
}

func TestContextString(t *testing.T) {
	tests := []struct {
		ctx  declschema.Context
		want string
	}{
		{declschema.Settings, "settings"},
		{declschema.Repo, "repo"},
		{declschema.Everywhere, "settings and repo"},
		{declschema.Context(0), "nowhere"},
		{declschema.Context(99), "unknown"},
	}
	for _, tc := range tests {
		if got := tc.ctx.String(); got != tc.want {
			t.Errorf("Context(%d).String() = %q, want %q", tc.ctx, got, tc.want)
		}
	}
}

func TestDirectionString(t *testing.T) {
	tests := []struct {
		dir  declschema.Direction
		want string
	}{
		{declschema.Append, "append"},
		{declschema.Union, "union"},
		{declschema.Fixed, "fixed"},
		{declschema.Direction(99), "fixed"},
	}
	for _, tc := range tests {
		if got := tc.dir.String(); got != tc.want {
			t.Errorf("Direction(%d).String() = %q, want %q", tc.dir, got, tc.want)
		}
	}
}

func TestWhyReason(t *testing.T) {
	whys := []declschema.Why{
		declschema.Reach,
		declschema.Authority,
		declschema.Verdict,
		declschema.Bootstrap,
		declschema.Ownership,
		declschema.Inline,
		declschema.Identity,
		declschema.Kept,
		declschema.Carried,
	}
	for _, why := range whys {
		if reason := why.Reason(); reason == "" {
			t.Errorf("Why(%s).Reason() is empty", why)
		}
	}
}

func TestSchemaField(t *testing.T) {
	schema := declschema.RepoSettingsSchema()
	field, ok := schema.Field("PromptExtension")
	if !ok || field.Model != "PromptExtension" {
		t.Errorf("Field(PromptExtension) = (%+v, %v), want found", field, ok)
	}
	_, ok = schema.Field("NonExistentField")
	if ok {
		t.Errorf("Field(NonExistentField) expected false, got true")
	}
}

func TestOfAndRepoRefusalsPanic(t *testing.T) {
	if _, ok := declschema.Of(model.DeclarationKind("bogus")); ok {
		t.Error("Of(bogus) expected false, got true")
	}

	defer func() {
		if r := recover(); r == nil {
			t.Errorf("RepoRefusals(bogus) should have panicked")
		}
	}()
	_ = declschema.RepoRefusals(model.DeclarationKind("bogus"))
}

// The two keys a repository writes that reach past what a task gets by
// default: a template's `capabilities:` (grain/task-1780), which
// pkg/repodecl holds to repo-read: and repo-write: entries, and its
// `push:` (grain/task-2536), which names refs of the declaring repository
// beyond the run's own branch. They are pinned here so that a third such
// key is a decision somebody makes on purpose, and so that they stay a
// template's alone -- a schedule takes it from the template it fires, and a suite
// has no reach of its own at all.
func TestATemplatesRepositoryAccessIsTheOnlyReachARepositoryWrites(t *testing.T) {
	for _, s := range declschema.Declarations() {
		var reach []string
		for _, f := range s.Fields {
			for _, k := range f.Keys {
				// `team:` has no field behind it either, and reaches
				// nothing: it may only name the default team.
				if k.In.Has(declschema.Repo) && f.Model == "" && k.Name != "team" {
					reach = append(reach, k.Name)
				}
			}
		}
		want := ""
		if s.Kind == model.DeclaredTemplate {
			want = "capabilities push"
		}
		if strings.Join(reach, " ") != want {
			t.Errorf("%s: a repository writes %v with no field behind it, want %q", s.Kind, reach, want)
		}
		refused := s.RepoRefusals()
		if s.Kind == model.DeclaredSuite {
			continue
		}
		for _, key := range []string{"reads", "writes", "grants", "target"} {
			if refused[key] != declschema.Reach {
				t.Errorf("%s: %q is refused for %q, want reach", s.Kind, key, refused[key])
			}
		}
	}
}

// grain/task-442: the scope set schema is held to model.APIScopeSet and
// to the api_scope_set table in both directions, as the declaration
// kinds are -- a field or column added to one has to be added here.
func TestTheAPIScopeSetSchemaMatchesTheModelAndTheTable(t *testing.T) {
	s := declschema.APIScopeSetSchema()
	cols, ok := tableColumns(t)[s.Table]
	if !ok {
		t.Fatalf("no `%s` table in pkg/model's DDL", s.Table)
	}
	fields, columns := map[string]bool{}, map[string]bool{}
	for _, f := range s.Fields {
		if _, ok := s.Type.FieldByName(f.Model); !ok {
			t.Errorf("the schema names model.APIScopeSet.%s, which does not exist", f.Model)
		}
		fields[f.Model] = true
		for _, c := range f.Columns {
			if !cols[c] {
				t.Errorf("%s says it is column %q, which `%s` does not have", f.Model, c, s.Table)
			}
			columns[c] = true
		}
		for _, k := range f.Keys {
			if k.In.Has(declschema.Repo) || k.Why != declschema.Authority {
				t.Errorf("%s: a repository may never declare a scope set, so every key is Authority", f.Model)
			}
		}
	}
	for i := 0; i < s.Type.NumField(); i++ {
		if name := s.Type.Field(i).Name; !fields[name] {
			t.Errorf("model.APIScopeSet.%s is not in the schema", name)
		}
	}
	for c := range cols {
		if !columns[c] {
			t.Errorf("`%s`.%s is no field of the schema", s.Table, c)
		}
	}
}
