package ui_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// approvalServer is a deployment with grain CI on bwsalmon/grain and a
// fake runner recording what the approval routes start.
func approvalServer(t *testing.T) (*ui.Server, *ui.Client, *model.Store, *fakeCIRunner) {
	t.Helper()
	runner := &fakeCIRunner{run: ci.Run{ID: "run-1"}}
	srv, client, store := releaseServer(t, runner)
	ctx := context.Background()
	cfg := model.DefaultConfig()
	cfg.TargetRepos = []string{"bwsalmon/grain"}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: model.GrainSourceRepo(), CIMode: model.CIModeGrain}); err != nil {
		t.Fatal(err)
	}
	return srv, client, store, runner
}

// Approving a pending release is a person's act. An agent is refused
// even with releases:write, the release-manager scope the route is
// under, and so is a caller that is an agent principal outright; nothing
// is started for either.
func TestAnAgentMayNotApproveAPendingRelease(t *testing.T) {
	srv, client, _, runner := approvalServer(t)
	seedAPITask(t, client, "7", "bwsalmon/grain", "api:releases:write")

	for _, verb := range []string{"approve", "reject"} {
		path := "/api/repos/bwsalmon/grain/pending-releases/v2.4.0/" + verb
		code, body := asAgent(t, srv, "7", http.MethodPost, path, "", "")
		if code != http.StatusForbidden || !strings.Contains(body, "only a person may "+verb) {
			t.Errorf("an agent holding releases:write %ss: %d %s, want 403 saying only a person may", verb, code, body)
		}
		if rec := doAsCaller(t, srv, agentPrefix+"run-1", http.MethodPost, path, ""); rec.Code != http.StatusForbidden {
			t.Errorf("an agent principal %ss: %d %s, want 403", verb, rec.Code, rec.Body)
		}
	}
	if len(runner.started) != 0 {
		t.Fatalf("an agent's approval started CI anyway: %+v", runner.started)
	}
}

// A person's approval starts promote-release.yml's promote job (and the
// approve job it needs) on trunk, telling it who asked and for which
// release in fields no API caller can set, and clears the release's
// "ready to promote" notification. A rejection starts the reject job.
func TestAPersonApprovesOrRejectsAPendingRelease(t *testing.T) {
	srv, _, store, runner := approvalServer(t)
	ctx := context.Background()
	subject := model.NotificationSubject(model.FamilyReleasePending, "v2.4.0")
	if _, err := store.RaiseNotification(ctx, model.Notification{Subject: subject, Severity: model.NotificationWarning,
		Title: "v2.4.0 is ready to promote", RaisedBy: "system:release-gate", RaisedAt: baseTime,
		Actions: model.ReleasePendingActions}); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodPost, "/api/repos/bwsalmon/grain/pending-releases/v2.4.0/approve", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("a person's approval: %d %s, want 202", rec.Code, rec.Body)
	}
	if len(runner.started) != 1 {
		t.Fatalf("started %d runs, want 1", len(runner.started))
	}
	got := runner.started[0]
	if got.Workflow != ui.PromoteReleaseWorkflow || got.Ref != "main" || got.Event != ci.EventManual ||
		len(got.Jobs) != 1 || got.Jobs[0] != ui.PromoteReleaseWorkflow+":promote" {
		t.Errorf("approval started %+v, want promote-release.yml's promote job on main", got)
	}
	if got.RequestedByKind != "human" || got.ReleaseVersion != "v2.4.0" || got.RequestedBy == "" {
		t.Errorf("approval run says kind %q, version %q, by %q; want a person approving v2.4.0",
			got.RequestedByKind, got.ReleaseVersion, got.RequestedBy)
	}
	open, err := store.ListNotifications(ctx, model.NotificationFilter{Open: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range open {
		if n.Subject == subject {
			t.Errorf("%s is still open after the approval started", subject)
		}
	}

	if rec := do(t, srv, http.MethodPost, "/api/repos/bwsalmon/grain/pending-releases/v2.4.0/reject", ""); rec.Code != http.StatusAccepted {
		t.Fatalf("a person's rejection: %d %s, want 202", rec.Code, rec.Body)
	}
	if last := runner.started[len(runner.started)-1]; len(last.Jobs) != 1 || last.Jobs[0] != ui.PromoteReleaseWorkflow+":reject" {
		t.Errorf("rejection started %+v, want the reject job", last.Jobs)
	}

	for _, bad := range []string{"v2.4.1", "latest", "v2.4.0-rc.1"} {
		if rec := do(t, srv, http.MethodPost, "/api/repos/bwsalmon/grain/pending-releases/"+bad+"/approve", ""); rec.Code != http.StatusBadRequest {
			t.Errorf("approving %q: %d %s, want 400", bad, rec.Code, rec.Body)
		}
	}
}
