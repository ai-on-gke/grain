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

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

const mergeRulesFile = `
defaults:
  ci:
    mode: grain
  stall_timeout: 90m
rules:
  - id: docs
    description: Documentation only needs lint.
    paths: ["docs/**"]
    ci:
      grain: [lint]
  - id: ui
    paths: ["ui/**"]
    ci:
      grain: ["ui e2e"]
  - id: deploy
    paths: ["deploy/**"]
    ci:
      mode: both
      github: [build, cla/google]
    auto_merge: never
    stall_timeout: 30m
`

const lintWorkflow = `
name: checks
on: [push]
jobs:
  lint:
    steps:
      - run: true
  ui:
    name: ui e2e
    steps:
      - run: true
`

func TestMergeRulesParseEveryField(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/merge-rules.yml":      mergeRulesFile,
		".grain/workflows/checks.yml": lintWorkflow,
	})
	got := declared.MergeRules
	if got == nil {
		t.Fatal("no merge rules declared")
	}
	want := &model.MergeRules{
		Path: ".grain/merge-rules.yml",
		Defaults: model.MergePolicy{CI: &model.CIRule{Mode: model.CIGateGrain},
			StallTimeout: 90 * time.Minute},
		Rules: []model.MergeRule{
			{ID: "docs", Description: "Documentation only needs lint.", Paths: []string{"docs/**"},
				MergePolicy: model.MergePolicy{CI: &model.CIRule{Grain: []string{"lint"}}}},
			{ID: "ui", Paths: []string{"ui/**"},
				MergePolicy: model.MergePolicy{CI: &model.CIRule{Grain: []string{"ui e2e"}}}},
			{ID: "deploy", Paths: []string{"deploy/**"}, MergePolicy: model.MergePolicy{
				CI:        &model.CIRule{Mode: model.CIGateBoth, GitHub: []string{"build", "cla/google"}},
				AutoMerge: model.AutoMergeNever, StallTimeout: 30 * time.Minute}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merge rules =\n%+v\nwant\n%+v", got, want)
	}
}

func TestMergeRulesWarnAboutAJobNothingDeclares(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/merge-rules.yml":      "rules:\n  - id: docs\n    paths: [docs]\n    ci:\n      grain: [lint, linter]\n",
		".grain/workflows/checks.yml": lintWorkflow,
	})
	w := declared.MergeRules.Warnings
	if len(w) != 1 || !strings.Contains(w[0], `"linter"`) || !strings.Contains(w[0], "rule docs") {
		t.Fatalf("warnings = %q, want one naming linter in rule docs", w)
	}
}

func TestMergeRulesAreReadUnderTheirOldName(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/sensitive.yml": "rules:\n  - id: deploy\n    paths: [deploy]\n    auto_merge: never\n",
	})
	if declared.MergeRules == nil || declared.MergeRules.Path != model.MergeRulesAliasPath {
		t.Fatalf("merge rules = %+v, want them read from %s", declared.MergeRules, model.MergeRulesAliasPath)
	}
	if len(declared.MergeRules.Warnings) != 0 {
		t.Fatalf("warnings = %q, want none", declared.MergeRules.Warnings)
	}
}

func TestMergeRulesUnderBothNamesWarnAndReadTheNewOne(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/merge-rules.yml": "rules:\n  - id: a\n    paths: [a]\n",
		".grain/sensitive.yml":   "rules:\n  - id: b\n    paths: [b]\n",
	})
	r := declared.MergeRules
	if r.Path != model.MergeRulesPath || len(r.Rules) != 1 || r.Rules[0].ID != "a" {
		t.Fatalf("merge rules = %+v, want .grain/merge-rules.yml's", r)
	}
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], ".grain/sensitive.yml") {
		t.Fatalf("warnings = %q, want one naming .grain/sensitive.yml", r.Warnings)
	}
}

