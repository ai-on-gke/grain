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

// Package upgrade drives an in-place upgrade of a running grain deployment:
// given a branch name, it fetches and checks out that branch in a git
// checkout already on disk, rebuilds bin/grain (Makefile's
// containerised `make container-build`, so nothing about the Go/Node
// toolchain the build needs has to be installed on the host beyond
// docker), installs the result, and -- if the deployment configured one
// -- runs a restart command to bring the new binary up.
//
// bwsalmon/agents#396 (filed "For v2"): "the UI should allow you to
// target a specific branch. When the upgrade button is pushed it should
// download the newest code from the branch, build it locally and start
// running the new version. For now this can restart the host if
// needed." This is deliberately the naive version that issue asked for:
// one upgrade at a time -- a failed build or a failed install leaves
// the old binary running untouched (Start never reaches RestartCmd
// unless every earlier step succeeded).
//
// bwsalmon/agents#418/#422: a build that succeeds can still be broken
// at runtime (a panic during init, a missing dynamic dependency, a
// binary that just exits immediately), and that used to be entirely
// invisible here -- Status would report "ok" forever with no trace of
// it. If Config.HealthCheckArgs is set, Start now runs the newly
// installed binary itself (InstallPath, not BuiltBinary) with those
// args right after install and before RestartCmd; a nonzero exit or a
// timeout there rolls the install back to whatever binary was at
// InstallPath before (or removes it, if this was the very first
// install) and reports PhaseFailed instead of cutting over to
// RestartCmd at all. This still cannot catch a binary that starts up
// fine but misbehaves only once it is actually serving real traffic --
// that class of failure needs a real health check against the running
// service, which is out of this package's reach (see HealthCheckArgs's
// own doc comment).
//
// bwsalmon/agents#633: checkout and build both used to run under
// context.Background(), so a git fetch against a stalled connection or a
// docker build stuck on an unresponsive registry never returned at all --
// Status sat on PhaseRunning forever, and nothing short of restarting
// the whole daemon process released the Upgrader to let a retry through.
// Config.Timeout (defaultTimeout, 45 minutes, if left unset) now bounds
// every step of an upgrade's staging, and restartTimeout the restart
// that follows it -- so no step of either pipeline can wedge a
// deployment by simply never finishing.
package upgrade

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ai-on-gke/grain/pkg/version"
)

// Phase is where an upgrade (or the deployment's last one) stands.
type Phase string

const (
	PhaseRunning Phase = "running"
	PhaseOK      Phase = "ok"
	PhaseFailed  Phase = "failed"
)

// Mode is which of this package's two pipelines an Upgrader is
// configured for -- see stage, which is where the choice is actually
// made. It exists so a caller that has to *describe* an upgrade before
// anyone triggers one (pkg/ui's own upgrade pane, whose copy used to
// promise a checkout and a build to every deployment including the ones
// that have neither) can say which of the two it is about to run,
// rather than guessing from Config fields it cannot see.
type Mode string

const (
	// ModeImage pulls the image CI published for the branch, health-
	// checks it, and repoints the deployment's ref file at it
	// (image.go).
	ModeImage Mode = "image"
	// ModeSource fetches the branch into a checkout on this host,
	// builds it, and installs the binary over the running one
	// (stageBinary).
	ModeSource Mode = "source"
	// ModeKube health-checks the image CI published for the branch by
	// running it as a throwaway pod, then repoints this deployment's own
	// Deployment at it and lets Kubernetes roll it out (kube.go).
	ModeKube Mode = "kube"
)

