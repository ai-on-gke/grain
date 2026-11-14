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
	"github.com/ai-on-gke/grain/pkg/declschema"
	"reflect"
	"slices"
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
	// The repo server's spelling of the same access answers to the same
	// rule: no file reaches grain's own configuration through it either.
	for _, entry := range []string{
		model.RepoWriteGrant(model.RepoRef{Owner: "grain", Name: "config"}),
		model.RepoReadGrant(model.RepoRef{Owner: "Grain", Name: "Config"}),
	} {
		msg := loadErr(t, map[string]string{
			".grain/templates/nightly.yml": "title: Bump dependencies\ncapabilities: ['" + entry + "']\n",
		})
		if !strings.Contains(msg, "grain's own configuration") {
			t.Errorf("%s: error = %q, want grain's own configuration refused", entry, msg)
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

// `grants:` is `capabilities:`' unified spelling (unified-grants §3): the
// same entries, under the same rule, and an entry both keys name -- in
// either spelling -- is held once.
func TestGrantsIsCapabilitiesUnifiedSpelling(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/templates/nightly.yml": "title: Bump dependencies\n" +
			"capabilities: [\"repo-read:acme/docs\"]\n" +
			"grants:\n" +
			"  - capability: https://grain.dev/capabilities/repo#read\n    with: {repo: acme/docs}\n" +
			"  - capability: https://grain.dev/capabilities/repo#write\n    with: {repo: acme/site}\n",
		".grain/schedules/it.yml": "template: nightly\ncapabilities: [\"https://grain.dev/capabilities/release#package-publish\", \"https://grain.dev/capabilities/grain#ci\", \"api:tasks:read\", \"https://grain.dev/capabilities/api#tasks:read\"]\n" +
			"grants: [\"https://grain.dev/capabilities/release#package-publish\", \"api:ci-read@" + repo.String() + "\", \"https://grain.dev/capabilities/gcp#gcp-key={\\\"permission_set\\\":\\\"deploy\\\"}\"]\nevery:\n  hours: 6\n",
	})
	tmpl := declared.Templates[0].Template
	if want := []model.RepoRef{{Owner: "acme", Name: "docs"}}; !slices.EqualFunc(tmpl.Reads, want, model.RepoRef.Same) {
		t.Errorf("Reads = %v, want %v once", tmpl.Reads, want)
	}
	if want := []model.WriteTarget{{Repo: model.RepoRef{Owner: "acme", Name: "site"}}}; !reflect.DeepEqual(tmpl.Writes, want) {
		t.Errorf("Writes = %v, want %v", tmpl.Writes, want)
	}
	sched := declared.Schedules[0].Schedule
	var got []string
	for _, g := range sched.Grants {
		got = append(got, g.Capability)
	}
	if want := []string{"https://grain.dev/capabilities/release#package-publish", model.APIGrantString("ci-read", repo.String()), "api:tasks:read", `https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"deploy"}`}; !slices.Equal(got, want) {
		t.Errorf("schedule Grants = %v, want %v", got, want)
	}

	msg := loadErr(t, map[string]string{
		".grain/templates/nightly.yml": "title: Bump dependencies\ngrants: [\"https://grain.dev/capabilities/repo#target={\\\"repo\\\":\\\"acme/x\\\",\\\"base\\\":\\\"\\\"}\"]\n",
	})
	if !strings.Contains(msg, "always target the declaring repository") {
		t.Errorf("a #target under grants: error = %q, want capabilities:' refusal", msg)
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

// A suite's `qualification:` stanza travels beside it to the sync,
// as the file wrote it; a suite without one has none.
func TestASuiteDeclaresItselfAReleaseGate(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/templates/smoke.yml": "title: Smoke the edition\n",
		".grain/suites/edition-qualification.yml": `
templates: [smoke]
mode: until-clean
max-passes: 1
qualification:
  check: grain/edition-qualification
  candidate: synced-release
  stage1: { status: grain/edition-qualification }
`,
		".grain/suites/sweep.yml": "templates: [smoke]\n",
	})
	if len(declared.Suites) != 2 {
		t.Fatalf("declared %d suites, want 2", len(declared.Suites))
	}
	byPath := map[string]model.DeclaredSuiteFile{}
	for _, f := range declared.Suites {
		byPath[f.Path] = f
	}
	got := byPath[".grain/suites/edition-qualification.yml"].Qualification
	want := model.SuiteQualification{Check: "grain/edition-qualification", Candidate: model.CandidateSyncedRelease,
		Stage1Status: "grain/edition-qualification"}
	if got == nil || *got != want {
		t.Errorf("qualification = %+v, want %+v", got, want)
	}
	if q := byPath[".grain/suites/sweep.yml"].Qualification; q != nil {
		t.Errorf("a suite with no stanza has qualification %+v, want none", q)
	}
}

