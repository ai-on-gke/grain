package ui_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/ci"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// authzCIServer is a deployment whose acme/widgets runs grain CI,
// watches main, and has an open release "2.1".
func authzCIServer(t *testing.T, runner *fakeCIRunner) *ui.Server {
	t.Helper()
	srv, client, store := releaseServer(t, runner)
	ctx := context.Background()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg := model.DefaultConfig()
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGrain}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: repo, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRelease(ctx, repo, "2.1", time.Now()); err != nil {
		t.Fatal(err)
	}
	seedAPITask(t, client, "ci-writer", "acme/widgets", "api:ci:write")
	return srv
}

func doAs(t *testing.T, srv *ui.Server, user, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return doAsPrincipal(t, srv, model.Principal{ID: user, Kind: model.PrincipalHuman}, method, path, body)
}

// agentPrefix marks a caller name doAsCaller reads as an agent's
// rather than a person's; actingAgentPrefix sends X-Grain-Acting-Task so
// agentAPIGate authorizes the task's api:<scope> grants first.
const (
	agentPrefix       = "agent:"
	actingAgentPrefix = "acting:"
)

// doAsCaller is doAs for a person, for an un-gated agent when user starts
// with agentPrefix, or for an agentAPIGate-authorized task when user starts
// with actingAgentPrefix.
func doAsCaller(t *testing.T, srv *ui.Server, user, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	if id, ok := strings.CutPrefix(user, actingAgentPrefix); ok {
		return doWithHeaders(t, srv, method, path, body, map[string]string{
			ui.ActingTaskHeader: id,
			"Content-Type":      "application/json",
		})
	}
	if id, ok := strings.CutPrefix(user, agentPrefix); ok {
		return doAsPrincipal(t, srv, model.Principal{ID: id, Kind: model.PrincipalAgent}, method, path, body)
	}
	return doAs(t, srv, user, method, path, body)
}

func doAsPrincipal(t *testing.T, srv *ui.Server, p model.Principal, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(ui.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// A release run is handed the repository's release secrets, so an un-gated
// agent is refused outright, while an agent task holding api:ci:write is
// held by checkReleaseRef to a branch that has been reviewed into (a
// watched base branch or a branch of an open release) with no pinned
// commit. Any person -- since whoever holds the access code is the owner --
// may start one on any branch and pinned to any commit.
func TestRepoReleaseRunIsAPersonsToStart(t *testing.T) {
	t.Parallel()
	const path = "/api/repos/acme/widgets/release-runs"
	cases := []struct {
		name, user, body string
		want             int
	}{
		{"agent refused", agentPrefix + "run-1", `{"branch":"main"}`, http.StatusForbidden},
		{"agent on a task branch refused", agentPrefix + "run-1", `{"branch":"grain/task-1"}`, http.StatusForbidden},
		{"ci:write agent on a watched base branch", actingAgentPrefix + "ci-writer", `{"branch":"main"}`, http.StatusAccepted},
		{"ci:write agent on refs/heads spelling", actingAgentPrefix + "ci-writer", `{"ref":"refs/heads/main"}`, http.StatusAccepted},
		{"ci:write agent on a release's prod branch", actingAgentPrefix + "ci-writer", `{"branch":"2.1"}`, http.StatusAccepted},
		{"ci:write agent on a release's latest branch", actingAgentPrefix + "ci-writer", `{"branch":"2.1.latest"}`, http.StatusAccepted},
		{"ci:write agent on a task branch refused", actingAgentPrefix + "ci-writer", `{"branch":"grain/task-1"}`, http.StatusForbidden},
		{"ci:write agent pinning a commit refused", actingAgentPrefix + "ci-writer", `{"branch":"main","commit":"abc123"}`, http.StatusForbidden},
		{"person on a watched base branch", "carol", `{"branch":"main"}`, http.StatusAccepted},
		{"person on refs/heads spelling", "carol", `{"ref":"refs/heads/main"}`, http.StatusAccepted},
		{"person on a release's prod branch", "carol", `{"branch":"2.1"}`, http.StatusAccepted},
		{"person on a release's latest branch", "carol", `{"branch":"2.1.latest"}`, http.StatusAccepted},
		{"person on any branch, pinned", "dave", `{"branch":"grain/task-1","commit":"abc123"}`, http.StatusAccepted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runner := &fakeCIRunner{run: ci.Run{ID: "release-1"}}
			srv := authzCIServer(t, runner)
			rec := doAsCaller(t, srv, tc.user, http.MethodPost, path, tc.body)
			if rec.Code != tc.want {
				t.Fatalf("%s: %d %s, want %d", tc.user, rec.Code, rec.Body, tc.want)
			}
			if tc.want != http.StatusAccepted {
				if len(runner.started) != 0 {
					t.Fatalf("refused request still started %+v", runner.started)
				}
				return
			}
			if len(runner.started) != 1 || !runner.started[0].TrustedRelease {
				t.Fatalf("started = %+v", runner.started)
			}
		})
	}
}

// A by-hand workflow_dispatch (or release) is the event release secrets
// are placed into, on whatever branch it names, so -- as on GitHub, where
// only somebody with write access fires one -- a caller that is not a
// person gets the run without them, even when authorized by api:ci:write.
// Every other event carries none anyway and is left alone.
func TestCIRunReleaseSecretsAreAPersonsToFire(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, user, event string
		withheld          bool
	}{
		{"agent's manual run", agentPrefix + "run-1", "workflow_dispatch", true},
		{"agent asking for a release by name", agentPrefix + "run-1", "release", true},
		{"ci:write agent's manual run", actingAgentPrefix + "ci-writer", "workflow_dispatch", true},
		{"ci:write agent asking for a release by name", actingAgentPrefix + "ci-writer", "release", true},
		{"ci:write agent's push run", actingAgentPrefix + "ci-writer", "push", false},
		{"agent's push run", agentPrefix + "run-1", "push", false},
		{"agent naming no event", agentPrefix + "run-1", "", false},
		{"person's manual run", "carol", "workflow_dispatch", false},
		{"person asking for a release by name", "carol", "release", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runner := &fakeCIRunner{run: ci.Run{ID: "run-1"}}
			srv := authzCIServer(t, runner)
			body := `{"repo":"acme/widgets","branch":"grain/task-1","team":"alpha","event":"` + tc.event + `"}`
			rec := doAsCaller(t, srv, tc.user, http.MethodPost, "/api/ci/runs", body)
			if rec.Code != http.StatusAccepted {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if len(runner.started) != 1 {
				t.Fatalf("started = %+v", runner.started)
			}
			got := runner.started[0]
			if got.WithholdReleaseSecrets != tc.withheld {
				t.Fatalf("WithholdReleaseSecrets = %v, want %v", got.WithholdReleaseSecrets, tc.withheld)
			}
			if tc.withheld && !strings.Contains(got.WithholdReason, "not a person") {
				t.Fatalf("WithholdReason = %q", got.WithholdReason)
			}
		})
	}
}