// Status is what Upgrader.Status reports: the deployment's last (or
// current) upgrade attempt. A zero Status (Phase "") means none has ever
// run against this StatusFile.
type Status struct {
	Branch string `json:"branch"`
	Phase  Phase  `json:"phase"`
	Detail string `json:"detail"`
	// Unchanged is true when an upgrade finished without cutting over or
	// restarting because the version on Branch was still the one this
	// deployment already ran.
	Unchanged bool `json:"unchanged,omitempty"`
	// Steps is the whole sequence this upgrade runs, in order, each one
	// carrying where it stands and what it last had to say (progress.go,
	// grain/task-37). It is written the moment an upgrade starts, with
	// every step still pending, so a pane polling GET /api/upgrade can
	// show "step 2 of 5: build" and the build's own latest output line
	// rather than an unqualified "running" for however many minutes the
	// whole thing takes. Empty on a Status written by an older grain,
	// which is why the pane treats it as optional.
	Steps      []Step     `json:"steps,omitempty"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	// Process identifies the process running this upgrade (processID),
	// so a later one reading "running" can tell a live upgrade from one
	// a restart cut short -- see Upgrader.Status.
	Process string `json:"process,omitempty"`
}

// Running is where a deployment already is, which is a different
// question from Status's "how did the last attempt to move it go"
// (grain/task-50). An operator about to upgrade onto a branch is
// deciding between what they have and what they would get, and Status
// answers only the second half: on a deployment whose last cut-over came
// from scripts/setup.sh rather than from the button, it answers nothing
// at all.
//
// Both fields are read from the deployment's own state on disk rather
// than from the process answering, so what they describe is precisely
// what a restart brings up. That is the same thing as what is running
// except in one window -- an upgrade that has repointed the ref file (or
// installed the binary) and not yet restarted -- which lasts seconds and
// which the attempt reported beside it explains. What is running *this
// instant* is a different value again, this process's own build stamp,
// and pkg/ui already serves that on GET /api/config for the sidebar.
type Running struct {
	// Ref is the image reference ImageConfig.RefFile names, which is
	// literally the tag grain-daemon.service starts (image.go's
	// readImageRef). Empty on ModeSource, which has no such file.
	Ref string
	// Build is the VCS stamp of the binary at Config.InstallPath -- the
	// commit ModeSource's own last install put there, read out of the
	// file rather than by running it. Zero on ModeImage, and on a binary
	// built without a stamp.
	Build version.Info
}

// Config is what an Upgrader needs to know to actually perform one.
type Config struct {
	// SrcDir is a git checkout of bwsalmon/grain already on disk --
	// scripts/setup.sh's own GRAIN_SRC_DIR -- that Start fetches,
	// checks out, and builds in place.
	SrcDir string
	// BuildCmd is run with SrcDir as its working directory to produce
	// a new binary -- []string{"make",
	// "container-build"} on a real deployment (Makefile's
	// containerised build). Overridable so a test can swap in something
	// that runs in milliseconds and needs no docker daemon.
	BuildCmd []string
	// BuiltBinary is where BuildCmd leaves the binary it built --
	// filepath.Join(SrcDir, "bin", "grain") on a real deployment.
	BuiltBinary string
	// InstallPath is where that binary is copied to run from. Start
	// writes it atomically (a temp file in the same directory, then
	// rename) so a reader (or a service restarting mid-copy) never sees
	// a partial binary.
	InstallPath string
	// HealthCheckArgs, if non-empty, are run as exec.Command(InstallPath,
	// HealthCheckArgs...) right after install and before RestartCmd --
	// []string{"schema-version"} on a real deployment: a subcommand that
	// touches no store, needs no config, and exits immediately, so
	// failing to even run it means the newly built binary is broken
	// outright (a panic in an init(), a missing dynamic library, a
	// build that produced something that isn't a working executable at
	// all) rather than that its actual serving behavior is somehow
	// wrong -- that narrower kind of failure only shows up once the
	// binary is actually handling real traffic, which is beyond what a
	// short-lived subprocess here can check. A nonzero exit or a
	// 10-second timeout rolls the install back (see install's own doc
	// comment) and reports PhaseFailed without ever running RestartCmd.
	// Empty skips the check and installs unconditionally -- the
	// previous behavior, and what every test with no real "grain"
	// binary of its own relies on.
	HealthCheckArgs []string
	// RestartCmd, given, is run after a successful build and install to
	// bring the new binary up -- []string{"sudo", "systemctl", "restart",
	// "grain-daemon.service"} on a real deployment
	// (scripts/setup.sh's own unit; see that script for the narrow
	// sudoers grant this needs). Nil skips the restart step entirely:
	// the build and install still happen, but nothing brings the new
	// binary up on its own -- useful for a deployment with no restart
	// mechanism of its own, and for tests.
	RestartCmd []string
	// BeforeRestart, when set, is called once just before RestartCmd
	// runs, with the branch being cut over to. It is how the daemon
	// writes down that the restart about to happen is one it asked for
	// (pkg/daemon's restart requests), so that the next start does not
	// count it towards a crash loop. It must not block: nothing after it
	// waits on it except the restart itself.
	BeforeRestart func(branch string)
	// Timeout bounds an upgrade's staging -- every step of whichever
	// pipeline is configured, up to and including the schema step, but
	// not the restart that follows it (restartTimeout, which has its own
	// reasons). The steps that need the bound are the ones that touch the
	// network or a container runtime: a git fetch against origin and a
	// cold docker build on the source path, an image pull on the
	// container one, any of which can, on a bad day, simply stop rather
	// than return an error -- a stalled TCP connection, an unresponsive
	// registry, a wedged Docker daemon. None of them used to be bounded
	// at all -- run's own ctx was context.Background() -- so that failure
	// mode did not fail: Status sat on PhaseRunning forever, the
	// Upgrader was never released, the UI's Upgrade button stayed on
	// "Upgrading…" indefinitely, and a second click just got
	// ErrUpgradeInProgress (bwsalmon/agents#633, "v2 Deploys are
	// hanging"). Zero uses defaultTimeout -- the same 45 minutes
	// terraform/gcp/files/config-sync.sh's own DEPLOY_TIMEOUT_SECS
	// budgets for this identical cold-build cost on the other path to
	// this same build, for the same reason.
	//
	// The short-lived steps take a narrower bound of their own on top of
	// this one, since a health check or a schema question that has not
	// answered in a couple of minutes is not going to (imageRunTimeout,
	// schemaCheckTimeout).
	Timeout time.Duration
	// StatusFile persists Status across the very restart RestartCmd
	// triggers -- an in-memory field would be lost the moment the new
	// binary's own process replaces this one. Required.
	StatusFile string
	// Image, when non-nil, replaces the checkout/build/install steps
	// with pulling the image CI published for the branch and pointing
	// the deployment at it -- what "upgrade" means on a host that runs
	// grain from a container rather than from a binary it built itself
	// (image.go, bwsalmon/agents#645). SrcDir, BuildCmd, BuiltBinary and
	// InstallPath are unused when it is set; every other field means
	// exactly what it does otherwise.
	Image *ImageConfig
	// Kube, when non-nil, replaces both of the above with a health-check
	// of the image CI published for the branch and a repoint of this
	// deployment's own Kubernetes Deployment at it (kube.go,
	// grain/task-358). SrcDir, BuildCmd, BuiltBinary, InstallPath and
	// Image are unused when it is set, and RestartCmd should be left nil
	// -- the rollout is the restart. It takes precedence over Image,
	// which a Kubernetes deployment also has a repository for.
	Kube *KubeConfig
	// Refusal, when set, is why this deployment cannot be upgraded from
	// the UI at all, and what to run instead: Start refuses every branch
	// with it. A host-mode install upgraded onto a build without host
	// mode is the case (pkg/daemon's hostModeUpgradeRefusal): its next
	// upgrade is the installer's migration, not another image swap.
	Refusal string
	// AllowedBranches is the only branches Start will upgrade this
	// deployment to, each a path.Match pattern ("main", "2.1", or
	// "release/*" -- a '*' there does not cross a '/'), or AnyBranchPattern
	// ("**") for every well-formed branch. Empty means
	// DefaultAllowedBranches. A well-formed branch name is not a reviewed
	// one: anybody who can push a branch to the repository can give it
	// any name they like, and an upgrade replaces this daemon outright
	// with whatever that branch builds (bwsalmon/grain#2385). So which
	// branches are trusted to become the daemon is the operator's to say
	// on the command line (-upgrade-allowed-branches), not something the
	// request naming the branch gets to decide.
	AllowedBranches []string
	// Hold, when set, is called at the very start of an upgrade to shut
	// this deployment's queue for the duration of it -- the whole point
	// being that an upgrade ends in a restart, and a run dispatched
	// while one is under way is a run that restart discards
	// (orchestrator.UpgradeHold, which is where the pause actually gets
	// written). A hook rather than a store handle, for the reason
	// SchemaConfig's own funcs are hooks: nothing in this package knows
	// what a deployment's rows are, and it should not have to.
	//
	// It returns the release, which run calls on exactly the paths that
	// leave this deployment running the build it already had -- a failed
	// staging, a success with no RestartCmd to bring anything up, a
	// restart that errored, and a restart that succeeded but did not
	// take this process with it. The one path that does not release is
	// the ordinary success: the process that would have to do it is gone
	// by then, so the release belongs to whatever comes up next
	// (orchestrator.ReleaseUpgradeHold, a startup pass). A nil release --
	// which is what a Hold that decided to take nothing hands back --
	// is simply not called.
	//
	// A Hold that fails is logged into the upgrade's own progress log
	// and the upgrade runs anyway: an operator asking for an upgrade did
	// not ask for a pause, and refusing to upgrade because a gate would
	// not close is the worse failure.
	Hold func(branch string) (release func(), err error)
	// Flush, when set, is step 3 of docs/non-disruptive-upgrade.md's
	// upgrade protocol: called once the new build is staged and just
	// before RestartCmd, it stops this daemon tailing the runs it drives
	// in run hosts, persists each one's spool cursor and stops writing
	// the store for them, so the next build adopts them where they stand
	// (orchestrator.RecoverRunHosts). The run hosts are not signalled.
	// It is bounded by flushTimeout, because it waits on the daemon's own
	// state reaching disk and never on the work.
	//
	// It returns resume, which run calls if the restart does not take
	// this process with it: the daemon carries on, and it adopts back
	// the runs it let go of. A Flush that fails is logged and the restart
	// goes ahead: the daemon's own stop detaches whatever it missed
	// (runhost.Host.Detach). On the Kubernetes path, whose restart has
	// no command and begins with the Deployment's repointing, it is
	// called inside staging instead, just before the rows are exported
	// and the Deployment is repointed (stageKube), and resume is called
	// if either fails. It is skipped there when the Deployment already
	// runs the image, because then nothing rolls out and no new pod
	// would ever adopt the runs.
	Flush func(ctx context.Context) (resume func(), err error)
	// Stopping, when set, is closed the moment this process starts
	// shutting down (pkg/daemon's run ctx.Done()). It tells a restart that
	// worked from one that did not. A restart that only asks for a
	// restart -- the container install's `touch .../control/restart`,
	// which a systemd path unit turns into a stop of this very container
	// -- comes back at once, and the stop that follows can take minutes
	// to drain (TimeoutStopSec=330). Without this, run saw this process
	// still alive restartGrace later and reopened the queue and resumed
	// the flushed runs in a daemon that was already tearing down its git
	// proxy and Actions cache (task 1504). Once it is closed, the hold and
	// the flushed runs belong to the next startup, as they do after any
	// other successful restart. Left nil, only restartGrace decides.
	Stopping <-chan struct{}
	// Schema, when set, makes an upgrade notice that the build it is
	// cutting over to knows a different database schema than the one
	// running, and carry this deployment's rows across rather than
	// leaving it on a store the new build refuses to open (schema.go,
	// grain/task-59). It applies to both pipelines. Left nil, an upgrade
	// does what it always did: nothing about the schema at all.
	Schema *SchemaConfig
	// ListTags lists an image repository's tags, for the released
	// versions Targets offers (targets.go). ListRegistryTags when left
	// nil; a test swaps in something that needs no registry.
	ListTags func(ctx context.Context, repository string) ([]string, error)
	// ManifestDigest reads the registry digest ("sha256:...") a tagged
	// image ref names, without pulling it, for AlreadyRunning (probe.go).
	// RegistryManifestDigest when left nil; a test swaps in something
	// that needs no registry.
	ManifestDigest func(ctx context.Context, ref string) (string, error)
	// LsRemote reads the commit origin's branch is at in SrcDir's
	// checkout, without fetching it, for AlreadyRunning on the source
	// pipeline. `git ls-remote` when left nil.
	LsRemote func(ctx context.Context, srcDir, branch string) (string, error)
	// ReleaseNotesArgs asks a target build for the release notes it
	// carries, before an upgrade onto it is started -- {"release-notes",
	// "--json"} on a real deployment, with "--from <running version>"
	// appended (releasenotes.go). Empty means the preview says the
	// notes are not available rather than asking.
	ReleaseNotesArgs []string
	// RunningRelease is the version this deployment runs, whose notes
	// and older a preview leaves out. pkg/version's own stamp when nil;
	// a test names one.
	RunningRelease func() string
	// Edition is the build edition this deployment runs (e.g. "google",
	// or "" / "oss" for core). When empty, Targets uses the running
	// image tag's edition or pkg/version's own Edition.
	Edition string
}

// defaultTimeout is Config.Timeout's fallback when left at its zero
// value -- see that field's own doc comment.
const defaultTimeout = 45 * time.Minute

// waitDelay bounds how long Wait (via CombinedOutput/Output, below)
// waits for a command's own stdio to actually finish once ctx cancels it
// and newCommand's own Cancel func has run. Context cancellation alone --
// even exec.CommandContext's default Cancel, which kills only the direct
// child -- does not unblock a Read against a pipe some surviving
// descendant still holds open, and that is exactly the shape a hung
// `make container-build` takes: make forks docker/npm/go, each of which
// inherits make's own stdout/stderr, so killing make itself leaves
// nothing to make the read end of that pipe see EOF. Verified live
// against `sh -c "sleep 300"` standing in for a wedged build: without
// WaitDelay, CombinedOutput blocked for the full 300 seconds despite a
// 50ms ctx deadline. newCommand's own process-group kill (below) reaches
// every descendant directly and makes this bound almost never the thing
// that actually fires; it exists as the backstop for whatever that kill
// still somehow misses.
const waitDelay = 5 * time.Second

// newCommand builds an *exec.Cmd whose cancellation -- ctx's Timeout
// expiring, most notably -- reliably bounds it even when the command
// forks children of its own that inherit its stdout/stderr pipes, the
// shape make/docker/npm take and exactly what checkout and build run.
// Setpgid puts the whole tree in a process group of its own; Cancel
// kills that group (a negative pid to syscall.Kill) rather than
// exec.CommandContext's own default of the direct child alone, so a
// grandchild cannot simply outlive its parent's death and keep running,
// orphaned, in the background; WaitDelay is waitDelay's own backstop.
func newCommand(ctx context.Context, dir, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = waitDelay
	return cmd
}

// Upgrader runs at most one upgrade at a time against a Config.
type Upgrader struct {
	cfg Config

	mu sync.Mutex
	// done is both "is an upgrade this process started still in flight"
	// and the channel Done hands a caller to wait on: nil when none is,
	// open for as long as one is, and closed and cleared by release as
	// the run goroutine lets go of this deployment. One field rather than
	// a flag beside a channel, so the two cannot disagree about whether
	// an upgrade is running.
	done chan struct{}
	// override is the image StartImage asked the upgrade in flight to
	// pull instead of the branch's tag (override.go); "" for an ordinary
	// one, and reset with done.
	override string

	// tags caches the registry's tag list for Targets.
	tags tagCache

	// notesMu runs one release notes preview at a time, and guards
	// notes, the recent previews by branch (releasenotes.go).
	notesMu sync.Mutex
	notes   map[string]releaseNotesEntry
}

// New builds an Upgrader. It does not validate cfg -- Start reports
// anything wrong the first time it is actually asked to do something,
// the same as most of this package's callers.
func New(cfg Config) *Upgrader {
	return &Upgrader{cfg: cfg}
}

// ErrUpgradeInProgress is what Start returns when an upgrade this same
// process started is still running. It does not detect an upgrade
// another process started -- see Start's own doc comment.
var ErrUpgradeInProgress = errors.New("an upgrade is already in progress")

// branchPattern is deliberately conservative rather than a full port of
// git-check-ref-format(1): letters, digits, '.', '_', '/' and '-',
// starting and ending on an alphanumeric. That is enough to reject
// anything that could be mistaken for a flag by the git/make commands
// Start shells out to (nothing here reaches a shell, so this is a
// clarity guard against a confusing git error, not primarily an
// injection defense) while still accepting every ordinary branch name.
var branchPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._/-]*[A-Za-z0-9])?$`)

