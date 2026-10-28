package ui_test

// HTTPClient's own tests: the same operations client_test.go already
// proves against a direct model.Store caller, driven instead through a
// real net/http round trip against a real httptest.Server wrapping
// ui.Server -- bwsalmon/agents#363's whole point, that a CLI reaching
// the daemon over REST behaves the same as one that used to open the
// store itself.

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

func testHTTPClient(t *testing.T) (*ui.HTTPClient, context.Context) {
	t.Helper()
	storeClient, _, ctx := testClient(t)
	srv := httptest.NewServer(ui.NewServerWithClient(storeClient))
	t.Cleanup(srv.Close)
	return accessCodeClient(storeClient, srv.URL), ctx
}

func TestHTTPClientCreateListAndGetRoundTrip(t *testing.T) {
	t.Parallel()
	c, ctx := testHTTPClient(t)

	created, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "fix it", Description: "please", Approved: true,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if created.ID == "" {
		t.Fatal("created task has no id")
	}
	if created.State != model.StateQueued {
		t.Fatalf("state = %q, want queued", created.State)
	}

	tasks, err := c.ListTasks(ctx)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != created.ID {
		t.Fatalf("ListTasks = %+v, want just the created task", tasks)
	}

	got, err := c.Task(ctx, created.ID)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if got.ID != created.ID {
		t.Fatalf("Task = %+v, want id %q", got, created.ID)
	}

	detail, err := c.GetTask(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if detail.ID != created.ID || detail.Comments == nil {
		t.Fatalf("GetTask = %+v, want the task plus a (possibly empty, non-nil) comment slice", detail)
	}
}

func TestHTTPClientUpdateSetCapabilityCommentCloseReopen(t *testing.T) {
	t.Parallel()
	c, ctx := testHTTPClient(t)
	created, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "fix it", Approved: true})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	title := "fix it for real"
	updated, err := c.UpdateTask(ctx, created.ID, ui.UpdateTaskRequest{Title: &title})
	if err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	if updated.Title != title {
		t.Fatalf("title after update = %q, want %q", updated.Title, title)
	}

	if err := c.SetCapability(ctx, created.ID, "gemini-key", true); err != nil {
		t.Fatalf("SetCapability(attach): %v", err)
	}
	withCap, err := c.Task(ctx, created.ID)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if len(withCap.Capabilities) != 1 || withCap.Capabilities[0] != "gemini-key" {
		t.Fatalf("capabilities after attach = %+v, want [gemini-key]", withCap.Capabilities)
	}
	if err := c.SetCapability(ctx, created.ID, "gemini-key", false); err != nil {
		t.Fatalf("SetCapability(detach): %v", err)
	}

	if err := c.AddComment(ctx, created.ID, "hello", nil); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	detail, err := c.GetTask(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	// Two, not one: UpdateTask's own title edit above already added the
	// first, noting the change for the same reason a live run needs to
	// see it (bwsalmon/agents#523) -- AddComment's "hello" is the second.
	if len(detail.Comments) != 2 || detail.Comments[0].Body == "" || detail.Comments[1].Body != "hello" {
		t.Fatalf("comments = %+v, want the title-edit note followed by one saying hello", detail.Comments)
	}

	if err := c.Close(ctx, created.ID, ui.CloseOptions{}); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closed, err := c.Task(ctx, created.ID)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if closed.State != model.StateClosed {
		t.Fatalf("state after close = %q, want closed", closed.State)
	}

	if err := c.Reopen(ctx, created.ID); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	reopened, err := c.Task(ctx, created.ID)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if reopened.State != model.StateQueued {
		t.Fatalf("state after reopen = %q, want queued", reopened.State)
	}
}

// TestHTTPClientRetry rounds out HTTPClient's own tests with Retry, the
// one mutating method nothing here exercised through a real HTTP round
// trip before now -- server_test.go and client_test.go each already
// prove the route and the underlying Client method.
func TestHTTPClientRetry(t *testing.T) {
	t.Parallel()
	c, ctx := testHTTPClient(t)
	created, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "fix it", Approved: true})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Retry on a task that has never failed is documented as a harmless
	// no-op (Client.Retry); this just proves the round trip itself
	// succeeds rather than erroring.
	if err := c.Retry(ctx, created.ID); err != nil {
		t.Fatalf("Retry: %v", err)
	}
}

