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

package repodecl_test

// The format a repository declares its own standing work in
// (grain/task-60): what a well-formed `.grain` directory produces, and
// what each of the refusals actually says.

import (
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/repodecl"
)

var repo = model.RepoRef{Owner: "bwsalmon", Name: "grain"}

func tree(files map[string]string) fstest.MapFS {
	out := fstest.MapFS{}
	for name, body := range files {
		out[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return out
}

func load(t *testing.T, files map[string]string) model.RepoDeclarations {
	t.Helper()
	declared, err := repodecl.Load(tree(files), repo)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return declared
}

func loadErr(t *testing.T, files map[string]string) string {
	t.Helper()
	declared, err := repodecl.Load(tree(files), repo)
	if err == nil {
		t.Fatalf("Load: no error, and it declared %d template(s), %d suite(s), %d schedule(s)",
			len(declared.Templates), len(declared.Suites), len(declared.Schedules))
	}
	return err.Error()
}

func TestATreeWithNoGrainDirectoryDeclaresNothing(t *testing.T) {
	declared := load(t, map[string]string{"README.md": "# a repository like every other"})
	if !declared.Empty() {
		t.Fatalf("declared %+v, want nothing", declared)
	}
}

func TestTemplateIsBoundToTheDeclaringRepo(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/templates/nightly.yml": `
name: Nightly dependency bump
title: Bump dependencies
body: |
  Update what has moved, run the tests.
base: main
auto-merge: true
network: none
nothing-needed: ack
custom-fields:
  team: infra
  reviewers: [ana, bo]
`,
	})
	if len(declared.Templates) != 1 {
		t.Fatalf("declared %d templates, want 1", len(declared.Templates))
	}
	file := declared.Templates[0]
	if file.Path != ".grain/templates/nightly.yml" {
		t.Errorf("path = %q", file.Path)
	}
	tmpl := file.Template
	if want := model.RepoDeclaredID(model.DeclaredTemplate, repo, "nightly"); tmpl.ID != want {
		t.Errorf("id = %q, want %q", tmpl.ID, want)
	}
	if tmpl.Target == nil || !tmpl.Target.Same(repo) {
		t.Errorf("target = %v, want the declaring repo -- a declaration cannot file work anywhere else", tmpl.Target)
	}
	if tmpl.Base != "main" || tmpl.Title != "Bump dependencies" || !tmpl.AutoMerge {
		t.Errorf("template = %+v", tmpl)
	}
	if tmpl.NetworkAccess != model.NetworkNone {
		t.Errorf("network = %q, want none", tmpl.NetworkAccess)
	}
	if tmpl.NothingNeededAction != model.NothingNeededAck {
		t.Errorf("nothing-needed = %q, want ack", tmpl.NothingNeededAction)
	}
	if got := tmpl.CustomFields["reviewers"]; len(got) != 2 || got[0] != "ana" {
		t.Errorf("reviewers = %v, want both of them", got)
	}
	if got := tmpl.CustomFields["team"]; len(got) != 1 || got[0] != "infra" {
		t.Errorf("team = %v, want the single value spelling to work too", got)
	}
}

// An unset nothing-needed is kept unset -- "close", the task's own
// default -- and anything outside the vocabulary is refused rather than
// read as either.
func TestATemplatesNothingNeededDefaultsToCloseAndRefusesTheUnknown(t *testing.T) {
	declared := load(t, map[string]string{".grain/templates/nightly.yml": "title: Bump dependencies\n"})
	if got := declared.Templates[0].Template.NothingNeededAction; got != "" || got.Resolved() != model.NothingNeededClose {
		t.Errorf("nothing-needed = %q, want unset (close)", got)
	}
	msg := loadErr(t, map[string]string{".grain/templates/nightly.yml": "title: Bump dependencies\nnothing-needed: ignore\n"})
	if !strings.Contains(msg, `nothing-needed is "close" or "ack"`) {
		t.Errorf("error = %q", msg)
	}
}

func TestATemplateWithoutATitleIsRefused(t *testing.T) {
	if msg := loadErr(t, map[string]string{".grain/templates/nightly.yml": "body: do it\n"}); !strings.Contains(msg, "needs a title") {
		t.Fatalf("error = %q", msg)
	}
}

// The refusal this whole feature rests on: a repository's own file may
// say what work to do in it, never what that work may touch.
func TestADeclarationMayNotWidenWhatItsWorkTouches(t *testing.T) {
	for _, field := range []string{
		"repo: somebody/else",
		"target: somebody/else",
		"reads: [somebody/else]",
		"writes: [somebody/else]",
		"grants: [gcp-key]",
	} {
		msg := loadErr(t, map[string]string{
			".grain/templates/nightly.yml": "title: Bump dependencies\n" + field + "\n",
		})
		if !strings.Contains(msg, "never what that work may touch") {
			t.Errorf("%q: error = %q, want the refusal to say why", field, msg)
		}
	}
}

// The one exception to the refusal above (grain/task-1780): a template's
// `capabilities:` may name the repositories its tasks read and push, as
// a `.grain/workflows` job's may, and they land on the template's Reads
// and Writes -- the same fields a settings row's `reads:` and `writes:`
// fill, so everything downstream of a template treats them alike.
func TestATemplateDeclaresTheRepositoriesItsTasksReachAndNothingElse(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/templates/sync.yml": "title: Sync the vendored copy\n" +
			"capabilities:\n" +
			"  - repo-read:acme/upstream\n" +
			"  - repo-write:acme/downstream\n" +
			"  - repo-read:acme/upstream\n" +
			"  - repo-write:" + repo.String() + "\n" +
			"  - repo-read:" + repo.String() + "\n",
	})
	if len(declared.Templates) != 1 {
		t.Fatalf("templates = %d, want 1", len(declared.Templates))
	}
	tmpl := declared.Templates[0].Template
	if len(tmpl.Reads) != 1 || tmpl.Reads[0] != (model.RepoRef{Owner: "acme", Name: "upstream"}) {
		t.Errorf("Reads = %v, want [acme/upstream] once, and not the declaring repository", tmpl.Reads)
	}
	if len(tmpl.Writes) != 1 || tmpl.Writes[0] != (model.WriteTarget{Repo: model.RepoRef{Owner: "acme", Name: "downstream"}}) {
		t.Errorf("Writes = %v, want [acme/downstream] on its default branch", tmpl.Writes)
	}
	if len(tmpl.Grants) != 0 {
		t.Errorf("Grants = %v, want none: repository access is not a capability grant", tmpl.Grants)
	}
	if *tmpl.Target != repo {
		t.Errorf("Target = %v, want the declaring repository still", tmpl.Target)
	}

	// And one without them reaches nothing past its own repository.
	plain := load(t, map[string]string{".grain/templates/sync.yml": "title: Sync the vendored copy\n"})
	if tmpl := plain.Templates[0].Template; len(tmpl.Reads)+len(tmpl.Writes) != 0 {
		t.Errorf("an undeclared template reaches %v / %v", tmpl.Reads, tmpl.Writes)
	}
}

func TestATemplatesCapabilitiesAcceptArbitraryCapabilities(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/templates/nightly.yml": "title: Bump dependencies\ncapabilities: [\"repo-read:acme/ok\", \"gcp-key:deploy\", \"package-publish\"]\n",
	})
	if len(declared.Templates) != 1 {
		t.Fatalf("templates = %d, want 1", len(declared.Templates))
	}
	tmpl := declared.Templates[0].Template
	if len(tmpl.Grants) != 2 {
		t.Fatalf("Grants = %v, want 2", tmpl.Grants)
	}
	if tmpl.Grants[0].Capability != "gcp-key:deploy" || tmpl.Grants[0].Via != ".grain/templates/nightly.yml" {
		t.Errorf("Grants[0] = %+v, want gcp-key:deploy via .grain/templates/nightly.yml", tmpl.Grants[0])
	}
	if tmpl.Grants[1].Capability != "package-publish" || tmpl.Grants[1].Via != ".grain/templates/nightly.yml" {
		t.Errorf("Grants[1] = %+v, want package-publish via .grain/templates/nightly.yml", tmpl.Grants[1])
	}

	for _, tc := range []struct{ entry, want string }{
		{"repo-write:", "does not name a repository"},
		{"repo-read:not-a-repo", "does not name a repository"},
		{"repo-write:grain/config", "grain's own configuration"},
		{"repo-read:Grain/Config", "grain's own configuration"},
		{"gcp-key:", "does not name a GCP key credential"},
		{"invalid capability", "is not a valid capability name"},
	} {
		msg := loadErr(t, map[string]string{
			".grain/templates/nightly.yml": "title: Bump dependencies\ncapabilities: [\"repo-read:acme/ok\", \"" + tc.entry + "\"]\n",
		})
		if !strings.Contains(msg, tc.want) || !strings.Contains(msg, tc.entry) {
			t.Errorf("%q: error = %q, want it named and %q", tc.entry, msg, tc.want)
		}
	}
}

// A schedule takes its repository access from the template it fires,
// and is told so rather than that it may not have any.
func TestAScheduleLeavesRepositoryAccessToItsTemplate(t *testing.T) {
	msg := loadErr(t, map[string]string{
		".grain/templates/nightly.yml": "title: Bump dependencies\ncapabilities: [\"repo-write:acme/tools\"]\n",
		".grain/schedules/it.yml":      "template: nightly\ncapabilities: [\"repo-write:acme/tools\"]\nevery:\n  hours: 6\n",
	})
	if !strings.Contains(msg, "repository access belongs on the template it fires") {
		t.Fatalf("error = %q", msg)
	}
}

func TestAScheduleAcceptsCapabilities(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/templates/nightly.yml": "title: Bump dependencies\n",
		".grain/schedules/it.yml":      "template: nightly\ncapabilities: [\"gcp-key:deploy\", \"package-publish\"]\nevery:\n  hours: 6\n",
	})
	if len(declared.Schedules) != 1 {
		t.Fatalf("schedules = %d, want 1", len(declared.Schedules))
	}
	sched := declared.Schedules[0].Schedule
	if len(sched.Grants) != 2 {
		t.Fatalf("Grants = %v, want 2", sched.Grants)
	}
	if sched.Grants[0].Capability != "gcp-key:deploy" || sched.Grants[0].Via != ".grain/schedules/it.yml" {
		t.Errorf("Grants[0] = %+v, want gcp-key:deploy via .grain/schedules/it.yml", sched.Grants[0])
	}
	if sched.Grants[1].Capability != "package-publish" || sched.Grants[1].Via != ".grain/schedules/it.yml" {
		t.Errorf("Grants[1] = %+v, want package-publish via .grain/schedules/it.yml", sched.Grants[1])
	}
}

func TestAMisspeltKeyIsAnErrorNamingIt(t *testing.T) {
	msg := loadErr(t, map[string]string{
		".grain/templates/nightly.yml": "title: Bump dependencies\nauto_merge: true\n",
	})
	if !strings.Contains(msg, "auto_merge") {
		t.Fatalf("error = %q, want it to name the key somebody actually typed", msg)
	}
}

func TestSuiteNamesItsTemplatesByFile(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/templates/nightly.yml": "title: Bump dependencies\n",
		".grain/templates/docs.yml":    "title: Sweep the docs\n",
		".grain/suites/sweep.yml": `
name: Nightly sweep
templates: [nightly, docs]
mode: until-clean
max-passes: 3
require-approval: true
auto-merge: false
`,
	})
	if len(declared.Suites) != 1 {
		t.Fatalf("declared %d suites, want 1", len(declared.Suites))
	}
	suite := declared.Suites[0].Suite
	if suite.Mode != model.SuiteUntilClean || suite.MaxPasses != 3 {
		t.Errorf("suite = %+v", suite)
	}
	if !suite.RequireApproval || suite.AutoMerge {
		t.Errorf("suite = %+v, want the file's own approval and auto-merge answers", suite)
	}
	if len(suite.Items) != 2 {
		t.Fatalf("suite items = %v", suite.Items)
	}
	if want := model.RepoDeclaredID(model.DeclaredTemplate, repo, "nightly"); suite.Items[0].TemplateID != want {
		t.Errorf("first item = %q, want %q", suite.Items[0].TemplateID, want)
	}
}

func TestASuiteCannotNameATemplateThisRepoDoesNotDeclare(t *testing.T) {
	msg := loadErr(t, map[string]string{
		".grain/suites/sweep.yml": "templates: [nightly]\n",
	})
	if !strings.Contains(msg, `no template "nightly"`) {
		t.Fatalf("error = %q", msg)
	}
}

func TestScheduleFiresADeclaredTemplateOnACadence(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/templates/nightly.yml": "title: Bump dependencies\n",
		".grain/schedules/nightly.yml": `
name: Every night
template: nightly
daily:
  at: "02:30"
overlap: allow
`,
	})
	if len(declared.Schedules) != 1 {
		t.Fatalf("declared %d schedules, want 1", len(declared.Schedules))
	}
	sched := declared.Schedules[0].Schedule
	if sched.TemplateID == nil || *sched.TemplateID != model.RepoDeclaredID(model.DeclaredTemplate, repo, "nightly") {
		t.Errorf("templateId = %v", sched.TemplateID)
	}
	if !sched.Target.Same(repo) {
		t.Errorf("target = %v, want the declaring repo", sched.Target)
	}
	if sched.Recurrence.Kind != model.RecurrenceDaily || sched.Recurrence.TimeOfDay != 2*60+30 {
		t.Errorf("recurrence = %+v", sched.Recurrence)
	}
	if sched.Overlap != model.OverlapAllow || !sched.Enabled {
		t.Errorf("schedule = %+v", sched)
	}
}

func TestADeclarationNamingAnotherTeamIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, path, file string }{
		{"template", ".grain/templates/nightly.yml", "title: Bump dependencies\nteam: security\n"},
		{"suite", ".grain/suites/smoke.yml", "templates: [smoke]\nteam: devops\n"},
		{"schedule", ".grain/schedules/nightly.yml", "template: smoke\nteam: platform\ndaily:\n  at: \"02:30\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{".grain/templates/smoke.yml": "title: Smoke tests\n", tc.path: tc.file}
			if err := loadErr(t, files); !strings.Contains(err, tc.path) || !strings.Contains(err, "does not exist") {
				t.Fatalf("Load = %s, want %s refused for naming a team", err, tc.path)
			}
		})
	}
}

func TestADeclarationNamingTheDefaultTeamParses(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/templates/nightly.yml": "title: Bump dependencies\nteam: default\n",
		".grain/suites/smoke.yml":      "templates: [nightly]\nteam: default\n",
		".grain/schedules/nightly.yml": "template: nightly\nteam: default\ndaily:\n  at: \"02:30\"\n",
	})
	if len(declared.Templates) != 1 || len(declared.Suites) != 1 || len(declared.Schedules) != 1 {
		t.Fatalf("declared %d templates, %d suites, %d schedules; want one of each",
			len(declared.Templates), len(declared.Suites), len(declared.Schedules))
	}
}

func TestEveryCadenceParses(t *testing.T) {
	for _, tc := range []struct {
		cadence string
		want    model.Recurrence
	}{
		{"every:\n  hours: 6\n", model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 6}},
		{"every:\n  minutes: 30\n", model.Recurrence{Kind: model.RecurrenceEveryNMinutes, EveryNMinutes: 30}},
		{"daily:\n  at: \"09:00\"\n", model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 9 * 60}},
		{"weekly:\n  day: monday\n  at: \"17:45\"\n",
			model.Recurrence{Kind: model.RecurrenceWeekly, Weekday: time.Monday, TimeOfDay: 17*60 + 45}},
		{"monthly:\n  day: 28\n  at: \"00:00\"\n",
			model.Recurrence{Kind: model.RecurrenceMonthly, DayOfMonth: 28}},
	} {
		declared := load(t, map[string]string{
			".grain/templates/nightly.yml": "title: Bump dependencies\n",
			".grain/schedules/it.yml":      "template: nightly\n" + tc.cadence,
		})
		if got := declared.Schedules[0].Schedule.Recurrence; got != tc.want {
			t.Errorf("%q: recurrence = %+v, want %+v", tc.cadence, got, tc.want)
		}
	}
}

func TestAScheduleNeedsExactlyOneCadence(t *testing.T) {
	files := map[string]string{
		".grain/templates/nightly.yml": "title: Bump dependencies\n",
		".grain/schedules/it.yml":      "template: nightly\n",
	}
	if msg := loadErr(t, files); !strings.Contains(msg, "exactly one cadence") {
		t.Fatalf("no cadence: error = %q", msg)
	}
	files[".grain/schedules/it.yml"] = "template: nightly\nevery:\n  hours: 6\ndaily:\n  at: \"09:00\"\n"
	if msg := loadErr(t, files); !strings.Contains(msg, "exactly one cadence") {
		t.Fatalf("two cadences: error = %q", msg)
	}
}

// `every` is in hours or in minutes, and in minutes never under the
// ten-minute floor.
func TestAnEveryCadenceTakesOneUnitAndMinutesHaveAFloor(t *testing.T) {
	files := map[string]string{
		".grain/templates/nightly.yml": "title: Bump dependencies\n",
		".grain/schedules/it.yml":      "template: nightly\nevery:\n  hours: 1\n  minutes: 30\n",
	}
	if msg := loadErr(t, files); !strings.Contains(msg, "not both") {
		t.Fatalf("hours and minutes: error = %q", msg)
	}
	files[".grain/schedules/it.yml"] = "template: nightly\nevery:\n  minutes: 1\n"
	if msg := loadErr(t, files); !strings.Contains(msg, "at least 10") {
		t.Fatalf("every minute: error = %q", msg)
	}
}

// A schedule is a cadence and a template is the work, so a file that
// tries to be both is refused rather than quietly filing a task nobody
// can find the text of.
func TestAScheduleCarriesNoInlineContent(t *testing.T) {
	msg := loadErr(t, map[string]string{
		".grain/templates/nightly.yml": "title: Bump dependencies\n",
		".grain/schedules/it.yml":      "template: nightly\ntitle: Bump dependencies\nevery:\n  hours: 6\n",
	})
	if !strings.Contains(msg, "says when, and a template says") {
		t.Fatalf("error = %q", msg)
	}
}

func TestScheduleExplicitTimeZone(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/templates/nightly.yml": "title: Bump dependencies\n",
		".grain/schedules/it.yml":      "template: nightly\ntimezone: America/New_York\ndaily:\n  at: \"09:00\"\n",
	})
	if len(declared.Schedules) != 1 {
		t.Fatalf("len(Schedules) = %d, want 1", len(declared.Schedules))
	}
	sched := declared.Schedules[0].Schedule
	if sched.TimeZone != "America/New_York" {
		t.Errorf("sched.TimeZone = %q, want America/New_York", sched.TimeZone)
	}
	if sched.Recurrence.TimeZone != "America/New_York" {
		t.Errorf("sched.Recurrence.TimeZone = %q, want America/New_York", sched.Recurrence.TimeZone)
	}
}

func TestScheduleRejectsInvalidTimeZone(t *testing.T) {
	msg := loadErr(t, map[string]string{
		".grain/templates/nightly.yml": "title: Bump dependencies\n",
		".grain/schedules/it.yml":      "template: nightly\ntimezone: Mars/Olympus_Mons\ndaily:\n  at: \"09:00\"\n",
	})
	if !strings.Contains(msg, "invalid timezone") && !strings.Contains(msg, "unknown time zone") {
		t.Fatalf("expected timezone validation error, got %q", msg)
	}
}

func TestOneFilePerDeclaration(t *testing.T) {
	msg := loadErr(t, map[string]string{
		".grain/templates/nightly.yml":  "title: Bump dependencies\n",
		".grain/templates/nightly.yaml": "title: Bump dependencies again\n",
	})
	if !strings.Contains(msg, "one file per declaration") {
		t.Fatalf("error = %q", msg)
	}
}

// Every verdict at once, rather than the first one and then a rebuild.
func TestEveryFileIsReportedNotJustTheFirst(t *testing.T) {
	msg := loadErr(t, map[string]string{
		".grain/templates/one.yml": "body: no title here\n",
		".grain/templates/two.yml": "body: nor here\n",
	})
	if !strings.Contains(msg, "one.yml") || !strings.Contains(msg, "two.yml") {
		t.Fatalf("error = %q, want both files named", msg)
	}
}

// The identity a suite and a schedule name a template by is the file,
// not the prose inside it: rewording `name:` must not orphan the row.
func TestIdentityComesFromTheFileNotTheName(t *testing.T) {
	first := load(t, map[string]string{".grain/templates/nightly.yml": "name: One\ntitle: Bump dependencies\n"})
	second := load(t, map[string]string{".grain/templates/nightly.yml": "name: Two\ntitle: Bump dependencies\n"})
	if first.Templates[0].Template.ID != second.Templates[0].Template.ID {
		t.Fatalf("renaming a template changed its id: %q then %q",
			first.Templates[0].Template.ID, second.Templates[0].Template.ID)
	}
}

func TestAnUnnamedDeclarationIsCalledAfterItsFile(t *testing.T) {
	declared := load(t, map[string]string{".grain/templates/nightly.yml": "title: Bump dependencies\n"})
	if got := declared.Templates[0].Template.Name; got != "nightly" {
		t.Fatalf("name = %q, want the file's own stem", got)
	}
}

func TestReviewDeclarationConflictWithTemplatesReview(t *testing.T) {
	msg := loadErr(t, map[string]string{
		".grain/review.yml":           "prompt: check code\n",
		".grain/templates/review.yml": "title: Review template\nbody: check code\n",
	})
	if !strings.Contains(msg, `already declares "review" -- one file per declaration`) {
		t.Fatalf("error = %q", msg)
	}
}

// The inputs a template asks for are the field this arrangement found
// missing: they have been a column, a table and a row of the settings
// export since grain/task-58, and until pkg/declschema made somebody
// answer for them, `fields:` in a declaration was "field not found".
func TestATemplateDeclaresTheInputsItAsksFor(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/templates/bump.yml": `
title: Bump {{package}}
body: Bump {{package}} in {{environment}} and run the tests.
fields:
  - name: package
    label: Which package?
    required: true
  - name: environment
    options: [staging, production]
    default: staging
`,
	})
	fields := declared.Templates[0].Template.Fields
	if len(fields) != 2 {
		t.Fatalf("fields = %+v, want both of them in the order the file asks for them", fields)
	}
	if fields[0].Name != "package" || fields[0].Label != "Which package?" || !fields[0].Required {
		t.Errorf("first field = %+v", fields[0])
	}
	if !fields[1].Selector() || fields[1].Default != "staging" {
		t.Errorf("second field = %+v, want the options and the default", fields[1])
	}
}

// Checked as it is read, the way the same declaration typed into the UI
// is (model.ValidateTemplateFields): a hole nothing could ever fill is
// refused to whoever wrote the file.
func TestATemplateFieldNothingCouldFillIsRefused(t *testing.T) {
	msg := loadErr(t, map[string]string{
		".grain/templates/bump.yml": "title: Bump it\nfields:\n  - name: env\n    options: [staging]\n    default: prod\n",
	})
	if !strings.Contains(msg, "not one of its options") {
		t.Fatalf("error = %q", msg)
	}
}

func TestReviewDeclarationBothYmlAndYamlRefused(t *testing.T) {
	msg := loadErr(t, map[string]string{
		".grain/review.yml":  "prompt: check code 1\n",
		".grain/review.yaml": "prompt: check code 2\n",
	})
	if !strings.Contains(msg, `already declares "review" -- one file per declaration`) {
		t.Fatalf("error = %q", msg)
	}
}

func TestReviewDeclarationBodyAndPromptMismatchRefused(t *testing.T) {
	msg := loadErr(t, map[string]string{
		".grain/review.yml": "body: do foo\nprompt: do bar\n",
	})
	if !strings.Contains(msg, "specify body or prompt, not both") {
		t.Fatalf("error = %q", msg)
	}
}

// A declared schedule is a cadence: what it files is the template's, so
// the keys that say what a firing carries are refused here -- and now
// say so, rather than being keys the struct simply had no field for.
func TestAScheduleDoesNotRestateWhatTheTemplateSays(t *testing.T) {
	for _, field := range []string{
		"title: Bump dependencies",
		"body: do it",
		"auto-merge: true",
		"network: none",
		"nothing-needed: ack",
		"custom-fields:\n  team: infra",
	} {
		msg := loadErr(t, map[string]string{
			".grain/templates/nightly.yml": "title: Bump dependencies\n",
			".grain/schedules/it.yml":      "template: nightly\nevery:\n  hours: 6\n" + field + "\n",
		})
		if !strings.Contains(msg, "says when, and a template says") {
			t.Errorf("%q: error = %q, want the refusal to say where it belongs", field, msg)
		}
	}
}

// The bookkeeping half of the same list. An id is derived from the file
// and the firing times are grain's record of a cadence already counting
// down, so a file that writes either is answered rather than obeyed.
func TestADeclarationDoesNotWriteWhatGrainKeeps(t *testing.T) {
	msg := loadErr(t, map[string]string{
		".grain/templates/nightly.yml": "title: Bump dependencies\nid: template-7\n",
	})
	if !strings.Contains(msg, "identity is its file") {
		t.Errorf("id: error = %q", msg)
	}
	msg = loadErr(t, map[string]string{
		".grain/templates/nightly.yml": "title: Bump dependencies\n",
		".grain/schedules/it.yml":      "template: nightly\nevery:\n  hours: 6\nnext-run-at: 2026-09-19T12:00:00Z\n",
	})
	if !strings.Contains(msg, "grain keeps this, not the file") {
		t.Errorf("next-run-at: error = %q", msg)
	}
}

// One sentence per reason, not one per key: a file that reaches for two
// capabilities has made one mistake.
func TestRefusalsAreGroupedByReason(t *testing.T) {
	msg := loadErr(t, map[string]string{
		".grain/templates/nightly.yml": "title: Bump dependencies\nreads: [somebody/else]\ngrants: [gcp-key]\nid: template-7\n",
	})
	if !strings.Contains(msg, "grants, reads:") {
		t.Errorf("error = %q, want both reach keys named in one sentence", msg)
	}
	if strings.Count(msg, "never what that work may touch") != 1 {
		t.Errorf("error = %q, want the rule said once", msg)
	}
	if !strings.Contains(msg, "identity is its file") {
		t.Errorf("error = %q, want the other mistake reported too", msg)
	}
}
