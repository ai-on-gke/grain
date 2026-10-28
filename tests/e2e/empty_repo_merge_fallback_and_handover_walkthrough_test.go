// End-to-end walkthrough and regression suite for §7 of docs/test-plans/2026-10-22.md:
// Empty-Repository First Push Default Branch Initialization, MergePullRequest
// Squash/Rebase Fallback, Handover Window Reset, and Package Registry Seams
// (commits 802ef0f40 / 6304429ab, 768dc4354 / 12e400c27, afe4b7fc6 / 9e575d1bb,
// f80f0f968 / 2214a74e8, abe8837c7 / c27f8cddb, 495d9c53a / 33c23efb9,
// ca841ce99 / 0a9cb9483, bfe65cca7, b15cf0a22, b5a556acd / 1cc933f49,
// e7c4a6dfc / 5961826e7).
package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/agent/sessionstore"
	"github.com/ai-on-gke/grain/pkg/capability/packagepublish"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type walkthroughAgentFunc func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error)

func (f walkthroughAgentFunc) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	return f(ctx, cfg)
}

// walkthroughSandboxes wraps HostSandboxes to configure git identity on each
// acquired sandbox directory so `git commit` works in hermetic test sandboxes.
type walkthroughSandboxes struct {
	inner orchestrator.Sandboxes
	t     *testing.T
}

func (s walkthroughSandboxes) Acquire(ctx context.Context, name string, shape orchestrator.Shape) (orchestrator.Sandbox, error) {
	sb, err := s.inner.Acquire(ctx, name, shape)
	if err != nil {
		return nil, err
	}
	if err := sb.ConfigureGitCredentials(ctx, "http://placeholder.example/x/y.git", "unused", mcp.GitIdentity{}); err != nil {
		s.t.Fatalf("configuring git credentials on %s: %v", name, err)
	}
	return sb, nil
}

func openWalkthroughStore(t *testing.T) (*model.Store, context.Context) {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("initializing store schema: %v", err)
	}
	return store, ctx
}

func startWalkthroughRun(t *testing.T, ctx context.Context, store *model.Store, d dispatch.Dispatch, at time.Time) {
	t.Helper()
	if err := store.StartRun(ctx, model.Run{
		ID: d.RunID, TaskID: d.TaskID, Sandbox: d.RunID, Attempt: d.Attempt, StartedAt: at,
	}, model.Limits{}); err != nil {
		t.Fatalf("StartRun(%s): %v", d.RunID, err)
	}
}

func walkthroughRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}
	return root
}

