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
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestCopyExecutableCopies covers the copy install and backupInstalled
// share: the destination ends up with every byte of the source and
// executable -- even under a umask that would strip that from the mode
// handed to OpenFile -- and the temp file the bytes stream through is
// gone by the time it returns.
func TestCopyExecutableCopies(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	dir := t.TempDir()
	src := filepath.Join(dir, "built")
	// Comfortably more than one io.Copy buffer, so a copy that stopped
	// after the first read would show up here.
	want := strings.Repeat("binary-bytes", 8192)
	if err := os.WriteFile(src, []byte(want), 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "installed")

	if err := copyExecutable(src, dst); err != nil {
		t.Fatalf("copyExecutable: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("reading the copy: %v", err)
	}
	if string(got) != want {
		t.Errorf("copy is %d bytes, want %d", len(got), len(want))
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Errorf("copy mode = %o, want 755 -- an upgrade has to be able to exec it", perm)
	}
	if _, err := os.Stat(dst + ".new"); !os.IsNotExist(err) {
		t.Errorf("temp file left behind next to the copy (err=%v)", err)
	}
}

// TestCopyExecutableMissingSource pins the error backupInstalled reads
// as "there is nothing installed yet to back up", and confirms a source
// that isn't there leaves no destination or temp file behind either.
func TestCopyExecutableMissingSource(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "backup")

	err := copyExecutable(filepath.Join(dir, "absent"), dst)
	if !os.IsNotExist(err) {
		t.Fatalf("copyExecutable of a missing source: err = %v, want a not-exist error", err)
	}
	for _, p := range []string{dst, dst + ".new"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s exists after a failed copy (err=%v)", filepath.Base(p), err)
		}
	}
}

func TestValidateBranch(t *testing.T) {
	valid := []string{"main", "grain/issue-396", "feature-1", "a", "a.b_c"}
	for _, b := range valid {
		if err := validateBranch(b); err != nil {
			t.Errorf("validateBranch(%q): unexpected error: %v", b, err)
		}
	}

	invalid := []string{"", "-flag", "has space", "trailing/", "/leading", "a..b", "a\nb", "a\tb"}
	for _, b := range invalid {
		if err := validateBranch(b); err == nil {
			t.Errorf("validateBranch(%q): expected an error, got nil", b)
		}
	}
}

// TestStartRefusesABranchItWasNotConfiguredFor: a well-formed branch
// name is not a reviewed branch, and an upgrade replaces the daemon with
// whatever the branch builds, so Start and ValidateBranch accept only
// the branches the deployment was configured with -- main alone when it
// was configured with none (bwsalmon/grain#2385).
func TestStartRefusesABranchItWasNotConfiguredFor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		allowed []string
		ok      []string
		refused []string
	}{
		{
			// grain's own release names (grain/task-2011), and still
			// nothing else: '*' does not cross a '/', so v1/evil is not a
			// release tag, and a branch merely starting with v is not one
			// either.
			name: "default",
			ok:   []string{"main", " main\n", "candidate", "latest", "v1.7.0", "v1.10.0", "v1.7.0-rc.1"},
			refused: []string{"totally-unreviewed-agent-branch", "claude/self-upgrade-to-whatever-i-want", "main2", "mainline", "feature/main",
				"grain/task-1", "feature", "v1/evil", "v1.7.0/evil", "release/v1.7.0", "v2-rewrite", "vendor-bump", "v", "candidate2", "latest/x"},
		},
		{
			name:    "configured",
			allowed: []string{"main", "2.1", "release/*"},
			ok:      []string{"main", "2.1", "release/3.0"},
			refused: []string{"2.1.latest", "release/3.0/evil", "release", "grain/task-1"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logToTest(t)
			dir := t.TempDir()
			u := New(Config{SrcDir: dir, StatusFile: filepath.Join(dir, "status.json"), AllowedBranches: tc.allowed})
			for _, b := range tc.ok {
				if got, err := u.ValidateBranch(b); err != nil || got != strings.TrimSpace(b) {
					t.Errorf("ValidateBranch(%q) = (%q, %v), want (%q, nil)", b, got, err, strings.TrimSpace(b))
				}
			}
			for _, b := range tc.refused {
				if _, err := u.ValidateBranch(b); !errors.Is(err, ErrBranchNotAllowed) {
					t.Errorf("ValidateBranch(%q) = %v, want ErrBranchNotAllowed", b, err)
				}
				if err := u.Start(b); !errors.Is(err, ErrBranchNotAllowed) {
					t.Errorf("Start(%q) = %v, want ErrBranchNotAllowed", b, err)
				}
			}
			// Refused before anything was written: nothing was started.
			if _, err := os.Stat(filepath.Join(dir, "status.json")); !os.IsNotExist(err) {
				t.Errorf("a refused Start wrote a status file (err=%v)", err)
			}
		})
	}
}

// runGit runs a git command against dir, failing the test on error --
// every fixture below is built entirely out of these.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// initBareRepo creates the bare repository at path -- the "origin" the
// fixture below pushes into -- and returns the path.
//
// The two config settings are what keep a push from outliving the test
// that made it: git runs auto-maintenance on the receiving side of a
// push, and since v2.47 it detaches, so the repacking goes on in the
// background while t.TempDir deletes the tree underneath it, and the
// deletion fails on files that land after RemoveAll has walked past them
// ("TempDir RemoveAll cleanup: unlinkat ...: directory not empty").
// Saying autoDetach=false does not turn the maintenance off, it runs it
// in the foreground, so the push that triggered it is what waits for it.
// pkg/cli/bare_remote_test.go's initBareRepo has the whole
// explanation, including why both keys are set and why nothing on a
// runner with git 2.39 can see any of this; this is that helper
// restated, because this codebase duplicates a small test helper across
// packages rather than sharing one (tests/e2e/harness_test.go's own
// comment on why).
func initBareRepo(t *testing.T, path string) string {
	t.Helper()
	runGit(t, filepath.Dir(path), "init", "--quiet", "--bare", path)
	runGit(t, path, "config", "maintenance.autoDetach", "false")
	runGit(t, path, "config", "gc.autoDetach", "false")
	return path
}

