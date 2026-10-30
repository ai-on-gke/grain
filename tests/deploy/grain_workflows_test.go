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

package deploy

// This repository's own .grain/workflows, checked the only way a file
// that nothing in this build imports can be: by parsing it with the
// parser that will run it (grain/task-185).
//
// It belongs in tests/deploy for the reason everything else here does --
// it is an agreement between files that no package's own tests can see.
// pkg/ci's tests prove the parser; this proves that what grain would
// actually be handed for *this* repository parses, names jobs that could
// run, and has not drifted into naming a make target the Makefile
// dropped. It runs nothing, so it costs nothing.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/ciapi"
	"github.com/ai-on-gke/grain/pkg/capability/packagepublish"
	"github.com/ai-on-gke/grain/pkg/capability/repowrite"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/repodecl"
)

func TestGrainWorkflowsParse(t *testing.T) {
	root := repoRoot(t)
	workflows, _, err := ci.LoadDir(os.DirFS(root))
	if err != nil {
		t.Fatalf("this repository's own %s does not parse: %v", ci.WorkflowDir, err)
	}
	if len(workflows) == 0 {
		t.Fatalf("no workflows found under %s", ci.WorkflowDir)
	}

	makefile := read(t, "Makefile")
	for _, w := range workflows {
		// A dispatch counts, because one of these files is deliberately
		// nothing else: .grain/workflows/image.yml builds the shipped
		// image from source for a branch that asks, and stopped
		// answering `release` when the release job started building and
		// testing the image it publishes (grain/task-180). What this is
		// still looking for is a file no event of any kind reaches.
		if !w.On.Matches(ci.EventPush, "grain/task-1") && !w.On.Matches(ci.EventRelease, "main") &&
			!w.On.Matches(ci.EventManual, "grain/task-1") {
			t.Errorf("%s: nothing would run this on a branch Grain pushes, releases or dispatches", w.Path)
		}
		for _, job := range w.Jobs {
			if len(job.Steps) == 0 {
				t.Errorf("%s: job %s has no steps", w.Path, job.ID)
			}
			for _, step := range job.Steps {
				for _, target := range makeTargets(step.Run) {
					if !strings.Contains(makefile, "\n"+target+":") {
						t.Errorf("%s: job %s runs `make %s`, and the Makefile has no such target",
							w.Path, job.ID, target)
					}
				}
			}
		}
	}
}

// makeTargets is every `make <target>` a step's script invokes -- the
// one part of a workflow this file can check against something else in
// the tree.
func makeTargets(script string) []string {
	var targets []string
	for _, line := range strings.Split(script, "\n") {
		fields := strings.Fields(line)
		for i, field := range fields {
			if field != "make" || i+1 >= len(fields) {
				continue
			}
			next := fields[i+1]
			if strings.HasPrefix(next, "-") {
				continue
			}
			targets = append(targets, next)
		}
	}
	return targets
}

// The directory is the one pkg/ci looks in, spelled the same way in
// both places -- a workflow in .grain/workflow/ or .github/ would be a
// file nothing ever reads and nothing ever complains about.
func TestGrainWorkflowDirIsWhereTheParserLooks(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(ci.WorkflowDir))); err != nil {
		t.Fatalf("%s: %v", ci.WorkflowDir, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".grain", "README.md")); err != nil {
		t.Errorf(".grain has no README saying what a file in it is trusted with: %v", err)
	}
}

func TestReleaseWorkflowPublishesThroughAnUncachedScript(t *testing.T) {
	workflows, _, err := ci.LoadDir(os.DirFS(repoRoot(t)))
	if err != nil {
		t.Fatal(err)
	}
	for _, workflow := range workflows {
		if workflow.Path != ".grain/workflows/release.yml" {
			continue
		}
		if !workflow.On.Matches(ci.EventRelease, "main") || workflow.On.Matches(ci.EventPush, "main") {
			t.Fatalf("release triggers = %+v", workflow.On)
		}
		if len(workflow.Jobs) != 1 || workflow.Jobs[0].Cache {
			t.Fatalf("release jobs = %+v, want one uncached job", workflow.Jobs)
		}
		if !strings.Contains(workflow.Jobs[0].Steps[0].Run, "scripts/release-images.sh") {
			t.Fatalf("release step does not invoke scripts/release-images.sh: %+v", workflow.Jobs[0].Steps)
		}
		return
	}
	t.Fatal("no Grain release workflow found")
}

// Nothing is published until the suite that says what the image contains
// has passed, and both halves of that are agreements between files.
//
// The release used to build and push while .grain/workflows/image.yml
// built the same image again and tested it, in a second workflow that
// started at the same moment. Grain CI has no `needs:` across workflows
// -- two workflows are two independent units of work -- so that ordering
// could not be written down, and a commit that broke what the image
// contains was published first and reddened afterwards (grain/task-180;
// 3d2d48f7d is the stale source export that reached a registry that way).
//
// So: one job, whose steps are ordered build, drive, push -- steps of a
// job share a sandbox, which is what lets the image the suite drove be
// the image the push publishes -- and no second workflow rebuilding the
// same image on the same event.
func TestTheReleasePublishesOnlyAfterTheContainerSuitePasses(t *testing.T) {
	workflows, _, err := ci.LoadDir(os.DirFS(repoRoot(t)))
	if err != nil {
		t.Fatal(err)
	}
	var release *ci.Workflow
	for i, workflow := range workflows {
		if workflow.Path == ".grain/workflows/release.yml" {
			release = &workflows[i]
			continue
		}
		if !workflow.On.Matches(ci.EventRelease, "main") {
			continue
		}
		for _, job := range workflow.Jobs {
			for _, step := range job.Steps {
				if strings.Contains(step.Run, "make image") {
					t.Errorf("%s: job %s builds the shipped image on `release` as well, "+
						"which builds it twice per merge and races the publish it cannot wait for",
						workflow.Path, job.ID)
				}
			}
		}
	}
	if release == nil {
		t.Fatal("no Grain release workflow found")
	}

	// The things the release job does before publishing, in the order it has to do
	// them in. Indices rather than presence: the whole claim is the
	// ordering.
	phase := func(marker string) int {
		for i, step := range release.Jobs[0].Steps {
			if strings.HasPrefix(marker, "./") && !strings.HasSuffix(marker, "/") {
				if slices.Contains(strings.Fields(step.Run), marker) {
					return i
				}
				continue
			}
			if strings.Contains(step.Run, marker) {
				return i
			}
		}
		t.Errorf("no step of the release job runs %q: %+v", marker, release.Jobs[0].Steps)
		return -1
	}
	build := phase("release-images.sh build")
	container := phase("./tests/container/")
	installer := phase("./tests/installer")
	installerTrixie := phase("./tests/installer/trixie")
	installerHostmode := phase("./tests/installer/hostmode")
	push := phase("release-images.sh push")
	if build < 0 || container < 0 || installer < 0 || installerTrixie < 0 || installerHostmode < 0 || push < 0 {
		t.FailNow()
	}
	// Steps of a job share a sandbox but not a shell, so the tags reach
	// the steps after the build through a file, and the job and the
	// script have to mean the same file by it.
	if release.Jobs[0].Env["RELEASE_IMAGES_STATE"] == "" {
		t.Errorf("the release job does not name RELEASE_IMAGES_STATE, which is the file the build phase "+
			"writes its tags to and the steps after it read: %+v", release.Jobs[0].Env)
	}
	contains(t, read(t, "scripts", "release-images.sh"), `state="${RELEASE_IMAGES_STATE:-`)

	if !(build < container && container < installer && installer < installerTrixie && installerTrixie < installerHostmode && installerHostmode < push) {
		t.Fatalf("the release job builds at step %d, drives tests/container at %d, drives tests/installer at %d/%d/%d and publishes at %d: "+
			"the suites are the gate, so they belong between the build and the push",
			build, container, installer, installerTrixie, installerHostmode, push)
	}
}

// The release job prunes obsolete package versions after publishing images,
// keeping GHCR storage bounded on every release.
func TestReleaseWorkflowPrunesOldPackages(t *testing.T) {
	workflows, _, err := ci.LoadDir(os.DirFS(repoRoot(t)))
	if err != nil {
		t.Fatal(err)
	}
	var release *ci.Workflow
	for i, workflow := range workflows {
		if workflow.Path == ".grain/workflows/release.yml" {
			release = &workflows[i]
			break
		}
	}
	if release == nil {
		t.Fatal("no Grain release workflow found")
	}

	phase := func(marker string) int {
		for i, step := range release.Jobs[0].Steps {
			if strings.Contains(step.Run, marker) {
				return i
			}
		}
		t.Errorf("no step of the release job runs %q: %+v", marker, release.Jobs[0].Steps)
		return -1
	}

	push := phase("release-images.sh push")
	prune := phase("cmd/ghcr-prune")
	if push < 0 || prune < 0 {
		t.FailNow()
	}

	if !(push < prune) {
		t.Fatalf("the release job publishes at step %d and prunes at %d: "+
			"pruning should run after publishing", push, prune)
	}

	pruneStep := release.Jobs[0].Steps[prune]
	if !strings.Contains(pruneStep.Run, "-apply") {
		t.Errorf("the prune step should run with -apply, got: %s", pruneStep.Run)
	}
}

// .grain/workflows/tests.yml reads the run it is in through grain's own
// variables, and holds no credential.
//
// This used to also hold the guest cache key in that file to the text
// build-artifacts.yml publishes under, because granule-vm resolved it
// there (grain/task-109). That job is gone -- nothing in this deployment
// uses granule any more (grain/task-171) -- and with it the only job in
// the file that pulled a published guest, so what is left is the pair of
// properties that hold whatever the file's jobs happen to be. Anyone
// adding a guest-resolving job back here should put the key assertion
// back with it; deploy_test.go keeps the .github/workflows spellings of
// that expression together and says why.
//
// The variable is the part that fails quietly. grain tells a job about
// its run in GRAIN_CI_* rather than GITHUB_* (pkg/ci's runEnv), precisely
// so that a script reading GITHUB_REPOSITORY in a grain sandbox finds
// nothing rather than something subtly different -- for a ghcr.io
// reference that would mean `ghcr.io//...` and a pull that fails on every
// commit.
func TestTheGrainTestWorkflowReadsGrainsOwnVariablesAndHoldsNoCredential(t *testing.T) {
	code := stripComments(read(t, ".grain", "workflows", "tests.yml"))
	absent(t, code, "GITHUB_REPOSITORY")

	// Nothing here logs in to a registry: a workflow that runs on an
	// unreviewed branch should hold nothing worth taking.
	absent(t, code, "docker login")
}

