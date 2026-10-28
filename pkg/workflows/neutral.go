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

package workflows

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// This file holds the pieces of pkg/ci the parser and planner need that
// are not themselves parsing or planning: the artifact and priority
// types a Request and a Job carry, the name checks the parser applies,
// the environment a step is given (which is part of a job's key), and
// the guest-cache key. They moved here with the parser so that it stays
// neutral (docs/design/grit-split.md, "Neutral packages"); pkg/ci
// re-exports each of them under its old name.

// LifecycleImageEnv is model.LifecycleImageEnv, restated because this
// package may not import pkg/model; pkg/model's tests hold the two
// spellings together.
const LifecycleImageEnv = "GRAIN_LIFECYCLE_IMAGE"

// CapabilitySyntax is what the parser needs to know about capability
// names whose grammar is grain's (pkg/model, pkg/capability/protocol)
// rather than this package's.
type CapabilitySyntax struct {
	// CheckGrant checks a capability reference's syntax
	// (model.CheckCapabilityGrantSyntax).
	CheckGrant func(ref string) error
	// DecodeList reads a `capabilities:` sequence, whose entries may be
	// names or reference grants with arguments (model.CapabilityList).
	DecodeList func(node *yaml.Node) ([]string, error)
	// IsRepoAccess reports whether a name is `repo-read:` or
	// `repo-write:` repository access (ci.IsRepoAccessCapability).
	IsRepoAccess func(id string) bool
	// DeclaredOnly reports whether a job's declaring id is what grants
	// it, subject to an authority other than the repository's CI
	// capabilities list (pkg/ci's declaredOnly): repository access,
	// capability references and api grants. JobCapabilities and
	// UnmetRequirements read it.
	DeclaredOnly func(id string) bool
}

// Capabilities is the CapabilitySyntax the parser applies. pkg/ci
// installs grain's when it is linked, which every grain binary does.
// What it holds before then accepts plain names only and checks nothing
// of a reference's grammar: deciding what a capability means is grain's
// business, not the executor's (docs/design/grit-split.md, "Secrets and
// trust").
var Capabilities = CapabilitySyntax{
	CheckGrant: func(string) error { return nil },
	DecodeList: func(node *yaml.Node) ([]string, error) {
		var list []string
		if err := node.Decode(&list); err != nil {
			return nil, errors.New("capabilities: expected a list of names")
		}
		return list, nil
	},
	IsRepoAccess: isRepoAccess,
	DeclaredOnly: func(id string) bool { return isRepoAccess(id) || IsCapabilityRef(id) },
}

func isRepoAccess(id string) bool {
	return strings.HasPrefix(id, "repo-read:") || strings.HasPrefix(id, "repo-write:")
}

// IsCapabilityRef is model.IsCapabilityRef: a capability reference rather
// than an in-process capability name. Names never contain "://", so the
// scheme alone tells them apart.
func IsCapabilityRef(s string) bool {
	return strings.HasPrefix(strings.TrimSpace(s), "https://")
}

// ArtifactSpec is one entry of a job's `artifacts:` list: what to call
// the thing that is kept, and which paths of the workspace go into it.
//
// The keys are `name` and `path`, which is what GitHub's
// actions/upload-artifact calls them -- this format uses GitHub's word
// wherever GitHub has one -- and `path` takes one path or several, the
// way that action's own multi-line `path:` does.
type ArtifactSpec struct {
	// Name is what this artifact is called: unique within the job, and
	// part of the file a download arrives as. ArtifactNamePattern is what
	// it may hold.
	Name string
	// Paths are what goes into it, relative to the workspace the
	// repository was checked out into (a step's `working-directory` has
	// nothing to do with it -- a job's artifacts are declared once for
	// the job, and the steps that produced them may each have run
	// somewhere different).
	//
	// A path that does not exist when the job finishes is not an error:
	// the ones that do exist are collected, and a declaration none of
	// whose paths exist is recorded as an artifact with nothing in it and
	// a note saying so -- which is GitHub's `if-no-files-found: warn`,
	// and the right answer for a test suite that only writes its
	// directory when something fails.
	Paths []string
}

