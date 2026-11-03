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
	"log"
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// The sandbox guest is a CI input, not a release artifact, and the
// switch that stops it being published is named for releases
// (grain/task-397).
//
// `.grain/workflows/release.yml` publishes three things on every merge:
// the grain image, the Kubernetes sandbox image, and -- through
// scripts/release-images.sh's guest phase -- `guest:inputs-<key>`, the
// content-addressed cache every job that resolves a sandbox guest reads.
// The first two are releases in the sense an operator would infer from
// RepoConfig.ReleaseWorkflowsDisabled's name. The third is not: it is
// what makes a branch's `real KVM sandboxes` job a `docker pull` instead
// of `granule guest build` booting a VM inside a CI sandbox and
// pulling ~1GB into it.
//
// So turning releases off costs every branch in flight tens of minutes
// on the longest job in the suite, and on 2026-09-20 it did: releases
// were switched off on the 19th to stop release runs stacking, 82
// merges landed on main in the day that followed, and every branch that
// merged main afterwards missed the cache and built a guest inline --
// 50 to 65 minutes each, against a pool of ten CI sandboxes, which
// saturated the pool and stacked seven pull requests behind one repair.
//
// Nothing anywhere connected the switch to that. The switch is in the
// repository pane, the consequence arrives in the CI queue an hour
// later, and the only reading that could have shown it -- the guest
// image read_deployment_health already reports -- said nothing about
// whether anything was still publishing one. This file is that missing
// line: the reading below rides on the sandbox health response, which is
// also what the deployment-health MCP tool hands an agent, and
// RepoReleaseSettings.PublishesSandboxGuest is the same fact put next to
// the switch itself.
//
// It reported rather than repaired, and the repair came separately
// (grain/task-409): a repository can now name the jobs grain dispatches
// on its base branch after every merge whatever its releases are doing
// (model.RepoConfig.PostMergeDispatchJobs), which for this repository is
// the guest cache job and nothing else. The reading below knows about
// that -- CacheDispatched -- because the question an operator is asking
// here is "is the cache following main", and with that setting on the
// answer is yes with releases off.
//
// And then it stopped needing to be asked (grain/task-457). Naming the
// job was still a step between an operator switching releases off and
// their CI staying fast, and the whole difficulty here is that nobody
// knows to take that step until the bill arrives. So for the one
// repository grain can establish the coupling for -- the one publishing
// the guest it is itself running, which is the same attribution this
// file already makes -- the cache job follows the base branch by
// default, while releases are off and while its CI holds the credential
// the job needs (model.GuestCacheFollowsBaseBranch).
//
// What that leaves for the reading below is the same job it always had,
// with the answer usually being yes: CacheDispatched is read from what
// grain will actually dispatch rather than from what the row names, so
// this can no longer report a cold cache grain is in fact keeping warm.
//
// # The switch being on is not the same as a guest coming out
//
// Everything above reads one operator setting, and until grain/task-230
// that setting was the whole of the reading: Publishing was `true`
// whenever releases were not switched off, and the sentence this file
// writes from it said, flatly, that merges publish the guest and the
// cache follows the branch. That sentence is an inference, and for two
// days in September it was wrong. `.grain/workflows/release.yml`'s
// `images` job failed on every merge to main from the 15th to the 19th
// -- eleven consecutive failures in five hours on the 18th alone --
// with releases switched on the whole time. Nothing published
// `guest:inputs-<key>` for three days, every branch in flight paid the
// inline ~1GB guest build for it, and this reading went on saying the
// cache was following main.
//
// So the reading now asks the second question too: not "is publishing
// switched on" but "did the last release run on this branch publish
// anything". grain already knows -- model.BaseBranchCI is the row the
// merge queue writes for exactly that run, and watchedBaseBranch was
// already reading it and keeping only the commit off it.
//
// Three of pkg/ci's four conclusions mean nothing was published, and
// this reading deliberately does not collapse them. A *failure* is a
// tree that is broken, and the merge queue already holds the branch and
// files a repair task for one (orchestrator's mergeStateBaseBroken).
// *errored* and *cancelled* earn neither: a release run lost to a daemon
// upgrade or cancelled by hand leaves the branch unheld, nobody named,
// no task filed, and no guest published, and the only trace of it is
// this line. That is the state this deployment's own main was in when
// this was written, so the gap is not hypothetical.