// newFixtureRepo builds a bare "origin" plus a working checkout cloned
// from it, both under t.TempDir(), with one commit on main and one on a
// second branch named branch -- so checkout has somewhere real to fetch
// and reset onto that differs from what the working checkout already
// has.
func newFixtureRepo(t *testing.T, branch string) (origin, checkout string) {
	t.Helper()
	root := t.TempDir()
	origin = initBareRepo(t, filepath.Join(root, "origin.git"))
	checkout = filepath.Join(root, "checkout")

	seed := filepath.Join(root, "seed")
	runGit(t, root, "init", "--quiet", "-b", "main", seed)
	runGit(t, seed, "-c", "user.name=test", "-c", "user.email=test@example.com",
		"commit", "--quiet", "--allow-empty", "-m", "initial")
	runGit(t, seed, "push", "--quiet", origin, "HEAD:refs/heads/main")
	runGit(t, seed, "checkout", "--quiet", "-b", branch)
	runGit(t, seed, "-c", "user.name=test", "-c", "user.email=test@example.com",
		"commit", "--quiet", "--allow-empty", "-m", "on "+branch)
	runGit(t, seed, "push", "--quiet", origin, "HEAD:refs/heads/"+branch)

	runGit(t, root, "clone", "--quiet", "-b", "main", origin, checkout)
	return origin, checkout
}

func TestUpgraderStartRunsCheckoutBuildInstallAndRestart(t *testing.T) {
	// The restart here succeeds and does not kill this process, so run
	// waits restartGrace out before believing it did nothing (hold_test.go).
	shortRestartGrace(t)
	_, checkout := newFixtureRepo(t, "feature")

	built := filepath.Join(checkout, "bin", "grain")
	installPath := filepath.Join(t.TempDir(), "grain")
	restartMarker := filepath.Join(t.TempDir(), "restarted")
	statusFile := filepath.Join(t.TempDir(), "upgrade-status.json")

	u := newUpgrader(t, Config{
		SrcDir: checkout,
		// A fake "build": writes a recognizable binary rather than
		// actually invoking make/docker, so this test needs neither.
		BuildCmd:    []string{"sh", "-c", "mkdir -p bin && printf fake-binary > bin/grain"},
		BuiltBinary: built,
		InstallPath: installPath,
		RestartCmd:  []string{"sh", "-c", "touch " + restartMarker},
		StatusFile:  statusFile,
	})

	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitForPhase(t, u, PhaseOK)

	installed, err := os.ReadFile(installPath)
	if err != nil {
		t.Fatalf("reading installed binary: %v", err)
	}
	if string(installed) != "fake-binary" {
		t.Errorf("installed binary content = %q, want %q", installed, "fake-binary")
	}
	info, err := os.Stat(installPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("installed binary is not executable: mode %v", info.Mode())
	}

	// The restart command runs after the "ok" status is already written
	// (deliberately -- see run's own comment), so waitForPhase returning
	// does not guarantee it has finished yet; poll for it too.
	waitForFile(t, restartMarker)

	// The working checkout should have moved onto the branch's own commit.
	logCmd := exec.Command("git", "-C", checkout, "log", "-1", "--format=%s")
	out, err := logCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "on feature\n" {
		t.Errorf("checkout HEAD commit message = %q, want %q", got, "on feature\n")
	}
}

// TestUpgraderRollsBackWhenHealthCheckFails is bwsalmon/agents#418/#422's
// fix: a build that succeeds can still produce a binary that is broken
// at runtime (a panic in some init(), a missing dynamic library, and so
// on), so this builds and installs a "binary" that is a perfectly valid
// executable -- checkout/build/install all genuinely succeed -- but
// which always exits 1 instead of doing anything useful, with
// HealthCheckArgs configured to actually run it. That should be caught
// before ever handing off to RestartCmd: the install gets rolled back
// to the binary that was there before (seeded here the way any upgrade
// after the very first one finds one already in place), and Status
// reports PhaseFailed rather than PhaseOK.
func TestUpgraderRollsBackWhenHealthCheckFails(t *testing.T) {
	_, checkout := newFixtureRepo(t, "feature")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}

	built := filepath.Join(checkout, "bin", "grain")
	installPath := filepath.Join(t.TempDir(), "grain")
	statusFile := filepath.Join(t.TempDir(), "upgrade-status.json")
	restartMarker := filepath.Join(t.TempDir(), "restarted")

	if err := os.WriteFile(installPath, []byte("previous-binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	u := newUpgrader(t, Config{
		SrcDir: checkout,
		// A fake "build" that produces a real, executable script -- so
		// build and install both succeed -- but one that always exits 1
		// instead of doing anything useful once it's actually run, the
		// same way HealthCheckArgs will run it below.
		BuildCmd:        []string{"sh", "-c", "mkdir -p bin && printf '#!/bin/sh\\nexit 1\\n' > bin/grain"},
		BuiltBinary:     built,
		InstallPath:     installPath,
		HealthCheckArgs: []string{"schema-version"},
		// Should never run: a failed health check must stop this before
		// it ever hands off to RestartCmd.
		RestartCmd: []string{"sh", "-c", "touch " + restartMarker},
		StatusFile: statusFile,
	})

	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	status := waitForPhase(t, u, PhaseFailed)
	if status.Detail == "" {
		t.Error("failed status carries no Detail")
	}

	// waitForPhase already only returns once the failure status -- which
	// this package only ever writes after rollback and after deciding
	// not to invoke RestartCmd -- is visible, so checking here (rather
	// than polling) is enough.
	if _, err := os.Stat(restartMarker); err == nil {
		t.Error("RestartCmd ran despite a failed health check")
	}

	installed, err := os.ReadFile(installPath)
	if err != nil {
		t.Fatalf("reading installPath after rollback: %v", err)
	}
	if string(installed) != "previous-binary" {
		t.Errorf("installPath after rollback = %q, want the previous binary restored", installed)
	}
	info, err := os.Stat(installPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("restored binary is not executable: mode %v", info.Mode())
	}
}

