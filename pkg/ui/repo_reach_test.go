// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ui_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gitproxy/gitproxytest"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The made-up command-backed source these tests run against.
func init() { gitproxytest.Install() }

// reachClient is a client whose team "bouncer" -- attached to no
// repositories, as in the deployment that reported this -- holds a
// credential GitHub (the simulator) says reaches gke-labs/bouncer with
// push and acme/docs read-only, and nothing else.
func reachClient(t *testing.T) (*ui.Server, *ui.Client) {
	t.Helper()
	srv, client, credentials := testServerWithCreds(t)
	ctx := context.Background()
	if err := credentials.SetToken("bouncer-cred", "tok-bouncer"); err != nil {
		t.Fatal(err)
	}
	sim := githubsim.New("acme", "widgets", "", "main")
	sim.Reachable = map[string][]githubsim.ReachableRepo{
		"tok-bouncer": {{FullName: "gke-labs/bouncer", Push: true}, {FullName: "acme/docs", Push: false}},
	}
	client.Config.GitReach = &gitproxy.Reach{Credentials: credentials, API: sim}
	// The credential is the deployment's git connection, which team
	// bouncer's tasks reach with (docs/single-tenant-reset.md, stage 1).
	if err := client.Store.UpdateConfig(ctx, func(tm *model.Config) error {
		tm.GitCredential = "bouncer-cred"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return srv, client
}

// A person filing against a repository the team's credential cannot
// reach is told so at the form -- the team, the repository, and where to
// fix it -- instead of the task failing its first clone at dispatch.
func TestCreateTaskRefusesARepoTheTeamCannotReach(t *testing.T) {
	t.Parallel()
	_, client := reachClient(t)
	ctx := context.Background()

	for name, req := range map[string]ui.CreateTaskRequest{
		"target": {Title: "t", Repo: "bwsalmon/grain"},
		"write":  {Title: "t", Repo: "gke-labs/bouncer", Writes: []ui.WriteTarget{{Repo: "acme/docs"}}},
		"read":   {Title: "t", Repo: "gke-labs/bouncer", Reads: []string{"bwsalmon/grain"}},
	} {
		_, err := client.CreateTask(ctx, req)
		var invalid *ui.ValidationError
		if !errors.As(err, &invalid) {
			t.Errorf("%s: CreateTask error = %v; want a validation error (a 400)", name, err)
			continue
		}
		for _, want := range []string{"the deployment", "Settings → Git"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: error %q does not mention %q", name, err, want)
			}
		}
	}

	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "ok",
		Repo: "gke-labs/bouncer", Reads: []string{"acme/docs"}})
	if err != nil {
		t.Fatalf("CreateTask on reachable repos: %v", err)
	}
	if st, err := client.Store.State(ctx, task.ID); err != nil || st == model.StateAwaitingReply {
		t.Errorf("reachable task state = %q (%v); want it left dispatchable", st, err)
	}
}

// An agent's proposal has nobody at a form to refuse: it is filed, so the
// work it describes is not lost, and parked on the same explanation.
func TestProposalToAnUnreachableRepoIsParked(t *testing.T) {
	t.Parallel()
	_, client := reachClient(t)
	ctx := context.Background()
	if err := client.Store.PutTask(ctx, model.Task{ID: "parent", Title: "parent"}); err != nil {
		t.Fatal(err)
	}
	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "follow-up",
		Repo: "bwsalmon/grain", ProposedBy: "parent"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if st, err := client.Store.State(ctx, task.ID); err != nil || st != model.StateAwaitingReply {
		t.Fatalf("state = %q (%v); want awaiting_reply", st, err)
	}
	comments, err := client.Store.Comments(ctx, task.ID)
	if err != nil || len(comments) != 1 || !strings.Contains(comments[0].Body, "bwsalmon/grain") ||
		!strings.Contains(comments[0].Body, "Settings → Git") {
		t.Fatalf("comments = %+v (%v); want one naming the repo and Settings → Git", comments, err)
	}
}

