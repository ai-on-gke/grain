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

package ui

// A person's Approve or Reject for a repository that publishes from
// GitHub Actions (ci_mode: github): grain CI holds none of its
// publishing credentials, so the answer is a workflow_dispatch of
// .github/workflows/promote-release.yml, which does
// (design/playbook/releases.playbook.md §0.2, gap 4).
//
// That workflow refuses a `*[bot]` actor, and this deployment dispatches
// as its own forge credential. So the dispatch carries a
// releaseapproval.Record -- repository, version, decision, the commit
// the -pending tag stages, the person, a 15-minute expiry -- signed with
// a key only this deployment holds, which the workflow verifies against
// the repository variable GRAIN_RELEASE_APPROVAL_KEY
// (scripts/verify-release-approval.sh). A record is signed here and
// nowhere else, after DecideRelease has refused every caller but a
// person.

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/releaseapproval"
)

// GitHubPromoteReleaseWorkflow is the GitHub Actions workflow a person's
// answer dispatches on a ci_mode: github repository.
const GitHubPromoteReleaseWorkflow = "promote-release.yml"

// ReleaseDispatch is the workflow_dispatch a person's answer started on
// GitHub. GitHub names no run for it; the run shows up under the
// workflow's runs, by the deployment's forge identity, within seconds.
type ReleaseDispatch struct {
	Repo     string    `json:"repo"`
	Workflow string    `json:"workflow"`
	Ref      string    `json:"ref"`
	Commit   string    `json:"commit"`
	By       string    `json:"by"`
	At       time.Time `json:"at"`
	Expires  time.Time `json:"expires"`
}

// releaseDispatchMu is held across the whole dispatch and store record:
// answers are rare, and two presses racing must start one run, not two.
// Pending-release answers are persisted in Store (RecordReleaseAnswer,
// LatestReleaseAnswer) so both release-watch and the second-press refusal
// survive a restart; rollback dispatches live on Client.rollbackDispatches.
var releaseDispatchMu sync.Mutex

// releaseForge is the forge connection release tags are read and the
// workflow dispatched through, or nils when this deployment has none.
func (c *Client) releaseForge() (github.ReleaseTagReader, github.WorkflowDispatcher) {
	var candidates []any
	if c.Config.Actions != nil {
		candidates = append(candidates, c.Config.Actions)
	}
	if c.Config.GitHubPullRequests != nil {
		candidates = append(candidates, c.Config.GitHubPullRequests)
	}
	for _, cand := range candidates {
		if l, ok := cand.(github.CallerLabeler); ok {
			cand = l.WithCaller("release-approval")
		}
		tags, ok1 := cand.(github.ReleaseTagReader)
		dispatcher, ok2 := cand.(github.WorkflowDispatcher)
		if ok1 && ok2 {
			return tags, dispatcher
		}
	}
	return nil, nil
}

// publishesFromGitHub reports whether repo's releases are GitHub
// Actions' (model.Store.ReleasesFromGitHubActions): its ci_mode is
// github, or it set none and grain CI is not on for it.
func (c *Client) publishesFromGitHub(ctx context.Context, repo model.RepoRef) (bool, error) {
	if c.Store == nil {
		return false, nil
	}
	return c.Store.ReleasesFromGitHubActions(ctx, repo)
}

// dispatchGitHubRelease signs actor's answer about version, dispatches
// promote-release.yml with it, and records the answer durably in Store.
// The caller has already established that actor is a person.
func (c *Client) dispatchGitHubRelease(ctx context.Context, repo model.RepoRef, version string, actor model.Principal,
	approve bool, answers ReleaseApproval) (ReleaseDispatch, error) {
	if err := c.actionsRepoServed(ctx, repo); err != nil {
		return ReleaseDispatch{}, err
	}
	tags, dispatcher := c.releaseForge()
	if tags == nil {
		return ReleaseDispatch{}, fmt.Errorf("this deployment has no forge connection that can dispatch %s's %s", repo, GitHubPromoteReleaseWorkflow)
	}
	decision := "reject"
	if approve {
		decision = "approve"
	}
	now := c.now().UTC()
	releaseDispatchMu.Lock()
	defer releaseDispatchMu.Unlock()
	if c.Store != nil {
		prevBy, prevAt, ok, err := c.Store.LatestReleaseAnswer(ctx, repo, version)
		if err != nil {
			return ReleaseDispatch{}, err
		}
		if ok && now.Before(prevAt.Add(releaseapproval.Lifetime)) {
			return ReleaseDispatch{}, validationErrorf("%s was already answered: %s dispatched %s for it at %s; "+
				"watch that run rather than starting another", version, prevBy, GitHubPromoteReleaseWorkflow, prevAt.Format(time.RFC3339))
		}
	}
	commit, err := github.TagCommit(tags, repo.Owner, repo.Name, version+"-pending")
	if err != nil {
		return ReleaseDispatch{}, fmt.Errorf("reading %s's %s-pending tag: %w", repo, version, github.Readable(err))
	}
	if commit == "" {
		return ReleaseDispatch{}, &NotFoundError{ID: version, message: fmt.Sprintf("%s is not pending: %s has no %s-pending tag", version, repo, version)}
	}
	signer, err := releaseapproval.LoadSigner(c.releaseApprovalDir())
	if err != nil {
		return ReleaseDispatch{}, err
	}
	signed, err := signer.Sign(ctx, releaseapproval.Record{
		Repo: repo.String(), Version: version, Decision: decision, Commit: commit, Approver: actor.ID,
		ShortSoakOK: answers.ShortSoakOK, AllowBreaking: answers.AllowBreaking, Issued: now, Expires: now.Add(releaseapproval.Lifetime),
	})
	if err != nil {
		return ReleaseDispatch{}, err
	}
	d := ReleaseDispatch{Repo: repo.String(), Workflow: GitHubPromoteReleaseWorkflow, Ref: releaseTrunk, Commit: commit,
		By: actor.ID, At: now, Expires: now.Add(releaseapproval.Lifetime)}
	inputs := map[string]string{
		"version":            version,
		"decision":           decision,
		"approval":           signed.Record,
		"approval_signature": signed.Signature,
	}
	if approve {
		inputs["short_soak_ok"] = fmt.Sprint(answers.ShortSoakOK)
		inputs["allow_breaking"] = fmt.Sprint(answers.AllowBreaking)
	}
	if err := dispatcher.DispatchWorkflow(repo.Owner, repo.Name, GitHubPromoteReleaseWorkflow, releaseTrunk, inputs); err != nil {
		return ReleaseDispatch{}, fmt.Errorf("dispatching %s's %s: %w", repo, GitHubPromoteReleaseWorkflow, github.Readable(err))
	}
	c.recordReleaseAnswer(ctx, repo, version, actor, decision, now)
	return d, nil
}