// TestUpgraderRollsBackToNothingWhenNoPreviousBinaryExisted covers the
// first-ever upgrade on a fresh deployment: there is nothing at
// InstallPath yet for a failed health check to restore, so rollback
// should remove the broken binary it just installed rather than leaving
// it there for a future restart to pick up.
func TestUpgraderRollsBackToNothingWhenNoPreviousBinaryExisted(t *testing.T) {
	_, checkout := newFixtureRepo(t, "feature")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}

	built := filepath.Join(checkout, "bin", "grain")
	installPath := filepath.Join(t.TempDir(), "grain")
	statusFile := filepath.Join(t.TempDir(), "upgrade-status.json")

	u := newUpgrader(t, Config{
		SrcDir:          checkout,
		BuildCmd:        []string{"sh", "-c", "mkdir -p bin && printf '#!/bin/sh\\nexit 1\\n' > bin/grain"},
		BuiltBinary:     built,
		InstallPath:     installPath,
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      statusFile,
	})

	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitForPhase(t, u, PhaseFailed)

	if _, err := os.Stat(installPath); !os.IsNotExist(err) {
		t.Errorf("installPath after rollback with no previous binary: got err %v, want a not-exist error", err)
	}
	if _, err := os.Stat(installPath + ".prev"); !os.IsNotExist(err) {
		t.Errorf("backup file left behind after rollback: got err %v, want a not-exist error", err)
	}
}

// TestUpgraderRemovesBackupOnceHealthCheckPasses confirms the ".prev"
// backup a passing health check no longer needs doesn't linger forever.
func TestUpgraderRemovesBackupOnceHealthCheckPasses(t *testing.T) {
	_, checkout := newFixtureRepo(t, "feature")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}

	built := filepath.Join(checkout, "bin", "grain")
	installPath := filepath.Join(t.TempDir(), "grain")
	statusFile := filepath.Join(t.TempDir(), "upgrade-status.json")

	if err := os.WriteFile(installPath, []byte("previous-binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	u := newUpgrader(t, Config{
		SrcDir: checkout,
		// A fake "build" that produces a real, executable script that
		// exits 0 -- a health check against it should pass.
		BuildCmd:        []string{"sh", "-c", "mkdir -p bin && printf '#!/bin/sh\\nexit 0\\n' > bin/grain"},
		BuiltBinary:     built,
		InstallPath:     installPath,
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      statusFile,
	})

	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitForPhase(t, u, PhaseOK)

	if _, err := os.Stat(installPath + ".prev"); !os.IsNotExist(err) {
		t.Errorf("backup file left behind after a passing health check: got err %v, want a not-exist error", err)
	}
}

func TestUpgraderStartRejectsConcurrentUpgrade(t *testing.T) {
	_, checkout := newFixtureRepo(t, "feature")
	// The first upgrade is held in its build until the second Start has
	// been made and refused, so "still running" is a fact rather than a
	// second of hoping (latch's own comment).
	building := newLatch(t)
	defer building.open()

	u := newUpgrader(t, Config{
		SrcDir:      checkout,
		BuildCmd:    []string{"sh", "-c", building.wait() + "mkdir -p bin && printf x > bin/grain"},
		BuiltBinary: filepath.Join(checkout, "bin", "grain"),
		InstallPath: filepath.Join(t.TempDir(), "grain"),
		StatusFile:  filepath.Join(t.TempDir(), "upgrade-status.json"),
	})

	if err := u.Start("feature"); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := u.Start("feature"); err != ErrUpgradeInProgress {
		t.Errorf("second Start: got %v, want ErrUpgradeInProgress", err)
	}

	building.open()
	waitForPhase(t, u, PhaseOK)
}

func TestUpgraderStartReportsFailure(t *testing.T) {
	_, checkout := newFixtureRepo(t, "feature")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}

	u := newUpgrader(t, Config{
		SrcDir:      checkout,
		BuildCmd:    []string{"sh", "-c", "exit 1"},
		BuiltBinary: filepath.Join(checkout, "bin", "grain"),
		InstallPath: filepath.Join(t.TempDir(), "grain"),
		StatusFile:  filepath.Join(t.TempDir(), "upgrade-status.json"),
	})

	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	status := waitForPhase(t, u, PhaseFailed)
	if status.Detail == "" {
		t.Error("failed status carries no Detail")
	}

	// A fresh Upgrader over the same StatusFile (standing in for the
	// next process, since RestartCmd's whole point is to replace this
	// one) reports the same failure back.
	fresh := New(Config{StatusFile: u.cfg.StatusFile})
	again, err := fresh.Status()
	if err != nil {
		t.Fatal(err)
	}
	if again.Phase != PhaseFailed {
		t.Errorf("fresh Upgrader's Status phase = %q, want %q", again.Phase, PhaseFailed)
	}
}

// TestUpgraderTimesOutInsteadOfHangingForever is bwsalmon/agents#633's
// regression test ("v2 Deploys are hanging"): before Config.Timeout
// existed, run's ctx was context.Background(), so a build command that
// simply never returned -- a stalled network fetch, a wedged docker
// daemon -- left Status on PhaseRunning forever and the Upgrader held
// permanently, with no way for a second Start to ever get through. A tiny
// Config.Timeout here stands in for that stall: the real default
// (defaultTimeout, 45 minutes) is not something a test can wait out, but
// the mechanism bounding it is the same regardless of the value.
func TestUpgraderTimesOutInsteadOfHangingForever(t *testing.T) {
	_, checkout := newFixtureRepo(t, "feature")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}

	u := newUpgrader(t, Config{
		SrcDir: checkout,
		// Far longer than Timeout below -- stands in for a build that
		// hangs rather than one that is merely slow.
		BuildCmd:    []string{"sh", "-c", "sleep 300"},
		BuiltBinary: filepath.Join(checkout, "bin", "grain"),
		InstallPath: filepath.Join(t.TempDir(), "grain"),
		StatusFile:  filepath.Join(t.TempDir(), "upgrade-status.json"),
		Timeout:     50 * time.Millisecond,
	})

	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// waitForPhase's own deadline is the assertion: without the fix this
	// never reaches PhaseFailed at all, and the test times out the same
	// way the hang it is guarding against would. Which step the deadline
	// happens to land in is not part of it -- 50ms is short enough that
	// on a loaded machine the checkout is still going when it expires --
	// because the mechanism being tested bounds the whole run rather
	// than any one step of it. Which is also why 50ms is not something a
	// busy runner can turn red: every way this deadline can land ends in
	// the PhaseFailed asserted below, and the build it stands in for
	// (`sleep 300`) cannot finish inside any of them.
	status := waitForPhase(t, u, PhaseFailed)
	if status.Detail == "" {
		t.Error("failed status carries no Detail")
	}

	// The Upgrader must have been released too -- otherwise a second
	// Start after the timeout still gets ErrUpgradeInProgress forever,
	// which is the same stuck deployment by another name. The status file
	// reaching "failed" is not yet that: run writes it and only then
	// unwinds, so asking straight away races the goroutine that is on its
	// way out and occasionally catches it still holding the Upgrader.
	waitForIdle(t, u)
	if err := u.Start("feature"); err != nil {
		t.Errorf("Start after a timed-out upgrade: %v, want nil (the timed-out run should have released the Upgrader)", err)
	}

	// That Start is a *real* run: it fetches and checks out inside
	// SrcDir before reaching the build that will time out, and it is
	// still doing so when this returns. newUpgrader's own cleanup is
	// what waits for it, before t.TempDir deletes the tree it is
	// working in.
}

