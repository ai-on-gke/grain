package ui_test

// POST /api/tasks/{id}/pull-request: the hop a dispatched run's own
// mcpserver makes to open its pull request while it is still running (see
// ui.Config.PullRequests). What actually opens one is the daemon's GitHub
// client, wired in behind Config.PullRequests, so these tests stand a
// fake in its place and assert on the plumbing around it: which task it
// is asked about, what comes back, and what happens when a deployment has
// none wired at all.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite"
	"github.com/bwsalmon/grain/pkg/ui"
)

type fakePullRequests struct {
	status     ui.PullRequestStatus
	err        error
	askedAbout []string
}

func (f *fakePullRequests) OpenForTask(_ context.Context, taskID string) (ui.PullRequestStatus, error) {
	f.askedAbout = append(f.askedAbout, taskID)
	return f.status, f.err
}

// pullRequestClient is testClient with a Config.PullRequests wired in --
// openers nil is the deployment that has none.
func pullRequestClient(t *testing.T, openers ui.PullRequests) (*ui.Client, context.Context) {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	client := ui.NewClient(ui.Config{
		Actor:         ui.DefaultActor("alice"),
		DefaultTarget: &repo,
		Capabilities:  ui.OfferedCapabilities(),
		PullRequests:  openers,
	}, store)
	client.Now = func() time.Time { return time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC) }
	return client, ctx
}

func TestOpenPullRequestAsksAboutTheTaskAndReportsWhatComesBack(t *testing.T) {
	t.Parallel()
	opener := &fakePullRequests{status: ui.PullRequestStatus{
		Repo: "acme/widgets", Number: 7, URL: "https://github.com/acme/widgets/pull/7",
		ChecksAvailable: true,
		Checks:          []ui.CheckStatus{{Name: "tests", Status: "in_progress"}},
	}}
	client, ctx := pullRequestClient(t, opener)
	task := create(t, client, ctx)

	status, err := client.OpenPullRequest(ctx, task.ID)
	if err != nil {
		t.Fatalf("OpenPullRequest: %v", err)
	}
	if len(opener.askedAbout) != 1 || opener.askedAbout[0] != task.ID {
		t.Fatalf("asked about %v, want just %q", opener.askedAbout, task.ID)
	}
	if status.Number != 7 || status.Repo != "acme/widgets" {
		t.Fatalf("status = %+v, want the fake's own answer", status)
	}
	if len(status.Checks) != 1 || status.Checks[0].Status != "in_progress" {
		t.Fatalf("checks = %+v, want the still-running one", status.Checks)
	}
}

// A task id nothing was ever filed under is a 404, not a call into
// GitHub: the opener is never even asked.
func TestOpenPullRequestForAnUnknownTask(t *testing.T) {
	t.Parallel()
	opener := &fakePullRequests{}
	client, ctx := pullRequestClient(t, opener)

	_, err := client.OpenPullRequest(ctx, "nope")
	var nf *ui.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want a NotFoundError", err)
	}
	if len(opener.askedAbout) != 0 {
		t.Fatalf("asked about %v, want nothing", opener.askedAbout)
	}
}

// A deployment with no GitHub client behind its UI answers 404 -- "this
// deployment does not offer that" -- rather than a 500 that reads like a
// failure, the same nil-means-unavailable shape POST /api/host/reboot
// already has.
func TestOpenPullRequestUnavailableWithoutAnOpener(t *testing.T) {
	t.Parallel()
	client, ctx := pullRequestClient(t, nil)
	task := create(t, client, ctx)
	srv := ui.NewServerWithClient(client)

	rec := do(t, srv, http.MethodPost, "/api/tasks/"+task.ID+"/pull-request", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}

	if _, err := client.OpenPullRequest(ctx, task.ID); err == nil {
		t.Fatal("expected a direct call to refuse too")
	}
}

