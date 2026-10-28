package ui_test

// POST /api/tasks/{id}/sandbox/recreate: the hop a dispatched run's own
// mcpserver makes to have its sandbox destroyed and rebuilt while it is
// still running (see ui.Config.SandboxRecreate). What actually rebuilds
// one is the daemon's own registry of live runs, wired in behind that
// field, so these tests stand a fake in its place and assert on the
// plumbing around it -- which task it is asked about, what comes back,
// and what a deployment with none wired at all does -- exactly as
// pull_requests_test.go does for the neighbouring hop.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite"
	"github.com/bwsalmon/grain/pkg/ui"
)

type fakeSandboxRecreator struct {
	recreation ui.SandboxRecreation
	err        error
	askedAbout []string
}

func (f *fakeSandboxRecreator) RecreateForTask(_ context.Context, taskID string) (ui.SandboxRecreation, error) {
	f.askedAbout = append(f.askedAbout, taskID)
	return f.recreation, f.err
}

// sandboxRecreateClient is pullRequestClient's counterpart: a client with
// a Config.SandboxRecreate wired in -- nil being the deployment that has
// none.
func sandboxRecreateClient(t *testing.T, recreator ui.SandboxRecreator) (*ui.Client, context.Context) {
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
		Actor:           ui.DefaultActor("alice"),
		DefaultTarget:   &repo,
		Capabilities:    ui.OfferedCapabilities(),
		SandboxRecreate: recreator,
	}, store)
	client.Now = func() time.Time { return time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC) }
	return client, ctx
}

func TestRecreateSandboxAsksAboutTheTaskAndReportsWhatComesBack(t *testing.T) {
	t.Parallel()
	recreator := &fakeSandboxRecreator{recreation: ui.SandboxRecreation{
		Sandbox:     "1-1",
		CheckoutDir: "work",
		Restored:    []string{"its git credentials for grain's git proxy"},
		Warnings:    []string{"this task's attachments could not be written back: disk full"},
	}}
	client, ctx := sandboxRecreateClient(t, recreator)
	task := create(t, client, ctx)

	recreation, err := client.RecreateSandbox(ctx, task.ID)
	if err != nil {
		t.Fatalf("RecreateSandbox: %v", err)
	}
	if len(recreator.askedAbout) != 1 || recreator.askedAbout[0] != task.ID {
		t.Fatalf("asked about %v, want just %q", recreator.askedAbout, task.ID)
	}
	if recreation.Sandbox != "1-1" || recreation.CheckoutDir != "work" {
		t.Fatalf("recreation = %+v, want the fake's own answer", recreation)
	}
	if len(recreation.Restored) != 1 || len(recreation.Warnings) != 1 {
		t.Fatalf("recreation = %+v, want both halves of the account carried through", recreation)
	}
}

// A task id nothing was ever filed under is a 404, not a sandbox
// rebuild: the registry is never even asked.
func TestRecreateSandboxForAnUnknownTask(t *testing.T) {
	t.Parallel()
	recreator := &fakeSandboxRecreator{}
	client, ctx := sandboxRecreateClient(t, recreator)

	_, err := client.RecreateSandbox(ctx, "nope")
	var nf *ui.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want a NotFoundError", err)
	}
	if len(recreator.askedAbout) != 0 {
		t.Fatalf("asked about %v, want nothing", recreator.askedAbout)
	}
}

// A UI not colocated with the daemon that owns the sandboxes answers 404
// -- "this deployment does not offer that" -- rather than a 500 that
// reads like a failure, the same nil-means-unavailable shape
// POST /api/tasks/{id}/pull-request already has.
func TestRecreateSandboxUnavailableWithoutARecreator(t *testing.T) {
	t.Parallel()
	client, ctx := sandboxRecreateClient(t, nil)
	task := create(t, client, ctx)
	srv := ui.NewServerWithClient(client)

	rec := do(t, srv, http.MethodPost, "/api/tasks/"+task.ID+"/sandbox/recreate", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}

	if _, err := client.RecreateSandbox(ctx, task.ID); err == nil {
		t.Fatal("expected a direct call to refuse too")
	}
}

// The whole hop, end to end over real HTTP: an HTTPClient (what
// `grain mcpserver` holds) against a real ui.Server.
func TestHTTPClientRecreateSandboxRoundTrip(t *testing.T) {
	t.Parallel()
	recreator := &fakeSandboxRecreator{recreation: ui.SandboxRecreation{
		Sandbox:     "1-1",
		CheckoutDir: "work",
		Restored:    []string{"a fresh clone of acme/widgets at ./work"},
		Warnings:    []string{"its git credentials could not be re-minted"},
	}}
	client, ctx := sandboxRecreateClient(t, recreator)
	task := create(t, client, ctx)
	srv := httptest.NewServer(ui.NewServerWithClient(client))
	t.Cleanup(srv.Close)

	recreation, err := accessCodeClient(client, srv.URL).RecreateSandbox(ctx, task.ID)
	if err != nil {
		t.Fatalf("RecreateSandbox over HTTP: %v", err)
	}
	if recreation.Sandbox != "1-1" || recreation.CheckoutDir != "work" {
		t.Fatalf("recreation = %+v, want the rebuilt sandbox the registry reported", recreation)
	}
	if len(recreation.Restored) != 1 || !strings.Contains(recreation.Restored[0], "fresh clone") {
		t.Fatalf("restored = %v, want it carried across the wire", recreation.Restored)
	}
	if len(recreation.Warnings) != 1 || !strings.Contains(recreation.Warnings[0], "re-minted") {
		t.Fatalf("warnings = %v, want them carried across the wire -- they are the half that "+
			"changes what the agent does next", recreation.Warnings)
	}
}

// A refusal (no live run on this daemon, a rebuild that failed) reaches
// the caller with its own message intact -- that message is what the
// agent reads and acts on.
func TestHTTPClientRecreateSandboxReportsAFailure(t *testing.T) {
	t.Parallel()
	recreator := &fakeSandboxRecreator{err: errors.New("this task has no live run on this daemon")}
	client, ctx := sandboxRecreateClient(t, recreator)
	task := create(t, client, ctx)
	srv := httptest.NewServer(ui.NewServerWithClient(client))
	t.Cleanup(srv.Close)

	_, err := accessCodeClient(client, srv.URL).RecreateSandbox(ctx, task.ID)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "no live run on this daemon") {
		t.Fatalf("err = %v, want the registry's own message", err)
	}
}
