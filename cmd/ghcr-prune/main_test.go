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

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeAPI is the packages API as a map, so the tests below drive the
// whole command -- listing, planning, reporting, deleting -- with no
// HTTP anywhere. api_test.go is where the real request shapes are
// pinned.
type fakeAPI struct {
	versions map[string][]Version
	branches map[string]bool
	deleted  []int64
	refuse   map[int64]error
	err      error
}

func (f *fakeAPI) ListVersions(pkg string) ([]Version, error) { return f.versions[pkg], f.err }
func (f *fakeAPI) ListBranchTags() (map[string]bool, error)   { return f.branches, f.err }
func (f *fakeAPI) DeleteVersion(pkg string, id int64) error {
	if err := f.refuse[id]; err != nil {
		return err
	}
	f.deleted = append(f.deleted, id)
	return nil
}

func fixture() *fakeAPI {
	return &fakeAPI{
		branches: map[string]bool{"main": true},
		versions: map[string][]Version{
			"grain": {
				{Package: "grain", ID: 1, Digest: "sha256:1", Tags: []string{"latest", "main", "sha-1111111"}, Created: daysAgo(1)},
				{Package: "grain", ID: 2, Digest: "sha256:2", Tags: []string{"sha-2222222", "grain-task-1"}, Created: daysAgo(400)},
				// Enough newer builds that version 2 is past the
				// count-based half of the commit rule as well as the
				// age window: what it is being judged on is that its
				// branch is gone.
				{Package: "grain", ID: 4, Digest: "sha256:4", Tags: []string{"sha-4444444"}, Created: daysAgo(2)},
				{Package: "grain", ID: 5, Digest: "sha256:5", Tags: []string{"sha-5555555"}, Created: daysAgo(3)},
			},
			"guest": {
				{Package: "guest", ID: 3, Digest: "sha256:3", Tags: []string{"latest", "main", "sha-1111111", "inputs-aaaaaaaaaaaaaaaa"}, Created: daysAgo(1)},
			},
		},
	}
}

