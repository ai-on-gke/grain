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

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// RepoReleaseSettings is the repository-owned release policy and the presence
// (never the values) of its custom release credentials.
type RepoReleaseSettings struct {
	Repo       string              `json:"repo"`
	Enabled    bool                `json:"enabled"`
	Available  bool                `json:"available"`
	SecretsDir string              `json:"secretsDir"`
	Secrets    []RepoReleaseSecret `json:"secrets"`
	// BaseBranchJobs is model.RepoConfig.BaseBranchCIJobs: the jobs that
	// judge this repository's base branches, empty for "everything a
	// push would run".
	//
	// It belongs to this pane rather than to a CI one because it is the
	// other half of the switch above. Turning releases off used to turn
	// off the merge queue's base-branch check with them -- the run that
	// judged `main` *was* the release event -- and naming jobs here was
	// what kept that check. Since grain/task-458 the check is kept either
	// way, and since grain/task-461 the judging run is a `push` whatever
	// the switch says and the release is a run of its own, so this is a
	// bill rather than a gate and it is read in both positions
	// (docs/merge-queue-behind.md, "Publishing is not judging"). An
	// operator switching publishing off is still the person who wants to
	// see it, at the moment they do it: a full push plan on every merge
	// is what they are agreeing to pay for by leaving it empty.
	BaseBranchJobs []string `json:"baseBranchJobs"`
	// PostMergeDispatchJobs is model.RepoConfig.PostMergeDispatchJobs:
	// the jobs grain dispatches on the base branch after every merge,
	// whatever the switch above says.
	//
	// It is on this pane for BaseBranchJobs' reason and one more of its
	// own (grain/task-397, grain/task-409): what it exists to keep
	// running is the piece of a release workflow that is a *build input*
	// rather than a release -- this repository's sandbox guest cache --
	// and the moment an operator needs to know that exists is the moment
	// they are turning releases off. It is shown whether or not they
	// are, because unlike BaseBranchJobs these jobs run either way.
	PostMergeDispatchJobs []string `json:"postMergeDispatchJobs"`
	// PublishesSandboxGuest is whether this repository's release
	// workflow is also what publishes the sandbox guest this
	// deployment's tasks and CI jobs run in -- read off the guest image
	// reference itself (sandbox_guest.go), so it is true for the
	// repository this deployment actually pulls its guest from and false
	// for every other.
	//
	// It is the other consequence of the switch above, and the one
	// nothing said out loud (grain/task-397). The switch is named for
	// releases; the guest cache is not a release, it is a CI input, and
	// turning releases off stops it being filled -- which every branch
	// then pays for in an inline ~1GB guest build on the longest job in
	// the suite. An operator switching publishing off is the person who
	// needs to be told that, at the moment they do it, which is what
	// BaseBranchJobs above is here for as well.
	PublishesSandboxGuest bool `json:"publishesSandboxGuest"`
	// SandboxGuestCacheJob is the job that fills the cache without
	// turning releases back on, for the pane to name beside the warning.
	// Empty when PublishesSandboxGuest is false.
	SandboxGuestCacheJob string `json:"sandboxGuestCacheJob,omitempty"`
	// BaseBranchGate is what has actually become of this repository's
	// base-branch check, as opposed to what BaseBranchJobs above says
	// should be checking it: nil while it is working, and otherwise the
	// branch nothing has judged and when a run was last started for it
	// (basebranchgate.go, grain/task-453).
	//
	// The two are not the same reading and the difference is the whole
	// point of this field. BaseBranchJobs is a setting, and a pane that
	// shows a setting shows it the same way on the day it was chosen and
	// on the day it has been costing something for 33 hours. This is the
	// consequence: a sentence naming the branch, saying the queue is
	// merging onto it anyway, and saying since when.
	//
	// It is also where a BaseBranchJobs that has fallen behind the
	// workflows it names turns up (ui.baseGateJudgedNothing,
	// grain/task-509). A list naming a job no workflow declares any more
	// is refused by the planner, so grain starts no run at all -- and
	// nothing on this pane could have told anybody, because whether a
	// name matches is a property of a commit and this pane has none.
	BaseBranchGate *BaseBranchGate `json:"baseBranchGate,omitempty"`
	// BaseBranchPublishing is what has become of the switch above when it
	// is on: nil while this repository is shipping, and otherwise the
	// branch nothing has published from and since when
	// (basebranchrelease.go, grain/task-511).
	//
	// It is the other half of the same distinction BaseBranchGate draws.
	// `Enabled` is a setting, and a pane that shows a setting shows it
	// the same way on the day it was chosen and on the day the last nine
	// releases have failed. Since grain/task-461 a failed release holds
	// nothing at all -- deliberately, and that is the right decision --
	// so this pane is where a repository that has quietly stopped
	// shipping is visible at all.
	BaseBranchPublishing *BaseBranchPublishing `json:"baseBranchPublishing,omitempty"`
	// BaseBranches is the branches of this repository grain is watching,
	// in order -- the (repo, branch) keys of model.BaseBranchCI, which is
	// exactly the set a base-branch run can be asked for
	// (base_branch_runs.go, grain/task-480).
	//
	// It is here because the second of this pane's two by-hand controls
	// needs it. "Run release by hand" takes any ref, since a release is
	// started against whatever an operator names; "Run the base-branch
	// check now" is about a branch grain already has a row for, and a box
	// an operator has to guess the spelling of would be a control that
	// answers 404 to a branch that plainly exists.
	BaseBranches []string `json:"baseBranches"`
	// SandboxGuestCacheAutomatic is whether grain dispatches that job on
	// this repository's base branch of its own accord, without it being
	// named in PostMergeDispatchJobs above
	// (model.GuestCacheFollowsBaseBranch, grain/task-457).
	//
	// It is what turns the warning beside the switch into a statement.
	// Before it, an operator switching releases off was told their guest
	// cache had stopped following `main` and handed a job id to type; the
	// cache now goes on following it, and the pane's job is to say so
	// rather than to ask for an action that has already been taken.
	//
	// False, with PublishesSandboxGuest true, is the state that still
	// wants the warning: a repository publishing this deployment's guest
	// whose CI is not granted `package-publish`, where grain cannot fill
	// the cache because the job it would start could not push.
	SandboxGuestCacheAutomatic bool `json:"sandboxGuestCacheAutomatic"`
}

