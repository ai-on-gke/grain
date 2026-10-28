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

// TestCLICreatesIssueAgentOpensPRAndUserMergeClosesIt is bwsalmon/agents#278's
// own scenario: file a task the way an operator actually would -- through
// the real grain CLI binary, not a seeded store row or a hand-built
// githubsim.Issue -- then drive it through a real orchestrator.RunCycle (a
// scripted agent pushing over a real local git server, standing in for the
// sandboxed agent that generates the code) until grain opens a real pull
// request, then have a second, independent github.Client submit that pull
// request by merging it for real -- both the git-level merge, over the same
// local git server, and the GitHub-level PUT .../merge call -- standing in
// for a human clicking "Merge pull request" the way the CLI itself already
// stands in for a human filing the issue. The loop closing is confirmed by
// reading the issue back through the grain CLI itself, not through the
// store, since that is the one thing every other test in this package
// already checks a different way.
//
// This is the one file in package e2e that builds a real github.Client --
// harness_test.go's own package doc comment explains why every other file
// here doesn't. A subprocess CLI has no Go values to share with the test
// process, so the only way for it to see the same GitHub state RunCycle
// mutates is a real HTTP server in front of a real Sim, the same rig
// cmd/graind/live_test.go already built for the daemon's own subprocess-style
// live test -- duplicated here (syncedSim, githubHostServer) for the same
// reason every other package in this codebase duplicates small test helpers
// rather than exporting them.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// syncedSim serializes every call into a *githubsim.Sim: the real Sim
// carries no lock of its own, fine when one goroutine drives it directly (as
// every other test in this package does), but this test drives it from an
// httptest.Server's own request-handling goroutines -- the grain CLI
// subprocess's REST calls and the "user"'s own merge call -- rather than
// only the test's own goroutine.
type syncedSim struct {
	mu  sync.Mutex
	sim *githubsim.Sim
}

func (s *syncedSim) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sim.Request(method, path, headers, body)
}

func (s *syncedSim) pullRequestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sim.PullRequests)
}

func (s *syncedSim) firstPullRequestNumber() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sim.PullRequests[0].Number
}

func (s *syncedSim) pullRequestNumber(idx int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sim.PullRequests[idx].Number
}

// githubHostServer serves one httptest.Server standing in for a
// -github-host: REST API calls (/repos/...) go to sim, everything else --
// the real git smart-HTTP traffic the scripted agent's clone/push and the
// simulated user's own merge push both use -- goes to this package's own
// gitHTTPBackend (harness_test.go), reused directly since both live in
// package e2e.
//
// sim is a github.Transport rather than the *syncedSim every caller here
// passes, so that a test wanting to see what actually arrived on the wire
// can wrap one (mcpserver_pull_request_test.go's authRecordingSim, which
// checks that a token really was sent) without a second copy of this
// handler.
func githubHostServer(t *testing.T, sim github.Transport, gitRoot string) string {
	t.Helper()
	mux := http.NewServeMux()
	api := func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		headers := map[string]string{}
		for k := range r.Header {
			headers[k] = r.Header.Get(k)
		}
		apiResp, err := sim.Request(r.Method, r.URL.RequestURI(), headers, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for k, v := range apiResp.Headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(apiResp.Status)
		w.Write(apiResp.Body)
	}
	mux.HandleFunc("/repos/", api)
	// GitHub's GraphQL endpoint, which review threads exist only on.
	mux.HandleFunc("/graphql", api)
	mux.Handle("/", gitHTTPBackend(gitRoot))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "http://")
}

// grainCLI is the built binary buildGrainCLI hands out, built once for
// the whole test binary rather than once per test.
//
// Once per test is what this was, and it was nearly free while the tests
// here ran one at a time: the first `go build` paid for compiling
// cmd/grain and everything under it, and the fifteen after it were cache
// hits worth a second each. Once the package runs in parallel those
// builds overlap, and Go's build cache does not deduplicate two
// concurrent builds of the same package -- it has no lock to wait on, so
// each of them compiles the whole tree itself and writes the same entries
// afterwards. On a cold cache that is the difference between paying for
// one build of cmd/grain and paying for as many as there are lanes, on
// the same cores every other test here is trying to use.
//
// A sync.Once, not a t.Cleanup-shaped fixture, because the thing being
// shared outlives any one test. The directory goes in TestMain's own
// cleanup instead, which is the only scope that covers all of them.
var (
	grainCLIOnce sync.Once
	grainCLIDir  string
	grainCLIPath string
	grainCLIErr  error
)

