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

package cli

// The state repository, from the daemon's own start-up: that a running
// grain really does clone the repository it was pointed at, really does
// export what an operator files through the UI back into it, and really
// does push that to the remote. Everything here goes through run() and
// the REST API, against a bare repository on disk -- there is no fake
// git and no fake store in this file.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"gopkg.in/yaml.v3"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestDaemonPushesItsDatabaseToTheStateRepository(t *testing.T) {
	t.Skip("operational SQL is no longer backed up to the state repository")
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets", "github"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedTestConnections(t, dataDir, "git:\n  - credential: anonymous\n")
	// The repository an operator created and pointed grain at: real, and
	// completely empty, which is the "start from scratch" case.
	remote := initBareRepo(t, filepath.Join(t.TempDir(), "state.git"))
	if err := staterepo.SaveSettings(dataDir, staterepo.Settings{Remote: remote}); err != nil {
		t.Fatal(err)
	}

	uiAddr := freeTCPAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	// Stopped, and waited for, before this test's own temporary
	// directories are removed: run() writes into the working tree until
	// the moment it returns, and t.TempDir's cleanup racing that is a
	// failure with nothing to do with what is being tested.
	defer func() {
		cancel()
		<-runErr
	}()
	seedTestAccessCode(t, dataDir)
	go func() {
		runErr <- daemon.Run(ctx, daemon.Config{
			DataDir: dataDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: time.Hour,
			GithubHost: "127.0.0.1:0", GithubInsecureHTTP: true,
			UiAddr: uiAddr, Actor: "tester",
		})
	}()

	client := accessCodeClient("http://" + uiAddr)
	waitForUI(t, ctx, client)

	// A task filed through the API is a row in the database, and the
	// point of all of this is that a row in the database is a file in the
	// repository.
	created, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "A task that should reach the state repository",
		Repo:  "owner/payments-api",
	})
	if err != nil {
		t.Fatalf("filing a task: %v", err)
	}

	// Synced on demand rather than by waiting out the daemon's own timer:
	// the same code path, reached through the bootstrap's own endpoint.
	if code, body := post(t, "http://"+uiAddr+"/api/state-repo/sync"); code != http.StatusOK {
		t.Fatalf("sync returned %d: %s", code, body)
	}

	out, err := exec.Command("git", "--git-dir", remote, "show", "main:"+staterepo.TablesDir+"/task.json").CombinedOutput()
	if err != nil {
		t.Fatalf("reading the dump out of the remote: %v: %s", err, out)
	}
	if !strings.Contains(string(out), created.ID) {
		t.Fatalf("the task did not reach the remote:\n%s", out)
	}
	if !strings.Contains(string(out), "A task that should reach the state repository") {
		t.Fatalf("the dump is not readable enough to review:\n%s", out)
	}

	// And what the bootstrap pane reports agrees with what actually
	// happened: a remote, a commit, and a secrets key on this host that
	// is not in the repository.
	code, body := get(t, "http://"+uiAddr+"/api/state-repo")
	if code != http.StatusOK {
		t.Fatalf("status returned %d: %s", code, body)
	}
	var status ui.StateRepoStatus
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatalf("decoding the status: %v: %s", err, body)
	}
	if !status.Available {
		t.Fatalf("status does not describe this deployment: %+v", status)
	}
	if status.Head == "" || status.SchemaVersion != status.BuildSchemaVersion {
		t.Fatalf("status does not describe the repository: %+v", status)
	}
	if !strings.HasPrefix(status.SecretsPublicKey, "grain-secret-pub-v1:") {
		t.Fatalf("no secrets key was minted for a fresh install: %+v", status)
	}
	// The private key is on this host, outside the working tree -- so a
	// clone of the remote cannot decrypt anything.
	if _, err := os.Stat(filepath.Join(dataDir, "secrets", "secrets.key")); err != nil {
		t.Fatalf("the private key is not where the deployment says it is: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "state-repo", "secrets.key")); !os.IsNotExist(err) {
		t.Fatal("the private key is inside the repository, which is pushed to the remote")
	}
}

