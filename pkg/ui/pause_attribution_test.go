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

package ui_test

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// docs/test-runs/2026-11-03-operator-attribution.md F1: a pause made with
// the access code read "Paused by owner@example.com." -- the deployment's
// actor shown as a person nobody verified -- and every resume note said
// the task "was paused by the operator", whoever had paused it. Both
// notes, and Task.Paused.By, now name each actor as PrincipalLabel does.
func TestPauseAndResumeNotesNameWhoPausedAsTheirLabel(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	accessCode := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "owner@example.com", Source: model.SourceAccessCode})
	alice := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com", Source: model.SourceIAP})
	agent := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: "7", Source: model.SourceSandbox})

	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "paused", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Pause(accessCode, task.ID, ui.PauseOptions{}); err != nil {
		t.Fatal(err)
	}
	detail, err := client.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Paused == nil || detail.Paused.By != "operator (access code)" {
		t.Fatalf("paused = %+v, want by %q", detail.Paused, "operator (access code)")
	}
	if err := client.Resume(alice, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := client.Pause(agent, task.ID, ui.PauseOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := client.Resume(alice, task.ID); err != nil {
		t.Fatal(err)
	}

	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var notes []string
	for _, c := range comments {
		notes = append(notes, c.Body)
	}
	all := strings.Join(notes, "\n")
	for _, want := range []string{
		"Paused by operator (access code).",
		"Resumed by alice@example.com. This task was paused by operator (access code) at ",
		"Paused by task 7.",
		"Resumed by alice@example.com. This task was paused by task 7 at ",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("notes lack %q:\n%s", want, all)
		}
	}
	for _, never := range []string{"owner@example.com", "paused by the operator"} {
		if strings.Contains(all, never) {
			t.Errorf("notes say %q:\n%s", never, all)
		}
	}
}

// F2 of the same report: every auto-merge-on/-off event a Client edit
// wrote named ui.(*Client).mutate, the wrapper every edit goes through,
// as "the Go function that wrote it". It names the edit itself now.
func TestAutoMergeEventNamesTheEditNotTheMutateWrapper(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "t", Description: "d", AutoMerge: true})
	if err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := client.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{AutoMerge: &off}); err != nil {
		t.Fatal(err)
	}
	events := autoMergeEvents(t, store, ctx, task.ID)
	if len(events) == 0 {
		t.Fatal("no auto-merge event")
	}
	last := events[len(events)-1]
	if last.Kind != model.TaskEventAutoMergeOff || !strings.HasPrefix(last.Detail, "ui.(*Client).UpdateTask (client.go:") {
		t.Fatalf("event = %s %q, want auto-merge-off by ui.(*Client).UpdateTask", last.Kind, last.Detail)
	}
}