// Every real-vm job runs the same three tests, and is still armed
// (grain/task-116).
//
// Both halves are worth holding. Every prerequisite this suite has is one
// it *skips* on -- GRAIN_REAL_VM_TESTS, docker, ssh-keygen, /dev/kvm --
// and `go test` prints "ok" for a package whose selected tests all
// skipped, so a job that lost the variable, or a -run pattern that lost a
// test to a rename, goes green while covering nothing. That is not a
// hypothetical: this suite spent its whole life skipping inside go-test
// while reading as coverage. The "--- PASS: <name>" assertion in the job
// is the thing that makes it impossible, so this test asserts the
// assertion.
//
// Written over whichever of the two tests.yml files define the job
// rather than over .grain's alone, which is what it used to name. That
// copy is out of the file for now -- the guest it resolves stopped being
// in the cache, so the job spent an hour per push building one inline and
// failing (grain/task-427; the file header has the measurements) -- and
// GitHub's is not. Naming one file would have turned this test red on the
// commit that took the job out, and naming neither would leave the
// arming unchecked; discovering them covers the grain copy again, without
// an edit here, on the commit that puts it back.
func TestEveryRealVMJobRunsAllThreeRealTests(t *testing.T) {
	jobs := map[string]string{}
	for _, target := range []struct {
		path []string
		job  string
	}{
		{[]string{".grain", "workflows", "tests.yml"}, "real-vm"},
		{[]string{".grain", "workflows", "nightly.yml"}, "kvm-sandboxes"},
	} {
		text := read(t, target.path...)
		if !strings.Contains(text, "\n  "+target.job+":\n") {
			continue
		}
		jobs[filepath.Join(target.path...)+":"+target.job] = stripComments(namedJob(t, text, target.job))
	}
	if len(jobs) == 0 {
		t.Fatal("no tests or nightly workflow defines a real-vm or kvm-sandboxes job, so nothing runs the KVM suite anywhere: " +
			"the suite this deployment's sandboxes rest on is being asserted by no file at all")
	}

	for where, job := range jobs {
		contains(t, job, `GRAIN_REAL_VM_TESTS: "1"`)
		contains(t, job, "-count=1")
		for _, name := range []string{
			"TestGranuleSandboxesAgainstARealDockerBackedVM",
			"TestGranuleSandboxesAcquireCreatesTwoRealVMsConcurrently",
			"TestTheModelProxiesAreReachableFromInsideARealGuest",
		} {
			// Twice: once in the -run pattern that selects it, once in the
			// list of names whose "--- PASS" line is required.
			if strings.Count(job, name) < 2 {
				t.Errorf("%s's real-vm job names %s %d times; it needs both the -run pattern and the PASS assertion",
					where, name, strings.Count(job, name))
			}
		}
		contains(t, job, `grep -q -- "--- PASS: $t" /tmp/granule-vm.log`)
	}

	// And the tests exist under those names, which is the half no
	// workflow file can check about itself.
	suite := read(t, "pkg", "orchestrator", "granule_docker_real_test.go") +
		read(t, "pkg", "orchestrator", "model_proxy_real_test.go")
	for _, name := range []string{
		"func TestGranuleSandboxesAgainstARealDockerBackedVM(",
		"func TestGranuleSandboxesAcquireCreatesTwoRealVMsConcurrently(",
		"func TestTheModelProxiesAreReachableFromInsideARealGuest(",
	} {
		contains(t, suite, name)
	}

	// The NetworkNone isolation test is grain's alone (grain/task-1541):
	// nightly's kvm-sandboxes job is the only runner that boots a real
	// guest against this host's own nftables rules, so it is held there
	// rather than in every job above.
	if job, ok := jobs[filepath.Join(".grain", "workflows", "nightly.yml")+":kvm-sandboxes"]; !ok {
		t.Error("nightly.yml has no kvm-sandboxes job, so nothing runs TestGranuleSandboxNetworkNoneIsolationOutsideGuestVM")
	} else if n := strings.Count(job, "TestGranuleSandboxNetworkNoneIsolationOutsideGuestVM"); n < 2 {
		t.Errorf("nightly.yml's kvm-sandboxes job names TestGranuleSandboxNetworkNoneIsolationOutsideGuestVM %d times; it needs both the -run pattern and the PASS assertion", n)
	}
	contains(t, suite, "func TestGranuleSandboxNetworkNoneIsolationOutsideGuestVM(")

	// So is the host-script grant materialized into a real guest: a
	// nightly suite, held in the same job.
	if job, ok := jobs[filepath.Join(".grain", "workflows", "nightly.yml")+":kvm-sandboxes"]; ok {
		if n := strings.Count(job, "TestHostScriptGrantInARealGranuleVM"); n < 2 {
			t.Errorf("nightly.yml's kvm-sandboxes job names TestHostScriptGrantInARealGranuleVM %d times; it needs both the -run pattern and the PASS assertion", n)
		}
	}
	contains(t, read(t, "pkg", "orchestrator", "host_script_real_test.go"), "func TestHostScriptGrantInARealGranuleVM(")
}

// The sandbox guest is published from Grain CI, not from GitHub Actions
// alone (grain/task-188).
//
// It was published from one place for a year: the `sandbox-guest` job in
// .github/workflows/build-artifacts.yml. That stopped being a place this
// deployment can publish from on 17 September 2026, when this account's
// Actions began being refused before their first step ("The job was not
// started because an Actions budget is preventing further use"), and the
// cost of it is not a missing tag -- it is that `guest:inputs-<key>`
// stops being written, so every later commit that touches a path
// guest-inputs.sh names misses that cache for ever and builds a guest
// inline, ~10 minutes and ~500MB of downloads inside a nested VM.
//
// The release job is what makes the guest cache independent of one
// vendor's billing: it builds the guest with the same script and
// publishes the same four names, on every merge, from a Grain CI sandbox.
// What it needs that no file here can give it is the credential -- the
// `package-publish` CI capability, which is operator-owned repository
// configuration (docs/ci-capabilities.md) -- and release_images_test.go
// holds the script to failing loudly when it was granted none.
//
// So this pins the two halves a file *can* keep: that the script
// publishes the cache tag and the moving tags rather than only the
// immutable one, and that the job it runs in is sized for the build it
// may have to do.
func TestTheGuestIsPublishedFromGrainCIAndNotFromActionsAlone(t *testing.T) {
	script := read(t, "scripts", "release-images.sh")

	// The build phase fills the content-addressed cache every other
	// resolver of the guest reads -- deploy_test.go's
	// TestEveryGuestCacheKeyIsDerivedTheSameWay is what keeps them all
	// naming the same tag -- and it does it on a miss, which is the only
	// time there is anything new to publish.
	contains(t, script, "granulefile build")
	contains(t, script, `push_image "$guest_cache"`)

	// And the names a deployment actually resolves. `guest:sha-<short>`
	// is what a grain image built from the same commit is stamped with,
	// and `guest:<branch>` is what a deployment upgraded onto a branch
	// pulls. `guest:latest` -- pkg/cli/sandboximage.go's default for a
	// build stamped with nothing -- is the last promoted release's, and
	// the promote phase moves it (promote_images_test.go).
	contains(t, script, `push_pinned "$guest"`)
	contains(t, script, `"$guest $root/guest:$branch"`)

	// Retried, for the reason build-artifacts.yml's own push step gives
	// at length: GHCR loses this race on its own about once in a hundred
	// and fifty pushes, answering `unknown blob` to a manifest PUT after
	// telling the client it has every layer. Bounded, so a registry that
	// is actually down or a credential that is actually wrong still fails
	// in seconds rather than being retried into a timeout.
	contains(t, script, "for attempt in 1 2 3; do")

	// The sandbox has to be able to hold a guest build, because on a
	// cache miss that is what this job does: the base image, the VM's
	// container and the commit's own layer are on the disk at once, and
	// the GitHub job doing the same work asks for a runner with ~80GB
	// free. A job sized for a `go build` fails a merge's publish
	// somewhere inside docker or qemu, with a message that rarely says
	// "disk".
	workflows, _, err := ci.LoadDir(os.DirFS(repoRoot(t)))
	if err != nil {
		t.Fatal(err)
	}
	for _, workflow := range workflows {
		if workflow.Path != ".grain/workflows/release.yml" {
			continue
		}
		for _, job := range workflow.Jobs {
			if job.Sandbox.DiskGB < 80 {
				t.Errorf("%s: job %s asks for %dGB of disk, and a guest build alone wants ~80",
					workflow.Path, job.ID, job.Sandbox.DiskGB)
			}
			if job.TimeoutMinutes > 0 && job.TimeoutMinutes < 60 {
				t.Errorf("%s: job %s times out after %d minutes, which a cache miss -- a guest build, two e2e suites and the push -- does not fit in",
					workflow.Path, job.ID, job.TimeoutMinutes)
			}
		}
		return
	}
	t.Fatal("no Grain release workflow found")
}

// A branch that changes a guest input can fill the cache before it
// merges, and can do it without publishing anything a deployment
// resolves (grain/task-222).
//
// The release job covers main and every branch that changes none of the
// paths guest-inputs.sh names. What it cannot cover is the branch that
// changes one: that branch's key does not exist until it lands, so until
// then every job resolving a guest on it builds one inline -- ~25
// minutes in a nested VM, and the only thing that has ever made
// "real KVM sandboxes" red, right up to the week that build stopped
// succeeding at all and took that job out of grain CI (grain/task-427).
// .grain/workflows/image.yml's `guest-cache` job is the opt-in that
// closes it.
//
// Two properties make that safe to run from a branch, and both are
// pinned here, because losing either quietly turns an opt-in cache
// warmer into a publisher of images every deployment pulls:
//
//   - it is one phase of the release publisher rather than a second copy
//     of it, so the key it fills is by construction the key a consumer
//     misses on (TestEveryGuestCacheKeyIsDerivedTheSameWay covers the
//     expression itself); and
//   - that phase pushes the content-addressed names and nothing else.
//     `guest:latest` in particular is pkg/cli/sandboximage.go's
//     default for a build stamped with no image, so a deployment does
//     resolve it.
func TestABranchCanWarmTheGuestCacheWithoutPublishingAnythingElse(t *testing.T) {
	script := read(t, "scripts", "release-images.sh")

	// The phase exists, and is the same function the release build runs
	// -- not a second derivation of the key beside it.
	contains(t, script, "guest-cache) resolve_guest ;;")
	contains(t, script, "resolve_guest() {")
	contains(t, script, "build_images() {\n  resolve_guest\n")
	if got := strings.Count(script, "guest-inputs.sh guest)"); got != 1 {
		t.Errorf("scripts/release-images.sh reads the guest input list %d times; the point of the phase split is that the publisher derives that key in one place", got)
	}

	// The moving tags are publish_images' (and `latest` is promote's),
	// and `guest-cache` runs neither: a phase that pushed a moving guest
	// tag from a branch would put an unreviewed branch's guest in front of
	// every deployment that resolves it.
	if strings.Contains(releaseScriptFunction(t, script, "resolve_guest"), ":latest") {
		t.Error("resolve_guest moves `guest:latest`, so `guest-cache` would move it from a branch")
	}
	published := publishImagesBody(t, script)
	for _, moving := range []string{
		`"$guest $root/guest:$branch"`,
	} {
		if !strings.Contains(published, moving) {
			t.Errorf("scripts/release-images.sh moves %s outside publish_images, so `guest-cache` would move it from a branch", moving)
		}
	}

	workflows, _, err := ci.LoadDir(os.DirFS(repoRoot(t)))
	if err != nil {
		t.Fatal(err)
	}
	for _, workflow := range workflows {
		if workflow.Path != ".grain/workflows/image.yml" {
			continue
		}
		for _, job := range workflow.Jobs {
			if job.ID != "guest-cache" {
				continue
			}
			var steps string
			for _, step := range job.Steps {
				steps += step.Run + "\n"
			}
			contains(t, steps, "./scripts/release-images.sh guest-cache")

			// A guest build on a miss is the whole reason this job
			// exists, so it has to be able to hold one: the base image,
			// the VM's container and the commit's own layer are on the
			// disk at once.
			if job.Sandbox.DiskGB < 80 {
				t.Errorf("%s: job %s asks for %dGB of disk, and a guest build alone wants ~80",
					workflow.Path, job.ID, job.Sandbox.DiskGB)
			}
			if job.TimeoutMinutes > 0 && job.TimeoutMinutes < 60 {
				t.Errorf("%s: job %s times out after %d minutes, which a base build and a guest build in a nested VM do not fit in",
					workflow.Path, job.ID, job.TimeoutMinutes)
			}

			// A replayed green tick is not a published image: the tag
			// this job fills can be deleted from the registry, and a
			// cached result for the commit would then report a warm
			// cache that is not there.
			if job.Cache {
				t.Errorf("%s: job %s caches its result, and a job whose whole purpose is a push to a registry must not (docs/ci-capabilities.md)",
					workflow.Path, job.ID)
			}
			return
		}
		t.Fatalf("%s declares no guest-cache job, so a branch that changes a guest input has no way to warm the cache before it merges", workflow.Path)
	}
	t.Fatal("no Grain image workflow found")
}