// buildGrainCLI compiles the real cmd/grain binary for the test -- the
// point of this file is proving the actual CLI a human runs at a shell files
// the issue and reads it back, not a Go call into pkg/ui that happens to
// share its code.
func buildGrainCLI(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not installed")
	}
	grainCLIOnce.Do(func() {
		grainCLIDir, grainCLIErr = os.MkdirTemp("", "grain-e2e-cli-")
		if grainCLIErr != nil {
			return
		}
		grainCLIPath = filepath.Join(grainCLIDir, "grain")
		cmd := exec.Command("go", "build", "-o", grainCLIPath, "github.com/ai-on-gke/grain/cmd/grain")
		if out, err := cmd.CombinedOutput(); err != nil {
			grainCLIErr = fmt.Errorf("building grain CLI: %v\n%s", err, out)
		}
	})
	if grainCLIErr != nil {
		t.Fatal(grainCLIErr)
	}
	return grainCLIPath
}

// runCLI runs the built grain binary and returns its stdout, failing the
// test with stderr attached on a non-zero exit. stdout is kept separate from
// stderr so -json output always parses cleanly.
func runCLI(t *testing.T, bin string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("grain %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String()
}

// runCLIStore runs the grain CLI binary against storeDir's embedded
// store, reached over HTTP rather than opened directly -- bwsalmon/agents#363
// turned the CLI into a REST client of the same pkg/ui.Server a real
// `grain daemon` embeds, so a subprocess test driving it needs something
// listening on the other end of -server. This opens the store, serves it
// exactly the way daemon.go's own startUIServer does, runs the CLI
// subprocess against that server, and tears both down again before
// returning -- reusing withStore's own "take turns" discipline, since
// embedded SQLite is what the test's own orchestrator.RunCycle calls
// (driven directly in this process, not through this server) take turns
// with.
func runCLIStore(t *testing.T, bin, storeDir string, args ...string) string {
	t.Helper()
	var out string
	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		cfg := ui.Config{Actor: ui.DefaultActor("operator"), Capabilities: ui.OfferedCapabilities()}
		srv := httptest.NewServer(ui.NewServer(withAccessCode(cfg), store))
		defer srv.Close()
		out = runCLI(t, bin, append([]string{"-server", srv.URL}, args...)...)
	})
	return out
}

// withStore opens the shared embedded SQLite store, hands it to fn, and
// closes it again.
//
// This test has two writers: the grain CLI, which runs as a real
// subprocess, and the orchestrator running in the test process --
// exactly the "daemon plus a CLI" case a real deployment has too, and
// which SQLite (pkg/model/sqlite's own doc comment) is built to let
// write to the same file at once. Opening and closing around every step
// here is not a workaround for that -- SQLite would tolerate a store
// held open across both -- it is what proves the handoff between the two
// writers actually goes through the store rather than through anything
// held in memory.
func withStore(t *testing.T, dir string, fn func(*model.Store, context.Context)) {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	defer db.Close()
	store := model.New(db)
	// Before db.Close above, deferred later so it runs first: the
	// connection a UI request's ChangeToken takes outlives a closed
	// database otherwise (Store.Close's own doc comment).
	defer store.Close()
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	fn(store, ctx)
}

// scriptedFramework turns a scripted step sequence into the factory
// orchestrator.Deps.Framework wants, one fresh antigravity.NewForTest per
// dispatch -- duplicated from pkg/orchestrator's own live_test.go helper
// of the same name for the same reason withStore is. The framework
// name a real deployment would switch on is ignored: there is one
// scripted framework here, and every task in these tests takes it.
func scriptedFramework(script []antigravity.Step) func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
	return func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
		return antigravity.NewForTest(antigravity.Steps(script...)), nil
	}
}

