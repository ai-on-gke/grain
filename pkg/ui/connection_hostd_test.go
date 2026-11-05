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
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// Nothing a task is filed or edited with chooses a hostd: a filing, a
// template, a template edit and a repository's settings that still name
// one are refused, saying where a hostd is set now, rather than ignored.
// Each attempt still shows the hostd, instance and owner its run
// recorded (task_run_hostd), read-only.
func TestATaskHostdIsRefusedAndEachRunShowsWhereItRan(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	registerHostds(t, store, model.Hostd{Name: "laptop"})
	refused := func(what string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), ui.TaskHostdRefusal) {
			t.Errorf("%s: err = %v, want the refusal %q", what, err, ui.TaskHostdRefusal)
		}
		var ve *ui.ValidationError
		if err != nil && !errors.As(err, &ve) {
			t.Errorf("%s: err %v is not a validation error (a 400)", what, err)
		}
	}
	_, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "train it", Description: "on a GPU", Approved: true, Hostd: "laptop"})
	refused("CreateTask", err)
	_, err = c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "train", Title: "Train", Description: "on a GPU", Hostd: "laptop"})
	refused("CreateTemplate", err)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "train", Title: "Train", Description: "on a GPU"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{Hostd: ptr("laptop")})
	refused("UpdateTemplate", err)
	_, err = c.SetRepoSettings(ctx, "acme/widgets", ui.SetRepoSettingsRequest{DefaultHostd: ptr("laptop")})
	refused("SetRepoSettings", err)

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "train it", Description: "on a GPU", Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	started := c.Now().UTC()
	if err := store.StartRun(ctx, model.Run{ID: task.ID + "-1", TaskID: task.ID, Sandbox: "s1", Attempt: 1, StartedAt: started}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordTaskRunHostd(ctx, model.TaskRunHostd{
		RunID: task.ID + "-1", TaskID: task.ID, Hostd: "laptop", Instance: "laptop.example.com", Owner: "alice@example.com", At: started.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	detail, err := c.GetTask(ctx, task.ID)
	if err != nil || len(detail.Attempts) != 1 {
		t.Fatalf("attempts = %+v, %v", detail.Attempts, err)
	}
	if h := detail.Attempts[0].Hostd; h == nil || h.Hostd != "laptop" || h.Instance != "laptop.example.com" || h.Owner != "alice@example.com" {
		t.Errorf("attempt 1's hostd = %+v, want laptop on laptop.example.com, owned by alice@example.com", h)
	}
}

// registerHostds puts hostds in the registry; the last is "gone",
// registered and revoked.
func registerHostds(t *testing.T, store *model.Store, hostds ...model.Hostd) {
	t.Helper()
	ctx := context.Background()
	for _, h := range append(hostds, model.Hostd{Name: "gone", Labels: map[string]string{"gpu": "yes", "zone": "a"}}) {
		if _, _, err := store.CreateHostd(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.RevokeHostd(ctx, "gone"); err != nil {
		t.Fatal(err)
	}
}
