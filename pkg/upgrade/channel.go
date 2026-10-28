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

package upgrade

// Which channel a deployment follows (grain/task-3689): the name an
// upgrade onto "the same thing, but newer" goes to. scripts/setup.sh
// installs a new deployment on latest and keeps an existing one on
// whatever it already runs, and the Upgrade pane, a scheduled upgrade
// and an agent's upgrade all default to the same answer, so a
// deployment that started on the last promoted release is offered the
// next promoted release rather than main's head.
//
// Read off the tag the deployment runs rather than configured with a
// flag of its own, for the reason Running is: the ref file (or the
// Kubernetes Deployment's spec) is what a restart brings up, so it is
// the one record of the channel that cannot disagree with the build.
// It is also the record an upgrade itself rewrites, so a deployment an
// operator moves onto candidate from the pane follows candidate from
// then on, and one setup.sh put on latest stays there.

import (
	"regexp"
	"slices"
	"strings"
)

// candidateTag is a release candidate's own tag, v1.7.0-rc.1 (or
// edition-qualified v2.0.0-rc.1-google), published by the cut beside
// candidate -- as versionTag's v1.6.0 is published by a promotion
// beside latest.
var candidateTag = regexp.MustCompile(`^v\d+\.\d+\.\d+(?:-(google|oss))?-rc\.\d+(?:-(google|oss))?$`)

func tagEdition(tag string) string {
	tag, _, _ = strings.Cut(tag, "@")
	if i := strings.LastIndex(tag, ":"); i > strings.LastIndex(tag, "/") {
		tag = tag[i+1:]
	}
	if m := versionTag.FindStringSubmatch(tag); m != nil {
		return m[4]
	}
	if m := candidateTag.FindStringSubmatch(tag); m != nil {
		if m[2] != "" {
			return m[2]
		}
		return m[1]
	}
	if _, build, ok := strings.Cut(tag, "+"); ok && (build == "google" || build == "oss") {
		return build
	}
	return ""
}

// ChannelForRef is the channel an image reference follows:
//
//   - latest, candidate (or rc) or main for those tags themselves;
//   - latest for a promoted release's own tag (v1.9.0), which is what
//     latest pointed at when it was installed;
//   - candidate for a release candidate's (v1.10.0-rc.1);
//   - the branch for any other branch's tag, with TagForBranch undone
//     (release..2.1 is release/2.1);
//   - "" for an immutable sha-<short sha> pin, a discarded RC tag
//     (v1.10.0-rc.1-discarded), or a reference with no tag at all,
//     none of which says what it would move to.
//
// A digest pinned beside the tag ("...:latest@sha256:...", what
// setup.sh writes once it has verified the image) is ignored: the tag
// is what was asked for, the digest only which build that was.
func ChannelForRef(ref string) string {
	ref, _, _ = strings.Cut(ref, "@")
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if colon <= slash {
		return ""
	}
	tag := ref[colon+1:]
	switch {
	case tag == "rc":
		return "candidate"
	case slices.Contains(standardChannels, tag):
		return tag
	case versionTag.MatchString(tag):
		return "latest"
	case candidateTag.MatchString(tag):
		return "candidate"
	case strings.HasPrefix(tag, "sha-") || strings.HasSuffix(tag, "-discarded"):
		return ""
	}
	branch := strings.ReplaceAll(tag, "..", "/")
	if validateBranch(branch) != nil {
		return ""
	}
	return branch
}

// Channel is the channel this deployment follows (ChannelForRef of what
// it runs), or "" when it cannot say: a deployment pinned to one build,
// one whose ref file cannot be read, or the source pipeline, which runs
// a binary it built rather than a tag anybody published.
func (u *Upgrader) Channel() string {
	if u.Mode() == ModeSource {
		return ""
	}
	running, err := u.Running()
	if err != nil {
		return ""
	}
	return ChannelForRef(running.Ref)
}
