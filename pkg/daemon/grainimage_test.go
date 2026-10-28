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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// Which image the deployment writes into the CI step of its own state
// repository, and in which order the two answers win.
//
// The default is this build's own reference rather than a tag that
// follows main, because `grain state check` refuses a dump stamped with
// a schema it does not know: a deployment held at an older tag whose
// check ran main's build would fail every pull request against its own
// state for a reason that had nothing to do with the change proposed in
// it. An operator's "checkImage" still wins, because they may have an
// answer this build cannot know -- a registry mirror, a tag of their own
// -- and because it is the documented way to hold that line still.
func TestTheCheckRunsThisBuildUnlessTheOperatorSaidOtherwise(t *testing.T) {
	t.Parallel()
	if got := checkImage(staterepo.Settings{}); got != DefaultGrainImage {
		t.Errorf("a deployment that has said nothing does not check its state with its own build: %q", got)
	}
	const theirs = "registry.example.internal/grain:sha-abc1234"
	if got := checkImage(staterepo.Settings{CheckImage: theirs}); got != theirs {
		t.Errorf("checkImage did not win: %q", got)
	}
}

// An unstamped build -- a `make build` on a laptop, a `go test` here --
// still has to name something real, since it may be the binary an
// operator runs `grain state ci` out of. The fallback is the tag CI
// keeps pointed at main, which is the answer grain gave for this before
// anything was stamped at all.
func TestAnUnstampedBuildStillNamesARealImage(t *testing.T) {
	t.Parallel()
	if DefaultGrainImage != staterepo.DefaultCheckImage {
		t.Errorf("the fallback is not the published default: %q", DefaultGrainImage)
	}
}

// What a release run is told this deployment runs, for cmd/ghcr-prune to
// keep: the build's own image, and the config check's image as well when
// an operator has pointed that somewhere else.
func TestDeployedImagesNameTheBuildAndTheCheckImage(t *testing.T) {
	previous := DefaultGrainImage
	DefaultGrainImage = "ghcr.io/bwsalmon/grain/grain:sha-7781f61"
	t.Cleanup(func() { DefaultGrainImage = previous })

	for _, tc := range []struct {
		name  string
		check func() string
		want  []string
	}{
		{"no check image wired", nil, []string{DefaultGrainImage}},
		{"the check runs this build", func() string { return DefaultGrainImage }, []string{DefaultGrainImage}},
		{"no operator answer", func() string { return " " }, []string{DefaultGrainImage}},
		{"an operator's own image", func() string { return "mirror.example/grain:pinned" },
			[]string{DefaultGrainImage, "mirror.example/grain:pinned"}},
	} {
		got := deployedImages(tc.check)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: deployedImages = %v, want %v", tc.name, got, tc.want)
		}
	}
}