// A merge whose container suite goes red still leaves the guest cache
// filled (grain/task-449).
//
// The release job's gate is the right shape for everything a deployment
// resolves: `guest:latest`, `guest:sha-<short>` and the grain images all
// wait for tests/container, and a red suite publishes none of them. The
// content-addressed cache tag is the one thing in that job that must not
// wait, and the difference is not a matter of degree:
//
//   - it is an input, not an artifact. Nothing pulls `guest:inputs-<key>`
//     except a job resolving a guest whose own tree derives that same key
//     (scripts/granule-guest-resolve.sh), and what it costs to miss is a
//     ~1GB `granule guest build` inside a nested VM -- 40-55 minutes on
//     the longest job in every branch's suite, and the only thing that
//     has ever made "real KVM sandboxes" red;
//   - it is safe by construction, which nothing else here is. The tag
//     names the hash of its own inputs, so the guest a failing commit
//     built is still exactly the guest those inputs describe. A suite
//     that goes red says something about the grain image, or about that
//     commit's Go code; it says nothing that could make the guest built
//     from an unchanged `third_party/granule`, `go.sum` and
//     `Granulefile` the wrong guest for them.
//
// So the push lives in `resolve_guest`, which the build phase runs before
// the gate, rather than in `publish_images`, which runs after it. Moving
// it would leave no test red anywhere else: every other assertion about
// this script asks whether the cache tag is pushed at all, and it would
// still be pushed -- on the merges that go green, which are exactly the
// merges that did not need the rescue. This asks where.
func TestARedReleaseGateDoesNotCostTheGuestCache(t *testing.T) {
	script := read(t, "scripts", "release-images.sh")

	// Both content-addressed names, because a base miss is a build of
	// its own and gets thrown away by the same failure.
	resolve := releaseScriptFunction(t, script, "resolve_guest")
	for _, cached := range []string{`push_image "$guest_cache"`, `push_image "$base_cache"`} {
		if !strings.Contains(resolve, cached) {
			t.Errorf("scripts/release-images.sh does not %s in resolve_guest, so the cache tag is filled by "+
				"some later phase: a merge whose gate fails would build a guest and throw it away", cached)
		}
	}

	// And nowhere else. In publish_images it would be behind the gate;
	// twice over, it would be pushed behind the gate as well as in front
	// of it, which reads as belt and braces and is a merge's worth of
	// guest build thrown away all the same.
	published := publishImagesBody(t, script)
	for _, cached := range []string{"$guest_cache", "$base_cache"} {
		if strings.Contains(published, cached) {
			t.Errorf("scripts/release-images.sh names %s inside publish_images, which the release job runs "+
				"after tests/container: the content-addressed cache is an input and does not wait on a verdict "+
				"about the images built over it", cached)
		}
	}
	for _, cached := range []string{`push_image "$guest_cache"`, `push_image "$base_cache"`} {
		if got := strings.Count(script, cached); got != 1 {
			t.Errorf("scripts/release-images.sh runs %s %d times; it belongs in resolve_guest and nowhere else", cached, got)
		}
	}

	// The phase that does it is the one in front of the gate. This is the
	// same ordering TestTheReleasePublishesOnlyAfterTheContainerSuitePasses
	// reads, asked the other way round: that test is about the push being
	// late, and this one is about the cache being early.
	workflows, _, err := ci.LoadDir(os.DirFS(repoRoot(t)))
	if err != nil {
		t.Fatal(err)
	}
	for _, workflow := range workflows {
		if workflow.Path != ".grain/workflows/release.yml" {
			continue
		}
		build, gate := -1, -1
		for i, step := range workflow.Jobs[0].Steps {
			if strings.Contains(step.Run, "release-images.sh build") {
				build = i
			}
			if strings.Contains(step.Run, "./tests/container/") {
				gate = i
			}
		}
		if build < 0 || gate < 0 {
			t.Fatalf("%s: found the build phase at step %d and the container suite at %d; "+
				"this test cannot say which side of the gate the cache is filled on", workflow.Path, build, gate)
		}
		if build > gate {
			t.Errorf("%s: the build phase runs at step %d and the container suite at %d, so the guest cache "+
				"is filled only where that suite passes", workflow.Path, build, gate)
		}
		return
	}
	t.Fatal("no Grain release workflow found")
}

// And the same job as grain itself now asks for it after every merge
// (grain/task-409): a repository naming
// model.RepoConfig.PostMergeDispatchJobs has those jobs dispatched on
// its base branch whenever the branch moves, which is how this
// repository's guest cache follows `main` with releases switched off.
//
// What that turns into is one ci.Request, planned against this
// repository's own workflow files, and this is the file where a claim of
// that shape can be checked. Three things have to hold of it, and all
// three are the kind that fail silently:
//
//   - `workflow_dispatch` reaches image.yml at all. It is the file's only
//     trigger, so a plan built under any other event matches nothing here
//     and concludes errored without running anything (grain/task-396 is
//     the same mistake made on the release event);
//   - the selection is that one job and nothing else. `container-e2e`
//     lives in the same file and is a forty-minute image build, so a
//     dispatch that dragged it in per merge would cost more than the
//     coupling this undoes;
//   - and the job is still uncached, because a replayed verdict is not a
//     published image (the section above says why at length).
func TestTheGuestCacheJobIsWhatAPostMergeDispatchPlans(t *testing.T) {
	workflows, _, err := ci.LoadDir(os.DirFS(repoRoot(t)))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := ci.PlanRun(ci.Request{
		Repo: "bwsalmon/grain", Ref: "main",
		Commit: "0123456789abcdef0123456789abcdef01234567",
		Event:  ci.EventManual,
		Jobs:   []string{".grain/workflows/image.yml:guest-cache"},
	}, workflows)
	if err != nil {
		t.Fatalf("a post-merge dispatch of the guest cache job plans nothing: %v", err)
	}
	if len(plan.Jobs) != 1 {
		var ids []string
		for _, job := range plan.Jobs {
			ids = append(ids, job.ID)
		}
		t.Fatalf("that dispatch plans %d jobs (%s), want the cache job alone: what follows every "+
			"merge has to be the job that fills the cache and not the suite around it",
			len(plan.Jobs), strings.Join(ids, ", "))
	}
	job := plan.Jobs[0]
	if job.Job.ID != "guest-cache" || job.WorkflowPath != ".grain/workflows/image.yml" {
		t.Fatalf("that dispatch plans %s:%s, want .grain/workflows/image.yml:guest-cache",
			job.WorkflowPath, job.Job.ID)
	}
	if job.Job.Cache {
		t.Error("the job a merge dispatches caches its result: a green tick replayed for this " +
			"commit would report a warm cache that may have been deleted from the registry")
	}
}

