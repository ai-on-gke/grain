package ui_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// pipelineWorld is a store with one of each thing the feed reads: a task
// filed at baseTime, a base-branch verdict an hour and two hours later,
// and a merge batch that finished at three.
func pipelineWorld(t *testing.T) (*ui.Client, ui.Task) {
	t.Helper()
	client, store, ctx := testClient(t)
	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "Teach the merge queue to wait", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	finished := baseTime.Add(2 * time.Hour)
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: repo, Branch: "main", RunID: "run-7",
		Commit: "abc123", StartedAt: baseTime.Add(time.Hour), FinishedAt: &finished, Conclusion: "failure",
		Attempts: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordMergeBatch(ctx, model.MergeBatchRecord{Repo: repo, Branch: "main", RunID: "run-8",
		Size: 3, Queued: 3, Outcome: model.MergeBatchMerged, Merged: 3,
		StartedAt: baseTime.Add(150 * time.Minute), FinishedAt: baseTime.Add(3 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	client.Now = func() time.Time { return baseTime.Add(4 * time.Hour) }
	return client, task
}

func changeWords(changes []ui.PipelineChange) string {
	var words []string
	for _, c := range changes {
		words = append(words, c.Kind+":"+c.From+">"+c.To)
	}
	return strings.Join(words, " ")
}

func TestPipelineChangesReadsEverySourceOldestFirst(t *testing.T) {
	client, task := pipelineWorld(t)
	page, err := client.PipelineChanges(t.Context(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := "task:>filed base:>running base:running>failure batch:running>merged"
	if got := changeWords(page.Changes); got != want {
		t.Fatalf("changes = %s, want %s", got, want)
	}
	if page.Changes[0].ID != task.ID || page.Changes[1].ID != "acme/widgets@main" {
		t.Errorf("ids = %q, %q; want the task and the branch", page.Changes[0].ID, page.Changes[1].ID)
	}
	if !strings.Contains(page.Changes[3].Detail, "merged 3 of 3") {
		t.Errorf("batch detail = %q, want how much of it merged", page.Changes[3].Detail)
	}
	if page.More {
		t.Error("More set on a page the limit did not cut")
	}
}

func TestPipelineChangesPagesByCursorWithoutRepeatingOrSkipping(t *testing.T) {
	client, _ := pipelineWorld(t)
	var seen []ui.PipelineChange
	var after *ui.PipelineCursor
	for range 5 {
		page, err := client.PipelineChanges(t.Context(), after, 1)
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, page.Changes...)
		c, err := ui.ParsePipelineCursor(page.Next)
		if err != nil {
			t.Fatalf("next %q does not parse: %v", page.Next, err)
		}
		after = &c
		if !page.More {
			break
		}
	}
	if got := changeWords(seen); got != "task:>filed base:>running base:running>failure batch:running>merged" {
		t.Fatalf("paged changes = %s", got)
	}
	// A read from the last cursor is empty and hands the same cursor back,
	// so a wake with nothing new writes the handover's cursor unchanged.
	page, err := client.PipelineChanges(t.Context(), after, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Changes) != 0 || page.Next != after.String() {
		t.Fatalf("read past the end = %d changes, next %q; want none and %q", len(page.Changes), page.Next, after)
	}
}

func TestPipelineChangesDatesATaskStallAtItsOnset(t *testing.T) {
	client, store, ctx := testClient(t)
	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "Stuck in the queue", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if task.State != model.StateQueued {
		if err := store.Approve(ctx, task.ID, model.Attribution{}, baseTime); err != nil {
			t.Fatal(err)
		}
	}
	// A queued task's patience of one hour: stalled from baseTime+1h.
	client.Config.Stall = func(state model.State, since, now time.Time) string {
		if state == model.StateQueued && now.Sub(since) >= time.Hour {
			return "queued for over an hour"
		}
		return ""
	}
	client.Now = func() time.Time { return baseTime.Add(5 * time.Hour) }
	page, err := client.PipelineChanges(ctx, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range page.Changes {
		if c.Kind == ui.PipelineChangeStall {
			if want := baseTime.Add(time.Hour); !c.At.Equal(want) {
				t.Errorf("stall at %s, want its onset %s", c.At, want)
			}
			if c.To != "queued stalled" {
				t.Errorf("stall to = %q", c.To)
			}
			return
		}
	}
	t.Fatalf("no stall in %s", changeWords(page.Changes))
}

func TestPipelineChangesRoute(t *testing.T) {
	client, _ := pipelineWorld(t)
	srv := ui.NewServerWithClient(client)
	rec := do(t, srv, http.MethodGet, "/api/pipeline/changes?limit=2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body)
	}
	page := decode[ui.PipelineChanges](t, rec)
	if len(page.Changes) != 2 || !page.More || page.Next == "" {
		t.Fatalf("page = %+v, want two changes, more, and a cursor", page)
	}
	for _, bad := range []string{"after=yesterday", "limit=-1"} {
		if rec := do(t, srv, http.MethodGet, "/api/pipeline/changes?"+bad, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("GET ?%s = %d, want 400", bad, rec.Code)
		}
	}
}

// A repository-scoped tasks:read grant reads only its own repository's
// feed: the gate admits it only to a call naming that repository, and the
// feed answers for that repository alone -- not, as GET /api/tasks does,
// for the whole deployment whatever repo= says.
func TestPipelineChangesRouteKeepsARepoScopedGrantToItsRepository(t *testing.T) {
	client, _ := pipelineWorld(t)
	srv := ui.NewServerWithClient(client)
	seedAPITask(t, client, "900", "acme/gadgets", "api:tasks:read@acme/gadgets")

	if code, body := asAgent(t, srv, "900", http.MethodGet, "/api/pipeline/changes", "", ""); code != http.StatusForbidden {
		t.Errorf("a repository-scoped grant naming no repository: %d %s, want 403", code, body)
	}
	if code, body := asAgent(t, srv, "900", http.MethodGet, "/api/pipeline/changes?repo=acme/widgets", "", ""); code != http.StatusForbidden {
		t.Errorf("a grant on acme/gadgets read acme/widgets: %d %s, want 403", code, body)
	}
	code, body := asAgent(t, srv, "900", http.MethodGet, "/api/pipeline/changes?repo=acme/gadgets", "", "")
	if code != http.StatusOK {
		t.Fatalf("a grant on acme/gadgets reading its own feed: %d %s", code, body)
	}
	if strings.Contains(body, "acme/widgets") || strings.Contains(body, "run-") {
		t.Errorf("acme/gadgets' feed carries acme/widgets' changes: %s", body)
	}

	page, err := client.PipelineChangesIn(t.Context(), "ACME/Widgets", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := changeWords(page.Changes), "task:>filed base:>running base:running>failure batch:running>merged"; got != want {
		t.Errorf("acme/widgets' feed = %s, want %s", got, want)
	}
}

func TestTaskExpectationRefusesOnEveryFieldThatDisagrees(t *testing.T) {
	task := ui.Task{ID: "832", Title: "Port the merge-queue fix", State: model.StateCompleted, PortOf: "831"}
	if err := (ui.TaskExpectation{State: "completed", TitlePrefix: "port the", Kind: "port"}).Check(task); err != nil {
		t.Fatalf("a matching expectation was refused: %v", err)
	}
	err := (ui.TaskExpectation{State: "queued", TitlePrefix: "Fix", Kind: "repair"}).Check(task)
	if err == nil {
		t.Fatal("a stale expectation was accepted")
	}
	for _, want := range []string{"state is completed, not queued", "does not start \"Fix\"", "kind is port, not repair", "Nothing was changed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not say %q", err, want)
		}
	}
	if err := (ui.TaskExpectation{Kind: "none"}).Check(task); err == nil {
		t.Error("kind none accepted for a port")
	}
}