// TestATaskSurvivesARestartWithAStateRepository is tests/container's own
// "a task survives the container that created it", at the level the state
// repository actually lives at -- and it is the regression that shipped
// with the first cut of this: the exporter runs on a timer, so a task
// filed seconds before a stop exists only in the database, and a start
// that imported the repository unconditionally rolled it straight back
// out again.
func TestATaskSurvivesARestartWithAStateRepository(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets", "github"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedTestConnections(t, dataDir, "git:\n  - credential: anonymous\n")

	// First daemon: file a task, then stop straight away -- before the
	// sync timer has ticked, which is the whole point.
	uiAddr := freeTCPAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	seedTestAccessCode(t, dataDir)
	go func() {
		runErr <- daemon.Run(ctx, daemon.Config{
			DataDir: dataDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: time.Hour,
			GithubHost: "127.0.0.1:0", GithubInsecureHTTP: true, UiAddr: uiAddr, Actor: "tester",
		})
	}()
	client := accessCodeClient("http://" + uiAddr)
	waitForUI(t, ctx, client)
	created, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "Filed just before the stop", Repo: "owner/payments-api"})
	if err != nil {
		t.Fatalf("filing a task: %v", err)
	}
	cancel()
	<-runErr

	// Second daemon, same data directory: the task has to still be there.
	secondAddr := freeTCPAddr(t)
	ctx2, cancel2 := context.WithCancel(context.Background())
	runErr2 := make(chan error, 1)
	seedTestAccessCode(t, dataDir)
	go func() {
		runErr2 <- daemon.Run(ctx2, daemon.Config{
			DataDir: dataDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: time.Hour,
			GithubHost: "127.0.0.1:0", GithubInsecureHTTP: true, UiAddr: secondAddr, Actor: "tester",
		})
	}()
	defer func() {
		cancel2()
		<-runErr2
	}()
	second := accessCodeClient("http://" + secondAddr)
	waitForUI(t, ctx2, second)
	if _, err := second.Task(ctx2, created.ID); err != nil {
		t.Fatalf("the task did not survive the restart: %v", err)
	}
}

