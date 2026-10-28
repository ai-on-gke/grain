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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// demoGitHubSimBranches are the branches the simulated repo is seeded
// with: its default, and one release branch, so a walkthrough has a
// branch other than main that exists as well as one that does not.
var demoGitHubSimBranches = []string{demoGitHubSimDefaultBranch, "release/1.0"}

const demoGitHubSimDefaultBranch = "main"

// configureGitHubSimDemo creates the bare repo under dir, seeds it with
// demoGitHubSimBranches and wires a githubsim.Sim for repo into cfg. It
// returns the bare repo's path too, so a later seed can push a branch a
// task of its own is meant to have pushed already (pushDemoGitHubSimBranch).
func configureGitHubSimDemo(dir string, cfg ui.Config, repo model.RepoRef) (ui.Config, string, error) {
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

	sim := githubsim.New(repo.Owner, repo.Name, bare, demoGitHubSimDefaultBranch)
	client := github.NewClient(sim, nil)
	cfg.GitHubBranches = client
	prReader := demoPullRequests{repo: repo, client: client}
	if prev, ok := cfg.GitHubPullRequests.(demoPullRequests); ok {
		prReader.store = prev.store
	}
	cfg.GitHubPullRequests = prReader
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
