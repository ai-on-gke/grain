package upgrade

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/installtrack"
	"github.com/ai-on-gke/grain/pkg/installtrack/installtracktest"
	"github.com/ai-on-gke/grain/pkg/version"
)

func TestTagForBranch(t *testing.T) {
	cases := map[string]string{
		"main":                  "main",
		"grain/issue-645":       "grain..issue-645",
		"claude/a/b":            "claude..a..b",
		"a./b":                  "a...b",
		"feature.with_symbols-": "feature.with_symbols-",
	}
	for branch, want := range cases {
		if got := TagForBranch(branch); got != want {
			t.Errorf("TagForBranch(%q) = %q, want %q", branch, got, want)
		}
	}
}

// TestTagForBranchGivesEveryBranchItsOwnTag is #2386: with '/' made '-',
// claude/task-123 and claude-task-123 were one tag, so whoever could push
// either branch could publish the image an upgrade onto the other pulled.
// Every pair here is two distinct branch names git accepts that the old
// substitution, or a careless replacement for it, would merge.
func TestTagForBranchGivesEveryBranchItsOwnTag(t *testing.T) {
	branches := []string{
		"claude/task-123", "claude-task-123", "claude--task-123",
		"a/b", "a-b", "a.b", "a./b", "a/b.c", "a.b/c",
		"a/b/c", "a-b/c", "a/b-c", "a-b-c",
		"x./y.z", "x/.y", "x.y/z",
	}
	seen := map[string]string{}
	for _, b := range branches {
		if err := validateBranch(b); err != nil {
			// x/.y is in the list as a name git refuses; the rest must
			// all be accepted for this test to say anything.
			if b != "x/.y" {
				t.Fatalf("validateBranch(%q) = %v, want it accepted", b, err)
			}
			continue
		}
		tag := TagForBranch(b)
		if other, ok := seen[tag]; ok {
			t.Errorf("branches %q and %q both resolve to tag %q", other, b, tag)
		}
		seen[tag] = b
	}
}

// TestValidateBranchRefusesTheNamesThatWouldAliasATag: the substitution
// is only injective over names git accepts as branches, so the ones that
// could otherwise be typed into the Upgrade pane to reach another
// branch's tag -- "claude..task" for claude/task -- are refused.
func TestValidateBranchRefusesTheNamesThatWouldAliasATag(t *testing.T) {
	for _, b := range []string{"", "claude..task", "a//b", "a/.b", "/a", "a/", ".a"} {
		if err := validateBranch(b); err == nil {
			t.Errorf("validateBranch(%q) = nil, want it refused", b)
		}
	}
	for _, b := range []string{"main", "grain/task-1706", "a./b", "v1.5.0"} {
		if err := validateBranch(b); err != nil {
			t.Errorf("validateBranch(%q) = %v, want it accepted", b, err)
		}
	}
}

// TestImageRefRefusesABranchNameThatAliasesAnother: imageRefIn does not
// lean on Start having validated the name, since the Kubernetes path and
// any later caller reach it too. "claude..task" spells the tag CI
// publishes for claude/task.
func TestImageRefRefusesABranchNameThatAliasesAnother(t *testing.T) {
	u := New(Config{Image: &ImageConfig{Repository: "ghcr.io/bwsalmon/grain/grain"}})
	for _, b := range []string{"claude..task", "claude/.task", "claude//task"} {
		if ref, err := u.imageRef(b); err == nil {
			t.Errorf("imageRef(%q) = %q, want it refused", b, ref)
		}
	}
	if ref, err := u.imageRef("claude/task"); err != nil || ref != "ghcr.io/bwsalmon/grain/grain:claude..task" {
		t.Errorf("imageRef(claude/task) = %q, %v", ref, err)
	}
}

// TestModeFollowsTheConfiguredPipeline: Config.Image is what stage
// branches on, and Mode is that same choice made answerable before an
// upgrade runs, for a caller that has to describe one (pkg/ui's pane).
func TestModeFollowsTheConfiguredPipeline(t *testing.T) {
	if got := New(Config{Image: &ImageConfig{Repository: "ghcr.io/bwsalmon/grain/grain"}}).Mode(); got != ModeImage {
		t.Errorf("Mode() with an image = %q, want %q", got, ModeImage)
	}
	if got := New(Config{SrcDir: t.TempDir()}).Mode(); got != ModeSource {
		t.Errorf("Mode() with a checkout = %q, want %q", got, ModeSource)
	}
}

// TestUpgraderImagePullsHealthChecksRecordsAndRestarts is the container
// path's counterpart to TestUpgraderStartRunsCheckoutBuildInstallAndRestart:
// no git fixture and no build at all -- just the four things an image
// upgrade does, each stubbed with a shell command that records what it
// was handed so the argv this package actually builds is asserted on
// rather than assumed.
func TestUpgraderImagePullsHealthChecksRecordsAndRestarts(t *testing.T) {
	shortRestartGrace(t)
	dir := t.TempDir()
	pulled := filepath.Join(dir, "pulled")
	ran := filepath.Join(dir, "ran")
	refFile := filepath.Join(dir, "image.env")
	restartMarker := filepath.Join(dir, "restarted")

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			// Stand-ins for `docker pull` and `docker run --rm`: each
			// appends its own arguments to a file, so the test can read
			// back exactly what it was asked to pull and run.
			PullCmd: []string{"sh", "-c", `printf '%s' "$*" > ` + pulled + `; exit 0`, "sh"},
			RunCmd:  []string{"sh", "-c", `printf '%s' "$*" > ` + ran + `; exit 0`, "sh"},
		},
		HealthCheckArgs: []string{"schema-version"},
		RestartCmd:      []string{"sh", "-c", "touch " + restartMarker},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("grain/issue-645"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseOK)

	const wantRef = "ghcr.io/bwsalmon/grain/grain:grain..issue-645"
	if got := readFile(t, pulled); got != wantRef {
		t.Errorf("pulled %q, want %q", got, wantRef)
	}
	// Everything after the pull is the image it fetched, not the tag it
	// fetched it by (pinImage).
	const pinned = wantRef + "@" + testDigest
	if got, want := readFile(t, ran), pinned+" schema-version"; got != want {
		t.Errorf("health check ran %q, want %q", got, want)
	}
	if got, want := readFile(t, refFile), "GRAIN_IMAGE="+pinned+"\n"; got != want {
		t.Errorf("ref file = %q, want %q", got, want)
	}
	if !strings.Contains(status.Detail, wantRef) {
		t.Errorf("status detail = %q, want it to name %q", status.Detail, wantRef)
	}
	waitForFile(t, restartMarker)
}

// TestUpgraderImagePullsTheSandboxImageTheNewBuildExpects covers the
// second half of a granule deployment's upgrade (bwsalmon/agents#645):
// grain and the sandbox container each task's VM runs inside are built
// from one commit, so upgrading one without the other would leave the
// next dispatched task reaching for an image nothing fetched. The new
// image is asked which sandbox it expects, and that is pulled before
// anything cuts over.
func TestUpgraderImagePullsTheSandboxImageTheNewBuildExpects(t *testing.T) {
	dir := t.TempDir()
	pulled := filepath.Join(dir, "pulled")
	refFile := filepath.Join(dir, "image.env")

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			// Appends each pull to one file, so the test reads back both
			// pulls and the order they happened in.
			PullCmd: []string{"sh", "-c", `printf '%s\n' "$*" >> ` + pulled + `; exit 0`, "sh"},
			// Stands in for the image: `schema-version` (the health
			// check) prints a version, `sandbox-image` prints the
			// sandbox ref it was built against.
			RunCmd: []string{"sh", "-c",
				`case "$2" in sandbox-image) echo ghcr.io/bwsalmon/grain/granule-sandbox:sha-abc1234;; *) echo 7;; esac`,
				"sh"},
			SandboxImageArgs: []string{"sandbox-image"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseOK)

	want := "ghcr.io/bwsalmon/grain/grain:main\nghcr.io/bwsalmon/grain/granule-sandbox:sha-abc1234\n"
	if got := readFile(t, pulled); got != want {
		t.Errorf("pulled %q, want %q", got, want)
	}
	if !strings.Contains(status.Detail, "granule-sandbox:sha-abc1234") {
		t.Errorf("status detail = %q, want it to name the sandbox image too", status.Detail)
	}
}

