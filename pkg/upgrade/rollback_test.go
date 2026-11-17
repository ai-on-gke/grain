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

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitOutput is runGit for a command whose output the test needs.
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// commitTo makes an empty commit with subject on top of whatever seed has
// checked out and pushes it to origin as ref, returning its id.
func commitTo(t *testing.T, seed, origin, subject, ref string) string {
	t.Helper()
	runGit(t, seed, "-c", "user.name=test", "-c", "user.email=test@example.com",
		"commit", "--quiet", "--allow-empty", "-m", subject)
	runGit(t, seed, "push", "--quiet", "--force", origin, "HEAD:"+ref)
	return gitOutput(t, seed, "rev-parse", "HEAD")
}

// rollbackFixture is a source deployment whose last upgrade replaced a
// build of previous: a bare origin, a seed repository that pushes into
// it, a checkout on main, and a status file recording previous as the
// PreviousCommit. Its build writes the subject of the commit it was built
// from into the "binary", so what got installed says what got built.
type rollbackFixture struct {
	origin, seed, checkout string
	install                string
	u                      *Upgrader
}

func newRollbackFixture(t *testing.T, previous func(f *rollbackFixture) string) (*rollbackFixture, string) {
	t.Helper()
	root := t.TempDir()
	f := &rollbackFixture{
		origin:   initBareRepo(t, filepath.Join(root, "origin.git")),
		seed:     filepath.Join(root, "seed"),
		checkout: filepath.Join(root, "checkout"),
		install:  filepath.Join(root, "installed-grain"),
	}
	runGit(t, root, "init", "--quiet", "-b", "main", f.seed)
	commitTo(t, f.seed, f.origin, "initial", "refs/heads/main")
	runGit(t, root, "clone", "--quiet", "-b", "main", f.origin, f.checkout)
	prev := previous(f)
	// The checkout is on main, and whatever the fixture pushed since is
	// fetched into it as origin's remote-tracking refs.
	runGit(t, f.checkout, "fetch", "--quiet", "origin")
	runGit(t, f.checkout, "reset", "--quiet", "--hard", "origin/main")

	statusFile := filepath.Join(root, "upgrade-status.json")
	if err := writeStatus(statusFile, Status{Branch: "main", Phase: PhaseOK, PreviousCommit: prev}); err != nil {
		t.Fatal(err)
	}
	f.u = newUpgrader(t, Config{
		SrcDir:          f.checkout,
		AllowedBranches: []string{"main"},
		BuildCmd:        []string{"sh", "-c", "mkdir -p bin && git log -1 --format=%s > bin/grain"},
		BuiltBinary:     filepath.Join(f.checkout, "bin", "grain"),
		InstallPath:     f.install,
		StatusFile:      statusFile,
	})
	return f, prev
}

func (f *rollbackFixture) installed(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(f.install)
	if err != nil {
		t.Fatalf("reading the installed binary: %v", err)
	}
	return strings.TrimSpace(string(data))
}

// A rollback on the source pipeline goes back to the commit the last
// upgrade replaced, by object id, on a deployment whose allow-list names
// main alone -- the commit is not a branch, and is not asked to be one.
// The commit is reachable on origin only from a branch the checkout has
// never fetched, so this is the fetch by id doing the work.
func TestStartRollbackChecksOutThePreviousCommitByObjectID(t *testing.T) {
	f, prev := newRollbackFixture(t, func(f *rollbackFixture) string {
		runGit(t, f.seed, "checkout", "--quiet", "-b", "old")
		prev := commitTo(t, f.seed, f.origin, "previous build", "refs/heads/old")
		runGit(t, f.seed, "checkout", "--quiet", "main")
		commitTo(t, f.seed, f.origin, "current build", "refs/heads/main")
		return prev
	})
	// Not fetched into the checkout: only main is followed from here on.
	runGit(t, f.checkout, "update-ref", "-d", "refs/remotes/origin/old")
	runGit(t, f.checkout, "reflog", "expire", "--expire=now", "--all")
	runGit(t, f.checkout, "gc", "--quiet", "--prune=now")
	if out, err := exec.Command("git", "-C", f.checkout, "cat-file", "-e", prev+"^{commit}").CombinedOutput(); err == nil {
		t.Fatalf("the checkout already has %s, so this would not test the fetch: %s", prev, out)
	}

	if err := f.u.StartRollback(); err != nil {
		t.Fatalf("StartRollback: %v", err)
	}
	status := waitForPhase(t, f.u, PhaseOK)
	if status.Branch != prev {
		t.Errorf("Status.Branch = %q, want the commit rolled back to, %q", status.Branch, prev)
	}
	if got := f.installed(t); got != "previous build" {
		t.Errorf("installed a build of %q, want one of %q", got, "previous build")
	}
	if head := gitOutput(t, f.checkout, "rev-parse", "HEAD"); head != prev {
		t.Errorf("checkout HEAD = %s, want %s", head, prev)
	}
}

