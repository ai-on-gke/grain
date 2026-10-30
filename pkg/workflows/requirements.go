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
	"fmt"
	"slices"
	"strings"
)

// A job says what it needs; a deployment says what it will give; and
// the two meet here, once, at the moment the job would otherwise start
// (grain/task-486).
//
// # A requirement is not a grant
//
// These used to be one thing. `RepoConfig.CICapabilities` was
// simultaneously "what this repository's jobs need" and "what they are
// given", which is why a repository naming a capability in a file read
// as a repository handing itself a credential -- and why the only
// available answer was to refuse the whole idea of a repository saying
// anything about capabilities at all.
//
// It dissolves once the two are spelled differently. A job declaring
// `capabilities: [gcp-key]` is stating that its steps cannot do their
// work without one; it is not producing a key, and a repository that
// writes the words into a file on a branch has gained exactly nothing.
// The grant is a separate act, made by the deployment, in grain's own
// settings, by somebody who can see every repository they are granting
// for -- and pkg/declschema still refuses a repository that column,
// with its reason rewritten to say which of the two halves it is
// refusing.
//
// # Why the declaration belongs on the job
//
// Because "this repository needs `gcp-key`" is much less useful than
// "this job needs `gcp-key`". The first can only be answered by
// stopping the repository; the second lets the other six jobs of the
// suite run and tells somebody precisely which one is stuck and why.
//
// # What happens when it cannot be met
//
// The job does not run, and says so in those words. That is the whole
// point of the exercise rather than a detail of it: what used to happen
// is that a capability quietly went missing from a repository's row and
// the jobs needing it ran anyway, without it, and failed several
// minutes later inside a step that named something else entirely. A run
// that says "this job requires `gemini-key` and this deployment does
// not provide it for this repository" costs no sandbox, blames the
// right thing, and is actionable by the one person who can act on it.
//
// # The residual question: permission
//
// A deployment may hold `gcp-key` and still not want an arbitrary
// repository drawing on it. That question is deliberately *not*
// answered in this file, and not in the declaration either: the
// deployment already answers it, per repository, in
// `RepoConfig.CICapabilities` -- a repository whose row does not name a
// capability does not get it, whatever its jobs declare. What is not
// built is a second, finer permission layer, because today's behaviour
// is the honest default (a deployment configured to work on a
// repository lets that repository's jobs use what it has granted that
// repository) and because the per-repository row is already the place
// such a layer would go. It belongs beside the ownership default, not
// in the declaration, if it is ever wanted.
//
// # Repository access is the exception
//
// `repo-read:` and `repo-write:` are not the deployment's to give
// (grain/task-1654). Whether a repository may be reached is already
// decided elsewhere, by its owner on GitHub, in what each team's
// credential can do. So for those two, the declaration in the repository
// is the allowance and the team's credential is the authority: a job's
// own `repo-read:`/`repo-write:` is met by being declared, the
// per-repository list is not consulted for them, and cmd/grain's
// ciSandbox puts the job's git requests through the proxy as its team.
// The untrusted-event gate on repo-write (pkg/capability/repowrite) is
// unchanged: that protects against untrusted code, which is a different
// question from whose approval a repository needs.
//
// # Optional capabilities
//
// `optional-capabilities:` is the third answer a job can give, between
// "I cannot work without it" and silence: "I use it when it is there".
// It exists for one shape of job -- grain's own release, which publishes
// images and signs them when it has a key -- and for the moment that
// shape met a deployment older than the key (grain/task-2016): a job
// *requiring* `image-signing` could not run on a build with no such
// provider, and that job is the only thing that publishes the build
// that would have one.
//
// So an optional capability is placed exactly when a required one would
// be -- the deployment provides it for this repository -- and when it
// is not, nothing is placed and nothing is refused. A name this
// deployment has never heard of is the same case as one it does not
// grant: absent. It still narrows: a job that declares optional
// capabilities and no required ones is given what it named and nothing
// else, because "a job gets only what it declares" has to hold for
// every way of declaring.
//
// A deployment older than the key cannot read it at all -- the parser
// refuses a key it does not know (yaml.go) -- so a workflow that must
// run on such a build cannot use it yet, whatever it would mean there.