// CloseOptions survives the round trip -- the half of `grain close
// -close-pull-request` that no direct-Client test can show, since the
// flag has to be encoded, sent, and read back off the request body
// before it reaches the same Client.Close everything else tests.
func TestHTTPClientCloseCarriesTheOptionAcrossTheWire(t *testing.T) {
	t.Parallel()
	for _, want := range []bool{false, true} {
		storeClient, store, ctx := testClient(t)
		closer := &recordingCloser{}
		storeClient.Config.PullRequestCloser = closer
		storeClient.Config.PullRequestComments = &recordingComments{}
		srv := httptest.NewServer(ui.NewServerWithClient(storeClient))
		t.Cleanup(srv.Close)
		task, ref := linkedTask(t, storeClient, store, ctx)

		c := accessCodeClient(storeClient, srv.URL)
		if err := c.Close(ctx, task.ID, ui.CloseOptions{ClosePullRequest: want}); err != nil {
			t.Fatalf("Close(%t): %v", want, err)
		}
		closed, err := c.Task(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if closed.State != model.StateClosed {
			t.Fatalf("state after close = %q, want closed", closed.State)
		}
		if want && (len(closer.closed) != 1 || closer.closed[0] != ref) {
			t.Fatalf("closed on GitHub = %+v, want %s", closer.closed, ref)
		}
		if !want && len(closer.closed) != 0 {
			t.Fatalf("closed on GitHub = %+v, want nothing", closer.closed)
		}
	}
}

func TestHTTPClientApproveOnAProposal(t *testing.T) {
	t.Parallel()
	c, ctx := testHTTPClient(t)
	created, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "needs a look"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if created.State != model.StateProposed {
		t.Fatalf("state as created = %q, want proposed", created.State)
	}
	if err := c.Approve(ctx, created.ID); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	approved, err := c.Task(ctx, created.ID)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if approved.State != model.StateQueued {
		t.Fatalf("state after approve = %q, want queued", approved.State)
	}

	// And back again, over the same wire: `grain withdraw`'s own round
	// trip, which is the only thing this client is for.
	if err := c.WithdrawApproval(ctx, created.ID); err != nil {
		t.Fatalf("WithdrawApproval: %v", err)
	}
	withdrawn, err := c.Task(ctx, created.ID)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if withdrawn.State != model.StateProposed {
		t.Fatalf("state after withdrawing approval = %q, want proposed", withdrawn.State)
	}
}

func TestHTTPClientTaskNotFoundIsNotFoundError(t *testing.T) {
	t.Parallel()
	c, ctx := testHTTPClient(t)
	_, err := c.GetTask(ctx, "does-not-exist")
	var nf *ui.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("GetTask on an unknown id: err = %v, want a *ui.NotFoundError", err)
	}
	// The server already formats its 404 body as "no task <id>" (see
	// Client's own NotFoundError.Error()); httpError used to wrap that
	// whole message back into NotFoundError{ID: message}, whose own
	// Error() prepended "no task " a second time, so every HTTPClient
	// caller -- the CLI, and the browser frontend, which displays the
	// JSON body's "error" field verbatim -- saw "no task no task
	// does-not-exist" instead of "no task does-not-exist".
	if got := nf.Error(); got != "no task does-not-exist" {
		t.Errorf("NotFoundError.Error() = %q, want %q", got, "no task does-not-exist")
	}
}

func TestHTTPClientCreateValidationErrorIsValidationError(t *testing.T) {
	t.Parallel()
	c, ctx := testHTTPClient(t)
	_, err := c.CreateTask(ctx, ui.CreateTaskRequest{})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("CreateTask with no title: err = %v, want a *ui.ValidationError", err)
	}
}