func validateBranch(branch string) error {
	if branch == "" {
		return errors.New("branch is required")
	}
	// "..", "//" and "/." are refused by git too, and are refused here
	// for a reason of this package's own: TagForBranch's tag is only one
	// branch's when none of them can occur (#2386).
	if !branchPattern.MatchString(branch) || strings.Contains(branch, "..") ||
		strings.Contains(branch, "//") || strings.Contains(branch, "/.") {
		return fmt.Errorf("branch %q is not a valid git ref name", branch)
	}
	return nil
}

// DefaultAllowedBranches is what Config.AllowedBranches means when it is
// left empty: the names grain's own release process moves, and nothing
// else (grain/task-2011). Every other branch -- a task's grain/task-N, a
// feature branch, a release/* branch -- still fails closed: a deployment
// that follows one says so (-upgrade-allowed-branches, with "**" or its
// own patterns), and one that never thought about it cannot be pointed
// at a branch nobody reviewed.
//
// Why these and not just main. Each carries only code that went through
// a merge queue as a reviewed pull request, moved there by grain's own
// release process rather than by whoever named it:
//
//   - main: the merge queue.
//   - candidate (and its alias rc): a pointer the release train
//     (scripts/release-from-trunk.sh) force-moves onto each vX.Y.0-rc.N
//     release candidate it cuts from an LKG main commit, so it is only
//     ever a main commit whose required suites passed.
//   - latest and vX.Y.Z: tags the train puts on a candidate that passed
//     its staging soak.
//
// What enforces that is not this list. grain's own sandboxes cannot push
// any of these refs (pkg/gitproxy: a run pushes only its own branch,
// unless it holds the team-granted repo#write any-ref grant,
// model.GitPushAnyRefGrant, formerly git-push-any-ref). But on
// GitHub anybody with write access to the repository can push a branch
// or a tag unless a ruleset stops them -- that is as true of main as of
// the rest, and it is GitHub's rulesets, not grain, that keep it out.
// On the image and Kubernetes pipelines the name is a registry tag, not
// a git ref: only the package-publish CI jobs write those (release.yml
// for a merged main or candidate head, promote.yml retagging that
// commit's sha- images as vX.Y.Z and latest), and with
// -upgrade-verify-cmd set the publisher's signature is checked before
// the image runs (verify.go). The source pipeline fetches the git ref
// itself, so there the rulesets are the whole of it.
//
// Narrower than "v*" on purpose: a bare v* also matches any branch a
// writer happens to call v2-rewrite or vendor-bump. This one wants the
// vMAJOR.MINOR.PATCH shape release tags have; like every path.Match
// '*', none of its stars crosses a '/'. It is a shape, not an exact
// semver check: each '*' still matches any other characters, so
// v1.2.3-hotfix or v1-x.2.3 match too.
var DefaultAllowedBranches = []string{"main", "candidate", "rc", "latest", "v[0-9]*.[0-9]*.[0-9]*"}

// ErrBranchNotAllowed is what Start and ValidateBranch return for a
// well-formed branch this deployment was not configured to upgrade to.
var ErrBranchNotAllowed = errors.New("branch is not one this deployment may upgrade to")

// AllowedBranches reports the patterns a branch has to match before
// Start will upgrade to it -- Config.AllowedBranches, or
// DefaultAllowedBranches when that was left empty.
func (u *Upgrader) AllowedBranches() []string {
	if len(u.cfg.AllowedBranches) == 0 {
		return DefaultAllowedBranches
	}
	return u.cfg.AllowedBranches
}

// CheckAllowedBranches reports the first of patterns checkBranch could
// never match a branch against: one path.Match refuses as malformed (an
// unclosed '[', an empty class), which it does not refuse at match time
// so much as quietly never match. AnyBranchPattern and blank entries are
// fine. For an installer to call before it writes -upgrade-allowed-
// branches into a unit, so a typo fails the install rather than leaving
// an allowlist that silently refuses the branch it was written for.
func CheckAllowedBranches(patterns []string) error {
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" || pattern == AnyBranchPattern {
			continue
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf("upgrade allowlist pattern %q is malformed (%v): each must be a path.Match pattern, e.g. main or release/*, or %s for any branch",
				pattern, err, AnyBranchPattern)
		}
	}
	return nil
}

// checkBranch is the whole of the check Start makes of a branch: that it
// is a well-formed ref name, and that it is one this deployment is
// configured to trust (Config.AllowedBranches).
func (u *Upgrader) checkBranch(branch string) error {
	if u.cfg.Refusal != "" {
		return errors.New(u.cfg.Refusal)
	}
	if err := validateBranch(branch); err != nil {
		return err
	}
	allowed := u.AllowedBranches()
	for _, pattern := range allowed {
		pattern = strings.TrimSpace(pattern)
		if pattern == AnyBranchPattern {
			return nil
		}
		if ok, err := path.Match(pattern, branch); err == nil && ok {
			return nil
		}
		if (branch == "rc" && pattern == "candidate") || (branch == "candidate" && pattern == "rc") {
			return nil
		}
	}
	return fmt.Errorf("%w: %q matches none of %s (set with -upgrade-allowed-branches)",
		ErrBranchNotAllowed, branch, strings.Join(allowed, ", "))
}

