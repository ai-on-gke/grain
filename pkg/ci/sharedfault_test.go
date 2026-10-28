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

package ci

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// proxy405 is the failure this file was written for: six branches, one
// deployment-wide cause, byte-identical output from the package cache
// down to the minute.
const proxy405 = `+ debootstrap --arch=amd64 trixie /build/root https://deb.debian.org/debian
I: Retrieving InRelease
E: Failed getting release file https://deb.debian.org/debian/dists/trixie/InRelease
E: proxy CONNECT returned 405 Method Not Allowed
debootstrap exited 1`

func failedRun(id, ref, commit string, at time.Time, jobID, step, output string) Run {
	finished := at
	return Run{
		ID:         id,
		Repo:       "gke-labs/kontur",
		Ref:        ref,
		Commit:     commit,
		FinishedAt: &finished,
		Conclusion: ConclusionFailure,
		Jobs: []JobRun{{
			JobID:      jobID,
			Name:       strings.ToUpper(jobID[:1]) + jobID[1:],
			Conclusion: ConclusionFailure,
			FinishedAt: &finished,
			Sandbox:    "ci-" + id,
			Steps: []StepRun{
				{Name: "checkout", Conclusion: ConclusionSuccess},
				{Name: step, Conclusion: ConclusionFailure, ExitCode: 1, Output: output},
			},
		}},
	}
}

func TestSharedFaultsNamesEveryBranchOneFaultIsOn(t *testing.T) {
	now := time.Date(2026, 9, 18, 17, 0, 0, 0, time.UTC)
	runs := []Run{
		failedRun("r1", "grain/task-238", "aaaaaaa1", now.Add(-10*time.Minute), "image", "build the image", proxy405),
		failedRun("r2", "grain/task-246", "bbbbbbb2", now.Add(-40*time.Minute), "image", "build the image", proxy405),
		failedRun("r3", "grain/task-251", "ccccccc3", now.Add(-90*time.Minute), "image", "build the image", proxy405),
	}
	faults := SharedFaults(runs, now, 0)
	if len(faults) != 1 {
		t.Fatalf("want one fault, got %d: %+v", len(faults), faults)
	}
	f := faults[0]
	if f.Seen() != 3 {
		t.Fatalf("want three branches, got %d (%v)", f.Seen(), f.Refs)
	}
	if f.JobID != "image" || f.Step != "build the image" {
		t.Fatalf("fault names the wrong job or step: %+v", f)
	}
	// Newest first, so the row a reader is looking at is the one at the
	// top of the list of the others.
	if f.Refs[0] != "grain/task-238" {
		t.Fatalf("want the newest failure first, got %v", f.Refs)
	}
	if got := f.Others("grain/task-238"); len(got) != 2 || got[0] != "grain/task-246" {
		t.Fatalf("Others left the reader's own branch in, or lost one: %v", got)
	}
	if _, ok := FaultFor(faults, "r2", "image"); !ok {
		t.Fatal("a run in the fault cannot find it by id")
	}
	if _, ok := FaultFor(faults, "r2", "lint"); ok {
		t.Fatal("a job that never failed was given another job's fault")
	}
}

// The clock, the commit, the branch and the sandbox differ on every
// branch by construction. A digest that did not mask them would never
// match anything, and the feature would silently never fire.
func TestSharedFaultsIgnoresWhatCannotBeEqualAcrossBranches(t *testing.T) {
	now := time.Date(2026, 9, 18, 17, 0, 0, 0, time.UTC)
	const output = `2026-09-18T14:31:02Z starting build of %s at %s
running for 41.3s in sandbox %s
E: proxy CONNECT returned 405 Method Not Allowed to the package cache
build failed`
	mk := func(id, ref, commit string, at time.Time) Run {
		run := failedRun(id, ref, commit, at, "image", "build the image", "")
		run.Jobs[0].Steps[1].Output = sprintf(output, ref, commit, "ci-"+id)
		return run
	}
	runs := []Run{
		mk("r1", "grain/task-238", "aaaaaaa1", now.Add(-5*time.Minute)),
		mk("r2", "grain/task-246", "bbbbbbb2", now.Add(-25*time.Minute)),
	}
	if got := SharedFaults(runs, now, 0); len(got) != 1 {
		t.Fatalf("want one fault across two branches, got %d", len(got))
	}
}