// 1. Empty repository bootstrap to normal PR flow (802ef0f40, 768dc4354)
func TestWalkthroughEmptyRepoBootstrapToNormalPRFlow(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	t.Run("open_pull_request_mid_run_initializes_main_and_closes_task", func(t *testing.T) {
		t.Parallel()
		if _, err := exec.LookPath("go"); err != nil {
			t.Skip("go toolchain not installed")
		}
		bin := buildGrainBinary(t)

		store, ctx := openWalkthroughStore(t)
		const owner, repoName = "acme", "empty-midrun"
		repo := model.RepoRef{Owner: owner, Name: repoName}
		bare := filepath.Join(t.TempDir(), repoName+".git")
		sim, err := githubsim.CreateEmptyRepository(owner, repoName, bare)
		if err != nil {
			t.Fatalf("CreateEmptyRepository: %v", err)
		}
		sim.PullRequestBase = githubsim.UniquePullRequestBase()
		wire := &syncedSim{sim: sim}
		ghClient := github.NewClient(wire, nil)

		if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CloneURL: bare}); err != nil {
			t.Fatalf("PutRepoConfig: %v", err)
		}

		actor := human("alice")
		task1 := model.Task{
			ID:        "1",
			Intent:    model.IntentImplement,
			Title:     "Bootstrap empty repository",
			Body:      "Create initial README.md",
			Origin:    model.Origin{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonDirect},
			Approval:  &model.Attribution{Actor: actor},
			Target:    &repo,
			Binding:   model.BindingDirective,
			AutoMerge: true,
		}
		if err := store.PutTask(ctx, task1); err != nil {
			t.Fatalf("PutTask: %v", err)
		}

		daemonURL := newTestServer(t, ui.NewServer(withAccessCode(ui.Config{
			Actor:        ui.DefaultActor("alice"),
			Capabilities: ui.OfferedCapabilities(),
			PullRequests: storeBackedOpener{store: store, client: ghClient},
		}), store))

		d1 := dispatch.Dispatch{TaskID: "1", RunID: "run-1", Attempt: 1}
		startWalkthroughRun(t, ctx, store, d1, baseTime)
		sandboxRoot := t.TempDir()
		if err := mcp.ConfigureGitCredentials(sandboxRoot, "http://placeholder.example/x/y.git", "unused", mcp.GitIdentity{}); err != nil {
			t.Fatalf("ConfigureGitCredentials: %v", err)
		}

		var openPRText string
		var pushedSHA string
		fw := walkthroughAgentFunc(func(runCtx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
			tools := mcp.NewSandboxTools(cfg.SandboxRoot)
			runCmd := func(command string) string {
				res := tools[0].Handler(runCtx, map[string]any{"command": command})
				if res.IsError {
					t.Fatalf("sandbox command %q failed:\n%s", command, res.Text)
				}
				return res.Text
			}
			// Verify prepareCheckout placed work on unborn branch grain/task-1.
			branchOut := runCmd("cd work && git branch --show-current")
			if !strings.Contains(branchOut, "stdout:\ngrain/task-1\n") {
				t.Fatalf("unborn branch = %q, want stdout grain/task-1", branchOut)
			}
			// Commit README.md and push origin HEAD.
			runCmd("cd work && printf '# Empty Repo Bootstrap\\n' > README.md && git add README.md && git commit -q -m 'Initial commit: add README' && git push -u origin HEAD")
			shaOut := runCmd("cd work && git rev-parse HEAD")
			for _, line := range strings.Split(shaOut, "\n") {
				line = strings.TrimSpace(line)
				if len(line) == 40 {
					pushedSHA = line
					break
				}
			}
			if pushedSHA == "" {
				t.Fatalf("failed to parse 40-char SHA from %q", shaOut)
			}

			// Call open_pull_request over real stdio MCP against the daemon.
			mcpProc := startMCPServer(t, bin, "-sandbox-root", cfg.SandboxRoot, "-server", daemonURL, "-task", "1")
			res, callErr := mcpProc.CallTool(runCtx, "open_pull_request", map[string]any{})
			if callErr != nil {
				t.Fatalf("open_pull_request MCP call: %v\nstderr:\n%s", callErr, mcpProc.stderr.String())
			}
			if res.IsError {
				t.Fatalf("open_pull_request returned error: %s", res.Text())
			}
			openPRText = res.Text()
			return &agent.Result{
				FinalText: "Initialized empty repository with README.md",
				ToolCalls: []agent.ToolCall{{Name: "run_command", Text: "pushed"}, {Name: "open_pull_request", Text: openPRText}},
			}, nil
		})

		res, err := orchestrator.RunDispatch(ctx, store, fw, orchestrator.Config{}, task1, d1, mcp.NewSandboxTools(sandboxRoot), sandboxRoot, "", nil, baseTime)
		if err != nil {
			t.Fatalf("RunDispatch: %v", err)
		}
		if err := orchestrator.ProcessResult(ctx, store, ghClient, task1, res, d1.RunID, baseTime.Add(time.Second)); err != nil {
			t.Fatalf("ProcessResult: %v", err)
		}

		wantOpenPRNote := fmt.Sprintf("Initialized main on %s at %s and marked task 1 done.", repo, pushedSHA[:12])
		if openPRText != wantOpenPRNote {
			t.Errorf("open_pull_request response = %q, want %q", openPRText, wantOpenPRNote)
		}
		if strings.Contains(openPRText, "#0") || strings.Contains(openPRText, "call this tool again") {
			t.Errorf("open_pull_request response must not claim PR #0 or ask to poll checks: %q", openPRText)
		}

		// Verify 0 pull requests were opened and default_branch is now "main" at pushedSHA.
		if prs := wire.openPullRequests(); len(prs) != 0 {
			t.Errorf("open pull requests = %+v, want 0", prs)
		}
		defaultBranch, err := ghClient.DefaultBranch(owner, repoName)
		if err != nil {
			t.Fatalf("DefaultBranch: %v", err)
		}
		if defaultBranch != "main" {
			t.Errorf("DefaultBranch = %q, want main", defaultBranch)
		}
		mainHead, err := ghClient.GetBranchHead(owner, repoName, "main")
		if err != nil || mainHead == nil {
			t.Fatalf("GetBranchHead(main): %+v, %v", mainHead, err)
		}
		if mainHead.SHA != pushedSHA {
			t.Errorf("main SHA = %q, want %q", mainHead.SHA, pushedSHA)
		}

		st, err := store.State(ctx, "1")
		if err != nil {
			t.Fatalf("store.State(1): %v", err)
		}
		if st != model.StateClosed {
			t.Errorf("task 1 state = %q, want closed", st)
		}
		obs, err := store.GetObservation(ctx, "1")
		if err != nil || obs == nil || obs.CompletedAt == nil || obs.ClosedAt == nil {
			t.Fatalf("task 1 observation = %+v (%v), want CompletedAt and ClosedAt recorded", obs, err)
		}
		comments, err := store.Comments(ctx, "1")
		if err != nil || len(comments) == 0 {
			t.Fatalf("task 1 comments = %+v (%v), want closing note", comments, err)
		}
		wantClosingComment := fmt.Sprintf(
			"Branch `grain/task-1` was the first branch pushed to empty repository %s, so grain initialized `main` at `%s` and set it as the repository's default branch rather than opening a pull request against itself.\n\ngrain has marked this task done as landed on `main`.",
			repo, pushedSHA[:12],
		)
		if comments[len(comments)-1].Body != wantClosingComment {
			t.Errorf("closing comment = %q, want %q", comments[len(comments)-1].Body, wantClosingComment)
		}
	})

	t.Run("finish_initializes_main_then_task_2_opens_pr_1_and_merges", func(t *testing.T) {
		t.Parallel()
		store, ctx := openWalkthroughStore(t)
		const owner, repoName = "acme", "empty-bootstrap"
		repo := model.RepoRef{Owner: owner, Name: repoName}
		bare := filepath.Join(t.TempDir(), repoName+".git")
		sim, err := githubsim.CreateEmptyRepository(owner, repoName, bare)
		if err != nil {
			t.Fatalf("CreateEmptyRepository: %v", err)
		}
		// Start pull request numbering at 1 so Task 2 opens PR #1 against main.
		sim.PullRequestBase = 1
		ghClient := github.NewClient(sim, nil)

		if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CloneURL: bare}); err != nil {
			t.Fatalf("PutRepoConfig: %v", err)
		}

		actor := human("alice")
		task1 := model.Task{
			ID:        "eb-1",
			Intent:    model.IntentImplement,
			Title:     "Bootstrap empty repo with README.md",
			Body:      "Commit README.md and push origin HEAD.",
			Origin:    model.Origin{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonDirect},
			Approval:  &model.Attribution{Actor: actor},
			Target:    &repo,
			Binding:   model.BindingDirective,
			AutoMerge: true,
		}
		if err := store.PutTask(ctx, task1); err != nil {
			t.Fatalf("PutTask(eb-1): %v", err)
		}

		clock := baseTime
		sandboxes := walkthroughSandboxes{inner: orchestrator.NewHostSandboxes(t.TempDir()), t: t}
		task1Script := []antigravity.Step{
			toolCall("run_command", map[string]any{
				"command": "cd work && printf '# Empty Repository\\nBootstrapped by task eb-1.\\n' > README.md && " +
					"git add README.md && git commit -q -m 'Initial commit: add README.md' && git push -u origin HEAD",
			}),
			finalText("Committed README.md and pushed initial branch."),
		}
		deps := orchestrator.Deps{
			Store:      store,
			Client:     ghClient,
			Sandboxes:  sandboxes,
			MaxWorkers: 1,
			Framework: func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
				return antigravity.NewForTest(antigravity.Steps(task1Script...)), nil
			},
		}

		if err := orchestrator.RunCycle(ctx, deps, clock); err != nil {
			t.Fatalf("RunCycle (task eb-1): %v", err)
		}

		// Verify task eb-1 closed as completed without opening a 0-commit PR.
		st1, err := store.State(ctx, "eb-1")
		if err != nil {
			t.Fatalf("store.State(eb-1): %v", err)
		}
		if st1 != model.StateClosed {
			t.Fatalf("task eb-1 state = %q, want closed", st1)
		}
		if len(sim.PullRequests) != 0 {
			t.Fatalf("sim.PullRequests after task eb-1 = %+v, want 0 PRs", sim.PullRequests)
		}
		defaultBranch, err := ghClient.DefaultBranch(owner, repoName)
		if err != nil {
			t.Fatalf("DefaultBranch after task eb-1: %v", err)
		}
		if defaultBranch != "main" {
			t.Fatalf("DefaultBranch = %q, want main", defaultBranch)
		}
		task1Head, err := ghClient.GetBranchHead(owner, repoName, "main")
		if err != nil || task1Head == nil || task1Head.SHA == "" {
			t.Fatalf("GetBranchHead(main) = %+v, %v", task1Head, err)
		}
		comments1, err := store.Comments(ctx, "eb-1")
		if err != nil || len(comments1) != 1 {
			t.Fatalf("task eb-1 comments = %+v (%v), want 1 closing note", comments1, err)
		}
		if !strings.Contains(comments1[0].Body, fmt.Sprintf("initialized `main` at `%s`", task1Head.SHA[:12])) {
			t.Errorf("task eb-1 comment = %q, want mention of initialized main at %s", comments1[0].Body, task1Head.SHA[:12])
		}

		// Now dispatch task eb-2 against the same repository: verify it clones main,
		// commits a second file, pushes its branch, opens PR #1 against main, and merges cleanly.
		task2 := model.Task{
			ID:        "eb-2",
			Intent:    model.IntentImplement,
			Title:     "Add CONTRIBUTING.md on top of main",
			Body:      "Add CONTRIBUTING.md and open PR #1 against main.",
			Origin:    model.Origin{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonDirect},
			Approval:  &model.Attribution{Actor: actor},
			Target:    &repo,
			Binding:   model.BindingDirective,
			AutoMerge: true,
		}
		if err := store.PutTask(ctx, task2); err != nil {
			t.Fatalf("PutTask(eb-2): %v", err)
		}

		task2Script := []antigravity.Step{
			toolCall("run_command", map[string]any{
				"command": "cd work && test -f README.md && " +
					"printf '# Contributing\\nFollow conventional commits.\\n' > CONTRIBUTING.md && " +
					"git add CONTRIBUTING.md && git commit -q -m 'docs: add CONTRIBUTING.md\n\nAdds contribution guidelines.' && " +
					"git push -u origin grain/task-eb-2",
			}),
			finalText("Added CONTRIBUTING.md and pushed grain/task-eb-2."),
		}
		deps.Framework = func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
			return antigravity.NewForTest(antigravity.Steps(task2Script...)), nil
		}

		clock = clock.Add(time.Minute)
		if err := orchestrator.RunCycle(ctx, deps, clock); err != nil {
			t.Fatalf("RunCycle (task eb-2 dispatch): %v", err)
		}

		if len(sim.PullRequests) != 1 {
			t.Fatalf("sim.PullRequests after task eb-2 push = %+v, want 1 PR", sim.PullRequests)
		}
		pr1 := sim.PullRequests[0]
		if pr1.Number != 1 || pr1.Base != "main" || pr1.Head != "grain/task-eb-2" {
			t.Fatalf("opened PR = %+v, want #1 base=main head=grain/task-eb-2", pr1)
		}

		// Mark PR #1 mergeable with passing CI check so the merge queue lands it into main.
		clean := true
		for i := range sim.PullRequests {
			sim.PullRequests[i].Mergeable = &clean
		}
		success := "success"
		sim.CheckRuns["grain/task-eb-2"] = []github.CheckRun{
			{Name: "ci", Status: "completed", Conclusion: &success},
		}

		// Run a second cycle to let the merge queue land PR #1 into main and close task eb-2.
		clock = clock.Add(5 * time.Minute)
		if err := orchestrator.RunCycle(ctx, deps, clock); err != nil {
			t.Fatalf("RunCycle (task eb-2 merge): %v", err)
		}

		st2, err := store.State(ctx, "eb-2")
		if err != nil {
			t.Fatalf("store.State(eb-2): %v", err)
		}
		if st2 != model.StateClosed {
			t.Fatalf("task eb-2 state after merge = %q, want closed", st2)
		}
		runs2, err := store.Runs(ctx, "eb-2")
		if err != nil || len(runs2) != 1 || runs2[0].Outcome != "succeeded" {
			t.Fatalf("task eb-2 runs = %+v (%v), want 1 succeeded run", runs2, err)
		}

		// Verify both README.md and CONTRIBUTING.md are present on main in the bare repo.
		for _, wantFile := range []string{"README.md", "CONTRIBUTING.md"} {
			cmd := exec.Command("git", "--git-dir", bare, "cat-file", "-e", "refs/heads/main:"+wantFile)
			if err := cmd.Run(); err != nil {
				t.Errorf("file %s missing on main after PR #1 merge: %v", wantFile, err)
			}
		}
	})
}