// ValidBranchName reports whether branch, trimmed, is a well-formed ref
// name -- the format half of ValidateBranch and nothing more. It says
// nothing about whether this deployment may upgrade to that branch;
// only an Upgrader, which knows its AllowedBranches, can say that.
func ValidBranchName(branch string) (string, error) {
	branch = strings.TrimSpace(branch)
	return branch, validateBranch(branch)
}

// ValidateBranch is the same check Start makes, for a caller that is
// writing a branch name down now and will hand it to Start later -- an
// upgrade scheduled to happen once this deployment has finished its work
// (model.ScheduledUpgrade). A name Start would refuse has to be refused
// while somebody is still there to retype it, rather than hours later in
// a log line nobody is reading.
//
// It trims for the same reason Start does: a branch pasted with a
// trailing newline is the branch, and a caller storing what this
// approved should store this.
func (u *Upgrader) ValidateBranch(branch string) (string, error) {
	branch = strings.TrimSpace(branch)
	return branch, u.checkBranch(branch)
}

// Start validates branch and, if no upgrade started by this same
// Upgrader is currently running, kicks one off in the background and
// returns immediately -- Status reports how it's going. It does not
// detect an upgrade a different process (a previous daemon, before a
// restart) left running: that process's own in-memory record of it
// died with it, so a fresh process always allows a new Start. What
// StatusFile survives a restart for is display continuity, not mutual
// exclusion across processes -- see the package doc comment's "one
// upgrade at a time" for the scope that claim is actually made at.
func (u *Upgrader) Start(branch string) error {
	branch = strings.TrimSpace(branch)
	if err := u.checkBranch(branch); err != nil {
		log.Printf("upgrade: refusing an upgrade to %q: %v", branch, err)
		return err
	}

	return u.start(branch, "")
}

// Status reads the deployment's last (or current) upgrade attempt back
// off disk -- not from memory, so it reports correctly even from a
// process that did not start it (the one this same upgrade's own
// RestartCmd brings up, most notably).
//
// A status file that says "running" but was written by some other
// process is one whose upgrade died with the process that ran it -- a
// daemon restarted, a host rebooted, or a pod evicted mid-build -- and
// nothing is ever going to finish it. Left alone, it said "running"
// forever: the pane's button stayed on "Upgrading…" across every
// restart, with no way to retry short of deleting the file by hand. It
// is reported, and recorded, as failed instead (interrupt, below).
//
// "Some other process" rather than "some other Upgrader": an Upgrader
// over the same StatusFile in this same process is looking at a live
// upgrade, and must see it running (Status.Process).
func (u *Upgrader) Status() (Status, error) {
	status, err := readStatus(u.cfg.StatusFile)
	if err != nil || status.Phase != PhaseRunning || status.Process == processID {
		return status, err
	}
	status = interrupt(status, time.Now().UTC())
	if err := writeStatus(u.cfg.StatusFile, status); err != nil {
		// The answer is right whether or not it was persisted; the next
		// read simply works it out again.
		log.Printf("upgrade: %s: recording the interrupted upgrade in %s: %v", status.Branch, u.cfg.StatusFile, err)
	} else {
		log.Printf("upgrade: %s: %s", status.Branch, status.Detail)
	}
	return status, nil
}

// processID tells this process's upgrades apart from a previous one's
// (Status.Process). Random rather than the pid, which a container
// restarts on the same value every time.
var processID = func() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail on any platform grain runs on; the
		// clock is a fallback that still differs across restarts.
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}()

// interruptedDetail is what an interrupted upgrade's Detail says.
const interruptedDetail = "interrupted: grain restarted before this upgrade finished, so it never completed -- start it again to retry"

// interrupt turns a "running" status nobody is running into a failed
// one, failing whichever step it was on the way progress.fail would have.
// Steps it never reached stay pending, the same as on any other failure.
func interrupt(status Status, now time.Time) Status {
	status.Phase = PhaseFailed
	status.Detail = interruptedDetail
	status.FinishedAt = &now
	steps := make([]Step, len(status.Steps))
	copy(steps, status.Steps)
	for i := range steps {
		if steps[i].State == StepRunning {
			steps[i].State = StepFailed
			steps[i].FinishedAt = &now
			steps[i].Detail = "interrupted by a restart"
		}
	}
	status.Steps = steps
	return status
}

// noUpgradeRunning is what Done hands back when nothing is in flight: a
// closed channel, since a caller waiting for this process to be finished
// upgrading has, in that case, already finished waiting.
var noUpgradeRunning = func() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()

// Done answers the one question about an upgrade that Status cannot:
// not "how did the attempt go", which is read back off disk so that any
// process can ask it, but "has the goroutine *this* process started let
// go of this deployment yet". The returned channel is closed once it
// has, and is already closed when no upgrade of this Upgrader's is in
// flight.
//
// The two are not the same moment, and the gap is not a small one. run
// writes PhaseOK before it so much as starts RestartCmd (deliberately --
// see run's own comment), so a status file saying "ok" still has the
// restart step's result and a final flush to come, and a failing restart
// adds restartGrace on top of that. Anything that must not pull the
// ground out from under an upgrade still finishing -- a test about to
// delete the directory the status file lives in, a caller about to
// replace the files a Config names -- wants this rather than a phase.
//
// It is a channel rather than a bool so a caller can block on it without
// polling; a caller that only wants to know whether an upgrade is in
// flight can read it without blocking:
//
//	select {
//	case <-u.Done():
//		// nothing of this process's is running
//	default:
//	}
//
// Note that it says nothing about an upgrade another process started --
// Start's own doc comment for why this whole flag is per-process.
func (u *Upgrader) Done() <-chan struct{} {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.done == nil {
		return noUpgradeRunning
	}
	return u.done
}

// release hands the Upgrader back: it lets the next Start through and
// closes whatever Done gave out for this run. In one place because every
// way out of an upgrade -- a Start that never got going, a failure, a
// success, a timeout -- has to do both, and one that did neither would
// wedge the deployment while one that did only the first would leave a
// waiter on a channel nothing ever closes.
func (u *Upgrader) release() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.done == nil {
		return
	}
	close(u.done)
	u.done = nil
	u.override = ""
}

// Mode reports which pipeline Start would run, without running it --
// see Mode's own doc comment for who needs to know that in advance.
func (u *Upgrader) Mode() Mode {
	// Kube first: a Kubernetes deployment names an image repository too
	// (KubeConfig.Repository), and a caller that set both would mean the
	// cluster rather than a ref file on a host it does not have.
	if u.cfg.Kube != nil {
		return ModeKube
	}
	if u.cfg.Image != nil {
		return ModeImage
	}
	return ModeSource
}

// restarts reports whether this upgrade ends with something replacing
// this process, which is what decides whether the success it records
// says "restarting" and whether the queue hold is this process's to
// release or the next startup's.
//
// Two ways that happens, and only one of them is a command: a configured
// RestartCmd, and a Kubernetes rollout, which is already under way by
// the time the last step returns because repointing the Deployment is
// what starts it.
func (u *Upgrader) restarts() bool {
	return len(u.cfg.RestartCmd) > 0 || u.cfg.Kube != nil
}

// Running reports what this deployment is on now, by whichever of the
// two pipelines' own records says so: the ref file on ModeImage, the
// installed binary's stamp on ModeSource (see Running's doc comment).
//
// The error is for a record that cannot be read -- a ref file that was
// never written, an install path with no binary at it yet -- and means
// "this deployment cannot say", not that anything is wrong with it. A
// caller displaying the answer should show nothing rather than fail: not
// knowing where a deployment came from is no reason to stop it upgrading
// (pkg/ui's own handler does exactly that).
func (u *Upgrader) Running() (Running, error) {
	if u.Mode() == ModeKube {
		// The cluster's own record rather than one grain keeps: the
		// Deployment's spec *is* what this deployment runs, so there is
		// no ref file here and nothing that could disagree with it.
		ref, err := u.cfg.Kube.client().DeploymentImage(context.Background(), u.cfg.Kube.Deployment, u.cfg.Kube.container())
		if err != nil {
			return Running{}, err
		}
		return Running{Ref: ref}, nil
	}
	if u.Mode() == ModeImage {
		ref, err := u.readImageRef()
		if err != nil {
			return Running{}, fmt.Errorf("reading the image this deployment runs: %w", err)
		}
		return Running{Ref: ref}, nil
	}
	if u.cfg.InstallPath == "" {
		return Running{}, errors.New("no install path configured")
	}
	info, err := version.ReadFile(u.cfg.InstallPath)
	if err != nil {
		return Running{}, fmt.Errorf("reading the build stamp of %s: %w", u.cfg.InstallPath, err)
	}
	return Running{Build: info}, nil
}