// dispatchGitHubRollback signs actor's rollback of version and dispatches
// promote-release.yml with decision=rollback. The caller has already
// established that actor is a person.
func (c *Client) dispatchGitHubRollback(ctx context.Context, repo model.RepoRef, version, actor string,
	opts ReleaseRollback) (ReleaseDispatch, error) {
	if err := c.actionsRepoServed(ctx, repo); err != nil {
		return ReleaseDispatch{}, err
	}
	tags, dispatcher := c.releaseForge()
	if tags == nil {
		return ReleaseDispatch{}, fmt.Errorf("this deployment has no forge connection that can dispatch %s's %s", repo, GitHubPromoteReleaseWorkflow)
	}
	key := repo.String() + "\x00rollback\x00" + version
	now := c.now().UTC()
	releaseDispatchMu.Lock()
	defer releaseDispatchMu.Unlock()
	if d, ok := c.rollbackDispatches[key]; ok {
		if now.Before(d.Expires) {
			return ReleaseDispatch{}, validationErrorf("%s rollback was already started: %s dispatched %s for it at %s; "+
				"watch that run rather than starting another", version, d.By, d.Workflow, d.At.Format(time.RFC3339))
		}
	}
	commit, err := github.TagCommit(tags, repo.Owner, repo.Name, version)
	if err != nil {
		return ReleaseDispatch{}, fmt.Errorf("reading %s's %s tag: %w", repo, version, github.Readable(err))
	}
	if commit == "" {
		return ReleaseDispatch{}, &NotFoundError{ID: version, message: fmt.Sprintf("%s is not a release: %s has no %s tag", version, repo, version)}
	}
	signer, err := releaseapproval.LoadSigner(c.releaseApprovalDir())
	if err != nil {
		return ReleaseDispatch{}, err
	}
	signed, err := signer.Sign(ctx, releaseapproval.Record{
		Repo: repo.String(), Version: version, Decision: "rollback", Commit: commit, Approver: actor,
		RollbackTo: opts.To, Reason: opts.Reason, Issued: now, Expires: now.Add(releaseapproval.Lifetime),
	})
	if err != nil {
		return ReleaseDispatch{}, err
	}
	d := ReleaseDispatch{Repo: repo.String(), Workflow: GitHubPromoteReleaseWorkflow, Ref: releaseTrunk, Commit: commit,
		By: actor, At: now, Expires: now.Add(releaseapproval.Lifetime)}
	inputs := map[string]string{
		"version":            version,
		"decision":           "rollback",
		"approval":           signed.Record,
		"approval_signature": signed.Signature,
	}
	if opts.To != "" {
		inputs["rollback_to"] = opts.To
	}
	if opts.Reason != "" {
		inputs["reason"] = opts.Reason
	}
	if err := dispatcher.DispatchWorkflow(repo.Owner, repo.Name, GitHubPromoteReleaseWorkflow, releaseTrunk, inputs); err != nil {
		return ReleaseDispatch{}, fmt.Errorf("dispatching %s's %s: %w", repo, GitHubPromoteReleaseWorkflow, github.Readable(err))
	}
	if c.rollbackDispatches == nil {
		c.rollbackDispatches = map[string]ReleaseDispatch{}
	}
	c.rollbackDispatches[key] = d
	return d, nil
}

// ReleaseApprovalKey is the public key promote-release.yml verifies an
// Inbox approval with: the value for the repository variable
// GRAIN_RELEASE_APPROVAL_KEY.
func (c *Client) ReleaseApprovalKey(context.Context) (string, error) {
	signer, err := releaseapproval.LoadSigner(c.releaseApprovalDir())
	if err != nil {
		return "", err
	}
	return signer.PublicKeyPEM()
}

func (s *Server) handleReleaseApprovalKey(w http.ResponseWriter, r *http.Request) {
	key, err := s.tasks.ReleaseApprovalKey(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"variable": "GRAIN_RELEASE_APPROVAL_KEY", "publicKey": key})
}

// releaseApprovalDir is the directory the deployment's signing key is
// kept in: Config.ReleaseApprovalDir, else its data directory.
func (c *Client) releaseApprovalDir() string {
	if c.Config.ReleaseApprovalDir != "" {
		return c.Config.ReleaseApprovalDir
	}
	return c.Config.DataDir
}