// TestUpgraderImageSaysWhenThePulledSandboxImageWillNotBeRun: a
// deployment whose own configuration pins a sandbox image still pulls the
// one the new build names -- there is no way to know from here that the
// pin is deliberate -- but it stops reporting that pull as a sandbox kept
// in step, because it is not one. The pin wins when a VM is created, so
// what the step has really done is spend an eleven-minute pull on an
// image nothing will run, and the drift between daemon and guest goes on
// growing (grain/task-306).
func TestUpgraderImageSaysWhenThePulledSandboxImageWillNotBeRun(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			RunCmd: []string{"sh", "-c",
				`case "$2" in sandbox-image) echo ghcr.io/bwsalmon/grain/guest:sha-e5544d6;; *) echo 7;; esac`,
				"sh"},
			SandboxImageArgs: []string{"sandbox-image"},
			SandboxImagePin:  "ghcr.io/bwsalmon/grain/guest:sha-2f89890",
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseOK)

	// Both tags, in the line the pane keeps afterwards: the one that was
	// pulled is what an operator would otherwise read as "this is what
	// runs now", and the pinned one is the answer to "then what is it
	// running?".
	for _, want := range []string{"guest:sha-e5544d6", "will not run", "guest:sha-2f89890"} {
		if !strings.Contains(status.Detail, want) {
			t.Errorf("status detail = %q, want it to say %q", status.Detail, want)
		}
	}
	var sandbox *Step
	for i := range status.Steps {
		if status.Steps[i].Name == stepSandbox {
			sandbox = &status.Steps[i]
		}
	}
	if sandbox == nil {
		t.Fatalf("no %q step in %+v", stepSandbox, status.Steps)
	}
	if !strings.Contains(sandbox.Detail, "guest:sha-2f89890") {
		t.Errorf("sandbox step detail = %q, want it to name the pinned image", sandbox.Detail)
	}
}

// A pin that already agrees with what the new build names is not drift
// and gets no commentary: the two are being kept in step, which is the
// ordinary case for a deployment that pinned the same tag CI publishes.
func TestUpgraderImageSaysNothingWhenThePinIsWhatTheBuildExpects(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			RunCmd: []string{"sh", "-c",
				`case "$2" in sandbox-image) echo ghcr.io/bwsalmon/grain/guest:sha-e5544d6;; *) echo 7;; esac`,
				"sh"},
			SandboxImageArgs: []string{"sandbox-image"},
			SandboxImagePin:  "ghcr.io/bwsalmon/grain/guest:sha-e5544d6",
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseOK)
	if strings.Contains(status.Detail, "will not run") {
		t.Errorf("status detail = %q, want no warning about a pin that matches", status.Detail)
	}
}

// TestUpgraderImageStopsWhenTheSandboxImageCannotBePulled: a sandbox
// image that cannot be fetched leaves the deployment where it was, the
// same as a failed health check. Cutting over anyway would produce a
// deployment that serves its UI perfectly well and fails every task it
// dispatches, which is a worse outcome than not upgrading.
func TestUpgraderImageStopsWhenTheSandboxImageCannotBePulled(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	restartMarker := filepath.Join(dir, "restarted")

	const previous = "GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:main\n"
	if err := os.WriteFile(refFile, []byte(previous), 0o644); err != nil {
		t.Fatal(err)
	}

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			// The grain image pulls; the sandbox image does not.
			PullCmd: []string{"sh", "-c",
				`case "$1" in *granule-sandbox*) echo 'manifest unknown' >&2; exit 1;; esac`, "sh"},
			RunCmd: []string{"sh", "-c",
				`case "$2" in sandbox-image) echo ghcr.io/bwsalmon/grain/granule-sandbox:sha-abc1234;; *) echo 7;; esac`,
				"sh"},
			SandboxImageArgs: []string{"sandbox-image"},
		},
		HealthCheckArgs: []string{"schema-version"},
		RestartCmd:      []string{"sh", "-c", "touch " + restartMarker},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseFailed)
	if !strings.Contains(status.Detail, "sandbox image") {
		t.Errorf("status detail = %q, want it to name the sandbox image", status.Detail)
	}
	if got := readFile(t, refFile); got != previous {
		t.Errorf("ref file = %q, want it untouched at %q", got, previous)
	}
	if _, err := os.Stat(restartMarker); err == nil {
		t.Error("restart command ran despite the sandbox image never arriving")
	}
}

// TestUpgraderImageToleratesAnImageWithNoSandboxAnswer: an older grain,
// pulled by a rollback, predates the sandbox-image subcommand and answers
// with a usage error rather than a ref. That is "nothing to pull", not a
// reason to fail a rollback that is otherwise fine.
func TestUpgraderImageToleratesAnImageWithNoSandboxAnswer(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			RunCmd: []string{"sh", "-c",
				`case "$2" in sandbox-image) echo 'flag provided but not defined' >&2; exit 2;; *) echo 7;; esac`,
				"sh"},
			SandboxImageArgs: []string{"sandbox-image"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("v-old"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseOK)
	if got, want := readFile(t, refFile),
		"GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:v-old@"+testDigest+"\n"; got != want {
		t.Errorf("ref file = %q, want %q", got, want)
	}
	// And the step says *why* it named none. "names no sandbox image" on
	// its own is the line a deployment that had quietly stopped keeping
	// its guest in step would also have shown (grain/task-338).
	detail := stepDetail(t, status, stepSandbox)
	if !strings.Contains(detail, "refused the question") {
		t.Errorf("%s step = %q, want it to say the build refused the question", stepSandbox, detail)
	}
	if !strings.Contains(detail, "flag provided but not defined") {
		t.Errorf("%s step = %q, want it to carry what the image said", stepSandbox, detail)
	}
}

// TestUpgraderImageStopsWhenTheSandboxQuestionCannotBeAsked is the other
// half of the test above, and the distinction the two of them exist for.
// An image that runs and refuses the subcommand has answered: there is
// no sandbox to keep in step. A docker that never got a container
// started has not -- and reading its 125 as "this build runs no sandbox"
// is how an upgrade cuts over having pulled no guest at all, leaving the
// next dispatched task reaching for an image nothing fetched
// (grain/task-338).
func TestUpgraderImageStopsWhenTheSandboxQuestionCannotBeAsked(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	restartMarker := filepath.Join(dir, "restarted")

	const previous = "GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:main\n"
	if err := os.WriteFile(refFile, []byte(previous), 0o644); err != nil {
		t.Fatal(err)
	}

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			// The health check runs; the sandbox question dies the way
			// `docker run` dies when the daemon could not start the
			// container -- 125, docker's own status, with docker's own
			// complaint on stderr rather than the image's.
			RunCmd: []string{"sh", "-c",
				`case "$2" in sandbox-image) echo 'Cannot connect to the Docker daemon' >&2; exit 125;; *) echo 7;; esac`,
				"sh"},
			SandboxImageArgs: []string{"sandbox-image"},
		},
		HealthCheckArgs: []string{"schema-version"},
		RestartCmd:      []string{"sh", "-c", "touch " + restartMarker},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseFailed)
	if !strings.Contains(status.Detail, "which sandbox image it expects") {
		t.Errorf("status detail = %q, want it to name the question that could not be asked", status.Detail)
	}
	if got := readFile(t, refFile); got != previous {
		t.Errorf("ref file = %q, want it untouched at %q", got, previous)
	}
	if _, err := os.Stat(restartMarker); err == nil {
		t.Error("restart command ran despite never learning which sandbox image the new build expects")
	}
}

// And the same for the bound: a question that hit imageRunTimeout is a
// question that was never answered, not an image saying it has no
// sandbox. This is the flavour grain/task-329 made rarer by raising that
// bound from ten seconds to two minutes without changing what a
// timed-out question meant.
func TestUpgraderImageStopsWhenTheSandboxQuestionTimesOut(t *testing.T) {
	shortImageRunTimeout(t)
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	restartMarker := filepath.Join(dir, "restarted")

	const previous = "GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:main\n"
	if err := os.WriteFile(refFile, []byte(previous), 0o644); err != nil {
		t.Fatal(err)
	}

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			// The health check answers at once; the sandbox question
			// never comes back, the way a container that will not start
			// tearing down does not.
			RunCmd: []string{"sh", "-c",
				`case "$2" in sandbox-image) sleep 60;; *) echo 7;; esac`, "sh"},
			SandboxImageArgs: []string{"sandbox-image"},
		},
		HealthCheckArgs: []string{"schema-version"},
		RestartCmd:      []string{"sh", "-c", "touch " + restartMarker},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseFailed)
	assertStoppedOnItsBound(t, status, stepSandbox)
	if !strings.Contains(status.Detail, "no answer within") {
		t.Errorf("status detail = %q, want it to say the bound is what stopped it", status.Detail)
	}
	if got := readFile(t, refFile); got != previous {
		t.Errorf("ref file = %q, want it untouched at %q", got, previous)
	}
	if _, err := os.Stat(restartMarker); err == nil {
		t.Error("restart command ran despite the sandbox question that never came back")
	}
}

