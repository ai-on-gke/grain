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

// The default that keeps a deployment's sandbox guest cache warm after
// its publisher's releases are switched off (grain/task-457).
//
// The interesting assertions here are the refusals. Adding a job to a
// repository's post-merge dispatch list is grain spending that
// repository's CI, under an event that admits a publishing credential,
// on nobody's explicit instruction -- so what has to be pinned is that
// it happens in exactly one situation and evaporates the moment any part
// of that situation stops being true.
package model_test

import (
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

var guestPublisher = model.RepoRef{Owner: "acme", Name: "widgets"}

// ourGuest is the image a deployment running guestPublisher's own
// published guest is built from.
const ourGuest = "ghcr.io/acme/widgets/guest:latest"

// releasesOff is the row the release toggle leaves behind: publishing
// stopped, and the CI capability that did the publishing still granted,
// because nobody revoked it.
func releasesOff() model.RepoConfig {
	return model.RepoConfig{
		Repo:           guestPublisher,
		CICapabilities: []string{model.CapabilityPackagePublish},
		Work:           model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
	}
}

func TestTheGuestCacheJobIsAddedForTheRepositoryPublishingOurGuest(t *testing.T) {
	t.Parallel()
	if !model.GuestCacheFollowsBaseBranch(releasesOff(), ourGuest) {
		t.Fatal("the cache does not follow the base branch of the repository publishing the guest " +
			"this deployment runs, with its releases off: that is the state the whole default is for")
	}
	got := model.EffectivePostMergeDispatchJobs(releasesOff(), ourGuest)
	if !slices.Equal(got, []string{model.SandboxGuestCacheJob}) {
		t.Fatalf("grain would dispatch %v, want %s alone", got, model.SandboxGuestCacheJob)
	}
}

// Each of the three conditions, removed one at a time. See the comment
// on this file: what matters is that the default is this narrow, not
// that it is convenient.
func TestTheGuestCacheDefaultHoldsOnlyWhereItIsSafe(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		cfg   func(*model.RepoConfig)
		image string
		why   string
	}{{
		name:  "releases still publishing",
		cfg:   func(c *model.RepoConfig) { c.Work = model.WorkOwnership{} },
		image: ourGuest,
		why: "the release run fills this cache on every merge, so dispatching it too is the " +
			"same ~1GB guest built twice",
	}, {
		name:  "no package-publish granted to its CI",
		cfg:   func(c *model.RepoConfig) { c.CICapabilities = nil },
		image: ourGuest,
		why: "grain may not decide a repository's CI should hold a credential to write its " +
			"packages; the job would stop on its first step anyway",
	}, {
		name:  "somebody else's guest",
		cfg:   func(*model.RepoConfig) {},
		image: "ghcr.io/other/project/guest:latest",
		why: "the inference is about the guest this deployment depends on, and says nothing " +
			"about release workflows in general",
	}, {
		name:  "no sandbox image at all",
		cfg:   func(*model.RepoConfig) {},
		image: "",
		why:   "a host-directory backend has no guest, so there is no cache to keep warm",
	}, {
		name:  "an image naming no repository",
		cfg:   func(*model.RepoConfig) {},
		image: "localhost:5000/handbuilt:dev",
		why: "guessing a repository from a reference this deployment did not publish would " +
			"spend some unrelated repository's CI",
	}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			cfg := releasesOff()
			c.cfg(&cfg)
			if model.GuestCacheFollowsBaseBranch(cfg, c.image) {
				t.Fatalf("the guest cache default fires with %s: %s", c.name, c.why)
			}
			if got := model.EffectivePostMergeDispatchJobs(cfg, c.image); len(got) != 0 {
				t.Fatalf("grain would dispatch %v with %s: %s", got, c.name, c.why)
			}
		})
	}
}

// A repository that names the job itself gets it once, and a repository
// that names other jobs keeps them: the default adds, it does not
// replace, and it does not double up.
func TestTheGuestCacheDefaultAddsToWhatWasNamedWithoutRepeatingIt(t *testing.T) {
	t.Parallel()
	named := releasesOff()
	named.PostMergeDispatchJobs = []string{model.SandboxGuestCacheJob}
	if got := model.EffectivePostMergeDispatchJobs(named, ourGuest); len(got) != 1 {
		t.Fatalf("a repository naming the cache job itself would have it dispatched as %v: "+
			"the same job twice in one request is a plan that asks for it twice", got)
	}

	other := releasesOff()
	other.PostMergeDispatchJobs = []string{"warm-something-else"}
	got := model.EffectivePostMergeDispatchJobs(other, ourGuest)
	if !slices.Equal(got, []string{"warm-something-else", model.SandboxGuestCacheJob}) {
		t.Fatalf("grain would dispatch %v: naming one job is not a way of declining the "+
			"guest cache, and the operator's own job must survive the default", got)
	}
}

// The caller must not be able to grow the stored slice out from under
// the row it came from: RepoConfig is read from the store and handed
// round by value, and a default that appended in place would edit it.
func TestTheEffectiveJobsDoNotAliasTheConfiguredOnes(t *testing.T) {
	t.Parallel()
	cfg := releasesOff()
	cfg.PostMergeDispatchJobs = make([]string, 1, 4)
	cfg.PostMergeDispatchJobs[0] = "warm-something-else"
	if got := model.EffectivePostMergeDispatchJobs(cfg, ourGuest); len(got) != 2 {
		t.Fatalf("grain would dispatch %v, want the named job and the cache job", got)
	}
	if len(cfg.PostMergeDispatchJobs) != 1 {
		t.Fatalf("the repository's own list grew to %v: what grain adds for itself must not "+
			"look like something the operator asked for", cfg.PostMergeDispatchJobs)
	}
}

func TestGuestImageRepoReadsThePublisherOffTheReference(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		image string
		want  string
	}{
		{"ghcr.io/acme/widgets/guest:latest", "acme/widgets"},
		{"ghcr.io/acme/widgets/guest:sha-abc1234", "acme/widgets"},
		{"ghcr.io/acme/widgets/guest@sha256:" + zeros(64), "acme/widgets"},
		{"ghcr.io/acme/widgets/guest", "acme/widgets"},
		// Not attributable, and each for its own reason: the wrong depth,
		// the wrong last element, an empty element, and nothing at all.
		{"ghcr.io/acme/guest:latest", ""},
		{"ghcr.io/acme/widgets/runtime:latest", ""},
		{"ghcr.io//widgets/guest:latest", ""},
		{"", ""},
	} {
		got, ok := model.GuestImageRepo(c.image)
		if c.want == "" {
			if ok {
				t.Errorf("%q was attributed to %s, want no repository", c.image, got)
			}
			continue
		}
		if !ok || got.String() != c.want {
			t.Errorf("%q was attributed to %q (ok=%v), want %s", c.image, got, ok, c.want)
		}
	}
}

func zeros(n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = '0'
	}
	return string(out)
}