// A 403 is writeClientError's answer to a ForbiddenError, and comes back
// as one: before, it was the only status writeClientError derives from a
// typed error that the client flattened into a plain error, so a caller
// could not tell "you may not" from any other failure.
func TestHTTPClientForbiddenIsForbiddenError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"user \"bob\" does not have administrator privileges"}`))
	}))
	t.Cleanup(srv.Close)
	_, err := ui.NewHTTPClient(srv.URL).GetSettings(context.Background())
	var fe *ui.ForbiddenError
	if !errors.As(err, &fe) {
		t.Fatalf("GetSettings answered 403: err = %v, want a *ui.ForbiddenError", err)
	}
	if got, want := fe.Error(), `user "bob" does not have administrator privileges`; got != want {
		t.Errorf("ForbiddenError.Error() = %q, want %q", got, want)
	}
}

// Config and GetSettings/UpdateSettings are the two other things the CLI
// reads over this same client -- Config carries the deployment's actor
// and default target as plain strings on the wire (configResponse), and
// this proves HTTPClient reconstructs them into the same shape a
// store-backed Client's own Config field already has.
func TestHTTPClientConfigReadsActorAndDefaultTarget(t *testing.T) {
	t.Parallel()
	c, ctx := testHTTPClient(t)
	cfg, err := c.Config(ctx)
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if cfg.Actor.ID != "alice" {
		t.Fatalf("actor = %+v, want alice", cfg.Actor)
	}
	if cfg.DefaultTarget == nil || cfg.DefaultTarget.String() != "acme/widgets" {
		t.Fatalf("default target = %v, want acme/widgets", cfg.DefaultTarget)
	}
	if len(cfg.Capabilities) != len(ui.OfferedCapabilities()) {
		t.Fatalf("capabilities = %d, want %d", len(cfg.Capabilities), len(ui.OfferedCapabilities()))
	}
}

// TestHTTPClientConfigReadsTargetRepos is
// TestHTTPClientConfigReadsActorAndDefaultTarget's own proof, extended
// to TargetRepos: it too rides configResponse's wire shape rather than
// GetSettings' (a daemon's TargetRepos is fixed at startup -- see
// pkg/cli/daemon.go's loadConfig doc comment -- so the CLI and the
// frontend both read it from here, not from the store-backed Settings).
func TestHTTPClientConfigReadsTargetRepos(t *testing.T) {
	t.Parallel()
	storeClient, store, ctx := testClient(t)
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets", "acme/other"})
	srv := httptest.NewServer(ui.NewServerWithClient(storeClient))
	t.Cleanup(srv.Close)
	c := accessCodeClient(storeClient, srv.URL)

	repos, err := c.ListRepos(ctx)
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 2 || repos[0].Repo != "acme/other" || repos[1].Repo != "acme/widgets" {
		t.Fatalf("repos = %v, want [acme/other acme/widgets]", repos)
	}
}

func TestHTTPClientSettingsRoundTrip(t *testing.T) {
	t.Parallel()
	c, ctx := testHTTPClient(t)
	before, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if before.Configured {
		t.Fatalf("settings on a fresh store = %+v, want Configured false", before)
	}

	pollInterval, maxWorkers, githubHost := "1m", 2, "github.com"
	after, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		PollInterval: &pollInterval, MaxMergesPerRepo: &maxWorkers,
		GitHubHost: &githubHost,
	})
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	// PollInterval round-trips through time.Duration.String(), which
	// normalizes "1m" to "1m0s" -- Settings.PollInterval's own doc
	// comment already says as much.
	if !after.Configured || after.PollInterval != "1m0s" || after.GitHubHost != githubHost {
		t.Fatalf("settings after update = %+v", after)
	}
}

// The repo family over the wire (grain/task-36): the four calls `grain
// repo` makes, driven end to end against a real server, since each one
// builds a path out of an owner/name and two of them are the only way
// the CLI can reach a repo's own stored defaults at all.
func TestHTTPClientRepoFamilyRoundTrip(t *testing.T) {
	t.Parallel()
	c, ctx := testHTTPClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatalf("seeding settings: %v", err)
	}

	if _, err := c.AddTargetRepo(ctx, "acme/widgets"); err != nil {
		t.Fatalf("AddTargetRepo: %v", err)
	}

	defaults, err := c.SetRepoDefaultCapabilities(ctx, "acme/widgets", []string{"self-debug"})
	if err != nil {
		t.Fatalf("SetRepoDefaultCapabilities: %v", err)
	}
	if !reflect.DeepEqual(defaults.DefaultCapabilities, []string{"self-debug"}) {
		t.Fatalf("own defaults after set = %v, want [self-debug]", defaults.DefaultCapabilities)
	}
	read, err := c.RepoDefaults(ctx, "acme/widgets")
	if err != nil {
		t.Fatalf("RepoDefaults: %v", err)
	}
	if !reflect.DeepEqual(read, defaults) {
		t.Fatalf("RepoDefaults = %+v, want what the write returned %+v", read, defaults)
	}

	// The other half of the same row (grain/task-114), written through
	// its own route: the text comes back, and the capabilities set just
	// above it survives -- PutRepoConfig replaces the row wholesale, so
	// "saving one pane wipes the other's work" is what this pins over the
	// wire as well as in Client's own tests.
	prompt, err := c.SetRepoPromptExtension(ctx, "acme/widgets", "Migrations live in db/.")
	if err != nil {
		t.Fatalf("SetRepoPromptExtension: %v", err)
	}
	if prompt.PromptExtension != "Migrations live in db/." {
		t.Fatalf("own prompt extension after set = %q, want the text just written", prompt.PromptExtension)
	}
	if !reflect.DeepEqual(prompt.DefaultCapabilities, []string{"self-debug"}) {
		t.Fatalf("own defaults after setting the prompt extension = %v, want [self-debug] still",
			prompt.DefaultCapabilities)
	}
	read, err = c.RepoDefaults(ctx, "acme/widgets")
	if err != nil {
		t.Fatalf("RepoDefaults: %v", err)
	}
	if !reflect.DeepEqual(read, prompt) {
		t.Fatalf("RepoDefaults = %+v, want what the write returned %+v", read, prompt)
	}

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "fix it", Repo: "acme/widgets", Approved: true})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	repos, err := c.ListRepos(ctx)
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("ListRepos = %+v, want one row", repos)
	}
	want := ui.RepoSummary{
		Repo: "acme/widgets", Configured: true, Tasks: 1,
		States:              map[model.State]int{model.StateQueued: 1},
		DefaultCapabilities: []string{"self-debug"},
		// Whether, not what: a row per repo is no place for a paragraph,
		// so the listing only points at `grain repo prompt-extension`.
		PromptExtension: true,
	}
	if !reflect.DeepEqual(repos[0], want) {
		t.Fatalf("ListRepos row = %+v, want %+v", repos[0], want)
	}

	// Removing a repo with a queued task in it is allowed rather than
	// refused (grain/task-411, and RemoveTargetRepo's own doc comment).
	// It strands nothing here and reports nothing, because this is the
	// only entry on the list: removing it leaves the list empty, and an
	// empty list is unrestricted, so the task it holds is admitted by the
	// list this save leaves behind. pkg/ui's own
	// TestRemoveTargetRepoSaysWhatItStrandedWhileTasksAreInPlay is the
	// case where something really does come off.
	if _, err := c.RemoveTargetRepo(ctx, "acme/widgets"); err != nil {
		t.Fatalf("RemoveTargetRepo with a queued task: %v", err)
	}
	// Put back and closed, so that what the rest of this test removes is
	// a repo with nothing in play -- the case it was written about.
	if _, err := c.AddTargetRepo(ctx, "acme/widgets"); err != nil {
		t.Fatalf("AddTargetRepo: %v", err)
	}
	if err := c.Close(ctx, task.ID, ui.CloseOptions{}); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Removing the repo removes its stored defaults and configuration
	// alongside its allowlist entry. On an unrestricted deployment
	// (targetRepos empty) with a closed task, it leaves an unconfigured
	// row that holds no defaults.
	if _, err = c.RemoveTargetRepo(ctx, "acme/widgets"); err != nil {
		t.Fatalf("RemoveTargetRepo: %v", err)
	}
	repos, err = c.ListRepos(ctx)
	if err != nil {
		t.Fatalf("ListRepos after remove: %v", err)
	}
	if len(repos) != 1 || repos[0].Configured || len(repos[0].DefaultCapabilities) != 0 {
		t.Fatalf("ListRepos after remove = %+v, want one unconfigured row with cleared defaults", repos)
	}
}