// The other direction, live: a change merged into the state repository
// while the daemon is running has to take effect in the daemon that is
// running, not in the next one (bwsalmon/grain#184). What must *not*
// happen alongside it is the rest of the import: the task filed here is
// the stand-in for every row a live run holds an id from, and it has to
// still be there afterwards.
func TestAMergedSettingsChangeTakesEffectWithoutARestart(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets", "github"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedTestConnections(t, dataDir, "git:\n  - credential: anonymous\n")

	uiAddr := freeTCPAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	defer func() {
		cancel()
		<-runErr
	}()
	seedTestAccessCode(t, dataDir)
	go func() {
		runErr <- daemon.Run(ctx, daemon.Config{
			DataDir: dataDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: time.Hour,
			GithubHost: "127.0.0.1:0", GithubInsecureHTTP: true,
			UiAddr: uiAddr, Actor: "tester",
		})
	}()
	client := accessCodeClient("http://" + uiAddr)
	waitForUI(t, ctx, client)

	// A template this deployment already has, so the repository holds a
	// row to change rather than only a file to create.
	seedThroughConfigRepo(t, uiAddr, dataDir, "/api/templates", `{"name":"nightly","title":"Run the nightly sweep"}`)
	created, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "A task a run is holding", Repo: "owner/payments-api"})
	if err != nil {
		t.Fatalf("filing a task: %v", err)
	}
	if code, body := post(t, "http://"+uiAddr+"/api/state-repo/sync"); code != http.StatusOK {
		t.Fatalf("sync returned %d: %s", code, body)
	}

	// Simulate a native grain/config pull request being fast-forwarded into
	// the local main branch: the template is retitled and another is added.
	work := daemon.StateRepoDir(dataDir)
	gitIn(t, work, "checkout", "-b", "agent-change")
	// The settings this repository is edited through are three nested
	// documents in the format a repository writes its own declarations in
	// (staterepo.DumpPath), not a file of rows per table.
	path := filepath.Join(work, filepath.FromSlash(staterepo.DumpPath("template")))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the dump: %v", err)
	}
	var doc struct {
		Templates []map[string]any `yaml:"templates"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing the dump: %v: %s", err, data)
	}
	rows := doc.Templates
	if len(rows) != 1 {
		t.Fatalf("the dump does not hold the template: %s", data)
	}
	rows[0]["title"] = "Run the nightly sweep, twice"
	added := map[string]any{}
	for k, v := range rows[0] {
		added[k] = v
	}
	added["id"] = "tpl-proposed-by-an-agent"
	added["name"] = "proposed"
	added["title"] = "Filed by a merged pull request"
	rows = append(rows, added)
	merged, err := yaml.Marshal(map[string]any{"templates": rows})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, merged, 0o644); err != nil {
		t.Fatalf("writing the merged dump: %v", err)
	}
	gitIn(t, work, "-c", "user.email=agent@grain", "-c", "user.name=agent", "commit", "-am", "Change the templates")
	gitIn(t, work, "checkout", "main")
	gitIn(t, work, "merge", "--ff-only", "agent-change")

	// One cycle of the daemon's own loop, reached through the same
	// endpoint the pane's "Sync now" uses -- rather than waiting out the
	// thirty-second timer that would get there on its own.
	if code, body := post(t, "http://"+uiAddr+"/api/state-repo/sync"); code != http.StatusOK {
		t.Fatalf("sync returned %d: %s", code, body)
	}

	code, body := get(t, "http://"+uiAddr+"/api/templates")
	if code != http.StatusOK {
		t.Fatalf("listing templates returned %d: %s", code, body)
	}
	var templates []ui.Template
	if err := json.Unmarshal([]byte(body), &templates); err != nil {
		t.Fatalf("decoding the templates: %v: %s", err, body)
	}
	titles := map[string]bool{}
	for _, tmpl := range templates {
		titles[tmpl.Title] = true
	}
	if !titles["Run the nightly sweep, twice"] {
		t.Fatalf("the merged edit is not live in the running daemon: %s", body)
	}
	if !titles["Filed by a merged pull request"] {
		t.Fatalf("the merged addition is not live in the running daemon: %s", body)
	}

	// And nothing else came with it: the task is still there, with the id
	// a live run would be holding.
	if _, err := client.Task(ctx, created.ID); err != nil {
		t.Fatalf("applying a settings change took a task with it: %v", err)
	}
}

// Reading the repository through the API a person actually has: the
// whole daemon, the real working tree, and the two routes the State
// pane's browser calls.
//
// The half that matters is what it will not serve. This directory holds
// .git, it holds the private key's neighbours, and older builds put the
// encrypted secrets file in it -- so the assertion is not only that
// tables/ comes back, but that nothing outside the exported set does,
// however it is spelled.
func TestTheExportedConfigurationCanBeReadThroughTheAPI(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets", "github"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedTestConnections(t, dataDir, "git:\n  - credential: anonymous\n")

	uiAddr := freeTCPAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	defer func() {
		cancel()
		<-runErr
	}()
	seedTestAccessCode(t, dataDir)
	go func() {
		runErr <- daemon.Run(ctx, daemon.Config{
			DataDir: dataDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: time.Hour,
			GithubHost: "127.0.0.1:0", GithubInsecureHTTP: true,
			UiAddr: uiAddr, Actor: "tester",
		})
	}()
	client := accessCodeClient("http://" + uiAddr)
	waitForUI(t, ctx, client)
	if code, body := post(t, "http://"+uiAddr+"/api/state-repo/sync"); code != http.StatusOK {
		t.Fatalf("sync returned %d: %s", code, body)
	}

	// Something beside the exported content, of the kind a deployment
	// really does keep there: an old build's ciphertext left in the
	// working tree.
	work := daemon.StateRepoDir(dataDir)
	if err := os.WriteFile(filepath.Join(work, staterepo.SecretsFile), []byte("ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, body := get(t, "http://"+uiAddr+"/api/state-repo/files")
	if code != http.StatusOK {
		t.Fatalf("listing returned %d: %s", code, body)
	}
	var tree ui.StateRepoTree
	if err := json.Unmarshal([]byte(body), &tree); err != nil {
		t.Fatalf("decoding the listing: %v: %s", err, body)
	}
	if tree.Head == "" {
		t.Errorf("the listing does not say which commit it is of: %s", body)
	}
	listed := map[string]bool{}
	for _, entry := range tree.Entries {
		listed[entry.Path] = true
		for _, forbidden := range []string{".git", ".gitignore", staterepo.SecretsFile} {
			if entry.Path == forbidden || strings.HasPrefix(entry.Path, forbidden+"/") {
				t.Errorf("the listing offers %s", entry.Path)
			}
		}
	}
	for _, want := range []string{staterepo.ReadmeFile, staterepo.SchemaVersionFile,
		staterepo.TablesDir, staterepo.TablesDir + "/grain_config.json"} {
		if !listed[want] {
			t.Errorf("%s is not in the listing: %s", want, body)
		}
	}

	// And one of them reads.
	code, body = get(t, "http://"+uiAddr+"/api/state-repo/file?path="+
		url.QueryEscape(staterepo.TablesDir+"/grain_config.json"))
	if code != http.StatusOK {
		t.Fatalf("reading the config dump returned %d: %s", code, body)
	}
	var file ui.StateRepoFile
	if err := json.Unmarshal([]byte(body), &file); err != nil {
		t.Fatalf("decoding the file: %v: %s", err, body)
	}
	if !strings.Contains(file.Content, "\"id\"") {
		t.Errorf("the config dump does not look like the table: %s", file.Content)
	}

	// Nothing else does, whatever spelling is tried -- and the refusal
	// says what may be read instead.
	for _, path := range []string{
		".git/config",
		".git/HEAD",
		".gitignore",
		staterepo.SecretsFile,
		"../secrets/secrets.key",
		"tables/../.git/config",
		"/etc/passwd",
	} {
		code, body := get(t, "http://"+uiAddr+"/api/state-repo/file?path="+url.QueryEscape(path))
		if code != http.StatusForbidden {
			t.Errorf("reading %s returned %d: %s", path, code, body)
			continue
		}
		if !strings.Contains(body, "browsable") {
			t.Errorf("refusing %s does not say what is browsable: %s", path, body)
		}
		if strings.Contains(body, "ciphertext") {
			t.Fatalf("refusing %s returned the file anyway: %s", path, body)
		}
	}
}

// A deployment that diverged from its remote, and got itself back.
//
// The failure is the one a push that fails leaves behind: grain commits
// its export locally, the push does not go through -- here because the
// remote is briefly not where the working tree thinks it is, in the real
// thing because an installation token expired -- and a pull request
// against the state repository is merged before the next push succeeds.
// The two branches are then on either side of the same parent, which
// Pull refuses to resolve, and until this nothing cleared that refusal:
// every tick logged it again, no merged change was ever applied, and no
// export ever reached the remote again.
//
// Both directions have to come back in one cycle, and both are asserted
// here: the merged retitle is live in the running daemon, and the task
// that only existed in the stranded commit is in the remote's own copy of
// the dump.
func TestADivergedStateRepositoryRecoversOnTheNextSync(t *testing.T) {
	t.Skip("operational SQL is no longer backed up to the state repository")
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets", "github"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedTestConnections(t, dataDir, "git:\n  - credential: anonymous\n")
	remote := initBareRepo(t, filepath.Join(t.TempDir(), "state.git"))
	if err := staterepo.SaveSettings(dataDir, staterepo.Settings{Remote: remote}); err != nil {
		t.Fatal(err)
	}

	uiAddr := freeTCPAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	defer func() {
		cancel()
		<-runErr
	}()
	seedTestAccessCode(t, dataDir)
	go func() {
		runErr <- daemon.Run(ctx, daemon.Config{
			DataDir: dataDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: time.Hour,
			GithubHost: "127.0.0.1:0", GithubInsecureHTTP: true,
			UiAddr: uiAddr, Actor: "tester",
		})
	}()
	client := accessCodeClient("http://" + uiAddr)
	waitForUI(t, ctx, client)

	seedThroughConfigRepo(t, uiAddr, dataDir, "/api/templates", `{"name":"nightly","title":"Run the nightly sweep"}`)
	if code, body := post(t, "http://"+uiAddr+"/api/state-repo/sync"); code != http.StatusOK {
		t.Fatalf("sync returned %d: %s", code, body)
	}

	// The push that fails. The remote is pointed somewhere that is not
	// there for as long as one export takes, which is what an expired
	// credential looks like from the working tree's side: the commit is
	// made, and it stays here.
	stateDir := filepath.Join(dataDir, "state-repo")
	gitIn(t, stateDir, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
	stranded, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "Filed while the remote was unreachable", Repo: "owner/payments-api"})
	if err != nil {
		t.Fatalf("filing a task: %v", err)
	}
	if code, _ := post(t, "http://"+uiAddr+"/api/state-repo/sync"); code == http.StatusOK {
		t.Fatal("a sync with an unreachable remote reported success")
	}
	gitIn(t, stateDir, "remote", "set-url", "origin", remote)

	// And the merge that lands before the push can be retried: an agent's
	// pull request against the settings, from a clone taken before any of
	// this happened.
	work := filepath.Join(t.TempDir(), "clone")
	if out, err := exec.Command("git", "clone", "--quiet", remote, work).CombinedOutput(); err != nil {
		t.Fatalf("cloning the remote: %v: %s", err, out)
	}
	path := filepath.Join(work, filepath.FromSlash(staterepo.DumpPath("template")))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the dump: %v", err)
	}
	var doc struct {
		Templates []map[string]any `yaml:"templates"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing the dump: %v: %s", err, data)
	}
	if len(doc.Templates) != 1 {
		t.Fatalf("the dump does not hold the template: %s", data)
	}
	doc.Templates[0]["title"] = "Retitled by a pull request nobody could push over"
	merged, err := yaml.Marshal(map[string]any{"templates": doc.Templates})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, merged, 0o644); err != nil {
		t.Fatalf("writing the merged dump: %v", err)
	}
	gitIn(t, work, "-c", "user.email=agent@grain", "-c", "user.name=agent", "commit", "-am", "Retitle the template")
	gitIn(t, work, "push", "--quiet", "origin", "main")

	// One ordinary cycle of the daemon's own loop, which is all the
	// recovery gets: the same call the timer makes.
	if code, body := post(t, "http://"+uiAddr+"/api/state-repo/sync"); code != http.StatusOK {
		t.Fatalf("the sync that should have recovered returned %d: %s", code, body)
	}

	// Inbound: what was merged is live in the daemon that is running.
	code, body := get(t, "http://"+uiAddr+"/api/templates")
	if code != http.StatusOK {
		t.Fatalf("listing templates returned %d: %s", code, body)
	}
	var templates []ui.Template
	if err := json.Unmarshal([]byte(body), &templates); err != nil {
		t.Fatalf("decoding the templates: %v: %s", err, body)
	}
	if len(templates) != 1 || templates[0].Title != "Retitled by a pull request nobody could push over" {
		t.Fatalf("the merged change never became live: %s", body)
	}

	// Outbound: the task that existed only in the commit the recovery
	// threw away is back in the remote's own copy of the dump, written
	// there by the export that followed the reset.
	out, err := exec.Command("git", "--git-dir", remote, "show", "main:"+staterepo.TablesDir+"/task.json").CombinedOutput()
	if err != nil {
		t.Fatalf("reading the dump out of the remote: %v: %s", err, out)
	}
	if !strings.Contains(string(out), stranded.ID) {
		t.Fatalf("the stranded export never reached the remote:\n%s", out)
	}

	// And the pane says the deployment is syncing again, rather than
	// still reporting the divergence it recovered from.
	code, body = get(t, "http://"+uiAddr+"/api/state-repo")
	if code != http.StatusOK {
		t.Fatalf("status returned %d: %s", code, body)
	}
	var status ui.StateRepoStatus
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatalf("decoding the status: %v: %s", err, body)
	}
	if status.Error != "" {
		t.Fatalf("the pane still reports the deployment as broken: %+v", status)
	}
}