// SandboxGuestPublishing is whether the sandbox guest this deployment
// runs is still being published, and what it costs when it is not.
//
// It is reported per deployment rather than per repository because the
// guest is a per-deployment fact: there is one sandbox image, built and
// published by one repository's release workflow, and every task and
// every CI job in this deployment resolves it. Which repository that is
// is read off the image reference itself (guestImageRepo) rather than
// configured, so a fork publishing its own guest is described by its own
// settings and not by the upstream's.
type SandboxGuestPublishing struct {
	// Repo is the repository whose release workflow publishes this
	// guest, as `owner/name`. Empty when the image reference is not one
	// this deployment can attribute to a repository, which is also the
	// only state in which Publishing below means nothing.
	Repo string `json:"repo,omitempty"`
	// Image is the guest reference the reading is about -- what
	// sandboxes are actually created from (SandboxImage.Image), which on
	// a deployment that pins nothing is the stamp this build goes with.
	Image string `json:"image"`
	// Publishing is whether a merge into Repo still publishes a guest:
	// false exactly when that repository's release workflows are
	// switched off, which is the state this whole reading exists to make
	// visible.
	Publishing bool `json:"publishing"`
	// CacheDispatched is whether the content-addressed guest cache
	// follows Repo's base branch regardless of Publishing: the
	// repository names the cache job among the jobs grain dispatches
	// after every merge (model.RepoConfig.PostMergeDispatchJobs,
	// grain/task-409).
	//
	// It is the difference between "releases are off" and "the cache is
	// cold", which before that setting existed were the same thing and
	// are the reason this reading is here at all. With it true and
	// Publishing false, no merge publishes the moving tags a deployment
	// resolves -- `guest:latest` stays where it was -- but every branch
	// merging this base still finds its inputs cached, which is the
	// expensive half.
	CacheDispatched bool `json:"cacheDispatched"`
	// Branch and BranchCommit are the base branch grain watches for Repo
	// and the tip it last recorded for it (model.BaseBranchCI) -- the
	// tree whose guest inputs a consumer derives its cache key from.
	// Empty on a deployment that watches none of that repository's
	// branches, where "behind" has nothing to be behind.
	Branch       string `json:"branch,omitempty"`
	BranchCommit string `json:"branchCommit,omitempty"`
	// GuestCommit is the commit Image's own tag names -- the `sha-` tag
	// scripts/release-images.sh publishes and the Makefile stamps in.
	// Empty for a moving tag (`guest:latest`), which names no commit and
	// so cannot be compared with one.
	GuestCommit string `json:"guestCommit,omitempty"`
	// Behind is whether the guest this deployment resolves was built
	// from a commit that is not Branch's recorded tip.
	//
	// Read it as "the published guest is not this tree's", not as "the
	// cache is cold": most merges change none of the paths
	// scripts/granule/guest-inputs.sh names, so a guest a hundred commits
	// old is still the guest main's own key resolves to. What makes
	// Behind worth acting on is Publishing being false beside it --
	// nothing will publish a newer one, so the first merge that does
	// change a guest input leaves every branch building one inline.
	Behind bool `json:"behind"`
	// Stalled is whether the last release run recorded for Branch ended
	// without publishing anything: Publishing says the switch is on, and
	// this says whether a guest is actually coming out from under it.
	//
	// True for each of pkg/ci's three non-success conclusions, because
	// each of them leaves the registry with the same nothing in it -- the
	// push is the last step of the job and none of them reach it
	// (.grain/workflows/release.yml). LastPublish keeps the word itself,
	// since which one it was decides whether anybody is already driving
	// the repair.
	//
	// Only ever true beside Publishing: with releases switched off there
	// is no release run for a merge to fail, and the reading above is
	// already the whole story. A second sentence about the same silence
	// would only make the first one harder to act on.
	Stalled bool `json:"stalled"`
	// LastPublish is pkg/ci's own word for how that run ended --
	// "failure", "errored" or "cancelled" where Stalled is true, and
	// "success" where the last one published. Empty when no run has
	// finished on this branch yet, including while the first one is
	// still going: a run in flight has published nothing *yet*, which is
	// not the same claim and is not worth a warning.
	LastPublish string `json:"lastPublish,omitempty"`
	// LastPublishCommit is the commit that run was judging, which is the
	// commit whose images were not published. It is usually but not
	// always BranchCommit: merges that land while a release run is in
	// flight move the tip and are judged by the next run (model.
	// BaseBranchCI's own header), so a stalled run can name a commit the
	// branch has already moved past.
	LastPublishCommit string `json:"lastPublishCommit,omitempty"`
	// LastPublishAt is when that conclusion was recorded -- how long this
	// deployment has been publishing nothing, which is the number that
	// turns "a release failed" into "a release has been failing since
	// Tuesday" and is the whole difference between a flake and an outage
	// nobody is driving.
	LastPublishAt *time.Time `json:"lastPublishAt,omitempty"`
	// LastPublishDetail is that run's verdict in words -- which jobs
	// failed (model.BaseBranchCI.Detail) -- so this line names what to go
	// and read rather than only that there is something to read.
	LastPublishDetail string `json:"lastPublishDetail,omitempty"`
	// CacheDispatch is pkg/ci's own word for how the last post-merge
	// dispatch that carried the guest cache job ended on Branch --
	// "success", "failure", "errored" or "cancelled"
	// (model.BaseBranchDispatch). Empty where no such dispatch has
	// finished, including while the first one is still going, for
	// LastPublish's reason: a run in flight has filled nothing *yet*,
	// which is not the same claim.
	//
	// CacheDispatched says grain will start one; this says what came of
	// the last one it started. They are a pair for the same reason
	// Publishing and Stalled are: the whole of this file's header is
	// that a switch being in the right position is not a guest coming
	// out, and the dispatch grain makes of its own accord is a second
	// place that gap can open. It is the state this deployment's own
	// main was in on 2026-09-21 -- releases off, the dispatch following
	// main exactly as designed, and the one it made for main's tip
	// errored on a sandbox that never came up -- and nothing read the
	// row that recorded it (model.BaseBranchDispatch.Conclusion: "it is
	// written down so that 'the cache is cold' has somewhere to be read
	// from").
	CacheDispatch string `json:"cacheDispatch,omitempty"`
	// CacheStalled is whether that dispatch ended without filling the
	// cache -- any conclusion but success, for publishStalled's reason.
	//
	// Unlike Stalled this is not held to Publishing being on: the
	// dispatch is what fills the cache when publishing is off, which is
	// precisely when its failure is the whole of the silence.
	CacheStalled bool `json:"cacheStalled"`
	// CacheDispatchCommit is the tip that dispatch was made for, and
	// CacheDispatchAt when its conclusion was recorded. The commit is
	// worth naming because nothing retries a dispatch for the same one
	// (model.BaseBranchDispatch): it is the tree whose key was not
	// filled, and the branch's next merge is what asks again.
	CacheDispatchCommit string     `json:"cacheDispatchCommit,omitempty"`
	CacheDispatchAt     *time.Time `json:"cacheDispatchAt,omitempty"`
	// CacheDispatchDetail is that run's own words
	// (model.BaseBranchDispatch.Detail), so this line names what to go
	// and read rather than only that there is something to read.
	CacheDispatchDetail string `json:"cacheDispatchDetail,omitempty"`
	// Detail is the same reading in the words an operator needs, because
	// this is a fact three readings away from its consequence and nobody
	// reconstructed it from the fields alone the first time.
	Detail string `json:"detail"`
}