// What judges this repository's base branch, checked against the files
// that decide it -- because the one time it was wrong, nothing said so for
// weeks (grain/task-355, swept for again by grain/task-425).
//
// The incident: `main` was judged by a run asked for under `release`,
// which for a native grain workflow matches `on: release` and nothing
// else. That was `release.yml:images` alone -- build an image and publish
// it -- so the control that exists to catch two branches green apart and
// broken together ran no test at all, while still holding every merge in
// the repository behind a `cache: false` publish job that failed for days.
// Which jobs a base-branch run contains is decided by the event and by the
// workflow files, and neither pkg/orchestrator's tests nor pkg/ci's can
// see this repository's own: orchestrator's pin that the gate is asked for
// under `push` (basebranchci_test.go), and this pins that a `push` of this
// repository is made of its tests.
//
// So the two halves of "the gate judges something", in the two directions
// that have each been wrong once:
//
//   - a `push` plan -- which is exactly what a merge is judged by, for
//     every repository, since grain/task-461 made
//     orchestrator.baseBranchCIEvent a constant -- contains the jobs that
//     run this repository's test suites, and none of the publisher's;
//   - a `release` plan contains only the publisher. grain/task-355's own
//     patch made `tests.yml` answer `release` beside `push`, which put the
//     whole suite inside a trusted run carrying release credentials and
//     still left the publish job holding the queue. An operator ruled
//     against it and 461 reversed it, but the reversed rule is still
//     written down in this deployment's docs as history, so re-applying it
//     is a one-line mistake that this catches.
func TestTheBaseBranchGateForThisRepositoryRunsItsTests(t *testing.T) {
	workflows, _, err := ci.LoadDir(os.DirFS(repoRoot(t)))
	if err != nil {
		t.Fatalf("this repository's own %s does not parse: %v", ci.WorkflowDir, err)
	}
	commit := "0123456789abcdef0123456789abcdef01234567"

	// Exactly what orchestrator.startBaseBranchCI asks for once `main` has
	// moved: an untrusted push of the whole push plan, narrowed by nothing
	// (baseBranchCIPolicy.request with no RepoConfig.BaseBranchCIJobs).
	gate, err := ci.PlanRun(ci.Request{
		Repo: "bwsalmon/grain", Ref: "main", Commit: commit,
		Event: ci.EventPush,
	}, workflows)
	if err != nil {
		t.Fatalf("a base-branch run of this repository plans nothing: %v", err)
	}
	if len(gate.Jobs) == 0 {
		t.Fatalf("a base-branch run of this repository is made of no job at all, so it judges "+
			"nothing and every merge lands unchecked: %s", gate.Unmatched)
	}
	// Each of this repository's own test suites has to be in there. Asking
	// only "does any job run a test" would not have caught the incident:
	// `release.yml:images` runs `go test ./tests/container/...` before it
	// publishes, so the gate that judged nothing about `main`'s tree did
	// run a `go test`, and that is why it read as a plausible gate for
	// weeks. What it never ran is the suites below.
	for _, suite := range []struct {
		what  string
		found func(ci.Job) bool
	}{
		{"this repository's Go packages (`go test ./...`)", runsTheGoPackageSuite},
		{"the front-end suite (`npm test`)", runsTheFrontEndSuite},
	} {
		if slices.ContainsFunc(gate.Jobs, func(job ci.PlannedJob) bool { return suite.found(job.Job) }) {
			continue
		}
		t.Errorf("the base-branch gate is %s, and not one of those jobs runs %s: a gate that "+
			"holds merges and files repair tasks while saying nothing about whether main's "+
			"own tests pass is grain/task-355's failure exactly",
			strings.Join(plannedJobNames(gate), ", "), suite.what)
	}
	var published []string
	for _, job := range gate.Jobs {
		if job.WorkflowPath == releaseWorkflowPath || job.WorkflowPath == promoteWorkflowPath {
			published = append(published, job.WorkflowPath+":"+job.Job.ID)
		}
	}
	if len(published) > 0 {
		t.Errorf("the base-branch gate contains %s, which is this repository's publisher: a "+
			"publish that fails would hold every merge, and the fix for the publish could not "+
			"be merged either (docs/merge-queue-behind.md, \"The release is not the gate\")",
			strings.Join(published, ", "))
	}

	// The other direction: what a merge publishes with, which is a second
	// run at the same tip and no part of the verdict
	// (orchestrator.reconcileBaseBranchRelease).
	release, err := ci.PlanRun(ci.Request{
		Repo: "bwsalmon/grain", Ref: "main", Commit: commit,
		Event: ci.EventRelease, TrustedRelease: true,
	}, workflows)
	if err != nil {
		t.Fatalf("a release run of this repository plans nothing: %v", err)
	}
	for _, job := range release.Jobs {
		if job.WorkflowPath == releaseWorkflowPath || job.WorkflowPath == promoteWorkflowPath {
			continue
		}
		t.Errorf("%s:%s answers `release`, so it runs inside the trusted release run holding "+
			"this repository's package credential -- every job of a release run is handed "+
			"them, whether it publishes or not. Judging is a `push` now, so a test job has no "+
			"reason to answer `release` (grain/task-461, docs/ci-capabilities.md)",
			job.WorkflowPath, job.Job.ID)
	}
}

// This repository's publisher, named once for the two claims above.
const releaseWorkflowPath = ".grain/workflows/release.yml"

// And the one job beside it that may answer `release`: it publishes too,
// retagging the last promoted release's images as `latest`, so it needs
// the credential the release run is handed for the same reason.
const promoteWorkflowPath = ".grain/workflows/promote.yml"

// runsTheGoPackageSuite reports whether a job runs the Go race test suite of
// this repository -- `go test -race` over module packages rather than over
// the publisher's two narrow suites over ./tests/container and ./tests/installer.
func runsTheGoPackageSuite(job ci.Job) bool {
	return runsALine(job, func(line string) bool {
		return strings.Contains(line, "go test") && strings.Contains(line, "-race") &&
			(strings.Contains(line, "./...") || strings.Contains(line, "./pkg/"))
	})
}

// runsTheFrontEndSuite reports whether a job runs ui/'s own tests. `npm
// test` is how the workflow spells it and vitest is what that resolves to;
// either counts, so renaming the script does not silently empty the gate.
func runsTheFrontEndSuite(job ci.Job) bool {
	return runsALine(job, func(line string) bool {
		return strings.Contains(line, "npm test") || strings.Contains(line, "vitest")
	})
}

// runsALine reports whether any line of any of this job's scripts is one
// match wants.
//
// It reads the scripts rather than the job names because a name is not
// evidence: `release.yml:images` was taken for a gate for weeks. Comment
// lines are dropped first -- this repository's workflow steps discuss `go
// test` at length in comments the shell never sees, and a comment is
// exactly the kind of mention that would let a job with its tests taken
// out of it go on passing this.
func runsALine(job ci.Job, match func(string) bool) bool {
	for _, step := range job.Steps {
		for _, line := range strings.Split(step.Run, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if match(line) {
				return true
			}
		}
	}
	return false
}

// The jobs a plan is made of, for a failure that has to say what the gate
// was instead of what it should have been. The same line
// orchestrator.baseCIJobsOf logs as each base-branch run starts.
func plannedJobNames(plan ci.Plan) []string {
	names := make([]string, 0, len(plan.Jobs))
	for _, job := range plan.Jobs {
		names = append(names, job.WorkflowPath+":"+job.Job.ID)
	}
	return names
}

// The body of publish_images, which is where the moving tags belong.
func publishImagesBody(t *testing.T, script string) string {
	t.Helper()
	return releaseScriptFunction(t, script, "publish_images")
}

// The body of one of the publisher's shell functions. The claims above
// are about which phase a push happens in rather than whether the script
// mentions it anywhere, and the phases are these functions.
func releaseScriptFunction(t *testing.T, script, name string) string {
	t.Helper()
	from := strings.Index(script, name+"() {")
	if from < 0 {
		t.Fatalf("scripts/release-images.sh has no %s function", name)
	}
	rest := script[from:]
	end := strings.Index(rest, "\n}\n")
	if end < 0 {
		t.Fatalf("scripts/release-images.sh's %s function does not end", name)
	}
	return rest[:end]
}

// A job's `paths:` is a promise that it reads nothing else, and the one
// way that promise fails is silently: a file a job really does read, left
// out of what it declared, is served from the cache on the commit that
// changed it and reported as the green it was on the commit before
// (pkg/ci/paths.go).
//
// So this is the promise checked against the files, for the declaration
// this repository actually ships. The list below is every non-Go file
// under this repository that something `go test ./...` runs opens --
// tests/deploy is most of it, and it is why the `go` job's exclusions are
// as short as they are: .github/workflows and terraform/gcp look like
// things no Go test could care about, and both are read a few files above
// this one.
func TestTheGoJobDeclaresEveryFileItsOwnTestsRead(t *testing.T) {
	goJob := grainJob(t, ".grain/workflows/tests.yml", "go")
	if len(goJob.Paths) == 0 && len(goJob.PathsIgnore) == 0 {
		t.Fatal("the `go` job in .grain/workflows/tests.yml declares neither paths: nor paths-ignore:, " +
			"so it keys on the commit and everything below this is checking nothing")
	}

	for _, path := range []string{
		// tests/deploy, which reads this repository as data.
		".grain/workflows/tests.yml",
		"deploy/terraform/gcp/variables.tf",
		"deploy/terraform/gcp/instance.tf",
		"deploy/terraform/gcp/files/deploy.sh",
		"deploy/gke/terraform/variables.tf",
		"deploy/kubernetes/gke/gateway.yaml",
		"scripts/deploy-gke.sh",
		"scripts/setup.sh",
		"Makefile",
		// The agent surface captures, which pkg/agent's own tests assert
		// against -- the one thing under docs/ that is not excluded, and
		// the reason the exclusion carries a `!` line.
		"docs/claude-surface.md",
		"docs/codex-surface.md",
		"docs/agy-surface.md",
		// The images and scripts the installer and guest suites drive.
		"Dockerfile",
		"Dockerfile.granule",
		"Granulefile",
		"scripts/installer-test-image.sh",
		"scripts/run-go-tests.sh",
		// Ordinary source, and the embedded playbooks, which are markdown
		// and must not be caught by the top-level `*.md` exclusion.
		"pkg/ci/paths.go",
		"ui/package.json",
		"go.mod",
		"pkg/capability/bootstrap/playbooks/gcp-capabilities.md",
	} {
		if !selectsPath(goJob, path) {
			t.Errorf("the `go` job does not declare %s, which something it runs reads: "+
				"a commit that changed only that file would be served the previous commit's green", path)
		}
	}

	// And the exclusion is doing something, or the declaration is a
	// no-op with a comment on it.
	for _, path := range []string{"docs/design.md", "docs/ci.md", "README.md", "LICENSE", "NOTICE"} {
		if selectsPath(goJob, path) {
			t.Errorf("the `go` job declares %s, which nothing it runs reads -- the exclusion is not working", path)
		}
	}

	// `go-e2e` runs tests/deploy, the package that reads most of the list
	// above, so it must be keyed on exactly what `go` is.
	goE2E := grainJob(t, ".grain/workflows/tests.yml", "go-e2e")
	if !slices.Equal(goE2E.Paths, goJob.Paths) || !slices.Equal(goE2E.PathsIgnore, goJob.PathsIgnore) {
		t.Errorf("the `go-e2e` job declares paths=%v paths-ignore=%v, want the `go` job's paths=%v paths-ignore=%v",
			goE2E.Paths, goE2E.PathsIgnore, goJob.Paths, goJob.PathsIgnore)
	}

	// Backend-only Go shards (`go-orchestrator`, `go-model`, `go-unit`) skip
	// `make frontend` and exclude `ui/**` so UI-only edits do not invalidate
	// them, while `go-unit` retains the two frontend JS files its parity tests
	// read directly (`ui/src/state.js` and `ui/src/taskAttributes.js`).
	for _, id := range []string{"go-orchestrator", "go-model", "go-unit"} {
		job := grainJob(t, ".grain/workflows/tests.yml", id)
		for _, path := range []string{
			"pkg/ci/paths.go",
			"go.mod",
			"docs/claude-surface.md",
			"docs/codex-surface.md",
			"docs/agy-surface.md",
			"pkg/capability/bootstrap/playbooks/gcp-capabilities.md",
		} {
			if !selectsPath(job, path) {
				t.Errorf("the %q job does not declare %s, which something it runs reads", id, path)
			}
		}
		for _, path := range []string{
			"docs/design.md",
			"docs/ci.md",
			"README.md",
			"LICENSE",
			"NOTICE",
			"ui/package.json",
			"ui/src/App.jsx",
		} {
			if selectsPath(job, path) {
				t.Errorf("the %q job declares %s, which nothing it runs reads -- its exclusion should ignore docs and ui", id, path)
			}
		}
	}

	goUnit := grainJob(t, ".grain/workflows/tests.yml", "go-unit")
	for _, path := range []string{"ui/src/state.js", "ui/src/taskAttributes.js"} {
		if !selectsPath(goUnit, path) {
			t.Errorf("the `go-unit` job does not declare %s, which pkg/mcp or pkg/cli reads directly", path)
		}
	}
}