// An unknown capability is refused where whoever chose it can still see
// the refusal, rather than stored and quietly never granted -- the same
// answer UpdateSettings gives for the deployment-wide set.
func TestHTTPClientSetRepoDefaultCapabilitiesRejectsAnUnknownID(t *testing.T) {
	t.Parallel()
	c, ctx := testHTTPClient(t)
	_, err := c.SetRepoDefaultCapabilities(ctx, "acme/widgets", []string{"no-such-capability"})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("SetRepoDefaultCapabilities with an unknown id: error = %v, want a ValidationError", err)
	}
}

// A repo with no owner is caught before a request is sent, as the same
// ValidationError the server would have given -- without it, the path
// built from "widgets" matches no route and comes back as a bare 404
// about nothing in particular. AddTargetRepo is deliberately not in this
// list: it carries the repo in the body rather than the path, so there
// is no route to miss and the server's own refusal arrives intact.
func TestHTTPClientRepoMethodsRejectAMalformedRepoLocally(t *testing.T) {
	t.Parallel()
	c := ui.NewHTTPClient("http://127.0.0.1:1") // never dialed
	ctx := context.Background()
	var ve *ui.ValidationError
	if _, err := c.RepoDefaults(ctx, "widgets"); !errors.As(err, &ve) {
		t.Errorf("RepoDefaults(\"widgets\"): error = %v, want a ValidationError", err)
	}
	if _, err := c.SetRepoDefaultCapabilities(ctx, "widgets", nil); !errors.As(err, &ve) {
		t.Errorf("SetRepoDefaultCapabilities(\"widgets\"): error = %v, want a ValidationError", err)
	}
	if _, err := c.SetRepoPromptExtension(ctx, "widgets", "text"); !errors.As(err, &ve) {
		t.Errorf("SetRepoPromptExtension(\"widgets\"): error = %v, want a ValidationError", err)
	}
	if _, err := c.RemoveTargetRepo(ctx, "widgets"); !errors.As(err, &ve) {
		t.Errorf("RemoveTargetRepo(\"widgets\"): error = %v, want a ValidationError", err)
	}
}