// guestCacheDispatch is how the cache is filled without turning releases
// back on: one job, on the branch whose inputs are missed, and it
// publishes only the content-addressed names
// (.grain/workflows/image.yml's `guest-cache`, whose own comment has the
// invocation and why it publishes nothing a deployment resolves).
//
// Named in pkg/model because grain now dispatches it of its own accord
// for the repository publishing this deployment's guest, so the decision
// and this reading of it have to be the same expression
// (model.EffectivePostMergeDispatchJobs).
const guestCacheDispatch = model.SandboxGuestCacheJob

// sandboxGuestPublishing is the reading GET /api/sandboxes carries, and
// through it read_deployment_health.
//
// nil where there is nothing to say: no sandbox image to name (a
// host-directory backend, a UI not colocated with a pool), no store to
// read the repository's settings out of, or an image reference this
// deployment cannot attribute to a repository. Those are three different
// silences, and each of them is also "no line in the pane", which is the
// same nil-means-unavailable contract the rest of this response gives.
func (c *Client) sandboxGuestPublishing(ctx context.Context) *SandboxGuestPublishing {
	if c.sandboxImage() == nil || c.Store == nil {
		return nil
	}
	image := c.sandboxGuestImage()
	repo, ok := guestImageRepo(image)
	if !ok {
		return nil
	}
	out := SandboxGuestPublishing{
		Repo: repo.String(), Image: image, Publishing: true,
		GuestCommit: guestImageCommit(image),
	}
	cfg, err := c.Store.GetRepoConfig(ctx, repo)
	if err != nil {
		// A reading that cannot be taken is left out rather than
		// answered with its zero value: "publishing: true" invented from
		// a failed read is the same silence this section exists to end.
		log.Printf("ui: reading %s's release settings for the sandbox guest reading: %v", repo, err)
		return nil
	}
	if cfg != nil {
		out.Publishing = model.RunsWorkItem(cfg, model.ReleaseItem)
		// What grain will actually dispatch, not what the row names:
		// for the repository publishing this deployment's own guest the
		// cache job follows the branch whether or not anybody named it
		// (model.GuestCacheFollowsBaseBranch, grain/task-457), and a
		// reading that answered from the row alone would tell an
		// operator the cache was cold while grain was filling it.
		out.CacheDispatched = slices.Contains(
			model.EffectivePostMergeDispatchJobs(*cfg, image), guestCacheDispatch)
	}
	if base := c.watchedBaseBranch(ctx, repo); base != nil {
		out.Branch, out.BranchCommit = base.Branch, base.Commit
		// The verdict on that branch is only about publishing where
		// publishing is what the run was for: see Stalled's own comment
		// for why releases being off is not also reported as a run that
		// published nothing.
		if out.Publishing {
			out.LastPublish = base.Conclusion
			out.Stalled = publishStalled(base.Conclusion)
		}
		if out.Stalled {
			out.LastPublishCommit, out.LastPublishAt = base.Commit, base.FinishedAt
			out.LastPublishDetail = base.Detail
		}
		// And the other publisher of the same cache: the dispatch grain
		// makes after a merge, whose failures nothing read until now.
		if dispatch := c.lastGuestCacheDispatch(ctx, repo, base.Branch); dispatch != nil {
			out.CacheDispatch = dispatch.Conclusion
			out.CacheStalled = publishStalled(dispatch.Conclusion)
			out.CacheDispatchCommit, out.CacheDispatchAt = dispatch.Commit, dispatch.FinishedAt
			out.CacheDispatchDetail = dispatch.Detail
		}
	}
	out.Behind = out.GuestCommit != "" && out.BranchCommit != "" &&
		!strings.HasPrefix(out.BranchCommit, out.GuestCommit)
	out.Detail = guestPublishingDetail(out)
	return &out
}