// The Go test suite in .grain/workflows/tests.yml is partitioned across five
// parallel -race jobs (`go`, `go-e2e`, `go-orchestrator`, `go-model`,
// `go-unit`) on 8-vCPU / 16 GiB sandboxes. The three backend shards stay
// within 20 minutes; `go` and `go-e2e`, which build the frontend before
// their tests, are allowed 25. Every package in `go list ./...` must be covered by at least one
// shard (so a newly added package can never be silently omitted from the
// gate) and by at most one shard (so no package is run twice under -race).
func TestTheGoJobsCoverEveryPackageInTheModuleOnce(t *testing.T) {
	const workflowPath = ".grain/workflows/tests.yml"
	shards := []string{"go", "go-e2e", "go-orchestrator", "go-model", "go-unit"}
	// The shards whose packages import pkg/ui, which go:embeds the built
	// frontend in pkg/ui/static.
	frontendShards := []string{"go", "go-e2e"}
	root := repoRoot(t)

	listPackages := func(patterns ...string) []string {
		t.Helper()
		args := append([]string{"list"}, patterns...)
		cmd := exec.Command("go", args...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s failed: %v\n%s", strings.Join(args, " "), err, out)
		}
		var pkgs []string
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				pkgs = append(pkgs, line)
			}
		}
		return pkgs
	}

	allPkgs := listPackages("./...")
	claimedBy := make(map[string][]string, len(allPkgs))

	for _, id := range shards {
		job := grainJob(t, workflowPath, id)
		if job.Sandbox.CPUs != 8 || job.Sandbox.MemoryMB != 16384 {
			t.Errorf("%s: job %s has sandbox cpus=%d memory-mb=%d, want cpus=8 memory-mb=16384",
				workflowPath, id, job.Sandbox.CPUs, job.Sandbox.MemoryMB)
		}
		// The two shards that build the frontend before their tests are
		// allowed 25; every other shard keeps the 20-minute bound.
		maxMinutes := 20
		if slices.Contains(frontendShards, id) {
			maxMinutes = 25
		}
		if job.TimeoutMinutes <= 0 || job.TimeoutMinutes > maxMinutes {
			t.Errorf("%s: job %s has timeout-minutes=%d, want between 1 and %d",
				workflowPath, id, job.TimeoutMinutes, maxMinutes)
		}

		buildsFrontend := false
		var patterns []string
		for _, step := range job.Steps {
			if strings.Contains(step.Run, "warm-npm-cache.sh") || slices.Contains(makeTargets(step.Run), "frontend") {
				buildsFrontend = true
			}
			if step.Name != "Test" {
				continue
			}
			joined := strings.ReplaceAll(step.Run, "\\\n", " ")
			for _, field := range strings.Fields(joined) {
				if strings.HasPrefix(field, "./") && !strings.HasPrefix(field, "./scripts/") {
					patterns = append(patterns, field)
				}
			}
		}
		if slices.Contains(frontendShards, id) && !buildsFrontend {
			t.Errorf("%s: job %s must build the frontend before testing packages that embed it", workflowPath, id)
		}
		if !slices.Contains(frontendShards, id) && buildsFrontend {
			t.Errorf("%s: backend shard %s should not run warm-npm-cache.sh or make frontend", workflowPath, id)
		}
		if len(patterns) == 0 {
			t.Fatalf("%s: job %s step \"Test\" names no ./package patterns", workflowPath, id)
		}
		for _, pkg := range listPackages(patterns...) {
			claimedBy[pkg] = append(claimedBy[pkg], id)
		}
	}

	for _, pkg := range allPkgs {
		jobs := claimedBy[pkg]
		if len(jobs) == 0 {
			t.Errorf("package %s (from `go list ./...`) is not covered by any Go test job in %s (%s): add it to one shard so it is tested in the gate",
				pkg, workflowPath, strings.Join(shards, ", "))
		} else if len(jobs) > 1 {
			t.Errorf("package %s is tested by multiple Go test jobs in %s (%s): assign it to exactly one shard",
				pkg, workflowPath, strings.Join(jobs, ", "))
		}
	}
}

// selectsPath asks the job's own declaration whether path is one of the
// files it is keyed on, by putting that one file in a tree and seeing
// whether it comes back.
func selectsPath(job ci.Job, path string) bool {
	tree := &ci.Tree{Files: []ci.TreeFile{{Path: path, Blob: "x"}}}
	_, ok := tree.Match(job.Paths, job.PathsIgnore)
	return ok
}

func grainJob(t *testing.T, workflowPath, jobID string) ci.Job {
	t.Helper()
	workflows, _, err := ci.LoadDir(os.DirFS(repoRoot(t)))
	if err != nil {
		t.Fatalf("this repository's own %s does not parse: %v", ci.WorkflowDir, err)
	}
	for _, w := range workflows {
		if w.Path != workflowPath {
			continue
		}
		for _, job := range w.Jobs {
			if job.ID == jobID {
				return job
			}
		}
	}
	t.Fatalf("%s declares no job %q", workflowPath, jobID)
	return ci.Job{}
}

// The guest a job says it resolves, and the guest it actually resolves.
//
// A job that resolves a sandbox guest declares the files that guest is
// built from (`guest-cache:`, pkg/grit/cache/guestcache.go), and grain warms the
// content-addressed cache for that key before letting the job take a
// machine. The declaration and the real input list
// (scripts/granule/guest-inputs.sh, which is what the publisher and every
// consumer derive the registry tag from) are two spellings of one fact in
// two files that cannot see each other, which is the shape every other
// test in this package exists for.
//
// The direction that matters is one way round. A declaration that selects
// *more* than the build reads costs at most a warm run that pulls a tag
// already in the registry. One that selects *less* is the failure: grain
// would derive the same guest key for two trees that build different
// guests, decide the second was already warmed, and leave the job to
// build one inline -- the very cost this is here to remove, arrived at
// silently. So this checks coverage rather than equality.
func TestTheGuestCacheDeclarationCoversEveryGuestInput(t *testing.T) {
	root := repoRoot(t)
	workflows, _, err := ci.LoadDir(os.DirFS(root))
	if err != nil {
		t.Fatalf("parsing this repository's own %s: %v", ci.WorkflowDir, err)
	}
	tree := repoTree(t, root)

	// Which jobs this is about, discovered rather than listed: a job that
	// runs the guest resolution script is a job that resolves a guest,
	// whatever it is called and whichever file it moves to.
	var resolvers, declared int
	for _, w := range workflows {
		for _, job := range w.Jobs {
			resolves := false
			for _, step := range job.Steps {
				if strings.Contains(step.Run, "granule-guest-resolve.sh") {
					resolves = true
				}
			}
			if !resolves {
				continue
			}
			resolvers++
			if len(job.GuestCache) == 0 {
				// Failed rather than reported, since grain/task-469: the
				// deployment parsing these files knows the key now, so a
				// guest-resolving job without one is an omission rather
				// than a thing that could not yet be written. The list to
				// add is .grain/workflows/kvm-route-stress.yml's, which is
				// guest-inputs.sh's own in glob form.
				t.Errorf("%s: job %s resolves a sandbox guest and declares no `guest-cache:`, so grain "+
					"warms nothing for it and a branch that changes a guest input builds a ~1GB guest "+
					"inline in this job (pkg/grit/cache/guestcache.go)", w.Path, job.ID)
				continue
			}
			declared++
			selected, ok := tree.Match(job.GuestCache, nil)
			if !ok {
				t.Errorf("%s: job %s declares guest inputs that match no file in this repository", w.Path, job.ID)
				continue
			}
			covered := map[string]bool{}
			for _, f := range selected {
				covered[f.Path] = true
			}
			for _, input := range guestInputs(t, root) {
				files := underPath(tree, input)
				if len(files) == 0 {
					t.Errorf("scripts/granule/guest-inputs.sh names %q, which is not in this repository", input)
					continue
				}
				for _, path := range files {
					if !covered[path] {
						t.Errorf("%s: job %s builds its guest from %s (guest-inputs.sh names %q) and its "+
							"`guest-cache:` does not select it, so a commit changing that file derives "+
							"the guest key of the tree it replaced", w.Path, job.ID, path, input)
					}
				}
			}
		}
	}
	if resolvers == 0 {
		t.Skip("no job in this repository's own workflows resolves a sandbox guest at the moment " +
			"(.grain/workflows/tests.yml's KVM job is out of the file, grain/task-427), so there is " +
			"no declaration to hold to guest-inputs.sh")
	}
	if declared == 0 {
		t.Errorf("%d job(s) resolve a sandbox guest and none of them declares its guest inputs, so "+
			"nothing above was compared with guest-inputs.sh", resolvers)
	}
}

// Every step that resolves a sandbox guest is declared infrastructure
// (grain/task-497).
//
// The guest resolution is the one step in this repository's CI that is
// allowed to take an hour and is never a statement about the commit: it
// pulls `guest:inputs-<key>` and, when the registry holds none, builds a
// ~1GB guest inline in a nested VM. Both halves of that are about this
// deployment -- what the publisher filled, what the archives served --
// and neither says anything about the tree being tested.
//
// Undeclared, it cost exactly what ci.Step.Infrastructure's doc comment
// describes: the inline build ran past the job's `timeout-minutes`, the
// job was recorded ci.ConclusionFailure on branches nothing was wrong
// with, the merge queue filed repairs against them, the base-branch gate
// read the same verdicts, and grain/task-427 deleted `real KVM
// sandboxes` from .grain/workflows/tests.yml to stop the noise -- a
// genuine test removed from the suite because a cache had made it
// unreliable.
//
// So this is the assertion that keeps it from rotting back, and it is
// written over the parsed workflows rather than over a list of jobs for
// the reason TestTheGuestCacheDeclarationCoversEveryGuestInput is: a
// step that runs the resolution script is a step that resolves a guest,
// whatever job it is in and whichever file that job moves to. A third
// one added tomorrow is covered on the day it lands.
//
// The bound is asserted with it because an infrastructure step that
// declares none falls back to a fresh copy of the *job's* own bound,
// and a job's bound is sized for the job's own work -- `real KVM
// sandboxes` allows 60 minutes for a suite whose `go test` is bounded at
// 40 -- while the build under this step has been measured at 50-110
// minutes. That is the same expiry this key exists to stop, arrived at
// from the other end.
//
// Both workflows that resolve a guest are held to this, with no file
// exempted. kvm-route-stress.yml was for a day (0f4f2dfa1), along with
// the `t.Skip` above -- not because its resolve step wanted the key any
// less, but because the daemon then parsing .grain/workflows did not
// know it and an undeclarable key cannot be asserted. Both came off in
// grain/task-546, once a push had measured that the running daemon
// carries ci.Step.Infrastructure. An exemption added here again should
// be read as that same temporary thing and removed the same way.
func TestEveryGuestResolvingStepIsInfrastructure(t *testing.T) {
	workflows, _, err := ci.LoadDir(os.DirFS(repoRoot(t)))
	if err != nil {
		t.Fatalf("parsing this repository's own %s: %v", ci.WorkflowDir, err)
	}

	const coldBuildMinutes = 120
	steps := 0
	for _, w := range workflows {
		for _, job := range w.Jobs {
			for _, step := range job.Steps {
				if !strings.Contains(step.Run, "granule-guest-resolve.sh") {
					continue
				}
				steps++
				if !step.Infrastructure {
					t.Errorf("%s: job %s's step %q resolves a sandbox guest and does not say "+
						"`infrastructure: true`, so a registry that does not hold this commit's key "+
						"is reported as a failure of the commit (ci.Step.Infrastructure; grain/task-427 "+
						"is what that cost last time)", w.Path, job.ID, step.DisplayName())
				}
				if step.TimeoutMinutes < coldBuildMinutes {
					t.Errorf("%s: job %s's step %q declares timeout-minutes: %d; an inline guest build "+
						"has been measured at 50-110 minutes and this step may attempt two, so it needs "+
						"a bound of its own of at least %d rather than the job's, which is sized for the "+
						"job's own work", w.Path, job.ID, step.DisplayName(), step.TimeoutMinutes, coldBuildMinutes)
				}
			}
		}
	}
	if steps == 0 {
		t.Fatal("no step in this repository's own workflows runs scripts/granule-guest-resolve.sh, so " +
			"nothing in grain CI boots a real sandbox guest and this assertion covers nothing")
	}
}

