package ui_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A refused delete of a console session in external mode leaves the
// session's task running: the route cancels that task before its store
// write, so it is refused at the guard rather than rehearsed.

func TestExternalModeRefusalDoesNotStopTheConsoleSessionsTask(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	storeSettingsRow(t, client)
	task := model.Task{
		ID:     "77",
		Intent: model.IntentAnalyze,
		Title:  "Director",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "console-session"}},
			Reason:      model.ReasonConsoleSession,
		},
		Tags:        []string{model.ConsoleSessionTag(model.DirectorSessionID)},
		Interactive: true,
		CreatedAt:   &baseTime,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	canceller := &recordingTaskCanceller{}
	client.Config.TaskCanceller = canceller
	client.Config.StateRepo = &fakeStateRepo{status: ui.StateRepoStatus{Remote: "https://github.com/acme/grain-config.git"}}

	rec := do(t, ui.NewServerWithClient(client), http.MethodDelete, "/api/console-sessions/"+model.DirectorSessionID, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409: %s", rec.Code, rec.Body)
	}
	if len(canceller.cancelled) != 0 {
		t.Fatalf("a refused delete cancelled %v", canceller.cancelled)
	}
	got, err := store.GetTask(context.Background(), "77")
	if err != nil || got == nil {
		t.Fatalf("GetTask: %v, %v", got, err)
	}
}