// mergeMethodPolicyTransport wraps a github.Transport (such as githubsim.Sim)
// to enforce repository merge-method rules on PUT /repos/{owner}/{repo}/pulls/{number}/merge,
// recording every merge_method attempted by MergePullRequestWithMessage.
type mergeMethodPolicyTransport struct {
	inner          github.Transport
	disallowMerge  bool
	disallowSquash bool

	mu       sync.Mutex
	attempts []string
}

func (m *mergeMethodPolicyTransport) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	if method == "PUT" && strings.HasSuffix(path, "/merge") && strings.Contains(path, "/pulls/") {
		var payload struct {
			MergeMethod string `json:"merge_method"`
		}
		_ = json.Unmarshal(body, &payload)

		m.mu.Lock()
		m.attempts = append(m.attempts, payload.MergeMethod)
		m.mu.Unlock()

		if (payload.MergeMethod == "" || payload.MergeMethod == "merge") && m.disallowMerge {
			return github.ApiResponse{
				Status: 405,
				Body:   []byte(`{"message":"Merge commits are not allowed on this repository","documentation_url":"https://docs.github.com/rest/pulls/pulls#merge-a-pull-request"}`),
			}, nil
		}
		if payload.MergeMethod == "squash" && m.disallowSquash {
			return github.ApiResponse{
				Status: 405,
				Body:   []byte(`{"message":"Squash commits are not allowed on this repository","documentation_url":"https://docs.github.com/rest/pulls/pulls#merge-a-pull-request"}`),
			}, nil
		}
	}
	return m.inner.Request(method, path, headers, body)
}

func (m *mergeMethodPolicyTransport) recordedAttempts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.attempts)
}

