package model_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// docsUIDeploy is docs/declarations.md's example file.
func docsUIDeploy() *model.MergeRules {
	return &model.MergeRules{
		Path: model.MergeRulesPath, Commit: "base1",
		Rules: []model.MergeRule{
			{ID: "docs", Paths: []string{"docs/**"},
				MergePolicy: model.MergePolicy{CI: &model.CIRule{Grain: []string{"lint"}}}},
			{ID: "ui", Paths: []string{"ui/**"},
				MergePolicy: model.MergePolicy{CI: &model.CIRule{Grain: []string{"ui e2e"}}}},
			{ID: "deploy", Paths: []string{"deploy/**"}, MergePolicy: model.MergePolicy{
				CI:        &model.CIRule{Mode: model.CIGateBoth, GitHub: []string{"build"}},
				AutoMerge: model.AutoMergeNever, StallTimeout: 30 * time.Minute}},
		},
	}
}

func TestMergeRulesModeIsTheStrictestOfTheRepositoryAndEveryMatchedRule(t *testing.T) {
	rules := docsUIDeploy()
	cases := []struct {
		name  string
		repo  model.CIMode
		files []string
		want  model.CIGateMode
	}{
		{"repository on grain, docs only", model.CIModeGrain, []string{"docs/a.md"}, model.CIGateGrain},
		{"repository on github, docs only", model.CIModeGitHub, []string{"docs/a.md"}, model.CIGateGitHub},
		{"deploy asks for both", model.CIModeGrain, []string{"deploy/x.tf"}, model.CIGateBoth},
		{"both beats grain across rules", model.CIModeGrain, []string{"docs/a.md", "deploy/x.tf"}, model.CIGateBoth},
		{"a rule adds grain to github", model.CIModeGitHub, nil, model.CIGateGitHub},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := rules.Effective(c.files, true, c.repo).Mode; got != c.want {
				t.Fatalf("mode = %q, want %q", got, c.want)
			}
		})
	}
	grainRule := &model.MergeRules{Rules: []model.MergeRule{{ID: "g", Paths: []string{"x"},
		MergePolicy: model.MergePolicy{CI: &model.CIRule{Mode: model.CIGateGrain}}}}}
	if got := grainRule.Effective([]string{"x/y"}, true, model.CIModeGitHub).Mode; got != model.CIGateBoth {
		t.Fatalf("grain rule on a github repository = %q, want both: a rule adds, never replaces", got)
	}
}

func TestMergeRulesNarrowTheGrainSuiteOnlyWhenEveryMatchedRuleDoes(t *testing.T) {
	rules := docsUIDeploy()

	p := rules.Effective([]string{"docs/a.md"}, true, model.CIModeGrain)
	if p.GrainFullSuite || !reflect.DeepEqual(p.GrainJobs, []string{"lint"}) {
		t.Fatalf("docs only: full=%v jobs=%v, want narrowed to lint", p.GrainFullSuite, p.GrainJobs)
	}

	p = rules.Effective([]string{"docs/a.md", "ui/b.js"}, true, model.CIModeGrain)
	if p.GrainFullSuite || !reflect.DeepEqual(p.GrainJobs, []string{"lint", "ui e2e"}) {
		t.Fatalf("docs and ui: full=%v jobs=%v, want narrowed to the union", p.GrainFullSuite, p.GrainJobs)
	}

	// A file no rule matches is the defaults', which name no jobs: the
	// full suite, with the named jobs on top.
	p = rules.Effective([]string{"docs/a.md", "pkg/x.go"}, true, model.CIModeGrain)
	if !p.GrainFullSuite || !reflect.DeepEqual(p.GrainJobs, []string{"lint"}) {
		t.Fatalf("docs and code: full=%v jobs=%v, want the full suite plus lint", p.GrainFullSuite, p.GrainJobs)
	}

	// deploy names no grain jobs, so it wants the whole suite.
	p = rules.Effective([]string{"docs/a.md", "deploy/x.tf"}, true, model.CIModeGrain)
	if !p.GrainFullSuite {
		t.Fatalf("docs and deploy: narrowed to %v, want the full suite", p.GrainJobs)
	}

	// A file list that could not be read in full applies every rule and
	// the defaults: never narrowed.
	p = rules.Effective([]string{"docs/a.md"}, false, model.CIModeGrain)
	if !p.GrainFullSuite || !p.AutoMergeNever {
		t.Fatalf("unread list: full=%v never=%v, want both", p.GrainFullSuite, p.AutoMergeNever)
	}

	// Defaults that name jobs narrow an unmatched file too.
	narrowDefaults := docsUIDeploy()
	narrowDefaults.Defaults = model.MergePolicy{CI: &model.CIRule{Grain: []string{"go"}}}
	p = narrowDefaults.Effective([]string{"docs/a.md", "pkg/x.go"}, true, model.CIModeGrain)
	if p.GrainFullSuite || !reflect.DeepEqual(p.GrainJobs, []string{"lint", "go"}) {
		t.Fatalf("narrow defaults: full=%v jobs=%v, want lint and go", p.GrainFullSuite, p.GrainJobs)
	}
}