func TestSharedFaultsKeepsBranchesApartWhenTheyFailDifferently(t *testing.T) {
	now := time.Date(2026, 9, 18, 17, 0, 0, 0, time.UTC)
	runs := []Run{
		failedRun("r1", "grain/task-238", "aaaaaaa1", now.Add(-5*time.Minute), "go", "go test ./...",
			"--- FAIL: TestQueueOrdersByPriority\n    queue_test.go:112: want 3 tasks, got 2\nFAIL\tgithub.com/ai-on-gke/grain/pkg/dispatch\t2.1s"),
		failedRun("r2", "grain/task-246", "bbbbbbb2", now.Add(-6*time.Minute), "go", "go test ./...",
			"--- FAIL: TestSandboxBudgetHoldsAJob\n    gate_test.go:44: want admitted, got held\nFAIL\tgithub.com/ai-on-gke/grain/pkg/ci\t3.4s"),
	}
	if got := SharedFaults(runs, now, 0); len(got) != 0 {
		t.Fatalf("two different test failures were called one fault: %+v", got)
	}
}

func TestSharedFaultsRefusesOneBranchFailingTwice(t *testing.T) {
	now := time.Date(2026, 9, 18, 17, 0, 0, 0, time.UTC)
	runs := []Run{
		failedRun("r1", "grain/task-238", "aaaaaaa1", now.Add(-5*time.Minute), "image", "build the image", proxy405),
		failedRun("r2", "grain/task-238", "aaaaaaa2", now.Add(-50*time.Minute), "image", "build the image", proxy405),
	}
	if got := SharedFaults(runs, now, 0); len(got) != 0 {
		t.Fatalf("one branch's repeated failure was called a shared fault: %+v", got)
	}
}

func TestSharedFaultsRefusesFailuresTooFarApart(t *testing.T) {
	now := time.Date(2026, 9, 18, 17, 0, 0, 0, time.UTC)
	runs := []Run{
		failedRun("r1", "grain/task-238", "aaaaaaa1", now.Add(-5*time.Minute), "image", "build the image", proxy405),
		failedRun("r2", "grain/task-246", "bbbbbbb2", now.Add(-30*time.Hour), "image", "build the image", proxy405),
	}
	if got := SharedFaults(runs, now, 0); len(got) != 0 {
		t.Fatalf("yesterday's failure was joined to today's: %+v", got)
	}
}

// "exit status 1" is every failure in the repository. Grouping on it
// would put unrelated branches under one heading on the strength of
// nothing at all.
func TestSharedFaultsRefusesAFailureWithNothingToSay(t *testing.T) {
	now := time.Date(2026, 9, 18, 17, 0, 0, 0, time.UTC)
	runs := []Run{
		failedRun("r1", "grain/task-238", "aaaaaaa1", now.Add(-5*time.Minute), "go", "make", "exit status 1"),
		failedRun("r2", "grain/task-246", "bbbbbbb2", now.Add(-6*time.Minute), "go", "make", "exit status 1"),
	}
	if got := SharedFaults(runs, now, 0); len(got) != 0 {
		t.Fatalf("a failure with no distinctive output was called a fault: %+v", got)
	}
}