// run performs the actual checkout/build/install/restart sequence.
// Every step short of RestartCmd itself failing stops here and records
// PhaseFailed with that step's error as Detail; RestartCmd, if it
// succeeds, is expected to end this process before run ever returns.
func (u *Upgrader) run(branch string, status Status) {
	// Registered first, so it runs last: everything below it -- the
	// progress's own stop, which waits for the flush loop to be gone --
	// has finished by the time Done's channel closes, which is what makes
	// that channel mean "this upgrade has stopped touching the disk"
	// rather than merely "it stopped working".
	defer u.release()

	timeout := u.cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	p := newProgress(u.cfg.StatusFile, branch, status, u.plan())
	defer p.stop()
	p.logf("starting: %s (%s to do it in)", u.description(), shortDuration(timeout))

	// Before the first step, and released on every path below that ends
	// with this deployment still running the build it already had -- see
	// Config.Hold, and hold's own comment for why this is never nil.
	release := u.hold(p, branch)

	detail, err := u.stage(ctx, p, branch)
	if err != nil {
		release()
		p.fail(err)
		return
	}
	if p.isUnchanged() {
		release()
		p.unchanged(detail)
		return
	}

	restarting := u.restarts()
	if len(u.cfg.RestartCmd) == 0 {
		p.succeed(detail, restarting)
		if !restarting {
			// Nothing is coming up to release the hold on the far side
			// of a restart, because there is no restart: this process
			// carries on serving, and it is the one that has to open the
			// gate again.
			release()
		}
		// Otherwise this is the Kubernetes path, where the restart has
		// no command because it has already begun: repointing the
		// Deployment started a rollout, and with strategy: Recreate the
		// next thing to happen to this process is being terminated. So
		// there is nothing to run and nothing to wait for -- and the
		// hold belongs to whatever comes up next, exactly as it does
		// after a successful RestartCmd below
		// (orchestrator.ReleaseUpgradeHold). The runs in run hosts were
		// flushed inside staging, before the repointing (stageKube), and
		// are the next pod's to adopt.
		return
	}

	// Best-effort, and its result is never observed: on a real
	// deployment this command replaces the process running this very
	// goroutine (systemctl restart on the unit this binary runs under),
	// so "it ran and this line never got to return" is the success case,
	// not a bug. It is still logged before it runs, because that log
	// line is the last thing this process writes about the upgrade and
	// so is where an operator reading the journal sees the handover: the
	// next lines in it come from the new build's own startup, or -- if
	// the restart failed -- do not come at all.
	//
	// A deadline of its own rather than whatever is left of the upgrade's:
	// everything this upgrade had to do is done by the time it gets here,
	// and one that spent the whole of Config.Timeout on a cold build would
	// otherwise have its restart killed the instant it started and be
	// reported as "staged but the restart failed" when nothing was ever
	// actually tried.
	//
	// The flush and the restart step's start come before succeed, not
	// after it: PhaseOK is what the upgrade check reads as "a new build
	// is up" (orchestrator.reconcileUpgradeCheck, Status.RestartPending),
	// and written any earlier it let this outgoing process file that
	// check itself while the flush was still waiting on in-flight work --
	// with this build's hash, and no restart gap or adoption to report
	// (task 1937).
	resume := u.flush(p)
	p.start(stepRestart)
	p.succeed(detail, true)
	if u.cfg.BeforeRestart != nil {
		u.cfg.BeforeRestart(branch)
	}
	rctx, rcancel := context.WithTimeout(context.Background(), restartTimeout)
	defer rcancel()
	rerr := runLogged(rctx, p, stepRestart, "", u.cfg.RestartCmd)
	if rerr == nil {
		p.done(stepRestart, "")
		// A restart that did what it was configured to do has already
		// killed this process -- the lines below never run, and the hold
		// is the next startup's to release. Still here, and not being
		// stopped, a while later means the command restarted something
		// that is not this deployment (or nothing at all), and then nobody
		// else is going to open the gate.
		//
		// A restart that only asks for a restart (a touch file, a path
		// unit) also comes back with this process still running, and it
		// is then stopped asynchronously and slowly. Config.Stopping is
		// how this process finds out, so it waits up to restartSettle for
		// that signal rather than only restartGrace.
		if u.awaitStop(p, restartSettle) {
			return
		}
		resume()
		release()
		return
	}
	// An error here is ambiguous exactly once: `systemctl restart` on
	// the unit this process runs under kills this process as part of
	// doing its job, and the systemctl child dies with it, so a
	// *successful* restart can hand back "signal: terminated" a moment
	// before this goroutine ceases to exist. Waiting restartGrace out
	// before recording anything settles it -- a restart that worked
	// takes this process with it well inside that window, and one that
	// is still here afterwards genuinely did not restart anything --
	// unless it is being stopped, which is the restart under way.
	if u.awaitStop(p, restartGrace) {
		return
	}
	resume()
	release()
	p.restartFailed(rerr)
}

// awaitStop waits after a restart command for evidence that the restart
// is taking this process with it. It reports true once Config.Stopping
// is closed, which means the queue hold and the flushed runs are the next
// startup's to release, so run must not resume them here. It reports
// false when wait runs out first. With no Stopping configured, the wait
// is restartGrace, which is how run worked before Stopping existed.
func (u *Upgrader) awaitStop(p *progress, wait time.Duration) bool {
	if u.cfg.Stopping == nil {
		time.Sleep(restartGrace)
		return false
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-u.cfg.Stopping:
		p.logf("this daemon is being stopped: the restart is under way, so the queue hold and the flushed runs are left for the next startup")
		return true
	case <-t.C:
		return false
	}
}

// flushTimeout bounds Config.Flush: the tails it stops are each one spool
// read and one cursor write away from letting go.
var flushTimeout = 30 * time.Second

// flush runs Config.Flush and hands back its resume, which is never nil
// for the same reason hold's release is not.
func (u *Upgrader) flush(p *progress) func() {
	if u.cfg.Flush == nil {
		return func() {}
	}
	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()
	resume, err := u.cfg.Flush(ctx)
	if err != nil {
		p.logf("could not flush every run's spool cursor before the restart: %v -- restarting anyway; "+
			"the daemon's own stop detaches what is left", err)
	}
	if resume == nil {
		return func() {}
	}
	return resume
}

// hold shuts this deployment's queue for the duration of an upgrade and
// hands back the func that opens it again -- see Config.Hold for what
// that is for and which paths release it.
//
// The returned func is never nil, so run can call it on every path that
// ends with this deployment still on the build it had without asking
// whether there was a hold to let go of: a deployment with no Hold
// configured, a hold that could not be taken, and a Hold that decided to
// take nothing all come back as "nothing to release".
func (u *Upgrader) hold(p *progress, branch string) func() {
	if u.cfg.Hold == nil {
		return func() {}
	}
	release, err := u.cfg.Hold(branch)
	if err != nil {
		// Logged into the upgrade's own progress log rather than
		// returned: an operator asked for an upgrade, not for a pause.
		p.logf("could not hold the queue for this upgrade: %v -- upgrading anyway", err)
		return func() {}
	}
	if release == nil {
		return func() {}
	}
	return release
}

// restartGrace is how long run waits, after a restart command comes
// back with an error, before believing it -- see run's own comment for
// what it disambiguates. A var only so a test need not sit through it.
var restartGrace = 3 * time.Second

// restartSettle is how long run waits, after a restart command that
// succeeded, for Config.Stopping to say that this process is being
// stopped. It applies only when Stopping is set. It is longer than
// restartGrace because a touch-file restart goes through a systemd path
// unit before the stop begins. It need not cover the stop itself, which
// can last as long as TimeoutStopSec: Stopping closes at the start of a
// stop, not at its end. A var for the same reason restartGrace is one.
var restartSettle = 30 * time.Second