func TestABadQualificationStanzaIsRefused(t *testing.T) {
	for stanza, want := range map[string]string{
		"qualification:\n  candidate: nightly-tags\n":                     "candidate",
		"qualification:\n  stage1: { workflow: nightly, status: ci }\n":   "not both",
		"qualification:\n  stage1: {}\n":                                  "stage1",
		"qualification:\n  checks: grain/x\n":                             "checks",
		"qualification:\n  upstream: example.com/acme/core\n":             "only read",
		"qualification:\n  candidate: synced-release\n  upstream: nope\n": "upstream",
	} {
		msg := loadErr(t, map[string]string{
			".grain/templates/smoke.yml": "title: Smoke\n",
			".grain/suites/gate.yml":     "templates: [smoke]\n" + stanza,
		})
		if !strings.Contains(msg, want) {
			t.Errorf("%q: error = %q, want it to mention %q", stanza, msg, want)
		}
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

func TestScheduleRunsADeclaredSuiteOnItsBase(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/templates/nightly.yml": "title: Bump dependencies\n",
		".grain/suites/sweep.yml":      "templates: [nightly]\n",
		".grain/schedules/sweep.yml": `
name: Nightly sweep
suite: sweep
base: " main "
daily:
  at: "02:30"
`,
	})
	if len(declared.Schedules) != 1 {
		t.Fatalf("declared %d schedules, want 1", len(declared.Schedules))
	}
	sched := declared.Schedules[0].Schedule
	if sched.SuiteID == nil || *sched.SuiteID != model.RepoDeclaredID(model.DeclaredSuite, repo, "sweep") {
		t.Errorf("suiteId = %v", sched.SuiteID)
	}
	if sched.Base != "main" {
		t.Errorf("base = %q, want main", sched.Base)
	}
}

// A suite run stacks its tasks against one named branch and has no default
// branch to fall back to (ui.CreateSuiteRun, ui.CreateSchedule), so a
// declared suite schedule with no `base:` is refused with the file's path
// rather than synced to fire a base-less run.
func TestASuiteScheduleNeedsABase(t *testing.T) {
	for _, tc := range []struct {
		name string
		base string
	}{
		{"omitted", ""},
		{"blank", "base: \"   \"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := loadErr(t, map[string]string{
				".grain/templates/nightly.yml": "title: Bump dependencies\n",
				".grain/suites/sweep.yml":      "templates: [nightly]\n",
				".grain/schedules/sweep.yml":   "suite: sweep\n" + tc.base + "daily:\n  at: \"02:30\"\n",
			})
			for _, want := range []string{
				".grain/schedules/sweep.yml",
				"needs `base:`",
				"no default branch to fall back to",
			} {
				if !strings.Contains(msg, want) {
					t.Fatalf("error = %q, want %q", msg, want)
				}
			}
		})
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
		".grain/templates/nightly.yml": "title: Bump dependencies\nreads: [somebody/else]\nwrites: [somebody/else]\nid: template-7\n",
	})
	if !strings.Contains(msg, "reads, writes:") {
		t.Errorf("error = %q, want both reach keys named in one sentence", msg)
	}
	if strings.Count(msg, "never what that work may touch") != 1 {
		t.Errorf("error = %q, want the rule said once", msg)
	}
	if !strings.Contains(msg, "identity is its file") {
		t.Errorf("error = %q, want the other mistake reported too", msg)
	}
}