// guestInputs is what the guest is built from, asked of the script that
// answers it rather than copied from it.
func guestInputs(t *testing.T, root string) []string {
	t.Helper()
	out, err := exec.Command("bash", filepath.Join(root, "scripts", "granule", "guest-inputs.sh"), "guest").Output()
	if err != nil {
		t.Fatalf("running scripts/granule/guest-inputs.sh: %v", err)
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	if len(paths) == 0 {
		t.Fatal("scripts/granule/guest-inputs.sh printed no guest inputs")
	}
	return paths
}

// repoTree is this repository as ci.Tree wants it -- every tracked file,
// and something that stands for its content. What the blob is does not
// matter here: nothing in this test compares two trees, it only asks
// which files a declaration selects.
func repoTree(t *testing.T, root string) *ci.Tree {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		t.Fatalf("listing this repository's files: %v", err)
	}
	tree := &ci.Tree{}
	for _, path := range strings.Split(string(out), "\x00") {
		if path != "" {
			tree.Files = append(tree.Files, ci.TreeFile{Path: path, Blob: path})
		}
	}
	if len(tree.Files) == 0 {
		t.Fatal("this repository has no tracked files, so nothing below tests anything")
	}
	return tree
}

// underPath is every file of the tree at input or beneath it -- the
// expansion `git rev-parse HEAD:<input>` performs implicitly by hashing a
// directory's whole tree object.
func underPath(tree *ci.Tree, input string) []string {
	var files []string
	for _, f := range tree.Files {
		if f.Path == input || strings.HasPrefix(f.Path, strings.TrimSuffix(input, "/")+"/") {
			files = append(files, f.Path)
		}
	}
	return files
}

// The ten globs a guest-resolving job declares, held to
// guest-inputs.sh on their own.
//
// .grain/workflows/kvm-route-stress.yml carries them now, so the test
// above covers the one job in this repository that resolves a guest.
// This stays because the list outlives that job: it is what the next
// guest-resolving job pastes into its own `guest-cache:`, and what makes
// a guest input added to guest-inputs.sh fail here rather than only in
// whichever workflow happens to declare one that day. What it checks is
// the part of writing them that can go wrong quietly -- that these ten
// patterns select every file the script names.
func TestTheGuestInputGlobsCoverWhatTheGuestIsBuiltFrom(t *testing.T) {
	root := repoRoot(t)
	tree := repoTree(t, root)
	patterns := []string{
		"Granulefile*",
		"scripts/granule/build-granule-image.sh",
		"third_party/granule/**",
		"cmd/grain-connect/main.go",
		"cmd/grain-sandbox/**",
		"pkg/hostscriptcli/**",
		"go.mod",
		"go.sum",
		"ui/package.json",
		"ui/package-lock.json",
	}
	selected, ok := tree.Match(patterns, nil)
	if !ok {
		t.Fatal("the guest input globs match no file in this repository")
	}
	covered := map[string]bool{}
	for _, f := range selected {
		covered[f.Path] = true
	}
	for _, input := range guestInputs(t, root) {
		files := underPath(tree, input)
		if len(files) == 0 {
			t.Errorf("scripts/granule/guest-inputs.sh names %q, which is not in this repository", input)
		}
		for _, path := range files {
			if !covered[path] {
				t.Errorf("%s is a guest input (guest-inputs.sh names %q) and none of %v selects it, so a "+
					"`guest-cache:` written from this list would derive one guest key for two trees "+
					"that build different guests", path, input, patterns)
			}
		}
	}
}

// The gate's own registry fetches, and which step each one happens in
// (grain/task-498).
//
// `npm ci` is two things at once: a download from registry.npmjs.org,
// whose failure is this deployment's, and a check that this commit's
// package-lock.json resolves, whose failure is the commit's. It exits 1
// either way. .grain/workflows/tests.yml therefore runs them as two
// steps -- scripts/warm-npm-cache.sh under `infrastructure: true`,
// which is allowed to fail without saying anything about the tree, and
// then an install with `npm_config_offline` set, which cannot reach the
// network and so can only fail on the tree.
//
// Neither half is visible in a job that goes green, so this holds the
// file to the shape: an install without a warm step in front of it is a
// branch one npm outage away from a red tick it did not earn, and an
// install that is not offline is the same thing with extra steps.
//
// Only .grain/workflows/tests.yml, because only this file is the gate:
// a nightly run that fetches is a red mail nobody merges against.
func TestEveryNpmInstallInTheGateInstallsFromAWarmedCache(t *testing.T) {
	const path = ".grain/workflows/tests.yml"
	root := repoRoot(t)
	workflows, _, err := ci.LoadDir(os.DirFS(root))
	if err != nil {
		t.Fatalf("this repository's own %s does not parse: %v", ci.WorkflowDir, err)
	}
	makefile := read(t, "Makefile")
	found := false
	for _, w := range workflows {
		if w.Path != path {
			continue
		}
		found = true
		for _, job := range w.Jobs {
			warmed := false
			for _, step := range job.Steps {
				if strings.Contains(step.Run, "warm-npm-cache.sh") {
					// `infrastructure:` and not merely
					// `continue-on-error:`. The weaker key was what
					// this step could have while the deployment's
					// daemon did not know the stronger one, and it only
					// stopped the fetch failing the job; the point of
					// the split is that a registry outage is *errored*
					// -- no verdict about the tree, no repair task,
					// nothing cached -- which is the word only
					// `infrastructure:` says (grain/task-546).
					if !step.Infrastructure {
						t.Errorf("%s: job %s fills the npm cache in a step that does not say "+
							"`infrastructure: true`, so a registry that will not answer is read as "+
							"something about this commit -- which is the bug this split exists to "+
							"fix, arriving through the fix", path, job.ID)
					}
					warmed = true
					continue
				}
				if !stepInstallsNpmPackages(step, makefile) {
					continue
				}
				if !warmed {
					t.Errorf("%s: job %s installs npm packages in step %q with nothing having filled "+
						"npm's cache first: whatever that step has to fetch, it fetches inside a step "+
						"whose failure is read as a verdict about the tree", path, job.ID, step.DisplayName())
				}
				if resolvedEnv(w, job, step)["npm_config_offline"] == "" {
					t.Errorf("%s: job %s installs npm packages in step %q without npm_config_offline, so "+
						"the install can still reach the registry -- and a registry that fails it there "+
						"reddens the branch", path, job.ID, step.DisplayName())
				}
			}
		}
	}
	if !found {
		t.Fatalf("%s is not among this repository's workflows", path)
	}
}

// Playwright's browsers are the gate's other download: ~300MB from
// cdn.playwright.dev and an apt-get, which say as much about the tree as
// npm's registry does. `make playwright-browsers` exists so that the
// download can be a step of its own, and a step of its own is what can
// carry `infrastructure:` -- which it now does (grain/task-546). A
// suite step that installs its own browsers again would put the
// download back inside the step whose failure is the verdict, which is
// the whole of what grain/task-498 took out of it; a bare step would
// leave it there under another name.
func TestTheBrowserDownloadInTheGateIsAStepOfItsOwn(t *testing.T) {
	const path = ".grain/workflows/tests.yml"
	for _, jobID := range []string{"ui-e2e"} {
		job := grainJob(t, path, jobID)
		found := false
		for _, step := range job.Steps {
			if !slices.Contains(makeTargets(step.Run), "playwright-browsers") {
				continue
			}
			found = true
			if !step.Infrastructure {
				t.Errorf("%s: job %s downloads Playwright's browsers in step %q without "+
					"`infrastructure: true`, so cdn.playwright.dev and an apt mirror can still fail "+
					"this job as a verdict about the commit", path, jobID, step.DisplayName())
			}
			break
		}
		if !found {
			t.Errorf("%s: job %s no longer runs `make playwright-browsers` in a step of its own -- if "+
				"the browsers are installed by the step that runs the suite again, that download is back in "+
				"the verdict's path", path, jobID)
		}
	}
}

// resolvedEnv is what one step of a job really sees: the workflow's
// environment, the job's over it, the step's over both -- pkg/ci's own
// layering (stepEnv), said here because a test that read only the step
// would pass a file that declares nothing anywhere.
func resolvedEnv(w ci.Workflow, job ci.Job, step ci.Step) map[string]string {
	env := map[string]string{}
	for _, layer := range []map[string]string{w.Env, job.Env, step.Env} {
		for name, value := range layer {
			env[name] = value
		}
	}
	return env
}

// stepInstallsNpmPackages reports whether running this step reaches an
// `npm ci` -- directly, or through a make target whose own recipe or
// prerequisites do. The Makefile is read rather than guessed at, since
// which targets install is exactly the thing that drifts: `make
// test-e2e` never mentions npm and installs it through two levels of
// prerequisite.
func stepInstallsNpmPackages(step ci.Step, makefile string) bool {
	if strings.Contains(step.Run, "npm ci") || strings.Contains(step.Run, "npm install") {
		return true
	}
	for _, target := range makeTargets(step.Run) {
		if makeTargetInstallsNpmPackages(makefile, target, map[string]bool{}) {
			return true
		}
	}
	return false
}

func makeTargetInstallsNpmPackages(makefile, target string, seen map[string]bool) bool {
	if seen[target] {
		return false
	}
	seen[target] = true
	recipe, prerequisites, ok := makeRule(makefile, target)
	if !ok {
		return false
	}
	if strings.Contains(recipe, "npm ci") || strings.Contains(recipe, "npm install") {
		return true
	}
	for _, prerequisite := range prerequisites {
		if makeTargetInstallsNpmPackages(makefile, prerequisite, seen) {
			return true
		}
	}
	return false
}