// RepoReleaseSecret is one secret a repository's release runs are given:
// its name, and whether a value is stored for it. The value itself is
// never read back.
type RepoReleaseSecret struct {
	Name string `json:"name"`
	Set  bool   `json:"set"`
}

// RepoReleaseSettings reads the release pane of repo's settings.
func (c *Client) RepoReleaseSettings(ctx context.Context, repo string) (RepoReleaseSettings, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return RepoReleaseSettings{}, validationErrorf("repo: %v", err)
	}
	cfg, err := c.Store.GetRepoConfig(ctx, parsed)
	if err != nil {
		return RepoReleaseSettings{}, err
	}
	out := RepoReleaseSettings{
		Repo: parsed.String(), Enabled: true, Available: c.Config.Secrets != nil,
		SecretsDir: ci.ReleaseSecretsDir, Secrets: []RepoReleaseSecret{},
		BaseBranchJobs: []string{}, PostMergeDispatchJobs: []string{},
		BaseBranches: []string{},
	}
	branches, err := c.watchedBaseBranches(ctx, parsed)
	if err != nil {
		return RepoReleaseSettings{}, err
	}
	out.BaseBranches = branches
	if c.publishesSandboxGuest(parsed) {
		out.PublishesSandboxGuest, out.SandboxGuestCacheJob = true, guestCacheDispatch
	}
	// Read whether or not this repository has a config row: a repository
	// with none still has base branches grain may be watching, and a
	// finding about one of them is not a setting this pane could have
	// shown instead.
	out.BaseBranchGate = c.baseBranchGate(ctx, parsed)
	// Read for the same reason, and with the switch's own state already
	// accounted for: baseBranchPublishingFor reports nothing about a
	// repository whose releases are off, which is a repository publishing
	// nothing on purpose rather than a repository that cannot.
	out.BaseBranchPublishing = c.baseBranchPublishingFor(ctx, parsed)
	if cfg == nil {
		return out, nil
	}
	out.Enabled = model.RunsWorkItem(cfg, model.ReleaseItem)
	// The same expression the reconciler dispatches from, so the pane
	// cannot ask for a job grain is already running (grain/task-457).
	out.SandboxGuestCacheAutomatic = model.GuestCacheFollowsBaseBranch(*cfg, c.sandboxGuestImage())
	if len(cfg.BaseBranchCIJobs) > 0 {
		out.BaseBranchJobs = cfg.BaseBranchCIJobs
	}
	if len(cfg.PostMergeDispatchJobs) > 0 {
		out.PostMergeDispatchJobs = cfg.PostMergeDispatchJobs
	}
	present := map[string]bool{}
	if c.Config.Secrets != nil {
		list, err := c.Config.Secrets.List()
		if err != nil {
			return RepoReleaseSettings{}, err
		}
		namespace, _ := ci.ReleaseSecretAddress(parsed.String(), "_")
		for _, secret := range list {
			if secret.Name != namespace {
				continue
			}
			for _, key := range secret.Keys {
				present[key] = true
			}
		}
	}
	for _, name := range cfg.ReleaseSecrets {
		out.Secrets = append(out.Secrets, RepoReleaseSecret{Name: name, Set: present[name]})
	}
	return out, nil
}