func TestDeclaredTemplatePriorityValidation(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/templates/urgent.yml":  "title: Urgent fix\npriority: 8\n",
		".grain/templates/default.yml": "title: Default fix\npriority: 0\n",
	})
	if len(declared.Templates) != 2 {
		t.Fatalf("Templates = %d, want 2", len(declared.Templates))
	}
	byName := map[string]model.Template{}
	for _, f := range declared.Templates {
		byName[f.Template.Name] = f.Template
	}
	if byName["urgent"].Priority == nil || *byName["urgent"].Priority != 8 {
		t.Errorf("urgent Priority = %v, want 8", byName["urgent"].Priority)
	}
	if byName["default"].Priority != nil {
		t.Errorf("default Priority = %v, want nil for 0", *byName["default"].Priority)
	}

	msg := loadErr(t, map[string]string{
		".grain/templates/bad.yml": "title: Bad priority\npriority: -1\n",
	})
	if !strings.Contains(msg, "priority must be a positive integer") {
		t.Errorf("negative priority error = %q, want 'priority must be a positive integer'", msg)
	}
}

// The issue importer's settings are grain's to set, not the repository's:
// a .grain/settings.yml declaring them -- above all one queueing imports
// without approval, which would let whoever can open an issue decide what
// an agent runs -- is refused as Authority, naming the key and the file
// (docs/test-runs/2026-10-31-github-issue-import.md, gap 4).
func TestDeclaredIssueImportIsRefusedAsAuthority(t *testing.T) {
	err := loadErr(t, map[string]string{
		".grain/settings.yml": "issue-import:\n  enabled: true\n  approved: true\n  label: grain\n",
	})
	for _, want := range []string{".grain/settings.yml", "issue-import", declschema.Authority.Reason()} {
		if !strings.Contains(err, want) {
			t.Fatalf("Load = %s\nwant it to say %q", err, want)
		}
	}
}

func TestDeclaredRepoCapabilityRejectsMissingRepoAndScheduleAccess(t *testing.T) {
	for _, bad := range []string{
		"https://grain.dev/capabilities/repo#read",
		`https://grain.dev/capabilities/repo#read={"repo":"not-a-repo"}`,
		"https://grain.dev/capabilities/repo#write",
		`https://grain.dev/capabilities/repo#write={"repo":"not-a-repo"}`,
	} {
		err := loadErr(t, map[string]string{
			".grain/templates/triage.yml": "title: Triage\ncapabilities:\n  - '" + bad + "'\n",
		})
		if !strings.Contains(err, "does not name a repository") {
			t.Errorf("template capability %q: got %q, want 'does not name a repository'", bad, err)
		}
	}

	schedErr := loadErr(t, map[string]string{
		".grain/templates/triage.yml": "title: Triage\n",
		".grain/schedules/nightly.yml": "template: triage\nevery:\n  hours: 6\ncapabilities:\n" +
			"  - capability: https://grain.dev/capabilities/repo#write\n    with:\n      repo: acme/widgets\n",
	})
	if !strings.Contains(schedErr, "repository access belongs on the template it fires") {
		t.Errorf("schedule with repo#write {repo}: got %q, want template-only refusal", schedErr)
	}
}