func TestMergeRuleRefusals(t *testing.T) {
	cases := map[string]struct{ file, want string }{
		"unknown key":       {"rules:\n  - id: a\n    paths: [a]\n    reads: [x]\n", "reads"},
		"unknown top key":   {"rule: []\n", "rule"},
		"unknown ci key":    {"rules:\n  - id: a\n    paths: [a]\n    ci:\n      jobs: [x]\n", "jobs"},
		"no id":             {"rules:\n  - paths: [a]\n", "id is required"},
		"bad id":            {"rules:\n  - id: Docs Only\n    paths: [a]\n", "id must be"},
		"duplicate id":      {"rules:\n  - id: a\n    paths: [a]\n  - id: a\n    paths: [b]\n", "already declared"},
		"no paths":          {"rules:\n  - id: a\n", "at least one path"},
		"bad glob":          {"rules:\n  - id: a\n    paths: [\"a/[b\"]\n", "paths:"},
		"bad mode":          {"rules:\n  - id: a\n    paths: [a]\n    ci:\n      mode: none\n", "not one of grain, github, both"},
		"bad auto_merge":    {"rules:\n  - id: a\n    paths: [a]\n    auto_merge: sometimes\n", "not one of allowed, never"},
		"bad stall_timeout": {"rules:\n  - id: a\n    paths: [a]\n    stall_timeout: soon\n", "not a duration"},
		"tiny stall":        {"defaults:\n  stall_timeout: 5s\n", "shorter than a minute"},
		"review":            {"rules:\n  - id: a\n    paths: [a]\n    review:\n      human: required\n", ".grain/triggers"},
		"bad batching":      {"rules:\n  - id: a\n    paths: [a]\n    batching: never\n", "not alone or a maximum batch size"},
		"zero batching":     {"defaults:\n  batching: 0\n", "1 or more, or alone"},
		"too many repairs":  {"rules:\n  - id: a\n    paths: [a]\n    repairs: 3\n", "not 0, 1 or 2"},
		"negative repairs":  {"defaults:\n  repairs: -1\n", "not 0, 1 or 2"},
		"bad flake_recheck": {"rules:\n  - id: a\n    paths: [a]\n    flake_recheck: sometimes\n", "flake_recheck"},
		"bad port":          {"rules:\n  - id: a\n    paths: [a]\n    port: maybe\n", "port"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			msg := loadErr(t, map[string]string{".grain/merge-rules.yml": c.file})
			if !strings.Contains(msg, c.want) || !strings.Contains(msg, ".grain/merge-rules.yml") {
				t.Errorf("error = %q, want it to name the file and say %q", msg, c.want)
			}
		})
	}
}

// grain/task-1660's options: batching, repairs, flake_recheck and port.
func TestMergeRulesParseTheQueueOptions(t *testing.T) {
	declared := load(t, map[string]string{".grain/merge-rules.yml": `
defaults:
  batching: 4
  flake_recheck: true
rules:
  - id: orchestrator
    paths: ["pkg/orchestrator/**"]
    batching: alone
    repairs: 1
  - id: docs
    paths: ["docs/**"]
    port: true
  - id: release
    paths: ["deploy/**"]
    batching: 2
    repairs: 0
    flake_recheck: false
    port: false
`})
	zero, one, yes, no := 0, 1, true, false
	got := declared.MergeRules
	want := &model.MergeRules{
		Path:     ".grain/merge-rules.yml",
		Defaults: model.MergePolicy{Batching: 4, FlakeRecheck: &yes},
		Rules: []model.MergeRule{
			{ID: "orchestrator", Paths: []string{"pkg/orchestrator/**"},
				MergePolicy: model.MergePolicy{Batching: model.BatchAlone, Repairs: &one}},
			{ID: "docs", Paths: []string{"docs/**"}, MergePolicy: model.MergePolicy{Port: &yes}},
			{ID: "release", Paths: []string{"deploy/**"}, MergePolicy: model.MergePolicy{
				Batching: 2, Repairs: &zero, FlakeRecheck: &no, Port: &no}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merge rules =\n%+v\nwant\n%+v", got, want)
	}
	if got.Empty() {
		t.Fatal("a file whose defaults only batch is not empty")
	}
}
