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

	"github.com/bwsalmon/grain/pkg/model"
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