// SetRepoReleaseEnabled switches repo's automatic release runs on or off.
func (c *Client) SetRepoReleaseEnabled(ctx context.Context, repo string, enabled bool) (RepoReleaseSettings, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return RepoReleaseSettings{}, validationErrorf("repo: %v", err)
	}
	// Written as an explicit override on the release work item rather
	// than as the repository's default, because this pane is about one
	// item: an operator switching publishing off here has said nothing
	// about the repository's schedules, and a write that moved the
	// default would have stopped those too (grain/task-486).
	//
	// Both directions are recorded. `on` is a real state, not the
	// absence of `off` -- which is the difference that matters when the
	// repository default is later turned off and this item was
	// deliberately left running.
	state := model.OwnershipOn
	if !enabled {
		state = model.OwnershipOff
	}
	if _, err := c.putRepoConfig(ctx, parsed, func(cfg *model.RepoConfig) {
		cfg.Work = cfg.Work.With(model.ReleaseItem, state)
	}); err != nil {
		return RepoReleaseSettings{}, err
	}
	return c.RepoReleaseSettings(ctx, parsed.String())
}

// SetRepoBaseBranchCIJobs names the jobs that judge this repository's base
// branches while its releases are off, replacing whatever it named before
// -- and an empty list is a real answer, meaning "everything a push would
// run" since grain/task-458, rather than the refusal it used to mean.
//
// Names are not checked against the repository's workflows here. Whether
// a job exists is a property of a commit rather than of the settings, and
// the plan that would answer it is built against a branch this call does
// not have: a name that matches nothing is an error at the moment the run
// is started, logged there (orchestrator.startBaseBranchCI).
//
// That argument is why the check cannot be here, and it used to be the
// whole of the answer -- which left the error arriving where nobody was:
// the run was refused, the reconciler logged it, the row was left as it
// was, and the same thing happened on the next cycle for ever with the
// queue merging throughout. It is recorded on the branch's own row now
// (orchestrator.refuseBaseBranchCI, grain/task-509) and reported beside
// this very list, as the gate reading above.
func (c *Client) SetRepoBaseBranchCIJobs(ctx context.Context, repo string, jobs []string) (RepoReleaseSettings, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return RepoReleaseSettings{}, validationErrorf("repo: %v", err)
	}
	cleaned := cleanJobNames(jobs)
	if _, err := c.putRepoConfig(ctx, parsed, func(cfg *model.RepoConfig) {
		cfg.BaseBranchCIJobs = cleaned
	}); err != nil {
		return RepoReleaseSettings{}, err
	}
	return c.RepoReleaseSettings(ctx, parsed.String())
}