// TestRanAndFailedSeparatesAnAnswerFromAHostThatCouldNotAsk covers the
// predicate itself against real processes, since every classification
// above rests on it and none of the cases are things a fake error can
// honestly stand in for.
func TestRanAndFailedSeparatesAnAnswerFromAHostThatCouldNotAsk(t *testing.T) {
	run := func(ctx context.Context, argv ...string) error {
		return newCommand(ctx, "", argv[0], argv[1:]...).Run()
	}
	ctx := context.Background()

	// The command chose its own status: an image refusing a subcommand
	// it does not have.
	if err := run(ctx, "sh", "-c", "exit 1"); !ranAndFailed(err) {
		t.Errorf("ranAndFailed(%v) = false for a command that ran and exited 1, want true", err)
	}
	// docker's own statuses, which say the container is what did not
	// happen.
	for _, code := range []int{dockerRunFailed, dockerCommandNotRunnable, dockerCommandNotFound} {
		err := run(ctx, "sh", "-c", fmt.Sprintf("exit %d", code))
		if ranAndFailed(err) {
			t.Errorf("ranAndFailed(%v) = true for docker's own exit %d, want false", err, code)
		}
	}
	// Killed on a deadline -- newCommand's process-group kill, which is
	// what every bound in this package eventually comes down to.
	bounded, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := run(bounded, "sleep", "60"); ranAndFailed(err) {
		t.Errorf("ranAndFailed(%v) = true for a command killed on its deadline, want false", err)
	}
	// Never started at all.
	if err := run(ctx, filepath.Join(t.TempDir(), "no-such-binary")); ranAndFailed(err) {
		t.Errorf("ranAndFailed(%v) = true for a command that never started, want false", err)
	}
}

// TestUpgraderImageLeavesRefFileAloneWhenHealthCheckFails is the
// image path's answer to TestUpgraderRollsBackWhenHealthCheckFails: it
// has no rollback because it never cuts over in the first place. A
// pulled image that cannot even run `schema-version` must leave the ref
// file exactly as it was -- so the restart that never happens would have
// brought up the same image the deployment already runs.
func TestUpgraderImageLeavesRefFileAloneWhenHealthCheckFails(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	restartMarker := filepath.Join(dir, "restarted")

	const previous = "GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:main\n"
	if err := os.WriteFile(refFile, []byte(previous), 0o644); err != nil {
		t.Fatal(err)
	}

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			// A "container" that starts and exits 1, standing in for an
			// image whose binary is broken outright.
			RunCmd: []string{"false"},
		},
		HealthCheckArgs: []string{"schema-version"},
		// Should never run: a failed health check stops this before the
		// deployment is ever pointed at the new image.
		RestartCmd: []string{"sh", "-c", "touch " + restartMarker},
		StatusFile: filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseFailed)
	if !strings.Contains(status.Detail, "health check") {
		t.Errorf("status detail = %q, want it to name the health check", status.Detail)
	}
	if got := readFile(t, refFile); got != previous {
		t.Errorf("ref file = %q, want it untouched at %q", got, previous)
	}
	if _, err := os.Stat(restartMarker); err == nil {
		t.Error("restart command ran despite the failed health check")
	}
}

// shortImageRunTimeout shrinks the container path's bound for a test
// that has to reach it, the way shortRestartGrace shrinks the wait after
// a restart: the real value is two minutes, which is the point of it and
// not something a test should sit through.
func shortImageRunTimeout(t *testing.T) {
	t.Helper()
	old := imageRunTimeout
	imageRunTimeout = 100 * time.Millisecond
	t.Cleanup(func() { imageRunTimeout = old })
}

// boundSlack is everything a bounded step spends either side of the
// bound itself: forking the command, killing its process group when the
// deadline expires, reaping it. Generous on purpose, because CI's hosts
// are contended -- and still far under every bound a wrongly wired step
// would be sitting on instead (the binary path's ten seconds,
// waitForPhase's minute, the sixty-second sleeps these tests hang on),
// which is the whole distinction the assertion below is drawn for.
const boundSlack = 5 * time.Second

// assertStoppedOnItsBound checks that the named step gave up because
// imageRunTimeout expired and not because something else, much slower,
// eventually did.
//
// It measures the step and not the whole of Start -- the same number
// the upgrader logs for itself ("step 4/7 sandbox image: failed after
// 102ms") -- which is the difference between a test about a bound and a
// test about how busy the host is. Timing Start charged a 100ms bound
// with the pull and the health check ahead of it as well, so a run
// whose two bare `sh` spawns took three seconds between them under load
// failed a branch that touches none of this package, announcing that
// "something other than imageRunTimeout is what stopped it" about a
// step the log showed had been killed at 102ms exactly as designed
// (grain/task-475).
func assertStoppedOnItsBound(t *testing.T, status Status, name string) {
	t.Helper()
	if took := stepDuration(t, status, name); took > imageRunTimeout+boundSlack {
		t.Errorf("the %s step took %s to give up on a %s bound -- something other than "+
			"imageRunTimeout is what stopped it", name, took, imageRunTimeout)
	}
}

// stepDuration reads back how long one step of a finished upgrade took,
// from the timestamps the pane's status keeps for it.
func stepDuration(t *testing.T, status Status, name string) time.Duration {
	t.Helper()
	for _, step := range status.Steps {
		if step.Name != name {
			continue
		}
		if step.StartedAt == nil || step.FinishedAt == nil {
			t.Fatalf("%q step has no start and finish to measure it between: %+v", name, step)
		}
		return step.FinishedAt.Sub(*step.StartedAt)
	}
	t.Fatalf("no %q step in %+v", name, status.Steps)
	return 0
}

// A container that answers and then does not exit is still bounded, and
// bounded by imageRunTimeout -- which is what stops that value from
// quietly going back to being the binary path's ten seconds, since a
// health check wired to any other bound would sit here for a minute
// instead of failing at once.
//
// The run command prints its answer immediately and then hangs, because
// that is the shape the real failure had: `docker run --rm` logging the
// schema version three seconds in and not returning, so the answer was
// in hand and thrown away with the process (grain/task-329).
func TestAnImageThatAnswersAndThenHangsFailsTheHealthCheckOnItsBound(t *testing.T) {
	shortImageRunTimeout(t)
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	restartMarker := filepath.Join(dir, "restarted")

	const previous = "GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:main\n"
	if err := os.WriteFile(refFile, []byte(previous), 0o644); err != nil {
		t.Fatal(err)
	}

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			RunCmd:     []string{"sh", "-c", "echo 18; sleep 60", "sh"},
		},
		HealthCheckArgs: []string{"schema-version"},
		RestartCmd:      []string{"sh", "-c", "touch " + restartMarker},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseFailed)
	// Failed on *this* bound, not on some other one that happens to be
	// shorter than waitForPhase's own minute: a health check wired back
	// to a hardcoded ten seconds would still reach PhaseFailed below,
	// and would take a hundred times as long to get there.
	assertStoppedOnItsBound(t, status, stepHealthCheck)
	if !strings.Contains(status.Detail, "health check") {
		t.Errorf("status detail = %q, want it to name the health check", status.Detail)
	}
	// And says how long it waited, not merely that it waited. The reader
	// of this line is an operator, or a container e2e log, asking which
	// bound expired -- and "it ran out of time" alone sent three release
	// failures' worth of readers looking through this package for the
	// answer (grain/task-341).
	if !strings.Contains(status.Detail, "ran out of time after ") {
		t.Errorf("status detail = %q, want it to say how long it waited before the bound stopped it",
			status.Detail)
	}
	if got := readFile(t, refFile); got != previous {
		t.Errorf("ref file = %q, want it untouched at %q", got, previous)
	}
	if _, err := os.Stat(restartMarker); err == nil {
		t.Error("restart command ran despite the health check that never came back")
	}
}

// And the value itself, since the value itself was the bug: ten seconds
// bounds a binary being exec'd, and this path is a whole container
// being created, run and torn down. Asserted as a floor rather than as
// the exact number -- what matters is that nobody matches it back to
// healthCheck's bound by hand, not that it stays at any one value
// forever. See imageRunTimeout's own comment for what that cost.
//
// The floor is four minutes because that is where the measurement in
// that comment puts it: the worst single `docker run --rm <image>
// schema-version` timed on a contended four-CPU host took 211 seconds,
// and a bound under that is one the release gate has already been shown
// to flake against.
func TestTheContainerPathsBoundAllowsForAContainerAndNotJustAnExec(t *testing.T) {
	if imageRunTimeout < 4*time.Minute {
		t.Errorf("imageRunTimeout = %s, which is under the 211s a single `docker run --rm` of "+
			"this image has been measured at on a loaded host -- the release failure it was "+
			"raised for, twice", imageRunTimeout)
	}
}