func TestUpgraderStartRejectsInvalidBranch(t *testing.T) {
	// No upgrade runs here, so newUpgrader is not what claims the log --
	// but the refusal is logged, and a line with no test on it is the
	// thing log_test.go exists to stop.
	logToTest(t)
	u := New(Config{StatusFile: filepath.Join(t.TempDir(), "upgrade-status.json")})
	if err := u.Start("-not-a-branch"); err == nil {
		t.Error("expected an error for an invalid branch, got nil")
	}
	status, err := u.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != "" {
		t.Errorf("a rejected Start should never have written a status; got phase %q", status.Phase)
	}
}

func TestStatusReportsIdleWhenNeverRun(t *testing.T) {
	u := New(Config{StatusFile: filepath.Join(t.TempDir(), "upgrade-status.json")})
	status, err := u.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != "" {
		t.Errorf("Phase = %q, want empty (never run)", status.Phase)
	}
}

// TestDoneOutlastsTheStatusFileSayingItIsFinished is Done's whole
// reason to exist, and the contract every test in this repo that runs a
// real upgrade against a t.TempDir now rests on -- pkg/ui's included,
// which cannot see u.done and so has nothing else to wait on.
//
// Status reaching PhaseOK is not the end of an upgrade: run writes it
// before it starts RestartCmd at all, so the restart step's own result
// and the final flush of the status file are both still to come. A
// caller that treated "ok" as the end -- a test whose t.TempDir removal
// then races those writes -- is the flake this closes, so what is
// asserted here is precisely the ordering: still open at "ok", closed
// only once the restart the upgrade fired has actually happened.
func TestDoneOutlastsTheStatusFileSayingItIsFinished(t *testing.T) {
	shortRestartGrace(t)
	dir := t.TempDir()
	// The restart command blocks in the latch, which is what makes the
	// "still open" assertion below a fact rather than a race won: with
	// the run goroutine parked in the restart, no amount of scheduling
	// luck can let it reach release first. However this test ends, the
	// restart has to be let go before newUpgrader's own cleanup sits down
	// to wait for it -- which the deferred open does, since defers run
	// ahead of every cleanup.
	restarting := newLatch(t)
	defer restarting.open()
	restartMarker := filepath.Join(dir, "restarted")

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    filepath.Join(dir, "image.env"),
			PullCmd:    []string{"true"},
		},
		RestartCmd: []string{"sh", "-c", restarting.wait() + "touch " + restartMarker},
		StatusFile: filepath.Join(dir, "upgrade-status.json"),
	})

	assertClosed(t, u.Done(), "before any upgrade has been started")

	if err := u.Start("grain/task-178"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	done := u.Done()
	waitForPhase(t, u, PhaseOK)
	select {
	case <-done:
		t.Fatal("Done closed while the restart step was still to run -- the writes that follow it can outlive a caller that waited on this")
	default:
	}

	restarting.open()
	select {
	case <-done:
	case <-time.After(waitTimeout):
		// One last look before believing the timer, for waitForIdle's
		// reason: both cases can be ready at once, and select would then
		// have picked this one by coin toss.
		select {
		case <-done:
		default:
			t.Fatalf("Done never closed, %s after the restart it was waiting on was let go", waitTimeout)
		}
	}
	if _, err := os.Stat(restartMarker); err != nil {
		t.Errorf("Done closed before the restart it fired had run: %v", err)
	}
	assertClosed(t, u.Done(), "after the upgrade finished")
}

// TestDoneReleasesAStartThatNeverGotGoing: Start can fail after it has
// claimed the Upgrader -- an unwritable status file -- and a caller
// already waiting on the channel it handed out has no other way to hear
// that nothing is coming.
func TestDoneReleasesAStartThatNeverGotGoing(t *testing.T) {
	// The Start below fails loudly in the log and nowhere else, and
	// nothing here calls newUpgrader to claim the log for this test.
	logToTest(t)
	// A status file inside a directory that does not exist: writeStatus
	// fails, so Start returns an error having started nothing.
	u := New(Config{StatusFile: filepath.Join(t.TempDir(), "nowhere", "upgrade-status.json")})
	if err := u.Start("main"); err == nil {
		t.Fatal("Start with an unwritable status file returned nil")
	}
	assertClosed(t, u.Done(), "after a Start that failed to record its status")
}

// assertClosed fails the test unless ch is already closed -- Done's
// answer for "nothing of this process's is running", which a caller
// reads without blocking.
func assertClosed(t *testing.T, ch <-chan struct{}, when string) {
	t.Helper()
	select {
	case <-ch:
	default:
		t.Errorf("Done() %s is not closed, so a caller waiting on it would block forever", when)
	}
}

// waitTimeout bounds every poll loop in this package's tests. It is
// deliberately far longer than any upgrade here needs: none of these
// tests measures how fast an upgrade is, only that it gets where it is
// going at all, so the only thing a tight bound can do is redden the
// build on a busy machine. On a CI runner testing every other package
// beside this one under -race, a `git fetch` that takes 20ms on an idle
// laptop has been seen to take the better part of a second, and the
// whole of an upgrade is a handful of those.
const waitTimeout = 60 * time.Second