// cleanJobNames is jobs as a list worth storing: each name trimmed, and
// the blanks and the repeats a comma-separated box picks up left out
// (grain/task-2414) -- "build-cache, docs, build-cache" names two jobs,
// and the repo page reads back what is stored, so it should say two.
func cleanJobNames(jobs []string) []string {
	cleaned := make([]string, 0, len(jobs))
	seen := make(map[string]bool, len(jobs))
	for _, job := range jobs {
		job = strings.TrimSpace(job)
		if job == "" || seen[job] {
			continue
		}
		seen[job] = true
		cleaned = append(cleaned, job)
	}
	return cleaned
}

// SetRepoPostMergeDispatchJobs names the jobs grain dispatches on this
// repository's base branches after a merge, replacing whatever it named
// before -- and an empty list is a real answer, meaning "nothing follows
// the branch but the release run, if there is one".
//
// Names are not checked against the repository's workflows here, for
// SetRepoBaseBranchCIJobs' reason: whether a job exists is a property of
// a commit, and a name that matches nothing in the dispatch plan is an
// error at the moment the run would be started, logged there
// (orchestrator.startBaseBranchDispatch) rather than silently dropped.
func (c *Client) SetRepoPostMergeDispatchJobs(ctx context.Context, repo string, jobs []string) (RepoReleaseSettings, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return RepoReleaseSettings{}, validationErrorf("repo: %v", err)
	}
	cleaned := cleanJobNames(jobs)
	if _, err := c.putRepoConfig(ctx, parsed, func(cfg *model.RepoConfig) {
		cfg.PostMergeDispatchJobs = cleaned
	}); err != nil {
		return RepoReleaseSettings{}, err
	}
	return c.RepoReleaseSettings(ctx, parsed.String())
}

// SetRepoReleaseSecret stores value as release secret name for repo and
// lists the name on the repository's config.
func (c *Client) SetRepoReleaseSecret(ctx context.Context, repo, name, value string) (RepoReleaseSettings, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return RepoReleaseSettings{}, validationErrorf("repo: %v", err)
	}
	if err := c.requireRepoPerson(ctx, parsed); err != nil {
		return RepoReleaseSettings{}, err
	}
	name = strings.TrimSpace(name)
	if err := ci.ValidateReleaseSecretName(name); err != nil {
		return RepoReleaseSettings{}, validationErrorf("secret: %v", err)
	}
	if value == "" {
		return RepoReleaseSettings{}, validationErrorf("secret %q needs a value", name)
	}
	if c.Config.Secrets == nil {
		return RepoReleaseSettings{}, errSecretsUnavailable
	}
	existing, err := c.Store.GetRepoConfig(ctx, parsed)
	if err != nil {
		return RepoReleaseSettings{}, err
	}
	needsRepoConfigWrite := existing == nil || !slices.Contains(existing.ReleaseSecrets, name)
	if needsRepoConfigWrite {
		if err := c.checkSettingsWritableUnrehearsed(ctx, "repo_config"); err != nil {
			return RepoReleaseSettings{}, err
		}
	}
	secret, key := ci.ReleaseSecretAddress(parsed.String(), name)
	if err := c.Config.Secrets.Set(secret, key, []byte(value)); err != nil {
		return RepoReleaseSettings{}, err
	}
	if needsRepoConfigWrite {
		if _, err := c.putRepoConfig(ctx, parsed, func(cfg *model.RepoConfig) {
			if !slices.Contains(cfg.ReleaseSecrets, name) {
				cfg.ReleaseSecrets = append(cfg.ReleaseSecrets, name)
				slices.Sort(cfg.ReleaseSecrets)
			}
		}); err != nil {
			return RepoReleaseSettings{}, err
		}
	}
	return c.RepoReleaseSettings(ctx, parsed.String())
}