// The pickers' list for a team with nothing attached: what its credential
// reaches, push marked, and never another team's repositories.
func TestReachableReposListsWhatTheTeamCredentialReaches(t *testing.T) {
	t.Parallel()
	srv, _ := reachClient(t)
	rec := do(t, srv, "GET", "/api/deployment/reachable-repos", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET reachable-repos: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Available bool                     `json:"available"`
		Repos     []gitproxy.ReachableRepo `json:"repos"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []gitproxy.ReachableRepo{{Repo: "acme/docs", Push: false}, {Repo: "gke-labs/bouncer", Push: true}}
	if !got.Available || len(got.Repos) != 2 || got.Repos[0] != want[0] || got.Repos[1] != want[1] {
		t.Errorf("reachable-repos = %+v; want available %+v", got, want)
	}
}

// ReachableRepos lists repositories from non-default ambient hosts (e.g. depot.code.forge.test),
// prefixing them with the host name so New Task dropdown options point to Depot.
func TestReachableReposListsDepotRepositories(t *testing.T) {
	srv, client, credentials := testServerWithCreds(t)
	ctx := context.Background()

	depotHost := "depot.code.forge.test"
	bin := t.TempDir()
	forgecli := "#!/bin/sh\necho 'forgecli-token-" + depotHost + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "forgecli"), []byte(forgecli), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := credentials.SetAmbientOn("depot-cred", gitproxy.CredentialHost{Host: depotHost, Kind: model.GitHostGitHub}, "forgecli"); err != nil {
		t.Fatal(err)
	}
	sim := githubsim.New("experimental", "bsalmon-test", "", "main")
	sim.Reachable = map[string][]githubsim.ReachableRepo{
		"forgecli-token-" + depotHost: {
			{FullName: "experimental/bsalmon-test", Push: true},
			{FullName: "corp/read-only", Push: false},
		},
	}
	credentials.AppTransport = sim
	client.Config.GitReach = &gitproxy.Reach{Credentials: credentials, Host: "github.com"}

	if err := client.Store.UpdateConfig(ctx, func(c *model.Config) error {
		c.GitConnections = []model.TeamGitConnection{{Credential: "depot-cred", Host: depotHost}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, "GET", "/api/deployment/reachable-repos", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET reachable-repos: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Available bool                     `json:"available"`
		Repos     []gitproxy.ReachableRepo `json:"repos"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []gitproxy.ReachableRepo{
		{Repo: depotHost + "/corp/read-only", Push: false},
		{Repo: depotHost + "/experimental/bsalmon-test", Push: true},
	}
	if !got.Available || len(got.Repos) != 2 || got.Repos[0] != want[0] || got.Repos[1] != want[1] {
		t.Errorf("reachable-repos = %+v; want available %+v", got, want)
	}
}

// With no stand-in for the API, a forgecli connection's listing is `forgecli api`
// itself: the fake forgecli's repos come back, and a lapsed corp login comes
// back as that connection's own error, saying to renew its login.
func TestReachableReposListsDepotRepositoriesThroughGGHAPI(t *testing.T) {
	srv, client, credentials := testServerWithCreds(t)
	ctx := context.Background()
	t.Setenv("GRAIN_IN_CONTAINER", "0")

	depotHost := "depot.code.forge.test"
	bin, state := t.TempDir(), t.TempDir()
	forgecli := `#!/bin/sh
if [ "$1 $2" = "auth token" ]; then echo "forgecli-token-$GH_HOST"; exit 0; fi
[ "$1" = api ] || exit 2
if [ -f "` + state + `/corp-login" ]; then
  printf 'HTTP/2.0 302 Found\r\nLocation: https://login.forge.test/?authLevel=2000000\r\n\r\n'
  exit 1
fi
printf 'HTTP/2.0 200 OK\r\nContent-Type: application/json\r\n\r\n'
printf '[{"full_name":"experimental/bsalmon-test","permissions":{"push":true}}]'
`
	if err := os.WriteFile(filepath.Join(bin, "forgecli"), []byte(forgecli), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := credentials.SetAmbientOn("depot-cred", gitproxy.CredentialHost{Host: depotHost, Kind: model.GitHostGitHub}, "forgecli"); err != nil {
		t.Fatal(err)
	}
	client.Config.GitReach = &gitproxy.Reach{Credentials: credentials, Host: "github.com",
		API: github.NewFakeTransport(), TTL: time.Nanosecond}
	if err := client.Store.UpdateConfig(ctx, func(c *model.Config) error {
		c.GitConnections = []model.TeamGitConnection{{Credential: "depot-cred", Host: depotHost}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	type answer struct {
		Available bool                      `json:"available"`
		Reason    string                    `json:"reason"`
		Repos     []gitproxy.ReachableRepo  `json:"repos"`
		Errors    []ui.ConnectionReachError `json:"errors"`
	}
	get := func() answer {
		t.Helper()
		rec := do(t, srv, "GET", "/api/deployment/reachable-repos", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET reachable-repos: %d %s", rec.Code, rec.Body.String())
		}
		var got answer
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	got := get()
	if !got.Available || len(got.Repos) != 1 || got.Repos[0].Repo != depotHost+"/experimental/bsalmon-test" ||
		!got.Repos[0].Push || len(got.Errors) != 0 {
		t.Fatalf("reachable-repos = %+v; want forgecli's one repository", got)
	}

	if err := os.WriteFile(filepath.Join(state, "corp-login"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got = get()
	if got.Available || len(got.Errors) != 1 || !got.Errors[0].LoginNeeded || got.Errors[0].Credential != "depot-cred" ||
		got.Errors[0].Host != depotHost || !strings.Contains(got.Errors[0].Error, "forge login needed: run `forgecli-login` as ") ||
		!strings.Contains(got.Reason, "run `forgecli-login`") {
		t.Fatalf("reachable-repos with a lapsed corp login = %+v", got)
	}
}

// assertParkedOnReach fails unless taskID is parked on the reach comment
// naming repo.
func assertParkedOnReach(t *testing.T, client *ui.Client, taskID, repo string) {
	t.Helper()
	ctx := context.Background()
	if st, err := client.Store.State(ctx, taskID); err != nil || st != model.StateAwaitingReply {
		t.Fatalf("task %s state = %q (%v); want awaiting_reply", taskID, st, err)
	}
	comments, err := client.Store.Comments(ctx, taskID)
	if err != nil || len(comments) != 1 || !strings.Contains(comments[0].Body, repo) ||
		!strings.Contains(comments[0].Body, "Settings → Git") {
		t.Fatalf("comments = %+v (%v); want one naming %s and Settings → Git", comments, err, repo)
	}
}

// "Run now" on a schedule files what the cadence would have, and parks
// it the same way when the schedule's team cannot reach its repository
// (grain/task-1857) -- the task exists either way.
func TestRunScheduleNowParksATaskTheTeamCannotReach(t *testing.T) {
	t.Parallel()
	_, client := reachClient(t)
	ctx := context.Background()
	if err := client.Store.PutSchedule(ctx, model.Schedule{
		ID: "sched-1", Title: "nightly", Body: "do it",
		Target:     model.RepoRef{Owner: "bwsalmon", Name: "grain"},
		Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 24},
		Enabled:    true, NextRunAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	run, err := client.RunScheduleNow(ctx, "sched-1")
	if err != nil {
		t.Fatalf("RunScheduleNow: %v", err)
	}
	if run.TaskID == "" {
		t.Fatal("RunScheduleNow filed nothing; want the task filed and parked")
	}
	assertParkedOnReach(t, client, run.TaskID, "bwsalmon/grain")
}

// A suite run's first pass is filed as the run starts, so it is parked
// rather than refused when the team cannot reach the repository.
func TestCreateSuiteRunParksAPassTheTeamCannotReach(t *testing.T) {
	t.Parallel()
	_, client := reachClient(t)
	ctx := context.Background()
	if err := client.Store.PutTemplate(ctx, model.Template{ID: "template-smoke", Name: "Smoke", Title: "Smoke",
		Body: "run it"}); err != nil {
		t.Fatal(err)
	}
	if err := client.Store.PutSuite(ctx, model.Suite{ID: "suite-1", Name: "smoke",
		Items: []model.SuiteItem{{TemplateID: "template-smoke"}}, Mode: model.SuiteCount, Count: 1}); err != nil {
		t.Fatal(err)
	}
	run, err := client.CreateSuiteRun(ctx, ui.CreateSuiteRunRequest{SuiteID: "suite-1", Repo: "bwsalmon/grain",
		Base: "main"})
	if err != nil {
		t.Fatalf("CreateSuiteRun: %v", err)
	}
	stored, err := client.Store.GetSuiteRun(ctx, run.ID)
	if err != nil || stored == nil || len(stored.Tasks) != 1 {
		t.Fatalf("suite run = (%+v, %v); want one task in its first pass", stored, err)
	}
	assertParkedOnReach(t, client, stored.Tasks[0].TaskID, "bwsalmon/grain")
}

// Multi-host discovery enumerates repositories from every configured git
// connection (both default host and on-premise instances like depot.code.forge.test),
// prefixing non-default hosts with their canonical host identifier.
func TestReachableReposMultiHostDiscovery(t *testing.T) {
	t.Parallel()
	srv, client, credentials := testServerWithCreds(t)
	ctx := context.Background()

	// Default host credential
	if err := credentials.SetToken("dotcom-cred", "tok-dotcom"); err != nil {
		t.Fatal(err)
	}
	dotcomSim := githubsim.New("acme", "widgets", "", "main")
	dotcomSim.Reachable = map[string][]githubsim.ReachableRepo{
		"tok-dotcom": {{FullName: "acme/widgets", Push: true}},
	}

	// Non-default GHOP host credential
	if err := credentials.SetToken("depot-cred", "tok-depot"); err != nil {
		t.Fatal(err)
	}
	if err := credentials.SetHost("depot-cred", gitproxy.CredentialHost{
		Host: "depot.code.forge.test",
		Kind: model.GitHostGitHub,
	}); err != nil {
		t.Fatal(err)
	}
	depotSim := githubsim.New("experimental", "bsalmon-test", "", "main")
	depotSim.Reachable = map[string][]githubsim.ReachableRepo{
		"tok-depot": {{FullName: "experimental/bsalmon-test", Push: true}},
	}

	client.Config.GitReach = &gitproxy.Reach{
		Credentials: credentials,
		API:         dotcomSim,
		Host:        "github.com",
		Transports: map[string]github.Transport{
			"depot.code.forge.test": depotSim,
		},
	}

	if err := client.Store.UpdateConfig(ctx, func(c *model.Config) error {
		c.GitConnections = []model.TeamGitConnection{
			{Credential: "dotcom-cred", Host: "github.com"},
			{Credential: "depot-cred", Host: "depot.code.forge.test"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, "GET", "/api/deployment/reachable-repos", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET reachable-repos: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Available bool                     `json:"available"`
		Repos     []gitproxy.ReachableRepo `json:"repos"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Available {
		t.Fatalf("available = false, want true")
	}
	want := []gitproxy.ReachableRepo{
		{Repo: "acme/widgets", Push: true},
		{Repo: "depot.code.forge.test/experimental/bsalmon-test", Push: true},
	}
	if len(got.Repos) != len(want) || got.Repos[0] != want[0] || got.Repos[1] != want[1] {
		t.Errorf("reachable-repos = %+v; want %+v", got.Repos, want)
	}
}

// Beside the repositories, reachable-repos says how each of the team's
// Git connections fared -- the Add repo picker's discovery status -- so a
// connection that lists nothing is named with its reason rather than
// leaving an empty picker unexplained. available, reason and repos keep
// the shape they always had.
func TestReachableReposReportsEachConnection(t *testing.T) {
	t.Parallel()
	srv, client, credentials := testServerWithCreds(t)
	ctx := context.Background()
	if err := credentials.SetToken("dotcom-cred", "tok-dotcom"); err != nil {
		t.Fatal(err)
	}
	sim := githubsim.New("acme", "widgets", "", "main")
	sim.Reachable = map[string][]githubsim.ReachableRepo{
		"tok-dotcom": {{FullName: "acme/widgets", Push: true}, {FullName: "acme/docs"}},
	}
	client.Config.GitReach = &gitproxy.Reach{Credentials: credentials, API: sim, Host: "github.com"}
	if err := client.Store.UpdateConfig(ctx, func(c *model.Config) error {
		c.GitConnections = []model.TeamGitConnection{
			{Credential: "dotcom-cred", Host: "github.com"},
			{Credential: "missing-cred", Host: "github.com", Scope: "other"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, "GET", "/api/deployment/reachable-repos", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET reachable-repos: %d %s", rec.Code, rec.Body.String())
	}
	var got ui.ReachableReposResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Available || got.Reason != "" || len(got.Repos) != 2 {
		t.Fatalf("reachable-repos = %+v; want available with two repos", got)
	}
	if len(got.Connections) != 2 {
		t.Fatalf("connections = %+v; want one per connection", got.Connections)
	}
	ok, bad := got.Connections[0], got.Connections[1]
	if ok.Credential != "dotcom-cred" || ok.Host != "github.com" || !ok.OK || ok.RepoCount != 2 || ok.Error != "" {
		t.Errorf("dotcom connection = %+v; want ok on github.com with 2 repos", ok)
	}
	if bad.Credential != "missing-cred" || bad.OK || bad.RepoCount != 0 || !strings.Contains(bad.Error, "missing-cred") {
		t.Errorf("missing connection = %+v; want not ok, with an error naming it", bad)
	}

	// A deployment with no connection at all still answers in the old
	// shape, with an empty (not null) connection list.
	if err := client.Store.UpdateConfig(ctx, func(c *model.Config) error {
		c.GitConnections, c.GitCredential = nil, ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rec = do(t, srv, "GET", "/api/deployment/reachable-repos", "")
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if raw["available"] != false || raw["reason"] == "" {
		t.Errorf("bare team = %v; want unavailable with a reason", raw)
	}
	for _, key := range []string{"repos", "connections"} {
		if list, ok := raw[key].([]any); !ok || len(list) != 0 {
			t.Errorf("bare team %s = %#v; want an empty list", key, raw[key])
		}
	}
}