// publishStalled is whether a base-branch run's conclusion means nothing
// was published for the commit it judged.
//
// Every conclusion that is not a success, and the pending one is not a
// conclusion: a run still going has not failed to publish, it has not
// published yet. Written as "anything but success" rather than as a list
// of the three, so that a conclusion pkg/ci grows later is read as "no
// guest came out of it" -- which is the safe way round for a reading
// whose whole job is to stop a silence being mistaken for a success.
func publishStalled(conclusion string) bool {
	return conclusion != "" && conclusion != string(ci.ConclusionSuccess)
}

// publishesSandboxGuest is whether one repository's release workflow is
// what publishes the guest this deployment's sandboxes are built from.
//
// The same attribution sandboxGuestPublishing makes, asked the other way
// round: there it names the repository whose switch to read, and here a
// repository asks whether its own switch is the one. A deployment with
// no sandbox image to name answers false for every repository, which is
// one line fewer in the pane rather than a warning about nothing.
func (c *Client) publishesSandboxGuest(repo model.RepoRef) bool {
	publisher, ok := guestImageRepo(c.sandboxGuestImage())
	return ok && publisher.Same(repo)
}

func (c *Client) sandboxImage() *SandboxImage {
	if c == nil {
		return nil
	}
	if c.Config.SandboxImageFunc != nil {
		return c.Config.SandboxImageFunc()
	}
	return c.Config.SandboxImage
}

