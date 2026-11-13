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
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/releaseapproval"
	"github.com/ai-on-gke/grain/pkg/secrets"
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

// releaseDispatchMu serialises release workflow dispatches: answers are
// rare, and two presses racing must read the store, dispatch, and record
// the decision as one step rather than starting two runs.
//
// Pending-release decisions live in model.Store (release_approval);
// releaseDispatches remembers recent rollback dispatches while their
// signed records are good, keyed by Store.ID so a second press starts
// nothing more without crossing stores in tests.
var (
	releaseDispatchMu sync.Mutex
	releaseDispatches = map[string]ReleaseDispatch{} // releaseDispatchKey(store, repo + "\x00rollback\x00" + version)
)

// releaseDispatchKey qualifies a releaseDispatches key by the store the
// answer was given against (model.Store.ID, never its address, which a
// later store can reuse), so two deployments in one process -- or two
// tests, or one test's two -count iterations -- never see each other's
// dispatch as already made.
func releaseDispatchKey(store *model.Store, key string) string {
	if store == nil {
		return key
	}
	return fmt.Sprintf("%d\x00%s", store.ID(), key)
}

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

// dispatchGitHubRelease signs approver's answer about version and
// dispatches promote-release.yml with it. The caller has already
// established that approver is a person.
func (c *Client) dispatchGitHubRelease(ctx context.Context, repo model.RepoRef, version, approver string,
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
	commit, err := github.TagCommit(tags, repo.StoredOwner(), repo.Name, version+"-pending")
	if err != nil {
		return ReleaseDispatch{}, fmt.Errorf("reading %s's %s-pending tag: %w", repo, version, github.Readable(err))
	}
	if commit == "" {
		return ReleaseDispatch{}, &NotFoundError{ID: version, message: fmt.Sprintf("%s is not pending: %s has no %s-pending tag", version, repo, version)}
	}
	if c.Store != nil {
		prior, err := c.Store.GetReleaseApprovalDecision(ctx, repo, version, commit)
		if err != nil {
			return ReleaseDispatch{}, err
		}
		if prior != nil && prior.ActiveAt(now) {
			return ReleaseDispatch{}, validationErrorf("%s was already answered: %s dispatched %s for it at %s; "+
				"watch that run rather than starting another", version, prior.DecidedBy, prior.Workflow, prior.DecidedAt.Format(time.RFC3339))
		}
	}
	signed, err := c.signReleaseApproval(ctx, releaseapproval.Record{
		Repo: repo.String(), Version: version, Decision: decision, Commit: commit, Approver: approver,
		ShortSoakOK: answers.ShortSoakOK, AllowBreaking: answers.AllowBreaking, Issued: now, Expires: now.Add(releaseapproval.Lifetime),
	})
	if err != nil {
		return ReleaseDispatch{}, err
	}
	d := ReleaseDispatch{Repo: repo.String(), Workflow: GitHubPromoteReleaseWorkflow, Ref: releaseTrunk, Commit: commit,
		By: approver, At: now, Expires: now.Add(releaseapproval.Lifetime)}
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
	if err := dispatcher.DispatchWorkflow(repo.StoredOwner(), repo.Name, GitHubPromoteReleaseWorkflow, releaseTrunk, inputs); err != nil {
		return ReleaseDispatch{}, fmt.Errorf("dispatching %s's %s: %w", repo, GitHubPromoteReleaseWorkflow, github.Readable(err))
	}
	if c.Store != nil {
		if err := c.Store.PutReleaseApprovalDecision(ctx, model.ReleaseApprovalDecision{
			Repo:      repo,
			Version:   version,
			Commit:    commit,
			Decision:  decision,
			Workflow:  GitHubPromoteReleaseWorkflow,
			DecidedBy: approver,
			DecidedAt: now,
		}); err != nil {
			return ReleaseDispatch{}, fmt.Errorf("recording %s's %s release decision: %w", repo, version, err)
		}
	}
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
	key := releaseDispatchKey(c.Store, repo.String()+"\x00rollback\x00"+version)
	now := c.now().UTC()
	releaseDispatchMu.Lock()
	defer releaseDispatchMu.Unlock()
	if d, ok := releaseDispatches[key]; ok {
		if now.Before(d.Expires) {
			return ReleaseDispatch{}, validationErrorf("%s rollback was already started: %s dispatched %s for it at %s; "+
				"watch that run rather than starting another", version, d.By, d.Workflow, d.At.Format(time.RFC3339))
		}
	}
	commit, err := github.TagCommit(tags, repo.StoredOwner(), repo.Name, version)
	if err != nil {
		return ReleaseDispatch{}, fmt.Errorf("reading %s's %s tag: %w", repo, version, github.Readable(err))
	}
	if commit == "" {
		return ReleaseDispatch{}, &NotFoundError{ID: version, message: fmt.Sprintf("%s is not a release: %s has no %s tag", version, repo, version)}
	}
	signed, err := c.signReleaseApproval(ctx, releaseapproval.Record{
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
	if err := dispatcher.DispatchWorkflow(repo.StoredOwner(), repo.Name, GitHubPromoteReleaseWorkflow, releaseTrunk, inputs); err != nil {
		return ReleaseDispatch{}, fmt.Errorf("dispatching %s's %s: %w", repo, GitHubPromoteReleaseWorkflow, github.Readable(err))
	}
	releaseDispatches[key] = d
	return d, nil
}

// ReleaseApprovalKey is the public key promote-release.yml verifies an
// Inbox approval with: the value for the repository variable
// GRAIN_RELEASE_APPROVAL_KEY.
func (c *Client) ReleaseApprovalKey(ctx context.Context) (string, error) {
	signer, err := c.releaseApprovalSigner(ctx)
	if err != nil {
		return "", err
	}
	return signer.PublicKeyPEM()
}

// releaseApprovalKeyNote is what GET /api/release-approval-key says
// beside the key: the variable is a copy, so it goes stale when the key
// changes.
const releaseApprovalKeyNote = "Changing the deployment's signing key -- its -release-approval-kms-key, " +
	"or its release-approval-key secret -- means updating GRAIN_RELEASE_APPROVAL_KEY to match " +
	"in every repository that runs promote-release.yml."

func (s *Server) handleReleaseApprovalKey(w http.ResponseWriter, r *http.Request) {
	key, err := s.tasks.ReleaseApprovalKey(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"variable": "GRAIN_RELEASE_APPROVAL_KEY", "publicKey": key,
		"note": releaseApprovalKeyNote})
}

// releaseApprovalSigner is the deployment's signing key: the Cloud KMS
// key version Config.ReleaseApprovalKMSKey names, else the one kept
// under releaseApprovalDir.
//
// A KMS key the deployment cannot read -- permission denied, a version
// that does not exist or is disabled, KMS unreachable -- is a
// releaseApprovalKMSError: the fault is upstream of this process and the
// operator has to fix it there, so the route says which key it is rather
// than answering an opaque "internal server error" (walkthrough
// 2026-11-13 §9, F2). What KMS said stays in the daemon's log, under the
// reference the answer carries, as for every other 5xx.
func (c *Client) releaseApprovalSigner(ctx context.Context) (*releaseapproval.Signer, error) {
	if c.Config.ReleaseApprovalKMSKey != "" {
		signer, err := releaseapproval.LoadKMSSigner(ctx, c.Config.ReleaseApprovalKMSKey)
		if err != nil {
			return nil, &releaseApprovalKMSError{err: err}
		}
		return signer, nil
	}
	return releaseapproval.LoadSigner(c.releaseApprovalDir())
}

// signReleaseApproval loads the deployment's signing key and signs r.
//
// Once GET /api/release-approval-key (or an earlier signing call) has
// cached a Cloud KMS key version's public key, releaseApprovalSigner
// returns without calling KMS, so a missing asymmetricSign grant, a
// disabled version or an unreachable KMS surfaces on Sign instead. Wrap
// that KMS error in releaseApprovalKMSError too, so Approve, Reject and
// Rollback still answer 502 naming -release-approval-kms-key rather than
// an opaque 500.
func (c *Client) signReleaseApproval(ctx context.Context, r releaseapproval.Record) (releaseapproval.Signed, error) {
	signer, err := c.releaseApprovalSigner(ctx)
	if err != nil {
		return releaseapproval.Signed{}, err
	}
	signed, err := signer.Sign(ctx, r)
	if err != nil {
		if c.Config.ReleaseApprovalKMSKey != "" && errors.Is(err, secrets.ErrKMS) {
			return releaseapproval.Signed{}, &releaseApprovalKMSError{err: err}
		}
		return releaseapproval.Signed{}, err
	}
	return signed, nil
}

// releaseApprovalKMSError is a release approval signing key held in
// Cloud KMS that could not be read or signed with. writeClientError answers
// it 502 (apierror.CodeUpstream) with releaseApprovalKMSMessage, logging err in full.
type releaseApprovalKMSError struct{ err error }

// releaseApprovalKMSMessage is what a caller is shown for a
// releaseApprovalKMSError: which setting to check, nothing of KMS's answer.
const releaseApprovalKMSMessage = "the release approval signing key in Cloud KMS (-release-approval-kms-key) " +
	"could not be used: check the key version exists and is enabled, and that this deployment may view its " +
	"public key and sign with it; the daemon log has KMS's answer"

func (e *releaseApprovalKMSError) Error() string { return e.err.Error() }

func (e *releaseApprovalKMSError) Unwrap() error { return e.err }

// releaseApprovalDir is the directory the deployment's signing key is
// kept in: Config.ReleaseApprovalDir, else its data directory.
func (c *Client) releaseApprovalDir() string {
	if c.Config.ReleaseApprovalDir != "" {
		return c.Config.ReleaseApprovalDir
	}
	return c.Config.DataDir
}