// The same divergence found at startup rather than on a tick, which used
// to be the worse half of it: Load refuses, run() returns the refusal,
// and the deployment does not come up at all -- over a commit that holds
// nothing but a dump of the database it is refusing to start against.
func TestADivergedStateRepositoryDoesNotStopTheDaemonFromStarting(t *testing.T) {
	t.Skip("operational SQL is no longer backed up to the state repository")
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets", "github"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedTestConnections(t, dataDir, "git:\n  - credential: anonymous\n")
	remote := initBareRepo(t, filepath.Join(t.TempDir(), "state.git"))
	if err := staterepo.SaveSettings(dataDir, staterepo.Settings{Remote: remote}); err != nil {
		t.Fatal(err)
	}

	// A first daemon, long enough to file a task and push it.
	uiAddr := freeTCPAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	seedTestAccessCode(t, dataDir)
	go func() {
		runErr <- daemon.Run(ctx, daemon.Config{
			DataDir: dataDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: time.Hour,
			GithubHost: "127.0.0.1:0", GithubInsecureHTTP: true, UiAddr: uiAddr, Actor: "tester",
		})
	}()
	client := accessCodeClient("http://" + uiAddr)
	waitForUI(t, ctx, client)
	created, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "Filed before the divergence", Repo: "owner/payments-api"})
	if err != nil {
		t.Fatalf("filing a task: %v", err)
	}
	if code, body := post(t, "http://"+uiAddr+"/api/state-repo/sync"); code != http.StatusOK {
		t.Fatalf("sync returned %d: %s", code, body)
	}
	cancel()
	<-runErr

	// A row written after that last push, so it lives in the database and
	// in the export commit below and nowhere else. That is the whole of
	// what a recovery has to not throw away: the reset is safe precisely
	// because the database still holds what the discarded commits held,
	// and a start that imported the remote's older dump over it would
	// make that argument false.
	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	stranded := model.Task{
		ID: "stranded", Intent: model.IntentImplement, Title: "Filed while the push was failing",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "tester"}},
			Reason:      model.ReasonDirect,
		},
		Target:  &model.RepoRef{Owner: "owner", Name: "payments-api"},
		Binding: model.BindingDirective,
	}
	if err := store.PutTask(context.Background(), stranded); err != nil {
		t.Fatalf("filing the stranded task: %v", err)
	}
	// An export committed on this host and never pushed -- grain's own
	// author, and nothing but the dump in it -- with a merge landing on
	// the remote on the other side of the same parent.
	stateDir := filepath.Join(dataDir, "state-repo")
	if err := staterepo.Export(context.Background(), db, stateDir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	db.Close()
	gitIn(t, stateDir, "-c", "user.email=grain@a-container-that-has-gone", "-c", "user.name=grain",
		"commit", "-am", "Update grain state")

	work := filepath.Join(t.TempDir(), "clone")
	if out, err := exec.Command("git", "clone", "--quiet", remote, work).CombinedOutput(); err != nil {
		t.Fatalf("cloning the remote: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(work, "NOTES.md"), []byte("merged while grain was down\n"), 0o644); err != nil {
		t.Fatalf("writing the merged file: %v", err)
	}
	gitIn(t, work, "add", "--all", ".")
	gitIn(t, work, "-c", "user.email=agent@grain", "-c", "user.name=agent", "commit", "-m", "A merged pull request")
	gitIn(t, work, "push", "--quiet", "origin", "main")

	// The deployment comes back up, on the remote's history, with the
	// task it had.
	secondAddr := freeTCPAddr(t)
	ctx2, cancel2 := context.WithCancel(context.Background())
	runErr2 := make(chan error, 1)
	seedTestAccessCode(t, dataDir)
	go func() {
		runErr2 <- daemon.Run(ctx2, daemon.Config{
			DataDir: dataDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: time.Hour,
			GithubHost: "127.0.0.1:0", GithubInsecureHTTP: true, UiAddr: secondAddr, Actor: "tester",
		})
	}()
	defer func() {
		cancel2()
		<-runErr2
	}()
	second := accessCodeClient("http://" + secondAddr)
	waitForUI(t, ctx2, second)
	if _, err := second.Task(ctx2, created.ID); err != nil {
		t.Fatalf("the task did not survive the recovery: %v", err)
	}
	if _, err := second.Task(ctx2, "stranded"); err != nil {
		t.Fatalf("the task that existed only in the commits the recovery reset away "+
			"did not survive it: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "NOTES.md")); err != nil {
		t.Fatalf("the merged change never arrived: %v", err)
	}
	// And what the recovery kept goes back out to the remote, rather than
	// living only on this host until the next thing goes wrong.
	if code, body := post(t, "http://"+secondAddr+"/api/state-repo/sync"); code != http.StatusOK {
		t.Fatalf("sync returned %d: %s", code, body)
	}
	out, err := exec.Command("git", "-C", remote, "show", "main:"+staterepo.TablesDir+"/task.json").CombinedOutput()
	if err != nil {
		t.Fatalf("reading the remote's dump: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "stranded") {
		t.Fatalf("the recovered task never reached the remote:\n%s", out)
	}
}

// A remote that cannot be reached at startup is the ordinary accident --
// a network blip, an installation token that expired overnight, a
// repository renamed on GitHub -- and it used to take the whole
// deployment down with it: Load returned the fetch failure, run()
// returned that, and systemd restarted the process into the same
// failure, UI and all, with the reason only in the journal. The
// database on this host is complete and the working tree is where the
// last fetch left it, so there is nothing here that starting would lose.
func TestAnUnreachableStateRepositoryDoesNotStopTheDaemonFromStarting(t *testing.T) {
	t.Skip("operational SQL is no longer backed up to the state repository")
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets", "github"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedTestConnections(t, dataDir, "git:\n  - credential: anonymous\n")
	remote := initBareRepo(t, filepath.Join(t.TempDir(), "state.git"))
	if err := staterepo.SaveSettings(dataDir, staterepo.Settings{Remote: remote}); err != nil {
		t.Fatal(err)
	}

	// A first daemon, long enough to clone the repository, file a task
	// and push it -- so this host has a working tree it has loaded.
	uiAddr := freeTCPAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	seedTestAccessCode(t, dataDir)
	go func() {
		runErr <- daemon.Run(ctx, daemon.Config{
			DataDir: dataDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: time.Hour,
			GithubHost: "127.0.0.1:0", GithubInsecureHTTP: true, UiAddr: uiAddr, Actor: "tester",
		})
	}()
	client := accessCodeClient("http://" + uiAddr)
	waitForUI(t, ctx, client)
	created, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "Filed before the remote stopped answering", Repo: "owner/payments-api",
	})
	if err != nil {
		t.Fatalf("filing a task: %v", err)
	}
	if code, body := post(t, "http://"+uiAddr+"/api/state-repo/sync"); code != http.StatusOK {
		t.Fatalf("sync returned %d: %s", code, body)
	}
	cancel()
	<-runErr

	// The remote goes away, and the deployment is restarted into it.
	gone := remote + ".gone"
	if err := os.Rename(remote, gone); err != nil {
		t.Fatalf("taking the remote away: %v", err)
	}
	secondAddr := freeTCPAddr(t)
	ctx2, cancel2 := context.WithCancel(context.Background())
	runErr2 := make(chan error, 1)
	seedTestAccessCode(t, dataDir)
	go func() {
		runErr2 <- daemon.Run(ctx2, daemon.Config{
			DataDir: dataDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: time.Hour,
			GithubHost: "127.0.0.1:0", GithubInsecureHTTP: true, UiAddr: secondAddr, Actor: "tester",
		})
	}()
	defer func() {
		cancel2()
		<-runErr2
	}()
	second := accessCodeClient("http://" + secondAddr)
	// The UI answering at all is most of what is being tested: this is
	// the process that used to exit before it ever bound the port.
	waitForUI(t, ctx2, second)
	if _, err := second.Task(ctx2, created.ID); err != nil {
		t.Fatalf("the deployment came up without the state it had: %v", err)
	}
	// And it says so where an operator looks, from the first paint rather
	// than after a tick of the sync loop: the pane carries the reason the
	// load could not finish.
	code, body := get(t, "http://"+secondAddr+"/api/state-repo")
	if code != http.StatusOK {
		t.Fatalf("status returned %d: %s", code, body)
	}
	var status ui.StateRepoStatus
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatalf("decoding the status: %v: %s", err, body)
	}
	if status.Error == "" {
		t.Fatalf("the pane reports nothing wrong with a deployment out of touch with its remote: %+v", status)
	}
	if !strings.Contains(status.Error, "could not be reached") {
		t.Fatalf("the pane does not say the remote is unreachable: %q", status.Error)
	}

	// The remote comes back, and the deployment catches up on demand --
	// nothing had to be restarted for the outage to end.
	if err := os.Rename(gone, remote); err != nil {
		t.Fatalf("bringing the remote back: %v", err)
	}
	if _, err := second.CreateTask(ctx2, ui.CreateTaskRequest{
		Title: "Filed while the remote was away", Repo: "owner/payments-api",
	}); err != nil {
		t.Fatalf("filing a task: %v", err)
	}
	if code, body := post(t, "http://"+secondAddr+"/api/state-repo/sync"); code != http.StatusOK {
		t.Fatalf("sync returned %d: %s", code, body)
	}
	out, err := exec.Command("git", "--git-dir", remote, "show", "main:"+staterepo.TablesDir+"/task.json").CombinedOutput()
	if err != nil {
		t.Fatalf("reading the dump out of the remote: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "Filed while the remote was away") {
		t.Fatalf("what was written during the outage never reached the remote:\n%s", out)
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func waitForUI(t *testing.T, ctx context.Context, client *ui.HTTPClient) {
	t.Helper()
	deadline := time.Now().Add(daemonWaitLimit)
	var lastErr error
	for time.Now().Before(deadline) {
		if _, err := client.ListTasks(ctx); err == nil {
			return
		} else {
			lastErr = err
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the UI/API server never came up: %v", lastErr)
}

func post(t *testing.T, url string) (int, string) {
	t.Helper()
	return postBody(t, url, "{}")
}

func postBody(t *testing.T, url, body string) (int, string) {
	t.Helper()
	resp, err := accessPost(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	// Read to the end rather than into a fixed buffer: a caller here
	// decodes the body as JSON, and a truncated one fails in a way that
	// says nothing about what is being tested.
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := accessGet(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestDaemonExternalConfigRepoPullsAndRefusesSettingsEditsViaAPI(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets", "github"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedTestConnections(t, dataDir, "git:\n  - credential: anonymous\n")

	// Prepare an external bare repository with an unrelated git history and its own template.
	remote := initBareRepo(t, filepath.Join(t.TempDir(), "external-state.git"))
	extSourceDir := t.TempDir()
	extStore, extDB, err := daemon.OpenStore("sqlite", "", extSourceDir)
	if err != nil {
		t.Fatalf("opening ext store: %v", err)
	}
	defer extDB.Close()
	if err := extStore.PutTemplate(context.Background(), model.Template{
		ID:    "tmpl-ext",
		Name:  "external-iac",
		Title: "External IaC managed template",
	}); err != nil {
		t.Fatal(err)
	}
	extWork := filepath.Join(t.TempDir(), "ext-work")
	gitIn(t, t.TempDir(), "clone", "--quiet", remote, extWork)
	if err := staterepo.Export(context.Background(), extDB, extWork); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.WriteSchemaVersion(extWork, model.SchemaVersion); err != nil {
		t.Fatal(err)
	}
	gitIn(t, extWork, "add", "-A")
	gitIn(t, extWork, "-c", "user.email=ops@grain", "-c", "user.name=ops", "commit", "-m", "Initial external IaC config")
	gitIn(t, extWork, "push", "--quiet", "-u", "origin", "main")

	uiAddr := freeTCPAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	defer func() {
		cancel()
		<-runErr
	}()
	seedTestAccessCode(t, dataDir)
	go func() {
		runErr <- daemon.Run(ctx, daemon.Config{
			DataDir: dataDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: time.Hour,
			GithubHost: "127.0.0.1:0", GithubInsecureHTTP: true,
			UiAddr: uiAddr, Actor: "tester",
		})
	}()

	client := accessCodeClient("http://" + uiAddr)
	waitForUI(t, ctx, client)

	// In Internal mode, a template is created through local grain/config:
	// the API refuses the write with the edit, which merges there.
	seedThroughConfigRepo(t, uiAddr, dataDir, "/api/templates", `{"name":"internal-tmpl","title":"Internal local sweep"}`)
	if code, body := post(t, "http://"+uiAddr+"/api/state-repo/sync"); code != http.StatusOK {
		t.Fatalf("sync in internal mode returned %d: %s", code, body)
	}

	// Switch to External config repository mode via POST /api/state-repo/remote.
	reqBody, _ := json.Marshal(map[string]any{"remote": remote, "branch": "main"})
	code, body := postBody(t, "http://"+uiAddr+"/api/state-repo/remote", string(reqBody))
	if code != http.StatusOK {
		t.Fatalf("POST /api/state-repo/remote returned %d: %s", code, body)
	}
	var status ui.StateRepoStatus
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatalf("decoding status: %v: %s", err, body)
	}
	if status.Mode != "external" || status.Remote != remote {
		t.Fatalf("status = %+v, want Mode=external Remote=%q", status, remote)
	}

	// Verify the external repository's template was pulled and applied, replacing the internal one.
	code, body = get(t, "http://"+uiAddr+"/api/templates")
	if code != http.StatusOK {
		t.Fatalf("GET /api/templates returned %d: %s", code, body)
	}
	if !strings.Contains(body, "External IaC managed template") || strings.Contains(body, "Internal local sweep") {
		t.Fatalf("expected external template in DB after switching to external mode, got:\n%s", body)
	}

	// Verify the external remote never had the internal template pushed to it.
	out, err := exec.Command("git", "--git-dir", remote, "show", "main:"+staterepo.DumpPath("template")).CombinedOutput()
	if err != nil {
		t.Fatalf("reading template dump from remote: %v: %s", err, out)
	}
	if strings.Contains(string(out), "Internal local sweep") {
		t.Fatalf("external remote received local internal commit:\n%s", out)
	}

	// Verify POST /api/settings and POST /api/templates are refused in External mode.
	code, body = postBody(t, "http://"+uiAddr+"/api/settings", `{"environmentName":"prod"}`)
	if code != http.StatusConflict {
		t.Fatalf("POST /api/settings in external mode returned %d (want 409): %s", code, body)
	}
	if !strings.Contains(body, "read-only") || !strings.Contains(body, remote) {
		t.Fatalf("expected read-only error naming external repo %q, got: %s", remote, body)
	}
	code, body = postBody(t, "http://"+uiAddr+"/api/templates", `{"name":"blocked","title":"Should be refused"}`)
	if code != http.StatusConflict {
		t.Fatalf("POST /api/templates in external mode returned %d (want 409): %s", code, body)
	}

	// Switch back to Internal mode via {"none": true}.
	code, body = postBody(t, "http://"+uiAddr+"/api/state-repo/remote", `{"none":true}`)
	if code != http.StatusOK {
		t.Fatalf("switching back to internal mode returned %d: %s", code, body)
	}
	var clearedStatus ui.StateRepoStatus
	if err := json.Unmarshal([]byte(body), &clearedStatus); err != nil {
		t.Fatalf("decoding status: %v: %s", err, body)
	}
	if clearedStatus.Mode != "internal" || clearedStatus.Remote != "" {
		t.Fatalf("expected internal mode with empty Remote after clearing, got %+v", clearedStatus)
	}

	// Back in Internal mode, POST /api/settings writes a bootstrap field
	// directly, and sends any other to local grain/config.
	code, body = postBody(t, "http://"+uiAddr+"/api/settings", `{"githubHost":"github.com","maxWorkers":2}`)
	if code != http.StatusOK {
		t.Fatalf("POST /api/settings (bootstrap) after switching back to internal mode returned %d: %s", code, body)
	}
	code, body = postBody(t, "http://"+uiAddr+"/api/settings", `{"environmentName":"staging"}`)
	if code != http.StatusConflict || !strings.Contains(body, `"repo":"grain/config"`) {
		t.Fatalf("POST /api/settings (non-bootstrap) after switching back to internal mode returned %d: %s", code, body)
	}
}

// seedThroughConfigRepo makes a configuration write the way internal
// mode takes one: the API refuses it with the edit, the edit is merged
// into local grain/config's main, and a sync applies it.
func seedThroughConfigRepo(t *testing.T, uiAddr, dataDir, route, reqBody string) {
	t.Helper()
	code, body := postBody(t, "http://"+uiAddr+route, reqBody)
	var refusal struct {
		Code   string                 `json:"code"`
		Change []staterepo.FileChange `json:"change"`
	}
	if code != http.StatusConflict || json.Unmarshal([]byte(body), &refusal) != nil || len(refusal.Change) == 0 {
		t.Fatalf("POST %s in internal mode returned %d, want the refusal with its edit: %s", route, code, body)
	}
	work := daemon.StateRepoDir(dataDir)
	gitIn(t, work, "checkout", "-b", "seed-change")
	if err := staterepo.ApplyChanges(work, refusal.Change); err != nil {
		t.Fatalf("applying the refusal's edit: %v", err)
	}
	gitIn(t, work, "add", "-A")
	gitIn(t, work, "-c", "user.email=ops@grain", "-c", "user.name=ops", "commit", "-m", "Seed "+route)
	gitIn(t, work, "checkout", "main")
	gitIn(t, work, "merge", "--ff-only", "seed-change")
	gitIn(t, work, "branch", "-d", "seed-change")
	if code, body := post(t, "http://"+uiAddr+"/api/state-repo/sync"); code != http.StatusOK {
		t.Fatalf("sync returned %d: %s", code, body)
	}
}