// DeleteRepoReleaseSecret removes release secret name from repo, value and
// listing both.
func (c *Client) DeleteRepoReleaseSecret(ctx context.Context, repo, name string) (RepoReleaseSettings, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return RepoReleaseSettings{}, validationErrorf("repo: %v", err)
	}
	if err := c.requireRepoPerson(ctx, parsed); err != nil {
		return RepoReleaseSettings{}, err
	}
	if err := c.checkSettingsWritableUnrehearsed(ctx, "repo_config"); err != nil {
		return RepoReleaseSettings{}, err
	}
	name = strings.TrimSpace(name)
	if err := ci.ValidateReleaseSecretName(name); err != nil {
		return RepoReleaseSettings{}, validationErrorf("secret: %v", err)
	}
	if c.Config.Secrets == nil {
		return RepoReleaseSettings{}, errSecretsUnavailable
	}
	secret, key := ci.ReleaseSecretAddress(parsed.String(), name)
	// A secret the repo declares but the store has lost -- the pane's
	// "Missing" row -- is removed all the same: its declaration is what
	// Remove is pressed to drop. One neither stored nor declared is a
	// 404, not the store's "no key" surfacing as a 500.
	stored, err := c.Config.Secrets.Has(secret, key)
	if err != nil {
		return RepoReleaseSettings{}, err
	}
	if stored {
		if err := c.Config.Secrets.DeleteKey(secret, key); err != nil {
			return RepoReleaseSettings{}, err
		}
	} else {
		existing, err := c.Store.GetRepoConfig(ctx, parsed)
		if err != nil {
			return RepoReleaseSettings{}, err
		}
		if existing == nil || !slices.Contains(existing.ReleaseSecrets, name) {
			return RepoReleaseSettings{}, &NotFoundError{ID: name, message: fmt.Sprintf("no release secret %s on %s", name, parsed)}
		}
	}
	if _, err := c.putRepoConfig(ctx, parsed, func(cfg *model.RepoConfig) {
		cfg.ReleaseSecrets = slices.DeleteFunc(cfg.ReleaseSecrets, func(existing string) bool {
			return existing == name
		})
	}); err != nil {
		return RepoReleaseSettings{}, err
	}
	return c.RepoReleaseSettings(ctx, parsed.String())
}

// RepoReleaseRunRequest is the body of POST
// /api/repos/{owner}/{name}/release-runs. Branch is read when Ref is
// empty.
type RepoReleaseRunRequest struct {
	Ref    string   `json:"ref,omitempty"`
	Branch string   `json:"branch,omitempty"`
	Commit string   `json:"commit,omitempty"`
	Jobs   []string `json:"jobs,omitempty"`
}

// StartRepoRelease starts a release run of repo by hand, whether or not its
// automatic release runs are switched on.
func (c *Client) StartRepoRelease(ctx context.Context, repo string, req RepoReleaseRunRequest) (ci.Run, error) {
	if c.Config.CI == nil {
		return ci.Run{}, fmt.Errorf("this deployment has no grain CI wired")
	}
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return ci.Run{}, validationErrorf("repo: %v", err)
	}
	// Who first, before anything about the repository is looked up: a
	// release run is handed the repository's release secrets, so starting
	// one is a person's act, like every other write to this
	// pane -- not any allow-listed caller's.
	if err := c.requireRepoPerson(ctx, parsed); err != nil {
		return ci.Run{}, err
	}
	enabled, err := c.ciEnabledFor(ctx, parsed)
	if err != nil {
		return ci.Run{}, err
	}
	if !enabled {
		return ci.Run{}, forbiddenErrorf("%s", ciNotEnabledMessage(parsed))
	}
	ref := strings.TrimSpace(req.Ref)
	if ref == "" {
		ref = strings.TrimSpace(req.Branch)
	}
	ref = strings.TrimPrefix(ref, "refs/heads/")
	commit := strings.TrimSpace(req.Commit)
	if err := c.checkReleaseRef(ctx, parsed, ref, commit); err != nil {
		return ci.Run{}, err
	}
	run, err := c.Config.CI.Start(ctx, ci.Request{
		Repo: parsed.String(), Ref: ref, Commit: commit,
		Event: ci.EventRelease, TrustedRelease: true,
		// A person may release any ref (checkReleaseRef), so ci.Service's
		// own check of the ref is theirs to have answered; anybody else
		// was held to the reviewed branches above, and is held to them
		// again there.
		ReleaseRefVouched: c.actor(ctx).Kind == model.PrincipalHuman,
		RequestedBy:       c.actor(ctx).ID, Jobs: req.Jobs,
	})
	if err != nil {
		// What Start refuses is the request's own fault -- a ref that does
		// not resolve, a workflow that does not parse -- so it is a 400
		// that says why, as POST /api/ci/runs answers the same refusal.
		return ci.Run{}, &ValidationError{err: err}
	}
	return run, nil
}