// A listing drops every step's output (RunStore.List), and a fault
// claimed off no evidence is exactly the wrong answer.
func TestSharedFaultsSaysNothingWithoutOutput(t *testing.T) {
	now := time.Date(2026, 9, 18, 17, 0, 0, 0, time.UTC)
	runs := []Run{
		failedRun("r1", "grain/task-238", "aaaaaaa1", now.Add(-5*time.Minute), "image", "build the image", ""),
		failedRun("r2", "grain/task-246", "bbbbbbb2", now.Add(-6*time.Minute), "image", "build the image", ""),
	}
	if got := SharedFaults(runs, now, 0); len(got) != 0 {
		t.Fatalf("a fault was reported from runs with no output: %+v", got)
	}
}

// A step that exited non-zero and a job grain never managed to run are
// different answers to "why is this red" -- ConclusionErrored's own
// argument -- and must never be folded together.
func TestSharedFaultsKeepsAnErroredJobApartFromAFailingStep(t *testing.T) {
	now := time.Date(2026, 9, 18, 17, 0, 0, 0, time.UTC)
	const text = "acquiring a sandbox: no machine free after 30m, the deployment is at its memory budget"
	failing := failedRun("r1", "grain/task-238", "aaaaaaa1", now.Add(-5*time.Minute), "image", "build the image", text)
	errored := failedRun("r2", "grain/task-246", "bbbbbbb2", now.Add(-6*time.Minute), "image", "build the image", "")
	errored.Jobs[0].Conclusion = ConclusionErrored
	errored.Jobs[0].Steps = nil
	errored.Jobs[0].Error = text
	if got := SharedFaults([]Run{failing, errored}, now, 0); len(got) != 0 {
		t.Fatalf("a failing step and an errored job were called one fault: %+v", got)
	}
	// But two errored jobs with the same story are one fault: a
	// deployment that cannot build sandboxes is not six branches' problem
	// either.
	second := errored
	second.ID, second.Ref = "r3", "grain/task-251"
	second.Jobs[0].Sandbox = "ci-r3"
	if got := SharedFaults([]Run{errored, second}, now, 0); len(got) != 1 {
		t.Fatalf("want one fault across two errored jobs, got %d", len(got))
	}
}

func TestSharedFaultsNamesOnlyTheFilesEveryBranchPrinted(t *testing.T) {
	now := time.Date(2026, 9, 18, 17, 0, 0, 0, time.UTC)
	// Both branches print the same failure, and each also prints a path
	// with its own branch in it -- a checkout directory, which is how a
	// per-branch path gets into output that is otherwise identical: the
	// ref is masked out of the digest (so the two are one fault) and is
	// still there in the path (so only one of them printed it).
	output := func(ref string) string {
		return `writing cache/` + ref + `/manifest.json
scripts/granule/guest-inputs.sh: line 40: debootstrap failed
E: proxy CONNECT returned 405 Method Not Allowed for https://deb.debian.org/debian/dists/trixie/InRelease
see /usr/share/debootstrap/scripts/trixie for the suite definition`
	}
	runs := []Run{
		failedRun("r1", "grain/task-238", "aaaaaaa1", now.Add(-5*time.Minute), "image", "build the image", output("grain/task-238")),
		failedRun("r2", "grain/task-246", "bbbbbbb2", now.Add(-6*time.Minute), "image", "build the image", output("grain/task-246")),
	}
	faults := SharedFaults(runs, now, 0)
	if len(faults) != 1 {
		t.Fatalf("want one fault, got %d", len(faults))
	}
	got := faults[0].Paths
	if len(got) != 1 || got[0] != "scripts/granule/guest-inputs.sh" {
		t.Fatalf("want only the path both branches printed, got %v", got)
	}
}

func TestPathsInRefusesURLsAndSystemFiles(t *testing.T) {
	line := "fetching https://deb.debian.org/debian/pool/main/g/grain.deb failed; /usr/lib/apt/methods/http died, see scripts/build.sh and node_modules/left-pad/index.js"
	got := pathsIn(line)
	if len(got) != 1 || got[0] != "scripts/build.sh" {
		t.Fatalf("want the repository's own file alone, got %v", got)
	}
}