// The default is a report and nothing else: deleting a package version
// cannot be undone, so -apply is a thing somebody has to ask for.
func TestWithoutApplyNothingIsDeleted(t *testing.T) {
	api := fixture()
	var out bytes.Buffer
	if err := prune(api, []string{"grain", "guest"}, policy(), false, now, &out); err != nil {
		t.Fatal(err)
	}
	if len(api.deleted) != 0 {
		t.Errorf("a dry run deleted %v", api.deleted)
	}
	if !strings.Contains(out.String(), "would delete 1 versions") {
		t.Errorf("a dry run should say what it would have done:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "dry run") {
		t.Errorf("a dry run should say that it was one:\n%s", out.String())
	}
}

func TestApplyDeletesExactlyThePlan(t *testing.T) {
	api := fixture()
	var out bytes.Buffer
	if err := prune(api, []string{"grain", "guest"}, policy(), true, now, &out); err != nil {
		t.Fatal(err)
	}
	if len(api.deleted) != 1 || api.deleted[0] != 2 {
		t.Fatalf("deleted %v, want just the 400-day-old build of a deleted branch (2)", api.deleted)
	}
	// A run that got through its whole plan has nothing to warn about,
	// which is the other half of the annotation below meaning anything.
	if strings.Contains(out.String(), "::warning") {
		t.Errorf("a run that deleted everything it planned to raised a warning:\n%s", out.String())
	}
}

// A cap that held versions back is an annotation, not just a line in the
// body of the report.
//
// It is the one failure here that no later run clears by itself: -deferred
// falling to zero next time means it was a backlog, and -deferred coming
// back every run means CI publishes faster than the cap deletes and the
// registry is growing regardless of this job. The report says so a few
// hundred lines in, which is a place nobody reads until they already
// suspect it -- and this workflow spent its whole life sized for a tenth
// of the build rate without anything saying a word.
func TestVersionsHeldBackByTheCapAreAnnotated(t *testing.T) {
	api := fixture()
	capped := policy()
	capped.MaxDeletes = 0
	var out bytes.Buffer
	if err := prune(api, []string{"grain", "guest"}, capped, true, now, &out); err != nil {
		t.Fatal(err)
	}
	if len(api.deleted) != 0 {
		t.Errorf("deleted %v against a cap of 0", api.deleted)
	}
	if !strings.Contains(out.String(), "::warning title=ghcr-prune::1 deletable versions were held back by -max-deletes (0)") {
		t.Errorf("a deferred version should be an annotation somebody sees:\n%s", out.String())
	}
}

// A public version GitHub will not delete for anybody (over 5,000
// downloads) must not turn the schedule permanently red -- no later run
// can clear it -- while a credential that cannot delete packages at all
// must fail loudly. The two are told apart by status, in api.go.
func TestARefusedDeletionIsWarnedAboutAndSteppedOver(t *testing.T) {
	api := fixture()
	api.versions["grain"] = append(api.versions["grain"], Version{
		Package: "grain", ID: 9, Digest: "sha256:9",
		Tags: []string{"sha-9999999", "grain-task-2"}, Created: daysAgo(401),
	})
	api.refuse = map[int64]error{2: &ErrRefused{Status: 400, Body: "too many downloads"}}
	var out bytes.Buffer
	if err := prune(api, []string{"grain", "guest"}, policy(), true, now, &out); err != nil {
		t.Fatalf("a refusal failed the run: %v", err)
	}
	if len(api.deleted) != 1 || api.deleted[0] != 9 {
		t.Errorf("deleted %v, want the run to carry on to 9 after 2 was refused", api.deleted)
	}
	if !strings.Contains(out.String(), "::warning") {
		t.Errorf("a refusal should be an annotation somebody sees:\n%s", out.String())
	}
}

func TestAnEmptyBranchListingStopsTheRun(t *testing.T) {
	api := fixture()
	api.branches = map[string]bool{}
	var out bytes.Buffer
	err := prune(api, []string{"grain"}, policy(), true, now, &out)
	if err == nil {
		t.Fatal("pruned against an empty branch listing, which would make every branch tag look dead")
	}
	if len(api.deleted) != 0 {
		t.Errorf("deleted %v before stopping", api.deleted)
	}
}

// The report is the only account anyone has of why a version is still
// there, so it names the tag that saved each one.
func TestTheReportSaysWhatIsHoldingEachVersion(t *testing.T) {
	api := fixture()
	var out bytes.Buffer
	if err := prune(api, []string{"grain", "guest"}, policy(), false, now, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		// Why each survivor survived...
		"pushed 24h ago, inside the 90d window",
		// ...and, for the one version going, every tag it takes with
		// it, which is the half a reader has to be able to check.
		"no live tag: sha-2222222 (400d), grain-task-1 (400d)",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report does not contain %q:\n%s", want, out.String())
		}
	}
}