// The attack task 4326 closed, and the reason rollback fetches by id: a
// branch pushed to origin under the name of the commit this deployment
// used to run. Neither the rollback nor an ordinary Start of that name
// may build it -- the rollback builds the commit itself, and Start
// refuses the name, which main alone does not allow.
func TestARollbackNeverBuildsABranchNamedAfterThePreviousCommit(t *testing.T) {
	f, prev := newRollbackFixture(t, func(f *rollbackFixture) string {
		prev := gitOutput(t, f.seed, "rev-parse", "HEAD")
		commitTo(t, f.seed, f.origin, "current build", "refs/heads/main")
		// A branch named after the previous commit, pointing somewhere
		// else entirely.
		runGit(t, f.seed, "checkout", "--quiet", "--orphan", "impostor")
		commitTo(t, f.seed, f.origin, "impostor", "refs/heads/"+prev)
		return prev
	})
	if got := gitOutput(t, f.checkout, "rev-parse", "refs/remotes/origin/"+prev); got == prev {
		t.Fatalf("origin's branch %s points at the commit it is named after, so this would not test anything", prev)
	}

	if err := f.u.Start(prev); !errors.Is(err, ErrBranchNotAllowed) {
		t.Fatalf("Start(%s) = %v, want ErrBranchNotAllowed: only StartRollback may go back to the previous commit", prev, err)
	}
	if err := f.u.StartRollback(); err != nil {
		t.Fatalf("StartRollback: %v", err)
	}
	waitForPhase(t, f.u, PhaseOK)
	if got := f.installed(t); got != "initial" {
		t.Errorf("installed a build of %q, want one of the previous commit (%q)", got, "initial")
	}
	if head := gitOutput(t, f.checkout, "rev-parse", "HEAD"); head != prev {
		t.Errorf("checkout HEAD = %s, want %s", head, prev)
	}
}

// Only a full object id is checked out: anything else in PreviousCommit
// -- a branch name, an abbreviation git would resolve -- is refused
// before an upgrade starts, as is a rollback with nothing recorded.
func TestStartRollbackRefusesAnythingButAFullCommitID(t *testing.T) {
	dir := t.TempDir()
	statusFile := filepath.Join(dir, "upgrade-status.json")
	u := newUpgrader(t, Config{SrcDir: dir, AllowedBranches: []string{"**"}, StatusFile: statusFile})
	if err := u.StartRollback(); !errors.Is(err, ErrNothingToRollBackTo) {
		t.Errorf("StartRollback with no status = %v, want ErrNothingToRollBackTo", err)
	}
	for _, prev := range []string{"main", "abc1234", strings.Repeat("A", 40), "--upload-pack=x" + strings.Repeat("a", 26)} {
		if err := writeStatus(statusFile, Status{Branch: "main", Phase: PhaseOK, PreviousCommit: prev}); err != nil {
			t.Fatal(err)
		}
		if err := u.StartRollback(); err == nil || !strings.Contains(err.Error(), "not a full git commit id") {
			t.Errorf("StartRollback to %q = %v, want a refusal", prev, err)
		}
		if st, _ := readStatus(statusFile); st.Phase != PhaseOK {
			t.Errorf("StartRollback to %q started an upgrade (phase %q)", prev, st.Phase)
		}
	}
}