// The other end of it, because the argument for raising this bound runs
// out before DefaultKubeHealthCheckTimeout's ten minutes and nothing
// said so.
//
// That bound covers one question put to one pod. This one is charged
// four times in a single staging -- stageImage's health check, the
// sandbox image, the flag names, and the schema version -- and
// schemaCheckTimeout's rehearsal is charged on top of all four. Every
// one of those sits inside Config.Timeout together with the pull that
// precedes them, and the pull is the step that actually has to move
// gigabytes over a network.
//
// So a raise that reads as harmless per-run ("ten minutes, like the
// Kubernetes path") is what would leave a slow pull with no budget at
// all and turn this package's most generous failure mode into its
// worst: an upgrade killed by Config.Timeout partway through, rather
// than by a bound that names which question went unanswered.
func TestTheContainerPathsBoundLeavesRoomForThePull(t *testing.T) {
	const runsPerStaging = 4
	worst := runsPerStaging*imageRunTimeout + schemaCheckTimeout
	// The pull is the only other thing inside Config.Timeout and the one
	// with real work to do, so it keeps a third of the budget: fifteen
	// minutes to move a multi-gigabyte image, which is generous for a
	// warm registry and not absurd for a cold one. At the values this
	// fence was written around -- five minutes each and five for the
	// rehearsal -- the pull has twenty; at the ten minutes parity with
	// DefaultKubeHealthCheckTimeout would suggest, it has none.
	const pullShare = 3
	if room := defaultTimeout - worst; room < defaultTimeout/pullShare {
		t.Errorf("imageRunTimeout = %s and schemaCheckTimeout = %s, so the %d container runs of "+
			"one staging plus the rehearsal can spend %s of defaultTimeout's %s and leave the "+
			"pull only %s", imageRunTimeout, schemaCheckTimeout, runsPerStaging, worst,
			defaultTimeout, room)
	}
}

// TestUpgraderImageFailsWhenPullFails covers the other stop: a branch
// with no image published for it (a push whose build-artifacts run has
// not finished, or a typo) fails at the pull, before the health check
// and before the ref file.
func TestUpgraderImageFailsWhenPullFails(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"sh", "-c", "echo 'manifest unknown' >&2; exit 1", "sh"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("no-such-branch"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseFailed)
	if !strings.Contains(status.Detail, "pull") || !strings.Contains(status.Detail, "manifest unknown") {
		t.Errorf("status detail = %q, want it to name the failed pull and its output", status.Detail)
	}
	if _, err := os.Stat(refFile); !os.IsNotExist(err) {
		t.Errorf("ref file exists after a failed pull (err=%v)", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// TestUpgraderImageFailsWithoutARepository: a deployment configured with
// -upgrade-image-ref-file but no repository to pull from has an Upgrade
// button that cannot work, and the click has to say so rather than write
// a ref file naming ":main".
func TestUpgraderImageFailsWithoutARepository(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")

	u := newUpgrader(t, Config{
		Image:      &ImageConfig{RefFile: refFile, PullCmd: []string{"true"}},
		StatusFile: filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseFailed)
	if !strings.Contains(status.Detail, "repository") {
		t.Errorf("status detail = %q, want it to name the missing repository", status.Detail)
	}
	if _, err := os.Stat(refFile); !os.IsNotExist(err) {
		t.Errorf("ref file written despite there being no image to name (err=%v)", err)
	}
}

// TestUpgraderImageRejectsABranchTooLongToBeATag stops at imageRef, before
// the pull: a branch over the registry's 128-character tag limit has no
// image published for it at all (CI could not have pushed the tag), so the
// failure names the limit rather than arriving as a puzzling 404 from a
// pull that was never going to work.
func TestUpgraderImageRejectsABranchTooLongToBeATag(t *testing.T) {
	dir := t.TempDir()
	pullMarker := filepath.Join(dir, "pulled")

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    filepath.Join(dir, "image.env"),
			PullCmd:    []string{"touch", pullMarker},
		},
		StatusFile: filepath.Join(dir, "upgrade-status.json"),
	})

	branch := "grain/" + strings.Repeat("x", maxTagLen)
	if err := u.Start(branch); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseFailed)
	if !strings.Contains(status.Detail, "no image is published") {
		t.Errorf("status detail = %q, want it to explain that no image exists for that branch", status.Detail)
	}
	if _, err := os.Stat(pullMarker); err == nil {
		t.Error("a branch too long to be a tag was pulled anyway")
	}
}

// TestUpgraderImageRejectsASecondClickWhileOneIsRunning is the image
// path's half of TestUpgraderStartRejectsConcurrentUpgrade: the UI
// disables the button while an upgrade runs, but nothing stops a second
// tab (or a reload that lands mid-upgrade) from posting again, and two
// concurrent pulls rewriting one ref file is not a race worth having.
func TestUpgraderImageRejectsASecondClickWhileOneIsRunning(t *testing.T) {
	dir := t.TempDir()
	// The first pull is held until the second Start has been made and
	// refused, so that "still pulling" is a fact rather than a second of
	// hoping (latch, in upgrade_test.go).
	pulling := newLatch(t)
	defer pulling.open()

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    filepath.Join(dir, "image.env"),
			// Via `sh -c`, since the ref is appended to whatever PullCmd
			// is: the trailing "sh" takes that position as $0 rather than
			// leaving the ref to be read as an argument of the script.
			PullCmd: []string{"sh", "-c", pulling.wait(), "sh"},
		},
		StatusFile: filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("main"); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := u.Start("main"); err != ErrUpgradeInProgress {
		t.Errorf("second Start: got %v, want ErrUpgradeInProgress", err)
	}
	pulling.open()
	waitForPhase(t, u, PhaseOK)
}

// TestUpgraderRunningReadsTheRefFile: the answer to "what is this
// deployment on?" on the container path is the ref file, and the case
// that matters most is the one with no upgrade on record at all --
// scripts/setup.sh wrote that line at install time, and until now the
// only thing the pane could show an operator was a last attempt that
// never happened (grain/task-50).
func TestUpgraderRunningReadsTheRefFile(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	writeTestFile(t, refFile, "GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:main\n")

	u := New(Config{
		Image:      &ImageConfig{Repository: "ghcr.io/bwsalmon/grain/grain", RefFile: refFile},
		StatusFile: filepath.Join(dir, "upgrade-status.json"),
	})

	running, err := u.Running()
	if err != nil {
		t.Fatalf("Running: %v", err)
	}
	if want := "ghcr.io/bwsalmon/grain/grain:main"; running.Ref != want {
		t.Errorf("Running().Ref = %q, want %q", running.Ref, want)
	}
	if running.Build != (version.Info{}) {
		t.Errorf("Running().Build = %+v on the image path, want the zero stamp", running.Build)
	}
}

// TestUpgraderRunningReadsTheRefFileSystemdWouldRead: the file is a
// systemd EnvironmentFile an operator may have edited by hand, so it is
// parsed the way systemd parses one -- comments and blank lines ignored,
// the last assignment winning, quotes stripped -- rather than taken as
// one line of exactly the shape writeImageRef leaves.
func TestUpgraderRunningReadsTheRefFileSystemdWouldRead(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	writeTestFile(t, refFile, "# pinned by hand, 2026-09-02\n"+
		"GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:main\n"+
		"\n"+
		`GRAIN_IMAGE="ghcr.io/bwsalmon/grain/grain:grain-task-39"`+"\n")

	u := New(Config{
		Image:      &ImageConfig{Repository: "ghcr.io/bwsalmon/grain/grain", RefFile: refFile},
		StatusFile: filepath.Join(dir, "upgrade-status.json"),
	})

	running, err := u.Running()
	if err != nil {
		t.Fatalf("Running: %v", err)
	}
	if want := "ghcr.io/bwsalmon/grain/grain:grain-task-39"; running.Ref != want {
		t.Errorf("Running().Ref = %q, want %q", running.Ref, want)
	}
}