func TestCLICreatesTaskAgentOpensPRAndUserMergeClosesIt(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	bin := buildGrainCLI(t)

	const owner, repoName = "acme", "widgets"
	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, owner, repoName+".git"), "main")
	run(t, upstream, "git", "-C", bare, "config", "http.receivepack", "true")
	seedParent := t.TempDir()
	run(t, seedParent, "git", "clone", bare, "seed")
	seed := filepath.Join(seedParent, "seed")
	run(t, seed, "git", "config", "user.email", "seed@example.com")
	run(t, seed, "git", "config", "user.name", "seed")
	if err := os.WriteFile(filepath.Join(seed, "NOTES.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", "NOTES.md")
	run(t, seed, "git", "commit", "-q", "-m", "initial commit")
	run(t, seed, "git", "push", "origin", "main")

	sim := &syncedSim{sim: githubsim.New(owner, repoName, bare, "main")}
	githubHost := githubHostServer(t, sim, upstream)

	// Step 1: create the task the way an operator actually would, through
	// the real CLI binary -- which takes no GitHub credentials at all and
	// writes the store directly, approved immediately so it is
	// dispatchable the moment it is filed.
	storeDir := t.TempDir()
	created := runCLIStore(t, bin, storeDir,
		"-json",
		"create",
		"-title", "add a NOTES entry",
		"-body", "please add a line to NOTES.md",
		"-repo", owner+"/"+repoName,
		"-approve",
	)
	var task ui.Task
	if err := json.Unmarshal([]byte(created), &task); err != nil {
		t.Fatalf("parsing grain create -json output: %v\n%s", err, created)
	}
	if task.ID == "" {
		t.Fatalf("grain create did not return a task id: %s", created)
	}
	if task.State != model.StateQueued {
		t.Fatalf("state after create = %q, want queued", task.State)
	}

	// Step 2: the agent generates the code and grain opens the PR. The
	// orchestrator reads the very task the CLI just wrote -- the two share
	// the store, where before they shared only a GitHub issue and each
	// kept its own store, which is exactly the split the inversion closed.
	remote := "http://" + githubHost + "/" + owner + "/" + repoName + ".git"
	sandboxes := credentialed(t, remote)

	branch := model.BranchName(task.ID)
	client := github.NewClient(sim, nil)
	deps := orchestrator.Deps{
		Client: client, Sandboxes: sandboxes, MaxWorkers: 1,
		Framework: scriptedFramework(pushScript(remote, branch, task.ID)),
	}

	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		deps.Store = store
		if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
			t.Fatalf("RunCycle (dispatch): %v", err)
		}
		st, err := store.State(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if st != model.StateAwaitingSubmit {
			t.Fatalf("state after the agent's push = %q, want awaiting_submit: the pull request is open and unsubmitted", st)
		}
	})
	if sim.pullRequestCount() != 1 {
		t.Fatalf("expected grain to have opened one pull request, got %d", sim.pullRequestCount())
	}
	prNumber := sim.firstPullRequestNumber()

	// Step 3: the user submits the PR -- a real git merge over the same
	// local git server the agent pushed to, plus a real HTTP PUT to the
	// merge endpoint, driven by a second, independent github.Client
	// standing in for a human clicking "Merge pull request" on GitHub.
	mergeParent := t.TempDir()
	run(t, mergeParent, "git", "clone", remote, "merge")
	mergeWd := filepath.Join(mergeParent, "merge")
	run(t, mergeWd, "git", "config", "user.email", "github@example.com")
	run(t, mergeWd, "git", "config", "user.name", "github (simulated merge)")
	run(t, mergeWd, "git", "fetch", "origin", branch)
	run(t, mergeWd, "git", "checkout", "main")
	run(t, mergeWd, "git", "merge", "--no-ff", "origin/"+branch, "-m", "Merge "+branch)
	run(t, mergeWd, "git", "push", "origin", "main")

	userTransport := github.NewRealTransport(githubHost)
	userTransport.UseTLS = false
	userClient := github.NewClient(userTransport, nil)
	if err := userClient.MergePullRequest(owner, repoName, prNumber, ""); err != nil {
		t.Fatalf("submitting (merging) the pull request: %v", err)
	}

	// Step 4: another cycle syncs the now-merged PR and closes the task
	// out -- SyncPullRequests' own job, the same second RunCycle
	// pkg/orchestrator/live_test.go's TestRunCycleCompletesEndToEnd makes.
	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		deps.Store = store
		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Minute)); err != nil {
			t.Fatalf("RunCycle (sync): %v", err)
		}
		st, err := store.State(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if st != model.StateClosed {
			t.Fatalf("state after the merge = %q, want closed", st)
		}
	})

	// The only thing this whole loop put on GitHub is the pull request.
	if len(sim.sim.Issues) != 0 {
		t.Fatalf("expected no GitHub issues at all, got %+v", sim.sim.Issues)
	}

	// Step 5: confirm the loop closed the way an operator would actually
	// see it -- by asking the CLI itself, in a fresh subprocess, not by
	// reading the store from in here.
	got := runCLIStore(t, bin, storeDir,
		"-json",
		"get", task.ID,
	)
	var detail ui.TaskDetail
	if err := json.Unmarshal([]byte(got), &detail); err != nil {
		t.Fatalf("parsing grain get -json output: %v\n%s", err, got)
	}
	if detail.State != model.StateClosed {
		t.Fatalf("task %s state = %q as the CLI reports it, want closed", task.ID, detail.State)
	}
}