func TestRunResolvesRepoAndTokenFromEnvironmentAndPlacements(t *testing.T) {
	tempDir := t.TempDir()
	origSandboxToken := sandboxTokenPath
	origPublishToken := publishTokenPath
	defer func() {
		sandboxTokenPath = origSandboxToken
		publishTokenPath = origPublishToken
	}()

	sandboxTokenPath = filepath.Join(tempDir, ".github-sandbox-token")
	publishTokenPath = filepath.Join(tempDir, ".package-publish-token")

	t.Run("fails when RELEASE_IMAGE_REPO is malformed and flags missing", func(t *testing.T) {
		t.Setenv("RELEASE_IMAGE_REPO", "invalid-no-slash")
		t.Setenv("GITHUB_TOKEN", "fake-token")
		var out bytes.Buffer
		err := run([]string{"-apply=false"}, &out)
		if err == nil || !strings.Contains(err.Error(), "are required") {
			t.Fatalf("expected missing repo error, got: %v", err)
		}
	})

	t.Run("fails when token missing", func(t *testing.T) {
		t.Setenv("RELEASE_IMAGE_REPO", "")
		t.Setenv("GRAIN_CI_REPO", "ai-on-gke/grain")
		t.Setenv("GITHUB_TOKEN", "")
		var out bytes.Buffer
		err := run([]string{"-apply=false"}, &out)
		if err == nil || !strings.Contains(err.Error(), "GITHUB_TOKEN is empty") {
			t.Fatalf("expected missing token error, got: %v", err)
		}
	})

	t.Run("defaults repo to bwsalmon/grain and resolves token from sandboxTokenPath", func(t *testing.T) {
		t.Setenv("RELEASE_IMAGE_REPO", "")
		t.Setenv("GRAIN_CI_REPO", "ai-on-gke/grain")
		t.Setenv("GITHUB_TOKEN", "")
		if err := os.WriteFile(sandboxTokenPath, []byte("ghs_test_token_123\n"), 0600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		// run will attempt API call and fail on network/unauthorized, but repo/token parsing succeeded
		err := run([]string{"-apply=false"}, &out)
		if err != nil && (strings.Contains(err.Error(), "are required") || strings.Contains(err.Error(), "GITHUB_TOKEN is empty")) {
			t.Fatalf("repo/token resolution failed unexpectedly: %v", err)
		}
	})

	t.Run("resolves repo from RELEASE_IMAGE_REPO and token from publishTokenPath", func(t *testing.T) {
		t.Setenv("RELEASE_IMAGE_REPO", "Acme/Grain-Test")
		t.Setenv("GRAIN_CI_REPO", "ai-on-gke/grain")
		t.Setenv("GITHUB_TOKEN", "")
		if err := os.Remove(sandboxTokenPath); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := os.WriteFile(publishTokenPath, []byte("ghp_publish_token_456\n"), 0600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		err := run([]string{"-apply=false"}, &out)
		if err != nil && (strings.Contains(err.Error(), "are required") || strings.Contains(err.Error(), "GITHUB_TOKEN is empty")) {
			t.Fatalf("repo/token resolution failed unexpectedly: %v", err)
		}
	})
}

func TestResolveRepo(t *testing.T) {
	t.Run("explicit flags override environment", func(t *testing.T) {
		t.Setenv("RELEASE_IMAGE_REPO", "envowner/envrepo")
		t.Setenv("GRAIN_CI_REPO", "ai-on-gke/grain")
		owner, repo, err := resolveRepo("flagowner", "flagrepo")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if owner != "flagowner" || repo != "flagrepo" {
			t.Errorf("got (%q, %q), want (%q, %q)", owner, repo, "flagowner", "flagrepo")
		}
	})

	t.Run("defaults to bwsalmon/grain when RELEASE_IMAGE_REPO is unset even if GRAIN_CI_REPO is ai-on-gke/grain", func(t *testing.T) {
		t.Setenv("RELEASE_IMAGE_REPO", "")
		t.Setenv("GRAIN_CI_REPO", "ai-on-gke/grain")
		owner, repo, err := resolveRepo("", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if owner != "bwsalmon" || repo != "grain" {
			t.Errorf("got (%q, %q), want (%q, %q)", owner, repo, "bwsalmon", "grain")
		}
	})

	t.Run("resolves and lowercases RELEASE_IMAGE_REPO when set", func(t *testing.T) {
		t.Setenv("RELEASE_IMAGE_REPO", "GrainOwner/GrainRepo")
		t.Setenv("GRAIN_CI_REPO", "ai-on-gke/grain")
		owner, repo, err := resolveRepo("", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if owner != "grainowner" || repo != "grainrepo" {
			t.Errorf("got (%q, %q), want (%q, %q)", owner, repo, "grainowner", "grainrepo")
		}
	})

	t.Run("explicit flags work with no environment variable set", func(t *testing.T) {
		t.Setenv("RELEASE_IMAGE_REPO", "")
		t.Setenv("GRAIN_CI_REPO", "")
		owner, repo, err := resolveRepo("flagowner", "flagrepo")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if owner != "flagowner" || repo != "flagrepo" {
			t.Errorf("got (%q, %q), want (%q, %q)", owner, repo, "flagowner", "flagrepo")
		}
	})

	t.Run("partial flag owner with fallback from RELEASE_IMAGE_REPO", func(t *testing.T) {
		t.Setenv("RELEASE_IMAGE_REPO", "grainowner/grainrepo")
		owner, repo, err := resolveRepo("flagowner", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if owner != "flagowner" || repo != "grainrepo" {
			t.Errorf("got (%q, %q), want (%q, %q)", owner, repo, "flagowner", "grainrepo")
		}
	})

	t.Run("partial flag repo with fallback from RELEASE_IMAGE_REPO", func(t *testing.T) {
		t.Setenv("RELEASE_IMAGE_REPO", "grainowner/grainrepo")
		owner, repo, err := resolveRepo("", "flagrepo")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if owner != "grainowner" || repo != "flagrepo" {
			t.Errorf("got (%q, %q), want (%q, %q)", owner, repo, "grainowner", "flagrepo")
		}
	})

	t.Run("errors when RELEASE_IMAGE_REPO is malformed", func(t *testing.T) {
		t.Setenv("RELEASE_IMAGE_REPO", "invalid")
		_, _, err := resolveRepo("", "")
		if err == nil {
			t.Fatal("expected error when RELEASE_IMAGE_REPO is not owner/repo")
		}
		if !strings.Contains(err.Error(), "-owner and -repo are required") {
			t.Errorf("unexpected error message: %v", err)
		}
	})
}

func TestResolveToken(t *testing.T) {
	t.Run("explicit token flag overrides everything", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "gh-token")
		t.Setenv("PACKAGE_PUBLISH_TOKEN", "pkg-token")
		got := resolveToken("flag-token")
		if got != "flag-token" {
			t.Errorf("got %q, want flag-token", got)
		}
	})

	t.Run("falls back across environment variables", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("PACKAGE_PUBLISH_TOKEN", "pkg-token")
		if got := resolveToken(""); got != "pkg-token" {
			t.Errorf("got %q, want pkg-token", got)
		}

		t.Setenv("PACKAGE_PUBLISH_TOKEN", "")
		t.Setenv("GHCR_TOKEN", "ghcr-token")
		if got := resolveToken(""); got != "ghcr-token" {
			t.Errorf("got %q, want ghcr-token", got)
		}

		t.Setenv("GHCR_TOKEN", "")
		t.Setenv("REGISTRY_PUSH_TOKEN", "registry-token")
		if got := resolveToken(""); got != "registry-token" {
			t.Errorf("got %q, want registry-token", got)
		}
	})
}

func TestRunRejectsInvalidFlags(t *testing.T) {
	var out bytes.Buffer
	err := run([]string{"-invalid-flag"}, &out)
	if err == nil {
		t.Fatal("expected error with invalid flag")
	}
}

// A negative -keep-for puts the cutoff in the future and switches the
// age protection off, so it is refused before anything is listed.
func TestRunRejectsNegativeLimits(t *testing.T) {
	for _, args := range [][]string{
		{"-keep-for=-1h", "-owner=o", "-repo=r", "-token=t"},
		{"-max-deletes=-1", "-owner=o", "-repo=r", "-token=t"},
	} {
		var out bytes.Buffer
		err := run(args, &out)
		if err == nil || !strings.Contains(err.Error(), "must not be negative") {
			t.Errorf("run(%v) = %v, want a refusal of the negative value", args, err)
		}
	}
}

// A retried DELETE that finds the version gone is the deletion this run
// asked for, so it is counted and the run carries on.
func TestAnAlreadyGoneDeletionIsCountedAndSteppedOver(t *testing.T) {
	api := fixture()
	api.versions["grain"] = append(api.versions["grain"], Version{
		Package: "grain", ID: 9, Digest: "sha256:9",
		Tags: []string{"sha-9999999", "grain-task-2"}, Created: daysAgo(401),
	})
	api.refuse = map[int64]error{2: &ErrAlreadyGone{}}
	var out bytes.Buffer
	if err := prune(api, []string{"grain", "guest"}, policy(), true, now, &out); err != nil {
		t.Fatalf("an already-gone version failed the run: %v", err)
	}
	if len(api.deleted) != 1 || api.deleted[0] != 9 {
		t.Errorf("deleted %v, want the run to carry on to 9", api.deleted)
	}
	if !strings.Contains(out.String(), "already gone 1") {
		t.Errorf("the summary should count the already-gone version:\n%s", out.String())
	}
}

// 3df0d2fd0 moved branch tags from '-' to ".." spelling ('/' becomes "..",
// pkg/upgrade's TagForBranch). A branch with a '/' is published under the
// new spelling from its next build, and its old dash tag is left behind:
// nothing resolves it any more, so it is held by the age window alone and
// then aged out, while the new spelling of the same live branch is kept at
// any age. A dash tag that is itself the name of a live branch (a branch
// literally called grain-task-9) is not legacy and is kept. Driven as a dry
// run over the branch list as ListBranchTags reads it from GitHub
// (docs/test-plans/2026-10-04.md §5 step 5).
func TestLegacyDashBranchTagsAgeOutAfterTheMigration(t *testing.T) {
	gh, _ := newAPI(ok(`[{"name":"main"},{"name":"grain/task-232"},{"name":"grain/task-233"},{"name":"grain-task-9"}]`))
	branches, err := gh.ListBranchTags()
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{
		branches: branches,
		versions: map[string][]Version{
			"grain": {
				// grain/task-232's last build before the migration, under
				// the old spelling only.
				{Package: "grain", ID: 1, Tags: []string{"sha-0ld0001", "grain-task-232"}, Created: daysAgo(5)},
				// The same branch's head build since, under the new one.
				{Package: "grain", ID: 2, Tags: []string{"sha-new0002", "grain..task-232"}, Created: daysAgo(5)},
				// grain/task-233 has not been rebuilt since: its dash tag
				// is still inside the window.
				{Package: "grain", ID: 3, Tags: []string{"sha-0ld0003", "grain-task-233"}, Created: hoursAgo(20)},
				// A branch whose name really has a dash.
				{Package: "grain", ID: 4, Tags: []string{"sha-0ld0004", "grain-task-9"}, Created: daysAgo(5)},
			},
		},
	}
	p := policy()
	p.KeepFor = 48 * time.Hour
	p.KeepCommits = 0

	var out bytes.Buffer
	if err := prune(api, []string{"grain"}, p, false, now, &out); err != nil {
		t.Fatal(err)
	}
	if len(api.deleted) != 0 {
		t.Errorf("a dry run deleted %v", api.deleted)
	}
	plan := BuildPlan(api.versions["grain"], branches, p, now)
	var deleted []int64
	for _, d := range plan.Delete {
		deleted = append(deleted, d.Version.ID)
	}
	if len(deleted) != 1 || deleted[0] != 1 {
		t.Fatalf("deleted %v, want only the orphaned legacy tag's version 1:\n%s", deleted, out.String())
	}
	if !strings.Contains(out.String(), "would delete 1 versions") || !strings.Contains(out.String(), "grain-task-232 (") {
		t.Errorf("the dry run should name the legacy tag it would delete:\n%s", out.String())
	}
	for _, id := range []int64{2, 3, 4} {
		if d := decide(t, api.versions["grain"], branches, p, id); !d.Keep {
			t.Errorf("version %d was deleted: %s", id, d.Reason)
		}
	}
}