func TestSameFileMatchesBothSpellingsOfOnePath(t *testing.T) {
	cases := []struct {
		printed, touched string
		want             bool
	}{
		{"scripts/build.sh", "scripts/build.sh", true},
		{"/home/agent/work/scripts/build.sh", "scripts/build.sh", true},
		{"./scripts/build.sh", "scripts/build.sh", true},
		{"build.sh", "scripts/build.sh", true},
		{"lib/state.go", "ui/relib/state.go", false},
		{"scripts/build.sh", "scripts/other.sh", false},
		{"", "scripts/build.sh", false},
	}
	for _, c := range cases {
		if got := SameFile(c.printed, c.touched); got != c.want {
			t.Errorf("SameFile(%q, %q) = %v, want %v", c.printed, c.touched, got, c.want)
		}
	}
}

// sprintf keeps the table above readable without importing fmt for one
// call.
func sprintf(format string, args ...string) string {
	out := format
	for _, arg := range args {
		out = strings.Replace(out, "%s", arg, 1)
	}
	return out
}

func TestNormalizeFailureStripsGitHubActionsLogNoiseAndRunnerPaths(t *testing.T) {
	linuxBytes, err := os.ReadFile(filepath.Join("testdata", "github_actions_linux.log"))
	if err != nil {
		t.Fatal(err)
	}
	winBytes, err := os.ReadFile(filepath.Join("testdata", "github_actions_windows.log"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 27, 12, 30, 0, 0, time.UTC)
	r1 := failedRun("101", "grain/task-10", "aaaaaaa1", now.Add(-15*time.Minute), "ci / test", "go test ./...", string(linuxBytes))
	r1.Source = FaultSourceGitHub
	r1.Repo = "acme/widgets"
	r2 := failedRun("102", "grain/task-11", "bbbbbbb2", now.Add(-5*time.Minute), "ci / test", "go test ./...", string(winBytes))
	r2.Source = FaultSourceGitHub
	r2.Repo = "acme/widgets"

	sig1, ok1 := FailureSignature(r1, r1.Jobs[0])
	sig2, ok2 := FailureSignature(r2, r2.Jobs[0])
	if !ok1 || !ok2 {
		t.Fatalf("FailureSignature ok = (%v, %v), want both true", ok1, ok2)
	}
	if sig1.Digest != sig2.Digest {
		t.Fatalf("Linux and Windows GitHub Actions fixtures produced different digests: %s vs %s", sig1.Digest, sig2.Digest)
	}
	wantPaths := []string{"pkg/auth/verify_test.go", "pkg/auth/verify.go"}
	if !slices.Equal(sig1.Paths, wantPaths) {
		t.Fatalf("Linux fixture paths = %v, want %v", sig1.Paths, wantPaths)
	}
	if !slices.Equal(sig2.Paths, wantPaths) {
		t.Fatalf("Windows fixture paths = %v, want %v", sig2.Paths, wantPaths)
	}

	faults := SharedFaults([]Run{r1, r2}, now, 0)
	if len(faults) != 1 || faults[0].Source != FaultSourceGitHub || faults[0].Seen() != 2 {
		t.Fatalf("SharedFaults over fixtures = %+v, want 1 github fault across 2 branches", faults)
	}
}

func TestSharedFaultsNeverJoinsGrainAndGitHubRuns(t *testing.T) {
	now := time.Date(2026, 9, 18, 17, 0, 0, 0, time.UTC)
	grainRun := failedRun("r1", "grain/task-238", "aaaaaaa1", now.Add(-10*time.Minute), "ci / image", "build the image", proxy405)
	grainRun.Source = FaultSourceGrain
	ghRun := failedRun("4001", "grain/task-246", "bbbbbbb2", now.Add(-5*time.Minute), "ci / image", "build the image", proxy405)
	ghRun.Source = FaultSourceGitHub

	if got := SharedFaults([]Run{grainRun, ghRun}, now, 0); len(got) != 0 {
		t.Fatalf("grain and GitHub failures with identical text were joined: %+v", got)
	}
}