// restartTimeout bounds the restart command, which is the one step of an
// upgrade that used to be bounded by nothing at all: it ran through
// exec.CommandContext and CombinedOutput rather than through runLogged,
// so cancelling its context killed only the direct child and left the
// read of a pipe some surviving grandchild still held open to block
// forever (waitDelay's own comment, measured again for this: a `sh -c
// "sleep 30 & wait"` under a 200ms deadline took the full 30 seconds to
// return). `sudo systemctl restart` has exactly that shape -- sudo forks
// systemctl -- so a restart that wedged rather than failing left run
// unable to return, the Upgrader never released, and the pane on
// "restarting" indefinitely: the same stuck deployment bwsalmon/agents#633
// fixed for checkout and build, reached by the one path that fix did not
// cover.
//
// Five minutes is well clear of what a restart can legitimately take --
// systemd's own default TimeoutStartSec is 90 seconds, and on a real
// deployment this command kills this process long before either bound
// matters -- while still being a bound. A var, like restartGrace and for
// the same reason: a test proving the bound exists cannot sit through it.
var restartTimeout = 5 * time.Minute

// plan is the list of steps this upgrade will run, in order, decided
// from the config before anything starts -- what lets the Upgrade pane
// say "step 2 of 5" from the first poll, and what run then walks. Steps
// this deployment has switched off are in it too, marked skipped as
// they are reached (progress.skip), so the pane shows a health check
// that was configured away rather than silently not showing one at all.
func (u *Upgrader) plan() []string {
	var steps []string
	switch u.Mode() {
	case ModeKube:
		// No pull step: the cluster pulls, and it does it as part of the
		// health check, which is the first moment anything asks a node
		// for this image. No restart step either -- the rollout the last
		// step starts is the restart (restarts). The flag check is the
		// image path's own, asked of a pod rather than of a container:
		// the args are in the Deployment spec and the image under them
		// moves with every rollout (kube.go's checkKubeFlags).
		steps = []string{stepVerify, stepHealthCheck, stepFlags, stepSandbox, stepPriorityClasses, stepSchema, stepRollout}
	case ModeImage:
		steps = []string{stepPull, stepVerify, stepHealthCheck, stepFlags, stepSandbox, stepSchema, stepRecord}
	default:
		steps = []string{stepCheckout, stepBuild, stepInstall, stepHealthCheck, stepSchema}
	}
	if len(u.cfg.RestartCmd) > 0 {
		steps = append(steps, stepRestart)
	}
	return steps
}

// description is the one-line "what this upgrade is about to do" that
// opens its log: which of the two pipelines it runs, and against what.
func (u *Upgrader) description() string {
	switch u.Mode() {
	case ModeKube:
		return fmt.Sprintf("checking an image from %s and repointing deployment/%s at it",
			u.cfg.Kube.Repository, u.cfg.Kube.Deployment)
	case ModeImage:
		return fmt.Sprintf("pulling from %s and pointing %s at it", u.EffectiveRepository(), u.cfg.Image.RefFile)
	}
	return fmt.Sprintf("building in %s and installing to %s", u.cfg.SrcDir, u.cfg.InstallPath)
}

// stage does everything an upgrade does short of announcing success and
// restarting: whichever of the two pipelines this deployment is
// configured for, run to completion. What it returns is the Detail line
// Status carries afterwards -- "built and installed", or the image now
// recorded -- since the two paths leave behind genuinely different
// things and a status saying "built" on a host that never built
// anything would be a lie an operator has to decode.
func (u *Upgrader) stage(ctx context.Context, p *progress, branch string) (string, error) {
	switch u.Mode() {
	case ModeKube:
		return u.stageKube(ctx, p, branch)
	case ModeImage:
		return u.stageImage(ctx, p, branch)
	}
	return u.stageBinary(ctx, p, branch)
}

// stageImage is the container path (image.go): pull, health-check, and
// only then point the deployment's ref file at what was pulled.
func (u *Upgrader) stageImage(ctx context.Context, p *progress, branch string) (string, error) {
	prevRef, _ := u.readImageRef()
	var prevSandbox string
	if prevRef != "" && len(u.cfg.Image.SandboxImageArgs) > 0 {
		// Best effort, and deliberately not fatal the way the same
		// question about the *new* image is: this one only decides which
		// old image cleanOldImages may keep, and the image it asks may
		// already have been removed from this host. Failing to name it
		// costs a stale guest image left on disk, not a deployment
		// reaching for one nothing pulled.
		prevSandbox, _, _ = u.sandboxImage(ctx, p, prevRef)
	}

	p.start(stepPull)
	override := u.overrideRef()
	tag := override
	if tag == "" {
		var err error
		if tag, err = u.imageRef(branch); err != nil {
			return "", err
		}
	}
	if err := u.pullImage(ctx, p, stepPull, tag); err != nil {
		if hint := registryHint(tag, err); hint != "" {
			return "", fmt.Errorf("pull: %w -- %s", err, hint)
		}
		return "", fmt.Errorf("pull: %w", err)
	}
	// From here on, the image the pull fetched and not the tag it was
	// fetched by: everything below checks ref, and ref is what the
	// restart runs, so the tag moving in between cannot slip an
	// unchecked image past them (pinImage).
	ref, err := u.pinImage(ctx, p, stepPull, tag)
	if err != nil {
		return "", fmt.Errorf("pull: %w", err)
	}
	p.done(stepPull, "pulled "+ref)
	// An image named outright is compared by digest alone: the branch it
	// is recorded under need not say which build it is.
	same := u.sameImageVersion(prevRef, branch, tag, ref)
	if override != "" {
		prevDigest, okPrev := digestRef(prevRef)
		newDigest, okNew := digestRef(ref)
		same = okPrev && okNew && prevDigest == newDigest
	} else if !same && u.discardedRCUnchangedDetail(ctx, branch) != "" {
		same = true
	}
	if same {
		if msg := u.discardedRCUnchangedDetail(ctx, branch); msg != "" && override == "" {
			return p.markUnchanged(msg), nil
		}
		return p.markUnchanged(fmt.Sprintf("the version on %s has not changed (already running %s)", branch, ref)), nil
	}

	// Who published it, before the health check below runs it: the pin
	// says which bytes, and this says whose (verify.go).
	if len(u.cfg.Image.VerifyCmd) > 0 {
		p.start(stepVerify)
		if err := verifyPinned(ctx, p, stepVerify, u.cfg.Image.VerifyCmd, ref); err != nil {
			return "", fmt.Errorf("%w; leaving this deployment on the image it already runs", err)
		}
		p.done(stepVerify, ref+" is signed")
	} else {
		p.logf("%s: warning: %s", stepVerify, unverifiedNote)
		p.skip(stepVerify, unverifiedNote)
	}

	if len(u.cfg.HealthCheckArgs) > 0 {
		p.start(stepHealthCheck)
		if err := u.healthCheckImage(ctx, p, ref); err != nil {
			// Nothing to roll back: the ref file below is still
			// untouched, so this deployment goes on running exactly the
			// image it was already running.
			return "", fmt.Errorf("health check failed on the pulled image, leaving this deployment on the image it already runs: %w", err)
		}
		p.done(stepHealthCheck, "the pulled image runs")
	} else {
		p.skip(stepHealthCheck, "no health check configured")
	}

	detail := "pulled " + ref

	// Whether the image that just proved it runs will accept the command
	// line this host's unit starts it with. Never fatal -- see
	// checkImageFlags -- so nothing here returns an error: what it can do
	// is put the flag names in front of an operator while they are still
	// watching the upgrade they asked for, rather than leaving them in a
	// journal nobody reads until something is already down.
	if len(u.cfg.Image.FlagNamesArgs) > 0 && len(u.cfg.Image.UnitFlags) > 0 {
		p.start(stepFlags)
		missing, err := u.checkImageFlags(ctx, p, ref)
		if note := reportFlags(p, ref, u.cfg.Image.UnitFlags, imageFlagFix, missing, err); note != "" {
			detail += "; " + note
		}
	} else {
		p.skip(stepFlags, "this deployment's flags are not written down on the host it runs on")
	}

	// The sandbox container that goes with it, before the cut-over
	// rather than after, for the same reason the health check comes
	// first: everything this deployment will need has to be on the host
	// already by the time the ref file names the new image.
	var sandbox string
	if len(u.cfg.Image.SandboxImageArgs) > 0 {
		p.start(stepSandbox)
		var why string
		var err error
		sandbox, why, err = u.sandboxImage(ctx, p, ref)
		if err != nil {
			// Not "this build runs no sandbox" -- this host could not put
			// the question at all (sandboxImage). Cutting over on that
			// would point the deployment at a build whose guest image
			// nothing has pulled, and nothing needs undoing to stop: the
			// ref file is still untouched, as it is for a failed health
			// check.
			return "", fmt.Errorf("asking %s which sandbox image it expects, so nothing was pulled to keep in step and this deployment stays on the image it already runs: %w", ref, err)
		}
		if sandbox == "" {
			// Why it names none, not just that it names none: the step is
			// where the difference between a rollback to a build older
			// than the question and a deployment that has quietly stopped
			// keeping its guest in step is visible.
			p.done(stepSandbox, ref+" names no sandbox image -- "+why)
		} else {
			if err := u.pullImage(ctx, p, stepSandbox, sandbox); err != nil {
				return "", fmt.Errorf("pulling the sandbox image %s that %s expects: %w", sandbox, ref, err)
			}
			// Signed by the same key as the grain image, and checked
			// before the pull counts. What was pulled is what is
			// checked, by its digest. A release build names its sandbox
			// by digest already (scripts/release-images.sh), so that
			// digest is both the one checked here and the one every VM
			// is later created from; a build that names a bare tag is
			// checked by whatever the tag pulled, and its VMs follow
			// the tag (docs/image-signing.md).
			if len(u.cfg.Image.VerifyCmd) > 0 {
				pinned, err := u.pinImage(ctx, p, stepSandbox, sandbox)
				if err == nil {
					err = verifyPinned(ctx, p, stepSandbox, u.cfg.Image.VerifyCmd, pinned)
				}
				if err != nil {
					return "", fmt.Errorf("the sandbox image %s that %s expects, so this deployment stays on the image it already runs: %w", sandbox, ref, err)
				}
			}
			mirroredSandbox, err := u.mirrorSandboxImage(ctx, p, stepSandbox, sandbox)
			if err != nil {
				return "", fmt.Errorf("mirroring the sandbox image %s that %s expects, so this deployment stays on the image it already runs: %w", sandbox, ref, err)
			}
			detail += " and its sandbox image " + sandbox
			mirrorNote := ""
			if mirroredSandbox != "" {
				mirrorNote = " (mirrored to " + mirroredSandbox + ")"
				detail += mirrorNote
			}
			// A pin that is not what this build names means the pull
			// above bought nothing: the daemon creates every VM from the
			// pinned image, so the guest stays where it is while grain
			// moves. Said on the step and again in the line the pane
			// keeps afterwards, since "pulled X and its sandbox image Y"
			// is otherwise read -- correctly, but wrongly -- as this
			// deployment now running Y.
			if unused := u.unusedSandboxNote(sandbox); unused != "" {
				detail += unused
				p.done(stepSandbox, "pulled "+sandbox+mirrorNote+unused)
			} else {
				p.done(stepSandbox, "pulled "+sandbox+mirrorNote)
			}
		}
	} else {
		p.skip(stepSandbox, "this deployment runs no sandbox containers")
	}

	// Last before the cut-over, and deliberately: if this deployment's
	// rows have to be written out for the new build to come back on
	// them, the shorter the gap between reading them and restarting the
	// process still writing them, the less of it is lost (schema.go, and
	// pkg/cli/schemaupgrade.go on what that gap costs).
	migrating, err := u.schemaStep(ctx, p, branch, imageRunner{u: u, ref: ref})
	if err != nil {
		return "", err
	}
	detail += migrating

	p.start(stepRecord)
	if err := u.writeImageRef(ref); err != nil {
		u.discardSchemaUpgrade()
		return "", fmt.Errorf("recording %s: %w", u.cfg.Image.RefFile, err)
	}
	u.cleanOldImages(ctx, p, ref, prevRef, sandbox, prevSandbox)
	p.done(stepRecord, u.cfg.Image.RefFile+" now names "+ref)
	return detail, nil
}