// `trigger: {git_ref: ...}` fires a template when another repository's ref
// moves (model.RecurrenceGitRef), and asks for a full ref name.
func TestAScheduleFiresATemplateWhenAnotherRepositorysRefMoves(t *testing.T) {
	files := map[string]string{
		".grain/templates/sync.yml": "title: Sync the upstream release\n",
		".grain/schedules/sync.yml": `
template: sync
trigger:
  git_ref:
    repo: upstream/core
    ref: refs/tags/latest
`,
	}
	declared := load(t, files)
	if len(declared.Schedules) != 1 {
		t.Fatalf("declared %d schedules, want 1", len(declared.Schedules))
	}
	r := declared.Schedules[0].Schedule.Recurrence
	if r.Kind != model.RecurrenceGitRef || r.GitRef == nil ||
		r.GitRef.Repo != "upstream/core" || r.GitRef.Ref != "refs/tags/latest" {
		t.Fatalf("recurrence = %+v (git_ref %+v)", r, r.GitRef)
	}

	files[".grain/schedules/sync.yml"] = "template: sync\ntrigger:\n  git_ref:\n    repo: upstream/core\n    ref: latest\n"
	if msg := loadErr(t, files); !strings.Contains(msg, "full `ref:`") {
		t.Fatalf("short ref: error = %q", msg)
	}
	files[".grain/schedules/sync.yml"] = "template: sync\ntrigger:\n  git_ref:\n    ref: refs/tags/latest\n"
	if msg := loadErr(t, files); !strings.Contains(msg, "repo: owner/name") {
		t.Fatalf("no repo: error = %q", msg)
	}
	files[".grain/schedules/sync.yml"] = "template: sync\ntrigger:\n  git_ref:\n    repo: grain/config\n    ref: refs/heads/main\n"
	if msg := loadErr(t, files); !strings.Contains(msg, "grain's own configuration") {
		t.Fatalf("watching grain/config: error = %q", msg)
	}
	// A retired per-team alias is refused too, as POST /api/schedules
	// refuses it (docs/test-runs/2026-11-07-releases-upgrades-ci.md, Bug F5).
	files[".grain/schedules/sync.yml"] = "template: sync\ntrigger:\n  git_ref:\n    repo: grain/config-alpha\n    ref: refs/heads/main\n"
	if msg := loadErr(t, files); !strings.Contains(msg, "grain/config-alpha not found: only grain/config exists") {
		t.Fatalf("watching retired grain/config-alpha: error = %q", msg)
	}
	files[".grain/schedules/sync.yml"] = "trigger:\n  git_ref:\n    repo: upstream/core\n    ref: refs/tags/latest\n"
	if msg := loadErr(t, files); !strings.Contains(msg, "needs a `template:`") {
		t.Fatalf("no template: error = %q", msg)
	}
}

func TestDeclaredAdmissionAndLegacyCapabilityAliases(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/templates/urgent.yml": "title: Urgent\nadmission: bypass\n",
		".grain/templates/legacy.yml": "title: Legacy\ncapabilities:\n  - priority-dispatch\n  - durable\n",
		".grain/schedules/urgent.yml": "template: urgent\nevery:\n  hours: 2\nadmission: durable\n",
		".grain/schedules/legacy.yml": "template: legacy\nevery:\n  hours: 4\ncapabilities:\n  - priority-dispatch\n",
	})
	tmpls := map[string]model.Template{}
	for _, f := range declared.Templates {
		tmpls[f.Template.Name] = f.Template
	}
	if tmpls["urgent"].Admission != model.AdmissionBypass {
		t.Errorf("urgent template Admission = %q, want %q", tmpls["urgent"].Admission, model.AdmissionBypass)
	}
	if tmpls["legacy"].Admission != model.AdmissionDurable {
		t.Errorf("legacy template Admission = %q, want %q", tmpls["legacy"].Admission, model.AdmissionDurable)
	}
	if len(tmpls["legacy"].Grants) != 0 {
		t.Errorf("legacy template Grants = %+v, want none", tmpls["legacy"].Grants)
	}
	scheds := map[string]model.Schedule{}
	for _, f := range declared.Schedules {
		scheds[f.Schedule.Title] = f.Schedule
	}
	if scheds["urgent"].Admission != model.AdmissionDurable {
		t.Errorf("urgent schedule Admission = %q, want %q", scheds["urgent"].Admission, model.AdmissionDurable)
	}
	if scheds["legacy"].Admission != model.AdmissionBypass || len(scheds["legacy"].Grants) != 0 {
		t.Errorf("legacy schedule = (%q, %+v), want (bypass, empty grants)", scheds["legacy"].Admission, scheds["legacy"].Grants)
	}

	if msg := loadErr(t, map[string]string{
		".grain/templates/bad.yml": "title: Bad\nadmission: turbo\n",
	}); !strings.Contains(msg, "admission") {
		t.Errorf("invalid template admission error = %q, want mention of admission", msg)
	}
	if msg := loadErr(t, map[string]string{
		".grain/settings.yml": "default-admission: bypass\n",
	}); !strings.Contains(msg, "default-admission") {
		t.Errorf("repo default-admission error = %q, want refusal of default-admission", msg)
	}
}