// sandboxGuestImage is the guest reference every reading here is about:
// what sandboxes are really created from, falling back to what this
// build is stamped for when the deployment could not say. "" for a
// deployment with no sandbox image at all, which attributes the guest to
// no repository and is how the host-directory backend answers.
func (c *Client) sandboxGuestImage() string {
	img := c.sandboxImage()
	if img == nil {
		return ""
	}
	if img.Image != "" {
		return img.Image
	}
	return img.Expected
}

// watchedBaseBranch is grain's own row for the base branch it is
// watching for one repository: the tip it last recorded there, and what
// its CI made of it.
//
// That row is the release run. A base-branch run on this repository
// plans `release.yml` and nothing else (it is the only workflow
// answering the event), so "the last base-branch verdict" and "the last
// attempt to publish a guest" are one fact, and reading it here costs
// nothing beyond the query the branch name already needed.
//
// Most repositories have exactly one such row. Where there are several
// -- a repository whose merges land on more than one base branch -- the
// most recently started run's branch is the one reported, because that
// is the branch whose tree this deployment's own sandboxes are being
// built against right now, and a reading that named all of them would be
// a list nobody can compare one image against.
//
// nil where there is no such row, which is a deployment watching none of
// that repository's branches: "behind" has nothing to be behind and no
// release run has been recorded to have failed.
func (c *Client) watchedBaseBranch(ctx context.Context, repo model.RepoRef) *model.BaseBranchCI {
	rows, err := c.Store.ListBaseBranchCI(ctx)
	if err != nil {
		log.Printf("ui: listing watched base branches for the sandbox guest reading: %v", err)
		return nil
	}
	var newest *model.BaseBranchCI
	for i := range rows {
		if !rows[i].Repo.Same(repo) {
			continue
		}
		if newest == nil || rows[i].StartedAt.After(newest.StartedAt) {
			newest = &rows[i]
		}
	}
	return newest
}

// lastGuestCacheDispatch is grain's own row for the last post-merge
// dispatch it made on one base branch, where that dispatch carried the
// guest cache job (model.BaseBranchDispatch,
// orchestrator.reconcileBaseBranchDispatch).
//
// nil for the three states that are not "the cache dispatch ended badly":
// no dispatch has been made on that branch, the row is still in flight,
// or the jobs it was made with do not include the guest cache one --
// which is a repository that names some other post-merge job and has
// nothing to say about a guest either way. The row's other jobs are not
// read: what this reading is about is the guest, and a repository whose
// dispatch failed *while* carrying the guest job has a cold cache
// however the rest of it went.
func (c *Client) lastGuestCacheDispatch(ctx context.Context, repo model.RepoRef, branch string) *model.BaseBranchDispatch {
	row, err := c.Store.GetBaseBranchDispatch(ctx, repo, branch)
	if err != nil {
		log.Printf("ui: reading %s's post-merge dispatch for the sandbox guest reading: %v", repo, err)
		return nil
	}
	if row == nil || row.InFlight() || row.Conclusion == "" {
		return nil
	}
	if !slices.Contains(row.Jobs, guestCacheDispatch) {
		return nil
	}
	return row
}

