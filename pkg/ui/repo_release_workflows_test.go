package ui_test

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/ci"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/secrets"
	"github.com/bwsalmon/grain/pkg/ui"
)

func releaseServer(t *testing.T, runner ci.Runner) (*ui.Server, *ui.Client, *model.Store) {
	t.Helper()
	client, store, _ := testClient(t)
	client.Config.Secrets = secrets.New(t.TempDir())
	client.Config.CI = runner
	return ui.NewServerWithClient(client), client, store
}

func TestRepoReleaseSettingsToggleAutomaticRuns(t *testing.T) {
	t.Parallel()
	srv, _, store := releaseServer(t, nil)

	rec := do(t, srv, http.MethodGet, "/api/repos/acme/widgets/release", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body)
	}
	if got := decode[ui.RepoReleaseSettings](t, rec); !got.Enabled || !got.Available || got.SecretsDir != ci.ReleaseSecretsDir {
		t.Fatalf("default settings = %+v", got)
	}

	rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/release", `{"enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	if got := decode[ui.RepoReleaseSettings](t, rec); got.Enabled {
		t.Fatalf("release remained enabled: %+v", got)
	}
	cfg, err := store.GetRepoConfig(context.Background(), model.RepoRef{Owner: "acme", Name: "widgets"})
	if err != nil || cfg == nil || model.RunsWorkItem(cfg, model.ReleaseItem) {
		t.Fatalf("stored config = %+v, %v", cfg, err)
	}
	rec = do(t, srv, http.MethodGet, "/api/config", "")
	config := decode[map[string]any](t, rec)
	if got := config["reposWithReleaseSettings"]; !reflect.DeepEqual(got, []any{"acme/widgets"}) {
		t.Fatalf("release-configured repo is not discoverable: %v", got)
	}
}

// The jobs that judge a base branch while publishing is off
// (grain/task-330). They are set from the same pane and the same endpoint
// as the switch that makes them matter, because the operator who turns
// publishing off is the one who is about to start paying for a whole
// push plan on every merge (grain/task-458).
func TestRepoReleaseSettingsTakeTheJobsThatJudgeABaseBranch(t *testing.T) {
	t.Parallel()
	srv, _, store := releaseServer(t, nil)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	rec := do(t, srv, http.MethodPut, "/api/repos/acme/widgets/release",
		`{"enabled":false,"baseBranchJobs":[" go ","",".grain/workflows/tests.yml:lint"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	want := []string{"go", ".grain/workflows/tests.yml:lint"}
	if got := decode[ui.RepoReleaseSettings](t, rec); got.Enabled ||
		!reflect.DeepEqual(got.BaseBranchJobs, want) {
		t.Fatalf("settings = %+v, want releases off and %v judging the base branch", got, want)
	}
	cfg, err := store.GetRepoConfig(context.Background(), repo)
	if err != nil || cfg == nil || !reflect.DeepEqual(cfg.BaseBranchCIJobs, want) {
		t.Fatalf("stored config = %+v, %v", cfg, err)
	}

	// An empty list is an answer rather than a missing field: it is how a
	// repository goes back to being judged by everything a push would run
	// (grain/task-458).
	rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/release", `{"baseBranchJobs":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT clearing the jobs: %d %s", rec.Code, rec.Body)
	}
	if got := decode[ui.RepoReleaseSettings](t, rec); len(got.BaseBranchJobs) != 0 || got.Enabled {
		t.Fatalf("settings = %+v, want no judging jobs and releases still off", got)
	}
	cfg, err = store.GetRepoConfig(context.Background(), repo)
	if err != nil || cfg == nil || len(cfg.BaseBranchCIJobs) != 0 || model.RunsWorkItem(cfg, model.ReleaseItem) {
		t.Fatalf("stored config = %+v, %v", cfg, err)
	}

	// And a request about neither is still a mistake worth reporting.
	if rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/release", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT with nothing to set: %d %s", rec.Code, rec.Body)
	}
}