// The whole of what a report can be asked for, over the wire: a span
// said as actual dates rather than as a length ending now, and a scope
// narrowing which tasks the numbers are about (grain/task-83). The
// in-process Client takes the same MetricsRequest, so what is only
// decidable here is that every field of it survives the URL.
func TestHTTPClientMetricsCarriesTheSpanAndScopeAcrossTheWire(t *testing.T) {
	t.Parallel()
	storeClient, _, ctx := testClient(t)
	srv := httptest.NewServer(ui.NewServerWithClient(storeClient))
	t.Cleanup(srv.Close)
	c := accessCodeClient(storeClient, srv.URL)
	for _, repo := range []string{"acme/widgets", "acme/docs"} {
		if _, err := c.CreateTask(ctx, ui.CreateTaskRequest{
			Title: "fix the thing", Approved: true, Repo: repo,
		}); err != nil {
			t.Fatalf("creating a task in %s: %v", repo, err)
		}
	}
	// The tasks are filed at baseTime; the reports below are asked for
	// an hour later, so a window ending "now" has them inside it rather
	// than exactly on its edge.
	storeClient.Now = func() time.Time { return baseTime.Add(time.Hour) }

	// A bare request leaves every default where it is declared: the
	// server's own window, and every task.
	rep, err := c.Metrics(ctx, ui.MetricsRequest{})
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	if got, want := rep.WindowSeconds, ui.DefaultMetricsWindow.Seconds(); got != want {
		t.Errorf("windowSeconds = %v, want the server's default %v", got, want)
	}
	if got, want := rep.Throughput.TasksFiled, 2; got != want {
		t.Errorf("tasksFiled unscoped = %d, want %d", got, want)
	}

	// A window is sent in a spelling the endpoint parses back to the
	// same duration.
	rep, err = c.Metrics(ctx, ui.MetricsRequest{Window: 36 * time.Hour})
	if err != nil {
		t.Fatalf("Metrics over a window: %v", err)
	}
	if got, want := rep.WindowSeconds, (36 * time.Hour).Seconds(); got != want {
		t.Errorf("windowSeconds = %v, want %v", got, want)
	}

	// The scope, repeated once per value, and echoed back so a caller
	// can head the report with the question it answers.
	rep, err = c.Metrics(ctx, ui.MetricsRequest{Repos: []string{"acme/widgets"}})
	if err != nil {
		t.Fatalf("Metrics scoped to a repo: %v", err)
	}
	if got, want := rep.Throughput.TasksFiled, 1; got != want {
		t.Errorf("tasksFiled in acme/widgets = %d, want %d", got, want)
	}
	if !reflect.DeepEqual(rep.Scope.Repos, []string{"acme/widgets"}) {
		t.Errorf("scope.repos = %v, want the repo the request named", rep.Scope.Repos)
	}
	if got, err := c.Metrics(ctx, ui.MetricsRequest{Authors: []string{"nobody"}}); err != nil {
		t.Fatalf("Metrics scoped to an author: %v", err)
	} else if got.Throughput.TasksFiled != 0 {
		t.Errorf("tasksFiled by nobody = %d, want 0", got.Throughput.TasksFiled)
	}

	// An absolute span, which is the half a window cannot say: a month
	// that ended long before now, and so a report with none of these
	// tasks in it.
	since := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2020, 2, 1, 0, 0, 0, 0, time.UTC)
	rep, err = c.Metrics(ctx, ui.MetricsRequest{Since: since, Until: until})
	if err != nil {
		t.Fatalf("Metrics over an absolute span: %v", err)
	}
	if !rep.Since.Equal(since) || !rep.Until.Equal(until) {
		t.Errorf("span = %s -> %s, want %s -> %s", rep.Since, rep.Until, since, until)
	}
	if rep.Throughput.TasksFiled != 0 {
		t.Errorf("tasksFiled in January 2020 = %d, want 0", rep.Throughput.TasksFiled)
	}
}

