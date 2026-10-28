package daemon

// query_merge_queue reports which branch each queued pull request merges
// into, and filters on it. A task's Base is empty for work on its
// repository's default branch, so these pin that the report resolves the
// real branch rather than printing and filtering on the empty string
// (F10 in docs/test-runs/2026-10-05-merge-rules.md).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bwsalmon/grain/pkg/ui"
)

// mergeQueueDaemon answers the three reads daemonMergeQueue makes.
func mergeQueueDaemon(t *testing.T, tasks []ui.Task, batches []ui.MergeBatch) *ui.HTTPClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body any
		switch r.URL.Path {
		case "/api/tasks":
			body = tasks
		case "/api/pipeline-pause":
			body = ui.PipelinePauseStatus{}
		case "/api/merge-batches":
			body = ui.MergeBatches{Batches: batches}
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return ui.NewHTTPClient(srv.URL)
}

func TestDaemonMergeQueueResolvesDefaultBranch(t *testing.T) {
	tasks := []ui.Task{
		{ID: "1", Repo: "o/r", MergeQueueState: "waiting"},                    // default branch
		{ID: "2", Repo: "o/r", Base: "candidate", MergeQueueState: "waiting"}, // declared base
		{ID: "3", Repo: "o/r", MergeQueueState: "testing"},                    // in a batch on main
		{ID: "4", Repo: "o/r"}, // not queued
	}
	batches := []ui.MergeBatch{{Repo: "o/r", Branch: "main", Members: []ui.MergeBatchMember{{TaskID: "3"}}}}
	lookups := 0
	q := daemonMergeQueue{
		client: mergeQueueDaemon(t, tasks, batches),
		defaultBranch: func(owner, repo string) (string, error) {
			lookups++
			if owner != "o" || repo != "r" {
				t.Errorf("default branch asked for %s/%s", owner, repo)
			}
			return "main", nil
		},
	}

	report, err := q.QueryMergeQueue(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, pr := range report.QueuedPRs {
		got[pr.TaskID] = pr.Branch
	}
	want := map[string]string{"1": "main", "2": "candidate", "3": "main"}
	if len(got) != len(want) {
		t.Fatalf("queued = %v, want %v", got, want)
	}
	for id, b := range want {
		if got[id] != b {
			t.Errorf("task %s branch = %q, want %q", id, got[id], b)
		}
	}
	if lookups != 1 {
		t.Errorf("default branch looked up %d times, want 1", lookups)
	}

	report, err = q.QueryMergeQueue(context.Background(), "", "main")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, pr := range report.QueuedPRs {
		ids = append(ids, pr.TaskID)
	}
	if len(ids) != 2 || ids[0] != "1" || ids[1] != "3" {
		t.Errorf("branch=main kept %v, want [1 3]", ids)
	}
	if len(report.Batches) != 1 {
		t.Errorf("branch=main batches = %d, want 1", len(report.Batches))
	}
}

// Without a way to read the default branch, a default-branch pull request
// is still reported -- it may be the one asked about -- rather than
// silently filtered out.
func TestDaemonMergeQueueKeepsUnresolvedBranch(t *testing.T) {
	tasks := []ui.Task{
		{ID: "1", Repo: "o/r", MergeQueueState: "waiting"},
		{ID: "2", Repo: "o/r", Base: "candidate", MergeQueueState: "waiting"},
	}
	q := daemonMergeQueue{
		client:        mergeQueueDaemon(t, tasks, nil),
		defaultBranch: func(string, string) (string, error) { return "", errors.New("no credential") },
	}
	report, err := q.QueryMergeQueue(context.Background(), "", "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.QueuedPRs) != 1 || report.QueuedPRs[0].TaskID != "1" {
		t.Fatalf("branch=main kept %+v, want only task 1", report.QueuedPRs)
	}
}

func TestDaemonMergeQueuePlainGitHost(t *testing.T) {
	lookups := 0
	q := daemonMergeQueue{
		client: mergeQueueDaemon(t, nil, nil),
		defaultBranch: func(owner, repo string) (string, error) {
			lookups++
			if owner != "git.example.com/acme" || repo != "widgets" {
				t.Errorf("default branch asked for %s/%s, want git.example.com/acme/widgets", owner, repo)
			}
			return "", errors.New("repository \"git.example.com/acme/widgets\" is on git.example.com, a plain git host with no forge API")
		},
	}

	report, err := q.QueryMergeQueue(context.Background(), "git.example.com/acme/widgets", "")
	if err != nil {
		t.Fatal(err)
	}
	if !report.Paused {
		t.Errorf("report.Paused = false, want true")
	}
	wantPause := "Merge queue is unavailable for git.example.com/acme/widgets: repository is on git.example.com, a plain git host"
	if report.PauseInfo != wantPause {
		t.Errorf("report.PauseInfo = %q, want %q", report.PauseInfo, wantPause)
	}
	if lookups != 1 {
		t.Errorf("lookups = %d, want 1", lookups)
	}
}