// guestPublishingDetail writes the reading out in the sentence that was
// missing, which is the whole point of this section: the fields say what
// is true, and this says what it costs.
func guestPublishingDetail(g SandboxGuestPublishing) string {
	var b strings.Builder
	if g.Publishing && g.Stalled {
		return appendCacheDispatchStalled(guestPublishStalledDetail(g), g)
	}
	if g.Publishing {
		b.WriteString("Merges into " + g.Repo + " publish the sandbox guest, so the content-addressed guest cache follows it.")
		switch {
		case g.Behind:
			b.WriteString(" This deployment runs the guest built from " + g.GuestCommit +
				", which is not " + g.Branch + "'s tip (" + shortCommit(g.BranchCommit) +
				") -- an ordinary gap, since most merges change none of the paths " +
				"scripts/granule/guest-inputs.sh names and a guest is only rebuilt when one does.")
		case g.GuestCommit != "" && g.BranchCommit != "":
			b.WriteString(" It is the guest built from " + g.GuestCommit +
				", which is " + g.Branch + "'s recorded tip.")
		}
		return appendCacheDispatchStalled(b.String(), g)
	}
	if g.CacheDispatched && g.CacheStalled {
		// The one state this reading used to describe as healthy while
		// the cache was going cold: releases off, the dispatch grain
		// makes instead following the branch exactly as designed, and
		// the last one it made not having filled anything.
		return "Release workflows are switched off for " + g.Repo +
			", so no merge publishes the moving guest tags, and " + guestCacheDispatch +
			" is what fills the content-addressed guest cache in their place. " +
			guestCacheStalledDetail(g)
	}
	if g.CacheDispatched {
		// Releases off, and the cache still following the branch: the
		// one state where the switch above costs a deployment nothing
		// it can feel, and worth saying in those words rather than
		// leaving an operator to read "publishing: false" as the outage
		// it used to be (grain/task-409).
		b.WriteString("Release workflows are switched off for " + g.Repo +
			", so no merge publishes the moving guest tags -- but " + guestCacheDispatch +
			" is dispatched on ")
		if g.Branch != "" {
			b.WriteString(g.Branch)
		} else {
			b.WriteString("its base branch")
		}
		b.WriteString(" after every merge, so the content-addressed guest cache goes on following" +
			" it and a branch whose inputs are unchanged still pulls a guest rather than building" +
			" one. This deployment goes on running the guest it already resolves")
		if g.GuestCommit != "" {
			b.WriteString(", built from " + g.GuestCommit)
		}
		b.WriteString(", until a release publishes a newer one.")
		// The evidence for the claim above rather than the claim alone:
		// a dispatch that ran and filled the cache, and when. The
		// sentence before this one is an inference from a setting, and
		// this file exists because that kind of inference was wrong for
		// three days in September.
		if g.CacheDispatch == string(ci.ConclusionSuccess) && g.CacheDispatchAt != nil {
			b.WriteString(" The last one ran for " + shortCommit(g.CacheDispatchCommit) +
				" and filled it on " + g.CacheDispatchAt.UTC().Format("2006-01-02 15:04Z") + ".")
		}
		return b.String()
	}
	b.WriteString("Release workflows are switched off for " + g.Repo +
		", so no merge publishes a sandbox guest: the guest cache stops following ")
	if g.Branch != "" {
		b.WriteString(g.Branch)
	} else {
		b.WriteString("its base branch")
	}
	b.WriteString(". That switch is named for releases, but the cache it also stops filling is a CI input" +
		" -- every job that resolves a guest for a tree whose inputs are not already cached builds one" +
		" inline instead, ~1GB inside a nested VM, tens of minutes on one of this deployment's CI" +
		" sandboxes, on every branch in flight at once.")
	if g.Behind {
		b.WriteString(" This deployment still runs the guest built from " + g.GuestCommit +
			"; " + g.Branch + " is at " + shortCommit(g.BranchCommit) + ".")
	}
	b.WriteString(" Naming " + guestCacheDispatch + " among the jobs dispatched after a merge" +
		" (the repository's release pane) is what makes the cache follow this branch again without" +
		" turning releases back on; dispatching it by hand fills it once, for one branch's current" +
		" inputs.")
	return b.String()
}