// makeRule pulls one rule out of the Makefile as text: its
// prerequisites, and the tab-indented lines under it. Enough of make's
// syntax for this repository's own Makefile, which is where it is only
// ever pointed -- there are no pattern rules, no double-colon rules and
// no target-specific variables in it.
func makeRule(makefile, target string) (recipe string, prerequisites []string, ok bool) {
	lines := strings.Split(makefile, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, target+":") || strings.HasPrefix(line, ".PHONY") {
			continue
		}
		ok = true
		prerequisites = strings.Fields(strings.TrimPrefix(line, target+":"))
		var recipeLines []string
		for _, next := range lines[i+1:] {
			if strings.HasPrefix(next, "\t") {
				recipeLines = append(recipeLines, next)
				continue
			}
			if strings.TrimSpace(next) == "" || strings.HasPrefix(next, "#") {
				continue
			}
			break
		}
		return strings.Join(recipeLines, "\n"), prerequisites, true
	}
	return "", nil, ok
}

// The release must run on the deployment that is going to publish it,
// including one older than the `image-signing` provider (grain/task-2016).
// Requiring the capability stopped every release on such a build in
// under a minute, and the release is the only thing that publishes the
// build that has the provider. So the images job requires only what
// every deployment that publishes at all provides, declares the key as
// optional so a granted one signs every release, and nothing in it
// turns a missing signing key from scripts/release-images.sh's warning
// into a failure.
func TestTheReleaseDoesNotRequireImageSigning(t *testing.T) {
	job := grainJob(t, ".grain/workflows/release.yml", "images")
	for _, id := range job.Capabilities {
		if id == "image-signing" {
			t.Errorf("the release images job requires image-signing: a deployment without its provider refuses every release, including the one that would upgrade it -- declare it under optional-capabilities instead")
		}
	}
	if !slices.Contains(job.OptionalCapabilities, "image-signing") {
		t.Errorf("the release images job does not declare image-signing under optional-capabilities (has %v): a granted signing key would never reach the push, and every release would publish unsigned", job.OptionalCapabilities)
	}
	if v, ok := job.Env["REQUIRE_IMAGE_SIGNING"]; ok && v != "false" {
		t.Errorf("the release images job sets REQUIRE_IMAGE_SIGNING=%q, which fails every release that has no signing key", v)
	}
	for _, step := range job.Steps {
		if v, ok := step.Env["REQUIRE_IMAGE_SIGNING"]; ok && v != "false" {
			t.Errorf("step %q sets REQUIRE_IMAGE_SIGNING=%q, which fails every release that has no signing key", step.Name, v)
		}
		if strings.Contains(step.Run, "REQUIRE_IMAGE_SIGNING=true") {
			t.Errorf("step %q sets REQUIRE_IMAGE_SIGNING=true, which fails every release that has no signing key", step.Name)
		}
	}
}

// third_party/granule is a module of its own, so the main module's shards
// never reach it: the `granule` job is the only thing in the gate that runs
// its tests, pkg/forward's refusal of out-of-spec Open frames among them.
// It has to stay the whole module, under -race, in that directory, off the
// vendor tree, and with no credential.
func TestTheGranuleJobRunsTheVendoredModulesOwnTests(t *testing.T) {
	const path = ".grain/workflows/tests.yml"
	job := grainJob(t, path, "granule")
	if !job.NoCapabilities || len(job.Capabilities) != 0 {
		t.Errorf("%s: the granule job must say `capabilities: []` (has %v)", path, job.Capabilities)
	}
	if got := job.Env["GOFLAGS"]; got != "-mod=vendor" {
		t.Errorf("%s: the granule job sets GOFLAGS=%q, want -mod=vendor so it builds from third_party/granule/vendor", path, got)
	}
	if !slices.Contains(job.Paths, "third_party/granule/**") {
		t.Errorf("%s: the granule job is not keyed on third_party/granule/** (paths %v)", path, job.Paths)
	}
	found := false
	for _, step := range job.Steps {
		if step.Name != "Test" {
			continue
		}
		found = true
		if step.WorkingDirectory != "third_party/granule" {
			t.Errorf("%s: the granule job's Test step runs in %q, want third_party/granule", path, step.WorkingDirectory)
		}
		fields := strings.Fields(step.Run)
		for _, want := range []string{"test", "-race", "./..."} {
			if !slices.Contains(fields, want) {
				t.Errorf("%s: the granule job's Test step does not pass %s to go test:\n%s", path, want, step.Run)
			}
		}
	}
	if !found {
		t.Errorf("%s: the granule job has no Test step", path)
	}
	if _, err := os.Stat(filepath.Join(repoRoot(t), "third_party", "granule", "pkg", "forward", "forward_test.go")); err != nil {
		t.Errorf("the test this job exists to run is gone: %v", err)
	}
}

// Every Go test step in the gate streams concise live progress through
// scripts/run-go-tests.sh rather than redirecting `go test` silently to a
// file or running a multi-minute package with neither -v nor -json.
func TestTheGrainGoTestStepsStreamProgressThroughRunGoTests(t *testing.T) {
	const tests = ".grain/workflows/tests.yml"
	const nightly = ".grain/workflows/nightly.yml"
	for _, tc := range []struct {
		path string
		job  string
		step string
	}{
		{tests, "go", "Test"},
		{tests, "go-e2e", "Test"},
		{tests, "go-orchestrator", "Test"},
		{tests, "go-model", "Test"},
		{tests, "go-unit", "Test"},
		{tests, "granule", "Test"},
		{tests, "postgres", "Test"},
		{tests, "run-host", "Test"},
		{tests, "run-host", "Daemon restart acceptance test"},
		{tests, "installer-e2e", "Run the installer e2e suite"},
		{tests, "package-cache", "Pull an image through the cache this sandbox is pointed at"},
		{tests, "package-cache", "Serve a container's own fetches through real interception"},
		// The two installer variants that left the gate (grain/task-980).
		{nightly, "installer-variants", "Run the installer trixie e2e suite"},
		{nightly, "installer-variants", "Run the installer hostmode e2e suite"},
	} {
		path := tc.path
		job := grainJob(t, path, tc.job)
		found := false
		for _, step := range job.Steps {
			if step.Name != tc.step {
				continue
			}
			found = true
			if !strings.Contains(step.Run, "scripts/run-go-tests.sh") {
				t.Errorf("%s: job %s step %q does not run scripts/run-go-tests.sh, so it produces no concise live progress stream:\n%s",
					path, tc.job, tc.step, step.Run)
			}
			if strings.Contains(step.Run, "> /tmp/gotest.log") {
				t.Errorf("%s: job %s step %q redirects `go test` directly to /tmp/gotest.log, leaving stdout blank until `go test` finishes",
					path, tc.job, tc.step)
			}
		}
		if !found {
			t.Errorf("%s: job %s has no step %q", path, tc.job, tc.step)
		}
	}
}

// The host-script grant is materialized into a container sandbox running
// the stock sandbox image built from the commit under test, on every push:
// that image once failed every capability grant with a port or a
// placement, and nothing ran one against it.
func TestTheStockSandboxImageTakesACapabilityGrantOnEveryPush(t *testing.T) {
	job := stripComments(namedJob(t, read(t, ".grain", "workflows", "tests.yml"), "host-script-container"))
	contains(t, job, "docker build -q -f Dockerfile.k8s-sandbox")
	contains(t, job, "GRAIN_HOST_SCRIPT_CONTAINER_IMAGE: grain-k8s-sandbox:host-script")
	contains(t, job, "-t grain-k8s-sandbox:host-script")
	if n := strings.Count(job, "TestHostScriptGrantInARealContainerSandbox"); n < 2 {
		t.Errorf("tests.yml's host-script-container job names TestHostScriptGrantInARealContainerSandbox %d times; it needs both the -run pattern and the PASS assertion", n)
	}
	contains(t, read(t, "pkg", "orchestrator", "host_script_real_test.go"), "func TestHostScriptGrantInARealContainerSandbox(")
}

// What left the pull request for the nightly suite (grain/task-980) has to
// have arrived there. Moving a job is two edits in two files, and the
// failure is silent in exactly one direction: delete it from tests.yml,
// forget nightly.yml, and nothing is red anywhere -- the suite simply stops
// existing. So each tests/installer package that has tests must be driven,
// with its own opt-in set, by tests.yml or by nightly.yml; and every
// Playwright project ui/playwright.config.js declares must be run by one
// of them, which for the gate means a job naming it in PLAYWRIGHT_PROJECT
// and for the nightly means `make test-e2e-long` with no project named at
// all. A package added under tests/installer, or a project added to the
// config, fails here until somebody decides where it runs.
func TestEverythingTheGateDoesNotRunTheNightlyDoes(t *testing.T) {
	root := repoRoot(t)
	workflows, _, err := ci.LoadDir(os.DirFS(root))
	if err != nil {
		t.Fatalf("this repository's own %s does not parse: %v", ci.WorkflowDir, err)
	}
	const tests = ".grain/workflows/tests.yml"
	const nightly = ".grain/workflows/nightly.yml"
	type runner struct {
		workflow ci.Workflow
		job      ci.Job
		step     ci.Step
	}
	var steps []runner
	for _, w := range workflows {
		if w.Path != tests && w.Path != nightly {
			continue
		}
		for _, job := range w.Jobs {
			for _, step := range job.Steps {
				steps = append(steps, runner{w, job, step})
			}
		}
	}

	// tests/installer's packages, each with the variable that opts its
	// tests in (every one of them skips without it).
	cmd := exec.Command("go", "list", "-f", "{{.ImportPath}} {{join .TestGoFiles \",\"}}", "./tests/installer/...")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list ./tests/installer/...: %v\n%s", err, out)
	}
	const module = "github.com/ai-on-gke/grain/"
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		dir := strings.TrimPrefix(fields[0], module)
		pkg := "./" + dir
		var source strings.Builder
		for _, file := range strings.Split(fields[1], ",") {
			source.WriteString(read(t, dir, file))
		}
		optIn := regexp.MustCompile(`Getenv\("(GRAIN_INSTALLER[A-Z_]*_E2E)"\)|optIn = "(GRAIN_INSTALLER[A-Z_]*_E2E)"`).FindStringSubmatch(source.String())
		if optIn == nil {
			t.Errorf("%s's tests read no GRAIN_INSTALLER_*_E2E opt-in, so this cannot tell whether a workflow really runs it", pkg)
			continue
		}
		optInName := optIn[1] + optIn[2]
		found := false
		for _, r := range steps {
			if resolvedEnv(r.workflow, r.job, r.step)[optInName] == "" {
				continue
			}
			if runsALine(ci.Job{Steps: []ci.Step{r.step}}, func(line string) bool {
				return strings.Contains(line, "go test "+pkg+" ")
			}) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no step of %s or %s runs `go test %s` with %s set: it runs nowhere before a merge, "+
				"and a skip-only package still says ok -- put it in the gate or in the nightly", tests, nightly, pkg, optInName)
		}
	}

	// The Playwright projects.
	config := read(t, "ui", "playwright.config.js")
	projects := regexp.MustCompile(`name:\s*"([a-z]+)"`).FindAllStringSubmatch(config, -1)
	if len(projects) == 0 {
		t.Fatal("ui/playwright.config.js declares no project this could read")
	}
	nightlyRunsAll := false
	for _, r := range steps {
		if r.workflow.Path == nightly && slices.Contains(makeTargets(r.step.Run), "test-e2e-long") &&
			resolvedEnv(r.workflow, r.job, r.step)["PLAYWRIGHT_PROJECT"] == "" {
			nightlyRunsAll = true
		}
	}
	gateRunsChromium := false
	for _, project := range projects {
		name := project[1]
		inGate := false
		for _, r := range steps {
			if r.workflow.Path == tests && resolvedEnv(r.workflow, r.job, r.step)["PLAYWRIGHT_PROJECT"] == name &&
				slices.Contains(makeTargets(r.step.Run), "test-e2e-suite") {
				inGate = true
			}
		}
		if name == "chromium" {
			gateRunsChromium = inGate
		}
		if !inGate && !nightlyRunsAll {
			t.Errorf("the %q Playwright project runs in neither %s (no job sets PLAYWRIGHT_PROJECT=%s) nor %s "+
				"(no `make test-e2e-long` job running every project)", name, tests, name, nightly)
		}
	}
	if !gateRunsChromium {
		t.Errorf("%s runs no job with PLAYWRIGHT_PROJECT=chromium, so the browser suite gates no pull request at all", tests)
	}
}