func TestMergeRulesUnionGitHubChecks(t *testing.T) {
	rules := docsUIDeploy()
	rules.Rules = append(rules.Rules, model.MergeRule{ID: "cla", Paths: []string{"deploy/**"},
		MergePolicy: model.MergePolicy{CI: &model.CIRule{GitHub: []string{"cla/google", "build"}}}})
	p := rules.Effective([]string{"deploy/x.tf"}, true, model.CIModeGrain)
	if !reflect.DeepEqual(p.GitHubChecks, []string{"build", "cla/google"}) || p.GitHubAllChecks {
		t.Fatalf("checks = %v all=%v, want build and cla/google only", p.GitHubChecks, p.GitHubAllChecks)
	}
	p = rules.Effective([]string{"deploy/x.tf", "README.md"}, true, model.CIModeGrain)
	if !p.GitHubAllChecks {
		t.Fatal("an unmatched file brings the defaults, which gate on every check")
	}
}

func TestMergeRulesAutoMergeNeverBeatsAllowed(t *testing.T) {
	rules := docsUIDeploy()
	rules.Defaults.AutoMerge = model.AutoMergeAllowed
	if p := rules.Effective([]string{"docs/a.md"}, true, model.CIModeGrain); p.AutoMergeNever {
		t.Fatal("docs only may merge itself")
	}
	p := rules.Effective([]string{"docs/a.md", "deploy/x.tf"}, true, model.CIModeGrain)
	if !p.AutoMergeNever {
		t.Fatal("deploy says never, and never beats allowed")
	}
	found := false
	for _, r := range p.Requirements {
		if r.Field == "auto_merge" && reflect.DeepEqual(r.From, []string{"deploy"}) {
			found = true
		}
	}
	if !found {
		t.Fatalf("requirements = %+v, want auto_merge from deploy", p.Requirements)
	}
}

func TestMergeRulesStallTimeoutIsTheShortest(t *testing.T) {
	rules := docsUIDeploy()
	rules.Defaults.StallTimeout = 90 * time.Minute
	if got := rules.Effective([]string{"docs/a.md"}, true, model.CIModeGrain).StallTimeout; got != 90*time.Minute {
		t.Fatalf("docs only: stall = %s, want the defaults' 1h30m", got)
	}
	if got := rules.Effective([]string{"deploy/x.tf"}, true, model.CIModeGrain).StallTimeout; got != 30*time.Minute {
		t.Fatalf("deploy: stall = %s, want 30m", got)
	}
	if got := docsUIDeploy().Effective([]string{"docs/a.md"}, true, model.CIModeGrain).StallTimeout; got != 0 {
		t.Fatalf("nothing set: stall = %s, want 0 (the deployment's deadline)", got)
	}
}

func TestMergePolicySaysWhichRuleCausedEachRequirement(t *testing.T) {
	p := docsUIDeploy().Effective([]string{"docs/a.md", "deploy/x.tf"}, true, model.CIModeGrain)
	text := p.Describe()
	for _, want := range []string{
		"`ci.mode: grain` -- from repository setting",
		"`ci.mode: both` -- from rule `deploy`",
		"`ci.grain: lint` -- from rule `docs`",
		"`ci.grain: full suite` -- from rule `deploy`",
		"`ci.github: build` -- from rule `deploy`",
		"`auto_merge: never` -- from rule `deploy`",
		"`stall_timeout: 30m` -- from rule `deploy`",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("description lacks %q:\n%s", want, text)
		}
	}
	link := model.MergePolicyLink(p)
	back, ok := model.MergePolicyOf(model.Task{Links: []model.Link{link}})
	if !ok || !reflect.DeepEqual(back, p) {
		t.Fatalf("round trip = %+v, %v; want %+v", back, ok, p)
	}
}

// queueOptions is a file using every one of grain/task-1660's options.
func queueOptions() *model.MergeRules {
	zero, one, two, yes, no := 0, 1, 2, true, false
	return &model.MergeRules{
		Path: model.MergeRulesPath, Commit: "base1",
		Defaults: model.MergePolicy{Batching: 5, Repairs: &two, FlakeRecheck: &yes, Port: &yes},
		Rules: []model.MergeRule{
			{ID: "orchestrator", Paths: []string{"pkg/orchestrator/**"},
				MergePolicy: model.MergePolicy{Batching: model.BatchAlone, Repairs: &one}},
			{ID: "small", Paths: []string{"pkg/ci/**"}, MergePolicy: model.MergePolicy{Batching: 3}},
			{ID: "smaller", Paths: []string{"pkg/ci/engine/**"}, MergePolicy: model.MergePolicy{Batching: 2}},
			{ID: "docs", Paths: []string{"docs/**"}, MergePolicy: model.MergePolicy{Port: &yes}},
			{ID: "release", Paths: []string{"deploy/**"},
				MergePolicy: model.MergePolicy{Repairs: &zero, FlakeRecheck: &no, Port: &no}},
		},
	}
}

