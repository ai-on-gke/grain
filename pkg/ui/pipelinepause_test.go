package ui_test

// grain/task-38: the deliberate dispatch gate as an operator sees it --
// GET/POST/DELETE /api/pipeline-pause, and the copy of the same reading
// GET /api/config carries so a banner can stand on every page.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type pipelinePauseBody struct {
	Pause   ui.PipelinePauseStatus `json:"pause"`
	Paused  *bool                  `json:"pausedNow"`
	Resumed *bool                  `json:"resumed"`
}

func TestPipelinePauseRoutes(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	_ = client.Store.PutConfig(context.Background(), model.Config{PollInterval: model.DefaultPollInterval})

	// A deployment nobody has paused.
	rec := do(t, srv, http.MethodGet, "/api/pipeline-pause", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[pipelinePauseBody](t, rec); got.Pause.Paused {
		t.Fatalf("got %+v on a fresh deployment, want not paused", got.Pause)
	}

	rec = do(t, srv, http.MethodPost, "/api/pipeline-pause", `{"reason":"patching the host"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("pausing: status = %d, %s", rec.Code, rec.Body)
	}
	got := decode[pipelinePauseBody](t, rec)
	if !got.Pause.Paused || got.Pause.Reason != "patching the host" {
		t.Fatalf("got %+v, want a pause carrying the reason it was given", got.Pause)
	}
	if got.Paused == nil || !*got.Paused {
		t.Error("pausedNow says this call did not pause a deployment that was running")
	}
	if got.Pause.ByKind != string(model.PrincipalHuman) {
		t.Errorf("byKind = %q, want a human: everything reaching this route is somebody at a UI or a CLI",
			got.Pause.ByKind)
	}
	if got.Pause.UntilTask != "" {
		t.Errorf("untilTask = %q, want empty: an operator's own pause waits on no task", got.Pause.UntilTask)
	}

	// The banner's own copy of it.
	rec = do(t, srv, http.MethodGet, "/api/config", "")
	cfg := decode[struct {
		PipelinePause *ui.PipelinePauseStatus `json:"pipelinePause"`
	}](t, rec)
	if cfg.PipelinePause == nil || !cfg.PipelinePause.Paused {
		t.Fatalf("GET /api/config carries %+v, want the pause a banner would draw", cfg.PipelinePause)
	}

	// Pausing an already-paused deployment replaces the reason and says
	// it was not this call that stopped it.
	rec = do(t, srv, http.MethodPost, "/api/pipeline-pause", `{"reason":"still patching"}`)
	got = decode[pipelinePauseBody](t, rec)
	if got.Paused == nil || *got.Paused {
		t.Error("pausedNow claims this call paused a deployment that was already paused")
	}
	if got.Pause.Reason != "still patching" {
		t.Errorf("reason = %q, want the newer one", got.Pause.Reason)
	}

	rec = do(t, srv, http.MethodDelete, "/api/pipeline-pause", "")
	got = decode[pipelinePauseBody](t, rec)
	if got.Pause.Paused {
		t.Errorf("got %+v after a resume, want a running deployment", got.Pause)
	}
	if got.Resumed == nil || !*got.Resumed {
		t.Error("resumed says there was no pause to lift, and there was")
	}

	// Resuming one that is already running is not an error -- whoever
	// pressed the button wanted a deployment that is running, and it is.
	rec = do(t, srv, http.MethodDelete, "/api/pipeline-pause", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("second resume: status = %d, %s", rec.Code, rec.Body)
	}
	if got := decode[pipelinePauseBody](t, rec); got.Resumed == nil || *got.Resumed {
		t.Error("resumed claims a second call lifted something")
	}
}

// A pause with no reason at all still says something: an empty banner is
// worse than a vague one, and an operator in a hurry should not have to
// type to stop a deployment.
func TestPausingWithNoReasonCarriesADefaultOne(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	_ = client.Store.PutConfig(context.Background(), model.Config{PollInterval: model.DefaultPollInterval})

	rec := do(t, srv, http.MethodPost, "/api/pipeline-pause", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[pipelinePauseBody](t, rec); !got.Pause.Paused || got.Pause.Reason == "" {
		t.Fatalf("got %+v, want a pause with some reason on it", got.Pause)
	}
}

// grain/task-399: a run holding dispatch-control pauses and runs tasks
// as itself, so the pause row and the task's conversation name the agent
// and its task rather than the operator. Only an agent may be named, and
// the reading says whether Run now passes the pause it describes.
func TestDispatchControlWritesAreAttributedToTheAgent(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()
	_ = client.Store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval})

	rec := do(t, srv, http.MethodPost, "/api/pipeline-pause",
		`{"reason":"manual mode","requestedBy":"7","requestedByKind":"agent"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("pausing as an agent: status = %d, %s", rec.Code, rec.Body)
	}
	got := decode[pipelinePauseBody](t, rec)
	if got.Pause.ByKind != string(model.PrincipalAgent) || got.Pause.ByID != "7" {
		t.Fatalf("paused by %s %q, want agent 7", got.Pause.ByKind, got.Pause.ByID)
	}
	if !got.Pause.RunNowPasses {
		t.Error("runNowPasses = false on an agent's own pause, want Run now to pass it")
	}

	rec = do(t, srv, http.MethodPost, "/api/pipeline-pause",
		`{"reason":"x","requestedBy":"someone","requestedByKind":"human"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("pausing as a named human: status = %d, want 400: a request names only an agent", rec.Code)
	}

	task := create(t, client, ctx)
	rec = do(t, srv, http.MethodPost, "/api/tasks/"+task.ID+"/run-now",
		`{"requestedBy":"7","requestedByKind":"agent"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("run-now as an agent: status = %d, %s", rec.Code, rec.Body)
	}
	detail, err := client.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Comments) != 1 || detail.Comments[0].Author != "7" {
		t.Fatalf("run-now note = %+v, want one note by agent 7", detail.Comments)
	}

	if err := client.Store.SetPipelinePause(ctx, model.PipelinePause{
		PausedAt: time.Now(), Reason: "upgrading", By: model.UpgradePrincipal,
	}); err != nil {
		t.Fatal(err)
	}
	rec = do(t, srv, http.MethodGet, "/api/pipeline-pause", "")
	if got := decode[pipelinePauseBody](t, rec); got.Pause.RunNowPasses {
		t.Error("runNowPasses = true under an upgrade's hold, want false: its restart would discard the run")
	}

	// An agent's pause taken over that hold (or standing before the
	// upgrade was scheduled) is still an upgrade on its way, and the
	// cycle holds a Run now behind it: the reading has to say so.
	if err := client.Store.SetPipelinePause(ctx, model.PipelinePause{
		PausedAt: time.Now(), Reason: "manual mode", By: model.Principal{Kind: model.PrincipalAgent, ID: "7"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := client.Store.SetScheduledUpgrade(ctx, model.ScheduledUpgrade{
		Branch: "main", RequestedAt: time.Now(), By: model.ScheduledUpgradePrincipal,
	}); err != nil {
		t.Fatal(err)
	}
	rec = do(t, srv, http.MethodGet, "/api/pipeline-pause", "")
	if got := decode[pipelinePauseBody](t, rec); got.Pause.RunNowPasses {
		t.Error("runNowPasses = true with an upgrade scheduled, want false: the cycle holds Run now behind it")
	}
}