// appendCacheDispatchStalled puts the cache-dispatch sentence after a
// reading that was about publishing, where there is one to put.
//
// The two are separate publishers of the same cache -- a release run and
// a post-merge dispatch -- and a repository can have both (releases on,
// with the cache job named in RepoConfig.PostMergeDispatchJobs). Where
// it does, the dispatch failing is its own silence and gets its own
// sentence rather than being folded into the release reading, which is
// about a different run with a different conclusion.
func appendCacheDispatchStalled(detail string, g SandboxGuestPublishing) string {
	if !g.CacheStalled {
		return detail
	}
	return detail + " " + guestCacheStalledDetail(g)
}

// guestCacheStalledDetail is the sentence for a post-merge dispatch of
// the guest cache job that ended without filling it.
//
// It says the same three things guestPublishStalledDetail does, in the
// same order and for the same reason -- what did not happen, what that
// costs while it has not, and what will and will not put it right --
// with one difference that is the whole reason this sentence exists
// separately: nothing at all reacts to this one. A failed release run
// holds the branch and files a repair task; a failed dispatch is
// recorded on its row and left, deliberately, because re-dispatching a
// 25-minute guest build every cycle would spend a deployment's whole CI
// capacity (model.BaseBranchDispatch.Conclusion). So the only thing that
// asks again is the branch's next merge, and until one lands the reader
// of this line is the only one who knows.
func guestCacheStalledDetail(g SandboxGuestPublishing) string {
	var b strings.Builder
	b.WriteString("The last " + guestCacheDispatch + " grain dispatched on ")
	if g.Branch != "" {
		b.WriteString(g.Branch)
	} else {
		b.WriteString("its base branch")
	}
	if g.CacheDispatchCommit != "" {
		b.WriteString(" (" + shortCommit(g.CacheDispatchCommit) + ")")
	}
	b.WriteString(" " + stalledVerb(g.CacheDispatch) + ", so nothing filled the" +
		" content-addressed guest cache for that tree")
	if g.CacheDispatchAt != nil {
		b.WriteString(" -- the last word on it is from " +
			g.CacheDispatchAt.UTC().Format("2006-01-02 15:04Z"))
	}
	b.WriteString(". Nothing retries a dispatch for the same commit, so until that branch" +
		" merges again every job resolving a guest whose inputs are not already cached builds" +
		" one inline instead, ~1GB inside a nested VM, tens of minutes on one of this" +
		" deployment's CI sandboxes, on every branch in flight at once.")
	if g.CacheDispatchDetail != "" {
		b.WriteString(" That run: " + g.CacheDispatchDetail + ".")
	}
	b.WriteString(" Nothing holds the branch and no repair task was filed for it -- a dispatch" +
		" is not a verdict on the tree. Dispatching it by hand fills the cache once, for this" +
		" branch's current inputs: grain ci run -repo " + g.Repo + " -branch ")
	if g.Branch != "" {
		b.WriteString(g.Branch)
	} else {
		b.WriteString("<branch>")
	}
	b.WriteString(" -job " + guestCacheDispatch + ".")
	return b.String()
}

