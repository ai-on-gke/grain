package ui_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// Expired host credentials are said once on the deployment-health pane,
// for the host, while runs are ending that way -- and the pane says it
// even where there is no sandbox pool to report, since it is about the
// host and not the pool.
func TestSandboxHealthSaysToRenewHostCredentialsOnceForTheHost(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()
	task := create(t, client, ctx)

	rec := do(t, srv, http.MethodGet, "/api/sandboxes", "")
	if got := decode[ui.SandboxHealthResponse](t, rec); got.HostCredentials != nil {
		t.Fatalf("before any run: hostCredentials = %+v, want none", got.HostCredentials)
	}

	const expired = "host credentials expired: run `renew-host-creds` as alice on build-1.example.test, then retry " +
		"(/opt/tools/agent-cli could not run: required key not available)"
	if err := client.Store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: task.ID, Sandbox: "s1", Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := client.Store.FinishRun(ctx, "r1", baseTime.Add(time.Second), model.EnvironmentOutcome, expired); err != nil {
		t.Fatal(err)
	}

	rec = do(t, srv, http.MethodGet, "/api/sandboxes", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[ui.SandboxHealthResponse](t, rec)
	if got.HostCredentials == nil {
		t.Fatal("hostCredentials absent while the host's runs cannot start")
	}
	if !strings.Contains(got.HostCredentials.Detail, "run `renew-host-creds`") || got.HostCredentials.Runs != 1 ||
		got.HostCredentials.TaskID != task.ID {
		t.Errorf("hostCredentials = %+v, want the renewal sentence, one run, on %s", got.HostCredentials, task.ID)
	}
}