// 2. Merge queue / orchestrator squash and rebase fallback (afe4b7fc6)
func TestWalkthroughMergePullRequestSquashAndRebaseFallback(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	cases := []struct {
		name           string
		disallowMerge  bool
		disallowSquash bool
		wantAttempts   []string
	}{
		{
			name:           "fallback_to_squash_when_merge_commits_disallowed",
			disallowMerge:  true,
			disallowSquash: false,
			wantAttempts:   []string{"", "squash"},
		},
		{
			name:           "fallback_to_rebase_when_merge_and_squash_disallowed",
			disallowMerge:  true,
			disallowSquash: true,
			wantAttempts:   []string{"", "squash", "rebase"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, ctx := openWalkthroughStore(t)
			const owner, repoName = "acme", "widgets"
			repo := model.RepoRef{Owner: owner, Name: repoName}

			upstream := t.TempDir()
			bare, _ := mcpPullRequestRepo(t, upstream, owner, repoName, "seed-branch", "seed commit")
			sim := githubsim.New(owner, repoName, bare, "main")
			sim.PullRequestBase = 1

			transport := &mergeMethodPolicyTransport{
				inner:          sim,
				disallowMerge:  tc.disallowMerge,
				disallowSquash: tc.disallowSquash,
			}
			ghClient := github.NewClient(transport, nil)

			if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CloneURL: bare}); err != nil {
				t.Fatalf("PutRepoConfig: %v", err)
			}

			actor := human("alice")
			parentTask := model.Task{
				ID:         "17",
				Intent:     model.IntentAnalyze,
				Title:      "Parent planning task",
				Body:       "File implementation task.",
				Origin:     model.Origin{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonDirect},
				Approval:   &model.Attribution{Actor: actor},
				Target:     &repo,
				Binding:    model.BindingDirective,
				WorkOrigin: model.GitHubOrigin(repo, 99),
			}
			if err := store.PutTask(ctx, parentTask); err != nil {
				t.Fatalf("PutTask(17): %v", err)
			}
			if err := store.ObserveField(ctx, "17", baseTime, func(o *model.Observation) {
				o.ClosedAt = &baseTime
			}); err != nil {
				t.Fatalf("ObserveField(17): %v", err)
			}
			proposer := model.Principal{Kind: model.PrincipalAgent, ID: "17"}
			task := model.Task{
				ID:     "42",
				Intent: model.IntentImplement,
				Title:  "Add fallback feature",
				Body:   "Verify MergePullRequestWithMessage squash/rebase fallback.",
				Origin: model.Origin{
					Attribution: model.Attribution{
						Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
						OnBehalfOf: &proposer,
					},
					Reason: model.ReasonProposal,
				},
				Approval:  &model.Attribution{Actor: actor},
				Target:    &repo,
				Binding:   model.BindingDirective,
				AutoMerge: true,
				Links:     []model.Link{{Kind: model.LinkProposedBy, Target: "17"}},
			}
			if err := store.PutTask(ctx, task); err != nil {
				t.Fatalf("PutTask: %v", err)
			}

			sandboxes := walkthroughSandboxes{inner: orchestrator.NewHostSandboxes(t.TempDir()), t: t}
			script := []antigravity.Step{
				toolCall("run_command", map[string]any{
					"command": "cd work && printf 'feature content\\n' > FEATURE.txt && " +
						"git add FEATURE.txt && " +
						"git commit -q -m 'feat: add FEATURE.txt\n\nAdds the requested feature.' && " +
						"git push -u origin grain/task-42",
				}),
				finalText("Pushed grain/task-42."),
			}
			deps := orchestrator.Deps{
				Store:      store,
				Client:     ghClient,
				Sandboxes:  sandboxes,
				MaxWorkers: 1,
				Framework: func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
					return antigravity.NewForTest(antigravity.Steps(script...)), nil
				},
			}

			clock := baseTime
			// First cycle dispatches task 42 and opens PR #1.
			if err := orchestrator.RunCycle(ctx, deps, clock); err != nil {
				t.Fatalf("RunCycle (dispatch): %v", err)
			}
			if len(sim.PullRequests) != 1 || sim.PullRequests[0].Number != 1 {
				t.Fatalf("PullRequests after dispatch = %+v, want PR #1", sim.PullRequests)
			}

			// Mark PR #1 mergeable with passing check run, then run SyncMergeQueue via RunCycle.
			clean := true
			for i := range sim.PullRequests {
				sim.PullRequests[i].Mergeable = &clean
			}
			success := "success"
			sim.CheckRuns["grain/task-42"] = []github.CheckRun{
				{Name: "ci", Status: "completed", Conclusion: &success},
			}

			// Second cycle runs SyncMergeQueue, which merges PR #1 via MergePullRequestWithMessage.
			clock = clock.Add(5 * time.Minute)
			if err := orchestrator.RunCycle(ctx, deps, clock); err != nil {
				t.Fatalf("RunCycle (merge): %v", err)
			}

			gotAttempts := transport.recordedAttempts()
			if !slices.Equal(gotAttempts, tc.wantAttempts) {
				t.Errorf("merge_method attempts = %v, want %v", gotAttempts, tc.wantAttempts)
			}

			st, err := store.State(ctx, "42")
			if err != nil {
				t.Fatalf("store.State(42): %v", err)
			}
			if st != model.StateClosed {
				t.Fatalf("task 42 state = %q, want closed", st)
			}

			// Verify Origin: and Filed-by: trailers were preserved in the commit message on main.
			logOut, err := exec.Command("git", "--git-dir", bare, "log", "-1", "--format=%B", "refs/heads/main").Output()
			if err != nil {
				t.Fatalf("reading main commit message: %v", err)
			}
			commitMsg := string(logOut)
			for _, wantTrailer := range []string{
				"Origin: acme/widgets#99",
				"Filed-by: task 17",
				"Approved-by: alice",
			} {
				if !strings.Contains(commitMsg, wantTrailer) {
					t.Errorf("merged commit message missing trailer %q:\n%s", wantTrailer, commitMsg)
				}
			}
		})
	}
}

