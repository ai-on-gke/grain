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

package model

import (
	"slices"
	"strings"
)

// The one piece of a release workflow grain will keep running after an
// operator switches releases off, without being asked to (grain/task-457).
//
// # Why there is a default here at all
//
// RepoConfig.PostMergeDispatchJobs exists because grain cannot tell what
// a release workflow produces by looking: a job that publishes an image
// a deployment resolves and a job that fills a cache later branches read
// are the same shape from outside, and only the operator knows which is
// which. So the jobs that must go on running are named, job by job
// (grain/task-409).
//
// That was right, and it left the operator doing the naming -- which for
// the case the whole thing was built for is a step nobody should have to
// take. Turning releases off because the publisher is broken is not a
// decision to make every branch's CI several times slower; it is a
// decision to stop publishing. The cache stopping too was a coupling
// nobody chose and, on 2026-09-20, a day of a saturated CI pool
// (docs/merge-queue-behind.md's "Publishing is also a CI input").
//
// # The one coupling grain can establish by itself
//
// It cannot read a release workflow. It can read the image its own
// sandboxes are created from: `<registry>/<owner>/<name>/guest` names
// the repository that publishes the guest this deployment runs, which
// is what GuestImageRepo below reads and what
// ui.SandboxGuestPublishing has reported since grain/task-397. For that
// one repository -- and no other -- grain knows that a piece of its
// release workflow is a build input this very deployment depends on.
//
// So the default is drawn as narrowly as that knowledge is:
//
//   - the repository publishes the sandbox guest *this deployment runs*,
//     read off the image reference rather than configured;
//   - its releases are switched off, so nothing else is filling the
//     cache -- with them on the release run does it, and a second
//     dispatch would be the same build twice (RepoConfig.PostMergeDispatchJobs
//     is not deduplicated against the release run, and says so);
//   - and its CI already holds `package-publish`, which is the operator
//     having granted this repository's jobs a credential that can write
//     to its packages. Grain adds no grant here and could not: the job
//     is dispatched under `workflow_dispatch` like any other
//     (packagepublish.SupportsCIEvent), and a repository whose CI was
//     never given that capability gets a job that stops on its first
//     step, so there is no point starting one.
//
// All three are already true of a repository the moment before its
// releases are switched off. That is the whole claim this default makes:
// not that grain may publish something new, but that the half of the
// merge that was never publishing -- a tag naming the hash of its own
// inputs, which nothing resolves unless a commit has those inputs --
// should not stop because the other half did.
//
// # How to turn it off
//
// By meaning it. Remove `package-publish` from the repository's CI
// capabilities and grain stops dispatching this, because the job could
// no longer push anyway; turn releases back on and grain stops too,
// because the release run has taken the work back. There is no third
// switch, on purpose: a switch for "do not keep my cache warm" would be
// one more thing to find, and the state it describes -- a repository
// whose CI may publish, whose releases are off, and which publishes the
// guest this deployment is running on right now -- is one where a cold
// cache has no upside to trade for.

// SandboxGuestCacheJob is the job that fills the content-addressed
// sandbox guest cache without publishing anything a deployment resolves
// (.grain/workflows/image.yml's own `guest-cache`, whose comment has
// what it does and does not push).
//
// Spelled plan-wide -- "<workflow path>:<job id>" -- because that is the
// form ci.Request.Jobs takes for a job that must be found in one
// particular workflow, and tests/deploy holds this exact string against
// the workflow files it has to match.
const SandboxGuestCacheJob = ".grain/workflows/image.yml:guest-cache"

// CapabilityPackagePublish is the capability that lets a CI job push to
// this repository's packages (pkg/capability/packagepublish, whose own
// CapabilityName is this constant).
//
// Named here, rather than reached for where it is implemented, because
// the decision below is about what an operator has already granted and
// pkg/model is under every package that asks. The provider owns what the
// grant *does*; this is only its spelling.
const CapabilityPackagePublish = "package-publish"

// GuestCacheFollowsBaseBranch reports whether grain fills this
// repository's sandbox guest cache after every merge of its own accord,
// without the job being named in RepoConfig.PostMergeDispatchJobs.
//
// guestImage is the reference this deployment's sandboxes are really
// created from (ui.SandboxImage.Image, orchestrator.Config.SandboxGuestImage);
// "" is a deployment with no sandbox image to attribute -- the
// host-directory backend -- and answers false for every repository.
//
// See this file's header for why the three conditions are these three.
// A repository that names the job itself is not this: it has asked, and
// EffectivePostMergeDispatchJobs will not name it twice.
func GuestCacheFollowsBaseBranch(cfg RepoConfig, guestImage string) bool {
	if RunsWorkItem(&cfg, ReleaseItem) {
		return false // the release run fills it on every merge
	}
	if !slices.Contains(cfg.CICapabilities, CapabilityPackagePublish) {
		return false // a job that cannot push is not worth dispatching
	}
	publisher, ok := GuestImageRepo(guestImage)
	return ok && publisher.Same(cfg.Repo)
}

// EffectivePostMergeDispatchJobs is what grain actually dispatches on
// this repository's base branch once it moves: the jobs the operator
// named, plus the sandbox guest cache job when
// GuestCacheFollowsBaseBranch applies and the list does not already
// name it.
//
// One function because two callers must agree: the reconciler that
// dispatches (orchestrator.startDueBaseBranchDispatch) and the pane that
// tells an operator whether the cache is following their base branch
// (ui.RepoReleaseSettings, ui.SandboxGuestPublishing). A pane deriving
// that separately is a pane that can be wrong about what grain is doing,
// which is the failure this whole area was filed about.
func EffectivePostMergeDispatchJobs(cfg RepoConfig, guestImage string) []string {
	jobs := slices.Clone(cfg.PostMergeDispatchJobs)
	if !GuestCacheFollowsBaseBranch(cfg, guestImage) {
		return jobs
	}
	if slices.Contains(jobs, SandboxGuestCacheJob) {
		return jobs // named explicitly; the default has nothing to add
	}
	return append(jobs, SandboxGuestCacheJob)
}

// GuestImageRepo is the repository that publishes a sandbox guest, read
// off the reference itself: `<registry>/<owner>/<name>/guest`, with any
// tag or digest taken off first.
//
// Read rather than configured so that a fork running its own guest is
// described by the fork's own settings and never by the upstream's, and
// so that a deployment running an image grain cannot attribute to any
// repository -- a mirror, a hand-built local tag -- is attributed to
// none rather than to a guess.
//
// The second result is false for exactly that case, and for the empty
// reference a deployment with no image at all has.
func GuestImageRepo(image string) (RepoRef, bool) {
	path := image
	// A digest first, because its `@sha256:...` also contains the colon a
	// tag is found by.
	if at := strings.Index(path, "@"); at >= 0 {
		path = path[:at]
	}
	if colon := strings.LastIndex(path, ":"); colon > strings.LastIndex(path, "/") {
		path = path[:colon]
	}
	parts := strings.Split(path, "/")
	if len(parts) != 4 || parts[3] != "guest" {
		return RepoRef{}, false
	}
	for _, part := range parts[:3] {
		if part == "" {
			return RepoRef{}, false
		}
	}
	return RepoRef{Owner: parts[1], Name: parts[2]}, true
}