// JobCapabilities is what a job's sandbox is actually given: what the
// deployment provides for this repository, narrowed to what the job
// said it needs.
//
// A job whose `capabilities:` is present but empty (`capabilities: []`,
// Job.NoCapabilities) has declared that it needs none of it, and gets
// none of it -- only what it lists under `optional-capabilities:` and
// the deployment provides. That is how a job that uses no credential
// stops queueing for (and minting) every finite one its repository is
// granted.
//
// A job that declares nothing gets the whole provided set, which is
// what every job got before the key existed. That is a compatibility
// rule and it is also the honest one: a job that has not said what it
// needs has not said it needs less, and silently taking a credential
// away from a workflow that was working would be the same class of
// silent loss this change exists to stop.
//
// A job that does declare is narrowed to its declaration, and that is
// worth having on its own: it is the difference between every job in a
// suite queueing for a finite credential and only the one that uses it
// doing so (Admission.Acquire reads this same list).
//
// Repository access is the exception on both sides (grain/task-1654).
// A `repo-read:` or `repo-write:` the job declares is kept whatever the
// deployment provides, because the declaration in the repository is the
// allowance and the team's own GitHub credential is the authority on
// whether it holds (cmd/grain's ciSandbox puts the job's git requests
// through the proxy as its team). And one the deployment's per-repository
// list names is never handed to a job that did not declare it: an
// operator's entry there is no longer what grants a repository, so a job
// that never asked gets no access from it.
//
// A capability reference (model.IsCapabilityRef) is kept and dropped the
// same way, for the same reason: the job's declaration is what asks for
// it and the deployment's capability policy is what decides, when the
// job's grants are minted (docs/capabilities-spec.md §4.2.2), so neither
// the repository's CI capabilities list nor its absence there says
// anything about it.
//
// The one exception to "a job that declares nothing gets the whole set"
// is a scarce capability (explicitOnly reports one; the daemon answers
// it from model.IsScarceResourceProvider): a finite external credential,
// such as a GCP service-account key, that admission mints before the
// job's sandbox exists. Handing one to every undeclared job minted and
// deleted a key per job of every run -- about fourteen per tests.yml
// run, repeated on every re-run -- which is what ran this deployment
// into IAM's per-minute write quota (grain/task-281). A job that uses
// one says so under `capabilities:` or `optional-capabilities:`.
// explicitOnly nil treats nothing as scarce.
func JobCapabilities(job Job, provided []string, explicitOnly func(string) bool) []string {
	var declared, rest []string
	for _, id := range job.Capabilities {
		if Capabilities.DeclaredOnly(id) {
			declared = append(declared, id)
		} else {
			rest = append(rest, id)
		}
	}
	var deployment []string
	for _, id := range provided {
		if !Capabilities.DeclaredOnly(id) {
			deployment = append(deployment, id)
		}
	}
	// Optional ones are placed only where the deployment provides them,
	// whatever narrow would do with an empty list.
	var optional []string
	for _, id := range job.OptionalCapabilities {
		if slices.Contains(deployment, id) {
			optional = append(optional, id)
		}
	}
	if len(rest) == 0 && (len(declared) > 0 || len(job.OptionalCapabilities) > 0 || job.NoCapabilities) {
		// A job that declared only repository access, or only what it
		// can do without, or `capabilities: []`, has said what it
		// needs, and it is not the deployment's whole set.
		return append(declared, optional...)
	}
	if len(rest) == 0 && explicitOnly != nil {
		// Declared nothing: the deployment's set, less what a job has to
		// ask for by name.
		implicit := deployment[:0:0]
		for _, id := range deployment {
			if !explicitOnly(id) {
				implicit = append(implicit, id)
			}
		}
		deployment = implicit
	}
	return append(append(declared, narrow(rest, deployment)...), optional...)
}

// JobSecrets is what release secret names a job's sandbox is given:
// the provided set narrowed to what the job declared in `secrets:`.
//
// Unlike JobCapabilities, a job that declares nothing gets nothing. The
// compatibility rule above is reasonable for capabilities, which a
// repository's row grants to its CI as a whole; it is not reasonable
// for release secrets -- signing keys and publish tokens -- which an
// event that carries them (a release, a workflow_dispatch) would
// otherwise place into every job of every workflow that answers it,
// including ones written on a branch nobody has reviewed (issue #2389).
// A job that uses a release secret says so by name, and only that job
// is handed it.
func JobSecrets(job Job, provided []string) []string {
	if len(job.Secrets) == 0 {
		return nil
	}
	return narrow(job.Secrets, provided)
}