// stageBinary is the original path: fetch the branch into a checkout on
// disk, build it, and install the result over the running binary.
func (u *Upgrader) stageBinary(ctx context.Context, p *progress, branch string) (string, error) {
	p.start(stepCheckout)
	if err := u.checkout(ctx, p, branch); err != nil {
		return "", fmt.Errorf("checkout: %w", err)
	}
	head, err := u.headCommit(ctx)
	if err != nil {
		// Purely descriptive: an upgrade that could not name the commit
		// it just checked out is still an upgrade.
		p.logf("%s: reading the commit just checked out: %v", stepCheckout, err)
	}
	p.done(stepCheckout, strings.TrimSpace(branch+" at "+head))
	if same, ok := u.sameBinaryVersion(ctx, branch, head); ok {
		return p.markUnchanged(fmt.Sprintf("the version on %s has not changed (already running %s)", branch, same)), nil
	}

	p.start(stepBuild)
	if err := u.build(ctx, p); err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	p.done(stepBuild, u.cfg.BuiltBinary)

	p.start(stepInstall)
	hadPrevious, err := u.backupInstalled()
	if err != nil {
		return "", fmt.Errorf("backing up previous binary: %w", err)
	}
	if err := u.install(); err != nil {
		// install writes a temp file and renames it, so a failure here
		// leaves the binary that was already at InstallPath untouched --
		// which makes the backup just taken a stale copy of it, and a
		// large one, sitting beside it until some later upgrade happens
		// to overwrite it.
		u.removeBackup()
		return "", fmt.Errorf("install: %w", err)
	}
	p.done(stepInstall, "installed to "+u.cfg.InstallPath)

	if len(u.cfg.HealthCheckArgs) > 0 {
		p.start(stepHealthCheck)
		if err := u.healthCheck(ctx, p); err != nil {
			if rerr := u.rollbackInstalled(hadPrevious); rerr != nil {
				return "", fmt.Errorf("health check failed (%w) and rollback also failed: %v", err, rerr)
			}
			if hadPrevious {
				return "", fmt.Errorf("health check failed on newly installed binary, rolled back to the previous one: %w", err)
			}
			return "", fmt.Errorf("health check failed on newly installed binary, removed it (no previous binary to roll back to): %w", err)
		}
		p.done(stepHealthCheck, "the installed binary runs")
	} else {
		p.skip(stepHealthCheck, "no health check configured")
	}

	// The same question the image path asks, one step later because on
	// this path the new build only exists once it is installed. The
	// rollback is the same one a failed health check gets: the binary
	// that was here comes back, and the restart that follows brings up
	// the build whose store this still is.
	migrating, err := u.schemaStep(ctx, p, branch, binaryRunner{u: u})
	if err != nil {
		if rerr := u.rollbackInstalled(hadPrevious); rerr != nil {
			return "", fmt.Errorf("%w -- and rolling the install back also failed: %v", err, rerr)
		}
		if hadPrevious {
			return "", fmt.Errorf("%w (rolled back to the binary that was here)", err)
		}
		return "", fmt.Errorf("%w (removed it -- there was no previous binary to roll back to)", err)
	}
	u.removeBackup()
	return "built and installed" + migrating, nil
}

// discardSchemaUpgrade calls the Discard hook, if this deployment has
// one, for the paths that fail after the schema step has already written
// this deployment's rows out: what it undoes is an instruction to the
// next start, and an instruction left behind an upgrade that did not
// happen is one some unrelated later restart would carry out.
func (u *Upgrader) discardSchemaUpgrade() {
	if u.cfg.Schema != nil && u.cfg.Schema.Discard != nil {
		u.cfg.Schema.Discard()
	}
}