// pollInterval is how long the waits below sleep between two looks at
// whatever they are waiting for. Short, because what they are waiting on
// is one goroutine's write of a small file and the point of the sleep is
// only to stop this one spinning while it happens.
const pollInterval = 20 * time.Millisecond

// waitFor polls look until it reports true, and reports false if
// waitTimeout goes by without that happening. Every wait in this package
// is written on top of it, so the loop below is the only place that has
// to get the deadline right -- and the caller keeps saying what it was
// waiting for, since only the caller can say what it last saw.
//
// It is deadline-driven rather than sleep-driven, which is the
// difference between "we gave it a minute" and "we stopped looking
// somewhere inside a minute". Each of these waits used to be written
//
//	for time.Now().Before(deadline) { ...look...; time.Sleep(20ms) }
//
// which makes its last look a whole poll interval *before* the deadline
// -- and on a runner loaded enough to deschedule this goroutine for
// longer than that, arbitrarily further before it. A wait that gives up
// having last looked at t-20ms (or at t-5s) reports "it never happened"
// about a window it never actually watched, which is a fact about how
// busy the machine was dressed up as a fact about this package. The loop
// here looks first and reads the clock second, so the last look is
// always made at or after the deadline: a wait that took the whole of
// waitTimeout still gets the answer it waited that long for.
func waitFor(look func() bool) bool {
	deadline := time.Now().Add(waitTimeout)
	for {
		if look() {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(pollInterval)
	}
}

// newUpgrader is New for a test that actually starts an upgrade: same
// Upgrader, plus a cleanup that waits for the run it started to be
// finished with the filesystem.
//
// An upgrade outlives the assertions made about it. The status file
// says "ok" before RestartCmd has even been started, and the writes
// that follow -- the restart step's own result, the final flush of the
// status file -- land after whatever the test was waiting for. A test
// that returns in that window leaves those writes racing t.TempDir's
// RemoveAll, and RemoveAll losing that race fails the test it belongs
// to from the cleanup path:
//
//	TempDir RemoveAll cleanup: unlinkat /tmp/TestX123/001: directory not empty
//
// -- a red build with nothing wrong behind it, on whichever test
// happened to lose. (Seen on CI before now, in this package; reproduced
// by running these tests many times over on a machine with more copies
// of them at once than it has cores.)
//
// Cleanups run last-registered-first, so this one runs before the
// removal t.TempDir registered. That ordering is not a coincidence to
// be maintained: a Config that names a file under t.TempDir has to have
// called t.TempDir to build it, which is to say before it could call
// this.
//
// It also points the package's log at t (log_test.go), so that whatever
// this upgrade has to say is reported under the name of the test that
// ran it. That has to come first for the same last-registered-first
// reason: the log is given back only once the upgrade using it is done.
func newUpgrader(t *testing.T, cfg Config) *Upgrader {
	t.Helper()
	logToTest(t)
	// The tests built on this are about what an upgrade does once it
	// has been allowed, so every branch they name is allowed;
	// TestStartRefusesABranchItWasNotConfiguredFor is the one about
	// which branches are.
	if cfg.AllowedBranches == nil {
		cfg.AllowedBranches = anyBranch
	}
	// No docker daemon here either to answer pinImage's question, so the
	// pulled tag answers with testDigest -- the one TestUpgraderImagePins*
	// is about sets its own.
	if cfg.Image != nil && cfg.Image.DigestCmd == nil {
		cfg.Image.DigestCmd = fakeDigestCmd
	}
	u := New(cfg)
	t.Cleanup(func() { waitForIdle(t, u) })
	return u
}

// testDigest is the digest fakeDigestCmd says every pulled image has.
const testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// fakeDigestCmd stands in for `docker image inspect`: it prints one
// RepoDigests line, the ref's repository at testDigest.
var fakeDigestCmd = []string{"sh", "-c", `printf '%s@%s\n' "${1%:*}" ` + testDigest, "sh"}

// anyBranch allows every branch the fixtures here use, slashes and all.
var anyBranch = []string{"*", "*/*"}

// latch is a file one of the fake commands below blocks on until the
// test opens it -- a way to hold an upgrade at a known point for exactly
// as long as the assertions about that point take, and no longer.
//
// Several tests here have to observe an upgrade *while* it is running:
// the pane showing which step is in progress and what its command last
// printed, a second Start being refused because the first has not
// finished. Each of them used to hold that moment open by putting `sleep
// 1` in the command being run, and then had one second to look. A second
// is a great deal of time on an idle laptop and none at all on a CI
// runner building every other package of this repository beside this one
// under -race: what has to happen inside that window is not the
// observation itself (one read of a small file) but the scheduling of
// the goroutine making it, and a sleep in a child process waits for no
// such thing. So each of those tests carried a failure that says the
// upgrade had already moved on when it was looked at -- a fact about how
// busy the runner was, reported as a fact about this package.
//
// A latch removes the window rather than widening it: the upgrade cannot
// leave the step until the test says so, so there is nothing to miss --
// and nothing to wait out either, which is a few seconds off this
// package's suite.
type latch struct {
	t    *testing.T
	path string
}

// newLatch makes one, closed. Every caller is expected to `defer
// l.open()` straight away: a test that fails before it opens the latch
// itself would otherwise leave the command blocked on it forever, and
// newUpgrader's cleanup would then sit out the whole of waitTimeout
// waiting for an upgrade that cannot finish. A deferred call runs before
// any cleanup, and runs whether the test returned or a t.Fatal unwound
// it.
func newLatch(t *testing.T) *latch {
	t.Helper()
	return &latch{t: t, path: filepath.Join(t.TempDir(), "latch")}
}

// wait is shell to put in front of whatever the command does next: it
// blocks there until open is called.
func (l *latch) wait() string {
	return "while [ ! -f " + l.path + " ]; do sleep 0.01; done; "
}

// open lets the command past, and says so if it cannot -- a latch that
// silently failed to open would look exactly like the hang it exists to
// prevent. Calling it twice is harmless, which is what makes the
// deferred call a backstop rather than a duplicate.
func (l *latch) open() {
	l.t.Helper()
	if err := os.WriteFile(l.path, nil, 0o644); err != nil {
		l.t.Errorf("opening the latch the command waits on: %v", err)
	}
}

// waitForIdle blocks until u has no upgrade running -- a stronger
// statement than anything the status file can make, since run writes
// the terminal status and only then unwinds and releases the Upgrader.
// It waits on Done rather than on the field behind it because that is
// the same wait pkg/ui's tests have to make from outside this package,
// and a helper here that took a shortcut unavailable to them would stop
// proving that the exported one is enough.
func waitForIdle(t *testing.T, u *Upgrader) {
	t.Helper()
	select {
	case <-u.Done():
		return
	case <-time.After(waitTimeout):
	}
	// A select whose cases are both ready picks between them at random,
	// and on a machine that descheduled this goroutine for the whole of
	// waitTimeout both of these are: the upgrade finished, and the timer
	// fired, and nothing says which happened first. So a timeout is only
	// a timeout after one last look at the channel that matters.
	select {
	case <-u.Done():
	default:
		// Not Fatalf: this is called from a cleanup as well as from a
		// test body, and a cleanup is no place to stop the world.
		t.Errorf("timed out after %s waiting for the upgrade to finish", waitTimeout)
	}
}

// waitForPhase polls Status until it reports want or a phase later than
// "running" that isn't want (a test bug), or times out.
func waitForPhase(t *testing.T, u *Upgrader, want Phase) Status {
	t.Helper()
	var last Status
	if waitFor(func() bool {
		status, err := u.Status()
		if err != nil {
			t.Fatal(err)
		}
		last = status
		if status.Phase == PhaseFailed && want != PhaseFailed {
			t.Fatalf("upgrade failed: %s", status.Detail)
		}
		return status.Phase == want
	}) {
		return last
	}
	// What it last saw, rather than only what it wanted: a wait that
	// times out here is the shape every flake in this package has taken,
	// and the phase and detail it was stuck on are the whole of the
	// difference between "which test failed" and "why".
	t.Fatalf("timed out after %s waiting for phase %q; last status was phase %q, %v of %v done, detail %q",
		waitTimeout, want, last.Phase, doneSteps(last), len(last.Steps), last.Detail)
	return Status{}
}

// doneSteps is how many of a status's steps have finished -- for the
// timeout message above, where "stuck two steps into five" says more
// about where an upgrade stopped than its phase alone can.
func doneSteps(status Status) int {
	n := 0
	for _, step := range status.Steps {
		if step.State == StepDone || step.State == StepSkipped {
			n++
		}
	}
	return n
}

// waitForFile polls for path to exist, failing the test if it never
// shows up -- for asserting on a side effect (the restart command) that
// happens after Status already reports the phase that triggers it.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	if waitFor(func() bool {
		_, err := os.Stat(path)
		return err == nil
	}) {
		return
	}
	t.Fatalf("timed out after %s waiting for %s to exist", waitTimeout, path)
}

