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

// bwsalmon/agents#446: GET /api/tasks/{id}/attempts/{number}/transcript is
// the API surface behind the UI's "show attempt agent logs" pane -- these
// cover status codes and JSON shape; client_test.go's TestAttemptTranscript
// already covers what the Client itself does with the store.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestGetAttemptTranscriptReturnsWhatWasRecorded(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()
	task := create(t, client, ctx)

	if err := client.Store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: task.ID, Sandbox: "s1",
		Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := client.Store.FinishRun(ctx, "r1", baseTime.Add(time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := client.Store.SetRunTranscript(ctx, "r1", "> run_command(...)\nok\n\npushed the change"); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodGet, "/api/tasks/"+task.ID+"/attempts/1/transcript", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[struct {
		Transcript string `json:"transcript"`
	}](t, rec)
	if got.Transcript != "> run_command(...)\nok\n\npushed the change" {
		t.Fatalf("transcript = %q", got.Transcript)
	}
}

func TestGetAttemptTranscriptOfUnknownAttemptIs404(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	task := create(t, client, context.Background())

	rec := do(t, srv, http.MethodGet, "/api/tasks/"+task.ID+"/attempts/1/transcript", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
}

func TestGetAttemptTranscriptRejectsANonIntegerAttemptNumber(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	task := create(t, client, context.Background())

	rec := do(t, srv, http.MethodGet, "/api/tasks/"+task.ID+"/attempts/nope/transcript", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
}