// The whole hop, end to end over real HTTP: an HTTPClient (what
// `grain mcpserver` holds) against a real ui.Server.
func TestHTTPClientOpenPullRequestRoundTrip(t *testing.T) {
	t.Parallel()
	opener := &fakePullRequests{status: ui.PullRequestStatus{
		Repo: "acme/widgets", Number: 7, URL: "https://github.com/acme/widgets/pull/7",
		ChecksAvailable: true,
		Checks: []ui.CheckStatus{
			{Name: "lint", Status: "completed", Conclusion: "success"},
		},
	}}
	client, ctx := pullRequestClient(t, opener)
	task := create(t, client, ctx)
	srv := httptest.NewServer(ui.NewServerWithClient(client))
	t.Cleanup(srv.Close)

	status, err := accessCodeClient(client, srv.URL).OpenPullRequest(ctx, task.ID)
	if err != nil {
		t.Fatalf("OpenPullRequest over HTTP: %v", err)
	}
	if status.Number != 7 || status.URL != "https://github.com/acme/widgets/pull/7" {
		t.Fatalf("status = %+v, want the pull request the opener reported", status)
	}
	if !status.ChecksAvailable {
		t.Error("ChecksAvailable = false, want it carried across the wire")
	}
	if len(status.Checks) != 1 || status.Checks[0].Conclusion != "success" {
		t.Fatalf("checks = %+v, want the completed one with its conclusion", status.Checks)
	}
}

// A failure to open one (an unpushed branch, GitHub refusing) reaches the
// caller with its own message intact -- that message is what the agent
// reads and acts on.
func TestHTTPClientOpenPullRequestReportsAFailure(t *testing.T) {
	t.Parallel()
	opener := &fakePullRequests{err: errors.New("grain/task-1 is not on acme/widgets yet")}
	client, ctx := pullRequestClient(t, opener)
	task := create(t, client, ctx)
	srv := httptest.NewServer(ui.NewServerWithClient(client))
	t.Cleanup(srv.Close)

	_, err := accessCodeClient(client, srv.URL).OpenPullRequest(ctx, task.ID)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "is not on acme/widgets yet") {
		t.Fatalf("err = %v, want the opener's own message", err)
	}
}

// reattachingPullRequests is a PullRequests that can also take a
// reopened task's pull request back (ui.PullRequestReattacher).
type reattachingPullRequests struct {
	fakePullRequests
	reattached []string
}

func (f *reattachingPullRequests) ReattachForTask(_ context.Context, taskID string) error {
	f.reattached = append(f.reattached, taskID)
	return nil
}

// Close, then reopen: the reopen is what asks for the pull request still
// open from the task's branch to be attached again -- the promise the
// close-time note makes -- and the close does not.
func TestReopeningATaskAsksForItsPullRequestBack(t *testing.T) {
	t.Parallel()
	prs := &reattachingPullRequests{}
	client, ctx := pullRequestClient(t, prs)
	task := create(t, client, ctx)

	if err := client.Close(ctx, task.ID, ui.CloseOptions{}); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(prs.reattached) != 0 {
		t.Fatalf("closing asked to re-attach %v", prs.reattached)
	}
	if err := client.Reopen(ctx, task.ID); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if len(prs.reattached) != 1 || prs.reattached[0] != task.ID {
		t.Fatalf("re-attached %v, want just %q", prs.reattached, task.ID)
	}
}

// nothingToPropose is the shape of orchestrator's alreadyLandedError as
// pkg/ui sees it: an error that says the branch's work is already on its
// base.
type nothingToPropose struct{ msg string }

func (e *nothingToPropose) Error() string          { return e.msg }
func (e *nothingToPropose) NothingToPropose() bool { return true }

// A branch whose work already landed is an answer, not a failure: 409
// with the sentence intact, never the 500 an agent reads as grain broken
// (docs/test-runs/2026-10-04-merge-queue-correctness.md).
func TestOpenPullRequestForWorkThatAlreadyLandedIsAConflictNotAServerError(t *testing.T) {
	t.Parallel()
	const said = "This branch's changes already landed on main as 696d2c0fe83d (acme/widgets#9); nothing to open."
	opener := &fakePullRequests{err: fmt.Errorf("opening: %w", &nothingToPropose{msg: said})}
	client, ctx := pullRequestClient(t, opener)
	task := create(t, client, ctx)
	srv := httptest.NewServer(ui.NewServerWithClient(client))
	t.Cleanup(srv.Close)
	remote := accessCodeClient(client, srv.URL)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/tasks/"+task.ID+"/pull-request", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+remote.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for work that already landed", resp.StatusCode)
	}
	_, err = remote.OpenPullRequest(ctx, task.ID)
	if err == nil || !strings.Contains(err.Error(), "already landed on main as 696d2c0fe83d") {
		t.Fatalf("err = %v, want the landed sentence", err)
	}
	if strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v, still reads as a server error", err)
	}
}