func narrow(required, provided []string) []string {
	if len(required) == 0 {
		return slices.Clone(provided)
	}
	var out []string
	for _, name := range required {
		if slices.Contains(provided, name) {
			out = append(out, name)
		}
	}
	return out
}

// UnmetRequirements is what this job requires and this deployment does
// not provide for this repository, one sentence per requirement,
// already in the words the job's record will carry.
//
// It is checked before anything is spent on the job -- before the
// cache, before the guest warm, before the grant admission and both
// sandbox gates -- because the answer does not depend on any of them
// and because a job that cannot run should cost nothing to find that
// out about.
//
// Deliberately not consulted: whether a *provider* is configured for a
// capability the repository's row does name, and whether that provider
// will materialize for this event. Both are questions only the daemon's
// capability registry can answer, both already produce their own errors
// where they are asked, and duplicating either here would mean two
// places that can disagree about what a deployment provides.
func UnmetRequirements(job Job, req Request) []string {
	var out []string
	if req.UntrustedCommit() != "" {
		// Said first and instead of the configuration sentence: the
		// repository may well grant every one of these, and sending
		// somebody to its settings would be sending them to the wrong
		// place.
		for _, id := range job.Capabilities {
			// A reference is the capability policy's to refuse, which
			// denies it to an untrusted run unless a rule opts it in
			// (SandboxRequest.Untrusted).
			if IsCapabilityRef(id) {
				continue
			}
			out = append(out, fmt.Sprintf("this job requires the capability %q, and %s", id, req.UntrustedCommit()))
		}
		for _, name := range job.Secrets {
			out = append(out, fmt.Sprintf("this job requires the release secret %q, and %s", name, req.UntrustedCommit()))
		}
		return out
	}
	for _, id := range job.Capabilities {
		if req.WithholdEventGated {
			if IsCapabilityRef(id) || strings.HasPrefix(id, "repo-read:") {
				continue
			}
			if !slices.Contains(req.Capabilities, id) {
				reason := req.WithholdReason
				if reason == "" {
					reason = fmt.Sprintf("a %s run started by a caller that is not a person places no capabilities", req.Event)
				}
				out = append(out, fmt.Sprintf("this job requires the capability %q, and %s", id, reason))
			}
			continue
		}
		// Repository access is met by the declaration itself; what the
		// team's credential can actually do is GitHub's to answer, when
		// the job's git asks (JobCapabilities). A capability reference
		// is the capability policy's to answer, when it is minted.
		if Capabilities.DeclaredOnly(id) {
			continue
		}
		if !slices.Contains(req.Capabilities, id) {
			out = append(out, fmt.Sprintf("this job requires the capability %q and this deployment does not provide it for %s", id, req.Repo))
		}
	}
	for _, name := range job.Secrets {
		if slices.Contains(req.ReleaseSecrets, name) {
			continue
		}
		// A push run carries no release secrets at all, whatever the
		// repository has configured (Service.Start), so a job that
		// needs one and is reached by a push is not a configuration
		// gap -- it is a job asked for under an event that can never
		// carry what it needs, and saying "this deployment does not
		// provide it" would send whoever reads it to the wrong place.
		switch {
		case req.EventDefaulted():
			out = append(out, fmt.Sprintf("this job requires the release secret %q, and a run that names no event places no release secrets -- ask for a %s run explicitly", name, EventManual))
		case req.WithholdReleaseSecrets && req.WithholdReason != "":
			out = append(out, fmt.Sprintf("this job requires the release secret %q, and %s", name, req.WithholdReason))
		case req.WithholdReleaseSecrets:
			out = append(out, fmt.Sprintf("this job requires the release secret %q, and a run grain starts to warm the sandbox guest cache places no release secrets", name))
		case req.Event == EventRelease || req.Event == EventManual:
			out = append(out, fmt.Sprintf("this job requires the release secret %q and this deployment does not provide it for %s", name, req.Repo))
		default:
			out = append(out, fmt.Sprintf("this job requires the release secret %q, and a %s run places no release secrets -- only a release or an explicit manual run does", name, req.Event))
		}
	}
	return out
}

// UnmetRequirementsError joins those sentences into the one line a
// job's record carries.
func UnmetRequirementsError(unmet []string) string {
	return strings.Join(unmet, "; ")
}