// guestPublishStalledDetail is the sentence for a deployment whose
// release switch is on and whose releases are not coming out: the state
// this reading was blind to, and the one it was blind to for three days
// (this file's header).
//
// It says three things in this order, because that is the order the
// reader needs them in: that publishing is not happening despite the
// switch, what it is costing while it does not, and who -- if anyone --
// is already on it. The last of those is the point of separating a
// failure from the other two conclusions. A failed run has the merge
// queue holding the branch and a repair task filed against the merge
// that broke it, so the useful thing to say is "that is already
// happening, here is what it found". An errored or cancelled one has
// none of that: nothing holds the branch, nobody is named, and the
// reader of this line is the only person who knows, so it ends by
// naming the one call that starts another.
func guestPublishStalledDetail(g SandboxGuestPublishing) string {
	var b strings.Builder
	b.WriteString("Release workflows are switched on for " + g.Repo +
		", but the last release run on ")
	if g.Branch != "" {
		b.WriteString(g.Branch)
	} else {
		b.WriteString("its base branch")
	}
	if g.LastPublishCommit != "" {
		b.WriteString(" (" + shortCommit(g.LastPublishCommit) + ")")
	}
	b.WriteString(" " + stalledVerb(g.LastPublish) + ", so it published nothing")
	if g.LastPublishAt != nil {
		b.WriteString(" -- nothing has been published since " +
			g.LastPublishAt.UTC().Format("2006-01-02 15:04Z"))
	}
	b.WriteString(". The push is the last step of that job, so a run that does not reach it" +
		" publishes no image and no `guest:inputs-` cache entry at all: while this is the last" +
		" word on that branch, every job that resolves a guest for a tree whose inputs are not" +
		" already cached builds one inline instead, ~1GB inside a nested VM, tens of minutes on" +
		" one of this deployment's CI sandboxes.")
	if g.Behind && g.GuestCommit != "" {
		b.WriteString(" This deployment still runs the guest built from " + g.GuestCommit +
			"; " + g.Branch + " is at " + shortCommit(g.BranchCommit) + ".")
	}
	if g.LastPublishDetail != "" {
		b.WriteString(" That run: " + g.LastPublishDetail + ".")
	}
	if g.LastPublish == string(ci.ConclusionFailure) {
		b.WriteString(" A failure holds the branch and files a repair task naming the merge that" +
			" broke it, so that is already being driven -- this line is here because the cost" +
			" above lands on every branch in flight meanwhile, and not only on the one being" +
			" repaired.")
		return b.String()
	}
	// errored or cancelled: the tree is not accused of anything, so
	// nothing in the merge queue reacts and nobody has been told.
	b.WriteString(" That is not a verdict on the tree, so nothing holds the branch and no repair" +
		" task was filed for it: a release lost to a daemon restart or cancelled by hand leaves" +
		" this line as the only trace. Starting another is POST /api/repos/" + g.Repo +
		"/release-runs.")
	return b.String()
}

// stalledVerb is how a run that published nothing ended, in words that
// say which of the three it was without making a reader match a status
// word to a meaning. "failed" is a tree; the other two are not, and the
// sentence around this one turns on that difference.
func stalledVerb(conclusion string) string {
	switch conclusion {
	case string(ci.ConclusionFailure):
		return "failed"
	case string(ci.ConclusionErrored):
		return "never ran -- grain could not get it onto a machine"
	case string(ci.ConclusionCancelled):
		return "was cancelled before it finished"
	default:
		return "ended as " + conclusion
	}
}

// shortCommit is a commit as a person reads one back, and as the rest of
// grain's own logs print it.
func shortCommit(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

// guestImageRepo is the repository whose release workflow publishes a
// sandbox guest, read off the image reference.
//
// grain publishes its guest beside its other two images, under the
// repository's own package namespace -- `<registry>/<owner>/<name>/guest`
// (scripts/release-images.sh) -- so the reference names the repository,
// and a fork's deployment is described by the fork's settings rather
// than the upstream's. Anything else is not attributed at all: guessing
// a repository from a reference this deployment did not publish would
// put another repository's release switch in front of an operator as the
// cause of their own slow CI.
//
// It lives in pkg/model now, because the orchestrator asks the same
// question before dispatching the guest cache job and the two must not
// be able to attribute one image to two repositories.
var guestImageRepo = model.GuestImageRepo

// guestImageCommit is the commit a guest reference names, for the `sha-`
// tags scripts/release-images.sh publishes and the Makefile stamps in
// (pkg/cli/sandboximage.go). Empty for a moving tag, which names a
// branch rather than a commit and so cannot be compared with one, and
// for a bare `repo@sha256:...`, which names no tag at all. A release
// stamps `guest:sha-<short>@sha256:...`, whose tag still says which
// commit the digest was built from.
func guestImageCommit(image string) string {
	image, _, _ = strings.Cut(image, "@")
	colon := strings.LastIndex(image, ":")
	if colon <= strings.LastIndex(image, "/") {
		return ""
	}
	tag := image[colon+1:]
	if !strings.HasPrefix(tag, "sha-") {
		return ""
	}
	return strings.TrimPrefix(tag, "sha-")
}
