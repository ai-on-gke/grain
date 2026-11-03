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

// demo_fork_pull_request.go is `grain demo -github-sim -fork-pull-request`:
// two pull requests on the simulated repo whose heads live in a
// contributor's fork, so a walkthrough can file a task against a fork head
// label (`contrib:fix/readme-typo`, model.ForkHead) from the New task form
// and see it resolve -- or, for the closed one, be refused -- through the
// real forkhead.Resolve over the sim's real `?head=` lookup
// (docs/test-runs/2026-10-31-task-targeting-parking.md).
//
// A fork's branch is not a branch of the repo, so it is not under
// refs/heads; GitHub keeps every pull request's head at refs/pull/N/head
// instead, and that is where the seed puts each one's commit -- what a run
// fetches through the git proxy to start its own grain branch from
// (forkhead.PR.Note).

import (
	"fmt"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
)

// demoForkOwner is the contributor whose fork both seeded pull requests
// come from.
const demoForkOwner = "contrib"

// demoForkPullRequests are the pull requests -fork-pull-request seeds:
// one open, which its label resolves to, and one closed, which its label
// must not.
var demoForkPullRequests = []struct {
	number int
	branch string
	title  string
	state  string
	file   string
}{
	{41, "fix/readme-typo", "Fix a typo in the README", "open", "README.md"},
	{42, "feat/old-idea", "An idea that was closed without merging", "closed", "IDEA.md"},
}

// seedDemoForkPullRequests records demoForkPullRequests on sim, each with
// a commit of its own on demoGitHubSimDefaultBranch at refs/pull/N/head in
// bare.
func seedDemoForkPullRequests(sim *githubsim.Sim, bare string, repo model.RepoRef) error {
	parent, err := demoGit(bare, "rev-parse", "--verify", "refs/heads/"+demoGitHubSimDefaultBranch)
	if err != nil {
		return err
	}
	for _, f := range demoForkPullRequests {
		blob, err := demoGitStdin(bare, f.title+"\n", "hash-object", "-w", "--stdin")
		if err != nil {
			return err
		}
		tree, err := demoGitStdin(bare, fmt.Sprintf("100644 blob %s\t%s\n", blob, f.file), "mktree")
		if err != nil {
			return err
		}
		commit, err := demoGit(bare, "commit-tree", "-p"+parent, "-m", f.title, "--end-of-options", tree)
		if err != nil {
			return err
		}
		if _, err := demoGit(bare, "update-ref", "--", fmt.Sprintf("refs/pull/%d/head", f.number), commit); err != nil {
			return err
		}
		sim.PullRequests = append(sim.PullRequests, githubsim.PullRequest{
			Number: f.number, Title: f.title, Body: "From " + demoForkOwner + "'s fork.",
			Head: f.branch, Base: demoGitHubSimDefaultBranch, State: f.state,
			HTMLURL:  fmt.Sprintf("https://github.example/%s/%s/pull/%d", repo.Owner, repo.Name, f.number),
			Author:   demoForkOwner,
			HeadRepo: demoForkOwner + "/" + repo.Name,
		})
	}
	// The sim numbers a pull request it opens by how many it already
	// holds, from PullRequestBase; keep grain's own first one at 9000.
	if sim.PullRequestBase == 0 {
		sim.PullRequestBase = 9000
	}
	sim.PullRequestBase -= len(demoForkPullRequests)
	return nil
}