// grain/task-2414: the repo page writes both lists from a comma-separated
// box, so a name typed twice was stored -- and shown back -- twice.
func TestRepoReleaseJobListsDropRepeatedNames(t *testing.T) {
	t.Parallel()
	srv, _, _ := releaseServer(t, nil)

	rec := do(t, srv, http.MethodPut, "/api/repos/acme/widgets/release",
		`{"postMergeDispatchJobs":["build-cache"," ","docs ","build-cache"],"baseBranchJobs":["go","go "]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	got := decode[ui.RepoReleaseSettings](t, rec)
	if want := []string{"build-cache", "docs"}; !reflect.DeepEqual(got.PostMergeDispatchJobs, want) {
		t.Errorf("postMergeDispatchJobs = %v, want %v", got.PostMergeDispatchJobs, want)
	}
	if want := []string{"go"}; !reflect.DeepEqual(got.BaseBranchJobs, want) {
		t.Errorf("baseBranchJobs = %v, want %v", got.BaseBranchJobs, want)
	}
}

func TestRepoReleaseSecretsAreWriteOnlyAndRepositoryScoped(t *testing.T) {
	t.Parallel()
	srv, client, store := releaseServer(t, nil)
	const value = "private-token-value"

	rec := do(t, srv, http.MethodPut, "/api/repos/acme/widgets/release-secrets/NPM_TOKEN", `{"value":"`+value+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), value) {
		t.Fatal("secret value came back through the API")
	}
	got := decode[ui.RepoReleaseSettings](t, rec)
	if len(got.Secrets) != 1 || got.Secrets[0].Name != "NPM_TOKEN" || !got.Secrets[0].Set {
		t.Fatalf("secret listing = %+v", got.Secrets)
	}
	secret, key := ci.ReleaseSecretAddress("acme/widgets", "NPM_TOKEN")
	resolved, err := client.Config.Secrets.Resolve(context.Background(), secret+"/"+key)
	if err != nil || resolved != value {
		t.Fatalf("encrypted-store value = %q, %v", resolved, err)
	}
	cfg, err := store.GetRepoConfig(context.Background(), model.RepoRef{Owner: "acme", Name: "widgets"})
	if err != nil || cfg == nil || len(cfg.ReleaseSecrets) != 1 || cfg.ReleaseSecrets[0] != "NPM_TOKEN" {
		t.Fatalf("stored names = %+v, %v", cfg, err)
	}

	rec = do(t, srv, http.MethodDelete, "/api/repos/acme/widgets/release-secrets/NPM_TOKEN", "")
	if rec.Code != http.StatusOK || len(decode[ui.RepoReleaseSettings](t, rec).Secrets) != 0 {
		t.Fatalf("DELETE: %d %s", rec.Code, rec.Body)
	}
	if _, err := client.Config.Secrets.Resolve(context.Background(), secret+"/"+key); err == nil {
		t.Fatal("deleted release secret still resolves")
	}
}

// Remove on a release secret the repo declares but the store no longer
// holds -- the pane's "Missing" row -- drops the declaration, and a
// delete of one that is neither is a 404 rather than the store's "no
// key" error answered as a 500.
func TestRepoReleaseSecretDeleteOfAMissingSecret(t *testing.T) {
	t.Parallel()
	srv, client, store := releaseServer(t, nil)
	if rec := do(t, srv, http.MethodPut, "/api/repos/acme/widgets/release-secrets/NPM_TOKEN", `{"value":"v"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	secret, key := ci.ReleaseSecretAddress("acme/widgets", "NPM_TOKEN")
	if err := client.Config.Secrets.DeleteKey(secret, key); err != nil {
		t.Fatal(err)
	}
	rec := do(t, srv, http.MethodDelete, "/api/repos/acme/widgets/release-secrets/NPM_TOKEN", "")
	if rec.Code != http.StatusOK || len(decode[ui.RepoReleaseSettings](t, rec).Secrets) != 0 {
		t.Fatalf("DELETE of a declared, missing secret: %d %s, want 200 with nothing listed", rec.Code, rec.Body)
	}
	cfg, err := store.GetRepoConfig(context.Background(), model.RepoRef{Owner: "acme", Name: "widgets"})
	if err != nil || (cfg != nil && len(cfg.ReleaseSecrets) != 0) {
		t.Fatalf("declarations after DELETE = %+v, %v", cfg, err)
	}
	rec = do(t, srv, http.MethodDelete, "/api/repos/acme/widgets/release-secrets/NPM_TOKEN", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE of an unknown secret: %d %s, want 404", rec.Code, rec.Body)
	}
}

func TestRepoReleaseSecretNamesAreSafeFileNames(t *testing.T) {
	t.Parallel()
	srv, _, _ := releaseServer(t, nil)
	for _, name := range []string{"lowercase", "9TOKEN", "TOKEN-NAME"} {
		rec := do(t, srv, http.MethodPut, "/api/repos/acme/widgets/release-secrets/"+name, `{"value":"x"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%q status = %d, want 400: %s", name, rec.Code, rec.Body)
		}
	}
}

func TestRepoReleaseCanRunByHandWhileAutomaticRunsAreOff(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{run: ci.Run{ID: "release-1"}}
	srv, _, store := releaseServer(t, runner)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(context.Background(), model.RepoConfig{
		Repo: repo, CIMode: model.CIModeGrain, Work: model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
	}); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodPost, "/api/repos/acme/widgets/release-runs", `{"branch":"main"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST: %d %s", rec.Code, rec.Body)
	}
	if len(runner.started) != 1 {
		t.Fatalf("started %d runs", len(runner.started))
	}
	req := runner.started[0]
	if req.Event != ci.EventRelease || !req.TrustedRelease || req.Ref != "main" || req.RequestedBy != "alice" {
		t.Fatalf("release request = %+v", req)
	}
}

// A release Start refuses -- here, a ref that does not resolve -- is the
// caller's to fix, so it is a 400 that says why rather than a 500 whose
// reason apierror.Body keeps to the daemon's log; and a repository grain
// CI is not enabled for is still a 403.
func TestRepoReleaseRunRefusalsSayWhy(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{err: errors.New(`ref "nope" does not resolve`)}
	srv, _, store := releaseServer(t, runner)
	if err := store.PutRepoConfig(context.Background(), model.RepoConfig{
		Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, CIMode: model.CIModeGrain,
	}); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodPost, "/api/repos/acme/widgets/release-runs", `{"branch":"nope"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "does not resolve") {
		t.Fatalf("refused start: %d %s, want 400 naming the reason", rec.Code, rec.Body)
	}

	rec = do(t, srv, http.MethodPost, "/api/repos/acme/gadgets/release-runs", `{"branch":"main"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("repo without grain CI: %d %s, want 403", rec.Code, rec.Body)
	}
}

// Setting and removing a release secret is a person's: an agent is
// refused with a 403, any person -- the owner -- may.
func TestRepoReleaseSecretsRequireAPerson(t *testing.T) {
	t.Parallel()
	routes := []struct {
		name, method, path, body string
	}{
		{"put", http.MethodPut, "/api/repos/acme/widgets/release-secrets/NPM_TOKEN", `{"value":"super-secret"}`},
		{"delete", http.MethodDelete, "/api/repos/acme/widgets/release-secrets/NPM_TOKEN", ""},
	}
	users := []struct {
		user    string
		allowed bool
	}{
		{agentPrefix + "run-1", false},
		{"zed", true},
		{"owner", true},
	}
	for _, rt := range routes {
		for _, u := range users {
			t.Run(rt.name+"/"+u.user, func(t *testing.T) {
				t.Parallel()
				srv, client, store := authzReleaseServerWithClient(t)
				// Pre-populate secret in both Secrets store and repo config for delete test so deletion succeeds when authorized.
				if rt.name == "delete" {
					repo := model.RepoRef{Owner: "acme", Name: "widgets"}
					secret, key := ci.ReleaseSecretAddress(repo.String(), "NPM_TOKEN")
					if err := client.Config.Secrets.Set(secret, key, []byte("super-secret")); err != nil {
						t.Fatal(err)
					}
					cfg, err := store.GetRepoConfig(context.Background(), repo)
					if err != nil {
						t.Fatal(err)
					}
					if cfg == nil {
						cfg = &model.RepoConfig{Repo: repo}
					}
					cfg.ReleaseSecrets = []string{"NPM_TOKEN"}
					if err := store.PutRepoConfig(context.Background(), *cfg); err != nil {
						t.Fatal(err)
					}
				}
				rec := doAsCaller(t, srv, u.user, rt.method, rt.path, rt.body)
				want := http.StatusForbidden
				if u.allowed {
					want = http.StatusOK
				}
				if rec.Code != want {
					t.Fatalf("%s %s: %d %s, want %d", u.user, rt.name, rec.Code, rec.Body, want)
				}
			})
		}
	}
}

// A task's branch is not a branch an agent may release, even when grain
// has a base-branch row for it (grain/task-949). A stacked review's merge
// into the reviewed task's branch used to write that row, and every
// watched base was a branch checkReleaseRef let an api:ci:write holder
// release -- running the task author's own workflows with the
// repository's release secrets mounted.
func TestAnAgentCannotReleaseATaskBranchThatHasABaseRow(t *testing.T) {
	runner := &fakeCIRunner{run: ci.Run{ID: "run-1"}}
	srv, client, store := releaseServer(t, runner)
	ctx := context.Background()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg := model.DefaultConfig()
	cfg.TargetRepos = []string{"acme/widgets"}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGrain}); err != nil {
		t.Fatal(err)
	}
	taskBranch := "grain/049f1e09/task-123"
	for _, branch := range []string{"main", taskBranch} {
		if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: repo, Branch: branch, StartedAt: baseTime}); err != nil {
			t.Fatal(err)
		}
	}
	seedAPITask(t, client, "1", "acme/widgets", "api:ci:write")

	code, body := asAgent(t, srv, "1", http.MethodPost, "/api/repos/acme/widgets/release-runs",
		`{"branch":"`+taskBranch+`"}`, "")
	if code != http.StatusForbidden || !strings.Contains(body, taskBranch+" is not a branch") {
		t.Fatalf("an agent's release of %s: %d %s, want 403 from checkReleaseRef", taskBranch, code, body)
	}
	if strings.Contains(body, "name one of "+taskBranch) || strings.Contains(body, ", "+taskBranch) {
		t.Fatalf("the refusal offers the task branch as a branch to release from: %s", body)
	}
	if len(runner.started) != 0 {
		t.Fatalf("a release was started anyway: %+v", runner.started)
	}
	// main, beside it, still may be.
	if code, body := asAgent(t, srv, "1", http.MethodPost, "/api/repos/acme/widgets/release-runs",
		`{"branch":"main"}`, ""); code != http.StatusAccepted {
		t.Fatalf("an agent's release of main: %d %s, want 202", code, body)
	}
	if len(runner.started) != 1 || runner.started[0].ReleaseRefVouched {
		t.Fatalf("main's release = %+v, want one, not vouched for: only a person vouches for a ref", runner.started)
	}
}