// releaseTrainPath is the daily release train (docs/design/release-from-trunk.md,
// RFT-6): it cuts, soaks and stages releases from main, and a person's
// approval through promote-release.yml is what makes one official.
const releaseTrainPath = ".grain/workflows/release-train.yml"

func releaseTrainWorkflow(t *testing.T) ci.Workflow {
	t.Helper()
	workflows, _, err := ci.LoadDir(os.DirFS(repoRoot(t)))
	if err != nil {
		t.Fatalf("this repository's own %s does not parse: %v", ci.WorkflowDir, err)
	}
	for _, w := range workflows {
		if w.Path == releaseTrainPath {
			return w
		}
	}
	t.Fatalf("%s does not exist", releaseTrainPath)
	return ci.Workflow{}
}

// The train runs daily at 04:15 UTC from grain's own cron, and on an
// operator's dispatch, with what scripts/release-from-trunk.sh needs and
// nothing else: repo-write for the rc, -discarded and -pending tags and
// refs/heads/candidate, release-gate-read for main's runs, staging and the
// release-notes task, and package-publish for the rc and pending images.
// Signing is optional, as in release.yml.
func TestTheReleaseTrainRunsDailyWithTheCapabilitiesItUses(t *testing.T) {
	w := releaseTrainWorkflow(t)
	var crons []string
	for _, s := range w.On.Schedule {
		crons = append(crons, s.Cron)
	}
	if !slices.Equal(crons, []string{"15 4 * * *"}) {
		t.Errorf("%s is scheduled at %v, want the one daily cron \"15 4 * * *\"", releaseTrainPath, crons)
	}
	if !w.On.Matches(ci.EventManual, "main") {
		t.Errorf("%s does not answer workflow_dispatch on main (%s)", releaseTrainPath, w.On.Describe())
	}
	for _, event := range []ci.Event{ci.EventPush, ci.EventPullRequest, ci.EventRelease} {
		if w.On.Matches(event, "main") {
			t.Errorf("%s answers %s on main: the train runs once a day, not on every merge", releaseTrainPath, event)
		}
	}
	if len(w.Jobs) != 1 {
		t.Fatalf("%s declares %d jobs, want the one train job", releaseTrainPath, len(w.Jobs))
	}
	job := w.Jobs[0]
	want := []string{"repo-write:ai-on-gke/grain", "release-gate-read", "package-publish"}
	if !slices.Equal(job.Capabilities, want) {
		t.Errorf("%s job %s holds %v, want %v", releaseTrainPath, job.ID, job.Capabilities, want)
	}
	if !slices.Equal(job.OptionalCapabilities, []string{"image-signing"}) {
		t.Errorf("%s job %s has optional capabilities %v, want [image-signing]: a deployment without a signing key must still release",
			releaseTrainPath, job.ID, job.OptionalCapabilities)
	}
	if job.Cache {
		t.Errorf("%s job %s may be replayed from the cache; it reads the remote's tags and staging, which the tree does not hold",
			releaseTrainPath, job.ID)
	}
	if !runsALine(job, func(line string) bool { return line == "bash ./scripts/release-from-trunk.sh" }) {
		t.Errorf("%s job %s does not run `bash ./scripts/release-from-trunk.sh` in its train mode", releaseTrainPath, job.ID)
	}
}

type releaseTrainCredential string

func (c releaseTrainCredential) Resolve(context.Context, string) (string, error) {
	return string(c), nil
}

type releaseTrainPackageCredential struct{}

func (releaseTrainPackageCredential) PackageCredential(registry, _, _ string) (packagepublish.Credential, error) {
	if registry == "" {
		registry = "ghcr.io"
	}
	return packagepublish.Credential{Registry: registry, Username: "bot", Token: "secret", Name: "test"}, nil
}

// The train's capabilities resolve for the repository it runs in. After
// the 2026-10-28 cutover the workflow named repo-write:ai-on-gke/grain
// while release-gate-read was still offered to bwsalmon/grain only, and
// every train on ai-on-gke/grain was refused before it started (task
// 1787): release-gate-read's default repository is model.GrainSourceRepo,
// and that has to be the repository the train is declared in.
func TestTheReleaseTrainsCapabilitiesAreGrantedOnGrainsOwnRepository(t *testing.T) {
	repo := model.GrainSourceRepo()
	if want := (model.RepoRef{Owner: "ai-on-gke", Name: "grain"}); !repo.Same(want) {
		t.Fatalf("model.GrainSourceRepo() = %s, want %s", repo, want)
	}
	job := releaseTrainWorkflow(t).Jobs[0]
	gate := ciapi.NewEndpoint()
	gate.SetURL("http://192.0.2.1:9000")
	reg := model.NewCapabilityRegistry(
		ciapi.NewAlias(gate, ciapi.Config{}, model.ReleaseGateReadCapability, ciapi.Aliases[model.ReleaseGateReadCapability]),
		packagepublish.New(packagepublish.Config{Credentials: releaseTrainPackageCredential{}}),
		repowrite.New(repo),
	)
	creds := releaseTrainCredential(`{"registry":"ghcr.io","username":"bot","token":"secret"}`)

	// An operator's dispatch holds every one of them; grain's own cron
	// holds all but package-publish, which stays release and dispatch only.
	for _, tc := range []struct {
		event   ci.Event
		trusted bool
		want    []string
	}{
		{ci.EventManual, false, job.Capabilities},
		{ci.EventSchedule, true, []string{model.RepoWriteCapability(repo), model.ReleaseGateReadCapability}},
	} {
		cc, resolved, err := daemon.ResolveCICapabilities(context.Background(), reg, creds, repo, "ci-job", tc.event, tc.trusted, job.Capabilities)
		if err != nil {
			t.Fatalf("%s: resolving %s's capabilities for %s: %v", tc.event, releaseTrainPath, repo, err)
		}
		for _, r := range resolved {
			if r.Resolution.Refused {
				t.Errorf("%s: %s was refused to %s's train: %s", tc.event, r.Grant.Capability, repo, r.Resolution.Reason)
			}
		}
		var granted []string
		for _, g := range cc.Task.Grants {
			granted = append(granted, g.Capability)
		}
		for _, id := range tc.want {
			if !slices.Contains(granted, id) {
				t.Errorf("%s: %s's train holds %v, want %s among them", tc.event, repo, granted, id)
			}
		}
	}
}

// The train stops at vX.Y.0-pending (task 1236): nothing it runs may push
// vX.Y.0, move `latest`, or call the approve or promote modes that do. Only
// .grain/workflows/promote-release.yml, which a person starts, makes a
// release official.
func TestTheReleaseTrainNeverPromotesARelease(t *testing.T) {
	w := releaseTrainWorkflow(t)
	forbidden := []string{
		"release-from-trunk.sh approve", "release-from-trunk.sh reject",
		"release-images.sh promote", "promote-release.yml", "promote.yml",
		"refs/tags/latest", "git tag", "git push",
	}
	for _, job := range w.Jobs {
		for _, step := range job.Steps {
			for _, f := range forbidden {
				if strings.Contains(step.Run, f) {
					t.Errorf("%s job %s step %q runs %q: the train never promotes a release; a person approves it through promote-release.yml",
						releaseTrainPath, job.ID, step.Name, f)
				}
			}
		}
	}
	script := read(t, "scripts", "release-from-trunk.sh")
	if !strings.Contains(script, `release-images.sh pending`) && !strings.Contains(script, `images pending`) {
		t.Error("scripts/release-from-trunk.sh no longer publishes a passing soak's images as :vX.Y.0-pending")
	}
}

// The release-notes template is the one the train files when a release is
// staged as vX.Y.0-pending (scripts/release-from-trunk.sh's
// file_release_notes): the stem it names, the fields it fills, on main and
// merged without a person, writing docs/releases/vX.Y.0.md with
// scripts/release-notes.sh and drafted highlights.
func TestTheReleaseNotesTemplateIsWhatTheTrainFiles(t *testing.T) {
	root := repoRoot(t)
	decl, err := repodecl.Load(os.DirFS(root), model.GrainSourceRepo())
	if err != nil {
		t.Fatalf("this repository's declarations do not load: %v", err)
	}
	script := read(t, "scripts", "release-from-trunk.sh")
	if !strings.Contains(script, `{template: "release-notes", fields: {version: $v, previous_version: $p}}`) {
		t.Fatal("scripts/release-from-trunk.sh no longer files the release-notes template with version and previous_version; update this test with it")
	}
	var tmpl *model.Template
	for i, f := range decl.Templates {
		if f.Path == ".grain/templates/release-notes.yml" {
			tmpl = &decl.Templates[i].Template
		}
	}
	if tmpl == nil {
		t.Fatal(".grain/templates/release-notes.yml is not among the declared templates")
	}
	if tmpl.ID != model.RepoDeclaredID(model.DeclaredTemplate, model.GrainSourceRepo(), "release-notes") {
		t.Errorf("release-notes template id = %q; the train files it by the stem release-notes", tmpl.ID)
	}
	if tmpl.Base != "main" || !tmpl.AutoMerge {
		t.Errorf("release-notes template base %q, auto-merge %v; want base main and auto-merge true", tmpl.Base, tmpl.AutoMerge)
	}
	fields := map[string]bool{}
	for _, f := range tmpl.Fields {
		fields[f.Name] = true
	}
	for _, name := range []string{"version", "previous_version"} {
		if !fields[name] {
			t.Errorf("release-notes template declares no %q field, which the train fills", name)
		}
	}
	for _, want := range []string{
		"scripts/release-notes.sh", "Highlights", "docs/releases/{{version}}.md", "{{version}}-pending", "nothing_needed",
	} {
		if !strings.Contains(tmpl.Body, want) {
			t.Errorf("release-notes template body does not mention %q", want)
		}
	}
}