// TestUpgraderRefusesToBuildOverADirtyCheckout is checkout's own refusal
// (its doc comment: the checkout is this pipeline's to manage, and local
// changes in it are somebody's in-progress work rather than something an
// Upgrade click should silently discard). The guard matters more than it
// looks: `git reset --hard` is what the step after it runs, so a bug here
// is not a failed upgrade but a lost working tree -- and it has to stop
// the pipeline dead, before the build, rather than merely be reported.
func TestUpgraderRefusesToBuildOverADirtyCheckout(t *testing.T) {
	_, checkout := newFixtureRepo(t, "feature")
	if err := os.WriteFile(filepath.Join(checkout, "local-work.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	buildMarker := filepath.Join(t.TempDir(), "built")
	u := newUpgrader(t, Config{
		SrcDir:      checkout,
		BuildCmd:    []string{"sh", "-c", "touch " + buildMarker},
		BuiltBinary: filepath.Join(checkout, "bin", "grain"),
		InstallPath: filepath.Join(t.TempDir(), "grain"),
		StatusFile:  filepath.Join(t.TempDir(), "upgrade-status.json"),
	})

	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	status := waitForPhase(t, u, PhaseFailed)
	if !strings.Contains(status.Detail, "uncommitted changes") {
		t.Errorf("status detail = %q, want it to say the checkout has uncommitted changes", status.Detail)
	}
	if _, err := os.Stat(buildMarker); err == nil {
		t.Error("the build ran despite the refusal to touch a dirty checkout")
	}
	if got := readFile(t, filepath.Join(checkout, "local-work.txt")); got != "mine" {
		t.Errorf("the local file the refusal exists to protect reads %q", got)
	}
}

// TestUpgraderFailsOnABranchThatDoesNotExist is the binary path's answer
// to TestUpgraderImageFailsWhenPullFails: the ordinary operator typo (or
// a branch deleted since it was last upgraded to) has to stop at the
// fetch, with a status that names the step, and leave the binary the
// deployment is running exactly where it was.
func TestUpgraderFailsOnABranchThatDoesNotExist(t *testing.T) {
	_, checkout := newFixtureRepo(t, "feature")

	installPath := filepath.Join(t.TempDir(), "grain")
	if err := os.WriteFile(installPath, []byte("previous-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	restartMarker := filepath.Join(t.TempDir(), "restarted")

	u := newUpgrader(t, Config{
		SrcDir:      checkout,
		BuildCmd:    []string{"sh", "-c", "mkdir -p bin && printf fake-binary > bin/grain"},
		BuiltBinary: filepath.Join(checkout, "bin", "grain"),
		InstallPath: installPath,
		RestartCmd:  []string{"sh", "-c", "touch " + restartMarker},
		StatusFile:  filepath.Join(t.TempDir(), "upgrade-status.json"),
	})

	if err := u.Start("no-such-branch"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	status := waitForPhase(t, u, PhaseFailed)
	if !strings.Contains(status.Detail, "checkout") {
		t.Errorf("status detail = %q, want it to name the checkout step", status.Detail)
	}
	if got := readFile(t, installPath); got != "previous-binary" {
		t.Errorf("installed binary = %q, want the running one left untouched", got)
	}
	if _, err := os.Stat(restartMarker); err == nil {
		t.Error("a failed checkout still asked for a restart")
	}
}

// TestUpgraderReportsRunningWhileTheBuildIsStillGoing is what the UI's
// Upgrade button is actually driven by: the panel polls GET /api/upgrade
// (pkg/ui/upgrade.go, which is this Status verbatim) and keeps the button
// disabled for as long as it reads "running". So an upgrade in flight has
// to be *visible* as running -- from a different Upgrader over the same
// StatusFile, since the process that started it is the one an upgrade
// restarts out from under itself.
func TestUpgraderReportsRunningWhileTheBuildIsStillGoing(t *testing.T) {
	_, checkout := newFixtureRepo(t, "feature")
	// Held in the build until the assertions below have read the status
	// back: they are about an upgrade that is still going, and this is
	// what makes it still be going when they run (latch).
	building := newLatch(t)
	defer building.open()

	statusFile := filepath.Join(t.TempDir(), "upgrade-status.json")
	u := newUpgrader(t, Config{
		SrcDir:      checkout,
		BuildCmd:    []string{"sh", "-c", building.wait() + "mkdir -p bin && printf fake-binary > bin/grain"},
		BuiltBinary: filepath.Join(checkout, "bin", "grain"),
		InstallPath: filepath.Join(t.TempDir(), "grain"),
		StatusFile:  statusFile,
	})

	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	observer := New(Config{StatusFile: statusFile})
	status, err := observer.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != PhaseRunning {
		t.Errorf("phase = %q, want %q while the build is still going", status.Phase, PhaseRunning)
	}
	if status.Branch != "feature" {
		t.Errorf("branch = %q, want the branch the upgrade was started on", status.Branch)
	}
	if status.StartedAt == nil {
		t.Error("a running upgrade carries no StartedAt")
	}
	if status.FinishedAt != nil {
		t.Errorf("a running upgrade already carries FinishedAt = %v", status.FinishedAt)
	}

	// And it does finish: the poll the panel keeps up is what eventually
	// turns the button back on.
	building.open()
	waitForPhase(t, u, PhaseOK)
}

// TestUpgraderFailsWithoutABuildCommand covers the misconfiguration a
// deployment reaches by setting -upgrade-src-dir on a build of grain that
// has no build command wired up: it fails the upgrade with a status that
// says so, rather than "succeeding" by installing whatever binary happens
// to be sitting at BuiltBinary from some earlier build.
func TestUpgraderFailsWithoutABuildCommand(t *testing.T) {
	_, checkout := newFixtureRepo(t, "feature")
	// Outside the checkout: a stray file inside it would trip checkout's
	// own dirty-tree refusal long before the missing build command was
	// ever reached.
	stale := filepath.Join(t.TempDir(), "grain")
	if err := os.WriteFile(stale, []byte("stale-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	installPath := filepath.Join(t.TempDir(), "grain")

	u := newUpgrader(t, Config{
		SrcDir:      checkout,
		BuiltBinary: stale,
		InstallPath: installPath,
		StatusFile:  filepath.Join(t.TempDir(), "upgrade-status.json"),
	})

	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseFailed)
	if !strings.Contains(status.Detail, "build command") {
		t.Errorf("status detail = %q, want it to name the missing build command", status.Detail)
	}
	if _, err := os.Stat(installPath); !os.IsNotExist(err) {
		t.Errorf("something was installed anyway (err=%v)", err)
	}
}

// TestStatusReportsAnUpgradeARestartInterruptedAsFailed covers a status
// file left on "running" by a process that died mid-upgrade: a fresh
// Upgrader, which is what the daemon that comes up next holds, has no
// upgrade in flight, so nothing will ever finish it. It used to be
// reported as running forever, pinning the pane on "Upgrading…" across
// every restart; it is failed now, on disk as well as in the answer, and
// a new Start goes through.
func TestStatusReportsAnUpgradeARestartInterruptedAsFailed(t *testing.T) {
	statusFile := filepath.Join(t.TempDir(), "upgrade-status.json")
	started := time.Now().UTC().Add(-11 * time.Hour)
	stale := Status{
		Branch:    "main",
		Process:   "a-daemon-that-has-since-restarted",
		Phase:     PhaseRunning,
		Detail:    "step 2 of 3: build",
		StartedAt: &started,
		Steps: []Step{
			{Name: stepCheckout, State: StepDone, StartedAt: &started, FinishedAt: &started},
			{Name: stepBuild, State: StepRunning, StartedAt: &started},
			{Name: stepInstall, State: StepPending},
		},
	}
	if err := writeStatus(statusFile, stale); err != nil {
		t.Fatal(err)
	}

	u := New(Config{StatusFile: statusFile})
	status, err := u.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Phase != PhaseFailed {
		t.Fatalf("phase = %q, want %q for an upgrade nothing is running", status.Phase, PhaseFailed)
	}
	if !strings.Contains(status.Detail, "interrupted") {
		t.Errorf("detail = %q, want it to say the upgrade was interrupted", status.Detail)
	}
	if status.FinishedAt == nil {
		t.Error("finishedAt unset on the interrupted upgrade")
	}
	if got := status.Steps[1].State; got != StepFailed {
		t.Errorf("build step = %q, want %q", got, StepFailed)
	}
	if got := status.Steps[0].State; got != StepDone {
		t.Errorf("checkout step = %q, want it left %q", got, StepDone)
	}
	if got := status.Steps[2].State; got != StepPending {
		t.Errorf("install step = %q, want it left %q", got, StepPending)
	}

	onDisk, err := readStatus(statusFile)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.Phase != PhaseFailed {
		t.Errorf("status file phase = %q, want %q recorded", onDisk.Phase, PhaseFailed)
	}
}

// TestStatusLeavesThisProcesssRunningUpgradeRunning is the other half:
// a "running" status this same process wrote is a live upgrade, whichever
// Upgrader reads it.
func TestStatusLeavesThisProcesssRunningUpgradeRunning(t *testing.T) {
	statusFile := filepath.Join(t.TempDir(), "upgrade-status.json")
	if err := writeStatus(statusFile, Status{Branch: "main", Phase: PhaseRunning, Process: processID}); err != nil {
		t.Fatal(err)
	}
	status, err := New(Config{StatusFile: statusFile}).Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Phase != PhaseRunning {
		t.Errorf("phase = %q, want %q while this process's own upgrade is in flight", status.Phase, PhaseRunning)
	}
}

func TestUpgraderSourceDoesNotUpgradeWhenReleaseHasNotChanged(t *testing.T) {
	_, checkout := newFixtureRepo(t, "v0.4.0")
	dir := t.TempDir()
	builtMarker := filepath.Join(dir, "built")
	restartedMarker := filepath.Join(dir, "restarted")
	var held, released bool

	u := newUpgrader(t, Config{
		SrcDir:         checkout,
		BuildCmd:       []string{"sh", "-c", "touch " + builtMarker},
		BuiltBinary:    filepath.Join(checkout, "bin", "grain"),
		InstallPath:    filepath.Join(dir, "grain"),
		RestartCmd:     []string{"sh", "-c", "touch " + restartedMarker},
		RunningRelease: func() string { return "v0.4.0" },
		StatusFile:     filepath.Join(dir, "upgrade-status.json"),
		Hold: func(string) (func(), error) {
			held = true
			return func() { released = true }, nil
		},
	})

	if err := u.Start("v0.4.0"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseOK)
	if !status.Unchanged {
		t.Errorf("status.Unchanged = false, want true (status=%+v)", status)
	}
	if !strings.Contains(status.Detail, "the version on v0.4.0 has not changed") {
		t.Errorf("status.Detail = %q, want it to say the version on v0.4.0 has not changed", status.Detail)
	}
	if _, err := os.Stat(builtMarker); !os.IsNotExist(err) {
		t.Errorf("build ran despite the version on v0.4.0 not having changed (err=%v)", err)
	}
	if _, err := os.Stat(restartedMarker); !os.IsNotExist(err) {
		t.Errorf("restart ran despite the version on v0.4.0 not having changed (err=%v)", err)
	}
	if !held || !released {
		t.Errorf("hold: held=%v released=%v, want the queue hold taken and immediately released", held, released)
	}
}

// A Refusal turns every branch away with itself: a host-mode install's
// next upgrade is its installer, not the UI.
func TestARefusalRefusesEveryBranch(t *testing.T) {
	u := New(Config{Image: &ImageConfig{}, AllowedBranches: []string{AnyBranchPattern},
		Refusal: "re-run the installer"})
	for _, branch := range []string{"main", "candidate"} {
		if err := u.Start(branch); err == nil || err.Error() != "re-run the installer" {
			t.Errorf("Start(%q) = %v, want the refusal", branch, err)
		}
	}
}

func TestUpgraderDiscardedRCDoesNotAutoDowngradeAndReportsUnchanged(t *testing.T) {
	dir := t.TempDir()
	uDefault := New(Config{SrcDir: dir, StatusFile: filepath.Join(dir, "status.json")})
	if got, err := uDefault.ValidateBranch("rc"); err != nil || got != "rc" {
		t.Fatalf("ValidateBranch(rc) on default allowlist = (%q, %v), want (rc, nil)", got, err)
	}
	if _, err := uDefault.ValidateBranch("rc2"); !errors.Is(err, ErrBranchNotAllowed) {
		t.Fatalf("ValidateBranch(rc2) = %v, want ErrBranchNotAllowed", err)
	}
	uCandidateOnly := New(Config{SrcDir: dir, StatusFile: filepath.Join(dir, "status.json"), AllowedBranches: []string{"main", "candidate", "latest"}})
	if got, err := uCandidateOnly.ValidateBranch("rc"); err != nil || got != "rc" {
		t.Fatalf("ValidateBranch(rc) with candidate allowed = (%q, %v), want (rc, nil)", got, err)
	}

	for _, branch := range []string{"candidate", "rc"} {
		t.Run(branch, func(t *testing.T) {
			dir := t.TempDir()
			refFile := filepath.Join(dir, "image.env")
			const prevRef = "ghcr.io/bwsalmon/grain/grain:candidate@" + testDigest
			writeTestFile(t, refFile, "GRAIN_IMAGE="+prevRef+"\n")

			u := newUpgrader(t, Config{
				Image: &ImageConfig{
					Repository: "ghcr.io/bwsalmon/grain/grain",
					RefFile:    refFile,
					PullCmd:    []string{"true"},
					RunCmd:     []string{"sh", "-c", "echo 7"},
				},
				RunningRelease: func() string { return "v2.3.0-rc.1" },
				ListTags: func(context.Context, string) ([]string, error) {
					return []string{"v2.1.1", "v2.3.0-rc.1", "v2.3.0-rc.1-discarded"}, nil
				},
				HealthCheckArgs: []string{"schema-version"},
				StatusFile:      filepath.Join(dir, "upgrade-status.json"),
			})

			if err := u.Start(branch); err != nil {
				t.Fatalf("Start(%q): %v", branch, err)
			}
			status := waitForPhase(t, u, PhaseOK)
			waitForIdle(t, u)
			if !status.Unchanged {
				t.Errorf("status.Unchanged = false, want true (status=%+v)", status)
			}
			wantDetail := "v2.3.0-rc.1 was discarded and no newer release candidate has been published yet; staying on the current build"
			if status.Detail != wantDetail {
				t.Errorf("status.Detail = %q, want %q", status.Detail, wantDetail)
			}
			if got := readFile(t, refFile); got != "GRAIN_IMAGE="+prevRef+"\n" {
				t.Errorf("ref file = %q, want it left at %q", got, prevRef)
			}
		})
	}
}

func TestUpgraderSourceRCFetchesCandidateBranch(t *testing.T) {
	shortRestartGrace(t)
	_, checkout := newFixtureRepo(t, "candidate")
	dir := t.TempDir()
	builtBin := filepath.Join(dir, "built-grain")
	writeTestFile(t, builtBin, "#!/bin/sh\necho ok\n")
	if err := os.Chmod(builtBin, 0o755); err != nil {
		t.Fatal(err)
	}
	installPath := filepath.Join(dir, "installed-grain")

	u := newUpgrader(t, Config{
		SrcDir:      checkout,
		BuildCmd:    []string{"true"},
		BuiltBinary: builtBin,
		InstallPath: installPath,
		StatusFile:  filepath.Join(dir, "status.json"),
	})
	if err := u.Start("rc"); err != nil {
		t.Fatalf("Start(rc): %v", err)
	}
	waitForPhase(t, u, PhaseOK)
	waitForIdle(t, u)
}