func TestDeclaredHostingAndLegacyCapabilityAlias(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/templates/shell.yml":  "title: Shell\nhosting: console\n",
		".grain/templates/legacy.yml": "title: Legacy\ncapabilities:\n  - console\n",
		".grain/schedules/shell.yml":  "template: shell\nevery:\n  hours: 2\nhosting: console\n",
		".grain/schedules/legacy.yml": "template: legacy\nevery:\n  hours: 4\ncapabilities:\n  - console\n",
	})
	tmpls := map[string]model.Template{}
	for _, f := range declared.Templates {
		tmpls[f.Template.Name] = f.Template
	}
	if tmpls["shell"].Hosting != model.HostingConsole {
		t.Errorf("shell template Hosting = %q, want %q", tmpls["shell"].Hosting, model.HostingConsole)
	}
	if tmpls["legacy"].Hosting != model.HostingConsole || len(tmpls["legacy"].Grants) != 0 {
		t.Errorf("legacy template = (%q, %+v), want (console, empty grants)", tmpls["legacy"].Hosting, tmpls["legacy"].Grants)
	}
	scheds := map[string]model.Schedule{}
	for _, f := range declared.Schedules {
		scheds[f.Schedule.Title] = f.Schedule
	}
	if scheds["shell"].Hosting != model.HostingConsole {
		t.Errorf("shell schedule Hosting = %q, want %q", scheds["shell"].Hosting, model.HostingConsole)
	}
	if scheds["legacy"].Hosting != model.HostingConsole || len(scheds["legacy"].Grants) != 0 {
		t.Errorf("legacy schedule = (%q, %+v), want (console, empty grants)", scheds["legacy"].Hosting, scheds["legacy"].Grants)
	}

	if msg := loadErr(t, map[string]string{
		".grain/templates/bad.yml": "title: Bad\nhosting: ssh\n",
	}); !strings.Contains(msg, "hosting") {
		t.Errorf("invalid template hosting error = %q, want mention of hosting", msg)
	}
	if msg := loadErr(t, map[string]string{
		".grain/suites/nightly.yml":  "name: Nightly\ntemplates: [shell]\n",
		".grain/templates/shell.yml": "title: Shell\n",
		".grain/schedules/bad.yml":   "suite: nightly\nbase: main\nevery:\n  hours: 24\nhosting: console\n",
	}); !strings.Contains(msg, "hosting") {
		t.Errorf("suite schedule hosting error = %q, want refusal of hosting", msg)
	}
	if msg := loadErr(t, map[string]string{
		".grain/settings.yml": "default-hosting: console\n",
	}); !strings.Contains(msg, "default-hosting") {
		t.Errorf("repo default-hosting error = %q, want refusal of default-hosting", msg)
	}
}