// 3. Multi-attempt handover window reset & suspended prompt preservation (f80f0f968, abe8837c7)
func TestWalkthroughMultiAttemptHandoverWindowResetAndSuspendedPrompt(t *testing.T) {
	t.Parallel()

	t.Run("multi_attempt_handover_window_reset", func(t *testing.T) {
		t.Parallel()
		store, ctx := openWalkthroughStore(t)

		actor := human("alice")
		task := model.Task{
			ID:       "1",
			Intent:   model.IntentAnalyze,
			Title:    "Investigate multi-attempt handover window reset",
			Body:     "Drive 3 attempts with >64 KiB pre-H1 and >64 KiB post-H1 comments.",
			Origin:   model.Origin{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonDirect},
			Approval: &model.Attribution{Actor: actor},
			Binding:  model.BindingDirective,
		}
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatalf("PutTask: %v", err)
		}

		clock := baseTime
		// Seed >64 KiB of comments prior to Attempt 1 (20 comments * 4 KiB = 80 KiB).
		chunk1 := strings.Repeat("A", 4096)
		for i := 1; i <= 20; i++ {
			clock = clock.Add(time.Second)
			if _, err := store.AddComment(ctx, model.Comment{
				TaskID:    "1",
				Author:    model.Attribution{Actor: actor},
				Body:      fmt.Sprintf("PRE-H1-COMMENT-%02d: %s", i, chunk1),
				CreatedAt: clock,
			}); err != nil {
				t.Fatalf("AddComment pre-H1 %d: %v", i, err)
			}
		}

		const h1Text = "H1-SUMMARY-SETTLED-INITIAL-DECISIONS-AND-INVARIANTS"
		const h2Text = "H2-SUMMARY-SECOND-WINDOW-REPLACES-H1-WITH-UPDATED-STATE"

		// Dispatch Attempt 1 through RunDispatch + ProcessResult:
		// Attempt 1 sees >64 KiB of comments, writes handover H1, and parks on ask_question.
		d1 := dispatch.Dispatch{TaskID: "1", RunID: "run-1", Attempt: 1}
		clock = clock.Add(time.Minute)
		startWalkthroughRun(t, ctx, store, d1, clock)
		var prompt1 string
		fw1 := walkthroughAgentFunc(func(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
			prompt1 = cfg.Prompt
			return &agent.Result{
				FinalText: "Wrote handover H1 and asked clarifying question.",
				ToolCalls: []agent.ToolCall{
					{Name: "write_handover", Arguments: map[string]any{"handover": h1Text}},
					{Name: "ask_question", Arguments: map[string]any{"question": "Attempt 1 question: proceed with phase 2?"}},
				},
			}, nil
		})
		sb1 := t.TempDir()
		res1, err := orchestrator.RunDispatch(ctx, store, fw1, orchestrator.Config{}, task, d1, mcp.NewSandboxTools(sb1), sb1, "", nil, clock)
		if err != nil {
			t.Fatalf("RunDispatch 1: %v", err)
		}
		if err := orchestrator.ProcessResult(ctx, store, nil, task, res1, d1.RunID, clock.Add(time.Second)); err != nil {
			t.Fatalf("ProcessResult 1: %v", err)
		}

		// Verify Attempt 1 prompt had PromptThreadCutNotice and initial handoverRequest.
		if !strings.Contains(prompt1, model.PromptThreadCutNotice) {
			t.Errorf("Attempt 1 prompt missing PromptThreadCutNotice")
		}
		if !strings.Contains(prompt1, "Before you finish, write this task's handover (write_handover)") {
			t.Errorf("Attempt 1 prompt missing initial handoverRequest")
		}
		if st, err := store.State(ctx, "1"); err != nil || st != model.StateAwaitingReply {
			t.Fatalf("state after Attempt 1 = %q (%v), want awaiting_reply", st, err)
		}

		// Accumulate >64 KiB more comments after H1 (20 comments * 4 KiB = 80 KiB),
		// ending with a human reply that unparks the task for Attempt 2.
		chunk2 := strings.Repeat("B", 4096)
		for i := 1; i <= 20; i++ {
			clock = clock.Add(time.Second)
			if _, err := store.AddComment(ctx, model.Comment{
				TaskID:    "1",
				Author:    model.Attribution{Actor: actor},
				Body:      fmt.Sprintf("POST-H1-COMMENT-%02d: %s", i, chunk2),
				CreatedAt: clock,
			}); err != nil {
				t.Fatalf("AddComment post-H1 %d: %v", i, err)
			}
		}

		// Dispatch Attempt 2: sees H1 carried, sees post-H1 overflow (>48 KiB tailBudget),
		// writes handover H2, and parks on ask_question.
		d2 := dispatch.Dispatch{TaskID: "1", RunID: "run-2", Attempt: 2}
		clock = clock.Add(time.Minute)
		startWalkthroughRun(t, ctx, store, d2, clock)
		var prompt2 string
		fw2 := walkthroughAgentFunc(func(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
			prompt2 = cfg.Prompt
			return &agent.Result{
				FinalText: "Wrote handover H2 and asked final confirmation question.",
				ToolCalls: []agent.ToolCall{
					{Name: "write_handover", Arguments: map[string]any{"handover": h2Text}},
					{Name: "ask_question", Arguments: map[string]any{"question": "Attempt 2 question: confirm final step?"}},
				},
			}, nil
		})
		sb2 := t.TempDir()
		res2, err := orchestrator.RunDispatch(ctx, store, fw2, orchestrator.Config{}, task, d2, mcp.NewSandboxTools(sb2), sb2, "", nil, clock)
		if err != nil {
			t.Fatalf("RunDispatch 2: %v", err)
		}
		if err := orchestrator.ProcessResult(ctx, store, nil, task, res2, d2.RunID, clock.Add(time.Second)); err != nil {
			t.Fatalf("ProcessResult 2: %v", err)
		}

		// Verify Attempt 2 prompt carried H1 and asked to consider replacing it (revising=true).
		if !strings.Contains(prompt2, h1Text) {
			t.Errorf("Attempt 2 prompt missing H1 handover text")
		}
		if !strings.Contains(prompt2, "Before you finish, consider replacing that handover with a fresh one (write_handover)") {
			t.Errorf("Attempt 2 prompt missing revising handoverRequest")
		}
		if st, err := store.State(ctx, "1"); err != nil || st != model.StateAwaitingReply {
			t.Fatalf("state after Attempt 2 = %q (%v), want awaiting_reply", st, err)
		}

		// Now post a short human reply (~40 bytes, well under the 48 KiB post-handover tailBudget)
		// to unpark the task for Attempt 3.
		clock = clock.Add(time.Minute)
		const shortHumanReply = "SHORT-HUMAN-REPLY-FOR-ATTEMPT-3: Confirmed, please finish."
		if _, err := store.AddComment(ctx, model.Comment{
			TaskID:    "1",
			Author:    model.Attribution{Actor: actor},
			Body:      shortHumanReply,
			CreatedAt: clock,
		}); err != nil {
			t.Fatalf("AddComment short human reply: %v", err)
		}

		// Dispatch Attempt 3 and inspect the prompt delivered to Attempt 3.
		d3 := dispatch.Dispatch{TaskID: "1", RunID: "run-3", Attempt: 3}
		clock = clock.Add(time.Minute)
		startWalkthroughRun(t, ctx, store, d3, clock)
		var prompt3 string
		fw3 := walkthroughAgentFunc(func(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
			prompt3 = cfg.Prompt
			return &agent.Result{
				FinalText: "Completed on attempt 3.",
				ToolCalls: []agent.ToolCall{
					{Name: "comment_on_issue", Arguments: map[string]any{"comment": "Finished after handover window reset."}},
				},
			}, nil
		})
		sb3 := t.TempDir()
		res3, err := orchestrator.RunDispatch(ctx, store, fw3, orchestrator.Config{}, task, d3, mcp.NewSandboxTools(sb3), sb3, "", nil, clock)
		if err != nil {
			t.Fatalf("RunDispatch 3: %v", err)
		}
		if err := orchestrator.ProcessResult(ctx, store, nil, task, res3, d3.RunID, clock.Add(time.Second)); err != nil {
			t.Fatalf("ProcessResult 3: %v", err)
		}

		// Verify Attempt 3 prompt properties:
		// 1. Superseded handover H1 is omitted.
		if strings.Contains(prompt3, h1Text) {
			t.Errorf("Attempt 3 prompt still contains superseded handover H1 (%q)", h1Text)
		}
		// 2. Latest handover H2 is present.
		if !strings.Contains(prompt3, h2Text) {
			t.Errorf("Attempt 3 prompt is missing latest handover H2 (%q)", h2Text)
		}
		// 3. PromptThreadCutNotice is present (because pre-H2 comments were elided).
		if !strings.Contains(prompt3, model.PromptThreadCutNotice) {
			t.Errorf("Attempt 3 prompt is missing PromptThreadCutNotice")
		}
		// 4. handoverRequest is omitted (because post-H2 bytes fit comfortably within tailBudget).
		for _, unwantedAsk := range []string{
			"Before you finish, consider replacing that handover",
			"Before you finish, write this task's handover",
		} {
			if strings.Contains(prompt3, unwantedAsk) {
				t.Errorf("Attempt 3 prompt unexpectedly contains handoverRequest %q despite post-H2 window reset:\n%s", unwantedAsk, prompt3)
			}
		}
		// 5. The short human reply after H2 is intact in the tail.
		if !strings.Contains(prompt3, shortHumanReply) {
			t.Errorf("Attempt 3 prompt missing short human reply %q", shortHumanReply)
		}
	})

	t.Run("suspended_prompt_preserves_updated_body_attachment_and_capability", func(t *testing.T) {
		t.Parallel()
		store, ctx := openWalkthroughStore(t)

		actor := human("alice")
		task := model.Task{
			ID:       "10",
			Intent:   model.IntentImplement,
			Title:    "Suspended session prompt test",
			Body:     "INITIAL-TASK-BODY-BEFORE-EDIT",
			Origin:   model.Origin{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonDirect},
			Approval: &model.Attribution{Actor: actor},
			Binding:  model.BindingDirective,
		}
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatalf("PutTask: %v", err)
		}
		if _, err := store.AddComment(ctx, model.Comment{
			TaskID:    "10",
			Author:    model.Attribution{Actor: actor},
			Body:      "INITIAL-COMMENT-DELIVERED-IN-TURN-1",
			CreatedAt: baseTime,
		}); err != nil {
			t.Fatalf("AddComment initial: %v", err)
		}

		capProvider := &walkthroughTestCapability{
			name:          "walkthrough-cap",
			promptSection: "WALKTHROUGH-CAPABILITY-DYNAMIC-PROMPT-SECTION",
		}
		cfg := orchestrator.Config{
			AgentFramework:  model.AgentFrameworkClaude,
			PromptExtension: "STANDING-DEPLOYMENT-EXTENSION-ONLY-IN-FULL-PROMPT",
			Capabilities:    model.NewCapabilityRegistry(capProvider),
		}

		// Turn 1: initial dispatch records a suspended session.
		d1 := dispatch.Dispatch{TaskID: "10", RunID: "r1", Attempt: 1}
		startWalkthroughRun(t, ctx, store, d1, baseTime)
		root1 := t.TempDir()
		fw1 := walkthroughAgentFunc(func(_ context.Context, rc agent.RunConfig) (*agent.Result, error) {
			if rc.SuspendedPrompt != "" {
				t.Errorf("Turn 1 SuspendedPrompt = %q, want empty", rc.SuspendedPrompt)
			}
			return &agent.Result{
				FinalText:        "Parked after turn 1",
				ToolCalls:        []agent.ToolCall{{Name: "run_command"}},
				ResumableSession: "sess-walkthrough-10",
			}, nil
		})
		if _, err := orchestrator.RunDispatch(ctx, store, fw1, cfg, task, d1, mcp.NewSandboxTools(root1), root1, "", nil, baseTime); err != nil {
			t.Fatalf("RunDispatch r1: %v", err)
		}

		// Between turns: edit task body, add a follow-up comment with an attachment,
		// and grant the capability on the task.
		replyID, err := store.AddComment(ctx, model.Comment{
			TaskID:    "10",
			Author:    model.Attribution{Actor: actor},
			Body:      "FOLLOW-UP-COMMENT-FOR-RESUMED-TURN",
			CreatedAt: baseTime.Add(time.Minute),
		})
		if err != nil {
			t.Fatalf("AddComment follow-up: %v", err)
		}
		if _, err := store.AddAttachment(ctx, model.Attachment{
			TaskID:      "10",
			CommentID:   &replyID,
			Filename:    "architecture.png",
			ContentType: "image/png",
			Content:     []byte("FAKEPNG"),
			Size:        7,
			CreatedAt:   baseTime.Add(time.Minute),
		}); err != nil {
			t.Fatalf("AddAttachment: %v", err)
		}

		task.Body = "UPDATED-TASK-BODY-AFTER-OPERATOR-EDIT"
		task.Grants = []model.Grant{{Capability: "walkthrough-cap", Via: model.GrantByLabel}}

		// Turn 2: resumed session turn.
		d2 := dispatch.Dispatch{TaskID: "10", RunID: "r2", Attempt: 2}
		startWalkthroughRun(t, ctx, store, d2, baseTime.Add(2*time.Minute))
		root2 := t.TempDir()
		var gotCfg agent.RunConfig
		fw2 := walkthroughAgentFunc(func(_ context.Context, rc agent.RunConfig) (*agent.Result, error) {
			gotCfg = rc
			return &agent.Result{
				FinalText:        "Completed on resumed turn",
				ToolCalls:        []agent.ToolCall{{Name: "run_command"}},
				ResumableSession: "sess-walkthrough-10",
			}, nil
		})
		if _, err := orchestrator.RunDispatch(ctx, store, fw2, cfg, task, d2, mcp.NewSandboxTools(root2), root2, "", nil, baseTime.Add(2*time.Minute)); err != nil {
			t.Fatalf("RunDispatch r2: %v", err)
		}

		if gotCfg.ResumeSession != "sess-walkthrough-10" {
			t.Fatalf("ResumeSession = %q, want sess-walkthrough-10", gotCfg.ResumeSession)
		}
		for _, want := range []string{
			"UPDATED-TASK-BODY-AFTER-OPERATOR-EDIT",
			"FOLLOW-UP-COMMENT-FOR-RESUMED-TURN",
			orchestrator.AttachmentsDir + "/1-architecture.png",
			"WALKTHROUGH-CAPABILITY-DYNAMIC-PROMPT-SECTION",
		} {
			if !strings.Contains(gotCfg.SuspendedPrompt, want) {
				t.Errorf("SuspendedPrompt missing %q:\n%s", want, gotCfg.SuspendedPrompt)
			}
			if !strings.Contains(gotCfg.Prompt, want) {
				t.Errorf("fallback Prompt missing %q:\n%s", want, gotCfg.Prompt)
			}
		}
		for _, omitted := range []string{
			"INITIAL-COMMENT-DELIVERED-IN-TURN-1",
			"STANDING-DEPLOYMENT-EXTENSION-ONLY-IN-FULL-PROMPT",
		} {
			if strings.Contains(gotCfg.SuspendedPrompt, omitted) {
				t.Errorf("SuspendedPrompt repeated %q:\n%s", omitted, gotCfg.SuspendedPrompt)
			}
			if !strings.Contains(gotCfg.Prompt, omitted) {
				t.Errorf("fallback Prompt missing %q:\n%s", omitted, gotCfg.Prompt)
			}
		}
	})
}