// headCommit is what the checkout now points at, for the log and for
// the checkout step's own line in the pane: "grain/task-37 at 7b137f0
// (Merge pull request #802…)" says more about which upgrade this is
// than the branch name alone, which is the same on every upgrade onto
// the same branch.
func (u *Upgrader) headCommit(ctx context.Context) (string, error) {
	cmd := newCommand(ctx, "", "git", "-C", u.cfg.SrcDir, "log", "-1", "--format=%h (%s)")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (u *Upgrader) headRevision(ctx context.Context) (string, error) {
	cmd := newCommand(ctx, "", "git", "-C", u.cfg.SrcDir, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// sameBinaryVersion reports whether the checkout on branch is already the
// build installed at InstallPath (or named by Config.RunningRelease).
func (u *Upgrader) sameBinaryVersion(ctx context.Context, branch, head string) (string, errorBool) {
	running, err := u.Running()
	if err == nil && !running.Build.Modified {
		if running.Build.Revision != "" {
			if rev, revErr := u.headRevision(ctx); revErr == nil && rev != "" && rev == running.Build.Revision {
				if head != "" {
					return head, true
				}
				return rev, true
			}
		}
		if running.Build.Release != "" && branch == running.Build.Release {
			return running.Build.Release, true
		}
	}
	if rel := u.runningRelease(); rel != "" && branch == rel {
		return rel, true
	}
	return "", false
}

type errorBool = bool

func sourceBranch(branch string) string {
	if branch == "rc" {
		return "candidate"
	}
	return branch
}

// checkout fetches branch from origin and hard-resets the checkout onto
// it -- mirroring scripts/setup.sh's own sync_repo, including its
// refusal to run against a dirty tree (this checkout is meant to be
// managed entirely by this pipeline; local changes in it are either a
// mistake or another operator's in-progress work, and either way this is
// not the thing that should decide to discard them).
func (u *Upgrader) checkout(ctx context.Context, p *progress, branch string) error {
	dirty, err := u.dirty(ctx)
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("%s has uncommitted changes -- refusing to overwrite them", u.cfg.SrcDir)
	}
	remoteBranch := sourceBranch(branch)
	if err := u.git(ctx, p, "fetch", "--quiet", "--end-of-options", "origin", remoteBranch); err != nil {
		return err
	}
	if err := u.git(ctx, p, "switch", "--quiet", "--end-of-options", remoteBranch); err != nil {
		return err
	}
	return u.git(ctx, p, "reset", "--quiet", "--hard", "origin/"+remoteBranch)
}

func (u *Upgrader) dirty(ctx context.Context) (bool, error) {
	cmd := newCommand(ctx, "", "git", "-C", u.cfg.SrcDir, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

func (u *Upgrader) git(ctx context.Context, p *progress, args ...string) error {
	argv := append([]string{"git", "-C", u.cfg.SrcDir}, args...)
	return runLogged(ctx, p, stepCheckout, "", argv)
}

func (u *Upgrader) build(ctx context.Context, p *progress) error {
	if len(u.cfg.BuildCmd) == 0 {
		return errors.New("no build command configured")
	}
	return runLogged(ctx, p, stepBuild, u.cfg.SrcDir, u.cfg.BuildCmd)
}

// copyExecutable copies src to dst, atomically: a temp file alongside
// dst, chmod'd executable, then renamed over it -- so nothing ever
// reads (or execs) a half-written binary. The bytes stream through
// rather than being read into memory first because both callers are
// copying a grain binary -- tens of megabytes, more once a release
// build's embedded UI assets are in it -- inside the daemon process, on
// a host that is by definition mid-upgrade.
func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	// The mode handed to OpenFile is masked by the process umask, and an
	// existing temp file left by an earlier interrupted upgrade keeps
	// its own mode regardless, so set it explicitly: what the rename
	// below publishes has to be executable.
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// install copies BuiltBinary to InstallPath, atomically -- see
// copyExecutable.
func (u *Upgrader) install() error {
	return copyExecutable(u.cfg.BuiltBinary, u.cfg.InstallPath)
}

// backupPath is where backupInstalled keeps whatever was at InstallPath
// before this upgrade's own install overwrites it, so rollbackInstalled
// has something to restore if the newly installed binary fails its
// health check.
func (u *Upgrader) backupPath() string {
	return u.cfg.InstallPath + ".prev"
}

// backupInstalled copies whatever is currently at InstallPath (an
// earlier upgrade's binary, or the one setup.sh first installed) aside
// before install overwrites it. hadPrevious is false, with no error,
// when there is nothing there yet -- the very first install onto a
// fresh deployment -- which rollbackInstalled uses to know it has
// nothing to restore.
func (u *Upgrader) backupInstalled() (hadPrevious bool, err error) {
	// The only thing missing here can be InstallPath itself: the backup
	// copyExecutable writes goes next to it, in a directory that has to
	// exist for InstallPath to have been openable at all.
	if err := copyExecutable(u.cfg.InstallPath, u.backupPath()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// rollbackInstalled undoes install: restores the binary backupInstalled
// set aside, or -- if there was none, meaning this was the first ever
// install -- removes the broken one instead of leaving it in place for
// a future restart to pick up.
func (u *Upgrader) rollbackInstalled(hadPrevious bool) error {
	if !hadPrevious {
		if err := os.Remove(u.cfg.InstallPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return os.Rename(u.backupPath(), u.cfg.InstallPath)
}

// removeBackup discards the backup taken before a health check that
// ended up passing (or was skipped entirely) -- there is nothing left
// to ever roll back to once the newly installed binary is confirmed
// good, and leaving it around would just be a stale file next to
// InstallPath forever.
func (u *Upgrader) removeBackup() {
	_ = os.Remove(u.backupPath())
}

// healthCheck runs the binary install just wrote to disk -- InstallPath
// itself, not BuiltBinary -- with Config.HealthCheckArgs, on the theory
// that a binary broken at runtime (a panic in some init(), a missing
// dynamic library, a build that produced something that plain doesn't
// run) fails this the same way it would fail for real, in well under
// the 10 seconds allotted here; a subcommand like "schema-version" that
// touches no store and needs no config is expected to return almost
// instantly, so the timeout exists only to bound a binary that
// unexpectedly hangs rather than exiting, not to allow for any real
// work.
func (u *Upgrader) healthCheck(ctx context.Context, p *progress) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	argv := append([]string{u.cfg.InstallPath}, u.cfg.HealthCheckArgs...)
	return runLogged(ctx, p, stepHealthCheck, "", argv)
}

// binaryRunner is schema.go's schemaRunner for the source path: the
// binary this upgrade has just installed, run in place. It needs no
// mount and ignores dir for that reason -- it is already looking at the
// same filesystem the dump was written to.
type binaryRunner struct{ u *Upgrader }

func (r binaryRunner) ask(ctx context.Context, p *progress, args []string) (string, error) {
	return runAsked(ctx, p, stepSchema, 10*time.Second, append([]string{r.u.cfg.InstallPath}, args...))
}

func (r binaryRunner) rehearse(ctx context.Context, p *progress, dir string, args []string) error {
	ctx, cancel := context.WithTimeout(ctx, schemaCheckTimeout)
	defer cancel()
	return runLogged(ctx, p, stepSchema, "", append([]string{r.u.cfg.InstallPath}, args...))
}

// ReadStatus reads a StatusFile back as it stands, for a reader that is
// not an Upgrader -- the daemon's restart-loop count, which asks whether
// the start it is counting followed an upgrade's own restart. Unlike
// Upgrader.Status it never rewrites the file: a missing file is a zero
// Status and nil.
func ReadStatus(path string) (Status, error) {
	return readStatus(path)
}

// RestartRequestedAt is when this upgrade ran its restart command, if it
// did: a successful upgrade (PhaseOK) whose restart step started and was
// not recorded as failing. That is a restart the upgrade asked for on
// purpose, and the start that follows it is not a crash. A status
// written before steps existed falls back to FinishedAt where Detail
// says it was restarting.
func (s Status) RestartRequestedAt() (time.Time, bool) {
	if s.Phase != PhaseOK || s.Unchanged {
		return time.Time{}, false
	}
	for _, step := range s.Steps {
		if step.Name != stepRestart {
			continue
		}
		if step.StartedAt == nil || step.State == StepFailed {
			return time.Time{}, false
		}
		return *step.StartedAt, true
	}
	if s.FinishedAt != nil && strings.HasSuffix(s.Detail, "; restarting") {
		return *s.FinishedAt, true
	}
	return time.Time{}, false
}

// RestartPending reports whether the successful upgrade s records is
// still waiting, as seen from a process that started at started, for the
// restart that brings its build up: true for the outgoing process, which
// is still running the old build, and false for the one that restart
// started. An upgrade that does not restart anything is never pending.
//
// A restart step still pending in a successful upgrade (a status written
// by a build that recorded success before flushing) is pending until a
// process started after the upgrade finished; a started of zero, which
// cannot tell, treats it as pending and everything else as not.
func (s Status) RestartPending(started time.Time) bool {
	if s.Phase != PhaseOK || s.Unchanged {
		return false
	}
	at, ok := s.RestartRequestedAt()
	if !ok {
		for _, step := range s.Steps {
			if step.Name == stepRestart && step.State == StepPending && s.FinishedAt != nil {
				at, ok = *s.FinishedAt, true
				if started.IsZero() {
					return true
				}
			}
		}
	}
	if !ok || started.IsZero() {
		return false
	}
	return !started.After(at)
}

func readStatus(path string) (Status, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Status{}, nil
	}
	if err != nil {
		return Status{}, err
	}
	var status Status
	if err := json.Unmarshal(data, &status); err != nil {
		return Status{}, err
	}
	return status, nil
}

// writeStatus persists status atomically, the same reason install
// copies a binary that way: a concurrent Status read must never see a
// half-written file.
func writeStatus(path string, status Status) error {
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