// checkReleaseRef refuses a release run of ref unless ref is a branch
// that has already been reviewed into: one of the repository's watched
// base branches, or a branch of one of its open releases. The workflow a
// run executes is read at ref, so a release of an arbitrary branch -- an
// unreviewed grain/task-* branch above all -- would run code its author
// controls with the release secrets mounted. A person -- the deployment's
// owner, since whoever holds the access code is (docs/access-code.md) --
// may still name any ref, and pin a commit, which is how a deployment
// republishes one by hand; no other caller may pin a commit, since
// nothing here can tell whether it is on ref at all.
func (c *Client) checkReleaseRef(ctx context.Context, repo model.RepoRef, ref, commit string) error {
	if c.Store == nil {
		return nil
	}
	if c.actor(ctx).Kind == model.PrincipalHuman {
		return nil
	}
	// checkRepoPerson's own reading of a deployment with no settings row
	// yet: nobody is refused.
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		return err
	}
	if cfg == nil {
		return nil
	}
	if commit != "" {
		return forbiddenErrorf("only a person may pin a release run of %s to a commit: name the branch instead", repo)
	}
	allowed, err := c.watchedBaseBranches(ctx, repo)
	if err != nil {
		return err
	}
	releases, err := c.Store.ListReleases(ctx, repo)
	if err != nil {
		return err
	}
	for _, rel := range releases {
		if rel.Status != model.ReleaseMerged {
			allowed = append(allowed, rel.ProdBranch(), rel.LatestBranch())
		}
	}
	// Never a task's branch, whatever row names it: a stacked review's
	// merge into one used to enrol it as a watched base (grain/task-949),
	// and its workflows are its author's.
	trusted := allowed[:0]
	for _, b := range allowed {
		isTask, err := c.Store.IsTaskBranch(ctx, b)
		if err != nil {
			return err
		}
		if !isTask {
			trusted = append(trusted, b)
		}
	}
	allowed = trusted
	if ref == "" {
		if len(allowed) == 0 {
			return forbiddenErrorf("name a branch: %s has no base branch grain is watching", repo)
		}
		return validationErrorf("branch: name one of %s", strings.Join(allowed, ", "))
	}
	for _, b := range allowed {
		if b == ref {
			return nil
		}
	}
	if len(allowed) == 0 {
		return forbiddenErrorf("%s is not a branch a release of %s may run from: grain watches none of its "+
			"branches yet, so only a system admin may start one", ref, repo)
	}
	return forbiddenErrorf("%s is not a branch a release of %s may run from: name one of %s, or ask a "+
		"system admin", ref, repo, strings.Join(allowed, ", "))
}