func requirementFrom(p model.EffectiveMergePolicy, field string) (string, []string) {
	for _, r := range p.Requirements {
		if r.Field == field {
			return r.Value, r.From
		}
	}
	return "", nil
}

func TestBatchingAloneBeatsAnySizeAndOtherwiseTheSmallestWins(t *testing.T) {
	rules := queueOptions()
	cases := []struct {
		name  string
		files []string
		want  int
		value string
		from  []string
	}{
		{"defaults only", []string{"README.md"}, 5, "5", []string{model.PolicySourceDefaults}},
		{"one size", []string{"pkg/ci/run.go"}, 3, "3", []string{"small"}},
		{"the smaller of two sizes", []string{"pkg/ci/engine/x.go"}, 2, "2", []string{"smaller"}},
		{"alone beats every size", []string{"pkg/ci/engine/x.go", "pkg/orchestrator/sync.go"},
			model.BatchAlone, "alone", []string{"orchestrator"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := rules.Effective(c.files, true, model.CIModeGrain)
			if p.BatchLimit != c.want {
				t.Fatalf("batch limit = %d, want %d", p.BatchLimit, c.want)
			}
			value, from := requirementFrom(p, "batching")
			if value != c.value || !reflect.DeepEqual(from, c.from) {
				t.Fatalf("batching requirement = %q from %v, want %q from %v", value, from, c.value, c.from)
			}
		})
	}
	if p := docsUIDeploy().Effective([]string{"docs/a.md"}, true, model.CIModeGrain); p.BatchLimit != 0 {
		t.Fatalf("no rule batching: limit = %d, want none", p.BatchLimit)
	}
}

func TestRepairsTheFewestWins(t *testing.T) {
	rules := queueOptions()
	cases := []struct {
		name  string
		files []string
		want  int
		from  []string
	}{
		{"defaults only", []string{"README.md"}, 2, nil},
		{"one", []string{"pkg/orchestrator/sync.go"}, 1, []string{"orchestrator"}},
		{"zero beats one", []string{"pkg/orchestrator/sync.go", "deploy/x.tf"}, 0, []string{"release"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := rules.Effective(c.files, true, model.CIModeGrain)
			if p.Repairs == nil || *p.Repairs != c.want {
				t.Fatalf("repairs = %v, want %d", p.Repairs, c.want)
			}
			n, from := p.RepairLimit(model.MaxAutomaticRepairs)
			if n != c.want || !reflect.DeepEqual(from, c.from) {
				t.Fatalf("repair limit = %d from %v, want %d from %v", n, from, c.want, c.from)
			}
		})
	}
	var none *model.EffectiveMergePolicy
	if n, _ := none.RepairLimit(model.MaxAutomaticRepairs); n != model.MaxAutomaticRepairs {
		t.Fatalf("no policy: limit %d, want the queue's own %d", n, model.MaxAutomaticRepairs)
	}
}

func TestFlakeRecheckAndPortFalseBeatsTrue(t *testing.T) {
	rules := queueOptions()
	p := rules.Effective([]string{"docs/a.md"}, true, model.CIModeGrain)
	if p.NoFlakeRecheck || p.NoPort {
		t.Fatalf("docs only: no flake recheck %v, no port %v; want both on", p.NoFlakeRecheck, p.NoPort)
	}
	if value, from := requirementFrom(p, "port"); value != "true" ||
		!reflect.DeepEqual(from, []string{"docs", model.PolicySourceDefaults}) {
		t.Fatalf("port requirement = %q from %v, want true from docs and the defaults", value, from)
	}
	p = rules.Effective([]string{"docs/a.md", "deploy/x.tf"}, true, model.CIModeGrain)
	if !p.NoFlakeRecheck || !p.NoPort {
		t.Fatalf("docs and release: no flake recheck %v, no port %v; want both off", p.NoFlakeRecheck, p.NoPort)
	}
	for _, field := range []string{"flake_recheck", "port"} {
		if value, from := requirementFrom(p, field); value != "false" || !reflect.DeepEqual(from, []string{"release"}) {
			t.Fatalf("%s requirement = %q from %v, want false from release", field, value, from)
		}
	}
	if d := p.Describe(); !strings.Contains(d, "`port: false` -- from rule `release`") ||
		!strings.Contains(d, "`repairs: 0` -- from rule `release`") {
		t.Fatalf("description does not name the rules:\n%s", d)
	}
}

// The defaults' queue options apply to every pull request, like its
// auto_merge: a file that says only them is still a file.
func TestDefaultsQueueOptionsApplyToEveryChange(t *testing.T) {
	no := false
	rules := &model.MergeRules{Defaults: model.MergePolicy{Port: &no},
		Rules: []model.MergeRule{{ID: "docs", Paths: []string{"docs/**"}}}}
	if rules.Empty() {
		t.Fatal("a file whose defaults say port: false is not empty")
	}
	if p := rules.Effective([]string{"docs/a.md"}, true, model.CIModeGrain); !p.NoPort {
		t.Fatal("the defaults' port: false did not apply to a change a rule matched")
	}
}
