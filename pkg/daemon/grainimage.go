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

package daemon

import (
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/version"
)

// DefaultGrainImage is overwritten at link time. The value here is the
// fallback for a build that does not stamp one -- a plain `make build`
// on a laptop, or a `docker build` with no --build-arg -- and is
// staterepo.DefaultCheckImage, the tag CI keeps pointed at main, so that
// the answer an unstamped build gives is exactly the one grain gave
// before anything was stamped at all. Written as a reference to that
// constant rather than as a second copy of the string: `-X` needs a
// variable initialised to a constant expression, and this is one.
var DefaultGrainImage = staterepo.DefaultCheckImage

// checkImage is the image grain writes into the CI step of its own state
// repository: what the operator asked for, or the build that is running.
//
// An operator's own answer wins because they may have one this build
// cannot know -- a mirror of the registry, a tag of their own -- and
// because "checkImage" in state-repo.json is the documented way to hold
// that line still.
func checkImage(settings staterepo.Settings) string {
	if settings.CheckImage != "" {
		return settings.CheckImage
	}
	return DefaultGrainImage
}

// buildIdentity is how this deployment names the build it is running,
// for the one commit message in its state repository that has to say a
// deployment was upgraded and which build it was upgraded from
// (staterepo.Config.Build).
//
// The image reference, because that is the name an operator upgrades
// *by* -- it is what `grain image` prints, what the CI step in the state
// repository is pointed at, and what a rollback names -- and the commit
// that image was built from, because two deployments can run the same
// moving tag on different days and a reviewer's next move is to read the
// difference between the two builds. checkImage is deliberately not used
// here: an operator may have pinned that to something this build is not.
//
// An unstamped binary -- `make build` on a laptop, a `go build
// -buildvcs=false`, any test binary -- has neither, and this reports
// only the image it falls back to. That is the same string across every
// such build, which is exactly right: the message it decides is "this is
// a different build from the one that wrote the last export", and an
// unstamped build has no evidence of that either way.
func buildIdentity() string {
	info := version.Get()
	if info.Revision == "" {
		return DefaultGrainImage
	}
	revision := info.Revision
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if info.Modified {
		revision += ", with uncommitted changes"
	}
	return DefaultGrainImage + " (built from " + revision + ")"
}