// A .grain template or schedule that declares `capabilities: [grain-ci]` (or
// its canonical grain#ci ref) wants read-only CI access to its own declaring
// repository: grain#ci itself is stripped by applyGrainCIGrant on a
// ci_mode: github repository and refused by CreateTask.
func TestDeclaredGrainCICapabilityMapsToRepoScopedCIRead(t *testing.T) {
	want := model.APIGrantString("ci-read", repo.String())
	for _, entry := range []string{model.GrainCIRef, model.GrainCIGrant} {
		declared := load(t, map[string]string{
			".grain/templates/train.yml": "title: Run train\ncapabilities:\n  - '" + entry + "'\n",
			".grain/schedules/train.yml": "template: train\nevery:\n  minutes: 20\ncapabilities:\n  - '" + entry + "'\n",
		})
		if len(declared.Templates) != 1 || len(declared.Templates[0].Template.Grants) != 1 ||
			declared.Templates[0].Template.Grants[0].Capability != want {
			t.Errorf("template(%q) grants = %+v, want [{Capability:%s}]", entry, declared.Templates[0].Template.Grants, want)
		}
		if len(declared.Schedules) != 1 || len(declared.Schedules[0].Schedule.Grants) != 1 ||
			declared.Schedules[0].Schedule.Grants[0].Capability != want {
			t.Errorf("schedule(%q) grants = %+v, want [{Capability:%s}]", entry, declared.Schedules[0].Schedule.Grants, want)
		}
	}

	// The retired bare grain-ci is no longer rewritten to the CI read.
	bare := load(t, map[string]string{
		".grain/templates/train.yml": "title: Run train\ncapabilities:\n  - 'grain-ci'\n",
	})
	if len(bare.Templates) != 1 || len(bare.Templates[0].Template.Grants) != 1 ||
		bare.Templates[0].Template.Grants[0].Capability != "grain-ci" {
		t.Errorf("bare grain-ci grants = %+v, want unrewritten [grain-ci]", bare.Templates[0].Template.Grants)
	}

	// Malformed capability-reference JSON is still rejected at parse time.
	if msg := loadErr(t, map[string]string{
		".grain/templates/train.yml": "title: Run train\ncapabilities:\n  - 'https://grain.dev/capabilities/grain#ci={bad'\n",
	}); msg == "" {
		t.Fatal("malformed grain#ci capability ref loaded without error")
	}
}

// repo#target (and repo#read / repo#write carrying a base) are task-request
// projections rather than capability grants: CreateTask refuses them, so a
// declaration naming one could never file a task. Refusing them here moves
// that verdict to `grain declarations check` and the declaration sync
// (docs/test-runs/2026-11-09-store-upgrades-128-129.md, O3).
func TestDeclaredCapabilitiesRefuseRepoTargetAndRequestFieldShapes(t *testing.T) {
	targetMessage := "always target the declaring repository (" + repo.String() + "), and its base is `base:`"
	for _, entry := range []string{
		"  - '" + model.RepoTargetRef + `={"repo":"acme/widgets","base":"main"}'` + "\n",
		"  - '" + model.RepoTargetRef + "'\n",
		"  - capability: " + model.RepoTargetRef + "\n    with:\n      repo: acme/widgets\n      base: main\n",
	} {
		for _, baseLine := range []string{"", "base: main\n"} {
			msg := loadErr(t, map[string]string{
				".grain/templates/t.yml": "title: Mixed\n" + baseLine + "capabilities:\n" + entry,
			})
			if !strings.Contains(msg, model.RepoTargetRef) || !strings.Contains(msg, targetMessage) {
				t.Errorf("template(%s%s): error = %q, want %s and %q", baseLine, entry, msg, model.RepoTargetRef, targetMessage)
			}
		}
		schedMsg := loadErr(t, map[string]string{
			".grain/templates/t.yml": "title: Plain\n",
			".grain/schedules/s.yml": "template: t\nevery:\n  hours: 6\ncapabilities:\n" + entry,
		})
		if !strings.Contains(schedMsg, model.RepoTargetRef) || !strings.Contains(schedMsg, targetMessage) {
			t.Errorf("schedule(%s): error = %q, want %s and %q", entry, schedMsg, model.RepoTargetRef, targetMessage)
		}
	}

	for _, tc := range []struct {
		entry string
		want  string
	}{
		{model.RepoWriteRef + `={"repo":"acme/other","base":"release"}`, "always lands on its default branch"},
		{model.RepoWriteRef + `={"repo":"acme/other","base":""}`, "always lands on its default branch"},
		{model.RepoReadRef + `={"repo":"acme/other","base":"main"}`, "takes only `repo: <owner>/<repo>`, not `base`"},
	} {
		msg := loadErr(t, map[string]string{
			".grain/templates/t.yml": "title: Mixed\ncapabilities:\n  - '" + tc.entry + "'\n",
		})
		if !strings.Contains(msg, tc.want) {
			t.Errorf("template(%q): error = %q, want %q", tc.entry, msg, tc.want)
		}
	}
}
