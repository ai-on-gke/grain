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

package demo

// demo_githubsim.go is `grain demo -github-sim`: a stand-in GitHub that
// knows the demo's target repo and which branches it has.
//
// Ordinary `grain demo` wires no GitHub client, so
// /api/repos/{owner}/{name}/github-branches answers 404 and every check
// of whether a base branch exists fails open: the New task form never
// says "No branch ...", the server accepts a one-shot task on a base the
// repo does not have, and the schedule and template forms never warn
// (docs/test-runs/2026-10-04-ui-followups.md, F2). This flag puts a
// githubsim.Sim behind a real github.RESTClient at ui.Config.GitHubBranches,
// where a deployment's own GitHub client sits, and the daemon's own
// comparison over the same client at ui.Config.RepoComparer, so a task's
// "Internal PR" page draws the branch's real diff -- over a real bare repo
// holding main and release/1.0. Everything in front of it -- the
// listing cache, the base-branch refusal, the forms' warnings -- is the
// real thing; only GitHub is not.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/ghwebhook"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type demoSimEventQueue struct {
	mu      sync.Mutex
	nextSeq uint64
	acked   uint64
	events  []ghwebhook.Event
}

var _ orchestrator.GitHubEventSource = (*demoSimEventQueue)(nil)

func (q *demoSimEventQueue) enqueue(ev ghwebhook.Event) uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.nextSeq++
	ev.Seq = q.nextSeq
	q.events = append(q.events, ev)
	return ev.Seq
}