// TestUpgraderRunningReportsARefFileItCannotRead: a deployment that
// cannot say where it is has to say so rather than claim an empty image,
// since the caller's whole choice is between showing a line and showing
// none.
func TestUpgraderRunningReportsARefFileItCannotRead(t *testing.T) {
	dir := t.TempDir()
	missing := New(Config{
		Image:      &ImageConfig{Repository: "ghcr.io/bwsalmon/grain/grain", RefFile: filepath.Join(dir, "image.env")},
		StatusFile: filepath.Join(dir, "upgrade-status.json"),
	})
	if running, err := missing.Running(); err == nil {
		t.Errorf("Running() with no ref file = %+v, want an error", running)
	}

	empty := filepath.Join(dir, "empty.env")
	writeTestFile(t, empty, "# nothing but a comment\n")
	quiet := New(Config{
		Image:      &ImageConfig{Repository: "ghcr.io/bwsalmon/grain/grain", RefFile: empty},
		StatusFile: filepath.Join(dir, "upgrade-status.json"),
	})
	if running, err := quiet.Running(); err == nil {
		t.Errorf("Running() with a ref file naming no image = %+v, want an error", running)
	}
}

// TestUpgraderRunningReadsTheInstalledBinaryOnTheSourcePath: the other
// pipeline has no ref file, and what it would bring up is the binary at
// InstallPath -- read as a file, so this stands the test binary itself in
// for one (unstamped, as every `go test` binary is, which is the "no
// stamp to show" answer the caller has to survive).
func TestUpgraderRunningReadsTheInstalledBinaryOnTheSourcePath(t *testing.T) {
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	u := New(Config{
		SrcDir:      dir,
		InstallPath: self,
		StatusFile:  filepath.Join(dir, "upgrade-status.json"),
	})

	running, err := u.Running()
	if err != nil {
		t.Fatalf("Running: %v", err)
	}
	if running.Ref != "" {
		t.Errorf("Running().Ref = %q on the source path, want no image", running.Ref)
	}

	// And a binary that isn't there yet -- an -upgrade-src-dir
	// deployment before its first install -- is a "cannot say", not a
	// build with no commit.
	notInstalled := New(Config{
		SrcDir:      dir,
		InstallPath: filepath.Join(dir, "grain"),
		StatusFile:  filepath.Join(dir, "upgrade-status.json"),
	})
	if running, err := notInstalled.Running(); err == nil {
		t.Errorf("Running() with nothing installed = %+v, want an error", running)
	}
}

// writeTestFile lays down a fixture, readFile's counterpart above.
func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// stepDetail is one named step out of a finished upgrade's status.
func stepDetail(t *testing.T, status Status, name string) string {
	t.Helper()
	for _, step := range status.Steps {
		if step.Name == name {
			return step.Detail
		}
	}
	t.Fatalf("no %q step in %+v", name, status.Steps)
	return ""
}

