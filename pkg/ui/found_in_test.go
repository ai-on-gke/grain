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
	"encoding/json"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestCreateTaskRecordsTheReleaseItIsFoundIn: the new-task form's "Found
// in" is stored as the task's model.LinkFoundIn and comes back on the
// task JSON as foundIn; a value that is no release version is refused; a
// backport filed onto release/vX.Y.Z with none stated records the
// branch's version.
func TestCreateTaskRecordsTheReleaseItIsFoundIn(t *testing.T) {
	client, store, ctx := testClient(t)

	created, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "Fix the crash on upgrade", FoundIn: "2.5.0-rc.2"})
	if err != nil {
		t.Fatal(err)
	}
	if created.FoundIn != "v2.5.0-rc.2" {
		t.Errorf("created foundIn = %q, want v2.5.0-rc.2", created.FoundIn)
	}
	stored, err := store.GetTask(ctx, created.ID)
	if err != nil || stored == nil {
		t.Fatal(err)
	}
	if got := model.FoundIn(*stored); got != "v2.5.0-rc.2" {
		t.Errorf("stored found-in = %q, want v2.5.0-rc.2", got)
	}
	detail, err := client.GetTask(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"foundIn":"v2.5.0-rc.2"`) {
		t.Errorf("task JSON does not carry foundIn:\n%s", raw)
	}

	if _, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "Fix it", FoundIn: "latest"}); err == nil {
		t.Error("a found-in that is no release version was accepted")
	}

	backport, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "Backport the fix", Repo: "acme/widgets", Base: "release/v2.4.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if backport.FoundIn != "v2.4.0" {
		t.Errorf("backport foundIn = %q, want v2.4.0", backport.FoundIn)
	}

	plain, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "Tidy the README"})
	if err != nil {
		t.Fatal(err)
	}
	if plain.FoundIn != "" {
		t.Errorf("a task against no release has foundIn %q", plain.FoundIn)
	}
}