func (q *demoSimEventQueue) Pending() ([]ghwebhook.Event, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []ghwebhook.Event
	for _, ev := range q.events {
		if ev.Seq > q.acked {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (q *demoSimEventQueue) Ack(seq uint64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if seq <= q.acked {
		return nil
	}
	q.acked = seq
	idx := 0
	for idx < len(q.events) && q.events[idx].Seq <= seq {
		idx++
	}
	if idx > 0 {
		q.events = append([]ghwebhook.Event(nil), q.events[idx:]...)
	}
	return nil
}

type demoSimState struct {
	calls        *demoGitHubCalls
	events       orchestrator.GitHubEventSource
	simEvents    *demoSimEventQueue
	declarations orchestrator.DeclarationSource
	// sims is every simulated repository by "owner/name": -github-sim's
	// own and demoGitHubSimUpstream, which the e2e hook
	// (githubSimDemoHandler) edits issues on.
	sims map[string]*githubsim.Sim
}

var demoSimClients sync.Map // map[github.Client]*demoSimState

type demoGitHubSimTransport struct {
	sim   *githubsim.Sim
	calls *demoGitHubCalls
	// others are the further simulated repositories, by "owner/name"
	// (demoGitHubSimUpstream). A /repos/ path naming a repository in
	// neither answers 404, as GitHub does for one the token cannot see,
	// rather than reaching a Sim configured for another repository,
	// which panics.
	others map[string]*githubsim.Sim
}

// simFor is the Sim that answers path.
func (t *demoGitHubSimTransport) simFor(path string) *githubsim.Sim {
	rest, ok := strings.CutPrefix(path, "/repos/")
	if !ok {
		return t.sim
	}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 {
		return t.sim
	}
	name, _, _ := strings.Cut(parts[1], "?")
	if strings.EqualFold(parts[0], t.sim.Owner) && strings.EqualFold(name, t.sim.Repo) {
		return t.sim
	}
	for key, other := range t.others {
		if strings.EqualFold(key, parts[0]+"/"+name) {
			return other
		}
	}
	return nil
}

func (t *demoGitHubSimTransport) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	sim := t.simFor(path)
	if sim == nil {
		return github.ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`)}, nil
	}
	resp, err := sim.Request(method, path, headers, body)
	if err == nil && t.calls != nil {
		caller, kind := "sync", "head read"
		switch {
		case strings.Contains(path, "/actions/"):
			caller, kind = "github-ci-tails", "checks"
		case strings.HasSuffix(path, "/branches"):
			caller, kind = "ui", "branch list"
		case strings.Contains(path, "/compare/"):
			caller, kind = "ui", "PR files"
		case strings.Contains(path, "/pulls/") && strings.HasSuffix(path, "/comments"):
			caller, kind = "review-threads", "review comments"
		case strings.Contains(path, "/pulls/"):
			caller, kind = "sync", "PR read"
		}
		if c := headers[github.CallerHeader]; c != "" {
			caller = c
		}
		t.calls.recordCall(caller, kind, resp.Status == 304)
	}
	return resp, err
}

// demoGitHubSimBranches are the branches the simulated repo is seeded
// with: its default, and one release branch, so a walkthrough has a
// branch other than main that exists as well as one that does not.
var demoGitHubSimBranches = []string{demoGitHubSimDefaultBranch, "release/1.0"}

const demoGitHubSimDefaultBranch = "main"

// demoGitHubSimUpstream is a second simulated repository, standing for the
// upstream a fork imports its issues from (model.IssueImportSettings.Repo):
// it has issues and nothing else -- no task targets it, and its branches
// are -github-sim's own bare repository's, which nothing reads.
var demoGitHubSimUpstream = model.RepoRef{Owner: "acme-upstream", Name: "widgets"}

// configureGitHubSimDemo creates the bare repo under dir, seeds it with
// demoGitHubSimBranches and wires a githubsim.Sim for repo into cfg. It
// returns the bare repo's path too, so a later seed can push a branch a
// task of its own is meant to have pushed already (pushDemoGitHubSimBranch).
func configureGitHubSimDemo(dir string, cfg ui.Config, repo model.RepoRef) (ui.Config, string, error) {
	return configureGitHubSimDemoWith(dir, cfg, repo, nil)
}

// configureGitHubSimDemoWith is configureGitHubSimDemo with seed, when
// not nil, run over the sim and its bare repo before anything reads them
// (seedDemoForkPullRequests).
func configureGitHubSimDemoWith(dir string, cfg ui.Config, repo model.RepoRef, seed func(sim *githubsim.Sim, bare string) error) (ui.Config, string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return cfg, "", fmt.Errorf("the simulated GitHub keeps its branches in a real git repo, and git is not installed: %w", err)
	}
	bare := filepath.Join(dir, "githubsim", repo.Owner, repo.Name+".git")
	if err := os.MkdirAll(filepath.Dir(bare), 0o700); err != nil {
		return cfg, "", err
	}
	if _, err := demoGit("", "init", "--bare", "-q", "-b", demoGitHubSimDefaultBranch, "--", bare); err != nil {
		return cfg, "", err
	}
	// One empty commit every branch points at, made with plumbing so no
	// working tree, and no user's own git identity, is needed.
	tree, err := demoGit(bare, "mktree")
	if err != nil {
		return cfg, "", err
	}
	commit, err := demoGit(bare, "commit-tree", "-m", "Initial commit", "--end-of-options", tree)
	if err != nil {
		return cfg, "", err
	}
	for _, branch := range demoGitHubSimBranches {
		if _, err := demoGit(bare, "update-ref", "--", "refs/heads/"+branch, commit); err != nil {
			return cfg, "", err
		}
	}

	ghCalls, _ := cfg.GitHubCalls.(*demoGitHubCalls)
	if ghCalls == nil {
		ghCalls = newDemoGitHubCalls()
		cfg.GitHubCalls = ghCalls
	}
	sim := githubsim.New(repo.Owner, repo.Name, bare, demoGitHubSimDefaultBranch)
	// Seed an open issue, a comment, and a review comment on the first PR number
	// (sim.PullRequestBase defaults to 9000) so `grain issue list|read` and
	// `grain pr reply|resolve` have realistic simulated records in `grain demo`.
	sim.Issues[1] = &githubsim.Issue{
		Title:  "Support sandbox CLI subcommands in demo mode",
		Body:   "Ensure the in-sandbox grain CLI works end-to-end against grain demo.",
		Author: "operator",
		Labels: map[string]struct{}{"enhancement": {}, "grain-agent": {}},
		State:  "open",
	}
	sim.Comments[1] = []github.Comment{
		{ID: 101, User: "operator", Body: "Please verify both /fs/ and /cli/ routes."},
	}
	lineOne := 1
	for _, prNum := range []int{9000, 9001, 9002} {
		sim.ReviewComments[prNum] = []github.ReviewComment{
			{ID: 501, Path: "README.md", Line: &lineOne, Body: "Please document this flag.", User: "reviewer"},
		}
	}
	if seed != nil {
		if err := seed(sim, bare); err != nil {
			return cfg, "", err
		}
	}
	upstream := githubsim.New(demoGitHubSimUpstream.Owner, demoGitHubSimUpstream.Name, bare, demoGitHubSimDefaultBranch)
	upstream.Issues[7] = &githubsim.Issue{
		Title:  "Widgets render blank on narrow screens",
		Body:   "Reported upstream: below 400px the widget grid collapses to nothing.",
		Author: "upstream-user",
		Labels: map[string]struct{}{"bug": {}},
		State:  "open",
	}
	others := map[string]*githubsim.Sim{demoGitHubSimUpstream.String(): upstream}
	client := github.NewClient(&demoGitHubSimTransport{sim: sim, calls: ghCalls, others: others}, nil)
	sims := map[string]*githubsim.Sim{repo.String(): sim, demoGitHubSimUpstream.String(): upstream}
	demoSimClients.Store(client, &demoSimState{
		calls:        ghCalls,
		simEvents:    &demoSimEventQueue{},
		declarations: demoBareDeclarations{repo: repo, bare: bare},
		sims:         sims,
	})
	cfg.GitHubBranches = client
	cfg.GitHubForkPullRequests = client
	prReader := demoPullRequests{repo: repo, client: client}
	if prev, ok := cfg.GitHubPullRequests.(demoPullRequests); ok {
		prReader.store = prev.store
	}
	cfg.GitHubPullRequests = prReader
	if prReader.store != nil {
		cfg.PullRequests = &daemon.PullRequestOpener{Store: prReader.store, Client: client}
	}
	// The pull request page a task's "Internal PR" link opens draws its
	// diff through the daemon's own comparison (merge base first, then
	// the diff from it), here over the sim's real compare endpoint.
	cfg.RepoComparer = demoGitHubSimComparer{repo: repo, github: daemon.GitHubRepoComparer{Client: client}}
	// The sim stands in for this one repo. Any other repo the demo
	// names keeps answering "not available", as it does with no
	// -github-sim at all, rather than a listing error.
	cfg.GitHubCredentialAvailable = func(owner, name string) bool {
		return repo.Same(model.RepoRef{Owner: owner, Name: name})
	}
	// The manual "Import issues now", over the sim, as the daemon wires
	// it over its own GitHub client (pkg/daemon's ui_server.go). Without
	// it the button answers that the deployment has no GitHub client.
	if store := prReader.store; store != nil {
		cfg.IssueImporter = func(ctx context.Context, target model.RepoRef) (model.IssueImportResult, error) {
			return orchestrator.ImportIssues(ctx, store, client, target, time.Now().UTC())
		}
	}
	return cfg, bare, nil
}

// demoPullRequests answers PullRequestReader and BranchExists for grain demo.
// When -github-sim is enabled, real pull requests and branches in the
// simulated bare repo win first; for seeded demo pull requests that were
// written directly into the store rather than opened through githubsim, it
// synthesizes a PullRequestDetail from the task's branch head in githubsim
// (if pushed), its recorded CI commit, its merge policy head, its merge-batch
// member SHA, or a deterministic commit SHA for its PR number.
type demoPullRequests struct {
	repo   model.RepoRef
	store  *model.Store
	client github.Client
}

func (p demoPullRequests) GetPullRequest(owner, name string, number int) (github.PullRequestDetail, error) {
	if !p.repo.Same(model.RepoRef{Owner: owner, Name: name}) {
		return github.PullRequestDetail{}, fmt.Errorf("the demo only has %s, not %s/%s", p.repo, owner, name)
	}
	if p.client != nil {
		if pr, err := p.client.GetPullRequest(owner, name, number); err == nil && pr.Number != 0 {
			return pr, nil
		}
	}
	if p.store == nil {
		return github.PullRequestDetail{}, fmt.Errorf("pull request %s/%s#%d not found", owner, name, number)
	}
	ctx := context.Background()
	tasks, err := p.store.ListTasks(ctx)
	if err != nil {
		return github.PullRequestDetail{}, err
	}
	var matched *model.Task
	for i := range tasks {
		ref := model.TaskPullRequest(tasks[i])
		if ref != nil && p.repo.Same(ref.Repo) && ref.Number == number {
			matched = &tasks[i]
			break
		}
	}
	if matched == nil {
		return github.PullRequestDetail{}, fmt.Errorf("pull request %s/%s#%d not found", owner, name, number)
	}
	base := strings.TrimSpace(matched.Base)
	if base == "" {
		base = demoGitHubSimDefaultBranch
	}
	var head string
	if p.client != nil {
		if b, err := p.client.GetBranchHead(owner, name, matched.BranchName()); err == nil && b != nil && strings.TrimSpace(b.SHA) != "" {
			head = strings.TrimSpace(b.SHA)
		}
	}
	if head == "" {
		if obs, err := p.store.GetObservation(ctx, matched.ID); err == nil && obs != nil && obs.CI != nil && strings.TrimSpace(obs.CI.Commit) != "" {
			head = strings.TrimSpace(obs.CI.Commit)
		}
	}
	if head == "" {
		if policy, ok := model.MergePolicyOf(*matched); ok && strings.TrimSpace(policy.Head) != "" {
			head = strings.TrimSpace(policy.Head)
		}
	}
	if head == "" {
		if batches, err := p.store.ListMergeBatches(ctx); err == nil {
			for _, batch := range batches {
				for _, m := range batch.Members {
					if m.Number == number && strings.TrimSpace(m.HeadSHA) != "" {
						head = strings.TrimSpace(m.HeadSHA)
						break
					}
				}
				if head != "" {
					break
				}
			}
		}
	}
	if head == "" {
		head = fmt.Sprintf("%040x", number)
	}
	return github.PullRequestDetail{
		Number:  number,
		Title:   matched.Title,
		State:   "open",
		BaseRef: base,
		HeadRef: matched.BranchName(),
		HeadSHA: head,
	}, nil
}

func (p demoPullRequests) BranchExists(owner, name, branch string) (bool, error) {
	if p.client == nil || !p.repo.Same(model.RepoRef{Owner: owner, Name: name}) {
		return false, fmt.Errorf("the simulated GitHub only has %s, not %s/%s", p.repo, owner, name)
	}
	return p.client.BranchExists(owner, name, branch)
}

// demoGitHubSimComparer is the daemon's GitHub comparison held to the
// one repo the sim stands for. The sim panics on a request for any other
// repo, and real GitHub would answer one this credential cannot see with
// an error, so any other repo is refused here the same way.
type demoGitHubSimComparer struct {
	repo   model.RepoRef
	github ui.RepoComparer
}

func (c demoGitHubSimComparer) Compare(ctx context.Context, repo model.RepoRef, base, head string) (ui.BranchComparison, error) {
	if !c.repo.Same(repo) {
		return ui.BranchComparison{}, fmt.Errorf("the simulated GitHub only has %s, not %s", c.repo, repo)
	}
	return c.github.Compare(ctx, c.repo, base, head)
}

// pushDemoGitHubSimBranch points branch at the simulated repo's default
// branch, as if the task that owns it had pushed it.
func pushDemoGitHubSimBranch(bare, branch string) error {
	_, err := demoGit(bare, "update-ref", "--", "refs/heads/"+branch, "refs/heads/"+demoGitHubSimDefaultBranch)
	return err
}

// demoGit runs git against gitDir (or none, for init) and returns its
// trimmed output.
func demoGit(gitDir string, args ...string) (string, error) {
	return demoGitStdin(gitDir, "", args...)
}

// demoGitStdin is demoGit with stdin as git's standard input.
func demoGitStdin(gitDir, stdin string, args ...string) (string, error) {
	if gitDir != "" {
		args = append([]string{"--git-dir", gitDir}, args...)
	}
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=grain demo", "GIT_AUTHOR_EMAIL=demo@grain.invalid",
		"GIT_COMMITTER_NAME=grain demo", "GIT_COMMITTER_EMAIL=demo@grain.invalid",
	)
	var stderr bytes.Buffer
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// githubSimDemoHandler serves /api/e2e/github-sim/repos/{owner}/{name}/...
// to loopback callers only, handing the rest of the path, the method and
// the body to that repository's simulated GitHub as a GitHub REST call:
// how a walkthrough opens an issue (POST .../issues), labels one after the
// fact (POST .../issues/N/labels) or closes one (PATCH .../issues/N with
// {"state":"closed"}) for the issue importer to find on its next pass.
//
// As a test-only, loopback-only extension that Sim.Request itself refuses,
// POST .../actions/runs appends a githubsim.WorkflowRun under the sim's
// mutex and, when ?webhook=1 or {"webhook":true} is set, enqueues a
// matching workflow_run webhook for the demo's event source. Everything
// else goes to app.
func githubSimDemoHandler(app http.Handler, client github.Client) http.Handler {
	const prefix = "/api/e2e/github-sim/repos/"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, prefix) {
			app.ServeHTTP(w, r)
			return
		}
		if !loopbackRequest(r) {
			http.Error(w, "the GitHub sim hook only answers loopback callers", http.StatusForbidden)
			return
		}
		var (
			st   *demoSimState
			sims map[string]*githubsim.Sim
		)
		if v, ok := demoSimClients.Load(client); ok {
			st, _ = v.(*demoSimState)
			if st != nil {
				sims = st.sims
			}
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/e2e/github-sim")
		parts := strings.SplitN(strings.TrimPrefix(path, "/repos/"), "/", 3)
		var sim *githubsim.Sim
		if len(parts) >= 2 {
			for key, s := range sims {
				if strings.EqualFold(key, parts[0]+"/"+parts[1]) {
					sim = s
				}
			}
		}
		if sim == nil {
			http.Error(w, "no simulated repository there", http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if r.Method == http.MethodPost && len(parts) == 3 && parts[2] == "actions/runs" {
			handleGitHubSimAddWorkflowRun(w, r, sim, st, body)
			return
		}
		if r.URL.RawQuery != "" {
			path += "?" + r.URL.RawQuery
		}
		resp, err := sim.Request(r.Method, path, map[string]string{}, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.Status)
		_, _ = w.Write(resp.Body)
	})
}

type simWorkflowJobPayload struct {
	Name             string `json:"name"`
	Conclusion       string `json:"conclusion"`
	Log              string `json:"log"`
	NeverStarted     bool   `json:"never_started"`
	NeverStartedAlt  bool   `json:"neverStarted"`
	FailureReason    string `json:"failure_reason"`
	FailureReasonAlt string `json:"failureReason"`
	FailingStep      string `json:"failing_step"`
	FailingStepAlt   string `json:"failingStep"`
}

type simWorkflowRunPayload struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Path           string `json:"path"`
	Workflow       string `json:"workflow"`
	HeadBranch     string `json:"head_branch"`
	HeadBranchAlt  string `json:"headBranch"`
	Branch         string `json:"branch"`
	HeadSHA        string `json:"head_sha"`
	HeadSHAAlt     string `json:"headSHA"`
	HeadRepo       string `json:"head_repo"`
	HeadRepoAlt    string `json:"headRepo"`
	HeadRepository *struct {
		FullName string `json:"full_name"`
	} `json:"head_repository"`
	Event         string                  `json:"event"`
	Status        string                  `json:"status"`
	Conclusion    string                  `json:"conclusion"`
	RunAttempt    int                     `json:"run_attempt"`
	RunAttemptAlt int                     `json:"runAttempt"`
	HTMLURL       string                  `json:"html_url"`
	HTMLURLAlt    string                  `json:"htmlURL"`
	Jobs          []simWorkflowJobPayload `json:"jobs"`
	Webhook       bool                    `json:"webhook"`
}

func firstSimString(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func handleGitHubSimAddWorkflowRun(w http.ResponseWriter, r *http.Request, sim *githubsim.Sim, st *demoSimState, body []byte) {
	var req simWorkflowRunPayload
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	wfPath := strings.TrimSpace(req.Path)
	if wfPath == "" && strings.TrimSpace(req.Workflow) != "" {
		wf := strings.TrimSpace(req.Workflow)
		switch {
		case strings.Contains(wf, "/"):
			wfPath = wf
		case strings.HasSuffix(wf, ".yml"), strings.HasSuffix(wf, ".yaml"):
			wfPath = ".github/workflows/" + wf
		default:
			wfPath = ".github/workflows/" + wf + ".yml"
		}
	}
	var headRepoFromObj string
	if req.HeadRepository != nil {
		headRepoFromObj = req.HeadRepository.FullName
	}
	attempt := req.RunAttempt
	if attempt <= 0 {
		attempt = req.RunAttemptAlt
	}
	jobs := make([]githubsim.WorkflowJob, len(req.Jobs))
	for i, j := range req.Jobs {
		jobs[i] = githubsim.WorkflowJob{
			Name:          j.Name,
			Conclusion:    j.Conclusion,
			Log:           j.Log,
			NeverStarted:  j.NeverStarted || j.NeverStartedAlt,
			FailureReason: firstSimString(j.FailureReason, j.FailureReasonAlt),
			FailingStep:   firstSimString(j.FailingStep, j.FailingStepAlt),
		}
	}
	added := sim.AddWorkflowRun(githubsim.WorkflowRun{
		ID:         req.ID,
		Name:       req.Name,
		Path:       wfPath,
		HeadBranch: firstSimString(req.HeadBranch, req.HeadBranchAlt, req.Branch),
		HeadSHA:    firstSimString(req.HeadSHA, req.HeadSHAAlt),
		HeadRepo:   firstSimString(req.HeadRepo, req.HeadRepoAlt, headRepoFromObj),
		Event:      req.Event,
		Status:     req.Status,
		Conclusion: req.Conclusion,
		RunAttempt: attempt,
		HTMLURL:    firstSimString(req.HTMLURL, req.HTMLURLAlt),
		Jobs:       jobs,
	})

	wantWebhook := req.Webhook
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("webhook"))) {
	case "1", "true", "yes":
		wantWebhook = true
	}

	var webhookSeq uint64
	if wantWebhook && st != nil && st.simEvents != nil {
		now := time.Now().UTC().Truncate(time.Second)
		started := now.Add(-time.Minute)
		action := added.Status
		if action == "" {
			action = "completed"
		}
		whPayload, err := json.Marshal(map[string]any{
			"action":     action,
			"repository": map[string]string{"full_name": sim.Owner + "/" + sim.Repo},
			"sender":     map[string]string{"login": "github-actions[bot]", "type": "Bot"},
			"workflow_run": map[string]any{
				"id":              added.ID,
				"name":            added.Name,
				"path":            added.Path,
				"head_branch":     added.HeadBranch,
				"head_sha":        added.HeadSHA,
				"head_repository": map[string]string{"full_name": added.HeadRepo},
				"event":           added.Event,
				"status":          added.Status,
				"conclusion":      added.Conclusion,
				"run_attempt":     added.RunAttempt,
				"html_url":        added.HTMLURL,
				"run_started_at":  started.Format(time.RFC3339),
				"updated_at":      now.Format(time.RFC3339),
			},
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		ev, err := ghwebhook.ParsePayload("workflow_run", fmt.Sprintf("sim-workflow-run-%d-%d", added.ID, added.RunAttempt), whPayload)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		webhookSeq = st.simEvents.enqueue(ev)
	}

	out := map[string]any{
		"id":              added.ID,
		"name":            added.Name,
		"path":            added.Path,
		"head_branch":     added.HeadBranch,
		"head_sha":        added.HeadSHA,
		"head_repository": map[string]string{"full_name": added.HeadRepo},
		"event":           added.Event,
		"status":          added.Status,
		"conclusion":      added.Conclusion,
		"run_attempt":     added.RunAttempt,
		"html_url":        added.HTMLURL,
	}
	if webhookSeq > 0 {
		out["webhook_queued"] = true
		out["webhook_seq"] = webhookSeq
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(out)
}