// The check grain/task-233 added: before cutting over, ask the image
// that has just been pulled whether it accepts the flags this host's
// systemd unit starts grain with. The unit is written once by
// scripts/setup.sh and the image moves under it on every upgrade, so
// this is the only moment the two can be compared while there is still
// something to be done about it.
func TestUpgraderImageReportsFlagsTheNewBuildWouldIgnore(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			// The image, standing in for a build that has dropped
			// -root-shell-control-dir: it still answers the health check
			// and it still lists its flags, and that flag is not among
			// them.
			RunCmd: []string{"sh", "-c",
				`case "$3" in -flag-names) printf 'data-dir\nmax-workers\nreboot-cmd\n';; *) echo 7;; esac`,
				"sh"},
			UnitFlags:     []string{"-data-dir", "-max-workers", "-root-shell-control-dir"},
			FlagNamesArgs: []string{"daemon", "-flag-names"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Reported, not refused, and deliberately: the upgrade that removes
	// a flag is exactly the one every already-installed host needs, so
	// failing it would strand every deployment on the build before the
	// removal (checkImageFlags).
	status := waitForPhase(t, u, PhaseOK)
	if got := readFile(t, refFile); !strings.Contains(got, "grain:main") {
		t.Errorf("ref file = %q, want the upgrade to have cut over anyway", got)
	}
	detail := stepDetail(t, status, stepFlags)
	if !strings.Contains(detail, "-root-shell-control-dir") {
		t.Errorf("%s step = %q, want it to name the flag the new build does not define", stepFlags, detail)
	}
	if strings.Contains(detail, "-data-dir") {
		t.Errorf("%s step = %q, want only the flags that are a problem in it", stepFlags, detail)
	}
	if !strings.Contains(detail, "setup.sh") {
		t.Errorf("%s step = %q, want the fix named -- the unit is on the host, not in the image", stepFlags, detail)
	}
	if !strings.Contains(status.Detail, "-root-shell-control-dir") {
		t.Errorf("status detail = %q, want the disagreement in the line the pane keeps afterwards", status.Detail)
	}
}

func TestUpgraderImageSaysNothingWhenTheNewBuildTakesEveryFlag(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			RunCmd: []string{"sh", "-c",
				`case "$3" in -flag-names) printf 'data-dir\nmax-workers\n';; *) echo 7;; esac`,
				"sh"},
			UnitFlags:     []string{"-data-dir", "-max-workers"},
			FlagNamesArgs: []string{"daemon", "-flag-names"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseOK)
	if detail := stepDetail(t, status, stepFlags); !strings.Contains(detail, "accepts all 2 flags") {
		t.Errorf("%s step = %q, want it to say the unit and the image agree", stepFlags, detail)
	}
}

// A rollback pulls a build older than the question, which answers with a
// usage error rather than a flag list. That is "nothing to compare", not
// a failed upgrade -- the same reading the sandbox-image step gives a
// build that refuses *its* question, and for the same reason.
func TestUpgraderImageCarriesOnWhenTheImageCannotListItsFlags(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			RunCmd: []string{"sh", "-c",
				`case "$3" in -flag-names) echo "flag provided but not defined: -flag-names" >&2; exit 2;; *) echo 7;; esac`,
				"sh"},
			UnitFlags:     []string{"-data-dir"},
			FlagNamesArgs: []string{"daemon", "-flag-names"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseOK)
	detail := stepDetail(t, status, stepFlags)
	if !strings.Contains(detail, "not checked") {
		t.Errorf("%s step = %q, want it to say the question could not be asked", stepFlags, detail)
	}
	if got := readFile(t, refFile); !strings.Contains(got, "grain:main") {
		t.Errorf("ref file = %q, want an unanswerable check not to stop a rollback", got)
	}
	if strings.Contains(detail, "never asked") {
		t.Errorf("%s step = %q, want a build that refused the question not reported as one that was never asked", stepFlags, detail)
	}
}

// The other way this check can come back empty-handed, which the step
// has to say differently: the image was never asked at all -- a docker
// that could not start a container, a question killed on its bound.
// Still not fatal, for the reason checkImageFlags gives, but "this build
// is too old to answer" would be a claim about the build that nothing
// here has evidence for (grain/task-338).
func TestUpgraderImageSaysSoWhenTheFlagQuestionWasNeverAsked(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			RunCmd: []string{"sh", "-c",
				`case "$3" in -flag-names) echo 'Cannot connect to the Docker daemon' >&2; exit 125;; *) echo 7;; esac`,
				"sh"},
			UnitFlags:     []string{"-data-dir"},
			FlagNamesArgs: []string{"daemon", "-flag-names"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseOK)
	detail := stepDetail(t, status, stepFlags)
	if !strings.Contains(detail, "never asked") {
		t.Errorf("%s step = %q, want it to say the question was never put", stepFlags, detail)
	}
	if got := readFile(t, refFile); !strings.Contains(got, "grain:main") {
		t.Errorf("ref file = %q, want a check that could not run not to stop an upgrade", got)
	}
}

// A deployment whose flags are not written down on the host it runs on
// -- Kubernetes, where the args are in the Deployment spec -- has
// nothing to compare and is told so, rather than being shown a step that
// silently did nothing.
func TestUpgraderImageSkipsTheFlagCheckWithNoUnitToCheck(t *testing.T) {
	dir := t.TempDir()
	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    filepath.Join(dir, "image.env"),
			PullCmd:    []string{"true"},
			RunCmd:     []string{"sh", "-c", "echo 7", "sh"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseOK)
	for _, step := range status.Steps {
		if step.Name == stepFlags && step.State != StepSkipped {
			t.Errorf("%s step = %v, want it skipped", stepFlags, step.State)
		}
	}
}

// TestUpgraderImageCleansUpOlderImagesKeepingPreviousVersion verifies that older
// images are removed after an upgrade, while keeping the newly upgraded version
// and the immediately previous version (grain/task-10).
func TestUpgraderImageCleansUpOlderImagesKeepingPreviousVersion(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	removed := filepath.Join(dir, "removed")
	const prevRef = "ghcr.io/bwsalmon/grain/grain:v2"
	writeTestFile(t, refFile, "GRAIN_IMAGE="+prevRef+"\n")

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			RunCmd:     []string{"sh", "-c", "echo 7"},
			ListCmd: []string{"sh", "-c",
				`echo ghcr.io/bwsalmon/grain/grain:v3
				 echo ghcr.io/bwsalmon/grain/grain:v2
				 echo ghcr.io/bwsalmon/grain/grain:v1
				 echo ghcr.io/bwsalmon/grain/grain:v0`,
				"sh"},
			RemoveCmd: []string{"sh", "-c", `printf '%s\n' "$*" >> ` + removed + `; exit 0`, "sh"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("v3"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, u, PhaseOK)

	wantRemoved := "ghcr.io/bwsalmon/grain/grain:v1\nghcr.io/bwsalmon/grain/grain:v0\n"
	if got := readFile(t, removed); got != wantRemoved {
		t.Errorf("removed images = %q, want %q", got, wantRemoved)
	}
	if got, want := readFile(t, refFile), "GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:v3@"+testDigest+"\n"; got != want {
		t.Errorf("ref file = %q, want %q", got, want)
	}
}

// TestUpgraderImageCleansUpOlderSandboxImagesKeepingPreviousVersion verifies that
// older sandbox images are also cleaned up while keeping the new and previous versions.
func TestUpgraderImageCleansUpOlderSandboxImagesKeepingPreviousVersion(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	removed := filepath.Join(dir, "removed")
	const prevRef = "ghcr.io/bwsalmon/grain/grain:v2"
	writeTestFile(t, refFile, "GRAIN_IMAGE="+prevRef+"\n")

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			RunCmd: []string{"sh", "-c",
				`case "$1" in
					*v3*) echo ghcr.io/bwsalmon/grain/granule-sandbox:sha-v3;;
					*v2*) echo ghcr.io/bwsalmon/grain/granule-sandbox:sha-v2;;
					*) echo 7;;
				esac`,
				"sh"},
			SandboxImageArgs: []string{"sandbox-image"},
			ListCmd: []string{"sh", "-c",
				`case "$1" in
					*granule-sandbox*)
						echo ghcr.io/bwsalmon/grain/granule-sandbox:sha-v3
						echo ghcr.io/bwsalmon/grain/granule-sandbox:sha-v2
						echo ghcr.io/bwsalmon/grain/granule-sandbox:sha-v1
						echo ghcr.io/bwsalmon/grain/granule-sandbox:sha-v0
						;;
					*grain*)
						echo ghcr.io/bwsalmon/grain/grain:v3
						echo ghcr.io/bwsalmon/grain/grain:v2
						echo ghcr.io/bwsalmon/grain/grain:v1
						;;
				esac`,
				"sh"},
			RemoveCmd: []string{"sh", "-c", `printf '%s\n' "$*" >> ` + removed + `; exit 0`, "sh"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("v3"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, u, PhaseOK)

	got := readFile(t, removed)
	for _, want := range []string{"ghcr.io/bwsalmon/grain/grain:v1", "ghcr.io/bwsalmon/grain/granule-sandbox:sha-v1", "ghcr.io/bwsalmon/grain/granule-sandbox:sha-v0"} {
		if !strings.Contains(got, want) {
			t.Errorf("removed images %q does not contain expected %q", got, want)
		}
	}
	for _, unwan := range []string{"ghcr.io/bwsalmon/grain/grain:v3", "ghcr.io/bwsalmon/grain/grain:v2", "ghcr.io/bwsalmon/grain/granule-sandbox:sha-v3", "ghcr.io/bwsalmon/grain/granule-sandbox:sha-v2"} {
		if strings.Contains(got, unwan) {
			t.Errorf("removed images %q unexpectedly contains kept image %q", got, unwan)
		}
	}
}

// TestUpgraderImageKeepsPinnedSandboxImage verifies that an explicitly pinned sandbox
// image is preserved during old image cleanup.
func TestUpgraderImageKeepsPinnedSandboxImage(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	removed := filepath.Join(dir, "removed")
	const prevRef = "ghcr.io/bwsalmon/grain/grain:v2"
	writeTestFile(t, refFile, "GRAIN_IMAGE="+prevRef+"\n")

	const pinned = "ghcr.io/bwsalmon/grain/granule-sandbox:sha-pinned"

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			RunCmd: []string{"sh", "-c",
				`case "$1" in
					*v3*) echo ghcr.io/bwsalmon/grain/granule-sandbox:sha-v3;;
					*v2*) echo ghcr.io/bwsalmon/grain/granule-sandbox:sha-v2;;
					*) echo 7;;
				esac`,
				"sh"},
			SandboxImageArgs: []string{"sandbox-image"},
			SandboxImagePin:  pinned,
			ListCmd: []string{"sh", "-c",
				`case "$1" in
					*granule-sandbox*)
						echo ghcr.io/bwsalmon/grain/granule-sandbox:sha-v3
						echo ghcr.io/bwsalmon/grain/granule-sandbox:sha-v2
						echo ` + pinned + `
						echo ghcr.io/bwsalmon/grain/granule-sandbox:sha-old
						;;
				esac`,
				"sh"},
			RemoveCmd: []string{"sh", "-c", `printf '%s\n' "$*" >> ` + removed + `; exit 0`, "sh"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("v3"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, u, PhaseOK)

	got := readFile(t, removed)
	if !strings.Contains(got, "ghcr.io/bwsalmon/grain/granule-sandbox:sha-old") {
		t.Errorf("removed images %q does not contain sha-old", got)
	}
	if strings.Contains(got, pinned) {
		t.Errorf("removed images %q should not have removed pinned image %q", got, pinned)
	}
}

// TestUpgraderImageToleratesCleanupFailures verifies that errors when listing or removing
// images do not fail the upgrade.
func TestUpgraderImageToleratesCleanupFailures(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	writeTestFile(t, refFile, "GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:v2\n")

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			RunCmd:     []string{"sh", "-c", "echo 7"},
			ListCmd:    []string{"sh", "-c", "echo ghcr.io/bwsalmon/grain/grain:v1"},
			RemoveCmd:  []string{"sh", "-c", "echo 'image is in use' >&2; exit 1", "sh"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("v3"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, u, PhaseOK)

	if got, want := readFile(t, refFile), "GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:v3@"+testDigest+"\n"; got != want {
		t.Errorf("ref file = %q, want %q", got, want)
	}
}

// The tag CI publishes for a branch moves on every push, so an upgrade
// that checked the tag and then recorded the tag would restart onto
// whatever it named by then (grain/task-1492). What is checked and what
// is recorded is the digest the pull actually fetched -- the one for the
// configured repository, when the image is known by others too.
func TestUpgraderImagePinsTheDigestItPulled(t *testing.T) {
	dir := t.TempDir()
	ran := filepath.Join(dir, "ran")
	refFile := filepath.Join(dir, "image.env")
	const want = "sha256:" + "feedfacefeedfacefeedfacefeedfacefeedfacefeedfacefeedfacefeedface"

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "registry.example:5000/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			DigestCmd: []string{"sh", "-c",
				`echo mirror.example/grain@sha256:aaaa
				 echo registry.example:5000/grain@` + want},
			RunCmd: []string{"sh", "-c", `printf '%s\n' "$*" >> ` + ran, "sh"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseOK)

	const pinned = "registry.example:5000/grain:main@" + want
	if got := readFile(t, refFile); got != "GRAIN_IMAGE="+pinned+"\n" {
		t.Errorf("ref file = %q, want it pinned to %s", got, pinned)
	}
	if got := readFile(t, ran); got != pinned+" schema-version\n" {
		t.Errorf("health check ran %q, want it run against %s", got, pinned)
	}
	if detail := stepDetail(t, status, stepPull); !strings.Contains(detail, pinned) {
		t.Errorf("%s step = %q, want it to name the tag and the digest it resolved to", stepPull, detail)
	}
}

// An image with no digest for the repository it was pulled from is not
// one this upgrade can pin, so it stops before the health check with the
// ref file untouched rather than falling back to recording the tag.
func TestUpgraderImageRefusesAnImageItCannotPin(t *testing.T) {
	dir := t.TempDir()
	ran := filepath.Join(dir, "ran")
	refFile := filepath.Join(dir, "image.env")
	const before = "GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:main@" + testDigest + "\n"
	writeTestFile(t, refFile, before)

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			// A locally built image tagged to look like the registry's:
			// no RepoDigests at all.
			DigestCmd: []string{"sh", "-c", "echo"},
			RunCmd:    []string{"sh", "-c", `printf '%s\n' "$*" >> ` + ran, "sh"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseFailed)
	if !strings.Contains(status.Detail, "no digest") {
		t.Errorf("status detail = %q, want it to say the pulled image has no digest", status.Detail)
	}
	if got := readFile(t, refFile); got != before {
		t.Errorf("ref file = %q, want it left at %q", got, before)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Errorf("an image that could not be pinned was run anyway: %q", readFile(t, ran))
	}
}

// The previous image is kept by its digest. Its tag has usually moved
// on by the time the cleanup runs -- the same branch tag now names the
// new image, or another branch's tag names the old one -- so matching
// the recorded ref's tag would keep the wrong thing.
func TestUpgraderImageCleanupKeepsThePinnedPreviousImageByDigest(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	removed := filepath.Join(dir, "removed")
	const repo = "ghcr.io/bwsalmon/grain/grain"
	const old = "sha256:" + "0000000000000000000000000000000000000000000000000000000000000000"
	writeTestFile(t, refFile, "GRAIN_IMAGE="+repo+":v2@"+old+"\n")

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: repo,
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			RunCmd:     []string{"sh", "-c", "echo 7"},
			// The default ListCmd's shape: `repo:tag repo@digest`.
			ListCmd: []string{"sh", "-c",
				`echo ` + repo + `:main ` + repo + `@` + testDigest + `
				 echo ` + repo + `:release ` + repo + `@` + old + `
				 echo ` + repo + `:v1 ` + repo + `@sha256:1111
				 echo '` + repo + `:<none> ` + repo + `@<none>'`,
				"sh"},
			RemoveCmd: []string{"sh", "-c", `printf '%s\n' "$*" >> ` + removed + `; exit 0`, "sh"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, u, PhaseOK)

	if got, want := readFile(t, removed), repo+":v1\n"; got != want {
		t.Errorf("removed images = %q, want only %q", got, want)
	}
}

func TestUpgraderImageCleanupRemovesUntaggedDigestsAndPreservesPreviousTag(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	removed := filepath.Join(dir, "removed")
	const (
		grainRepo = "ghcr.io/bwsalmon/grain/grain"
		guestRepo = "ghcr.io/bwsalmon/grain/guest"
		prevGrain = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
		pinnedRef = guestRepo + ":sha-pinned"
	)
	writeTestFile(t, refFile, "GRAIN_IMAGE="+grainRepo+":main@"+prevGrain+"\n")

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: grainRepo,
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			RunCmd: []string{"sh", "-c",
				`case "$1 $2" in
					*` + testDigest + `*sandbox-image*) echo ` + guestRepo + `:sha-new;;
					*` + prevGrain + `*sandbox-image*) echo ` + guestRepo + `:sha-prev;;
					*) echo 7;;
				esac`,
				"sh"},
			SandboxImageArgs: []string{"sandbox-image"},
			SandboxImagePin:  pinnedRef,
			ListCmd: []string{"sh", "-c",
				`case "$1" in
					` + grainRepo + `)
						echo '` + grainRepo + `:main ` + grainRepo + `@` + testDigest + `'
						echo '` + grainRepo + `:previous ` + grainRepo + `@sha256:prevtagdigest'
						echo '` + grainRepo + `:<none> ` + grainRepo + `@` + prevGrain + `'
						echo '` + grainRepo + `:<none> ` + grainRepo + `@sha256:staleuntagged'
						echo '` + grainRepo + `:sha-old ` + grainRepo + `@sha256:oldtagged'
						;;
					` + guestRepo + `)
						echo '` + guestRepo + `:sha-new ` + guestRepo + `@sha256:newguest'
						echo '` + guestRepo + `:sha-prev ` + guestRepo + `@sha256:prevguest'
						echo '` + guestRepo + `:previous ` + guestRepo + `@sha256:guestprevtag'
						echo '` + pinnedRef + ` ` + guestRepo + `@sha256:pinnedguest'
						echo '` + guestRepo + `:sha-stale ` + guestRepo + `@sha256:staleguest'
						echo '` + guestRepo + `:inputs-123 ` + guestRepo + `@sha256:inputsguest'
						echo '` + guestRepo + `:<none> ` + guestRepo + `@sha256:untaggedguest'
						;;
				esac`,
				"sh"},
			RemoveCmd: []string{"sh", "-c", `printf '%s\n' "$*" >> ` + removed + `; exit 0`, "sh"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, u, PhaseOK)

	got := readFile(t, removed)
	for _, want := range []string{
		grainRepo + "@sha256:staleuntagged",
		grainRepo + ":sha-old",
		guestRepo + ":sha-stale",
		guestRepo + ":inputs-123",
		guestRepo + "@sha256:untaggedguest",
	} {
		if !strings.Contains(got, want+"\n") {
			t.Errorf("removed images = %q, missing expected %q", got, want)
		}
	}
	for _, kept := range []string{
		grainRepo + ":main",
		grainRepo + "@" + testDigest,
		grainRepo + ":previous",
		grainRepo + "@" + prevGrain,
		guestRepo + ":sha-new",
		guestRepo + ":sha-prev",
		guestRepo + ":previous",
		pinnedRef,
	} {
		if strings.Contains(got, kept) {
			t.Errorf("removed images = %q, unexpectedly removed kept image %q", got, kept)
		}
	}
}

func TestRepositoryOf(t *testing.T) {
	for ref, want := range map[string]string{
		"ghcr.io/bwsalmon/grain/grain":                  "ghcr.io/bwsalmon/grain/grain",
		"ghcr.io/bwsalmon/grain/grain:main":             "ghcr.io/bwsalmon/grain/grain",
		"ghcr.io/bwsalmon/grain/grain@sha256:abcd":      "ghcr.io/bwsalmon/grain/grain",
		"ghcr.io/bwsalmon/grain/grain:main@sha256:abcd": "ghcr.io/bwsalmon/grain/grain",
		"localhost:5000/grain":                          "localhost:5000/grain",
		"localhost:5000/grain:main@sha256:abcd":         "localhost:5000/grain",
	} {
		if got := repositoryOf(ref); got != want {
			t.Errorf("repositoryOf(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestUpgraderImageTransitionsAcrossRepositoriesAndStaysOnSuccessor(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	removed := filepath.Join(dir, "removed")
	const legacyRepo = installtrack.LegacyGrainRepository
	const trackRepo = installtracktest.Repository
	writeTestFile(t, refFile, "GRAIN_IMAGE="+legacyRepo+":v2.0.0\n")

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository:       legacyRepo,
			TargetRepository: trackRepo,
			RefFile:          refFile,
			PullCmd:          []string{"true"},
			RunCmd:           []string{"sh", "-c", "echo 7"},
			DigestCmd:        []string{"sh", "-c", `echo "${1%:*}@` + testDigest + `"`, "sh"},
			ListCmd: []string{"sh", "-c",
				`case "$1" in
					` + trackRepo + `)
						echo ` + trackRepo + `:main
						echo ` + trackRepo + `:v1.9.0
						;;
					*)
						echo ` + legacyRepo + `:v2.0.0
						echo ` + legacyRepo + `:v1.8.0
						;;
				esac`,
				"sh"},
			RemoveCmd: []string{"sh", "-c", `printf '%s\n' "$*" >> ` + removed + `; exit 0`, "sh"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, u, PhaseOK)

	wantRef := "GRAIN_IMAGE=" + trackRepo + ":main@" + testDigest + "\n"
	if got := readFile(t, refFile); got != wantRef {
		t.Fatalf("refFile = %q, want %q", got, wantRef)
	}

	// Older images in both the legacy and new repositories are cleaned up, while
	// the immediately previous legacy version (v2.0.0) and new track version (main) are kept.
	gotRemoved := readFile(t, removed)
	for _, want := range []string{legacyRepo + ":v1.8.0", trackRepo + ":v1.9.0"} {
		if !strings.Contains(gotRemoved, want) {
			t.Errorf("removed images = %q, want %q removed", gotRemoved, want)
		}
	}
	for _, keep := range []string{legacyRepo + ":v2.0.0", trackRepo + ":main"} {
		if strings.Contains(gotRemoved, keep) {
			t.Errorf("removed images = %q, unexpectedly removed kept image %q", gotRemoved, keep)
		}
	}

	// After restart, even if the systemd unit still passes -upgrade-image with
	// LegacyGrainRepository and no TargetRepository, the upgrader sees the shifted
	// repository in RefFile and stays on trackRepo.
	uAfterRestart := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: legacyRepo,
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			RunCmd:     []string{"sh", "-c", "echo 7"},
			DigestCmd:  []string{"sh", "-c", `echo "${1%:*}@` + testDigest + `"`, "sh"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status-2.json"),
	})
	if got := uAfterRestart.EffectiveRepository(); got != trackRepo {
		t.Fatalf("EffectiveRepository() after restart = %q, want %q", got, trackRepo)
	}

	// Rejecting untrusted repository transition via SetTargetRepository.
	if err := uAfterRestart.SetTargetRepository("evil.example/grain"); err == nil {
		t.Error("SetTargetRepository(evil.example/grain) succeeded, want error")
	}
}

// Before the split there was one daemon image, grain/grain, and it was an
// edition's build; after it, grain/grain is the core build and the
// edition's build has a repository of its own. A deployment detected as
// on that edition's track that still names grain/grain -- on
// -upgrade-image and in its image-ref file -- upgrades onto the track's
// repository, the build it was already running, and not onto the core
// build that drops its edition's features. An OSS-track one stays on
// grain/grain and gets the core build (grain/task-4944).
func TestUpgraderImageMovesARegisteredTrackLegacyDeploymentOntoItsRepository(t *testing.T) {
	const legacyRepo = installtrack.LegacyGrainRepository
	const movedRepo = installtracktest.MovedRepository
	const trackRepo = installtracktest.Repository
	for _, c := range []struct {
		name     string
		fromRepo string
		track    installtrack.Track
		wantRepo string
	}{
		{"track-from-legacy", legacyRepo, installtracktest.Track, trackRepo},
		{"track-from-moved", movedRepo, installtracktest.Track, trackRepo},
		{"oss-from-legacy", legacyRepo, installtrack.TrackOSS, legacyRepo},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			refFile := filepath.Join(dir, "image.env")
			pulled := filepath.Join(dir, "pulled")
			writeTestFile(t, refFile, "GRAIN_IMAGE="+c.fromRepo+":main\n")
			u := newUpgrader(t, Config{
				Image: &ImageConfig{
					Repository: c.fromRepo,
					Track:      c.track,
					RefFile:    refFile,
					PullCmd:    []string{"sh", "-c", `printf '%s\n' "$1" >> ` + pulled, "sh"},
					RunCmd:     []string{"sh", "-c", "echo 7"},
					DigestCmd:  []string{"sh", "-c", `echo "${1%:*}@` + testDigest + `"`, "sh"},
				},
				HealthCheckArgs: []string{"schema-version"},
				StatusFile:      filepath.Join(dir, "upgrade-status.json"),
			})
			if got := u.EffectiveRepository(); got != c.wantRepo {
				t.Errorf("EffectiveRepository() = %q, want %q", got, c.wantRepo)
			}
			if err := u.Start("main"); err != nil {
				t.Fatalf("Start: %v", err)
			}
			waitForPhase(t, u, PhaseOK)
			if got, want := readFile(t, pulled), c.wantRepo+":main\n"; got != want {
				t.Errorf("pulled %q, want %q", got, want)
			}
			if got, want := readFile(t, refFile), "GRAIN_IMAGE="+c.wantRepo+":main@"+testDigest+"\n"; got != want {
				t.Errorf("refFile = %q, want %q", got, want)
			}
			// And the next upgrade stays where this one went.
			if got := u.EffectiveRepository(); got != c.wantRepo {
				t.Errorf("after the upgrade EffectiveRepository() = %q, want %q", got, c.wantRepo)
			}
		})
	}
}

func TestUpgraderImageDoesNotUpgradeWhenVersionHasNotChanged(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	alreadyRunning := "ghcr.io/bwsalmon/grain/grain:main@" + testDigest
	writeTestFile(t, refFile, "GRAIN_IMAGE="+alreadyRunning+"\n")

	pulled := filepath.Join(dir, "pulled")
	checked := filepath.Join(dir, "checked")
	restarted := filepath.Join(dir, "restarted")
	var held, released bool

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"sh", "-c", `printf '%s\n' "$1" >> ` + pulled, "sh"},
			RunCmd:     []string{"sh", "-c", `printf '%s\n' "$1" >> ` + checked, "sh"},
			DigestCmd:  []string{"sh", "-c", `echo "${1%:*}@` + testDigest + `"`, "sh"},
		},
		HealthCheckArgs: []string{"schema-version"},
		RestartCmd:      []string{"sh", "-c", "touch " + restarted},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
		Hold: func(string) (func(), error) {
			held = true
			return func() { released = true }, nil
		},
	})

	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseOK)
	if !status.Unchanged {
		t.Errorf("status.Unchanged = false, want true (status=%+v)", status)
	}
	if !strings.Contains(status.Detail, "the version on main has not changed") {
		t.Errorf("status.Detail = %q, want it to say the version on main has not changed", status.Detail)
	}
	if strings.Contains(status.Detail, "restarting") {
		t.Errorf("status.Detail = %q, want no restart reported when the version has not changed", status.Detail)
	}
	if _, ok := status.RestartRequestedAt(); ok {
		t.Error("RestartRequestedAt() = true on an unchanged upgrade")
	}
	if _, err := os.Stat(checked); !os.IsNotExist(err) {
		t.Errorf("health check ran despite the image digest not having changed (err=%v)", err)
	}
	if _, err := os.Stat(restarted); !os.IsNotExist(err) {
		t.Errorf("restart command ran despite the image digest not having changed (err=%v)", err)
	}
	if !held || !released {
		t.Errorf("hold: held=%v released=%v, want the queue hold taken and immediately released", held, released)
	}
	for _, step := range status.Steps {
		if step.Name == stepPull {
			if step.State != StepDone {
				t.Errorf("%s state = %q, want %q", step.Name, step.State, StepDone)
			}
			continue
		}
		if step.State != StepSkipped {
			t.Errorf("%s state = %q, want %q when version has not changed", step.Name, step.State, StepSkipped)
		}
	}
}