// ArtifactNamePattern is what an artifact may be called. It is narrow on
// purpose: the name is part of a filename on the daemon's disk and part
// of a URL path, and every character outside this set is one more thing
// two layers have to agree about escaping.
var ArtifactNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]*$`)

// Priority is how one job ranks against the others waiting at a Gate:
// higher goes first. It is a bare number on purpose, and it is the whole
// of what pkg/ci knows about why one job matters more than another --
// that a branch is at the head of a merge queue, or that its run has jobs
// finished already, is decided a layer up, and a gate that understood
// those reasons would be pkg/ci reaching into pkg/orchestrator.
//
// The scale is deliberately small, because age is measured in the same
// units: SharedGate gives a waiter one step of rank for every AgeBoost it
// has been queueing, so a job ranked N steps below the work ahead of it
// overtakes that work after N*AgeBoost of waiting. Ranks a hundred apart
// would be a priority order with no anti-starvation floor left in it.
type Priority int

const (
	// PriorityNormal is the rank of a job nobody has said anything about.
	PriorityNormal Priority = 0
	// PriorityUnderway is what Engine adds for a run that has finished
	// jobs already -- finish what is started before starting more. Four
	// branches sharing the deployment fairly all finish at T; running
	// them one after another finishes the first at T/4, and a finished
	// run is a merge rather than progress (grain/task-294's argument, one
	// layer down).
	PriorityUnderway Priority = 1
	// PriorityMergeHead is what the change at the front of its
	// repository's merge queue is worth: its verdict is the one that
	// unblocks everything queued behind it, so CI time spent on it
	// converts into merges rather than into progress. cmd/grain is what
	// knows which change that is; pkg/ci only knows the number.
	//
	// Four steps rather than one, and DefaultAgeBoost apart: an ordinary
	// job queued behind a merge-queue head overtakes it after four
	// boosts, which is the escalation this scale promises.
	PriorityMergeHead Priority = 4
	// PriorityReleaseGate is CI a trunk release is judged on: the
	// post-merge suite and release run on a repository's default branch,
	// and the scheduled suites declared as release gates (nightly,
	// release-train). Their verdicts are what a release candidate is
	// selected on, and on 2026-10-22 a nightly at PriorityNormal was
	// starved and then OOM-cancelled by ordinary task load
	// (docs/design/release-from-trunk.md, RFT-2). It ranks above a
	// merge-queue head and below a repair, and it is the lowest rank that
	// may spend the repair reserve (MaySpendReserve) and that has a hole
	// held for it the moment the budget refuses it (ReservesAtOnce). It
	// stays below PriorityRepair even with PriorityUnderway added.
	PriorityReleaseGate Priority = 6
	// PriorityRepair is CI a red base branch's repair is waiting on: the
	// base branch's own confirmation run, and the run on the repair's
	// own branch (cmd/grain is what knows which those are). It ranks
	// above a merge-queue head, since the head cannot merge onto a red
	// base either, and with PriorityReleaseGate it is a rank that may
	// spend the budget's repair reserve (model.Budget.RepairReserve,
	// grain/task-488) and that does not stand aside for a waiting agent
	// run (MaySpendReserve).
	PriorityRepair Priority = 8
)

// MaySpendReserve reports whether a job at this rank may be admitted into
// the budget's repair reserve (model.Budget.RepairReserve) and is exempt
// from standing aside for a waiting agent run: a repair, and a release
// gate (PriorityReleaseGate).
func (p Priority) MaySpendReserve() bool { return p >= PriorityReleaseGate }

// ReservesAtOnce reports whether a job at this rank has capacity held for
// it from the first refusal rather than after SharedGate.ReserveAfter --
// so a 4 vCPU / 16 GiB release-gate job holds the hole as jobs drain
// instead of being leapfrogged by 1 vCPU jobs ranked below it. An
// operator who has turned reservation off (a negative ReserveAfter) has
// turned it off for these too.
func (p Priority) ReservesAtOnce() bool { return p >= PriorityReleaseGate }

var releaseSecretName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// ValidateReleaseSecretName keeps custom names usable by ordinary release
// scripts and, more importantly, safe as a single file name.
func ValidateReleaseSecretName(name string) error {
	if !releaseSecretName.MatchString(name) {
		return fmt.Errorf("%q is not a release secret name: use uppercase letters, digits, and underscores, starting with a letter or underscore", name)
	}
	return nil
}

// validateRequirementName is what the parser applies to one entry of
// `capabilities:` or `secrets:`. It checks the shape of a name and
// nothing about whether it exists: a workflow naming a capability this
// deployment has never heard of is a job that does not run and says
// which name it wanted, which is a better error than a parse failure
// that takes the whole workflow -- and every other job in it -- down
// with it.
func validateRequirementName(kind, name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%s: an empty name requires nothing", kind)
	}
	if name != strings.TrimSpace(name) {
		return fmt.Errorf("%s: %q has leading or trailing space", kind, name)
	}
	return nil
}

// DeployedImagesEnv is the variable a release job finds the images of
// the running deployment in, comma-separated (Request.DeployedImages).
const DeployedImagesEnv = "GRAIN_DEPLOYED_IMAGES"

// RequestedByKindEnv, RequestedByEnv and ReleaseVersionEnv are the
// variables a job started by a person-only route finds who asked, and
// for which release, in (Request.RequestedByKind, Request.ReleaseVersion).
const (
	RequestedByKindEnv = "GRAIN_CI_REQUESTED_BY_KIND"
	RequestedByEnv     = "GRAIN_CI_REQUESTED_BY"
	ReleaseVersionEnv  = "GRAIN_CI_RELEASE_VERSION"
)

// RunEnv is what grain tells every step about the run it is part of --
// the GITHUB_* variables' counterpart, spelled GRAIN_CI_* because they
// are not the same variables and a script that reads GITHUB_SHA here
// should find nothing rather than something subtly different.
//
// CI=true is the exception, and is there because it is not grain's
// variable at all: it is the one every test runner, linter and build
// tool on earth already checks.
//
// It takes a Request and two strings rather than the Plan and the
// PlannedJob it is called with here, because Fingerprint calls it too:
// what grain tells a job is part of what that job does, so the whole of
// this map goes into the job's key (see jobFingerprint.Env). A variable
// added here is therefore covered by the key the moment it is added,
// which is the only version of that rule nobody can forget to apply.
func RunEnv(req Request, workflow, jobID string) map[string]string {
	env := map[string]string{
		"CI":                "true",
		"GRAIN_CI":          "true",
		"GRAIN_CI_REPO":     req.Repo,
		"GRAIN_CI_REF":      req.Ref,
		"GRAIN_CI_COMMIT":   req.Commit,
		"GRAIN_CI_EVENT":    string(req.Event),
		"GRAIN_CI_WORKFLOW": workflow,
		"GRAIN_CI_JOB":      jobID,
	}
	// Only for a repository that declares a lifecycle, so that a job in
	// one that does not finds nothing rather than an empty string it
	// might `docker push` to. Spelled without the CI_ infix because it is
	// not a fact about this run: it is the repository's own declaration,
	// read out of its `.grain/lifecycle.yml`.
	if req.LifecycleImage != "" {
		env[LifecycleImageEnv] = req.LifecycleImage
	}
	if len(req.DeployedImages) > 0 {
		env[DeployedImagesEnv] = strings.Join(req.DeployedImages, ",")
	}
	// Only when grain established who asked (Request.RequestedByKind), so
	// a job no person-only route started finds nothing rather than an
	// empty kind it might compare against, and every other job's key is
	// what it always was.
	if req.RequestedByKind != "" {
		env[RequestedByKindEnv] = req.RequestedByKind
		env[RequestedByEnv] = req.RequestedBy
	}
	if req.ReleaseVersion != "" {
		env[ReleaseVersionEnv] = req.ReleaseVersion
	}
	return env
}

// guestCacheKeyVersion is bumped when the *meaning* of a guest key
// changes, for fingerprintVersion's reason: it is part of every key, so
// bumping it makes every key in the flight book below a key nobody has
// warmed rather than one somebody warmed under different rules. It is
// separate from fingerprintVersion because the two answer different
// questions -- that one is "is this the same work", this one is "is this
// the same image" -- and a cache-key change should not make every
// deployment warm every guest again.
const guestCacheKeyVersion = 1

// GuestCacheKey is the identity of the guest a tree builds: the repository
// and the content of the files the job declared its guest is built from.
//
// The repository is in it because the cache is per repository -- the tag
// lives under that repository's own package namespace
// (scripts/release-images.sh) -- so two repositories with identical files
// have two guests, and a warm of one is not a warm of the other.
//
// The paths travel with the hashes, for Content's reason in
// jobFingerprint: a guest input *appearing* or *vanishing* changes what
// the build produces even when every file that stayed is byte for byte
// what it was.
func GuestCacheKey(repo string, files []TreeFile) Key {
	content := make([]string, 0, len(files))
	for _, f := range files {
		content = append(content, f.Path+"\x00"+f.Blob)
	}
	return HashOf("guest", struct {
		Version int      `json:"v"`
		Repo    string   `json:"repo"`
		Content []string `json:"content"`
	}{Version: guestCacheKeyVersion, Repo: repo, Content: content})
}