func (s *Server) handleGetRepoRelease(w http.ResponseWriter, r *http.Request) {
	out, err := s.tasks.RepoReleaseSettings(r.Context(), r.PathValue("owner")+"/"+r.PathValue("name"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// setRepoReleaseRequest is PUT /api/repos/{owner}/{name}/release's body.
type setRepoReleaseRequest struct {
	Enabled *bool `json:"enabled"`
	// BaseBranchJobs is a pointer so that an empty list -- "nothing
	// judges this repository's base branches" -- is tellable from a
	// request that is only about the switch above.
	BaseBranchJobs *[]string `json:"baseBranchJobs"`
	// PostMergeDispatchJobs is a pointer for the same reason: an
	// empty list -- "nothing is dispatched after a merge" -- is a
	// decision, and has to be tellable from a request that is about
	// one of the other two fields.
	PostMergeDispatchJobs *[]string `json:"postMergeDispatchJobs"`
}

func (s *Server) handleSetRepoRelease(w http.ResponseWriter, r *http.Request) {
	parsed, err := s.tasks.parseRepo(r.PathValue("owner") + "/" + r.PathValue("name"))
	if err != nil {
		writeClientError(w, validationErrorf("repo: %v", err))
		return
	}
	if err := s.tasks.requireRepoPerson(r.Context(), parsed); err != nil {
		writeClientError(w, err)
		return
	}
	var req setRepoReleaseRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Enabled == nil && req.BaseBranchJobs == nil && req.PostMergeDispatchJobs == nil {
		writeError(w, http.StatusBadRequest,
			fmt.Errorf("enabled, baseBranchJobs or postMergeDispatchJobs is required"))
		return
	}
	repo := r.PathValue("owner") + "/" + r.PathValue("name")
	var out RepoReleaseSettings
	if req.Enabled != nil {
		if out, err = s.tasks.SetRepoReleaseEnabled(r.Context(), repo, *req.Enabled); err != nil {
			writeClientError(w, err)
			return
		}
	}
	if req.BaseBranchJobs != nil {
		if out, err = s.tasks.SetRepoBaseBranchCIJobs(r.Context(), repo, *req.BaseBranchJobs); err != nil {
			writeClientError(w, err)
			return
		}
	}
	if req.PostMergeDispatchJobs != nil {
		if out, err = s.tasks.SetRepoPostMergeDispatchJobs(r.Context(), repo, *req.PostMergeDispatchJobs); err != nil {
			writeClientError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// setRepoReleaseSecretRequest is PUT
// /api/repos/{owner}/{name}/release-secrets/{secret}'s body.
type setRepoReleaseSecretRequest struct {
	Value string `json:"value"`
}

func (s *Server) handleSetRepoReleaseSecret(w http.ResponseWriter, r *http.Request) {
	if !s.requireReleaseSecretAdmin(w, r) {
		return
	}
	if s.tasks.Config.Secrets == nil {
		writeError(w, http.StatusNotFound, errSecretsUnavailable)
		return
	}
	var req setRepoReleaseSecretRequest
	if !readJSON(w, r, &req) {
		return
	}
	out, err := s.tasks.SetRepoReleaseSecret(r.Context(), r.PathValue("owner")+"/"+r.PathValue("name"), r.PathValue("secret"), req.Value)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// requireReleaseSecretAdmin asks SetRepoReleaseSecret's and
// DeleteRepoReleaseSecret's own question -- repo admin of the path's
// repository -- before whether this deployment has a secrets store or
// what the body holds, so a caller who may not touch the repo's release
// secrets is told that and nothing about the deployment or their body.
func (s *Server) requireReleaseSecretAdmin(w http.ResponseWriter, r *http.Request) bool {
	repo, err := s.tasks.parseRepo(r.PathValue("owner") + "/" + r.PathValue("name"))
	if err != nil {
		writeClientError(w, validationErrorf("repo: %v", err))
		return false
	}
	if err := s.tasks.requireRepoPerson(r.Context(), repo); err != nil {
		writeClientError(w, err)
		return false
	}
	return true
}

func (s *Server) handleDeleteRepoReleaseSecret(w http.ResponseWriter, r *http.Request) {
	if !s.requireReleaseSecretAdmin(w, r) {
		return
	}
	if s.tasks.Config.Secrets == nil {
		writeError(w, http.StatusNotFound, errSecretsUnavailable)
		return
	}
	out, err := s.tasks.DeleteRepoReleaseSecret(r.Context(), r.PathValue("owner")+"/"+r.PathValue("name"), r.PathValue("secret"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleStartRepoRelease(w http.ResponseWriter, r *http.Request) {
	repo, err := s.tasks.parseRepo(r.PathValue("owner") + "/" + r.PathValue("name"))
	if err != nil {
		writeClientError(w, validationErrorf("repo: %v", err))
		return
	}
	// Asked before the body is read or the runner looked for, so a
	// caller who may not start a release is told that and nothing else.
	if err := s.tasks.requireRepoPerson(r.Context(), repo); err != nil {
		writeClientError(w, err)
		return
	}
	if s.tasks.Config.CI == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("this deployment has no grain CI wired"))
		return
	}
	var req RepoReleaseRunRequest
	if !readJSON(w, r, &req) {
		return
	}
	run, err := s.tasks.StartRepoRelease(r.Context(), repo.String(), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}