func TestRemovableImage(t *testing.T) {
	const repo = "ghcr.io/bwsalmon/grain/grain"
	for _, c := range []struct {
		names []string
		want  string
	}{
		{[]string{repo + ":v1", repo + "@sha256:1111"}, repo + ":v1"},
		{[]string{repo + ":<none>", repo + "@sha256:1111"}, repo + "@sha256:1111"},
		{[]string{repo + ":<none>", repo + "@<none>"}, ""},
		// An empty digest is no reference at all: `docker image rm repo@`
		// is an invalid reference format. cleanOldImages falls back to the ID.
		{[]string{repo + ":<none>", repo + "@"}, ""},
		{[]string{"other/repo:v1"}, ""},
	} {
		if got := removableImage(repo, c.names); got != c.want {
			t.Errorf("removableImage(%q) = %q, want %q", c.names, got, c.want)
		}
	}
}

func TestSameImageID(t *testing.T) {
	const full = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0123456789ab", full, true},
		{full, "0123456789ab", true},
		{full, full, true},
		{"0123456789ac", full, false},
		{"0123", full, false},
	} {
		if got := sameImageID(c.a, c.b); got != c.want {
			t.Errorf("sameImageID(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// An image docker lists with neither a tag nor a digest -- `repo:<none>
// repo@`, an empty {{.Digest}} -- is removed by its ID rather than by the
// `repo@` that is no reference at all. But never when that ID is also a
// kept image's, listed again under its kept tag, or a running container's.
func TestUpgraderImageCleanupRemovesEmptyDigestImagesByIDButNeverKeptOnes(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	removed := filepath.Join(dir, "removed")
	const (
		repo        = "ghcr.io/bwsalmon/grain/grain"
		prevID      = "aaaaaaaaaaaa" // the rollback image, kept by its tag
		staleID     = "bbbbbbbbbbbb"
		runningID   = "cccccccccccc"
		runningFull = "sha256:cccccccccccc0000000000000000000000000000000000000000000000000000"
	)
	writeTestFile(t, refFile, "GRAIN_IMAGE="+repo+":v2\n")

	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: repo,
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			RunCmd:     []string{"sh", "-c", "echo 7"},
			ListCmd: []string{"sh", "-c",
				`echo '` + repo + `:v3 ` + repo + `@` + testDigest + ` dddddddddddd'
				 echo '` + repo + `:<none> ` + repo + `@ ` + prevID + `'
				 echo '` + repo + `:v2 ` + repo + `@ ` + prevID + `'
				 echo '` + repo + `:<none> ` + repo + `@ ` + staleID + `'
				 echo '` + repo + `:<none> ` + repo + `@<none> ` + staleID + `'
				 echo '` + repo + `:<none> ` + repo + `@ ` + runningID + `'`,
				"sh"},
			ContainerImagesCmd: []string{"echo", runningFull},
			RemoveCmd:          []string{"sh", "-c", `printf '%s\n' "$*" >> ` + removed + `; exit 0`, "sh"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})

	if err := u.Start("v3"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, u, PhaseOK)

	if got, want := readFile(t, removed), staleID+"\n"; got != want {
		t.Errorf("removed images = %q, want only %q, once", got, want)
	}
}