// The rollback target outlives upgrades that never cut over (F5 of
// docs/test-runs/2026-11-13-upgrade-cancel.md). Each upgrade records the
// build running when it began, so a cancelled one recorded the build the
// last upgrade had just put there, and "rolling back" went nowhere while
// what that upgrade replaced was forgotten. A cancelled upgrade and a
// failed one after it both carry the first upgrade's previous image
// forward, and the rollback goes back to it.
func TestRollbackOutlivesACancelledAndAFailedUpgrade(t *testing.T) {
	shortRestartGrace(t)
	dir := t.TempDir()
	prevDigest := "sha256:" + strings.Repeat("c", 64)
	newDigest := "sha256:" + strings.Repeat("d", 64)
	prevRef := "docker.io/grainai/grain:v1@" + prevDigest
	refFile := filepath.Join(dir, "grain-image.env")
	if err := os.WriteFile(refFile, []byte("GRAIN_IMAGE="+prevRef+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "docker.io/grainai/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			DigestCmd:  []string{"sh", "-c", `case "$1" in *:v1*) echo "docker.io/grainai/grain@` + prevDigest + `";; *) echo "docker.io/grainai/grain@` + newDigest + `";; esac`, "sh"},
			RunCmd:     []string{"true"},
			ListCmd:    []string{"true"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("main"); err != nil {
		t.Fatalf("Start(main): %v", err)
	}
	waitForPhase(t, u, PhaseOK)
	waitForIdle(t, u)
	upgraded, err := u.Running()
	if err != nil || upgraded.Ref == prevRef {
		t.Fatalf("Running after the upgrade = (%+v, %v), want something other than %q", upgraded, err, prevRef)
	}

	// Cancelled in its grace window: nothing changes, and the record's
	// PreviousImage is the build running now.
	u.cfg.GraceWindow = time.Hour
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start(main) to cancel: %v", err)
	}
	if ok, err := u.Cancel("alice@example.com", "changed my mind"); !ok || err != nil {
		t.Fatalf("Cancel = (%v, %v), want it cancelled", ok, err)
	}
	st := waitForPhase(t, u, PhaseCancelled)
	waitForIdle(t, u)
	if st.PreviousImage != upgraded.Ref {
		t.Errorf("the cancelled upgrade's PreviousImage = %q, want the build it found running, %q", st.PreviousImage, upgraded.Ref)
	}
	if got := st.Rollback().Image; got != prevRef {
		t.Errorf("after a cancelled upgrade Rollback().Image = %q, want the first upgrade's previous image %q", got, prevRef)
	}

	// Failed at its pull: still nothing to go back to but prevRef.
	u.cfg.GraceWindow = 0
	u.cfg.Image.PullCmd = []string{"false"}
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start(main) to fail: %v", err)
	}
	st = waitForPhase(t, u, PhaseFailed)
	waitForIdle(t, u)
	if got := st.Rollback().Image; got != prevRef {
		t.Errorf("after a failed upgrade Rollback().Image = %q, want %q", got, prevRef)
	}
	if target, err := u.RollbackTarget(st); err != nil || target.Image != prevRef {
		t.Errorf("RollbackTarget = (%+v, %v), want %q", target, err, prevRef)
	}

	u.cfg.Image.PullCmd = []string{"true"}
	if err := u.StartRollback(); err != nil {
		t.Fatalf("StartRollback: %v", err)
	}
	st = waitForPhase(t, u, PhaseOK)
	waitForIdle(t, u)
	if running, err := u.Running(); err != nil || running.Ref != prevRef {
		t.Fatalf("Running after the rollback = (%+v, %v), want %q", running, err, prevRef)
	}
	// The rollback cut over itself, so what it replaced is next.
	if got := st.Rollback().Image; got != upgraded.Ref {
		t.Errorf("after the rollback Rollback().Image = %q, want the build it replaced, %q", got, upgraded.Ref)
	}
}

// Which record a rollback reads: its own Previous* once an upgrade cut
// over (a restart that failed after staging included -- the ref file
// already names the new build), and what it carried otherwise.
func TestStatusRollbackPicksTheLastCutOver(t *testing.T) {
	prev := RollbackTarget{Image: "example/grain:v2@sha256:" + strings.Repeat("a", 64)}
	carried := RollbackTarget{Image: "example/grain:v1@sha256:" + strings.Repeat("b", 64)}
	for _, tc := range []struct {
		name string
		s    Status
		want RollbackTarget
	}{
		{"ok", Status{Phase: PhaseOK}, prev},
		{"unchanged", Status{Phase: PhaseOK, Unchanged: true}, carried},
		{"cancelled", Status{Phase: PhaseCancelled}, carried},
		{"failed", Status{Phase: PhaseFailed, Steps: []Step{{Name: stepPull, State: StepFailed}, {Name: stepRestart, State: StepPending}}}, carried},
		{"restart failed", Status{Phase: PhaseFailed, Steps: []Step{{Name: stepRestart, State: StepFailed}}}, prev},
		{"running", Status{Phase: PhaseRunning}, carried},
	} {
		tc.s.PreviousImage, tc.s.RollbackImage = prev.Image, carried.Image
		if got := tc.s.Rollback(); got != tc.want {
			t.Errorf("%s: Rollback() = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// A rollback is offered only where StartRollback would take it (F6): an
// image not pinned by digest is what a tag names today, not what this
// deployment ran.
func TestRollbackTargetRefusesAnImageNotPinnedByDigest(t *testing.T) {
	dir := t.TempDir()
	u := newUpgrader(t, Config{
		Image:      &ImageConfig{Repository: "docker.io/grainai/grain", RefFile: filepath.Join(dir, "grain-image.env")},
		StatusFile: filepath.Join(dir, "upgrade-status.json"),
	})
	if _, err := u.RollbackTarget(Status{Phase: PhaseOK, PreviousImage: "docker.io/grainai/grain:v1"}); !errors.Is(err, ErrNothingToRollBackTo) || !strings.Contains(err.Error(), "not pinned by digest") {
		t.Errorf("RollbackTarget(tag only) = %v, want ErrNothingToRollBackTo naming the digest", err)
	}
	if _, err := u.RollbackTarget(Status{Phase: PhaseCancelled, PreviousImage: "docker.io/grainai/grain:v1@sha256:" + strings.Repeat("a", 64)}); !errors.Is(err, ErrNothingToRollBackTo) {
		t.Errorf("RollbackTarget(cancelled, nothing carried) = %v, want ErrNothingToRollBackTo", err)
	}
}