func TestHTTPClientRetriesTransientFailures(t *testing.T) {
	t.Parallel()
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 2 {
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte(`{"error": "bad gateway"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok": true}`))
	}))
	defer srv.Close()

	c := ui.NewHTTPClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := c.Task(ctx, "any"); err != nil {
		t.Fatalf("expected retry to succeed, got %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

// dropAfterReading is a server that reads a request through to the end of
// its body -- so whatever the request asked for has, as far as the client
// can ever tell, been done -- and then drops the connection without a
// response: a daemon stopped between applying a request and answering it.
func dropAfterReading(t *testing.T, applied *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		applied.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijacking: %v", err)
			return
		}
		conn.Close()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A POST the server read and then dropped the connection on may well have
// been applied, so sending it again can apply it twice: a comment posted
// once must never land twice because a daemon restarted under it
// (docs/test-plans/2026-10-11.md §7, step 3).
func TestHTTPClientDoesNotResendAPOSTTheServerMayHaveApplied(t *testing.T) {
	t.Parallel()
	var applied atomic.Int32
	srv := dropAfterReading(t, &applied)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := ui.NewHTTPClient(srv.URL).AddComment(ctx, "42", "only once", nil)
	if err == nil {
		t.Fatal("AddComment over a dropped connection succeeded, want the error")
	}
	if n := applied.Load(); n != 1 {
		t.Fatalf("the server received the POST %d times, want exactly 1", n)
	}
}

// An error status in answer to a POST that grain may have acted on is
// reported rather than resent. A bare 502 or 503 is a gateway that may
// have forwarded it before its upstream dropped (IAP's load balancer
// losing its backend), a 504 is one that gave up waiting on a daemon that
// may yet have applied it, and a 502 or 503 carrying grain's own JSON
// error is grain answering; see retryableResponse and
// docs/test-runs/2026-10-12-upgrade-drain-interrupted.md, finding 1.
func TestHTTPClientDoesNotResendAPOSTOnAGatewayError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status      int
		contentType string
	}{
		{http.StatusBadGateway, "application/json"},
		{http.StatusServiceUnavailable, "application/json"},
		{http.StatusBadGateway, "text/html"},
		{http.StatusServiceUnavailable, "text/plain"},
		{http.StatusGatewayTimeout, "text/plain"},
	} {
		status := tc.status
		var received atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			received.Add(1)
			w.Header().Set("Content-Type", tc.contentType)
			w.WriteHeader(status)
			w.Write([]byte(`{"error": "gateway"}`))
		}))
		err := ui.NewHTTPClient(srv.URL).AddComment(context.Background(), "42", "only once", nil)
		srv.Close()
		if err == nil {
			t.Errorf("status %d (%s): AddComment succeeded, want the error", status, tc.contentType)
		}
		if n := received.Load(); n != 1 {
			t.Errorf("status %d (%s): the server received the POST %d times, want exactly 1", status, tc.contentType, n)
		}
	}
}

// A read is safe to send again however far the last attempt got, so a
// GET rides out a connection dropped mid-request.
func TestHTTPClientRetriesAGETOverADroppedConnection(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) <= 2 {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id": "42"}`))
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := ui.NewHTTPClient(srv.URL).Task(ctx, "42"); err != nil {
		t.Fatalf("Task over two dropped connections: %v", err)
	}
	if n := attempts.Load(); n != 3 {
		t.Fatalf("the server saw %d attempts, want 3", n)
	}
}

// A refused connection is the one failure that proves a request was never
// sent, so even a POST is retried through it -- which is how a comment
// posted while the daemon is down between its stop and its start lands,
// once, as soon as the new process is listening.
func TestHTTPClientRetriesAPOSTWhileTheDaemonIsNotListening(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	var received atomic.Int32
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go func() { done <- ui.NewHTTPClient("http://"+addr).AddComment(ctx, "42", "only once", nil) }()

	time.Sleep(500 * time.Millisecond)
	ln, err = net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("re-listening on %s: %v", addr, err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusCreated)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	if err := <-done; err != nil {
		t.Fatalf("AddComment across the restart: %v", err)
	}
	if n := received.Load(); n != 1 {
		t.Fatalf("the server received the POST %d times, want exactly 1", n)
	}
}