type walkthroughTestCapability struct {
	model.BaseCapability
	name          string
	promptSection string
}

func (c *walkthroughTestCapability) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:      c.name,
		Label:     c.name,
		Source:    model.GrantByLabel,
		Provision: model.ProvisionPlace,
	}
}

func (c *walkthroughTestCapability) Resolve(context.Context, model.CapabilityContext) (model.Resolution, error) {
	return model.Honoured(), nil
}

func (c *walkthroughTestCapability) Materialize(context.Context, model.CapabilityContext) (model.Materialization, error) {
	return model.Materialization{
		Placements: []model.Placement{{
			Side:    model.SideSandbox,
			Path:    ".walkthrough-cap-placed",
			Content: "placed",
		}},
	}, nil
}

func (c *walkthroughTestCapability) PromptSection(context.Context, model.CapabilityContext, []model.Placement) (string, error) {
	return c.promptSection, nil
}

// 4. Antigravity resumed turn with prior step ERROR (495d9c53a)
func TestWalkthroughAntigravityResumedTurnWithPriorStepError(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not installed")
	}
	grainBin := buildGrainBinary(t)

	store, ctx := openWalkthroughStore(t)
	actor := human("alice")
	task := model.Task{
		ID:       "77",
		Intent:   model.IntentAnalyze,
		Title:    "Verify Antigravity resumed turn with prior step ERROR",
		Body:     "Ensure a resumed conversation with an earlier ERROR step and a final DONE agent_response completes on attempt 1.",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: actor},
		Binding:  model.BindingDirective,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("PutTask: %v", err)
	}

	// Pre-seed a suspended Antigravity session on task 77 so RunDispatch
	// passes ResumeSession="conv-resumed-err-77" (--conversation conv-resumed-err-77).
	const convID = "conv-resumed-err-77"
	if err := store.SetSuspendedSession(ctx, task.ID, &model.SuspendedSession{
		Framework: model.AgentFrameworkAntigravity,
		Token:     convID,
		RunID:     "run-0",
		At:        baseTime,
	}, baseTime); err != nil {
		t.Fatalf("SetSuspendedSession: %v", err)
	}

	// Build the tool roster list that agy's init event advertises so verifyToolRoster
	// sees every published tool registered eagerly.
	var eagerTools []string
	for _, name := range agent.PublishedToolNames() {
		eagerTools = append(eagerTools, mcp.AgyQualifiedToolName(name))
	}
	toolsJSON, err := json.Marshal(eagerTools)
	if err != nil {
		t.Fatalf("marshaling eager tools: %v", err)
	}

	const closingReport = "Closing report from resumed Antigravity turn: completed cleanly despite historical step ERROR."
	const finalResponse = "Completed investigation on resumed turn and posted closing report."

	// Write a stub agy binary that:
	// 1. Verifies `--conversation conv-resumed-err-77` is in argv and logs the invocation count.
	// 2. Emits a resumed conversation stream-json containing:
	//    - init event with the full eager tool roster
	//    - step 0 (from the earlier attempt in the conversation) with state="ERROR"
	//    - step 1 (MCP tool call to comment_on_issue) with state="ACTIVE" and state="DONE"
	//    - step 2 (the resumed turn's own final agent_response) with state="DONE"
	//    - terminal result event with status="ERROR" (referencing step 0's historical error)
	//      and non-empty response=finalResponse
	// 3. Exits 1 (matching agy's behavior when any step in the resumed conversation had ERROR).
	stubDir := t.TempDir()
	invocationsFile := filepath.Join(stubDir, "agy-invocations.log")
	stubAgy := filepath.Join(stubDir, "agy")

	initLine := fmt.Sprintf(`{"event":"init","conversation_id":%q,"init":{"cwd":"/tmp","tools":%s,"permission_mode":"always-proceed"}}`, convID, string(toolsJSON))
	errStepLine := fmt.Sprintf(`{"event":"step_update","step_update":{"conversation_id":%q,"step_index":0,"step_type":"agent_response","state":"ERROR","error":{"type":"APIError","message":"earlier turn hit transient error"}}}`, convID)
	toolActiveLine := fmt.Sprintf(`{"event":"step_update","step_update":{"conversation_id":%q,"step_index":1,"step_type":"tool","state":"ACTIVE","tool_name":"mcp_grain-sandbox_comment_on_issue","tool_info":{"name":"mcp_grain-sandbox_comment_on_issue","parameters":{"comment":%q}}}}`, convID, closingReport)
	toolDoneLine := fmt.Sprintf(`{"event":"step_update","step_update":{"conversation_id":%q,"step_index":1,"step_type":"tool","state":"DONE","tool_name":"mcp_grain-sandbox_comment_on_issue","tool_info":{"name":"mcp_grain-sandbox_comment_on_issue","output":"Comment recorded."}}}`, convID)
	respActiveLine := fmt.Sprintf(`{"event":"step_update","step_update":{"conversation_id":%q,"step_index":2,"step_type":"agent_response","state":"ACTIVE","text_delta":%q}}`, convID, finalResponse)
	respDoneLine := fmt.Sprintf(`{"event":"step_update","step_update":{"conversation_id":%q,"step_index":2,"step_type":"agent_response","state":"DONE"}}`, convID)
	resultLine := fmt.Sprintf(`{"event":"result","result":{"conversation_id":%q,"status":"ERROR","response":%q,"error":"Step 0 failed: earlier turn hit transient error","num_turns":2}}`, convID, finalResponse)

	scriptContent := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %q
