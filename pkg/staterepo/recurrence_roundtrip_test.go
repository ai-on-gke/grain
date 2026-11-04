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

package staterepo_test

// Every cadence a schedule can have, through the settings document and
// back: export, import, export again is the identity.
//
// grain/task-2649 is why this ranges over model.RecurrenceKinds rather
// than over the kinds somebody remembered: the exporter wrote a
// github_workflow_run schedule as `trigger:`, the importer never looked
// for that key, and every deployment holding one could not import its
// own export -- which is to say could not upgrade. A kind added later
// without a case here fails TestEveryRecurrenceKindRoundTrips outright.

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

type recurrenceCase struct {
	name string
	r    model.Recurrence
	// block is the key the settings document carries the cadence under.
	block string
}

// recurrenceCases is the cases for each kind; every model.RecurrenceKind
// needs at least one.
var recurrenceCases = map[model.RecurrenceKind][]recurrenceCase{
	model.RecurrenceEveryNHours: {
		{"every 6 hours", model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 6}, "every"},
	},
	model.RecurrenceEveryNMinutes: {
		{"every 30 minutes", model.Recurrence{Kind: model.RecurrenceEveryNMinutes, EveryNMinutes: 30}, "every"},
	},
	model.RecurrenceDaily: {
		{"daily at 09:00", model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 9 * 60}, "daily"},
	},
	model.RecurrenceWeekly: {
		{"weekly on tuesday", model.Recurrence{Kind: model.RecurrenceWeekly, TimeOfDay: 17*60 + 30, Weekday: time.Tuesday}, "weekly"},
	},
	model.RecurrenceMonthly: {
		{"monthly on the 31st", model.Recurrence{Kind: model.RecurrenceMonthly, TimeOfDay: 0, DayOfMonth: 31}, "monthly"},
	},
	model.RecurrenceGitHubWorkflowRun: {
		{"workflow only", model.Recurrence{
			Kind:              model.RecurrenceGitHubWorkflowRun,
			GitHubWorkflowRun: &model.GitHubWorkflowRunTrigger{Workflow: "nightly.yml"},
		}, "trigger"},
		{"with conclusions", model.Recurrence{
			Kind: model.RecurrenceGitHubWorkflowRun,
			GitHubWorkflowRun: &model.GitHubWorkflowRunTrigger{
				Workflow: "nightly.yml", Conclusions: []string{"failure", "timed_out", "startup_failure"},
			},
		}, "trigger"},
		{"with branches", model.Recurrence{
			Kind: model.RecurrenceGitHubWorkflowRun,
			GitHubWorkflowRun: &model.GitHubWorkflowRunTrigger{
				Workflow: ".github/workflows/nightly.yml", Branches: []string{"main"},
			},
		}, "trigger"},
		{"with conclusions and branches", model.Recurrence{
			Kind: model.RecurrenceGitHubWorkflowRun,
			GitHubWorkflowRun: &model.GitHubWorkflowRunTrigger{
				Workflow:    "nightly.yml",
				Conclusions: []string{"failure", "timed_out", "startup_failure"},
				Branches:    []string{"main", "release/1.0"},
			},
		}, "trigger"},
	},
	model.RecurrenceGitRef: {
		{"another repository's tag", model.Recurrence{
			Kind:   model.RecurrenceGitRef,
			GitRef: &model.GitRefTrigger{Repo: "upstream/core", Ref: "refs/tags/latest"},
		}, "trigger"},
		{"a host-qualified repository's branch", model.Recurrence{
			Kind:   model.RecurrenceGitRef,
			GitRef: &model.GitRefTrigger{Repo: "git.example.com/team/lib", Ref: "refs/heads/main"},
		}, "trigger"},
	},
}

func TestEveryRecurrenceKindRoundTrips(t *testing.T) {
	t.Parallel()
	for _, kind := range model.RecurrenceKinds {
		cases := recurrenceCases[kind]
		if len(cases) == 0 {
			t.Errorf("recurrence kind %q has no round-trip case: add one to recurrenceCases", kind)
			continue
		}
		for _, c := range cases {
			t.Run(string(kind)+"/"+c.name, func(t *testing.T) {
				t.Parallel()
				roundTripRecurrence(t, c)
			})
		}
	}
	for kind := range recurrenceCases {
		found := false
		for _, k := range model.RecurrenceKinds {
			found = found || k == kind
		}
		if !found {
			t.Errorf("recurrenceCases has %q, which model.RecurrenceKinds does not list", kind)
		}
	}
}

func roundTripRecurrence(t *testing.T, c recurrenceCase) {
	t.Helper()
	ctx := context.Background()
	store, db := openDB(t)
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "sched-1", Title: c.name, Target: model.RepoRef{Owner: "example", Name: "repo"}, Recurrence: c.r,
		Overlap: model.OverlapSkip, Enabled: true, NextRunAt: now, CreatedAt: now,
	}); err != nil {
		t.Fatalf("putting a schedule: %v", err)
	}

	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	path := filepath.Join(dir, filepath.FromSlash(staterepo.DumpPath("schedule")))
	schedules := read(t, path)
	if !strings.Contains(schedules, "\n    "+c.block+":") {
		t.Errorf("the schedules document does not carry the cadence under `%s:`:\n%s", c.block, schedules)
	}
	if c.block != "recurrence" && strings.Contains(schedules, "recurrence:") {
		t.Errorf("the schedules document falls back to `recurrence:`:\n%s", schedules)
	}

	restored, freshDB := openDB(t)
	if err := staterepo.Import(ctx, freshDB, dir); err != nil {
		t.Fatalf("importing: %v", err)
	}
	got, err := restored.GetSchedule(ctx, "sched-1")
	if err != nil || got == nil {
		t.Fatalf("reading the schedule back: %v %v", got, err)
	}
	if !reflect.DeepEqual(got.Recurrence, c.r) {
		t.Errorf("recurrence = %+v (trigger %+v), want %+v (trigger %+v)",
			got.Recurrence, got.Recurrence.GitHubWorkflowRun, c.r, c.r.GitHubWorkflowRun)
	}

	again := t.TempDir()
	if err := staterepo.Export(ctx, freshDB, again); err != nil {
		t.Fatalf("exporting the restore: %v", err)
	}
	if got := read(t, filepath.Join(again, filepath.FromSlash(staterepo.DumpPath("schedule")))); got != schedules {
		t.Errorf("the restore exports a different schedules document:\n%s\nwant:\n%s", got, schedules)
	}
}