while IFS= read -r _; do :; done
printf '%%s\n' %q
printf '%%s\n' %q
printf '%%s\n' %q
printf '%%s\n' %q
printf '%%s\n' %q
printf '%%s\n' %q
printf '%%s\n' %q
exit 1
`, invocationsFile, initLine, errStepLine, toolActiveLine, toolDoneLine, respActiveLine, respDoneLine, resultLine)

	if err := os.WriteFile(stubAgy, []byte(scriptContent), 0o755); err != nil {
		t.Fatalf("writing stub agy: %v", err)
	}

	sessStore := sessionstore.New(t.TempDir())
	fw := antigravity.New(stubAgy, grainBin, antigravity.WithSessionStore(sessStore))

	d1 := dispatch.Dispatch{TaskID: task.ID, RunID: "run-1", Attempt: 1}
	clock := baseTime.Add(time.Minute)
	startWalkthroughRun(t, ctx, store, d1, clock)
	sandboxRoot := t.TempDir()
	cfg := orchestrator.Config{AgentFramework: model.AgentFrameworkAntigravity}

	res, err := orchestrator.RunDispatch(ctx, store, fw, cfg, task, d1, mcp.NewSandboxTools(sandboxRoot), sandboxRoot, "", nil, clock)
	if err != nil {
		t.Fatalf("RunDispatch returned error on resumed turn with completed final agent_response: %v", err)
	}
	if err := orchestrator.ProcessResult(ctx, store, nil, task, res, d1.RunID, clock.Add(time.Second)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	// Verify stub agy was invoked once (no retry) and received --conversation conv-resumed-err-77.
	invocationsRaw, err := os.ReadFile(invocationsFile)
	if err != nil {
		t.Fatalf("reading invocations file: %v", err)
	}
	invocations := strings.Split(strings.TrimSpace(string(invocationsRaw)), "\n")
	if len(invocations) != 1 {
		t.Errorf("agy invocations = %d (%v), want 1 (no retry)", len(invocations), invocations)
	}
	if !strings.Contains(invocations[0], "--conversation "+convID) {
		t.Errorf("agy argv = %q, want --conversation %s", invocations[0], convID)
	}

	// Verify the task completed on attempt 1 with CompletedAt stamped and the closing comment relayed.
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil || obs == nil || obs.CompletedAt == nil {
		t.Fatalf("task observation = %+v (%v), want CompletedAt stamped on attempt 1", obs, err)
	}
	runs, err := store.Runs(ctx, task.ID)
	if err != nil || len(runs) != 1 || runs[0].Outcome != "succeeded" {
		t.Errorf("task runs = %+v (%v), want 1 succeeded run", runs, err)
	}
	comments, err := store.Comments(ctx, task.ID)
	if err != nil || len(comments) != 1 || comments[0].Body != closingReport {
		t.Errorf("task comments = %+v (%v), want closing report %q", comments, err, closingReport)
	}
}

// credentialSetAdapter adapts *gitproxy.CredentialSet to packagepublish.CredentialSource.
type credentialSetAdapter struct {
	set *gitproxy.CredentialSet
}

func (a credentialSetAdapter) PackageCredential(registry, owner, repo string) (packagepublish.Credential, error) {
	c, err := a.set.PackageCredential(registry, owner, repo)
	if err != nil {
		return packagepublish.Credential{}, err
	}
	return packagepublish.Credential{
		Registry: c.Registry,
		Username: c.Username,
		Token:    c.Token,
		Name:     c.Name,
	}, nil
}

// 5. Package registry credential checks (b5a556acd, bfe65cca7, b15cf0a22)
func TestWalkthroughPackageRegistryCredentialChecks(t *testing.T) {
	// Not parallel at top-level because subtests use t.Setenv.

	t.Run("package_publish_capability_classic_and_fine_grained_pats", func(t *testing.T) {
		ctx := context.Background()
		repo := model.RepoRef{Owner: "acme", Name: "widgets"}
		cc := model.CapabilityContext{
			Task: model.Task{ID: "ci-job-1", Target: &repo},
		}

		// 1. Fine-grained PAT (github_pat_...) is refused by PackageCredential and package-publish Resolve for ghcr.io.
		fgDir := t.TempDir()
		mustWriteFile(t, filepath.Join(fgDir, "credentials.json"), `{"acme/widgets": "fg-pat"}`)
		mustWriteFile(t, filepath.Join(fgDir, "fg-pat.token"), "github_pat_11ABCDEFG0123456789_abcdefghijklmnop")
		fgSet, err := gitproxy.LoadCredentialSet(fgDir)
		if err != nil {
			t.Fatalf("LoadCredentialSet(fg): %v", err)
		}
		fgSet.SetConnections(func(owner, name string) (string, bool) {
			if owner == "acme" && name == "widgets" {
				return "fg-pat", true
			}
			return "", false
		})
		fgProvider := packagepublish.New(packagepublish.Config{Credentials: credentialSetAdapter{set: fgSet}})
		fgRes, err := fgProvider.Resolve(ctx, cc)
		if err != nil {
			t.Fatalf("fgProvider.Resolve: %v", err)
		}
		if !fgRes.Refused || !strings.Contains(fgRes.Reason, "fine-grained personal access token") || !strings.Contains(fgRes.Reason, "write:packages") {
			t.Errorf("fine-grained PAT resolution = %+v, want refused mentioning fine-grained and write:packages", fgRes)
		}

		// 2. Classic PAT (ghp_...) resolves and materializes /home/debian/.package-publish-{registry,username,token}.
		classicDir := t.TempDir()
		const classicToken = "ghp_0123456789abcdef0123456789abcdef0123"
		mustWriteFile(t, filepath.Join(classicDir, "credentials.json"), `{"acme/widgets": "classic-pat"}`)
		mustWriteFile(t, filepath.Join(classicDir, "classic-pat.token"), classicToken)
		classicSet, err := gitproxy.LoadCredentialSet(classicDir)
		if err != nil {
			t.Fatalf("LoadCredentialSet(classic): %v", err)
		}
		classicSet.SetConnections(func(owner, name string) (string, bool) {
			if owner == "acme" && name == "widgets" {
				return "classic-pat", true
			}
			return "", false
		})
		classicProvider := packagepublish.New(packagepublish.Config{Credentials: credentialSetAdapter{set: classicSet}})
		classicRes, err := classicProvider.Resolve(ctx, cc)
		if err != nil || classicRes.Refused {
			t.Fatalf("classicProvider.Resolve = %+v, %v; want honoured", classicRes, err)
		}
		mat, err := classicProvider.Materialize(ctx, cc)
		if err != nil {
			t.Fatalf("classicProvider.Materialize: %v", err)
		}
		placed := map[string]string{}
		for _, p := range mat.Placements {
			placed[p.Path] = p.Content
		}
		if placed[packagepublish.RegistryPath] != "ghcr.io" ||
			placed[packagepublish.UsernamePath] != "x-access-token" ||
			placed[packagepublish.TokenPath] != classicToken {
			t.Errorf("materialized placements = %+v, want ghcr.io / x-access-token / %s", placed, classicToken)
		}
	})

	t.Run("release_images_pat_scope_check_and_docker_config_json", func(t *testing.T) {
		var mu sync.Mutex
		var currentScopes string
		rateLimitSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/rate_limit" {
				http.NotFound(w, r)
				return
			}
			mu.Lock()
			scopes := currentScopes
			mu.Unlock()
			w.Header().Set("X-OAuth-Scopes", scopes)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}))
		defer rateLimitSrv.Close()

		rootDir := walkthroughRepoRoot(t)
		runReleaseImagesWithScopes := func(t *testing.T, phase, token, scopes string) (string, string, error) {
			t.Helper()
			mu.Lock()
			currentScopes = scopes
			mu.Unlock()

			homeDir := t.TempDir()
			binDir := filepath.Join(homeDir, "bin")
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatalf("mkdir bin: %v", err)
			}
			// Stub docker that writes ~/.docker/config.json on `docker login <registry> -u <user> --password-stdin`.
			dockerStub := `#!/bin/sh
if [ "$1" = "login" ]; then
  reg="$2"
  user="$4"
  IFS= read -r pass || true
  mkdir -p "$HOME/.docker"
  auth="$(printf '%s:%s' "$user" "$pass" | base64 | tr -d '\n')"
  printf '{"auths":{"%s":{"auth":"%s"}}}\n' "$reg" "$auth" > "$HOME/.docker/config.json"
  echo "Login Succeeded"
  exit 0
fi
exit 0
`
			if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte(dockerStub), 0o755); err != nil {
				t.Fatalf("writing docker stub: %v", err)
			}

			scriptPath := filepath.Join(rootDir, "scripts", "release-images.sh")
			cmd := exec.Command("bash", scriptPath, phase)
			cmd.Dir = rootDir
			cmd.Env = []string{
				"PATH=" + binDir + ":" + os.Getenv("PATH"),
				"HOME=" + homeDir,
				"GRAIN_CI=true",
				"GRAIN_CI_REPO=bwsalmon/grain",
				"GRAIN_CI_REF=candidate",
				"GRAIN_CI_COMMIT=0123456789abcdef0123456789abcdef01234567",
				"RELEASE_IMAGES_STATE=" + filepath.Join(homeDir, "release-images.env"),
				"GITHUB_API_URL=" + rateLimitSrv.URL,
				"PACKAGE_PUBLISH_REGISTRY=ghcr.io",
				"PACKAGE_PUBLISH_USERNAME=x-access-token",
				"PACKAGE_PUBLISH_TOKEN=" + token,
			}
			out, err := cmd.CombinedOutput()
			cfgBytes, _ := os.ReadFile(filepath.Join(homeDir, ".docker", "config.json"))
			return string(out), string(cfgBytes), err
		}

		const classicToken = "ghp_0123456789abcdef0123456789abcdef0123"

		// Case 1: Classic PAT lacking write:packages -> refused before build, ~/.docker/config.json not written.
		outMissing, dockerCfgMissing, errMissing := runReleaseImagesWithScopes(t, "build", classicToken, "repo, workflow")
		if errMissing == nil {
			t.Fatalf("expected release-images.sh build to fail when classic PAT lacks write:packages, but succeeded:\n%s", outMissing)
		}
		for _, want := range []string{
			"classic PAT with scopes 'repo, workflow'",
			"missing 'write:packages'",
			"stopping before the build rather than at the push",
		} {
			if !strings.Contains(outMissing, want) {
				t.Errorf("output missing %q:\n%s", want, outMissing)
			}
		}
		if dockerCfgMissing != "" {
			t.Errorf("~/.docker/config.json was written despite missing write:packages: %s", dockerCfgMissing)
		}

		// Case 2: Classic PAT possessing write:packages -> passes scope check and writes ~/.docker/config.json.
		outValid, dockerCfgValid, _ := runReleaseImagesWithScopes(t, "push", classicToken, "repo, write:packages, workflow")
		wantAuth := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + classicToken))
		if !strings.Contains(dockerCfgValid, `"ghcr.io"`) || !strings.Contains(dockerCfgValid, wantAuth) {
			t.Errorf("~/.docker/config.json = %q, want ghcr.io entry with base64 auth %q (output:\n%s)", dockerCfgValid, wantAuth, outValid)
		}
	})
}
